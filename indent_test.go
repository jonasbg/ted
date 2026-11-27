package main

import (
	"os"
	"strings"
	"testing"

	"gioui.org/io/key"
)

// TestTabIndentsSelection drives Tab and Shift+Tab through the real key
// router: a selection within one line indents that line instead of being
// replaced, multi-line selections shift every line, and with no selection
// Shift+Tab outdents the caret's line.
func TestTabIndentsSelection(t *testing.T) {
	dir := t.TempDir()
	f := dir + "/a.txt"
	if err := os.WriteFile(f, []byte("alpha beta\ngamma\ndelta\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, dir, f)
	b := h.a.bufs[h.a.active]
	b.indent = "    "
	check := func(step, want string) {
		t.Helper()
		if got := b.text(); got != want {
			t.Fatalf("%s: got %q, want %q", step, got, want)
		}
	}

	// select "beta" on line 0
	b.cur = Pos{0, 6}
	b.anchor, b.selecting = b.cur, true
	b.cur = Pos{0, 10}
	h.keys(press(key.NameTab))
	check("tab on single-line selection", "    alpha beta\ngamma\ndelta\n")
	if s, e := b.selRange(); string(b.textRange(s, e)) != "beta" {
		t.Fatalf("selection moved off the word: %q", b.textRange(s, e))
	}
	h.keys(press(key.NameTab, key.ModShift))
	check("shift-tab on single-line selection", "alpha beta\ngamma\ndelta\n")

	// select lines 0..1 and shift them
	b.anchor, b.cur, b.selecting = Pos{0, 2}, Pos{1, 2}, true
	h.keys(press(key.NameTab))
	check("tab on multi-line selection", "    alpha beta\n    gamma\ndelta\n")
	h.keys(press(key.NameTab, key.ModShift))
	check("shift-tab on multi-line selection", "alpha beta\ngamma\ndelta\n")

	// no selection: Tab indents at the caret, Shift+Tab outdents the line
	b.selecting = false
	b.cur = Pos{2, 0}
	h.keys(press(key.NameTab))
	check("tab on caret line", "alpha beta\ngamma\n    delta\n")
	b.cur = Pos{2, 7}
	h.keys(press(key.NameTab, key.ModShift))
	check("shift-tab on caret line", "alpha beta\ngamma\ndelta\n")
	if b.cur != (Pos{2, 3}) {
		t.Fatalf("caret after outdent: %v", b.cur)
	}

	// off-grid indent snaps to indent stops both ways: 6 -> 4 -> 8 -> 4
	b.cur = Pos{2, 0}
	b.insert([]rune("      "), "")
	b.anchor, b.cur, b.selecting = Pos{2, 6}, Pos{2, 8}, true
	for _, st := range []struct {
		shift bool
		want  string
	}{{true, "    delta"}, {false, "        delta"}, {true, "    delta"}} {
		var mods []key.Modifiers
		if st.shift {
			mods = append(mods, key.ModShift)
		}
		h.keys(press(key.NameTab, mods...))
		if got := string(b.lines[2]); got != st.want {
			t.Fatalf("shift=%v: got %q, want %q", st.shift, got, st.want)
		}
		if s, e := b.selRange(); string(b.textRange(s, e)) != "de" {
			t.Fatalf("selection drifted: %q", b.textRange(s, e))
		}
	}
}

// TestShiftTabKeepsNesting outdents a pasted YAML block that sits 3 columns
// in: every line moves by the same amount, so the nesting stays valid.
func TestShiftTabKeepsNesting(t *testing.T) {
	yaml := "apiVersion: argoproj.io/v1alpha1\n" +
		"kind: Application\n" +
		"metadata:\n" +
		"  name: searxng\n" +
		"  namespace: argocd\n" +
		"spec:\n" +
		"  project: default\n" +
		"  source:\n" +
		"    repoURL: https://example.com/gitops.git\n" +
		"    targetRevision: main\n" +
		"  syncPolicy:\n" +
		"    syncOptions:\n" +
		"    - CreateNamespace=true\n"
	pasted := ""
	for _, l := range strings.SplitAfter(strings.TrimSuffix(yaml, "\n"), "\n") {
		pasted += "   " + l
	}
	pasted += "\n"
	for _, tc := range []struct {
		indent  string
		presses int
	}{{"    ", 1}, {"  ", 2}} { // 3 -> 0, or 3 -> 2 -> 0
		dir := t.TempDir()
		f := dir + "/app.yaml"
		if err := os.WriteFile(f, []byte(pasted), 0o644); err != nil {
			t.Fatal(err)
		}
		h := newHarness(t, dir, f)
		b := h.a.bufs[h.a.active]
		b.indent = tc.indent
		h.keys(press("A", key.ModShortcut))
		for i := 0; i < tc.presses; i++ {
			h.keys(press(key.NameTab, key.ModShift))
		}
		if got := b.text(); got != yaml {
			t.Fatalf("indent %q: got\n%s\nwant\n%s", tc.indent, got, yaml)
		}
		h.keys(press(key.NameTab))
		if got := string(b.lines[3]); got != tc.indent+"  name: searxng" {
			t.Fatalf("indent %q: Tab back gave %q", tc.indent, got)
		}
	}
}

// TestPasteIntoEmptyBufferSetsIndent pastes the space-indented YAML into a
// new .go file (tab default): the paste decides the indent, and Shift+Tab
// clears the 3-column offset in one press.
func TestPasteIntoEmptyBufferSetsIndent(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir, dir+"/new.go")
	b := h.a.bufs[h.a.active]
	if b.indent != "\t" {
		t.Fatalf("new .go file indent %q, want tab", b.indent)
	}
	h.a.onPaste("   a:\n     b: 1\n     c:\n       - d\n")
	if b.indent != "  " {
		t.Fatalf("indent after paste %q, want 2 spaces", b.indent)
	}

	// a tab buffer holding space-indented lines steps by a tab's width
	b.indent = "\t"
	h.keys(press("A", key.ModShortcut), press(key.NameTab, key.ModShift))
	if got := b.text(); got != "a:\n  b: 1\n  c:\n    - d\n" {
		t.Fatalf("shift-tab in tab buffer: %q", got)
	}

	// pasting unindented text into an empty buffer keeps the chosen indent
	h.a.openFile(dir+"/other.go", 0, 0)
	h.frame()
	b = h.a.bufs[h.a.active]
	b.indent = "        "
	h.a.onPaste("x\ny\n")
	if b.indent != "        " {
		t.Fatalf("unindented paste changed indent to %q", b.indent)
	}
}
