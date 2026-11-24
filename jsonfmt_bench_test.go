package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Costs of JSON formatting, the formatted view of minified files and base64
// decoding in place. Run with:
// go test -run '^$' -bench 'Benchmark(JSON|B64)' -benchmem .
//
// Anything a key press or a click triggers runs on the UI goroutine, so
// compare ns/op with a 16 ms frame; opening a file has roughly 200 ms.
var (
	jsonBenchSink     string
	jsonBenchIntSink  int
	jsonBenchBoolSink bool
)

// benchMinifiedJSON returns about size bytes of minified JSON on one line: an
// array of records with nesting, numbers, escapes, non-ASCII text and a
// base64 token, roughly what an API dump or a HAR file holds.
func benchMinifiedJSON(size int) string {
	var sb strings.Builder
	sb.Grow(size + 512)
	sb.WriteString(`{"version":3,"items":[`)
	for i := 0; sb.Len() < size; i++ {
		if i > 0 {
			sb.WriteByte(',')
		}
		fmt.Fprintf(&sb, `{"id":%d,"name":"user-%d","email":"user%d@example.com","active":%t,"score":%d.5e-2,`+
			`"tags":["alpha","beta","gamma"],"profile":{"age":%d,"city":"Tromsø","geo":[69.6492,18.9553],"empty":{}},`+
			`"token":"eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6InRlZCJ9","note":"blåbærsyltetøy \"quoted\" {not: [structure]}, ok"}`,
			i, i, i, i%2 == 0, i%1000, 20+i%50)
	}
	sb.WriteString(`]}`)
	return sb.String()
}

func benchSizeName(n int) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%dMB", n>>20)
	}
	return fmt.Sprintf("%dKB", n>>10)
}

// benchBufferFrom builds a buffer the way loadBuffer does, without the
// formatted view.
func benchBufferFrom(path, content string) *Buffer {
	b := newBuffer(path)
	b.setContent([]byte(content))
	return b
}

// BenchmarkJSONPretty is prettyJSON alone: validation plus the re-indenting
// scanner. Format, opening a minified file and every reload of one pay it.
func BenchmarkJSONPretty(b *testing.B) {
	for _, size := range []int{1 << 20, 10 << 20} {
		src := benchMinifiedJSON(size)
		b.Run("minified_"+benchSizeName(size), func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(src)))
			for i := 0; i < b.N; i++ {
				out, err := prettyJSON(src, "  ")
				if err != nil {
					b.Fatal(err)
				}
				jsonBenchSink = out
			}
		})
	}
	// Formatting a file that is formatted already ("Already formatted").
	src, err := prettyJSON(benchMinifiedJSON(10<<20), "  ")
	if err != nil {
		b.Fatal(err)
	}
	b.Run("formatted_"+benchSizeName(len(src)), func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(src)))
		for i := 0; i < b.N; i++ {
			out, err := prettyJSON(src, "  ")
			if err != nil {
				b.Fatal(err)
			}
			jsonBenchSink = out
		}
	})
}

// BenchmarkJSONPrettyParts splits prettyJSON's cost on 10 MB: the validation
// it does now (json.Indent into a buffer that is thrown away), the cheaper
// json.Valid, and what is left for the scanner.
func BenchmarkJSONPrettyParts(b *testing.B) {
	src := benchMinifiedJSON(10 << 20)
	b.Run("validate_Indent_10MB", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(src)))
		for i := 0; i < b.N; i++ {
			if err := json.Indent(&bytes.Buffer{}, []byte(src), "", "  "); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("validate_Valid_10MB", func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(src)))
		for i := 0; i < b.N; i++ {
			jsonBenchBoolSink = json.Valid([]byte(src))
		}
	})
}

// BenchmarkJSONStructured is the check every open and reload of a JSON file
// runs. A hand-formatted file pays for a full format before it is rejected.
func BenchmarkJSONStructured(b *testing.B) {
	min := benchMinifiedJSON(10 << 20)
	pretty, err := prettyJSON(min, "  ")
	if err != nil {
		b.Fatal(err)
	}
	for _, c := range []struct{ name, src string }{
		{"minified_10MB", min},
		{"formatted_" + benchSizeName(len(pretty)), pretty},
	} {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(c.src)))
			for i := 0; i < b.N; i++ {
				jsonBenchSink, jsonBenchBoolSink = structuredJSON("x.json", c.src, "  ")
			}
		})
	}
}

// BenchmarkJSONLoadBuffer opens the same minified bytes as .json (formatted
// view) and as .txt (plain load). The difference is what the view costs on
// open; the 1 MB pair shows how it scales.
func BenchmarkJSONLoadBuffer(b *testing.B) {
	dir := b.TempDir()
	for _, size := range []int{1 << 20, 10 << 20} {
		src := benchMinifiedJSON(size)
		for _, ext := range []string{".json", ".txt"} {
			p := filepath.Join(dir, "min"+benchSizeName(size)+ext)
			if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
				b.Fatal(err)
			}
			b.Run(strings.TrimPrefix(ext, ".")+"_"+benchSizeName(size), func(b *testing.B) {
				b.ReportAllocs()
				b.SetBytes(int64(len(src)))
				for i := 0; i < b.N; i++ {
					buf, err := loadBuffer(p)
					if err != nil {
						b.Fatal(err)
					}
					if buf.jsonView != (ext == ".json") {
						b.Fatalf("jsonView = %v for %s", buf.jsonView, ext)
					}
					jsonBenchIntSink = len(buf.lines)
				}
			})
		}
	}
	// A formatted file of the size the 10 MB view becomes: what opening it
	// would cost if it were stored that way (and the price of the check).
	pretty, err := prettyJSON(benchMinifiedJSON(10<<20), "  ")
	if err != nil {
		b.Fatal(err)
	}
	p := filepath.Join(dir, "pretty.json")
	if err := os.WriteFile(p, []byte(pretty), 0o644); err != nil {
		b.Fatal(err)
	}
	b.Run("json_formatted_"+benchSizeName(len(pretty)), func(b *testing.B) {
		b.ReportAllocs()
		b.SetBytes(int64(len(pretty)))
		for i := 0; i < b.N; i++ {
			buf, err := loadBuffer(p)
			if err != nil {
				b.Fatal(err)
			}
			jsonBenchIntSink = len(buf.lines)
		}
	})
}

// BenchmarkJSONReloadFromDisk is checkDisk's reload of a minified file shown
// formatted, after a tool changed one value in it. It runs on the UI
// goroutine each time the file changes.
func BenchmarkJSONReloadFromDisk(b *testing.B) {
	for _, size := range []int{1 << 20, 10 << 20} {
		src := benchMinifiedJSON(size)
		at := strings.Index(src[len(src)/2:], `"age":`) + len(src)/2 + len(`"age":`)
		variants := [2]string{src, src[:at] + "9" + src[at+1:]}
		p := filepath.Join(b.TempDir(), "min.json")
		if err := os.WriteFile(p, []byte(variants[0]), 0o644); err != nil {
			b.Fatal(err)
		}
		b.Run("one_value_changed_"+benchSizeName(size), func(b *testing.B) {
			buf, err := loadBuffer(p)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				if err := os.WriteFile(p, []byte(variants[(i+1)%2]), 0o644); err != nil {
					b.Fatal(err)
				}
				buf.undo, buf.redo, buf.savedID = nil, nil, 0
				b.StartTimer()
				if err := buf.reloadFromDisk(); err != nil {
					b.Fatal(err)
				}
			}
			if !buf.jsonView || buf.dirty() {
				b.Fatalf("view=%v dirty=%v", buf.jsonView, buf.dirty())
			}
		})
	}
}

// BenchmarkJSONReplaceAllMinimal is the buffer edit under Format and reload:
// everything differs (minified to formatted), one value differs in a
// formatted buffer, and nothing differs.
func BenchmarkJSONReplaceAllMinimal(b *testing.B) {
	min := benchMinifiedJSON(10 << 20)
	pretty, err := prettyJSON(min, "  ")
	if err != nil {
		b.Fatal(err)
	}

	b.Run("minified_to_formatted_10MB", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			buf := benchBufferFrom("x.json", min)
			b.StartTimer()
			if !buf.replaceAllMinimal(pretty) {
				b.Fatal("nothing replaced")
			}
		}
	})

	at := strings.Index(pretty[len(pretty)/2:], `"age": `) + len(pretty)/2 + len(`"age": `)
	variants := [2]string{pretty, pretty[:at] + "9" + pretty[at+1:]}
	b.Run("one_value_in_"+benchSizeName(len(pretty)), func(b *testing.B) {
		buf := benchBufferFrom("x.json", pretty)
		buf.maxCols()
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if !buf.replaceAllMinimal(variants[(i+1)%2]) {
				b.Fatal("nothing replaced")
			}
			buf.undo = buf.undo[:0]
		}
	})

	b.Run("unchanged_"+benchSizeName(len(pretty)), func(b *testing.B) {
		buf := benchBufferFrom("x.json", pretty)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if buf.replaceAllMinimal(pretty) {
				b.Fatal("replaced an identical buffer")
			}
		}
	})
}

// BenchmarkJSONFormat is Alt+Shift+F end to end: text(), prettyJSON and
// replaceAllMinimal.
func BenchmarkJSONFormat(b *testing.B) {
	a := &App{}
	for _, size := range []int{1 << 20, 10 << 20} {
		min := benchMinifiedJSON(size)
		b.Run("minified_"+benchSizeName(size), func(b *testing.B) {
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				b.StopTimer()
				buf := benchBufferFrom("x.json", min)
				b.StartTimer()
				a.formatJSON(buf)
				if len(buf.lines) < 100 {
					b.Fatalf("not formatted: %s", a.msg)
				}
			}
		})
	}
	pretty, err := prettyJSON(benchMinifiedJSON(10<<20), "  ")
	if err != nil {
		b.Fatal(err)
	}
	// The key pressed on a file that needs nothing.
	b.Run("already_formatted_"+benchSizeName(len(pretty)), func(b *testing.B) {
		buf := benchBufferFrom("x.json", pretty)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			a.formatJSON(buf)
		}
		if len(buf.undo) != 0 {
			b.Fatal("formatting a formatted buffer made an edit")
		}
	})
	// One value pasted minified into a formatted file, then Format.
	at := strings.Index(pretty[len(pretty)/2:], `"geo": [`) + len(pretty)/2
	end := at + strings.Index(pretty[at:], "]") + 1
	touched := pretty[:at] + `"geo":[69.6492,18.9553]` + pretty[end:]
	b.Run("one_value_in_"+benchSizeName(len(pretty)), func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			b.StopTimer()
			buf := benchBufferFrom("x.json", touched)
			b.StartTimer()
			a.formatJSON(buf)
			if len(buf.undo) != 1 {
				b.Fatalf("expected one undo step: %s", a.msg)
			}
		}
	})
}

// benchB64Inputs are what a Cmd/Ctrl+click can land on.
func benchB64Inputs() []struct{ name, in string } {
	text := strings.Repeat("The quick brown fox jumps over the lazy dog. Blåbærsyltetøy.\n", 100<<10/62)
	doc := benchMinifiedJSON(100 << 10)
	bin := make([]byte, 100<<10)
	copy(bin, "\x89PNG\r\n\x1a\n")
	for i := 8; i < len(bin); i++ {
		bin[i] = byte(i * 31)
	}
	junk := make([]byte, 100<<10)
	for i := range junk {
		junk[i] = byte(i*131 + 7)
	}
	return []struct{ name, in string }{
		{"word_not_base64", "function"},
		{"short_text", "aGVsbG8gd29ybGQ="},
		{"jwt_segment", "eyJzdWIiOiIxMjM0NTY3ODkwIiwibmFtZSI6InRlZCIsImlhdCI6MTUxNjIzOTAyMn0"},
		{"text_100KB", base64.StdEncoding.EncodeToString([]byte(text))},
		{"json_100KB_rawurl", base64.RawURLEncoding.EncodeToString([]byte(doc))},
		{"png_100KB", base64.StdEncoding.EncodeToString(bin)},
		{"binary_100KB_unknown", base64.StdEncoding.EncodeToString(junk)},
	}
}

// BenchmarkB64Decode is decodeB64 alone, the test every Cmd/Ctrl+click runs.
func BenchmarkB64Decode(b *testing.B) {
	for _, c := range benchB64Inputs() {
		b.Run(c.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(c.in)))
			for i := 0; i < b.N; i++ {
				jsonBenchSink, _, _, jsonBenchBoolSink = decodeB64(c.in)
			}
		})
	}
}

// BenchmarkB64Toggle is one click end to end: find the run, decode, format
// decoded JSON, edit the buffer. The click that encodes it back is outside
// the timer (it is an undo).
func BenchmarkB64Toggle(b *testing.B) {
	a := &App{}
	for _, c := range benchB64Inputs()[1:5] {
		b.Run(c.name, func(b *testing.B) {
			buf := benchBufferFrom("x.yaml", "config:\n  token: "+c.in+" # note\nend: true\n")
			p := Pos{1, 12}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if !a.toggleBase64(buf, p) || len(buf.b64) != 1 {
					b.Fatal("did not decode")
				}
				b.StopTimer()
				if !a.toggleBase64(buf, p) || len(buf.b64) != 0 {
					b.Fatal("did not encode")
				}
				buf.redo = nil
				b.StartTimer()
			}
		})
	}
}

// benchSpanBuffer is a 100 000 line buffer with n base64 values decoded in
// place, spread from the middle on, so edits above them move every span.
func benchSpanBuffer(b *testing.B, n int) *Buffer {
	var sb strings.Builder
	for i := 0; i < 100_000; i++ {
		if i >= 60_000 && i < 60_000+n {
			sb.WriteString("  token: aGVsbG8gd29ybGQ= # note\n")
		} else {
			sb.WriteString("func example(a int) string { return \"value\" }\n")
		}
	}
	buf := benchBufferFrom("", sb.String())
	buf.maxCols()
	a := &App{}
	for i := 0; i < n; i++ {
		if !a.toggleBase64(buf, Pos{60_000 + i, 12}) {
			b.Fatal("did not decode")
		}
	}
	if len(buf.b64) != n {
		b.Fatalf("%d spans, want %d", len(buf.b64), n)
	}
	return buf
}

// BenchmarkB64InsertWithSpans is a key press (insert, then undo to keep the
// buffer the same size) with 0, 50 and 1000 decoded spans to keep in place.
// Compare with BenchmarkBufferTypeAtMiddle.
func BenchmarkB64InsertWithSpans(b *testing.B) {
	for _, n := range []int{0, 50, 1000} {
		b.Run(fmt.Sprintf("char_%d_spans", n), func(b *testing.B) {
			buf := benchSpanBuffer(b, n)
			text := []rune("x")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buf.cur = Pos{50_000, 0}
				buf.insert(text, "")
				buf.doUndo()
			}
			if len(buf.b64) != n {
				b.Fatalf("%d spans left, want %d", len(buf.b64), n)
			}
		})
	}
	// Enter moves spans to other lines, and typing inside a span grows it.
	for _, n := range []int{0, 50} {
		b.Run(fmt.Sprintf("newline_%d_spans", n), func(b *testing.B) {
			buf := benchSpanBuffer(b, n)
			text := []rune("\n")
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				buf.cur = Pos{50_000, 0}
				buf.insert(text, "")
				buf.doUndo()
			}
			if len(buf.b64) != n {
				b.Fatalf("%d spans left, want %d", len(buf.b64), n)
			}
		})
	}
	b.Run("char_inside_span_50_spans", func(b *testing.B) {
		buf := benchSpanBuffer(b, 50)
		text := []rune("x")
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			buf.cur = Pos{60_010, 12}
			buf.insert(text, "")
			buf.doUndo()
		}
		if len(buf.b64) != 50 {
			b.Fatalf("%d spans left", len(buf.b64))
		}
	})
}
