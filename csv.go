package main

import (
	"image/color"
	"math"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/alecthomas/chroma/v2"
)

// Rainbow CSV: delimited files get one colour per column, and cells can be
// selected as a block (Alt+drag, or Alt+click a header to take the whole
// column). The status bar shows count, sum and average of the selection.

type csvState struct {
	delim rune
	block *cellBlock // cell selection, nil when none

	statsKey statsKey
	stats    csvStats
}

// cellBlock is a rectangle of cells from an anchor to a cursor cell. With
// cols set it spans every data row (all but the header) of its columns.
type cellBlock struct {
	ar, ac int // anchor row, field
	cr, cc int // cursor row, field
	cols   bool
}

// blockBounds is the rows and fields the cell block covers, inclusive.
// Whole columns stop at the last non-empty line (files end with a newline).
func (b *Buffer) blockBounds() (r0, r1, c0, c1 int) {
	k := b.csv.block
	r0, r1 = min(k.ar, k.cr), max(k.ar, k.cr)
	c0, c1 = min(k.ac, k.cc), max(k.ac, k.cc)
	if k.cols {
		last := len(b.lines) - 1
		for last > 0 && len(b.lines[last]) == 0 {
			last--
		}
		r0, r1 = min(1, last), last
	}
	return
}

type field struct{ start, end int } // rune columns, end exclusive

// csvDelimFor decides whether a file is delimited text and returns its
// delimiter, or 0.
func csvDelimFor(path, sample string) rune {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".tsv", ".tab":
		return '\t'
	case ".psv":
		return '|'
	case ".csv":
		return sniffDelim(sample)
	}
	return 0
}

// sniffDelim picks the candidate that splits the first lines into the same,
// largest number of fields. Comma wins ties.
func sniffDelim(sample string) rune {
	lines := strings.Split(strings.TrimRight(sample, "\n"), "\n")
	if len(lines) > 10 {
		lines = lines[:10]
	}
	best, bestN := ',', 0
	for _, d := range []rune{',', ';', '\t', '|'} {
		n := -1
		for _, l := range lines {
			c := len(csvFields([]rune(strings.TrimSuffix(l, "\r")), d))
			if n < 0 || c < n {
				n = c // fields every line has
			}
		}
		if n > bestN {
			best, bestN = d, n
		}
	}
	return best
}

// csvFields splits one line into fields, honouring double-quoted fields
// with "" escapes. Quoted fields spanning lines are treated per line.
func csvFields(line []rune, delim rune) []field {
	out := make([]field, 0, 16)
	start, inQ := 0, false
	for i, r := range line {
		switch {
		case r == '"':
			inQ = !inQ
		case r == delim && !inQ:
			out = append(out, field{start, i})
			start = i + 1
		}
	}
	return append(out, field{start, len(line)})
}

// fieldAt is the index of the field containing rune column c.
func fieldAt(fs []field, c int) int {
	for i, f := range fs {
		if c <= f.end {
			return i
		}
	}
	return len(fs) - 1
}

// cellText is a field's value with surrounding quotes removed.
func cellText(line []rune, f field) string {
	s := strings.TrimSpace(string(line[f.start:f.end]))
	if len(s) >= 2 && s[0] == '"' && s[len(s)-1] == '"' {
		s = strings.ReplaceAll(s[1:len(s)-1], `""`, `"`)
	}
	return s
}

// parseNumber reads a cell as a number, tolerating thousands separators,
// currency signs, a trailing % and a decimal comma (when unambiguous).
func parseNumber(s string) (float64, bool) {
	s = strings.TrimSpace(s)
	s = strings.TrimLeft(s, "$€£¥")
	s = strings.TrimSuffix(s, "%")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "_", "")
	if s == "" {
		return 0, false
	}
	if strings.Count(s, ",") == 1 && !strings.Contains(s, ".") {
		// 3,5 is a decimal comma; 1,234 is a thousands separator
		if i := strings.IndexByte(s, ','); len(s)-i-1 != 3 {
			s = strings.Replace(s, ",", ".", 1)
		}
	}
	s = strings.ReplaceAll(s, ",", "")
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// rainbow is the per-column palette, taken from the theme's syntax colours
// so it matches the rest of the editor.
func (t *Theme) rainbow() []color.NRGBA {
	if t.rainbowCols != nil {
		return t.rainbowCols
	}
	types := []chroma.TokenType{
		chroma.NameFunction, chroma.LiteralString, chroma.Keyword, chroma.LiteralNumber,
		chroma.NameClass, chroma.NameBuiltin, chroma.NameTag, chroma.Operator,
	}
	var out []color.NRGBA
	for _, tt := range types {
		c := t.tokenStyle(tt).fg
		dup := c == t.fg
		for _, o := range out {
			if o == c {
				dup = true
			}
		}
		if !dup {
			out = append(out, c)
		}
	}
	if len(out) < 3 {
		out = append(out, t.accent, t.gitNew, t.gitMod, t.orange)
	}
	t.rainbowCols = out
	return out
}

// csvSpans colours each field of a line by its column; delimiters are dim
// and the header row is bold.
func (a *App) csvSpans(b *Buffer, li int) []span {
	line := b.lines[li]
	pal := a.th.rainbow()
	fs := csvFields(line, b.csv.delim)
	out := make([]span, 0, 2*len(fs))
	for i, f := range fs {
		out = append(out, span{f.start, tokStyle{fg: pal[i%len(pal)], bold: li == 0}})
		if f.end < len(line) {
			out = append(out, span{f.end, tokStyle{fg: a.th.dim}})
		}
	}
	return out
}

// spansFor returns the style spans used to draw line li.
func (a *App) spansFor(b *Buffer, li int) []span {
	if b.csv != nil {
		return a.csvSpans(b, li)
	}
	return b.hl.lineSpans(li)
}

// ---- selection statistics ----

type statsKey struct {
	ver      int
	block    cellBlock
	hasBlock bool
	s, e     Pos
}

type csvStats struct {
	cells, numbers int
	sum, lo, hi    float64
}

// eachSelectedCell calls f for every cell in the block selection, or for
// every cell the text selection touches.
func (b *Buffer) eachSelectedCell(f func(line []rune, fl field)) {
	if k := b.csv.block; k != nil {
		r0, r1, c0, c1 := b.blockBounds()
		for li := r0; li <= r1; li++ {
			line := b.lines[li]
			fs := csvFields(line, b.csv.delim)
			for c := c0; c <= c1 && c < len(fs); c++ {
				f(line, fs[c])
			}
		}
		return
	}
	s, e := b.selRange()
	for li := s.L; li <= e.L; li++ {
		line := b.lines[li]
		sc, ec := 0, len(line)
		if li == s.L {
			sc = s.C
		}
		if li == e.L {
			ec = e.C
			if ec == 0 && li > s.L {
				break // selection ends at the start of this line
			}
		}
		for _, fl := range csvFields(line, b.csv.delim) {
			if fl.end >= sc && fl.start <= ec && !(fl.start == ec && ec > sc && li == e.L) {
				f(line, fl)
			}
		}
	}
}

// selectionStats summarises the selected cells, cached per selection.
func (b *Buffer) selectionStats() (csvStats, bool) {
	cs := b.csv
	if cs == nil || (cs.block == nil && !b.hasSel()) {
		return csvStats{}, false
	}
	key := statsKey{ver: b.version}
	if cs.block != nil {
		key.block, key.hasBlock = *cs.block, true
	} else {
		key.s, key.e = b.selRange()
	}
	if key == cs.statsKey && cs.stats.cells > 0 {
		return cs.stats, true
	}
	var st csvStats
	b.eachSelectedCell(func(line []rune, fl field) {
		t := cellText(line, fl)
		if t == "" {
			return
		}
		st.cells++
		if v, ok := parseNumber(t); ok {
			if st.numbers == 0 || v < st.lo {
				st.lo = v
			}
			if st.numbers == 0 || v > st.hi {
				st.hi = v
			}
			st.numbers++
			st.sum += v
		}
	})
	cs.statsKey, cs.stats = key, st
	return st, st.cells > 0
}

// fmtNum prints a number compactly: integers without decimals, others with
// up to four significant decimals.
func fmtNum(v float64) string {
	if v == math.Trunc(v) && math.Abs(v) < 1e15 {
		return groupThousands(strconv.FormatFloat(v, 'f', 0, 64))
	}
	s := strconv.FormatFloat(v, 'f', 4, 64)
	s = strings.TrimRight(strings.TrimRight(s, "0"), ".")
	if i := strings.IndexByte(s, '.'); i >= 0 {
		return groupThousands(s[:i]) + s[i:]
	}
	return groupThousands(s)
}

func groupThousands(s string) string {
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var sb strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			sb.WriteByte(',')
		}
		sb.WriteRune(r)
	}
	if neg {
		return "-" + sb.String()
	}
	return sb.String()
}

// statsLabel is the short status bar summary.
func (st csvStats) label() string {
	if st.numbers == 0 {
		return "Count " + strconv.Itoa(st.cells)
	}
	return "Count " + strconv.Itoa(st.cells) + "  Sum " + fmtNum(st.sum) + "  Avg " + fmtNum(st.sum/float64(st.numbers))
}

// detail is the longer summary shown when the status item is clicked.
func (st csvStats) detail() string {
	s := strconv.Itoa(st.cells) + " cells"
	if st.numbers > 0 {
		s += ", " + strconv.Itoa(st.numbers) + " numeric: sum " + fmtNum(st.sum) +
			", average " + fmtNum(st.sum/float64(st.numbers)) +
			", min " + fmtNum(st.lo) + ", max " + fmtNum(st.hi)
	}
	return s
}

// columnLabel names the field under the caret, e.g. "Field 3: price".
func (b *Buffer) columnLabel() string {
	fs := csvFields(b.lines[b.cur.L], b.csv.delim)
	i := fieldAt(fs, b.cur.C)
	s := "Field " + strconv.Itoa(i+1)
	if len(b.lines) > 1 {
		if hs := csvFields(b.lines[0], b.csv.delim); i < len(hs) {
			if name := cellText(b.lines[0], hs[i]); name != "" && b.cur.L > 0 {
				if len([]rune(name)) > 24 {
					name = string([]rune(name)[:23]) + "…"
				}
				s += ": " + name
			}
		}
	}
	return s
}

// ---- block selection ----

// cellAt is the row and field at a buffer position.
func (b *Buffer) cellAt(p Pos) (int, int) {
	return p.L, fieldAt(csvFields(b.lines[p.L], b.csv.delim), p.C)
}

// startBlock begins a cell selection at p; on the header row it takes
// whole columns. extend keeps the existing anchor.
func (b *Buffer) startBlock(p Pos, extend bool) {
	r, c := b.cellAt(p)
	if extend && b.csv.block != nil {
		b.csv.block.cr, b.csv.block.cc = r, c
	} else {
		b.csv.block = &cellBlock{ar: r, ac: c, cr: r, cc: c, cols: r == 0 && len(b.lines) > 1}
	}
	b.selecting = false
	b.cur = p
}

func (b *Buffer) dragBlock(p Pos) {
	if k := b.csv.block; k != nil {
		k.cr, k.cc = b.cellAt(p)
		b.cur = p
	}
}

// clearBlock drops the cell selection; it reports whether there was one.
func (b *Buffer) clearBlock() bool {
	if b.csv == nil || b.csv.block == nil {
		return false
	}
	b.csv.block = nil
	return true
}

// blockText is the selected cells as tab-separated rows, the format
// spreadsheets paste into cells.
func (b *Buffer) blockText() string {
	k := b.csv.block
	r0, r1, c0, c1 := b.blockBounds()
	if k.cols {
		r0 = 0 // copying whole columns includes their header
	}
	var sb strings.Builder
	for li := r0; li <= r1; li++ {
		line := b.lines[li]
		fs := csvFields(line, b.csv.delim)
		for c := c0; c <= c1; c++ {
			if c > c0 {
				sb.WriteByte('\t')
			}
			if c < len(fs) {
				sb.WriteString(cellText(line, fs[c]))
			}
		}
		sb.WriteByte('\n')
	}
	return sb.String()
}

// blockCells returns the field ranges to highlight on line li.
func (b *Buffer) blockCells(li int) []field {
	k := b.csv.block
	if k == nil {
		return nil
	}
	r0, r1, c0, c1 := b.blockBounds()
	if k.cols && li == 0 {
		r0 = 0 // the header of a selected column is marked too
	}
	if li < r0 || li > r1 {
		return nil
	}
	fs := csvFields(b.lines[li], b.csv.delim)
	if c0 >= len(fs) {
		return nil
	}
	return fs[c0:min(c1+1, len(fs))]
}

// setCSV switches b to rainbow CSV mode with delimiter d.
func (b *Buffer) setCSV(d rune) {
	b.csv = &csvState{delim: d}
	h := b.hl
	h.lexer, h.spans = nil, nil
	h.lang = csvLangName(d)
}

func csvLangName(d rune) string {
	switch d {
	case '\t':
		return "TSV"
	case ';':
		return "CSV (;)"
	case '|':
		return "CSV (|)"
	}
	return "CSV"
}
