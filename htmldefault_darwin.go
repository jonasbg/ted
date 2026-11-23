//go:build darwin

package main

/*
#cgo LDFLAGS: -framework CoreServices -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <CoreServices/CoreServices.h>
#include <stdlib.h>
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
	safariBundle = "com.apple.Safari"
)

// setHTMLDefault makes ted (on) or Safari (off) the default handler for
// HTML files, via the LaunchServices role-handler API.
func setHTMLDefault(on bool) error {
	utiC := C.CString(htmlUTI)
	bidC := C.CString(safariBundle)
	if on {
		t := bidC
		bidC = C.CString(tedBundle)
		C.free(unsafe.Pointer(t))
	}
	uti := C.CFStringCreateWithCString(C.kCFAllocatorDefault, utiC, C.kCFStringEncodingUTF8)
	bid := C.CFStringCreateWithCString(C.kCFAllocatorDefault, bidC, C.kCFStringEncodingUTF8)
	st := C.LSSetDefaultRoleHandlerForContentType(uti, C.LSRolesMask(C.kLSRolesAll), bid)
	C.CFRelease(C.CFTypeRef(uti))
	C.CFRelease(C.CFTypeRef(bid))
	C.free(unsafe.Pointer(utiC))
	C.free(unsafe.Pointer(bidC))
	if st != 0 {
		return fmt.Errorf("LaunchServices refused to change the HTML default (status %d)", st)
	}
	return nil
}

// htmlDefaultIsTed reports whether ted is the default app for HTML files.
func htmlDefaultIsTed() bool {
	utiC := C.CString(htmlUTI)
	defer C.free(unsafe.Pointer(utiC))
	uti := C.CFStringCreateWithCString(C.kCFAllocatorDefault, utiC, C.kCFStringEncodingUTF8)
	defer C.CFRelease(C.CFTypeRef(uti))
	h := C.LSCopyDefaultRoleHandlerForContentType(uti, C.LSRolesMask(C.kLSRolesAll))
	if h == 0 {
		return false
	}
	defer C.CFRelease(C.CFTypeRef(h))
	var buf [256]C.char
	if C.CFStringGetCString(h, &buf[0], C.CFIndex(len(buf)), C.kCFStringEncodingUTF8) == 0 {
		return false
	}
	return strings.EqualFold(C.GoString(&buf[0]), tedBundle) // LaunchServices may lower-case it
}
