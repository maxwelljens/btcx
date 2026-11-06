package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// writeFile writes content to path with perm. The chmod defeats the umask so
// permission tests are meaningful.
func writeFile(t *testing.T, path, content string, perm os.FileMode) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), perm); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chmod(path, perm); err != nil {
		t.Fatalf("chmod %s: %v", path, err)
	}
}

// writeTree creates dir with the given slash-separated relative paths, each
// written with mode 0600, and returns dir.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		writeFile(t, filepath.Join(dir, filepath.FromSlash(name)), body, 0o600)
	}
	return dir
}

const (
	workAccount = `display_name = "Maxwell Jensen"
email = "max@example.org"
real_name = "Maxwell"
signature = "Maxwell Jensen"

[imap]
host = "imap.example.org"
port = 993
username = "max@example.org"
password = "imap-secret"
tls = "tls"

[smtp]
host = "smtp.example.org"
port = 587
username = "max@example.org"
password_command = "pass show 'mail/example'"
tls = "starttls"

[sent]
mailbox = "Sent Items"

[drafts]
mailbox = "Drafts"

[trash]
mailbox = "Trash"
`

	homeAccount = `display_name = "Home"
email = "me@example.net"

[imap]
host = "imap.example.net"

[smtp]
host = "smtp.example.net"
`
)

func TestLoadDirFull(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.toml": `theme = "dark"
editor = "alacritty -e nvim {file}"
watch = false
block_remote_content = false
script_dir = "/opt/btcx/scripts"
script_enabled = false
log_level = "debug"
`,
		"accounts/work.toml": workAccount,
		"accounts/home.toml": homeAccount,
	})

	cfg, warnings, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %+v", warnings)
	}
	if cfg.Dir != dir {
		t.Errorf("Dir = %q, want %q", cfg.Dir, dir)
	}
	if cfg.Theme != "dark" || cfg.LogLevel != "debug" || cfg.Watch || cfg.BlockRemoteContent ||
		cfg.ScriptEnabled || cfg.Editor != "alacritty -e nvim {file}" || cfg.ScriptDir != "/opt/btcx/scripts" {
		t.Errorf("general settings not applied: %+v", cfg)
	}

	want := []Account{
		{
			Name:        "home",
			DisplayName: "Home",
			Email:       "me@example.net",
			IMAP: Server{
				Host:     "imap.example.net",
				Port:     993,
				TLS:      TLSImplicit,
				Username: "",
			},
			SMTP:          Server{Host: "smtp.example.net", TLS: TLSStartTLS, Port: 587},
			SentMailbox:   "Sent",
			DraftsMailbox: "Drafts",
			TrashMailbox:  "Trash",
		},
		{
			Name:        "work",
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
				Host:            "smtp.example.org",
				Port:            587,
				Username:        "max@example.org",
				PasswordCommand: []string{"pass", "show", "mail/example"},
				TLS:             TLSStartTLS,
			},
			SentMailbox:   "Sent Items",
			DraftsMailbox: "Drafts",
			TrashMailbox:  "Trash",
			Signature:     "Maxwell Jensen",
		},
	}
	if !reflect.DeepEqual(cfg.Accounts, want) {
		t.Errorf("Accounts = %#v, want %#v", cfg.Accounts, want)
	}
}

func TestLoadDirDefaults(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.toml":           "",
		"accounts/a.toml":       homeAccount,
		"accounts/README.md":    "not an account",
		"accounts/.hidden.toml": "email = \"x@y.z\"",
		"accounts/sub/b.toml":   homeAccount,
	})

	cfg, warnings, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("unexpected warnings: %+v", warnings)
	}
	if cfg.Theme != "system" || cfg.LogLevel != "info" || !cfg.Watch || !cfg.BlockRemoteContent || !cfg.ScriptEnabled {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if want := filepath.Join(dir, "scripts"); cfg.ScriptDir != want {
		t.Errorf("ScriptDir = %q, want %q", cfg.ScriptDir, want)
	}
	if len(cfg.Accounts) != 1 || cfg.Accounts[0].Name != "a" {
		t.Fatalf("only accounts/*.toml should load, got %+v", cfg.Accounts)
	}
	acct := cfg.Accounts[0]
	if acct.IMAP.Port != 993 || acct.SMTP.Port != 587 {
		t.Errorf("default ports = %d/%d, want 993/587", acct.IMAP.Port, acct.SMTP.Port)
	}
	if acct.IMAP.TLS != TLSImplicit || acct.SMTP.TLS != TLSStartTLS {
		t.Errorf("default TLS = %q/%q", acct.IMAP.TLS, acct.SMTP.TLS)
	}
	if acct.SentMailbox != "Sent" || acct.DraftsMailbox != "Drafts" || acct.TrashMailbox != "Trash" {
		t.Errorf("default mailboxes = %q/%q/%q", acct.SentMailbox, acct.DraftsMailbox, acct.TrashMailbox)
	}
	if acct.RealName != "" {
		t.Errorf("RealName = %q, want empty (callers fall back to DisplayName)", acct.RealName)
	}
}

func TestLoadDirMissingFiles(t *testing.T) {
	dir := t.TempDir() // nothing on disk
	cfg, warnings, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if cfg == nil {
		t.Fatal("nil config")
	}
	if len(cfg.Accounts) != 0 {
		t.Errorf("Accounts = %v, want none", cfg.Accounts)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %+v, want one per missing file", warnings)
	}
	if warnings[0].File != "config.toml" || warnings[1].File != "accounts" {
		t.Errorf("warning files = %q, %q", warnings[0].File, warnings[1].File)
	}
}

func TestLoadDirUnknownKeys(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.toml":     "theme = \"dark\"\nfuture_knob = 1\n",
		"accounts/a.toml": "email = \"a@b.c\"\n\n[imap]\nhost = \"h\"\nunknown = true\n\n[smtp]\nhost = \"s\"\n",
	})
	_, warnings, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, w := range warnings {
		got[w.Key] = w.File
		if w.Message == "" {
			t.Errorf("warning %+v has no message", w)
		}
	}
	if got["future_knob"] != "config.toml" {
		t.Errorf("missing warning for config.toml key, got %+v", warnings)
	}
	if got["imap.unknown"] != filepath.Join("accounts", "a.toml") {
		t.Errorf("missing warning for account key, got %+v", warnings)
	}
	if len(warnings) != 2 {
		t.Errorf("warnings = %+v, want 2", warnings)
	}
}

func TestLoadDirInvalid(t *testing.T) {
	// account is the valid account this test mutates; empty means "use base".
	base := `display_name = "Max"
email = "max@example.org"

[imap]
host = "imap.example.org"
username = "max@example.org"
password = "secret"

[smtp]
host = "smtp.example.org"
`
	tests := []struct {
		name     string
		config   string
		account  string // empty: use base
		wantErr  bool   // any error, when wantErrs is empty
		wantErrs []string
	}{
		{name: "valid base", account: base},
		{name: "theme", config: "theme = \"neon\"\n", account: base,
			wantErrs: []string{"theme", "system"}},
		{name: "log level", config: "log_level = \"verbose\"\n", account: base,
			wantErrs: []string{"log_level", "warn"}},
		{name: "editor quote", config: "editor = \"nvim '\"\n", account: base,
			wantErrs: []string{"editor", "unterminated"}},
		{name: "tls none", account: strings.Replace(base, `password = "secret"`, "tls = \"none\"", 1),
			wantErrs: []string{"imap.tls", "plaintext"}},
		{name: "smtp tls none", account: strings.Replace(base, `host = "smtp.example.org"`, "host = \"smtp.example.org\"\ntls = \"none\"", 1),
			wantErrs: []string{"smtp.tls", "plaintext"}},
		{name: "tls bogus", account: strings.Replace(base, `password = "secret"`, "tls = \"ssl\"", 1),
			wantErrs: []string{"imap.tls", "starttls"}},
		{name: "empty host", account: strings.Replace(base, `host = "imap.example.org"`, `host = ""`, 1),
			wantErrs: []string{"imap.host", "required"}},
		{name: "port too big", account: strings.Replace(base, "host = \"imap.example.org\"", "host = \"h\"\nport = 70000", 1),
			wantErrs: []string{"imap.port", "65535"}},
		{name: "port zero explicit", account: strings.Replace(base, "host = \"imap.example.org\"", "host = \"h\"\nport = 0", 1),
			wantErrs: []string{"imap.port", "between 1 and 65535"}},
		{name: "negative port", account: strings.Replace(base, "host = \"imap.example.org\"", "host = \"h\"\nport = -1", 1),
			wantErrs: []string{"imap.port"}},
		{name: "both credentials", account: strings.Replace(base, `password = "secret"`, "password = \"s\"\npassword_command = \"pass show\"", 1),
			wantErrs: []string{"password_command", "not both"}},
		{name: "bad email", account: strings.Replace(base, `email = "max@example.org"`, `email = "Max <max@example.org>"`, 1),
			wantErrs: []string{"email", "bare address"}},
		{name: "missing email", account: strings.Replace(base, "email = \"max@example.org\"\n", "", 1),
			wantErrs: []string{"email", "required"}},
		{name: "password command quote", account: strings.Replace(base, `password = "secret"`, `password_command = "pass show 'mail"`, 1),
			wantErrs: []string{"password_command", "unterminated"}},
		{name: "malformed toml", config: "theme = \n", account: base, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			account := tt.account
			if account == "" {
				account = base
			}
			dir := writeTree(t, map[string]string{
				"config.toml":     tt.config,
				"accounts/a.toml": account,
			})
			cfg, _, err := LoadDir(dir)
			if !tt.wantErr && len(tt.wantErrs) == 0 {
				if err != nil {
					t.Fatalf("LoadDir() error = %v", err)
				}
				if cfg == nil {
					t.Fatal("nil config")
				}
				return
			}
			if err == nil {
				t.Fatalf("LoadDir() = %+v, want error containing %q", cfg, tt.wantErrs)
			}
			for _, want := range tt.wantErrs {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestLoadDirPasswordPermissions(t *testing.T) {
	account := `email = "a@b.c"

[imap]
host = "imap.example.org"
password = "secret"

[smtp]
host = "smtp.example.org"
`
	commandOnly := `email = "a@b.c"

[imap]
host = "imap.example.org"
password_command = "pass show mail"

[smtp]
host = "smtp.example.org"
`
	tests := []struct {
		name      string
		content   string
		perm      os.FileMode
		wantWarns int
	}{
		{"static password 0600", account, 0o600, 0},
		{"static password 0644", account, 0o644, 1},
		{"static password 0640", account, 0o640, 1},
		{"command only 0644", commandOnly, 0o644, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			writeFile(t, filepath.Join(dir, "config.toml"), "", 0o600)
			writeFile(t, filepath.Join(dir, "accounts", "a.toml"), tt.content, tt.perm)
			_, warnings, err := LoadDir(dir)
			if err != nil {
				t.Fatal(err)
			}
			if len(warnings) != tt.wantWarns {
				t.Fatalf("warnings = %+v, want %d", warnings, tt.wantWarns)
			}
			for _, w := range warnings {
				if !strings.Contains(w.Message, "chmod 600") {
					t.Errorf("warning %+v does not tell the user to fix the mode", w)
				}
			}
		})
	}
}

func TestLoadDirInsecureSkipVerifyWarning(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.toml": "",
		"accounts/a.toml": `email = "a@b.c"

[imap]
host = "h"
insecure_skip_verify = true

[smtp]
host = "s"
insecure_skip_verify = true
`,
	})
	_, warnings, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 2 {
		t.Fatalf("warnings = %+v, want one per endpoint", warnings)
	}
	keys := []string{warnings[0].Key, warnings[1].Key}
	if !reflect.DeepEqual(keys, []string{"imap.insecure_skip_verify", "smtp.insecure_skip_verify"}) {
		t.Errorf("warning keys = %v", keys)
	}
	if !strings.Contains(warnings[0].Message, "DISABLED") {
		t.Errorf("warning is not loud: %+v", warnings[0])
	}
}

func TestAccount(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"config.toml":        "",
		"accounts/work.toml": workAccount,
	})
	cfg, _, err := LoadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := cfg.Account("work")
	if !ok || got.Email != "max@example.org" {
		t.Fatalf("Account(work) = %+v, %v", got, ok)
	}
	if _, ok := cfg.Account("nope"); ok {
		t.Error("Account(nope) found an account")
	}
}

func TestDefaultDir(t *testing.T) {
	tests := []struct {
		name    string
		xdg     string
		home    string
		want    string
		wantErr bool
	}{
		{"xdg set", "/xdg", "/home/u", "/xdg/btcx", false},
		{"xdg unset", "", "/home/u", "/home/u/.config/btcx", false},
		{"xdg relative ignored", "relative/dir", "/home/u", "/home/u/.config/btcx", false},
		{"no home", "", "", "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", tt.xdg)
			t.Setenv("HOME", tt.home)
			got, err := DefaultDir()
			if tt.wantErr {
				if err == nil {
					t.Fatalf("DefaultDir() = %q, want error", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("DefaultDir() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestHelperProcess is not a real test: it is the child process used by
// TestResolvePassword. Modes are selected through GO_HELPER_MODE.
func TestHelperProcess(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	switch os.Getenv("GO_HELPER_MODE") {
	case "secret":
		fmt.Println("hunter2")
	case "empty":
		// no output
	case "fail":
		fmt.Println("hunter2 on stdout")
		fmt.Fprintln(os.Stderr, "hunter2 on stderr")
		os.Exit(3)
	case "sleep":
		time.Sleep(5 * time.Second)
	}
	os.Exit(0)
}

func TestResolvePassword(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")

	tests := []struct {
		name     string
		account  Account
		mode     string
		ctx      func() (context.Context, context.CancelFunc)
		want     string
		wantErr  []string
		fastFail bool
	}{
		{
			name:    "static password",
			account: Account{Name: "a", IMAP: Server{Password: "static-pw"}},
			want:    "static-pw",
		},
		{
			name:    "command output",
			account: Account{Name: "a", IMAP: Server{PasswordCommand: []string{os.Args[0], "-test.run=TestHelperProcess"}}},
			mode:    "secret",
			want:    "hunter2",
		},
		{
			name:    "smtp fallback",
			account: Account{Name: "a", SMTP: Server{PasswordCommand: []string{os.Args[0], "-test.run=TestHelperProcess"}}},
			mode:    "secret",
			want:    "hunter2",
		},
		{
			name:    "command fails",
			account: Account{Name: "a", IMAP: Server{PasswordCommand: []string{os.Args[0], "-test.run=TestHelperProcess"}}},
			mode:    "fail",
			wantErr: []string{"password_command"},
		},
		{
			name:    "empty output",
			account: Account{Name: "a", IMAP: Server{PasswordCommand: []string{os.Args[0], "-test.run=TestHelperProcess"}}},
			mode:    "empty",
			wantErr: []string{"no output"},
		},
		{
			name:    "nothing configured",
			account: Account{Name: "a"},
			wantErr: []string{"no password"},
		},
		{
			name:    "missing binary",
			account: Account{Name: "a", IMAP: Server{PasswordCommand: []string{"btcx-no-such-command"}}},
			wantErr: []string{"btcx-no-such-command"},
		},
		{
			name:    "timeout",
			account: Account{Name: "a", IMAP: Server{PasswordCommand: []string{os.Args[0], "-test.run=TestHelperProcess"}}},
			mode:    "sleep",
			ctx: func() (context.Context, context.CancelFunc) {
				return context.WithTimeout(context.Background(), 100*time.Millisecond)
			},
			wantErr:  []string{"deadline exceeded"},
			fastFail: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv("GO_HELPER_MODE", tt.mode)
			ctx := context.Background()
			if tt.ctx != nil {
				var cancel context.CancelFunc
				ctx, cancel = tt.ctx()
				defer cancel()
			}

			start := time.Now()
			got, err := tt.account.ResolvePassword(ctx)
			if tt.fastFail && time.Since(start) > 3*time.Second {
				t.Errorf("ResolvePassword took %v, timeout not enforced", time.Since(start))
			}
			if len(tt.wantErr) > 0 {
				if err == nil {
					t.Fatalf("ResolvePassword() = %q, want error", got)
				}
				for _, want := range tt.wantErr {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("error %q does not contain %q", err, want)
					}
				}
				if strings.Contains(err.Error(), "hunter2") {
					t.Errorf("error leaks the password: %q", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Errorf("ResolvePassword() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestResolvePasswordTimeoutIsDeadline(t *testing.T) {
	t.Setenv("GO_WANT_HELPER_PROCESS", "1")
	t.Setenv("GO_HELPER_MODE", "sleep")
	acct := Account{Name: "a", IMAP: Server{PasswordCommand: []string{os.Args[0], "-test.run=TestHelperProcess"}}}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := acct.ResolvePassword(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want context.DeadlineExceeded", err)
	}
}
