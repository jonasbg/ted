package main

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
)

func TestCSVFields(t *testing.T) {
	line := []rune(`a,"b,c",,"say ""hi"""`)
	fs := csvFields(line, ',')
	var got []string
	for _, f := range fs {
		got = append(got, cellText(line, f))
	}
	want := []string{"a", "b,c", "", `say "hi"`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("fields %q", got)
	}
	if i := fieldAt(fs, 3); i != 1 {
		t.Fatalf("fieldAt inside quotes = %d", i)
	}
}

func TestSniffDelim(t *testing.T) {
	for in, want := range map[string]rune{
		"a,b,c\n1,2,3\n":         ',',
		"a;b;c\n1,5;2;3\n":       ';',
		"a\tb\n1\t2\n":           '\t',
		"name|age\nann|3\n":      '|',
		"just one column\nx\n":   ',',
		`"x;y",b` + "\n" + "1,2": ',',
	} {
		if got := sniffDelim(in); got != want {
			t.Errorf("sniffDelim(%q) = %q, want %q", in, got, want)
		}
	}
	if csvDelimFor("a.tsv", "") != '\t' || csvDelimFor("a.go", "a,b") != 0 {
		t.Fatal("csvDelimFor by extension")
	}
}

func TestParseNumber(t *testing.T) {
	for in, want := range map[string]float64{
		"42": 42, " -3.5 ": -3.5, "1,234": 1234, "1,234.5": 1234.5, "3,5": 3.5,
		"$9.99": 9.99, "12%": 12, "1_000": 1000, "1e3": 1000,
	} {
		if v, ok := parseNumber(in); !ok || v != want {
			t.Errorf("parseNumber(%q) = %v %v", in, v, ok)
		}
	}
	for _, in := range []string{"", "abc", "NaN", "Inf", "12abc"} {
		if _, ok := parseNumber(in); ok {
			t.Errorf("parseNumber(%q) accepted", in)
		}
	}
	if s := fmtNum(1234567); s != "1,234,567" {
		t.Errorf("fmtNum int %q", s)
	}
	if s := fmtNum(-1234.5); s != "-1,234.5" {
		t.Errorf("fmtNum float %q", s)
	}
}

func csvBuffer(text string) *Buffer {
	b := newBuffer("/x/data.csv")
	b.setContent([]byte(text))
	b.resolveLexer()
	return b
}

func TestCSVSelectionStats(t *testing.T) {
	b := csvBuffer("item,qty,price\napple,3,1.5\npear,,2\nfig,4,x\n")
	if b.csv == nil || b.csv.delim != ',' || b.hl.lang != "CSV" {
		t.Fatalf("not detected as CSV: %+v lang=%q", b.csv, b.hl.lang)
	}
	// Alt+click the "qty" header: the whole column, header excluded
	b.startBlock(Pos{0, 6}, false)
	st, ok := b.selectionStats()
	if !ok || st.cells != 2 || st.numbers != 2 || st.sum != 7 {
		t.Fatalf("qty column: %+v", st)
	}
	// extend over price too
	b.dragBlock(Pos{0, 10})
	st, _ = b.selectionStats()
	if st.cells != 5 || st.numbers != 4 || st.sum != 10.5 || st.lo != 1.5 || st.hi != 4 {
		t.Fatalf("qty+price: %+v", st)
	}
	if got := b.blockText(); got != "qty\tprice\n3\t1.5\n\t2\n4\tx\n" {
		t.Fatalf("blockText %q", got)
	}
	// a block of cells from a data row
	b.startBlock(Pos{1, 0}, false)
	b.dragBlock(Pos{2, 5}) // the empty qty cell of "pear"
	st, _ = b.selectionStats()
	if st.cells != 3 || st.sum != 3 || st.label() != "Count 3  Sum 3  Avg 3" {
		t.Fatalf("block: %+v %q", st, st.label())
	}
	// rows via a text selection (as the gutter makes): rows 2-3, all cells
	b.clearBlock()
	b.anchor, b.cur, b.selecting = Pos{1, 0}, Pos{3, 0}, true
	st, _ = b.selectionStats()
	if st.cells != 5 || st.sum != 6.5 { // apple,3,1.5,pear,2
		t.Fatalf("rows: %+v", st)
	}
	// caret label names the column
	b.selecting = false
	b.cur = Pos{2, len("pear,")}
	if got := b.columnLabel(); got != "Field 2: qty" {
		t.Fatalf("columnLabel %q", got)
	}
}

func TestCSVUI(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sales.csv")
	var sb strings.Builder
	sb.WriteString("region,month,units,revenue\n")
	for i, r := range []string{"north", "south", "east", "west", "north", "south"} {
		sb.WriteString(r + ",2026-0" + string(rune('1'+i)) + "," + []string{"12", "7", "30", "5", "18", "9"}[i] + "," + []string{"1200.50", "640", "2999.99", "410", "1810", "905.25"}[i] + "\n")
	}
	os.WriteFile(p, []byte(sb.String()), 0o644)
	h := newHarness(t, dir, p)
	h.frame()
	b := h.a.buf()
	if b.csv == nil {
		t.Fatal("csv mode not on")
	}
	u := &h.a.ui
	at := func(l, c int) f32.Point {
		x := u.text.Min.X + u.padLeft + c*u.charW + u.charW/2
		y := u.editor.Min.Y + u.padTop + l*u.lineH + u.lineH/2
		return f32.Pt(float32(x), float32(y))
	}
	altDrag := func(from, to f32.Point, mods key.Modifiers) {
		h.r.Queue(pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: from, Modifiers: mods},
			pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, Buttons: pointer.ButtonPrimary, Position: to, Modifiers: mods},
			pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, Position: to, Modifiers: mods})
		h.frame()
	}
	statusText := func() string {
		for _, it := range u.statusItems {
			if it.id == "csv" {
				st, ok := b.selectionStats()
				if ok {
					return st.label()
				}
				return b.columnLabel()
			}
		}
		t.Fatal("no csv status item")
		return ""
	}
	// Alt+click the "units" header (line 0, col of "units")
	units := strings.Index("region,month,units", "units")
	altDrag(at(0, units+1), at(0, units+1), key.ModAlt)
	if got := statusText(); got != "Count 6  Sum 81  Avg 13.5" {
		t.Fatalf("units column: %q", got)
	}
	// drag across to revenue
	altDrag(at(0, units+1), at(0, units+8), key.ModAlt)
	h.frame()
	h.shot("20-csv-columns")
	st, _ := b.selectionStats()
	if st.cells != 12 || st.numbers != 12 {
		t.Fatalf("units+revenue: %+v", st)
	}
	// clicking without Alt drops the cell selection
	h.click(int(at(2, 1).X), int(at(2, 1).Y))
	if b.csv.block != nil {
		t.Fatal("plain click kept the cell block")
	}
	// a cell block in the data, then Escape clears it
	altDrag(at(1, 0), at(3, units+1), key.ModAlt)
	if st, _ := b.selectionStats(); st.cells != 9 || st.sum != 49 {
		t.Fatalf("cell block: %+v", st)
	}
	h.shot("21-csv-block")
	h.keys(press(key.NameEscape))
	if b.csv.block != nil {
		t.Fatal("Escape kept the cell block")
	}
	r := image.Rectangle{}
	for _, it := range u.statusItems {
		if it.id == "csv" {
			r = it.r
		}
	}
	if r.Empty() {
		t.Fatal("csv status item missing")
	}
}
