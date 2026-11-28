package main

import (
	"bytes"
	"image"
	"image/color"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/yuin/goldmark"
	gast "github.com/yuin/goldmark/ast"
	east "github.com/yuin/goldmark/extension"
	xast "github.com/yuin/goldmark/extension/ast"
	gtext "github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
	_ "golang.org/x/image/webp"
)

// The Markdown preview is drawn natively: the file is parsed with goldmark
// (CommonMark plus GitHub tables, task lists, strikethrough and autolinks)
// and laid out into a flat list of positioned text runs and shapes. The
// layout is cached until the text, the width, the zoom or the theme changes,
// so scrolling only replays the visible items.

// mdState is a buffer's preview: on/off, scroll and the cached layout.
type mdState struct {
	on     bool
	vy     float32
	doc    *mdDoc
	key    mdKey
	tag    int
	sync   int    // source line to scroll to after the next layout, or -1
	anchor string // heading to scroll to after the next layout

	hoverLink int
	images    map[string]*mdImage
	spans     map[codeKey][][]span // highlighted code blocks, kept across relayouts

	// cached parsed AST so window/splitter resize doesn't re-parse Markdown
	astVer    int
	astSrc    []byte
	astRoot   gast.Node
	astLines  []int
	clickPos  image.Point
	clickTime time.Time
}

var mdParser = goldmark.New(goldmark.WithExtensions(east.GFM)).Parser()

type codeKey struct {
	lang, src string
	th        *Theme
}

var (
	lexerMu    sync.Mutex
	lexerNames = map[string]chroma.Lexer{} // nil when the name is unknown
)

// lexerByName looks a fence language up once; chroma's lookup falls back to
// scanning every lexer's file patterns, which is slow for unknown names.
func lexerByName(lang string) chroma.Lexer {
	lang = strings.ToLower(lang)
	lexerMu.Lock()
	defer lexerMu.Unlock()
	if l, ok := lexerNames[lang]; ok {
		return l
	}
	l := lexers.Get(lang)
	lexerNames[lang] = l
	return l
}

// highlight returns syntax spans for a code block, cached by its text.
func (l *mdLayout) highlight(lang, src string) [][]span {
	if lang == "" {
		return nil
	}
	k := codeKey{lang, src, l.th}
	if sp, ok := l.md.spans[k]; ok {
		return sp
	}
	var sp [][]span
	if lex := lexerByName(lang); lex != nil {
		sp = computeSpans(lex, l.th, src)
	}
	if l.md.spans == nil || len(l.md.spans) > 2000 {
		l.md.spans = map[codeKey][][]span{}
	}
	l.md.spans[k] = sp
	return sp
}

type mdKey struct {
	ver, width, px int
	th             *Theme
	web            int // bumped when a link card or web image arrives
}

type mdDoc struct {
	items   []mdItem
	height  int
	blocks  []mdBlock // where each block starts, for source line mapping
	links   []mdLink
	anchors map[string]int
	web     map[string]bool // web pages and images the document uses
}

type mdBlock struct{ y, line int }

type mdLink struct {
	r    image.Rectangle
	dest string
	card bool
}

type mdItemKind uint8

const (
	miText mdItemKind = iota
	miFill
	miStroke
	miImage
)

// mdItem is one thing to paint, in content coordinates.
type mdItem struct {
	kind mdItemKind
	r    image.Rectangle // bounds; for text, Min is (x, baseline) and Max.Y the bottom
	s    string
	f    face
	px   int
	cell int // >0: draw on an integer character grid (fenced code blocks)
	col  color.NRGBA
	rad  int
	img  *mdImage
	src  image.Rectangle // part of img to show; empty means all of it
}

// textAt places text with its baseline at (x, baseline); bottom is the
// descender line. image.Rect would reorder the corners.
func textAt(x, baseline, bottom int) image.Rectangle {
	return image.Rectangle{Min: image.Pt(x, baseline), Max: image.Pt(x, bottom)}
}

func (it *mdItem) top() int {
	if it.kind == miText {
		return it.r.Min.Y - it.px
	}
	return it.r.Min.Y
}

type mdImage struct {
	op   paint.ImageOp
	size image.Point
	ok   bool
	mod  time.Time
}

func isMarkdown(b *Buffer) bool {
	if b == nil || b.virtualKey != "" {
		return false
	}
	switch strings.ToLower(filepath.Ext(b.path)) {
	case ".md", ".markdown", ".mdown", ".mkd", ".mkdn":
		return true
	}
	return b.path == "" && strings.EqualFold(b.hl.lang, "markdown")
}

func (b *Buffer) previewing() bool { return b.md != nil && b.md.on && isMarkdown(b) }

// toggleMarkdownPreview switches the active Markdown buffer between its
// source and the rendered preview, keeping roughly the same place in view.
func (a *App) toggleMarkdownPreview() {
	b := a.buf()
	if !isMarkdown(b) {
		a.setMsg("Preview is available for Markdown files")
		return
	}
	if b.md == nil {
		b.md = &mdState{hoverLink: -1, sync: -1}
	}
	md := b.md
	md.on = !md.on
	if md.on {
		md.sync = b.scrollY
		if b.hasSel() || b.cur.L >= b.scrollY && b.cur.L < b.scrollY+a.ui.editor.Dy()/max(1, a.ui.lineH) {
			md.sync = b.cur.L
		}
		if a.find != nil {
			a.closeFind()
		}
		return
	}
	// back to the source, scrolled to the block at the top of the preview
	if d := md.doc; d != nil {
		line := 0
		for _, bl := range d.blocks {
			if float32(bl.y) > md.vy+1 {
				break
			}
			line = bl.line
		}
		b.vy = float32(line * a.ui.lineH)
		a.follow = false
	}
	// free the layout, images and highlighting until the preview is back
	md.doc, md.images, md.spans = nil, nil, nil
	md.astRoot, md.astSrc, md.astLines = nil, nil, nil
}

func (a *App) mdBodyPx(gtx layout.Context) int { return gtx.Sp(unit.Sp(a.fontSp + 1)) }

func (a *App) drawMarkdown(gtx layout.Context, b *Buffer) {
	ops := gtx.Ops
	u := &a.ui
	th := a.th
	md := b.md
	r := u.editor
	fill(ops, r, th.bg)

	pad := gtx.Dp(32)
	cw := min(r.Dx()-2*pad, gtx.Dp(unit.Dp(860*a.fontSp/defaultFontSp)))
	cw = max(cw, gtx.Dp(120))
	ox := r.Min.X + (r.Dx()-cw)/2
	topPad := gtx.Dp(24)
	px := a.mdBodyPx(gtx)
	k := mdKey{b.version, cw, px, th, a.webVer}
	if md.doc == nil || md.key != k {
		md.doc = a.layoutMarkdown(gtx, b, cw, px)
		md.key = k
	}
	d := md.doc
	if md.anchor != "" {
		if y, ok := d.anchor(md.anchor); ok {
			md.vy = float32(y)
		}
		md.anchor = ""
	} else if md.sync >= 0 {
		y := 0
		for _, bl := range d.blocks {
			if bl.line > md.sync {
				break
			}
			y = bl.y
		}
		md.vy = float32(y - gtx.Dp(8))
		md.sync = -1
	}
	viewH := r.Dy()
	maxVy := float32(max(0, d.height+topPad+gtx.Dp(48)-viewH))
	oy := r.Min.Y + topPad

	// pointer: scrolling, links, double-click to jump to the source
	toContent := func(p f32.Point) image.Point {
		return image.Pt(int(p.X)-ox, int(p.Y)-oy+int(md.vy))
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &md.tag, Kinds: pointer.Press | pointer.Move | pointer.Leave | pointer.Scroll,
			ScrollX: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}, ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		switch e.Kind {
		case pointer.Scroll:
			md.vy = max(0, min(maxVy, md.vy+e.Scroll.Y))
			a.scrollAt = gtx.Now
		case pointer.Leave:
			md.hoverLink = -1
		case pointer.Move:
			md.hoverLink = d.linkAt(toContent(e.Position))
		case pointer.Press:
			if e.Buttons != pointer.ButtonPrimary {
				continue
			}
			a.focus = focusEditor
			p := toContent(e.Position)
			if i := d.linkAt(p); i >= 0 {
				a.followMdLink(b, d.links[i].dest)
				continue
			}
			dbl := gtx.Now.Sub(md.clickTime) < 450*time.Millisecond &&
				abs(p.X-md.clickPos.X) <= gtx.Dp(12) && abs(p.Y-md.clickPos.Y) <= gtx.Dp(12)
			md.clickPos, md.clickTime = p, gtx.Now
			if dbl {
				md.clickTime = time.Time{}
				line := 0
				for _, bl := range d.blocks {
					if bl.y > p.Y {
						break
					}
					line = bl.line
				}
				a.gotoPos(b, line+1, 1) // leaves the preview
				continue
			}
		}
	}
	md.vy = max(0, min(maxVy, md.vy))

	st := clip.Rect(r).Push(ops)
	event.Op(ops, &md.tag)
	pointer.CursorDefault.Add(ops)
	top, bot := int(md.vy)-gtx.Dp(4), int(md.vy)+viewH
	base := image.Pt(ox, oy-int(md.vy))
	for i := range d.items {
		it := &d.items[i]
		if it.r.Max.Y < top || it.top() > bot {
			continue
		}
		a.paintMdItem(ops, it, base)
	}
	for i, l := range d.links {
		if l.r.Max.Y < top || l.r.Min.Y > bot {
			continue
		}
		lr := l.r.Add(base)
		handCursor(ops, lr)
		if i == md.hoverLink && l.card {
			strokeRRect(ops, lr, gtx.Dp(8), float32(gtx.Dp(1.5)), th.accent)
		} else if i == md.hoverLink {
			fill(ops, image.Rect(lr.Min.X, lr.Max.Y-gtx.Dp(1.5), lr.Max.X, lr.Max.Y), th.accent)
		}
	}
	st.Pop()

	a.drawScrollbar(gtx, &md.vy, maxVy)
}

func (a *App) paintMdItem(ops *op.Ops, it *mdItem, base image.Point) {
	switch it.kind {
	case miText:
		if it.cell > 0 {
			a.tc.drawCell(ops, it.f, it.px, it.cell, base.X+it.r.Min.X, base.Y+it.r.Min.Y, it.s, it.col)
		} else {
			a.tc.draw(ops, it.f, it.px, base.X+it.r.Min.X, base.Y+it.r.Min.Y, it.s, it.col)
		}
	case miFill:
		rrect(ops, it.r.Add(base), it.rad, it.col)
	case miStroke:
		strokeRRect(ops, it.r.Add(base), it.rad, 1, it.col)
	case miImage:
		src := it.src
		if src.Empty() {
			src = image.Rectangle{Max: it.img.size}
		}
		drawImage(ops, it.img.op, src, it.r.Add(base), it.rad)
	}
}

// checkMdImages re-lays out the visible preview when one of its local images
// changed on disk, so images an agent rewrites show up. Called every second.
func (a *App) checkMdImages() {
	b := a.buf()
	if b == nil || !b.previewing() {
		return
	}
	for p, im := range b.md.images {
		if st, err := os.Stat(p); err == nil && !st.ModTime().Equal(im.mod) {
			a.webVer++
			return
		}
	}
}

func (d *mdDoc) linkAt(p image.Point) int {
	for i := len(d.links) - 1; i >= 0; i-- {
		if p.In(d.links[i].r) {
			return i
		}
	}
	return -1
}

// followMdLink opens a link from the preview: #anchors scroll, relative
// paths open in ted (Markdown files stay in preview), URLs go to the browser.
func (a *App) followMdLink(b *Buffer, dest string) {
	md := b.md
	switch {
	case dest == "":
		return
	case strings.HasPrefix(dest, "#"):
		if y, ok := md.doc.anchor(dest[1:]); ok {
			md.vy = float32(y)
		}
		return
	case isWebURL(dest) || strings.HasPrefix(dest, "mailto:"):
		openURL(dest)
		a.setMsg("Opened %s", dest)
		return
	case strings.Contains(dest, ":") && !filepath.IsAbs(dest) && !strings.HasPrefix(dest, "."):
		// file://, app schemes and the like could launch programs
		a.setErr("Ted only opens web, mail and file links: %s", dest)
		return
	}
	p, frag, _ := strings.Cut(dest, "#")
	if u, err := url.PathUnescape(p); err == nil {
		p = u
	}
	if !filepath.IsAbs(p) && b.path != "" {
		p = filepath.Join(filepath.Dir(b.path), p)
	}
	if st, err := os.Stat(p); err != nil || st.IsDir() {
		a.setErr("Can't open %s", dest)
		return
	}
	if a.openFile(p, 0, 0) {
		if nb := a.buf(); isMarkdown(nb) {
			if nb.md == nil {
				nb.md = &mdState{hoverLink: -1, sync: -1}
			}
			nb.md.on = true
			nb.md.anchor = frag
		}
	}
}

// anchor finds a heading by its #fragment, percent-encoded or not.
func (d *mdDoc) anchor(frag string) (int, bool) {
	if u, err := url.PathUnescape(frag); err == nil {
		frag = u
	}
	y, ok := d.anchors[strings.ToLower(frag)]
	return y, ok
}

func openURL(u string) {
	cmd := "xdg-open"
	switch runtime.GOOS {
	case "darwin":
		cmd = "open"
	case "windows":
		cmd = "explorer"
	}
	go exec.Command(cmd, u).Run()
}

// mdKeyPress handles keys while the preview has focus. It reports false for
// keys the editor should handle after switching back to the source.
func (a *App) mdKeyPress(b *Buffer, e key.Event) bool {
	md := b.md
	sc := e.Modifiers.Contain(key.ModShortcut)
	step := float32(a.ui.lineH * 3)
	page := float32(max(a.ui.editor.Dy()-a.ui.lineH*3, a.ui.lineH))
	end := float32(1 << 30)
	if md.doc != nil {
		end = float32(md.doc.height)
	}
	switch e.Name {
	case key.NameUpArrow:
		if sc {
			md.vy = 0
		} else {
			md.vy -= step
		}
	case key.NameDownArrow:
		if sc {
			md.vy = end
		} else {
			md.vy += step
		}
	case key.NamePageUp:
		md.vy -= page
	case key.NamePageDown:
		md.vy += page
	case key.NameSpace:
		if e.Modifiers.Contain(key.ModShift) {
			md.vy -= page
		} else {
			md.vy += page
		}
	case key.NameHome:
		md.vy = 0
	case key.NameEnd:
		md.vy = end
	case key.NameEscape:
		a.toggleMarkdownPreview()
	default:
		if sc && (e.Name == "F" || e.Name == "H") {
			a.toggleMarkdownPreview() // search the source
			return false
		}
		return true // swallow typing and editing keys
	}
	md.vy = max(0, md.vy)
	a.scrollAt = time.Now()
	return true
}

// ---- layout ----

type mdStyle struct {
	bold, italic, code, strike bool
	link                       string
	col                        color.NRGBA
}

// mdRun is a piece of inline content with one style.
type mdRun struct {
	s     string
	st    mdStyle
	br    bool // hard line break
	img   string
	alt   string
	check int // task list box: 1 open, 2 done
}

type mdLayout struct {
	a          *App
	gtx        layout.Context
	th         *Theme
	src        []byte
	dir        string
	md         *mdState
	doc        *mdDoc
	y          int
	px         int // body text size
	codePx     int
	lineStarts []int
	first      bool
	trail      int // space the last block left below itself
}

func (a *App) layoutMarkdown(gtx layout.Context, b *Buffer, width, px int) *mdDoc {
	md := b.md
	if md.astRoot == nil || md.astVer != b.version {
		src := []byte(b.text())
		md.astSrc = src
		md.astRoot = mdParser.Parse(gtext.NewReader(src))
		ls := []int{0}
		for i, c := range src {
			if c == '\n' {
				ls = append(ls, i+1)
			}
		}
		md.astLines = ls
		md.astVer = b.version
	}
	l := &mdLayout{a: a, gtx: gtx, th: a.th, src: md.astSrc, md: md, px: px, first: true,
		lineStarts: md.astLines,
		doc:        &mdDoc{anchors: map[string]int{}, web: map[string]bool{}}}
	l.codePx = max(8, px*86/100)
	if b.path != "" {
		l.dir = filepath.Dir(b.path)
	}
	for n := md.astRoot.FirstChild(); n != nil; n = n.NextSibling() {
		l.block(n, 0, width, l.th.fg)
	}
	l.doc.height = l.y
	sort.SliceStable(l.doc.blocks, func(i, j int) bool { return l.doc.blocks[i].y < l.doc.blocks[j].y })
	return l.doc
}

func (l *mdLayout) dp(v float32) int {
	return l.gtx.Dp(unit.Dp(v * float32(l.px) / float32(l.gtx.Sp(unit.Sp(defaultFontSp+1)))))
}

func (l *mdLayout) lineH(px int) int { return px * 165 / 100 }

// after adds the space a block leaves below itself, remembered so a
// container (quote, list item) can take its last child's space back.
func (l *mdLayout) after(v int) {
	l.y += v
	l.trail = v
}

func (l *mdLayout) add(it mdItem) { l.doc.items = append(l.doc.items, it) }

// srcLine returns the 0-based source line where n starts.
func (l *mdLayout) srcLine(n gast.Node) int {
	off := firstOffset(n)
	if off < 0 {
		return -1
	}
	return sort.SearchInts(l.lineStarts, off+1) - 1
}

func firstOffset(n gast.Node) int {
	if n.Type() == gast.TypeBlock && n.Lines().Len() > 0 {
		return n.Lines().At(0).Start
	}
	if t, ok := n.(*gast.Text); ok {
		return t.Segment.Start
	}
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		if o := firstOffset(c); o >= 0 {
			return o
		}
	}
	return -1
}

func (l *mdLayout) mark(n gast.Node) {
	if line := l.srcLine(n); line >= 0 {
		l.doc.blocks = append(l.doc.blocks, mdBlock{l.y, line})
	}
}

// gap adds vertical space between blocks, but not before the first one.
func (l *mdLayout) gap(v int) {
	if !l.first {
		l.y += v
	}
}

func (l *mdLayout) block(n gast.Node, x, w int, fg color.NRGBA) {
	th := l.th
	l.trail = 0
	switch n := n.(type) {
	case *gast.Heading:
		scale := []int{0, 200, 150, 125, 110, 100, 90}[min(n.Level, 6)]
		px := l.px * scale / 100
		l.gap(l.dp(map[bool]float32{true: 28, false: 20}[n.Level <= 2]))
		l.mark(n)
		runs := l.inlines(n, mdStyle{bold: true, col: fg})
		if n.Level == 6 {
			for i := range runs {
				runs[i].st.col = th.dim
			}
		}
		sl := slug(runsText(runs))
		for i, base := 1, sl; ; i++ {
			if _, dup := l.doc.anchors[sl]; !dup {
				break
			}
			sl = base + "-" + strconv.Itoa(i)
		}
		l.doc.anchors[sl] = l.y
		l.y += l.flow(runs, x, w, px, l.lineH(px)*85/100, 0)
		if n.Level <= 2 {
			l.y += l.dp(6)
			l.add(mdItem{kind: miFill, r: image.Rect(x, l.y, x+w, l.y+max(1, l.dp(1))), col: th.border})
			l.y += l.dp(4)
		}
		l.after(l.dp(10))
	case *gast.Paragraph:
		l.gap(0)
		l.mark(n)
		runs := l.inlines(n, mdStyle{col: fg})
		if u := soleLink(runs); u != "" {
			l.doc.web[u] = true
			if c := l.a.linkCard(u); c != nil {
				l.card(c, u, x, w)
				break
			}
		}
		l.y += l.flow(runs, x, w, l.px, l.lineH(l.px), 0)
		l.after(l.dp(14))
	case *gast.TextBlock:
		l.mark(n)
		l.y += l.flow(l.inlines(n, mdStyle{col: fg}), x, w, l.px, l.lineH(l.px), 0)
		if n.NextSibling() != nil {
			l.after(l.dp(6))
		}
	case *gast.ThematicBreak:
		l.y += l.dp(10)
		l.add(mdItem{kind: miFill, r: image.Rect(x, l.y, x+w, l.y+max(2, l.dp(2))), col: th.border, rad: 1})
		l.after(l.dp(24))
	case *gast.Blockquote:
		y0 := l.y
		inner := mixC(fg, th.bg, 0.25)
		bar := l.dp(4)
		in := max(0, min(l.dp(18), w-l.dp(120)))
		l.first = true
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			l.block(c, x+in, w-in, inner)
			l.first = false
		}
		l.y -= l.trail // the last block's spacing belongs outside
		l.y = max(l.y, y0+l.lineH(l.px))
		l.add(mdItem{kind: miFill, r: image.Rect(x, y0, x+bar, l.y), col: mixC(th.bg, th.fg, 0.2), rad: bar / 2})
		l.after(l.dp(16))
	case *gast.List:
		l.list(n, x, w, fg, 0)
		l.after(l.dp(10))
	case *gast.FencedCodeBlock:
		l.gap(0)
		l.mark(n)
		l.code(n, string(n.Language(l.src)), x, w)
	case *gast.CodeBlock:
		l.gap(0)
		l.mark(n)
		l.code(n, "", x, w)
	case *gast.HTMLBlock:
		l.mark(n)
		var sb strings.Builder
		for i := 0; i < n.Lines().Len(); i++ {
			s := n.Lines().At(i)
			sb.Write(s.Value(l.src))
		}
		if n.HasClosure() {
			sb.Write(n.ClosureLine.Value(l.src))
		}
		l.html(sb.String(), x, w, fg)
	case *xast.Table:
		l.gap(0)
		l.mark(n)
		l.table(n, x, w, fg)
		l.after(l.dp(16))
	default:
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			l.block(c, x, w, fg)
		}
		return
	}
	l.first = false
}

func (l *mdLayout) list(n *gast.List, x, w int, fg color.NRGBA, depth int) {
	th := l.th
	num := n.Start
	if num == 0 && n.IsOrdered() {
		num = 1
	}
	indent := l.dp(26)
	if n.IsOrdered() {
		// room for the widest number
		last := num + n.ChildCount() - 1
		indent = max(indent, l.a.tc.width(fUI, l.px, strconv.Itoa(last)+".")+l.dp(10))
	}
	indent = max(0, min(indent, w-l.dp(120))) // deep nesting stops indenting
	for it := n.FirstChild(); it != nil; it = it.NextSibling() {
		if !n.IsTight && it != n.FirstChild() {
			l.y += l.dp(6)
		}
		y0 := l.y
		lh := l.lineH(l.px)
		asc, desc := l.a.tc.metrics(fUI, l.px)
		baseline := y0 + (lh-(asc+desc))/2 + asc
		if !hasCheckBox(it) {
			if n.IsOrdered() {
				m := strconv.Itoa(num) + "."
				mw := l.a.tc.width(fUI, l.px, m)
				l.add(mdItem{kind: miText, r: textAt(x+indent-l.dp(8)-mw, baseline, baseline+desc), s: m, f: fUI, px: l.px, col: th.dim})
			} else {
				cx, cy := x+indent-l.dp(14), baseline-asc*36/100
				rad := max(2, l.dp(2.6))
				if depth%2 == 1 {
					l.add(mdItem{kind: miStroke, r: image.Rect(cx-rad, cy-rad, cx+rad, cy+rad), col: th.dim, rad: rad})
				} else {
					l.add(mdItem{kind: miFill, r: image.Rect(cx-rad, cy-rad, cx+rad, cy+rad), col: th.dim, rad: rad})
				}
			}
		}
		num++
		for c := it.FirstChild(); c != nil; c = c.NextSibling() {
			if sub, ok := c.(*gast.List); ok {
				l.y += l.dp(4)
				l.list(sub, x+indent, w-indent, fg, depth+1)
				continue
			}
			l.first = true
			l.block(c, x+indent, w-indent, fg)
			if c.NextSibling() == nil {
				l.y -= l.trail // the item's last block: no gap before the next item
			}
		}
		l.first = false
		l.y = max(l.y, y0+lh)
		if n.IsTight && it.NextSibling() != nil {
			l.y += l.dp(4)
		}
	}
}

// soleLink returns the web URL when a paragraph is nothing but one link.
func soleLink(runs []mdRun) string {
	u := ""
	for _, r := range runs {
		if r.br || r.check != 0 || r.img != "" {
			return ""
		}
		if r.st.link == "" {
			if strings.TrimSpace(r.s) != "" {
				return ""
			}
			continue
		}
		if u != "" && r.st.link != u {
			return ""
		}
		u = r.st.link
	}
	if !isWebURL(u) || strings.TrimSpace(runsText(runs)) != u {
		return "" // [label](url) keeps its label; bare URLs become cards
	}
	return u
}

// card draws a link preview: title, description and site, with the page's
// image on the right.
func (l *mdLayout) card(c *ogCard, u string, x, w int) {
	th := l.th
	tc := l.a.tc
	pad := l.dp(16)
	h := l.dp(108)
	if c.desc == "" {
		h = l.dp(80)
	}
	box := image.Rect(x, l.y, x+w, l.y+h)
	l.add(mdItem{kind: miFill, r: box, col: mixC(th.bg, th.fg, 0.035), rad: l.dp(8)})
	textR := box.Max.X - pad
	if c.img != nil && w > l.dp(360) {
		tw := min(h*191/100, w*36/100)
		ir := image.Rect(box.Max.X-tw, box.Min.Y, box.Max.X, box.Max.Y)
		l.add(mdItem{kind: miImage, r: ir.Inset(1), img: c.img, src: coverCrop(c.img.size, ir), rad: l.dp(7)})
		// square off the image's left corners against the text
		l.add(mdItem{kind: miFill, r: image.Rect(ir.Min.X, ir.Min.Y+1, ir.Min.X+l.dp(8), ir.Max.Y-1), col: mixC(th.bg, th.fg, 0.035)})
		textR = ir.Min.X - pad
	}
	l.add(mdItem{kind: miStroke, r: box, col: th.border, rad: l.dp(8)})
	tw := max(l.dp(40), textR-(x+pad))
	sitePx := max(8, l.px*80/100)
	y := box.Min.Y + pad
	_, sd := tc.metrics(fUI, sitePx)
	site := c.site
	if site == "" {
		if pu, err := url.Parse(u); err == nil {
			site = pu.Hostname()
		}
	}
	y += sitePx
	l.add(mdItem{kind: miText, r: textAt(x+pad, y, y+sd), s: tc.truncate(fUI, sitePx, tw, site), f: fUI, px: sitePx, col: th.faint})
	y += l.dp(10) + l.px
	_, td := tc.metrics(fUIBold, l.px)
	l.add(mdItem{kind: miText, r: textAt(x+pad, y, y+td), s: tc.truncate(fUIBold, l.px, tw, c.title), f: fUIBold, px: l.px, col: th.accent})
	dpx := max(8, l.px*88/100)
	_, dd := tc.metrics(fUI, dpx)
	for _, line := range wrapText(tc, fUI, dpx, tw, c.desc, 2) {
		y += dpx * 150 / 100
		l.add(mdItem{kind: miText, r: textAt(x+pad, y, y+dd), s: line, f: fUI, px: dpx, col: th.dim})
	}
	l.doc.links = append(l.doc.links, mdLink{box, u, true})
	l.y += h
	l.after(l.dp(16))
}

// wrapText breaks s into at most n lines of width w, ending with an ellipsis
// when it does not fit.
func wrapText(tc *textCache, f face, px, w int, s string, n int) []string {
	words := strings.Fields(s)
	var lines []string
	cur := ""
	for i, wd := range words {
		try := wd
		if cur != "" {
			try = cur + " " + wd
		}
		if tc.width(f, px, try) <= w || cur == "" {
			cur = try
			continue
		}
		if len(lines) == n-1 {
			return append(lines, tc.truncate(f, px, w, cur+" "+strings.Join(words[i:], " ")))
		}
		lines = append(lines, cur)
		cur = wd
	}
	if cur != "" {
		lines = append(lines, tc.truncate(f, px, w, cur))
	}
	return lines
}

func hasCheckBox(item gast.Node) bool {
	if c := item.FirstChild(); c != nil {
		if t := c.FirstChild(); t != nil {
			_, ok := t.(*xast.TaskCheckBox)
			return ok
		}
	}
	return false
}

// expandTabs expands tabs to 4-column tab stops per line.
func expandTabs(s string, w int) string {
	if !strings.ContainsRune(s, '\t') {
		return s
	}
	var sb strings.Builder
	sb.Grow(len(s) + 16)
	col := 0
	for _, r := range s {
		switch r {
		case '\n':
			sb.WriteByte('\n')
			col = 0
		case '\t':
			n := w - col%w
			for range n {
				sb.WriteByte(' ')
			}
			col += n
		default:
			sb.WriteRune(r)
			col += runeW(r)
		}
	}
	return sb.String()
}

// code draws a fenced or indented code block, highlighted with chroma.
func (l *mdLayout) code(n gast.Node, lang string, x, w int) {
	th := l.th
	var sb strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		s := n.Lines().At(i)
		sb.Write(s.Value(l.src))
	}
	src := strings.TrimRight(expandTabs(sb.String(), tabW), "\n")
	lines := strings.Split(src, "\n")
	px := l.codePx
	asc, desc := l.a.tc.metrics(fMono, px)
	lh := (asc + desc) * 150 / 100
	padX, padY := l.dp(16), l.dp(14)
	charW := max(1, l.a.tc.width(fMono, px, "0"))
	label := strings.ToLower(lang)
	labelH := 0
	if label != "" {
		labelH = l.dp(18)
	}
	h := 2*padY + len(lines)*lh + labelH
	box := image.Rect(x, l.y, x+w, l.y+h)
	l.add(mdItem{kind: miFill, r: box, col: mixC(th.bg, th.fg, 0.055), rad: l.dp(8)})
	if label != "" {
		tag := label
		if label == "mermaid" {
			tag = "mermaid · diagram source"
		}
		lpx := max(8, px*85/100)
		tw := l.a.tc.width(fUIBold, lpx, tag)
		l.add(mdItem{kind: miText, r: textAt(box.Max.X-padX-tw, l.y+padY+l.dp(6), l.y+padY+l.dp(10)), s: tag, f: fUIBold, px: lpx, col: th.faint})
	}
	spans := l.highlight(lang, src)
	maxCols := max(1, (w-2*padX)/charW)
	y := l.y + padY + labelH
	for li, line := range lines {
		rs := []rune(line)
		// display column of each rune; wide runes take two
		cols := make([]int, len(rs)+1)
		for i, r := range rs {
			cols[i+1] = cols[i] + max(1, runeW(r))
		}
		if cols[len(rs)] > maxCols {
			n := 0
			for n < len(rs) && cols[n+1] <= maxCols-1 {
				n++
			}
			rs = append(rs[:n:n], '…')
			cols = append(cols[:n+1:n+1], cols[n]+1)
		}
		bl := y + (lh-(asc+desc))/2 + asc
		var sp []span
		if li < len(spans) {
			sp = spans[li]
		}
		segs := []span{{0, tokStyle{fg: th.fg}}}
		if len(sp) > 0 {
			if sp[0].start > 0 {
				segs = append(segs, sp...)
			} else {
				segs = sp
			}
		}
		for si, s := range segs {
			if s.start >= len(rs) {
				break
			}
			e := len(rs)
			if si+1 < len(segs) {
				e = min(e, segs[si+1].start)
			}
			if e <= s.start {
				continue
			}
			seg := rs[s.start:e]
			wide := false
			for _, r := range seg {
				if runeW(r) > 1 {
					wide = true
					break
				}
			}
			cell := charW
			if wide {
				cell = 0
			}
			l.add(mdItem{kind: miText, r: textAt(x+padX+cols[s.start]*charW, bl, bl+desc), s: string(seg), f: monoFace(s.st), px: px, cell: cell, col: s.st.fg})
		}
		y += lh
	}
	l.y += h
	l.after(l.dp(16))
}

// html shows a raw HTML block as dim source; ted does not render HTML.
func (l *mdLayout) html(s string, x, w int, fg color.NRGBA) {
	s = strings.TrimRight(s, "\n")
	if strings.TrimSpace(s) == "" {
		return
	}
	px := l.codePx
	asc, desc := l.a.tc.metrics(fMono, px)
	lh := (asc + desc) * 145 / 100
	for _, line := range strings.Split(s, "\n") {
		bl := l.y + (lh-(asc+desc))/2 + asc
		l.add(mdItem{kind: miText, r: textAt(x, bl, bl+desc), s: l.a.tc.truncate(fMono, px, w, line), f: fMono, px: px, col: l.th.faint})
		l.y += lh
	}
	l.after(l.dp(12))
}

func (l *mdLayout) table(t *xast.Table, x, w int, fg color.NRGBA) {
	th := l.th
	type cell struct {
		runs  []mdRun
		align xast.Alignment
	}
	var rows [][]cell
	ncol := 0
	for r := t.FirstChild(); r != nil; r = r.NextSibling() {
		_, header := r.(*xast.TableHeader)
		var row []cell
		for c := r.FirstChild(); c != nil; c = c.NextSibling() {
			tc, _ := c.(*xast.TableCell)
			al := xast.AlignNone
			if tc != nil {
				al = tc.Alignment
			}
			row = append(row, cell{l.inlines(c, mdStyle{bold: header, col: fg}), al})
		}
		ncol = max(ncol, len(row))
		rows = append(rows, row)
	}
	if ncol == 0 {
		return
	}
	padX, padY := l.dp(12), l.dp(7)
	padX = max(2, min(padX, w/ncol/4)) // many columns: less padding
	natural := make([]int, ncol)
	minimum := make([]int, ncol)
	for _, row := range rows {
		for c, ce := range row {
			nw, mw := l.measure(ce.runs, l.px)
			natural[c] = max(natural[c], nw+2*padX)
			minimum[c] = max(minimum[c], min(mw, w/ncol)+2*padX)
		}
	}
	widths := append([]int(nil), natural...)
	if sum(natural) > w {
		// give every column its minimum, then share what is left by need
		copy(widths, minimum)
		left := w - sum(minimum)
		extra := 0
		for c := range natural {
			extra += natural[c] - minimum[c]
		}
		if left > 0 && extra > 0 {
			for c := range widths {
				widths[c] += left * (natural[c] - minimum[c]) / extra
			}
		}
		if left < 0 {
			// even the minimums don't fit: shrink every column; long words
			// in the cells are cut to fit
			total := sum(minimum)
			for c := range widths {
				widths[c] = max(2*padX+1, minimum[c]*w/total)
			}
		}
	}
	tw := sum(widths)
	y0 := l.y
	lh := l.lineH(l.px) * 90 / 100
	for ri, row := range rows {
		// lay each cell out on its own, then place the row at the tallest
		start := len(l.doc.items)
		rowH := lh
		cx := x
		saveY := l.y
		for c := 0; c < ncol; c++ {
			if c < len(row) {
				l.y = saveY + padY
				h := l.flow(row[c].runs, cx+padX, widths[c]-2*padX, l.px, lh, row[c].align)
				rowH = max(rowH, h)
			}
			cx += widths[c]
		}
		l.y = saveY
		rowH += 2 * padY
		bg := color.NRGBA{}
		switch {
		case ri == 0:
			bg = mixC(th.bg, th.fg, 0.06)
		case ri%2 == 0:
			bg = mixC(th.bg, th.fg, 0.025)
		}
		if bg.A != 0 {
			// backgrounds go under the row's text
			items := append([]mdItem(nil), l.doc.items[start:]...)
			l.doc.items = append(l.doc.items[:start], mdItem{kind: miFill, r: image.Rect(x, l.y, x+tw, l.y+rowH), col: bg})
			l.doc.items = append(l.doc.items, items...)
		}
		if ri > 0 {
			l.add(mdItem{kind: miFill, r: image.Rect(x, l.y, x+tw, l.y+1), col: th.border})
		}
		l.y += rowH
	}
	// column rules and the outer frame
	cx := x
	for c := 0; c < ncol-1; c++ {
		cx += widths[c]
		l.add(mdItem{kind: miFill, r: image.Rect(cx, y0, cx+1, l.y), col: withA(th.border, 0xa0)})
	}
	l.add(mdItem{kind: miStroke, r: image.Rect(x, y0, x+tw, l.y), col: th.border, rad: l.dp(6)})
}

func sum(xs []int) int {
	t := 0
	for _, x := range xs {
		t += x
	}
	return t
}

func runFace(st mdStyle) face {
	switch {
	case st.code && st.bold:
		return fMonoBold
	case st.code:
		return fMono
	case st.bold && st.italic:
		return fUIBoldItalic
	case st.bold:
		return fUIBold
	case st.italic:
		return fUIItalic
	}
	return fUI
}

func (l *mdLayout) runPx(st mdStyle, px int) int {
	if st.code {
		return max(8, px*88/100)
	}
	return px
}

// measure returns the single-line width of runs and the widest word.
func (l *mdLayout) measure(runs []mdRun, px int) (int, int) {
	total, widest, line := 0, 0, 0
	for _, r := range runs {
		if r.br {
			total = max(total, line)
			line = 0
			continue
		}
		if r.check != 0 {
			line += px + l.dp(8)
			continue
		}
		s := r.s
		if r.img != "" {
			s = "[" + r.alt + "]"
		}
		f, rpx := runFace(r.st), l.runPx(r.st, px)
		line += l.a.tc.width(f, rpx, s)
		for _, w := range strings.Fields(s) {
			widest = max(widest, l.a.tc.width(f, rpx, w))
		}
	}
	return max(total, line), widest
}

// flow lays out inline runs from l.y, wrapping at w, and returns the height.
func (l *mdLayout) flow(runs []mdRun, x0, w, px, lh int, align xast.Alignment) int {
	th := l.th
	tc := l.a.tc
	asc, desc := tc.metrics(fUI, px)
	y := l.y
	x := x0
	lineStart := len(l.doc.items)
	linkStart := len(l.doc.links)
	baseline := func() int { return y + (lh-(asc+desc))/2 + asc }
	endLine := func() {
		if align == xast.AlignCenter || align == xast.AlignRight {
			used := x - x0
			shift := w - used
			if align == xast.AlignCenter {
				shift /= 2
			}
			for i := lineStart; i < len(l.doc.items); i++ {
				l.doc.items[i].r = l.doc.items[i].r.Add(image.Pt(shift, 0))
			}
			for i := linkStart; i < len(l.doc.links); i++ {
				l.doc.links[i].r = l.doc.links[i].r.Add(image.Pt(shift, 0))
			}
		}
		y += lh
		x = x0
		lineStart = len(l.doc.items)
		linkStart = len(l.doc.links)
	}
	empty := true
	put := func(s string, st mdStyle) {
		f, rpx := runFace(st), l.runPx(st, px)
		sw := tc.width(f, rpx, s)
		if x > x0 && x+sw > x0+w {
			endLine()
			s = strings.TrimLeft(s, " ")
			if s == "" {
				return
			}
			sw = tc.width(f, rpx, s)
		}
		if x == x0 {
			s = strings.TrimLeft(s, " ")
			sw = tc.width(f, rpx, s)
		}
		if sw > w {
			// A word longer than the line (a URL, base64, minified JSON) is
			// cut into line-sized pieces, measured rune by rune so the cost
			// stays linear in its length.
			rs := []rune(s)
			start, acc := 0, 0
			for i, r := range rs {
				rw := tc.width(f, rpx, string(r))
				if acc+rw > x0+w-x && i > start {
					l.putText(string(rs[start:i]), st, f, rpx, x, baseline(), lh, y)
					endLine()
					start, acc = i, 0
				}
				acc += rw
			}
			s = string(rs[start:])
			sw = tc.width(f, rpx, s)
		}
		l.putText(s, st, f, rpx, x, baseline(), lh, y)
		x += sw
		empty = false
	}
	for _, r := range runs {
		switch {
		case r.br:
			endLine()
		case r.check != 0:
			s := px * 85 / 100
			bx, by := x, baseline()-asc*80/100
			box := image.Rect(bx, by, bx+s, by+s)
			if r.check == 2 {
				l.add(mdItem{kind: miFill, r: box, col: th.accent, rad: l.dp(3)})
				l.add(mdItem{kind: miText, r: textAt(bx+(s-tc.width(fUIBold, s*9/10, "✓"))/2, by+s*82/100, by+s), s: "✓", f: fUIBold, px: s * 9 / 10, col: th.onAccent})
			} else {
				l.add(mdItem{kind: miStroke, r: box, col: th.dim, rad: l.dp(3)})
			}
			x += s + l.dp(8)
			empty = false
		case r.img != "":
			if img := l.image(r.img); img != nil {
				if x > x0 {
					endLine()
				}
				sz := img.size
				// show at its natural size in dp, scaled down to fit
				dw := l.gtx.Dp(unit.Dp(float32(sz.X)))
				dw = max(1, min(dw, w))
				dh := dw * sz.Y / max(1, sz.X)
				if dh > l.dp(900) {
					dh = l.dp(900)
					dw = dh * sz.X / max(1, sz.Y)
				}
				ir := image.Rect(x0, y+l.dp(4), x0+dw, y+l.dp(4)+dh)
				l.add(mdItem{kind: miImage, r: ir, img: img})
				if r.st.link != "" {
					l.doc.links = append(l.doc.links, mdLink{r: ir, dest: r.st.link})
				}
				y += dh + l.dp(8)
				x = x0
				lineStart = len(l.doc.items)
				linkStart = len(l.doc.links)
				empty = true
				continue
			}
			st := r.st
			st.italic = true
			if st.link == "" {
				st.col = th.dim
			}
			alt := r.alt
			if alt == "" {
				alt = filepath.Base(r.img)
			}
			put("["+alt+"]", st)
		case r.st.code:
			// a code span stays one piece unless it is too long for the line
			put(r.s, r.st)
		default:
			// split into words, keeping each word's trailing space
			s := r.s
			for len(s) > 0 {
				i := strings.IndexByte(s, ' ')
				var word string
				if i < 0 {
					word, s = s, ""
				} else {
					j := i
					for j < len(s) && s[j] == ' ' {
						j++
					}
					word, s = s[:j], s[j:]
				}
				put(word, r.st)
			}
		}
	}
	if !empty || x > x0 {
		endLine()
	}
	return y - l.y
}

// putText adds one word or phrase in the given style, with code and link
// decorations.
func (l *mdLayout) putText(s string, st mdStyle, f face, px, x, baseline, lh, top int) {
	if s == "" {
		return
	}
	th := l.th
	tc := l.a.tc
	_, desc := tc.metrics(f, px)
	col := st.col
	if st.code {
		trim := strings.TrimRight(s, " ")
		cw := tc.width(f, px, trim)
		pad := l.dp(3)
		asc2, desc2 := tc.metrics(f, px)
		l.add(mdItem{kind: miFill, r: image.Rect(x-pad, baseline-asc2-pad/2, x+cw+pad, baseline+desc2+pad/2), col: mixC(th.bg, th.fg, 0.09), rad: l.dp(4)})
		if st.link == "" {
			col = mixC(th.fg, th.orange, 0.55)
		}
	}
	if st.link != "" {
		col = th.accent
	}
	l.add(mdItem{kind: miText, r: textAt(x, baseline, baseline+desc), s: s, f: f, px: px, col: col})
	if st.strike {
		sy := baseline - px*30/100
		l.add(mdItem{kind: miFill, r: image.Rect(x, sy, x+tc.width(f, px, strings.TrimRight(s, " ")), sy+max(1, l.dp(1))), col: col})
	}
	if st.link != "" {
		l.doc.links = append(l.doc.links, mdLink{r: image.Rect(x, top, x+tc.width(f, px, strings.TrimRight(s, " ")), top+lh), dest: st.link})
	}
}

// image loads an image referenced from the document: local files once
// (again when they change), web images through the shared downloader.
func (l *mdLayout) image(dest string) *mdImage {
	if isWebURL(dest) {
		l.doc.web[dest] = true
		return l.a.webImageFor(dest)
	}
	if strings.Contains(dest, "://") || strings.HasPrefix(dest, "data:") || dest == "" {
		return nil
	}
	p := dest
	if i := strings.IndexAny(p, "?#"); i >= 0 {
		p = p[:i]
	}
	if !filepath.IsAbs(p) {
		if l.dir == "" {
			return nil
		}
		p = filepath.Join(l.dir, p)
	}
	st, err := os.Stat(p)
	if err != nil || st.Size() > 40<<20 {
		return nil
	}
	if l.md.images == nil {
		l.md.images = map[string]*mdImage{}
	}
	if im, ok := l.md.images[p]; ok && im.mod.Equal(st.ModTime()) {
		if !im.ok {
			return nil
		}
		return im
	}
	// decode in the background; the layout is redone when it lands
	pending := &mdImage{mod: st.ModTime()}
	l.md.images[p] = pending
	a, mod := l.a, st.ModTime()
	go func() {
		im, _, err := decodeImageFile(p, 2400)
		a.post(func() {
			if err == nil {
				im.mod = mod
				*pending = *im
			}
			a.webVer++
		})
	}()
	return nil
}

// inlines collects n's inline content as styled runs.
func (l *mdLayout) inlines(n gast.Node, st mdStyle) []mdRun {
	var out []mdRun
	var walk func(n gast.Node, st mdStyle)
	walk = func(n gast.Node, st mdStyle) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *gast.Text:
				v := c.Segment.Value(l.src)
				if !c.IsRaw() {
					v = util.ResolveEntityNames(util.ResolveNumericReferences(util.UnescapePunctuations(v)))
				}
				out = append(out, mdRun{s: string(v), st: st})
				if c.HardLineBreak() {
					out = append(out, mdRun{br: true})
				} else if c.SoftLineBreak() {
					out = append(out, mdRun{s: " ", st: st})
				}
			case *gast.String:
				out = append(out, mdRun{s: string(c.Value), st: st})
			case *gast.CodeSpan:
				var sb bytes.Buffer
				for t := c.FirstChild(); t != nil; t = t.NextSibling() {
					switch t := t.(type) {
					case *gast.Text:
						sb.Write(t.Segment.Value(l.src))
					case *gast.String:
						sb.Write(t.Value)
					}
				}
				s := st
				s.code = true
				out = append(out, mdRun{s: sb.String(), st: s})
			case *gast.Emphasis:
				s := st
				if c.Level >= 2 {
					s.bold = true
				} else {
					s.italic = true
				}
				walk(c, s)
			case *xast.Strikethrough:
				s := st
				s.strike = true
				walk(c, s)
			case *gast.Link:
				s := st
				s.link = string(c.Destination)
				walk(c, s)
			case *gast.AutoLink:
				s := st
				s.link = string(c.URL(l.src))
				if c.AutoLinkType == gast.AutoLinkEmail && !strings.HasPrefix(s.link, "mailto:") {
					s.link = "mailto:" + s.link
				}
				out = append(out, mdRun{s: string(c.Label(l.src)), st: s})
			case *gast.Image:
				var alt strings.Builder
				for t := c.FirstChild(); t != nil; t = t.NextSibling() {
					if tt, ok := t.(*gast.Text); ok {
						alt.Write(tt.Segment.Value(l.src))
					}
				}
				out = append(out, mdRun{img: string(c.Destination), alt: alt.String(), st: st})
			case *xast.TaskCheckBox:
				ch := 1
				if c.IsChecked {
					ch = 2
				}
				out = append(out, mdRun{check: ch})
			case *gast.RawHTML:
				var sb strings.Builder
				for i := 0; i < c.Segments.Len(); i++ {
					s := c.Segments.At(i)
					sb.Write(s.Value(l.src))
				}
				tag := strings.ToLower(strings.ReplaceAll(sb.String(), " ", ""))
				if strings.HasPrefix(tag, "<br") {
					out = append(out, mdRun{br: true})
				}
				// other inline tags are dropped; their text still shows
			default:
				walk(c, st)
			}
		}
	}
	walk(n, st)
	return out
}

func runsText(runs []mdRun) string {
	var sb strings.Builder
	for _, r := range runs {
		sb.WriteString(r.s)
	}
	return sb.String()
}

// slug makes a GitHub-style heading anchor: "Hello, World!" -> "hello-world".
func slug(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(s)) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_':
			sb.WriteRune(r)
		case r == ' ':
			sb.WriteByte('-')
		}
	}
	return sb.String()
}
