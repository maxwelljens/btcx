package ui

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"

	"btcx/internal/contract"
	"btcx/internal/model"
)

// stubController records calls and does nothing else.
type stubController struct {
	calls []string
}

func (s *stubController) record(name string) { s.calls = append(s.calls, name) }

func (s *stubController) Start() error { s.record("Start"); return nil }
func (s *stubController) Shutdown()    { s.record("Shutdown") }
func (s *stubController) Accounts() []contract.AccountInfo {
	return []contract.AccountInfo{{Name: "work", Email: "me@example.org"}}
}
func (s *stubController) ActiveAccount() string { return "work" }
func (s *stubController) SelectAccount(name string) {
	s.record("SelectAccount")
}
func (s *stubController) SelectFolder(account, folder string) { s.record("SelectFolder") }
func (s *stubController) OpenMessage(account string, ref model.Ref) {
	s.record("OpenMessage")
}
func (s *stubController) Refresh(account string)               { s.record("Refresh") }
func (s *stubController) Search(account, folder, query string) { s.record("Search") }
func (s *stubController) MarkSeen(account string, ref model.Ref, seen bool) {
	s.record("MarkSeen")
}
func (s *stubController) SetFlagged(account string, ref model.Ref, flagged bool) {
	s.record("SetFlagged")
}
func (s *stubController) DeleteMessage(account string, ref model.Ref) { s.record("DeleteMessage") }
func (s *stubController) LoadRemoteContent(account string, ref model.Ref) {
	s.record("LoadRemoteContent")
}
func (s *stubController) SaveAttachment(account string, ref model.Ref, index int, destPath string) error {
	s.record("SaveAttachment")
	return nil
}
func (s *stubController) NewCompose(account string) contract.ComposeDraft {
	s.record("NewCompose")
	return contract.ComposeDraft{Account: account}
}
func (s *stubController) ReplyTo(account string, ref model.Ref, replyAll bool) contract.ComposeDraft {
	s.record("ReplyTo")
	return contract.ComposeDraft{Account: account}
}
func (s *stubController) Forward(account string, ref model.Ref) contract.ComposeDraft {
	s.record("Forward")
	return contract.ComposeDraft{Account: account}
}
func (s *stubController) Send(draft contract.ComposeDraft) { s.record("Send") }
func (s *stubController) SaveDraft(draft contract.ComposeDraft) {
	s.record("SaveDraft")
}
func (s *stubController) StartExternalEdit(draft contract.ComposeDraft) contract.EditRequestID {
	s.record("StartExternalEdit")
	return 1
}
func (s *stubController) CreateAccount(spec contract.AccountSpec) (string, error) {
	s.record("CreateAccount")
	return "accounts/" + spec.Name + ".toml", nil
}
func (s *stubController) VerifyAccount(spec contract.AccountSpec) { s.record("VerifyAccount") }
func (s *stubController) Reload()                                 { s.record("Reload") }

// requireGTK skips the test when no display is available, and initialises GTK
// pinned to the test goroutine otherwise.
func requireGTK(t *testing.T) {
	t.Helper()
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no display available")
	}
	runtime.LockOSThread()
	if ok := gtk.InitCheck(); !ok {
		t.Skip("GTK cannot initialise")
	}
}

// twoAccountController is a stubController with a second account, so account
// switching can be exercised.
type twoAccountController struct{ stubController }

func (c *twoAccountController) Accounts() []contract.AccountInfo {
	return []contract.AccountInfo{{Name: "work"}, {Name: "home"}}
}

func newTestWindow(t *testing.T, ctrl contract.Controller) *MainWindow {
	t.Helper()
	requireGTK(t)
	a := &app{
		opts:       Options{Title: "btcx", Version: "test"},
		controller: ctrl,
		gtkApp:     gtk.NewApplication("org.btcx.Test", gio.ApplicationDefaultFlags),
	}
	return newMainWindow(a)
}

func TestMainWindowWidgets(t *testing.T) {
	ctrl := &stubController{}
	w := newTestWindow(t, ctrl)

	if got := w.Title(); got != "btcx" {
		t.Errorf("title = %q, want btcx", got)
	}
	if w.search == nil || w.folders == nil || w.msgs == nil || w.reader == nil {
		t.Fatal("window parts are missing")
	}

	// The metadata header must not collapse next to the web view: natural
	// height propagation keeps subject/from/to and the remote-content bar
	// visible without scrolling, while the clamp bounds huge headers so the
	// message body keeps its room. (Regression: only the subject was
	// visible and everything else needed scrolling.)
	if !w.reader.scroll.PropagateNaturalHeight() {
		t.Error("reader header would collapse: PropagateNaturalHeight is false")
	}
	if h := w.reader.scroll.MaxContentHeight(); h <= 0 {
		t.Error("reader header has no height clamp; a huge header could squeeze out the body")
	}

	w.setFolders("work", []contract.FolderInfo{
		{Name: "INBOX", Unread: 2},
		{Name: "Sent", Role: "sent"},
	})
	if len(w.folderNames) != 2 {
		t.Fatalf("folder rows = %d, want 2", len(w.folderNames))
	}
	if w.folders.RowAtIndex(0) == nil {
		t.Fatal("first folder row missing")
	}
	// The row text is built by folderLabel, covered by the pure tests above;
	// reading it back here would go through gotk4's GValue walk, which trips
	// the race detector's checkptr mode.
	if got, want := w.folderNames[0], "INBOX"; got != want {
		t.Errorf("folder name = %q, want %q", got, want)
	}

	w.setMessages("work", "INBOX", []contract.MessageRow{{
		Ref:     model.Ref{Mailbox: "INBOX", UID: 7},
		Subject: "Hello",
		From:    model.Address{Addr: "alice@example.org"},
		Date:    time.Now(),
		Unread:  true,
		Flagged: true,
	}})
	if len(w.msgs.rows) != 1 {
		t.Fatalf("message rows = %d, want 1", len(w.msgs.rows))
	}
	if got := w.msgs.model.NItems(); got != 1 {
		t.Errorf("model items = %d, want 1", got)
	}
	w.msgs.view.GrabFocus()
	w.msgs.sel.SetSelected(0)
	if got, ok := w.msgs.selected(); !ok || got.Ref.UID != 7 {
		t.Errorf("selected = %+v, %v", got, ok)
	}
	if len(ctrl.calls) == 0 || ctrl.calls[len(ctrl.calls)-1] != "OpenMessage" {
		t.Errorf("calls = %v, want OpenMessage", ctrl.calls)
	}
}

// emptyController reports no configured accounts, the first-run state.
type emptyController struct{ stubController }

func (c *emptyController) Accounts() []contract.AccountInfo { return nil }

func TestEmptyStateWithoutAccounts(t *testing.T) {
	w := newTestWindow(t, &emptyController{})
	if got := w.stack.VisibleChildName(); got != "welcome" {
		t.Errorf("empty account list shows %q, want welcome", got)
	}
	configured := newTestWindow(t, &stubController{})
	if got := configured.stack.VisibleChildName(); got != "mail" {
		t.Errorf("configured account shows %q, want mail", got)
	}
}

func TestSetupWindowCollect(t *testing.T) {
	w := newTestWindow(t, &stubController{})
	s := newSetupWindow(w.app, w)
	defer s.Close()

	s.email.SetText("jane@example.org")
	if got := s.name.Text(); got != "exampleorg" {
		t.Errorf("account name = %q, want exampleorg", got)
	}
	if got := s.imapUser.Text(); got != "jane@example.org" {
		t.Errorf("IMAP user = %q, want the address", got)
	}

	if _, err := s.collect(); err == nil {
		t.Error("collect accepted a form without credentials or servers")
	}

	s.imapHost.SetText("imap.example.org")
	s.smtpHost.SetText("smtp.example.org")
	s.password.SetText("secret")
	spec, err := s.collect()
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if spec.IMAP.Port != 993 || spec.SMTP.Port != 587 {
		t.Errorf("ports = %d/%d, want 993/587", spec.IMAP.Port, spec.SMTP.Port)
	}
	if spec.SMTP.TLS != "starttls" || spec.IMAP.TLS != "tls" {
		t.Errorf("TLS = %q/%q", spec.IMAP.TLS, spec.SMTP.TLS)
	}

	s.passStack.SetVisibleChildName("command")
	s.password.SetText("")
	s.passCommand.SetText("pass show mail/example.org")
	spec, err = s.collect()
	if err != nil {
		t.Fatalf("collect with password command: %v", err)
	}
	if spec.Password != "" || spec.PasswordCommand == "" {
		t.Errorf("password = %q, command = %q", spec.Password, spec.PasswordCommand)
	}
}

func TestReaderShowsMessage(t *testing.T) {
	w := newTestWindow(t, &stubController{})
	w.reader.show(contract.MessageView{
		Account:              "work",
		Ref:                  model.Ref{Mailbox: "INBOX", UID: 1},
		Subject:              "Hi",
		From:                 []model.Address{{Addr: "alice@example.org"}},
		Date:                 time.Now(),
		PangoBody:            "body <b>text</b>",
		ShowRemoteContentBar: true,
		Attachments:          []contract.AttachmentInfo{{Filename: "a.txt", Size: 10}, {Filename: "b.png", Inline: true}},
		RemoteImages:         []contract.InlineImage{{URL: "https://example.org/x.png", Failed: true}},
	})
	if got := w.reader.view.Subject; got != "Hi" {
		t.Errorf("reader subject = %q, want Hi", got)
	}
}

// TestStaleResult pins the F7 guard: async results outlive the selection that
// asked for them, so only a delivery for the current account and folder is
// allowed to replace the message list.
func TestStaleResult(t *testing.T) {
	cases := []struct {
		name                  string
		account, folder       string
		curAccount, curFolder string
		stale                 bool
	}{
		{"same selection", "work", "INBOX", "work", "INBOX", false},
		{"folder not known yet adopts", "work", "INBOX", "work", "", false},
		{"account not known yet", "work", "INBOX", "", "", false},
		{"previous account", "home", "INBOX", "work", "INBOX", true},
		{"previous folder", "work", "Sent", "work", "INBOX", true},
		{"empty delivery", "", "", "work", "INBOX", false},
	}
	for _, c := range cases {
		if got := staleResult(c.account, c.folder, c.curAccount, c.curFolder); got != c.stale {
			t.Errorf("%s: staleResult = %v, want %v", c.name, got, c.stale)
		}
	}
}

// TestAccountSwitchClearsView covers the rest of F7: leaving an account must
// clear its mail and reader, and late results from it must not repaint either.
func TestAccountSwitchClearsView(t *testing.T) {
	w := newTestWindow(t, &twoAccountController{})
	w.setMessages("work", "INBOX", []contract.MessageRow{{
		Ref:     model.Ref{Mailbox: "INBOX", UID: 1},
		Subject: "Hello",
	}})
	w.showMessage(contract.MessageView{Account: "work", Subject: "Hello", Date: time.Now()})
	if len(w.msgs.rows) != 1 || w.reader.view.Subject != "Hello" {
		t.Fatalf("setup: rows = %d, reader = %q", len(w.msgs.rows), w.reader.view.Subject)
	}

	w.selectAccount("home")
	if len(w.msgs.rows) != 0 || w.msgs.model.NItems() != 0 {
		t.Errorf("account switch kept %d rows", len(w.msgs.rows))
	}
	if w.reader.view.Subject != "" {
		t.Errorf("account switch kept the reader on %q", w.reader.view.Subject)
	}
	if got := w.status.Text(); got != "Loading home…" {
		t.Errorf("status = %q, want loading", got)
	}

	// Results for the account we left must be dropped.
	w.setMessages("work", "INBOX", []contract.MessageRow{{
		Ref:     model.Ref{Mailbox: "INBOX", UID: 1},
		Subject: "Hello",
	}})
	w.showMessage(contract.MessageView{Account: "work", Subject: "Hello"})
	if len(w.msgs.rows) != 0 || w.reader.view.Subject != "" {
		t.Error("late results from the previous account were shown")
	}
}

func TestComposeWindowValidation(t *testing.T) {
	w := newTestWindow(t, &stubController{})
	c := newComposeWindow(w, contract.ComposeDraft{
		Account: "work",
		To:      []model.Address{{Addr: "alice@example.org"}},
		Subject: "Re: Hi",
		Plain:   "body",
	})
	if got := c.to.Text(); got != "alice@example.org" {
		t.Errorf("To = %q", got)
	}
	if got := c.subject.Text(); got != "Re: Hi" {
		t.Errorf("Subject = %q", got)
	}
	if c.bodyText() != "body" {
		t.Errorf("body = %q", c.bodyText())
	}

	c.to.SetText("not an address @@")
	if _, ok := c.collect(); ok {
		t.Error("collect() accepted an invalid address")
	}

	c.to.SetText("alice@example.org, bob@example.org")
	draft, ok := c.collect()
	if !ok {
		t.Fatal("collect() rejected valid addresses")
	}
	if len(draft.To) != 2 {
		t.Errorf("parsed %d addresses, want 2", len(draft.To))
	}
	if len(c.draft.Attachments) != 0 {
		t.Errorf("attachments = %d, want 0", len(c.draft.Attachments))
	}
}

func TestExternalEditReplacesDraft(t *testing.T) {
	w := newTestWindow(t, &stubController{})
	c := newComposeWindow(w, contract.ComposeDraft{Account: "work", Subject: "old"})
	c.applyDraft(contract.ComposeDraft{
		Subject: "new",
		To:      []model.Address{{Addr: "bob@example.org"}},
		Plain:   "edited",
	})
	if got := c.subject.Text(); got != "new" {
		t.Errorf("subject = %q, want new", got)
	}
	if got := c.bodyText(); got != "edited" {
		t.Errorf("body = %q, want edited", got)
	}
	if c.dirty() {
		t.Error("window is dirty right after applying an external edit")
	}
}

// TestConfirmDiscardWaitsForTheUser guards the close gate: opening the
// confirmation, and dismissing it, must never discard the draft by itself.
func TestConfirmDiscardWaitsForTheUser(t *testing.T) {
	requireGTK(t)
	accepted := false
	win := alertWindow(nil, "Discard message?", "Its content will be lost.",
		"Keep editing", "Discard", func() { accepted = true })
	ctx := glib.MainContextDefault()
	for i := 0; i < 50; i++ {
		ctx.Iteration(false)
	}
	win.Close()
	for i := 0; i < 50; i++ {
		ctx.Iteration(false)
	}
	if accepted {
		t.Fatal("discard ran without the user accepting")
	}
}

// TestEventsMarshalOntoMainLoop checks that Events calls from another goroutine
// are queued on the GLib main loop instead of touching widgets directly. No
// window exists, so the handlers are required to do nothing but be dispatched.
func TestEventsMarshalOntoMainLoop(t *testing.T) {
	a := &app{}
	done := make(chan struct{})
	go func() {
		a.OnStatus("busy")
		a.OnError(errors.New("boom"))
		a.OnSendResult(true, "fine")
		a.OnVerifyResult(contract.VerifyResult{})
		a.OnExternalEditDone(42, contract.ComposeDraft{}, errors.New("gone"))
		close(done)
	}()
	<-done

	ran := make(chan struct{})
	a.idle(func() { close(ran) })

	ctx := glib.MainContextDefault()
	for i := 0; i < 1000; i++ {
		ctx.Iteration(false)
		select {
		case <-ran:
			return
		default:
		}
	}
	t.Fatal("idle callbacks were not dispatched")
}

// autoQuitController pushes a full view-model through Events and quits the
// application shortly after startup, so Run returns and the whole wiring
// (activate -> Start -> Shutdown) is exercised.
type autoQuitController struct {
	stubController
	events    contract.Events
	quitAfter uint
}

func (c *autoQuitController) SetEvents(events contract.Events) { c.events = events }

func (c *autoQuitController) pushEvents() {
	if c.events == nil {
		return
	}
	go func() {
		// Delivered from another goroutine on purpose: the UI must marshal.
		c.events.OnStatus("loading")
		c.events.OnFolders("work", []contract.FolderInfo{
			{Name: "INBOX", Unread: 2},
			{Name: "Sent", Role: "sent"},
		})
		c.events.OnMessages("work", "INBOX", []contract.MessageRow{
			{Ref: model.Ref{Mailbox: "INBOX", UID: 1}, Subject: "Hello", From: model.Address{Name: "Alice", Addr: "alice@example.org"}, Date: time.Now(), Unread: true},
			{Ref: model.Ref{Mailbox: "INBOX", UID: 2}, Subject: "Report", Date: time.Now(), Flagged: true, HasAttachments: true},
		})
		c.events.OnMessageOpened(contract.MessageView{
			Account:              "work",
			Ref:                  model.Ref{Mailbox: "INBOX", UID: 1},
			Subject:              "Hello",
			From:                 []model.Address{{Name: "Alice", Addr: "alice@example.org"}},
			Date:                 time.Now(),
			PangoBody:            "Hi there <b>bold</b>",
			ShowRemoteContentBar: true,
			RemoteImages:         []contract.InlineImage{{URL: "https://example.org/a.png", Failed: true}},
			Attachments:          []contract.AttachmentInfo{{Index: 0, Filename: "report.pdf", Size: 2048}},
		})
	}()
}

func (c *autoQuitController) Start() error {
	c.pushEvents()
	if app := gio.ApplicationGetDefault(); app != nil {
		glib.TimeoutAdd(c.quitAfter, func() bool {
			app.Quit()
			return false
		})
	}
	return c.stubController.Start()
}

const runChildEnv = "BTCX_UI_RUN_CHILD"

// TestRun runs the real main loop in a child process: GTK keeps global state
// that the widget tests above disturb, so the loop needs a clean process.
func TestRun(t *testing.T) {
	if os.Getenv("DISPLAY") == "" && os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("no display available")
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunChild$", "-test.timeout=30s")
	cmd.Env = append(os.Environ(), runChildEnv+"=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("child process failed: %v\n%s", err, out)
	}
}

// TestRunChild builds the window with a stub controller, quits after a moment
// and checks that shutdown reached the controller.
func TestRunChild(t *testing.T) {
	if os.Getenv(runChildEnv) == "" {
		t.Skip("only runs as a subprocess of TestRun")
	}
	requireGTK(t)
	ctrl := &autoQuitController{quitAfter: 250}
	if code := Run(Options{Title: "btcx", Version: "test", Theme: "dark"}, ctrl, []string{"btcx"}); code != 0 {
		t.Fatalf("Run() = %d, want 0", code)
	}
	for _, call := range ctrl.calls {
		if call == "Shutdown" {
			return
		}
	}
	t.Fatalf("controller not shut down, calls = %v", ctrl.calls)
}
