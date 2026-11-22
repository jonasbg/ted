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

	// cached drawing, re-recorded when any input changes
	ops   op.Ops
	call  op.CallOp
	key   minimapKey
	valid bool
}

type minimapKey struct {
	buf             *Buffer
	ver, hlVer, hlF int
	first, n        int
	w, h, lh, cw    int
	theme           *Theme
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
		ev, ok := gtx.Event(pointer.Filter{Target: &m.tag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Enter | pointer.Leave | pointer.Scroll,
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
		case pointer.Release:
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

	st := clip.Rect(r).Push(ops)
	key := minimapKey{b, b.version, b.hl.version, b.hl.from, first, n, r.Dx(), r.Dy(), lh, cw, th}
	if !m.valid || m.key != key {
		m.ops.Reset()
		rec := op.Record(&m.ops)
		a.paintMinimapLines(&m.ops, b, first, rows, r.Dx(), lh, cw)
		m.call = rec.Stop()
		m.key, m.valid = key, true
	}
	t := op.Offset(image.Pt(r.Min.X+gtx.Dp(6), r.Min.Y-off)).Push(ops)
	m.call.Add(ops)
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

// paintMinimapLines draws rows starting at line first as small blocks, one
// per run of same-coloured non-space characters.
func (a *App) paintMinimapLines(ops *op.Ops, b *Buffer, first, rows, w, lh, cw int) {
	th := a.th
	maxCols := (w - 8) / cw
	blockH := max(1, lh-1)
	for i := 0; i < rows; i++ {
		li := first + i
		if li >= len(b.lines) {
			break
		}
		line := b.lines[li]
		y := i * lh
		if b.diffView {
			if c := diffLineBg(th, line); c.A != 0 {
				fill(ops, image.Rect(-6, y, w, y+lh), c)
			}
		}
		spans := b.hl.lineSpans(li)
		si := 0
		col := 0
		runStart := -1
		var runCol color.NRGBA
		flush := func(end int) {
			if runStart >= 0 && end > runStart {
				paint.FillShape(ops, withA(runCol, 0x9a), clip.Rect(image.Rect(runStart*cw, y, end*cw, y+blockH)).Op())
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
	}
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
