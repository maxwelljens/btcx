package imapx_test

// Opt-in integration probe against a real IMAP server. This test is
// SKIPPED unless BTCX_REAL_IMAP=1 is set; the CI suite never touches real
// servers. It exists so that "no email loads" bug reports against real
// accounts can be reproduced through btcx's own client code path.
//
// Secrets: the account's password is resolved by the config package
// (static field or password_command) and passed straight into Dial; it is
// never printed. Configure with:
//
//	BTCX_REAL_IMAP=1                 enable the probe
//	BTCX_REAL_CONFIG_DIR=<dir>       config dir (default: user default)
//	BTCX_REAL_ACCOUNT=<name>         account name (default: personal)

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"btcx/internal/config"
	"btcx/internal/imapx"
)

func TestRealServer(t *testing.T) {
	if os.Getenv("BTCX_REAL_IMAP") != "1" {
		t.Skip("set BTCX_REAL_IMAP=1 to probe a real IMAP account")
	}

	dir := os.Getenv("BTCX_REAL_CONFIG_DIR")
	if dir == "" {
		var err error
		if dir, err = config.DefaultDir(); err != nil {
			t.Fatal(err)
		}
	}
	cfg, _, err := config.LoadDir(dir)
	if err != nil {
		t.Fatalf("load config %s: %v", dir, err)
	}
	name := os.Getenv("BTCX_REAL_ACCOUNT")
	if name == "" {
		name = "personal"
	}
	acct, ok := cfg.Account(name)
	if !ok {
		t.Fatalf("account %q not found in %s", name, dir)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	pw, err := acct.IMAP.ResolvePassword(ctx, acct.Name)
	if err != nil {
		t.Fatalf("resolve password: %v", err)
	}

	mode := imapx.TLSImplicit
	if acct.IMAP.TLS == "starttls" {
		mode = imapx.TLSStartTLS
	}
	cl, err := imapx.Dial(ctx, imapx.DialConfig{
		Host:               acct.IMAP.Host,
		Port:               acct.IMAP.Port,
		Username:           acct.IMAP.Username,
		Password:           pw,
		TLS:                mode,
		InsecureSkipVerify: acct.IMAP.InsecureSkipVerify,
		CAFile:             acct.IMAP.CAFile,
	})
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer cl.Close()

	// The exact startup path the user's log fails on: list folders, then
	// load page one of INBOX. The first envelope fetch below is the loop
	// signal — it must be able to go RED with "unexpected EOF".
	if boxes, err := cl.Mailboxes(ctx); err != nil {
		t.Fatalf("mailboxes: %v", err)
	} else {
		t.Logf("mailboxes: %d", len(boxes))
	}

	for _, n := range []int{1, 10, 500} {
		n := n
		t.Run("envelopes", func(t *testing.T) {
			start := time.Now()
			envs, err := cl.Envelopes(ctx, "INBOX", 1, n)
			// The corrected contract: a *PartialError alongside the rows is
			// an acceptable, honest degradation (broken server, mail loads).
			var pe *imapx.PartialError
			if errors.As(err, &pe) {
				t.Logf("degraded fetch accepted: %v", pe)
				err = nil
			}
			if err != nil {
				t.Fatalf("envelopes INBOX count=%d: %v", n, err)
			}
			if len(envs) == 0 {
				t.Fatalf("envelopes INBOX count=%d: no mail loaded", n)
			}
			// Sanity check for active mailboxes: the newest message must
			// be recent. A stale window (oldest-message-first) shows up
			// here immediately.
			newest := envs[len(envs)-1].Date
			oldest := envs[0].Date
			t.Logf("count=%d got=%d oldest=%s newest=%s in %s",
				n, len(envs), oldest.Format("2006-01-02"), newest.Format("2006-01-02"),
				time.Since(start).Round(time.Millisecond))
			if n > 10 && newest.Before(time.Now().AddDate(0, -2, 0)) {
				t.Errorf("newest message is from %s — the list is showing old mail, not the newest",
					newest.Format("2006-01-02"))
			}
		})
	}
}
