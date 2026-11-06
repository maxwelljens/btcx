package ui

import (
	"strings"
	"testing"

	"btcx/internal/contract"
)

func TestGuessServers(t *testing.T) {
	imap, smtp := guessServers(" jane@Example.ORG ")
	if imap.Host != "imap.example.org" || imap.Port != 993 || imap.TLS != "tls" || imap.Username != "jane@Example.ORG" {
		t.Errorf("imap = %+v", imap)
	}
	if smtp.Host != "smtp.example.org" || smtp.Port != 587 || smtp.TLS != "starttls" {
		t.Errorf("smtp = %+v", smtp)
	}

	if imap, smtp := guessServers("not-an-address"); imap.Host != "" || smtp.Host != "" {
		t.Errorf("guessed from an address without @: %+v %+v", imap, smtp)
	}
}

func TestDeriveAccountName(t *testing.T) {
	cases := map[string]string{
		"jane@example.org":       "exampleorg",
		"JANE@EXAMPLE.ORG":       "exampleorg",
		"jane@sub.example.co.uk": "subexamplecouk",
		"jane@exa mple!.org":     "exampleorg",
		"jane@":                  "",
		"no-at-sign":             "",
	}
	for email, want := range cases {
		if got := deriveAccountName(email); got != want {
			t.Errorf("deriveAccountName(%q) = %q, want %q", email, got, want)
		}
	}
}

func TestValidateSpec(t *testing.T) {
	valid := contract.AccountSpec{
		Email:    "jane@example.org",
		IMAP:     contract.ServerSpec{Host: "imap.example.org", Port: 993, Username: "jane@example.org", TLS: "tls"},
		SMTP:     contract.ServerSpec{Host: "smtp.example.org", Port: 587, Username: "jane@example.org", TLS: "starttls"},
		Password: "secret",
	}

	cases := []struct {
		name   string
		mutate func(*contract.AccountSpec)
		want   string
	}{
		{"valid", func(*contract.AccountSpec) {}, ""},
		{"empty email", func(s *contract.AccountSpec) { s.Email = " " }, "enter your e-mail address"},
		{"bad email", func(s *contract.AccountSpec) { s.Email = "jane@" }, "valid e-mail"},
		{"two addresses", func(s *contract.AccountSpec) { s.Email = "a@x.org, b@y.org" }, "one e-mail"},
		{"empty host", func(s *contract.AccountSpec) { s.IMAP.Host = "" }, "incoming server host"},
		{"bad TLS", func(s *contract.AccountSpec) { s.SMTP.TLS = "plain" }, "server security"},
		{"bad port", func(s *contract.AccountSpec) { s.IMAP.Port = 70000 }, "incoming server port"},
		{"display name in email", func(s *contract.AccountSpec) { s.Email = "Jane <jane@example.org>" }, "bare e-mail"},
		{"both credentials", func(s *contract.AccountSpec) { s.PasswordCommand = "pass show x" }, "either a password or a password command"},
		{"no credentials", func(s *contract.AccountSpec) { s.Password = "" }, "either a password or a password command"},
		{"header injection", func(s *contract.AccountSpec) { s.IMAP.Host = "imap.example.org\r\nX: y" }, "line breaks"},
	}
	for _, c := range cases {
		spec := valid
		c.mutate(&spec)
		err := validateSpec(spec)
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want containing %q", c.name, err, c.want)
		}
	}
}

// TestTLSComboRoundTrip pins the index-to-mode mapping the account file gets.
func TestTLSComboRoundTrip(t *testing.T) {
	requireGTK(t)
	for active, want := range map[uint]string{0: "tls", 1: "starttls"} {
		c := tlsCombo(active)
		if got := c.Selected(); got != active {
			t.Errorf("tlsCombo(%d).Selected() = %d", active, got)
		}
		if got := tlsAt(c); got != want {
			t.Errorf("tlsCombo(%d) -> %q, want %q", active, got, want)
		}
	}
}

func TestGeneratedFilesText(t *testing.T) {
	got := generatedFilesText([]string{"config.toml", "accounts/example.toml.example"})
	want := "First launch — btcx created example configuration for you:\n" +
		"• config.toml\n• accounts/example.toml.example"
	if got != want {
		t.Errorf("generatedFilesText = %q, want %q", got, want)
	}
	if got := generatedFilesText(nil); !strings.HasPrefix(got, "First launch") || strings.Contains(got, "•") {
		t.Errorf("empty generatedFilesText = %q", got)
	}
}
