package main

import (
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
)

const mdSample = "# Release notes\n" +
	"\n" +
	"Ted renders **Markdown** natively, with *emphasis*, ***both***, `inline code`, ~~struck~~ text\n" +
	"and [links](https://example.com) that wrap across lines when the paragraph is long enough to need it, like this one.\n" +
	"\n" +
	"## Table\n" +
	"\n" +
	"| Feature | Status | Notes |\n" +
	"|:--------|:------:|------:|\n" +
	"| Tables | ✓ | Columns size to their content and wrap when the window is narrow |\n" +
	"| Task lists | ✓ | `- [x]` |\n" +
	"| Mermaid | source | Shown as a labelled code block |\n" +
	"\n" +
	"## Lists\n" +
	"\n" +
	"- First item\n" +
	"- Second item with a nested list:\n" +
	"  - nested one\n" +
	"  - nested two\n" +
	"- [x] done task\n" +
	"- [ ] open task\n" +
	"\n" +
	"1. one\n" +
	"2. two\n" +
	"3. three\n" +
	"\n" +
	"> A quote that explains something important,\n" +
	"> over two source lines.\n" +
	"\n" +
	"```go\n" +
	"func main() {\n" +
	"\tfmt.Println(\"hello\")\n" +
	"}\n" +
	"```\n" +
	"\n" +
	"```mermaid\n" +
	"graph TD; A-->B\n" +
	"```\n" +
	"\n" +
	"---\n" +
	"\n" +
	"![a square](square.png)\n" +
	"\n" +
	"See [the table](#table) or [the other file](other.md).\n"

func writeMdFixture(t *testing.T) (string, string) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notes.md")
	if err := os.WriteFile(p, []byte(mdSample), 0o644); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(dir, "other.md"), []byte("# Other\n\nSecond file.\n"), 0o644)
	img := image.NewRGBA(image.Rect(0, 0, 120, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 120; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 2), 0x80, uint8(y * 3), 0xff})
		}
	}
	f, _ := os.Create(filepath.Join(dir, "square.png"))
	png.Encode(f, img)
	f.Close()
	return dir, p
}

func TestMarkdownPreview(t *testing.T) {
	dir, p := writeMdFixture(t)
	h := newHarness(t, dir, p)
	b := h.a.buf()
	if !isMarkdown(b) {
		t.Fatal("notes.md is not recognised as Markdown")
	}
	h.keys(press("V", key.ModShortcut, key.ModShift))
	h.settle()
	h.shot("md-preview")
	if !b.previewing() || b.md.doc == nil {
		t.Fatal("Cmd+Shift+V did not open the preview")
	}
	d := b.md.doc
	var images, words int
	for _, it := range d.items {
		switch it.kind {
		case miImage:
			images++
		case miText:
			words++
		}
	}
	if images != 1 {
		t.Errorf("want the local image drawn once, got %d", images)
	}
	if words < 50 {
		t.Errorf("only %d text items laid out", words)
	}
	if _, ok := d.anchors["table"]; !ok {
		t.Errorf("no anchor for the Table heading: %v", d.anchors)
	}
	if len(d.links) < 3 {
		t.Errorf("want links recorded, got %d", len(d.links))
	}

	// typing and pasting do nothing in the preview
	ver := b.version
	h.typ("x")
	h.keys(press(key.NameDeleteBackward))
	if b.version != ver {
		t.Error("typing in the preview changed the text")
	}

	// scrolling moves the preview, not the source
	h.keys(press(key.NameEnd))
	if b.md.vy <= 0 {
		t.Error("End did not scroll the preview")
	}
	h.shot("md-preview-end")

	// an anchor link scrolls to its heading
	var anchor mdLink
	for _, l := range d.links {
		if l.dest == "#table" {
			anchor = l
		}
	}
	h.keys(press(key.NameEnd))
	h.clickMd(b, anchor.r.Min.Add(image.Pt(2, 2)))
	if want := float32(d.anchors["table"]); b.md.vy != want {
		t.Errorf("anchor link scrolled to %v, want %v", b.md.vy, want)
	}

	// a relative link opens the other file, also in preview
	for _, l := range d.links {
		if l.dest == "other.md" {
			h.keys(press(key.NameEnd))
			h.clickMd(b, l.r.Min.Add(image.Pt(2, 2)))
			break // each word of the link has its own rect
		}
	}
	if nb := h.a.buf(); nb.name() != "other.md" || !nb.previewing() {
		t.Fatalf("relative link did not open other.md in preview: %s", nb.name())
	}
	h.a.active = h.a.indexOf(b)
	h.frame()

	// Esc goes back to the source, near the place that was in view
	h.keys(press(key.NameHome))
	h.keys(press(key.NameEscape))
	if b.previewing() {
		t.Fatal("Esc did not return to the source")
	}
	h.typ("!")
	if b.version == ver {
		t.Error("typing in the source after the preview did nothing")
	}
}

// clickMd clicks at a point in the preview's content coordinates.
func (h *harness) clickMd(b *Buffer, p image.Point) {
	h.frame()
	u := &h.a.ui
	r := u.editor
	cw := b.md.key.width
	ox := r.Min.X + (r.Dx()-cw)/2
	oy := r.Min.Y + 48 // topPad at 2 px per dp
	pos := f32.Pt(float32(ox+p.X), float32(oy+p.Y)-b.md.vy)
	if pos.Y < float32(r.Min.Y+20) || pos.Y > float32(r.Max.Y-20) {
		// scroll so the point is in view first
		b.md.vy = float32(p.Y - 100)
		h.frame()
		pos = f32.Pt(float32(ox+p.X), float32(oy+p.Y)-b.md.vy)
	}
	h.r.Queue(pointer.Event{Kind: pointer.Move, Position: pos, Source: pointer.Mouse})
	h.r.Queue(pointer.Event{Kind: pointer.Press, Position: pos, Buttons: pointer.ButtonPrimary, Source: pointer.Mouse})
	h.r.Queue(pointer.Event{Kind: pointer.Release, Position: pos, Source: pointer.Mouse})
	h.frame()
	h.frame()
}

func TestMarkdownDoubleClickJumpsToSource(t *testing.T) {
	dir, p := writeMdFixture(t)
	h := newHarness(t, dir, p)
	b := h.a.buf()
	h.keys(press("V", key.ModShortcut, key.ModShift))
	h.settle()
	// find the block for the ordered list ("1. one" is source line 23)
	var y int
	for _, bl := range b.md.doc.blocks {
		if bl.line == 22 {
			y = bl.y
		}
	}
	if y == 0 {
		t.Fatalf("no block for line 23: %+v", b.md.doc.blocks)
	}
	pt := image.Pt(40, y+4)
	h.clickMd(b, pt)
	h.clickMd(b, pt)
	if b.previewing() {
		t.Fatal("double-click did not return to the source")
	}
	if b.cur.L != 22 {
		t.Errorf("caret on line %d, want 23", b.cur.L+1)
	}
}

func TestMarkdownNarrowTable(t *testing.T) {
	dir, p := writeMdFixture(t)
	h := newHarness(t, dir, p)
	h.sz = image.Pt(900, 1500)
	h.a.showSidebar = false
	h.keys(press("V", key.ModShortcut, key.ModShift))
	h.settle()
	h.shot("md-narrow")
	b := h.a.buf()
	cw := b.md.key.width
	for _, it := range b.md.doc.items {
		if it.kind == miText && it.r.Min.X+h.a.tc.width(it.f, it.px, it.s) > cw+4 {
			t.Errorf("%q overflows the %dpx column at x=%d", it.s, cw, it.r.Min.X)
		}
	}
}

func TestSlug(t *testing.T) {
	for in, want := range map[string]string{
		"Hello, World!":     "hello-world",
		"What's new in 0.3": "whats-new-in-03",
		"  Table ":          "table",
	} {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// Regressions found in review.

func TestPreviewFromFindBarBlocksEditing(t *testing.T) {
	dir, p := writeMdFixture(t)
	h := newHarness(t, dir, p)
	b := h.a.buf()
	h.keys(press("F", key.ModShortcut))
	if h.a.focus != focusFind {
		t.Fatal("Cmd+F did not focus the find bar")
	}
	h.keys(press("V", key.ModShortcut, key.ModShift))
	ver := b.version
	h.keys(press(key.NameReturn), press(key.NameDeleteBackward), press("K", key.ModShortcut, key.ModShift))
	if b.version != ver || b.dirty() {
		t.Fatal("keys edited the hidden source after opening the preview from the find bar")
	}
	h.keys(press(key.NameEscape))
	if b.previewing() {
		t.Error("Esc did not leave the preview")
	}
}

func TestLongWordLayoutIsLinear(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "blob.md")
	os.WriteFile(p, []byte("# Blob\n\n"+strings.Repeat("QUJDRA", 7000)+"\n"), 0o644)
	h := newHarness(t, dir, p)
	t0 := time.Now()
	h.keys(press("V", key.ModShortcut, key.ModShift))
	if d := time.Since(t0); d > 2*time.Second {
		t.Errorf("a 42,000 character word took %v to lay out", d)
	}
	b := h.a.buf()
	for _, it := range b.md.doc.items {
		if it.kind == miText && it.r.Min.X+h.a.tc.width(it.f, it.px, it.s) > b.md.key.width+4 {
			t.Fatalf("a piece of the long word overflows the column")
		}
	}
}

func TestPreviewRefusesNonWebSchemes(t *testing.T) {
	dir, p := writeMdFixture(t)
	h := newHarness(t, dir, p)
	h.keys(press("V", key.ModShortcut, key.ModShift))
	b := h.a.buf()
	h.a.followMdLink(b, "file:///Applications/Calculator.app")
	if !h.a.msgErr || !strings.Contains(h.a.msg, "only opens") {
		t.Errorf("file:// link was not refused: %q", h.a.msg)
	}
}

func TestJumpToLineLeavesPreview(t *testing.T) {
	dir, p := writeMdFixture(t)
	h := newHarness(t, dir, p)
	h.keys(press("V", key.ModShortcut, key.ModShift))
	h.a.openFile(p, 20, 1)
	h.frame()
	if b := h.a.buf(); b.previewing() || b.cur.L != 19 {
		t.Errorf("jump to line 20: previewing=%v line=%d", b.previewing(), b.cur.L+1)
	}
}

func TestShiftPasteOutsideMarkdown(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.txt")
	os.WriteFile(p, []byte("x\n"), 0o644)
	h := newHarness(t, dir, p)
	h.keys(press("V", key.ModShortcut, key.ModShift))
	if strings.Contains(h.a.msg, "Preview") {
		t.Errorf("Cmd+Shift+V in a text file talked about the preview: %q", h.a.msg)
	}
}

func TestWideTableAndDuplicateHeadings(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "wide.md")
	var sb strings.Builder
	sb.WriteString("# Same\n\n# Same\n\n|")
	for c := 0; c < 60; c++ {
		sb.WriteString(" column" + strings.Repeat("x", c%7) + " |")
	}
	sb.WriteString("\n|")
	for c := 0; c < 60; c++ {
		sb.WriteString("---|")
	}
	sb.WriteString("\n|")
	for c := 0; c < 60; c++ {
		sb.WriteString(" value |")
	}
	sb.WriteString("\n")
	os.WriteFile(p, []byte(sb.String()), 0o644)
	h := newHarness(t, dir, p)
	h.keys(press("V", key.ModShortcut, key.ModShift))
	b := h.a.buf()
	d := b.md.doc
	for _, it := range d.items {
		if it.r.Max.X > b.md.key.width+4 && it.kind != miText {
			t.Fatalf("table shape reaches x=%d in a %d px column", it.r.Max.X, b.md.key.width)
		}
	}
	if d.anchors["same"] >= d.anchors["same-1"] {
		t.Errorf("duplicate headings: %v", d.anchors)
	}
}
