package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode/utf8"

	"gioui.org/io/clipboard"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/transfer"
	"gioui.org/layout"
)

var isMac = runtime.GOOS == "darwin"

const allMods = key.ModCtrl | key.ModCommand | key.ModShift | key.ModAlt | key.ModSuper

// isAltChord reports whether e is an Alt/Option shortcut ted binds whose
// key would otherwise also type a character on macOS.
func isAltChord(e key.Event) bool {
	m := e.Modifiers
	if !m.Contain(key.ModAlt) || m.Contain(key.ModShortcut) {
		return false
	}
	shift := m.Contain(key.ModShift)
	return e.Name == "R" && !shift || e.Name == "F" && shift
}

func (a *App) handleKeys(gtx layout.Context) {
	filters := []event.Filter{
		key.FocusFilter{Target: &a.keyTag},
		key.Filter{Focus: &a.keyTag, Name: "", Optional: allMods},
		// Gio sends plain Tab and Shift+Tab as system events (for focus
		// moves), and those only match a filter that names the key.
		key.Filter{Focus: &a.keyTag, Name: key.NameTab, Optional: allMods},
		transfer.TargetFilter{Target: &a.keyTag, Type: "application/text"},
	}
	// On macOS an Option chord is typed as text (Option+Shift+F gives "Ï")
	// before its key event arrives. Hold each one-character edit until the
	// next event, and drop it when that turns out to be a chord ted handles.
	var held *key.EditEvent
	flush := func() {
		if held != nil {
			e := *held
			held = nil
			a.onEdit(gtx, e)
		}
	}
	defer flush()
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		a.blinkStart = gtx.Now
		if ke, isKey := ev.(key.Event); isKey && held != nil && ke.State == key.Press && isAltChord(ke) {
			held = nil
		}
		if _, isEdit := ev.(key.EditEvent); !isEdit {
			flush()
		}
		switch e := ev.(type) {
		case key.Event:
			if e.State == key.Press {
				a.onKey(gtx, e)
			}
		case key.EditEvent:
			flush()
			if utf8.RuneCountInString(e.Text) == 1 && e.Text != "\n" && e.Text != "\r" && e.Text != "\t" {
				held = &e
				continue
			}
			a.onEdit(gtx, e)
		case key.SelectionEvent:
			if b := a.buf(); b != nil && a.focus == focusEditor && a.overlay == nil {
				b.anchor = b.posAt(e.Start)
				b.cur = b.posAt(e.End)
				b.selecting = e.Start != e.End
			}
		case transfer.DataEvent:
			rd := e.Open()
			data, err := io.ReadAll(rd)
			rd.Close()
			if err == nil {
				a.onPaste(string(data))
			}
		}
	}
}

func (a *App) onKey(gtx layout.Context, e key.Event) {
	m := e.Modifiers
	sc := m.Contain(key.ModShortcut)
	shift := m.Contain(key.ModShift)
	alt := m.Contain(key.ModAlt)
	ctrl := m.Contain(key.ModCtrl)

	// Global shortcuts.
	if sc {
		switch e.Name {
		case ",":
			if o := a.overlay; o != nil && o.kind == ovSettings {
				a.closeOverlay()
			} else {
				a.openSettings()
			}
			return
		case "V":
			if shift && a.overlay == nil && isMarkdown(a.buf()) {
				a.toggleMarkdownPreview()
				return
			}
			// elsewhere Cmd/Ctrl+Shift+V pastes, as before
		case "K":
			if !shift {
				q := ""
				if b := a.buf(); b != nil && b.hasSel() && !containsNL(b.selText()) {
					q = string(b.selText())
				}
				a.openSearch(q)
				return
			}
		case "Q":
			a.quit()
			return
		case "S":
			if b := a.buf(); b != nil {
				if shift {
					a.saveBufferAs(b)
				} else {
					a.saveBuffer(b, nil)
				}
			}
			return
		case "T":
			if shift {
				a.closeOverlay()
				a.reopenClosedTab()
				return
			}
		case "W":
			a.closeOverlay()
			if a.buf() != nil {
				a.requestClose(a.active)
			}
			return
		case "N":
			// always a quick scratch buffer; Cmd+S asks for a name later
			a.closeOverlay()
			a.newUntitled()
			return
		case "O":
			a.closeOverlay()
			a.prompt("Open file", "", true, func(s string) {
				if s = strings.TrimSpace(s); s != "" {
					p, line, col := splitLineSuffix(s)
					a.openFile(p, line, col)
				}
			})
			return
		case "B":
			if a.root == "" {
				return
			}
			a.showSidebar = !a.showSidebar
			if !a.showSidebar && a.focus == focusTree {
				a.focus = focusEditor
			}
			return
		case "G":
			if shift {
				a.showSideView(sideChanges)
				return
			}
		case "H":
			if shift {
				a.showSideView(sideHistory)
				return
			}
		case "E":
			if shift {
				a.showSideView(sideFiles)
				if b := a.buf(); b != nil && b.path != "" {
					a.tree.reveal(b.path)
				}
				a.ui.treeFollow = true
				return
			}
		case "=", "+":
			if b := a.buf(); b != nil && b.image != nil && a.overlay == nil {
				z := b.image.zoom
				if z <= 0 {
					z = 1
				}
				b.image.zoom = min(16, z*1.25)
				return
			}
			a.fontSp = min(40, a.fontSp+1)
			return
		case "-":
			if b := a.buf(); b != nil && b.image != nil && a.overlay == nil {
				z := b.image.zoom
				if z <= 0 {
					z = 1
				}
				b.image.zoom = max(0.1, z/1.25)
				return
			}
			a.fontSp = max(8, a.fontSp-1)
			return
		case "0":
			if b := a.buf(); b != nil && b.image != nil && a.overlay == nil {
				b.image.zoom, b.image.panX, b.image.panY = 0, 0, 0
				return
			}
			a.fontSp = defaultFontSp
			return
		case "1", "2", "3", "4", "5", "6", "7", "8", "9":
			if i := int(e.Name[0] - '1'); i < len(a.bufs) {
				a.closeOverlay()
				a.active = i
				a.focus = focusEditor
			}
			return
		}
	}
	if e.Name == key.NameTab && ctrl {
		if shift {
			a.cycleTab(-1)
		} else {
			a.cycleTab(1)
		}
		return
	}
	if sc && alt && (e.Name == key.NameLeftArrow || e.Name == key.NameRightArrow) {
		a.cycleTab(map[bool]int{true: -1, false: 1}[e.Name == key.NameLeftArrow])
		return
	}
	if e.Name == "F1" {
		a.overlay = &Overlay{hover: -1, hoverBtn: -1, kind: ovHelp}
		return
	}

	if a.overlay != nil {
		if sc && e.Name == "V" && (a.overlay.kind == ovSearch || a.overlay.kind == ovPrompt || a.overlay.kind == ovPick) {
			a.pasteTarget = a.focus
			gtx.Execute(clipboard.ReadCmd{Tag: &a.keyTag})
			return
		}
		a.overlayKey(gtx, e)
		return
	}
	b := a.buf()
	if b == nil || a.focus == focusTree {
		if a.sideView != sideFiles {
			a.scmKey(e)
			return
		}
		a.treeKey(gtx, e)
		return
	}
	if b.image != nil {
		return // nothing to edit in an image
	}
	if b.previewing() && a.focus != focusFind {
		if a.mdKeyPress(b, e) {
			return
		}
	}
	// Find bar and editor share these.
	switch {
	case sc && e.Name == "F" && !alt:
		a.openFind(false)
		return
	case sc && (e.Name == "H" || (e.Name == "F" && alt)):
		a.openFind(true)
		return
	case e.Name == "F" && alt && shift && !sc && a.focus != focusFind:
		a.formatJSON(b)
		return
	case e.Name == "G" && ctrl && (isMac || !shift):
		a.gotoLinePrompt()
		return
	case e.Name == "G" && sc:
		a.findStep(!shift)
		return
	case e.Name == "F3":
		a.findStep(!shift)
		return
	case sc && e.Name == "V":
		a.pasteTarget = a.focus
		gtx.Execute(clipboard.ReadCmd{Tag: &a.keyTag})
		return
	}
	if a.focus == focusFind && a.find != nil {
		a.findKey(gtx, e)
		return
	}
	a.editorKey(gtx, b, e)
}

func (a *App) cycleTab(d int) {
	if len(a.bufs) == 0 {
		return
	}
	a.closeOverlay()
	a.active = (a.active + d + len(a.bufs)) % len(a.bufs)
	a.focus = focusEditor
}

func (a *App) gotoLinePrompt() {
	b := a.buf()
	if b == nil {
		return
	}
	a.prompt(fmt.Sprintf("Go to line (1 – %d), or line:column", len(b.lines)), "", false, func(s string) {
		var ln, col int
		s = strings.TrimSpace(s)
		if _, err := fmt.Sscanf(strings.Replace(s, ":", " ", 1), "%d %d", &ln, &col); err != nil {
			if _, err := fmt.Sscanf(s, "%d", &ln); err != nil {
				return
			}
		}
		if ln < 0 {
			ln = len(b.lines) + ln + 1
		}
		a.gotoPos(b, ln, max(col, 1))
	})
}

func (a *App) editorKey(gtx layout.Context, b *Buffer, e key.Event) {
	m := e.Modifiers
	sc := m.Contain(key.ModShortcut)
	shift := m.Contain(key.ModShift)
	alt := m.Contain(key.ModAlt)
	word := m.Contain(key.ModShortcutAlt) // Alt on macOS, Ctrl elsewhere
	if b.csv != nil && b.csv.block != nil {
		switch {
		case sc && (e.Name == "C" || e.Name == "X"):
			// copy the cells as tab-separated rows (never cut: that would
			// leave ragged rows)
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(b.blockText()))})
			a.lineCopy = ""
			st, _ := b.selectionStats()
			a.setMsg("Copied %s", st.detail())
			return
		case e.Name == key.NameEscape:
			b.clearBlock()
			return
		case e.Name == key.NameShift || e.Name == key.NameAlt || e.Name == key.NameCommand || e.Name == key.NameCtrl:
			// a bare modifier
		default:
			b.clearBlock()
		}
	}
	a.follow = true
	vertical := false

	move := func(f func()) {
		if shift {
			if !b.hasSel() {
				b.anchor = b.cur
			}
			b.selecting = true
		} else if b.hasSel() && (e.Name == key.NameLeftArrow || e.Name == key.NameRightArrow) && !word && !sc {
			s, en := b.selRange()
			b.selecting = false
			if e.Name == key.NameLeftArrow {
				b.cur = s
			} else {
				b.cur = en
			}
			return
		} else {
			b.selecting = false
		}
		f()
	}
	lineStart := func() {
		ind := len(leadingWS(b.lines[b.cur.L]))
		if b.cur.C == ind {
			b.cur.C = 0
		} else {
			b.cur.C = ind
		}
	}
	page := max(1, a.ui.editor.Dy()/a.ui.lineH-2)

	switch e.Name {
	case key.NameLeftArrow:
		move(func() {
			switch {
			case sc && isMac:
				lineStart()
			case word:
				b.cur = b.wordMove(b.cur, -1)
			case b.cur.C > 0:
				b.cur.C--
			case b.cur.L > 0:
				b.cur = Pos{b.cur.L - 1, len(b.lines[b.cur.L-1])}
			}
		})
	case key.NameRightArrow:
		move(func() {
			switch {
			case sc && isMac:
				b.cur.C = len(b.lines[b.cur.L])
			case word:
				b.cur = b.wordMove(b.cur, 1)
			case b.cur.C < len(b.lines[b.cur.L]):
				b.cur.C++
			case b.cur.L < len(b.lines)-1:
				b.cur = Pos{b.cur.L + 1, 0}
			}
		})
	case key.NameUpArrow, key.NameDownArrow:
		d := 1
		if e.Name == key.NameUpArrow {
			d = -1
		}
		switch {
		case alt && shift:
			b.duplicateLines(d > 0)
		case alt:
			b.moveLines(d)
		case sc && isMac:
			move(func() {
				if d < 0 {
					b.cur = Pos{0, 0}
				} else {
					l := len(b.lines) - 1
					b.cur = Pos{l, len(b.lines[l])}
				}
			})
		case m.Contain(key.ModCtrl) && !isMac:
			b.vy = max(0, b.vy+float32(d*a.ui.lineH))
			a.follow = false
		default:
			vertical = true
			move(func() { b.vmove(d) })
		}
	case key.NamePageUp:
		vertical = true
		move(func() { b.vmove(-page); b.vy -= float32(page * a.ui.lineH) })
	case key.NamePageDown:
		vertical = true
		move(func() { b.vmove(page); b.vy += float32(page * a.ui.lineH) })
	case key.NameHome:
		move(func() {
			if sc || m.Contain(key.ModCtrl) {
				b.cur = Pos{0, 0}
			} else {
				lineStart()
			}
		})
	case key.NameEnd:
		move(func() {
			if sc || m.Contain(key.ModCtrl) {
				l := len(b.lines) - 1
				b.cur = Pos{l, len(b.lines[l])}
			} else {
				b.cur.C = len(b.lines[b.cur.L])
			}
		})
	case key.NameEscape:
		if a.find != nil {
			a.closeFind()
		}
		b.selecting = false
		a.msg = ""
	case key.NameReturn, key.NameEnter:
		if b.readOnly {
			break
		}
		switch {
		case sc && shift:
			b.selecting = false
			b.cur.C = 0
			b.insert(append(append([]rune(nil), leadingWS(b.lines[b.cur.L])...), '\n'), "")
			b.cur = Pos{b.cur.L - 1, len(b.lines[b.cur.L-1])}
			b.setLastCurA(b.cur)
		case sc:
			b.selecting = false
			b.cur.C = len(b.lines[b.cur.L])
			b.newline()
		default:
			b.newline()
		}
	case key.NameTab:
		b.indentSel(shift)
	case key.NameDeleteBackward:
		switch {
		case b.hasSel():
			b.deleteSel()
		case sc && isMac:
			b.deleteRange(Pos{b.cur.L, 0}, b.cur, "")
		case word:
			b.deleteRange(b.wordMove(b.cur, -1), b.cur, "")
		default:
			b.backspace()
		}
	case key.NameDeleteForward:
		if shift && !word && !sc {
			a.copy(gtx, b, true) // Shift+Delete cuts (Omarchy's Super+X sends it)
			return
		}
		if word && !b.hasSel() {
			b.deleteRange(b.cur, b.wordMove(b.cur, 1), "")
		} else {
			b.deleteForward()
		}
	case "Z":
		if sc {
			if shift {
				b.doRedo()
			} else if !b.doUndo() {
				a.setMsg("Nothing to undo")
			}
		}
	case "Y":
		if sc {
			b.doRedo()
		}
	case "A":
		if sc {
			b.selectAll()
			a.follow = false
		}
	case "C", "X":
		if sc {
			a.copy(gtx, b, e.Name == "X")
		}
	case "D":
		if sc {
			if q := b.selectNextOccurrence(); q != nil {
				a.lastQ = q
			}
		}
	case "L":
		if sc {
			b.selectLine()
		}
	case "K":
		if sc && shift {
			b.deleteLines()
		}
	case "/":
		if sc {
			if !b.toggleComment() {
				a.setMsg("No line comment syntax for this file type")
			}
		}
	case "R":
		if alt && !sc {
			a.reload(b)
		}
	default:
		a.follow = false
	}
	if !vertical {
		b.wantCol = -1
	}
}

func (a *App) copy(gtx layout.Context, b *Buffer, cut bool) {
	var text string
	if b.hasSel() {
		text = string(b.selText())
		a.lineCopy = ""
		if cut {
			b.deleteSel()
		}
	} else {
		text = string(b.lines[b.cur.L]) + "\n"
		a.lineCopy = text
		if cut {
			b.deleteLines()
		}
	}
	gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(text))})
}

func (a *App) onPaste(s string) {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	switch {
	case a.overlay != nil:
		a.overlayText(s)
		return
	case a.pasteTarget == focusFind && a.find != nil:
		a.findText(s)
		return
	}
	b := a.buf()
	if b == nil || s == "" || b.previewing() {
		return
	}
	if b.readOnly {
		a.setMsg("%s is a read-only view", b.name())
		return
	}
	if b.clearBlock() {
		a.setMsg("Cell selection cleared; paste goes at the caret")
	}
	b.breakUndo()
	wasEmpty := len(b.lines) == 1 && len(b.lines[0]) == 0
	if s == a.lineCopy && !b.hasSel() {
		col := b.cur.C
		b.cur = Pos{b.cur.L, 0}
		b.insert([]rune(s), "")
		b.cur.C = min(col, len(b.lines[b.cur.L]))
	} else {
		b.insert([]rune(s), "")
	}
	// Text pasted into an empty buffer sets its indent style, as it would
	// have when opened from a file. Only indented text says anything about
	// it, so an indent picked by hand survives other pastes.
	if wasEmpty && hasIndentedLine(b.lines) {
		b.indent = detectIndent(b.lines, b.path)
	}
	b.breakUndo()
	a.follow = true
}

// onEdit receives typed text from the platform input system.
func (a *App) onEdit(gtx layout.Context, e key.EditEvent) {
	if e.Text == "\n" || e.Text == "\r" || e.Text == "\t" {
		return // handled as key events
	}
	switch {
	case a.overlay != nil:
		a.overlayText(e.Text)
		return
	case a.focus == focusFind && a.find != nil:
		a.findText(e.Text)
		return
	}
	b := a.buf()
	if b == nil || a.focus == focusTree {
		if a.sideView == sideFiles {
			for _, r := range e.Text {
				a.treeChar(gtx, r)
			}
		}
		return
	}
	if b.previewing() {
		return
	}
	if b.readOnly {
		a.setMsg("%s is a read-only view", b.name())
		return
	}
	a.follow = true
	b.wantCol = -1
	b.clearBlock()
	lo, hi := e.Range.Start, e.Range.End
	if lo > hi {
		lo, hi = hi, lo
	}
	curOff := b.offsetOf(b.cur)
	plain := lo == hi && lo == curOff && !b.hasSel()
	if b.hasSel() {
		s, en := b.selRange()
		plain = lo == b.offsetOf(s) && hi == b.offsetOf(en)
	}
	if plain || lo < 0 {
		b.typeText(e.Text)
		return
	}
	// The input method is replacing a range (dead keys, composition).
	p0, p1 := b.posAt(lo), b.posAt(hi)
	b.selecting = false
	end := b.replaceRange(p0, p1, []rune(e.Text))
	b.cur = end
}

// ---- file tree ----

func (a *App) treeKey(gtx layout.Context, e key.Event) {
	t := a.tree
	n := t.selected()
	sc := e.Modifiers.Contain(key.ModShortcut)
	a.ui.treeFollow = true
	switch e.Name {
	case key.NameUpArrow:
		t.sel = max(0, t.sel-1)
	case key.NameDownArrow:
		t.sel = min(len(t.rows)-1, t.sel+1)
	case key.NamePageUp:
		t.sel = max(0, t.sel-15)
	case key.NamePageDown:
		t.sel = min(len(t.rows)-1, t.sel+15)
	case key.NameHome:
		t.sel = 0
	case key.NameEnd:
		t.sel = len(t.rows) - 1
	case key.NameReturn, key.NameEnter:
		a.treeActivate(n, true)
	case key.NameRightArrow:
		if n != nil && n.isDir && !n.expanded {
			t.toggle(n)
		} else if n != nil && n.isDir {
			t.sel = min(len(t.rows)-1, t.sel+1)
		} else {
			a.treeActivate(n, false)
		}
	case key.NameLeftArrow:
		a.treeLeft(n)
	case key.NameEscape:
		if a.buf() != nil {
			a.focus = focusEditor
		}
	case key.NameDeleteForward:
		a.treeDelete(n)
	case key.NameDeleteBackward:
		if sc {
			a.treeDelete(n)
		}
	case "F2":
		a.treeRename(n)
	case "C":
		if sc && n != nil {
			p := a.rel(n.path)
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(p))})
			a.setMsg("Copied path %s", p)
		}
	}
}

func (a *App) treeChar(gtx layout.Context, r rune) {
	t := a.tree
	n := t.selected()
	a.ui.treeFollow = true
	switch r {
	case 'j':
		t.sel = min(len(t.rows)-1, t.sel+1)
	case 'k':
		t.sel = max(0, t.sel-1)
	case 'l', ' ':
		if n != nil && n.isDir && !n.expanded {
			t.toggle(n)
		} else {
			a.treeActivate(n, false)
		}
	case 'h':
		a.treeLeft(n)
	case 'a', 'n':
		a.treeNew(n)
	case 'r':
		a.treeRename(n)
	case 'd':
		a.treeDelete(n)
	case '.':
		t.showHidden = !t.showHidden
		t.refresh()
		a.setMsg("Dotfiles %s", map[bool]string{true: "shown", false: "hidden"}[t.showHidden])
	case 'R':
		t.refresh()
		a.refreshGit()
		a.loadFiles()
	case 'c':
		for _, c := range t.root.children {
			c.expanded = false
		}
		t.flatten()
		t.sel = 0
	case 'y':
		if n != nil {
			p := a.rel(n.path)
			gtx.Execute(clipboard.WriteCmd{Type: "application/text", Data: io.NopCloser(strings.NewReader(p))})
			a.setMsg("Copied path %s", p)
		}
	}
}

func (a *App) treeLeft(n *node) {
	t := a.tree
	if n == nil {
		return
	}
	if n.isDir && n.expanded {
		t.toggle(n)
		return
	}
	if n.parent != nil && n.parent != t.root {
		for i, r := range t.rows {
			if r == n.parent {
				t.sel = i
				break
			}
		}
	}
}

func (a *App) treeActivate(n *node, focusEditorAfter bool) {
	if n == nil {
		return
	}
	if n.isDir {
		a.tree.toggle(n)
		return
	}
	if focusEditorAfter {
		a.openFile(n.path, 0, 0) // Enter opens a permanent tab
		return
	}
	if a.openPreview(n.path, 0, 0) {
		a.focus = focusTree
	}
}

func (a *App) targetDir(n *node) string {
	if n == nil {
		return a.root
	}
	if n.isDir {
		return n.path
	}
	return filepath.Dir(n.path)
}

func (a *App) treeNew(n *node) {
	dir := a.rel(a.targetDir(n))
	if dir == "." {
		dir = ""
	} else {
		dir += "/"
	}
	a.prompt("New file (end with / to create a folder)", dir, true, func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		path := a.abs(s)
		if strings.HasSuffix(s, "/") {
			if err := os.MkdirAll(path, 0o755); err != nil {
				a.setErr("%v", err)
				return
			}
			a.tree.refresh()
			a.tree.reveal(path)
			return
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			a.setErr("%v", err)
			return
		}
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, nil, 0o644); err != nil {
				a.setErr("%v", err)
				return
			}
		}
		a.tree.refresh()
		a.openFile(path, 0, 0)
		a.loadFiles()
	})
}

func (a *App) treeRename(n *node) {
	if n == nil {
		return
	}
	a.prompt("Rename or move", a.rel(n.path), true, func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		dst := a.abs(s)
		if dst == n.path {
			return
		}
		if stDst, err := os.Stat(dst); err == nil {
			// Allow case-only renames on case-insensitive filesystems (e.g. macOS APFS).
			stSrc, err2 := os.Stat(n.path)
			if err2 != nil || !os.SameFile(stSrc, stDst) {
				a.setErr("%s already exists", s)
				return
			}
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			a.setErr("%v", err)
			return
		}
		if err := os.Rename(n.path, dst); err != nil {
			a.setErr("%v", err)
			return
		}
		for _, b := range a.bufs {
			if b.path == n.path {
				b.path = dst
				if !b.hl.manual {
					b.hl.resolved = false
					b.hl.lexer = nil
					b.hl.spans = nil
					b.hl.version = -1
					b.csv = nil
					b.resolveLexer()
				}
			} else if strings.HasPrefix(b.path, n.path+"/") {
				b.path = dst + b.path[len(n.path):]
			}
		}
		a.tree.refresh()
		a.tree.reveal(dst)
		a.refreshGit()
		a.loadFiles()
		a.setMsg("Renamed to %s", a.rel(dst))
	})
}

// moveToTrash sends path to the OS Trash when available, falling back to RemoveAll.
func moveToTrash(path string) error {
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("trash"); err == nil {
			if exec.Command("trash", path).Run() == nil {
				return nil
			}
		}
		script := `on run argv
			tell application "Finder" to delete (POSIX file (item 1 of argv) as alias)
		end run`
		if exec.Command("osascript", "-e", script, "--", path).Run() == nil {
			return nil
		}
	case "linux":
		if _, err := exec.LookPath("gio"); err == nil {
			if exec.Command("gio", "trash", path).Run() == nil {
				return nil
			}
		}
	}
	return os.RemoveAll(path)
}

func (a *App) treeDelete(n *node) {
	if n == nil {
		return
	}
	what := "file"
	if n.isDir {
		what = "folder and everything in it"
	}
	a.confirm(fmt.Sprintf("Delete %s %s?", what, a.rel(n.path)), []button{
		{"Delete", 'y', func() {
			if err := moveToTrash(n.path); err != nil {
				a.setErr("%v", err)
				return
			}
			for i := len(a.bufs) - 1; i >= 0; i-- {
				if p := a.bufs[i].path; (p == n.path || strings.HasPrefix(p, n.path+"/")) && !a.bufs[i].dirty() {
					a.closeBuffer(i)
				}
			}
			a.tree.refresh()
			a.refreshGit()
			a.loadFiles()
			a.setMsg("Deleted %s", a.rel(n.path))
		}},
		{"Cancel", 'c', func() {}},
	})
}
