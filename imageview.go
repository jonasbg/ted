package main

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	xdraw "golang.org/x/image/draw"
)

// Images open in a read-only tab that shows the picture fitted to the
// window. It reloads when the file changes on disk, which makes it handy for
// watching images an agent or a script writes.

var imageExts = map[string]string{".png": "PNG", ".jpg": "JPEG", ".jpeg": "JPEG", ".gif": "GIF", ".webp": "WebP"}

func isImagePath(p string) bool {
	_, ok := imageExts[strings.ToLower(filepath.Ext(p))]
	return ok
}

type imageView struct {
	img     *mdImage
	orig    image.Point // size before any downscaling
	format  string
	err     string
	loading bool
	stale   bool // the file changed; load again but keep showing the old one

	zoom     float32 // 0 = fit to window; >0 = scale factor relative to dp size
	panX     float32
	panY     float32
	dragging bool
	dragLast f32.Point
	tag      int

	checker struct {
		key [2]int
		th  *Theme
		op  paint.ImageOp
	}
}

func (iv *imageView) zoomLabel() string {
	if iv.zoom <= 0 {
		return "Fit"
	}
	return fmt.Sprintf("%d%%", int(iv.zoom*100+0.5))
}

func (iv *imageView) toggleZoom() {
	if iv.zoom <= 0 {
		iv.zoom = 1
	} else {
		iv.zoom = 0
	}
	iv.panX, iv.panY = 0, 0
}

func loadImageBuffer(path string) (*Buffer, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, fmt.Errorf("is a directory")
	}
	b := newBuffer(path)
	b.readOnly = true
	b.hl.resolved = true
	b.modTime, b.size = st.ModTime(), st.Size()
	b.image = &imageView{format: imageExts[strings.ToLower(filepath.Ext(path))]}
	return b, nil
}

// Images larger than this are not decoded (an RGBA copy of 50 megapixels
// is 200 MB).
const (
	maxImageSide   = 20000
	maxImagePixels = 50_000_000
)

// decodeImage decodes data, scaling it down to at most maxSide pixels on its
// longest side. It returns the image and its original size.
func decodeImage(data []byte, maxSide int) (*mdImage, image.Point, error) {
	// check the declared size first: a small file can claim a huge image
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, image.Point{}, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width > maxImageSide || cfg.Height > maxImageSide || cfg.Width*cfg.Height > maxImagePixels {
		return nil, image.Pt(cfg.Width, cfg.Height), fmt.Errorf("image is too large (%d × %d)", cfg.Width, cfg.Height)
	}
	src, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, image.Point{}, err
	}
	orig := src.Bounds().Size()
	if orig.X <= 0 || orig.Y <= 0 {
		return nil, orig, fmt.Errorf("empty image")
	}
	if m := max(orig.X, orig.Y); m > maxSide {
		w, h := orig.X*maxSide/m, orig.Y*maxSide/m
		dst := image.NewRGBA(image.Rect(0, 0, max(1, w), max(1, h)))
		xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
		src = dst
	}
	if o := exifOrientation(data); o > 1 {
		src = orient(src, o)
		if o >= 5 {
			orig = image.Pt(orig.Y, orig.X)
		}
	}
	return &mdImage{op: paint.NewImageOp(src), size: src.Bounds().Size(), ok: true}, orig, nil
}

func decodeImageFile(path string, maxSide int) (*mdImage, image.Point, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, image.Point{}, err
	}
	return decodeImage(data, maxSide)
}

func (a *App) loadImageView(b *Buffer) {
	iv := b.image
	if iv.loading {
		return
	}
	iv.loading, iv.stale = true, false
	path := b.path
	go func() {
		img, orig, err := decodeImageFile(path, 4096)
		text := err != nil && looksLikeText(path)
		a.post(func() {
			iv.loading = false
			if text && iv.img == nil {
				a.reopenAsText(b)
				return
			}
			if err != nil {
				iv.err = err.Error()
				if iv.img != nil && b == a.buf() {
					a.setErr("Can't reload %s: %v", b.name(), err)
				}
				return
			}
			iv.img, iv.orig, iv.err = img, orig, ""
		})
	}()
}

// imageChanged notes a new version of an image file on disk.
func (a *App) imageChanged(b *Buffer, st os.FileInfo) {
	b.modTime, b.size = st.ModTime(), st.Size()
	b.image.stale = true
	if b == a.buf() {
		a.setMsg("%s changed on disk and was reloaded", b.name())
	}
}

func (a *App) drawImageView(gtx layout.Context, b *Buffer) {
	ops := gtx.Ops
	th := a.th
	iv := b.image
	r := a.ui.editor
	fill(ops, r, th.bg)
	if (iv.img == nil && iv.err == "") || iv.stale {
		a.loadImageView(b)
	}
	if iv.img == nil {
		msg := "Loading…"
		col := th.dim
		if iv.err != "" {
			msg, col = "Can't show this image: "+iv.err, th.errorFg
		}
		w := a.tc.width(fUI, a.ui.uiPx, msg)
		a.tc.draw(ops, fUI, a.ui.uiPx, r.Min.X+(r.Dx()-w)/2, r.Min.Y+r.Dy()/2, msg, col)
		return
	}
	pad := gtx.Dp(24)
	availW, availH := max(1, r.Dx()-2*pad), max(1, r.Dy()-2*pad)

	// Handle zoom (scroll / double-click) and pan (drag when zoomed).
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &iv.tag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Scroll,
			ScrollX: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}, ScrollY: pointer.ScrollRange{Min: -1 << 20, Max: 1 << 20}})
		if !ok {
			break
		}
		e := ev.(pointer.Event)
		switch e.Kind {
		case pointer.Scroll:
			if e.Modifiers.Contain(key.ModShortcut) || e.Modifiers.Contain(key.ModCtrl) || iv.zoom <= 0 {
				if e.Scroll.Y != 0 {
					base := iv.zoom
					if base <= 0 {
						// Start zooming from the current fit ratio.
						natW := float32(max(1, gtx.Dp(unit.Dp(iv.orig.X))))
						natH := float32(max(1, gtx.Dp(unit.Dp(iv.orig.Y))))
						base = min(1, min(float32(availW)/natW, float32(availH)/natH))
					}
					if e.Scroll.Y < 0 {
						base *= 1.15
					} else {
						base /= 1.15
					}
					iv.zoom = max(0.1, min(16, base))
				}
			} else {
				iv.panX -= e.Scroll.X
				iv.panY -= e.Scroll.Y
			}
		case pointer.Press:
			if e.Buttons != pointer.ButtonPrimary {
				continue
			}
			a.focus = focusEditor
			if a.ui.isDoubleClick(gtx.Now, "img", 0) {
				iv.toggleZoom()
				continue
			}
			if iv.zoom > 0 {
				iv.dragging = true
				iv.dragLast = e.Position
			}
		case pointer.Drag:
			if iv.dragging {
				iv.panX += e.Position.X - iv.dragLast.X
				iv.panY += e.Position.Y - iv.dragLast.Y
				iv.dragLast = e.Position
			}
		case pointer.Release, pointer.Cancel:
			iv.dragging = false
		}
	}

	// one image pixel per dp, like a browser, shrunk to fit the tab unless zoomed
	natW, natH := gtx.Dp(unit.Dp(iv.orig.X)), gtx.Dp(unit.Dp(iv.orig.Y))
	var w, h int
	if iv.zoom > 0 {
		w = max(1, int(float32(natW)*iv.zoom+0.5))
		h = max(1, int(float32(natH)*iv.zoom+0.5))
	} else {
		w, h = natW, natH
		if w > availW {
			h, w = h*availW/max(1, w), availW
		}
		if h > availH {
			w, h = w*availH/max(1, h), availH
		}
		w, h = max(1, w), max(1, h)
	}
	maxPanX := float32(max(0, (w-availW)/2+pad))
	maxPanY := float32(max(0, (h-availH)/2+pad))
	iv.panX = max(-maxPanX, min(maxPanX, iv.panX))
	iv.panY = max(-maxPanY, min(maxPanY, iv.panY))
	at := image.Pt(r.Min.X+(r.Dx()-w)/2+int(iv.panX), r.Min.Y+(r.Dy()-h)/2+int(iv.panY))
	dst := image.Rectangle{Min: at, Max: at.Add(image.Pt(w, h))}

	viewClip := clip.Rect(r).Push(ops)
	event.Op(ops, &iv.tag)
	// a checkerboard shows through transparent pixels
	cell := max(1, gtx.Dp(8))
	cols, rows := max(1, (w+cell-1)/cell), max(1, (h+cell-1)/cell)
	// Cap checkerboard texture size when zoomed way in.
	if cols > 512 || rows > 512 {
		cell = max(cell, max((w+511)/512, (h+511)/512))
		cols, rows = max(1, (w+cell-1)/cell), max(1, (h+cell-1)/cell)
	}
	if k := [2]int{cols, rows}; iv.checker.key != k || iv.checker.th != th {
		c := image.NewNRGBA(image.Rect(0, 0, cols, rows))
		c1, c2 := mixC(th.bg, th.fg, 0.07), mixC(th.bg, th.fg, 0.13)
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				col := c1
				if (x+y)%2 == 1 {
					col = c2
				}
				c.SetNRGBA(x, y, col)
			}
		}
		iv.checker.key, iv.checker.th = k, th
		iv.checker.op = paint.NewImageOp(c)
		iv.checker.op.Filter = paint.FilterNearest
	}
	cb := clip.Rect(dst).Push(ops)
	drawImage(ops, iv.checker.op, image.Rect(0, 0, cols, rows), image.Rect(dst.Min.X, dst.Min.Y, dst.Min.X+cols*cell, dst.Min.Y+rows*cell), 0)
	cb.Pop()
	op := iv.img.op
	if w > iv.img.size.X*2 {
		op.Filter = paint.FilterNearest // keep enlarged pixel art crisp
	}
	drawImage(ops, op, image.Rectangle{Max: iv.img.size}, dst, 0)
	strokeRRect(ops, dst.Inset(-1), 0, 1, withA(th.border, 0xc0))
	viewClip.Pop()
}

// drawImage paints the src part of an image into dst, clipped to dst with
// rounded corners of radius rad.
func drawImage(ops *op.Ops, im paint.ImageOp, src, dst image.Rectangle, rad int) {
	if src.Empty() || dst.Empty() {
		return
	}
	cl := clip.UniformRRect(dst, rad).Push(ops)
	sx := float32(dst.Dx()) / float32(src.Dx())
	sy := float32(dst.Dy()) / float32(src.Dy())
	tr := f32.AffineId().
		Offset(f32.Pt(-float32(src.Min.X), -float32(src.Min.Y))).
		Scale(f32.Point{}, f32.Pt(sx, sy)).
		Offset(f32.Pt(float32(dst.Min.X), float32(dst.Min.Y)))
	t := op.Affine(tr).Push(ops)
	im.Add(ops)
	paint.PaintOp{}.Add(ops)
	t.Pop()
	cl.Pop()
}

// coverCrop returns the centred part of an image of size sz that has the
// aspect ratio of dst, like CSS object-fit: cover.
func coverCrop(sz image.Point, dst image.Rectangle) image.Rectangle {
	if dst.Dx() <= 0 || dst.Dy() <= 0 {
		return image.Rectangle{Max: sz}
	}
	want := float64(dst.Dx()) / float64(dst.Dy())
	have := float64(sz.X) / float64(sz.Y)
	if have > want {
		w := int(float64(sz.Y) * want)
		x := (sz.X - w) / 2
		return image.Rect(x, 0, x+w, sz.Y)
	}
	h := int(float64(sz.X) / want)
	y := (sz.Y - h) / 2
	return image.Rect(0, y, sz.X, y+h)
}

func humanSize(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%d KB", (n+512)>>10)
	}
	return fmt.Sprintf("%d bytes", n)
}

// looksLikeText reports whether a file that failed to decode as an image is
// really text, such as a Git LFS pointer or a mislabelled file.
func looksLikeText(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	head := make([]byte, 8000)
	n, _ := f.Read(head)
	head = head[:n]
	return n > 0 && bytes.IndexByte(head, 0) < 0 && utf8.Valid(head[:max(0, n-4)])
}

// reopenAsText replaces an image tab with a text tab for the same file.
func (a *App) reopenAsText(b *Buffer) {
	i := a.indexOf(b)
	if i < 0 {
		return
	}
	nb, err := loadBuffer(b.path)
	if err != nil {
		return
	}
	nb.preview = b.preview
	nb.resolveLexer()
	a.bufs[i] = nb
	if i == a.active {
		a.setMsg("%s is not an image; showing it as text", nb.name())
	}
}
