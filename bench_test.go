package main

import (
	"os"
	"testing"
	"time"
)

func TestBigFileTiming(t *testing.T) {
	p := os.Getenv("TED_BIG")
	if p == "" {
		t.Skip()
	}
	t0 := time.Now()
	b, err := loadBuffer(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("load %d lines: %v", len(b.lines), time.Since(t0))
	b.resolveLexer()
	t0 = time.Now()
	sp := computeSpans(b.hl.lexer, newTheme("catppuccin-mocha"), b.text())
	t.Logf("highlight: %v (%d lines)", time.Since(t0), len(sp))
	t0 = time.Now()
	b.cur = Pos{len(b.lines) / 2, 0}
	for i := 0; i < 100; i++ {
		b.insert([]rune("x"), "type")
	}
	b.insert([]rune("\n"), "")
	t.Logf("101 edits mid-file: %v", time.Since(t0))
	t0 = time.Now()
	_ = b.text()
	t.Logf("text(): %v", time.Since(t0))
}
