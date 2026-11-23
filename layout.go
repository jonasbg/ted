package main

import (
	"fmt"
	"image"
	"image/color"
	"strings"
	"time"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"
)

// uiState holds geometry from the current frame plus pointer state.
type uiState struct {
	uiPx, codePx         int
	lineH, charW         int
	codeAsc, codeDesc    int
	uiAsc, uiDesc, rowH  int
	padTop, padLeft      int
	sidebar, tabs        image.Rectangle
	title, activity      image.Rectangle
	editor, gutter, text image.Rectangle
	status               image.Rectangle
	thumb                image.Rectangle
	tabRects, tabCloses  []image.Rectangle
	tabScroll            int

	treeVy     float32
	treeFollow bool
	hoverRow   int
	hoverTab   int
	hoverSplit bool

	clickCount   int
	lastClick    time.Time
	lastClickPos Pos
	dragEditor   bool
	dragBlock    bool // dragging out a CSV cell selection
	dragPos      f32.Point
	dragSplit    bool
	dragThumb    bool
	hoverThumb   bool
	dragThumbOff float32

	caret image.Rectangle

	statusItems []statusItem
	hoverStatus string

	clickWhat string
	clickIdx  int
	clickAt   time.Time

	editorTag, treeTag, tabsTag, splitTag, thumbTag, statusTag int
}

func (a *App) layout(gtx layout.Context) {
	ops := gtx.Ops
	u := &a.ui
	th := a.th
	W, H := gtx.Constraints.Max.X, gtx.Constraints.Max.Y

	u.uiPx = gtx.Sp(unit.Sp(13))
	u.codePx = gtx.Sp(unit.Sp(a.fontSp))
	u.codeAsc, u.codeDesc = a.tc.metrics(fMono, u.codePx)
	u.lineH = int(float32(u.codeAsc+u.codeDesc)*1.45 + 0.5)
	u.charW = max(1, a.tc.width(fMono, u.codePx, "0"))
	u.uiAsc, u.uiDesc = a.tc.metrics(fUI, u.uiPx)
	u.rowH = gtx.Dp(24)
	u.padTop = gtx.Dp(6)
	u.padLeft = gtx.Dp(10)

	fill(ops, image.Rect(0, 0, W, H), th.bg)

	// One focus target receives all keyboard and text input.
	area := clip.Rect(image.Rect(0, 0, W, H)).Push(ops)
	event.Op(ops, &a.keyTag)
	key.InputHintOp{Tag: &a.keyTag, Hint: key.HintAny}.Add(ops)
	area.Pop()
	if !gtx.Focused(&a.keyTag) {
		gtx.Execute(key.FocusCmd{Tag: &a.keyTag})
	}
	a.handleKeys(gtx)

	statusH := gtx.Dp(24)
	tabsH := gtx.Dp(36)
	// An empty title strip across the top holds the macOS window buttons
	// and drags the window; everything else starts below it.
	titleH := 0
	if a.lightsInset(gtx) > 0 {
		titleH = gtx.Dp(titleBarDp)
	}
	actW := gtx.Dp(48)
	sw := 0
	if a.showSidebar {
		sw = min(max(gtx.Dp(unit.Dp(a.sidebarDp)), gtx.Dp(140)), W*2/3)
	}
	u.title = image.Rect(0, 0, W, titleH)
	u.activity = image.Rect(0, titleH, actW, H-statusH)
	u.sidebar = image.Rect(actW, titleH, actW+sw, H-statusH)
	u.status = image.Rect(0, H-statusH, W, H)
	ex := actW + sw
	if a.showSidebar {
		ex += gtx.Dp(panelGapDp) // breathing room instead of a divider line
	}
	u.tabs = image.Rect(ex, titleH, W, titleH+tabsH)
	u.editor = image.Rect(ex, titleH+tabsH, W, H-statusH)

	if b := a.buf(); b != nil {
		if b.image != nil {
			a.drawImageView(gtx, b)
		} else if b.previewing() {
			a.drawMarkdown(gtx, b)
		} else {
			a.drawEditor(gtx, b)
		}
		a.drawTabs(gtx)
	} else {
		a.drawWelcome(gtx, image.Rect(ex, titleH, W, H-statusH))
	}
	if titleH > 0 {
		fill(ops, u.title, th.bg)
		ds := clip.Rect(u.title).Push(ops)
		system.ActionInputOp(system.ActionMove).Add(ops)
		ds.Pop()
	}
	a.drawActivityBar(gtx)
	if a.showSidebar {
		a.drawSidebar(gtx)
	}
	a.drawStatus(gtx)
	if b := a.buf(); a.find != nil && b != nil && (b.previewing() || b.image != nil) {
		a.closeFind() // e.g. switched to a preview or image tab
	}
	if a.find != nil && a.buf() != nil {
		a.drawFindBar(gtx)
	}
	if a.overlay != nil {
		a.drawOverlay(gtx)
	}
	a.reportIME(gtx)

	// caret blink
	if b := a.buf(); a.focused && a.focus == focusEditor && a.overlay == nil && b != nil && !b.previewing() && b.image == nil {
		const period = 530 * time.Millisecond
		el := gtx.Now.Sub(a.blinkStart)
		next := a.blinkStart.Add((el/period + 1) * period)
		gtx.Execute(op.InvalidateCmd{At: next})
	}
}

func (a *App) caretVisible(gtx layout.Context) bool {
	if !a.focused {
		return false
	}
	el := gtx.Now.Sub(a.blinkStart)
	return (el/(530*time.Millisecond))%2 == 0
}

// uiBaseline returns the baseline that vertically centres UI text in a row.
func (a *App) uiBaseline(top, h int) int {
	return top + (h-(a.ui.uiAsc+a.ui.uiDesc))/2 + a.ui.uiAsc
}

func (a *App) drawWelcome(gtx layout.Context, r image.Rectangle) {
	ops := gtx.Ops
	u := &a.ui
	th := a.th
	fill(ops, r, th.bg)
	sc := shortcutName()
	lines := [][2]string{
		{"Go to file or search", sc + "+K"},
		{"New file", sc + "+N"},
		{"Toggle file tree", sc + "+B"},
		{"All shortcuts", "F1"},
	}
	rowH := gtx.Dp(30)
	y := r.Min.Y + r.Dy()/2 - len(lines)*rowH/2
	cx := r.Min.X + r.Dx()/2
	big := gtx.Sp(28)
	title := "ted"
	tw := a.tc.width(fUIBold, big, title)
	a.tc.draw(ops, fUIBold, big, cx-tw/2, y-gtx.Dp(36), title, th.faint)
	for _, l := range lines {
		lw := a.tc.width(fUI, u.uiPx, l[0])
		a.tc.draw(ops, fUI, u.uiPx, cx-gtx.Dp(12)-lw, a.uiBaseline(y, rowH), l[0], th.dim)
		a.tc.draw(ops, fMono, u.uiPx, cx+gtx.Dp(12), a.uiBaseline(y, rowH), l[1], th.fg)
		y += rowH
	}
}

// ---- tabs ----

func (a *App) drawTabs(gtx layout.Context) {
	ops := gtx.Ops
	u := &a.ui
	th := a.th
	r := u.tabs
	fill(ops, r, th.tabBar)

	pad := gtx.Dp(12)
	closeW := gtx.Dp(18)

	widths := make([]int, len(a.bufs))
	total := 0
	for i, b := range a.bufs {
		widths[i] = pad + a.tc.width(tabFace(b), u.uiPx, b.name()) + gtx.Dp(8) + closeW + gtx.Dp(6)
		total += widths[i]
	}
	// keep the active tab in view
	x0 := 0
	for i := 0; i < a.active; i++ {
		x0 += widths[i]
	}
	if x0 < u.tabScroll {
		u.tabScroll = x0
	}
	if a.active >= 0 && x0+widths[a.active] > u.tabScroll+r.Dx() {
		u.tabScroll = x0 + widths[a.active] - r.Dx()
	}
	u.tabScroll = max(0, min(u.tabScroll, total-r.Dx()))

	// pointer
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &u.tabsTag, Kinds: pointer.Press | pointer.Move | pointer.Leave | pointer.Scroll,
			ScrollX: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}, ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		pt := image.Pt(int(e.Position.X), int(e.Position.Y))
		switch e.Kind {
		case pointer.Leave:
			u.hoverTab = -1
		case pointer.Move:
			u.hoverTab = -1
			for i, tr := range u.tabRects {
				if pt.In(tr) {
					u.hoverTab = i
				}
			}
		case pointer.Scroll:
			u.tabScroll += int(e.Scroll.X + e.Scroll.Y)
		case pointer.Press:
			for i, tr := range u.tabRects {
				if !pt.In(tr) {
					continue
				}
				if e.Buttons == pointer.ButtonTertiary || (i < len(u.tabCloses) && pt.In(u.tabCloses[i])) {
					a.requestClose(i)
				} else {
					if u.isDoubleClick(gtx.Now, "tab", i) {
						a.bufs[i].preview = false
					}
					a.active = i
					a.focus = focusEditor
				}
				break
			}
		}
	}
	st := clip.Rect(r).Push(ops)
	event.Op(ops, &u.tabsTag)
	u.tabRects = u.tabRects[:0]
	u.tabCloses = u.tabCloses[:0]
	x := r.Min.X - u.tabScroll
	for i, b := range a.bufs {
		tr := image.Rect(x, r.Min.Y, x+widths[i], r.Max.Y)
		u.tabRects = append(u.tabRects, tr)
		active := i == a.active
		fg := th.tabIdleFg
		// Tabs sit directly on the window background (one surface with the
		// title bar); the active one is marked by a soft pill and brighter text.
		if active {
			// a warm underline marks the active tab
			ul := image.Rect(tr.Min.X+gtx.Dp(8), r.Max.Y-gtx.Dp(2), tr.Max.X-gtx.Dp(8), r.Max.Y)
			rrect(ops, ul, gtx.Dp(1), th.orange)
			fg = th.fg
		} else if i == u.hoverTab {
			fg = mixC(th.tabIdleFg, th.fg, 0.5)
		}
		if b.conflict || b.diskGone {
			fg = th.errorFg
		}
		a.tc.draw(ops, tabFace(b), u.uiPx, x+pad, a.uiBaseline(r.Min.Y, r.Dy()), b.name(), fg)
		cx := tr.Max.X - gtx.Dp(6) - closeW/2
		cy := r.Min.Y + r.Dy()/2
		cr := image.Rect(cx-closeW/2, cy-closeW/2, cx+closeW/2, cy+closeW/2)
		u.tabCloses = append(u.tabCloses, cr)
		// tabs are clickable: show the hand cursor over them
		hc := clip.Rect(tr).Push(ops)
		pointer.CursorPointer.Add(ops)
		hc.Pop()
		switch {
		case i == u.hoverTab:
			rrect(ops, cr, gtx.Dp(4), mixC(th.bg, th.fg, 0.1))
			cross(ops, cx, cy, gtx.Dp(7), float32(gtx.Dp(1.4)), th.fg)
		case b.dirty():
			dot(ops, cx, cy, gtx.Dp(4), th.fg)
		case active:
			cross(ops, cx, cy, gtx.Dp(7), float32(gtx.Dp(1.4)), th.dim)
		}
		x += widths[i]
	}
	st.Pop()
}

// ---- status bar ----

func (a *App) drawStatus(gtx layout.Context) {
	ops := gtx.Ops
	u := &a.ui
	th := a.th
	r := u.status
	fill(ops, r, th.statusBg)
	bl := a.uiBaseline(r.Min.Y, r.Dy())
	px := gtx.Sp(12)
	gap := gtx.Dp(16)
	x := r.Min.X + gtx.Dp(12)
	if a.branch != "" {
		x += a.tc.draw(ops, fUI, px, x, bl, a.branch, th.statusFg) + gap
	}
	b := a.buf()
	if b != nil {
		label := a.rel(b.path)
		if b.path == "" {
			label = b.name()
		}
		x += a.tc.draw(ops, fUI, px, x, bl, label, th.statusFg)
		switch {
		case b.conflict:
			x += gtx.Dp(8)
			x += a.tc.draw(ops, fUIBold, px, x, bl, "changed on disk", th.errorFg)
		case b.diskGone:
			x += gtx.Dp(8)
			x += a.tc.draw(ops, fUIBold, px, x, bl, "deleted on disk", th.errorFg)
		}
	}
	// right side; the items are buttons, like VS Code's status bar
	var right, ids []string
	if b != nil {
		pos := fmt.Sprintf("Ln %d, Col %d", b.cur.L+1, visCol(b.lines[b.cur.L], b.cur.C)+1)
		if b.hasSel() {
			s, e := b.selRange()
			n := len(b.selText())
			if e.L > s.L {
				pos += fmt.Sprintf(" (%d lines, %d chars)", e.L-s.L+1, n)
			} else {
				pos += fmt.Sprintf(" (%d selected)", n)
			}
		}
		ind := "Tabs"
		if b.indent != "\t" {
			ind = fmt.Sprintf("Spaces: %d", len(b.indent))
		}
		eol := "LF"
		if b.crlf {
			eol = "CRLF"
		}
		lang := b.hl.lang
		if lang == "" {
			lang = "Plain Text"
		}
		right = []string{pos, ind, "UTF-8", eol, lang}
		ids = []string{"pos", "indent", "enc", "eol", "lang"}
		if b.csv != nil {
			// selection summary, else the column under the caret
			item := b.columnLabel()
			if st, ok := b.selectionStats(); ok {
				item = st.label()
			}
			right = append([]string{item}, right...)
			ids = append([]string{"csv"}, ids...)
		}
		if iv := b.image; iv != nil {
			right, ids = []string{iv.format, humanSize(b.size)}, []string{"imgfmt", "imgsize"}
			if iv.img != nil {
				right = append([]string{fmt.Sprintf("%d × %d", iv.orig.X, iv.orig.Y)}, right...)
				ids = append([]string{"imgdim"}, ids...)
			}
		}
		if b.previewing() {
			// the source's controls (indentation, line endings) would edit it
			right, ids = []string{"Source", lang}, []string{"md", "lang"}
		} else if isMarkdown(b) {
			right = append([]string{"Preview"}, right...)
			ids = append([]string{"md"}, ids...)
		}
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &u.statusTag, Kinds: pointer.Press | pointer.Move | pointer.Leave})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		pt := image.Pt(int(e.Position.X), int(e.Position.Y))
		hit := ""
		for _, it := range u.statusItems {
			if pt.In(it.r) {
				hit = it.id
			}
		}
		switch e.Kind {
		case pointer.Leave:
			u.hoverStatus = ""
		case pointer.Move:
			u.hoverStatus = hit
		case pointer.Press:
			if hit != "" {
				a.statusClick(hit)
			}
		}
	}
	u.statusItems = u.statusItems[:0]
	ss := clip.Rect(r).Push(ops)
	event.Op(ops, &u.statusTag)
	ss.Pop()
	rx := r.Max.X - gtx.Dp(12)
	for i := len(right) - 1; i >= 0; i-- {
		w := a.tc.width(fUI, px, right[i])
		rx -= w
		if rx < x+gap {
			break
		}
		ir := image.Rect(rx-gtx.Dp(6), r.Min.Y+gtx.Dp(3), rx+w+gtx.Dp(6), r.Max.Y-gtx.Dp(3))
		col := th.statusFg
		if u.hoverStatus == ids[i] {
			rrect(ops, ir, gtx.Dp(4), mixC(th.statusBg, th.fg, 0.1))
			col = th.fg
		}
		a.tc.draw(ops, fUI, px, rx, bl, right[i], col)
		u.statusItems = append(u.statusItems, statusItem{ids[i], ir})
		hc := clip.Rect(ir).Push(ops)
		pointer.CursorPointer.Add(ops)
		hc.Pop()
		rx -= gap
	}
	if a.msg != "" {
		col := th.accent
		f := fUI
		if a.msgErr {
			col, f = th.errorFg, fUIBold
		}
		avail := rx - x - gap*2
		if avail > gtx.Dp(60) {
			a.tc.draw(ops, f, px, x+gap*2, bl, a.tc.truncate(f, px, avail, a.msg), col)
		}
	}
}

// ---- sidebar ----

func (a *App) drawSidebar(gtx layout.Context) {
	ops := gtx.Ops
	u := &a.ui
	th := a.th
	t := a.tree
	r := u.sidebar
	fill(ops, r, th.sidebar)
	headerH := gtx.Dp(36)
	rowH := u.rowH
	list := image.Rect(r.Min.X, r.Min.Y+headerH, r.Max.X, r.Max.Y)
	viewH := list.Dy()

	// keep keyboard selection in view
	if u.treeFollow {
		u.treeFollow = false
		top := float32(t.sel * rowH)
		if top < u.treeVy {
			u.treeVy = top
		}
		if top+float32(rowH) > u.treeVy+float32(viewH) {
			u.treeVy = top + float32(rowH) - float32(viewH)
		}
	}
	maxVy := float32(max(0, len(t.rows)*rowH-viewH+rowH))
	u.treeVy = max(0, min(u.treeVy, maxVy))

	// pointer
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &u.treeTag, Kinds: pointer.Press | pointer.Move | pointer.Leave | pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		idx := int((e.Position.Y - float32(list.Min.Y) + u.treeVy) / float32(rowH))
		if e.Position.Y < float32(list.Min.Y) {
			idx = -1
		}
		inHeader := e.Position.Y < float32(r.Min.Y+headerH)
		if !inHeader && (a.sideView != sideFiles || e.Position.Y < float32(list.Min.Y)) {
			continue // the view switcher or another view handles this
		}
		switch e.Kind {
		case pointer.Leave:
			u.hoverRow = -1
		case pointer.Move:
			u.hoverRow = idx
		case pointer.Scroll:
			u.treeVy = max(0, min(u.treeVy+e.Scroll.Y, maxVy))
		case pointer.Press:
			if inHeader {
				// header: the + button creates a file
				if a.sideView == sideFiles && e.Position.X > float32(r.Max.X-gtx.Dp(34)) {
					a.treeNew(t.selected())
				}
				a.focus = focusTree
				continue
			}
			a.focus = focusTree
			if idx < len(t.rows) {
				t.sel = idx
				n := t.rows[idx]
				if n.isDir {
					t.toggle(n)
				} else if u.isDoubleClick(gtx.Now, "tree", idx) {
					a.openFile(n.path, 0, 0) // pins the preview
				} else if a.openPreview(n.path, 0, 0) {
					a.focus = focusEditor
				}
			}
		}
	}
	st := clip.Rect(r).Push(ops)
	event.Op(ops, &u.treeTag)

	// header: the view's title
	name := strings.ToUpper(t.root.name)
	switch a.sideView {
	case sideChanges:
		name = "SOURCE CONTROL"
	case sideHistory:
		name = "HISTORY"
	}
	hcol := th.dim
	if a.focus == focusTree {
		hcol = th.fg
	}
	nx := r.Min.X + gtx.Dp(14)
	a.tc.draw(ops, fUIBold, gtx.Sp(11), nx, a.uiBaseline(r.Min.Y, headerH), a.tc.truncate(fUIBold, gtx.Sp(11), r.Max.X-nx-gtx.Dp(40), name), hcol)
	if a.sideView == sideFiles {
		plus(ops, r.Max.X-gtx.Dp(18), r.Min.Y+headerH/2, gtx.Dp(10), float32(gtx.Dp(1.4)), th.dim)
		handCursor(ops, image.Rect(r.Max.X-gtx.Dp(30), r.Min.Y+gtx.Dp(6), r.Max.X-gtx.Dp(6), r.Min.Y+headerH-gtx.Dp(6)))
	}
	if a.sideView != sideFiles {
		if a.sideView == sideChanges {
			a.drawChanges(gtx, list)
		} else {
			a.drawHistory(gtx, list)
		}
	}
	ls := clip.Rect(list).Push(ops)
	if a.sideView != sideFiles {
		ls.Pop()
		ls = clip.Rect(image.Rectangle{}).Push(ops) // nothing more to draw here
	}
	if len(t.rows) > 0 && a.sideView == sideFiles {
		// rows are clickable: hand cursor over them
		rows := image.Rect(list.Min.X, list.Min.Y, list.Max.X, min(list.Max.Y, list.Min.Y+len(t.rows)*rowH-int(u.treeVy)))
		hc := clip.Rect(rows).Push(ops)
		pointer.CursorPointer.Add(ops)
		hc.Pop()
	}
	active := ""
	if b := a.buf(); b != nil {
		active = b.path
	}
	first := int(u.treeVy) / rowH
	if a.sideView != sideFiles {
		first = len(t.rows)
	}
	for i := first; i < len(t.rows); i++ {
		y := list.Min.Y + i*rowH - int(u.treeVy)
		if y > list.Max.Y {
			break
		}
		n := t.rows[i]
		row := image.Rect(r.Min.X, y, r.Max.X, y+rowH)
		switch {
		case i == t.sel && a.focus == focusTree:
			rowBg(gtx, row, th.sideSel)
		case n.path == active:
			rowBg(gtx, row, mixC(th.sidebar, th.fg, 0.07))
		case i == u.hoverRow:
			rowBg(gtx, row, th.hover)
		}
		indent := gtx.Dp(12) + (n.depth-1)*gtx.Dp(14)
		cx := r.Min.X + indent + gtx.Dp(4)
		cy := y + rowH/2
		// indent guides
		for d := 1; d < n.depth; d++ {
			gx := r.Min.X + gtx.Dp(12) + (d-1)*gtx.Dp(14) + gtx.Dp(4)
			fill(ops, image.Rect(gx, y, gx+1, y+rowH), mixC(th.sidebar, th.fg, 0.08))
		}
		fg := th.fg
		if n.isDir {
			chevron(ops, cx, cy, gtx.Dp(7), n.expanded, float32(gtx.Dp(1.3)), th.dim)
			fg = th.dirFg
		}
		if strings.HasPrefix(n.name, ".") {
			fg = th.dim
		}
		gs := t.gitStatusFor(n.path)
		switch gs {
		case 'M':
			fg = th.gitMod
		case '?', 'A':
			fg = th.gitNew
		case 'D':
			fg = th.gitDel
		}
		tx := cx + gtx.Dp(10)
		f := fUI
		if n.path == active {
			f = fUIBold
		}
		avail := r.Max.X - tx - gtx.Dp(26)
		a.tc.draw(ops, f, u.uiPx, tx, a.uiBaseline(y, rowH), a.tc.truncate(f, u.uiPx, avail, n.name), fg)
		if gs != 0 && !n.isDir {
			mark := string(gs)
			if gs == '?' {
				mark = "U"
			}
			mw := a.tc.width(fUIBold, gtx.Sp(11), mark)
			a.tc.draw(ops, fUIBold, gtx.Sp(11), r.Max.X-gtx.Dp(14)-mw, a.uiBaseline(y, rowH), mark, fg)
		} else if gs != 0 {
			dot(ops, r.Max.X-gtx.Dp(17), cy, gtx.Dp(3), withA(fg, 0xb0))
		}
	}
	if len(t.rows) == 0 && a.sideView == sideFiles {
		a.tc.draw(ops, fUI, u.uiPx, r.Min.X+gtx.Dp(16), a.uiBaseline(list.Min.Y, rowH), "Empty folder", th.dim)
	}
	ls.Pop()
	st.Pop()

	// splitter
	split := image.Rect(r.Max.X, r.Min.Y, r.Max.X+gtx.Dp(panelGapDp), r.Max.Y)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &u.splitTag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Enter | pointer.Leave})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		switch e.Kind {
		case pointer.Enter:
			u.hoverSplit = true
		case pointer.Leave:
			u.hoverSplit = false
		case pointer.Press:
			u.dragSplit = true
		case pointer.Drag:
			if u.dragSplit {
				a.sidebarDp = max(140, (e.Position.X-float32(r.Min.X))/gtx.Metric.PxPerDp)
			}
		case pointer.Release:
			u.dragSplit = false
		}
	}
	// No divider: spacing separates the panel from the editor. A faint
	// line appears only while the resize handle is hovered or dragged.
	if u.hoverSplit || u.dragSplit {
		gx := r.Max.X + gtx.Dp(panelGapDp)/2
		fill(ops, image.Rect(gx-1, r.Min.Y+gtx.Dp(8), gx+1, r.Max.Y-gtx.Dp(8)), withA(th.accent, 0x90))
	}
	ss := clip.Rect(split).Push(ops)
	pointer.CursorColResize.Add(ops)
	event.Op(ops, &u.splitTag)
	ss.Pop()
}

var _ = color.NRGBA{}

// panelGapDp is the space between the side panel and the editor.
const panelGapDp = 10

// rowBg draws a list row highlight as a slightly inset rounded rectangle.
func rowBg(gtx layout.Context, row image.Rectangle, c color.NRGBA) {
	r := image.Rect(row.Min.X+gtx.Dp(6), row.Min.Y+gtx.Dp(1), row.Max.X-gtx.Dp(4), row.Max.Y-gtx.Dp(1))
	rrect(gtx.Ops, r, gtx.Dp(5), c)
}

// isDoubleClick reports whether this press on (what, idx) follows a press on
// the same target closely enough to count as a double click.
func (u *uiState) isDoubleClick(now time.Time, what string, idx int) bool {
	dbl := u.clickWhat == what && u.clickIdx == idx && now.Sub(u.clickAt) < 450*time.Millisecond
	u.clickWhat, u.clickIdx, u.clickAt = what, idx, now
	if dbl {
		u.clickWhat = "" // a third click starts over
	}
	return dbl
}

func tabFace(b *Buffer) face {
	if b.preview {
		return fUIItalic
	}
	return fUI
}

type statusItem struct {
	id string
	r  image.Rectangle
}
