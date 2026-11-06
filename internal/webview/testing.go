package webview

// cgo cannot be used from *_test.go files, so the widget probes the tests need
// live here as thin wrappers around webview.c.

/*
#include "webview.h"
*/
import "C"

import "unsafe"

func probeGtkInit() bool { return C.btcx_webview_gtk_init() != 0 }

func probeJavaScriptEnabled(v *View) bool {
	return C.btcx_webview_settings_javascript(ptr(v.w)) != 0
}

func probeHasFocus(v *View) bool {
	return C.btcx_webview_widget_has_focus(ptr(v.w)) != 0
}

func probeVisible(v *View) bool { return C.btcx_webview_widget_visible(ptr(v.w)) != 0 }

func probeParented(v *View) bool { return C.btcx_webview_widget_parent(ptr(v.w)) != 0 }

// probeTitle returns the document title WebKit currently reports ("" if the
// load has not produced one yet).
func probeTitle(v *View) string { return C.GoString(C.btcx_webview_title(ptr(v.w))) }

// probeShowWindow creates and presents a toplevel window with one vertical
// box, returning the box pointer (0 on failure). The window lives for the
// whole test process (see TestWidgets).
func probeShowWindow() uintptr { return uintptr(C.btcx_webview_test_show_window()) }

// probeLoadURI asks for a navigation the view must refuse.
func probeLoadURI(v *View, uri string) {
	c := C.CString(uri)
	defer C.free(unsafe.Pointer(c))
	C.btcx_webview_load_uri(ptr(v.w), c)
}

// probeIterate drains pending GTK main context events without blocking.
func probeIterate() { C.btcx_webview_iterate() }

func ptr(w *C.WebKitWebView) C.uintptr_t {
	return C.uintptr_t(uintptr(unsafe.Pointer(w))) // pointer stays valid for the duration of the call
}
