//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework CoreServices -framework CoreFoundation -framework Foundation -framework UniformTypeIdentifiers
#include <CoreFoundation/CoreFoundation.h>
#include <CoreServices/CoreServices.h>
#include <stdlib.h>
#import <Foundation/Foundation.h>
#import <UniformTypeIdentifiers/UniformTypeIdentifiers.h>

// tedUTIForExt is the type Finder gives files with this ending, or NULL.
static CFStringRef tedUTIForExt(const char *ext) {
	@autoreleasepool {
		UTType *t = [UTType typeWithFilenameExtension:[NSString stringWithUTF8String:ext]];
		if (t == nil) {
			return NULL;
		}
		return CFRetain((__bridge CFStringRef)t.identifier);
	}
}
*/
import "C"

import (
	"fmt"
	"strings"
	"unsafe"
)

const (
	tedBundle    = "io.github.jonasbg.ted"
	htmlUTI      = "public.html"
	dataUTI      = "public.data" // what macOS calls files with no ending
	safariBundle = "com.apple.Safari"
)

func cfString(s string) C.CFStringRef {
	cs := C.CString(s)
	defer C.free(unsafe.Pointer(cs))
	return C.CFStringCreateWithCString(C.kCFAllocatorDefault, cs, C.kCFStringEncodingUTF8)
}

func goString(s C.CFStringRef) string {
	var buf [512]C.char
	if C.CFStringGetCString(s, &buf[0], C.CFIndex(len(buf)), C.kCFStringEncodingUTF8) == 0 {
		return ""
	}
	return C.GoString(&buf[0])
}

// setDefaultHandler makes bundle the default app for a content type.
func setDefaultHandler(uti, bundle string) error {
	u, b := cfString(uti), cfString(bundle)
	defer C.CFRelease(C.CFTypeRef(u))
	defer C.CFRelease(C.CFTypeRef(b))
	if st := C.LSSetDefaultRoleHandlerForContentType(u, C.LSRolesMask(C.kLSRolesAll), b); st != 0 {
		return fmt.Errorf("LaunchServices refused to change the default for %s (status %d)", uti, st)
	}
	return nil
}

// defaultIsTed reports whether ted is the default app for a content type.
func defaultIsTed(uti string) bool {
	u := cfString(uti)
	defer C.CFRelease(C.CFTypeRef(u))
	h := C.LSCopyDefaultRoleHandlerForContentType(u, C.LSRolesMask(C.kLSRolesAll))
	if h == 0 {
		return false
	}
	defer C.CFRelease(C.CFTypeRef(h))
	return strings.EqualFold(goString(h), tedBundle) // LaunchServices may lower-case it
}

// utiForExt is the content type macOS gives files with this ending.
func utiForExt(ext string) string {
	cs := C.CString(ext)
	defer C.free(unsafe.Pointer(cs))
	u := C.tedUTIForExt(cs)
	if u == 0 {
		return ""
	}
	defer C.CFRelease(C.CFTypeRef(u))
	return goString(u)
}

// defaultUTIs are the content types "Make default" binds: one per ending in
// fileTypes, plus public.data for Dockerfiles and dotfiles. HTML never.
func defaultUTIs() []string {
	seen := map[string]bool{htmlUTI: true}
	var utis []string
	for _, ext := range append(defaultExts(), "") {
		uti := dataUTI
		if ext != "" {
			uti = utiForExt(ext)
		}
		if uti == "" || seen[uti] {
			continue
		}
		seen[uti] = true
		utis = append(utis, uti)
	}
	return utis
}

// setAllDefaults makes ted the default app for every supported file type
// except HTML.
func setAllDefaults() error {
	for _, uti := range defaultUTIs() {
		if err := setDefaultHandler(uti, tedBundle); err != nil {
			return err
		}
	}
	return nil
}

// defaultsCount is how many of the supported file types open in ted.
func defaultsCount() (ted, total int) {
	utis := defaultUTIs()
	for _, uti := range utis {
		if defaultIsTed(uti) {
			ted++
		}
	}
	return ted, len(utis)
}

// setHTMLDefault makes ted (on) or Safari (off) the default handler for
// HTML files, via the LaunchServices role-handler API.
func setHTMLDefault(on bool) error {
	bundle := safariBundle
	if on {
		bundle = tedBundle
	}
	if err := setDefaultHandler(htmlUTI, bundle); err != nil {
		return fmt.Errorf("LaunchServices refused to change the HTML default: %w", err)
	}
	return nil
}

// htmlDefaultIsTed reports whether ted is the default app for HTML files.
func htmlDefaultIsTed() bool { return defaultIsTed(htmlUTI) }
