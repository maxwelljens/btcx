package app

import (
	"sync"

	"btcx/internal/contract"
	"btcx/internal/model"
)

// mailState owns one account's folder list and message rows with their
// invariants (full-folder vs search subset, counts). Its own lock; safe to use
// without Controller.mu.
type mailState struct {
	mu        sync.Mutex
	folders   []model.Mailbox
	counts    map[string]folderCount
	folder    string
	rows      []contract.MessageRow
	searching bool
}

// newMailState returns empty mail state.
func newMailState() *mailState {
	return &mailState{counts: map[string]folderCount{}}
}

// Replace installs rows as the current list. The slice is copied because later
// flag updates mutate the stored rows, and the caller's slice may still be read
// by the UI event it is about to pass on. Only a whole-folder list updates the
// folder counts; a search result is a subset.
func (s *mailState) Replace(folder string, rows []contract.MessageRow, searching bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.folder = folder
	s.rows = append([]contract.MessageRow(nil), rows...)
	s.searching = searching
	if !searching {
		s.counts[folder] = countRows(s.rows)
	}
}

// Update applies fn to the row for ref and keeps the folder counts honest. The
// caller publishes the returned row.
func (s *mailState) Update(ref model.Ref, fn func(*contract.MessageRow)) (contract.MessageRow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.rows {
		if s.rows[i].Ref != ref {
			continue
		}
		fn(&s.rows[i])
		row := s.rows[i]
		// A search result list is a subset of the folder; only recompute the
		// folder counts when the list is the whole folder.
		if !s.searching {
			s.counts[s.folder] = countRows(s.rows)
		}
		return row, true
	}
	return contract.MessageRow{}, false
}

// Remove drops ref from the current list, keeping the folder counts in step: the
// message is gone from the folder even when the row came from a search subset.
func (s *mailState) Remove(ref model.Ref) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := make([]contract.MessageRow, 0, len(s.rows))
	removed := false
	for _, r := range s.rows {
		if r.Ref != ref {
			kept = append(kept, r)
			continue
		}
		removed = true
		if cnt, ok := s.counts[r.Ref.Mailbox]; ok {
			cnt.total = max(cnt.total-1, 0)
			if r.Unread {
				cnt.unread = max(cnt.unread-1, 0)
			}
			s.counts[r.Ref.Mailbox] = cnt
		}
	}
	s.rows = kept
	return removed
}

// MergeInbox folds fresh inbox rows into the current list, newest first, when
// the inbox is shown and no search is active — a search result list is a subset
// of the folder, so merging would mix folder rows into it and corrupt the
// counts. It reports whether it merged, and the published list when it did.
func (s *mailState) MergeInbox(rows []contract.MessageRow) ([]contract.MessageRow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.folder != inboxName || s.searching {
		return nil, false
	}
	s.rows = mergeRows(rows, s.rows)
	s.counts[inboxName] = countRows(s.rows)
	// Copy under the lock: flag updates mutate s.rows in place.
	return append([]contract.MessageRow(nil), s.rows...), true
}

// Row returns a copy of the list row for ref.
func (s *mailState) Row(ref model.Ref) (contract.MessageRow, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.rows {
		if r.Ref == ref {
			return r, true
		}
	}
	return contract.MessageRow{}, false
}

// Rows returns a copy of the current list.
func (s *mailState) Rows() []contract.MessageRow {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]contract.MessageRow(nil), s.rows...)
}

// Current reports the shown folder and whether its rows are a search subset.
func (s *mailState) Current() (folder string, searching bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.folder, s.searching
}

// FolderCounts returns a copy of the per-folder counts.
func (s *mailState) FolderCounts() map[string]folderCount {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]folderCount, len(s.counts))
	for name, cnt := range s.counts {
		out[name] = cnt
	}
	return out
}

// SetFolders installs the account's mailboxes.
func (s *mailState) SetFolders(boxes []model.Mailbox) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.folders = boxes
}

// Folders returns a copy of the account's mailboxes.
func (s *mailState) Folders() []model.Mailbox {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]model.Mailbox(nil), s.folders...)
}
