package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"btcx/internal/config"
	"btcx/internal/contract"
	"btcx/internal/model"
)

// ---- fake IMAP server ----

type fakeAppend struct {
	Mailbox string
	Raw     []byte
	Flags   model.Flags
}

type fakeMove struct {
	Mailbox string
	UID     uint32
	Dest    string
}

type fakeFlagCall struct {
	Mailbox string
	UID     uint32
	Add     model.Flags
	Remove  model.Flags
}

type fakeSend struct {
	Account string
	Raw     []byte
	Rcpts   []model.Address
}

// fakeServer holds the data every fakeClient shares.
type fakeServer struct {
	mu        sync.Mutex
	boxes     []model.Mailbox
	envelopes map[string][]model.Envelope
	// envelopesErr, when set, is returned alongside envelope rows to
	// simulate a degraded fetch (server failure with usable mail).
	envelopesErr error
	raw          map[model.Ref][]byte
	// rawByAccount overrides raw per account, so tests can give two accounts
	// different bytes for the same ref.
	rawByAccount map[string]map[model.Ref][]byte
	search       map[string][]uint32
	watchUIDs    []uint32

	appends   []fakeAppend
	moves     []fakeMove
	flagCalls []fakeFlagCall
	sends     []fakeSend
	watchEnd  []error
	dials     int

	watchStarted chan struct{}
	watchOnce    sync.Once
}

func newFakeServer() *fakeServer {
	return &fakeServer{
		envelopes:    map[string][]model.Envelope{},
		raw:          map[model.Ref][]byte{},
		rawByAccount: map[string]map[model.Ref][]byte{},
		search:       map[string][]uint32{},
		watchStarted: make(chan struct{}),
	}
}

func (s *fakeServer) addBox(name string, attrs ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.boxes = append(s.boxes, model.Mailbox{Name: name, Delimiter: "/", Attributes: attrs})
}

// seed adds n envelopes with ascending UIDs and dates; even UIDs are seen.
func (s *fakeServer) seed(mailbox string, n int) {
	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	for i := 1; i <= n; i++ {
		s.addEnvelope(mailbox, uint32(i), fmt.Sprintf("Subject %d", i), i%2 == 0, base.Add(time.Duration(i)*time.Minute))
	}
}

func (s *fakeServer) addEnvelope(mailbox string, uid uint32, subject string, seen bool, date time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.envelopes[mailbox] = append(s.envelopes[mailbox], model.Envelope{
		Ref:       model.Ref{Mailbox: mailbox, UIDValidity: 1, UID: uid},
		MessageID: fmt.Sprintf("m%d@example.org", uid),
		Subject:   subject,
		From:      []model.Address{{Name: "Sender", Addr: "sender@example.org"}},
		To:        []model.Address{{Name: "Me", Addr: "me@example.org"}},
		Date:      date,
		Flags:     model.Flags{Seen: seen},
	})
}

func (s *fakeServer) setRaw(ref model.Ref, raw string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.raw[ref] = []byte(raw)
}

// setRawFor stores bytes only account sees for ref.
func (s *fakeServer) setRawFor(account string, ref model.Ref, raw string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rawByAccount[account] == nil {
		s.rawByAccount[account] = map[model.Ref][]byte{}
	}
	s.rawByAccount[account][ref] = []byte(raw)
}

func (s *fakeServer) setSearch(mailbox, query string, uids []uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.search[mailbox+"\x00"+query] = uids
}

func (s *fakeServer) setWatch(uids []uint32) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.watchUIDs = uids
}

func (s *fakeServer) appendsCopy() []fakeAppend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeAppend(nil), s.appends...)
}

func (s *fakeServer) movesCopy() []fakeMove {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeMove(nil), s.moves...)
}

func (s *fakeServer) flagCallsCopy() []fakeFlagCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeFlagCall(nil), s.flagCalls...)
}

func (s *fakeServer) sendsCopy() []fakeSend {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]fakeSend(nil), s.sends...)
}

func (s *fakeServer) watchEndCopy() []error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]error(nil), s.watchEnd...)
}

func (s *fakeServer) dialCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.dials
}

// awaitWatchStart blocks until some client entered Watch.
func (s *fakeServer) awaitWatchStart(t *testing.T) {
	t.Helper()
	select {
	case <-s.watchStarted:
	case <-time.After(10 * time.Second):
		t.Fatal("watcher did not start")
	}
}

// ---- fake IMAP client ----

type fakeClient struct {
	s       *fakeServer
	account string
}

func (f *fakeClient) Mailboxes(ctx context.Context) ([]model.Mailbox, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	return append([]model.Mailbox(nil), f.s.boxes...), nil
}

func (f *fakeClient) Envelopes(ctx context.Context, mailbox string, startUID uint32, count int) ([]model.Envelope, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	if startUID == 0 {
		startUID = 1
	}
	var out []model.Envelope
	for _, e := range f.s.envelopes[mailbox] {
		if e.Ref.UID < startUID {
			continue
		}
		out = append(out, e)
		if count > 0 && len(out) >= count {
			break
		}
	}
	if f.s.envelopesErr != nil && len(out) > 0 {
		return out, f.s.envelopesErr
	}
	return out, nil
}

func (f *fakeClient) RawMessage(ctx context.Context, mailbox string, uid uint32) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	ref := model.Ref{Mailbox: mailbox, UIDValidity: 1, UID: uid}
	raw, ok := f.s.rawByAccount[f.account][ref]
	if !ok {
		raw, ok = f.s.raw[ref]
	}
	if !ok {
		return nil, fmt.Errorf("fake: no message %s/%d", mailbox, uid)
	}
	return raw, nil
}

func (f *fakeClient) SetFlags(ctx context.Context, mailbox string, uid uint32, add, remove model.Flags) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	f.s.flagCalls = append(f.s.flagCalls, fakeFlagCall{Mailbox: mailbox, UID: uid, Add: add, Remove: remove})
	return nil
}

func (f *fakeClient) Search(ctx context.Context, mailbox, text string) ([]uint32, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	return append([]uint32(nil), f.s.search[mailbox+"\x00"+text]...), nil
}

func (f *fakeClient) Append(ctx context.Context, mailbox string, raw []byte, flags model.Flags) (model.Ref, error) {
	if err := ctx.Err(); err != nil {
		return model.Ref{}, err
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	f.s.appends = append(f.s.appends, fakeAppend{Mailbox: mailbox, Raw: append([]byte(nil), raw...), Flags: flags})
	return model.Ref{Mailbox: mailbox, UIDValidity: 1, UID: 900}, nil
}

func (f *fakeClient) Move(ctx context.Context, mailbox string, uid uint32, dest string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	f.s.mu.Lock()
	defer f.s.mu.Unlock()
	f.s.moves = append(f.s.moves, fakeMove{Mailbox: mailbox, UID: uid, Dest: dest})
	return nil
}

func (f *fakeClient) Watch(ctx context.Context, mailbox string, onNew func([]uint32)) error {
	f.s.watchOnce.Do(func() { close(f.s.watchStarted) })
	f.s.mu.Lock()
	uids := append([]uint32(nil), f.s.watchUIDs...)
	f.s.mu.Unlock()
	if len(uids) > 0 {
		onNew(uids)
	}
	<-ctx.Done()
	f.s.mu.Lock()
	f.s.watchEnd = append(f.s.watchEnd, ctx.Err())
	f.s.mu.Unlock()
	return nil
}

func (f *fakeClient) Close() error { return nil }

// ---- fake connector ----

type fakeConnector struct {
	server       *fakeServer
	sendErr      error
	dialErr      error
	dialFailures int // fail this many dials before succeeding
}

func (f *fakeConnector) DialIMAP(ctx context.Context, a config.Account) (MailClient, error) {
	f.server.mu.Lock()
	f.server.dials++
	f.server.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.dialErr != nil {
		return nil, f.dialErr
	}
	f.server.mu.Lock()
	failing := f.dialFailures > 0
	if failing {
		f.dialFailures--
	}
	f.server.mu.Unlock()
	if failing {
		return nil, errors.New("fake: dial failed")
	}
	return &fakeClient{s: f.server, account: a.Name}, nil
}

func (f *fakeConnector) Send(ctx context.Context, a config.Account, raw []byte, rcpts []model.Address) error {
	f.server.mu.Lock()
	f.server.sends = append(f.server.sends, fakeSend{
		Account: a.Name,
		Raw:     append([]byte(nil), raw...),
		Rcpts:   append([]model.Address(nil), rcpts...),
	})
	err := f.sendErr
	f.server.mu.Unlock()
	if err != nil {
		return err
	}
	return ctx.Err()
}

// ---- recording events ----

type messagesEvent struct {
	Account string
	Folder  string
	Rows    []contract.MessageRow
}

type mailEvent struct {
	Account string
	Row     contract.MessageRow
}

type sendOutcome struct {
	Success bool
	Message string
}

type editOutcome struct {
	ID    contract.EditRequestID
	Draft contract.ComposeDraft
	Err   error
}

type recEvents struct {
	mu       sync.Mutex
	statuses []string
	errs     []error
	folders  map[string][]contract.FolderInfo
	messages []messagesEvent
	changed  []contract.MessageRow
	newMail  []mailEvent
	opened   []contract.MessageView
	sends    []sendOutcome
	verifies []contract.VerifyResult
	edits    []editOutcome
	notify   chan struct{}
}

func newEvents() *recEvents {
	return &recEvents{folders: map[string][]contract.FolderInfo{}, notify: make(chan struct{}, 1)}
}

func (r *recEvents) signal() {
	select {
	case r.notify <- struct{}{}:
	default:
	}
}

func (r *recEvents) OnStatus(message string) {
	r.mu.Lock()
	r.statuses = append(r.statuses, message)
	r.mu.Unlock()
	r.signal()
}

func (r *recEvents) OnError(err error) {
	r.mu.Lock()
	r.errs = append(r.errs, err)
	r.mu.Unlock()
	r.signal()
}

func (r *recEvents) OnFolders(account string, folders []contract.FolderInfo) {
	r.mu.Lock()
	r.folders[account] = append([]contract.FolderInfo(nil), folders...)
	r.mu.Unlock()
	r.signal()
}

func (r *recEvents) OnMessages(account, folder string, rows []contract.MessageRow) {
	r.mu.Lock()
	r.messages = append(r.messages, messagesEvent{Account: account, Folder: folder, Rows: append([]contract.MessageRow(nil), rows...)})
	r.mu.Unlock()
	r.signal()
}

func (r *recEvents) OnMessageChanged(row contract.MessageRow) {
	r.mu.Lock()
	r.changed = append(r.changed, row)
	r.mu.Unlock()
	r.signal()
}

func (r *recEvents) OnNewMail(account string, row contract.MessageRow) {
	r.mu.Lock()
	r.newMail = append(r.newMail, mailEvent{Account: account, Row: row})
	r.mu.Unlock()
	r.signal()
}

func (r *recEvents) OnMessageOpened(view contract.MessageView) {
	r.mu.Lock()
	r.opened = append(r.opened, view)
	r.mu.Unlock()
	r.signal()
}

func (r *recEvents) OnSendResult(success bool, message string) {
	r.mu.Lock()
	r.sends = append(r.sends, sendOutcome{Success: success, Message: message})
	r.mu.Unlock()
	r.signal()
}

func (r *recEvents) OnVerifyResult(result contract.VerifyResult) {
	r.mu.Lock()
	r.verifies = append(r.verifies, result)
	r.mu.Unlock()
	r.signal()
}

func (r *recEvents) OnExternalEditDone(id contract.EditRequestID, draft contract.ComposeDraft, err error) {
	r.mu.Lock()
	r.edits = append(r.edits, editOutcome{ID: id, Draft: draft, Err: err})
	r.mu.Unlock()
	r.signal()
}

// wait blocks until cond is true. cond calls the locking accessors below.
func (r *recEvents) wait(t *testing.T, what string, cond func() bool) {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		if cond() {
			return
		}
		select {
		case <-r.notify:
		case <-timeout:
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func (r *recEvents) foldersFor(account string) []contract.FolderInfo {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contract.FolderInfo(nil), r.folders[account]...)
}

func (r *recEvents) messageEvents() []messagesEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]messagesEvent(nil), r.messages...)
}

func (r *recEvents) messageRows(account, folder string) []contract.MessageRow {
	events := r.messageEvents()
	for i := len(events) - 1; i >= 0; i-- {
		if events[i].Account == account && events[i].Folder == folder {
			return events[i].Rows
		}
	}
	return nil
}

func (r *recEvents) openedView(ref model.Ref) (contract.MessageView, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.opened) - 1; i >= 0; i-- {
		if r.opened[i].Ref == ref {
			return r.opened[i], true
		}
	}
	return contract.MessageView{}, false
}

func (r *recEvents) changedRow(ref model.Ref) (contract.MessageRow, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := len(r.changed) - 1; i >= 0; i-- {
		if r.changed[i].Ref == ref {
			return r.changed[i], true
		}
	}
	return contract.MessageRow{}, false
}

func (r *recEvents) newMailRows() []mailEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mailEvent(nil), r.newMail...)
}

func (r *recEvents) sendResults() []sendOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]sendOutcome(nil), r.sends...)
}

func (r *recEvents) editResults() []editOutcome {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]editOutcome(nil), r.edits...)
}

func (r *recEvents) verifyResults() []contract.VerifyResult {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]contract.VerifyResult(nil), r.verifies...)
}

func (r *recEvents) statusList() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.statuses...)
}

func (r *recEvents) errText() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var b strings.Builder
	for _, err := range r.errs {
		b.WriteString(err.Error())
		b.WriteString("\n")
	}
	return b.String()
}

// ---- test helpers ----

func testAccount(name string) config.Account {
	return config.Account{
		Name:          name,
		DisplayName:   "Display " + name,
		RealName:      "Real " + name,
		Email:         name + "@example.org",
		IMAP:          config.Server{Host: "imap.example.org", Port: 993, Username: name, Password: "secret", TLS: config.TLSImplicit},
		SMTP:          config.Server{Host: "smtp.example.org", Port: 587, Username: name, Password: "secret", TLS: config.TLSStartTLS},
		SentMailbox:   "Sent",
		DraftsMailbox: "Drafts",
		TrashMailbox:  "Trash",
		Signature:     "Regards,\n" + name,
	}
}

func testConfig(t *testing.T, accounts ...config.Account) *config.Config {
	t.Helper()
	dir := t.TempDir()
	return &config.Config{
		Theme:              "system",
		Watch:              false,
		BlockRemoteContent: true,
		LogLevel:           "error",
		ScriptDir:          dir + "/scripts",
		Accounts:           accounts,
		Dir:                dir,
	}
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// newTestController builds a controller over the fake server.
func newTestController(t *testing.T, cfg *config.Config, srv *fakeServer) (*Controller, *recEvents, *fakeConnector) {
	t.Helper()
	ev := newEvents()
	conn := &fakeConnector{server: srv}
	c := New(cfg, ev, discardLogger(), conn)
	t.Cleanup(c.Shutdown)
	return c, ev, conn
}

// startApp starts c and waits until account's folder list arrives.
func startApp(t *testing.T, c *Controller, ev *recEvents, account string) {
	t.Helper()
	if err := c.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	ev.wait(t, "folders for "+account, func() bool { return len(ev.foldersFor(account)) > 0 })
}
