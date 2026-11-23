package main

import (
	"fmt"
	"image"
	"path/filepath"
	"runtime"
	"strings"

	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op/clip"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
)

// Sidebar views.
const (
	sideFiles = iota
	sideChanges
	sideHistory
	activitySettings
)

type scmRow struct {
	section string     // header row when c == nil
	count   int        // header: number of files
	c       *gitChange // file row
}

type scmState struct {
	changes       []gitChange
	commits       []gitCommit
	head          string
	ahead, behind int
	upstream      bool
	logLoaded     bool

	rows      []scmRow
	collapsed map[string]bool
	sel       int
	vy        float32
	follow    bool

	histSel    int
	histVy     float32
	histFollow bool

	hover    int
	tabRects [4]image.Rectangle
	tabHover int
	tag      int
	tabsTag  int
}

var sectionOrder = []string{"Merge Conflicts", "Staged Changes", "Changes", "Untracked"}

func (s *scmState) rebuild() {
	if s.collapsed == nil {
		s.collapsed = map[string]bool{}
	}
	by := map[string][]*gitChange{}
	for i := range s.changes {
		c := &s.changes[i]
		for _, sec := range c.sections() {
			by[sec] = append(by[sec], c)
		}
	}
	s.rows = s.rows[:0]
	for _, sec := range sectionOrder {
		cs := by[sec]
		if len(cs) == 0 {
			continue
		}
		s.rows = append(s.rows, scmRow{section: sec, count: len(cs)})
		if s.collapsed[sec] {
			continue
		}
		for _, c := range cs {
			s.rows = append(s.rows, scmRow{section: sec, c: c})
		}
	}
	if s.sel >= len(s.rows) {
		s.sel = max(0, len(s.rows)-1)
	}
}

func (s *scmState) changeCount() int { return len(s.changes) }

// applyGit takes a fresh snapshot from the background refresh.
func (a *App) applyGit(g gitSnapshot) {
	s := &a.scm
	s.changes = g.changes
	s.head = g.head
	s.ahead, s.behind, s.upstream = g.ahead, g.behind, g.upstream
	if g.logLoaded {
		s.commits = g.commits
		s.logLoaded = true
	}
	s.rebuild()
	a.refreshVirtual()
}

func (a *App) showSideView(v int) {
	a.closeOverlay()
	if a.root == "" {
		a.setMsg("Open a folder with ted <folder> to browse the project")
		return
	}
	a.sideView = v
	a.showSidebar = true
	a.focus = focusTree
	if v == sideChanges || v == sideHistory {
		a.refreshGit()
	}
}

// ---- read-only virtual tabs (diffs, commits) ----

// openVirtual shows generated, read-only text in a (preview) tab. src
// regenerates the content when git state changes.
func (a *App) openVirtual(key, title string, src func() string, preview bool) {
	for i, b := range a.bufs {
		if b.virtualKey == key {
			a.active = i
			a.focus = focusEditor
			if !preview {
				b.preview = false
			}
			a.updateVirtual(b)
			return
		}
	}
	b := newBuffer("")
	b.title, b.virtualKey, b.virtualSrc = title, key, src
	b.readOnly, b.diffView = true, true
	b.setContent([]byte(src()))
	h := b.hl
	h.resolved, h.manual = true, true
	if l := lexers.Get("diff"); l != nil {
		h.lexer = chroma.Coalesce(l)
		h.lang = "Diff"
	}
	b.preview = preview
	a.placeBuffer(b, preview)
	a.focus = focusEditor
}

func (a *App) updateVirtual(b *Buffer) {
	if b.virtualSrc == nil {
		return
	}
	txt := b.virtualSrc()
	if txt == b.text() {
		return
	}
	cur, vy := b.cur, b.vy
	b.setContent([]byte(txt))
	b.cur = b.clamp(cur)
	b.vy = vy
	b.selecting = false
}

// refreshVirtual regenerates open working-tree diffs (they change as you edit).
func (a *App) refreshVirtual() {
	for _, b := range a.bufs {
		if strings.HasPrefix(b.virtualKey, "diff:") {
			a.updateVirtual(b)
		}
	}
}

func (a *App) openChange(r scmRow, preview bool) {
	if r.c == nil {
		return
	}
	root := a.tree.gitRoot
	c := *r.c
	sec := r.section
	name := filepath.Base(c.path)
	label := map[string]string{"Staged Changes": "Staged", "Changes": "Working Tree", "Untracked": "Untracked", "Merge Conflicts": "Conflict"}[sec]
	a.openVirtual("diff:"+sec+":"+c.path, name+" ("+label+")", func() string {
		// find the current state of this path, it may have moved sections
		return diffFor(root, sec, c)
	}, preview)
}

func (a *App) openCommit(c gitCommit, preview bool) {
	root := a.tree.gitRoot
	subj := c.subject
	if len([]rune(subj)) > 32 {
		subj = string([]rune(subj)[:31]) + "…"
	}
	a.openVirtual("commit:"+c.hash, c.short+" "+subj, func() string { return commitText(root, c.hash) }, preview)
}

// ---- activity bar: icons that switch the sidebar view ----

func (a *App) drawActivityBar(gtx layout.Context) {
	ops := gtx.Ops
	th := a.th
	s := &a.scm
	r := a.ui.activity
	fill(ops, r, th.activityBg)
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.tabsTag, Kinds: pointer.Press | pointer.Move | pointer.Leave})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		pt := image.Pt(int(e.Position.X), int(e.Position.Y))
		s.tabHover = -1
		for i, tr := range s.tabRects {
			if pt.In(tr) {
				s.tabHover = i
			}
		}
		if e.Kind == pointer.Leave {
			s.tabHover = -1
		}
		if e.Kind == pointer.Press && s.tabHover >= 0 {
			if s.tabHover == activitySettings {
				a.openSettings()
				continue
			}
			if a.showSidebar && a.sideView == s.tabHover {
				// clicking the active view's icon hides the panel, as in VS Code
				a.showSidebar = false
				if a.focus == focusTree {
					a.focus = focusEditor
				}
			} else {
				a.showSideView(s.tabHover)
			}
		}
	}
	st := clip.Rect(r).Push(ops)
	event.Op(ops, &s.tabsTag)
	st.Pop()

	size := gtx.Dp(48)
	for i := range s.tabRects {
		if i == activitySettings && runtime.GOOS != "linux" {
			continue
		}
		tr := image.Rect(r.Min.X, r.Min.Y+gtx.Dp(4)+i*size, r.Max.X, r.Min.Y+gtx.Dp(4)+(i+1)*size)
		if i == activitySettings {
			tr = image.Rect(r.Min.X, r.Max.Y-size, r.Max.X, r.Max.Y)
		}
		s.tabRects[i] = tr
		active := a.showSidebar && a.sideView == i
		col := mixC(th.activityBg, th.fg, 0.45)
		if active {
			col = th.fg
			fill(ops, image.Rect(tr.Min.X, tr.Min.Y+gtx.Dp(10), tr.Min.X+gtx.Dp(2), tr.Max.Y-gtx.Dp(10)), th.orange)
		} else if s.tabHover == i {
			col = mixC(th.activityBg, th.fg, 0.8)
		}
		cx, cy := (tr.Min.X+tr.Max.X)/2, (tr.Min.Y+tr.Max.Y)/2
		unit := float32(gtx.Dp(1))
		switch i {
		case activitySettings:
			for j, pos := range []int{-4, 4, -2} {
				y := cy + (j-1)*gtx.Dp(6)
				x := cx + pos*gtx.Dp(1)
				fill(ops, image.Rect(cx-gtx.Dp(10), y, cx+gtx.Dp(10), y+gtx.Dp(1)), col)
				rrect(ops, image.Rect(x-gtx.Dp(2), y-gtx.Dp(2), x+gtx.Dp(2), y+gtx.Dp(3)), gtx.Dp(1), col)
			}
		case sideFiles:
			iconFiles(ops, cx, cy, unit, col)
		case sideChanges:
			iconBranch(ops, cx, cy, unit, col)
			if n := s.changeCount(); n > 0 {
				txt := fmt.Sprint(n)
				if n > 99 {
					txt = "99+"
				}
				px := gtx.Sp(9)
				w := max(gtx.Dp(16), a.tc.width(fUIBold, px, txt)+gtx.Dp(8))
				br := image.Rect(cx+gtx.Dp(2), cy+gtx.Dp(3), cx+gtx.Dp(2)+w, cy+gtx.Dp(3)+gtx.Dp(16))
				rrect(ops, br, gtx.Dp(8), th.accent)
				tw := a.tc.width(fUIBold, px, txt)
				a.tc.draw(ops, fUIBold, px, br.Min.X+(br.Dx()-tw)/2, a.uiBaseline(br.Min.Y, br.Dy())-gtx.Dp(1)+gtx.Dp(1), txt, th.onAccent)
			}
		case sideHistory:
			iconCommit(ops, cx, cy, unit, col)
		}
		handCursor(ops, tr)
	}
}

// ---- Changes view ----

func (a *App) drawChanges(gtx layout.Context, list image.Rectangle) {
	ops := gtx.Ops
	th := a.th
	u := &a.ui
	s := &a.scm
	rowH := u.rowH
	infoH := gtx.Dp(26)
	if a.tree.gitRoot == "" {
		a.sideEmpty(gtx, list, "Not a git repository")
		return
	}
	info := image.Rect(list.Min.X, list.Min.Y, list.Max.X, list.Min.Y+infoH)
	body := image.Rect(list.Min.X, info.Max.Y, list.Max.X, list.Max.Y)
	viewH := body.Dy()

	if s.follow {
		s.follow = false
		top := float32(s.sel * rowH)
		if top < s.vy {
			s.vy = top
		}
		if top+float32(rowH) > s.vy+float32(viewH) {
			s.vy = top + float32(rowH) - float32(viewH)
		}
	}
	maxVy := float32(max(0, len(s.rows)*rowH-viewH+rowH))
	s.vy = max(0, min(s.vy, maxVy))

	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.tag, Kinds: pointer.Press | pointer.Move | pointer.Leave | pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		idx := int((e.Position.Y - float32(body.Min.Y) + s.vy) / float32(rowH))
		if e.Position.Y < float32(body.Min.Y) || idx >= len(s.rows) {
			idx = -1
		}
		switch e.Kind {
		case pointer.Leave:
			s.hover = -1
		case pointer.Move:
			s.hover = idx
		case pointer.Scroll:
			s.vy = max(0, min(s.vy+e.Scroll.Y, maxVy))
		case pointer.Press:
			a.focus = focusTree
			if idx < 0 {
				continue
			}
			s.sel = idx
			r := s.rows[idx]
			if r.c == nil {
				s.collapsed[r.section] = !s.collapsed[r.section]
				s.rebuild()
				continue
			}
			a.openChange(r, !u.isDoubleClick(gtx.Now, "scm", idx))
		}
	}
	st := clip.Rect(list).Push(ops)
	event.Op(ops, &s.tag)

	// branch line
	px := gtx.Sp(12)
	x := list.Min.X + gtx.Dp(14)
	bl := a.uiBaseline(info.Min.Y, info.Dy())
	if a.branch != "" {
		x += a.tc.draw(ops, fUIBold, px, x, bl, a.branch, th.fg) + gtx.Dp(8)
	}
	if s.upstream {
		sync := fmt.Sprintf("↑%d ↓%d", s.ahead, s.behind)
		a.tc.draw(ops, fUI, px, x, bl, sync, th.dim)
	}

	bs := clip.Rect(body).Push(ops)
	if len(s.rows) == 0 {
		a.tc.draw(ops, fUI, u.uiPx, body.Min.X+gtx.Dp(14), a.uiBaseline(body.Min.Y+gtx.Dp(4), rowH), "No changes. The working tree is clean.", th.dim)
	}
	first := int(s.vy) / rowH
	for i := first; i < len(s.rows); i++ {
		y := body.Min.Y + i*rowH - int(s.vy)
		if y > body.Max.Y {
			break
		}
		r := s.rows[i]
		row := image.Rect(list.Min.X, y, list.Max.X, y+rowH)
		switch {
		case i == s.sel && a.focus == focusTree:
			rowBg(gtx, row, th.sideSel)
		case i == s.hover:
			rowBg(gtx, row, th.hover)
		}
		handCursor(ops, row)
		bl := a.uiBaseline(y, rowH)
		if r.c == nil {
			chevron(ops, list.Min.X+gtx.Dp(16), y+rowH/2, gtx.Dp(7), !s.collapsed[r.section], float32(gtx.Dp(1.3)), th.dim)
			a.tc.draw(ops, fUIBold, gtx.Sp(11), list.Min.X+gtx.Dp(28), bl, strings.ToUpper(r.section), th.dim)
			cnt := fmt.Sprint(r.count)
			cw := a.tc.width(fUIBold, gtx.Sp(11), cnt)
			a.tc.draw(ops, fUIBold, gtx.Sp(11), list.Max.X-gtx.Dp(14)-cw, bl, cnt, th.dim)
			continue
		}
		letter := r.c.letter(r.section)
		col := th.gitMod
		switch letter {
		case 'A', 'U', '?':
			col = th.gitNew
		case 'D', '!':
			col = th.gitDel
		}
		name, dir := splitDir(r.c.path)
		tx := list.Min.X + gtx.Dp(28)
		lw := a.tc.width(fUIBold, gtx.Sp(11), string(letter))
		avail := list.Max.X - tx - gtx.Dp(24) - lw
		nameF := fUI
		if letter == 'D' {
			nameF = fUIItalic
		}
		nm := a.tc.truncate(nameF, u.uiPx, avail, name)
		tx += a.tc.draw(ops, nameF, u.uiPx, tx, bl, nm, th.fg)
		if dir != "." && avail-a.tc.width(nameF, u.uiPx, nm) > gtx.Dp(30) {
			d := a.tc.truncateLeft(fUI, gtx.Sp(11), avail-a.tc.width(nameF, u.uiPx, nm)-gtx.Dp(8), dir)
			a.tc.draw(ops, fUI, gtx.Sp(11), tx+gtx.Dp(8), bl, d, th.dim)
		}
		a.tc.draw(ops, fUIBold, gtx.Sp(11), list.Max.X-gtx.Dp(14)-lw, bl, string(letter), col)
	}
	bs.Pop()
	st.Pop()
}

// ---- History view ----

func (a *App) drawHistory(gtx layout.Context, list image.Rectangle) {
	ops := gtx.Ops
	th := a.th
	u := &a.ui
	s := &a.scm
	if a.tree.gitRoot == "" {
		a.sideEmpty(gtx, list, "Not a git repository")
		return
	}
	rowH := gtx.Dp(46)
	viewH := list.Dy()
	if s.histFollow {
		s.histFollow = false
		top := float32(s.histSel * rowH)
		if top < s.histVy {
			s.histVy = top
		}
		if top+float32(rowH) > s.histVy+float32(viewH) {
			s.histVy = top + float32(rowH) - float32(viewH)
		}
	}
	maxVy := float32(max(0, len(s.commits)*rowH-viewH+rowH))
	s.histVy = max(0, min(s.histVy, maxVy))
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &s.tag, Kinds: pointer.Press | pointer.Move | pointer.Leave | pointer.Scroll,
			ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		idx := int((e.Position.Y - float32(list.Min.Y) + s.histVy) / float32(rowH))
		if idx >= len(s.commits) {
			idx = -1
		}
		switch e.Kind {
		case pointer.Leave:
			s.hover = -1
		case pointer.Move:
			s.hover = idx
		case pointer.Scroll:
			s.histVy = max(0, min(s.histVy+e.Scroll.Y, maxVy))
		case pointer.Press:
			a.focus = focusTree
			if idx >= 0 {
				s.histSel = idx
				a.openCommit(s.commits[idx], !u.isDoubleClick(gtx.Now, "hist", idx))
			}
		}
	}
	st := clip.Rect(list).Push(ops)
	event.Op(ops, &s.tag)
	if len(s.commits) == 0 {
		msg := "No commits yet"
		if !s.logLoaded {
			msg = "Loading history…"
		}
		a.tc.draw(ops, fUI, u.uiPx, list.Min.X+gtx.Dp(14), a.uiBaseline(list.Min.Y+gtx.Dp(4), u.rowH), msg, th.dim)
	}
	first := int(s.histVy) / rowH
	for i := first; i < len(s.commits); i++ {
		y := list.Min.Y + i*rowH - int(s.histVy)
		if y > list.Max.Y {
			break
		}
		c := s.commits[i]
		row := image.Rect(list.Min.X, y, list.Max.X, y+rowH)
		switch {
		case i == s.histSel && a.focus == focusTree:
			rowBg(gtx, row, th.sideSel)
		case i == s.hover:
			rowBg(gtx, row, th.hover)
		}
		handCursor(ops, row)
		// timeline
		lx := list.Min.X + gtx.Dp(18)
		line := mixC(th.sidebar, th.fg, 0.28)
		if i > 0 {
			fill(ops, image.Rect(lx, y, lx+1, y+gtx.Dp(15)), line)
		}
		if i < len(s.commits)-1 {
			fill(ops, image.Rect(lx, y+gtx.Dp(23), lx+1, y+rowH), line)
		}
		dc := th.dim
		if c.hash == s.head {
			dc = th.accent
		}
		strokeRRect(ops, image.Rect(lx-gtx.Dp(4), y+gtx.Dp(15), lx+gtx.Dp(5), y+gtx.Dp(24)), gtx.Dp(5), float32(gtx.Dp(1.5)), dc)
		tx := lx + gtx.Dp(14)
		avail := list.Max.X - tx - gtx.Dp(10)
		a.tc.draw(ops, fUI, u.uiPx, tx, y+gtx.Dp(20), a.tc.truncate(fUI, u.uiPx, avail, c.subject), th.fg)
		meta := c.short + "  " + c.author + "  " + relTime(c.when)
		a.tc.draw(ops, fUI, gtx.Sp(11), tx, y+gtx.Dp(37), a.tc.truncate(fUI, gtx.Sp(11), avail, meta), th.dim)
	}
	st.Pop()
}

func (a *App) sideEmpty(gtx layout.Context, list image.Rectangle, msg string) {
	a.tc.draw(gtx.Ops, fUI, a.ui.uiPx, list.Min.X+gtx.Dp(14), a.uiBaseline(list.Min.Y+gtx.Dp(4), a.ui.rowH), msg, a.th.dim)
}

// ---- keyboard ----

func (a *App) scmKey(e key.Event) {
	s := &a.scm
	if a.sideView == sideHistory {
		n := len(s.commits)
		switch e.Name {
		case key.NameUpArrow:
			s.histSel = max(0, s.histSel-1)
		case key.NameDownArrow:
			s.histSel = max(0, min(n-1, s.histSel+1))
		case key.NamePageUp:
			s.histSel = max(0, s.histSel-10)
		case key.NamePageDown:
			s.histSel = max(0, min(n-1, s.histSel+10))
		case key.NameReturn, key.NameEnter, key.NameSpace:
			if s.histSel < n {
				a.openCommit(s.commits[s.histSel], e.Name == key.NameSpace)
				if e.Name == key.NameSpace {
					a.focus = focusTree
				}
			}
		case key.NameEscape:
			if a.buf() != nil {
				a.focus = focusEditor
			}
		}
		s.histFollow = true
		return
	}
	n := len(s.rows)
	switch e.Name {
	case key.NameUpArrow:
		s.sel = max(0, s.sel-1)
	case key.NameDownArrow:
		s.sel = max(0, min(n-1, s.sel+1))
	case key.NameReturn, key.NameEnter, key.NameSpace:
		if s.sel < n {
			r := s.rows[s.sel]
			if r.c == nil {
				s.collapsed[r.section] = !s.collapsed[r.section]
				s.rebuild()
			} else {
				a.openChange(r, e.Name == key.NameSpace)
				if e.Name == key.NameSpace {
					a.focus = focusTree
				}
			}
		}
	case key.NameEscape:
		if a.buf() != nil {
			a.focus = focusEditor
		}
	}
	s.follow = true
}
