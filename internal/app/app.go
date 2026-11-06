// Package app implements contract.Controller: the application layer that
// wires configuration, mail plumbing, scripting and the external editor
// together. It contains no GTK code, so the whole controller is testable
// headless.
//
// Threading: every Controller method returns promptly. Anything that touches
// the network runs on a goroutine owned by the controller (goRun) and reports
// exclusively through contract.Events, which may be called from any goroutine.
// All mutable state is guarded by Controller.mu.
//
// The two seams a fake implements in tests are Connector (opening
// connections, delivering mail) and MailClient (one IMAP session). The real
// implementation of Connector lives in connector.go.
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"btcx/internal/buildinfo"
	"btcx/internal/config"
	"btcx/internal/contract"
	"btcx/internal/model"
	"btcx/internal/script"
)

const (
	// inboxName is the mailbox the new-mail watcher observes.
	inboxName = "INBOX"

	// pageSize caps the message list: a folder is loaded as its first
	// pageSize envelopes by UID and then shown newest-first, so the list stays
	// bounded regardless of mailbox size.
	pageSize = 500

	// maxCachedMessages bounds the parsed-message cache (oldest evicted).
	maxCachedMessages = 100

	// initialBackoff and maxBackoff bound reconnect delays.
	initialBackoff = time.Second
	maxBackoff     = time.Minute

	// errorDedup suppresses repeated identical user-visible errors.
	errorDedup = 30 * time.Second

	// syncCallTimeout bounds the handful of Controller methods that must
	// return a value synchronously (ReplyTo, Forward, SaveAttachment).
	syncCallTimeout = 30 * time.Second
)

// MailClient is the IMAP seam the controller depends on. *imapx.Client
// satisfies it; tests inject a fake.
type MailClient interface {
	Mailboxes(ctx context.Context) ([]model.Mailbox, error)
	Envelopes(ctx context.Context, mailbox string, startUID uint32, count int) ([]model.Envelope, error)
	RawMessage(ctx context.Context, mailbox string, uid uint32) ([]byte, error)
	SetFlags(ctx context.Context, mailbox string, uid uint32, add, remove model.Flags) error
	Search(ctx context.Context, mailbox, text string) ([]uint32, error)
	Append(ctx context.Context, mailbox string, raw []byte, flags model.Flags) (model.Ref, error)
	Move(ctx context.Context, mailbox string, uid uint32, dest string) error
	Watch(ctx context.Context, mailbox string, onNew func(uids []uint32)) error
	Close() error
}

// Connector opens connections for an account and submits messages. The real
// implementation maps config onto imapx and smtpx.
type Connector interface {
	DialIMAP(ctx context.Context, account config.Account) (MailClient, error)
	Send(ctx context.Context, account config.Account, raw []byte, rcpts []model.Address) error
}

// accountState is the per-account runtime state. Every field except cfg is
// guarded by Controller.mu; dialMu serializes connection attempts.
type accountState struct {
	cfg config.Account

	dialMu sync.Mutex
	client MailClient

	folders  []model.Mailbox
	counts   map[string]folderCount
	folder   string
	rows     []contract.MessageRow
	watching bool
}

type folderCount struct {
	total  int
	unread int
}

// Controller implements contract.Controller.
type Controller struct {
	cfg    *config.Config
	events contract.Events
	log    *slog.Logger
	conn   Connector
	http   *http.Client

	ctx    context.Context
	cancel context.CancelFunc

	mu        sync.Mutex
	accounts  map[string]*accountState
	order     []string
	active    string
	scripts   *script.Engine
	messages  map[model.Ref]*model.Message
	msgOrder  []model.Ref
	nextEdit  contract.EditRequestID
	lastErr   string
	lastErrAt time.Time
	started   bool
	startErr  error
	closed    bool

	wg           sync.WaitGroup
	shutdownOnce sync.Once
}

var _ contract.Controller = (*Controller)(nil)

// New returns a controller for cfg. events must be non-nil and safe for
// concurrent use; connector nil selects the real IMAP/SMTP connector. A nil
// cfg is a startup wiring error and panics.
func New(cfg *config.Config, events contract.Events, logger *slog.Logger, connector Connector) *Controller {
	if cfg == nil {
		panic("app: nil config")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if connector == nil {
		connector = realConnector{log: logger}
	}
	ctx, cancel := context.WithCancel(context.Background())
	c := &Controller{
		cfg:      cfg,
		events:   events,
		log:      logger,
		conn:     connector,
		http:     newHTTPClient(),
		ctx:      ctx,
		cancel:   cancel,
		accounts: make(map[string]*accountState, len(cfg.Accounts)),
		messages: make(map[model.Ref]*model.Message),
	}
	for _, a := range cfg.Accounts {
		c.accounts[a.Name] = &accountState{cfg: a, counts: map[string]folderCount{}}
		c.order = append(c.order, a.Name)
	}
	if len(c.order) > 0 {
		c.active = c.order[0]
	}
	return c
}

// Start loads scripts, runs their on_startup hooks and connects the
// configured accounts in the background. Errors from individual accounts are
// reported through Events.OnError; the only value returned is a local setup
// failure (creating the script engine).
func (c *Controller) Start() error {
	c.mu.Lock()
	if c.started {
		err := c.startErr
		c.mu.Unlock()
		return err
	}
	c.started = true
	c.mu.Unlock()

	eng, err := script.New(c.cfg.ScriptDir, c.cfg.ScriptEnabled, buildinfo.Version(), c.log)
	if err != nil {
		err = fmt.Errorf("app: scripting: %w", err)
		c.mu.Lock()
		c.startErr = err
		c.mu.Unlock()
		c.fail(err)
		return err
	}
	c.mu.Lock()
	c.scripts = eng
	c.mu.Unlock()

	warnings, err := eng.Load()
	if err != nil {
		c.fail(fmt.Errorf("app: script load: %w", err))
	}
	c.reportWarnings("script load", warnings)
	c.reportWarnings("script startup", eng.OnStartup())

	c.goRun(c.connectAccounts)
	return nil
}

// Shutdown cancels all goroutines, waits for them and closes the remaining
// connections. It is idempotent.
func (c *Controller) Shutdown() {
	c.shutdownOnce.Do(func() {
		c.mu.Lock()
		c.closed = true
		c.cancel()
		c.mu.Unlock()

		c.wg.Wait()

		c.mu.Lock()
		clients := make([]MailClient, 0, len(c.accounts))
		for _, st := range c.accounts {
			if st.client != nil {
				clients = append(clients, st.client)
				st.client = nil
			}
		}
		c.mu.Unlock()
		for _, cl := range clients {
			if err := cl.Close(); err != nil {
				c.log.Debug("app: close connection", "err", err)
			}
		}
	})
}

// Accounts lists the configured accounts in config order.
func (c *Controller) Accounts() []contract.AccountInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]contract.AccountInfo, 0, len(c.order))
	for _, name := range c.order {
		out = append(out, contract.AccountInfo{Name: name, Email: c.accounts[name].cfg.Email})
	}
	return out
}

// ActiveAccount returns the currently selected account name.
func (c *Controller) ActiveAccount() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.active
}

// SelectAccount selects an account for the UI. Unknown names are ignored.
func (c *Controller) SelectAccount(name string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.accounts[name]; ok {
		c.active = name
	}
}

// connectAccounts connects every configured account in turn and starts its
// watcher.
func (c *Controller) connectAccounts(ctx context.Context) {
	for _, name := range c.accountNames() {
		if ctx.Err() != nil {
			return
		}
		st := c.state(name)
		if st == nil {
			continue
		}
		if err := c.syncFolders(ctx, st); err != nil {
			c.fail(fmt.Errorf("app: account %s: %w", name, err))
			c.goRun(func(ctx context.Context) { c.retryAccount(ctx, st) })
			continue
		}
		if c.cfg.Watch {
			c.startWatch(name)
		}
	}
}

// retryAccount retries a failed account connection with bounded backoff until
// it succeeds or the controller shuts down.
func (c *Controller) retryAccount(ctx context.Context, st *accountState) {
	backoff := initialBackoff
	for c.sleep(ctx, backoff) {
		if err := c.syncFolders(ctx, st); err != nil {
			c.fail(fmt.Errorf("app: account %s: %w", st.cfg.Name, err))
			backoff = min(backoff*2, maxBackoff)
			continue
		}
		if c.cfg.Watch {
			c.startWatch(st.cfg.Name)
		}
		return
	}
}

// state returns the runtime state of an account, or nil.
func (c *Controller) state(name string) *accountState {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.accounts[name]
}

// accountNames returns the account names in config order.
func (c *Controller) accountNames() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string(nil), c.order...)
}

// clientFor returns the account's connected client, dialing when needed.
func (c *Controller) clientFor(ctx context.Context, st *accountState) (MailClient, error) {
	st.dialMu.Lock()
	defer st.dialMu.Unlock()

	c.mu.Lock()
	cl := st.client
	c.mu.Unlock()
	if cl != nil {
		return cl, nil
	}

	cl, err := c.conn.DialIMAP(ctx, st.cfg)
	if err != nil {
		return nil, fmt.Errorf("connect %s: %w", st.cfg.Name, err)
	}
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		cl.Close()
		return nil, fmt.Errorf("connect %s: %w", st.cfg.Name, context.Canceled)
	}
	st.client = cl
	c.mu.Unlock()
	return cl, nil
}

// dropClient closes and forgets the account's client so the next command
// reconnects.
func (c *Controller) dropClient(st *accountState) {
	c.mu.Lock()
	cl := st.client
	st.client = nil
	c.mu.Unlock()
	if cl == nil {
		return
	}
	if err := cl.Close(); err != nil {
		c.log.Debug("app: close connection", "account", st.cfg.Name, "err", err)
	}
}

// engine returns the script engine, or nil before Start.
func (c *Controller) engine() *script.Engine {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.scripts
}

// goRun runs fn on a controller goroutine. Work requested after Shutdown is
// dropped, and Shutdown waits for everything accepted here.
func (c *Controller) goRun(fn func(context.Context)) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.wg.Add(1)
	ctx := c.ctx
	c.mu.Unlock()
	go func() {
		defer c.wg.Done()
		fn(ctx)
	}()
}

// fail reports err to the user, suppressing an identical repeat inside
// errorDedup so a dead server cannot flood the UI.
func (c *Controller) fail(err error) {
	if err == nil {
		return
	}
	msg := err.Error()
	c.mu.Lock()
	suppress := msg == c.lastErr && time.Since(c.lastErrAt) < errorDedup
	closed := c.closed
	c.lastErr, c.lastErrAt = msg, time.Now()
	c.mu.Unlock()
	c.log.Warn("app: " + msg)
	if closed || suppress {
		return
	}
	c.events.OnError(err)
}

// reportWarnings logs script warnings and surfaces them once as an error.
func (c *Controller) reportWarnings(what string, warnings []string) {
	if len(warnings) == 0 {
		return
	}
	for _, w := range warnings {
		c.log.Warn("app: " + what + ": " + w)
	}
	c.fail(fmt.Errorf("app: %s: %s", what, warnings[len(warnings)-1]))
}

// sleep waits d, returning false when the controller is shutting down.
func (c *Controller) sleep(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
