#ifndef BTCX_WEBVIEW_H
#define BTCX_WEBVIEW_H

#include <stdint.h>
#include <gtk/gtk.h>
#include <webkit/webkit.h>

/* NOTE: btcx_webview_link_clicked() is deliberately NOT declared here. cgo
 * generates its declaration in _cgo_export.h, which is included into the
 * preamble of the //export file; a second declaration with a different
 * const-qualification would be a conflicting-types error there. webview.c
 * declares it locally instead. */

/* Creates a hardened web view. The returned widget carries one reference that
 * the caller owns until btcx_webview_detach(); *handler_id receives the
 * "decide-policy" handler id. */
WebKitWebView *btcx_webview_new(guint64 handle, gulong *handler_id);

/* Appends the view to a GtkBox given by its raw pointer (GTK4 ownership
 * transfer, so the box owns it from here on) and makes it fill the pane. */
void btcx_webview_attach(uintptr_t box, WebKitWebView *view);

/* Disconnects the link handler, unparents and drops the caller's reference. */
void btcx_webview_detach(WebKitWebView *view, gulong handler_id);

/* Replaces the document; relative URLs resolve against about:blank. */
void btcx_webview_load(WebKitWebView *view, const char *html);

void btcx_webview_set_visible(WebKitWebView *view, gboolean visible);

/* ---- test probes (used from testing.go only) ---- */

gboolean btcx_webview_gtk_init(void);
gboolean btcx_webview_settings_javascript(uintptr_t view);
void btcx_webview_focus(WebKitWebView *view);
gboolean btcx_webview_widget_has_focus(uintptr_t view);
gboolean btcx_webview_widget_visible(uintptr_t view);
uintptr_t btcx_webview_widget_parent(uintptr_t view);
/* Borrowed string, valid while the view lives. */
const char *btcx_webview_title(uintptr_t view);
/* Asks for a foreign navigation; the view must ignore it. */
void btcx_webview_load_uri(uintptr_t view, const char *uri);
void btcx_webview_iterate(void);

/* Creates and presents a toplevel window holding one vertical box (returns the
 * box) so a web view can actually be realised and load a document. */
uintptr_t btcx_webview_test_show_window(void);

#endif
