//go:build !darwin

package main

import "errors"

// setHTMLDefault only makes sense on macOS, where file types are bound to
// apps through LaunchServices.
func setHTMLDefault(on bool) error {
	return errors.New("--set-html-default is only available on macOS")
}

func htmlDefaultIsTed() bool { return false }

func setAllDefaults() error { return errors.New("only available on macOS") }

func defaultsCount() (ted, total int) { return 0, 0 }

func lsRegister(string) error { return errors.New("only available on macOS") }
