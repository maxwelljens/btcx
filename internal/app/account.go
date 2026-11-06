package app

import (
	"context"
	"errors"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
	"sync"
	"time"

	"btcx/internal/config"
	"btcx/internal/contract"
	"btcx/internal/imapx"
	"btcx/internal/smtpx"
)

// accountNameRE is the accepted shape of an account name. The name becomes the
// accounts/<name>.toml file stem, so it must stay a plain lowercase word.
var accountNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

// verifyTimeout bounds each VerifyAccount connection test.
const verifyTimeout = 15 * time.Second

// CreateAccount validates spec, writes it as accounts/<name>.toml and makes it
// usable without a restart: the account state is rebuilt from disk and, when
// the controller is not running yet, Start is called.
func (c *Controller) CreateAccount(spec contract.AccountSpec) (string, error) {
	a, err := accountFromSpec(spec)
	if err != nil {
		return "", fmt.Errorf("app: create account: %w", err)
	}
	path, err := config.WriteAccount(c.cfg.Dir, a)
	if err != nil {
		return "", fmt.Errorf("app: create account %s: %w", a.Name, err)
	}

	c.reloadMu.Lock()
	err = c.reloadConfig(true)
	c.reloadMu.Unlock()
	if err != nil {
		// The file is written; only activating it failed.
		return path, err
	}
	return path, nil
}

// VerifyAccount tests IMAP login and SMTP submission for spec in the background
// (nothing is sent). Both checks run even when one fails; the outcome arrives
// as one OnVerifyResult call. Passwords are resolved like the connector does
// and never logged.
func (c *Controller) VerifyAccount(spec contract.AccountSpec) {
	c.goRun(func(ctx context.Context) {
		a, err := accountFromSpec(spec)
		if err != nil {
			detail := contract.ServerCheck{Detail: err.Error()}
			c.events.OnVerifyResult(contract.VerifyResult{IMAP: detail, SMTP: detail})
			return
		}

		var (
			wg  sync.WaitGroup
			res contract.VerifyResult
		)
		wg.Add(2)
		go func() {
			defer wg.Done()
			password, err := a.IMAP.ResolvePassword(ctx, a.Name)
			if err != nil {
				res.IMAP = contract.ServerCheck{Detail: err.Error()}
				return
			}
			res.IMAP = c.verifyIMAP(ctx, a, password)
		}()
		go func() {
			defer wg.Done()
			var password string
			if a.SMTP.Username != "" {
				var err error
				if password, err = a.SMTP.ResolvePassword(ctx, a.Name); err != nil {
					res.SMTP = contract.ServerCheck{Detail: err.Error()}
					return
				}
			}
			res.SMTP = c.verifySMTP(ctx, a, password)
		}()
		wg.Wait()
		c.events.OnVerifyResult(res)
	})
}

// Reload re-reads the configuration directory and reconnects. In-flight work
// from the previous configuration is cancelled; the active account is kept when
// it still exists. Failures are reported through Events.OnError.
func (c *Controller) Reload() {
	c.goRun(func(context.Context) {
		c.reloadMu.Lock()
		defer c.reloadMu.Unlock()
		if err := c.reloadConfig(false); err != nil {
			c.fail(err)
		}
	})
}

// reloadConfig rebuilds the account state from disk and reconnects when the
// controller is running. start makes the first account write bring the
// controller up (CreateAccount). The caller holds reloadMu.
func (c *Controller) reloadConfig(start bool) error {
	if err := c.rebuildAccounts(); err != nil {
		return err
	}
	c.mu.Lock()
	started := c.started
	c.mu.Unlock()
	if !started {
		if start {
			return c.Start()
		}
		return nil
	}

	// Restart the scripts as Start does, so edited hook files take effect.
	if eng := c.engine(); eng != nil {
		warnings, err := eng.Load()
		if err != nil {
			c.fail(fmt.Errorf("app: reload: script load: %w", err))
		}
		c.reportWarnings("script load", warnings)
		c.reportWarnings("script startup", eng.OnStartup())
	}
	c.goRun(c.connectAccounts)
	return nil
}

// rebuildAccounts loads the configuration directory and replaces the account
// state with a fresh generation. The running generation is cancelled so its
// watchers, retries and commands stop; the new generation context is installed
// before returning, so the next goRun works on the new configuration. A
// configuration error leaves the running generation untouched.
func (c *Controller) rebuildAccounts() error {
	cfg, warnings, err := config.LoadDir(c.cfg.Dir)
	if err != nil {
		return fmt.Errorf("app: reload: %w", err)
	}
	for _, w := range warnings {
		c.log.Warn("app: config", "file", w.File, "key", w.Key, "message", w.Message)
	}

	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return errors.New("app: reload: controller is shut down")
	}
	var old []MailClient
	for _, st := range c.accounts {
		if st.client != nil {
			old = append(old, st.client)
		}
	}
	stop := c.runCancel
	c.accounts = make(map[string]*accountState, len(cfg.Accounts))
	c.order = make([]string, 0, len(cfg.Accounts))
	for _, a := range cfg.Accounts {
		c.accounts[a.Name] = newAccountState(a)
		c.order = append(c.order, a.Name)
	}
	if _, ok := c.accounts[c.active]; !ok {
		c.active = ""
		if len(c.order) > 0 {
			c.active = c.order[0]
		}
	}
	// The cache is keyed by ref, and the next generation may be a different
	// server that mints the same UIDVALIDITY: never serve its predecessor's
	// bodies.
	c.cache.Clear()
	c.runCtx, c.runCancel = context.WithCancel(c.ctx)
	c.mu.Unlock()

	stop()
	for _, cl := range old {
		if err := cl.Close(); err != nil {
			c.log.Debug("app: close connection", "err", err)
		}
	}
	return nil
}

// accountFromSpec validates and normalises a wizard spec into a configuration
// account. Nothing is written to disk.
func accountFromSpec(spec contract.AccountSpec) (config.Account, error) {
	email := strings.TrimSpace(spec.Email)
	if email == "" {
		return config.Account{}, errors.New("email is required")
	}
	addr, err := mail.ParseAddress(email)
	if err != nil || addr.Address != email {
		return config.Account{}, fmt.Errorf("email = %q: not a bare address (example: max@example.org)", email)
	}

	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = defaultAccountName(email)
	}
	if !accountNameRE.MatchString(name) {
		return config.Account{}, fmt.Errorf("name = %q: use lowercase letters, digits, '-' or '_'", name)
	}

	display := strings.TrimSpace(spec.DisplayName)
	if display == "" {
		display, _, _ = strings.Cut(email, "@")
	}

	password := spec.Password
	var command []string
	if spec.PasswordCommand != "" {
		if password != "" {
			return config.Account{}, errors.New("set either password or password_command, not both")
		}
		if command, err = config.SplitCommand(spec.PasswordCommand); err != nil {
			return config.Account{}, fmt.Errorf("password_command: %w", err)
		}
	}

	imap, err := serverFromSpec("imap", spec.IMAP, email, 993, 143)
	if err != nil {
		return config.Account{}, err
	}
	smtp, err := serverFromSpec("smtp", spec.SMTP, email, 465, 587)
	if err != nil {
		return config.Account{}, err
	}
	imap.Password, imap.PasswordCommand = password, command
	smtp.Password, smtp.PasswordCommand = password, command

	return config.Account{
		Name:        name,
		DisplayName: display,
		Email:       email,
		IMAP:        imap,
		SMTP:        smtp,
		Signature:   spec.Signature,
	}, nil
}

// serverFromSpec validates one wizard server and applies the port and username
// defaults: 0 selects the conventional port for the TLS mode, and an empty
// username falls back to the account address.
func serverFromSpec(which string, s contract.ServerSpec, email string, implicitPort, starttlsPort int) (config.Server, error) {
	host := strings.TrimSpace(s.Host)
	if host == "" {
		return config.Server{}, fmt.Errorf("%s.host is required", which)
	}
	mode := config.TLSMode(strings.ToLower(strings.TrimSpace(s.TLS)))
	switch mode {
	case config.TLSImplicit, config.TLSStartTLS:
	default:
		return config.Server{}, fmt.Errorf("%s.tls = %q: must be \"tls\" or \"starttls\"", which, s.TLS)
	}
	port := s.Port
	if port == 0 {
		port = implicitPort
		if mode == config.TLSStartTLS {
			port = starttlsPort
		}
	}
	if port < 1 || port > 65535 {
		return config.Server{}, fmt.Errorf("%s.port = %d: must be between 1 and 65535", which, s.Port)
	}
	username := s.Username
	if username == "" {
		username = email
	}
	return config.Server{Host: host, Port: port, Username: username, TLS: mode}, nil
}

// defaultAccountName derives an account name from the email domain, dropping
// every character outside [a-z0-9_-].
func defaultAccountName(email string) string {
	_, domain, ok := strings.Cut(email, "@")
	if !ok {
		return ""
	}
	var b strings.Builder
	for _, r := range strings.ToLower(domain) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return strings.TrimLeft(b.String(), "-_")
}

// checkIMAP is VerifyAccount's IMAP probe: dial and list the mailboxes.
func (c *Controller) checkIMAP(ctx context.Context, a config.Account, password string) contract.ServerCheck {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	if a.IMAP.InsecureSkipVerify {
		c.log.Warn("app: IMAP TLS certificate verification is DISABLED",
			"account", a.Name, "host", a.IMAP.Host)
	}
	cl, err := imapx.Dial(ctx, imapx.DialConfig{
		Host:               a.IMAP.Host,
		Port:               a.IMAP.Port,
		Username:           username(a.IMAP.Username, a.Email),
		Password:           password,
		TLS:                imapTLS(a.IMAP.TLS),
		InsecureSkipVerify: a.IMAP.InsecureSkipVerify,
		CAFile:             a.IMAP.CAFile,
		DialTimeout:        verifyTimeout,
	})
	if err != nil {
		return contract.ServerCheck{Detail: err.Error()}
	}
	defer func() { _ = cl.Close() }()

	boxes, err := cl.Mailboxes(ctx)
	if err != nil {
		return contract.ServerCheck{Detail: err.Error()}
	}
	return contract.ServerCheck{
		OK:     true,
		Detail: fmt.Sprintf("connected, TLS verified, logged in (%d folders)", len(boxes)),
	}
}

// checkSMTP is VerifyAccount's SMTP probe: connect and authenticate without
// sending a message.
func (c *Controller) checkSMTP(ctx context.Context, a config.Account, password string) contract.ServerCheck {
	ctx, cancel := context.WithTimeout(ctx, verifyTimeout)
	defer cancel()
	if a.SMTP.InsecureSkipVerify {
		c.log.Warn("app: SMTP TLS certificate verification is DISABLED",
			"account", a.Name, "host", a.SMTP.Host)
	}
	err := smtpx.Verify(ctx, smtpx.SendConfig{
		Host:               a.SMTP.Host,
		Port:               a.SMTP.Port,
		Username:           a.SMTP.Username,
		Password:           password,
		TLS:                smtpTLS(a.SMTP.TLS),
		InsecureSkipVerify: a.SMTP.InsecureSkipVerify,
		CAFile:             a.SMTP.CAFile,
		Timeout:            verifyTimeout,
	})
	if err != nil {
		return contract.ServerCheck{Detail: err.Error()}
	}
	return contract.ServerCheck{OK: true, Detail: "connected, TLS verified, authenticated"}
}
