package main

import (
	"os"
	"regexp"
	"slices"
	"testing"
)

// Every ending Info.plist declares a type for is listed on the settings
// page, so the list and what macOS hands to ted stay in step.
func TestFileTypesCoverPlist(t *testing.T) {
	plist, err := os.ReadFile("packaging/Info.plist")
	if err != nil {
		t.Fatal(err)
	}
	listed := append(defaultExts(), openWithOnly...)
	block := regexp.MustCompile(`(?s)<key>public\.filename-extension</key>\s*<array>(.*?)</array>`)
	ext := regexp.MustCompile(`<string>([^<]+)</string>`)
	n := 0
	for _, b := range block.FindAllSubmatch(plist, -1) {
		for _, m := range ext.FindAllSubmatch(b[1], -1) {
			n++
			if e := string(m[1]); !slices.Contains(listed, e) {
				t.Errorf(".%s is declared in Info.plist but not listed in fileTypes", e)
			}
		}
	}
	if n < 50 {
		t.Fatalf("found only %d declared endings in Info.plist", n)
	}
}

func TestDefaultUTIs(t *testing.T) {
	if !isMac {
		t.Skip("macOS only")
	}
	utis := defaultUTIs()
	if slices.Contains(utis, "public.html") {
		t.Error("Make default would take HTML from Safari")
	}
	for _, want := range []string{"public.data", "public.json", "public.c-source", "com.sun.java-source", "net.daringfireball.markdown"} {
		if !slices.Contains(utis, want) {
			t.Errorf("Make default misses %s", want)
		}
	}
}
