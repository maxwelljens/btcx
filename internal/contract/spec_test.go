package contract

import (
	"errors"
	"strings"
	"testing"
)

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
		if got := DeriveAccountName(email); got != want {
			t.Errorf("DeriveAccountName(%q) = %q, want %q", email, got, want)
		}
	}
}

func TestNormalizeDefaults(t *testing.T) {
	spec := AccountSpec{
		Email:    " Max.Mustermann@Mail.Example-1.org ",
		IMAP:     ServerSpec{Host: " imap.example.org ", TLS: "TLS"},
		SMTP:     ServerSpec{Host: "smtp.example.org", TLS: "STARTTLS"},
		Password: "s3cr3t",
	}
	if err := spec.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if spec.Email != "Max.Mustermann@Mail.Example-1.org" {
		t.Errorf("email = %q, want it trimmed", spec.Email)
	}
	if spec.Name != "mailexample-1org" {
		t.Errorf("name = %q, want mailexample-1org", spec.Name)
	}
	if spec.DisplayName != "Max.Mustermann" {
		t.Errorf("display name = %q, want the local part", spec.DisplayName)
	}
	if spec.IMAP.Port != 993 || spec.IMAP.TLS != "tls" {
		t.Errorf("imap = %+v, want 993/tls", spec.IMAP)
	}
	if spec.SMTP.Port != 587 || spec.SMTP.TLS != "starttls" {
		t.Errorf("smtp = %+v, want 587/starttls", spec.SMTP)
	}
	if spec.IMAP.Username != spec.Email || spec.SMTP.Username != spec.Email {
		t.Errorf("usernames = %q/%q, want the address", spec.IMAP.Username, spec.SMTP.Username)
	}

	// An explicit port and username win over the defaults.
	spec = AccountSpec{
		Name:     "work",
		Email:    "jane@example.org",
		IMAP:     ServerSpec{Host: "imap.example.org", Port: 1993, Username: "j", TLS: "tls"},
		SMTP:     ServerSpec{Host: "smtp.example.org", TLS: "tls"},
		Password: "secret",
	}
	if err := spec.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	if spec.IMAP.Port != 1993 || spec.IMAP.Username != "j" || spec.SMTP.Port != 465 {
		t.Errorf("explicit values not kept: %+v %+v", spec.IMAP, spec.SMTP)
	}
}

func TestNormalizeRejectsInvalidSpecs(t *testing.T) {
	valid := AccountSpec{
		Email:    "jane@example.org",
		IMAP:     ServerSpec{Host: "imap.example.org", Port: 993, Username: "jane@example.org", TLS: "tls"},
		SMTP:     ServerSpec{Host: "smtp.example.org", Port: 587, Username: "jane@example.org", TLS: "starttls"},
		Password: "secret",
	}

	cases := []struct {
		name   string
		mutate func(*AccountSpec)
		field  string
		want   string
	}{
		{"valid", func(*AccountSpec) {}, "", ""},
		{"empty email", func(s *AccountSpec) { s.Email = " " }, "email", "email is required"},
		{"bad email", func(s *AccountSpec) { s.Email = "jane@" }, "email", "valid e-mail"},
		{"two addresses", func(s *AccountSpec) { s.Email = "a@x.org, b@y.org" }, "email", "one e-mail"},
		{"empty host", func(s *AccountSpec) { s.IMAP.Host = "" }, "imap.host", "imap.host is required"},
		{"bad TLS", func(s *AccountSpec) { s.SMTP.TLS = "plain" }, "smtp.tls", "must be"},
		{"bad port", func(s *AccountSpec) { s.IMAP.Port = 70000 }, "imap.port", "must be between 1 and 65535"},
		{"display name in email", func(s *AccountSpec) { s.Email = "Jane <jane@example.org>" }, "email", "bare e-mail"},
		{"both credentials", func(s *AccountSpec) { s.PasswordCommand = "pass show x" }, "password", "either password or password_command"},
		{"no credentials", func(s *AccountSpec) { s.Password = "" }, "password", "either password or password_command"},
		{"header injection", func(s *AccountSpec) { s.IMAP.Host = "imap.example.org\r\nX: y" }, "imap.host", "line breaks"},
		{"bad name", func(s *AccountSpec) { s.Name = "Max Mustermann" }, "name", "lowercase letters"},
	}
	for _, c := range cases {
		spec := valid
		c.mutate(&spec)
		err := spec.Normalize()
		if c.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", c.name, err)
			}
			continue
		}
		var field *FieldError
		if !errors.As(err, &field) {
			t.Errorf("%s: error = %v, want a *FieldError", c.name, err)
			continue
		}
		if field.Field != c.field {
			t.Errorf("%s: field = %q, want %q", c.name, field.Field, c.field)
		}
		if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: error = %v, want containing %q", c.name, err, c.want)
		}
	}
}
