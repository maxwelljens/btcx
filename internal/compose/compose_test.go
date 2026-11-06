package compose

import (
	"bytes"
	stdmail "net/mail"
	"regexp"
	"strings"
	"testing"
	"time"

	"btcx/internal/mailparse"
	"btcx/internal/model"
)

var testDate = time.Date(2024, 5, 6, 7, 8, 9, 0, time.UTC)

func baseMessage() *model.Message {
	m := &model.Message{Header: model.NewHeader(), Plain: "hello body"}
	m.Header.Set("From", "Alice <alice@example.org>")
	m.Header.Set("To", "Bob <bob@example.net>")
	m.Header.Set("Cc", "carol@example.net")
	m.Header.Set("Bcc", "dave@example.net")
	m.Header.Set("Subject", "greetings")
	return m
}

func testOptions() Options {
	return Options{
		From:            model.Address{Name: "Alice", Addr: "alice@example.org"},
		MessageIDDomain: "example.org",
		Date:            testDate,
	}
}

func TestBuildStructure(t *testing.T) {
	pdf := model.Attachment{Filename: "doc.pdf", ContentType: "application/pdf", Data: []byte("%PDF")}
	png := model.Attachment{Filename: "logo.png", ContentType: "image/png", ContentID: "logo", Data: []byte{0x89, 'P'}, Inline: true}

	tests := []struct {
		name string
		msg  *model.Message
		want []string
	}{
		{"plain", &model.Message{Plain: "x"}, []string{"text/plain"}},
		{"empty body", &model.Message{}, []string{"text/plain"}},
		{"html only", &model.Message{HTML: "<p>x</p>"}, []string{"text/html"}},
		{"alternative", &model.Message{Plain: "x", HTML: "<p>x</p>"}, []string{
			"multipart/alternative", " text/plain", " text/html",
		}},
		{"attachment", &model.Message{Plain: "x", Attachments: []model.Attachment{pdf}}, []string{
			"multipart/mixed", " text/plain", " application/pdf",
		}},
		{"inline", &model.Message{HTML: `<img src="cid:logo">`, Attachments: []model.Attachment{png}}, []string{
			"multipart/related", " text/html", " image/png",
		}},
		{"alternative and inline", &model.Message{Plain: "x", HTML: "<p>x</p>", Attachments: []model.Attachment{png}}, []string{
			"multipart/related", " multipart/alternative", "  text/plain", "  text/html", " image/png",
		}},
		{"all combined", &model.Message{Plain: "x", HTML: "<p>x</p>", Attachments: []model.Attachment{png, pdf}}, []string{
			"multipart/mixed", " multipart/related", "  multipart/alternative",
			"   text/plain", "   text/html", "  image/png", " application/pdf",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := Build(tc.msg, testOptions())
			if err != nil {
				t.Fatalf("Build: %v", err)
			}
			got := mimeTree(t, raw)
			if strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("structure = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBuildHeaders(t *testing.T) {
	raw, err := Build(baseMessage(), testOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	h := headerOf(t, raw)

	want := map[string]string{
		"From":         `"Alice" <alice@example.org>`,
		"To":           `"Bob" <bob@example.net>`,
		"Cc":           "carol@example.net",
		"Bcc":          "dave@example.net",
		"Subject":      "greetings",
		"Date":         "Mon, 06 May 2024 07:08:09 +0000",
		"MIME-Version": "1.0",
		"X-Mailer":     "btcx",
	}
	for k, v := range want {
		if got := h.Get(k); got != v {
			t.Errorf("%s = %q, want %q", k, got, v)
		}
	}
	if !regexp.MustCompile(`^<[0-9]+\.[0-9a-f]{16}@example\.org>$`).MatchString(h.Get("Message-ID")) {
		t.Errorf("Message-ID = %q, want <nonce.hex@example.org>", h.Get("Message-ID"))
	}
}

func TestBuildCarriesThreadAndCustomHeaders(t *testing.T) {
	m := baseMessage()
	m.Header.Set("In-Reply-To", "<parent@example.org>")
	m.Header.Set("References", "<root@example.org> <parent@example.org>")
	m.Header.Set("X-Custom", "one")
	m.Header.Set("Content-Type", "text/evil")
	opts := testOptions()
	opts.UserAgent = "btcx/test"

	raw, err := Build(m, opts)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	h := headerOf(t, raw)
	if got := h.Get("In-Reply-To"); got != "<parent@example.org>" {
		t.Errorf("In-Reply-To = %q", got)
	}
	if got := h.Get("References"); got != "<root@example.org> <parent@example.org>" {
		t.Errorf("References = %q", got)
	}
	if got := h.Get("X-Custom"); got != "one" {
		t.Errorf("X-Custom = %q", got)
	}
	if got := h.Get("X-Mailer"); got != "btcx/test" {
		t.Errorf("X-Mailer = %q", got)
	}
	if got := h.Get("Content-Type"); !strings.HasPrefix(got, "text/plain") {
		t.Errorf("Content-Type = %q, want btcx-owned text/plain", got)
	}
}

func TestMessageIDUnique(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		raw, err := Build(baseMessage(), testOptions())
		if err != nil {
			t.Fatalf("Build: %v", err)
		}
		id := headerOf(t, raw).Get("Message-ID")
		if seen[id] {
			t.Fatalf("duplicate Message-ID %q", id)
		}
		seen[id] = true
	}
	raw, err := Build(baseMessage(), Options{From: model.Address{Addr: "a@b.c"}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if id := headerOf(t, raw).Get("Message-ID"); !strings.HasSuffix(id, "@localhost>") {
		t.Errorf("Message-ID = %q, want default localhost domain", id)
	}
}

func TestBuildRejectsInjection(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(m *model.Message, opts *Options)
	}{
		{"missing from", func(m *model.Message, opts *Options) { opts.From = model.Address{} }},
		{"from name CRLF", func(m *model.Message, opts *Options) {
			opts.From.Name = "Alice\r\nBcc: evil@example.org"
		}},
		{"from addr CRLF", func(m *model.Message, opts *Options) {
			opts.From.Addr = "alice@example.org\nBcc: evil@example.org"
		}},
		{"from name NUL", func(m *model.Message, opts *Options) { opts.From.Name = "Ali\x00ce" }},
		{"subject CRLF", func(m *model.Message, opts *Options) {
			m.Header.Set("Subject", "hi\r\nBcc: evil@example.org")
		}},
		{"subject NUL", func(m *model.Message, opts *Options) { m.Header.Set("Subject", "hi\x00") }},
		{"to CRLF", func(m *model.Message, opts *Options) {
			m.Header.Set("To", "bob@example.net\r\nBcc: evil@example.org")
		}},
		{"cc CRLF", func(m *model.Message, opts *Options) {
			m.Header.Set("Cc", "carol@example.net\nBcc: evil@example.org")
		}},
		{"bcc NUL", func(m *model.Message, opts *Options) { m.Header.Set("Bcc", "dave@example.net\x00") }},
		{"custom header CRLF", func(m *model.Message, opts *Options) {
			m.Header.Set("X-Custom", "ok\r\nBcc: evil@example.org")
		}},
		{"custom header name injection", func(m *model.Message, opts *Options) {
			m.Header.Set("X-Custom\r\nBcc", "evil@example.org")
		}},
		{"message id domain space", func(m *model.Message, opts *Options) { opts.MessageIDDomain = "evil domain" }},
		{"user agent CRLF", func(m *model.Message, opts *Options) { opts.UserAgent = "btcx\r\nX: y" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := baseMessage()
			opts := testOptions()
			tc.mutate(m, &opts)
			raw, err := Build(m, opts)
			if err == nil {
				t.Fatalf("Build succeeded, want error; raw:\n%s", raw)
			}
		})
	}
}

func TestBuildSanitizesFilename(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"report.pdf", "report.pdf"},
		{"../../etc/passwd", "passwd"},
		{`..\..\windows\system32\evil.exe`, "evil.exe"},
		{"evil\r\nX-Injected: 1.txt", "evilX-Injected: 1.txt"},
		{"", ""},
		{"..", ""},
		{"/etc/passwd", "passwd"},
		{"nul\x00byte.txt", "nulbyte.txt"},
	}
	for _, tc := range tests {
		if got := sanitizeFilename(tc.in); got != tc.want {
			t.Errorf("sanitizeFilename(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	m := baseMessage()
	m.Attachments = []model.Attachment{{
		Filename:    "../../etc/passwd\r\nX-Injected: yes",
		ContentType: "application/octet-stream",
		Data:        []byte("x"),
	}}
	raw, err := Build(m, testOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if h := headerOf(t, raw); h.Get("X-Injected") != "" {
		t.Fatalf("filename injected a header:\n%s", raw)
	}
	got, err := mailparse.ParseBytes(raw)
	if err != nil {
		t.Fatalf("mailparse.ParseBytes: %v", err)
	}
	if len(got.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(got.Attachments))
	}
	name := got.Attachments[0].Filename
	if strings.ContainsAny(name, "\r\n") || strings.Contains(name, "..") {
		t.Errorf("filename = %q, want sanitized", name)
	}
	if !strings.HasPrefix(name, "passwd") {
		t.Errorf("filename = %q, want passwd prefix", name)
	}
}

func TestBodyCannotEscapeIntoHeaders(t *testing.T) {
	bodies := []string{
		"line one\r\n.\r\nline two",
		"From nobody@example.org Mon Jan 1 00:00:00 2024\nbody",
		"\r\nBcc: evil@example.org\r\n\r\ninjected",
		"normal\r\n\r\nX-Injected: yes",
	}
	for _, body := range bodies {
		m := &model.Message{Plain: body}
		raw, err := Build(m, testOptions())
		if err != nil {
			t.Fatalf("Build(%q): %v", body, err)
		}
		h := headerOf(t, raw)
		if h.Get("Bcc") != "" || h.Get("X-Injected") != "" {
			t.Fatalf("body injected a header:\n%s", raw)
		}
		got, err := mailparse.ParseBytes(raw)
		if err != nil {
			t.Fatalf("mailparse.ParseBytes: %v", err)
		}
		if normalizeCRLF(got.Plain) != normalizeCRLF(body) {
			t.Errorf("body round-trip = %q, want %q", got.Plain, body)
		}
	}
}

func TestValidateHeader(t *testing.T) {
	ok := []string{"", "plain", "Zażółć gęślą jaźń", "a\tb"}
	for _, v := range ok {
		if err := ValidateHeader(v); err != nil {
			t.Errorf("ValidateHeader(%q) = %v, want nil", v, err)
		}
	}
	bad := []string{"a\rb", "a\nb", "a\r\nb", "a\x00b"}
	for _, v := range bad {
		if err := ValidateHeader(v); err == nil {
			t.Errorf("ValidateHeader(%q) = nil, want error", v)
		}
	}
}

func TestReply(t *testing.T) {
	orig := baseMessage()
	orig.Plain = "line1\n\nline3"
	orig.Header.Set("Date", testDate.Format(time.RFC1123Z))
	orig.Header.Set("Message-ID", "<orig@example.org>")
	orig.Header.Set("References", "<root@example.org>")

	tests := []struct {
		name    string
		subject string
		want    string
	}{
		{"plain", "x", "Re: x"},
		{"already replied", "Re: x", "Re: x"},
		{"double replied", "Re: Re: x", "Re: x"},
		{"lowercase", "re: x", "Re: x"},
		{"empty", "", "Re: "},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			o := *orig
			o.Header = orig.Header.Clone()
			o.Header.Set("Subject", tc.subject)
			got := Reply(&o, model.Address{Name: "Bob", Addr: "bob@example.net"}, "thanks")
			if got.Subject() != tc.want {
				t.Errorf("Subject = %q, want %q", got.Subject(), tc.want)
			}
		})
	}

	reply := Reply(orig, model.Address{Name: "Bob", Addr: "bob@example.net"}, "thanks")
	if got, want := reply.Header.Get("In-Reply-To"), "<orig@example.org>"; got != want {
		t.Errorf("In-Reply-To = %q, want %q", got, want)
	}
	if got, want := reply.Header.Get("References"), "<root@example.org> <orig@example.org>"; got != want {
		t.Errorf("References = %q, want %q", got, want)
	}
	if got := reply.Header.Get("To"); got != `"Alice" <alice@example.org>` {
		t.Errorf("To = %q, want original From", got)
	}

	wantPlain := "thanks\n\nOn Mon, 06 May 2024 07:08:09 +0000, \"Alice\" <alice@example.org> wrote:\n" +
		"> line1\n> \n> line3"
	if reply.Plain != wantPlain {
		t.Errorf("quoted body:\n%q\nwant:\n%q", reply.Plain, wantPlain)
	}

	raw, err := Build(reply, testOptions())
	if err != nil {
		t.Fatalf("Build reply: %v", err)
	}
	h := headerOf(t, raw)
	if h.Get("In-Reply-To") != "<orig@example.org>" || h.Get("References") != "<root@example.org> <orig@example.org>" {
		t.Errorf("thread headers lost: %v", h)
	}
	if h.Get("To") != `"Alice" <alice@example.org>` {
		t.Errorf("To = %q", h.Get("To"))
	}
}

func TestReplyPrefersReplyTo(t *testing.T) {
	orig := baseMessage()
	orig.Header.Set("Reply-To", "List <list@example.org>")
	reply := Reply(orig, model.Address{Addr: "bob@example.net"}, "")
	if got := reply.Header.Get("To"); got != `"List" <list@example.org>` {
		t.Errorf("To = %q, want Reply-To", got)
	}
}

func TestReplyChainGrowth(t *testing.T) {
	orig := baseMessage()
	orig.Header.Set("Message-ID", "<c@example.org>")
	orig.Header.Set("References", "<a@example.org> <b@example.org> <c@example.org>")
	reply := Reply(orig, model.Address{Addr: "bob@example.net"}, "")
	if got := reply.Header.Get("References"); got != "<a@example.org> <b@example.org> <c@example.org>" {
		t.Errorf("References = %q, want unchanged chain (orig already last)", got)
	}
}

func TestForward(t *testing.T) {
	orig := baseMessage()
	orig.Plain = "original body"
	orig.Header.Set("Date", testDate.Format(time.RFC1123Z))

	fwd := Forward(orig, model.Address{Addr: "bob@example.net"}, "see below")
	if got, want := fwd.Subject(), "Fwd: greetings"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
	again := Forward(&model.Message{Header: func() model.Header {
		h := model.NewHeader()
		h.Set("Subject", "Fwd: Re: greetings")
		return h
	}()}, model.Address{Addr: "bob@example.net"}, "")
	if got, want := again.Subject(), "Fwd: Re: greetings"; got != want {
		t.Errorf("Subject = %q, want %q", got, want)
	}
	if len(fwd.Attachments) != 1 {
		t.Fatalf("attachments = %d, want 1", len(fwd.Attachments))
	}
	att := fwd.Attachments[0]
	if att.ContentType != "message/rfc822" {
		t.Errorf("attachment ContentType = %q", att.ContentType)
	}
	if len(att.Data) == 0 {
		t.Error("forwarded message attachment is empty")
	}
	for _, want := range []string{
		"see below", "---------- Forwarded message ----------",
		`From: "Alice" <alice@example.org>`, "Subject: greetings", "original body",
	} {
		if !strings.Contains(fwd.Plain, want) {
			t.Errorf("forward body missing %q:\n%s", want, fwd.Plain)
		}
	}

	raw, err := Build(fwd, testOptions())
	if err != nil {
		t.Fatalf("Build forward: %v", err)
	}
	tree := mimeTree(t, raw)
	want := []string{"multipart/mixed", " text/plain", " message/rfc822"}
	if strings.Join(tree, "|") != strings.Join(want, "|") {
		t.Fatalf("structure = %q, want %q", tree, want)
	}

	got, err := mailparse.ParseBytes(raw)
	if err != nil {
		t.Fatalf("mailparse.ParseBytes: %v", err)
	}
	var inner *model.Message
	for _, att := range got.Attachments {
		if att.ContentType == "message/rfc822" {
			inner, err = mailparse.ParseBytes(att.Data)
			if err != nil {
				t.Fatalf("parse attached original: %v", err)
			}
		}
	}
	if inner == nil {
		t.Fatal("no message/rfc822 attachment found")
	}
	if inner.Subject() != "greetings" || inner.Plain != "original body" {
		t.Errorf("attached original = subject %q body %q", inner.Subject(), inner.Plain)
	}
	if d, ok := inner.Date(); !ok || !d.Equal(testDate) {
		t.Errorf("attached original Date = %v, %v; want %v", d, ok, testDate)
	}
}

func TestBuildNilMessage(t *testing.T) {
	if _, err := Build(nil, testOptions()); err == nil {
		t.Fatal("Build(nil) succeeded, want error")
	}
}

// headerOf returns the top-level header parsed with net/mail.
func headerOf(t *testing.T, raw []byte) stdmail.Header {
	t.Helper()
	msg, err := stdmail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("net/mail: %v\n%s", err, raw)
	}
	return msg.Header
}

func TestStructureContentType(t *testing.T) {
	raw, err := Build(&model.Message{Plain: "x"}, testOptions())
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := headerOf(t, raw).Get("Content-Type"); !strings.EqualFold(got, "text/plain; charset=UTF-8") {
		t.Errorf("Content-Type = %q, want text/plain; charset=UTF-8", got)
	}
}
