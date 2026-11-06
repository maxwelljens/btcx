package app

import (
	"testing"
	"time"

	"btcx/internal/contract"
	"btcx/internal/model"
)

func TestWatchDeliversNewMail(t *testing.T) {
	srv := newFakeServer()
	srv.addBox("INBOX")
	srv.seed("INBOX", 1)
	srv.addEnvelope("INBOX", 42, "Fresh mail", false, time.Now())
	srv.setWatch([]uint32{42})

	cfg := testConfig(t, testAccount("alice"))
	cfg.Watch = true
	c, ev, _ := newTestController(t, cfg, srv)
	startApp(t, c, ev, "alice")

	ev.wait(t, "new mail", func() bool { return len(ev.newMailRows()) == 1 })
	mail := ev.newMailRows()[0]
	if mail.Account != "alice" || mail.Row.Ref.UID != 42 {
		t.Fatalf("new mail = %+v, want alice uid 42", mail)
	}
	if mail.Row.Subject != "Fresh mail" {
		t.Errorf("row subject = %q", mail.Row.Subject)
	}

	ev.wait(t, "inbox refresh", func() bool {
		for _, r := range ev.messageRows("alice", "INBOX") {
			if r.Ref.UID == 42 {
				return true
			}
		}
		return false
	})

	c.Shutdown()
	ends := srv.watchEndCopy()
	if len(ends) == 0 || ends[0] == nil {
		t.Fatalf("watch context not cancelled: %v", ends)
	}
}

// TestMailStateCountsFollowFullFolderOnly locks the invariants mailState owns:
// folder counts track only a whole-folder list, search rows never rewrite them,
// and new mail merges only into a shown inbox.
func TestMailStateCountsFollowFullFolderOnly(t *testing.T) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	row := func(uid uint32, unread bool, age time.Duration) contract.MessageRow {
		return contract.MessageRow{
			Ref:    model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: uid},
			Date:   base.Add(age),
			Unread: unread,
		}
	}
	ref := func(uid uint32) model.Ref { return model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: uid} }

	s := newMailState()
	s.SetFolders([]model.Mailbox{{Name: "INBOX"}})
	if got := s.Folders(); len(got) != 1 || got[0].Name != "INBOX" {
		t.Fatalf("Folders = %+v", got)
	}

	s.Replace("INBOX", []contract.MessageRow{row(1, true, time.Minute), row(2, true, 2*time.Minute)}, false)
	if got, want := s.FolderCounts()["INBOX"], (folderCount{total: 2, unread: 2}); got != want {
		t.Fatalf("counts after replace = %+v, want %+v", got, want)
	}

	// A flag update keeps the counts honest.
	if _, ok := s.Update(ref(1), func(r *contract.MessageRow) { r.Unread = false }); !ok {
		t.Fatal("Update missed a listed row")
	}
	if got := s.FolderCounts()["INBOX"]; got != (folderCount{total: 2, unread: 1}) {
		t.Errorf("counts after update = %+v, want 2/1", got)
	}
	if _, ok := s.Update(ref(99), func(*contract.MessageRow) {}); ok {
		t.Error("Update reported an unlisted row")
	}

	// A search result is a subset: neither installing it nor updating a row in
	// it may rewrite the counts.
	s.Replace("INBOX", []contract.MessageRow{row(2, true, 2*time.Minute)}, true)
	s.Update(ref(2), func(r *contract.MessageRow) { r.Unread = false })
	if got := s.FolderCounts()["INBOX"]; got != (folderCount{total: 2, unread: 1}) {
		t.Errorf("counts after search = %+v, want the folder's 2/1", got)
	}
	if folder, searching := s.Current(); folder != "INBOX" || !searching {
		t.Errorf("Current = %q/%v, want INBOX/search", folder, searching)
	}

	// New mail must not merge into a search list.
	if rows, merged := s.MergeInbox([]contract.MessageRow{row(3, true, 3*time.Minute)}); merged || rows != nil {
		t.Errorf("merge into a search list = %v/%v, want refused", rows, merged)
	}
	if rows := s.Rows(); len(rows) != 1 {
		t.Errorf("rows after refused merge = %d, want the search row", len(rows))
	}

	// Back to the whole folder: a merge lands newest first.
	s.Replace("INBOX", []contract.MessageRow{row(2, true, 2*time.Minute), row(1, true, time.Minute)}, false)
	rows, merged := s.MergeInbox([]contract.MessageRow{row(3, true, 3*time.Minute), row(2, true, 2*time.Minute)})
	if !merged {
		t.Fatal("merge refused on a shown inbox")
	}
	if len(rows) != 3 || rows[0].Ref.UID != 3 || rows[1].Ref.UID != 2 || rows[2].Ref.UID != 1 {
		t.Fatalf("merged rows = %+v, want UIDs 3,2,1", rows)
	}
	if got := s.FolderCounts()[inboxName]; got != (folderCount{total: 3, unread: 3}) {
		t.Errorf("counts after merge = %+v, want 3/3", got)
	}

	// Another folder is not the inbox.
	s.Replace("Sent", []contract.MessageRow{row(4, false, 4*time.Minute)}, false)
	if _, merged := s.MergeInbox([]contract.MessageRow{row(5, true, 5*time.Minute)}); merged {
		t.Error("merge accepted while another folder is shown")
	}

	// Rows hands out a copy: flag updates mutate the stored rows in place.
	s.Rows()[0].Subject = "mutated"
	if got := s.Rows()[0].Subject; got == "mutated" {
		t.Error("Rows handed out the stored slice")
	}
}

// TestMailStateRemoveKeepsCounts proves a removed row leaves both the list and
// the counts — a deleted message is gone from the folder even when its row came
// from a search subset — and that the bool reports the removal.
func TestMailStateRemoveKeepsCounts(t *testing.T) {
	ref := func(uid uint32) model.Ref { return model.Ref{Mailbox: "INBOX", UIDValidity: 1, UID: uid} }
	row := func(uid uint32, unread bool) contract.MessageRow {
		return contract.MessageRow{Ref: ref(uid), Unread: unread}
	}

	s := newMailState()
	s.Replace("INBOX", []contract.MessageRow{row(1, true), row(2, false)}, false)
	if !s.Remove(ref(1)) {
		t.Fatal("Remove missed a listed row")
	}
	if s.Remove(ref(1)) {
		t.Error("Remove reported a row twice")
	}
	if rows := s.Rows(); len(rows) != 1 || rows[0].Ref != ref(2) {
		t.Fatalf("rows after remove = %+v, want only uid 2", rows)
	}
	if got := s.FolderCounts()["INBOX"]; got != (folderCount{total: 1, unread: 0}) {
		t.Errorf("counts after remove = %+v, want 1/0", got)
	}

	s.Replace("INBOX", []contract.MessageRow{row(2, false)}, true)
	s.Remove(ref(2))
	if got := s.FolderCounts()["INBOX"]; got != (folderCount{total: 0, unread: 0}) {
		t.Errorf("counts after removing a search row = %+v, want 0/0", got)
	}
}
