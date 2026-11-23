package main

import (
	_ "embed"
	"fmt"
	"image"
	"image/color"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"gioui.org/layout"
)

//go:embed packaging/ted.desktop
var desktopEntry string

// defaultsResult reports a file-association change made in the background.
type defaultsResult struct {
	err error
	msg string
}

// openSettings shows the settings page (Cmd/Ctrl+,): version, editor and
// file tree options, and the default-app association for this platform.
func (a *App) openSettings() {
	a.closeOverlay()
	a.overlay = &Overlay{hover: -1, hoverBtn: -1, kind: ovSettings, htmlTed: isMac && htmlDefaultIsTed()}
}

func osName() string {
	switch runtime.GOOS {
	case "darwin":
		return "macOS"
	case "linux":
		return "Linux"
	}
	return runtime.GOOS
}

func (a *App) setDefaults(msg string, f func() error) {
	if a.settingDefaults {
		return
	}
	a.settingDefaults = true
	a.setMsg("Setting file associations…")
	go func() { a.post(defaultsResult{f(), msg}) }()
}

func (a *App) setDotfiles(on bool) {
	a.tree.showHidden = on
	a.tree.refresh()
}

// settingsRow is one labelled setting with its control on the right.
type settingsRow struct {
	label, help string
	draw        func(r image.Rectangle) // draws the control right-aligned in r
}

func (a *App) drawSettings(gtx layout.Context, o *Overlay) {
	ops := gtx.Ops
	th := a.th
	W, H := gtx.Constraints.Max.X, gtx.Constraints.Max.Y
	sc := shortcutName()
	o.buttons = o.buttons[:0]
	// control adds a clickable area; clicks run action and keep the page open
	control := func(r image.Rectangle, action func()) bool {
		i := len(o.btns)
		o.btns = append(o.btns, r)
		o.buttons = append(o.buttons, button{action: action})
		handCursor(ops, r)
		return o.hoverBtn == i
	}
	pill := func(r image.Rectangle, label string, primary bool, action func()) {
		hover := control(r, action)
		bg, fg := th.panel2, th.fg
		if primary {
			bg, fg = th.accent, th.onAccent
		}
		if hover {
			bg = mixC(bg, th.fg, 0.12)
		}
		rrect(ops, r, gtx.Dp(6), bg)
		w := a.tc.width(fUIBold, a.ui.uiPx, label)
		a.tc.draw(ops, fUIBold, a.ui.uiPx, r.Min.X+(r.Dx()-w)/2, a.uiBaseline(r.Min.Y, r.Dy()), label, fg)
	}
	ctlH := gtx.Dp(28)
	// toggle draws an on/off switch at the right edge of r
	toggle := func(r image.Rectangle, on bool, set func(bool)) {
		w, h := gtx.Dp(40), gtx.Dp(22)
		sr := image.Rect(r.Max.X-w, r.Min.Y+(r.Dy()-h)/2, r.Max.X, r.Min.Y+(r.Dy()+h)/2)
		hover := control(sr, func() { set(!on) })
		bg := mixC(th.panel2, th.fg, 0.08)
		if on {
			bg = th.accent
		}
		if hover {
			bg = mixC(bg, th.fg, 0.12)
		}
		rrect(ops, sr, h/2, bg)
		k := h - gtx.Dp(6)
		kx := sr.Min.X + gtx.Dp(3)
		if on {
			kx = sr.Max.X - gtx.Dp(3) - k
		}
		knob := th.fg
		if on {
			knob = th.onAccent
		}
		rrect(ops, image.Rect(kx, sr.Min.Y+gtx.Dp(3), kx+k, sr.Min.Y+gtx.Dp(3)+k), k/2, knob)
	}

	sections := []struct {
		title string
		rows  []settingsRow
	}{
		{"Editor", []settingsRow{{
			"Font size", sc + "+= and " + sc + "+- also change it, " + sc + "+0 resets",
			func(r image.Rectangle) {
				y := r.Min.Y + (r.Dy()-ctlH)/2
				x := r.Max.X
				plus := image.Rect(x-ctlH, y, x, y+ctlH)
				x = plus.Min.X - gtx.Dp(44)
				val := strconv.Itoa(int(a.fontSp))
				vw := a.tc.width(fMono, a.ui.uiPx, val)
				a.tc.draw(ops, fMono, a.ui.uiPx, x+(gtx.Dp(44)-vw)/2, a.uiBaseline(y, ctlH), val, th.fg)
				minus := image.Rect(x-ctlH, y, x, y+ctlH)
				pill(minus, "−", false, func() { a.fontSp = max(8, a.fontSp-1) })
				pill(plus, "+", false, func() { a.fontSp = min(40, a.fontSp+1) })
				if a.fontSp != defaultFontSp {
					rw := a.tc.width(fUIBold, a.ui.uiPx, "Reset") + gtx.Dp(24)
					pill(image.Rect(minus.Min.X-gtx.Dp(10)-rw, y, minus.Min.X-gtx.Dp(10), y+ctlH), "Reset", false, func() { a.fontSp = defaultFontSp })
				}
			},
		}}},
		{"File tree", []settingsRow{{
			"Show dotfiles", "Names starting with a dot. Press . in the file tree to toggle",
			func(r image.Rectangle) { toggle(r, a.tree.showHidden, a.setDotfiles) },
		}}},
		{"Markdown preview", []settingsRow{{
			"Link cards and web images", "Fetches pages linked on a line of their own, and remote images",
			func(r image.Rectangle) {
				toggle(r, !a.noWeb, func(on bool) {
					a.noWeb = !on
					a.webVer++
				})
			},
		}}},
	}
	switch runtime.GOOS {
	case "darwin":
		sections = append(sections, struct {
			title string
			rows  []settingsRow
		}{"Default app", []settingsRow{{
			"Open HTML files in Ted", "When off, HTML files open in Safari",
			func(r image.Rectangle) {
				toggle(r, o.htmlTed, func(on bool) {
					o.htmlTed = on
					msg := "HTML files now open in Safari"
					if on {
						msg = "HTML files now open in Ted"
					}
					a.setDefaults(msg, func() error { return setHTMLDefault(on) })
				})
			},
		}}})
	case "linux":
		sections = append(sections, struct {
			title string
			rows  []settingsRow
		}{"Default app", []settingsRow{{
			"Open text and code files in Ted", "Sets Ted as the handler for its file types with gio mime",
			func(r image.Rectangle) {
				w := a.tc.width(fUIBold, a.ui.uiPx, "Make default") + gtx.Dp(28)
				y := r.Min.Y + (r.Dy()-ctlH)/2
				pill(image.Rect(r.Max.X-w, y, r.Max.X, y+ctlH), "Make default", false, func() {
					a.setDefaults("Ted is the default editor for text and code files", setFileDefaults)
				})
			},
		}}})
	}

	pad := gtx.Dp(28)
	headH := gtx.Dp(104)
	secH := gtx.Dp(36)
	rowH := gtx.Dp(56)
	footH := gtx.Dp(68)
	bh := headH + footH
	for _, s := range sections {
		bh += secH + len(s.rows)*rowH
	}
	bw := min(W-gtx.Dp(40), gtx.Dp(580))
	bx := (W - bw) / 2
	by := max(gtx.Dp(40), min(gtx.Dp(72), (H-bh)/2))
	rad := gtx.Dp(12)
	o.box = image.Rect(bx, by, bx+bw, by+bh)
	shadow(ops, o.box, rad, gtx.Dp(22), th.shadow)
	rrect(ops, o.box, rad, th.panel)
	strokeRRect(ops, o.box, rad, 1, th.border)

	// header: the app icon, name, version and platform
	ic := gtx.Dp(56)
	ix, iy := bx+pad, by+(headH-ic)/2
	drawAppIcon(gtx, image.Rect(ix, iy, ix+ic, iy+ic))
	tx := ix + ic + gtx.Dp(18)
	a.tc.draw(ops, fUIBold, gtx.Sp(20), tx, iy+gtx.Dp(22), "Ted", th.fg)
	a.tc.draw(ops, fUI, a.ui.uiPx, tx, iy+gtx.Dp(42), "Version "+version, th.fg)
	a.tc.draw(ops, fUI, gtx.Sp(12), tx, iy+gtx.Dp(59), osName()+" · "+runtime.GOARCH+" · "+runtime.Version(), th.dim)
	// close button
	cs := gtx.Dp(28)
	cr := image.Rect(o.box.Max.X-gtx.Dp(14)-cs, by+gtx.Dp(14), o.box.Max.X-gtx.Dp(14), by+gtx.Dp(14)+cs)
	cfg := th.dim
	if control(cr, a.closeOverlay) {
		rrect(ops, cr, gtx.Dp(6), th.panel2)
		cfg = th.fg
	}
	cw := a.tc.width(fUI, gtx.Sp(14), "✕")
	a.tc.draw(ops, fUI, gtx.Sp(14), cr.Min.X+(cs-cw)/2, a.uiBaseline(cr.Min.Y, cs), "✕", cfg)
	y := by + headH
	fill(ops, image.Rect(bx, y-1, bx+bw, y), th.border)

	for _, s := range sections {
		a.tc.draw(ops, fUIBold, gtx.Sp(11), bx+pad, y+gtx.Dp(26), strings.ToUpper(s.title), th.accent)
		y += secH
		for _, row := range s.rows {
			a.tc.draw(ops, fUI, a.ui.uiPx, bx+pad, y+gtx.Dp(22), row.label, th.fg)
			ctl := image.Rect(bx+bw/2, y, bx+bw-pad, y+rowH)
			hw := bw - 2*pad - gtx.Dp(130)
			a.tc.draw(ops, fUI, gtx.Sp(12), bx+pad, y+gtx.Dp(41), a.tc.truncate(fUI, gtx.Sp(12), hw, row.help), th.dim)
			row.draw(ctl)
			y += rowH
		}
	}

	// footer
	fill(ops, image.Rect(bx, y+gtx.Dp(8), bx+bw, y+gtx.Dp(9)), th.border)
	fy := y + gtx.Dp(8) + (footH-gtx.Dp(8)-gtx.Dp(32))/2
	kw := a.tc.width(fUIBold, a.ui.uiPx, "Keyboard shortcuts") + gtx.Dp(28)
	pill(image.Rect(bx+pad, fy, bx+pad+kw, fy+gtx.Dp(32)), "Keyboard shortcuts", false, func() {
		a.overlay = &Overlay{hover: -1, hoverBtn: -1, kind: ovHelp}
	})
	dw := a.tc.width(fUIBold, a.ui.uiPx, "Done") + gtx.Dp(36)
	pill(image.Rect(bx+bw-pad-dw, fy, bx+bw-pad, fy+gtx.Dp(32)), "Done", true, a.closeOverlay)
	hint := "Esc closes"
	hw := a.tc.width(fUI, gtx.Sp(12), hint)
	a.tc.draw(ops, fUI, gtx.Sp(12), bx+bw-pad-dw-gtx.Dp(14)-hw, a.uiBaseline(fy, gtx.Dp(32)), hint, th.faint)
}

// drawAppIcon draws ted's icon (the shapes tools/mkicon renders) into r.
func drawAppIcon(gtx layout.Context, r image.Rectangle) {
	ops := gtx.Ops
	s := float64(r.Dx()) / 824 // the tile spans 100..924 on mkicon's canvas
	at := func(x0, y0, x1, y1 float64) image.Rectangle {
		return image.Rect(r.Min.X+int((x0-100)*s), r.Min.Y+int((y0-100)*s), r.Min.X+int((x1-100)*s), r.Min.Y+int((y1-100)*s))
	}
	rrect(ops, r, int(185*s), hexColor("#1f2b4d"))
	const h, step = 64, 112
	bar := func(line int, x, w float64, c string) {
		y := 268 + float64(line)*step
		rrect(ops, at(x, y, x+w, y+h), max(1, int(6*s)), hexColor(c))
	}
	bar(0, 232, 240, "#bb9af7")
	bar(0, 496, 296, "#7aa2f7")
	bar(1, 328, 200, "#9ece6a")
	bar(1, 552, 168, "#7dcfff")
	bar(2, 328, 336, "#c0caf5")
	bar(3, 232, 128, "#bb9af7")
	fill(ops, at(232, 736, 560, 768), hexColor("#ff9e64"))
}

func setFileDefaults() error {
	for _, line := range strings.Split(desktopEntry, "\n") {
		types, found := strings.CutPrefix(line, "MimeType=")
		if !found {
			continue
		}
		for _, mime := range strings.Split(strings.TrimSuffix(types, ";"), ";") {
			out, err := exec.Command("gio", "mime", mime, "ted.desktop").CombinedOutput()
			if err != nil {
				return fmt.Errorf("Could not set %s: %w %s", mime, err, strings.TrimSpace(string(out)))
			}
		}
		return nil
	}
	return fmt.Errorf("Ted's desktop entry lists no file types")
}

func hexColor(s string) color.NRGBA {
	c, _ := parseHex(s)
	return c
}
