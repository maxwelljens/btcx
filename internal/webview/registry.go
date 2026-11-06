package webview

import (
	"sync"
	"sync/atomic"
)

// Link callbacks live in a small mutex-guarded registry instead of a
// cgo.Handle: the callbacks are cheap to look up, plain Go code, and testable
// without touching C. Each view owns one handle for its lifetime.
var (
	cbMu    sync.Mutex
	cbFuncs = make(map[uint64]func(string))
	cbSeq   atomic.Uint64
)

// registerCallback stores fn and returns its handle. A nil fn is stored as a
// no-op so that handle accounting stays uniform.
func registerCallback(fn func(string)) uint64 {
	if fn == nil {
		fn = func(string) {}
	}
	h := cbSeq.Add(1)
	cbMu.Lock()
	cbFuncs[h] = fn
	cbMu.Unlock()
	return h
}

// callCallback invokes the callback for h, if any. The function is copied out
// under the lock so a callback may itself call Detach/New without deadlocking.
func callCallback(h uint64, uri string) {
	cbMu.Lock()
	fn := cbFuncs[h]
	cbMu.Unlock()
	if fn != nil {
		fn(uri)
	}
}

// unregisterCallback drops the callback for h. Unknown handles are ignored.
func unregisterCallback(h uint64) {
	cbMu.Lock()
	delete(cbFuncs, h)
	cbMu.Unlock()
}

// registeredCallbacks reports the number of live handles (test helper).
func registeredCallbacks() int {
	cbMu.Lock()
	defer cbMu.Unlock()
	return len(cbFuncs)
}
