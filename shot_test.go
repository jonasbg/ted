package main

import (
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/gpu/headless"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// harness renders the real UI offscreen and feeds it real input events.
type harness struct {
	t   *testing.T
	a   *App
	w   *headless.Window
	r   input.Router
	ops op.Ops
	sz  image.Point

	redraws int
}

func newHarness(t *testing.T, root string, files ...string) *harness {
	sz := image.Pt(2400, 1500)
	w, err := headless.NewWindow(sz.X, sz.Y)
	if err != nil {
		t.Skip("no headless GPU:", err)
	}
	a := newApp(nil, root, newTheme(os.Getenv("TED_THEME_TEST")), defaultFontSp)
	a.focused = true
	a.noSession = true
	for _, f := range files {
		a.openFile(f, 0, 0)
	}
	if len(a.bufs) == 0 {
		a.focus = focusTree
	}
	h := &harness{t: t, a: a, w: w, sz: sz}
	h.frame()
	return h
}

func (h *harness) frame() {
	h.ops.Reset()
	gtx := layout.Context{
		Ops:         &h.ops,
		Now:         time.Now(),
		Metric:      unit.Metric{PxPerDp: 2, PxPerSp: 2},
		Constraints: layout.Exact(h.sz),
		Source:      h.r.Source(),
	}
	h.a.frame(gtx)
	h.r.Frame(&h.ops)
	if at, wake := h.r.WakeupTime(); wake && !at.After(time.Now()) && h.redraws < 3 {
		h.redraws++
		h.frame() // the app asked for an immediate redraw
		h.redraws--
		return
	}
	if err := h.w.Frame(&h.ops); err != nil {
		h.t.Fatal(err)
	}
}

// settle lets background work (highlighting, git, file index) land.
func (h *harness) settle() {
	for i := 0; i < 20; i++ {
		time.Sleep(20 * time.Millisecond)
		h.frame()
	}
}

func (h *harness) keys(evs ...event.Event) {
	for _, e := range evs {
		// app.Window queues plain Tab and Shift+Tab as system events (to move
		// focus when nothing handles them); do the same so tests see what the
		// real window delivers.
		if k, ok := e.(key.Event); ok && k.State == key.Press && k.Name == key.NameTab &&
			(k.Modifiers == 0 || k.Modifiers == key.ModShift) {
			e = input.SystemEvent{Event: e}
		}
		h.r.Queue(e)
		h.frame()
	}
	h.frame()
}

func (h *harness) typ(s string) {
	for _, r := range s {
		h.keys(key.EditEvent{Range: key.Range{Start: h.a.imeSel.Start, End: h.a.imeSel.End}, Text: string(r)})
	}
}

func press(name key.Name, mods ...key.Modifiers) key.Event {
	var m key.Modifiers
	for _, x := range mods {
		m |= x
	}
	return key.Event{Name: name, Modifiers: m, State: key.Press}
}

func (h *harness) shot(name string) {
	dir := os.Getenv("TED_SHOT")
	if dir == "" {
		return
	}
	img := image.NewRGBA(image.Rectangle{Max: h.sz})
	if err := h.w.Screenshot(img); err != nil {
		h.t.Fatal(err)
	}
	f, _ := os.Create(filepath.Join(dir, name+".png"))
	png.Encode(f, img)
	f.Close()
}

func TestUIScreenshots(t *testing.T) {
	root, _ := os.Getwd()
	h := newHarness(t, root, filepath.Join(root, "editor.go"))
	h.a.gotoPos(h.a.buf(), 120, 1)
	h.settle()
	h.shot("1-editor")

	// Cmd+K: go to file
	h.keys(press("K", key.ModShortcut))
	h.typ("ovrl")
	h.settle()
	h.shot("2-quickopen")
	if o := h.a.overlay; o == nil || len(o.quickHits) == 0 || o.quickHits[0].path != "overlay.go" {
		t.Fatalf("quick open did not find overlay.go: %+v", o)
	}
	h.keys(press(key.NameReturn))
	if h.a.buf().name() != "overlay.go" {
		t.Fatalf("expected overlay.go, got %s", h.a.buf().name())
	}

	// find
	h.keys(press("F", key.ModShortcut))
	h.typ("lineInput")
	h.settle()
	h.shot("3-find")
	if h.a.find == nil || h.a.find.info == "" || h.a.find.info == "No results" {
		t.Fatalf("find failed: %+v", h.a.find)
	}
	h.keys(press(key.NameEscape))

	// Cmd+K: text in the folder, then in the open tabs only
	h.keys(press("K", key.ModShortcut))
	h.typ("reloadFromDisk")
	time.Sleep(400 * time.Millisecond)
	h.settle()
	h.shot("4-grep")
	o := h.a.overlay
	if o == nil || o.scope != scopeFolder || len(o.grepHits) == 0 {
		t.Fatalf("folder search found nothing")
	}
	h.keys(press(key.NameTab))
	h.settle()
	if o.scope != scopeOpen {
		t.Fatalf("Tab did not switch to the open tabs")
	}
	for _, g := range o.grepHits {
		if g.buf == nil {
			t.Fatalf("open-tabs search returned a file hit: %+v", g)
		}
	}
	h.shot("4b-grep-open")
	h.keys(press(key.NameTab), press(key.NameEscape))
	h.keys(press("F1"))
	h.shot("5-help")
}

func TestTypingThroughIME(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.go")
	os.WriteFile(p, []byte("package main\n\nfunc main() {\n}\n"), 0o644)
	h := newHarness(t, dir, p)
	b := h.a.buf()
	b.cur = Pos{2, len(b.lines[2])}
	h.frame()
	h.keys(press(key.NameReturn))
	h.typ("if x {")
	h.keys(press(key.NameReturn))
	h.typ("println(\"hi\")")
	want := "package main\n\nfunc main() {\n\tif x {\n\t\tprintln(\"hi\")\n\t}\n}\n"
	if got := b.text(); got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
	// dead key style composition: IME replaces the last char
	end := h.a.imeSel.End
	h.keys(key.EditEvent{Range: key.Range{Start: end - 1, End: end}, Text: "é"})
	if l := string(b.lines[4]); l != "\t\tprintln(\"hi\"é" {
		t.Fatalf("IME replace: %q", l)
	}
	h.keys(press("Z", key.ModShortcut))
	h.keys(press("S", key.ModShortcut))
	data, _ := os.ReadFile(p)
	if string(data) != b.text() {
		t.Fatalf("save mismatch")
	}
	h.shot("6-typing")
}

func TestTitlebarLayout(t *testing.T) {
	root, _ := os.Getwd()
	h := newHarness(t, root, filepath.Join(root, "main.go"), filepath.Join(root, "app.go"))
	h.settle()
	h.shot("7-titlebar")
	h.keys(press("B", key.ModShortcut))
	h.settle()
	h.shot("8-nosidebar")
}

func TestTabCursorIsPointer(t *testing.T) {
	root, _ := os.Getwd()
	h := newHarness(t, root, filepath.Join(root, "main.go"), filepath.Join(root, "app.go"))
	tab := h.a.ui.tabRects[0]
	h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(tab.Min.X+10), float32(tab.Min.Y+10))})
	h.frame()
	if c := h.r.Cursor(); c != pointer.CursorPointer {
		t.Fatalf("cursor over tab = %v, want pointer", c)
	}
	ed := h.a.ui.text
	h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(ed.Min.X+50), float32(ed.Min.Y+50))})
	h.frame()
	if c := h.r.Cursor(); c != pointer.CursorText {
		t.Fatalf("cursor over text = %v, want text", c)
	}
	// clicking the first tab still switches to it
	h.r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(float32(tab.Min.X+10), float32(tab.Min.Y+10))},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(float32(tab.Min.X+10), float32(tab.Min.Y+10))})
	h.frame()
	if h.a.active != 0 {
		t.Fatalf("click on tab 0 left active=%d", h.a.active)
	}
}

func TestTreeWithFolders(t *testing.T) {
	root := t.TempDir()
	for _, p := range []string{
		"cmd/server/main.go", "cmd/cli/main.go", "internal/api/handler.go", "internal/api/routes.go",
		"internal/db/migrations/001_init.sql", "internal/db/store.go", "web/src/App.tsx", "web/src/index.css",
		"web/package.json", "docs/architecture.md", ".github/workflows/ci.yml", "go.mod", "README.md", "Makefile",
	} {
		full := filepath.Join(root, p)
		os.MkdirAll(filepath.Dir(full), 0o755)
		os.WriteFile(full, []byte("package api\n\nfunc Handler() {}\n"), 0o644)
	}
	h := newHarness(t, root, filepath.Join(root, "internal/api/handler.go"))
	h.a.tree.reveal(filepath.Join(root, "internal/db/migrations/001_init.sql"))
	h.a.tree.reveal(filepath.Join(root, "internal/api/handler.go"))
	h.a.tree.gitRoot = root
	h.a.tree.git = map[string]byte{filepath.Join(root, "internal/api/routes.go"): 'M', filepath.Join(root, "internal"): 'M', filepath.Join(root, "internal/api"): 'M', filepath.Join(root, "docs/architecture.md"): '?', filepath.Join(root, "docs"): '?'}
	h.settle()
	n := h.a.tree.rows[0]
	if !n.isDir {
		t.Fatal("dirs should sort first")
	}
	row := h.a.ui.sidebar.Min.Y + gtxDp(36) + 5
	h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(h.a.ui.sidebar.Min.X+40), float32(row))})
	h.frame()
	if c := h.r.Cursor(); c != pointer.CursorPointer {
		t.Fatalf("cursor over tree row = %v", c)
	}
	h.shot("9-folders")
}

func gtxDp(v float32) int { return int(v * 2) }

func (h *harness) click(x, y int) {
	p := f32.Pt(float32(x), float32(y))
	h.r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: p})
	h.frame()
}

func TestPreviewTabs(t *testing.T) {
	root := t.TempDir()
	for _, n := range []string{"a.go", "b.go", "c.go", "d.go"} {
		os.WriteFile(filepath.Join(root, n), []byte("package x\n"), 0o644)
	}
	h := newHarness(t, root)
	u := &h.a.ui
	rowY := func(i int) int { return u.sidebar.Min.Y + gtxDp(36) + i*u.rowH + u.rowH/2 }
	names := func() string {
		s := ""
		for _, b := range h.a.bufs {
			if b.preview {
				s += "~"
			}
			s += b.name() + " "
		}
		return s
	}
	// single clicks reuse one preview tab
	h.click(u.sidebar.Min.X+60, rowY(0))
	time.Sleep(500 * time.Millisecond)
	h.click(u.sidebar.Min.X+60, rowY(1))
	if got := names(); got != "~b.go " {
		t.Fatalf("after two single clicks: %q", got)
	}
	// double click pins
	time.Sleep(500 * time.Millisecond)
	h.click(u.sidebar.Min.X+60, rowY(2))
	h.click(u.sidebar.Min.X+60, rowY(2))
	if got := names(); got != "c.go " {
		t.Fatalf("after double click: %q", got)
	}
	// next single click opens a new preview next to it
	time.Sleep(500 * time.Millisecond)
	h.click(u.sidebar.Min.X+60, rowY(3))
	if got := names(); got != "c.go ~d.go " {
		t.Fatalf("after preview: %q", got)
	}
	h.shot("10-preview")
	// editing pins the preview
	h.typ("x")
	h.frame()
	if got := names(); got != "c.go d.go " {
		t.Fatalf("after edit: %q", got)
	}
	// double click on a preview tab pins it
	time.Sleep(500 * time.Millisecond)
	h.click(u.sidebar.Min.X+60, rowY(0))
	h.frame()
	tab := u.tabRects[h.a.active]
	h.click(tab.Min.X+12, tab.Min.Y+10)
	h.click(tab.Min.X+12, tab.Min.Y+10)
	if got := names(); got != "c.go d.go a.go " {
		t.Fatalf("after tab double click: %q", got)
	}
}

func TestHorizontalScrollByMouse(t *testing.T) {
	dir := t.TempDir()
	long := strings.Repeat("x", 300)
	short := strings.Repeat("short\n", 200)
	p := filepath.Join(dir, "w.txt")
	os.WriteFile(p, []byte(short+long+"\nend\n"), 0o644)
	h := newHarness(t, dir, p)
	ed := h.a.ui.text
	pos := f32.Pt(float32(ed.Min.X+100), float32(ed.Min.Y+100))
	b := h.a.buf()
	scroll := func(d f32.Point, mods key.Modifiers) {
		h.r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: pos, Scroll: d, Modifiers: mods})
		h.frame()
	}
	// the long line is off screen: nothing visible overflows, so no sideways scroll
	scroll(f32.Pt(500, 0), 0)
	if b.vx != 0 {
		t.Fatalf("scrolled sideways with nothing overflowing: vx=%v", b.vx)
	}
	// bring the long line into view
	b.vy = float32(190 * h.a.ui.lineH)
	h.frame()
	// a diagonal swipe stays vertical
	vy := b.vy
	scroll(f32.Pt(30, 30), 0)
	if b.vx != 0 || b.vy == vy {
		t.Fatalf("diagonal swipe: vx=%v vy=%v (was %v)", b.vx, b.vy, vy)
	}
	scroll(f32.Pt(200, 0), 0)
	if b.vx != 200 {
		t.Fatalf("sideways swipe: vx=%v, want 200", b.vx)
	}
	scroll(f32.Pt(-500, 0), 0)
	if b.vx != 0 {
		t.Fatalf("swipe back: vx=%v, want 0", b.vx)
	}
	scroll(f32.Pt(0, 40), key.ModShift)
	if b.vx != 40 {
		t.Fatalf("shift+wheel: vx=%v, want 40", b.vx)
	}
	b.vx = 0

	// the caret still pulls the view along a long line
	b.cur = Pos{200, 300}
	h.a.follow = true
	h.frame()
	if b.vx <= 0 {
		t.Fatal("view did not follow the caret to the end of the long line")
	}
}

func TestUntitledScratch(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir)
	h.a.focus = focusTree // Cmd+N works from the tree too, without asking
	h.keys(press("N", key.ModShortcut))
	h.keys(press("N", key.ModShortcut))
	if h.a.overlay != nil || len(h.a.bufs) != 2 || h.a.bufs[0].name() != "Untitled-1" || h.a.bufs[1].name() != "Untitled-2" {
		t.Fatalf("cmd+n: overlay=%v bufs=%d", h.a.overlay, len(h.a.bufs))
	}
	h.typ("package main\n")
	h.keys(press(key.NameReturn))
	h.typ("func main() {")
	h.frame()
	if h.a.buf().hl.lang != "Go" {
		t.Fatalf("language not detected from content: %q", h.a.buf().hl.lang)
	}
	h.shot("11-untitled")
	h.keys(press("S", key.ModShortcut))
	if h.a.overlay == nil || h.a.overlay.kind != ovPrompt {
		t.Fatal("cmd+s on untitled should ask for a name")
	}
	h.typ("scratch.go")
	h.keys(press(key.NameReturn))
	if _, err := os.Stat(filepath.Join(dir, "scratch.go")); err != nil {
		t.Fatalf("not saved: %v", err)
	}
	if h.a.buf().name() != "scratch.go" || h.a.buf().dirty() {
		t.Fatalf("after save: %s dirty=%v", h.a.buf().name(), h.a.buf().dirty())
	}
}

func TestStatusBarItems(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.py")
	os.WriteFile(p, []byte("def f():\n\treturn 1\n"), 0o644)
	h := newHarness(t, dir, p)
	h.frame()
	item := func(id string) image.Rectangle {
		for _, it := range h.a.ui.statusItems {
			if it.id == id {
				return it.r
			}
		}
		t.Fatalf("no status item %s", id)
		return image.Rectangle{}
	}
	// hand cursor over items
	r := item("lang")
	h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(r.Min.X+4), float32(r.Min.Y+4))})
	h.frame()
	if c := h.r.Cursor(); c != pointer.CursorPointer {
		t.Fatalf("cursor over status item = %v", c)
	}
	// language picker
	h.click(r.Min.X+4, r.Min.Y+4)
	if o := h.a.overlay; o == nil || o.kind != ovPick {
		t.Fatal("language picker did not open")
	}
	h.typ("ruby")
	h.shot("12-langpick")
	h.keys(press(key.NameReturn))
	if got := h.a.buf().hl.lang; got != "Ruby" {
		t.Fatalf("language = %q", got)
	}
	// line endings
	h.frame()
	r = item("eol")
	h.click(r.Min.X+4, r.Min.Y+4)
	h.typ("crlf")
	h.keys(press(key.NameReturn))
	b := h.a.buf()
	if !b.crlf || !b.dirty() {
		t.Fatalf("crlf=%v dirty=%v", b.crlf, b.dirty())
	}
	// convert indentation to spaces
	h.frame()
	r = item("indent")
	h.click(r.Min.X+4, r.Min.Y+4)
	h.typ("convert spaces")
	h.keys(press(key.NameReturn))
	if got := string(b.lines[1]); got != "    return 1" {
		t.Fatalf("convert: %q", got)
	}
	h.keys(press("S", key.ModShortcut))
	data, _ := os.ReadFile(p)
	if string(data) != "def f():\r\n    return 1\r\n" {
		t.Fatalf("saved %q", data)
	}
}

func TestPickerItemsHaveHandCursor(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.txt")
	os.WriteFile(p, []byte("hi\n"), 0o644)
	h := newHarness(t, dir, p)
	h.a.statusClick("eol")
	h.frame()
	o := h.a.overlay
	if o == nil || len(o.items) == 0 {
		t.Fatal("picker not shown")
	}
	r := o.items[1]
	h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(r.Min.X+10), float32(r.Min.Y+5))})
	h.frame()
	if c := h.r.Cursor(); c != pointer.CursorPointer {
		t.Fatalf("cursor over picker item = %v", c)
	}
	// a single click picks the item
	h.click(r.Min.X+10, r.Min.Y+5)
	if !h.a.buf().crlf {
		t.Fatal("clicking CRLF did not apply it")
	}
}

func TestPickerHover(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.txt")
	os.WriteFile(p, []byte("hi\n"), 0o644)
	h := newHarness(t, dir, p)
	h.a.statusClick("indent")
	h.frame()
	o := h.a.overlay
	if o.hover != -1 {
		t.Fatalf("hover should start at -1, got %d", o.hover)
	}
	r := o.items[3]
	h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Position: f32.Pt(float32(r.Min.X+10), float32(r.Min.Y+5))})
	h.frame()
	if o.hover != 3 {
		t.Fatalf("hover = %d, want 3", o.hover)
	}
	h.shot("13-hover")
}

func TestPickerScrollSpeed(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.txt")
	os.WriteFile(p, []byte("hi\n"), 0o644)
	h := newHarness(t, dir, p)
	h.a.statusClick("lang")
	h.frame()
	o := h.a.overlay
	sel := o.sel
	r := o.items[2]
	pos := f32.Pt(float32(r.Min.X+10), float32(r.Min.Y+5))
	// a trackpad swipe: 30 small events of 4px each = 120px = 2 rows at 60px
	for i := 0; i < 30; i++ {
		h.r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: pos, Scroll: f32.Pt(0, 4)})
		h.frame()
	}
	if o.sel != sel {
		t.Fatalf("wheel moved the selection: %d -> %d", sel, o.sel)
	}
	if want := 120 / o.rowPx; o.scroll != want {
		t.Fatalf("scrolled %d rows, want %d (rowPx=%d)", o.scroll, want, o.rowPx)
	}
	// arrow keys bring the selection back into view
	h.keys(press(key.NameDownArrow))
	if o.sel != sel+1 || o.scroll > o.sel {
		t.Fatalf("after down: sel=%d scroll=%d", o.sel, o.scroll)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=Test", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestSourceControlViews(t *testing.T) {
	dir := t.TempDir()
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		dir = r
	}
	gitRun(t, dir, "init", "-q", "-b", "main")
	write := func(p, s string) {
		os.MkdirAll(filepath.Dir(filepath.Join(dir, p)), 0o755)
		os.WriteFile(filepath.Join(dir, p), []byte(s), 0o644)
	}
	write("main.go", "package main\n\nfunc main() {\n\tprintln(\"hello\")\n}\n")
	write("lib/util.go", "package lib\n\nfunc Add(a, b int) int { return a + b }\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "Initial commit")
	write("README.md", "# demo\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "Add a readme with the project name")
	// working state: one staged, one unstaged, one untracked
	write("lib/util.go", "package lib\n\nfunc Add(a, b int) int { return a + b }\n\nfunc Sub(a, b int) int { return a - b }\n")
	gitRun(t, dir, "add", "lib/util.go")
	write("main.go", "package main\n\nfunc main() {\n\tprintln(\"hello, world\")\n}\n")
	write("notes.txt", "todo\n")

	h := newHarness(t, dir, filepath.Join(dir, "main.go"))
	h.a.tree.gitRoot = dir
	// load git state synchronously for the test
	h.a.applyGit(gitSnapshotFor(dir, "", true))
	h.a.branch = "main"

	// Cmd+Shift+G opens the Changes view
	h.keys(press("G", key.ModShortcut|key.ModShift))
	if h.a.sideView != sideChanges || !h.a.showSidebar {
		t.Fatal("changes view not shown")
	}
	var secs []string
	for _, r := range h.a.scm.rows {
		if r.c == nil {
			secs = append(secs, fmt.Sprintf("%s:%d", r.section, r.count))
		}
	}
	if got := strings.Join(secs, " "); got != "Staged Changes:1 Changes:1 Untracked:1" {
		t.Fatalf("sections: %s", got)
	}
	h.frame()
	h.shot("14-changes")

	// click the unstaged main.go row: opens a read-only diff preview
	u := &h.a.ui
	rowAt := func(i int) (int, int) {
		return u.sidebar.Min.X + 80, u.sidebar.Min.Y + gtxDp(36) + gtxDp(26) + i*u.rowH + u.rowH/2
	}
	idx := -1
	for i, r := range h.a.scm.rows {
		if r.c != nil && r.section == "Changes" {
			idx = i
		}
	}
	x, y := rowAt(idx)
	h.click(x, y)
	h.frame()
	b := h.a.buf()
	if !b.readOnly || !strings.Contains(b.text(), "+\tprintln(\"hello, world\")") || !b.preview {
		t.Fatalf("diff tab: ro=%v preview=%v\n%s", b.readOnly, b.preview, b.text())
	}
	if b.name() != "main.go (Working Tree)" {
		t.Fatalf("diff tab name %q", b.name())
	}
	// typing is refused in a read-only view
	before := b.text()
	h.typ("x")
	if b.text() != before {
		t.Fatal("read-only diff was edited")
	}
	h.shot("15-diff")

	// History view lists commits newest first; clicking opens the commit
	h.keys(press("H", key.ModShortcut|key.ModShift))
	if h.a.sideView != sideHistory || len(h.a.scm.commits) != 2 || h.a.scm.commits[0].subject != "Add a readme with the project name" {
		t.Fatalf("history: %+v", h.a.scm.commits)
	}
	h.frame()
	h.click(u.sidebar.Min.X+80, u.sidebar.Min.Y+gtxDp(36)+gtxDp(46)+gtxDp(20))
	h.frame()
	b = h.a.buf()
	if !strings.Contains(b.text(), "Initial commit") || !strings.Contains(b.text(), "+func Add") {
		t.Fatalf("commit tab:\n%s", b.text())
	}
	h.shot("16-history")

	// the activity bar icons switch views; clicking the active one hides the panel
	act := h.a.scm.tabRects[sideFiles]
	h.click(act.Min.X+act.Dx()/2, act.Min.Y+act.Dy()/2)
	if h.a.sideView != sideFiles {
		t.Fatal("files icon did not switch view")
	}
	h.frame()
	act = h.a.scm.tabRects[sideFiles]
	h.click(act.Min.X+act.Dx()/2, act.Min.Y+act.Dy()/2)
	if h.a.showSidebar {
		t.Fatal("clicking the active icon should hide the panel")
	}
}

func TestMinimap(t *testing.T) {
	root, _ := os.Getwd()
	h := newHarness(t, root, filepath.Join(root, "overlay.go"))
	h.settle()
	m := &h.a.mm
	if m.rect.Empty() {
		t.Fatal("minimap not shown")
	}
	b := h.a.buf()
	if b.vy != 0 {
		t.Fatalf("vy=%v", b.vy)
	}
	// clicking near the bottom jumps down the file
	h.click(m.rect.Min.X+20, m.rect.Max.Y-20)
	if b.vy <= 0 {
		t.Fatal("click on minimap did not scroll")
	}
	jumped := b.vy
	jumpedTo := func(*Buffer) float32 { return jumped }
	// press near the top jumps back up; dragging down from there scrolls down
	p0 := f32.Pt(float32(m.rect.Min.X+20), float32(m.rect.Min.Y+40))
	h.r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: p0})
	h.frame()
	afterPress := b.vy
	h.r.Queue(pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: f32.Pt(p0.X, p0.Y+300)})
	h.frame()
	h.r.Queue(pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: f32.Pt(p0.X, p0.Y+300)})
	h.frame()
	if afterPress >= jumpedTo(b) || b.vy <= afterPress {
		t.Fatalf("press near top %v, after drag down %v", afterPress, b.vy)
	}
	if b.hasSel() {
		t.Fatal("minimap interaction selected text")
	}
	h.shot("17-minimap")

	// the viewport box hides when idle and shows on scroll or hover
	gtx := layout.Context{Now: time.Now()}
	h.a.scrollAt = time.Now().Add(-5 * time.Second)
	if v := h.a.scrollVisibility(gtx, false); v != 0 {
		t.Fatalf("idle visibility %v, want 0", v)
	}
	if v := h.a.scrollVisibility(gtx, true); v != 1 {
		t.Fatalf("hover visibility %v, want 1", v)
	}
	ed := h.a.ui.text
	h.r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(float32(ed.Min.X+50), float32(ed.Min.Y+50)), Scroll: f32.Pt(0, 60)})
	h.frame()
	if v := h.a.scrollVisibility(layout.Context{Now: time.Now()}, false); v != 1 {
		t.Fatalf("visibility right after scrolling %v, want 1", v)
	}
}
