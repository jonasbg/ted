//go:build !darwin

package main

import "errors"

// setHTMLDefault only makes sense on macOS, where file types are bound to
// apps through LaunchServices.
func setHTMLDefault(on bool) error {
	return errors.New("--set-html-default is only available on macOS")
}
