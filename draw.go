package main

import (
	"image"
	"image/color"
	"os"

	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"github.com/go-text/typesetting/fontscan"
	"golang.org/x/image/math/fixed"
)

// Font faces used by the UI.
type face uint8

const (
	fMono face = iota
	fMonoBold
	fMonoItalic
	fMonoBoldItalic
	fUI
	fUIBold
	fUIItalic
	fUIBoldItalic
	nFaces
)

const monoFamilies = "JetBrains Mono, SF Mono, Menlo, Cascadia Code, Fira Code, Source Code Pro, DejaVu Sans Mono, Liberation Mono, Ubuntu Mono, Go Mono, monospace"
const uiFamilies = "Helvetica Neue, Inter, Segoe UI, Cantarell, Ubuntu, Noto Sans, DejaVu Sans, Go, sans-serif"

var faceFonts = [nFaces]font.Font{
	fMono:           {Typeface: monoFamilies},
	fMonoBold:       {Typeface: monoFamilies, Weight: font.Bold},
	fMonoItalic:     {Typeface: monoFamilies, Style: font.Italic},
	fMonoBoldItalic: {Typeface: monoFamilies, Weight: font.Bold, Style: font.Italic},
	fUI:             {Typeface: uiFamilies},
	fUIBold:         {Typeface: uiFamilies, Weight: font.SemiBold},
	fUIItalic:       {Typeface: uiFamilies, Style: font.Italic},
	fUIBoldItalic:   {Typeface: uiFamilies, Weight: font.SemiBold, Style: font.Italic},
}

func monoFace(st tokStyle) face {
	switch {
	case st.bold && st.italic:
		return fMonoBoldItalic
	case st.bold:
		return fMonoBold
	case st.italic:
		return fMonoItalic
	}
	return fMono
}

// textCache shapes strings once and keeps the resulting vector paths as
// reusable macros, so redrawing a frame is just replaying cached ops.
type textCache struct {
	sh     *text.Shaper
	ready  chan struct{} // closed once sh is built and warmed
	m      map[textKey]*textEntry
	glyphs []text.Glyph
	met    map[[2]int][2]int
}

type textKey struct {
	s  string
	f  face
	px int
}

type textEntry struct {
	call op.CallOp
	adv  int
	ops  *op.Ops
}

// newTextCache builds the shaper in the background: parsing the bundled
// fonts, indexing the system fonts and loading the faces in use takes longer
// than creating the window, so the two overlap instead of adding up.
func newTextCache() *textCache {
	c := &textCache{m: map[textKey]*textEntry{}, met: map[[2]int][2]int{}, ready: make(chan struct{})}
	fonts := faceFonts // useOmarchyFont has already run
	go func() {
		// Index the system fonts while the bundled ones are parsed; the
		// index is process-wide, so NewShaper below reuses it.
		indexed := make(chan struct{})
		go func() {
			if dir, err := os.UserCacheDir(); err == nil {
				fontscan.NewFontMap(quietLog{}).UseSystemFonts(dir)
			}
			close(indexed)
		}()
		coll := gofont.Collection()
		<-indexed
		sh := text.NewShaper(text.WithCollection(coll))
		for _, f := range fonts {
			sh.LayoutString(text.Parameters{Font: f, PxPerEm: fixed.I(16), MaxLines: 1, MaxWidth: 1 << 24}, "Mg")
			for _, ok := sh.NextGlyph(); ok; _, ok = sh.NextGlyph() {
			}
		}
		c.sh = sh
		close(c.ready)
	}()
	return c
}

// isReady reports whether the shaper is built, without waiting.
func (c *textCache) isReady() bool {
	select {
	case <-c.ready:
		return true
	default:
		return false
	}
}

// quietLog drops fontscan's diagnostics, as gio does unless debugging.
type quietLog struct{}

func (quietLog) Printf(string, ...any) {}

// shaper returns the shaper, waiting for newTextCache to finish building it.
func (c *textCache) shaper() *text.Shaper {
	<-c.ready
	return c.sh
}

func (c *textCache) get(f face, px int, s string) *textEntry {
	k := textKey{s, f, px}
	if e, ok := c.m[k]; ok {
		return e
	}
	if len(c.m) > 12000 {
		c.m = map[textKey]*textEntry{}
	}
	sh := c.shaper()
	sh.LayoutString(text.Parameters{
		Font:     faceFonts[f],
		PxPerEm:  fixed.I(px),
		MaxLines: 1,
		MaxWidth: 1 << 24,
	}, s)
	c.glyphs = c.glyphs[:0]
	var adv fixed.Int26_6
	for g, ok := sh.NextGlyph(); ok; g, ok = sh.NextGlyph() {
		c.glyphs = append(c.glyphs, g)
		adv += g.Advance
	}
	e := &textEntry{adv: adv.Round(), ops: new(op.Ops)}
	m := op.Record(e.ops)
	if len(c.glyphs) > 0 {
		g0 := c.glyphs[0]
		t := op.Affine(f32.AffineId().Offset(f32.Pt(float32(g0.X)/64, 0))).Push(e.ops)
		path := sh.Shape(c.glyphs)
		cl := clip.Outline{Path: path}.Op().Push(e.ops)
		paint.PaintOp{}.Add(e.ops)
		cl.Pop()
		if bm := sh.Bitmaps(c.glyphs); bm != (op.CallOp{}) {
			bm.Add(e.ops)
		}
		t.Pop()
	}
	e.call = m.Stop()
	c.m[k] = e
	return e
}

// draw paints s with its baseline at (x, baseline) and returns its advance.
func (c *textCache) draw(ops *op.Ops, f face, px, x, baseline int, s string, col color.NRGBA) int {
	if s == "" {
		return 0
	}
	e := c.get(f, px, s)
	t := op.Offset(image.Pt(x, baseline)).Push(ops)
	paint.ColorOp{Color: col}.Add(ops)
	e.call.Add(ops)
	t.Pop()
	return e.adv
}

func (c *textCache) width(f face, px int, s string) int {
	if s == "" {
		return 0
	}
	return c.get(f, px, s).adv
}

// metrics returns ascent and descent in pixels for a face and size.
func (c *textCache) metrics(f face, px int) (int, int) {
	k := [2]int{int(f), px}
	if m, ok := c.met[k]; ok {
		return m[0], m[1]
	}
	sh := c.shaper()
	sh.LayoutString(text.Parameters{Font: faceFonts[f], PxPerEm: fixed.I(px), MaxLines: 1, MaxWidth: 1 << 24}, "Mg")
	asc, desc := px, px/4
	if g, ok := sh.NextGlyph(); ok {
		asc, desc = g.Ascent.Ceil(), g.Descent.Ceil()
		for _, ok := sh.NextGlyph(); ok; _, ok = sh.NextGlyph() {
		}
	}
	c.met[k] = [2]int{asc, desc}
	return asc, desc
}

// truncate shortens s with an ellipsis to fit w pixels.
func (c *textCache) truncate(f face, px, w int, s string) string {
	if c.width(f, px, s) <= w {
		return s
	}
	rs := []rune(s)
	lo, hi := 0, len(rs)
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if c.width(f, px, string(rs[:mid])+"…") <= w {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return string(rs[:lo]) + "…"
}

// truncateLeft keeps the end of s (useful for paths).
func (c *textCache) truncateLeft(f face, px, w int, s string) string {
	if c.width(f, px, s) <= w {
		return s
	}
	rs := []rune(s)
	lo, hi := 0, len(rs)
	for lo < hi {
		mid := (lo + hi) / 2
		if c.width(f, px, "…"+string(rs[mid:])) <= w {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	return "…" + string(rs[lo:])
}

// ---- shapes ----

func fill(ops *op.Ops, r image.Rectangle, c color.NRGBA) {
	if r.Empty() || c.A == 0 {
		return
	}
	paint.FillShape(ops, c, clip.Rect(r).Op())
}

func rrect(ops *op.Ops, r image.Rectangle, rad int, c color.NRGBA) {
	if r.Empty() || c.A == 0 {
		return
	}
	paint.FillShape(ops, c, clip.UniformRRect(r, rad).Op(ops))
}

func strokeRRect(ops *op.Ops, r image.Rectangle, rad int, width float32, c color.NRGBA) {
	paint.FillShape(ops, c, clip.Stroke{Path: clip.UniformRRect(r, rad).Path(ops), Width: width}.Op())
}

// shadow draws a soft drop shadow under r.
func shadow(ops *op.Ops, r image.Rectangle, rad, size int, c color.NRGBA) {
	steps := 6
	for i := steps; i >= 1; i-- {
		d := size * i / steps
		a := uint8(int(c.A) / (steps + 2))
		rr := image.Rect(r.Min.X-d, r.Min.Y-d/2, r.Max.X+d, r.Max.Y+d)
		rrect(ops, rr, rad+d, color.NRGBA{c.R, c.G, c.B, a})
	}
}

// chevron draws a small arrow pointing right (or down when open).
func chevron(ops *op.Ops, cx, cy, size int, open bool, width float32, c color.NRGBA) {
	var p clip.Path
	p.Begin(ops)
	s := float32(size) / 2
	x, y := float32(cx), float32(cy)
	if open {
		p.MoveTo(f32.Pt(x-s, y-s/2))
		p.LineTo(f32.Pt(x, y+s/2))
		p.LineTo(f32.Pt(x+s, y-s/2))
	} else {
		p.MoveTo(f32.Pt(x-s/2, y-s))
		p.LineTo(f32.Pt(x+s/2, y))
		p.LineTo(f32.Pt(x-s/2, y+s))
	}
	paint.FillShape(ops, c, clip.Stroke{Path: p.End(), Width: width}.Op())
}

// cross draws an "x" glyph centred at (cx, cy).
func cross(ops *op.Ops, cx, cy, size int, width float32, c color.NRGBA) {
	s := float32(size) / 2
	x, y := float32(cx), float32(cy)
	var p clip.Path
	p.Begin(ops)
	p.MoveTo(f32.Pt(x-s, y-s))
	p.LineTo(f32.Pt(x+s, y+s))
	p.MoveTo(f32.Pt(x+s, y-s))
	p.LineTo(f32.Pt(x-s, y+s))
	paint.FillShape(ops, c, clip.Stroke{Path: p.End(), Width: width}.Op())
}

func dot(ops *op.Ops, cx, cy, r int, c color.NRGBA) {
	rrect(ops, image.Rect(cx-r, cy-r, cx+r, cy+r), r, c)
}

// plus draws a "+" centred at (cx, cy).
func plus(ops *op.Ops, cx, cy, size int, width float32, c color.NRGBA) {
	s := float32(size) / 2
	x, y := float32(cx), float32(cy)
	var p clip.Path
	p.Begin(ops)
	p.MoveTo(f32.Pt(x-s, y))
	p.LineTo(f32.Pt(x+s, y))
	p.MoveTo(f32.Pt(x, y-s))
	p.LineTo(f32.Pt(x, y+s))
	paint.FillShape(ops, c, clip.Stroke{Path: p.End(), Width: width}.Op())
}
