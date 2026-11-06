package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestFirstRun(t *testing.T) {
	tests := []struct {
		name  string
		files map[string]string
		dir   bool // create the directory (empty files map still creates nothing)
		want  bool
	}{
		{name: "missing dir", want: true},
		{name: "empty dir", dir: true, want: true},
		{name: "only config", dir: true, files: map[string]string{"config.toml": ""}, want: false},
		{name: "only account", dir: true, files: map[string]string{"accounts/x.toml": "email = \"a@b.c\"\n"}, want: false},
		{name: "both", dir: true, files: map[string]string{
			"config.toml":         "",
			"accounts/x.toml":     "email = \"a@b.c\"\n",
			"accounts/y.toml":     "email = \"a@b.c\"\n",
			"accounts/README":     "not an account",
			"accounts/.x.toml":    "email = \"a@b.c\"\n",
			"accounts/ex.example": "ignored",
		}, want: false},
		{name: "only example files", dir: true, files: map[string]string{
			ExampleAccountPath: "commented",
			ExampleScriptPath:  "-- lua",
		}, want: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "btcx")
			if tt.dir {
				if err := os.MkdirAll(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for rel, body := range tt.files {
				writeFile(t, filepath.Join(dir, filepath.FromSlash(rel)), body, 0o600)
			}
			got, err := FirstRun(dir)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("FirstRun() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestBootstrap(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "btcx") // parents do not exist yet
	created, err := Bootstrap(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{ExampleAccountPath, ExampleConfigPath, ExampleScriptPath}
	if !reflect.DeepEqual(created, want) {
		t.Fatalf("created = %v, want %v", created, want)
	}

	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("dir mode = %o, want 700", perm)
	}
	for _, rel := range want {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("%s mode = %o, want 600", rel, perm)
		}
	}

	// Fully commented examples must still be valid TOML.
	for _, rel := range []string{ExampleConfigPath, ExampleAccountPath} {
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		var v map[string]any
		if _, err := toml.Decode(string(body), &v); err != nil {
			t.Errorf("%s does not parse: %v", rel, err)
		}
		if len(v) != 0 {
			t.Errorf("%s defines %d keys; the example must ship fully commented", rel, len(v))
		}
	}

	// A freshly bootstrapped directory loads clean: no warnings at all.
	if _, warnings, err := LoadDir(dir); err != nil {
		t.Fatal(err)
	} else if len(warnings) != 0 {
		t.Errorf("LoadDir after Bootstrap warns: %+v", warnings)
	}

	// A second run creates nothing and never clobbers user edits.
	edited := "# my own config\n"
	writeFile(t, filepath.Join(dir, ExampleConfigPath), edited, 0o600)
	created, err = Bootstrap(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(created) != 0 {
		t.Errorf("second Bootstrap created %v, want nothing", created)
	}
	body, err := os.ReadFile(filepath.Join(dir, ExampleConfigPath))
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != edited {
		t.Errorf("config.toml was overwritten: %q", body)
	}
}

func TestExampleContent(t *testing.T) {
	for _, rel := range []string{ExampleAccountPath, ExampleConfigPath, ExampleScriptPath} {
		body, err := ExampleContent(rel)
		if err != nil || len(body) == 0 {
			t.Errorf("ExampleContent(%q) = %d bytes, %v", rel, len(body), err)
		}
	}
	if _, err := ExampleContent("accounts/nope.toml"); err == nil {
		t.Error("ExampleContent accepted an unknown path")
	}
}

func TestTemplateQuality(t *testing.T) {
	markers := map[string][]string{
		ExampleAccountPath: {"imap.gmail.com", "smtp.gmail.com", "outlook.office365.com", "imap.fastmail.com",
			"imap.mail.yahoo.com", "password_command", "starttls", "chmod 600", "App Password", "ca_file",
			"insecure_skip_verify", "[sent]", "signature", "IMAP settings"},
		ExampleConfigPath: {"theme", "editor = \"alacritty -e nvim {file}\"", "watch", "block_remote_content",
			"script_dir", "script_enabled", "log_level", "tracking"},
		ExampleScriptPath: {"on_startup", "on_new_mail", "before_send", "after_send", "btcx.log", "btcx.version",
			"envelope.from[1]", "flags", "no os, io, require", "instruction"},
	}
	for rel, wants := range markers {
		body, err := ExampleContent(rel)
		if err != nil {
			t.Fatal(err)
		}
		text := string(body)
		for _, want := range wants {
			if !strings.Contains(text, want) {
				t.Errorf("%s no longer mentions %q; the tutorial lost content", rel, want)
			}
		}
	}
}

func roundTripAccount(name string) Account {
	return Account{
		Name:        name,
		DisplayName: "Maxwell Jensen",
		Email:       "max@example.org",
		RealName:    "Maxwell",
		IMAP: Server{
			Host:     "imap.example.org",
			Port:     993,
			Username: "max@example.org",
			Password: "imap-secret",
			TLS:      TLSImplicit,
		},
		SMTP: Server{
			Host:     "smtp.example.org",
			Port:     587,
			Username: "max@example.org",
			Password: "smtp-secret",
			TLS:      TLSStartTLS,
		},
		SentMailbox:   "Sent Items",
		DraftsMailbox: "Drafts",
		TrashMailbox:  "Trash",
		Signature:     "-- \nMaxwell Jensen",
	}
}

func TestWriteAccountRoundTrip(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Account)
	}{
		{"static passwords", func(a *Account) {}},
		{"password command", func(a *Account) {
			a.IMAP.Password = ""
			a.IMAP.PasswordCommand = []string{"pass", "show", "mail/example.org"}
			a.SMTP.Password = ""
			a.SMTP.PasswordCommand = []string{"secret-tool", "lookup", "service", "mail", "account", "max example.org"}
		}},
		{"defaults filled", func(a *Account) {
			a.IMAP.TLS, a.SMTP.TLS = "", ""
			a.IMAP.Port, a.SMTP.Port = 0, 0
			a.SentMailbox, a.DraftsMailbox, a.TrashMailbox = "", "", ""
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "config.toml"), "", 0o600)

			in := roundTripAccount("personal")
			tt.modify(&in)
			normalizeAccount(&in)

			path, err := WriteAccount(dir, in)
			if err != nil {
				t.Fatal(err)
			}
			if want := filepath.Join(dir, "accounts", "personal.toml"); path != want {
				t.Errorf("path = %q, want %q", path, want)
			}
			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if perm := info.Mode().Perm(); perm != 0o600 {
				t.Errorf("mode = %o, want 600", perm)
			}

			cfg, warnings, err := LoadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 0 {
				t.Errorf("unexpected warnings: %+v", warnings)
			}
			if len(cfg.Accounts) != 1 {
				t.Fatalf("loaded %d accounts, want 1", len(cfg.Accounts))
			}
			if got := cfg.Accounts[0]; !reflect.DeepEqual(got, in) {
				t.Errorf("round trip lost data:\n got %#v\nwant %#v", got, in)
			}
		})
	}
}

func TestWriteAccountValidation(t *testing.T) {
	tests := []struct {
		name   string
		modify func(*Account)
		want   string
	}{
		{"missing imap host", func(a *Account) { a.IMAP.Host = "" }, "imap.host"},
		{"bad tls", func(a *Account) { a.IMAP.TLS = "ssl" }, "imap.tls"},
		{"password and command", func(a *Account) { a.SMTP.PasswordCommand = []string{"pass", "show", "x"} }, "not both"},
		{"port range", func(a *Account) { a.IMAP.Port = 70000 }, "65535"},
		{"bad email", func(a *Account) { a.Email = "Max <max@example.org>" }, "bare address"},
		{"empty name", func(a *Account) { a.Name = "" }, "account name"},
		{"odd name", func(a *Account) { a.Name = "bad name" }, "account name"},
		{"path name", func(a *Account) { a.Name = "../escape" }, "account name"},
		{"toml suffix", func(a *Account) { a.Name = "personal.toml" }, "account name"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			in := roundTripAccount("personal")
			tt.modify(&in)
			path, err := WriteAccount(dir, in)
			if err == nil {
				t.Fatalf("WriteAccount() = %q, want error", path)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not contain %q", err, tt.want)
			}
			if _, statErr := os.Stat(filepath.Join(dir, "accounts")); statErr == nil {
				t.Error("validation failure created the accounts directory")
			}
		})
	}
}

func TestWriteAccountRefusesOverwrite(t *testing.T) {
	dir := t.TempDir()
	a := roundTripAccount("personal")
	path, err := WriteAccount(dir, a)
	if err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := WriteAccount(dir, a); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("second WriteAccount() error = %v, want refusal", err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != string(original) {
		t.Error("refused overwrite still changed the file")
	}
}
