package app

import (
	"bytes"
	"log/slog"
	"os"
	"strings"
	"testing"
)

func TestStartConnectsAccountsAndLoadsFolders(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX", `\Inbox`)
	srv.addBox("Sent", `\Sent`)
	srv.addBox("Archive")
	srv.seed("INBOX", 3)

	cfg := testConfig(t, testAccount("alice"), testAccount("bob"))
	c, ev, _ := newTestController(t, cfg, srv)

	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ev.wait(t, "alice folders", func() bool { return len(ev.foldersFor("alice")) == 3 })
	ev.wait(t, "bob folders", func() bool { return len(ev.foldersFor("bob")) == 3 })
	ev.wait(t, "alice messages", func() bool { return len(ev.messageRows("alice", "INBOX")) == 3 })

	rows := ev.messageRows("alice", "INBOX")
	if rows[0].Ref.UID != 3 {
		t.Errorf("first row UID = %d, want newest (3)", rows[0].Ref.UID)
	}
	// seed marks even UIDs seen: newest (3) is unread, 2 is read, 1 is unread.
	if !rows[0].Unread || rows[1].Unread || !rows[2].Unread {
		t.Errorf("read flags = %v,%v,%v, want true,false,true", rows[0].Unread, rows[1].Unread, rows[2].Unread)
	}

	var roles []string
	for _, f := range ev.foldersFor("alice") {
		roles = append(roles, f.Name+"="+f.Role)
	}
	joined := strings.Join(roles, ",")
	for _, want := range []string{"INBOX=", "Sent=sent", "Archive="} {
		if !strings.Contains(joined, want) {
			t.Errorf("folder roles %q missing %q", joined, want)
		}
	}
	if got := ev.foldersFor("alice")[0].Total; got != 3 {
		t.Errorf("INBOX total = %d, want 3", got)
	}
	if got := ev.foldersFor("alice")[0].Unread; got != 2 {
		t.Errorf("INBOX unread = %d, want 2", got)
	}

	if got := c.ActiveAccount(); got != "alice" {
		t.Errorf("ActiveAccount = %q, want alice", got)
	}
	if got := len(c.Accounts()); got != 2 {
		t.Errorf("Accounts = %d, want 2", got)
	}
	c.SelectAccount("bob")
	if got := c.ActiveAccount(); got != "bob" {
		t.Errorf("ActiveAccount after select = %q, want bob", got)
	}
	c.SelectAccount("nobody")
	if got := c.ActiveAccount(); got != "bob" {
		t.Errorf("unknown account changed active to %q", got)
	}

	c.Shutdown()
	c.Shutdown() // idempotent
}

func TestStartLoadsScriptsAndReportsWarnings(t *testing.T) {
	var logs bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))

	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)

	cfg := testConfig(t, testAccount("alice"))
	cfg.ScriptDir = "testdata/scripts"
	cfg.ScriptEnabled = true

	ev := newEvents()
	c := New(cfg, ev, logger, &fakeConnector{server: srv})
	t.Cleanup(c.Shutdown)
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ev.wait(t, "script warning", func() bool {
		return strings.Contains(ev.errText(), "bad.lua")
	})
	if !strings.Contains(logs.String(), "startup hook ran") {
		t.Fatalf("on_startup hook did not log through btcx.log:\n%s", logs.String())
	}
}

func TestShutdownStopsWatchers(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)

	cfg := testConfig(t, testAccount("alice"))
	cfg.Watch = true
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")
	srv.awaitWatchStart(t)

	c.Shutdown()
	ends := srv.watchEndCopy()
	if len(ends) == 0 {
		t.Fatal("watcher never ran")
	}
	for _, err := range ends {
		if err == nil {
			t.Fatal("watch context was not cancelled on Shutdown")
		}
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func TestStartRetriesFailedAccount(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)

	cfg := testConfig(t, testAccount("alice"))
	ev := newEvents()
	conn := &fakeConnector{server: srv, dialFailures: 1}
	c := New(cfg, ev, discardLogger(), conn)
	t.Cleanup(c.Shutdown)

	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ev.wait(t, "first error", func() bool { return strings.Contains(ev.errText(), "dial failed") })
	ev.wait(t, "folders after retry", func() bool { return len(ev.foldersFor("alice")) > 0 })
}
