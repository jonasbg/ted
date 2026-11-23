package main

import (
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

func TestFileURLPath(t *testing.T) {
	u, _ := url.Parse("file:///Users/me/My%20Data/a.csv")
	if p, ok := fileURLPath(u); !ok || p != "/Users/me/My Data/a.csv" {
		t.Fatalf("got %q %v", p, ok)
	}
	u, _ = url.Parse("https://example.com/a.csv")
	if _, ok := fileURLPath(u); ok {
		t.Fatal("non-file URL accepted")
	}
}

// Files from Finder go to a project window containing them or the standalone
// window, then to another ted process, and only then to a new window.
func TestOpenRouterRouting(t *testing.T) {
	dir, _ := filepath.EvalSymlinks(t.TempDir())
	projA := filepath.Join(dir, "a")
	projB := filepath.Join(dir, "b")
	for _, d := range []string{filepath.Join(projA, "sub"), projB} {
		os.MkdirAll(d, 0o755)
	}
	fa := filepath.Join(projA, "sub", "x.txt")
	fb := filepath.Join(projB, "y.txt")
	os.WriteFile(fa, []byte("x"), 0o644)
	os.WriteFile(fb, []byte("y"), 0o644)

	winA := newApp(nil, projA, newTheme(""), defaultFontSp)
	windows.Lock()
	windows.byRoot = map[string]*App{projA: winA}
	windows.Unlock()
	defer func() { windows.Lock(); windows.byRoot = map[string]*App{}; windows.Unlock() }()

	var opened, sent []string
	r := &openRouter{
		open: func(root string, ts []target) *App { opened = append(opened, root); return nil },
		send: func(root string, ts []target) bool { sent = append(sent, root); return false },
	}
	r.route(fa) // a lone file inside open project window projA
	winA.mu.Lock()
	got := len(winA.pending)
	winA.mu.Unlock()
	if got != 1 || len(opened) != 0 || len(sent) != 0 {
		t.Fatalf("file in open project: pending=%d opened=%v sent=%v", got, opened, sent)
	}
	r.route(fb) // no window contains it: a standalone window
	if len(sent) != 1 || len(opened) != 1 || opened[0] != "" {
		t.Fatalf("file outside open projects: opened=%v sent=%v", opened, sent)
	}
	// with a standalone window open, the next lone file goes there
	solo := newApp(nil, "", newTheme(""), defaultFontSp)
	windows.Lock()
	windows.byRoot[""] = solo
	windows.Unlock()
	r.route(fb)
	solo.mu.Lock()
	got = len(solo.pending)
	solo.mu.Unlock()
	if got != 1 || len(opened) != 1 {
		t.Fatalf("standalone window: pending=%d opened=%v", got, opened)
	}
	// a folder gets its own project window
	r.route(projB)
	if len(opened) != 2 || opened[1] != projB {
		t.Fatalf("folder: opened=%v", opened)
	}
}
