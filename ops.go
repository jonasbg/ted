package main

// Editing operations on a Buffer. These are UI-independent so the GUI only
// translates input into calls here.

import (
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/mattn/go-runewidth"
)

const tabW = 4

func runeW(r rune) int {
	if r < 32 {
		return 1
	}
	w := runewidth.RuneWidth(r)
	if w < 1 {
		return 1
	}
	return w
}

// visCol is the grid column of rune index c in line.
func visCol(line []rune, c int) int {
	vc := 0
	for i := 0; i < c && i < len(line); i++ {
		if line[i] == '\t' {
			vc += tabW - vc%tabW
		} else {
			vc += runeW(line[i])
		}
	}
	return vc
}

// colFromVis is the rune index closest to grid column vc.
func colFromVis(line []rune, vc int) int {
	x := 0
	for i, r := range line {
		w := runeW(r)
		if r == '\t' {
			w = tabW - x%tabW
		}
		if x+w > vc {
			if vc-x >= (w+1)/2 && w > 1 {
				return i + 1
			}
			return i
		}
		x += w
	}
	return len(line)
}

// colFromVisRound is like colFromVis but rounds to the nearest boundary,
// which is what a mouse click wants. vc is fractional.
func colFromVisRound(line []rune, vc float32) int {
	x := 0
	for i, r := range line {
		w := runeW(r)
		if r == '\t' {
			w = tabW - x%tabW
		}
		if float32(x)+float32(w)/2 > vc {
			return i
		}
		x += w
	}
	return len(line)
}

var pairs = map[rune]rune{'(': ')', '[': ']', '{': '}'}
var closers = map[rune]bool{')': true, ']': true, '}': true}
var quotes = map[rune]bool{'"': true, '\'': true, '`': true}

func (b *Buffer) typeText(s string) {
	rs := []rune(s)
	if len(rs) == 1 {
		b.typeRune(rs[0])
		return
	}
	b.insert(rs, "type")
}

func (b *Buffer) typeRune(r rune) {
	line := b.lines[b.cur.L]
	// Wrap a selection in brackets/quotes.
	if b.hasSel() {
		if cl, ok := pairs[r]; ok || quotes[r] {
			if !ok {
				cl = r
			}
			s, e := b.selRange()
			n := len(b.undo)
			b.selecting = false
			b.cur = e
			b.insert([]rune{cl}, "")
			b.cur = s
			b.insert([]rune{r}, "")
			b.groupSince(n)
			b.anchor = Pos{s.L, s.C + 1}
			if e.L == s.L {
				e.C++
			}
			b.cur = e
			b.selecting = true
			return
		}
	}
	// Type over an auto-inserted closer or quote.
	if (closers[r] || quotes[r]) && !b.hasSel() && b.cur.C < len(line) && line[b.cur.C] == r {
		b.cur.C++
		return
	}
	next := rune(0)
	if b.cur.C < len(line) {
		next = line[b.cur.C]
	}
	free := next == 0 || unicode.IsSpace(next) || closers[next] || next == ',' || next == ';'
	if cl, ok := pairs[r]; ok && !b.hasSel() && free {
		b.insert([]rune{r, cl}, "type")
		b.cur.C--
		return
	}
	if quotes[r] && !b.hasSel() && free {
		prev := rune(0)
		if b.cur.C > 0 {
			prev = line[b.cur.C-1]
		}
		if prev == 0 || unicode.IsSpace(prev) || pairs[prev] != 0 || prev == '=' || prev == ',' || prev == ':' {
			b.insert([]rune{r, r}, "type")
			b.cur.C--
			return
		}
	}
	b.insert([]rune{r}, "type")
	if closers[r] {
		l := b.lines[b.cur.L]
		if len(leadingWS(l)) == len(l)-1 && b.cur.L > 0 && b.cur.C == len(l) {
			b.alignCloser()
		}
	}
}

// alignCloser matches the indentation of a lone closing bracket to its opener.
func (b *Buffer) alignCloser() {
	if p, ok := matchBracket(b, Pos{b.cur.L, b.cur.C - 1}); ok {
		want := leadingWS(b.lines[p.L])
		have := leadingWS(b.lines[b.cur.L])
		if string(want) != string(have) {
			b.replaceRange(Pos{b.cur.L, 0}, Pos{b.cur.L, len(have)}, append([]rune(nil), want...))
			b.cur = Pos{b.cur.L, len(want) + 1}
		}
	}
}

func (b *Buffer) newline() {
	if b.hasSel() {
		b.deleteSel()
	}
	line := b.lines[b.cur.L]
	ind := append([]rune(nil), leadingWS(line)...)
	if len(ind) > b.cur.C {
		ind = ind[:b.cur.C]
	}
	prev := rune(0)
	for i := b.cur.C - 1; i >= 0; i-- {
		if !unicode.IsSpace(line[i]) {
			prev = line[i]
			break
		}
	}
	next := rune(0)
	if b.cur.C < len(line) {
		next = line[b.cur.C]
	}
	b.breakUndo()
	n := len(b.undo)
	opens := prev == '{' || prev == '(' || prev == '[' || (prev == ':' && strings.HasPrefix(b.hl.lang, "Python"))
	ws := b.cur.C
	for ws > len(ind) && (line[ws-1] == ' ' || line[ws-1] == '\t') {
		ws--
	}
	if ws < b.cur.C {
		b.deleteRange(Pos{b.cur.L, ws}, b.cur, "")
	}
	if opens {
		inner := append(append([]rune{'\n'}, ind...), []rune(b.indent)...)
		if closers[next] && pairs[prev] == next {
			b.insert(append(append(inner, '\n'), ind...), "")
			b.cur = Pos{b.cur.L - 1, len(ind) + len([]rune(b.indent))}
		} else {
			b.insert(inner, "")
		}
	} else {
		b.insert(append([]rune{'\n'}, ind...), "")
	}
	b.groupSince(n)
	b.breakUndo()
}

func (b *Buffer) backspace() {
	if b.hasSel() {
		b.deleteSel()
		return
	}
	if b.cur.C == 0 {
		if b.cur.L > 0 {
			b.deleteRange(Pos{b.cur.L - 1, len(b.lines[b.cur.L-1])}, b.cur, "del")
		}
		return
	}
	line := b.lines[b.cur.L]
	if b.cur.C < len(line) {
		prev, next := line[b.cur.C-1], line[b.cur.C]
		if (pairs[prev] != 0 && pairs[prev] == next) || (quotes[prev] && prev == next) {
			b.deleteRange(Pos{b.cur.L, b.cur.C - 1}, Pos{b.cur.L, b.cur.C + 1}, "del")
			return
		}
	}
	if b.indent != "\t" && len(leadingWS(line)) >= b.cur.C && line[b.cur.C-1] == ' ' {
		w := len(b.indent)
		vc := visCol(line, b.cur.C)
		target := ((vc - 1) / w) * w
		c := b.cur.C
		for c > 0 && line[c-1] == ' ' && visCol(line, c-1) >= target {
			c--
		}
		b.deleteRange(Pos{b.cur.L, c}, b.cur, "del")
		return
	}
	b.deleteRange(Pos{b.cur.L, b.cur.C - 1}, b.cur, "del")
}

func (b *Buffer) deleteForward() {
	if b.hasSel() {
		b.deleteSel()
	} else if b.cur.C < len(b.lines[b.cur.L]) {
		b.deleteRange(b.cur, Pos{b.cur.L, b.cur.C + 1}, "del")
	} else if b.cur.L < len(b.lines)-1 {
		b.deleteRange(b.cur, Pos{b.cur.L + 1, 0}, "del")
	}
}

func (b *Buffer) selectedLines() (int, int) {
	if !b.hasSel() {
		return b.cur.L, b.cur.L
	}
	s, e := b.selRange()
	if e.C == 0 && e.L > s.L {
		e.L--
	}
	return s.L, e.L
}

func (b *Buffer) indentSel(out bool) {
	single := !b.hasSel()
	if !single {
		s, e := b.selRange()
		single = s.L == e.L
	}
	if !out && single {
		if b.hasSel() {
			b.deleteSel()
		}
		if b.indent == "\t" {
			b.insert([]rune{'\t'}, "type")
		} else {
			w := len(b.indent)
			vc := visCol(b.lines[b.cur.L], b.cur.C)
			b.insert([]rune(strings.Repeat(" ", w-vc%w)), "type")
		}
		return
	}
	l0, l1 := b.selectedLines()
	n := len(b.undo)
	sel, anc, cur := b.selecting, b.anchor, b.cur
	for l := l0; l <= l1; l++ {
		line := b.lines[l]
		if out {
			k := 0
			if len(line) > 0 && line[0] == '\t' {
				k = 1
			} else {
				for k < len(b.indent) && k < len(line) && line[k] == ' ' {
					k++
				}
			}
			if k > 0 {
				b.replaceRange(Pos{l, 0}, Pos{l, k}, nil)
				if anc.L == l {
					anc.C = max(0, anc.C-k)
				}
				if cur.L == l {
					cur.C = max(0, cur.C-k)
				}
			}
		} else if len(line) > 0 {
			ind := []rune(b.indent)
			b.replaceRange(Pos{l, 0}, Pos{l, 0}, ind)
			if anc.L == l && (anc.C > 0 || !sel) {
				anc.C += len(ind)
			}
			if cur.L == l && (cur.C > 0 || !sel) {
				cur.C += len(ind)
			}
		}
	}
	b.groupSince(n)
	b.selecting, b.anchor, b.cur = sel, b.clamp(anc), b.clamp(cur)
}

func commentToken(b *Buffer) string {
	ext := strings.ToLower(filepath.Ext(b.path))
	base := strings.ToLower(filepath.Base(b.path))
	switch ext {
	case ".py", ".sh", ".bash", ".zsh", ".fish", ".rb", ".yaml", ".yml", ".toml", ".r", ".pl", ".conf", ".ini", ".nix", ".tf", ".ex", ".exs", ".jl", ".cmake", ".ps1", ".env", ".gitignore", ".dockerignore":
		return "#"
	case ".sql", ".lua", ".hs", ".elm", ".ada":
		return "--"
	case ".lisp", ".clj", ".cljs", ".el", ".scm", ".asm", ".s":
		return ";"
	case ".tex", ".erl", ".m":
		return "%"
	case ".vim":
		return "\""
	case ".html", ".xml", ".md", ".json", ".svg":
		return ""
	}
	switch base {
	case "makefile", "dockerfile", "justfile", ".bashrc", ".zshrc", ".profile", "containerfile", "gnumakefile":
		return "#"
	}
	if strings.HasPrefix(b.hl.lang, "Bash") || strings.HasPrefix(b.hl.lang, "Python") {
		return "#"
	}
	return "//"
}

// toggleComment comments or uncomments the selected lines. It returns false
// when the file type has no line comment syntax.
func (b *Buffer) toggleComment() bool {
	tok := commentToken(b)
	if tok == "" {
		return false
	}
	l0, l1 := b.selectedLines()
	all := true
	minInd := -1
	for l := l0; l <= l1; l++ {
		line := b.lines[l]
		ws := len(leadingWS(line))
		if ws == len(line) {
			continue
		}
		if !strings.HasPrefix(string(line[ws:]), tok) {
			all = false
		}
		if minInd < 0 || ws < minInd {
			minInd = ws
		}
	}
	if minInd < 0 {
		return true
	}
	n := len(b.undo)
	sel, anc, cur := b.selecting, b.anchor, b.cur
	for l := l0; l <= l1; l++ {
		line := b.lines[l]
		ws := len(leadingWS(line))
		if ws == len(line) {
			continue
		}
		var d, at int
		if all {
			k := len([]rune(tok))
			if ws+k < len(line) && line[ws+k] == ' ' {
				k++
			}
			b.replaceRange(Pos{l, ws}, Pos{l, ws + k}, nil)
			d, at = -k, ws
		} else {
			ins := []rune(tok + " ")
			b.replaceRange(Pos{l, minInd}, Pos{l, minInd}, ins)
			d, at = len(ins), minInd
		}
		if anc.L == l && anc.C >= at {
			anc.C = max(at, anc.C+d)
		}
		if cur.L == l && cur.C >= at {
			cur.C = max(at, cur.C+d)
		}
	}
	b.groupSince(n)
	b.selecting, b.anchor, b.cur = sel, b.clamp(anc), b.clamp(cur)
	return true
}

func (b *Buffer) moveLines(d int) {
	l0, l1 := b.selectedLines()
	if (d < 0 && l0 == 0) || (d > 0 && l1 >= len(b.lines)-1) {
		return
	}
	n := len(b.undo)
	sel, anc, cur := b.selecting, b.anchor, b.cur
	if d < 0 {
		moved := append([]rune(nil), b.lines[l0-1]...)
		b.replaceRange(Pos{l0 - 1, 0}, Pos{l0, 0}, nil)
		b.replaceRange(Pos{l1 - 1, len(b.lines[l1-1])}, Pos{l1 - 1, len(b.lines[l1-1])}, append([]rune{'\n'}, moved...))
	} else {
		moved := append([]rune(nil), b.lines[l1+1]...)
		b.replaceRange(Pos{l1, len(b.lines[l1])}, Pos{l1 + 1, len(b.lines[l1+1])}, nil)
		b.replaceRange(Pos{l0, 0}, Pos{l0, 0}, append(moved, '\n'))
	}
	b.groupSince(n)
	anc.L += d
	cur.L += d
	b.selecting, b.anchor, b.cur = sel, b.clamp(anc), b.clamp(cur)
}

func (b *Buffer) duplicateLines(down bool) {
	l0, l1 := b.selectedLines()
	text := b.textRange(Pos{l0, 0}, Pos{l1, len(b.lines[l1])})
	sel, anc, cur := b.selecting, b.anchor, b.cur
	b.replaceRange(Pos{l1, len(b.lines[l1])}, Pos{l1, len(b.lines[l1])}, append([]rune{'\n'}, text...))
	if down {
		d := l1 - l0 + 1
		anc.L += d
		cur.L += d
	}
	b.selecting, b.anchor, b.cur = sel, anc, cur
}

func (b *Buffer) deleteLines() {
	l0, l1 := b.selectedLines()
	col := b.cur.C
	if l1 < len(b.lines)-1 {
		b.replaceRange(Pos{l0, 0}, Pos{l1 + 1, 0}, nil)
	} else if l0 > 0 {
		b.replaceRange(Pos{l0 - 1, len(b.lines[l0-1])}, Pos{l1, len(b.lines[l1])}, nil)
		l0--
	} else {
		b.replaceRange(Pos{0, 0}, Pos{l1, len(b.lines[l1])}, nil)
	}
	b.selecting = false
	b.cur = b.clamp(Pos{l0, col})
}

func (b *Buffer) selectLine() {
	if b.hasSel() {
		s, e := b.selRange()
		b.anchor = Pos{s.L, 0}
		if e.C == 0 && e.L > s.L {
			e.L--
		}
		if e.L+1 < len(b.lines) {
			b.cur = Pos{e.L + 1, 0}
		} else {
			b.cur = Pos{e.L, len(b.lines[e.L])}
		}
	} else {
		b.anchor = Pos{b.cur.L, 0}
		if b.cur.L+1 < len(b.lines) {
			b.cur = Pos{b.cur.L + 1, 0}
		} else {
			b.cur = Pos{b.cur.L, len(b.lines[b.cur.L])}
		}
	}
	b.selecting = true
}

func (b *Buffer) selectAll() {
	b.anchor = Pos{0, 0}
	l := len(b.lines) - 1
	b.cur = Pos{l, len(b.lines[l])}
	b.selecting = true
}

func (b *Buffer) wordBounds(p Pos) (int, int) {
	line := b.lines[p.L]
	s, e := p.C, p.C
	if s < len(line) && !isWordRune(line[s]) && (s == 0 || !isWordRune(line[s-1])) {
		// not on a word: select the single character / run of spaces
		if s < len(line) {
			e = s + 1
			if unicode.IsSpace(line[s]) {
				for s > 0 && unicode.IsSpace(line[s-1]) {
					s--
				}
				for e < len(line) && unicode.IsSpace(line[e]) {
					e++
				}
			}
		}
		return s, e
	}
	for s > 0 && isWordRune(line[s-1]) {
		s--
	}
	for e < len(line) && isWordRune(line[e]) {
		e++
	}
	return s, e
}

// selectNextOccurrence selects the word at the cursor, or moves the selection
// to the next occurrence of the selected text. It returns the query used.
func (b *Buffer) selectNextOccurrence() []rune {
	if !b.hasSel() {
		s, e := b.wordBounds(b.cur)
		if s == e {
			return nil
		}
		b.anchor = Pos{b.cur.L, s}
		b.cur = Pos{b.cur.L, e}
		b.selecting = true
		return b.selText()
	}
	q := b.selText()
	if containsNL(q) {
		return nil
	}
	_, e := b.selRange()
	if m, ok := findFrom(b, q, e, true); ok {
		b.anchor = m
		b.cur = Pos{m.L, m.C + len(q)}
		b.selecting = true
	}
	return q
}

// wordMove moves by word in direction d (-1 or 1).
func (b *Buffer) wordMove(p Pos, d int) Pos {
	line := b.lines[p.L]
	class := func(r rune) int {
		switch {
		case unicode.IsSpace(r):
			return 0
		case isWordRune(r):
			return 1
		}
		return 2
	}
	if d < 0 {
		if p.C == 0 {
			if p.L > 0 {
				return Pos{p.L - 1, len(b.lines[p.L-1])}
			}
			return p
		}
		c := p.C
		for c > 0 && class(line[c-1]) == 0 {
			c--
		}
		if c > 0 {
			k := class(line[c-1])
			for c > 0 && class(line[c-1]) == k {
				c--
			}
		}
		return Pos{p.L, c}
	}
	if p.C >= len(line) {
		if p.L < len(b.lines)-1 {
			return Pos{p.L + 1, 0}
		}
		return p
	}
	c := p.C
	for c < len(line) && class(line[c]) == 0 {
		c++
	}
	if c < len(line) {
		k := class(line[c])
		for c < len(line) && class(line[c]) == k {
			c++
		}
	}
	return Pos{p.L, c}
}

func (b *Buffer) vmove(d int) {
	if b.wantCol < 0 {
		b.wantCol = visCol(b.lines[b.cur.L], b.cur.C)
	}
	l := b.cur.L + d
	if l < 0 {
		b.cur = Pos{0, 0}
		return
	}
	if l >= len(b.lines) {
		l = len(b.lines) - 1
		b.cur = Pos{l, len(b.lines[l])}
		return
	}
	b.cur = Pos{l, colFromVis(b.lines[l], b.wantCol)}
}

// matchBracket finds the bracket matching the one at p.
func matchBracket(b *Buffer, p Pos) (Pos, bool) {
	if p.C < 0 || p.C >= len(b.lines[p.L]) {
		return p, false
	}
	r := b.lines[p.L][p.C]
	var want rune
	dir := 1
	switch r {
	case '(', '[', '{':
		want = pairs[r]
	case ')':
		want, dir = '(', -1
	case ']':
		want, dir = '[', -1
	case '}':
		want, dir = '{', -1
	default:
		return p, false
	}
	depth := 0
	l, c := p.L, p.C
	for steps := 0; steps < 200000; steps++ {
		c += dir
		for c < 0 || c >= len(b.lines[l]) {
			l += dir
			if l < 0 || l >= len(b.lines) || abs(l-p.L) > 3000 {
				return p, false
			}
			// an empty line leaves c out of range, so the loop skips it
			if dir > 0 {
				c = 0
			} else {
				c = len(b.lines[l]) - 1
			}
		}
		ch := b.lines[l][c]
		if ch == r {
			depth++
		} else if ch == want {
			if depth == 0 {
				return Pos{l, c}, true
			}
			depth--
		}
	}
	return p, false
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}

// ---- find helpers ----

func caseSensitive(q []rune) bool {
	for _, r := range q {
		if unicode.IsUpper(r) {
			return true
		}
	}
	return false
}

// matchesInLine returns start indices of q in line.
func matchesInLine(line, q []rune, cs bool) []int {
	if len(q) == 0 || len(q) > len(line) {
		return nil
	}
	var out []int
outer:
	for i := 0; i+len(q) <= len(line); i++ {
		for j, r := range q {
			c := line[i+j]
			if c != r && (cs || unicode.ToLower(c) != unicode.ToLower(r)) {
				continue outer
			}
		}
		out = append(out, i)
		i += len(q) - 1
	}
	return out
}

// findFrom searches forward (or backward) from p, wrapping around.
func findFrom(b *Buffer, q []rune, p Pos, forward bool) (Pos, bool) {
	if len(q) == 0 {
		return p, false
	}
	cs := caseSensitive(q)
	n := len(b.lines)
	if forward {
		for k := 0; k <= n; k++ {
			l := (p.L + k) % n
			for _, m := range matchesInLine(b.lines[l], q, cs) {
				if k == 0 && m < p.C {
					continue
				}
				if k == n && m >= p.C {
					break
				}
				return Pos{l, m}, true
			}
		}
	} else {
		for k := 0; k <= n; k++ {
			l := ((p.L-k)%n + n) % n
			ms := matchesInLine(b.lines[l], q, cs)
			for i := len(ms) - 1; i >= 0; i-- {
				m := ms[i]
				if k == 0 && m >= p.C {
					continue
				}
				return Pos{l, m}, true
			}
		}
	}
	return p, false
}

func countMatches(b *Buffer, q []rune, at Pos) (idx, total int) {
	cs := caseSensitive(q)
	for l, line := range b.lines {
		for _, m := range matchesInLine(line, q, cs) {
			total++
			if l == at.L && m == at.C {
				idx = total
			}
		}
	}
	return
}

func (b *Buffer) replaceAll(q, with []rune) int {
	cs := caseSensitive(q)
	count := 0
	n := len(b.undo)
	for l := 0; l < len(b.lines); l++ {
		ms := matchesInLine(b.lines[l], q, cs)
		for i := len(ms) - 1; i >= 0; i-- {
			b.replaceRange(Pos{l, ms[i]}, Pos{l, ms[i] + len(q)}, with)
			count++
		}
		if len(ms) > 0 {
			l += strings.Count(string(with), "\n") * len(ms)
		}
	}
	b.groupSince(n)
	b.selecting = false
	b.cur = b.clamp(b.cur)
	return count
}

func containsNL(rs []rune) bool {
	for _, r := range rs {
		if r == '\n' {
			return true
		}
	}
	return false
}

// splitLineSuffix parses "path:12:3" into ("path", 12, 3).
func splitLineSuffix(s string) (string, int, int) {
	line, col := 0, 0
	for k := 0; k < 2; k++ {
		i := strings.LastIndexByte(s, ':')
		if i < 0 {
			break
		}
		n, err := strconv.Atoi(s[i+1:])
		if err != nil {
			break
		}
		col, line = line, n
		s = s[:i]
	}
	if line == 0 && col != 0 {
		line, col = col, 0
	}
	return s, line, col
}

// byteToRuneCol converts a byte offset within a line to a rune index.
func byteToRuneCol(line []rune, off int) int {
	n := 0
	for i, r := range line {
		if n >= off {
			return i
		}
		n += len(string(r))
	}
	return len(line)
}

func wordLeft(s []rune, i int) int {
	for i > 0 && !isWordRune(s[i-1]) {
		i--
	}
	for i > 0 && isWordRune(s[i-1]) {
		i--
	}
	return i
}

// completePath completes a filesystem path relative to root.
func completePath(root, s string) string {
	expanded := s
	if strings.HasPrefix(s, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			expanded = filepath.Join(home, s[2:])
			if strings.HasSuffix(s, "/") {
				expanded += "/"
			}
		}
	}
	full := expanded
	if !filepath.IsAbs(full) {
		full = filepath.Join(root, expanded)
		if strings.HasSuffix(expanded, "/") || expanded == "" {
			full += "/"
		}
	}
	dir, prefix := filepath.Split(full)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return s
	}
	var names []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), prefix) && (prefix != "" || !strings.HasPrefix(e.Name(), ".")) {
			n := e.Name()
			if e.IsDir() {
				n += "/"
			}
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return s
	}
	sort.Strings(names)
	common := names[0]
	for _, n := range names[1:] {
		for !strings.HasPrefix(n, common) {
			common = common[:len(common)-1]
		}
	}
	return s + strings.TrimPrefix(common, prefix)
}

// ---- rune offsets (for the platform text-input / IME protocol) ----

func (b *Buffer) offsetOf(p Pos) int {
	b.ensureLineStarts()
	p = b.clamp(p)
	return b.lineStarts[p.L] + p.C
}

func (b *Buffer) posAt(off int) Pos {
	b.ensureLineStarts()
	if off <= 0 {
		return Pos{0, 0}
	}
	ls := b.lineStarts
	l := sort.Search(len(ls), func(i int) bool { return ls[i] > off }) - 1
	if l < 0 {
		l = 0
	}
	return b.clamp(Pos{l, off - ls[l]})
}

func (b *Buffer) ensureLineStarts() {
	if b.lineStartsVer == b.version && len(b.lineStarts) == len(b.lines) {
		return
	}
	if cap(b.lineStarts) < len(b.lines) {
		b.lineStarts = make([]int, len(b.lines))
	}
	b.lineStarts = b.lineStarts[:len(b.lines)]
	off := 0
	for i, l := range b.lines {
		b.lineStarts[i] = off
		off += len(l) + 1
	}
	b.lineStartsVer = b.version
}
