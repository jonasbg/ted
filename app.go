package main

import (
	"context"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
)

type focusArea int

const (
	focusEditor focusArea = iota
	focusTree
	focusFind
)

// Background results, delivered to the UI goroutine through App.post.
type tickEvent struct{}
type filesEvent struct{ files []string }
type gitEvent struct{ s gitSnapshot }
type grepEvent struct {
	gen       int
	hits      []grepHit
	truncated bool
}
type openEvent struct{ targets []target }

type target struct {
	path      string
	line, col int
}

type App struct {
	w     *app.Window
	th    *Theme
	tc    *textCache
	root  string
	tree  *Tree
	title string

	bufs   []*Buffer
	active int
	focus  focusArea

	showSidebar bool
	sidebarDp   float32
	fontSp      float32

	msg     string
	msgErr  bool
	msgTime time.Time

	overlay *Overlay
	find    *FindBar
	lastQ   []rune // last search, for find-next and match highlights

	mu      sync.Mutex
	pending []any

	files        []string
	filesLoading bool
	gitBusy      bool
	branch       string
	tickN        int
	sessionSig   string

	lineCopy    string
	pasteTarget focusArea
	follow      bool // scroll the caret into view on the next frame
	focused     bool // window has OS focus
	blinkStart  time.Time
	keyTag      int // focus target for all keyboard input
	imeSel      key.Range
	imeSnippet  key.Snippet

	noSession bool
	socket    string

	sideView int // sideFiles, sideChanges or sideHistory
	scm      scmState

	nativeView     uintptr // NSView on macOS, for title bar styling
	nativeTitlebar bool    // keep the standard title bar
	lastSize       [2]int

	ui uiState
}

func newApp(w *app.Window, root string, th *Theme, fontSp float32) *App {
	a := &App{w: w, th: th, root: root, showSidebar: true, sidebarDp: 250, fontSp: fontSp, follow: true}
	a.tc = newTextCache()
	a.tree = newTree(root)
	a.tree.gitRoot = gitToplevel(root)
	a.blinkStart = time.Now()
	a.ui.hoverTab, a.ui.hoverRow = -1, -1
	a.scm.hover, a.scm.tabHover = -1, -1
	return a
}

func (a *App) post(v any) {
	a.mu.Lock()
	a.pending = append(a.pending, v)
	a.mu.Unlock()
	if a.w != nil {
		a.w.Invalidate()
	}
}

func (a *App) buf() *Buffer {
	if a.active >= 0 && a.active < len(a.bufs) {
		return a.bufs[a.active]
	}
	return nil
}

func (a *App) setMsg(format string, args ...any) {
	a.msg = fmt.Sprintf(format, args...)
	a.msgErr = false
	a.msgTime = time.Now()
}

func (a *App) setErr(format string, args ...any) {
	a.setMsg(format, args...)
	a.msgErr = true
}

func (a *App) rel(p string) string {
	if p == "" {
		return "untitled"
	}
	if r, err := filepath.Rel(a.root, p); err == nil && !strings.HasPrefix(r, "..") {
		return r
	}
	if home, err := os.UserHomeDir(); err == nil && strings.HasPrefix(p, home+"/") {
		return "~" + p[len(home):]
	}
	return p
}

func (a *App) abs(p string) string {
	if strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, p[2:])
		}
	}
	if !filepath.IsAbs(p) {
		p = filepath.Join(a.root, p)
	}
	return filepath.Clean(p)
}

// ---- opening and closing files ----

// openFile opens path in a permanent tab.
func (a *App) openFile(path string, line, col int) bool {
	return a.open(path, line, col, false)
}

// openPreview opens path in the preview tab (shown in italics), which is
// reused by the next preview unless it gets pinned, VS Code style.
func (a *App) openPreview(path string, line, col int) bool {
	return a.open(path, line, col, true)
}

func (a *App) open(path string, line, col int, preview bool) bool {
	path = a.abs(path)
	for i, b := range a.bufs {
		if b.path == path {
			if !preview {
				b.preview = false
			}
			a.active = i
			a.focus = focusEditor
			if line > 0 {
				a.gotoPos(b, line, col)
			}
			a.tree.reveal(path)
			return true
		}
	}
	b, err := loadBuffer(path)
	if err != nil {
		if err == errBinary {
			a.setErr("%s is a binary file", a.rel(path))
		} else {
			a.setErr("Can't open %s: %v", a.rel(path), err)
		}
		return false
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		a.setMsg("New file: %s (created when you save)", a.rel(path))
	}
	b.preview = preview
	a.placeBuffer(b, preview)
	a.focus = focusEditor
	b.resolveLexer()
	if b.hl.lexer != nil && len(b.lines) < 20000 {
		// highlight small files before the first paint to avoid a flash
		if t := b.text(); len(t) < 128<<10 {
			b.hl.spans = computeSpans(b.hl.lexer, a.th, t)
			b.hl.version = b.version
		}
	}
	if line > 0 {
		a.gotoPos(b, line, col)
	}
	a.tree.reveal(path)
	a.follow = true
	return true
}

// placeBuffer adds b as a tab: into the reusable preview slot, over a
// pristine untitled buffer, or at the end.
func (a *App) placeBuffer(b *Buffer, preview bool) {
	pi := -1
	if preview {
		for i, x := range a.bufs {
			if x.preview && !x.dirty() {
				pi = i
				break
			}
		}
	}
	cb := a.buf()
	switch {
	case pi >= 0:
		a.bufs[pi] = b
		a.active = pi
	case cb != nil && cb.path == "" && cb.virtualKey == "" && !cb.dirty() && len(cb.lines) == 1 && len(cb.lines[0]) == 0:
		a.bufs[a.active] = b
	default:
		a.bufs = append(a.bufs, b)
		a.active = len(a.bufs) - 1
	}
}

func (a *App) gotoPos(b *Buffer, line, col int) {
	b.cur = b.clamp(Pos{line - 1, max(col-1, 0)})
	b.selecting = false
	b.wantCol = -1
	b.centerOnCursor = true
	a.follow = true
}

func (a *App) newUntitled() {
	b := newBuffer("")
	// lowest number not already used by an open untitled tab
	used := map[int]bool{}
	for _, x := range a.bufs {
		if x.path == "" {
			used[x.untitledN] = true
		}
	}
	for b.untitledN = 1; used[b.untitledN]; b.untitledN++ {
	}
	a.bufs = append(a.bufs, b)
	a.active = len(a.bufs) - 1
	a.focus = focusEditor
}

func (a *App) closeBuffer(i int) {
	if i < 0 || i >= len(a.bufs) {
		return
	}
	a.bufs = append(a.bufs[:i], a.bufs[i+1:]...)
	if a.active >= len(a.bufs) {
		a.active = len(a.bufs) - 1
	} else if a.active > i {
		a.active--
	}
	if len(a.bufs) == 0 {
		a.focus = focusTree
		a.find = nil
	}
}

func (a *App) requestClose(i int) {
	if i < 0 || i >= len(a.bufs) {
		return
	}
	b := a.bufs[i]
	if !b.dirty() {
		a.closeBuffer(i)
		return
	}
	a.active = i
	a.confirm(fmt.Sprintf("Save changes to %s before closing?", b.name()), []button{
		{"Save", 's', func() { a.saveBuffer(b, func() { a.closeBuffer(a.indexOf(b)) }) }},
		{"Don't Save", 'd', func() { a.closeBuffer(a.indexOf(b)) }},
		{"Cancel", 'c', func() {}},
	})
}

func (a *App) indexOf(b *Buffer) int {
	for i, x := range a.bufs {
		if x == b {
			return i
		}
	}
	return -1
}

func (a *App) saveBuffer(b *Buffer, then func()) {
	if b.readOnly {
		a.setMsg("%s is a read-only view", b.name())
		return
	}
	if b.path == "" {
		dir := ""
		if a.showSidebar {
			if d := a.rel(a.targetDir(a.tree.selected())); d != "." {
				dir = d + "/"
			}
		}
		a.prompt("Save "+b.name()+" as", dir, true, func(s string) {
			s = strings.TrimSpace(s)
			if s == "" || strings.HasSuffix(s, "/") {
				return
			}
			p := a.abs(s)
			commit := func() {
				b.path = p
				b.untitledN = 0
				b.hl.resolved = false
				b.hl.lexer = nil
				b.hl.version = -1
				b.resolveLexer()
				a.saveBuffer(b, then)
				a.tree.reveal(p)
				a.loadFiles()
			}
			if _, err := os.Stat(p); err == nil {
				a.confirm(fmt.Sprintf("%s already exists. Replace it?", a.rel(p)), []button{
					{"Replace", 'r', commit},
					{"Cancel", 'c', func() {}},
				})
				return
			}
			commit()
		})
		return
	}
	if st, err := os.Stat(b.path); err == nil && !b.modTime.IsZero() && (!st.ModTime().Equal(b.modTime) || st.Size() != b.size) {
		b.conflict = true
	}
	if b.conflict {
		a.confirm(fmt.Sprintf("%s changed on disk since you opened it.", b.name()), []button{
			{"Overwrite", 'o', func() { b.conflict = false; a.saveBuffer(b, then) }},
			{"Load Disk Version", 'l', func() { a.reload(b) }},
			{"Cancel", 'c', func() {}},
		})
		return
	}
	if err := b.save(); err != nil {
		a.setErr("Save failed: %v", err)
		return
	}
	a.setMsg("Saved %s", a.rel(b.path))
	a.tree.refresh()
	a.refreshGit()
	if then != nil {
		then()
	}
}

func (a *App) reload(b *Buffer) {
	if b.path == "" {
		return
	}
	if err := b.reloadFromDisk(); err != nil {
		a.setErr("Reload failed: %v", err)
		return
	}
	a.setMsg("Reloaded %s from disk (undo restores your version)", b.name())
}

func (a *App) quit() {
	a.saveSession()
	if a.w != nil {
		a.w.Perform(system.ActionClose)
	}
}

// ---- background work ----

func (a *App) scheduleHighlight(b *Buffer, visibleLines int) {
	if b == nil {
		return
	}
	b.resolveLexer()
	h := b.hl
	if h.lexer == nil || h.running {
		return
	}
	size := 0
	for _, l := range b.lines {
		size += len(l) + 1
		if size > fullHighlightBytes {
			break
		}
	}
	windowed := size > fullHighlightBytes
	if !windowed && h.version == b.version && !h.windowed {
		return
	}
	from, to := 0, len(b.lines)
	if windowed {
		top, bot := b.scrollY, min(len(b.lines), b.scrollY+visibleLines+1)
		if h.version == b.version && h.windowed && top >= h.from && bot <= h.from+len(h.spans) {
			return
		}
		from = max(0, top-hlWindowMargin)
		to = min(len(b.lines), bot+hlWindowMargin)
	}
	var sb strings.Builder
	for i := from; i < to; i++ {
		if i > from {
			sb.WriteByte('\n')
		}
		sb.WriteString(string(b.lines[i]))
	}
	text := sb.String()
	h.running = true
	ver, lexer, th := b.version, h.lexer, a.th
	go func() {
		a.post(hlResult{b, ver, from, windowed, computeSpans(lexer, th, text)})
	}()
}

func (a *App) loadFiles() {
	if a.filesLoading {
		return
	}
	a.filesLoading = true
	root := a.root
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		a.post(filesEvent{listFiles(ctx, root)})
	}()
}

func (a *App) refreshGit() {
	if a.tree.gitRoot == "" || a.gitBusy {
		return
	}
	a.gitBusy = true
	root := a.tree.gitRoot
	prev := a.scm.head
	wantLog := !a.scm.logLoaded && a.sideView == sideHistory
	go func() { a.post(gitEvent{gitSnapshotFor(root, prev, wantLog)}) }()
}

// checkDisk notices files changed by other programs (AI agents, git, ...).
func (a *App) checkDisk() {
	for _, b := range a.bufs {
		if b.path == "" {
			continue
		}
		st, err := os.Stat(b.path)
		if err != nil {
			if os.IsNotExist(err) && !b.modTime.IsZero() && !b.diskGone {
				b.diskGone = true
				if b == a.buf() {
					a.setErr("%s was deleted on disk", b.name())
				}
			}
			continue
		}
		b.diskGone = false
		if b.modTime.IsZero() || (st.ModTime().Equal(b.modTime) && st.Size() == b.size) {
			continue
		}
		if b.dirty() {
			if !b.conflict {
				b.conflict = true
				a.setErr("%s changed on disk while you have unsaved edits", b.name())
			}
			continue
		}
		if err := b.reloadFromDisk(); err == nil && b == a.buf() {
			a.setMsg("%s changed on disk and was reloaded (undo restores the previous text)", b.name())
		}
	}
}

func (a *App) startTicker() {
	go func() {
		for {
			time.Sleep(time.Second)
			a.post(tickEvent{})
		}
	}()
}

func (a *App) drainPending() {
	a.mu.Lock()
	evs := a.pending
	a.pending = nil
	a.mu.Unlock()
	for _, d := range evs {
		switch d := d.(type) {
		case hlResult:
			h := d.buf.hl
			h.running = false
			h.spans = d.spans
			h.from = d.from
			h.windowed = d.windowed
			h.version = d.version
		case filesEvent:
			a.filesLoading = false
			a.files = d.files
			if o := a.overlay; o != nil && o.kind == ovQuick {
				o.updateQuick(a)
			}
		case gitEvent:
			a.gitBusy = false
			a.tree.git = d.s.status
			a.branch = d.s.branch
			a.applyGit(d.s)
		case grepEvent:
			if o := a.overlay; o != nil && o.kind == ovGrep && o.gen == d.gen {
				o.grepHits = d.hits
				o.truncated = d.truncated
				o.busy = false
				o.sel, o.scroll = 0, 0
			}
		case openEvent:
			for _, t := range d.targets {
				a.openFile(t.path, t.line, t.col)
			}
			if a.w != nil {
				a.w.Perform(system.ActionRaise)
			}
		case tickEvent:
			a.tickN++
			a.checkDisk()
			if a.tickN%2 == 0 {
				a.tree.refresh()
				a.refreshGit()
			}
			if a.tickN%3 == 0 {
				a.saveSessionIfChanged()
			}
		}
	}
}

func (a *App) updateTitle() {
	t := filepath.Base(a.root)
	if b := a.buf(); b != nil {
		d := ""
		if b.dirty() {
			d = " ●"
		}
		t = b.name() + d + " — " + t
	}
	if t != a.title && a.w != nil {
		a.title = t
		if !a.setNativeTitle(t) {
			a.w.Option(app.Title(t))
		}
	}
}

// ---- the frame ----

func (a *App) frame(gtx layout.Context) {
	a.drainPending()
	for _, b := range a.bufs {
		if b.preview && b.dirty() {
			b.preview = false // editing a preview keeps it
		}
	}
	if a.msg != "" && !a.msgErr && time.Since(a.msgTime) > 6*time.Second {
		a.msg = ""
	}
	if a.msg != "" && a.msgErr && time.Since(a.msgTime) > 12*time.Second {
		a.msg = ""
	}
	before := a.stateSig()
	a.layout(gtx)
	// Widgets drawn early in the frame (tabs, editor) can be stale if a
	// later one (tree, overlay) changed state; redraw right away.
	if a.stateSig() != before {
		gtx.Execute(op.InvalidateCmd{})
	}
	title := a.title
	a.updateTitle()
	// AppKit re-lays out the title bar on resize and title changes, which
	// resets the traffic lights; put them back.
	if sz := [2]int{gtx.Constraints.Max.X, gtx.Constraints.Max.Y}; sz != a.lastSize || title != a.title {
		a.lastSize = sz
		a.styleWindow()
	}
}

// titleBarDp is the height of the empty title strip on macOS.
const titleBarDp = 38

// lightsInset is the space kept free for the macOS window buttons.
func (a *App) lightsInset(gtx layout.Context) int {
	if !hiddenTitlebar || a.nativeTitlebar {
		return 0
	}
	return gtx.Dp(trafficLightsDp)
}

// rect helper
func rect(x, y, w, h int) image.Rectangle { return image.Rect(x, y, x+w, y+h) }

type frameSig struct {
	active, nbufs, sel, ver int
	buf                     *Buffer
	cur, anchor             Pos
	sel2, preview           bool
	focus                   focusArea
	overlay                 *Overlay
	find                    *FindBar
	sidebar                 bool
}

func (a *App) stateSig() frameSig {
	s := frameSig{active: a.active, nbufs: len(a.bufs), sel: a.tree.sel, buf: a.buf(), focus: a.focus,
		overlay: a.overlay, find: a.find, sidebar: a.showSidebar}
	if b := s.buf; b != nil {
		s.ver, s.cur, s.anchor, s.sel2, s.preview = b.version, b.cur, b.anchor, b.selecting, b.preview
	}
	return s
}
