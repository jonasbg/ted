package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScratchJSONDetectedAndFormatted(t *testing.T) {
	// pasted (or restored) minified JSON: recognised and formatted
	b := newBuffer("")
	b.insert([]rune(`{"a":[1,2],"b":{"c":"d"}}`), "")
	b.resolveLexer()
	if b.hl.lang != "JSON" {
		t.Fatalf("lang %q", b.hl.lang)
	}
	if want := "{\n  \"a\": [\n    1,\n    2\n  ],\n  \"b\": {\n    \"c\": \"d\"\n  }\n}\n"; b.text() != want {
		t.Fatalf("got %q", b.text())
	}
	b.doUndo()
	if b.text() != `{"a":[1,2],"b":{"c":"d"}}` {
		t.Fatalf("format should be its own undo step: %q", b.text())
	}

	// typed by hand: recognised, but not reformatted under the caret
	b = newBuffer("")
	for _, r := range `{"a":1}` {
		b.insert([]rune{r}, "type")
		b.resolveLexer()
	}
	if b.hl.lang != "JSON" || b.text() != `{"a":1}` {
		t.Fatalf("typed: lang %q text %q", b.hl.lang, b.text())
	}

	// not JSON: left alone
	b = newBuffer("")
	b.insert([]rune("{ not json"), "")
	b.resolveLexer()
	if b.hl.lang == "JSON" {
		t.Fatal("invalid JSON recognised as JSON")
	}
}

func TestUnnamedJSONFileGetsView(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"response", "dump.txt"} {
		p := filepath.Join(dir, name)
		os.WriteFile(p, []byte(`{"ok":true,"items":[1,2,3]}`), 0o644)
		b, err := loadBuffer(p)
		if err != nil {
			t.Fatal(err)
		}
		b.resolveLexer()
		if !b.jsonView || b.hl.lang != "JSON" || b.dirty() || !strings.Contains(b.text(), "\n  \"items\": [\n") {
			t.Errorf("%s: view=%v lang=%q dirty=%v\n%s", name, b.jsonView, b.hl.lang, b.dirty(), b.text())
		}
	}
	// a file another language claims is not touched
	p := filepath.Join(dir, "x.js")
	os.WriteFile(p, []byte(`{"ok":true}`), 0o644)
	if b, _ := loadBuffer(p); b.jsonView {
		t.Error("x.js shown as JSON")
	}
}

func TestPickingJSONFormats(t *testing.T) {
	h := newHarness(t, t.TempDir())
	h.a.newUntitled()
	b := h.a.buf()
	for _, r := range `{"a":1}` {
		b.insert([]rune{r}, "type")
	}
	h.frame()
	h.a.statusClick("lang")
	o := h.a.overlay
	if o == nil || o.onPick == nil {
		t.Fatal("no language picker")
	}
	h.a.closeOverlay()
	o.onPick("JSON")
	if b.text() != "{\n  \"a\": 1\n}\n" && b.text() != "{\n    \"a\": 1\n}\n" {
		t.Fatalf("picking JSON did not format: %q", b.text())
	}
}

func bigMinifiedJSON(n int) string {
	var sb strings.Builder
	sb.WriteString(`{"items":[`)
	for i := 0; i < n; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":%d,"name":"item %d","tags":["a","b"]}`, i, i)
	}
	sb.WriteString(`]}`)
	return sb.String()
}

func TestLargeReloadInBackground(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.json")
	src := bigMinifiedJSON(40000)
	if len(src) <= asyncReloadSize {
		t.Fatalf("test file too small: %d", len(src))
	}
	os.WriteFile(p, []byte(src), 0o644)
	h := newHarness(t, dir, p)
	h.frame()
	b := h.a.buf()
	if !b.jsonView {
		t.Fatal("no formatted view")
	}
	wait := func(cond func() bool) bool {
		for i := 0; i < 200 && !cond(); i++ {
			time.Sleep(10 * time.Millisecond)
			h.frame()
		}
		return cond()
	}

	// an external write is picked up without blocking the frame
	os.WriteFile(p, []byte(strings.Replace(src, `"item 20000"`, `"changed"`, 1)), 0o644)
	os.Chtimes(p, time.Now(), time.Now().Add(time.Second))
	start := time.Now()
	h.a.checkDisk()
	if d := time.Since(start); d > 30*time.Millisecond {
		t.Errorf("checkDisk blocked for %v", d)
	}
	if !b.reloading {
		t.Fatal("large file did not reload in the background")
	}
	if !wait(func() bool { return !b.reloading }) || !strings.Contains(b.text(), `"name": "changed"`) || b.dirty() || !b.jsonView {
		t.Fatalf("reload: reloading=%v dirty=%v view=%v", b.reloading, b.dirty(), b.jsonView)
	}

	// typing while the file is read: the edit wins, and the change is
	// reported as a conflict on the next check
	os.WriteFile(p, []byte(strings.Replace(src, `"item 30000"`, `"again"`, 1)), 0o644)
	os.Chtimes(p, time.Now(), time.Now().Add(2*time.Second))
	h.a.checkDisk()
	b.cur = Pos{0, 1}
	b.insert([]rune(" "), "type")
	wait(func() bool { return !b.reloading })
	if strings.Contains(b.text(), `"again"`) || !strings.HasPrefix(b.text(), "{ ") {
		t.Fatal("background reload overwrote an edit")
	}
	h.a.checkDisk()
	if !b.conflict {
		t.Fatal("no conflict after the dropped reload")
	}
}

func TestScratchJSONKeepsItsIndent(t *testing.T) {
	src := "{\n    \"a\": [\n        1\n    ],\n    \"b\": 2\n}\n"
	b := newBuffer("")
	b.insert([]rune(src), "")
	b.resolveLexer()
	if b.hl.lang != "JSON" || b.text() != src || b.dirty() != true {
		t.Fatalf("lang %q, text changed: %q", b.hl.lang, b.text())
	}
	if len(b.undo) != 1 {
		t.Fatalf("formatting added an undo step to already formatted JSON: %d", len(b.undo))
	}
}
