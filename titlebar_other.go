//go:build !darwin

package main

import (
	"gioui.org/app"
	"gioui.org/io/event"
)

const hiddenTitlebar = false
const trafficLightsDp = 0

func (a *App) platformEvent(e event.Event) {}
func (a *App) styleWindow()                {}

func windowOptions(native bool) []app.Option { return nil }
func (a *App) setNativeTitle(t string) bool  { return false }
