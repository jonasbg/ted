package main

import (
	"image/color"
	"path/filepath"
	"strings"
	"sync"

	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/alecthomas/chroma/v2/styles"
)

// Files up to fullHighlightBytes are highlighted as a whole; larger files are
// highlighted in a window around the viewport so editing stays instant.
const fullHighlightBytes = 384 << 10
const hlWindowMargin = 300

type tokStyle struct {
	fg     color.NRGBA
	bold   bool
	italic bool
}

type span struct {
	start int // rune column where this style begins
	st    tokStyle
}

type highlightState struct {
	lexer    chroma.Lexer
	lang     string
	resolved bool
	spans    [][]span
	from     int // line index of spans[0]
	windowed bool
	guessVer int
	manual   bool // language picked by hand
	running  bool
	version  int // buffer version the spans were computed for
}

type hlResult struct {
	theme    *Theme
	buf      *Buffer
	version  int
	from     int
	windowed bool
	spans    [][]span
}

// lineSpans returns the cached spans for line li, if any.
func (h *highlightState) lineSpans(li int) []span {
	i := li - h.from
	if i < 0 || i >= len(h.spans) {
		return nil
	}
	return h.spans[i]
}

// Theme holds the UI colours, derived from a chroma style.
type Theme struct {
	rainbowCols []color.NRGBA // per-column CSV colours, derived lazily
	name        string
	light       bool
	cs          *chroma.Style
	mu          sync.Mutex
	cache       map[chroma.TokenType]tokStyle

	bg, fg, dim, faint       color.NRGBA
	curLine, selection       color.NRGBA
	selInactive              color.NRGBA
	match, matchCur, bracket color.NRGBA
	gutter, gutterCur        color.NRGBA
	sidebar, sideSel, hover  color.NRGBA
	panel, panel2, border    color.NRGBA
	statusBg, statusFg       color.NRGBA
	tabBar, tabIdleFg        color.NRGBA
	accent, onAccent, caret  color.NRGBA
	gitMod, gitNew, gitDel   color.NRGBA
	errorFg, dirFg, shadow   color.NRGBA
	indentGuide              color.NRGBA
	diffAdd, diffDel         color.NRGBA
	diffHunk                 color.NRGBA
	orange, activityBg       color.NRGBA
}

func nrgba(c chroma.Colour) color.NRGBA {
	return color.NRGBA{c.Red(), c.Green(), c.Blue(), 0xff}
}

func mixC(a, b color.NRGBA, t float64) color.NRGBA {
	f := func(x, y uint8) uint8 { return uint8(float64(x)*(1-t) + float64(y)*t + 0.5) }
	return color.NRGBA{f(a.R, b.R), f(a.G, b.G), f(a.B, b.B), 0xff}
}

func withA(c color.NRGBA, a uint8) color.NRGBA { c.A = a; return c }

func newTheme(name string) *Theme {
	st := styles.Get(name)
	if st == nil || st == styles.Fallback {
		st = styles.Get("catppuccin-mocha")
	}
	return themeFromStyle(st)
}

// themeFromStyle derives the full UI palette from a syntax style.
func themeFromStyle(st *chroma.Style) *Theme {
	t := &Theme{name: st.Name, cs: st, cache: map[chroma.TokenType]tokStyle{}}
	bgE := st.Get(chroma.Background)
	bg := color.NRGBA{0x1e, 0x1e, 0x2e, 0xff}
	fg := color.NRGBA{0xcd, 0xd6, 0xf4, 0xff}
	if bgE.Background.IsSet() {
		bg = nrgba(bgE.Background)
	}
	if bgE.Colour.IsSet() {
		fg = nrgba(bgE.Colour)
	}
	t.light = bgE.Background.IsSet() && bgE.Background.Brightness() > 0.5
	black, white := color.NRGBA{0, 0, 0, 0xff}, color.NRGBA{0xff, 0xff, 0xff, 0xff}
	toward := white
	away := black
	if t.light {
		toward, away = black, white
	}
	_ = away
	accent := color.NRGBA{0x89, 0xb4, 0xfa, 0xff}
	if e := st.Get(chroma.NameFunction); e.Colour.IsSet() {
		accent = nrgba(e.Colour)
	}
	t.bg, t.fg = bg, fg
	t.dim = mixC(bg, fg, 0.55)
	t.faint = mixC(bg, fg, 0.3)
	t.curLine = mixC(bg, toward, 0.035)
	t.selection = mixC(bg, accent, 0.30)
	t.selInactive = mixC(bg, fg, 0.15)
	yellow := color.NRGBA{0xf9, 0xe2, 0xaf, 0xff}
	t.match = mixC(bg, yellow, 0.22)
	t.matchCur = mixC(bg, yellow, 0.45)
	t.bracket = mixC(bg, fg, 0.22)
	t.gutter = mixC(bg, fg, 0.32)
	t.gutterCur = mixC(bg, fg, 0.85)
	// The title bar, tab strip and status bar share the editor background so
	// the window reads as one surface; only the sidebar is a touch deeper.
	t.tabBar = bg
	t.statusBg = bg
	t.sidebar = bg // one surface: the panels share the editor background
	t.sideSel = mixC(t.sidebar, accent, 0.2)
	t.hover = mixC(t.sidebar, toward, 0.05)
	t.panel = mixC(bg, toward, 0.06)
	t.panel2 = mixC(bg, toward, 0.12)
	t.border = mixC(bg, fg, 0.14)
	t.statusFg = mixC(bg, fg, 0.55)
	t.tabIdleFg = mixC(bg, fg, 0.55)
	t.accent = accent
	t.onAccent = bg
	t.caret = accent
	t.gitMod = colourOr(st, chroma.NameBuiltin, color.NRGBA{0xf9, 0xe2, 0xaf, 0xff})
	t.gitNew = colourOr(st, chroma.LiteralString, color.NRGBA{0xa6, 0xe3, 0xa1, 0xff})
	t.gitDel = colourOr(st, chroma.GenericDeleted, color.NRGBA{0xf3, 0x8b, 0xa8, 0xff})
	t.errorFg = colourOr(st, chroma.Error, color.NRGBA{0xf3, 0x8b, 0xa8, 0xff})
	t.dirFg = mixC(fg, accent, 0.25)
	t.shadow = color.NRGBA{0, 0, 0, 0x60}
	t.indentGuide = mixC(bg, fg, 0.13)
	t.diffAdd = mixC(bg, t.gitNew, 0.13)
	t.diffDel = mixC(bg, t.gitDel, 0.14)
	t.diffHunk = mixC(bg, accent, 0.1)
	t.orange = color.NRGBA{0xf8, 0xa8, 0x4a, 0xff}
	t.activityBg = bg
	if t.light {
		t.orange = color.NRGBA{0xe0, 0x82, 0x10, 0xff}
	}
	return t
}

func colourOr(st *chroma.Style, tt chroma.TokenType, def color.NRGBA) color.NRGBA {
	if e := st.Get(tt); e.Colour.IsSet() {
		return nrgba(e.Colour)
	}
	return def
}

func (t *Theme) tokenStyle(tt chroma.TokenType) tokStyle {
	t.mu.Lock()
	defer t.mu.Unlock()
	if s, ok := t.cache[tt]; ok {
		return s
	}
	e := t.cs.Get(tt)
	s := tokStyle{fg: t.fg}
	if e.Colour.IsSet() {
		s.fg = nrgba(e.Colour)
	}
	s.bold = e.Bold == chroma.Yes
	s.italic = e.Italic == chroma.Yes
	t.cache[tt] = s
	return s
}

// lexerFor picks a lexer by file name, then by content.
func lexerFor(path string, sample string) chroma.Lexer {
	base := filepath.Base(path)
	l := lexers.Match(base)
	if l == nil {
		switch strings.ToLower(base) {
		case "dockerfile", "containerfile":
			l = lexers.Get("docker")
		case ".bashrc", ".zshrc", ".profile", ".bash_profile", ".zprofile", ".envrc", ".env":
			l = lexers.Get("bash")
		case "justfile":
			l = lexers.Get("makefile")
		}
	}
	if l == nil && strings.HasPrefix(sample, "#!") {
		first := sample
		if i := strings.IndexByte(first, '\n'); i >= 0 {
			first = first[:i]
		}
		switch {
		case strings.Contains(first, "python"):
			l = lexers.Get("python")
		case strings.Contains(first, "node") || strings.Contains(first, "deno") || strings.Contains(first, "bun"):
			l = lexers.Get("javascript")
		case strings.Contains(first, "sh"):
			l = lexers.Get("bash")
		case strings.Contains(first, "ruby"):
			l = lexers.Get("ruby")
		case strings.Contains(first, "perl"):
			l = lexers.Get("perl")
		}
	}
	if l == nil && sample != "" {
		l = lexers.Analyse(sample)
	}
	if l == nil {
		return nil
	}
	return chroma.Coalesce(l)
}

func (b *Buffer) resolveLexer() {
	h := b.hl
	// Untitled buffers have no file name to go by: keep guessing from the
	// content as it is typed or pasted, until a language is recognised.
	if h.resolved && h.lexer == nil && b.path == "" && !h.manual && b.version != h.guessVer {
		h.resolved = false
	}
	if h.resolved {
		return
	}
	h.guessVer = b.version
	h.resolved = true
	sample := ""
	for i := 0; i < len(b.lines) && i < 40; i++ {
		sample += string(b.lines[i]) + "\n"
	}
	if d := csvDelimFor(b.path, sample); d != 0 {
		b.setCSV(d)
		return
	}
	h.lexer = lexerFor(b.path, sample)
	if h.lexer != nil {
		h.lang = h.lexer.Config().Name
		if h.lang == "plaintext" {
			h.lang = "Plain Text"
		}
		// a scratch buffer just recognised as Go or Make: use their tab
		// indentation unless the text already set a style
		if b.path == "" && (h.lang == "Go" || h.lang == "Makefile") && b.indent != "\t" {
			b.indent = "\t"
		}
	}
}

// computeSpans tokenises text into per-line style spans.
func computeSpans(lexer chroma.Lexer, theme *Theme, text string) [][]span {
	it, err := lexer.Tokenise(nil, text)
	if err != nil {
		return nil
	}
	out := make([][]span, 1, strings.Count(text, "\n")+1)
	col := 0
	for tok := it(); tok != chroma.EOF; tok = it() {
		st := theme.tokenStyle(tok.Type)
		val := tok.Value
		for {
			i := strings.IndexByte(val, '\n')
			part := val
			if i >= 0 {
				part = val[:i]
			}
			if part != "" {
				line := &out[len(out)-1]
				if n := len(*line); n == 0 || (*line)[n-1].st != st {
					*line = append(*line, span{col, st})
				}
				col += countRunes(part)
			}
			if i < 0 {
				break
			}
			out = append(out, nil)
			col = 0
			val = val[i+1:]
		}
	}
	return out
}

func countRunes(s string) int {
	n := 0
	for range s {
		n++
	}
	return n
}

// spliceLines keeps cached spans aligned with the buffer's lines while an
// edit is waiting to be re-highlighted: at index i, remove del entries and
// insert ins empty ones.
func (h *highlightState) spliceLines(i, del, ins int) {
	if h == nil || h.spans == nil {
		return
	}
	i -= h.from
	if i < 0 || i > len(h.spans) {
		return
	}
	if i+del > len(h.spans) {
		del = len(h.spans) - i
	}
	var fill []span
	if i > 0 {
		fill = h.spans[i-1]
	}
	out := make([][]span, 0, len(h.spans)-del+ins)
	out = append(out, h.spans[:i]...)
	for k := 0; k < ins; k++ {
		out = append(out, fill)
	}
	out = append(out, h.spans[i+del:]...)
	h.spans = out
}
