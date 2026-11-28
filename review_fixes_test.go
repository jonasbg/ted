package main

import (
	"encoding/base64"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
)

func TestReadOnlyBufferNeverPanicsOnEdits(t *testing.T) {
	b := bufFrom("{}\nlast")
	b.readOnly = true
	b.cur = Pos{0, 1}
	b.newline()
	if b.cur.L < 0 || b.cur.L >= len(b.lines) {
		t.Fatalf("newline on readOnly left cursor out of bounds: %v", b.cur)
	}
	b.cur = Pos{1, 2}
	b.duplicateLines(true)
	if b.cur.L < 0 || b.cur.L >= len(b.lines) {
		t.Fatalf("duplicateLines on readOnly left cursor out of bounds: %v", b.cur)
	}
	b.moveLines(-1)
	b.moveLines(1)
	b.deleteLines()
	b.indentSel(false)
	b.indentSel(true)
	b.toggleComment()
	b.typeText("hello")
	b.backspace()
	b.deleteForward()
	if got := b.text(); got != "{}\nlast" {
		t.Fatalf("readOnly buffer was modified: %q", got)
	}
	if b.convertIndent(true) != 0 {
		t.Fatal("convertIndent modified readOnly buffer")
	}
}

func TestReadOnlyCmdShiftEnterDoesNotPanic(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir)
	b := newBuffer("")
	b.setContentString("line1\nline2\n")
	b.readOnly = true
	b.cur = Pos{0, 0}
	h.a.bufs = []*Buffer{b}
	h.a.active = 0
	h.a.focus = focusEditor
	h.frame()
	// Previously panicked with b.lines[-1] on read-only buffers at line 0.
	h.keys(press(key.NameReturn, key.ModShortcut, key.ModShift))
	// Alt+Shift+Down at last line previously moved cur.L past len(b.lines).
	b.cur = Pos{len(b.lines) - 1, 0}
	h.keys(press(key.NameDownArrow, key.ModAlt, key.ModShift))
	h.frame()
	if b.text() != "line1\nline2\n" {
		t.Fatalf("read-only buffer mutated: %q", b.text())
	}
}

func TestOverlayPasteAndLineInputKeys(t *testing.T) {
	// Test word navigation/delete on Linux where ModShortcut == ModShortcutAlt.
	in := &lineInput{}
	in.set("hello world foo")
	in.all = false
	if !isMac {
		in.key(press(key.NameLeftArrow, key.ModCtrl))
		if in.cur != len("hello world ") {
			t.Fatalf("Ctrl+Left on Linux jumped to %d, want %d", in.cur, len("hello world "))
		}
		in.key(press(key.NameDeleteBackward, key.ModCtrl))
		if in.String() != "hello foo" {
			t.Fatalf("Ctrl+Backspace on Linux gave %q, want %q", in.String(), "hello foo")
		}
	}

	// Test paste into overlay (Cmd/Ctrl+K).
	dir := t.TempDir()
	h := newHarness(t, dir)
	h.keys(press("K", key.ModShortcut))
	if h.a.overlay == nil || h.a.overlay.kind != ovSearch {
		t.Fatal("Cmd+K did not open search overlay")
	}
	h.a.onPaste("pasted-query")
	if got := h.a.overlay.in.String(); got != "pasted-query" {
		t.Fatalf("overlay paste = %q, want pasted-query", got)
	}
}

func TestUnicodeFuzzyScoreAndCompletePath(t *testing.T) {
	// Previously panicked when strings.ToLower changed UTF-8 byte length (e.g. İ U+0130).
	cands := []string{
		"İSTANBUL/Ångström.go",
		"Kelvin/café_🚀.txt",
		"src/MainView.go",
	}
	for _, q := range []string{"ang", "kel", "mv", "café", "stan"} {
		hits := fuzzyFilter(q, cands, 10)
		for _, h := range hits {
			for _, idx := range h.matches {
				if idx < 0 || idx >= len(h.path) {
					t.Fatalf("invalid match index %d in %q", idx, h.path)
				}
			}
		}
	}

	// completePath must not cut multi-byte UTF-8 runes in half.
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "ä.txt"), nil, 0o644)
	os.WriteFile(filepath.Join(dir, "ö.txt"), nil, 0o644)
	got := completePath(dir, "")
	if !utf8.ValidString(got) {
		t.Fatalf("completePath produced invalid UTF-8: %q", got)
	}
	if got != "" {
		t.Fatalf("completePath of ä.txt / ö.txt = %q, want empty", got)
	}
}

func TestAdjacentBase64EncodeAllSpans(t *testing.T) {
	a := &App{}
	e1 := base64.StdEncoding.EncodeToString([]byte("hello world"))
	e2 := base64.StdEncoding.EncodeToString([]byte("second part"))
	b := bufFrom(e1 + " " + e2)
	if !a.toggleBase64(b, Pos{0, 2}) {
		t.Fatal("failed to decode first span")
	}
	if !a.toggleBase64(b, Pos{0, len("hello world ") + 2}) {
		t.Fatal("failed to decode second span")
	}
	// Delete the space between them so sp1.c == sp2.a (adjacent spans).
	b.deleteRange(Pos{0, len("hello world")}, Pos{0, len("hello world") + 1}, "")
	if len(b.b64) != 2 || b.b64[0].c != b.b64[1].a {
		t.Fatalf("expected adjacent spans, got %+v", b.b64)
	}
	b.encodeAllSpans()
	if got, want := b.text(), e1+e2; got != want {
		t.Fatalf("adjacent encodeAllSpans corrupted text:\ngot:  %q\nwant: %q", got, want)
	}
}

func TestReplaceOneRespectsSmartCase(t *testing.T) {
	a := newApp(nil, "", newTheme("github"), defaultFontSp)
	b := bufFrom("foo Foo")
	a.bufs = []*Buffer{b}
	a.active = 0
	a.openFind(true)
	a.find.in.set("Foo")
	a.find.repl.set("Bar")
	// Manually select lowercase "foo": smart-case query "Foo" must NOT replace it.
	b.anchor, b.cur, b.selecting = Pos{0, 0}, Pos{0, 3}, true
	a.replaceOne()
	if b.text() != "foo Foo" {
		t.Fatalf("replaceOne replaced wrong-case selection: %q", b.text())
	}
	// Now findStep moved selection to "Foo"; next replaceOne replaces it.
	a.replaceOne()
	if b.text() != "foo Bar" {
		t.Fatalf("replaceOne did not replace matching-case selection: %q", b.text())
	}
}

func TestAlignCloserSingleUndoAndPairRedo(t *testing.T) {
	b := bufFrom("func f() {\n\t")
	b.cur = Pos{1, 1}
	b.typeRune('}')
	if got := b.text(); got != "func f() {\n}" {
		t.Fatalf("alignCloser: %q", got)
	}
	// One Ctrl+Z must undo both typing '}' and its dedent.
	b.doUndo()
	if got := b.text(); got != "func f() {\n\t" {
		t.Fatalf("single undo after alignCloser: %q", got)
	}

	// Auto-pair redo restores cursor inside the brackets.
	b2 := bufFrom("")
	b2.typeRune('(')
	if b2.text() != "()" || b2.cur != (Pos{0, 1}) {
		t.Fatalf("after auto-pair: %q cur=%v", b2.text(), b2.cur)
	}
	b2.doUndo()
	b2.doRedo()
	if b2.text() != "()" || b2.cur != (Pos{0, 1}) {
		t.Fatalf("after redo of auto-pair: %q cur=%v, want {0 1}", b2.text(), b2.cur)
	}
}

func TestBlockOffsetMatchesLinear(t *testing.T) {
	rng := rand.New(rand.NewPCG(7, 9))
	var sb strings.Builder
	for i := 0; i < 700; i++ {
		sb.WriteString(strings.Repeat("a", i%17))
		sb.WriteByte('\n')
	}
	b := newBuffer("")
	b.setContentString(sb.String())

	linearOffset := func(p Pos) int {
		off := 0
		for i := 0; i < p.L; i++ {
			off += len(b.lines[i]) + 1
		}
		return off + p.C
	}

	pieces := []string{"x", "hello", "世界", "\n", "\nline\n"}
	for step := 0; step < 500; step++ {
		l := rng.IntN(len(b.lines))
		p := Pos{l, rng.IntN(len(b.lines[l]) + 1)}
		if rng.IntN(2) == 0 {
			b.cur = p
			b.insert([]rune(pieces[rng.IntN(len(pieces))]), "")
		} else {
			l2 := min(len(b.lines)-1, l+rng.IntN(3))
			q := Pos{l2, rng.IntN(len(b.lines[l2]) + 1)}
			b.deleteRange(p, q, "")
		}
		probe := Pos{rng.IntN(len(b.lines)), 0}
		probe.C = rng.IntN(len(b.lines[probe.L]) + 1)
		gotOff := b.offsetOf(probe)
		wantOff := linearOffset(probe)
		if gotOff != wantOff {
			t.Fatalf("step %d: offsetOf(%v) = %d, want %d", step, probe, gotOff, wantOff)
		}
		if back := b.posAt(gotOff); back != probe {
			t.Fatalf("step %d: posAt(%d) = %v, want %v", step, gotOff, back, probe)
		}
	}
}

func TestCSVParseNumberNegativeCurrencyAndEuropean(t *testing.T) {
	cases := map[string]float64{
		"-$1,234.50": -1234.50,
		"$-42.5":     -42.5,
		"-€42,50":    -42.50,
		"1.234,56":   1234.56,
		"-1.234,56":  -1234.56,
		"€ 100":      100,
	}
	for in, want := range cases {
		if v, ok := parseNumber(in); !ok || v != want {
			t.Errorf("parseNumber(%q) = %v, %v; want %v", in, v, ok, want)
		}
	}
}

func TestTabLabelsDisambiguate(t *testing.T) {
	b1 := newBuffer("/proj/api/main.go")
	b2 := newBuffer("/proj/cli/main.go")
	b3 := newBuffer("/proj/README.md")
	got := tabLabels([]*Buffer{b1, b2, b3})
	want := []string{"api/main.go", "cli/main.go", "README.md"}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tabLabels = %v, want %v", got, want)
		}
	}
}

func TestBlockCommentAndLangCommentToggle(t *testing.T) {
	// HTML block comment
	h := newBuffer("/x/index.html")
	h.setContentString("  <div>hi</div>\n")
	h.cur = Pos{0, 4}
	if !h.toggleComment() || h.text() != "  <!-- <div>hi</div> -->\n" {
		t.Fatalf("html comment: %q", h.text())
	}
	if !h.toggleComment() || h.text() != "  <div>hi</div>\n" {
		t.Fatalf("html uncomment: %q", h.text())
	}
	// CSS block comment
	c := newBuffer("/x/app.css")
	c.setContentString("color: red;\n")
	if !c.toggleComment() || c.text() != "/* color: red; */\n" {
		t.Fatalf("css comment: %q", c.text())
	}
	if !c.toggleComment() || c.text() != "color: red;\n" {
		t.Fatalf("css uncomment: %q", c.text())
	}
	// Untitled buffer with SQL picked manually uses "--"
	u := newBuffer("")
	u.setContentString("SELECT 1;\n")
	u.setLanguage("sql")
	if !u.toggleComment() || u.text() != "-- SELECT 1;\n" {
		t.Fatalf("sql comment: %q", u.text())
	}
}

func TestDoubleClickDragBackwardsKeepsInitialWord(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "w.txt")
	os.WriteFile(p, []byte("alpha beta gamma\n"), 0o644)
	h := newHarness(t, dir, p)
	h.frame()
	u := &h.a.ui
	pt := func(c int) f32.Point {
		return f32.Pt(float32(u.text.Min.X+u.padLeft+c*u.charW+u.charW/2), float32(u.editor.Min.Y+u.padTop+u.lineH/2))
	}
	// Double-click "beta" (cols 6..10), then drag left onto "alpha" (col 1).
	beta := pt(7)
	alpha := pt(1)
	h.r.Queue(
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: beta},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: beta},
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: beta},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: alpha},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: alpha},
	)
	h.frame()
	if got := string(h.a.buf().selText()); got != "alpha beta" {
		t.Fatalf("double-click drag backwards selected %q, want %q", got, "alpha beta")
	}
}

func TestMonospaceFromFontconfigEditBeforeTest(t *testing.T) {
	conf := `<fontconfig><match><edit name="family"><string>Mono</string></edit><test name="family"><string>monospace</string></test></match></fontconfig>`
	// Must not panic even when <edit> precedes <test>.
	_ = monospaceFromFontconfig(conf)
}

func TestToUTF8TruncatedMidRune(t *testing.T) {
	// "Café" followed by a truncated 3-byte UTF-8 sequence (only first 2 bytes of '€' = E2 82 AC).
	data := append([]byte("Café "), 0xE2, 0x82)
	got := string(toUTF8(data, "text/html"))
	if got != "Café " {
		t.Fatalf("truncated UTF-8 was corrupted: %q", got)
	}
}

func TestReopenClosedTab(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.go")
	os.WriteFile(p, []byte("package main\n\nfunc main() {}\n"), 0o644)
	a := newApp(nil, dir, newTheme("github"), defaultFontSp)
	a.noSession = true
	a.openFile(p, 3, 5)
	a.closeBuffer(0)
	if len(a.bufs) != 0 {
		t.Fatal("buffer not closed")
	}
	a.reopenClosedTab()
	if len(a.bufs) != 1 || a.bufs[0].path != p || a.bufs[0].cur.L != 2 {
		t.Fatalf("reopenClosedTab failed: bufs=%d cur=%v", len(a.bufs), a.bufs[0].cur)
	}
}

func TestFontsWithFamilyDoesNotMutateGlobal(t *testing.T) {
	orig := faceFonts[fMono].Typeface
	f1 := fontsWithFamily("CustomMono")
	f2 := fontsWithFamily("CustomMono")
	if faceFonts[fMono].Typeface != orig {
		t.Fatal("fontsWithFamily mutated global faceFonts")
	}
	if f1[fMono].Typeface != f2[fMono].Typeface {
		t.Fatal("fontsWithFamily not deterministic")
	}
}

func TestExpandTabsTabStops(t *testing.T) {
	if got := expandTabs("a\tb\ncd\te", 4); got != "a   b\ncd  e" {
		t.Fatalf("expandTabs = %q", got)
	}
}
