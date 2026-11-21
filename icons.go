package main

import (
	"image"
	"image/color"

	"gioui.org/f32"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
)

// Line icons for the activity bar, drawn as vector paths on a 24-unit grid
// centred on (cx, cy), in the style of VS Code's codicons.

func iconStroke(ops *op.Ops, c color.NRGBA, width float32, build func(p *clip.Path)) {
	var p clip.Path
	p.Begin(ops)
	build(&p)
	paint.FillShape(ops, c, clip.Stroke{Path: p.End(), Width: width}.Op())
}

func ring(ops *op.Ops, x, y, r, width float32, c color.NRGBA) {
	e := clip.Ellipse{Min: image.Pt(int(x-r), int(y-r)), Max: image.Pt(int(x+r+0.5), int(y+r+0.5))}
	paint.FillShape(ops, c, clip.Stroke{Path: e.Path(ops), Width: width}.Op())
}

// iconFiles: two overlapping documents, the front one with a folded corner.
func iconFiles(ops *op.Ops, cx, cy int, u float32, c color.NRGBA) {
	x, y := float32(cx), float32(cy)
	pt := func(dx, dy float32) f32.Point { return f32.Pt(x+dx*u, y+dy*u) }
	w := 1.5 * u
	// back document (only its visible top/left edges)
	iconStroke(ops, c, w, func(p *clip.Path) {
		p.MoveTo(pt(-6, 6))
		p.LineTo(pt(-6, -9))
		p.LineTo(pt(3, -9))
	})
	// front document with a dog-ear
	iconStroke(ops, c, w, func(p *clip.Path) {
		p.MoveTo(pt(-2.5, -5.5))
		p.LineTo(pt(4.5, -5.5))
		p.LineTo(pt(8.5, -1.5))
		p.LineTo(pt(8.5, 9.5))
		p.LineTo(pt(-2.5, 9.5))
		p.Close()
		p.MoveTo(pt(4.5, -5.5))
		p.LineTo(pt(4.5, -1.5))
		p.LineTo(pt(8.5, -1.5))
	})
}

// iconBranch: the source control symbol, two commits on a line and a
// branch curving off to a third.
func iconBranch(ops *op.Ops, cx, cy int, u float32, c color.NRGBA) {
	x, y := float32(cx), float32(cy)
	pt := func(dx, dy float32) f32.Point { return f32.Pt(x+dx*u, y+dy*u) }
	w := 1.5 * u
	r := 2.6 * u
	ring(ops, x-5*u, y-7*u, r, w, c)
	ring(ops, x-5*u, y+7*u, r, w, c)
	ring(ops, x+6*u, y-4*u, r, w, c)
	iconStroke(ops, c, w, func(p *clip.Path) {
		p.MoveTo(pt(-5, -4.2))
		p.LineTo(pt(-5, 4.2))
		p.MoveTo(pt(6, -1.2))
		p.CubeTo(pt(6, 3), pt(-5, 1), pt(-5, 4.2))
	})
}

// iconCommit: a commit on a line, used for history.
func iconCommit(ops *op.Ops, cx, cy int, u float32, c color.NRGBA) {
	x, y := float32(cx), float32(cy)
	pt := func(dx, dy float32) f32.Point { return f32.Pt(x+dx*u, y+dy*u) }
	w := 1.5 * u
	ring(ops, x, y, 4*u, w, c)
	iconStroke(ops, c, w, func(p *clip.Path) {
		p.MoveTo(pt(-10, 0))
		p.LineTo(pt(-4, 0))
		p.MoveTo(pt(4, 0))
		p.LineTo(pt(10, 0))
	})
}
