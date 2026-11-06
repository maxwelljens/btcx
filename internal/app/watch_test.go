package app

import (
	"testing"
	"time"
)

func TestWatchDeliversNewMail(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)
	srv.addEnvelope("INBOX", 42, "Fresh mail", false, time.Now())
	srv.setWatch([]uint32{42})

	cfg := testConfig(t, testAccount("alice"))
	cfg.Watch = true
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	ev.wait(t, "new mail", func() bool { return len(ev.newMailRows()) == 1 })
	mail := ev.newMailRows()[0]
	if mail.Account != "alice" || mail.Row.Ref.UID != 42 {
		t.Fatalf("new mail = %+v, want alice uid 42", mail)
	}
	if mail.Row.Subject != "Fresh mail" {
		t.Errorf("row subject = %q", mail.Row.Subject)
	}

	ev.wait(t, "inbox refresh", func() bool {
		for _, r := range ev.messageRows("alice", "INBOX") {
			if r.Ref.UID == 42 {
				return true
			}
		}
		return false
	})

	c.Shutdown()
	ends := srv.watchEndCopy()
	if len(ends) == 0 || ends[0] == nil {
		t.Fatalf("watch context not cancelled: %v", ends)
	}
}
