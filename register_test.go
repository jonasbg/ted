package main

import (
	"slices"
	"testing"
)

func TestBundleCandidates(t *testing.T) {
	keg := "/opt/homebrew/Cellar/ted/0.3.4"
	home := "/Users/u"
	apps := home + "/Applications/ted.app"
	for _, c := range []struct {
		name    string
		exe     string
		bundles []string
		want    []string
	}{
		{"cli with ~/Applications link", keg + "/bin/ted", []string{apps, keg + "/ted.app"}, []string{apps}},
		{"cli without link", keg + "/bin/ted", []string{keg + "/ted.app"}, []string{keg + "/ted.app"}},
		{"launched from the keg bundle", keg + "/ted.app/Contents/MacOS/ted", []string{apps, keg + "/ted.app"}, []string{keg + "/ted.app", apps}},
		{"dev build, nothing installed", "/src/ted/ted", nil, nil},
	} {
		got := bundleCandidates(c.exe, home, func(p string) bool { return slices.Contains(c.bundles, p) })
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %q, want %q", c.name, got, c.want)
		}
	}
}
