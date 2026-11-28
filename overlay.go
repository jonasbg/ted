package main

import (
	"context"
	"fmt"
	"image"
	"io"
	"runtime"
	"strconv"
	"strings"
	"time"

	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
)

// ---- single-line text input ----

type lineInput struct {
	r   []rune
	cur int
	all bool // whole text selected: typing replaces it
}

func (in *lineInput) set(s string) {
	in.r = []rune(s)
	in.cur = len(in.r)
	in.all = s != ""
}

func (in *lineInput) String() string { return string(in.r) }

func (in *lineInput) insert(s string) {
	if in.all {
		in.r, in.cur, in.all = in.r[:0], 0, false
	}
	rs := []rune(strings.NewReplacer("\r\n", " ", "\n", " ", "\t", " ").Replace(s))
	in.r = append(in.r[:in.cur], append(rs, in.r[in.cur:]...)...)
	in.cur += len(rs)
}

// key handles editing keys; changed reports whether the text changed.
func (in *lineInput) key(e key.Event) (handled, changed bool) {
	word := e.Modifiers.Contain(key.ModShortcutAlt)
	sc := e.Modifiers.Contain(key.ModShortcut)
	switch e.Name {
	case key.NameLeftArrow:
		in.all = false
		switch {
		case sc && isMac:
			in.cur = 0
		case word:
			in.cur = wordLeft(in.r, in.cur)
		case in.cur > 0:
			in.cur--
		}
	case key.NameRightArrow:
		in.all = false
		switch {
		case sc && isMac:
			in.cur = len(in.r)
		case word:
			for in.cur < len(in.r) && !isWordRune(in.r[in.cur]) {
				in.cur++
			}
			for in.cur < len(in.r) && isWordRune(in.r[in.cur]) {
				in.cur++
			}
		case in.cur < len(in.r):
			in.cur++
		}
	case key.NameHome:
		in.all, in.cur = false, 0
	case key.NameEnd:
		in.all, in.cur = false, len(in.r)
	case key.NameDeleteBackward:
		switch {
		case in.all:
			in.r, in.cur, in.all = in.r[:0], 0, false
		case sc && isMac:
			in.r = in.r[in.cur:]
			in.cur = 0
		case word:
			n := wordLeft(in.r, in.cur)
			in.r = append(in.r[:n], in.r[in.cur:]...)
			in.cur = n
		case in.cur > 0:
			in.r = append(in.r[:in.cur-1], in.r[in.cur:]...)
			in.cur--
		default:
			return true, false
		}
		return true, true
	case key.NameDeleteForward:
		if in.all {
			in.r, in.cur, in.all = in.r[:0], 0, false
			return true, true
		}
		if in.cur < len(in.r) {
			in.r = append(in.r[:in.cur], in.r[in.cur+1:]...)
			return true, true
		}
	case "A":
		if sc {
			in.all = true
			in.cur = len(in.r)
			return true, false
		}
		return false, false
	default:
		return false, false
	}
	return true, false
}

// copyCut handles Cmd/Ctrl+C and Cmd/Ctrl+X on a single-line input.
func (in *lineInput) copyCut(gtx layout.Context, e key.Event) (handled, changed bool) {
	if !e.Modifiers.Contain(key.ModShortcut) || (e.Name != "C" && e.Name != "X") {
		return false, false
	}
	if len(in.r) == 0 {
		return true, false
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(string(in.r)))})
	if e.Name == "X" {
		in.r, in.cur, in.all = in.r[:0], 0, false
		return true, true
	}
	return true, false
}

func (a *App) drawInput(gtx layout.Context, in *lineInput, r image.Rectangle, focused bool, placeholder string) {
	ops := gtx.Ops
	th := a.th
	u := &a.ui
	px := gtx.Sp(14)
	asc, desc := a.tc.metrics(fUI, px)
	bl := r.Min.Y + (r.Dy()-(asc+desc))/2 + asc
	x := r.Min.X
	st := clip.Rect(r).Push(ops)
	defer st.Pop()
	if len(in.r) == 0 {
		if placeholder != "" {
			a.tc.draw(ops, fUI, px, x, bl, placeholder, th.faint)
		}
	} else {
		pre := string(in.r[:in.cur])
		// scroll the text left if the caret would be outside
		cw := a.tc.width(fUI, px, pre)
		shift := max(0, cw-r.Dx()+gtx.Dp(4))
		if in.all {
			w := a.tc.width(fUI, px, string(in.r))
			fill(ops, image.Rect(x-shift, bl-asc-gtx.Dp(2), x-shift+w, bl+desc+gtx.Dp(2)), th.selection)
		}
		a.tc.draw(ops, fUI, px, x-shift, bl, string(in.r), th.fg)
		x += cw - shift
	}
	if len(in.r) == 0 {
		x = r.Min.X
	}
	if focused && a.caretVisible(gtx) {
		fill(ops, image.Rect(x, bl-asc-gtx.Dp(1), x+max(2, gtx.Dp(1.5)), bl+desc+gtx.Dp(1)), th.caret)
	}
	_ = u
}

// ---- overlays ----

type ovKind int

const (
	ovSearch ovKind = iota // Cmd+K: file names and contents, in the folder or the open tabs
	ovPrompt
	ovConfirm
	ovHelp
	ovPick
	ovSettings
)

type button struct {
	label  string
	hotkey rune
	action func()
}

type Overlay struct {
	kind      ovKind
	title     string
	in        lineInput
	sel       int
	scroll    int
	quickHits []fuzzyHit
	grepHits  []grepHit
	scope     searchScope
	scopeBtn  image.Rectangle
	htmlTed   bool // settings: ted is the HTML default (macOS)
	showTypes bool // settings: list the supported file types
	// settings: how many supported file types open in ted (macOS)
	tedDefaults, allDefaults int
	gen                      int
	cancel                   context.CancelFunc
	busy                     bool
	truncated                bool

	onSubmit     func(string)
	pathComplete bool

	message string
	buttons []button

	choices []string // ovPick choices
	current string
	onPick  func(string)

	box       image.Rectangle
	scrollAcc float32 // sub-row scroll distance carried between events
	rowPx     int
	followSel bool // keep the selection in view (keyboard), not after wheel scrolls
	hover     int  // list row under the mouse, -1 when none
	hoverBtn  int
	items     []image.Rectangle
	btns      []image.Rectangle
	tag       int
}

func (a *App) closeOverlay() {
	if o := a.overlay; o != nil && o.cancel != nil {
		o.cancel()
	}
	a.overlay = nil
	a.imeSel = keyRangeInvalid
}

var keyRangeInvalid = key.Range{Start: -1, End: -1}

func (a *App) prompt(title, initial string, paths bool, submit func(string)) {
	a.overlay = &Overlay{hover: -1, hoverBtn: -1, kind: ovPrompt, title: title, onSubmit: submit, pathComplete: paths}
	a.overlay.in.set(initial)
	a.overlay.in.all = false
}

func (a *App) confirm(message string, buttons []button) {
	a.overlay = &Overlay{hover: -1, hoverBtn: -1, kind: ovConfirm, message: message, buttons: buttons}
}

type searchScope int

const (
	scopeFolder searchScope = iota // the folder ted was opened on
	scopeOpen                      // the files open in tabs, unsaved edits included
)

// searchFileCap is how many file-name matches sit above the content matches
// once something is typed, so a common word doesn't bury the text hits.
const searchFileCap = 10

// openSearch opens Cmd+K: go to file and search file contents in one list,
// scoped to the folder or to the open tabs (Tab switches).
func (a *App) openSearch(initial string) {
	a.closeOverlay()
	scope := a.searchScope
	if a.root == "" {
		scope = scopeOpen
	}
	a.overlay = &Overlay{hover: -1, hoverBtn: -1, kind: ovSearch, scope: scope}
	a.overlay.in.set(initial)
	if scope == scopeFolder {
		a.loadFiles()
	}
	a.overlay.changed(a)
}

func (a *App) toggleSearchScope() {
	o := a.overlay
	if a.root == "" {
		a.setMsg("Open a folder with ted <folder> to search it")
		return
	}
	if o.scope == scopeFolder {
		o.scope = scopeOpen
	} else {
		o.scope = scopeFolder
		a.loadFiles()
	}
	a.searchScope = o.scope
	o.changed(a)
}

// showBuffer switches to the tab holding b.
func (a *App) showBuffer(b *Buffer) bool {
	for i, x := range a.bufs {
		if x == b {
			a.active = i
			a.focus = focusEditor
			return true
		}
	}
	return false
}

// showLabelled switches to the open tab searchBufs labels l.
func (a *App) showLabelled(l string, line, col int) {
	bufs, labels := a.searchBufs()
	for i, x := range labels {
		if x == l && a.showBuffer(bufs[i]) {
			if line > 0 {
				a.gotoPos(bufs[i], line, col)
			}
			return
		}
	}
}

// searchBufs lists the open tabs the open-files scope covers, with the
// label each is matched and shown by. Diff views are left out.
func (a *App) searchBufs() ([]*Buffer, []string) {
	var bufs []*Buffer
	var labels []string
	for _, b := range a.bufs {
		if b.virtualKey != "" {
			continue
		}
		l := b.name()
		if b.path != "" {
			l = a.rel(b.path)
		}
		bufs = append(bufs, b)
		labels = append(labels, l)
	}
	return bufs, labels
}

func (o *Overlay) changed(a *App) {
	o.sel, o.scroll = 0, 0
	o.followSel = true
	switch o.kind {
	case ovPick:
		o.updateQuick(a)
	case ovSearch:
		o.updateQuick(a)
		o.startGrep(a)
	}
}

// pick shows a searchable list and calls onPick with the chosen item.
func (a *App) pick(title string, items []string, current string, onPick func(string)) {
	a.overlay = &Overlay{hover: -1, hoverBtn: -1, kind: ovPick, title: title, choices: items, current: current, onPick: onPick}
	a.overlay.updateQuick(a)
	a.overlay.followSel = true
	for i, h := range a.overlay.quickHits {
		if h.path == current {
			a.overlay.sel = i
		}
	}
}

func (o *Overlay) updateQuick(a *App) {
	if o.kind == ovPick {
		o.quickHits = fuzzyFilter(strings.TrimSpace(o.in.String()), o.choices, 1000)
		return
	}
	q, _, _ := splitLineSuffix(o.in.String())
	q = strings.TrimPrefix(q, "./")
	q = strings.ReplaceAll(q, " ", "")
	files := a.files
	if o.scope == scopeOpen {
		_, files = a.searchBufs()
	} else if q == "" && len(a.bufs) > 0 {
		// Prioritize open files at the top when the search box is empty.
		seen := make(map[string]bool, len(a.bufs))
		ordered := make([]string, 0, min(len(a.files)+len(a.bufs), 300))
		for _, b := range a.bufs {
			if b.path == "" || b.virtualKey != "" {
				continue
			}
			rel := a.rel(b.path)
			if !strings.HasPrefix(rel, "..") && !strings.HasPrefix(rel, "/") && !strings.HasPrefix(rel, "~") && !seen[rel] {
				seen[rel] = true
				ordered = append(ordered, rel)
			}
		}
		for _, f := range a.files {
			if len(ordered) >= 300 {
				break
			}
			if !seen[f] {
				ordered = append(ordered, f)
			}
		}
		files = ordered
	}
	limit := 300
	if q != "" {
		limit = searchFileCap
	}
	o.quickHits = fuzzyFilter(q, files, limit)
}

func (o *Overlay) startGrep(a *App) {
	if o.cancel != nil {
		o.cancel()
	}
	o.gen++
	q := o.in.String()
	if strings.TrimSpace(q) == "" {
		o.grepHits, o.busy, o.truncated = nil, false, false
		return
	}
	if o.scope == scopeOpen {
		// in memory, so unsaved edits are found too
		bufs, labels := a.searchBufs()
		o.grepHits, o.truncated = bufferGrep(bufs, labels, q)
		o.busy = false
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	o.cancel = cancel
	o.busy = true
	gen, root := o.gen, a.root
	go func() {
		select { // debounce typing
		case <-time.After(120 * time.Millisecond):
		case <-ctx.Done():
			return
		}
		hits, trunc := projectGrep(ctx, root, q)
		if ctx.Err() == nil {
			a.post(grepEvent{gen, hits, trunc})
		}
	}()
}

func (o *Overlay) count() int {
	switch o.kind {
	case ovPick:
		return len(o.quickHits)
	case ovSearch:
		return len(o.quickHits) + len(o.grepHits)
	case ovHelp:
		return len(helpRows())
	}
	return 0
}

func (a *App) overlayKey(gtx layout.Context, e key.Event) {
	o := a.overlay
	if o.kind == ovConfirm {
		switch e.Name {
		case key.NameEscape:
			a.closeOverlay()
			for _, b := range o.buttons {
				if b.hotkey == 'c' {
					b.action()
				}
			}
		case key.NameReturn, key.NameEnter:
			a.closeOverlay()
			o.buttons[0].action()
		}
		return
	}
	if o.kind == ovSettings {
		switch e.Name {
		case key.NameEscape, key.NameReturn, key.NameEnter:
			a.closeOverlay()
		}
		return
	}
	if o.kind == ovHelp {
		switch e.Name {
		case key.NameUpArrow:
			o.scroll = max(0, o.scroll-1)
		case key.NameDownArrow:
			o.scroll++
		case key.NamePageUp:
			o.scroll = max(0, o.scroll-10)
		case key.NamePageDown:
			o.scroll += 10
		default:
			a.closeOverlay()
		}
		return
	}
	if handled, changed := o.in.copyCut(gtx, e); handled {
		if changed {
			o.changed(a)
		}
		return
	}
	n := o.count()
	switch e.Name {
	case key.NameEscape:
		a.closeOverlay()
		return
	case key.NameUpArrow:
		o.sel = max(0, o.sel-1)
		o.followSel = true
		return
	case key.NameDownArrow:
		o.sel = max(0, min(o.sel+1, n-1))
		o.followSel = true
		return
	case key.NamePageUp:
		o.sel = max(0, o.sel-10)
		o.followSel = true
		return
	case key.NamePageDown:
		o.sel = max(0, min(o.sel+10, n-1))
		o.followSel = true
		return
	case key.NameReturn, key.NameEnter:
		a.overlayAccept()
		return
	case key.NameTab:
		if o.kind == ovSearch {
			a.toggleSearchScope()
			return
		}
		if o.pathComplete {
			o.in.set(completePath(a.root, o.in.String()))
			o.in.all = false
		}
		return
	}
	if _, changed := o.in.key(e); changed {
		o.changed(a)
	}
}

func (a *App) overlayText(s string) {
	o := a.overlay
	switch o.kind {
	case ovConfirm:
		for _, r := range strings.ToLower(s) {
			for _, b := range o.buttons {
				if b.hotkey == r {
					a.closeOverlay()
					b.action()
					return
				}
			}
		}
		return
	case ovHelp:
		a.closeOverlay()
		return
	case ovSettings:
		return
	}
	o.in.insert(s)
	o.changed(a)
}

func (a *App) overlayAccept() {
	o := a.overlay
	switch o.kind {
	case ovSearch:
		p, line, col := splitLineSuffix(o.in.String())
		nf := len(o.quickHits)
		switch {
		case o.sel < nf:
			a.closeOverlay()
			if o.scope == scopeOpen {
				a.showLabelled(o.quickHits[o.sel].path, line, col)
			} else {
				a.openFile(o.quickHits[o.sel].path, line, col)
			}
		case o.sel < nf+len(o.grepHits):
			h := o.grepHits[o.sel-nf]
			a.closeOverlay()
			ok := false
			if h.buf != nil {
				ok = a.showBuffer(h.buf) && h.line <= len(h.buf.lines)
				if ok {
					a.gotoPos(h.buf, h.line, 1)
				}
			} else {
				ok = a.openPreview(h.path, h.line, 1)
			}
			if ok {
				b := a.buf()
				col := byteToRuneCol(b.lines[b.cur.L], h.col-1)
				q := []rune(o.in.String())
				a.lastQ = q
				b.anchor = Pos{b.cur.L, col}
				b.cur = Pos{b.cur.L, min(len(b.lines[b.cur.L]), col+len(q))}
				b.selecting = true
				b.centerOnCursor = true
			}
		case p != "" && o.scope == scopeFolder:
			a.closeOverlay()
			a.openFile(p, line, col)
		}
	case ovPrompt:
		a.closeOverlay()
		if o.onSubmit != nil {
			o.onSubmit(o.in.String())
		}
	case ovPick:
		if o.sel < len(o.quickHits) {
			a.closeOverlay()
			o.onPick(o.quickHits[o.sel].path)
		}
	}
}

func (a *App) drawOverlay(gtx layout.Context) {
	ops := gtx.Ops
	th := a.th
	o := a.overlay
	W, H := gtx.Constraints.Max.X, gtx.Constraints.Max.Y

	// a full-window catcher closes the overlay when clicking outside
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &o.tag, Kinds: pointer.Press | pointer.Move | pointer.Leave | pointer.Scroll, ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		pt := image.Pt(int(e.Position.X), int(e.Position.Y))
		if e.Kind == pointer.Move || e.Kind == pointer.Leave {
			o.hover, o.hoverBtn = -1, -1
			if e.Kind == pointer.Move {
				for i, ir := range o.items {
					if pt.In(ir) {
						o.hover = o.scroll + i
					}
				}
				for i, br := range o.btns {
					if pt.In(br) {
						o.hoverBtn = i
					}
				}
				if pt.In(o.scopeBtn) {
					o.hoverBtn = -2
				}
			}
			continue
		}
		if e.Kind == pointer.Scroll {
			// Scroll the view by the distance travelled, a row at a time,
			// without moving the selection (trackpads send many events).
			o.scrollAcc += e.Scroll.Y
			rh := float32(max(1, o.rowPx))
			n := int(o.scrollAcc / rh)
			o.scrollAcc -= float32(n) * rh
			if n != 0 {
				o.scroll = max(0, o.scroll+n)
				o.followSel = false
				if o.hover >= 0 {
					o.hover += n
				}
			}
			continue
		}
		if e.Buttons != pointer.ButtonPrimary {
			continue
		}
		if !pt.In(o.box) {
			if o.kind != ovConfirm {
				a.closeOverlay()
				return
			}
			continue
		}
		if pt.In(o.scopeBtn) {
			a.toggleSearchScope()
			continue
		}
		for i, br := range o.btns {
			if pt.In(br) && o.kind == ovSettings {
				o.buttons[i].action() // settings stay open
				return
			}
			if pt.In(br) {
				a.closeOverlay()
				o.buttons[i].action()
				return
			}
		}
		for i, ir := range o.items {
			if pt.In(ir) {
				o.sel = o.scroll + i
				a.overlayAccept() // one click picks, as in VS Code
				return
			}
		}
	}
	all := clip.Rect(image.Rect(0, 0, W, H)).Push(ops)
	event.Op(ops, &o.tag)
	pointer.CursorDefault.Add(ops)
	all.Pop()
	if o.kind == ovConfirm || o.kind == ovPrompt || o.kind == ovHelp || o.kind == ovSettings {
		fill(ops, image.Rect(0, 0, W, H), withA(th.shadow, 0x40))
	}
	if o.kind == ovSettings {
		o.btns = o.btns[:0]
		a.drawSettings(gtx, o)
		return
	}

	rowH := gtx.Dp(30)
	inputH := gtx.Dp(44)
	bw := min(W-gtx.Dp(40), gtx.Dp(680))
	bx := (W - bw) / 2
	by := gtx.Dp(56)
	rad := gtx.Dp(10)
	o.items = o.items[:0]
	o.btns = o.btns[:0]

	switch o.kind {
	case ovConfirm:
		bw = min(W-gtx.Dp(40), gtx.Dp(460))
		bx = (W - bw) / 2
		bh := gtx.Dp(120)
		by = H/2 - bh
		o.box = image.Rect(bx, by, bx+bw, by+bh)
		shadow(ops, o.box, rad, gtx.Dp(18), th.shadow)
		rrect(ops, o.box, rad, th.panel)
		strokeRRect(ops, o.box, rad, 1, th.border)
		px := gtx.Sp(14)
		a.tc.draw(ops, fUIBold, px, bx+gtx.Dp(20), by+gtx.Dp(36), a.tc.truncate(fUIBold, px, bw-gtx.Dp(40), o.message), th.fg)
		if o.title != "" {
			a.tc.draw(ops, fUI, a.ui.uiPx, bx+gtx.Dp(20), by+gtx.Dp(61), o.title, th.dim)
		}
		x := o.box.Max.X - gtx.Dp(16)
		bhh := gtx.Dp(30)
		for i := len(o.buttons) - 1; i >= 0; i-- {
			b := o.buttons[i]
			w := a.tc.width(fUI, a.ui.uiPx, b.label) + gtx.Dp(28)
			x -= w
			br := image.Rect(x, o.box.Max.Y-gtx.Dp(16)-bhh, x+w, o.box.Max.Y-gtx.Dp(16))
			bg, fg := th.panel2, th.fg
			if i == 0 {
				bg, fg = th.accent, th.onAccent
			}
			if i == o.hoverBtn {
				bg = mixC(bg, th.fg, 0.12)
			}
			rrect(ops, br, gtx.Dp(6), bg)
			a.tc.draw(ops, fUIBold, a.ui.uiPx, x+gtx.Dp(14), a.uiBaseline(br.Min.Y, bhh), b.label, fg)
			o.btns = append([]image.Rectangle{br}, o.btns...)
			handCursor(ops, br)
			x -= gtx.Dp(8)
		}
		return
	case ovPrompt:
		bw = min(W-gtx.Dp(40), gtx.Dp(560))
		bx = (W - bw) / 2
		titleH := gtx.Dp(30)
		o.box = image.Rect(bx, by, bx+bw, by+titleH+inputH+gtx.Dp(8))
		shadow(ops, o.box, rad, gtx.Dp(18), th.shadow)
		rrect(ops, o.box, rad, th.panel)
		strokeRRect(ops, o.box, rad, 1, th.border)
		a.tc.draw(ops, fUIBold, gtx.Sp(12), bx+gtx.Dp(16), a.uiBaseline(by+gtx.Dp(4), titleH), o.title, th.dim)
		ir := image.Rect(bx+gtx.Dp(10), by+titleH, bx+bw-gtx.Dp(10), by+titleH+inputH-gtx.Dp(6))
		rrect(ops, ir, gtx.Dp(6), th.bg)
		strokeRRect(ops, ir, gtx.Dp(6), 1, withA(th.accent, 0xa0))
		hint := ""
		if o.pathComplete {
			hint = "Tab completes"
		}
		a.drawInput(gtx, &o.in, ir.Inset(gtx.Dp(10)), true, hint)
		return
	case ovHelp:
		rows := helpRows()
		bw = min(W-gtx.Dp(40), gtx.Dp(620))
		bx = (W - bw) / 2
		bh := min(H-by-gtx.Dp(40), len(rows)*gtx.Dp(24)+gtx.Dp(56))
		o.box = image.Rect(bx, by, bx+bw, by+bh)
		shadow(ops, o.box, rad, gtx.Dp(18), th.shadow)
		rrect(ops, o.box, rad, th.panel)
		strokeRRect(ops, o.box, rad, 1, th.border)
		a.tc.draw(ops, fUIBold, gtx.Sp(15), bx+gtx.Dp(20), by+gtx.Dp(32), "Keyboard shortcuts", th.fg)
		lh := gtx.Dp(24)
		o.rowPx = lh
		visible := (bh - gtx.Dp(56)) / lh
		o.scroll = max(0, min(o.scroll, len(rows)-visible))
		st := clip.Rect(image.Rect(bx, by+gtx.Dp(44), bx+bw, by+bh-gtx.Dp(8))).Push(ops)
		for i := 0; i < visible+1 && o.scroll+i < len(rows); i++ {
			row := rows[o.scroll+i]
			y := by + gtx.Dp(48) + i*lh
			bl := a.uiBaseline(y, lh)
			if row[1] == "" {
				a.tc.draw(ops, fUIBold, gtx.Sp(11), bx+gtx.Dp(20), bl, strings.ToUpper(row[0]), th.accent)
				continue
			}
			a.tc.draw(ops, fMono, gtx.Sp(12), bx+gtx.Dp(20), bl, row[0], th.gitMod)
			a.tc.draw(ops, fUI, a.ui.uiPx, bx+gtx.Dp(220), bl, row[1], th.fg)
		}
		st.Pop()
		return
	}

	// quick open / project search: input + list
	n := o.count()
	maxRows := min(14, max(3, (H-by-inputH-gtx.Dp(60))/rowH))
	rows := min(n, maxRows)
	listH := rows*rowH + gtx.Dp(8)
	if n == 0 {
		listH = rowH + gtx.Dp(8)
	}
	o.box = image.Rect(bx, by, bx+bw, by+inputH+listH)
	shadow(ops, o.box, rad, gtx.Dp(18), th.shadow)
	rrect(ops, o.box, rad, th.panel)
	strokeRRect(ops, o.box, rad, 1, th.border)

	status := ""
	placeholder := ""
	switch o.kind {
	case ovPick:
		placeholder = o.title
	case ovSearch:
		where := "open tabs"
		if o.scope == scopeFolder {
			where = a.tree.root.name
		}
		placeholder = "Go to file or search text in " + where
		switch {
		case o.scope == scopeFolder && a.filesLoading && len(a.files) == 0:
			status = "indexing…"
		case o.busy:
			status = "searching…"
		case len(o.in.r) == 0:
		case o.truncated:
			status = fmt.Sprintf("%d+ matches", len(o.grepHits))
		default:
			status = fmt.Sprintf("%d matches", len(o.grepHits))
		}
	}
	right := o.box.Max.X - gtx.Dp(16)
	o.scopeBtn = image.Rectangle{}
	if o.kind == ovSearch {
		// scope chip: click or Tab switches between the folder and the open tabs
		label := "Folder"
		if o.scope == scopeOpen {
			label = "Open tabs"
		}
		cpx := gtx.Sp(12)
		cw := a.tc.width(fUI, cpx, label) + a.tc.width(fUI, cpx, "Tab") + gtx.Dp(26)
		ch := gtx.Dp(24)
		cy := by + (inputH-ch)/2
		o.scopeBtn = image.Rect(right-cw, cy, right, cy+ch)
		bg := th.panel2
		if o.hoverBtn == -2 {
			bg = mixC(bg, th.fg, 0.12)
		}
		rrect(ops, o.scopeBtn, gtx.Dp(6), bg)
		cx := a.tc.draw(ops, fUI, cpx, o.scopeBtn.Min.X+gtx.Dp(9), a.uiBaseline(cy, ch), label, th.fg)
		a.tc.draw(ops, fUI, cpx, o.scopeBtn.Min.X+gtx.Dp(17)+cx, a.uiBaseline(cy, ch), "Tab", th.dim)
		handCursor(ops, o.scopeBtn)
		right = o.scopeBtn.Min.X - gtx.Dp(12)
	}
	sw := a.tc.width(fUI, gtx.Sp(12), status)
	a.tc.draw(ops, fUI, gtx.Sp(12), right-sw, a.uiBaseline(by, inputH), status, th.dim)
	a.drawInput(gtx, &o.in, image.Rect(bx+gtx.Dp(16), by, right-gtx.Dp(12)-sw, by+inputH), true, placeholder)
	fill(ops, image.Rect(bx, by+inputH-1, bx+bw, by+inputH), th.border)

	o.rowPx = rowH
	if o.followSel {
		if o.sel < o.scroll {
			o.scroll = o.sel
		}
		if o.sel >= o.scroll+rows {
			o.scroll = o.sel - rows + 1
		}
	}
	o.scroll = max(0, min(o.scroll, n-rows))
	ly := by + inputH + gtx.Dp(4)
	if n == 0 && len(o.in.r) > 0 && !o.busy {
		msg := "No matches"
		if o.kind == ovSearch && o.scope == scopeFolder {
			msg = "No matches. Enter opens or creates this path."
		}
		a.tc.draw(ops, fUI, a.ui.uiPx, bx+gtx.Dp(16), a.uiBaseline(ly, rowH), msg, th.dim)
	}
	px := a.ui.uiPx
	for i := 0; i < rows; i++ {
		idx := o.scroll + i
		y := ly + i*rowH
		ir := image.Rect(bx+gtx.Dp(6), y, o.box.Max.X-gtx.Dp(6), y+rowH)
		o.items = append(o.items, ir)
		handCursor(ops, ir)
		switch {
		case idx == o.sel:
			rrect(ops, ir, gtx.Dp(6), th.sideSel)
		case idx == o.hover:
			rrect(ops, ir, gtx.Dp(6), mixC(th.panel, th.fg, 0.07))
		}
		bl := a.uiBaseline(y, rowH)
		x := bx + gtx.Dp(16)
		switch o.kind {
		case ovPick:
			h := o.quickHits[idx]
			a.drawHighlighted(gtx, fUI, px, x, bl, h.path, 0, h.matches)
			if h.path == o.current {
				cw := a.tc.width(fUI, px, "✓")
				a.tc.draw(ops, fUI, px, ir.Max.X-gtx.Dp(12)-cw, bl, "✓", th.accent)
			}
		case ovSearch:
			nf := len(o.quickHits)
			if idx >= nf {
				if idx == nf && nf > 0 {
					// file names above, text matches below
					fill(ops, image.Rect(ir.Min.X+gtx.Dp(10), y, ir.Max.X-gtx.Dp(10), y+1), th.border)
				}
				a.drawGrepRow(gtx, o, o.grepHits[idx-nf], ir, x, y, bl, rowH, bw)
				continue
			}
			h := o.quickHits[idx]
			// file name first, folder dimmed after it
			slash := strings.LastIndexByte(h.path, '/')
			name, dir := h.path[slash+1:], ""
			if slash >= 0 {
				dir = h.path[:slash]
			}
			x += a.drawHighlighted(gtx, fUI, px, x, bl, name, slash+1, h.matches)
			if dir != "" {
				x += gtx.Dp(10)
				dir = a.tc.truncateLeft(fUI, gtx.Sp(12), ir.Max.X-x-gtx.Dp(10), dir)
				a.tc.draw(ops, fUI, gtx.Sp(12), x, bl, dir, th.dim)
			}
		}
	}
}

// drawGrepRow draws one text match: path:line dimmed, then the line with
// the match highlighted.
func (a *App) drawGrepRow(gtx layout.Context, o *Overlay, h grepHit, ir image.Rectangle, x, y, bl, rowH, bw int) {
	ops := gtx.Ops
	th := a.th
	loc := h.path + ":" + strconv.Itoa(h.line)
	lw := min(a.tc.width(fUI, gtx.Sp(12), loc), bw*2/5)
	a.tc.draw(ops, fUI, gtx.Sp(12), x, bl, a.tc.truncateLeft(fUI, gtx.Sp(12), lw, loc), th.dim)
	x += lw + gtx.Dp(14)
	text := strings.TrimLeft(strings.ReplaceAll(h.text, "\t", " "), " ")
	q := o.in.String()
	lt, lq := text, q
	if !smartCase(q) {
		lt, lq = strings.ToLower(text), strings.ToLower(q)
	}
	cp := gtx.Sp(12.5)
	avail := ir.Max.X - x - gtx.Dp(10)
	if k := strings.Index(lt, lq); k >= 0 && lq != "" && len(lt) == len(text) && len(lq) == len(q) && k+len(q) <= len(text) {
		pre, mid, post := text[:k], text[k:k+len(q)], text[k+len(q):]
		// keep the match visible on long lines
		if a.tc.width(fMono, cp, pre) > avail/2 {
			pre = a.tc.truncateLeft(fMono, cp, avail/3, pre)
		}
		x += a.tc.draw(ops, fMono, cp, x, bl, pre, th.fg)
		mw := a.tc.width(fMono, cp, mid)
		rrect(ops, image.Rect(x-1, y+gtx.Dp(5), x+mw+1, y+rowH-gtx.Dp(5)), gtx.Dp(3), th.matchCur)
		x += a.tc.draw(ops, fMono, cp, x, bl, mid, th.fg)
		a.tc.draw(ops, fMono, cp, x, bl, a.tc.truncate(fMono, cp, max(0, ir.Max.X-x-gtx.Dp(10)), post), th.fg)
	} else {
		a.tc.draw(ops, fMono, cp, x, bl, a.tc.truncate(fMono, cp, avail, text), th.fg)
	}
}

// drawHighlighted draws s with the characters at byte indices (offset by
// base, since idx refers to the full path) emphasised. Returns the width.
func (a *App) drawHighlighted(gtx layout.Context, f face, px, x, bl int, s string, base int, idx []int) int {
	ops := gtx.Ops
	set := map[int]bool{}
	for _, i := range idx {
		set[i-base] = true
	}
	start := x
	var run strings.Builder
	runHL := false
	flush := func() {
		if run.Len() == 0 {
			return
		}
		c := a.th.fg
		ff := f
		if runHL {
			c = a.th.accent
			ff = fUIBold
		}
		x += a.tc.draw(ops, ff, px, x, bl, run.String(), c)
		run.Reset()
	}
	for i, r := range s {
		hl := set[i]
		if hl != runHL {
			flush()
			runHL = hl
		}
		run.WriteRune(r)
	}
	flush()
	return x - start
}

// ---- find bar ----

type FindBar struct {
	in       lineInput
	repl     lineInput
	replace  bool
	field    int // 0 find, 1 replace
	origin   Pos
	info     string
	box      image.Rectangle
	btns     []findBtn
	hoverBtn string
	tag      int
}

type findBtn struct {
	id string
	r  image.Rectangle
}

func (a *App) openFind(replace bool) {
	b := a.buf()
	if b == nil {
		return
	}
	if a.find == nil {
		a.find = &FindBar{}
		if len(a.lastQ) > 0 {
			a.find.in.set(string(a.lastQ))
		}
	}
	f := a.find
	f.replace = f.replace || replace
	f.field = 0
	if replace && len(f.in.r) > 0 && !b.hasSel() {
		f.field = 1
	}
	if b.hasSel() {
		if t := b.selText(); !containsNL(t) {
			f.in.set(string(t))
		}
	}
	f.in.all = len(f.in.r) > 0
	f.origin = b.cur
	if b.hasSel() {
		f.origin, _ = b.selRange()
	}
	a.focus = focusFind
	a.findUpdate()
}

func (a *App) closeFind() {
	if a.find != nil {
		a.lastQ = append([]rune(nil), a.find.in.r...)
	}
	a.find = nil
	a.focus = focusEditor
	a.imeSel = keyRangeInvalid
}

func (a *App) findUpdate() {
	b := a.buf()
	f := a.find
	if b == nil || f == nil {
		return
	}
	q := f.in.r
	if len(q) == 0 {
		f.info = ""
		return
	}
	at := Pos{-1, -1}
	if b.hasSel() {
		at, _ = b.selRange()
	}
	i, t := countMatches(b, q, at)
	switch {
	case t == 0:
		f.info = "No results"
	case i == 0:
		f.info = fmt.Sprintf("%d results", t)
	default:
		f.info = fmt.Sprintf("%d of %d", i, t)
	}
}

func (a *App) findIncremental() {
	b := a.buf()
	f := a.find
	q := f.in.r
	a.lastQ = append([]rune(nil), q...)
	if m, ok := findFrom(b, q, f.origin, true); ok && len(q) > 0 {
		a.selectMatch(b, m, len(q))
	} else {
		b.selecting = false
	}
	a.findUpdate()
}

func (a *App) selectMatch(b *Buffer, p Pos, n int) {
	b.anchor = p
	b.cur = Pos{p.L, p.C + n}
	b.selecting = true
	a.follow = true
}

func (a *App) findStep(forward bool) {
	b := a.buf()
	if b == nil {
		return
	}
	q := a.lastQ
	if a.find != nil {
		q = a.find.in.r
		a.lastQ = append([]rune(nil), q...)
	}
	if len(q) == 0 {
		a.openFind(false)
		return
	}
	from := b.cur
	if b.hasSel() {
		s, e := b.selRange()
		from = e
		if !forward {
			from = s
		}
	}
	if m, ok := findFrom(b, q, from, forward); ok {
		a.selectMatch(b, m, len(q))
	} else {
		a.setErr("No results for \"%s\"", string(q))
	}
	a.findUpdate()
}

func (a *App) replaceOne() {
	b := a.buf()
	f := a.find
	q := f.in.r
	if len(q) == 0 {
		return
	}
	if b.hasSel() {
		sel := string(b.selText())
		qs := string(q)
		matched := sel == qs
		if !caseSensitive(q) {
			matched = strings.EqualFold(sel, qs)
		}
		if matched {
			s, e := b.selRange()
			end := b.replaceRange(s, e, append([]rune(nil), f.repl.r...))
			b.cur = end
			b.selecting = false
		}
	}
	a.findStep(true)
}

func (a *App) replaceAllMatches() {
	b := a.buf()
	f := a.find
	if len(f.in.r) == 0 {
		return
	}
	n := b.replaceAll(f.in.r, append([]rune(nil), f.repl.r...))
	a.setMsg("Replaced %d occurrence(s)", n)
	a.findUpdate()
}

func (a *App) findKey(gtx layout.Context, e key.Event) {
	f := a.find
	in := &f.in
	if f.field == 1 {
		in = &f.repl
	}
	if handled, changed := in.copyCut(gtx, e); handled {
		if changed && f.field == 0 {
			a.findIncremental()
		}
		return
	}
	sc := e.Modifiers.Contain(key.ModShortcut)
	switch e.Name {
	case key.NameEscape:
		a.closeFind()
		return
	case key.NameReturn, key.NameEnter:
		switch {
		case f.field == 1 && (sc || e.Modifiers.Contain(key.ModAlt)):
			a.replaceAllMatches()
		case f.field == 1:
			a.replaceOne()
		default:
			a.findStep(!e.Modifiers.Contain(key.ModShift))
		}
		return
	case key.NameDownArrow:
		a.findStep(true)
		return
	case key.NameUpArrow:
		a.findStep(false)
		return
	case key.NameTab:
		if f.replace {
			f.field = 1 - f.field
			if f.field == 1 {
				f.repl.all = len(f.repl.r) > 0
			}
		}
		return
	}
	if _, changed := in.key(e); changed && f.field == 0 {
		a.findIncremental()
	}
}

func (a *App) findText(s string) {
	f := a.find
	if f.field == 1 {
		f.repl.insert(s)
		return
	}
	f.in.insert(s)
	a.findIncremental()
}

func (a *App) drawFindBar(gtx layout.Context) {
	ops := gtx.Ops
	th := a.th
	f := a.find
	r := a.ui.editor
	rowH := gtx.Dp(34)
	w := min(gtx.Dp(480), r.Dx()-gtx.Dp(40))
	h := rowH + gtx.Dp(12)
	if f.replace {
		h += rowH + gtx.Dp(6)
	}
	x := r.Max.X - w - gtx.Dp(24)
	y := r.Min.Y + gtx.Dp(8)
	f.box = image.Rect(x, y, x+w, y+h)

	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &f.tag, Kinds: pointer.Press | pointer.Move | pointer.Leave})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		pt := image.Pt(int(e.Position.X), int(e.Position.Y))
		hit := ""
		for _, btn := range f.btns {
			if pt.In(btn.r) {
				hit = btn.id
			}
		}
		switch e.Kind {
		case pointer.Leave:
			f.hoverBtn = ""
		case pointer.Move:
			f.hoverBtn = hit
		case pointer.Press:
			if e.Buttons != pointer.ButtonPrimary {
				continue
			}
			switch hit {
			case "prev":
				a.findStep(false)
				continue
			case "next":
				a.findStep(true)
				continue
			case "close":
				a.closeFind()
				return
			case "one":
				a.replaceOne()
				continue
			case "all":
				a.replaceAllMatches()
				continue
			}
			a.focus = focusFind
			if e.Position.Y > float32(y+gtx.Dp(6)+rowH) && f.replace {
				f.field = 1
			} else {
				f.field = 0
			}
		}
	}

	shadow(ops, f.box, gtx.Dp(8), gtx.Dp(12), th.shadow)
	rrect(ops, f.box, gtx.Dp(8), th.panel)
	strokeRRect(ops, f.box, gtx.Dp(8), 1, th.border)
	st := clip.Rect(f.box).Push(ops)
	event.Op(ops, &f.tag)
	pointer.CursorText.Add(ops)
	st.Pop()

	f.btns = f.btns[:0]
	drawBtn := func(id, label string, br image.Rectangle) {
		f.btns = append(f.btns, findBtn{id, br})
		bg, fg := th.panel2, th.dim
		if f.hoverBtn == id {
			bg = mixC(th.panel2, th.fg, 0.14)
			fg = th.fg
		}
		rrect(ops, br, gtx.Dp(4), bg)
		lw := a.tc.width(fUI, gtx.Sp(11.5), label)
		a.tc.draw(ops, fUI, gtx.Sp(11.5), br.Min.X+(br.Dx()-lw)/2, a.uiBaseline(br.Min.Y, br.Dy()), label, fg)
		handCursor(ops, br)
	}

	focused := a.focus == focusFind
	ctlW := gtx.Dp(156)
	field := func(i int, in *lineInput, ry int, ph string) {
		ir := image.Rect(x+gtx.Dp(8), ry, x+w-gtx.Dp(8)-ctlW, ry+rowH)
		rrect(ops, ir, gtx.Dp(5), th.bg)
		if focused && f.field == i {
			strokeRRect(ops, ir, gtx.Dp(5), 1, withA(th.accent, 0xc0))
		}
		a.drawInput(gtx, in, ir.Inset(gtx.Dp(8)), focused && f.field == i, ph)
	}
	r0y := y + gtx.Dp(6)
	field(0, &f.in, r0y, "Find")
	// Right-side controls for Find: match count, prev, next, close
	bs := gtx.Dp(24)
	by := r0y + (rowH-bs)/2
	bx := x + w - gtx.Dp(8) - bs
	drawBtn("close", "✕", image.Rect(bx, by, bx+bs, by+bs))
	bx -= bs + gtx.Dp(4)
	drawBtn("next", "↓", image.Rect(bx, by, bx+bs, by+bs))
	bx -= bs + gtx.Dp(4)
	drawBtn("prev", "↑", image.Rect(bx, by, bx+bs, by+bs))
	col := th.dim
	if f.info == "No results" {
		col = th.errorFg
	}
	infoAvail := bx - (x + w - ctlW) - gtx.Dp(6)
	if infoAvail > gtx.Dp(20) {
		a.tc.draw(ops, fUI, gtx.Sp(11.5), x+w-ctlW+gtx.Dp(2), a.uiBaseline(r0y, rowH), a.tc.truncate(fUI, gtx.Sp(11.5), infoAvail, f.info), col)
	}
	if f.replace {
		ry := y + gtx.Dp(12) + rowH
		field(1, &f.repl, ry, "Replace")
		rby := ry + (rowH-bs)/2
		allW := gtx.Dp(44)
		oneW := gtx.Dp(64)
		ax := x + w - gtx.Dp(8) - allW
		drawBtn("all", "All", image.Rect(ax, rby, ax+allW, rby+bs))
		ox := ax - gtx.Dp(6) - oneW
		drawBtn("one", "Replace", image.Rect(ox, rby, ox+oneW, rby+bs))
	}
}

// ---- help ----

func shortcutName() string {
	if runtime.GOOS == "darwin" {
		return "Cmd"
	}
	return "Ctrl"
}

func helpRows() [][2]string {
	c := shortcutName()
	word := "Alt"
	if runtime.GOOS != "darwin" {
		word = "Ctrl"
	}
	findNext := "F3 / Shift+F3"
	if runtime.GOOS == "darwin" {
		findNext = "Cmd+G / F3 (Shift+F3 prev)"
	}
	return [][2]string{
		{"Files", ""},
		{c + "+K", "Go to file and search text (Tab: folder / open tabs)"},
		{c + "+O", "Open a path"},
		{c + "+N", "New untitled scratch file (" + c + "+S names it)"},
		{c + "+S / " + c + "+Shift+S", "Save / Save as"},
		{c + "+W / " + c + "+Shift+T", "Close tab / Reopen closed tab"},
		{"Ctrl+Tab", "Next tab (Shift for previous)"},
		{c + "+1 … 9", "Go to tab"},
		{"Alt+R", "Reload from disk (undoable)"},
		{c + "+Q", "Quit (unsaved work is kept for next time)"},
		{"Search", ""},
		{c + "+F", "Find in file"},
		{c + "+Alt+F / " + c + "+H", "Replace"},
		{findNext, "Next / previous match"},
		{c + "+D", "Select word, then next occurrence"},
		{"Ctrl+G", "Go to line"},
		{"Editing", ""},
		{c + "+Z / " + c + "+Shift+Z", "Undo / redo"},
		{c + "+X / C / V", "Cut / copy / paste (whole line if nothing selected)"},
		{c + "+A", "Select all"},
		{c + "+L", "Select line (repeat to extend)"},
		{c + "+Shift+K", "Delete line"},
		{c + "+/", "Toggle line comment"},
		{c + "+Enter", "New line below (Shift: above)"},
		{"Tab / Shift+Tab", "Indent / outdent"},
		{"Alt+Up / Down", "Move line"},
		{"Alt+Shift+Up / Down", "Duplicate line"},
		{word + "+Left / Right", "Move by word"},
		{word + "+Backspace", "Delete word"},
		{"CSV / TSV files", ""},
		{"Alt+click / drag", "Select cells (status bar shows count, sum, average)"},
		{"Alt+click header", "Select the whole column (drag for more columns)"},
		{"Alt+Shift+click", "Extend the cell selection"},
		{"Click line numbers", "Select rows"},
		{c + "+C", "Copy selected cells as tab-separated rows"},
		{"View", ""},
		{c + "+,", "Settings and version"},
		{c + "+B", "Toggle the side panel"},
		{c + "+Shift+E", "Files"},
		{c + "+Shift+G", "Source control: staged, unstaged and untracked changes"},
		{c + "+Shift+H", "History: commits (click one to see its diff)"},
		{c + "+= / " + c + "+-", "Zoom in / out (" + c + "+0 resets)"},
		{"JSON", ""},
		{"Alt+Shift+F", "Format (or \"Format\" in the status bar; undoable)"},
		{"Minified files", "Open formatted; \"Formatted view\" in the status bar shows the file as on disk"},
		{"Pasted JSON", "Recognised and formatted; picking JSON as the language formats too"},
		{"Base64", ""},
		{c + "+click", "Decode base64 in place; " + c + "+click the shaded text to encode it again"},
		{"Markdown", ""},
		{c + "+Shift+V", "Preview / source (Esc also returns to the source)"},
		{"Double-click preview", "Jump to that place in the source"},
		{"File tree (when focused)", ""},
		{"Click / Space", "Preview file (italic tab, replaced by the next)"},
		{"Double-click / Enter", "Open file in a tab that stays"},
		{"a", "New named file (end with / for a folder)"},
		{"r  or  F2", "Rename / move"},
		{"d  or  Delete", "Delete (asks first)"},
		{".", "Show / hide dotfiles"},
		{"y", "Copy relative path"},
	}
}

// handCursor shows the pointing-hand cursor over a clickable area.
func handCursor(ops *op.Ops, r image.Rectangle) {
	st := clip.Rect(r).Push(ops)
	pointer.CursorPointer.Add(ops)
	st.Pop()
}
