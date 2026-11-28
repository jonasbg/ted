package main

import (
	"bytes"
	"cmp"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// b64Span is base64 that was decoded in place. The buffer keeps its ends up
// to date as text is edited, so Cmd/Ctrl+click on it encodes it back.
type b64Span struct {
	a, c   Pos
	orig   string           // the base64 as it was written
	enc    *base64.Encoding // the alphabet and padding it used
	text   string           // what was inserted, to tell whether it was edited
	prefix string           // indentation added to continuation lines
	isJSON bool
	crlf   bool
	undoID int // the decode's undo step             // decoded JSON shown pretty; compact it again
}

func isB64Rune(r rune) bool {
	return r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z' || r >= '0' && r <= '9' ||
		r == '+' || r == '/' || r == '-' || r == '_' || r == '='
}

// b64Run returns the run of base64 characters at column c of line, without
// '=' except as trailing padding. A JWT's dots split it into segments.
func b64Run(line []rune, c int) (int, int) {
	if c >= len(line) || !isB64Rune(line[c]) {
		if c > 0 && c-1 < len(line) && isB64Rune(line[c-1]) {
			c--
		} else {
			return c, c
		}
	}
	s, e := c, c
	for s > 0 && isB64Rune(line[s-1]) {
		s--
	}
	for e < len(line) && isB64Rune(line[e]) {
		e++
	}
	// '=' is padding only at the end of a value; after it a new value
	// starts (KEY=value, a=b&c=d), so keep the part holding the click
	for i := s; i < e-1; i++ {
		if line[i] == '=' && line[i+1] != '=' {
			if c <= i {
				e = i + 1
				break
			}
			s = i + 1
		}
	}
	for s < e && line[s] == '=' {
		s++
	}
	return s, e
}

var b64Encodings = []*base64.Encoding{base64.StdEncoding, base64.URLEncoding, base64.RawStdEncoding, base64.RawURLEncoding}

// decodeB64 decodes s if it is base64 of readable text. kind names binary
// content (an image, a PDF) that can't be shown as text.
func decodeB64(s string) (text string, enc *base64.Encoding, kind string, ok bool) {
	if len(s) < 4 {
		return "", nil, "", false
	}
	for _, e := range b64Encodings {
		data, err := e.DecodeString(s)
		if err != nil || len(data) == 0 {
			continue
		}
		if readableText(data) {
			return string(data), e, "", true
		}
		if ct := http.DetectContentType(data); ct != "application/octet-stream" && !strings.HasPrefix(ct, "text/") {
			return "", e, fmt.Sprintf("%s, %s", strings.Split(ct, ";")[0], humanSize(int64(len(data)))), false
		}
	}
	return "", nil, "", false
}

// readableText reports whether data is UTF-8 text with no control
// characters other than line breaks and tabs.
func readableText(data []byte) bool {
	if !utf8.Valid(data) {
		return false
	}
	for _, r := range string(data) {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if r == utf8.RuneError || !unicode.IsPrint(r) && !unicode.IsSpace(r) {
			return false
		}
	}
	return true
}

// toggleBase64 decodes the base64 under p in place, or encodes a decoded
// span back. It reports whether it did anything.
func (a *App) toggleBase64(b *Buffer, p Pos) bool {
	for i, sp := range b.b64 {
		if !p.Less(sp.a) && !sp.c.Less(p) {
			a.encodeSpan(b, i)
			return true
		}
	}
	line := b.lines[p.L]
	var s, e int
	var orig, text, kind string
	var enc *base64.Encoding
	ok := false
	for _, cand := range b64Candidates(line, p.C) {
		s, e = cand[0], cand[1]
		orig = string(line[s:e])
		var k string
		if text, enc, k, ok = decodeB64(orig); ok {
			break
		}
		if kind == "" {
			kind = k
		}
	}
	if !ok {
		if kind != "" {
			a.setMsg("Base64 of %s: not text, so it stays encoded", kind)
			return true
		}
		return false
	}
	sp := b64Span{orig: orig, enc: enc, crlf: strings.Contains(text, "\r\n")}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	if b.readOnly {
		first, _, more := strings.Cut(text, "\n")
		if more {
			first += " …"
		}
		a.setMsg("Base64: %s", first)
		return true
	}
	if trimmed := strings.TrimSpace(text); len(trimmed) > 0 && (trimmed[0] == '{' || trimmed[0] == '[') && json.Valid([]byte(text)) {
		if pretty, err := prettyJSON(text, b.indent); err == nil {
			text = strings.TrimSuffix(pretty, "\n")
			sp.isJSON = true
		}
	}
	if strings.Contains(text, "\n") {
		sp.prefix = string(leadingWS(line))
		text = strings.ReplaceAll(text, "\n", "\n"+sp.prefix)
	}
	sp.text = text
	sp.a = Pos{p.L, s}
	end := b.replaceRange(sp.a, Pos{p.L, e}, []rune(text))
	b.breakUndo()
	sp.c = end
	sp.undoID = b.topID()
	b.b64 = append(b.b64, sp)
	b.cur, b.selecting = sp.a, false
	a.setMsg("Decoded base64 (%s+click encodes it again)", shortcutName())
	return true
}

// b64Candidates lists runs to try at column c: the whole base64 run, then
// the part between '+' or '/' (a diff's "+", a path) holding the click.
func b64Candidates(line []rune, c int) [][2]int {
	s, e := b64Run(line, c)
	if e-s < 8 {
		return nil
	}
	out := [][2]int{{s, e}}
	if c < s {
		c = s
	}
	ps, pe := c, c
	for ps > s && line[ps-1] != '+' && line[ps-1] != '/' {
		ps--
	}
	for pe < e && line[pe] != '+' && line[pe] != '/' {
		pe++
	}
	if (ps != s || pe != e) && pe-ps >= 8 {
		out = append(out, [2]int{ps, pe})
	}
	return out
}

// encoded returns text as base64 for span sp: the original string when
// the text is unedited, else the edits in the same alphabet and padding.
func (sp b64Span) encoded(text string) (string, bool) {
	if text == sp.text {
		return sp.orig, false
	}
	if sp.prefix != "" {
		text = strings.ReplaceAll(text, "\n"+sp.prefix, "\n")
	}
	if sp.isJSON {
		var buf bytes.Buffer
		if json.Compact(&buf, []byte(text)) == nil {
			text = buf.String()
		}
	}
	if sp.crlf {
		text = strings.ReplaceAll(text, "\n", "\r\n")
	}
	return sp.enc.EncodeToString([]byte(text)), true
}

// encodeSpan turns decoded span i back into base64.
func (a *App) encodeSpan(b *Buffer, i int) {
	sp := b.b64[i]
	out, edited := sp.encoded(string(b.textRange(sp.a, sp.c)))
	if !edited && b.topID() == sp.undoID {
		// nothing happened since the decode: step back, so a clean file
		// is clean again
		b.doUndo()
	} else {
		b.inB64Encode = true
		b.replaceRange(sp.a, sp.c, []rune(out)) // deleting the span drops it
		b.inB64Encode = false
		b.breakUndo()
	}
	b.cur, b.selecting = sp.a, false
	if edited {
		a.setMsg("Encoded your edits as base64")
	} else {
		a.setMsg("Encoded as base64 again (unchanged)")
	}
}

// encodeAllSpans encodes every decoded region back, as one undo step, so
// a save never writes decoded text. It reports whether there were any.
func (b *Buffer) encodeAllSpans() bool {
	if len(b.b64) == 0 || b.readOnly {
		return false
	}
	n, cur := len(b.undo), b.cur
	b.inB64Encode = true
	defer func() { b.inB64Encode = false }()
	for range len(b.b64) {
		if len(b.b64) == 0 {
			break
		}
		i := 0 // last region first, so earlier positions stay put
		for j := range b.b64 {
			if b.b64[i].a.Less(b.b64[j].a) {
				i = j
			}
		}
		sp := b.b64[i]
		out, _ := sp.encoded(string(b.textRange(sp.a, sp.c)))
		b.replaceRange(sp.a, sp.c, []rune(out))
	}
	b.b64 = nil
	b.groupSince(n)
	b.cur = b.clamp(cur)
	return true
}

// textEncoded is the text with decoded regions encoded back, without
// changing the buffer. The hot-exit session stores this.
func (b *Buffer) textEncoded() string {
	if len(b.b64) == 0 {
		return b.text()
	}
	spans := slices.Clone(b.b64)
	slices.SortFunc(spans, func(x, y b64Span) int { return cmp.Compare(b.offsetOf(y.a), b.offsetOf(x.a)) })
	t := []rune(b.text())
	for _, sp := range spans {
		ao, co := b.offsetOf(sp.a), b.offsetOf(sp.c)
		out, _ := sp.encoded(string(t[ao:co]))
		t = slices.Replace(t, ao, co, []rune(out)...)
	}
	return string(t)
}

// shiftB64Insert moves decoded spans for text inserted at p ending at e.
// Typing inside a span or at its end grows it; typing at its start does not.
func (b *Buffer) shiftB64Insert(p, e Pos) {
	if len(b.b64) == 0 {
		return
	}
	mv := func(q Pos) Pos {
		switch {
		case q.L == p.L && q.C >= p.C:
			return Pos{e.L, e.C + q.C - p.C}
		case q.L > p.L:
			return Pos{q.L + e.L - p.L, q.C}
		}
		return q
	}
	for i := range b.b64 {
		sp := &b.b64[i]
		if b.inB64Encode && sp.c == p {
			// Re-encoding a span immediately after sp must not grow sp into it.
			continue
		}
		if !sp.c.Less(p) {
			sp.c = mv(sp.c)
			if !sp.a.Less(p) {
				sp.a = mv(sp.a)
			}
		}
	}
}

// shiftB64Delete moves decoded spans for the deletion of a..c, dropping
// spans that are deleted whole.
func (b *Buffer) shiftB64Delete(a, c Pos) {
	mv := func(q Pos) Pos {
		switch {
		case !a.Less(q):
			return q
		case !q.Less(c):
			if q.L == c.L {
				return Pos{a.L, a.C + q.C - c.C}
			}
			return Pos{q.L - (c.L - a.L), q.C}
		}
		return a
	}
	kept := b.b64[:0]
	for _, sp := range b.b64 {
		sp.a, sp.c = mv(sp.a), mv(sp.c)
		if sp.a != sp.c {
			kept = append(kept, sp)
		}
	}
	b.b64 = kept
}
