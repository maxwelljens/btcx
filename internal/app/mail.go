package app

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"btcx/internal/config"
	"btcx/internal/contract"
	"btcx/internal/mailparse"
	"btcx/internal/model"
	"btcx/internal/render"
	"btcx/internal/sanitize"
)

// syncFolders lists the account's mailboxes, publishes them, then loads the
// current folder (or the initial one on first run).
func (c *Controller) syncFolders(ctx context.Context, st *accountState) error {
	cl, err := c.clientFor(ctx, st)
	if err != nil {
		return err
	}
	boxes, err := cl.Mailboxes(ctx)
	if err != nil {
		c.dropClient(st)
		return fmt.Errorf("list mailboxes: %w", err)
	}

	c.mu.Lock()
	st.folders = boxes
	folder := st.folder
	c.mu.Unlock()
	c.events.OnFolders(st.cfg.Name, c.folderInfos(st))

	if folder == "" || !hasMailbox(boxes, folder) {
		folder = initialFolder(boxes)
	}
	if folder == "" {
		return nil
	}
	return c.loadFolder(ctx, st, folder)
}

// loadFolder replaces the message list with folder's first pageSize
// envelopes by UID, shown newest first.
func (c *Controller) loadFolder(ctx context.Context, st *accountState, folder string) error {
	if folder == "" {
		return errors.New("empty folder name")
	}
	cl, err := c.clientFor(ctx, st)
	if err != nil {
		return err
	}
	envs, err := cl.Envelopes(ctx, folder, 1, pageSize)
	if err != nil {
		c.dropClient(st)
		return fmt.Errorf("load %q: %w", folder, err)
	}
	rows := rowsFromEnvelopes(envs)
	c.storeRows(st, folder, rows)
	c.events.OnMessages(st.cfg.Name, folder, rows)
	c.events.OnFolders(st.cfg.Name, c.folderInfos(st))
	return nil
}

// SelectFolder loads a folder's message list.
func (c *Controller) SelectFolder(account, folder string) {
	st := c.state(account)
	if st == nil {
		c.fail(fmt.Errorf("app: select folder: unknown account %q", account))
		return
	}
	c.goRun(func(ctx context.Context) {
		if err := c.loadFolder(ctx, st, folder); err != nil {
			c.fail(fmt.Errorf("app: select %s/%s: %w", account, folder, err))
		}
	})
}

// Refresh re-lists the folders and reloads the current one.
func (c *Controller) Refresh(account string) {
	st := c.state(account)
	if st == nil {
		c.fail(fmt.Errorf("app: refresh: unknown account %q", account))
		return
	}
	c.goRun(func(ctx context.Context) {
		if err := c.syncFolders(ctx, st); err != nil {
			c.fail(fmt.Errorf("app: refresh %s: %w", account, err))
		}
	})
}

// Search replaces the message list with the messages matching query. An empty
// query reloads the current folder.
func (c *Controller) Search(account, folder, query string) {
	st := c.state(account)
	if st == nil {
		c.fail(fmt.Errorf("app: search: unknown account %q", account))
		return
	}
	if strings.TrimSpace(query) == "" {
		c.Refresh(account)
		return
	}
	c.goRun(func(ctx context.Context) {
		cl, err := c.clientFor(ctx, st)
		if err != nil {
			c.fail(fmt.Errorf("app: search %s: %w", account, err))
			return
		}
		uids, err := cl.Search(ctx, folder, query)
		if err != nil {
			c.dropClient(st)
			c.fail(fmt.Errorf("app: search %s: %w", account, err))
			return
		}
		envs, err := envelopesForUIDs(ctx, cl, folder, uids)
		if err != nil {
			c.dropClient(st)
			c.fail(fmt.Errorf("app: search %s: %w", account, err))
			return
		}
		rows := rowsFromEnvelopes(envs)
		c.storeRows(st, folder, rows)
		c.events.OnMessages(st.cfg.Name, folder, rows)
	})
}

// OpenMessage fetches, parses, sanitizes and renders one message, marks it
// seen, and delivers it through OnMessageOpened and OnMessageChanged.
func (c *Controller) OpenMessage(account string, ref model.Ref) {
	st := c.state(account)
	if st == nil {
		c.fail(fmt.Errorf("app: open: unknown account %q", account))
		return
	}
	c.goRun(func(ctx context.Context) {
		msg, err := c.message(ctx, st, ref)
		if err != nil {
			c.fail(fmt.Errorf("app: open %s: %w", ref, err))
			return
		}
		view := c.view(st, ref, msg)
		row, known := c.row(st, ref)
		if !known || row.Unread {
			if err := c.applyFlags(ctx, st, ref, model.Flags{Seen: true}, model.Flags{}); err != nil {
				c.fail(fmt.Errorf("app: mark seen %s: %w", ref, err))
			} else {
				view.Unread = false
				if changed, ok := c.updateRow(st, ref, func(r *contract.MessageRow) { r.Unread = false }); ok {
					c.events.OnMessageOpened(view)
					c.events.OnMessageChanged(changed)
					return
				}
			}
		}
		c.events.OnMessageOpened(view)
	})
}

// MarkSeen adds or removes \Seen.
func (c *Controller) MarkSeen(account string, ref model.Ref, seen bool) {
	add, remove := model.Flags{}, model.Flags{}
	update := func(r *contract.MessageRow) { r.Unread = !seen }
	if seen {
		add.Seen = true
	} else {
		remove.Seen = true
	}
	c.setFlag(account, ref, add, remove, update)
}

// SetFlagged adds or removes \Flagged.
func (c *Controller) SetFlagged(account string, ref model.Ref, flagged bool) {
	add, remove := model.Flags{}, model.Flags{}
	update := func(r *contract.MessageRow) { r.Flagged = flagged }
	if flagged {
		add.Flagged = true
	} else {
		remove.Flagged = true
	}
	c.setFlag(account, ref, add, remove, update)
}

// setFlag applies a flag change on the server and in the list.
func (c *Controller) setFlag(account string, ref model.Ref, add, remove model.Flags, update func(*contract.MessageRow)) {
	st := c.state(account)
	if st == nil {
		c.fail(fmt.Errorf("app: flag: unknown account %q", account))
		return
	}
	c.goRun(func(ctx context.Context) {
		if err := c.applyFlags(ctx, st, ref, add, remove); err != nil {
			c.fail(fmt.Errorf("app: flag %s: %w", ref, err))
			return
		}
		if row, ok := c.updateRow(st, ref, update); ok {
			c.events.OnMessageChanged(row)
			c.events.OnFolders(st.cfg.Name, c.folderInfos(st))
		}
	})
}

// DeleteMessage moves the message to the trash, falling back to \Deleted when
// no trash mailbox is configured.
func (c *Controller) DeleteMessage(account string, ref model.Ref) {
	st := c.state(account)
	if st == nil {
		c.fail(fmt.Errorf("app: delete: unknown account %q", account))
		return
	}
	c.goRun(func(ctx context.Context) {
		cl, err := c.clientFor(ctx, st)
		if err != nil {
			c.fail(fmt.Errorf("app: delete %s: %w", ref, err))
			return
		}
		if dest := st.cfg.TrashMailbox; dest != "" && dest != ref.Mailbox {
			err = cl.Move(ctx, ref.Mailbox, ref.UID, dest)
		} else {
			err = cl.SetFlags(ctx, ref.Mailbox, ref.UID, model.Flags{Deleted: true}, model.Flags{})
		}
		if err != nil {
			c.dropClient(st)
			c.fail(fmt.Errorf("app: delete %s: %w", ref, err))
			return
		}
		c.removeRow(st, ref)
	})
}

// SaveAttachment writes one attachment to destPath. It blocks on the network
// when the message is not cached, so the UI should offer it from the reader
// pane where the message has already been opened.
func (c *Controller) SaveAttachment(account string, ref model.Ref, index int, destPath string) error {
	st := c.state(account)
	if st == nil {
		return fmt.Errorf("app: save attachment: unknown account %q", account)
	}
	if destPath == "" {
		return errors.New("app: save attachment: empty destination path")
	}
	msg, err := c.messageSync(st, ref)
	if err != nil {
		return fmt.Errorf("app: save attachment: %w", err)
	}
	if index < 0 || index >= len(msg.Attachments) {
		return fmt.Errorf("app: save attachment: index %d out of range", index)
	}
	if err := os.WriteFile(destPath, msg.Attachments[index].Data, 0o600); err != nil {
		return fmt.Errorf("app: save attachment: %w", err)
	}
	return nil
}

// storeRows installs rows as the current list. The slice is copied because
// later flag updates mutate the stored rows, and the caller's slice may still
// be read by the UI event it is about to pass on.
func (c *Controller) storeRows(st *accountState, folder string, rows []contract.MessageRow) {
	c.mu.Lock()
	defer c.mu.Unlock()
	st.folder = folder
	st.rows = append([]contract.MessageRow(nil), rows...)
	st.counts[folder] = countRows(st.rows)
}

// message returns the parsed message for ref, fetching it when not cached.
func (c *Controller) message(ctx context.Context, st *accountState, ref model.Ref) (*model.Message, error) {
	c.mu.Lock()
	cached := c.messages[ref]
	c.mu.Unlock()
	if cached != nil {
		return cached, nil
	}

	cl, err := c.clientFor(ctx, st)
	if err != nil {
		return nil, err
	}
	raw, err := cl.RawMessage(ctx, ref.Mailbox, ref.UID)
	if err != nil {
		c.dropClient(st)
		return nil, fmt.Errorf("fetch: %w", err)
	}
	msg, err := mailparse.ParseBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("parse: %w", err)
	}
	c.cacheMessage(ref, msg)
	return msg, nil
}

// messageSync fetches a message for the synchronous Controller methods,
// bounded by syncCallTimeout.
func (c *Controller) messageSync(st *accountState, ref model.Ref) (*model.Message, error) {
	ctx, cancel := context.WithTimeout(c.ctx, syncCallTimeout)
	defer cancel()
	return c.message(ctx, st, ref)
}

// cacheMessage stores msg, evicting the oldest entry beyond the cap.
func (c *Controller) cacheMessage(ref model.Ref, msg *model.Message) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.messages[ref]; ok {
		c.messages[ref] = msg
		return
	}
	c.messages[ref] = msg
	c.msgOrder = append(c.msgOrder, ref)
	for len(c.msgOrder) > maxCachedMessages {
		delete(c.messages, c.msgOrder[0])
		c.msgOrder = c.msgOrder[1:]
	}
}

// view builds the reader-pane content for msg.
func (c *Controller) view(st *accountState, ref model.Ref, msg *model.Message) contract.MessageView {
	row, _ := c.row(st, ref)
	body := render.PangoFromPlain(msg.Plain)
	if strings.TrimSpace(msg.HTML) != "" {
		body = render.Pango(msg.HTML)
	}
	date, _ := msg.Date()
	return contract.MessageView{
		Account:              st.cfg.Name,
		Ref:                  ref,
		Subject:              msg.Subject(),
		From:                 msg.From(),
		To:                   msg.To(),
		Cc:                   msg.Cc(),
		Date:                 date,
		PangoBody:            body,
		ShowRemoteContentBar: c.cfg.BlockRemoteContent && msg.HTML != "" && sanitize.ReferencesRemoteContent(msg.HTML),
		RemoteImages:         inlineImages(msg),
		Attachments:          attachmentInfos(msg),
		Unread:               row.Unread,
		Flagged:              row.Flagged,
	}
}

// attachmentInfos describes msg's attachments with sanitized names.
func attachmentInfos(msg *model.Message) []contract.AttachmentInfo {
	out := make([]contract.AttachmentInfo, 0, len(msg.Attachments))
	for i, a := range msg.Attachments {
		out = append(out, contract.AttachmentInfo{
			Index:       i,
			Filename:    safeFilename(a.Filename),
			ContentType: a.ContentType,
			Size:        int64(len(a.Data)),
			Inline:      a.Inline,
		})
	}
	return out
}

// applyFlags changes one message's flags on the server.
func (c *Controller) applyFlags(ctx context.Context, st *accountState, ref model.Ref, add, remove model.Flags) error {
	cl, err := c.clientFor(ctx, st)
	if err != nil {
		return err
	}
	if err = cl.SetFlags(ctx, ref.Mailbox, ref.UID, add, remove); err != nil {
		c.dropClient(st)
		return fmt.Errorf("set flags: %w", err)
	}
	return nil
}

// row returns a copy of the list row for ref.
func (c *Controller) row(st *accountState, ref model.Ref) (contract.MessageRow, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, r := range st.rows {
		if r.Ref == ref {
			return r, true
		}
	}
	return contract.MessageRow{}, false
}

// updateRow applies update to the row for ref and keeps the folder counts
// honest. The caller publishes the returned row.
func (c *Controller) updateRow(st *accountState, ref model.Ref, update func(*contract.MessageRow)) (contract.MessageRow, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for i := range st.rows {
		if st.rows[i].Ref != ref {
			continue
		}
		update(&st.rows[i])
		row := st.rows[i]
		// A search result list is a subset of the folder; only recompute the
		// folder counts when the list is the whole folder.
		if cnt, ok := st.counts[st.folder]; ok && st.folder == row.Ref.Mailbox && cnt.total == len(st.rows) {
			st.counts[st.folder] = countRows(st.rows)
		}
		return row, true
	}
	return contract.MessageRow{}, false
}

// removeRow drops ref from the current list and the cache, keeping the folder
// counts in step.
func (c *Controller) removeRow(st *accountState, ref model.Ref) {
	c.mu.Lock()
	kept := make([]contract.MessageRow, 0, len(st.rows))
	for _, r := range st.rows {
		if r.Ref != ref {
			kept = append(kept, r)
			continue
		}
		if cnt, ok := st.counts[r.Ref.Mailbox]; ok {
			cnt.total = max(cnt.total-1, 0)
			if r.Unread {
				cnt.unread = max(cnt.unread-1, 0)
			}
			st.counts[r.Ref.Mailbox] = cnt
		}
	}
	st.rows = kept
	folder := st.folder
	delete(c.messages, ref)
	c.mu.Unlock()

	c.events.OnMessages(st.cfg.Name, folder, kept)
	c.events.OnFolders(st.cfg.Name, c.folderInfos(st))
}

// folderInfos converts the account's mailboxes into UI folder entries.
func (c *Controller) folderInfos(st *accountState) []contract.FolderInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]contract.FolderInfo, 0, len(st.folders))
	for _, m := range st.folders {
		count := st.counts[m.Name]
		out = append(out, contract.FolderInfo{
			Name:   m.Name,
			Role:   folderRole(st.cfg, m),
			Total:  count.total,
			Unread: count.unread,
		})
	}
	return out
}

// folderRole reports the mailbox role, falling back to the configured special
// mailboxes when the server advertises no special-use attribute.
func folderRole(a config.Account, m model.Mailbox) string {
	if r := m.Role(); r != "" {
		return r
	}
	switch m.Name {
	case a.SentMailbox:
		return "sent"
	case a.DraftsMailbox:
		return "drafts"
	case a.TrashMailbox:
		return "trash"
	}
	return ""
}

// countRows recomputes a folder's total and unread counts.
func countRows(rows []contract.MessageRow) folderCount {
	count := folderCount{total: len(rows)}
	for _, r := range rows {
		if r.Unread {
			count.unread++
		}
	}
	return count
}

// rowFromEnvelope converts one envelope into a list row.
func rowFromEnvelope(e model.Envelope) contract.MessageRow {
	var from model.Address
	if len(e.From) > 0 {
		from = e.From[0]
	}
	return contract.MessageRow{
		Ref:            e.Ref,
		Subject:        e.Subject,
		From:           from,
		Date:           e.Date,
		Unread:         !e.Flags.Seen,
		Flagged:        e.Flags.Flagged,
		HasAttachments: e.HasAttachments,
	}
}

// rowsFromEnvelopes converts envelopes into list rows, newest first, capped at
// pageSize.
func rowsFromEnvelopes(envs []model.Envelope) []contract.MessageRow {
	rows := make([]contract.MessageRow, 0, len(envs))
	for _, e := range envs {
		rows = append(rows, rowFromEnvelope(e))
	}
	sortRows(rows)
	if len(rows) > pageSize {
		rows = rows[:pageSize]
	}
	return rows
}

// sortRows orders rows by date, newest first, breaking ties by UID.
func sortRows(rows []contract.MessageRow) {
	sort.SliceStable(rows, func(i, j int) bool {
		if !rows[i].Date.Equal(rows[j].Date) {
			return rows[i].Date.After(rows[j].Date)
		}
		return rows[i].Ref.UID > rows[j].Ref.UID
	})
}

// mergeRows folds fresh rows into the current list, newest first.
func mergeRows(fresh, current []contract.MessageRow) []contract.MessageRow {
	seen := make(map[model.Ref]bool, len(fresh))
	out := make([]contract.MessageRow, 0, len(fresh)+len(current))
	for _, r := range fresh {
		seen[r.Ref] = true
		out = append(out, r)
	}
	for _, r := range current {
		if !seen[r.Ref] {
			out = append(out, r)
		}
	}
	sortRows(out)
	if len(out) > pageSize {
		out = out[:pageSize]
	}
	return out
}

// hasMailbox reports whether boxes contains name (INBOX is case-insensitive).
func hasMailbox(boxes []model.Mailbox, name string) bool {
	for _, m := range boxes {
		if m.Name == name || (strings.EqualFold(m.Name, inboxName) && strings.EqualFold(name, inboxName)) {
			return true
		}
	}
	return false
}

// initialFolder picks INBOX, else the first selectable mailbox.
func initialFolder(boxes []model.Mailbox) string {
	for _, m := range boxes {
		if strings.EqualFold(m.Name, inboxName) {
			return m.Name
		}
	}
	for _, m := range boxes {
		if !containsAttr(m.Attributes, `\Noselect`) {
			return m.Name
		}
	}
	return ""
}

// containsAttr reports whether attributes contains attr (case-insensitive).
func containsAttr(attributes []string, attr string) bool {
	for _, a := range attributes {
		if strings.EqualFold(a, attr) {
			return true
		}
	}
	return false
}

// envelopesForUIDs fetches summaries for search hits.
//
// shortcut: one request per contiguous UID run, capped at pageSize results; a
// search returning very sparse hits still costs one round trip per run.
func envelopesForUIDs(ctx context.Context, cl MailClient, mailbox string, uids []uint32) ([]model.Envelope, error) {
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	if len(uids) > pageSize {
		uids = uids[len(uids)-pageSize:]
	}
	var out []model.Envelope
	for i := 0; i < len(uids); {
		j := i
		for j+1 < len(uids) && uids[j+1] == uids[j]+1 {
			j++
		}
		envs, err := cl.Envelopes(ctx, mailbox, uids[i], j-i+1)
		if err != nil {
			return nil, fmt.Errorf("envelopes: %w", err)
		}
		out = append(out, envs...)
		i = j + 1
	}
	return out, nil
}

// safeFilename reduces an untrusted attachment name to a single path element.
func safeFilename(name string) string {
	name = strings.NewReplacer("\r", "", "\n", "", "\x00", "", "/", "", `\`, "").Replace(name)
	name = strings.TrimSpace(filepath.Base(name))
	switch name {
	case "", ".", "..", "/":
		return ""
	}
	return name
}
