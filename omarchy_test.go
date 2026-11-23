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

func TestOnlyExplicitFoldersOpenAProject(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	for _, args := range [][]string{nil, {"file.go"}} {
		root, _ := resolveArgs(args)
		if root != "" {
			t.Fatalf("args %v opened folder %q", args, root)
		}
	}
	root, _ := resolveArgs([]string{"."})
	want, _ := filepath.EvalSymlinks(dir)
	if root != want {
		t.Fatalf("explicit folder: got %q, want %q", root, want)
	}
}

func TestRestoreDraftWithoutProject(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	t.Setenv("HOME", t.TempDir())
	project := newApp(nil, dir, newTheme("github"), defaultFontSp)
	path := filepath.Join(dir, "saved.txt")
	if err := os.WriteFile(path, []byte("on disk"), 0600); err != nil {
		t.Fatal(err)
	}
	if !project.openFile(path, 0, 0) {
		t.Fatal("open saved file")
	}
	project.newUntitled()
	project.buf().replaceRange(Pos{}, Pos{}, []rune("unfinished"))
	project.saveSession()
	standalone := newApp(nil, "", project.th, defaultFontSp)
	standalone.restoreSession(dir, true)
	standalone.loadFiles()
	if len(standalone.bufs) != 1 || standalone.buf().text() != "unfinished" || !standalone.buf().dirty() ||
		standalone.showSidebar || standalone.filesLoading || standalone.tree.gitRoot != "" || standalone.tree.root.loaded {
		t.Fatal("expected only the draft and no open project")
	}
}

func TestDiscardLastDraft(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CACHE_HOME", t.TempDir())
	a := newApp(nil, "", newTheme("github"), defaultFontSp)
	a.newUntitled()
	a.buf().replaceRange(Pos{}, Pos{}, []rune("draft"))
	a.saveSession()
	a.requestClose(a.active)
	a.overlayText("c")
	if a.buf() == nil || a.buf().text() != "draft" {
		t.Fatal("cancel lost the draft")
	}
	a.requestClose(a.active)
	a.overlayText("d")
	if len(a.bufs) != 0 {
		t.Fatal("discard left a tab open")
	}
	reopened := newApp(nil, "", a.th, defaultFontSp)
	reopened.restoreSession("", true)
	if len(reopened.bufs) != 0 {
		t.Fatal("discarded draft was restored")
	}
}
