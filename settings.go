package main

import (
	_ "embed"
	"fmt"
	"os/exec"
	"strings"
)

//go:embed packaging/ted.desktop
var desktopEntry string

type defaultsResult struct{ err error }

func (a *App) openSettings() {
	a.closeOverlay()
	a.confirm("Settings", []button{
		{"Make default", 'm', func() {
			if a.settingDefaults {
				return
			}
			a.settingDefaults = true
			a.setMsg("Setting file associations…")
			go func() { a.post(defaultsResult{setFileDefaults()}) }()
		}},
		{"Close", 'c', func() {}},
	})
	a.overlay.title = "Open text and code files in Ted by default."
}

func setFileDefaults() error {
	for _, line := range strings.Split(desktopEntry, "\n") {
		types, found := strings.CutPrefix(line, "MimeType=")
		if !found {
			continue
		}
		for _, mime := range strings.Split(strings.TrimSuffix(types, ";"), ";") {
			out, err := exec.Command("gio", "mime", mime, "ted.desktop").CombinedOutput()
			if err != nil {
				return fmt.Errorf("Could not set %s: %w %s", mime, err, strings.TrimSpace(string(out)))
			}
		}
		return nil
	}
	return fmt.Errorf("Ted's desktop entry lists no file types")
}
