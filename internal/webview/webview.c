#include "webview.h"

#include <stdint.h>
#include <string.h>

/* Defined by cgo in the //export file. Types are ABI-compatible with cgo's
 * generated declaration (guint64 == uint64_t on LP64). */
extern void btcx_webview_link_clicked(uint64_t handle, const char *uri);

/* Set while btcx_webview_load() is putting a document into the view. */
#define BTCX_ALLOW_LOAD_KEY "btcx-allow-load"

/* decide-policy: nothing navigates except the view's own document load.
 *
 * load_html() itself goes through this signal (type NAVIGATION_ACTION,
 * navigation type OTHER, no user gesture, URI about:blank), so an unconditional
 * ignore would leave the reader pane blank forever. The load we started is
 * therefore allowed once — recognised by the pending flag set in
 * btcx_webview_load() plus all of: navigation type OTHER, no user gesture, not
 * a redirect, and exactly about:blank. Everything else — link clicks (reported
 * to Go instead), form submits, history moves, redirects, foreign URIs — is
 * ignored. TRUE stops further handlers. */
static gboolean
btcx_webview_decide_policy(WebKitWebView *view, WebKitPolicyDecision *decision,
                           WebKitPolicyDecisionType type, gpointer user_data)
{
    guint64 handle = (guint64)(uintptr_t)user_data;
    WebKitNavigationAction *action = NULL;

    if (type == WEBKIT_POLICY_DECISION_TYPE_NAVIGATION_ACTION ||
        type == WEBKIT_POLICY_DECISION_TYPE_NEW_WINDOW_ACTION) {
        action = webkit_navigation_policy_decision_get_navigation_action(
            WEBKIT_NAVIGATION_POLICY_DECISION(decision));
    }

    if (action && g_object_get_data(G_OBJECT(view), BTCX_ALLOW_LOAD_KEY)) {
        WebKitURIRequest *request = webkit_navigation_action_get_request(action);
        const char *uri = request ? webkit_uri_request_get_uri(request) : NULL;

        if (webkit_navigation_action_get_navigation_type(action) == WEBKIT_NAVIGATION_TYPE_OTHER &&
            !webkit_navigation_action_is_user_gesture(action) &&
            !webkit_navigation_action_is_redirect(action) &&
            uri && strcmp(uri, "about:blank") == 0) {
            g_object_set_data(G_OBJECT(view), BTCX_ALLOW_LOAD_KEY, NULL);
            webkit_policy_decision_use(decision);
            return TRUE;
        }
    }

    webkit_policy_decision_ignore(decision);

    /* Link clicks are the only navigation the UI cares about; they leave via
     * the Go registry callback. Redirects and everything else report nothing. */
    if (action &&
        webkit_navigation_action_get_navigation_type(action) ==
            WEBKIT_NAVIGATION_TYPE_LINK_CLICKED) {
        WebKitURIRequest *request = webkit_navigation_action_get_request(action);
        const char *uri = request ? webkit_uri_request_get_uri(request) : NULL;
        if (uri)
            btcx_webview_link_clicked(handle, uri);
    }
    return TRUE;
}

/* Turns off everything that can execute code, reach the network on its own or
 * pop up UI. Only settings that exist in WebKitGTK 6.0 are touched; plugins
 * and media capture have no setter any more (NPAPI is gone, capture requires
 * enable_media_stream, which is off). */
static void
btcx_webview_harden(WebKitSettings *s)
{
    webkit_settings_set_enable_javascript(s, FALSE);
    webkit_settings_set_enable_javascript_markup(s, FALSE);
    webkit_settings_set_javascript_can_open_windows_automatically(s, FALSE);
    webkit_settings_set_javascript_can_access_clipboard(s, FALSE);
    webkit_settings_set_enable_write_console_messages_to_stdout(s, FALSE);
    webkit_settings_set_enable_developer_extras(s, FALSE);

    webkit_settings_set_enable_webaudio(s, FALSE);
    webkit_settings_set_enable_webgl(s, FALSE);
    webkit_settings_set_enable_webrtc(s, FALSE);
    webkit_settings_set_enable_media_stream(s, FALSE);
    webkit_settings_set_enable_mediasource(s, FALSE);
    webkit_settings_set_enable_encrypted_media(s, FALSE);
    webkit_settings_set_enable_media(s, FALSE);
    webkit_settings_set_enable_mock_capture_devices(s, FALSE);

    webkit_settings_set_allow_modal_dialogs(s, FALSE);
    webkit_settings_set_allow_top_navigation_to_data_urls(s, FALSE);
    webkit_settings_set_allow_universal_access_from_file_urls(s, FALSE);
    webkit_settings_set_allow_file_access_from_file_urls(s, FALSE);

    /* Explicitly written although it is the default: flipping it would disable
     * the same-origin policy, so keep it visible and locked down. */
    webkit_settings_set_disable_web_security(s, FALSE);
}

WebKitWebView *
btcx_webview_new(guint64 handle, gulong *handler_id)
{
    WebKitWebView *view = WEBKIT_WEB_VIEW(webkit_web_view_new());

    /* The widget arrives floating. Take our own reference so it survives
     * Detach() being called after its parent was destroyed: the box drops its
     * reference on unparent, ours goes last. */
    g_object_ref_sink(view);

    btcx_webview_harden(webkit_web_view_get_settings(view));

    *handler_id = g_signal_connect(view, "decide-policy",
                                   G_CALLBACK(btcx_webview_decide_policy),
                                   (gpointer)(uintptr_t)handle);
    return view;
}

void
btcx_webview_attach(uintptr_t box, WebKitWebView *view)
{
    gtk_widget_set_hexpand(GTK_WIDGET(view), TRUE);
    gtk_widget_set_vexpand(GTK_WIDGET(view), TRUE);
    gtk_box_append(GTK_BOX((GtkWidget *)(uintptr_t)box), GTK_WIDGET(view));
}

void
btcx_webview_detach(WebKitWebView *view, gulong handler_id)
{
    g_signal_handler_disconnect(view, handler_id);
    gtk_widget_unparent(GTK_WIDGET(view));
    g_object_unref(view);
}

void
btcx_webview_load(WebKitWebView *view, const char *html)
{
    /* about:blank: relative URLs must resolve nowhere. The decide-policy
     * handler lets exactly this one load through. */
    g_object_set_data(G_OBJECT(view), BTCX_ALLOW_LOAD_KEY, GINT_TO_POINTER(1));
    webkit_web_view_load_html(view, html, "about:blank");
}

void
btcx_webview_set_visible(WebKitWebView *view, gboolean visible)
{
    gtk_widget_set_visible(GTK_WIDGET(view), visible);
}

/* ---- test probes ---- */

gboolean
btcx_webview_gtk_init(void)
{
    return gtk_init_check();
}

gboolean
btcx_webview_settings_javascript(uintptr_t view)
{
    return webkit_settings_get_enable_javascript(
        webkit_web_view_get_settings(WEBKIT_WEB_VIEW((GtkWidget *)(uintptr_t)view)));
}

void
btcx_webview_focus(WebKitWebView *view)
{
    if (view)
        gtk_widget_grab_focus(GTK_WIDGET(view));
}

gboolean
btcx_webview_widget_has_focus(uintptr_t view)
{
    GtkWidget *widget = GTK_WIDGET((GtkWidget *)(uintptr_t)view);
    GtkRoot *root = gtk_widget_get_root(widget);
    if (root == NULL || !GTK_IS_WINDOW(root))
        return gtk_widget_has_focus(widget);
    /* The window manager decides which toplevel has the desktop focus;
       what Focus() must achieve is becoming the window's focus widget. */
    return gtk_window_get_focus(GTK_WINDOW(root)) == widget;
}

gboolean
btcx_webview_widget_visible(uintptr_t view)
{
    return gtk_widget_get_visible(GTK_WIDGET((GtkWidget *)(uintptr_t)view));
}

uintptr_t
btcx_webview_widget_parent(uintptr_t view)
{
    return (uintptr_t)gtk_widget_get_parent(GTK_WIDGET((GtkWidget *)(uintptr_t)view));
}

const char *
btcx_webview_title(uintptr_t view)
{
    /* Borrowed: copied into Go by C.GoString on the caller's side. */
    return webkit_web_view_get_title(WEBKIT_WEB_VIEW((GtkWidget *)(uintptr_t)view));
}

void
btcx_webview_load_uri(uintptr_t view, const char *uri)
{
    webkit_web_view_load_uri(WEBKIT_WEB_VIEW((GtkWidget *)(uintptr_t)view), uri);
}

void
btcx_webview_iterate(void)
{
    while (g_main_context_iteration(NULL, FALSE))
        ;
}

static GtkWidget *test_window;

uintptr_t
btcx_webview_test_show_window(void)
{
    GtkWidget *box;

    test_window = gtk_window_new();
    gtk_window_set_default_size(GTK_WINDOW(test_window), 400, 300);
    box = gtk_box_new(GTK_ORIENTATION_VERTICAL, 0);
    gtk_window_set_child(GTK_WINDOW(test_window), box);
    gtk_window_present(GTK_WINDOW(test_window));
    return (uintptr_t)box;
}
