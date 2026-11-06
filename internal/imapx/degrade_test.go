package imapx

// Regression tests for surviving a server that dies mid-fetch — the real
// "no email loads" bug: Dovecot answered
//
//	* BYE Internal error occurred. Refer to server log for more information.
//
// while generating the BODYSTRUCTURE of one specific message, severing the
// connection. The whole mailbox load used to fail. The client must instead
// deliver every message, degrading only the affected group to "no
// attachment info", and report the degradation honestly.

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// snipeListener hands out connections whose client command stream is
// inspected: when observe returns true for a command line, the connection
// dies mid-dialogue like the crashing Dovecot ("* BYE" + close).
type snipeListener struct {
	net.Listener
	observe func(line string) bool
}

func (l *snipeListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &snipeConn{Conn: c, observe: l.observe}, nil
}

var literalRe = regexp.MustCompile(`\{(\d+)\+?\}\r\n$`)

type snipeConn struct {
	net.Conn
	observe func(string) bool

	residue     []byte       // bytes not yet classified
	literal     int          // remaining literal bytes to pass opaquely
	out         bytes.Buffer // safe bytes ready for the server
	killed      bool
	passthrough bool // after STARTTLS the stream is TLS ciphertext: never scan it
}

func (s *snipeConn) Read(p []byte) (int, error) {
	for {
		if s.out.Len() > 0 {
			return s.out.Read(p)
		}
		if s.killed {
			return 0, io.EOF
		}
		if s.passthrough {
			return s.Conn.Read(p)
		}
		tmp := make([]byte, 4096)
		n, err := s.Conn.Read(tmp)
		if n > 0 {
			s.feed(tmp[:n])
			continue
		}
		if err != nil {
			return 0, err
		}
	}
}

func (s *snipeConn) feed(b []byte) {
	s.residue = append(s.residue, b...)
	for !s.killed {
		if s.literal > 0 {
			take := s.literal
			if take > len(s.residue) {
				take = len(s.residue)
			}
			s.out.Write(s.residue[:take])
			s.residue = s.residue[take:]
			s.literal -= take
			if s.literal > 0 {
				return
			}
			continue
		}
		i := bytes.Index(s.residue, []byte("\r\n"))
		if i < 0 {
			return
		}
		line := string(s.residue[:i])
		s.residue = s.residue[i+2:]
		// Commands are tagged ("T1 STARTTLS"); match the command word.
		if f := strings.Fields(line); len(f) >= 2 && strings.EqualFold(f[1], "STARTTLS") {
			// Everything after this is TLS ciphertext: stop scanning, or
			// the classifier would corrupt the handshake.
			s.out.WriteString(line + "\r\n")
			s.out.Write(s.residue)
			s.residue = nil
			s.passthrough = true
			return
		}
		if m := literalRe.FindStringSubmatch(line + "\r\n"); m != nil {
			if n, err := strconv.Atoi(m[1]); err == nil {
				s.literal = n
			}
		}
		if s.observe != nil && s.observe(line) {
			s.killed = true
			_, _ = s.Conn.Write([]byte("* BYE Internal error occurred. Refer to server log for more information.\r\n"))
			_ = s.Conn.Close()
			return
		}
		s.out.WriteString(line + "\r\n")
	}
}

// fetchesBodyStructure reports whether an IMAP command line fetches the
// BODY/BODYSTRUCTURE item (not BODY.PEEK sections).
func fetchesBodyStructure(line string) bool {
	upper := strings.ToUpper(line)
	if !strings.Contains(upper, "FETCH") || strings.Contains(upper, "PEEK") {
		return false
	}
	return strings.Contains(upper, "BODYSTRUCTURE") ||
		regexp.MustCompile(`\(\s*(?:UID\s+|ENVELOPE\s+|FLAGS\s+|RFC822\.SIZE\s+)*BODY(?:\s|\))`).MatchString(upper)
}

// rawWithAttachment builds a message whose body structure contains a PDF
// attachment part (mirrors the fixture style in client_test.go).
func rawWithAttachment(subject string) []byte {
	return []byte(strings.Join([]string{
		"From: sender@example.org",
		"To: user@example.org",
		"Subject: " + subject,
		"MIME-Version: 1.0",
		`Content-Type: multipart/mixed; boundary="b1"`,
		"",
		"--b1",
		"Content-Type: text/plain; charset=utf-8",
		"",
		"hello",
		"--b1",
		"Content-Type: application/pdf",
		`Content-Disposition: attachment; filename="doc.pdf"`,
		"Content-Transfer-Encoding: base64",
		"",
		"JVBERi0xLjQK",
		"--b1--",
		"",
	}, "\r\n"))
}

// seedInbox appends 30 messages; every third one carries a PDF attachment.
func seedInbox(t *testing.T, cl *Client) {
	t.Helper()
	for i := 0; i < 30; i++ {
		subject := fmt.Sprintf("Subject: msg %02d", i+1)
		raw := rawMessage(subject, "", "body")
		if i%3 == 0 {
			raw = rawWithAttachment(subject)
		}
		mustAppend(t, cl, "INBOX", raw)
	}
}

// TestEnvelopesSurvivesServerByeOnBodyStructure is the "no email loads"
// regression test: the server kills every fetch that touches a poisoned
// group (here uid 11:20 and the bulk fetch) and must not take the mailbox
// down with it.
func TestEnvelopesSurvivesServerByeOnBodyStructure(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	cl := ts.dial(t, nil)
	seedInbox(t, cl)
	cl.envelopeChunk = 10

	ts.setKill(func(line string) bool {
		if !fetchesBodyStructure(line) {
			return false
		}
		// Kill the bulk attempt and the 11:20 chunk; let healthy chunks
		// and all degraded (no-BODY) fetches through.
		return !(strings.Contains(line, "1:10") || strings.Contains(line, "21:30"))
	})

	envs, err := cl.Envelopes(context.Background(), "INBOX", 1, 30)
	if len(envs) != 30 {
		t.Fatalf("got %d envelopes, want 30 (mail must load); err=%v", len(envs), err)
	}
	var pe *PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("want PartialError, got %v", err)
	}
	for _, e := range envs {
		uid := e.Ref.UID
		degraded := uid >= 11 && uid <= 20
		has := uid%3 == 1 // uids 1,4,7,... carry attachments
		if degraded && e.HasAttachments {
			t.Errorf("uid %d: degraded chunk must report no attachments", uid)
		}
		if !degraded && e.HasAttachments != has {
			t.Errorf("uid %d: HasAttachments=%v, want %v (healthy chunks keep detail)", uid, e.HasAttachments, has)
		}
	}
}

// TestEnvelopesDegradesAllChunksWhenServerAlwaysDies is the worst case:
// every BODYSTRUCTURE fetch is fatal. All mail must still load, plainly.
func TestEnvelopesDegradesAllChunksWhenServerAlwaysDies(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	cl := ts.dial(t, nil)
	seedInbox(t, cl)
	cl.envelopeChunk = 10

	ts.setKill(fetchesBodyStructure)

	envs, err := cl.Envelopes(context.Background(), "INBOX", 1, 30)
	if len(envs) != 30 {
		t.Fatalf("got %d envelopes, want 30; err=%v", len(envs), err)
	}
	var pe *PartialError
	if !errors.As(err, &pe) {
		t.Fatalf("want PartialError, got %v", err)
	}
	for _, e := range envs {
		if e.HasAttachments {
			t.Errorf("uid %d: expected degraded (no attachment info)", e.Ref.UID)
		}
	}
}

// TestEnvelopesFastPathUnchangedOnHealthyServer guards the redesign: a
// healthy server must see exactly ONE envelope fetch (the fast path), with
// full detail and no degradation error.
func TestEnvelopesFastPathUnchangedOnHealthyServer(t *testing.T) {
	ts := newTestServer(t, TLSImplicit)
	cl := ts.dial(t, nil)
	seedInbox(t, cl)
	cl.envelopeChunk = 10

	bodyFetches := 0
	ts.setKill(func(line string) bool {
		if fetchesBodyStructure(line) {
			bodyFetches++
		}
		return false
	})

	envs, err := cl.Envelopes(context.Background(), "INBOX", 1, 30)
	if err != nil {
		t.Fatalf("healthy fetch: %v", err)
	}
	if len(envs) != 30 {
		t.Fatalf("got %d envelopes, want 30", len(envs))
	}
	if bodyFetches != 1 {
		t.Errorf("healthy server saw %d body fetches, want exactly 1 (fast path)", bodyFetches)
	}
	var pe *PartialError
	if errors.As(err, &pe) {
		t.Error("healthy fetch must not report degradation")
	}
}

func TestPartialErrorMessage(t *testing.T) {
	pe := &PartialError{Groups: 2, Err: errors.New("boom")}
	if !strings.Contains(pe.Error(), "boom") || !strings.Contains(pe.Error(), "attachment") {
		t.Errorf("unhelpful message: %q", pe.Error())
	}
	if !errors.Is(pe, pe.Err) {
		t.Error("Unwrap missing")
	}
}
