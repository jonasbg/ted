package main

import (
	"image"
	"image/color"
	"strconv"
	"strings"
	"time"
	"unicode"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
)

func (a *App) drawEditor(gtx layout.Context, b *Buffer) {
	ops := gtx.Ops
	u := &a.ui
	th := a.th
	r := u.editor
	lineH, charW := u.lineH, u.charW

	digits := max(3, len(strconv.Itoa(len(b.lines))))
	gutterW := (digits+3)*charW + gtx.Dp(6)
	u.gutter = image.Rect(r.Min.X, r.Min.Y, r.Min.X+gutterW, r.Max.Y)
	u.text = image.Rect(r.Min.X+gutterW, r.Min.Y, r.Max.X, r.Max.Y)
	minimap := a.showMinimap(gtx, r.Dx())
	if minimap {
		u.text.Max.X -= gtx.Dp(minimapDp)
	}
	viewH := r.Dy()
	textW := u.text.Dx() - u.padLeft - gtx.Dp(14)
	maxVy := float32(max(0, (len(b.lines)-1)*lineH))

	// the widest line bounds horizontal scrolling
	maxVx := float32(max(0, b.maxCols()*charW+8*charW-textW))
	a.editorPointer(gtx, b, maxVy, maxVx)

	// scroll to keep the caret in view
	if b.centerOnCursor {
		b.centerOnCursor = false
		b.vy = float32(b.cur.L*lineH - viewH/2 + lineH)
		a.follow = true
	}
	if a.follow {
		a.follow = false
		margin := float32(min(3*lineH, viewH/4))
		cy := float32(b.cur.L * lineH)
		if cy < b.vy+margin {
			b.vy = cy - margin
		}
		if cy+float32(lineH)+float32(u.padTop) > b.vy+float32(viewH)-margin {
			b.vy = cy + float32(lineH+u.padTop) - float32(viewH) + margin
		}
		cx := float32(visCol(b.lines[b.cur.L], b.cur.C) * charW)
		if cx < b.vx {
			b.vx = max(0, cx-float32(8*charW))
		}
		if cx > b.vx+float32(textW-2*charW) {
			b.vx = cx - float32(textW) + float32(8*charW)
		}
	}
	b.vy = max(0, min(b.vy, maxVy))
	b.vx = max(0, min(b.vx, maxVx))
	if b != a.lastScrollBuf || b.vy != a.lastVy {
		if b == a.lastScrollBuf {
			a.scrollAt = gtx.Now // the view moved: show scroll indicators
		}
		a.lastScrollBuf, a.lastVy = b, b.vy
	}
	first := int(b.vy) / lineH
	off := int(b.vy) % lineH
	b.scrollY = first
	visible := viewH/lineH + 2

	// pointer target over the whole editor
	st := clip.Rect(r).Push(ops)
	event.Op(ops, &u.editorTag)
	pointer.CursorText.Add(ops)
	st.Pop()

	editorFocused := a.focus == focusEditor && a.overlay == nil
	hasSel := b.hasSel()
	var selS, selE Pos
	if hasSel {
		selS, selE = b.selRange()
	}
	// what to highlight as "matches"
	var q []rune
	if a.find != nil && len(a.find.in.r) > 0 {
		q = a.find.in.r
	} else if hasSel && selS.L == selE.L && selE.C-selS.C <= 200 {
		t := b.selText()
		word := true
		for _, r := range t {
			if !isWordRune(r) {
				word = false
			}
		}
		if word {
			q = t
		}
	}
	cs := caseSensitive(q)
	// bracket match
	var brs []Pos
	if !hasSel {
		for _, c := range []int{b.cur.C, b.cur.C - 1} {
			if p, ok := matchBracket(b, Pos{b.cur.L, c}); ok {
				brs = []Pos{{b.cur.L, c}, p}
				break
			}
		}
	}
	iw := tabW
	if b.indent != "\t" {
		iw = max(1, len(b.indent))
	}

	xs := u.text.Min.X + u.padLeft - int(b.vx)
	colX := func(vc int) int { return xs + vc*charW }
	c0 := int(b.vx)/charW - 2
	c1 := c0 + textW/charW + 6

	ts := clip.Rect(u.text).Push(ops)
	for i := 0; i < visible; i++ {
		li := first + i
		if li >= len(b.lines) {
			break
		}
		line := b.lines[li]
		y := r.Min.Y + u.padTop + i*lineH - off
		baseline := y + (lineH-(u.codeAsc+u.codeDesc))/2 + u.codeAsc
		row := image.Rect(u.text.Min.X, y, u.text.Max.X, y+lineH)

		if li == b.cur.L && !hasSel && editorFocused {
			fill(ops, row, th.curLine)
		}
		if b.diffView {
			if c := diffLineBg(th, line); c.A != 0 {
				fill(ops, row, c)
			}
		}
		// indent guides
		ind := indentCols(b, li)
		for k := 0; k*iw < ind; k++ {
			gx := colX(k * iw)
			if gx >= u.text.Min.X {
				fill(ops, image.Rect(gx, y, gx+1, y+lineH), th.indentGuide)
			}
		}
		// selection
		if hasSel && li >= selS.L && li <= selE.L {
			sc, ec := 0, len(line)
			if li == selS.L {
				sc = selS.C
			}
			if li == selE.L {
				ec = selE.C
			}
			x0, x1 := colX(visCol(line, sc)), colX(visCol(line, ec))
			if li < selE.L {
				x1 += charW / 2
			}
			col := th.selection
			if !editorFocused && a.focus != focusFind {
				col = th.selInactive
			}
			fill(ops, image.Rect(x0, y, x1, y+lineH), col)
		}
		// CSV cell block
		if b.csv != nil {
			for _, f := range b.blockCells(li) {
				x0, x1 := colX(visCol(line, f.start)), colX(visCol(line, f.end))
				if x1 == x0 {
					x1 += charW / 2 // keep empty cells visible
				}
				fill(ops, image.Rect(x0, y, x1, y+lineH), th.selection)
			}
		}
		// search matches
		if len(q) > 0 {
			for _, m := range matchesInLine(line, q, cs) {
				p := Pos{li, m}
				if hasSel && p == selS {
					if a.find != nil {
						strokeRRect(ops, image.Rect(colX(visCol(line, m)), y+1, colX(visCol(line, m+len(q))), y+lineH-1), 2, float32(gtx.Dp(1)), th.matchCur)
					}
					continue
				}
				fill(ops, image.Rect(colX(visCol(line, m)), y+1, colX(visCol(line, m+len(q))), y+lineH-1), th.match)
			}
		}
		for _, p := range brs {
			if p.L == li {
				x := colX(visCol(line, p.C))
				strokeRRect(ops, image.Rect(x, y+1, x+charW, y+lineH-1), 2, float32(gtx.Dp(1)), th.bracket)
			}
		}
		a.drawLineText(gtx, b, li, line, baseline, colX, c0, c1)
	}
	// caret
	u.caret = image.Rectangle{}
	if cy := b.cur.L - first; cy >= 0 && cy < visible {
		y := r.Min.Y + u.padTop + cy*lineH - off
		x := colX(visCol(b.lines[b.cur.L], b.cur.C))
		u.caret = image.Rect(x, y, x+max(2, gtx.Dp(2)), y+lineH)
		if editorFocused && a.caretVisible(gtx) {
			fill(ops, u.caret, th.caret)
		} else if a.focus == focusFind && a.find != nil {
			// keep a faint caret so you know where you are
			fill(ops, u.caret, withA(th.caret, 0x60))
		}
	}
	ts.Pop()

	// gutter
	g := u.gutter
	fill(ops, g, th.bg)
	for i := 0; i < visible; i++ {
		li := first + i
		if li >= len(b.lines) {
			break
		}
		y := r.Min.Y + u.padTop + i*lineH - off
		baseline := y + (lineH-(u.codeAsc+u.codeDesc))/2 + u.codeAsc
		num := strconv.Itoa(li + 1)
		col := th.gutter
		if li == b.cur.L {
			col = th.gutterCur
		}
		x := g.Max.X - gtx.Dp(14) - len(num)*charW
		a.tc.draw(ops, fMono, u.codePx, x, baseline, num, col)
	}
	// shadow at the gutter edge when scrolled horizontally
	if b.vx > 0 {
		for k := 0; k < gtx.Dp(6); k++ {
			fill(ops, image.Rect(g.Max.X+k, r.Min.Y, g.Max.X+k+1, r.Max.Y), color.NRGBA{0, 0, 0, uint8(40 - k*40/gtx.Dp(6))})
		}
	}

	if minimap {
		a.drawMinimap(gtx, b, maxVy)
	} else {
		a.drawScrollbar(gtx, &b.vy, maxVy)
	}
	a.scheduleHighlight(b, visible)

	// keep auto-scrolling while dragging outside the text
	if u.dragEditor {
		if u.dragPos.Y < float32(r.Min.Y) || u.dragPos.Y > float32(r.Max.Y) {
			gtx.Execute(op.InvalidateCmd{At: gtx.Now.Add(16 * time.Millisecond)})
		}
	}
}

// indentCols is the indentation (in columns) used for indent guides; blank
// lines borrow it from their neighbours so guides stay continuous.
func indentCols(b *Buffer, li int) int {
	line := b.lines[li]
	ws := leadingWS(line)
	if len(ws) < len(line) {
		return visCol(line, len(ws))
	}
	prev, next := 0, 0
	for k := li - 1; k >= 0 && k > li-100; k-- {
		if l := b.lines[k]; len(leadingWS(l)) < len(l) {
			prev = visCol(l, len(leadingWS(l)))
			break
		}
	}
	for k := li + 1; k < len(b.lines) && k < li+100; k++ {
		if l := b.lines[k]; len(leadingWS(l)) < len(l) {
			next = visCol(l, len(leadingWS(l)))
			break
		}
	}
	return min(prev, next)
}

// drawLineText draws one line's text, grouped into runs of equal style.
func (a *App) drawLineText(gtx layout.Context, b *Buffer, li int, line []rune, baseline int, colX func(int) int, c0, c1 int) {
	ops := gtx.Ops
	u := &a.ui
	def := tokStyle{fg: a.th.fg}
	spans := a.spansFor(b, li)
	si := 0
	var seg []rune
	segStart := -1
	var segSt tokStyle
	flush := func() {
		if len(seg) > 0 {
			a.tc.draw(ops, monoFace(segSt), u.codePx, colX(segStart), baseline, string(seg), segSt.fg)
		}
		seg = seg[:0]
		segStart = -1
	}
	vc := 0
	for ci, r := range line {
		w := runeW(r)
		if r == '\t' {
			w = tabW - vc%tabW
		}
		if vc+w < c0 {
			vc += w
			continue
		}
		if vc > c1 {
			break
		}
		st := def
		for si+1 < len(spans) && spans[si+1].start <= ci {
			si++
		}
		if si < len(spans) && spans[si].start <= ci {
			st = spans[si].st
		}
		if segStart < 0 || st != segSt {
			flush()
			segStart, segSt = vc, st
		}
		switch {
		case r == '\t':
			flush()
		case r < 32 || r == 0x7f:
			flush()
			a.tc.draw(ops, fMono, u.codePx, colX(vc), baseline, "?", a.th.errorFg)
		case w > 1 || unicode.Is(unicode.Mn, r):
			// draw wide runes on their own so the grid stays aligned
			flush()
			a.tc.draw(ops, monoFace(st), u.codePx, colX(vc), baseline, string(r), st.fg)
		default:
			seg = append(seg, r)
		}
		vc += w
		if segStart < 0 {
			segStart, segSt = vc, st
		}
	}
	flush()
}

func (a *App) drawScrollbar(gtx layout.Context, vy *float32, maxVy float32) {
	ops := gtx.Ops
	u := &a.ui
	r := u.editor
	viewH := float32(r.Dy())
	contentH := maxVy + viewH
	u.thumb = image.Rectangle{}
	if maxVy <= 0 {
		return
	}
	trackW := gtx.Dp(12)
	track := image.Rect(r.Max.X-trackW, r.Min.Y, r.Max.X, r.Max.Y)
	thumbH := max(float32(gtx.Dp(28)), viewH*viewH/contentH)
	ty := float32(r.Min.Y) + *vy/maxVy*(viewH-thumbH)

	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &u.thumbTag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Enter | pointer.Leave})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		switch e.Kind {
		case pointer.Enter:
			u.hoverThumb = true
		case pointer.Leave:
			u.hoverThumb = false
		case pointer.Press:
			u.dragThumb = true
			if e.Position.Y >= ty && e.Position.Y <= ty+thumbH {
				u.dragThumbOff = e.Position.Y - ty
			} else {
				u.dragThumbOff = thumbH / 2
			}
			fallthrough
		case pointer.Drag:
			if u.dragThumb {
				p := (e.Position.Y - u.dragThumbOff - float32(r.Min.Y)) / (viewH - thumbH)
				*vy = max(0, min(maxVy, p*maxVy))
				ty = float32(r.Min.Y) + *vy/maxVy*(viewH-thumbH)
			}
		case pointer.Release:
			u.dragThumb = false
		}
	}
	th := a.th
	alpha := float32(0x38)
	if u.dragThumb {
		alpha = 0x70
	}
	u.thumb = image.Rect(track.Min.X+gtx.Dp(3), int(ty), track.Max.X-gtx.Dp(3), int(ty+thumbH))
	// hidden until you scroll or hover over it
	if vis := a.scrollVisibility(gtx, u.hoverThumb || u.dragThumb); vis > 0 {
		rrect(ops, u.thumb, gtx.Dp(3), withA(th.fg, uint8(alpha*vis)))
	}
	st := clip.Rect(track).Push(ops)
	pointer.CursorDefault.Add(ops)
	event.Op(ops, &u.thumbTag)
	st.Pop()
}

// posAtPoint maps a window coordinate to a buffer position.
func (a *App) posAtPoint(b *Buffer, p f32.Point) Pos {
	u := &a.ui
	ly := p.Y - float32(u.editor.Min.Y+u.padTop) + b.vy
	l := int(ly) / u.lineH
	if ly < 0 {
		l = 0
		if b.vy == 0 {
			return Pos{0, 0}
		}
	}
	if l >= len(b.lines) {
		l = len(b.lines) - 1
		return Pos{l, len(b.lines[l])}
	}
	vc := (p.X - float32(u.text.Min.X+u.padLeft) + b.vx) / float32(u.charW)
	if vc < 0 {
		vc = 0
	}
	return Pos{l, colFromVisRound(b.lines[l], vc)}
}

func (a *App) editorPointer(gtx layout.Context, b *Buffer, maxVy, maxVx float32) {
	u := &a.ui
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &u.editorTag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Scroll | pointer.Cancel,
			ScrollX: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}, ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		switch e.Kind {
		case pointer.Scroll:
			// Lock to the dominant axis so a vertical swipe doesn't drift
			// sideways and a sideways swipe doesn't nudge the view up or down.
			dx, dy := e.Scroll.X, e.Scroll.Y
			if e.Modifiers.Contain(key.ModShift) && dx == 0 {
				dx, dy = dy, 0 // shift+wheel scrolls sideways on plain mice
			}
			if abs32(dx) > 2*abs32(dy) {
				dy = 0
			} else {
				dx = 0
			}
			b.vy = max(0, min(maxVy, b.vy+dy))
			if dx != 0 {
				// Only as far as the lines on screen overflow, so a stray
				// swipe does nothing when everything visible already fits.
				// Scrolling back left is always allowed.
				limit := min(maxVx, a.visibleOverflow(b))
				if dx < 0 {
					limit = max(limit, b.vx)
				}
				b.vx = max(0, min(limit, b.vx+dx))
			}
		case pointer.Press:
			if e.Buttons != pointer.ButtonPrimary {
				continue
			}
			a.focus = focusEditor
			a.blinkStart = gtx.Now
			a.editorClick(b, e)
		case pointer.Drag:
			if u.dragEditor {
				u.dragPos = e.Position
				a.editorDrag(b)
			}
		case pointer.Release, pointer.Cancel:
			u.dragEditor = false
		}
	}
	// auto-scroll while the drag is held outside the view
	if u.dragEditor {
		r := u.editor
		step := float32(u.lineH) / 2
		if u.dragPos.Y < float32(r.Min.Y) {
			b.vy = max(0, b.vy-step*(1+(float32(r.Min.Y)-u.dragPos.Y)/20))
			a.editorDrag(b)
		} else if u.dragPos.Y > float32(r.Max.Y) {
			b.vy = min(maxVy, b.vy+step*(1+(u.dragPos.Y-float32(r.Max.Y))/20))
			a.editorDrag(b)
		}
	}
}

func (a *App) editorClick(b *Buffer, e pointer.Event) {
	u := &a.ui
	p := a.posAtPoint(b, e.Position)
	now := time.Now()
	if now.Sub(u.lastClick) < 450*time.Millisecond && u.lastClickPos.L == p.L && abs(u.lastClickPos.C-p.C) <= 1 {
		u.clickCount++
	} else {
		u.clickCount = 1
	}
	u.lastClick, u.lastClickPos = now, p
	b.wantCol = -1
	b.breakUndo()
	u.dragEditor = true
	u.dragPos = e.Position
	inGutter := e.Position.X < float32(u.gutter.Max.X)
	u.dragBlock = false
	if b.csv != nil {
		// Alt+click selects cells (a header cell takes the whole column);
		// any other click drops the cell selection.
		if e.Modifiers.Contain(key.ModAlt) && !inGutter {
			b.startBlock(p, e.Modifiers.Contain(key.ModShift))
			u.dragBlock = true
			return
		}
		b.clearBlock()
	}
	switch {
	case inGutter:
		b.anchor = Pos{p.L, 0}
		if p.L+1 < len(b.lines) {
			b.cur = Pos{p.L + 1, 0}
		} else {
			b.cur = Pos{p.L, len(b.lines[p.L])}
		}
		b.selecting = true
		u.clickCount = 3
	case e.Modifiers.Contain(key.ModShift):
		if !b.hasSel() {
			b.anchor = b.cur
		}
		b.cur = p
		b.selecting = true
	case u.clickCount == 2:
		s, en := b.wordBounds(p)
		b.anchor, b.cur = Pos{p.L, s}, Pos{p.L, en}
		b.selecting = true
	case u.clickCount >= 3:
		b.anchor = Pos{p.L, 0}
		if p.L+1 < len(b.lines) {
			b.cur = Pos{p.L + 1, 0}
		} else {
			b.cur = Pos{p.L, len(b.lines[p.L])}
		}
		b.selecting = true
	default:
		b.cur, b.anchor = p, p
		b.selecting = true // becomes a real selection once dragged
	}
}

func (a *App) editorDrag(b *Buffer) {
	u := &a.ui
	p := a.posAtPoint(b, u.dragPos)
	if u.dragBlock && b.csv != nil {
		b.dragBlock(p)
		return
	}
	switch {
	case u.clickCount == 2:
		s, e := b.wordBounds(p)
		if p.Less(b.anchor) {
			b.cur = Pos{p.L, s}
		} else {
			b.cur = Pos{p.L, e}
		}
	case u.clickCount >= 3:
		if p.Less(b.anchor) {
			b.cur = Pos{p.L, 0}
		} else if p.L+1 < len(b.lines) {
			b.cur = Pos{p.L + 1, 0}
		} else {
			b.cur = Pos{p.L, len(b.lines[p.L])}
		}
	default:
		b.cur = p
	}
	b.selecting = true
}

// reportIME tells the platform text-input system where the caret is, so
// input methods, dead keys and accent menus work.
func (a *App) reportIME(gtx layout.Context) {
	b := a.buf()
	if b == nil || a.overlay != nil || a.focus != focusEditor || b.previewing() || b.image != nil {
		return
	}
	anc := b.cur
	if b.hasSel() {
		anc = b.anchor
	}
	sel := key.Range{Start: b.offsetOf(anc), End: b.offsetOf(b.cur)}
	c := a.ui.caret
	caret := key.Caret{Pos: f32.Pt(float32(c.Min.X), float32(c.Max.Y-a.ui.codeDesc)), Ascent: float32(a.ui.codeAsc), Descent: float32(a.ui.codeDesc)}
	if sel != a.imeSel {
		a.imeSel = sel
		gtx.Execute(key.SelectionCmd{Tag: &a.keyTag, Range: sel, Caret: caret})
	}
	ls := b.offsetOf(Pos{b.cur.L, 0})
	sn := key.Snippet{Range: key.Range{Start: ls, End: ls + len(b.lines[b.cur.L])}, Text: string(b.lines[b.cur.L])}
	if sn != a.imeSnippet {
		a.imeSnippet = sn
		gtx.Execute(key.SnippetCmd{Tag: &a.keyTag, Snippet: sn})
	}
}

func abs32(x float32) float32 {
	if x < 0 {
		return -x
	}
	return x
}

// diffLineBg tints added, removed and hunk-header lines in diff views.
func diffLineBg(th *Theme, line []rune) color.NRGBA {
	if len(line) == 0 {
		return color.NRGBA{}
	}
	s := string(line[:min(4, len(line))])
	switch {
	case line[0] == '+' && !strings.HasPrefix(s, "+++"):
		return th.diffAdd
	case line[0] == '-' && !strings.HasPrefix(s, "---"):
		return th.diffDel
	case strings.HasPrefix(s, "@@"):
		return th.diffHunk
	}
	return color.NRGBA{}
}

// visibleOverflow is how far the widest line on screen runs past the
// right edge of the text area, in pixels; 0 when every visible line fits.
func (a *App) visibleOverflow(b *Buffer) float32 {
	u := &a.ui
	lineH, charW := max(1, u.lineH), u.charW
	b.maxCols() // make sure lineWidths is filled
	first := max(0, int(b.vy)/lineH)
	last := min(len(b.lineWidths), first+u.text.Dy()/lineH+2)
	widest := 0
	for i := first; i < last; i++ {
		widest = max(widest, b.lineWidths[i])
	}
	textW := u.text.Dx() - u.padLeft - charW
	return float32(max(0, widest*charW+2*charW-textW))
}
