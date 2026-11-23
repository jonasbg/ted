package main

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"gioui.org/io/key"
)

func TestBufferGrep(t *testing.T) {
	b := bufFrom("ÄÄ needle\nnothing\nNEEDLE here")
	hits, trunc := bufferGrep([]*Buffer{b}, []string{"x.txt"}, "needle")
	if trunc || len(hits) != 2 {
		t.Fatalf("got %d hits (truncated %v), want 2", len(hits), trunc)
	}
	// byte column, after the two 2-byte runes and a space
	if h := hits[0]; h.line != 1 || h.col != 6 || h.buf != b || h.path != "x.txt" {
		t.Fatalf("first hit %+v", h)
	}
	if hits[1].line != 3 || hits[1].col != 1 {
		t.Fatalf("second hit %+v", hits[1])
	}
	// smart case: an upper-case query matches case-sensitively
	if hits, _ := bufferGrep([]*Buffer{b}, []string{"x.txt"}, "NEEDLE"); len(hits) != 1 || hits[0].line != 3 {
		t.Fatalf("smart case: %+v", hits)
	}
	// lower-casing that changes byte length keeps the column right
	k := bufFrom("İx target")
	if hits, _ := bufferGrep([]*Buffer{k}, []string{"k"}, "target"); len(hits) != 1 || hits[0].col != len("İx ")+1 {
		t.Fatalf("length-changing case fold: %+v", hits)
	}
}

func TestSearchOpenTabsFindsUnsavedText(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	os.WriteFile(a, []byte("alpha\n"), 0o644)
	os.WriteFile(b, []byte("beta\n"), 0o644)
	os.WriteFile(filepath.Join(dir, "c.txt"), []byte("unsavedword on disk only\n"), 0o644)
	h := newHarness(t, dir, a, b)
	buf := h.a.buf()
	buf.cur = Pos{0, 0}
	h.frame()
	h.typ("unsavedword ")

	h.keys(press("K", key.ModShortcut))
	h.keys(press(key.NameTab)) // folder -> open tabs
	h.typ("unsavedword")
	h.settle()
	o := h.a.overlay
	if o == nil || o.scope != scopeOpen {
		t.Fatalf("not in the open-tabs scope: %+v", o)
	}
	if len(o.grepHits) != 1 || o.grepHits[0].buf != buf {
		t.Fatalf("want the one unsaved hit in %s, got %+v", buf.name(), o.grepHits)
	}
	// Enter on the hit selects the match in that tab
	o.sel = len(o.quickHits)
	h.keys(press(key.NameReturn))
	if h.a.overlay != nil || h.a.buf() != buf || string(buf.selText()) != "unsavedword" {
		t.Fatalf("Enter did not select the match: sel %q", string(buf.selText()))
	}

	// the scope is remembered, and file names match open tabs only
	h.keys(press("K", key.ModShortcut))
	h.typ("c.txt")
	h.settle()
	if o := h.a.overlay; o.scope != scopeOpen || len(o.quickHits) != 0 {
		t.Fatalf("open-tabs scope matched a closed file: %+v", o.quickHits)
	}
	h.keys(press(key.NameTab))
	time.Sleep(400 * time.Millisecond)
	h.settle()
	if o := h.a.overlay; o.scope != scopeFolder || len(o.quickHits) == 0 || o.quickHits[0].path != "c.txt" {
		t.Fatalf("folder scope did not find c.txt: %+v", o.quickHits)
	}
}

func TestRefreshGitWhileBusyRunsAgain(t *testing.T) {
	a := newHarness(t, t.TempDir()).a
	a.tree.gitRoot = t.TempDir()
	a.gitBusy = true
	a.refreshGit()
	if !a.gitAgain {
		t.Fatal("a refresh asked for while one runs was dropped")
	}
}

func TestSettingsPage(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, dir)
	h.keys(press(",", key.ModShortcut))
	h.settle()
	o := h.a.overlay
	if o == nil || o.kind != ovSettings {
		t.Fatalf("Cmd+, did not open settings: %+v", o)
	}
	h.shot("7-settings")
	// buttons in draw order: close, font -, font +, dotfiles switch, ...
	if len(o.btns) < 4 {
		t.Fatalf("settings drew %d controls", len(o.btns))
	}
	font := h.a.fontSp
	r := o.btns[2]
	h.click(r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
	if h.a.fontSp != font+1 || h.a.overlay == nil {
		t.Fatalf("+ gave font %v (was %v), overlay %v", h.a.fontSp, font, h.a.overlay)
	}
	h.settle()
	r = o.btns[4] // close, -, +, Reset (shown once the size changed), dotfiles
	h.click(r.Min.X+r.Dx()/2, r.Min.Y+r.Dy()/2)
	if !h.a.tree.showHidden {
		t.Fatal("dotfiles switch did not turn on")
	}
	h.keys(press(key.NameEscape))
	if h.a.overlay != nil {
		t.Fatal("Esc did not close settings")
	}
}
