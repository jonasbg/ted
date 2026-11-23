package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// Keep editing costs visible in bench output. Run with:
// go test -run '^$' -bench 'Benchmark(Buffer|GitStatus)' -benchmem
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

func BenchmarkBufferMaxCols(b *testing.B) {
	buf := benchmarkBuffer(100_000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		speedSink = buf.maxCols()
	}
}

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
