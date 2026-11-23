// mkicon renders ted's app icon as PNG files: a flat dark tile with lines
// of "code" as square-cut colour blocks, and the orange underline used for
// the active tab.
//
//	go run ./tools/mkicon -out dir -sizes 16,32,...
package main

import (
	"flag"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type rrect struct {
	x0, y0, x1, y1, r float64
	c                 color.NRGBA
}

// inside reports whether (x, y) lies in the rounded rectangle.
func (r rrect) inside(x, y float64) bool {
	if x < r.x0 || x > r.x1 || y < r.y0 || y > r.y1 {
		return false
	}
	cx := math.Max(r.x0+r.r, math.Min(x, r.x1-r.r))
	cy := math.Max(r.y0+r.r, math.Min(y, r.y1-r.r))
	return (x-cx)*(x-cx)+(y-cy)*(y-cy) <= r.r*r.r
}

func hex(s string) color.NRGBA {
	v, _ := strconv.ParseUint(strings.TrimPrefix(s, "#"), 16, 32)
	return color.NRGBA{uint8(v >> 16), uint8(v >> 8), uint8(v), 0xff}
}

// shapes on a 1024-unit canvas (macOS icon grid: tile inset ~10%). Flat:
// solid fills, square-cut bars on an even grid, no pills or shading.
func shapes() []rrect {
	tile := rrect{100, 100, 924, 924, 185, hex("#1a1b26")}
	const h, step, r = 64, 112, 6 // bar height, line pitch, corner radius
	bar := func(line int, x, w float64, c string) rrect {
		y := 268 + float64(line)*step
		return rrect{x, y, x + w, y + h, r, hex(c)}
	}
	return []rrect{
		tile,
		bar(0, 232, 240, "#bb9af7"),             // keyword
		bar(0, 496, 296, "#7aa2f7"),             // function
		bar(1, 328, 200, "#9ece6a"),             // string
		bar(1, 552, 168, "#7dcfff"),             // operator
		bar(2, 328, 336, "#c0caf5"),             // text
		bar(3, 232, 128, "#bb9af7"),             // closing brace
		{232, 736, 560, 768, 0, hex("#ff9e64")}, // the active-tab underline
	}
}

func render(size int) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, size, size))
	sh := shapes()
	const ss = 4 // supersampling per axis
	scale := 1024.0 / float64(size)
	for py := 0; py < size; py++ {
		for px := 0; px < size; px++ {
			var r, g, b, a float64
			for sy := 0; sy < ss; sy++ {
				for sx := 0; sx < ss; sx++ {
					x := (float64(px) + (float64(sx)+0.5)/ss) * scale
					y := (float64(py) + (float64(sy)+0.5)/ss) * scale
					// paint shapes in order, later ones on top
					var c color.NRGBA
					for _, s := range sh {
						if s.inside(x, y) {
							c = s.c
						}
					}
					if c.A > 0 {
						r += float64(c.R)
						g += float64(c.G)
						b += float64(c.B)
						a++
					}
				}
			}
			if a > 0 {
				n := float64(ss * ss)
				img.SetNRGBA(px, py, color.NRGBA{uint8(r / a), uint8(g / a), uint8(b / a), uint8(255 * a / n)})
			}
		}
	}
	return img
}

func main() {
	out := flag.String("out", ".", "output directory")
	sizes := flag.String("sizes", "16,32,64,128,256,512,1024", "comma separated sizes")
	name := flag.String("name", "icon_%d.png", "file name pattern")
	flag.Parse()
	os.MkdirAll(*out, 0o755)
	for _, s := range strings.Split(*sizes, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(s))
		if err != nil {
			continue
		}
		filename := *name
		if strings.Contains(filename, "%d") {
			filename = fmt.Sprintf(filename, n)
		}
		f, err := os.Create(filepath.Join(*out, filename))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		png.Encode(f, render(n))
		f.Close()
	}
}
