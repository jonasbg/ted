package main

import (
	"bufio"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
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
	Preview bool    `json:"preview,omitempty"`
	DiskMod int64   `json:"disk_mod,omitempty"`
}

type sessionFile struct {
	Root    string       `json:"root"`
	Active  int          `json:"active"`
	Sidebar bool         `json:"sidebar"`
	Width   float32      `json:"sidebar_width"`
	Font    float32      `json:"font"`
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
	fmt.Fprintf(&sb, "%d|%v|%v|", a.active, a.showSidebar, a.fontSp)
	for _, b := range a.bufs {
		fmt.Fprintf(&sb, "%s:%d:%v:%d;", b.path, b.cur.L, b.dirty(), b.version)
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

// autosaveSession is saveSessionIfChanged for the periodic tick: the
// snapshot is taken here, but encoding and writing it (tens of ms for a
// large unsaved file) happen off the UI goroutine.
func (a *App) autosaveSession() {
	if a.noSession {
		return
	}
	if sig := a.sessionSignature(); sig != a.sessionSig {
		seq, s := a.sessionSnapshot()
		go a.writeSession(seq, s)
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
	a.sessionSig = a.sessionSignature()
	a.sessionSeq++
	s := sessionFile{Root: a.root, Active: a.active, Sidebar: a.showSidebar, Width: a.sidebarDp, Font: a.fontSp}
	for _, b := range a.bufs {
		t := sessionTab{Path: b.path, Line: b.cur.L + 1, Col: b.cur.C + 1, Vy: b.vy, Preview: b.preview}
		if b.dirty() {
			txt := b.text()
			t.Unsaved = &txt
			if !b.modTime.IsZero() {
				t.DiskMod = b.modTime.UnixNano()
			}
		} else if b.path == "" {
			continue
		}
		s.Tabs = append(s.Tabs, t)
	}
	return a.sessionSeq, s
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
	os.MkdirAll(filepath.Dir(p), 0o700)
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err == nil {
		if os.Rename(tmp, p) == nil {
			os.WriteFile(lastRootFile(), []byte(s.Root+"\n"), 0o600)
		}
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
		if t.Unsaved != nil && *t.Unsaved != b.text() {
			b.replaceRange(Pos{0, 0}, b.posFromOffset(1<<62), []rune(*t.Unsaved))
			b.breakUndo()
			if t.DiskMod != 0 && !b.modTime.IsZero() && b.modTime.UnixNano() != t.DiskMod {
				b.conflict = true
			}
			restored++
		}
		if i == s.Active {
			active = a.active
		}
		b.cur = b.clamp(Pos{t.Line - 1, t.Col - 1})
		b.selecting = false
		b.vy = t.Vy
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
func sendToRunning(root string, targets []target) bool {
	c, err := net.DialTimeout("unix", socketPath(root), 300*time.Millisecond)
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
	a.socket = p
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
