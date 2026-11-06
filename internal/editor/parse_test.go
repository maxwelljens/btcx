package editor

import (
	"strings"
	"testing"
)

func TestParseDraft(t *testing.T) {
	tests := []struct {
		name    string
		text    string
		subject string
		to      string
		plain   string
		bcc     bool
		wantErr bool
	}{
		{
			name:    "simple CRLF",
			text:    "To: a@x\r\nSubject: hi\r\n\r\nbody",
			subject: "hi",
			to:      "a@x",
			plain:   "body",
		},
		{
			name:    "simple LF",
			text:    "To: a@x\nSubject: hi\n\nbody",
			subject: "hi",
			to:      "a@x",
			plain:   "body",
		},
		{
			name:    "folded subject",
			text:    "Subject: Hello\r\n World\r\n\r\nx",
			subject: "Hello World",
			plain:   "x",
		},
		{
			name:    "multi line body",
			text:    "Subject: s\n\nline one\nline two\n",
			subject: "s",
			plain:   "line one\nline two",
		},
		{
			name:    "empty body",
			text:    "Subject: hi\n\n",
			subject: "hi",
			plain:   "",
		},
		{
			name:    "only headers without blank line",
			text:    "Subject: hi\nTo: b@x",
			subject: "hi",
			to:      "b@x",
			plain:   "",
		},
		{
			name:  "empty text",
			text:  "",
			plain: "",
		},
		{
			name:    "case insensitive keys",
			text:    "to: a@x\nsubject: hi\n\nbody",
			subject: "hi",
			to:      "a@x",
			plain:   "body",
		},
		{
			name:    "folded injection stays in the subject",
			text:    "Subject: hi\n\tBcc: evil@x\n\nbody",
			subject: "hi Bcc: evil@x",
			plain:   "body",
		},
		{
			name:    "unknown headers dropped, known kept",
			text:    "Subject: hi\nContent-Type: text/html\nBcc: c@x\n\nbody",
			subject: "hi",
			plain:   "body",
			bcc:     true,
		},
		{
			name:    "malformed header line",
			text:    "garbage\n\nbody",
			wantErr: true,
		},
		{
			name:    "binary junk",
			text:    "\x00\x01\x02junk\n\nbody",
			wantErr: true,
		},
		{
			name:    "continuation without header",
			text:    " more\n\nbody",
			wantErr: true,
		},
		{
			name:    "huge first line",
			text:    strings.Repeat("A", maxHeaderLine+1) + "\n\nbody",
			wantErr: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			msg, err := ParseDraft(tc.text)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseDraft(%q) succeeded, want error", tc.text)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseDraft: %v", err)
			}
			if got := msg.Header.Get("Subject"); got != tc.subject {
				t.Fatalf("Subject = %q, want %q", got, tc.subject)
			}
			if got := msg.Header.Get("To"); got != tc.to {
				t.Fatalf("To = %q, want %q", got, tc.to)
			}
			if got := msg.Header.Get("Bcc"); (got != "") != tc.bcc {
				t.Fatalf("Bcc = %q, want present=%v", got, tc.bcc)
			}
			if msg.Plain != tc.plain {
				t.Fatalf("Plain = %q, want %q", msg.Plain, tc.plain)
			}
			if msg.HTML != "" || len(msg.Attachments) != 0 {
				t.Fatalf("draft produced HTML or attachments")
			}
			if msg.Header.Has("Content-Type") {
				t.Fatalf("unknown header reached the message")
			}
		})
	}
}

func TestParseDraftLongBodyAllowed(t *testing.T) {
	msg, err := ParseDraft("Subject: s\n\n" + strings.Repeat("b", maxHeaderLine+1))
	if err != nil {
		t.Fatalf("ParseDraft: %v", err)
	}
	if len(msg.Plain) != maxHeaderLine+1 {
		t.Fatalf("body length = %d", len(msg.Plain))
	}
}

// TestCleanValue covers the defence-in-depth strip of line breaks that could
// otherwise start a new header downstream.
func TestCleanValue(t *testing.T) {
	got := cleanValue("  a\r\nBcc: evil@x\x00  ")
	if got != "aBcc: evil@x" {
		t.Fatalf("cleanValue = %q", got)
	}
}

func FuzzParseDraft(f *testing.F) {
	f.Add("To: a@x\r\nSubject: hi\r\n\r\nbody")
	f.Add("")
	f.Add("Subject: x\r\n\tfolded\r\n\r\n\x00")
	f.Fuzz(func(t *testing.T, s string) {
		msg, err := ParseDraft(s)
		if err == nil && msg == nil {
			t.Fatal("nil message without error")
		}
		if err == nil {
			for _, f := range msg.Header.Fields() {
				if strings.ContainsAny(f.Value, "\r\n") {
					t.Fatalf("header %s retained a line break", f.Key)
				}
			}
		}
	})
}
