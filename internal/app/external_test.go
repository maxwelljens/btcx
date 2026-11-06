package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"btcx/internal/contract"
	"btcx/internal/model"
)

// writeFakeEditor installs a command that overwrites the draft file with fixed
// content. The editor package launches it without a shell.
func writeFakeEditor(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fake-editor")
	script := "#!/bin/sh\ncat > \"$1\" <<'BTCEOF'\n" + content + "BTCEOF\n"
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake editor: %v", err)
	}
	return path
}

func TestStartExternalEditMergesChanges(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)

	cfg := testConfig(t, testAccount("alice"))
	cfg.Editor = writeFakeEditor(t, "To: carol@example.org\nCc: \nBcc: \nSubject: Edited subject\n\nedited body\n\n")

	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	draft := contract.ComposeDraft{
		Account: "alice",
		To:      []model.Address{{Addr: "bob@example.org"}},
		Subject: "Original subject",
		Plain:   "original body",
		Attachments: []contract.DraftAttachment{{
			Filename:    "notes.txt",
			ContentType: "text/plain",
			Data:        []byte("attachment bytes"),
		}},
	}
	id := c.StartExternalEdit(draft)
	ev.wait(t, "edit result", func() bool { return len(ev.editResults()) == 1 })

	result := ev.editResults()[0]
	if result.ID != id {
		t.Errorf("edit id = %d, want %d", result.ID, id)
	}
	if result.Err != nil {
		t.Fatalf("external edit: %v", result.Err)
	}
	if result.Draft.Subject != "Edited subject" {
		t.Errorf("subject = %q", result.Draft.Subject)
	}
	if !strings.Contains(result.Draft.Plain, "edited body") {
		t.Errorf("body = %q", result.Draft.Plain)
	}
	if len(result.Draft.To) != 1 || result.Draft.To[0].Addr != "carol@example.org" {
		t.Errorf("To = %+v", result.Draft.To)
	}
	if len(result.Draft.Attachments) != 1 || result.Draft.Attachments[0].Filename != "notes.txt" {
		t.Errorf("attachments lost: %+v", result.Draft.Attachments)
	}
	if result.Draft.Account != "alice" {
		t.Errorf("account lost: %q", result.Draft.Account)
	}
}

func TestExternalEditWithoutEditor(t *testing.T) {
	t.Setenv("VISUAL", "")
	t.Setenv("EDITOR", "")
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)

	cfg := testConfig(t, testAccount("alice"))
	cfg.Editor = "/nonexistent/btcx-editor"

	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	c.StartExternalEdit(contract.ComposeDraft{Account: "alice", Subject: "x"})
	ev.wait(t, "edit result", func() bool { return len(ev.editResults()) == 1 })
	if err := ev.editResults()[0].Err; err == nil {
		t.Fatal("missing editor reported success")
	}
}
