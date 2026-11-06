package ui

import (
	"testing"

	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

// TestSendResultsUseTheirOwnWindow pins the send-id correlation: a result
// consumes its own compose window and nothing else. The failure path touches
// no widget, so placeholder windows suffice.
func TestSendResultsUseTheirOwnWindow(t *testing.T) {
	a := &app{}
	first, second := &ComposeWindow{}, &ComposeWindow{}
	a.trackSend(1, first)
	a.trackSend(2, second)

	ctx := glib.MainContextDefault()
	// wait runs the main loop until cond holds (the handlers marshal onto it).
	wait := func(what string, cond func() bool) {
		t.Helper()
		for i := 0; i < 1000; i++ {
			ctx.Iteration(false)
			if cond() {
				return
			}
		}
		t.Fatalf("timed out waiting for %s", what)
	}

	a.OnSendResult(1, false, "nope")
	wait("the send result", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		_, tracked := a.sends[1]
		return !tracked
	})

	a.mu.Lock()
	left := a.sends[2]
	n := len(a.sends)
	a.mu.Unlock()
	if left != second || n != 1 {
		t.Fatalf("send 1 result consumed the wrong window: %d tracked, %p", n, left)
	}

	// An unknown id is ignored and leaves the tracked window alone.
	a.OnSendResult(99, true, "")
	wait("the unknown send result", func() bool {
		a.mu.Lock()
		defer a.mu.Unlock()
		return len(a.sends) == 1
	})
}
