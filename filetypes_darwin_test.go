//go:build darwin

package main

import (
	"slices"
	"testing"
)

func TestDefaultUTIs(t *testing.T) {
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
