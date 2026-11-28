package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"unicode/utf8"
	"unsafe"

	"github.com/alecthomas/chroma/v2/lexers"
)

// isJSON reports whether the buffer holds a JSON file, by extension or, for
// untitled buffers and odd names, by the highlighter's language.
func isJSON(b *Buffer) bool {
	if b == nil || b.virtualKey != "" || b.image != nil {
		return false
	}
	return isJSONPath(b.path) || strings.EqualFold(b.hl.lang, "JSON")
}

// prettyJSON re-indents src with indent per level, keeping key order and the
// exact spelling of strings and numbers. The result ends with one newline.
// encoding/json only validates here: its Indent unescapes \u sequences,
// which would rewrite strings the author spelled on purpose.
func prettyJSON(src, indent string) (string, error) {
	raw := unsafe.Slice(unsafe.StringData(src), len(src))
	if !json.Valid(raw) {
		// Indent only for its SyntaxError, which says where
		if err := json.Indent(&bytes.Buffer{}, raw, "", indent); err != nil {
			return "", err
		}
		return "", errors.New("invalid JSON")
	}
	var out strings.Builder
	out.Grow(len(src) + len(src)/4)
	depth := 0
	newline := func() {
		out.WriteByte('\n')
		for range depth {
			out.WriteString(indent)
		}
	}
	// nextSig returns the index of the next non-whitespace byte from i.
	nextSig := func(i int) int {
		for i < len(src) && (src[i] == ' ' || src[i] == '\t' || src[i] == '\n' || src[i] == '\r') {
			i++
		}
		return i
	}
	for i := 0; i < len(src); i++ {
		switch c := src[i]; c {
		case ' ', '\t', '\n', '\r':
		case '"':
			j := i + 1
			for ; j < len(src) && src[j] != '"'; j++ {
				if src[j] == '\\' {
					j++
				}
			}
			out.WriteString(src[i : j+1])
			i = j
		case '{', '[':
			out.WriteByte(c)
			if j := nextSig(i + 1); j < len(src) && (src[j] == '}' || src[j] == ']') {
				out.WriteByte(src[j]) // keep {} and [] on one line
				i = j
				continue
			}
			depth++
			newline()
		case '}', ']':
			depth--
			newline()
			out.WriteByte(c)
		case ',':
			out.WriteByte(',')
			newline()
		case ':':
			out.WriteString(": ")
		default:
			out.WriteByte(c)
		}
	}
	out.WriteByte('\n')
	return out.String(), nil
}

// formatJSON pretty-prints the buffer as one undoable edit that touches only
// what changed. On a syntax error it moves the caret to the fault and says
// what is wrong instead of changing anything.
func (a *App) formatJSON(b *Buffer) {
	if b == nil || b.readOnly {
		return
	}
	b.encodeAllSpans()
	src := b.text()
	if strings.TrimSpace(src) == "" {
		a.setMsg("Nothing to format")
		return
	}
	indent := b.indent
	if indent == "" {
		indent = "  "
	}
	out, err := prettyJSON(src, indent)
	if err != nil {
		if !isJSON(b) {
			a.setMsg("Format works on JSON; this file is not JSON")
			return
		}
		var se *json.SyntaxError
		if errors.As(err, &se) {
			p := b.posFromOffset(utf8.RuneCountInString(src[:max(0, min(int(se.Offset)-1, len(src)))]))
			b.cur, b.selecting = p, false
			a.follow = true
			a.setMsg("Not formatted: %s (line %d, column %d)", se.Error(), p.L+1, p.C+1)
			return
		}
		a.setMsg("Not formatted: %v", err)
		return
	}
	if !b.replaceAllMinimal(out) {
		a.setMsg("Already formatted")
		return
	}
	a.follow = true
	a.setMsg("Formatted JSON (%s to undo)", shortcutName()+"+Z")
}

// jsonCandidate reports whether a file may hold JSON worth checking: a JSON
// name, or a name no language claims (no extension, .txt, .log, .out, .tmp,
// untitled) with text that starts like JSON.
func jsonCandidate(path, s string) bool {
	if isJSONPath(path) {
		return true
	}
	if !unclaimedName(path) {
		return false
	}
	t := strings.TrimLeft(s, " \t\r\n")
	return t != "" && (t[0] == '{' || t[0] == '[')
}

func unclaimedName(path string) bool {
	if path == "" {
		return true
	}
	switch strings.ToLower(filepath.Ext(path)) {
	case ".txt", ".log", ".out", ".tmp", ".response", ".body":
		return true
	}
	return lexers.Match(filepath.Base(path)) == nil
}

// looksLikeJSON reports whether the buffer's text is a JSON object or array
// though its name does not say so: a scratch buffer, or a file such as
// "response" or "dump.txt".
func (b *Buffer) looksLikeJSON() bool {
	if b.image != nil || b.virtualKey != "" || isJSONPath(b.path) || !unclaimedName(b.path) {
		return false
	}
	first := ""
	for _, l := range b.lines {
		if t := strings.TrimSpace(string(l)); t != "" {
			first = t
			break
		}
	}
	if first == "" || first[0] != '{' && first[0] != '[' {
		return false
	}
	t := b.text()
	return len(t) <= 64<<20 && json.Valid([]byte(t))
}

// autoFormatJSON formats a scratch buffer just recognised as JSON, when the
// JSON was pasted or restored rather than typed, as one undoable edit.
// Files on disk get the formatted view on open instead.
func (b *Buffer) autoFormatJSON() {
	if b.path != "" || b.readOnly || b.hl.manual {
		return
	}
	if n := len(b.undo); n > 0 && (b.undo[n-1].kind == "type" || b.undo[n-1].kind == "del") {
		return
	}
	// the text's own indentation, else JSON's usual two spaces
	indent := detectIndent(b.lines, "untitled.json")
	if out, err := prettyJSON(b.text(), indent); err == nil {
		b.indent = indent
		b.replaceAllMinimal(out)
		b.cur, b.selecting = Pos{}, false
	}
}

func isJSONPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json", ".geojson", ".webmanifest", ".har", ".jsonld", ".topojson":
		return true
	}
	return false
}

// structuredJSON returns s formatted when it is minified JSON: valid, on
// at most three lines or with lines averaging 300 bytes or more, and
// formatting at least doubles its line count. Files laid out by hand (even
// compactly, or with another indent) are left as they are.
func structuredJSON(path, s, indent string) (string, bool) {
	if path == "" || !jsonCandidate(path, s) || len(s) > 64<<20 || strings.TrimSpace(s) == "" {
		return "", false
	}
	if lines := strings.Count(strings.TrimRight(s, "\r\n"), "\n") + 1; lines > 3 && len(s)/lines < 300 {
		return "", false
	}
	out, err := prettyJSON(s, indent)
	if err != nil || out == s {
		return "", false
	}
	if strings.Count(out, "\n") < 2*(strings.Count(strings.TrimRight(s, "\n"), "\n")+1) {
		return "", false
	}
	return out, true
}

// toggleJSONView switches a clean minified JSON file between the formatted
// view and the bytes on disk.
func (a *App) toggleJSONView(b *Buffer) {
	if b.dirty() {
		a.setMsg("Save or undo your edits first")
		return
	}
	if err := b.setJSONRaw(b.jsonView); err != nil {
		a.setErr("Can't read %s: %v", a.rel(b.path), err)
		return
	}
	b.cur, b.selecting = Pos{}, false
	b.vy, b.vx = 0, 0
	if b.jsonView {
		a.setMsg("Shown formatted; the file on disk stays minified unless you edit and save")
	} else {
		a.setMsg("Showing the file as it is on disk")
	}
}

// setJSONRaw shows a clean JSON file as it is on disk (raw) or formatted.
func (b *Buffer) setJSONRaw(raw bool) error {
	was := b.jsonRaw
	b.jsonRaw = raw
	if err := b.reloadFromDisk(); err != nil {
		b.jsonRaw = was
		return err
	}
	b.undo, b.redo, b.savedID = nil, nil, 0
	b.b64 = nil
	return nil
}

// diskLines switches a formatted view to the file as it is on disk, for
// line numbers that count lines on disk (search hits, file:line).
func (b *Buffer) diskLines() {
	if b.jsonView && !b.dirty() {
		b.setJSONRaw(true)
	}
}
