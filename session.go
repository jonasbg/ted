package main

import (
	"bufio"
	"cmp"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// ---- hot exit: tabs and unsaved edits survive closing the window ----

type sessionTab struct {
	Path    string  `json:"path"`
	Line    int     `json:"line"`
	Col     int     `json:"col"`
	Vy      float32 `json:"vy"`
	Unsaved *string `json:"unsaved,omitempty"`
	CRLF    bool    `json:"crlf,omitempty"`
	Preview bool    `json:"preview,omitempty"`
	Md      bool    `json:"md_preview,omitempty"`
	MdVy    float32 `json:"md_vy,omitempty"`
	DiskMod int64   `json:"disk_mod,omitempty"`
}

type sessionFile struct {
	Root    string       `json:"root"`
	Active  int          `json:"active"`
	Sidebar bool         `json:"sidebar"`
	Width   float32      `json:"sidebar_width"`
	Font    float32      `json:"font"`
	Dotfile bool         `json:"dotfiles,omitempty"`
	NoWeb   bool         `json:"no_web,omitempty"`
	Tabs    []sessionTab `json:"tabs"`
}

func hashString(s string) string {
	h := sha1.Sum([]byte(s))
	return hex.EncodeToString(h[:])[:16]
}

func cacheDir() string {
	d, err := os.UserCacheDir()
	if err != nil {
		d = os.TempDir()
	}
	return filepath.Join(d, "ted")
}

func sessionPath(root string) string {
	return filepath.Join(cacheDir(), "sessions", hashString(root)+".json")
}

func (a *App) sessionSignature() string {
	var sb strings.Builder
	showHidden := a.tree != nil && a.tree.showHidden
	fmt.Fprintf(&sb, "%d|%v|%v|%v|%v|", a.active, a.showSidebar, a.fontSp, showHidden, a.noWeb)
	for _, b := range a.bufs {
		fmt.Fprintf(&sb, "%s:%d:%v:%d:%v;", b.path, b.cur.L, b.dirty(), b.version, b.previewing())
	}
	return sb.String()
}

func (a *App) saveSessionIfChanged() {
	if a.noSession {
		return
	}
	if sig := a.sessionSignature(); sig != a.sessionSig {
		a.saveSession()
	}
}

// pendingTab carries immutable line slices so text serialization can run off
// the UI goroutine during periodic autosaves.
type pendingTab struct {
	idx   int
	lines [][]rune
	b64   []b64Span
}

// autosaveSession is saveSessionIfChanged for the periodic tick: only lightweight
// slice headers are cloned on the UI goroutine; text serialization, JSON encoding
// and disk I/O run off the UI goroutine.
func (a *App) autosaveSession() {
	if a.noSession {
		return
	}
	if sig := a.sessionSignature(); sig != a.sessionSig {
		seq, s, pending := a.sessionSnapshotDeferred()
		go func() {
			for _, pt := range pending {
				txt := textEncodedLines(pt.lines, pt.b64)
				s.Tabs[pt.idx].Unsaved = &txt
			}
			a.writeSession(seq, s)
		}()
	}
}

// saveSession writes the session now; it waits for any background write.
func (a *App) saveSession() {
	if a.noSession {
		return
	}
	a.writeSession(a.sessionSnapshot())
}

// sessionSnapshot captures the session on the UI goroutine. seq orders
// snapshots so an older background write never replaces a newer file.
func (a *App) sessionSnapshot() (uint64, sessionFile) {
	seq, s, pending := a.sessionSnapshotDeferred()
	for _, pt := range pending {
		txt := textEncodedLines(pt.lines, pt.b64)
		s.Tabs[pt.idx].Unsaved = &txt
	}
	return seq, s
}

func (a *App) sessionSnapshotDeferred() (uint64, sessionFile, []pendingTab) {
	a.sessionSig = a.sessionSignature()
	a.sessionSeq++
	showHidden := a.tree != nil && a.tree.showHidden
	s := sessionFile{Root: a.root, Active: a.active, Sidebar: a.showSidebar, Width: a.sidebarDp, Font: a.fontSp, Dotfile: showHidden, NoWeb: a.noWeb}
	var pending []pendingTab
	for _, b := range a.bufs {
		t := sessionTab{Path: b.path, Line: b.cur.L + 1, Col: b.cur.C + 1, Vy: b.vy, CRLF: b.crlf, Preview: b.preview, Md: b.previewing()}
		if t.Md {
			t.MdVy = b.md.vy
		}
		if b.dirty() {
			if !b.modTime.IsZero() {
				t.DiskMod = b.modTime.UnixNano()
			}
			pending = append(pending, pendingTab{idx: len(s.Tabs), lines: slices.Clone(b.lines), b64: slices.Clone(b.b64)})
		} else if b.path == "" {
			continue
		}
		s.Tabs = append(s.Tabs, t)
	}
	return a.sessionSeq, s, pending
}

// textEncodedLines serializes lines with any decoded base64 spans re-encoded,
// safely usable on a background goroutine because line slices are immutable.
func textEncodedLines(lines [][]rune, spans []b64Span) string {
	if len(spans) == 0 {
		return textOf(lines)
	}
	offset := func(p Pos) int {
		off := 0
		for i := 0; i < p.L && i < len(lines); i++ {
			off += len(lines[i]) + 1
		}
		return off + p.C
	}
	spans = slices.Clone(spans)
	slices.SortFunc(spans, func(x, y b64Span) int { return cmp.Compare(offset(y.a), offset(x.a)) })
	t := []rune(textOf(lines))
	for _, sp := range spans {
		ao, co := offset(sp.a), offset(sp.c)
		if ao < 0 || co > len(t) || ao > co {
			continue
		}
		out, _ := sp.encoded(string(t[ao:co]))
		t = slices.Replace(t, ao, co, []rune(out)...)
	}
	return string(t)
}

func (a *App) writeSession(seq uint64, s sessionFile) {
	a.sessionMu.Lock()
	defer a.sessionMu.Unlock()
	if seq <= a.sessionDone {
		return // a newer snapshot is already on disk
	}
	a.sessionDone = seq
	data, err := json.Marshal(s)
	if err != nil {
		return
	}
	p := sessionPath(s.Root)
	dir := filepath.Dir(p)
	os.MkdirAll(dir, 0o700)
	f, err := os.CreateTemp(dir, ".session-*.tmp")
	if err != nil {
		return
	}
	tmp := f.Name()
	_, werr := f.Write(data)
	cerr := f.Close()
	if werr != nil || cerr != nil {
		os.Remove(tmp)
		return
	}
	if os.Rename(tmp, p) == nil {
		os.WriteFile(lastRootFile(), []byte(s.Root+"\n"), 0o600)
	} else {
		os.Remove(tmp)
	}
}

func (a *App) restoreSession(root string, unsavedOnly bool) {
	data, err := os.ReadFile(sessionPath(root))
	if err != nil {
		return
	}
	var s sessionFile
	if json.Unmarshal(data, &s) != nil {
		return
	}
	if s.Width > 0 {
		a.sidebarDp = s.Width
	}
	if s.Font > 0 {
		a.fontSp = s.Font
	}
	a.noWeb = s.NoWeb
	if s.Dotfile && !a.tree.showHidden {
		a.setDotfiles(true)
	}
	a.showSidebar = a.root != "" && (s.Sidebar || len(s.Tabs) == 0)
	restored := 0
	active := -1
	for i, t := range s.Tabs {
		if unsavedOnly && t.Unsaved == nil {
			continue
		}
		var b *Buffer
		if t.Path == "" {
			if t.Unsaved == nil {
				continue
			}
			a.newUntitled()
			b = a.buf()
		} else {
			if _, err := os.Stat(t.Path); err != nil && t.Unsaved == nil {
				continue // file is gone and we had nothing unsaved
			}
			if !a.open(t.Path, 0, 0, t.Preview) {
				continue
			}
			b = a.buf()
		}
		if t.Unsaved != nil {
			if *t.Unsaved != b.text() {
				last := len(b.lines) - 1
				b.replaceRange(Pos{0, 0}, Pos{last, len(b.lines[last])}, []rune(*t.Unsaved))
				b.breakUndo()
				if t.DiskMod != 0 && !b.modTime.IsZero() && b.modTime.UnixNano() != t.DiskMod {
					b.conflict = true
				}
				restored++
			}
			if t.CRLF != b.crlf {
				b.crlf = t.CRLF
				b.metaDirty = true
			}
		}
		if i == s.Active {
			active = a.active
		}
		b.cur = b.clamp(Pos{t.Line - 1, t.Col - 1})
		b.selecting = false
		b.vy = t.Vy
		if t.Md {
			if b.path == "" {
				// an untitled buffer is Markdown only by choice; that choice
				// is what put it in preview, so restore it
				b.setLanguage("markdown")
			}
			if isMarkdown(b) {
				b.md = &mdState{on: true, hoverLink: -1, sync: t.Line - 1, vy: t.MdVy}
				if t.MdVy > 0 {
					b.md.sync = -1 // back where the preview was scrolled to
				}
			}
		}
	}
	if active >= 0 {
		a.active = active
	}
	a.msg = ""
	if restored > 0 {
		a.setMsg("Restored unsaved changes in %d file(s)", restored)
	}
	a.follow = false
	a.sessionSig = a.sessionSignature()
}

// ---- single instance: `ted file` opens in the window already showing its project ----

func socketPath(root string) string {
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("ted-%d", os.Getuid()))
	os.MkdirAll(dir, 0o700)
	return filepath.Join(dir, hashString(root)+".sock")
}

type ipcTarget struct {
	Path string `json:"path"`
	Line int    `json:"line"`
	Col  int    `json:"col"`
}

// sendToRunning hands the targets to a running window for root, if any.
// When root is empty and files are given, it first checks whether a running
// project window owns any ancestor directory of the first file.
func sendToRunning(root string, targets []target) bool {
	if root == "" && len(targets) > 0 {
		dir := filepath.Dir(targets[0].path)
		if r, err := filepath.EvalSymlinks(dir); err == nil {
			dir = r
		}
		for {
			if sendToSocket(socketPath(dir), targets) {
				return true
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return sendToSocket(socketPath(root), targets)
}

func sendToSocket(sock string, targets []target) bool {
	c, err := net.DialTimeout("unix", sock, 300*time.Millisecond)
	if err != nil {
		return false
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(2 * time.Second))
	var ts []ipcTarget
	for _, t := range targets {
		ts = append(ts, ipcTarget{t.path, t.line, t.col})
	}
	if err := json.NewEncoder(c).Encode(ts); err != nil {
		return false
	}
	reply, err := bufio.NewReader(c).ReadString('\n')
	return err == nil && strings.TrimSpace(reply) == "ok"
}

func (a *App) listen() {
	p := socketPath(a.root)
	os.Remove(p) // stale socket from a crashed instance
	l, err := net.Listen("unix", p)
	if err != nil {
		return
	}
	a.mu.Lock()
	select {
	case <-a.done:
		a.mu.Unlock()
		l.Close()
		os.Remove(p)
		return
	default:
		a.socket = p
		a.listener = l
		a.mu.Unlock()
	}
	for {
		c, err := l.Accept()
		if err != nil {
			return
		}
		go func(c net.Conn) {
			defer c.Close()
			c.SetDeadline(time.Now().Add(2 * time.Second))
			var ts []ipcTarget
			if err := json.NewDecoder(c).Decode(&ts); err != nil {
				return
			}
			var out []target
			for _, t := range ts {
				out = append(out, target{t.Path, t.Line, t.Col})
			}
			a.post(openEvent{out})
			c.Write([]byte("ok\n"))
		}(c)
	}
}
