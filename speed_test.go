package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Keep editing costs visible in bench output. Run with:
// go test -run '^$' -bench 'Benchmark(Buffer|Session|CSV|GitStatus)' -benchmem
var speedSink int
var speedTextSink string

func benchmarkBuffer(lines int) *Buffer {
	line := []byte("func example(a int) string { return \"value\" }\n")
	data := make([]byte, 0, lines*len(line))
	for i := 0; i < lines; i++ {
		data = append(data, line...)
	}
	b := newBuffer("")
	b.setContent(data)
	b.maxCols()
	return b
}

// BenchmarkBufferMaxCols is the first-display cost: measuring every line.
func BenchmarkBufferMaxCols(b *testing.B) {
	buf := benchmarkBuffer(100_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.lineWidths, buf.widthCount = nil, nil
		speedSink = buf.maxCols()
	}
}

// Every line has the same width, so each edit touches a widest line.
func BenchmarkBufferTypeAtMiddle(b *testing.B) {
	buf := benchmarkBuffer(100_000)
	text := []rune("x")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.cur = Pos{len(buf.lines) / 2, 0}
		buf.insert(text, "")
		buf.doUndo()
	}
}

func BenchmarkBufferTypeIntoLongFile(b *testing.B) {
	buf := benchmarkBuffer(150_000)
	text := []rune("x")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.cur = Pos{len(buf.lines) / 2, 0}
		buf.insert(text, "")
		buf.doUndo()
	}
}

func BenchmarkBufferText(b *testing.B) {
	buf := benchmarkBuffer(100_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		speedTextSink = buf.text()
	}
}

// One line is the unique widest; typing into it and undoing grows and
// shrinks the maximum.
func BenchmarkBufferEditUniqueWidestLine(b *testing.B) {
	buf := benchmarkBuffer(100_000)
	buf.cur = Pos{10, 0}
	buf.insert([]rune("xxxxxxxxxxxxxxxxxxxxxxxx"), "")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.cur = Pos{10, 0}
		buf.insert([]rune("y"), "")
		buf.doUndo()
	}
}

func BenchmarkBufferEnterAtMiddle(b *testing.B) {
	buf := benchmarkBuffer(100_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.cur = Pos{len(buf.lines) / 2, 3}
		buf.insert([]rune("\n"), "")
		buf.doUndo()
	}
}

func BenchmarkBufferTextUnicode(b *testing.B) {
	line := []byte("// café 世界 — naïve résumé, emoji 🚀 and ASCII tail text\n")
	data := make([]byte, 0, 100_000*len(line))
	for i := 0; i < 100_000; i++ {
		data = append(data, line...)
	}
	buf := newBuffer("")
	buf.setContent(data)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		speedTextSink = buf.text()
	}
}

// The UI-goroutine part of an autosave with a large unsaved file.
func BenchmarkSessionSnapshotDirty100k(b *testing.B) {
	a := &App{bufs: []*Buffer{benchmarkBuffer(100_000)}}
	a.bufs[0].cur = Pos{1, 0}
	a.bufs[0].insert([]rune("x"), "")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, s := a.sessionSnapshot()
		speedSink = len(s.Tabs)
	}
}

func benchmarkCSV(rows int) *Buffer {
	data := []byte("id,name,price,note\n")
	for i := 0; i < rows; i++ {
		data = fmt.Appendf(data, "%d,item %d,%d.%02d,\"quoted, text\"\n", i, i, i%1000, i%100)
	}
	buf := newBuffer("bench.csv")
	buf.setContent(data)
	buf.setCSV(',')
	return buf
}

// One frame's worth of rainbow spans.
func BenchmarkCSVSpansFrame(b *testing.B) {
	a := &App{th: newTheme("github")}
	buf := benchmarkCSV(1000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		for li := 0; li < 60; li++ {
			speedSink += len(a.csvSpans(buf, li))
		}
	}
}

// Stats over a whole selected column, recomputed each time.
func BenchmarkCSVColumnStats100k(b *testing.B) {
	buf := benchmarkCSV(100_000)
	buf.startBlock(Pos{0, 13}, false) // header of "price": whole column
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		buf.csv.statsOK = false
		st, _ := buf.selectionStats()
		speedSink = st.numbers
	}
}

// Stats for a selected column with no values: drawn every frame, so it must
// come from the cache.
func BenchmarkCSVEmptyColumnStatsPerFrame(b *testing.B) {
	data := []byte("a,empty\n")
	for i := 0; i < 100_000; i++ {
		data = fmt.Appendf(data, "%d,\n", i)
	}
	buf := newBuffer("bench.csv")
	buf.setContent(data)
	buf.setCSV(',')
	buf.startBlock(Pos{0, 3}, false)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, ok := buf.selectionStats()
		if ok {
			b.Fatal("empty column reported values")
		}
	}
}

func BenchmarkGitStatus10k(b *testing.B) {
	if _, err := exec.LookPath("git"); err != nil {
		b.Skip("git unavailable")
	}
	root := b.TempDir()
	for i := 0; i < 10_000; i++ {
		path := filepath.Join(root, fmt.Sprintf("pkg%02d/file%04d.go", i/1000, i%1000))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			b.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package example\n"), 0o600); err != nil {
			b.Fatal(err)
		}
	}
	run := func(args ...string) {
		b.Helper()
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			b.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	run("add", ".")
	run("-c", "user.name=Benchmark", "-c", "user.email=benchmark@example.invalid", "-c", "commit.gpgsign=false", "commit", "-qm", "fixture")
	if err := os.WriteFile(filepath.Join(root, "pkg00/file0000.go"), []byte("package changed\n"), 0o600); err != nil {
		b.Fatal(err)
	}
	b.ReportMetric(10_000, "files")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		status, changes := readGitStatus(root)
		speedSink = len(status) + len(changes)
	}
}
