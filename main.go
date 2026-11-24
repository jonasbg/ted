// ted is a small, fast native editor you open from the terminal:
// `ted path` shows the file with its project tree, instantly.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"

	"github.com/alecthomas/chroma/v2/styles"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/unit"
)

var version = "0.3.4"

const defaultFontSp = 14

func usage() {
	fmt.Fprintf(os.Stderr, `ted %s - a fast, native code editor you open from the terminal

Usage:
  ted [flags] [path[:line[:col]] ...]

  ted                 restore unsaved tabs or show the welcome screen
  ted src/            open a folder
  ted main.go         open a file without a folder tree
  ted main.go:42      jump to a line
  ted a.go b.go       open several files

ted returns to your shell immediately. If a window for the same project is
already open, the files open there.

Flags:
  -w, --wait          block until the window closes (for GIT_EDITOR, etc.)
  -n, --new           always open a new window
  --native-titlebar   keep the standard macOS title bar
  --theme NAME        colour theme (env TED_THEME), see --themes
  --themes            list themes
  --version           print version

  macOS file-type defaults (LaunchServices):
  --set-html-default   make ted the default app for HTML files
  --unset-html-default restore Safari as the default for HTML files
                       (ted stays available via "Open With")

Press F1 in the editor for keyboard shortcuts.
`, version)
}

func main() {
	flag.Usage = usage
	theme := flag.String("theme", envOr("TED_THEME", "catppuccin-mocha"), "colour theme")
	listThemes := flag.Bool("themes", false, "list themes")
	showVersion := flag.Bool("version", false, "print version")
	wait := flag.Bool("wait", false, "wait for the window to close")
	flag.BoolVar(wait, "w", false, "wait for the window to close")
	nativeTB := flag.Bool("native-titlebar", false, "keep the standard macOS title bar")
	newWin := flag.Bool("new", false, "open a new window")
	flag.BoolVar(newWin, "n", false, "open a new window")
	flag.Bool("launcher", false, "desktop launcher invocation")
	setHTML := flag.Bool("set-html-default", false, "make ted the default app for HTML files (macOS)")
	unsetHTML := flag.Bool("unset-html-default", false, "restore Safari as the default app for HTML files (macOS)")
	flag.CommandLine.Parse(launchArgs(os.Args[1:]))

	switch {
	case *setHTML && *unsetHTML:
		fatal(fmt.Errorf("use only one of --set-html-default and --unset-html-default"))
	case *setHTML:
		if err := setHTMLDefault(true); err != nil {
			fatal(err)
		}
		fmt.Println("HTML files now open in ted by default")
		return
	case *unsetHTML:
		if err := setHTMLDefault(false); err != nil {
			fatal(err)
		}
		fmt.Println("HTML files now open in Safari by default")
		return
	}
	if *showVersion {
		fmt.Println("ted", version)
		return
	}
	if *listThemes {
		fmt.Println(strings.Join(styles.Names(), "\n"))
		return
	}

	root, targets := resolveArgs(flag.Args())
	// Started from Finder, Spotlight or the Dock: macOS owns the process,
	// so stay in the foreground instead of detaching.
	fg := os.Getenv("TED_FOREGROUND") != "" || inAppBundle()
	if !*wait && !*newWin && !fg && sendToRunning(root, targets) {
		return
	}
	if !*wait && !fg {
		detach()
		return
	}

	go registerBundle()

	explicit := os.Getenv("TED_THEME") != ""
	flag.Visit(func(f *flag.Flag) {
		if f.Name == "theme" {
			explicit = true
		}
	})
	cfg = windowConfig{theme: *theme, explicitTheme: explicit, nativeTitlebar: *nativeTB, wait: *wait}

	if *wait {
		// One window, no session, no IPC; the process ends with it.
		openWindow(root, targets)
		app.Main()
		return
	}
	// Finder and the Dock deliver files to an app bundle as open-document
	// events rather than arguments, so route those through the same logic.
	r := newOpenRouter()
	coldFinder := isMac && inAppBundle() && len(flag.Args()) == 0
	go r.run(root, targets, coldFinder)
	for e := range app.Events {
		if u, ok := e.(app.URLEvent); ok {
			if p, ok := fileURLPath(u.URL); ok {
				r.paths <- p
			}
		}
	}
}

// windowConfig holds the options shared by every window of this process.
type windowConfig struct {
	theme          string
	explicitTheme  bool
	nativeTitlebar bool
	wait           bool
}

var cfg windowConfig

// windows tracks the open windows by project root. The process exits when
// the last one closes.
var windows = struct {
	sync.Mutex
	byRoot map[string]*App
}{byRoot: map[string]*App{}}

// openWindow creates a window for root with the given files open.
func openWindow(root string, targets []target) *App {
	w := new(app.Window)
	opts := []app.Option{app.Title("ted"), app.Size(unit.Dp(1280), unit.Dp(820)), app.MinSize(unit.Dp(420), unit.Dp(300))}
	w.Option(append(opts, windowOptions(cfg.nativeTitlebar)...)...)
	// Theme: an explicit --theme or TED_THEME wins; on Omarchy follow the
	// desktop theme and font; otherwise the built-in default.
	th := newTheme(cfg.theme)
	var om *omarchyTheme
	if !cfg.explicitTheme {
		if om = loadOmarchyTheme(); om != nil {
			if t := themeFromOmarchy(om); t != nil {
				th = t
			}
			useOmarchyFont(omarchyFont())
		}
	}
	a := newApp(w, root, th, defaultFontSp)
	a.omarchy, a.themeFixed = om, cfg.explicitTheme
	a.noSession = cfg.wait
	a.nativeTitlebar = cfg.nativeTitlebar
	if !a.noSession {
		a.restoreSession(root, root == "")
		if root == "" && len(targets) == 0 && len(a.bufs) == 0 {
			if last, err := os.ReadFile(lastRootFile()); err == nil {
				if prior := strings.TrimSuffix(string(last), "\n"); prior != "" {
					a.restoreSession(prior, true)
				}
			}
		}
	}
	for _, t := range targets {
		a.openFile(t.path, t.line, t.col)
	}
	if len(a.bufs) == 0 && root != "" {
		a.focus = focusTree
	}
	windows.Lock()
	windows.byRoot[root] = a
	windows.Unlock()
	if !cfg.wait {
		go a.listen()
	}
	a.startTicker()
	a.refreshGit()
	a.loadFiles()

	go func() {
		err := a.loop()
		if a.socket != "" {
			os.Remove(a.socket)
		}
		if err != nil {
			fmt.Fprintln(os.Stderr, "ted:", err)
		}
		windows.Lock()
		if windows.byRoot[root] == a {
			delete(windows.byRoot, root)
		}
		last := len(windows.byRoot) == 0
		windows.Unlock()
		if last {
			if err != nil {
				os.Exit(1)
			}
			os.Exit(0)
		}
	}()
	return a
}

func (a *App) loop() error {
	var ops op.Ops
	for {
		switch e := a.w.Event().(type) {
		case app.DestroyEvent:
			a.saveSession()
			return e.Err
		case app.ConfigEvent:
			if e.Config.Focused && !a.focused {
				// catch up on commits and checkouts made in a terminal
				a.refreshGit()
			}
			a.focused = e.Config.Focused
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			a.frame(gtx)
			e.Frame(gtx.Ops)
		default:
			a.platformEvent(e)
		}
	}
}

// launchArgs drops arguments macOS adds when launching an app bundle.
func launchArgs(args []string) []string {
	out := args[:0:0]
	for _, a := range args {
		if !strings.HasPrefix(a, "-psn_") {
			out = append(out, a)
		}
	}
	return out
}

func inAppBundle() bool {
	exe, err := os.Executable()
	return err == nil && strings.Contains(exe, ".app/Contents/MacOS/")
}

// lastRootFile identifies the most recently saved session for draft recovery.
func lastRootFile() string { return filepath.Join(cacheDir(), "last-root") }

// resolveArgs turns command-line paths into a project root and files.
func resolveArgs(args []string) (string, []target) {
	var files []target
	root := ""
	for _, arg := range args {
		p, line, col := arg, 0, 0
		if _, err := os.Stat(p); err != nil {
			if q, l, c := splitLineSuffix(arg); l > 0 {
				p, line, col = q, l, c
			}
		}
		abs, err := filepath.Abs(p)
		if err != nil {
			continue
		}
		if st, err := os.Stat(abs); err == nil && st.IsDir() {
			if root == "" {
				root = abs
			}
			continue
		}
		files = append(files, target{abs, line, col})
	}
	if root != "" {
		if r, err := filepath.EvalSymlinks(root); err == nil {
			root = r
		}
	}
	return root, files
}

// detach re-runs ted in the background so the shell prompt returns at once.
func detach() {
	exe, err := os.Executable()
	if err != nil {
		fatal(err)
	}
	cmd := exec.Command(exe, os.Args[1:]...)
	cmd.Env = append(os.Environ(), "TED_FOREGROUND=1")
	if wd, err := os.Getwd(); err == nil {
		cmd.Dir = wd
	}
	os.MkdirAll(cacheDir(), 0o700)
	if log, err := os.OpenFile(filepath.Join(cacheDir(), "ted.log"), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600); err == nil {
		cmd.Stdout, cmd.Stderr = log, log
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fatal(err)
	}
	cmd.Process.Release()
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, "ted:", err)
	os.Exit(1)
}
