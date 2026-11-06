// Package webview embeds WebKitGTK inside the gotk4 UI for rendering HTML
// e-mail bodies.
//
// This is a hand-rolled cgo package: gotk4 v0.4.1 ships no WebKit bindings, so
// the few needed entry points are called directly. It links against
// webkitgtk-6.0 (the GTK4 flavour). The GTK3 flavour, webkit2gtk-4.1, cannot
// be embedded in a GTK4 widget tree and is not used.
//
// # Security posture
//
// The view renders hostile mail, so it is configured to do as little as
// possible, and WebKit's defaults are not trusted:
//
//   - JavaScript, JavaScript markup execution, clipboard access and automatic
//     window opening are off.
//   - WebGL, WebRTC, media streams, Web Audio, MediaSource, encrypted media,
//     media playback and mock capture devices are off: mail must not run
//     code or drive hardware.
//   - Developer extras are off, console messages are not written to stdout,
//     and diagnostics that could leak message content to the terminal are not
//     enabled. Plugins no longer exist in WebKitGTK 6.0 (NPAPI was removed,
//     and there is no settings setter); media capture likewise has no setter
//     and is disabled through enable_media_stream.
//   - Modal dialogs, file access from file URLs, top-level navigation to data:
//     URLs and excessive diagnostics are off, and the same-origin policy is
//     never disabled.
//   - Navigation is refused: the "decide-policy" handler ignores every
//     decision and stops the signal, the single exception being the document
//     load this package itself started (recognised by a pending flag plus
//     navigation type OTHER, no user gesture, no redirect, URI exactly
//     about:blank — see webview.c). Link clicks are the only renderer-driven
//     navigation that is reported: they surface through the openURL callback
//     given to New, and the view never follows the target.
//   - Documents are loaded with an about:blank base URI, so relative URLs
//     resolve nowhere.
//   - No network requests are performed: render.WebHTML inlines every image
//     as a data: URI, so a loaded document has nothing external to fetch.
//     That guarantee is enforced upstream; this package relies on it.
//   - The WebKit web process is sandboxed by default on Linux (bubblewrap),
//     and the network process runs separately from the UI process.
//
// GTK must be initialised before New is called, and every method must be
// called from the GTK main thread. Only the link callback registry is
// mutex-guarded, because the C trampoline may run on any GTK thread.
package webview

/*
#cgo pkg-config: webkitgtk-6.0
#include <stdlib.h>
#include "webview.h"
*/
import "C"

import "unsafe"

// View is a hardened WebKitGTK web view for displaying sanitized HTML mail.
type View struct {
	w        *C.WebKitWebView
	handle   uint64
	sig      C.gulong
	attached bool
}

// New creates a view with JavaScript, plugins, developer extras and console
// output disabled. openURL is called (from the GTK main thread) when the user
// clicks a link; the view itself never navigates.
func New(openURL func(url string)) *View {
	v := &View{handle: registerCallback(openURL)}
	v.w = C.btcx_webview_new(C.guint64(v.handle), &v.sig)
	return v
}

// Attach inserts the view into the gotk4 Box given by its raw pointer
// (obtained via (*gtk.Box).Native()). The view takes over the widget: the
// caller must not also append it. Sets hexpand/vexpand so it fills the reader
// pane.
func (v *View) Attach(boxNative uintptr) {
	if v == nil || v.w == nil || boxNative == 0 || v.attached {
		return
	}
	C.btcx_webview_attach(C.uintptr_t(boxNative), v.w)
	v.attached = true
}

// Focus moves keyboard focus to the view. Message rebuilds call this so
// focus cannot fall onto the selectable header labels — a focused
// selectable GtkLabel highlights its entire text.
func (v *View) Focus() {
	if v == nil || v.w == nil {
		return
	}
	C.btcx_webview_focus(v.w)
}

// Load replaces the displayed document. html is a complete UTF-8 document (as
// produced by render.WebHTML) containing only data: URIs for images — the view
// performs NO network requests.
func (v *View) Load(html string) {
	if v == nil || v.w == nil {
		return
	}
	c := C.CString(html)
	defer C.free(unsafe.Pointer(c))
	C.btcx_webview_load(v.w, c)
}

// SetVisible toggles visibility (hide when showing plain text).
func (v *View) SetVisible(visible bool) {
	if v == nil || v.w == nil {
		return
	}
	C.btcx_webview_set_visible(v.w, gbool(visible))
}

// Detach removes the view from its parent (destroying the widget). Idempotent.
func (v *View) Detach() {
	if v == nil || v.w == nil {
		return
	}
	unregisterCallback(v.handle)
	C.btcx_webview_detach(v.w, v.sig)
	v.w = nil
	v.attached = false
}

// btcx_webview_link_clicked is the C-to-Go trampoline for decide-policy. It
// runs on the GTK main thread; it must not call back into GTK.
//
//export btcx_webview_link_clicked
func btcx_webview_link_clicked(handle C.guint64, uri *C.char) {
	if uri == nil {
		return
	}
	callCallback(uint64(handle), C.GoString(uri))
}

func gbool(b bool) C.gboolean {
	if b {
		return 1
	}
	return 0
}
