package app

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"btcx/internal/config"
	"btcx/internal/contract"
)

// testSpec is a minimal wizard submission: name, display name, ports and
// usernames are left for the controller to default.
func testSpec() contract.AccountSpec {
	return contract.AccountSpec{
		Email:     "max@example.org",
		IMAP:      contract.ServerSpec{Host: "imap.example.org", TLS: "tls"},
		SMTP:      contract.ServerSpec{Host: "smtp.example.org", TLS: "starttls"},
		Password:  "s3cr3t",
		Signature: "Regards,\nMax",
	}
}

// waitUntil polls cond until it holds or the test times out.
func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestCreateAccountWritesAndActivates(t *testing.T) {
	cfg := testConfig(t)
	c, _, _ := newTestController(t, cfg, newFakeServer())

	path, err := c.CreateAccount(testSpec())
	if err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if want := filepath.Join(cfg.Dir, "accounts", "exampleorg.toml"); path != want {
		t.Fatalf("path = %q, want %q", path, want)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat account file: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("account file mode = %o, want 600", got)
	}

	// The file must load back as the normalised spec.
	want, err := accountFromSpec(testSpec())
	if err != nil {
		t.Fatalf("accountFromSpec: %v", err)
	}
	// config.WriteAccount states the mailbox defaults explicitly.
	want.SentMailbox, want.DraftsMailbox, want.TrashMailbox = "Sent", "Drafts", "Trash"

	reloaded, _, err := config.LoadDir(cfg.Dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(reloaded.Accounts) != 1 {
		t.Fatalf("loaded %d accounts, want 1", len(reloaded.Accounts))
	}
	if got := reloaded.Accounts[0]; !reflect.DeepEqual(got, want) {
		t.Errorf("loaded account = %+v, want %+v", got, want)
	}

	// It is active in the running controller without a restart.
	accounts := c.Accounts()
	if len(accounts) != 1 || accounts[0].Name != "exampleorg" || accounts[0].Email != "max@example.org" {
		t.Fatalf("Accounts = %+v, want exampleorg/max@example.org", accounts)
	}
	if got := c.ActiveAccount(); got != "exampleorg" {
		t.Errorf("ActiveAccount = %q, want exampleorg", got)
	}
}

func TestCreateAccountDefaults(t *testing.T) {
	spec := testSpec()
	spec.Email = "Max.Mustermann@Mail.Example-1.org"
	spec.IMAP.TLS = "STARTTLS"
	spec.SMTP.TLS = "tls"
	spec.Password = ""
	spec.PasswordCommand = "pass show mail/example.org"

	c, _, _ := newTestController(t, testConfig(t), newFakeServer())
	if _, err := c.CreateAccount(spec); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}

	loaded, _, err := config.LoadDir(c.cfg.Dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	got := loaded.Accounts[0]
	if got.Name != "mailexample-1org" {
		t.Errorf("name = %q, want mailexample-1org", got.Name)
	}
	if got.DisplayName != "Max.Mustermann" {
		t.Errorf("display name = %q, want Max.Mustermann", got.DisplayName)
	}
	if got.IMAP.Port != 143 || got.SMTP.Port != 465 {
		t.Errorf("ports = %d/%d, want 143/465", got.IMAP.Port, got.SMTP.Port)
	}
	if got.IMAP.Username != spec.Email || got.SMTP.Username != spec.Email {
		t.Errorf("usernames = %q/%q, want %q", got.IMAP.Username, got.SMTP.Username, spec.Email)
	}
	if !reflect.DeepEqual(got.IMAP.PasswordCommand, []string{"pass", "show", "mail/example.org"}) {
		t.Errorf("password_command = %v", got.IMAP.PasswordCommand)
	}
	if got.IMAP.Password != "" || got.SMTP.Password != "" {
		t.Error("password_command account also stored a static password")
	}
}

func TestCreateAccountRejectsInvalidSpecs(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*contract.AccountSpec)
		wantErr string
	}{
		{"bad name", func(s *contract.AccountSpec) { s.Name = "Max Mustermann" }, "name"},
		{"uppercase name", func(s *contract.AccountSpec) { s.Name = "Max" }, "name"},
		{"leading dash name", func(s *contract.AccountSpec) { s.Name = "-max" }, "name"},
		{"empty email", func(s *contract.AccountSpec) { s.Email = "" }, "email is required"},
		{"address with display name", func(s *contract.AccountSpec) { s.Email = "Max <max@example.org>" }, "email"},
		{"both credentials", func(s *contract.AccountSpec) { s.PasswordCommand = "pass show x" }, "either password or password_command"},
		{"bad tls", func(s *contract.AccountSpec) { s.IMAP.TLS = "ssl" }, "must be"},
		{"missing host", func(s *contract.AccountSpec) { s.SMTP.Host = "" }, "smtp.host is required"},
		{"bad port", func(s *contract.AccountSpec) { s.IMAP.Port = 70000 }, "port"},
		{"bad password command", func(s *contract.AccountSpec) {
			s.Password = ""
			s.PasswordCommand = "pass show 'unterminated"
		}, "password_command"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := testSpec()
			tt.mutate(&spec)
			c, _, _ := newTestController(t, testConfig(t), newFakeServer())

			if _, err := c.CreateAccount(spec); err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
			if entries, err := os.ReadDir(filepath.Join(c.cfg.Dir, "accounts")); err == nil && len(entries) != 0 {
				t.Fatalf("rejected spec wrote %d account files", len(entries))
			}
			if got := len(c.Accounts()); got != 0 {
				t.Fatalf("rejected spec added %d accounts", got)
			}
		})
	}
}

func TestCreateAccountRefusesOverwrite(t *testing.T) {
	c, _, _ := newTestController(t, testConfig(t), newFakeServer())
	if _, err := c.CreateAccount(testSpec()); err != nil {
		t.Fatalf("CreateAccount: %v", err)
	}
	if _, err := c.CreateAccount(testSpec()); err == nil || !strings.Contains(err.Error(), "refusing to overwrite") {
		t.Fatalf("second CreateAccount err = %v, want a refusal", err)
	}
}

func TestVerifyAccountBothOK(t *testing.T) {
	c, ev, _ := newTestController(t, testConfig(t), newFakeServer())

	var gotIMAP, gotSMTP string
	c.verifyIMAP = func(_ context.Context, a config.Account, password string) contract.ServerCheck {
		gotIMAP = a.IMAP.Host + "/" + a.IMAP.Username + "/" + password
		return contract.ServerCheck{OK: true, Detail: "imap ok"}
	}
	c.verifySMTP = func(_ context.Context, a config.Account, password string) contract.ServerCheck {
		gotSMTP = a.SMTP.Host + "/" + a.SMTP.Username + "/" + password
		return contract.ServerCheck{OK: true, Detail: "smtp ok"}
	}

	c.VerifyAccount(testSpec())
	ev.wait(t, "verify result", func() bool { return len(ev.verifyResults()) == 1 })

	res := ev.verifyResults()[0]
	if !res.IMAP.OK || res.IMAP.Detail != "imap ok" {
		t.Errorf("IMAP = %+v", res.IMAP)
	}
	if !res.SMTP.OK || res.SMTP.Detail != "smtp ok" {
		t.Errorf("SMTP = %+v", res.SMTP)
	}
	if want := "imap.example.org/max@example.org/s3cr3t"; gotIMAP != want {
		t.Errorf("IMAP check got %q, want %q", gotIMAP, want)
	}
	if want := "smtp.example.org/max@example.org/s3cr3t"; gotSMTP != want {
		t.Errorf("SMTP check got %q, want %q", gotSMTP, want)
	}
}

func TestVerifyAccountReportsBothChecks(t *testing.T) {
	c, ev, _ := newTestController(t, testConfig(t), newFakeServer())

	c.verifyIMAP = func(context.Context, config.Account, string) contract.ServerCheck {
		return contract.ServerCheck{Detail: "imap: connection refused"}
	}
	c.verifySMTP = func(context.Context, config.Account, string) contract.ServerCheck {
		return contract.ServerCheck{OK: true, Detail: "smtp ok"}
	}

	c.VerifyAccount(testSpec())
	ev.wait(t, "verify result", func() bool { return len(ev.verifyResults()) == 1 })

	res := ev.verifyResults()[0]
	if res.IMAP.OK || !strings.Contains(res.IMAP.Detail, "connection refused") {
		t.Errorf("IMAP = %+v, want the failure", res.IMAP)
	}
	if !res.SMTP.OK || res.SMTP.Detail != "smtp ok" {
		t.Errorf("SMTP = %+v, want success despite the IMAP failure", res.SMTP)
	}
}

func TestVerifyAccountRejectsInvalidSpec(t *testing.T) {
	c, ev, _ := newTestController(t, testConfig(t), newFakeServer())
	called := false
	c.verifyIMAP = func(context.Context, config.Account, string) contract.ServerCheck {
		called = true
		return contract.ServerCheck{OK: true}
	}

	spec := testSpec()
	spec.Email = ""
	c.VerifyAccount(spec)
	ev.wait(t, "verify result", func() bool { return len(ev.verifyResults()) == 1 })

	res := ev.verifyResults()[0]
	if res.IMAP.OK || res.SMTP.OK {
		t.Errorf("invalid spec reported success: %+v", res)
	}
	if !strings.Contains(res.IMAP.Detail, "email is required") || res.IMAP.Detail != res.SMTP.Detail {
		t.Errorf("details = %q / %q", res.IMAP.Detail, res.SMTP.Detail)
	}
	if called {
		t.Error("invalid spec still ran a connection check")
	}
}

func TestVerifyAccountUsesPasswordCommand(t *testing.T) {
	c, ev, _ := newTestController(t, testConfig(t), newFakeServer())

	var gotIMAP, gotSMTP string
	c.verifyIMAP = func(_ context.Context, _ config.Account, password string) contract.ServerCheck {
		gotIMAP = password
		return contract.ServerCheck{OK: true}
	}
	c.verifySMTP = func(_ context.Context, _ config.Account, password string) contract.ServerCheck {
		gotSMTP = password
		return contract.ServerCheck{OK: true}
	}

	spec := testSpec()
	spec.Password = ""
	spec.PasswordCommand = writePasswordCommand(t, "from-command")

	c.VerifyAccount(spec)
	ev.wait(t, "verify result", func() bool { return len(ev.verifyResults()) == 1 })

	if gotIMAP != "from-command" || gotSMTP != "from-command" {
		t.Fatalf("checks got passwords %q/%q, want the command's output", gotIMAP, gotSMTP)
	}
	for _, check := range []contract.ServerCheck{ev.verifyResults()[0].IMAP, ev.verifyResults()[0].SMTP} {
		if strings.Contains(check.Detail, "from-command") {
			t.Errorf("detail leaks the password: %q", check.Detail)
		}
	}
}

// writePasswordCommand installs an executable that prints password.
func writePasswordCommand(t *testing.T, password string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-pass")
	body := "#!/bin/sh\nprintf '%s\\n' '" + password + "'\n"
	if err := os.WriteFile(path, []byte(body), 0o700); err != nil {
		t.Fatalf("write fake password command: %v", err)
	}
	return path
}

func TestReloadPicksUpNewAccount(t *testing.T) {
	dir := t.TempDir()
	if _, err := config.WriteAccount(dir, testAccount("alice")); err != nil {
		t.Fatalf("write alice: %v", err)
	}
	cfg, _, err := config.LoadDir(dir)
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}

	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 2)

	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")
	ev.wait(t, "alice rows", func() bool { return len(ev.messageRows("alice", "INBOX")) == 2 })
	dials := srv.dialCount()

	if _, err := config.WriteAccount(dir, testAccount("bob")); err != nil {
		t.Fatalf("write bob: %v", err)
	}
	c.Reload()

	ev.wait(t, "bob folders", func() bool { return len(ev.foldersFor("bob")) > 0 })
	if got := len(c.Accounts()); got != 2 {
		t.Fatalf("Accounts = %d, want 2", got)
	}
	if got := c.ActiveAccount(); got != "alice" {
		t.Errorf("ActiveAccount = %q, want alice kept", got)
	}
	if got := srv.dialCount(); got <= dials {
		t.Errorf("dials = %d, want more than %d after reload", got, dials)
	}

	// The previous generation's watcher was cancelled.
	waitUntil(t, "old watcher cancelled", func() bool { return len(srv.watchEndCopy()) > 0 })
	for _, err := range srv.watchEndCopy() {
		if err == nil {
			t.Fatal("watcher context was not cancelled by Reload")
		}
	}
}
