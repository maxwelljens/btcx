package ui

/*
#cgo pkg-config: gtk4
#include <gdk/x11/gdkx.h>
*/
import "C"

import (
	"unsafe"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// xidOf returns the X11 window id of w under the X11 GDK backend, or 0 on
// Wayland or when the surface is not realized yet. It exists for the
// preview screenshot harness (scripts/preview.sh), which captures each
// window by id.
func xidOf(w *gtk.Window) uintptr {
	if w == nil {
		return 0
	}
	native := w.Widget.Native()
	if native == nil {
		return 0
	}
	surf := native.Surface()
	if surf == nil {
		return 0
	}
	// Native() hands back the GdkSurface's C address as a uintptr; the
	// cast feeds it straight back to C in the same expression and never
	// dereferences Go memory.
	return uintptr(C.gdk_x11_surface_get_xid((*C.GdkSurface)(unsafe.Pointer( //nolint:govet // uintptr holds a C pointer, cast back in the same expression
		coreglib.BaseObject(surf).Native()))))
}
