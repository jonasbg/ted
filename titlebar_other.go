//go:build !darwin

package main

import (
	"os"

	"gioui.org/app"
	"gioui.org/io/event"
)

const hiddenTitlebar = false
const trafficLightsDp = 0

func (a *App) platformEvent(e event.Event) {}
func (a *App) styleWindow()                {}

// windowOptions: tiling compositors such as Hyprland (Omarchy) draw no
// title bars; ask for none, or Gio would draw its own fallback bar.
func windowOptions(native bool) []app.Option {
	if !native && os.Getenv("HYPRLAND_INSTANCE_SIGNATURE") != "" {
		return []app.Option{app.Decorated(false)}
	}
	return nil
}

func (a *App) setNativeTitle(t string) bool { return false }

// appLaunched is a macOS launch signal; elsewhere it never fires.
func appLaunched() <-chan struct{} { return nil }
