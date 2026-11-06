package app

import (
	"strings"
	"testing"
	"time"

	"btcx/internal/contract"
	"btcx/internal/model"
)

// replyDraft starts a reply and returns the draft its OnDraftReady delivered.
func replyDraft(t *testing.T, c *Controller, ev *recEvents, account string, ref model.Ref, mode contract.ReplyMode) contract.ComposeDraft {
	t.Helper()
	before := len(ev.draftResults())
	id := c.StartReply(account, ref, mode)
	ev.wait(t, "reply draft", func() bool { return len(ev.draftResults()) > before })
	got := ev.draftResults()[before]
	if got.ID != id {
		t.Fatalf("draft id = %d, want %d", got.ID, id)
	}
	return got.Draft
}

// forwardDraft starts a forward and returns the draft OnDraftReady delivered.
func forwardDraft(t *testing.T, c *Controller, ev *recEvents, account string, ref model.Ref) contract.ComposeDraft {
	t.Helper()
	before := len(ev.draftResults())
	id := c.StartForward(account, ref)
	ev.wait(t, "forward draft", func() bool { return len(ev.draftResults()) > before })
	got := ev.draftResults()[before]
	if got.ID != id {
		t.Fatalf("draft id = %d, want %d", got.ID, id)
	}
	return got.Draft
}

// TestDraftsArriveOncePerRequest pins the async draft contract: every
// StartReply/StartForward gets its own id and exactly one OnDraftReady.
func TestDraftsArriveOncePerRequest(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)
	ref := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 1}
	srv.setRaw(ref, threadMessage)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	reply := replyDraft(t, c, ev, "alice", ref, contract.ReplyDirect)
	if reply.ReplyTo != ref || !strings.Contains(reply.Plain, "> original body line") {
		t.Errorf("reply draft lost its threading or quote: %+v", reply)
	}
	if fwd := forwardDraft(t, c, ev, "alice", ref); fwd.Subject != "Fwd: Original subject" {
		t.Errorf("forward subject = %q", fwd.Subject)
	}

	// Two requests in flight at once must not share an id.
	ids := []contract.EditRequestID{
		c.StartReply("alice", ref, contract.ReplyDirect),
		c.StartReply("alice", ref, contract.ReplyAll),
	}
	if ids[0] == ids[1] {
		t.Fatalf("concurrent drafts share id %d", ids[0])
	}
	ev.wait(t, "concurrent drafts", func() bool { return len(ev.draftResults()) == 4 })
	time.Sleep(50 * time.Millisecond) // a duplicate delivery has to show up by now
	if got := len(ev.draftResults()); got != 4 {
		t.Fatalf("draft deliveries = %d, want 4", got)
	}
	seen := map[contract.EditRequestID]bool{}
	for _, d := range ev.draftResults() {
		seen[d.ID] = true
	}
	for _, id := range ids {
		if !seen[id] {
			t.Errorf("no draft delivered for id %d", id)
		}
	}
}

// TestStartReplyUnknownAccountStillDeliversDraft pins that a failed draft
// request reports the error and still answers, so the UI never waits.
func TestStartReplyUnknownAccountStillDeliversDraft(t *testing.T) {
	c, ev, _ := newTestController(t, testConfig(t), newFakeServer())

	id := c.StartReply("nobody", model.Ref{Mailbox: "INBOX", UID: 1}, contract.ReplyDirect)
	ev.wait(t, "draft for unknown account", func() bool { return len(ev.draftResults()) == 1 })
	got := ev.draftResults()[0]
	if got.ID != id || got.Draft.Account != "nobody" {
		t.Errorf("draft = %+v, want id %d for account nobody", got, id)
	}
	if !strings.Contains(ev.errText(), "unknown account") {
		t.Errorf("errors = %q, want the unknown-account failure", ev.errText())
	}
}

// TestSendResultsCarryTheirID pins the send correlation: two attempts get
// distinct ids and each result names the attempt it belongs to.
func TestSendResultsCarryTheirID(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.addBox("Sent", `\Sent`)
	srv.seed("INBOX", 1)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	draft := contract.ComposeDraft{
		Account: "alice",
		To:      []model.Address{{Addr: "bob@example.org"}},
		Subject: "one",
		Plain:   "Body",
	}
	first := c.Send(draft)
	draft.Subject = "two"
	second := c.Send(draft)
	if first == second {
		t.Fatalf("two sends share id %d", first)
	}

	ev.wait(t, "both send results", func() bool { return len(ev.sendResults()) == 2 })
	ids := map[contract.SendID]bool{}
	for _, r := range ev.sendResults() {
		if !r.Success {
			t.Fatalf("send %d failed: %s", r.ID, r.Message)
		}
		ids[r.ID] = true
	}
	if !ids[first] || !ids[second] {
		t.Errorf("results name ids %v, want %d and %d", ids, first, second)
	}

	// A rejected attempt still answers with the id it was given.
	unknown := c.Send(contract.ComposeDraft{Account: "nobody", To: draft.To})
	ev.wait(t, "unknown account result", func() bool { return len(ev.sendResults()) == 3 })
	last := ev.sendResults()[2]
	if last.ID != unknown || last.Success {
		t.Errorf("result = %+v, want a failure for id %d", last, unknown)
	}
}
