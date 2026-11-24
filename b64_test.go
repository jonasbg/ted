package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDecodeB64(t *testing.T) {
	cases := []struct {
		in   string
		want string
		ok   bool
	}{
		{"aGVsbG8gd29ybGQ=", "hello world", true},
		{"aGVsbG8gd29ybGQ", "hello world", true},        // unpadded
		{"eyJzdWIiOiIxMjM0In0", `{"sub":"1234"}`, true}, // a JWT segment
		{"function", "", false},
		{"deadbeefcafe", "", false},
		{"abc", "", false},
	}
	for _, c := range cases {
		got, _, _, ok := decodeB64(c.in)
		if ok != c.ok || got != c.want {
			t.Errorf("decodeB64(%q) = %q, %v", c.in, got, ok)
		}
	}
	if _, _, kind, ok := decodeB64("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg=="); ok || !strings.HasPrefix(kind, "image/png") {
		t.Errorf("png: kind %q ok %v", kind, ok)
	}
}

func TestB64Run(t *testing.T) {
	line := []rune(`token: "aGk=", x=1, jwt eyJh.eyJi.c2ln`)
	if s, e := b64Run(line, 10); string(line[s:e]) != "aGk=" {
		t.Errorf("quoted: %q", string(line[s:e]))
	}
	if s, e := b64Run(line, 30); string(line[s:e]) != "eyJi" {
		t.Errorf("jwt segment: %q", string(line[s:e]))
	}
}

func TestBase64InPlace(t *testing.T) {
	a := &App{}
	b := bufFrom("  secret: aGVsbG8gd29ybGQ= # note\n")
	b.indent = "  "
	if !a.toggleBase64(b, Pos{0, 12}) {
		t.Fatal("did not decode")
	}
	if got := b.text(); got != "  secret: hello world # note\n" {
		t.Fatalf("decoded: %q", got)
	}
	// typing before the span moves it; Cmd+click encodes the original back
	b.cur = Pos{0, 0}
	b.insert([]rune(">"), "")
	if !a.toggleBase64(b, Pos{0, 14}) {
		t.Fatal("did not encode")
	}
	if got := b.text(); got != ">  secret: aGVsbG8gd29ybGQ= # note\n" {
		t.Fatalf("encoded: %q", got)
	}
	if len(b.b64) != 0 {
		t.Fatal("span should be gone")
	}

	// edited text is encoded with the same alphabet and padding
	b = bufFrom("x aGVsbG8gdw")
	a.toggleBase64(b, Pos{0, 3})
	if b.text() != "x hello w" {
		t.Fatalf("raw decode: %q", b.text())
	}
	b.cur = Pos{0, 4}
	b.insert([]rune("!"), "") // inside the span
	a.toggleBase64(b, Pos{0, 3})
	if want := "x " + base64.RawStdEncoding.EncodeToString([]byte("he!llo w")); b.text() != want {
		t.Fatalf("re-encode: %q, want %q", b.text(), want)
	}

	// decoded JSON is shown structured and compacted again after edits
	b = bufFrom("  v: eyJhIjoxfQ==")
	b.indent = "  "
	a.toggleBase64(b, Pos{0, 6})
	if got := b.text(); got != "  v: {\n    \"a\": 1\n  }" {
		t.Fatalf("json decode: %q", got)
	}
	b.doUndo()
	if b.text() != "  v: eyJhIjoxfQ==" || len(b.b64) != 0 {
		t.Fatalf("undo: %q spans=%d", b.text(), len(b.b64))
	}
}

func TestMinifiedJSONView(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "min.json")
	raw := `{"a":[1,2],"b":{"c":true}}`
	os.WriteFile(p, []byte(raw), 0o644)
	b, err := loadBuffer(p)
	if err != nil {
		t.Fatal(err)
	}
	if !b.jsonView || !strings.Contains(b.text(), "\n  \"a\": [\n") || b.dirty() {
		t.Fatalf("view=%v dirty=%v\n%s", b.jsonView, b.dirty(), b.text())
	}
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != raw {
		t.Fatalf("unedited save rewrote the file: %q", data)
	}
	// a tool rewrites the file: the reload is structured too
	os.WriteFile(p, []byte(`{"a":[1,2,3],"b":{"c":true}}`), 0o644)
	if err := b.reloadFromDisk(); err != nil {
		t.Fatal(err)
	}
	if !b.jsonView || !strings.Contains(b.text(), "    3\n") || b.dirty() {
		t.Fatalf("reload: view=%v dirty=%v\n%s", b.jsonView, b.dirty(), b.text())
	}

	// hand-formatted files are left alone
	p2 := filepath.Join(dir, "hand.json")
	os.WriteFile(p2, []byte("{\n    \"a\": 1,\n    \"b\": [1, 2]\n}\n"), 0o644)
	b2, _ := loadBuffer(p2)
	if b2.jsonView {
		t.Fatal("hand-formatted JSON should not be reformatted")
	}
}

// The cases below come from review findings.

func TestB64TypingAtEndOfSpan(t *testing.T) {
	a := &App{}
	b := bufFrom("aGVsbG8gd29ybGQ=")
	a.toggleBase64(b, Pos{0, 2})
	b.cur = Pos{0, 11}
	b.backspace()
	b.insert([]rune("D!"), "type")
	a.toggleBase64(b, Pos{0, 2})
	if want := base64.StdEncoding.EncodeToString([]byte("hello worlD!")); b.text() != want {
		t.Fatalf("got %q, want %q", b.text(), want)
	}
}

func TestB64UndoRestoresSpans(t *testing.T) {
	a := &App{}
	orig := "key: aGVsbG8gd29ybGQ= end"
	b := bufFrom(orig)
	a.toggleBase64(b, Pos{0, 6})
	// delete through the end of the decoded text, then undo
	b.deleteRange(Pos{0, 11}, Pos{0, 20}, "")
	b.doUndo()
	if b.text() != "key: hello world end" || len(b.b64) != 1 || b.b64[0].c != (Pos{0, 16}) {
		t.Fatalf("after undo: %q %+v", b.text(), b.b64)
	}
	a.toggleBase64(b, Pos{0, 6})
	if b.text() != orig {
		t.Fatalf("encode after undo: %q", b.text())
	}
	// decode, undo, redo: the region is back and still encodes
	a.toggleBase64(b, Pos{0, 6})
	b.doUndo()
	b.doRedo()
	if len(b.b64) != 1 {
		t.Fatal("redo lost the decoded region")
	}
	a.toggleBase64(b, Pos{0, 6})
	if b.text() != orig {
		t.Fatalf("encode after redo: %q", b.text())
	}
	// decode, edit elsewhere, encode, undo: decoded and tracked again
	a.toggleBase64(b, Pos{0, 6})
	b.cur = Pos{0, 0}
	b.insert([]rune("# "), "")
	a.toggleBase64(b, Pos{0, 8})
	b.doUndo()
	if len(b.b64) != 1 || !strings.Contains(b.text(), "hello world") {
		t.Fatalf("undo of encode: %q %d", b.text(), len(b.b64))
	}
}

func TestB64KeyValueAndPaths(t *testing.T) {
	a := &App{}
	for _, c := range []struct{ in, want string }{
		{"TOKEN=aGVsbG8gd29ybGQ=", "TOKEN=hello world"},
		{"TOKEN=aGVsbG8gd29ybGQ", "TOKEN=hello world"},
		{"+aGVsbG8gd29ybGQ=", "+hello world"},
		{"a/b/aGVsbG8gd29ybGQ=", "a/b/hello world"},
	} {
		b := bufFrom(c.in)
		a.toggleBase64(b, Pos{0, len(c.in) - 3})
		if b.text() != c.want {
			t.Errorf("%q: got %q", c.in, b.text())
		}
	}
	b := bufFrom("TOKEN=aGVsbG8gd29ybGQ=")
	if a.toggleBase64(b, Pos{0, 1}) {
		t.Errorf("clicking the key decoded %q", b.text())
	}
}

func TestB64KeepsCRLF(t *testing.T) {
	a := &App{}
	enc := base64.StdEncoding.EncodeToString([]byte("a\r\nb\r\n"))
	b := bufFrom("v " + enc)
	a.toggleBase64(b, Pos{0, 3})
	b.cur = Pos{0, 3}
	b.insert([]rune("y"), "")
	a.toggleBase64(b, Pos{0, 2})
	if want := "v " + base64.StdEncoding.EncodeToString([]byte("ay\r\nb\r\n")); b.text() != want {
		t.Fatalf("got %q want %q", b.text(), want)
	}
}

func TestSaveEncodesDecodedBase64(t *testing.T) {
	a := &App{}
	p := filepath.Join(t.TempDir(), "s.yaml")
	orig := "data:\n  token: aGVsbG8gd29ybGQ=\n"
	os.WriteFile(p, []byte(orig), 0o644)
	b, _ := loadBuffer(p)
	a.toggleBase64(b, Pos{1, 10})
	if got := b.textEncoded(); got != orig {
		t.Fatalf("session text %q", got)
	}
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != orig {
		t.Fatalf("saved %q", data)
	}
}
