package main

import (
	"bytes"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"

	"gioui.org/f32"
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
	checker struct {
		key [2]int
		th  *Theme
		op  paint.ImageOp
	}
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
		a.post(func() {
			iv.loading = false
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
	availW, availH := r.Dx()-2*pad, r.Dy()-2*pad
	// one image pixel per dp, like a browser, shrunk to fit the tab
	w, h := gtx.Dp(unit.Dp(iv.orig.X)), gtx.Dp(unit.Dp(iv.orig.Y))
	if w > availW {
		h, w = h*availW/max(1, w), availW
	}
	if h > availH {
		w, h = w*availH/max(1, h), availH
	}
	w, h = max(1, w), max(1, h)
	at := image.Pt(r.Min.X+(r.Dx()-w)/2, r.Min.Y+(r.Dy()-h)/2)
	dst := image.Rectangle{Min: at, Max: at.Add(image.Pt(w, h))}

	// a checkerboard shows through transparent pixels
	cell := gtx.Dp(8)
	cols, rows := (w+cell-1)/cell, (h+cell-1)/cell
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
