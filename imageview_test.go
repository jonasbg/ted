package main

import (
	"fmt"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"
)

func writePNG(t *testing.T, p string, w, h int, c color.NRGBA) {
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			cc := c
			if x < w/2 {
				cc.A = 0x60 // half transparent, to show the checkerboard
			}
			img.SetNRGBA(x, y, cc)
		}
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	png.Encode(f, img)
	f.Close()
}

func TestImageView(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "chart.png")
	writePNG(t, p, 640, 360, color.NRGBA{0x30, 0x90, 0xe0, 0xff})
	h := newHarness(t, dir, p)
	h.settle()
	b := h.a.buf()
	if b == nil || b.image == nil {
		t.Fatalf("chart.png did not open as an image (msg %q)", h.a.msg)
	}
	if b.image.img == nil || b.image.orig != image.Pt(640, 360) {
		t.Fatalf("image not decoded: %+v", b.image)
	}
	h.shot("image-view")
	ids := map[string]bool{}
	for _, it := range h.a.ui.statusItems {
		ids[it.id] = true
	}
	if !ids["imgdim"] || !ids["imgfmt"] || !ids["imgsize"] {
		t.Errorf("status bar is missing image details: %v", ids)
	}

	// typing does nothing and saving is refused
	h.typ("x")
	h.keys(press("S", key.ModShortcut))
	if b.dirty() {
		t.Error("an image tab became dirty")
	}

	// an agent rewrites the file: the tab picks up the new image
	time.Sleep(20 * time.Millisecond)
	writePNG(t, p, 200, 400, color.NRGBA{0xe0, 0x60, 0x30, 0xff})
	os.Chtimes(p, time.Now().Add(time.Second), time.Now().Add(time.Second))
	h.a.checkDisk()
	h.settle()
	if b.image.orig != image.Pt(200, 400) {
		t.Errorf("image not reloaded after it changed on disk: %v", b.image.orig)
	}
}

func TestParseMeta(t *testing.T) {
	m := parseMeta([]byte(`<!doctype html><html><head>
<title> Plain   title </title>
<meta property="og:title" content="Card title">
<meta name="description" content="Fallback description">
<meta property="og:image" content="/img.png">
</head><body><meta property="og:title" content="ignored"></body></html>`))
	if m["og:title"] != "Card title" || m["title"] != "Plain title" || m["description"] != "Fallback description" || m["og:image"] != "/img.png" {
		t.Errorf("parseMeta = %v", m)
	}
}

func TestLinkCards(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/article", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `<html><head><meta property="og:title" content="How link cards work">
<meta property="og:description" content="A page with Open Graph tags, a description long enough to wrap onto a second line in the card so the ellipsis shows up at the end of it.">
<meta property="og:site_name" content="Example Blog"><meta property="og:image" content="/cover.png"></head></html>`)
	})
	mux.HandleFunc("/cover.png", func(w http.ResponseWriter, r *http.Request) {
		img := image.NewNRGBA(image.Rect(0, 0, 1200, 630))
		for y := 0; y < 630; y++ {
			for x := 0; x < 1200; x++ {
				img.SetNRGBA(x, y, color.NRGBA{uint8(x / 5), 0x70, uint8(y / 3), 0xff})
			}
		}
		png.Encode(w, img)
	})
	mux.HandleFunc("/plain", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, "not html")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	dir := t.TempDir()
	p := filepath.Join(dir, "links.md")
	doc := "# Links\n\n<" + srv.URL + "/article>\n\nInline [link](" + srv.URL + "/article) stays text.\n\n<" + srv.URL + "/plain>\n"
	os.WriteFile(p, []byte(doc), 0o644)
	h := newHarness(t, dir, p)
	h.keys(press("V", key.ModShortcut, key.ModShift))
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		h.settle()
		if c := h.a.og[srv.URL+"/article"]; c != nil && c.done {
			if c2 := h.a.og[srv.URL+"/plain"]; c2 != nil && c2.done {
				break
			}
		}
	}
	h.settle()
	h.shot("md-linkcard")
	b := h.a.buf()
	cards := 0
	for _, l := range b.md.doc.links {
		if l.card {
			cards++
		}
	}
	if cards != 1 {
		t.Errorf("want exactly one card (the bare article link), got %d", cards)
	}
	if c := h.a.og[srv.URL+"/article"]; c == nil || c.img == nil || !strings.Contains(c.title, "link cards") {
		t.Errorf("card not loaded with its image: %+v", c)
	}

	// switching web content off brings back plain links
	h.a.noWeb = true
	h.a.webVer++
	h.settle()
	for _, l := range b.md.doc.links {
		if l.card {
			t.Error("card still shown with web content off")
		}
	}
}

func TestHugeImageIsRefused(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "huge.png")
	f, _ := os.Create(p)
	png.Encode(f, image.NewGray(image.Rect(0, 0, 12000, 12000))) // 144 MP, ~1 MB file
	f.Close()
	if _, _, err := decodeImageFile(p, 4096); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("12000 × 12000 image was decoded: %v", err)
	}
}
