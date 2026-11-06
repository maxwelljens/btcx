package model

import "testing"

func TestAddressString(t *testing.T) {
	tests := []struct {
		name string
		addr Address
		want string
	}{
		{"empty", Address{}, ""},
		{"bare addr", Address{Addr: "a@b.c"}, "a@b.c"},
		// net/mail quotes display names conservatively; still valid RFC 5322.
		{"simple name", Address{Name: "Alice", Addr: "a@b.c"}, `"Alice" <a@b.c>`},
		{"quoted name", Address{Name: "Doe, John", Addr: "j@d.e"}, `"Doe, John" <j@d.e>`},
		{"name only", Address{Name: "Alice"}, `"Alice"`},
		// Hostile input must never inject header fields.
		{"crlf injection", Address{Name: "A\r\nBcc: evil@x.example", Addr: "a@b.c"}, `"ABcc: evil@x.example" <a@b.c>`},
		{"crlf in addr", Address{Addr: "a@b.c\r\nX-Injected: 1"}, "a@b.cX-Injected: 1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.addr.String(); got != tt.want {
				t.Errorf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseAddressList(t *testing.T) {
	got, err := ParseAddressList(`Alice <alice@example.org>, bob@example.org`)
	if err != nil {
		t.Fatal(err)
	}
	want := []Address{
		{Name: "Alice", Addr: "alice@example.org"},
		{Addr: "bob@example.org"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d addresses, want %d: %#v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("address %d = %#v, want %#v", i, got[i], want[i])
		}
	}

	// Empty input yields nil without error.
	got, err = ParseAddressList("   ")
	if err != nil || got != nil {
		t.Errorf("empty input: got %#v, %v; want nil, nil", got, err)
	}

	// Invalid input is an error, not a panic.
	if _, err := ParseAddressList("<not-an-address"); err == nil {
		t.Error("expected error for malformed address")
	}
}

func TestFormatAddressList(t *testing.T) {
	got := FormatAddressList([]Address{
		{Name: "Alice", Addr: "a@b.c"},
		{}, // skipped
		{Addr: "x@y.z"},
	})
	want := `"Alice" <a@b.c>, x@y.z`
	if got != want {
		t.Errorf("FormatAddressList = %q, want %q", got, want)
	}
}

func TestHeaderBasics(t *testing.T) {
	var h Header
	h.Add("content-type", "text/plain")
	h.Add("Content-Type", "text/html")
	h.Add("X-Custom", "one")

	if got := h.Get("CONTENT-TYPE"); got != "text/plain" {
		t.Errorf("Get = %q, want %q", got, "text/plain")
	}
	if got := h.Values("Content-Type"); len(got) != 2 {
		t.Errorf("Values = %v, want 2 entries", got)
	}
	if !h.Has("x-custom") {
		t.Error("Has(x-custom) = false, want true")
	}

	h.Set("Content-Type", "multipart/mixed")
	if got := h.Values("Content-Type"); len(got) != 1 || got[0] != "multipart/mixed" {
		t.Errorf("after Set, Values = %v", got)
	}

	h.Delete("x-custom")
	if h.Has("x-custom") {
		t.Error("Delete left the field in place")
	}

	if got := h.Get("missing"); got != "" {
		t.Errorf("Get(missing) = %q, want empty", got)
	}
}

func TestHeaderOrderPreserved(t *testing.T) {
	var h Header
	h.Add("B", "2")
	h.Add("A", "1")
	h.Add("C", "3")
	got := h.Fields()
	if got[0].Key != "B" || got[1].Key != "A" || got[2].Key != "C" {
		t.Errorf("field order not preserved: %v", got)
	}
}

func TestHeaderClone(t *testing.T) {
	var h Header
	h.Add("Subject", "orig")
	c := h.Clone()
	c.Set("Subject", "changed")
	if h.Get("Subject") != "orig" {
		t.Error("Clone shares backing storage with original")
	}
}

func TestCanonicalKey(t *testing.T) {
	tests := map[string]string{
		"content-type": "Content-Type",
		"CONTENT-TYPE": "Content-Type",
		"mime-version": "Mime-Version",
		"X-Mailer":     "X-Mailer",
		"in-reply-to":  "In-Reply-To",
	}
	for in, want := range tests {
		if got := canonicalKey(in); got != want {
			t.Errorf("canonicalKey(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMessageHelpers(t *testing.T) {
	m := &Message{Header: NewHeader()}
	m.Header.Set("From", "Alice <alice@example.org>")
	m.Header.Set("To", "Bob <bob@example.org>, carol@example.org")
	m.Header.Set("Subject", "Hello")
	m.Header.Set("Message-ID", "<msg1@example.org>")
	m.Header.Set("In-Reply-To", "<prev@example.org>")
	m.Header.Set("References", "<root@example.org> <prev@example.org>")

	if got := m.Subject(); got != "Hello" {
		t.Errorf("Subject = %q", got)
	}
	if got := m.MessageID(); got != "msg1@example.org" {
		t.Errorf("MessageID = %q", got)
	}
	if got := m.InReplyTo(); got != "prev@example.org" {
		t.Errorf("InReplyTo = %q", got)
	}
	if got := m.References(); len(got) != 2 || got[0] != "root@example.org" || got[1] != "prev@example.org" {
		t.Errorf("References = %v", got)
	}
	if !m.IsReplyTo("prev@example.org") {
		t.Error("IsReplyTo(prev) = false")
	}
	if m.IsReplyTo("other@example.org") {
		t.Error("IsReplyTo(other) = true")
	}

	from := m.From()
	if len(from) != 1 || from[0].Addr != "alice@example.org" {
		t.Errorf("From = %v", from)
	}
	to := m.To()
	if len(to) != 2 {
		t.Errorf("To = %v", to)
	}

	// Date parses and falls back safely.
	m.Header.Set("Date", "not a date")
	if _, ok := m.Date(); ok {
		t.Error("Date() reported success for a malformed date")
	}
	m.Header.Set("Date", "Mon, 02 Jan 2006 15:04:05 -0700")
	if d, ok := m.Date(); !ok || d.Year() != 2006 {
		t.Errorf("Date = %v, %v", d, ok)
	}
	m.Header.Delete("Date")
	if _, ok := m.Date(); ok {
		t.Error("Date() reported success for a missing date")
	}
}

func TestRefString(t *testing.T) {
	r := Ref{Mailbox: "INBOX", UIDValidity: 42, UID: 7}
	if got := r.String(); got != "INBOX/42/7" {
		t.Errorf("Ref.String = %q", got)
	}
}

func TestMailboxRole(t *testing.T) {
	tests := []struct {
		attrs []string
		want  string
	}{
		{[]string{`\Sent`}, "sent"},
		{[]string{`\Drafts`, `\Marked`}, "drafts"},
		{[]string{`\Junk`}, "junk"},
		{[]string{`\Noselect`}, ""},
		{nil, ""},
		{[]string{`\Sent`, `\Drafts`}, "sent"}, // first match wins
	}
	for _, tt := range tests {
		m := Mailbox{Name: "x", Attributes: tt.attrs}
		if got := m.Role(); got != tt.want {
			t.Errorf("Role(%v) = %q, want %q", tt.attrs, got, tt.want)
		}
	}
}
