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
	"syscall"

	"github.com/alecthomas/chroma/v2/styles"

	"gioui.org/app"
	"gioui.org/op"
	"gioui.org/unit"
)

var version = "0.2.0"

const defaultFontSp = 14

func usage() {
	fmt.Fprintf(os.Stderr, `ted %s - a fast, native code editor you open from the terminal

Usage:
  ted [flags] [path[:line[:col]] ...]

  ted                 open the current folder
  ted src/            open a folder
  ted main.go         open a file (the tree shows its git project)
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
	flag.Parse()

	if *showVersion {
		fmt.Println("ted", version)
		return
	}
	if *listThemes {
		fmt.Println(strings.Join(styles.Names(), "\n"))
		return
	}

	root, targets := resolveArgs(flag.Args())
	fg := os.Getenv("TED_FOREGROUND") != ""
	if !*wait && !*newWin && !fg && sendToRunning(root, targets) {
		return
	}
	if !*wait && !fg {
		detach()
		return
	}

	w := new(app.Window)
	opts := []app.Option{app.Title("ted"), app.Size(unit.Dp(1280), unit.Dp(820)), app.MinSize(unit.Dp(420), unit.Dp(300))}
	w.Option(append(opts, windowOptions(*nativeTB)...)...)
	a := newApp(w, root, newTheme(*theme), defaultFontSp)
	a.noSession = *wait
	a.nativeTitlebar = *nativeTB
	if !a.noSession {
		a.restoreSession()
	}
	for _, t := range targets {
		a.openFile(t.path, t.line, t.col)
	}
	if len(a.bufs) == 0 {
		a.focus = focusTree
	}
	if !*wait {
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
			os.Exit(1)
		}
		os.Exit(0)
	}()
	app.Main()
}

func (a *App) loop() error {
	var ops op.Ops
	for {
		switch e := a.w.Event().(type) {
		case app.DestroyEvent:
			a.saveSession()
			return e.Err
		case app.ConfigEvent:
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

// resolveArgs turns command-line paths into a project root and files.
func resolveArgs(args []string) (string, []target) {
	cwd, _ := os.Getwd()
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
	if root == "" {
		root = cwd
		if len(files) > 0 {
			// Use the file's project: its git repo, else its folder (unless
			// it already lives under the current directory).
			f := files[0].path
			dir := filepath.Dir(f)
			if top := gitToplevel(dir); top != "" {
				root = top
			} else if !strings.HasPrefix(f, cwd+string(filepath.Separator)) {
				root = dir
			}
		}
	}
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
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
