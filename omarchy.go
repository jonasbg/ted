package main

import (
	"bufio"
	"fmt"
	"image/color"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gioui.org/font"
	"github.com/alecthomas/chroma/v2"
)

// Omarchy integration: follow the desktop's current theme (colors.toml) and
// font, and switch live when the theme changes.

type omarchyTheme struct {
	name    string
	dir     string
	palette map[string]string // colors.toml keys -> "#rrggbb"
	light   bool
	stamp   string // changes when the theme changes
}

// omarchyThemeDirs lists where the current theme lives: Omarchy 4 keeps it
// in ~/.local/state, earlier versions in ~/.config.
func omarchyThemeDirs(home string) []string {
	return []string{
		filepath.Join(home, ".local/state/omarchy/current/theme"),
		filepath.Join(home, ".config/omarchy/current/theme"),
	}
}

func loadOmarchyTheme() *omarchyTheme {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	for _, dir := range omarchyThemeDirs(home) {
		pal := readColorsTOML(filepath.Join(dir, "colors.toml"))
		if len(pal) == 0 {
			pal = readAlacrittyColors(filepath.Join(dir, "alacritty.toml"))
		}
		if pal["background"] == "" || pal["foreground"] == "" {
			continue
		}
		t := &omarchyTheme{dir: dir, palette: pal}
		if b, err := os.ReadFile(filepath.Join(filepath.Dir(dir), "theme.name")); err == nil {
			t.name = strings.TrimSpace(string(b))
		} else if target, err := filepath.EvalSymlinks(dir); err == nil {
			t.name = filepath.Base(target)
		}
		_, lightFile := os.Stat(filepath.Join(dir, "light.mode"))
		t.light = pal["mode"] == "light" || lightFile == nil
		t.stamp = omarchyStamp(dir)
		return t
	}
	return nil
}

// omarchyStamp identifies the current theme cheaply (used for live reload).
func omarchyStamp(dir string) string {
	var sb strings.Builder
	if target, err := filepath.EvalSymlinks(dir); err == nil {
		sb.WriteString(target)
	}
	for _, f := range []string{"colors.toml", "alacritty.toml", "../theme.name"} {
		if st, err := os.Stat(filepath.Join(dir, f)); err == nil {
			fmt.Fprintf(&sb, "|%s:%d", st.ModTime().Format(time.RFC3339Nano), st.Size())
		}
	}
	return sb.String()
}

// key = "value", key = 'value' or key = bare (a # outside quotes starts a comment)
var tomlLine = regexp.MustCompile(`^\s*([A-Za-z0-9_.]+)\s*=\s*(?:"([^"]*)"|'([^']*)'|([^\s#]+))`)

// readColorsTOML parses Omarchy's flat colors.toml (key = "#rrggbb").
// Legacy short names are mapped to the canonical ones.
func readColorsTOML(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	pal := map[string]string{}
	legacy := map[string]string{"bg": "background", "fg": "foreground", "dark_bg": "dark_background", "light_bg": "lighter_background", "dark_fg": "dark_foreground", "light_fg": "light_foreground"}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		m := tomlLine.FindStringSubmatch(sc.Text())
		if m == nil {
			continue
		}
		k, v := m[1], m[2]+m[3]+m[4]
		if canon, ok := legacy[k]; ok {
			if _, set := pal[canon]; !set {
				pal[canon] = v
			}
			continue
		}
		pal[k] = v
	}
	return pal
}

// readAlacrittyColors is a fallback for themes without colors.toml.
func readAlacrittyColors(path string) map[string]string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	pal := map[string]string{}
	section := ""
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = strings.Trim(line, "[] ")
			continue
		}
		m := tomlLine.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		k, v := m[1], strings.Replace(m[2]+m[3]+m[4], "0x", "#", 1)
		switch section {
		case "colors.primary":
			pal[k] = v
		case "colors.normal":
			if _, ok := pal[k]; !ok {
				pal[k] = v
			}
			if k == "black" {
				pal["muted"] = v
			}
		case "colors.bright":
			if k == "black" {
				pal["muted"] = v
			} else {
				pal["bright_"+k] = v
			}
		case "colors.selection":
			if k == "background" {
				pal["selection"] = v
			}
		}
	}
	return pal
}

func parseHex(s string) (color.NRGBA, bool) {
	s = strings.TrimPrefix(strings.TrimSpace(s), "#")
	if len(s) != 6 {
		return color.NRGBA{}, false
	}
	var r, g, b uint8
	if _, err := fmt.Sscanf(s, "%02x%02x%02x", &r, &g, &b); err != nil {
		return color.NRGBA{}, false
	}
	return color.NRGBA{r, g, b, 0xff}, true
}

// themeFromOmarchy builds a syntax style and UI theme from the palette, so
// ted matches the terminal, bar and launcher of the current Omarchy theme.
func themeFromOmarchy(o *omarchyTheme) *Theme {
	p := o.palette
	get := func(keys ...string) string {
		for _, k := range keys {
			if v := p[k]; v != "" {
				if _, ok := parseHex(v); ok {
					return v
				}
			}
		}
		return ""
	}
	bg, fg := get("background"), get("foreground")
	muted := get("muted", "dark_foreground")
	if muted == "" {
		mb, _ := parseHex(bg)
		mf, _ := parseHex(fg)
		c := mixC(mb, mf, 0.45)
		muted = fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
	}
	or := func(v, def string) string {
		if v == "" {
			return def
		}
		return v
	}
	blue := or(get("blue"), fg)
	magenta := or(get("magenta", "bright_magenta"), blue)
	cyan := or(get("cyan", "bright_cyan"), blue)
	green := or(get("green", "bright_green"), fg)
	yellow := or(get("yellow", "bright_yellow"), fg)
	orange := or(get("orange", "bright_yellow", "yellow"), yellow)
	red := or(get("red", "bright_red"), fg)

	entries := chroma.StyleEntries{
		chroma.Background:          "bg:" + bg + " " + fg,
		chroma.Text:                fg,
		chroma.Error:               red,
		chroma.Comment:             "italic " + muted,
		chroma.CommentPreproc:      magenta,
		chroma.Keyword:             magenta,
		chroma.KeywordConstant:     orange,
		chroma.KeywordType:         cyan,
		chroma.KeywordNamespace:    magenta,
		chroma.Name:                fg,
		chroma.NameFunction:        blue,
		chroma.NameBuiltin:         cyan,
		chroma.NameClass:           yellow,
		chroma.NameDecorator:       yellow,
		chroma.NameTag:             red,
		chroma.NameAttribute:       yellow,
		chroma.NameConstant:        orange,
		chroma.NameNamespace:       cyan,
		chroma.NameVariableMagic:   red,
		chroma.LiteralString:       green,
		chroma.LiteralStringEscape: cyan,
		chroma.LiteralStringRegex:  cyan,
		chroma.LiteralNumber:       orange,
		chroma.Operator:            cyan,
		chroma.Punctuation:         fg,
		chroma.GenericHeading:      "bold " + blue,
		chroma.GenericSubheading:   "bold " + magenta,
		chroma.GenericInserted:     green,
		chroma.GenericDeleted:      red,
		chroma.GenericEmph:         "italic",
		chroma.GenericStrong:       "bold",
	}
	name := "omarchy"
	if o.name != "" {
		name = "omarchy-" + o.name
	}
	st, err := chroma.NewStyle(name, entries)
	if err != nil {
		return nil
	}
	t := themeFromStyle(st)
	// Use the theme's own accents for the UI.
	if c, ok := parseHex(get("accent", "blue")); ok {
		t.accent, t.caret = c, c
		t.sideSel = mixC(t.sidebar, c, 0.2)
		t.selection = mixC(t.bg, c, 0.3)
	}
	if c, ok := parseHex(get("selection")); ok {
		t.selection = mixC(t.bg, c, 0.6)
	}
	if c, ok := parseHex(orange); ok {
		t.orange = c
	}
	if c, ok := parseHex(green); ok {
		t.gitNew = c
		t.diffAdd = mixC(t.bg, c, 0.13)
	}
	if c, ok := parseHex(yellow); ok {
		t.gitMod = c
	}
	if c, ok := parseHex(red); ok {
		t.gitDel, t.errorFg = c, c
		t.diffDel = mixC(t.bg, c, 0.14)
	}
	t.light = o.light || t.light
	return t
}

// omarchyFont returns the monospace family Omarchy configured through
// fontconfig (omarchy-font-set writes it), if any.
func omarchyFont() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	data, err := os.ReadFile(filepath.Join(home, ".config/fontconfig/fonts.conf"))
	if err != nil {
		return ""
	}
	return monospaceFromFontconfig(string(data))
}

var fcMatch = regexp.MustCompile(`(?s)<match[^>]*>(.*?)</match>`)
var fcString = regexp.MustCompile(`<string>\s*([^<]+?)\s*</string>`)

func monospaceFromFontconfig(conf string) string {
	for _, m := range fcMatch.FindAllStringSubmatch(conf, -1) {
		body := m[1]
		test := strings.Index(body, "<test")
		edit := strings.Index(body, "<edit")
		if test < 0 || edit < 0 || !strings.Contains(body[test:edit], ">monospace<") {
			continue
		}
		if s := fcString.FindStringSubmatch(body[edit:]); s != nil {
			return s[1]
		}
	}
	return ""
}

// useOmarchyFont puts the desktop font first for code, and for the UI too,
// since Omarchy uses it as the system font.
func useOmarchyFont(family string) {
	if family == "" {
		return
	}
	for f := range faceFonts {
		ff := faceFonts[f]
		ff.Typeface = font.Typeface(family + ", " + string(ff.Typeface))
		faceFonts[f] = ff
	}
}

// checkOmarchyTheme switches theme live when Omarchy's theme changes.
func (a *App) checkOmarchyTheme() {
	if a.omarchy == nil || a.themeFixed {
		return
	}
	if omarchyStamp(a.omarchy.dir) == a.omarchy.stamp {
		return
	}
	o := loadOmarchyTheme()
	if o == nil {
		return
	}
	if t := themeFromOmarchy(o); t != nil {
		a.omarchy = o
		a.setTheme(t)
	}
}

// setTheme swaps the theme and re-highlights every open buffer.
func (a *App) setTheme(t *Theme) {
	a.th = t
	for _, b := range a.bufs {
		b.hl.version = -1
		b.hl.spans = nil
	}
	a.mm.valid = false
	a.styleWindow()
}
