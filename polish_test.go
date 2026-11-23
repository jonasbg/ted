package main

import (
	"bytes"
	"encoding/binary"
	"image"
	"image/color"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// jpegWithOrientation encodes img as a JPEG carrying an EXIF orientation tag.
func jpegWithOrientation(t *testing.T, img image.Image, o uint16) []byte {
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 95}); err != nil {
		t.Fatal(err)
	}
	// TIFF, little endian, one IFD entry: orientation (0x0112), SHORT, 1, o
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0}
	e := make([]byte, 12)
	binary.LittleEndian.PutUint16(e[0:], 0x0112)
	binary.LittleEndian.PutUint16(e[2:], 3)
	binary.LittleEndian.PutUint32(e[4:], 1)
	binary.LittleEndian.PutUint16(e[8:], o)
	tiff = append(tiff, e...)
	tiff = append(tiff, 0, 0, 0, 0)
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, 0, 0}
	binary.BigEndian.PutUint16(app1[2:], uint16(len(seg)+2))
	app1 = append(app1, seg...)
	j := buf.Bytes()
	return append(append(append([]byte{}, j[:2]...), app1...), j[2:]...)
}

func TestExifOrientation(t *testing.T) {
	// 64 × 32: left half red, right half blue
	img := image.NewRGBA(image.Rect(0, 0, 64, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 64; x++ {
			c := color.RGBA{0xff, 0, 0, 0xff}
			if x >= 32 {
				c = color.RGBA{0, 0, 0xff, 0xff}
			}
			img.Set(x, y, c)
		}
	}
	data := jpegWithOrientation(t, img, 6) // stored turned left; show rotated clockwise
	if o := exifOrientation(data); o != 6 {
		t.Fatalf("orientation = %d, want 6", o)
	}
	im, orig, err := decodeImage(data, 4096)
	if err != nil {
		t.Fatal(err)
	}
	if orig != image.Pt(32, 64) || im.size != image.Pt(32, 64) {
		t.Fatalf("rotated size: orig %v, decoded %v; want 32 × 64", orig, im.size)
	}
	// rotated clockwise, the left (red) half ends up on top
	out := orient(img, 6).(*image.RGBA)
	if r, _, b, _ := out.At(16, 8).RGBA(); r < 0x8000 || b > 0x8000 {
		t.Errorf("top of the rotated image is not red")
	}
	if r, _, b, _ := out.At(16, 56).RGBA(); b < 0x8000 || r > 0x8000 {
		t.Errorf("bottom of the rotated image is not blue")
	}
	var plain bytes.Buffer
	jpeg.Encode(&plain, img, nil)
	if o := exifOrientation(plain.Bytes()); o != 1 {
		t.Errorf("JPEG without EXIF: orientation %d", o)
	}
}

func TestLegacyCharsetTitles(t *testing.T) {
	page := []byte("<html><head><meta charset=\"windows-1252\"><meta property=\"og:title\" content=\"Caf\xe9 \x93menu\x94 \x96 2026\"></head></html>")
	m := parseMeta(toUTF8(page, "text/html"))
	if want := "Café “menu” – 2026"; m["og:title"] != want {
		t.Errorf("title %q, want %q", m["og:title"], want)
	}
	if got := toUTF8([]byte("já"), "text/html; charset=utf-8"); string(got) != "já" {
		t.Errorf("valid UTF-8 changed: %q", got)
	}
}

func TestWebCacheRetryAndTrim(t *testing.T) {
	a := newApp(nil, "", newTheme("github"), defaultFontSp)
	u := "https://example.invalid/page"
	failed := &ogCard{done: true, at: time.Now().Add(-3 * time.Minute)}
	a.og = map[string]*ogCard{u: failed}
	a.linkCard(u)
	if a.og[u] == failed {
		t.Error("a failed card was not fetched again after the retry delay")
	}
	recent := &ogCard{done: true, at: time.Now()}
	a.og[u] = recent
	a.linkCard(u)
	if a.og[u] != recent {
		t.Error("a recent failure was fetched again right away")
	}

	big := &mdImage{size: image.Pt(2400, 2400), ok: true} // 23 MB each
	a.webImgs = map[string]*webImage{}
	for i := 0; i < 12; i++ {
		a.webImgs[strings.Repeat("x", i+1)] = &webImage{done: true, img: big}
	}
	keep := "https://example.invalid/shown.png"
	a.webImgs[keep] = &webImage{done: true, img: big}
	b := newBuffer("/tmp/x.md")
	b.md = &mdState{on: true, doc: &mdDoc{web: map[string]bool{keep: true}}}
	a.bufs = []*Buffer{b}
	a.active = 0
	a.trimWebCache()
	if len(a.webImgs) != 1 || a.webImgs[keep] == nil {
		t.Errorf("after trimming %d images remain (want only the one shown)", len(a.webImgs))
	}
}

func TestUntitledPreviewRestored(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	a := newApp(nil, "", newTheme("github"), defaultFontSp)
	a.newUntitled()
	b := a.buf()
	b.replaceRange(Pos{}, Pos{}, []rune("# Draft\n\nSome **notes** with a list:\n\n- one\n- two\n"))
	b.setLanguage("markdown") // picked in the status bar
	a.toggleMarkdownPreview()
	b.md.vy = 42
	a.saveSession()
	re := newApp(nil, "", a.th, defaultFontSp)
	re.restoreSession("", true)
	if len(re.bufs) != 1 {
		t.Fatalf("restored %d tabs", len(re.bufs))
	}
	if nb := re.bufs[0]; !nb.previewing() || nb.md.vy != 42 {
		t.Errorf("untitled preview not restored: previewing=%v", nb.previewing())
	}
}

func TestNonImageWithImageNameOpensAsText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.png")
	os.WriteFile(p, []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 123\n"), 0o644)
	h := newHarness(t, dir, p)
	h.settle()
	b := h.a.buf()
	if b.image != nil || !strings.Contains(b.text(), "git-lfs") {
		t.Fatalf("LFS pointer was not reopened as text (msg %q)", h.a.msg)
	}
}

func TestQuoteEndingInCodeBlockSpacing(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "q.md")
	os.WriteFile(p, []byte("> text\n>\n> ```\n> code\n> ```\n\nafter\n"), 0o644)
	h := newHarness(t, dir, p)
	h.a.toggleMarkdownPreview()
	h.frame()
	d := h.a.buf().md.doc
	var barBottom, afterTop int
	for _, it := range d.items {
		if it.kind == miFill && it.r.Dx() <= 10 && it.r.Dy() > 50 {
			barBottom = it.r.Max.Y // the quote's bar
		}
		if it.kind == miText && it.s == "after" {
			afterTop = it.top()
		}
	}
	// the gap below the quote should be its own spacing, not doubled
	if gap := afterTop - barBottom; gap > 120 {
		t.Errorf("gap below a quote ending in code is %d px", gap)
	}
}
