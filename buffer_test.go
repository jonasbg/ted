package main

import (
	"os"
	"path/filepath"
	"testing"
)

func bufFrom(s string) *Buffer {
	b := newBuffer("")
	b.lines = splitLines(s)
	return b
}

func TestInsertDeleteUndoRedo(t *testing.T) {
	b := bufFrom("hello\nworld")
	b.cur = Pos{0, 5}
	b.insert([]rune(", there\nnew"), "")
	if got := b.text(); got != "hello, there\nnew\nworld" {
		t.Fatalf("after insert: %q", got)
	}
	if b.cur != (Pos{1, 3}) {
		t.Fatalf("cursor %v", b.cur)
	}
	b.deleteRange(Pos{0, 2}, Pos{2, 1}, "")
	if got := b.text(); got != "heorld" {
		t.Fatalf("after delete: %q", got)
	}
	b.doUndo()
	if got := b.text(); got != "hello, there\nnew\nworld" {
		t.Fatalf("after undo: %q", got)
	}
	b.doUndo()
	if got := b.text(); got != "hello\nworld" {
		t.Fatalf("after 2nd undo: %q", got)
	}
	if b.dirty() {
		t.Fatal("should be clean after undoing everything")
	}
	b.doRedo()
	b.doRedo()
	if got := b.text(); got != "heorld" {
		t.Fatalf("after redo: %q", got)
	}
}

func TestTypingGroupsIntoOneUndo(t *testing.T) {
	b := bufFrom("")
	for _, r := range "abc" {
		b.insert([]rune{r}, "type")
	}
	if len(b.undo) != 1 {
		t.Fatalf("want 1 undo group, got %d", len(b.undo))
	}
	b.doUndo()
	if b.text() != "" {
		t.Fatalf("got %q", b.text())
	}
}

func TestReloadFromDiskIsMinimalAndUndoable(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "f.txt")
	os.WriteFile(p, []byte("one\ntwo\nthree\nfour\n"), 0o644)
	b, err := loadBuffer(p)
	if err != nil {
		t.Fatal(err)
	}
	b.cur = Pos{3, 2} // on "four"
	os.WriteFile(p, []byte("one\nTWO\nextra\nthree\nfour\n"), 0o644)
	if err := b.reloadFromDisk(); err != nil {
		t.Fatal(err)
	}
	if got := b.text(); got != "one\nTWO\nextra\nthree\nfour\n" {
		t.Fatalf("reload: %q", got)
	}
	if b.cur != (Pos{4, 2}) {
		t.Fatalf("cursor should follow its line, got %v", b.cur)
	}
	if b.dirty() {
		t.Fatal("reloaded buffer should be clean")
	}
	b.doUndo()
	if got := b.text(); got != "one\ntwo\nthree\nfour\n" {
		t.Fatalf("undo reload: %q", got)
	}
}

func TestSaveRoundTripCRLF(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "w.txt")
	os.WriteFile(p, []byte("a\r\nb\r\n"), 0o600)
	b, _ := loadBuffer(p)
	b.cur = Pos{1, 1}
	b.insert([]rune("c"), "")
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(p)
	if string(data) != "a\r\nbc\r\n" {
		t.Fatalf("got %q", data)
	}
	st, _ := os.Stat(p)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("mode not preserved: %v", st.Mode())
	}
}

func TestBinaryRejected(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.bin")
	os.WriteFile(p, []byte{1, 0, 2}, 0o644)
	if _, err := loadBuffer(p); err != errBinary {
		t.Fatalf("want errBinary, got %v", err)
	}
}

func TestDetectIndent(t *testing.T) {
	if got := detectIndent(splitLines("a\n  b\n    c\n  d"), "x.txt"); got != "  " {
		t.Fatalf("got %q", got)
	}
	if got := detectIndent(splitLines("a\n\tb"), "x.txt"); got != "\t" {
		t.Fatalf("got %q", got)
	}
}

func TestSplitLineSuffix(t *testing.T) {
	cases := []struct {
		in        string
		p         string
		line, col int
	}{
		{"main.go", "main.go", 0, 0},
		{"main.go:12", "main.go", 12, 0},
		{"main.go:12:4", "main.go", 12, 4},
	}
	for _, c := range cases {
		p, l, co := splitLineSuffix(c.in)
		if p != c.p || l != c.line || co != c.col {
			t.Errorf("%s: got %s %d %d", c.in, p, l, co)
		}
	}
}

func TestFuzzyPrefersBaseName(t *testing.T) {
	hits := fuzzyFilter("app", []string{"internal/apple/zzz.go", "cmd/app.go", "a/p/p.go"}, 10)
	if len(hits) == 0 || hits[0].path != "cmd/app.go" {
		t.Fatalf("got %+v", hits)
	}
}

func TestFindWraps(t *testing.T) {
	b := bufFrom("foo\nbar foo\nbaz")
	p, ok := findFrom(b, []rune("foo"), Pos{1, 5}, true)
	if !ok || p != (Pos{0, 0}) {
		t.Fatalf("got %v %v", p, ok)
	}
	p, ok = findFrom(b, []rune("FOO"), Pos{0, 0}, true)
	if ok {
		t.Fatal("uppercase query should be case-sensitive")
	}
	p, ok = findFrom(b, []rune("foo"), Pos{0, 0}, false)
	if !ok || p != (Pos{1, 4}) {
		t.Fatalf("backward got %v", p)
	}
}

func TestMatchBracket(t *testing.T) {
	b := bufFrom("func() {\n\n  x(1)\n}")
	p, ok := matchBracket(b, Pos{0, 7})
	if !ok || p != (Pos{3, 0}) {
		t.Fatalf("got %v %v", p, ok)
	}
	p, ok = matchBracket(b, Pos{3, 0})
	if !ok || p != (Pos{0, 7}) {
		t.Fatalf("reverse got %v %v", p, ok)
	}
}

func TestSpliceKeepsSpansAligned(t *testing.T) {
	b := bufFrom("a\nb\nc")
	b.hl.spans = make([][]span, 3)
	b.cur = Pos{0, 1}
	b.insert([]rune("\nx\ny"), "")
	if len(b.hl.spans) != len(b.lines) {
		t.Fatalf("spans %d lines %d", len(b.hl.spans), len(b.lines))
	}
	b.deleteRange(Pos{0, 0}, Pos{3, 0}, "")
	if len(b.hl.spans) != len(b.lines) {
		t.Fatalf("after delete spans %d lines %d", len(b.hl.spans), len(b.lines))
	}
}

func TestVisCol(t *testing.T) {
	line := []rune("\tab世c")
	if visCol(line, 1) != 4 || visCol(line, 3) != 6 || visCol(line, 4) != 8 {
		t.Fatal("visCol wrong")
	}
	if colFromVis(line, 8) != 4 {
		t.Fatalf("colFromVis got %d", colFromVis(line, 8))
	}
}
