package compose

import (
	"bytes"
	"errors"
	"io"
	"mime"
	"strings"
	"testing"

	"github.com/emersion/go-message"

	"btcx/internal/mailparse"
	"btcx/internal/model"
)

// roundTripCases covers every MIME shape Build can emit; the message bodies
// are produced lazily so each test gets a clean copy.
var roundTripCases = []struct {
	name string
	msg  func() *model.Message
}{
	{"plain", func() *model.Message { return &model.Message{Plain: "simple body"} }},
	{"empty", func() *model.Message { return &model.Message{} }},
	{"html", func() *model.Message { return &model.Message{HTML: "<p>hello</p>"} }},
	{"alternative", func() *model.Message {
		return &model.Message{Plain: "text", HTML: "<p>text</p>"}
	}},
	{"attachment", func() *model.Message {
		return &model.Message{Plain: "text", Attachments: []model.Attachment{testPDF}}
	}},
	{"inline", func() *model.Message {
		return &model.Message{
			HTML:        `<img src="cid:logo@example.org">`,
			Attachments: []model.Attachment{testPNG},
		}
	}},
	{"all combined", func() *model.Message {
		return &model.Message{
			Plain:       "text",
			HTML:        `<p>text</p><img src="cid:logo@example.org">`,
			Attachments: []model.Attachment{testPNG, testPDF},
		}
	}},
	{"unicode bodies", func() *model.Message {
		return &model.Message{
			Plain: "Zażółć gęślą jaźń\n日本語のテキスト",
			HTML:  "<p>Grüße — 日本語</p>",
		}
	}},
}

var (
	testPDF = model.Attachment{
		Filename:    "żółć.pdf",
		ContentType: "application/pdf",
		Data:        []byte("%PDF-1.4\r\nbinary\x00data"),
	}
	testPNG = model.Attachment{
		Filename:    "logo.png",
		ContentType: "image/png",
		ContentID:   "logo@example.org",
		Data:        []byte{0x89, 'P', 'N', 'G', 0x00, 0xff},
		Inline:      true,
	}
)

// roundTripMessage gives a case message the standard envelope.
func roundTripMessage(m *model.Message) *model.Message {
	m.Header = baseMessage().Header.Clone()
	m.Header.Set("Subject", "round trip")
	return m
}

func TestRoundTrip(t *testing.T) {
	for _, tc := range roundTripCases {
		t.Run(tc.name, func(t *testing.T) {
			m := roundTripMessage(tc.msg())
			raw, err := Build(m, testOptions())
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			got, err := mailparse.ParseBytes(raw)
			if err != nil {
				t.Fatalf("mailparse.ParseBytes: %v", err)
			}
			// MIME canonicalises line endings to CRLF on the wire.
			if normalizeCRLF(got.Plain) != normalizeCRLF(m.Plain) {
				t.Errorf("Plain = %q, want %q", got.Plain, m.Plain)
			}
			if normalizeCRLF(got.HTML) != normalizeCRLF(m.HTML) {
				t.Errorf("HTML = %q, want %q", got.HTML, m.HTML)
			}
			if got.Subject() != m.Subject() {
				t.Errorf("Subject = %q, want %q", got.Subject(), m.Subject())
			}
			if from := got.From(); len(from) != 1 || from[0].Addr != "alice@example.org" {
				t.Errorf("From = %+v", from)
			}
			if to := got.To(); len(to) != 1 || to[0].Addr != "bob@example.net" {
				t.Errorf("To = %+v", to)
			}
			if len(got.Attachments) != len(m.Attachments) {
				t.Fatalf("attachments = %d, want %d", len(got.Attachments), len(m.Attachments))
			}
			for i, want := range m.Attachments {
				att := got.Attachments[i]
				if att.Filename != want.Filename {
					t.Errorf("attachment %d filename = %q, want %q", i, att.Filename, want.Filename)
				}
				if att.ContentType != want.ContentType {
					t.Errorf("attachment %d type = %q, want %q", i, att.ContentType, want.ContentType)
				}
				if att.ContentID != want.ContentID {
					t.Errorf("attachment %d cid = %q, want %q", i, att.ContentID, want.ContentID)
				}
				if att.Inline != want.Inline {
					t.Errorf("attachment %d inline = %v, want %v", i, att.Inline, want.Inline)
				}
				if !bytes.Equal(att.Data, want.Data) {
					t.Errorf("attachment %d data = %q, want %q", i, att.Data, want.Data)
				}
			}
		})
	}
}

// mimeTree lists the content types of raw, indented by nesting depth.
func mimeTree(t *testing.T, raw []byte) []string {
	t.Helper()
	e, err := message.Read(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("message.Read: %v\n%s", err, raw)
	}
	return treeOf(t, e, 0)
}

func treeOf(t *testing.T, e *message.Entity, depth int) []string {
	t.Helper()
	ct, _, _ := e.Header.ContentType()
	out := []string{strings.Repeat(" ", depth) + ct}
	mr := e.MultipartReader()
	if mr == nil {
		return out
	}
	for {
		p, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			return out
		}
		if err != nil {
			t.Fatalf("multipart read: %v", err)
		}
		out = append(out, treeOf(t, p, depth+1)...)
	}
}

// normalizeCRLF makes body comparisons independent of wire line endings.
func normalizeCRLF(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", "\n"), "\n", "\r\n")
}

func decodeWord(t *testing.T, v string) string {
	t.Helper()
	s, err := (&mime.WordDecoder{}).DecodeHeader(v)
	if err != nil {
		t.Fatalf("decode header %q: %v", v, err)
	}
	return s
}

func TestRoundTripNonASCIISubject(t *testing.T) {
	subjects := []string{
		"Zażółć gęślą jaźń",
		"Grüße aus Köln",
		"日本語の件名",
	}
	for _, subject := range subjects {
		m := baseMessage()
		m.Header.Set("Subject", subject)
		raw, err := Build(m, testOptions())
		if err != nil {
			t.Fatalf("Build(%q): %v", subject, err)
		}
		encoded := headerOf(t, raw).Get("Subject")
		if !strings.Contains(encoded, "=?utf-8?") && !strings.Contains(encoded, "=?UTF-8?") {
			t.Errorf("Subject %q not RFC 2047 encoded: %q", subject, encoded)
		}
		if got := decodeWord(t, encoded); got != subject {
			t.Errorf("decoded Subject = %q, want %q", got, subject)
		}
		got, err := mailparse.ParseBytes(raw)
		if err != nil {
			t.Fatalf("mailparse.ParseBytes: %v", err)
		}
		if got.Subject() != subject {
			t.Errorf("parsed Subject = %q, want %q", got.Subject(), subject)
		}
	}
}

func TestRoundTripAttachmentTraversalName(t *testing.T) {
	m := baseMessage()
	m.Attachments = []model.Attachment{{
		Filename:    "../../etc/passwd",
		ContentType: "application/octet-stream",
		Data:        []byte("x"),
	}}
	raw, err := Build(m, testOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got, err := mailparse.ParseBytes(raw)
	if err != nil {
		t.Fatalf("mailparse.ParseBytes: %v", err)
	}
	if len(got.Attachments) != 1 || got.Attachments[0].Filename != "passwd" {
		t.Fatalf("attachments = %+v, want filename passwd", got.Attachments)
	}
}
