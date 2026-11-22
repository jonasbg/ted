package main

import (
	"image/color"
	"os"
	"path/filepath"
	"testing"

	"github.com/alecthomas/chroma/v2"
)

// A Tokyo Night palette in Omarchy's colors.toml format (test fixture).
const tokyoNight = `mode = "dark"
accent = "#7aa2f7"
selection = "#2e3c64"
muted = "#565f89"
background = "#1a1b26"
foreground = "#a9b1d6"
red = "#f7768e"
yellow = "#e0af68"
orange = "#ff9e64"
green = "#9ece6a"
cyan = "#7dcfff"
blue = "#7aa2f7"
magenta = "#bb9af7"
`

func fakeOmarchy(t *testing.T, name, colors string) string {
	home := t.TempDir()
	cur := filepath.Join(home, ".local/state/omarchy/current")
	os.MkdirAll(filepath.Join(cur, "theme"), 0o755)
	os.WriteFile(filepath.Join(cur, "theme", "colors.toml"), []byte(colors), 0o644)
	os.WriteFile(filepath.Join(cur, "theme.name"), []byte(name+"\n"), 0o644)
	t.Setenv("HOME", home)
	return home
}

func TestOmarchyTheme(t *testing.T) {
	fakeOmarchy(t, "tokyo-night", tokyoNight)
	o := loadOmarchyTheme()
	if o == nil || o.name != "tokyo-night" || o.light {
		t.Fatalf("theme not detected: %+v", o)
	}
	th := themeFromOmarchy(o)
	want := map[string][2]any{
		"bg":     {th.bg, "#1a1b26"},
		"fg":     {th.fg, "#a9b1d6"},
		"accent": {th.accent, "#7aa2f7"},
		"orange": {th.orange, "#ff9e64"},
	}
	for k, v := range want {
		c, _ := parseHex(v[1].(string))
		if v[0] != c {
			t.Errorf("%s = %v, want %v", k, v[0], v[1])
		}
	}
	// keywords use the theme's magenta, strings its green
	if s := th.tokenStyle(chromaKeyword()); s.fg != mustHex("#bb9af7") {
		t.Errorf("keyword colour %v", s.fg)
	}
}

func TestOmarchyLiveSwitch(t *testing.T) {
	home := fakeOmarchy(t, "tokyo-night", tokyoNight)
	h := newHarness(t, t.TempDir())
	h.a.omarchy = loadOmarchyTheme()
	h.a.setTheme(themeFromOmarchy(h.a.omarchy))
	cur := filepath.Join(home, ".local/state/omarchy/current")
	light := "mode = \"light\"\nbackground = \"#e1e2e7\"\nforeground = \"#3760bf\"\naccent = \"#2e7de9\"\n"
	os.WriteFile(filepath.Join(cur, "theme", "colors.toml"), []byte(light), 0o644)
	os.WriteFile(filepath.Join(cur, "theme.name"), []byte("tokyo-night-day\n"), 0o644)
	h.a.checkOmarchyTheme()
	if h.a.th.bg != mustHex("#e1e2e7") || !h.a.th.light {
		t.Fatalf("live switch failed: bg=%v light=%v", h.a.th.bg, h.a.th.light)
	}
}

func TestOmarchyFontAndFallbacks(t *testing.T) {
	conf := `<?xml version="1.0"?><fontconfig>
  <match target="pattern"><test qual="any" name="family"><string>monospace</string></test>
    <edit name="family" mode="prepend_first" binding="strong"><string>JetBrainsMono Nerd Font</string></edit></match>
</fontconfig>`
	if f := monospaceFromFontconfig(conf); f != "JetBrainsMono Nerd Font" {
		t.Fatalf("font %q", f)
	}
	// legacy short keys in colors.toml
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "c.toml"), []byte("bg = \"#000000\"\nfg = \"#ffffff\"\n"), 0o644)
	if p := readColorsTOML(filepath.Join(dir, "c.toml")); p["background"] != "#000000" || p["foreground"] != "#ffffff" {
		t.Fatalf("legacy keys: %v", p)
	}
	// older themes: alacritty.toml only
	os.WriteFile(filepath.Join(dir, "a.toml"), []byte("[colors.primary]\nbackground = \"0x1a1b26\"\nforeground = \"#a9b1d6\"\n\n[colors.normal]\nred = \"#f7768e\"\n"), 0o644)
	if p := readAlacrittyColors(filepath.Join(dir, "a.toml")); p["background"] != "#1a1b26" || p["red"] != "#f7768e" {
		t.Fatalf("alacritty fallback: %v", p)
	}
}

func TestOmarchyScreenshot(t *testing.T) {
	fakeOmarchy(t, "tokyo-night", tokyoNight)
	root, _ := os.Getwd()
	h := newHarness(t, root, filepath.Join(root, "omarchy.go"))
	h.a.setTheme(themeFromOmarchy(loadOmarchyTheme()))
	h.settle()
	h.shot("18-omarchy-tokyo-night")
}

func mustHex(s string) color.NRGBA { c, _ := parseHex(s); return c }

func chromaKeyword() chroma.TokenType { return chroma.Keyword }

func TestLauncherStartReopensLastProject(t *testing.T) {
	home := t.TempDir()
	if r, err := filepath.EvalSymlinks(home); err == nil {
		home = r
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	proj := filepath.Join(home, "code", "proj")
	os.MkdirAll(proj, 0o755)
	os.MkdirAll(cacheDir(), 0o700)
	os.WriteFile(lastRootFile(), []byte(proj+"\n"), 0o600)
	t.Chdir(home) // launchers start apps in the home folder
	if root, _ := resolveArgs(nil, false); root != home {
		t.Fatalf("terminal opened %q, want home", root)
	}
	root, files := resolveArgs(nil, true)
	if root != proj || len(files) != 0 {
		t.Fatalf("root=%q files=%v, want last project %q", root, files, proj)
	}
	// with no remembered project, fall back to home rather than /
	os.Remove(lastRootFile())
	if root, _ := resolveArgs(nil, true); root != home {
		t.Fatalf("root=%q, want home", root)
	}
}
