package webview

import (
	"sync"
	"testing"
)

func TestRegisterCallbackLifecycle(t *testing.T) {
	before := registeredCallbacks()

	var got []string
	h := registerCallback(func(u string) { got = append(got, u) })
	if registeredCallbacks() != before+1 {
		t.Fatalf("registry size = %d, want %d", registeredCallbacks(), before+1)
	}
	if h == 0 {
		t.Fatal("handle is zero; handles must never be zero")
	}
	callCallback(h, "https://example.test/a")
	if len(got) != 1 || got[0] != "https://example.test/a" {
		t.Fatalf("callback got %v", got)
	}
	unregisterCallback(h)
	if registeredCallbacks() != before {
		t.Fatalf("registry size = %d after detach, want %d", registeredCallbacks(), before)
	}
	// A detached handle is inert, and unregistering twice is harmless.
	callCallback(h, "https://example.test/b")
	if len(got) != 1 {
		t.Fatalf("callback ran after unregister: %v", got)
	}
	unregisterCallback(h)
}

func TestRegisterCallbackUniqueHandles(t *testing.T) {
	seen := make(map[uint64]bool)
	for range 100 {
		h := registerCallback(nil)
		if seen[h] {
			t.Fatalf("duplicate handle %d", h)
		}
		seen[h] = true
		unregisterCallback(h)
	}
}

func TestNilCallbackIsNoop(t *testing.T) {
	h := registerCallback(nil)
	callCallback(h, "about:blank") // must not panic
	unregisterCallback(h)
}

// A callback that detaches itself must not deadlock: the registry copies the
// function out before invoking it.
func TestCallbackMayReenterRegistry(t *testing.T) {
	var h uint64
	h = registerCallback(func(string) { unregisterCallback(h) })
	callCallback(h, "x")
	if registeredCallbacks() != 0 {
		t.Fatalf("registry size = %d, want 0", registeredCallbacks())
	}
}

func TestRegistryConcurrent(t *testing.T) {
	const n = 50
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(1)
		go func() {
			defer wg.Done()
			h := registerCallback(func(string) {})
			if i%2 == 0 {
				callCallback(h, "u")
			}
			unregisterCallback(h)
		}()
	}
	wg.Wait()
	if registeredCallbacks() != 0 {
		t.Fatalf("registry size = %d after concurrent use, want 0", registeredCallbacks())
	}
}
