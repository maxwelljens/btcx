package app

import (
	"sync"
	"testing"

	"btcx/internal/model"
)

// TestConcurrentOperations is a race-detector smoke test: opening messages,
// changing flags and refreshing all touch the controller state from several
// goroutines at once.
func TestConcurrentOperations(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.addBox("Sent", `\Sent`)
	srv.seed("INBOX", 20)
	for uid := uint32(1); uid <= 20; uid++ {
		srv.setRaw(model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: uid}, threadMessage)
	}

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")
	ev.wait(t, "rows", func() bool { return len(ev.messageRows("alice", "INBOX")) == 20 })

	ref := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 5}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 5; j++ {
				c.OpenMessage("alice", ref)
				c.MarkSeen("alice", ref, true)
				c.SetFlagged("alice", ref, j%2 == 0)
				switch i % 3 {
				case 0:
					c.Refresh("alice")
				case 1:
					c.SelectFolder("alice", "INBOX")
				case 2:
					c.Search("alice", "INBOX", "missing")
				}
			}
		}(i)
	}
	wg.Wait()

	ev.wait(t, "message opened", func() bool { _, ok := ev.openedView(ref); return ok })

	c.Shutdown()
}
