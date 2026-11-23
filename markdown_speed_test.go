package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
)

// TestMarkdownPreviewSpeed guards the preview's costs on a 300 KB document
// (run with -v to see the numbers). Limits are generous: they catch
// regressions like an uncached lexer lookup (relayout took 700 ms).
func TestMarkdownPreviewSpeed(t *testing.T) {
	if testing.Short() {
		t.Skip("speed check")
	}
	readme, _ := os.ReadFile("README.md")
	dir := t.TempDir()
	var sb strings.Builder
	for i := 0; i < 40; i++ {
		sb.Write(readme)
		sb.WriteString("\n" + mdSample)
	}
	p := filepath.Join(dir, "big.md")
	os.WriteFile(p, []byte(sb.String()), 0o644)
	h := newHarness(t, dir, p)
	b := h.a.buf()
	t.Logf("document: %d KB, %d lines", sb.Len()>>10, len(b.lines))

	t0 := time.Now()
	h.keys(press("V", key.ModShortcut, key.ModShift))
	t.Logf("open preview (parse + layout + first frames): %v, %d items", time.Since(t0), len(b.md.doc.items))

	// layout only, warm text cache
	gtx := h.lastGtx()
	t0 = time.Now()
	for i := 0; i < 5; i++ {
		h.a.layoutMarkdown(gtx, b, b.md.key.width, b.md.key.px)
	}
	relayout := time.Since(t0) / 5
	t.Logf("relayout (warm cache): %v", relayout)
	if relayout > 300*time.Millisecond { // 13 ms on an M-series Mac, 57 ms on CI
		t.Errorf("relayout took %v", relayout)
	}
	t.Logf("text cache entries: %d", len(h.a.tc.m))

	// scrolling frames
	n := 60
	t0 = time.Now()
	for i := 0; i < n; i++ {
		h.r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(1500, 700), Scroll: f32.Pt(0, 400)})
		h.frame()
	}
	frame := time.Since(t0) / time.Duration(n)
	t.Logf("scroll frame (incl. GPU): %v", frame)

	// the source editor for comparison
	h.keys(press(key.NameEscape))
	t0 = time.Now()
	for i := 0; i < n; i++ {
		h.r.Queue(pointer.Event{Kind: pointer.Scroll, Source: pointer.Mouse, Position: f32.Pt(1500, 700), Scroll: f32.Pt(0, 400)})
		h.frame()
	}
	source := time.Since(t0) / time.Duration(n)
	t.Logf("source editor scroll frame: %v", source)
	// Compared with the source editor on the same machine: CI runners
	// without a GPU render every frame slowly (24 ms for the source).
	if frame > 3*source+5*time.Millisecond {
		t.Errorf("scrolling the preview took %v per frame, the source %v", frame, source)
	}
}

func (h *harness) lastGtx() layout.Context {
	return layout.Context{Ops: new(op.Ops), Now: time.Now(), Metric: unit.Metric{PxPerDp: 2, PxPerSp: 2}, Constraints: layout.Exact(h.sz)}
}
