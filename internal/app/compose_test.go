package app

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"btcx/internal/contract"
	"btcx/internal/model"
)

func TestWithSignature(t *testing.T) {
	tests := []struct {
		name string
		body string
		sig  string
		want string
	}{
		{"no signature", "Body", "", "Body"},
		{"appended once", "Body", "Regards", "Body\n\n-- \nRegards"},
		{"empty body", "", "Regards", "-- \nRegards"},
		{"trailing newlines trimmed", "Body\n\n", "Regards", "Body\n\n-- \nRegards"},
		{"not duplicated", "Body\n\n-- \nRegards", "Regards", "Body\n\n-- \nRegards"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := withSignature(tt.body, tt.sig); got != tt.want {
				t.Errorf("withSignature(%q, %q) = %q, want %q", tt.body, tt.sig, got, tt.want)
			}
		})
	}
}

func TestSendHappyPath(t *testing.T) {
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
		Cc:      []model.Address{{Addr: "carol@example.org"}},
		Bcc:     []model.Address{{Addr: "dave@example.org"}},
		Subject: "Hi",
		Plain:   "Body",
	}
	c.Send(draft)
	ev.wait(t, "send result", func() bool { return len(ev.sendResults()) == 1 })

	results := ev.sendResults()
	if !results[0].Success {
		t.Fatalf("send failed: %s", results[0].Message)
	}
	if results[0].Message != "Message sent" {
		t.Errorf("message = %q", results[0].Message)
	}

	sends := srv.sendsCopy()
	if len(sends) != 1 {
		t.Fatalf("connector sends = %d, want 1", len(sends))
	}
	raw := string(sends[0].Raw)
	for _, want := range []string{"To: bob@example.org", "Cc: carol@example.org", "Subject: Hi", "Regards,"} {
		if !strings.Contains(raw, want) {
			t.Errorf("raw message missing %q:\n%s", want, raw)
		}
	}
	if strings.Contains(raw, "Bcc") {
		t.Errorf("raw message leaks Bcc recipients:\n%s", raw)
	}
	if len(sends[0].Rcpts) != 3 {
		t.Fatalf("rcpts = %+v, want To+Cc+Bcc", sends[0].Rcpts)
	}
	if sends[0].Rcpts[2].Addr != "dave@example.org" {
		t.Errorf("Bcc recipient missing from envelope: %+v", sends[0].Rcpts)
	}

	appends := srv.appendsCopy()
	if len(appends) != 1 || appends[0].Mailbox != "Sent" || !appends[0].Flags.Seen {
		t.Fatalf("sent copy = %+v, want an append to Sent", appends)
	}
}

func TestSendVetoedByScript(t *testing.T) {
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "veto.lua"), `
function before_send(message)
  return false
end
`)
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)

	cfg := testConfig(t, testAccount("alice"))
	cfg.ScriptDir = dir
	cfg.ScriptEnabled = true
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	c.Send(contract.ComposeDraft{
		Account: "alice",
		To:      []model.Address{{Addr: "bob@example.org"}},
		Subject: "Hi",
		Plain:   "Body",
	})
	ev.wait(t, "veto result", func() bool { return len(ev.sendResults()) == 1 })

	result := ev.sendResults()[0]
	if result.Success {
		t.Fatal("script vetoed the send but it succeeded")
	}
	if !strings.Contains(result.Message, "cancelled by a script") {
		t.Errorf("message = %q, want the veto reason", result.Message)
	}
	if got := len(srv.sendsCopy()); got != 0 {
		t.Errorf("connector.Send called %d times after veto", got)
	}
	if got := len(srv.appendsCopy()); got != 0 {
		t.Errorf("draft appended to Sent after veto: %+v", srv.appendsCopy())
	}
}

func TestSendFailureReported(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, conn := newTestController(t, cfg, srv)
	conn.sendErr = errors.New("smtp: 550 relay denied")
	startApp(t, c, ev, "alice")

	c.Send(contract.ComposeDraft{
		Account: "alice",
		To:      []model.Address{{Addr: "bob@example.org"}},
		Subject: "Hi",
		Plain:   "Body",
	})
	ev.wait(t, "send result", func() bool { return len(ev.sendResults()) == 1 })

	result := ev.sendResults()[0]
	if result.Success {
		t.Fatal("failed send reported as success")
	}
	if !strings.Contains(result.Message, "550 relay denied") {
		t.Errorf("message = %q, want the SMTP error", result.Message)
	}
	if got := len(srv.appendsCopy()); got != 0 {
		t.Errorf("failed send appended a Sent copy: %+v", srv.appendsCopy())
	}
}

func TestSendRejectsEmptyRecipients(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	c.Send(contract.ComposeDraft{Account: "alice", Subject: "Hi", Plain: "Body"})
	ev.wait(t, "send result", func() bool { return len(ev.sendResults()) == 1 })
	if ev.sendResults()[0].Success {
		t.Fatal("send without recipients succeeded")
	}
	if got := len(srv.sendsCopy()); got != 0 {
		t.Errorf("connector called without recipients")
	}
}

func TestSaveDraft(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.addBox("Drafts", `\Drafts`)
	srv.seed("INBOX", 1)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	c.SaveDraft(contract.ComposeDraft{
		Account: "alice",
		To:      []model.Address{{Addr: "bob@example.org"}},
		Bcc:     []model.Address{{Addr: "dave@example.org"}},
		Subject: "Draft",
		Plain:   "Body",
	})
	ev.wait(t, "draft status", func() bool {
		for _, s := range ev.statusList() {
			if s == "Draft saved" {
				return true
			}
		}
		return false
	})

	appends := srv.appendsCopy()
	if len(appends) != 1 || appends[0].Mailbox != "Drafts" || !appends[0].Flags.Draft {
		t.Fatalf("appends = %+v, want one Draft-flagged append", appends)
	}
	if !strings.Contains(string(appends[0].Raw), "Bcc: dave@example.org") {
		t.Errorf("saved draft lost its Bcc field:\n%s", appends[0].Raw)
	}
}

const threadMessage = "From: Bob <bob@example.org>\r\n" +
	"To: Alice <alice@example.org>\r\n" +
	"Cc: Carol <carol@example.org>\r\n" +
	"Subject: Original subject\r\n" +
	"Date: Mon, 2 Jan 2026 15:04:05 -0700\r\n" +
	"Message-ID: <orig@example.org>\r\n" +
	"References: <root@example.org>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"original body line\r\n"

func TestComposeBuilders(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)
	ref := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 1}
	srv.setRaw(ref, threadMessage)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	if got := c.NewCompose("alice"); got.Account != "alice" || got.Plain != "" || got.ReplyTo != (model.Ref{}) {
		t.Errorf("NewCompose = %+v", got)
	}

	reply := c.ReplyTo("alice", ref, false)
	if reply.ReplyTo != ref {
		t.Errorf("ReplyTo ref = %v, want %v", reply.ReplyTo, ref)
	}
	if reply.Subject != "Re: Original subject" {
		t.Errorf("reply subject = %q", reply.Subject)
	}
	if len(reply.To) != 1 || reply.To[0].Addr != "bob@example.org" {
		t.Errorf("reply To = %+v", reply.To)
	}
	if !strings.Contains(reply.Plain, "> original body line") {
		t.Errorf("reply body is not quoted: %q", reply.Plain)
	}

	all := c.ReplyTo("alice", ref, true)
	var to []string
	for _, a := range all.To {
		to = append(to, a.Addr)
	}
	joined := strings.Join(to, ",")
	if !strings.Contains(joined, "bob@example.org") || !strings.Contains(joined, "carol@example.org") {
		t.Errorf("reply-all To = %q, want bob and carol", joined)
	}
	if strings.Contains(joined, "alice@example.org") {
		t.Errorf("reply-all includes the account itself: %q", joined)
	}

	fwd := c.Forward("alice", ref)
	if fwd.Subject != "Fwd: Original subject" {
		t.Errorf("forward subject = %q", fwd.Subject)
	}
	if fwd.ReplyTo != (model.Ref{}) {
		t.Errorf("forward carries a threading ref: %v", fwd.ReplyTo)
	}
	if len(fwd.Attachments) != 1 || fwd.Attachments[0].ContentType != "message/rfc822" {
		t.Errorf("forward attachments = %+v, want the original message", fwd.Attachments)
	}
}

func TestSendThreadsReply(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.addBox("Sent", `\Sent`)
	srv.seed("INBOX", 1)
	ref := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 1}
	srv.setRaw(ref, threadMessage)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	c.Send(c.ReplyTo("alice", ref, false))
	ev.wait(t, "send", func() bool { return len(ev.sendResults()) == 1 })
	if !ev.sendResults()[0].Success {
		t.Fatalf("reply send failed: %s", ev.sendResults()[0].Message)
	}
	raw := string(srv.sendsCopy()[0].Raw)
	if !strings.Contains(raw, "In-Reply-To: <orig@example.org>") {
		t.Errorf("raw missing In-Reply-To:\n%s", raw)
	}
	if !strings.Contains(raw, "References: <root@example.org> <orig@example.org>") {
		t.Errorf("raw missing References chain:\n%s", raw)
	}
	if !strings.Contains(raw, "Subject: Re: Original subject") {
		t.Errorf("raw missing Re: subject:\n%s", raw)
	}
}

func TestSendUnknownAccount(t *testing.T) {
	srv := newFakeServer()
	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)

	c.Send(contract.ComposeDraft{Account: "nobody", To: []model.Address{{Addr: "a@example.org"}}})
	ev.wait(t, "send result", func() bool { return len(ev.sendResults()) == 1 })
	if ev.sendResults()[0].Success {
		t.Fatal("send to unknown account succeeded")
	}
	if got := len(srv.sendsCopy()); got != 0 {
		t.Error("connector called for unknown account")
	}
}
