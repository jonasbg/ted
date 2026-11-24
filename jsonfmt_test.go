package main

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/io/key"
)

func TestPrettyJSON(t *testing.T) {
	got, err := prettyJSON("{\"b\":1,\"a\":[1, 2,{}],\"s\":\"\\u00e6 ø\",\"e\":[] ,\"n\":1.50}", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"b\": 1,\n  \"a\": [\n    1,\n    2,\n    {}\n  ],\n  \"s\": \"\\u00e6 ø\",\n  \"e\": [],\n  \"n\": 1.50\n}\n"
	if got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	if _, err := prettyJSON(`{"a":1,}`, "  "); err == nil {
		t.Fatal("trailing comma should be an error")
	}
}

func TestFormatJSONBuffer(t *testing.T) {
	a := &App{}
	b := bufFrom("{\"x\": {\"y\": true}}\n")
	b.path = "/tmp/x.json"
	b.indent = "    "
	a.formatJSON(b)
	want := "{\n    \"x\": {\n        \"y\": true\n    }\n}\n"
	if got := b.text(); got != want {
		t.Fatalf("got %q", got)
	}
	b.doUndo()
	if got := b.text(); got != "{\"x\": {\"y\": true}}\n" {
		t.Fatalf("undo should restore in one step, got %q", got)
	}

	bad := bufFrom("{\n  \"a\": 1\n  \"b\": 2\n}\n")
	bad.path = "/tmp/bad.json"
	a.formatJSON(bad)
	if bad.text() != "{\n  \"a\": 1\n  \"b\": 2\n}\n" {
		t.Fatal("invalid JSON must not be changed")
	}
	if bad.cur.L != 2 {
		t.Fatalf("caret should move to the error line, got %v", bad.cur)
	}
}

func TestPrettyJSONKeepsStrings(t *testing.T) {
	in := "{\"a\":\"\\u00e6 \\/ \\u2028 x\\\\\\\"y\\\\\",\"b\":[ ],\"big\":12345678901234567890123}"
	got, err := prettyJSON(in, "\t")
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n\t\"a\": \"\\u00e6 \\/ \\u2028 x\\\\\\\"y\\\\\",\n\t\"b\": [],\n\t\"big\": 12345678901234567890123\n}\n"
	if got != want {
		t.Fatalf("got %q\nwant %q", got, want)
	}
	again, _ := prettyJSON(got, "\t")
	if again != got {
		t.Fatal("formatting should be idempotent")
	}
	if got, _ := prettyJSON(" 42 ", "  "); got != "42\n" {
		t.Fatalf("scalar: %q", got)
	}
}

func TestFormatJSONInUI(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "data.json")
	os.WriteFile(p, []byte("{\"name\":\"ted\",\n\"tags\":\n[\"go\",\n\"gio\"]}"), 0o644)
	h := newHarness(t, dir, p)
	h.frame()
	var r image.Rectangle
	for _, it := range h.a.ui.statusItems {
		if it.id == "fmt" {
			r = it.r
		}
	}
	if r.Empty() {
		t.Fatal("no Format item in the status bar for a JSON file")
	}
	h.click(r.Min.X+4, r.Min.Y+4)
	want := "{\n  \"name\": \"ted\",\n  \"tags\": [\n    \"go\",\n    \"gio\"\n  ]\n}\n"
	if got := h.a.buf().text(); got != want {
		t.Fatalf("status bar Format: %q", got)
	}
	h.keys(press("Z", key.ModShortcut))
	if h.a.buf().dirty() {
		t.Fatal("undo should leave the file clean")
	}
	h.keys(press("F", key.ModAlt, key.ModShift))
	h.settle()
	h.shot("json-format")
	if got := h.a.buf().text(); got != want {
		t.Fatalf("Alt+Shift+F: %q", got)
	}
}

func TestMinifiedJSONViewInUI(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "min.json")
	os.WriteFile(p, []byte(`{"token":"eyJzdWIiOiJ0ZWQifQ","n":1}`), 0o644)
	h := newHarness(t, dir, p)
	h.frame()
	b := h.a.buf()
	if !b.jsonView || b.dirty() {
		t.Fatalf("view=%v dirty=%v", b.jsonView, b.dirty())
	}
	label := ""
	for _, it := range h.a.ui.statusItems {
		if it.id == "jsonview" {
			label = "found"
		}
	}
	if label == "" {
		t.Fatal("no Formatted view item")
	}
	// Cmd/Ctrl+click the token decodes it in place
	if !h.a.toggleBase64(b, Pos{1, 14}) {
		t.Fatalf("no decode on %q", string(b.lines[1]))
	}
	h.settle()
	h.shot("b64-inline")
	if !strings.Contains(b.text(), `"sub": "ted"`) {
		t.Fatalf("decoded:\n%s", b.text())
	}
	h.a.toggleBase64(b, Pos{2, 6})
	if strings.Contains(b.text(), "sub") || b.dirty() {
		t.Fatalf("encode back: dirty=%v\n%s", b.dirty(), b.text())
	}
	// the status bar switches to the bytes on disk and back
	h.a.toggleJSONView(b)
	if b.jsonView || len(b.lines) != 1 {
		t.Fatalf("raw view: %d lines", len(b.lines))
	}
	h.a.toggleJSONView(b)
	if !b.jsonView || len(b.lines) < 3 {
		t.Fatal("back to formatted view")
	}
}

func TestJSONViewReviewCases(t *testing.T) {
	dir := t.TempDir()
	// compact hand layout is not minified
	geo := filepath.Join(dir, "a.geojson")
	os.WriteFile(geo, []byte("{\"type\": \"Feature\",\n \"geometry\": {\"type\": \"LineString\",\n  \"coordinates\": [[1, 2], [3, 4], [5, 6]]},\n \"properties\": {\"name\": \"x\"}\n}\n"), 0o644)
	if b, _ := loadBuffer(geo); b.jsonView {
		t.Error("hand-laid-out GeoJSON shown as minified")
	}
	// an unedited view whose file was deleted is written on save
	p := filepath.Join(dir, "m.json")
	os.WriteFile(p, []byte(`{"a":[1,2,3]}`), 0o644)
	b, _ := loadBuffer(p)
	os.Remove(p)
	b.diskGone = true
	if err := b.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); err != nil {
		t.Error("save did not recreate the deleted file")
	}
	// disk line numbers switch to the bytes on disk
	os.WriteFile(p, []byte(`{"a":[1,2,3]}`), 0o644)
	b, _ = loadBuffer(p)
	b.diskLines()
	if b.jsonView || len(b.lines) != 1 || b.dirty() {
		t.Errorf("diskLines: view=%v lines=%d", b.jsonView, len(b.lines))
	}
}

func TestOptionChordTypesNothing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "x.json")
	os.WriteFile(p, []byte("{\"a\":1,\n\"b\":2,\n\"c\":3,\n\"d\":4}\n"), 0o644)
	h := newHarness(t, dir, p)
	h.frame()
	// what macOS sends for Option+Shift+F in one go: the text, then the key
	h.r.Queue(key.EditEvent{Range: key.Range{Start: 0, End: 0}, Text: "Ï"}, press("F", key.ModAlt, key.ModShift))
	h.frame()
	h.settle()
	if got := h.a.buf().text(); got != "{\n  \"a\": 1,\n  \"b\": 2,\n  \"c\": 3,\n  \"d\": 4\n}\n" {
		t.Fatalf("got %q", got)
	}
	// ordinary typing still goes through
	h.r.Queue(key.EditEvent{Range: key.Range{Start: 0, End: 0}, Text: "x"}, press("X"))
	h.frame()
	h.settle()
	if !strings.HasPrefix(h.a.buf().text(), "x{") {
		t.Fatalf("typing lost: %q", h.a.buf().text()[:5])
	}
}
