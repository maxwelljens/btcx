package imapx

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/emersion/go-imap/v2"

	"btcx/internal/model"
)

func rawMessage(lines ...string) []byte {
	return []byte(strings.Join(lines, "\r\n"))
}

func mustAppend(t *testing.T, c *Client, mailbox string, raw []byte) model.Ref {
	t.Helper()
	ref, err := c.Append(context.Background(), mailbox, raw, model.Flags{})
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	return ref
}

func TestDialCertificateVerification(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)

	// Default configuration: verification must reject the self-signed cert.
	cfg := ts.config()
	cfg.InsecureSkipVerify = false
	cfg.Username, cfg.Password = testUser, testPass
	if c, err := Dial(context.Background(), cfg); err == nil {
		c.Close()
		t.Fatal("Dial accepted a self-signed certificate without a trust root")
	} else if !strings.Contains(strings.ToLower(err.Error()), "certificate") {
		t.Fatalf("error does not mention the certificate: %v", err)
	}

	// The dangerous escape hatch works.
	insecure := ts.dial(t, func(cfg *DialConfig) { cfg.InsecureSkipVerify = true })
	if _, err := insecure.Mailboxes(context.Background()); err != nil {
		t.Fatalf("insecure connection unusable: %v", err)
	}

	// A CA file restores verification.
	trusted := ts.dial(t, func(cfg *DialConfig) {
		cfg.InsecureSkipVerify = false
		cfg.CAFile = ts.caFile(t)
	})
	if _, err := trusted.Mailboxes(context.Background()); err != nil {
		t.Fatalf("CA-verified connection unusable: %v", err)
	}
}

func TestDialStartTLS(t *testing.T) {
	ts := newTestServer(t, TLSStartTLS)
	c := ts.dial(t, func(cfg *DialConfig) {
		cfg.InsecureSkipVerify = false
		cfg.CAFile = ts.caFile(t)
	})
	if _, err := c.Mailboxes(context.Background()); err != nil {
		t.Fatalf("STARTTLS connection unusable: %v", err)
	}

	// A server without STARTTLS must fail instead of continuing in cleartext.
	plain := newTestServer(t, 0)
	cfg := plain.config()
	cfg.TLS = TLSStartTLS
	if c, err := Dial(context.Background(), cfg); err == nil {
		c.Close()
		t.Fatal("Dial succeeded against a server without STARTTLS")
	}
}

func TestDialAuthFailure(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	cfg := ts.config()
	cfg.Password = "wrong-password"
	_, err := Dial(context.Background(), cfg)
	if err == nil {
		t.Fatal("Dial accepted a wrong password")
	}
	if !strings.Contains(err.Error(), "login") {
		t.Errorf("error lacks login context: %v", err)
	}
	if strings.Contains(err.Error(), "wrong-password") || strings.Contains(err.Error(), testPass) {
		t.Errorf("error leaks a password: %v", err)
	}
}

func TestMailboxes(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	c := ts.dial(t, nil)

	boxes, err := c.Mailboxes(context.Background())
	if err != nil {
		t.Fatalf("Mailboxes: %v", err)
	}
	byName := make(map[string]model.Mailbox, len(boxes))
	for _, m := range boxes {
		byName[m.Name] = m
	}
	for _, want := range []string{"INBOX", "Sent", "Archive"} {
		if _, ok := byName[want]; !ok {
			t.Errorf("mailbox %q missing from %v", want, boxes)
		}
	}
	sent, ok := byName["Sent"]
	if !ok {
		t.Fatal("Sent mailbox missing")
	}
	if sent.Delimiter != "/" {
		t.Errorf("Sent delimiter = %q, want %q", sent.Delimiter, "/")
	}
	if !slices.Contains(sent.Attributes, string(imap.MailboxAttrSubscribed)) {
		t.Errorf("Sent attributes %v lack the subscribed attribute", sent.Attributes)
	}
}

func TestMailboxesTooMany(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	for i := 0; i <= maxMailboxes; i++ {
		if err := ts.user.Create(fmt.Sprintf("bulk-%05d", i), nil); err != nil {
			t.Fatalf("create mailbox %d: %v", i, err)
		}
	}
	c := ts.dial(t, nil)

	_, err := c.Mailboxes(context.Background())
	if !errors.Is(err, errTooManyMailboxes) {
		t.Fatalf("Mailboxes = %v, want errTooManyMailboxes", err)
	}
	if got := err.Error(); !strings.Contains(got, "imapx: too many mailboxes") {
		t.Errorf("error text = %q", got)
	}
	// The connection survives: the command was drained, not abandoned.
	if _, err := c.Mailboxes(context.Background()); !errors.Is(err, errTooManyMailboxes) {
		t.Errorf("connection unusable after the cap: %v", err)
	}
}

func TestMailboxFromList(t *testing.T) {
	got := mailboxFromList(&imap.ListData{
		Mailbox: "Sent",
		Delim:   '/',
		Attrs:   []imap.MailboxAttr{imap.MailboxAttrSent},
	})
	if got.Name != "Sent" || got.Delimiter != "/" || got.Role() != "sent" {
		t.Errorf("mailboxFromList = %+v (role %q)", got, got.Role())
	}
	if bare := mailboxFromList(&imap.ListData{Mailbox: "INBOX"}); bare.Delimiter != "" {
		t.Errorf("NIL delimiter became %q", bare.Delimiter)
	}
}

func TestEnvelopes(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	c := ts.dial(t, nil)

	plain := rawMessage(
		"From: Alice <alice@example.org>",
		"To: Bob <bob@example.org>",
		"Cc: carol@example.org",
		"Subject: =?UTF-8?Q?Gr=C3=BC=C3=9Fe?=",
		"Date: Mon, 02 Jan 2006 15:04:05 -0700",
		"Message-ID: <m1@example.org>",
		"MIME-Version: 1.0",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"Hello body",
	)
	attached := rawMessage(
		"From: Carol <carol@example.org>",
		"To: dave@example.org",
		"Subject: report",
		"Date: Tue, 03 Jan 2006 10:00:00 +0000",
		"Message-ID: <m2@example.org>",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="b1"`,
		"",
		"--b1",
		"Content-Type: text/plain",
		"",
		"see attached",
		"--b1",
		"Content-Type: application/pdf",
		`Content-Disposition: attachment; filename="report.pdf"`,
		"",
		"pdfdata",
		"--b1--",
	)
	textAttachment := rawMessage(
		"From: Carol <carol@example.org>",
		"To: dave@example.org",
		"Subject: notes",
		"Message-ID: <m3@example.org>",
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="b2"`,
		"",
		"--b2",
		"Content-Type: text/plain",
		"",
		"body text",
		"--b2",
		"Content-Type: text/plain",
		`Content-Disposition: attachment; filename="notes.txt"`,
		"",
		"notes",
		"--b2--",
	)
	for _, raw := range [][]byte{plain, attached, textAttachment} {
		mustAppend(t, c, "INBOX", raw)
	}

	envs, err := c.Envelopes(context.Background(), "INBOX", 1, 10)
	if err != nil {
		t.Fatalf("Envelopes: %v", err)
	}
	if len(envs) != 3 {
		t.Fatalf("got %d envelopes, want 3", len(envs))
	}
	first := envs[0]
	if first.Ref.Mailbox != "INBOX" || first.Ref.UID != 1 || first.Ref.UIDValidity == 0 {
		t.Errorf("Ref = %+v", first.Ref)
	}
	if first.MessageID != "m1@example.org" {
		t.Errorf("MessageID = %q", first.MessageID)
	}
	if first.Subject != "Grüße" {
		t.Errorf("Subject = %q, want Grüße", first.Subject)
	}
	if len(first.From) != 1 || first.From[0] != (model.Address{Name: "Alice", Addr: "alice@example.org"}) {
		t.Errorf("From = %+v", first.From)
	}
	if len(first.To) != 1 || first.To[0].Addr != "bob@example.org" {
		t.Errorf("To = %+v", first.To)
	}
	if len(first.Cc) != 1 || first.Cc[0].Addr != "carol@example.org" {
		t.Errorf("Cc = %+v", first.Cc)
	}
	if first.Date.Year() != 2006 {
		t.Errorf("Date = %v", first.Date)
	}
	if first.Size != int64(len(plain)) {
		t.Errorf("Size = %d, want %d", first.Size, len(plain))
	}
	if first.HasAttachments {
		t.Error("plain message reported attachments")
	}
	if !envs[1].HasAttachments {
		t.Error("multipart message with a PDF reported no attachments")
	}
	if !envs[2].HasAttachments {
		t.Error("text part with disposition=attachment reported no attachments")
	}

	// Ascending, filtered by startUID and capped by count.
	envs, err = c.Envelopes(context.Background(), "INBOX", 2, 10)
	if err != nil {
		t.Fatalf("Envelopes from UID 2: %v", err)
	}
	if len(envs) != 2 || envs[0].Ref.UID != 2 || envs[1].Ref.UID != 3 {
		t.Fatalf("UID filtering wrong: %+v", envs)
	}
	envs, err = c.Envelopes(context.Background(), "INBOX", 1, 2)
	if err != nil {
		t.Fatalf("Envelopes count 2: %v", err)
	}
	if len(envs) != 2 {
		t.Fatalf("count cap ignored: got %d", len(envs))
	}

	// n:* on an empty tail must not return the last message.
	empty, err := c.Envelopes(context.Background(), "INBOX", 99, 10)
	if err != nil {
		t.Fatalf("Envelopes past the end: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("Envelopes past the end returned %+v", empty)
	}
}

func TestRawMessageDoesNotSetSeen(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	c := ts.dial(t, nil)
	raw := rawMessage("From: a@example.org", "Subject: peek", "", "body")
	ref := mustAppend(t, c, "INBOX", raw)

	got, err := c.RawMessage(context.Background(), "INBOX", ref.UID)
	if err != nil {
		t.Fatalf("RawMessage: %v", err)
	}
	if !bytes.Equal(got, raw) {
		t.Errorf("RawMessage returned %q, want %q", got, raw)
	}

	envs, err := c.Envelopes(context.Background(), "INBOX", 1, 10)
	if err != nil {
		t.Fatalf("Envelopes: %v", err)
	}
	if len(envs) != 1 || envs[0].Flags.Seen {
		t.Fatalf("BODY.PEEK[] set \\Seen: %+v", envs)
	}
}

func TestSetFlags(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	c := ts.dial(t, nil)
	ref := mustAppend(t, c, "INBOX", rawMessage("Subject: flags", "", "body"))

	flagsOf := func() model.Flags {
		t.Helper()
		envs, err := c.Envelopes(context.Background(), "INBOX", 1, 10)
		if err != nil {
			t.Fatalf("Envelopes: %v", err)
		}
		if len(envs) != 1 {
			t.Fatalf("got %d envelopes", len(envs))
		}
		return envs[0].Flags
	}

	if err := c.SetFlags(context.Background(), "INBOX", ref.UID,
		model.Flags{Flagged: true, Draft: true}, model.Flags{}); err != nil {
		t.Fatalf("add flags: %v", err)
	}
	if f := flagsOf(); !f.Flagged || !f.Draft || f.Seen {
		t.Fatalf("flags after add = %+v", f)
	}

	if err := c.SetFlags(context.Background(), "INBOX", ref.UID,
		model.Flags{Seen: true}, model.Flags{Flagged: true}); err != nil {
		t.Fatalf("update flags: %v", err)
	}
	if f := flagsOf(); !f.Seen || f.Flagged || !f.Draft {
		t.Fatalf("flags after update = %+v", f)
	}
}

func TestSearch(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	c := ts.dial(t, nil)
	mustAppend(t, c, "INBOX", rawMessage(
		"From: Alice <alice@example.org>",
		"To: bob@example.org",
		"Subject: Invoice 42",
		"",
		"quarterly numbers enclosed",
	))
	mustAppend(t, c, "INBOX", rawMessage(
		"From: Bob <bob@example.org>",
		"To: bob@example.org",
		"Subject: Lunch?",
		"",
		"See you at noon",
	))

	tests := []struct {
		name string
		text string
		want []uint32
	}{
		{"subject", "Invoice", []uint32{1}},
		{"body", "noon", []uint32{2}},
		{"from", "alice@example.org", []uint32{1}},
		{"no match", "nothing-matches-this", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := c.Search(context.Background(), "INBOX", tt.text)
			if err != nil {
				t.Fatalf("Search(%q): %v", tt.text, err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("Search(%q) = %v, want %v", tt.text, got, tt.want)
			}
			for i := range tt.want {
				if got[i] != tt.want[i] {
					t.Fatalf("Search(%q) = %v, want %v", tt.text, got, tt.want)
				}
			}
		})
	}

	if _, err := c.Search(context.Background(), "INBOX", "  "); err == nil {
		t.Error("empty search query accepted")
	}
}

func TestAppendReturnsRef(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	c := ts.dial(t, nil)

	raw := rawMessage("Subject: appended", "", "hello")
	ref := mustAppend(t, c, "INBOX", raw)
	if ref.Mailbox != "INBOX" || ref.UID == 0 || ref.UIDValidity == 0 {
		t.Fatalf("Append returned %+v", ref)
	}
	if got, err := c.RawMessage(context.Background(), "INBOX", ref.UID); err != nil {
		t.Fatalf("RawMessage: %v", err)
	} else if !bytes.Equal(got, raw) {
		t.Errorf("read back %q, want %q", got, raw)
	}
}

func TestMove(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	c := ts.dial(t, nil)

	raw := rawMessage("Subject: movable", "", "move me")
	ref := mustAppend(t, c, "INBOX", raw)
	if err := c.Move(context.Background(), "INBOX", ref.UID, "Archive"); err != nil {
		t.Fatalf("Move: %v", err)
	}

	envs, err := c.Envelopes(context.Background(), "INBOX", 1, 10)
	if err != nil {
		t.Fatalf("Envelopes INBOX: %v", err)
	}
	if len(envs) != 0 {
		t.Fatalf("message still in INBOX: %+v", envs)
	}

	envs, err = c.Envelopes(context.Background(), "Archive", 1, 10)
	if err != nil {
		t.Fatalf("Envelopes Archive: %v", err)
	}
	if len(envs) != 1 {
		t.Fatalf("message not in Archive: %+v", envs)
	}
	if got, err := c.RawMessage(context.Background(), "Archive", envs[0].Ref.UID); err != nil {
		t.Fatalf("RawMessage Archive: %v", err)
	} else if !bytes.Equal(got, raw) {
		t.Errorf("moved message differs: %q", got)
	}
}

func TestWatch(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	c := ts.dial(t, nil)
	other := ts.dial(t, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	updates := make(chan []uint32, 4)
	errCh := make(chan error, 1)
	go func() {
		errCh <- c.Watch(ctx, "INBOX", func(uids []uint32) { updates <- uids })
	}()

	// Wait until Watch owns the connection.
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, err := c.Mailboxes(context.Background())
		if errors.Is(err, ErrBusy) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Watch never took the connection; last error %v", err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := c.Watch(context.Background(), "INBOX", func([]uint32) {}); !errors.Is(err, ErrBusy) {
		t.Fatalf("second Watch = %v, want ErrBusy", err)
	}

	ref := mustAppend(t, other, "INBOX", rawMessage("Subject: new", "", "hi"))
	select {
	case uids := <-updates:
		if len(uids) != 1 || uids[0] != ref.UID {
			t.Fatalf("onNew got %v, want [%d]", uids, ref.UID)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("onNew was never called")
	}

	cancel()
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("Watch returned %v after cancellation", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Watch did not return after cancellation")
	}

	// The connection is usable again once Watch is gone.
	if _, err := c.Mailboxes(context.Background()); err != nil {
		t.Fatalf("client unusable after Watch: %v", err)
	}
}

func TestMessageTooLarge(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	c := ts.dial(t, nil)
	ref := mustAppend(t, c, "INBOX", rawMessage("Subject: big", "", strings.Repeat("x", 128)))

	c.maxMessageBytes = 16
	_, err := c.RawMessage(context.Background(), "INBOX", ref.UID)
	if !errors.Is(err, ErrMessageTooLarge) {
		t.Fatalf("RawMessage = %v, want ErrMessageTooLarge", err)
	}
	if err.Error() != "imapx: message too large" {
		t.Errorf("error text = %q", err)
	}
}

func TestFlagsConversion(t *testing.T) {
	in := model.Flags{Seen: true, Answered: true, Flagged: true, Deleted: true, Draft: true, Recent: true}
	if f := flagsFromIMAP(flagsToIMAP(in)); f != (model.Flags{Seen: true, Answered: true, Flagged: true, Deleted: true, Draft: true}) {
		t.Errorf("round trip = %+v", f)
	}
	if got := flagsToIMAP(model.Flags{Recent: true}); len(got) != 0 {
		t.Errorf("\\Recent must not be sent, got %v", got)
	}
}

func TestUIDsFrom(t *testing.T) {
	uids, err := uidsFrom(imap.UIDSet{{Start: 3, Stop: 5}, {Start: 1, Stop: 1}, {Start: 3, Stop: 4}})
	if err != nil {
		t.Fatalf("uidsFrom: %v", err)
	}
	want := []uint32{1, 3, 4, 5}
	if len(uids) != len(want) {
		t.Fatalf("uidsFrom = %v, want %v", uids, want)
	}
	for i := range want {
		if uids[i] != want[i] {
			t.Fatalf("uidsFrom = %v, want %v", uids, want)
		}
	}

	if _, err := uidsFrom(imap.UIDSet{{Start: 1, Stop: 0}}); err == nil {
		t.Error("unbounded range accepted")
	}
	if _, err := uidsFrom(imap.SeqSet{{Start: 1, Stop: 2}}); err == nil {
		t.Error("sequence numbers accepted as UIDs")
	}
}

// stallingListener accepts TCP connections and never writes a byte, so both
// the TLS handshake and the IMAP greeting stall forever.
func stallingListener(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	var (
		mu    sync.Mutex
		conns []net.Conn
	)
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
		}
	}()
	return ln.Addr().(*net.TCPAddr).Port
}

func TestDialStalledServerTimesOut(t *testing.T) {
	port := stallingListener(t)
	const timeout = time.Second

	for _, tt := range []struct {
		name string
		mode TLSMode
	}{
		{"implicit", TLSImplicit},
		{"starttls", TLSStartTLS},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := DialConfig{
				Host:               "127.0.0.1",
				Port:               port,
				Username:           testUser,
				Password:           testPass,
				TLS:                tt.mode,
				InsecureSkipVerify: true,
				DialTimeout:        timeout,
			}
			start := time.Now()
			c, err := Dial(context.Background(), cfg)
			elapsed := time.Since(start)
			if err == nil {
				c.Close()
				t.Fatal("Dial succeeded against a server that never responds")
			}
			if elapsed > 5*time.Second {
				t.Errorf("Dial blocked for %v with a %v dial timeout", elapsed, timeout)
			}
			// Either the context or the socket deadline may fire first.
			if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Errorf("err = %v, want a deadline error", err)
			}
		})
	}
}
