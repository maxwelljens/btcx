package mailparse

import (
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"btcx/internal/model"
)

// wire joins header and body lines with CRLF, the line ending of the wire
// format.
func wire(lines ...string) string { return strings.Join(lines, "\r\n") }

var simplePlain = wire(
	"From: Alice <alice@example.org>",
	"To: Bob <bob@example.org>",
	"Subject: Hello",
	"Date: Mon, 02 Jan 2006 15:04:05 -0700",
	"Message-ID: <abc@example.org>",
	"MIME-Version: 1.0",
	"Content-Type: text/plain; charset=utf-8",
	"Content-Transfer-Encoding: 7bit",
	"",
	"Hello, world!",
	"",
)

var qpLatin1 = wire(
	"From: =?ISO-8859-1?Q?Andr=E9?= <andre@example.org>",
	"Subject: =?ISO-8859-1?Q?Gr=FC=DFe?=",
	"MIME-Version: 1.0",
	"Content-Type: text/plain; charset=ISO-8859-1",
	"Content-Transfer-Encoding: quoted-printable",
	"",
	"Gr=FC=DFe aus M=FCnchen.",
	"",
)

var utf8EncodedWords = wire(
	"From: =?UTF-8?Q?=D0=9F=D1=80=D0=B8=D0=B2=D0=B5=D1=82?= <pv@example.org>",
	"Subject: =?UTF-8?B?0J/RgNC40LLQtdGC?=",
	"MIME-Version: 1.0",
	"Content-Type: text/plain; charset=utf-8",
	"",
	"body",
	"",
)

var htmlOnly = wire(
	"Subject: HTML only",
	"MIME-Version: 1.0",
	"Content-Type: text/html; charset=utf-8",
	"",
	`<p>Hi <b>there</b></p>`,
	"",
)

var alternative = wire(
	"Subject: alternative",
	"MIME-Version: 1.0",
	`Content-Type: multipart/alternative; boundary="alt"`,
	"",
	"--alt",
	"Content-Type: text/plain; charset=utf-8",
	"",
	"plain version",
	"--alt",
	"Content-Type: text/html; charset=utf-8",
	"",
	"<p>html version</p>",
	"--alt--",
	"",
)

var mixedAttachments = wire(
	"Subject: mixed",
	"MIME-Version: 1.0",
	`Content-Type: multipart/mixed; boundary="mix"`,
	"",
	"--mix",
	"Content-Type: text/plain; charset=utf-8",
	"",
	"body text",
	"--mix",
	"Content-Type: application/pdf",
	"Content-Disposition: attachment; filename*=utf-8''r%C3%A9sum%C3%A9.pdf",
	"Content-Transfer-Encoding: base64",
	"",
	"JVBERi0xLjQK",
	"--mix",
	"Content-Type: text/plain; name=\"=?UTF-8?Q?notes_=E2=9C=93.txt?=\"",
	"Content-Disposition: attachment; filename=\"=?UTF-8?Q?notes_=E2=9C=93.txt?=\"",
	"Content-Transfer-Encoding: base64",
	"",
	"aGVsbG8=",
	"--mix--",
	"",
)

var relatedInline = wire(
	"Subject: related",
	"MIME-Version: 1.0",
	`Content-Type: multipart/related; boundary="rel"`,
	"",
	"--rel",
	"Content-Type: text/html; charset=utf-8",
	"",
	`<img src="cid:img1@example.org">`,
	"--rel",
	"Content-Type: image/png",
	"Content-ID: <img1@example.org>",
	"Content-Transfer-Encoding: base64",
	"",
	"iVBORw0KGgo=",
	"--rel--",
	"",
)

var forwarded = wire(
	"Subject: fwd",
	"MIME-Version: 1.0",
	`Content-Type: multipart/mixed; boundary="m"`,
	"",
	"--m",
	"Content-Type: text/plain",
	"",
	"see attached",
	"--m",
	"Content-Type: message/rfc822",
	`Content-Disposition: attachment; filename="fwd.eml"`,
	"",
	"From: Inner <inner@example.org>",
	"Subject: Inner subject",
	"",
	"inner body",
	"--m--",
	"",
)

var multiplePlain = wire(
	"Subject: two plain parts",
	"MIME-Version: 1.0",
	`Content-Type: multipart/mixed; boundary="m"`,
	"",
	"--m",
	"Content-Type: text/plain",
	"",
	"first",
	"--m",
	"Content-Type: text/plain",
	"",
	"second",
	"--m--",
	"",
)

// nested builds a message with n nested multipart/mixed wrappers around a
// single text/plain leaf.
func nested(n int) string {
	part := wire("Content-Type: text/plain", "", "deep")
	for i := 0; i < n; i++ {
		b := "b" + strconv.Itoa(i)
		part = wire("Content-Type: multipart/mixed; boundary="+b, "", "--"+b, part, "--"+b+"--", "")
	}
	return wire("From: x@example.org", "MIME-Version: 1.0", part)
}

func mustParse(t *testing.T, raw string) *model.Message {
	t.Helper()
	m, err := ParseBytes([]byte(raw))
	if err != nil {
		t.Fatalf("ParseBytes: %v", err)
	}
	return m
}

func TestParseSimplePlain(t *testing.T) {
	m := mustParse(t, simplePlain)

	wantKeys := []string{
		"From", "To", "Subject", "Date", "Message-Id", "Mime-Version",
		"Content-Type", "Content-Transfer-Encoding",
	}
	got := make([]string, 0, len(wantKeys))
	for _, f := range m.Header.Fields() {
		got = append(got, f.Key)
	}
	if strings.Join(got, ",") != strings.Join(wantKeys, ",") {
		t.Errorf("header keys/order = %v, want %v", got, wantKeys)
	}

	if got, want := m.Plain, "Hello, world!\r\n"; got != want {
		t.Errorf("Plain = %q, want %q", got, want)
	}
	if m.HTML != "" {
		t.Errorf("HTML = %q, want empty", m.HTML)
	}
	if len(m.Attachments) != 0 {
		t.Errorf("Attachments = %d, want 0", len(m.Attachments))
	}
	if got, want := m.Subject(), "Hello"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
	if got, want := m.MessageID(), "abc@example.org"; got != want {
		t.Errorf("MessageID = %q, want %q", got, want)
	}
	if d, ok := m.Date(); !ok || !d.Equal(time.Date(2006, 1, 2, 15, 4, 5, 0, time.FixedZone("", -7*3600))) {
		t.Errorf("Date = %v, %v", d, ok)
	}
}

func TestParseFoldedHeaderIsUnfolded(t *testing.T) {
	raw := wire(
		"Subject: a folded",
		"\tsubject line",
		"Content-Type: text/plain",
		"",
		"body",
		"",
	)
	m := mustParse(t, raw)
	got := m.Header.Get("Subject")
	if strings.ContainsAny(got, "\r\n") {
		t.Errorf("Subject still folded: %q", got)
	}
	if !strings.Contains(got, "a folded") || !strings.Contains(got, "subject line") {
		t.Errorf("Subject = %q, want both fragments", got)
	}
}

func TestParseEncodedWordsAndCharsets(t *testing.T) {
	t.Run("quoted-printable latin-1", func(t *testing.T) {
		m := mustParse(t, qpLatin1)
		if got, want := m.Plain, "Grüße aus München.\r\n"; got != want {
			t.Errorf("Plain = %q, want %q", got, want)
		}
		if got, want := m.Subject(), "Grüße"; got != want {
			t.Errorf("Subject = %q, want %q", got, want)
		}
		from := m.From()
		if len(from) != 1 || from[0].Name != "André" || from[0].Addr != "andre@example.org" {
			t.Errorf("From = %+v", from)
		}
	})

	t.Run("utf-8 encoded words", func(t *testing.T) {
		m := mustParse(t, utf8EncodedWords)
		if got, want := m.Subject(), "Привет"; got != want {
			t.Errorf("Subject = %q, want %q", got, want)
		}
		from := m.From()
		if len(from) != 1 || from[0].Name != "Привет" {
			t.Errorf("From = %+v", from)
		}
	})
}

func TestParseHTMLOnly(t *testing.T) {
	m := mustParse(t, htmlOnly)
	if m.Plain != "" {
		t.Errorf("Plain = %q, want empty", m.Plain)
	}
	if got, want := m.HTML, "<p>Hi <b>there</b></p>\r\n"; got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
}

func TestParseAlternativeKeepsBoth(t *testing.T) {
	m := mustParse(t, alternative)
	if got, want := m.Plain, "plain version"; got != want {
		t.Errorf("Plain = %q, want %q", got, want)
	}
	if got, want := m.HTML, "<p>html version</p>"; got != want {
		t.Errorf("HTML = %q, want %q", got, want)
	}
	if len(m.Attachments) != 0 {
		t.Errorf("Attachments = %d, want 0", len(m.Attachments))
	}
}

func TestParseMixedAttachments(t *testing.T) {
	m := mustParse(t, mixedAttachments)
	if got, want := m.Plain, "body text"; got != want {
		t.Errorf("Plain = %q, want %q", got, want)
	}
	if len(m.Attachments) != 2 {
		t.Fatalf("Attachments = %d, want 2", len(m.Attachments))
	}

	pdf := m.Attachments[0]
	if pdf.Filename != "résumé.pdf" {
		t.Errorf("Filename = %q, want résumé.pdf", pdf.Filename)
	}
	if pdf.ContentType != "application/pdf" || pdf.Inline {
		t.Errorf("pdf = %+v", pdf)
	}
	if got, want := string(pdf.Data), "%PDF-1.4\n"; got != want {
		t.Errorf("pdf Data = %q, want %q", got, want)
	}

	notes := m.Attachments[1]
	if notes.Filename != "notes ✓.txt" {
		t.Errorf("Filename = %q, want notes ✓.txt", notes.Filename)
	}
	if got, want := string(notes.Data), "hello"; got != want {
		t.Errorf("notes Data = %q, want %q", got, want)
	}
}

func TestParseRelatedInline(t *testing.T) {
	m := mustParse(t, relatedInline)
	if !strings.Contains(m.HTML, "cid:img1@example.org") {
		t.Errorf("HTML = %q", m.HTML)
	}
	if len(m.Attachments) != 1 {
		t.Fatalf("Attachments = %d, want 1", len(m.Attachments))
	}
	att := m.Attachments[0]
	if !att.Inline {
		t.Error("Inline = false, want true")
	}
	if att.ContentID != "img1@example.org" {
		t.Errorf("ContentID = %q, want img1@example.org", att.ContentID)
	}
	if att.ContentType != "image/png" {
		t.Errorf("ContentType = %q", att.ContentType)
	}
	if got, want := string(att.Data), "\x89PNG\r\n\x1a\n"; got != want {
		t.Errorf("Data = %q, want %q", got, want)
	}
}

func TestParseMessageRFC822(t *testing.T) {
	m := mustParse(t, forwarded)
	if len(m.Attachments) != 1 {
		t.Fatalf("Attachments = %d, want 1", len(m.Attachments))
	}
	att := m.Attachments[0]
	if att.ContentType != "message/rfc822" {
		t.Errorf("ContentType = %q", att.ContentType)
	}
	if att.Filename != "fwd.eml" {
		t.Errorf("Filename = %q", att.Filename)
	}
	if !strings.Contains(string(att.Data), "Subject: Inner subject") {
		t.Errorf("Data = %q", att.Data)
	}
}

func TestParseMultiplePlainPartsJoined(t *testing.T) {
	m := mustParse(t, multiplePlain)
	if got, want := m.Plain, "first\nsecond"; got != want {
		t.Errorf("Plain = %q, want %q", got, want)
	}
}

func TestParseNestingDepth(t *testing.T) {
	if _, err := ParseBytes([]byte(nested(10))); err != nil {
		t.Errorf("depth 10: %v", err)
	}
	_, err := ParseBytes([]byte(nested(31)))
	if !errors.Is(err, errTooDeep) {
		t.Errorf("depth 31: err = %v, want errTooDeep", err)
	}
}

func TestParseLimits(t *testing.T) {
	t.Run("part count", func(t *testing.T) {
		_, err := parse(strings.NewReader(mixedAttachments), limits{maxMIMEDepth, 2, maxDecodedBytes})
		if !errors.Is(err, errTooManyParts) {
			t.Errorf("err = %v, want errTooManyParts", err)
		}
	})
	t.Run("decoded bytes", func(t *testing.T) {
		_, err := parse(strings.NewReader(simplePlain), limits{maxMIMEDepth, maxPartCount, 8})
		if !errors.Is(err, errTooLarge) {
			t.Errorf("err = %v, want errTooLarge", err)
		}
	})
	t.Run("default part cap", func(t *testing.T) {
		raw := "Content-Type: multipart/mixed; boundary=x\r\n\r\n" +
			strings.Repeat("--x\r\n\r\n", maxPartCount) + "--x--\r\n"
		if _, err := ParseBytes([]byte(raw)); !errors.Is(err, errTooManyParts) {
			t.Errorf("err = %v, want errTooManyParts", err)
		}
	})
}

func TestParseMalformed(t *testing.T) {
	underline := strings.Repeat("a", maxHeaderBytes+1)
	tests := []struct {
		name    string
		raw     string
		wantErr bool
	}{
		{
			name: "broken base64 is best effort",
			raw: wire(
				"Content-Type: text/plain",
				"Content-Transfer-Encoding: base64",
				"",
				"!!!not base64!!!",
				"",
			),
		},
		{
			name: "missing boundary",
			raw: wire(
				"Content-Type: multipart/mixed",
				"",
				"--x",
				"Content-Type: text/plain",
				"",
				"body",
				"--x--",
				"",
			),
			wantErr: true,
		},
		{
			name: "truncated multipart",
			raw: wire(
				"Content-Type: multipart/mixed; boundary=x",
				"",
				"--x",
				"Content-Type: text/plain",
				"",
				"never closed",
			),
			wantErr: true,
		},
		{
			name: "unknown charset",
			raw: wire(
				"Content-Type: text/plain; charset=x-not-a-charset",
				"",
				"plain ascii survives",
				"",
			),
		},
		{
			name:    "absurd header line",
			raw:     "Subject: " + underline + "\r\n\r\nbody\r\n",
			wantErr: true,
		},
		{
			name: "NUL bytes",
			raw:  "Subject: a\x00b\r\nContent-Type: text/plain\r\n\r\nbo\x00dy\r\n",
		},
		{
			name: "header injection attempt",
			raw: wire(
				"Subject: a",
				"Bcc: evil@example.org",
				"Content-Type: text/plain",
				"",
				"body",
				"",
			),
		},
		{
			name: "empty message",
			raw:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m, err := ParseBytes([]byte(tt.raw))
			if tt.wantErr {
				if err == nil {
					t.Fatalf("err = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("err = %v, want nil", err)
			}
			for _, f := range m.Header.Fields() {
				if strings.ContainsAny(f.Value, "\r\n") {
					t.Errorf("field %q keeps a line break: %q", f.Key, f.Value)
				}
			}
		})
	}
}

func TestParseEnvelopeHeaderOnly(t *testing.T) {
	raw := wire(
		"From: =?UTF-8?Q?=D0=9F=D1=80=D0=B8=D0=B2=D0=B5=D1=82?= <pv@example.org>",
		"To: Bob <bob@example.org>",
		"Cc: carol@example.org",
		"Subject: =?UTF-8?B?0J/RgNC40LLQtdGC?=",
		"Date: Mon, 02 Jan 2006 15:04:05 -0700",
		"Message-ID: <abc@example.org>",
		`Content-Type: multipart/mixed; boundary="x"`,
		"",
	)

	env, err := ParseEnvelope([]byte(raw))
	if err != nil {
		t.Fatalf("ParseEnvelope: %v", err)
	}
	if got, want := env.Subject, "Привет"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
	if got, want := env.MessageID, "abc@example.org"; got != want {
		t.Errorf("MessageID = %q, want %q", got, want)
	}
	if len(env.From) != 1 || env.From[0].Name != "Привет" {
		t.Errorf("From = %+v", env.From)
	}
	if len(env.To) != 1 || env.To[0].Addr != "bob@example.org" {
		t.Errorf("To = %+v", env.To)
	}
	if len(env.Cc) != 1 || env.Cc[0].Addr != "carol@example.org" {
		t.Errorf("Cc = %+v", env.Cc)
	}
	if env.Date.IsZero() {
		t.Error("Date is zero")
	}
	if env.Size != int64(len(raw)) {
		t.Errorf("Size = %d, want %d", env.Size, len(raw))
	}
	if !env.HasAttachments {
		t.Error("HasAttachments = false, want true for multipart/mixed")
	}
}

func TestParseEnvelopeAttachmentsHeuristic(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want bool
	}{
		{"plain", simplePlain, false},
		{"html", htmlOnly, false},
		{"alternative", alternative, false},
		{"mixed", mixedAttachments, true},
		{"related", relatedInline, true},
		{"disposition attachment", "Content-Disposition: attachment\r\n\r\nx\r\n", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env, err := ParseEnvelope([]byte(tt.raw))
			if err != nil {
				t.Fatalf("ParseEnvelope: %v", err)
			}
			if env.HasAttachments != tt.want {
				t.Errorf("HasAttachments = %v, want %v", env.HasAttachments, tt.want)
			}
		})
	}
}

func TestParseEnvelopeMalformedNoPanic(t *testing.T) {
	for _, raw := range []string{"", "\r\n", "garbage", "Subject: x", "Subject: x\r\n\r\n"} {
		if _, err := ParseEnvelope([]byte(raw)); err != nil && raw == "Subject: x\r\n\r\n" {
			t.Errorf("ParseEnvelope(%q) = %v, want nil", raw, err)
		}
	}
}

func TestEnvelopeFrom(t *testing.T) {
	m := &model.Message{Header: model.NewHeader()}
	m.Header.Set("From", "Alice <alice@example.org>")
	m.Header.Set("To", "Bob <bob@example.org>")
	m.Header.Set("Cc", "carol@example.org")
	m.Header.Set("Subject", "=?UTF-8?Q?Gr=C3=BC=C3=9Fe?=")
	m.Header.Set("Date", "Mon, 02 Jan 2006 15:04:05 -0700")
	m.Header.Set("Message-ID", "<id@example.org>")
	m.Attachments = []model.Attachment{{ContentType: "application/pdf"}}

	env := EnvelopeFrom(m)
	if env.Subject != "Grüße" {
		t.Errorf("Subject = %q", env.Subject)
	}
	if env.MessageID != "id@example.org" {
		t.Errorf("MessageID = %q", env.MessageID)
	}
	if len(env.From) != 1 || env.To[0].Addr != "bob@example.org" || env.Cc[0].Addr != "carol@example.org" {
		t.Errorf("addresses = %+v %+v %+v", env.From, env.To, env.Cc)
	}
	if env.Date.IsZero() {
		t.Error("Date is zero")
	}
	if !env.HasAttachments {
		t.Error("HasAttachments = false, want true")
	}
	if env.Size != 0 {
		t.Errorf("Size = %d, want 0", env.Size)
	}
}
