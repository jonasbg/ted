package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
	"unsafe"
)

// Pos is a position in a buffer: line index and rune index within the line.
type Pos struct{ L, C int }

func (a Pos) Less(b Pos) bool { return a.L < b.L || (a.L == b.L && a.C < b.C) }

func orderPos(a, b Pos) (Pos, Pos) {
	if b.Less(a) {
		return b, a
	}
	return a, b
}

type editKind int

const (
	editInsert editKind = iota
	editDelete
)

type edit struct {
	kind editKind
	at   Pos
	text []rune
	curB Pos // cursor before
	curA Pos // cursor after
}

type undoGroup struct {
	id    int
	edits []edit
	when  time.Time
	kind  string // "type", "del", or "" (never merges)
	// the decoded base64 regions before and after this group, so undo
	// and redo put them back with the text
	before, after []b64Span
}

// Buffer holds one open file.
type Buffer struct {
	path      string // absolute path, "" for untitled
	lines     [][]rune
	crlf      bool
	indent    string // indent unit: "\t" or N spaces
	mode      os.FileMode
	modTime   time.Time
	size      int64
	diskGone  bool // file was deleted on disk
	conflict  bool // changed on disk while we have unsaved edits
	preview   bool // VS Code style preview tab: replaced by the next preview
	untitledN int  // number shown for unsaved new buffers
	metaDirty bool // a change that isn't a text edit (line endings)

	// generated, read-only content (diffs, commits)
	title      string
	virtualKey string
	virtualSrc func() string
	readOnly   bool
	diffView   bool

	cur       Pos
	anchor    Pos
	selecting bool
	wantCol   int // sticky visual column for vertical movement

	// view state (pixels), owned by the UI
	scrollY        int     // first visible line, kept for the highlighter
	vy, vx         float32 // scroll offsets in pixels
	centerOnCursor bool
	widest         int         // cached width of the widest line, in columns
	lineWidths     []int       // width of each line, in columns; nil until first display
	widthCount     map[int]int // how many lines have each width
	lineStarts     []int
	lineStartsVer  int

	undo    []undoGroup
	redo    []undoGroup
	nextID  int
	savedID int

	version int // bumped on every change; used by the highlighter
	hl      *highlightState
	csv     *csvState  // set for delimited files (rainbow columns)
	md      *mdState   // Markdown preview, once opened
	image   *imageView // set for image files, shown instead of text
	b64     []b64Span  // base64 decoded in place (Cmd/Ctrl+click)
	// jsonView: minified JSON on disk, shown formatted (not an edit).
	// jsonRaw: the user asked to see this file as it is on disk.
	jsonView, jsonRaw bool
	spansPending      []b64Span // decoded regions before the edit in progress
	spansNoted        bool
	inUndo            bool
}

func newBuffer(path string) *Buffer {
	b := &Buffer{path: path, lines: [][]rune{{}}, indent: "    ", mode: 0o644}
	b.hl = &highlightState{}
	return b
}

var errBinary = errors.New("binary file")

func loadBuffer(path string) (*Buffer, error) {
	b := newBuffer(path)
	st, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			b.indent = defaultIndent(path)
			return b, nil // new file
		}
		return nil, err
	}
	if st.IsDir() {
		return nil, errors.New("is a directory")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	head := data
	if len(head) > 8000 {
		head = head[:8000]
	}
	if bytes.IndexByte(head, 0) >= 0 {
		return nil, errBinary
	}
	if out, ok := structuredJSON(path, string(data), defaultIndent(path)); ok {
		crlf := bytes.Contains(data, []byte("\r\n"))
		b.setContent([]byte(out))
		b.crlf, b.jsonView = crlf, true
	} else {
		b.setContent(data)
	}
	b.mode = st.Mode().Perm()
	b.modTime = st.ModTime()
	b.size = st.Size()
	return b, nil
}

func (b *Buffer) setContent(data []byte) {
	s := string(data)
	b.crlf = strings.Contains(s, "\r\n")
	if b.crlf {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	b.lines = splitLines(s)
	b.lineWidths, b.widthCount = nil, nil
	b.widest = 0
	b.indent = detectIndent(b.lines, b.path)
	b.version++
}

func splitLines(s string) [][]rune {
	parts := strings.Split(s, "\n")
	lines := make([][]rune, len(parts))
	for i, p := range parts {
		lines[i] = []rune(p)
	}
	return lines
}

func (b *Buffer) text() string {
	n := max(0, len(b.lines)-1)
	for _, line := range b.lines {
		for _, r := range line {
			switch l := utf8.RuneLen(r); {
			case r < utf8.RuneSelf:
				n++
			case l > 0:
				n += l
			default:
				n += 3 // AppendRune writes U+FFFD for invalid runes
			}
		}
	}
	if n == 0 {
		return ""
	}
	buf := make([]byte, 0, n)
	for i, line := range b.lines {
		if i > 0 {
			buf = append(buf, '\n')
		}
		for _, r := range line {
			if r < utf8.RuneSelf {
				buf = append(buf, byte(r))
			} else {
				buf = utf8.AppendRune(buf, r)
			}
		}
	}
	// buf is never touched again, so the string can share its memory
	return unsafe.String(unsafe.SliceData(buf), len(buf))
}

func (b *Buffer) name() string {
	if b.title != "" {
		return b.title
	}
	if b.path == "" {
		return fmt.Sprintf("Untitled-%d", max(1, b.untitledN))
	}
	return filepath.Base(b.path)
}

func (b *Buffer) dirty() bool { return b.topID() != b.savedID || b.metaDirty }

func (b *Buffer) topID() int {
	if len(b.undo) == 0 {
		return 0
	}
	return b.undo[len(b.undo)-1].id
}

func (b *Buffer) save() error {
	if b.path == "" {
		return errors.New("no file name")
	}
	if b.jsonView && !b.dirty() && !b.diskGone {
		if st, err := os.Stat(b.path); err == nil && st.ModTime().Equal(b.modTime) && st.Size() == b.size {
			return nil // only the view is formatted; the file is unchanged
		}
	}
	b.encodeAllSpans()
	b.jsonView = false // edited: the formatted text is what gets saved
	data := b.text()
	if b.crlf {
		data = strings.ReplaceAll(data, "\n", "\r\n")
	}
	target := b.path
	if r, err := filepath.EvalSymlinks(target); err == nil {
		target = r
	}
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	// Write atomically: temp file in the same dir, then rename.
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(target)+".ted-*")
	if err != nil {
		// Directory not writable for temp files; fall back to in-place write.
		if err2 := os.WriteFile(target, []byte(data), b.mode); err2 != nil {
			return err2
		}
	} else {
		_, werr := tmp.WriteString(data)
		cerr := tmp.Close()
		if werr == nil {
			werr = cerr
		}
		if werr == nil {
			werr = os.Chmod(tmp.Name(), b.mode)
		}
		if werr == nil {
			werr = os.Rename(tmp.Name(), target)
		}
		if werr != nil {
			os.Remove(tmp.Name())
			return werr
		}
	}
	if st, err := os.Stat(b.path); err == nil {
		b.modTime, b.size = st.ModTime(), st.Size()
	}
	b.savedID = b.topID()
	b.metaDirty = false
	b.conflict, b.diskGone = false, false
	return nil
}

// ---- primitive edits ----

func (b *Buffer) clamp(p Pos) Pos {
	if p.L < 0 {
		return Pos{0, 0}
	}
	if p.L >= len(b.lines) {
		l := len(b.lines) - 1
		return Pos{l, len(b.lines[l])}
	}
	if p.C < 0 {
		p.C = 0
	}
	if p.C > len(b.lines[p.L]) {
		p.C = len(b.lines[p.L])
	}
	return p
}

// rawInsert inserts text at p and returns the end position.
func (b *Buffer) rawInsert(p Pos, text []rune) Pos {
	b.noteSpans()
	b.version++
	line := b.lines[p.L]
	tail := append([]rune(nil), line[p.C:]...)
	head := line[:p.C]
	var segs [][]rune
	start := 0
	for i, r := range text {
		if r == '\n' {
			segs = append(segs, text[start:i])
			start = i + 1
		}
	}
	segs = append(segs, text[start:])
	if len(segs) == 1 {
		nl := make([]rune, 0, len(head)+len(text)+len(tail))
		nl = append(append(append(nl, head...), text...), tail...)
		b.lines[p.L] = nl
		b.updateWidths(p.L, 1, 1)
		end := Pos{p.L, p.C + len(text)}
		b.shiftB64Insert(p, end)
		return end
	}
	newLines := make([][]rune, len(segs))
	newLines[0] = append(append([]rune(nil), head...), segs[0]...)
	for i := 1; i < len(segs)-1; i++ {
		newLines[i] = append([]rune(nil), segs[i]...)
	}
	last := segs[len(segs)-1]
	newLines[len(segs)-1] = append(append([]rune(nil), last...), tail...)
	b.hl.spliceLines(p.L+1, 0, len(segs)-1)
	b.lines = slices.Replace(b.lines, p.L, p.L+1, newLines...)
	b.updateWidths(p.L, 1, len(newLines))
	end := Pos{p.L + len(segs) - 1, len(last)}
	b.shiftB64Insert(p, end)
	return end
}

func (b *Buffer) textRange(a, c Pos) []rune {
	a, c = orderPos(a, c)
	if a.L == c.L {
		return append([]rune(nil), b.lines[a.L][a.C:c.C]...)
	}
	var out []rune
	out = append(out, b.lines[a.L][a.C:]...)
	for l := a.L + 1; l < c.L; l++ {
		out = append(out, '\n')
		out = append(out, b.lines[l]...)
	}
	out = append(out, '\n')
	out = append(out, b.lines[c.L][:c.C]...)
	return out
}

func (b *Buffer) rawDelete(a, c Pos) []rune {
	a, c = orderPos(a, c)
	removed := b.textRange(a, c)
	b.noteSpans()
	b.version++
	if len(b.b64) > 0 {
		b.shiftB64Delete(a, c)
	}
	head := b.lines[a.L][:a.C]
	tail := b.lines[c.L][c.C:]
	nl := make([]rune, 0, len(head)+len(tail))
	nl = append(append(nl, head...), tail...)
	if a.L == c.L {
		b.lines[a.L] = nl
		b.updateWidths(a.L, 1, 1)
		return removed
	}
	b.hl.spliceLines(a.L+1, c.L-a.L, 0)
	b.lines = slices.Replace(b.lines, a.L, c.L+1, nl)
	b.updateWidths(a.L, c.L-a.L+1, 1)
	return removed
}

// ---- undoable edits ----

func (b *Buffer) pushEdit(e edit, kind string) {
	b.redo = nil
	now := time.Now()
	if kind != "" && len(b.undo) > 0 {
		g := &b.undo[len(b.undo)-1]
		if g.kind == kind && now.Sub(g.when) < 1500*time.Millisecond && g.id != b.savedID {
			last := g.edits[len(g.edits)-1]
			if last.curA == e.curB {
				g.edits = append(g.edits, e)
				g.when = now
				b.spansNoted = false
				return
			}
		}
	}
	b.nextID++
	b.undo = append(b.undo, undoGroup{id: b.nextID, edits: []edit{e}, when: now, kind: kind, before: b.takeSpans()})
}

// breakUndo makes the next edit start a new undo group.
func (b *Buffer) breakUndo() {
	if len(b.undo) > 0 {
		b.undo[len(b.undo)-1].kind = ""
	}
}

// insert inserts text at the cursor (replacing the selection) and records undo.
func (b *Buffer) insert(text []rune, kind string) {
	if b.readOnly {
		return
	}
	if b.hasSel() {
		b.deleteSel()
		kind = ""
	}
	before := b.cur
	end := b.rawInsert(b.cur, text)
	b.cur = end
	b.pushEdit(edit{kind: editInsert, at: before, text: append([]rune(nil), text...), curB: before, curA: end}, kind)
	b.selecting = false
}

func (b *Buffer) deleteRange(a, c Pos, kind string) {
	if b.readOnly {
		return
	}
	a, c = orderPos(a, c)
	if a == c {
		return
	}
	curB := b.cur
	removed := b.rawDelete(a, c)
	b.cur = a
	b.pushEdit(edit{kind: editDelete, at: a, text: removed, curB: curB, curA: a}, kind)
	b.selecting = false
}

// replaceRange is a delete+insert recorded as one undo group.
func (b *Buffer) replaceRange(a, c Pos, text []rune) Pos {
	if b.readOnly {
		return a
	}
	a, c = orderPos(a, c)
	b.breakUndo()
	curB := b.cur
	var g []edit
	if a != c {
		removed := b.rawDelete(a, c)
		g = append(g, edit{kind: editDelete, at: a, text: removed, curB: curB, curA: a})
	}
	end := a
	if len(text) > 0 {
		end = b.rawInsert(a, text)
		g = append(g, edit{kind: editInsert, at: a, text: append([]rune(nil), text...), curB: a, curA: end})
	}
	if len(g) > 0 {
		b.redo = nil
		b.nextID++
		b.undo = append(b.undo, undoGroup{id: b.nextID, edits: g, when: time.Now(), before: b.takeSpans()})
	}
	return end
}

// beginGroup / endGroup allow several edits to collapse into one undo step.
func (b *Buffer) groupSince(n int) {
	if len(b.undo)-n <= 1 {
		if len(b.undo) > n {
			b.undo[len(b.undo)-1].kind = ""
		}
		return
	}
	merged := undoGroup{id: b.undo[len(b.undo)-1].id, when: time.Now(), before: b.undo[n].before}
	for _, g := range b.undo[n:] {
		merged.edits = append(merged.edits, g.edits...)
	}
	b.undo = append(b.undo[:n], merged)
}

func (b *Buffer) doUndo() bool {
	if len(b.undo) == 0 {
		return false
	}
	b.undo[len(b.undo)-1].after = slices.Clone(b.b64)
	g := b.undo[len(b.undo)-1]
	b.inUndo = true
	defer func() { b.inUndo = false; b.b64 = slices.Clone(g.before) }()
	b.undo = b.undo[:len(b.undo)-1]
	for i := len(g.edits) - 1; i >= 0; i-- {
		e := g.edits[i]
		if e.kind == editInsert {
			b.rawDelete(e.at, endOf(e.at, e.text))
		} else {
			b.rawInsert(e.at, e.text)
		}
		b.cur = e.curB
	}
	b.redo = append(b.redo, g)
	b.selecting = false
	b.cur = b.clamp(b.cur)
	return true
}

func (b *Buffer) doRedo() bool {
	if len(b.redo) == 0 {
		return false
	}
	g := b.redo[len(b.redo)-1]
	b.inUndo = true
	defer func() { b.inUndo = false; b.b64 = slices.Clone(g.after) }()
	b.redo = b.redo[:len(b.redo)-1]
	for _, e := range g.edits {
		if e.kind == editInsert {
			b.rawInsert(e.at, e.text)
		} else {
			b.rawDelete(e.at, endOf(e.at, e.text))
		}
		b.cur = e.curA
	}
	g.kind = ""
	b.undo = append(b.undo, g)
	b.selecting = false
	b.cur = b.clamp(b.cur)
	return true
}

// noteSpans remembers the decoded regions before the first raw change of
// an edit; the undo group the edit makes takes them as its "before".
func (b *Buffer) noteSpans() {
	if b.inUndo || b.spansNoted {
		return
	}
	b.spansPending = slices.Clone(b.b64)
	b.spansNoted = true
}

func (b *Buffer) takeSpans() []b64Span {
	s := b.spansPending
	b.spansPending, b.spansNoted = nil, false
	return s
}

func endOf(p Pos, text []rune) Pos {
	for _, r := range text {
		if r == '\n' {
			p.L++
			p.C = 0
		} else {
			p.C++
		}
	}
	return p
}

// ---- selection ----

func (b *Buffer) hasSel() bool { return b.selecting && b.anchor != b.cur }

func (b *Buffer) selRange() (Pos, Pos) { return orderPos(b.anchor, b.cur) }

func (b *Buffer) selText() []rune {
	if !b.hasSel() {
		return nil
	}
	a, c := b.selRange()
	return b.textRange(a, c)
}

func (b *Buffer) deleteSel() {
	if !b.hasSel() {
		return
	}
	a, c := b.selRange()
	b.deleteRange(a, c, "")
}

// ---- reload from disk ----

// reloadFromDisk replaces the content with what's on disk as a single undoable
// edit covering only the changed region, so the cursor stays put and the
// change can be undone with Ctrl+Z.
func (b *Buffer) reloadFromDisk() error {
	data, err := os.ReadFile(b.path)
	if err != nil {
		return err
	}
	st, err := os.Stat(b.path)
	if err != nil {
		return err
	}
	s := string(data)
	crlf := strings.Contains(s, "\r\n")
	if crlf {
		s = strings.ReplaceAll(s, "\r\n", "\n")
	}
	b.crlf = crlf
	b.modTime, b.size = st.ModTime(), st.Size()
	b.conflict, b.diskGone = false, false
	if !b.jsonRaw {
		if out, ok := structuredJSON(b.path, s, b.indent); ok {
			s = out
		}
	}
	b.jsonView = s != strings.ReplaceAll(string(data), "\r\n", "\n")
	b.replaceAllMinimal(s)
	b.savedID = b.topID()
	return nil
}

// replaceAllMinimal makes the buffer hold newT by replacing only the region
// that differs, as one undo step, keeping the cursor on unchanged content.
// It reports whether anything changed.
//
// The common ends are found on bytes; only the middle becomes runes.
func (b *Buffer) replaceAllMinimal(newS string) bool {
	oldS := b.text()
	if oldS == newS {
		return false
	}
	pre := 0
	for pre < len(oldS) && pre < len(newS) && oldS[pre] == newS[pre] {
		pre++
	}
	for pre > 0 && (pre < len(oldS) && !utf8.RuneStart(oldS[pre]) || pre < len(newS) && !utf8.RuneStart(newS[pre])) {
		pre--
	}
	suf := 0
	for suf < len(oldS)-pre && suf < len(newS)-pre && oldS[len(oldS)-1-suf] == newS[len(newS)-1-suf] {
		suf++
	}
	for suf > 0 && !utf8.RuneStart(oldS[len(oldS)-suf]) {
		suf--
	}
	preR := utf8.RuneCountInString(oldS[:pre])
	a := b.posFromOffset(preR)
	c := b.posFromOffset(preR + utf8.RuneCountInString(oldS[pre:len(oldS)-suf]))
	cur := b.cur
	oldLines := len(b.lines)
	end := b.replaceRange(a, c, []rune(newS[pre:len(newS)-suf]))
	b.breakUndo()
	// Keep the cursor stable relative to unchanged content.
	switch {
	case cur.Less(a):
		b.cur = cur
	case !cur.Less(c) && cur.L == c.L:
		b.cur = Pos{end.L, end.C + cur.C - c.C}
	case !cur.Less(c):
		b.cur = Pos{cur.L + len(b.lines) - oldLines, cur.C}
	default:
		b.cur = a
	}
	b.cur = b.clamp(b.cur)
	b.selecting = false
	return true
}

func (b *Buffer) posFromOffset(off int) Pos {
	for i, l := range b.lines {
		if off <= len(l) {
			return Pos{i, off}
		}
		off -= len(l) + 1
	}
	last := len(b.lines) - 1
	return Pos{last, len(b.lines[last])}
}

func runesEq(a, b []rune) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// ---- indentation helpers ----

func defaultIndent(path string) string {
	base := strings.ToLower(filepath.Base(path))
	ext := filepath.Ext(base)
	if ext == ".go" || base == "makefile" || base == "gnumakefile" || ext == ".mk" {
		return "\t"
	}
	switch ext {
	case ".js", ".ts", ".tsx", ".jsx", ".json", ".yaml", ".yml", ".rb", ".html", ".css", ".scss", ".vue", ".svelte", ".lua", ".nix", ".tf", ".dart":
		return "  "
	}
	return "    "
}

func detectIndent(lines [][]rune, path string) string {
	tabs, spaces := 0, 0
	counts := map[int]int{}
	prev := 0
	n := 0
	for _, l := range lines {
		if n > 2000 {
			break
		}
		if len(l) == 0 {
			continue
		}
		n++
		if l[0] == '\t' {
			tabs++
			continue
		}
		sp := 0
		for sp < len(l) && l[sp] == ' ' {
			sp++
		}
		if sp == len(l) {
			continue
		}
		if sp > 0 {
			spaces++
		}
		if d := sp - prev; d >= 2 && d <= 8 {
			counts[d]++
		}
		prev = sp
	}
	if tabs > spaces && tabs > 0 {
		return "\t"
	}
	if spaces > 0 {
		best, bc := 0, 0
		for d, c := range counts {
			if c > bc || (c == bc && d < best) {
				best, bc = d, c
			}
		}
		if best > 0 {
			return strings.Repeat(" ", best)
		}
	}
	return defaultIndent(path)
}

func leadingWS(l []rune) []rune {
	i := 0
	for i < len(l) && (l[i] == ' ' || l[i] == '\t') {
		i++
	}
	return l[:i]
}

func isWordRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

// Widths are measured on first display; after that edits only measure the
// lines they change, and a per-width line count finds the next widest line
// without rescanning the file.
func (b *Buffer) maxCols() int {
	if b.lineWidths == nil {
		b.lineWidths = make([]int, len(b.lines))
		b.widthCount = make(map[int]int)
		b.widest = 0
		for i, line := range b.lines {
			w := visCol(line, len(line))
			b.lineWidths[i] = w
			b.widthCount[w]++
			b.widest = max(b.widest, w)
		}
	}
	return b.widest
}

// updateWidths re-measures lines after oldCount lines starting at first were
// replaced by newCount lines.
func (b *Buffer) updateWidths(first, oldCount, newCount int) {
	if b.lineWidths == nil {
		return
	}
	for _, w := range b.lineWidths[first : first+oldCount] {
		if b.widthCount[w]--; b.widthCount[w] <= 0 {
			delete(b.widthCount, w)
		}
	}
	if newCount < oldCount {
		b.lineWidths = slices.Delete(b.lineWidths, first+newCount, first+oldCount)
	} else if newCount > oldCount {
		b.lineWidths = slices.Insert(b.lineWidths, first+oldCount, make([]int, newCount-oldCount)...)
	}
	for i := first; i < first+newCount; i++ {
		line := b.lines[i]
		w := visCol(line, len(line))
		b.lineWidths[i] = w
		b.widthCount[w]++
		b.widest = max(b.widest, w)
	}
	if b.widthCount[b.widest] > 0 {
		return
	}
	// The widest line got narrower or went away: step down to the next width
	// in use. Shortening a line by a few columns costs a few steps; after a
	// big drop (a minified line deleted) take the max over the distinct widths.
	for steps := 0; b.widest > 0 && b.widthCount[b.widest] == 0; steps++ {
		if steps == 256 {
			b.widest = 0
			for w := range b.widthCount {
				b.widest = max(b.widest, w)
			}
			break
		}
		b.widest--
	}
}
