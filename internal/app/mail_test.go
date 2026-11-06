package app

import (
	"os"
	"strings"
	"testing"
	"time"

	"btcx/internal/model"
)

const htmlMessage = "From: Alice <alice@example.org>\r\n" +
	"To: Bob <bob@example.org>\r\n" +
	"Subject: Hello\r\n" +
	"Date: Mon, 2 Jan 2026 15:04:05 -0700\r\n" +
	"Message-ID: <orig@example.org>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/html; charset=utf-8\r\n" +
	"\r\n" +
	"<p>Hello there</p><script>alert('x')</script>" +
	"<img src=\"http://tracker.example/p.gif\" alt=\"t\">\r\n"

const plainMessage = "From: Alice <alice@example.org>\r\n" +
	"To: Bob <bob@example.org>\r\n" +
	"Subject: Plain\r\n" +
	"Date: Tue, 3 Jan 2026 10:00:00 +0000\r\n" +
	"Message-ID: <plain@example.org>\r\n" +
	"MIME-Version: 1.0\r\n" +
	"Content-Type: text/plain; charset=utf-8\r\n" +
	"\r\n" +
	"plain body line\r\n"

func TestSelectFolderSortsAndCaps(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 600)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")
	ev.wait(t, "initial rows", func() bool { return len(ev.messageRows("alice", "INBOX")) > 0 })

	before := len(ev.messageEvents())
	c.SelectFolder("alice", "INBOX")
	ev.wait(t, "select folder rows", func() bool { return len(ev.messageEvents()) > before })

	rows := ev.messageRows("alice", "INBOX")
	if len(rows) != pageSize {
		t.Fatalf("rows = %d, want %d", len(rows), pageSize)
	}
	for i := 1; i < len(rows); i++ {
		if rows[i].Date.After(rows[i-1].Date) {
			t.Fatalf("rows not sorted newest first at %d", i)
		}
		if rows[i].Ref.UID >= rows[i-1].Ref.UID {
			t.Fatalf("UIDs not descending at %d: %d then %d", i, rows[i-1].Ref.UID, rows[i].Ref.UID)
		}
	}
}

func TestOpenMessageSanitizesRendersAndMarksSeen(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 2)
	htmlRef := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 1}
	plainRef := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 2}
	srv.setRaw(htmlRef, htmlMessage)
	srv.setRaw(plainRef, plainMessage)

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")
	ev.wait(t, "rows", func() bool { return len(ev.messageRows("alice", "INBOX")) == 2 })

	c.OpenMessage("alice", htmlRef)
	ev.wait(t, "html view", func() bool { _, ok := ev.openedView(htmlRef); return ok })
	view, _ := ev.openedView(htmlRef)
	if !strings.Contains(view.PangoBody, "Hello there") {
		t.Errorf("body = %q, want the sanitized text", view.PangoBody)
	}
	if strings.Contains(view.PangoBody, "<script") || strings.Contains(view.PangoBody, "alert") {
		t.Errorf("body leaked script content: %q", view.PangoBody)
	}
	if !strings.Contains(view.PangoBody, "blocked remote image") {
		t.Errorf("body = %q, want a blocked-image placeholder", view.PangoBody)
	}
	if !view.ShowRemoteContentBar {
		t.Error("ShowRemoteContentBar = false, want true for remote images")
	}
	if view.Unread {
		t.Error("view still unread after auto MarkSeen")
	}
	if got := view.From; len(got) != 1 || got[0].Addr != "alice@example.org" {
		t.Errorf("From = %+v", got)
	}
	if view.Subject != "Hello" || view.Account != "alice" || view.Ref != htmlRef {
		t.Errorf("view header fields wrong: %+v", view)
	}

	ev.wait(t, "row marked seen", func() bool {
		row, ok := ev.changedRow(htmlRef)
		return ok && !row.Unread
	})
	calls := srv.flagCallsCopy()
	found := false
	for _, call := range calls {
		if call.UID == 1 && call.Add.Seen {
			found = true
		}
	}
	if !found {
		t.Fatalf("MarkSeen not sent to client: %+v", calls)
	}

	c.OpenMessage("alice", plainRef)
	ev.wait(t, "plain view", func() bool { _, ok := ev.openedView(plainRef); return ok })
	plain, _ := ev.openedView(plainRef)
	if !strings.Contains(plain.PangoBody, "plain body line") {
		t.Errorf("plain body = %q", plain.PangoBody)
	}
	if plain.ShowRemoteContentBar {
		t.Error("plain text view asked to load remote content")
	}
	if plain.Unread {
		t.Error("already-seen message reported unread")
	}
}

func TestMarkSeenSetFlaggedAndDelete(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX", `\Inbox`)
	srv.addBox("Trash", `\Trash`)
	srv.seed("INBOX", 2)

	trashless := testAccount("bob")
	trashless.TrashMailbox = ""

	cfg := testConfig(t, testAccount("alice"), trashless)
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")
	ev.wait(t, "alice rows", func() bool { return len(ev.messageRows("alice", "INBOX")) == 2 })
	ev.wait(t, "bob rows", func() bool { return len(ev.messageRows("bob", "INBOX")) == 2 })

	ref := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 1}
	c.MarkSeen("alice", ref, true)
	ev.wait(t, "seen row", func() bool {
		row, ok := ev.changedRow(ref)
		return ok && !row.Unread
	})
	c.SetFlagged("alice", ref, true)
	ev.wait(t, "flagged row", func() bool {
		row, ok := ev.changedRow(ref)
		return ok && row.Flagged
	})

	calls := srv.flagCallsCopy()
	if len(calls) < 2 {
		t.Fatalf("flag calls = %+v, want MarkSeen and SetFlagged", calls)
	}
	if !calls[0].Add.Seen {
		t.Errorf("first flag call = %+v, want Seen add", calls[0])
	}
	if !calls[1].Add.Flagged {
		t.Errorf("second flag call = %+v, want Flagged add", calls[1])
	}

	c.DeleteMessage("alice", ref)
	ev.wait(t, "row removed", func() bool {
		for _, r := range ev.messageRows("alice", "INBOX") {
			if r.Ref == ref {
				return false
			}
		}
		return true
	})
	moves := srv.movesCopy()
	if len(moves) != 1 || moves[0].Dest != "Trash" || moves[0].UID != 1 {
		t.Fatalf("moves = %+v, want INBOX/1 -> Trash", moves)
	}

	// bob has no trash mailbox: \Deleted is the fallback.
	c.DeleteMessage("bob", ref)
	ev.wait(t, "bob row removed", func() bool {
		for _, r := range ev.messageRows("bob", "INBOX") {
			if r.Ref == ref {
				return false
			}
		}
		return true
	})
	if len(srv.movesCopy()) != 1 {
		t.Errorf("fallback delete used Move: %+v", srv.movesCopy())
	}
	deleted := false
	for _, call := range srv.flagCallsCopy() {
		if call.UID == ref.UID && call.Add.Deleted {
			deleted = true
		}
	}
	if !deleted {
		t.Errorf("fallback delete did not set \\Deleted: %+v", srv.flagCallsCopy())
	}
}

func TestSearchDeliversSortedRows(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 5)
	srv.setSearch("INBOX", "needle", []uint32{2, 5})

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")
	ev.wait(t, "rows", func() bool { return len(ev.messageRows("alice", "INBOX")) == 5 })

	c.Search("alice", "INBOX", "needle")
	ev.wait(t, "search rows", func() bool { return len(ev.messageRows("alice", "INBOX")) == 2 })

	rows := ev.messageRows("alice", "INBOX")
	if rows[0].Ref.UID != 5 || rows[1].Ref.UID != 2 {
		t.Fatalf("rows = %d,%d, want 5,2", rows[0].Ref.UID, rows[1].Ref.UID)
	}
}

func TestSaveAttachmentWritesFile(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)
	ref := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 1}
	srv.setRaw(ref, "From: a@example.org\r\nSubject: att\r\nMIME-Version: 1.0\r\n"+
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n"+
		"--b\r\nContent-Type: text/plain\r\n\r\nbody\r\n"+
		"--b\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=\"../../evil.txt\"\r\n\r\npayload\r\n--b--\r\n")

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	dest := t.TempDir() + "/saved.txt"
	if err := c.SaveAttachment("alice", ref, 0, dest); err != nil {
		t.Fatalf("SaveAttachment: %v", err)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read saved attachment: %v", err)
	}
	if strings.TrimSpace(string(b)) != "payload" {
		t.Errorf("saved content = %q", b)
	}
	if err := c.SaveAttachment("alice", ref, 9, dest); err == nil {
		t.Error("out-of-range attachment index accepted")
	}
}

// accountMessage builds a message whose subject, body and attachment payload
// name the account, so a cross-account cache hit is impossible to miss.
func accountMessage(who string) string {
	return "From: Sender <sender@example.org>\r\n" +
		"To: Me <me@example.org>\r\n" +
		"Subject: " + who + " mail\r\n" +
		"Date: Mon, 2 Jan 2026 15:04:05 -0700\r\n" +
		"Message-ID: <" + who + "@example.org>\r\n" +
		"MIME-Version: 1.0\r\n" +
		"Content-Type: multipart/mixed; boundary=\"b\"\r\n\r\n" +
		"--b\r\nContent-Type: text/plain\r\n\r\n" + who + " body\r\n" +
		"--b\r\nContent-Type: text/plain\r\nContent-Disposition: attachment; filename=\"" + who + ".txt\"\r\n\r\n" +
		who + " payload\r\n--b--\r\n"
}

// TestMessageCacheIsPerAccount proves that two accounts with the same Refs do
// not share cached mail: refs are only unique per server.
func TestMessageCacheIsPerAccount(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)
	ref := model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 1}
	srv.setRawFor("alice", ref, accountMessage("alice"))
	srv.setRawFor("bob", ref, accountMessage("bob"))

	cfg := testConfig(t, testAccount("alice"), testAccount("bob"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")
	ev.wait(t, "bob rows", func() bool { return len(ev.messageRows("bob", "INBOX")) == 1 })

	c.OpenMessage("alice", ref)
	ev.wait(t, "alice view", func() bool { _, ok := ev.openedView(ref); return ok })
	c.OpenMessage("bob", ref)
	ev.wait(t, "bob view", func() bool {
		view, ok := ev.openedView(ref)
		return ok && view.Account == "bob"
	})

	opened := map[string]string{}
	ev.mu.Lock()
	for _, view := range ev.opened {
		opened[view.Account] = view.Subject + "|" + view.PangoBody
	}
	ev.mu.Unlock()
	if !strings.Contains(opened["alice"], "alice mail") {
		t.Errorf("alice view = %q, want alice's message", opened["alice"])
	}
	if !strings.Contains(opened["bob"], "bob mail") {
		t.Errorf("bob view = %q, want bob's message", opened["bob"])
	}
	if strings.Contains(opened["bob"], "alice") {
		t.Errorf("bob was served alice's cached message: %q", opened["bob"])
	}

	dest := t.TempDir() + "/saved.txt"
	if err := c.SaveAttachment("bob", ref, 0, dest); err != nil {
		t.Fatalf("SaveAttachment: %v", err)
	}
	b, err := os.ReadFile(dest)
	if err != nil {
		t.Fatalf("read saved attachment: %v", err)
	}
	if strings.TrimSpace(string(b)) != "bob payload" {
		t.Errorf("bob's attachment content = %q, want bob payload", b)
	}
}

// TestCacheEvictsByByteBudget proves the cache is bounded by decoded size, not
// just entry count.
func TestCacheEvictsByByteBudget(t *testing.T) {
	cfg := testConfig(t, testAccount("alice"))
	c, _, _ := newTestController(t, cfg, newFakeServer())
	c.cacheBudget = 2000

	for uid := uint32(1); uid <= 5; uid++ {
		msg := &model.Message{Plain: strings.Repeat("x", 900)}
		c.cacheMessage("alice", model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: uid}, msg, nil)
	}

	c.mu.Lock()
	size, count := c.cacheSize, len(c.messages)
	_, keptNewest := c.messages[cacheKey{"alice", model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 5}}]
	_, keptOldest := c.messages[cacheKey{"alice", model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: 1}}]
	order := len(c.msgOrder)
	c.mu.Unlock()

	if size > c.cacheBudget {
		t.Errorf("cache size = %d, want <= %d", size, c.cacheBudget)
	}
	if count != 2 || order != 2 {
		t.Errorf("cached entries = %d (order %d), want 2 for a 2000-byte budget of 900-byte messages", count, order)
	}
	if !keptNewest {
		t.Error("newest message was evicted")
	}
	if keptOldest {
		t.Error("oldest message survived; eviction is not oldest-first")
	}
}

// TestSearchLeavesFolderCountsAlone proves a search result set never rewrites
// the folder counters, and new mail does not merge into a search list.
func TestSearchLeavesFolderCountsAlone(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 5)
	srv.setSearch("INBOX", "needle", []uint32{2, 5})
	srv.addEnvelope("INBOX", 42, "Fresh mail", false, time.Now())

	cfg := testConfig(t, testAccount("alice"))
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")
	ev.wait(t, "rows", func() bool { return len(ev.messageRows("alice", "INBOX")) == 6 })

	inbox := func() (total, unread int) {
		folders := ev.foldersFor("alice")
		for _, f := range folders {
			if f.Name == "INBOX" {
				return f.Total, f.Unread
			}
		}
		return 0, 0
	}
	// The folder counts arrive in a second OnFolders after the rows.
	ev.wait(t, "counted folder", func() bool { tot, _ := inbox(); return tot == 6 })
	total, unread := inbox()

	c.Search("alice", "INBOX", "needle")
	ev.wait(t, "search rows", func() bool { return len(ev.messageRows("alice", "INBOX")) == 2 })
	ev.wait(t, "folders after search", func() bool {
		c.mu.Lock()
		defer c.mu.Unlock()
		return c.accounts["alice"].searching
	})

	if gotTotal, gotUnread := inbox(); gotTotal != total || gotUnread != unread {
		t.Errorf("counts after search = %d/%d, want %d/%d", gotTotal, gotUnread, total, unread)
	}

	// A new-mail merge must not touch the search result list.
	st := c.state("alice")
	c.handleNewMail(c.ctx, st, []uint32{42})
	if rows := ev.messageRows("alice", "INBOX"); len(rows) != 2 {
		t.Errorf("rows after new mail = %d, want the 2 search rows", len(rows))
	}
	c.mu.Lock()
	rows, searching := len(st.rows), st.searching
	c.mu.Unlock()
	if rows != 2 || !searching {
		t.Errorf("stored rows = %d (searching %v), want 2 search rows", rows, searching)
	}
	if len(ev.newMailRows()) != 1 {
		t.Errorf("new mail not announced during a search: %+v", ev.newMailRows())
	}

	// Leaving the search restores full counts.
	c.SelectFolder("alice", "INBOX")
	ev.wait(t, "full folder", func() bool {
		tot, _ := inbox()
		return len(ev.messageRows("alice", "INBOX")) == 6 && tot == 6
	})
}
