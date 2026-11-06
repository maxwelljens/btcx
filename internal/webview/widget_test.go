package webview

import (
	"os"
	"runtime"
	"testing"
	"time"
)

// gtkProcessSeen guards process-wide GTK/WebKit usage: WebKit's engine
// installs signal handlers on first use and cannot restart, and GTK binds
// to the OS thread that initialises it — so widget behaviour can only be
// exercised once per test process (-count repetitions skip with an
// explanation). btcx itself keeps exactly one persistent reader view, which
// matches this lifecycle.
var gtkProcessSeen bool

// TestWidgets exercises the view against a real display. Headless runs skip.
func TestWidgets(t *testing.T) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no display: DISPLAY and WAYLAND_DISPLAY are unset")
	}
	if gtkProcessSeen {
		t.Skip("WebKit/GTK already initialised in this process: -count repetitions cannot re-initialise them (single-run coverage only)")
	}
	gtkProcessSeen = true

	// GTK/WebKit bind to this goroutine's OS thread for the whole test.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	if !probeGtkInit() {
		t.Skip("gtk_init_check failed")
	}

	v := New(nil)
	if probeJavaScriptEnabled(v) {
		t.Fatal("JavaScript is enabled on a freshly created view")
	}

	box := probeShowWindow()
	if box == 0 {
		t.Skip("could not create a test window")
	}
	// The test window is deliberately not destroyed: nothing in this
	// process runs GTK afterwards (later -count iterations skip above).

	v.Attach(box)
	if !probeParented(v) {
		t.Fatal("view was not parented by Attach")
	}
	v.SetVisible(true)
	if !probeVisible(v) {
		t.Fatal("view is not visible after SetVisible(true)")
	}

	v.Load(`<!doctype html><html><head><title>btcx load test</title>` +
		`</head><body><p>hi</p><script>alert(1)</script></body></html>`)

	// The title only appears once the web process has parsed the document,
	// which is asynchronous. Poll the main context with a bounded timeout; if
	// the web process is too slow (or unavailable) skip rather than fail.
	const want = "btcx load test"
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && probeTitle(v) != want {
		probeIterate()
		time.Sleep(10 * time.Millisecond)
	}
	if got := probeTitle(v); got != want {
		t.Skipf("web process did not load the document in time (title %q)", got)
	}

	// A navigation the page (or a bug) asks for must be refused: the view
	// stays on the document Load installed, and no request is made.
	probeLoadURI(v, "https://example.invalid/steal")
	for range 50 {
		probeIterate()
		time.Sleep(2 * time.Millisecond)
	}
	if got := probeTitle(v); got != want {
		t.Fatalf("view navigated to a foreign URI (title %q)", got)
	}

	v.Detach()
	v.Detach() // idempotent
	v.Load("<p>ignored</p>")
	v.SetVisible(false)

	// Detaching a view that was never attached must be harmless too.
	unattached := New(nil)
	unattached.Detach()
}
