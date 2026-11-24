package main

import (
	"os"
	"path/filepath"
	"strings"
)

// registerBundle tells macOS to re-read ted's app bundle once per version,
// so file types added in an upgrade take effect. macOS caches them, and the
// ~/Applications/ted.app that Homebrew's caveat suggests links into the
// keg, so it never notices an upgrade. Homebrew can't do this itself: its
// post-install sandbox blocks LaunchServices (lsregister fails with -10822).
func registerBundle() {
	if !isMac {
		return
	}
	stamp := filepath.Join(cacheDir(), "registered-version")
	if b, _ := os.ReadFile(stamp); strings.TrimSpace(string(b)) == version {
		return
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if r, err := filepath.EvalSymlinks(exe); err == nil {
		exe = r
	}
	home, _ := os.UserHomeDir()
	isBundle := func(p string) bool {
		fi, err := os.Stat(filepath.Join(p, "Contents", "Info.plist"))
		return err == nil && fi.Mode().IsRegular()
	}
	done := false
	for _, b := range bundleCandidates(exe, home, isBundle) {
		if lsRegister(b) == nil {
			done = true
		}
	}
	if done {
		os.MkdirAll(filepath.Dir(stamp), 0o755)
		os.WriteFile(stamp, []byte(version+"\n"), 0o644)
	}
}

// bundleCandidates lists the ted.app bundles to register: the one this
// binary runs from, and ~/Applications/ted.app. Homebrew's own copy (next to
// bin/ted in the keg) only when there is no ~/Applications/ted.app, so
// macOS doesn't collect a registration for every version's keg.
func bundleCandidates(exe, home string, isBundle func(string) bool) []string {
	var out []string
	add := func(p string) {
		p = filepath.Clean(p)
		for _, q := range out {
			if q == p {
				return
			}
		}
		if isBundle(p) {
			out = append(out, p)
		}
	}
	if i := strings.Index(exe, ".app/Contents/MacOS/"); i >= 0 {
		add(exe[:i+len(".app")])
	}
	apps := ""
	if home != "" {
		apps = filepath.Join(home, "Applications", "ted.app")
		add(apps)
	}
	if apps == "" || !isBundle(apps) {
		add(filepath.Join(filepath.Dir(exe), "..", "ted.app"))
	}
	return out
}
