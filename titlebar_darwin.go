//go:build darwin

package main

/*
#cgo CFLAGS: -x objective-c -fobjc-arc
#cgo LDFLAGS: -framework AppKit
#import <AppKit/AppKit.h>
#include <stdlib.h>
#include <unistd.h>

// tedStyleWindow hides the title bar the way Ghostty and Zed do: the window
// keeps its traffic lights, but content extends underneath a transparent,
// title-less bar. The buttons are re-centred in a bar of barHeight points.
static void tedStyleWindow(CFTypeRef viewRef, CGFloat barHeight, int tedDebug, CGFloat r, CGFloat g, CGFloat b, int dark) {
	NSView *view = (__bridge NSView *)viewRef;
	dispatch_async(dispatch_get_main_queue(), ^{
		NSWindow *w = view.window;
		if (w == nil) {
			return;
		}
		w.titlebarAppearsTransparent = YES;
		w.titleVisibility = NSWindowTitleHidden;
		w.styleMask |= NSWindowStyleMaskFullSizeContentView;
		w.movableByWindowBackground = NO;
		// Match the theme so the bar, resize edges and traffic lights blend in.
		w.backgroundColor = [NSColor colorWithSRGBRed:r green:g blue:b alpha:1];
		w.appearance = [NSAppearance appearanceNamed:(dark ? NSAppearanceNameDarkAqua : NSAppearanceNameAqua)];
		if (w.styleMask & NSWindowStyleMaskFullScreen) {
			return; // AppKit manages the buttons in full screen
		}
		NSButton *btns[3] = {
			[w standardWindowButton:NSWindowCloseButton],
			[w standardWindowButton:NSWindowMiniaturizeButton],
			[w standardWindowButton:NSWindowZoomButton],
		};
		for (int i = 0; i < 3; i++) {
			NSButton *b = btns[i];
			if (b == nil || b.superview == nil) {
				continue;
			}
			b.hidden = NO; // Gio hides them for undecorated windows; we want them
			NSRect f = b.frame;
			CGFloat containerH = b.superview.frame.size.height;
			// centre the button within the top barHeight points of the window
			CGFloat fromTop = (barHeight - f.size.height) / 2;
			f.origin.y = containerH - fromTop - f.size.height;
			f.origin.x = 13 + i * 20;
			[b setFrameOrigin:f.origin];
		}
		if (tedDebug) {
			NSButton *c = btns[0];
			NSLog(@"ted titlebar: transparent=%d titleHidden=%d fullSize=%d close=%@ in %@ hidden=%d alpha=%.2f superHidden=%d",
				w.titlebarAppearsTransparent, w.titleVisibility == NSWindowTitleHidden,
				(w.styleMask & NSWindowStyleMaskFullSizeContentView) != 0,
				NSStringFromRect(c.frame), NSStringFromRect(c.superview.frame), c.hidden, c.alphaValue, c.superview.hidden);
			NSLog(@"ted view=%@ frame=%@ contentView=%@ frame=%@ window=%@ layoutRect=%@",
				[view class], NSStringFromRect(view.frame), [w.contentView class], NSStringFromRect(w.contentView.frame),
				NSStringFromRect(w.frame), NSStringFromRect(w.contentLayoutRect));
			for (NSView *v = c; v != nil; v = v.superview) {
				NSLog(@"ted  chain %@ hidden=%d alpha=%.2f frame=%@", [v class], v.hidden, v.alphaValue, NSStringFromRect(v.frame));
			}
		}
	});
}

// tedObserveLaunch writes a byte to fd when the app finishes launching.
// AppKit hands over the files it was opened with before that.
static void tedObserveLaunch(int fd) {
	[[NSNotificationCenter defaultCenter] addObserverForName:NSApplicationDidFinishLaunchingNotification
		object:nil queue:nil usingBlock:^(NSNotification *n) {
			char b = 1;
			write(fd, &b, 1);
			close(fd);
		}];
}

static void tedSetTitle(CFTypeRef viewRef, const char *title) {
	NSView *view = (__bridge NSView *)viewRef;
	NSString *t = [NSString stringWithUTF8String:title];
	dispatch_async(dispatch_get_main_queue(), ^{
		view.window.title = t;
	});
}
*/
import "C"

import (
	"os"
	"syscall"
	"unsafe"

	"gioui.org/app"
	"gioui.org/io/event"
)

const hiddenTitlebar = true

// trafficLightsDp is the width reserved at the top-left for the window buttons.
const trafficLightsDp = 78

func (a *App) platformEvent(e event.Event) {
	if v, ok := e.(app.AppKitViewEvent); ok && v.View != 0 {
		a.nativeView = v.View
		a.styleWindow()
	}
}

var tedDebug = C.int(map[bool]int{true: 1}[os.Getenv("TED_DEBUG") != ""])

func (a *App) styleWindow() {
	if a.nativeView != 0 && !a.nativeTitlebar {
		bg := a.th.bg
		dark := C.int(1)
		if a.th.light {
			dark = 0
		}
		C.tedStyleWindow(C.CFTypeRef(a.nativeView), C.CGFloat(titleBarDp), tedDebug,
			C.CGFloat(float64(bg.R)/255), C.CGFloat(float64(bg.G)/255), C.CGFloat(float64(bg.B)/255), dark)
	}
}

// windowOptions are extra options for the initial window. Gio must think the
// window is undecorated, or it draws its own fallback title bar and keeps
// resetting ours.
func windowOptions(native bool) []app.Option {
	if native {
		return nil
	}
	return []app.Option{app.Decorated(false)}
}

// setNativeTitle sets the window title without going through Gio's
// Configure, which would re-apply its decoration settings.
func (a *App) setNativeTitle(t string) bool {
	if a.nativeView == 0 || a.nativeTitlebar {
		return false
	}
	ct := C.CString(t)
	defer C.free(unsafe.Pointer(ct))
	C.tedSetTitle(C.CFTypeRef(a.nativeView), ct)
	return true
}

// appLaunched is closed once macOS has finished launching the app, by which
// time it has delivered any files the app was opened with. Call it before
// app.Events or app.Main.
func appLaunched() <-chan struct{} {
	r, w, err := os.Pipe()
	if err != nil {
		return nil
	}
	ch := make(chan struct{})
	go func() {
		r.Read(make([]byte, 1))
		r.Close()
		close(ch)
	}()
	// The C side owns the write end from here on.
	fd, err := syscall.Dup(int(w.Fd()))
	w.Close()
	if err != nil {
		return nil
	}
	C.tedObserveLaunch(C.int(fd))
	return ch
}
