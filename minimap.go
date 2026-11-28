package main

import (
	"image"
	"image/color"
	"time"
	"unicode"

	"gioui.org/io/event"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// The minimap replaces the scrollbar: a miniature of the whole file with a
// box marking the visible part. Click to jump, drag the box to scroll.

const minimapDp = 96

type minimapState struct {
	tag      int
	rect     image.Rectangle
	dragging bool
	dragOff  float32
	hover    bool

	// cached rasterized minimap image, re-rendered when any input changes
	imgOp paint.ImageOp
	imgSz image.Point
	key   minimapKey
	valid bool
}

type minimapKey struct {
	buf             *Buffer
	ver, hlVer, hlF int
	first, n        int
	w, h, lh, cw    int
	theme           *Theme
	query           string
}

// showMinimap reports whether there is room for a minimap in an editor of
// width w (in px).
func (a *App) showMinimap(gtx layout.Context, w int) bool {
	return w >= gtx.Dp(480)
}

func (a *App) drawMinimap(gtx layout.Context, b *Buffer, maxVy float32) {
	ops := gtx.Ops
	u := &a.ui
	th := a.th
	m := &a.mm
	r := image.Rect(u.text.Max.X, u.editor.Min.Y, u.editor.Max.X, u.editor.Max.Y)
	m.rect = r
	lh := max(2, gtx.Dp(2)) // minimap line height
	cw := max(1, gtx.Dp(1)) // minimap column width
	n := len(b.lines)
	viewH := float32(r.Dy())
	contentH := float32(n * lh)
	// When the file is taller than the minimap, the minimap scrolls too,
	// in proportion to the editor.
	mmTop := float32(0)
	if contentH > viewH && maxVy > 0 {
		mmTop = b.vy / maxVy * (contentH - viewH)
	}
	k := float32(lh) / float32(u.lineH) // minimap px per editor px

	sliderH := float32(u.editor.Dy()) * k
	// vy for a given slider top: sliderTop is linear in vy, sliderTop = vy*slope
	slope := k
	if contentH > viewH && maxVy > 0 {
		slope = k - (contentH-viewH)/maxVy
	}

	sliderTop := func() float32 { return b.vy * slope }
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &m.tag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Enter | pointer.Leave | pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		y := e.Position.Y - float32(r.Min.Y)
		setTop := func(top float32) {
			if slope > 0 {
				b.vy = max(0, min(maxVy, top/slope))
			}
		}
		switch e.Kind {
		case pointer.Enter:
			m.hover = true
		case pointer.Leave:
			m.hover = false
		case pointer.Scroll:
			b.vy = max(0, min(maxVy, b.vy+e.Scroll.Y))
		case pointer.Press:
			if e.Buttons != pointer.ButtonPrimary {
				continue
			}
			st := sliderTop()
			if y >= st && y <= st+sliderH {
				m.dragOff = y - st
			} else {
				// jump: centre the clicked line in the editor
				line := (y + mmTop) / float32(lh)
				b.vy = max(0, min(maxVy, line*float32(u.lineH)-float32(u.editor.Dy())/2))
				m.dragOff = sliderH / 2
				if slope > 0 {
					m.dragOff = y - sliderTop()
				}
			}
			m.dragging = true
			a.follow = false
		case pointer.Drag:
			if m.dragging {
				setTop(y - m.dragOff)
			}
		case pointer.Release, pointer.Cancel:
			m.dragging = false
		}
	}
	// recompute with the (possibly) new scroll position
	if contentH > viewH && maxVy > 0 {
		mmTop = b.vy / maxVy * (contentH - viewH)
	}
	first := int(mmTop) / lh
	off := int(mmTop) % lh
	rows := r.Dy()/lh + 2

	var findQ string
	var findRunes []rune
	if a.find != nil && len(a.find.in.r) > 0 {
		findRunes = a.find.in.r
		findQ = string(findRunes)
	}
	st := clip.Rect(r).Push(ops)
	key := minimapKey{b, b.version, b.hl.version, b.hl.from, first, n, r.Dx(), r.Dy(), lh, cw, th, findQ}
	if !m.valid || m.key != key {
		img := a.rasterMinimap(b, first, rows, r.Dx(), lh, cw, findRunes)
		m.imgOp = paint.NewImageOp(img)
		m.imgOp.Filter = paint.FilterNearest
		m.imgSz = img.Bounds().Size()
		m.key, m.valid = key, true
	}
	t := op.Offset(image.Pt(r.Min.X, r.Min.Y-off)).Push(ops)
	cl := clip.Rect(image.Rectangle{Max: m.imgSz}).Push(ops)
	m.imgOp.Add(ops)
	paint.PaintOp{}.Add(ops)
	cl.Pop()
	t.Pop()

	// caret line
	cy := r.Min.Y + b.cur.L*lh - int(mmTop)
	fill(ops, image.Rect(r.Min.X, cy, r.Max.X, cy+lh), withA(th.accent, 0xc0))
	// visible region: only while scrolling or hovering, like overlay scrollbars
	vis := a.scrollVisibility(gtx, m.hover || m.dragging)
	if vis > 0 {
		alpha := float32(0x1c)
		if m.hover {
			alpha = 0x26
		}
		if m.dragging {
			alpha = 0x36
		}
		top := r.Min.Y + int(sliderTop())
		fill(ops, image.Rect(r.Min.X, top, r.Max.X, top+int(sliderH)), withA(th.fg, uint8(alpha*vis)))
	}

	pointer.CursorDefault.Add(ops)
	event.Op(ops, &m.tag)
	st.Pop()
}

func premulRGBA(c color.NRGBA) [4]byte {
	a := uint32(c.A)
	return [4]byte{byte(uint32(c.R) * a / 255), byte(uint32(c.G) * a / 255), byte(uint32(c.B) * a / 255), c.A}
}

func fillPix(img *image.RGBA, x0, y0, x1, y1 int, px [4]byte) {
	b := img.Bounds()
	x0 = max(b.Min.X, x0)
	y0 = max(b.Min.Y, y0)
	x1 = min(b.Max.X, x1)
	y1 = min(b.Max.Y, y1)
	if x0 >= x1 || y0 >= y1 {
		return
	}
	for y := y0; y < y1; y++ {
		off := img.PixOffset(x0, y)
		for x := x0; x < x1; x++ {
			img.Pix[off] = px[0]
			img.Pix[off+1] = px[1]
			img.Pix[off+2] = px[2]
			img.Pix[off+3] = px[3]
			off += 4
		}
	}
}

// rasterMinimap renders rows starting at line first into a single RGBA image
// instead of emitting thousands of individual vector draw ops.
func (a *App) rasterMinimap(b *Buffer, first, rows, w, lh, cw int, findQ []rune) *image.RGBA {
	th := a.th
	imgW := max(1, w)
	imgH := max(1, rows*lh)
	img := image.NewRGBA(image.Rect(0, 0, imgW, imgH))
	padX := min(6, imgW/4)
	maxCols := max(1, (imgW-padX-2)/cw)
	blockH := max(1, lh-1)
	cs := caseSensitive(findQ)
	matchPx := premulRGBA(withA(th.orange, 0xd8))

	for i := 0; i < rows; i++ {
		li := first + i
		if li >= len(b.lines) {
			break
		}
		line := b.lines[li]
		y := i * lh
		if b.diffView {
			if c := diffLineBg(th, line); c.A != 0 {
				fillPix(img, 0, y, imgW, y+lh, premulRGBA(c))
			}
		}
		spans := a.spansFor(b, li)
		si := 0
		col := 0
		runStart := -1
		var runCol color.NRGBA
		flush := func(end int) {
			if runStart >= 0 && end > runStart {
				fillPix(img, padX+runStart*cw, y, padX+end*cw, y+blockH, premulRGBA(withA(runCol, 0x9a)))
			}
			runStart = -1
		}
		for ci, r := range line {
			if col >= maxCols {
				break
			}
			wdt := 1
			if r == '\t' {
				wdt = tabW - col%tabW
			} else if runeW(r) > 1 {
				wdt = 2
			}
			c := th.fg
			for si+1 < len(spans) && spans[si+1].start <= ci {
				si++
			}
			if si < len(spans) && spans[si].start <= ci {
				c = spans[si].st.fg
			}
			if unicode.IsSpace(r) {
				flush(col)
			} else if runStart < 0 || c != runCol {
				flush(col)
				runStart, runCol = col, c
			}
			col += wdt
		}
		flush(min(col, maxCols))
		if len(findQ) > 0 && hasMatchInLine(line, findQ, cs) {
			// Subtle match marker tick at the right edge of the minimap.
			fillPix(img, imgW-max(4, cw*4), y, imgW, y+max(lh, 2), matchPx)
		}
	}
	return img
}

func hasMatchInLine(line, q []rune, cs bool) bool {
	if len(q) == 0 || len(q) > len(line) {
		return false
	}
	// Cap scan length on extremely long lines to keep minimap rasterization fast.
	limit := min(len(line), 8000)
outer:
	for i := 0; i+len(q) <= limit; i++ {
		for j, r := range q {
			c := line[i+j]
			if c != r && (cs || unicode.ToLower(c) != unicode.ToLower(r)) {
				continue outer
			}
		}
		return true
	}
	return false
}

// scrollVisibility returns how visible (0 to 1) scroll indicators should be:
// fully while hovered or for a moment after scrolling, then fading out.
func (a *App) scrollVisibility(gtx layout.Context, hovered bool) float32 {
	const hold, fade = 900 * time.Millisecond, 300 * time.Millisecond
	if hovered {
		return 1
	}
	el := gtx.Now.Sub(a.scrollAt)
	switch {
	case el < hold:
		gtx.Execute(op.InvalidateCmd{At: a.scrollAt.Add(hold)})
		return 1
	case el < hold+fade:
		gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(16 * time.Millisecond)})
		return 1 - float32(el-hold)/float32(fade)
	}
	return 0
}
