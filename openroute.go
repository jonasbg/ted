package main

import (
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// coldLaunchWait is the longest a bundle started by Finder waits for the
// open-document event before falling back to the last project. macOS sends
// it before it finishes launching the app, so normally launched ends the
// wait well before this.
const coldLaunchWait = 400 * time.Millisecond

// openRouter decides where files handed to the process (by Finder, the
// Dock or "Open With") should open. It runs on one goroutine so a batch of
// files from the same project lands in one window.
type openRouter struct {
	paths    chan string
	launched <-chan struct{} // see appLaunched; nil waits the full coldLaunchWait
	// for tests: how to open a window and reach other ted processes
	open func(root string, targets []target) *App
	send func(root string, targets []target) bool
}

func newOpenRouter() *openRouter {
	return &openRouter{paths: make(chan string, 64), launched: appLaunched(), open: openWindow, send: sendToRunning}
}

// run opens the initial window, then routes incoming paths forever.
func (r *openRouter) run(root string, targets []target, coldFinder bool) {
	if coldFinder {
		// Launched without arguments from an app bundle: if Finder is about
		// to hand us a file, open the window with it rather than with the
		// recovered drafts of the last session.
		p := ""
		select {
		case p = <-r.paths:
		case <-r.launched:
			// Files come before the launch finishes, so none is coming
			// unless one is already queued.
			select {
			case p = <-r.paths:
			default:
			}
		case <-time.After(coldLaunchWait):
		}
		if p == "" {
			r.open(root, targets)
		} else {
			r.route(p)
			r.drain()
			windows.Lock()
			n := len(windows.byRoot)
			windows.Unlock()
			if n == 0 {
				// every file went to an already running ted
				os.Exit(0)
			}
		}
	} else {
		r.open(root, targets)
	}
	for p := range r.paths {
		r.route(p)
	}
}

// drain routes paths that arrived in the same batch as the first one.
func (r *openRouter) drain() {
	for {
		select {
		case p := <-r.paths:
			r.route(p)
		case <-time.After(50 * time.Millisecond):
			return
		}
	}
}

// route opens one path: in a window of this process (a project containing
// the file, or the standalone window), else in another ted process, else in
// a new window.
func (r *openRouter) route(p string) {
	root, targets := resolveArgs([]string{p})
	if a := windowFor(root, targets); a != nil {
		a.post(openEvent{targets}) // also raises the window
		return
	}
	if r.send(root, targets) {
		return
	}
	r.open(root, targets)
}

// windowFor finds the window of this process that should show root and
// targets.
func windowFor(root string, targets []target) *App {
	windows.Lock()
	defer windows.Unlock()
	if root != "" || len(targets) == 0 {
		return windows.byRoot[root]
	}
	// A lone file: prefer a project window that contains it, else the
	// standalone (no folder) window.
	f := targets[0].path
	var best *App
	for r, a := range windows.byRoot {
		if r != "" && strings.HasPrefix(f, r+string(filepath.Separator)) && (best == nil || len(r) > len(best.root)) {
			best = a
		}
	}
	if best == nil {
		best = windows.byRoot[""]
	}
	return best
}

// fileURLPath turns a file:// URL from an open-document event into a path.
func fileURLPath(u *url.URL) (string, bool) {
	if u == nil || u.Scheme != "file" {
		return "", false
	}
	p := u.Path
	if p == "" {
		return "", false
	}
	return filepath.Clean(p), true
}
