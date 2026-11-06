// Package smtpx submits already-composed RFC 5322 messages to an SMTP
// submission server (RFC 6409). It encrypts the session before sending any
// credentials and refuses to authenticate over plaintext.
//
// The payload is transmitted verbatim: smtpx never parses or rewrites headers,
// so Bcc recipients live only in the envelope. The single normalisation is a
// trailing CRLF appended when the payload lacks one, because DATA ends at
// CRLF-dot-CRLF.
package smtpx

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"

	"btcx/internal/model"
)

// TLSMode selects how the submission connection is encrypted.
type TLSMode int

const (
	// TLSImplicit dials SMTPS (port 465): TLS from the first byte.
	TLSImplicit TLSMode = iota + 1
	// TLSStartTLS dials plaintext, requires the STARTTLS extension and
	// upgrades before AUTH (port 587).
	TLSStartTLS
)

// defaultTimeout bounds every dial, handshake and SMTP command when
// SendConfig.Timeout is zero.
const defaultTimeout = 30 * time.Second

// SendConfig describes one submission: where to send, how to authenticate,
// and the envelope sender.
type SendConfig struct {
	Host string
	Port int // 0 -> 465 for TLSImplicit, 587 for TLSStartTLS

	// Username and Password enable AUTH. An empty Username skips it.
	Username string
	Password string

	TLS TLSMode

	// InsecureSkipVerify disables certificate verification. DANGEROUS: it
	// makes the connection trivially interceptable. Never the default.
	InsecureSkipVerify bool
	// CAFile is an optional PEM bundle appended to the system roots.
	CAFile string

	From model.Address // envelope sender (MAIL FROM)
	// Timeout bounds the connect phase (dial, TLS or STARTTLS handshake,
	// greeting) and every SMTP command; 0 -> 30s.
	Timeout time.Duration
}

// ValidationError reports a request rejected locally, before any I/O.
type ValidationError struct{ Err error }

func (e *ValidationError) Error() string { return "smtpx: " + e.Err.Error() }
func (e *ValidationError) Unwrap() error { return e.Err }

// RecipientError reports the recipients the server refused. No message data
// is transmitted when any recipient is rejected, so a retry never duplicates
// mail.
type RecipientError struct {
	Failed []model.Address
	Err    error
}

func (e *RecipientError) Error() string {
	return fmt.Sprintf("smtpx: %d recipient(s) rejected: %v", len(e.Failed), e.Err)
}

func (e *RecipientError) Unwrap() error { return e.Err }

// Send submits raw RFC 5322 bytes to rcpts, the envelope recipients. Bcc
// addresses belong in rcpts only; raw is never modified to match them.
func Send(ctx context.Context, cfg SendConfig, raw []byte, rcpts []model.Address) error {
	if err := validate(cfg, raw, rcpts); err != nil {
		return &ValidationError{Err: err}
	}
	// DATA is terminated by CRLF-dot-CRLF, so an unterminated last line must
	// be closed first or the terminator would be swallowed into the body.
	if !bytes.HasSuffix(raw, []byte("\r\n")) {
		raw = append(raw[:len(raw):len(raw)], '\r', '\n')
	}

	c, closer, err := connect(ctx, cfg)
	if err != nil {
		return fmt.Errorf("smtpx: %w", err)
	}
	defer closer()

	if cfg.Username != "" {
		if err := authenticate(c, cfg); err != nil {
			return fmt.Errorf("smtpx: %w", err)
		}
	}
	return transact(ctx, c, cfg, raw, rcpts)
}

// connect dials the server, encrypts the session and reads the greeting,
// returning a client ready for AUTH. Cancelling ctx closes the socket for the
// rest of the session, so the caller must call close when done.
func connect(ctx context.Context, cfg SendConfig) (*smtp.Client, func(), error) {
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	tlsCfg, err := cfg.tlsConfig()
	if err != nil {
		return nil, nil, err
	}

	dialer := net.Dialer{Timeout: timeout}
	addr := net.JoinHostPort(cfg.Host, strconv.Itoa(cfg.port()))
	conn, err := dialer.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, nil, fmt.Errorf("dial %s: %w", addr, err)
	}
	// Closing the socket is what makes an in-flight command return as soon as
	// ctx is cancelled or its deadline expires.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	closer := func() {
		stop()
		_ = conn.Close()
	}

	// go-smtp reads the greeting and runs the STARTTLS handshake without a
	// context, so an expiry closes the socket; the deadline bounds any I/O in
	// between.
	connectCtx, cancelConnect := context.WithTimeout(ctx, timeout)
	_ = conn.SetDeadline(time.Now().Add(timeout))
	stopConnect := context.AfterFunc(connectCtx, func() { conn.Close() })
	c, err := newClient(connectCtx, conn, cfg, tlsCfg)
	stopConnect()
	cancelConnect()
	if err != nil {
		closer()
		return nil, nil, err
	}
	_ = conn.SetDeadline(time.Time{})
	c.CommandTimeout = timeout
	c.SubmissionTimeout = timeout
	return c, closer, nil
}

func (c SendConfig) port() int {
	if c.Port != 0 {
		return c.Port
	}
	if c.TLS == TLSImplicit {
		return 465
	}
	return 587
}

// tlsConfig builds the verification config. ServerName is always set so that
// STARTTLS cannot silently fall back to an empty (unverified) hostname.
func (c SendConfig) tlsConfig() (*tls.Config, error) {
	tc := &tls.Config{
		ServerName:         c.Host,
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: c.InsecureSkipVerify, // #nosec G402 - explicit, documented escape hatch
	}
	if c.CAFile != "" {
		pemData, err := os.ReadFile(c.CAFile)
		if err != nil {
			return nil, fmt.Errorf("read CA file: %w", err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pemData) {
			return nil, fmt.Errorf("no PEM certificates in %s", c.CAFile)
		}
		tc.RootCAs = pool
	}
	return tc, nil
}

func validate(cfg SendConfig, raw []byte, rcpts []model.Address) error {
	switch {
	case cfg.Host == "":
		return errors.New("empty host")
	case len(raw) == 0:
		return errors.New("empty message")
	case cfg.TLS != TLSImplicit && cfg.TLS != TLSStartTLS:
		return fmt.Errorf("invalid TLS mode %d", cfg.TLS)
	case !validAddr(cfg.From.Addr):
		return fmt.Errorf("invalid sender address %q", cfg.From.Addr)
	case len(rcpts) == 0:
		return errors.New("no recipients")
	}
	for _, r := range rcpts {
		if !validAddr(r.Addr) {
			return fmt.Errorf("invalid recipient address %q", r.Addr)
		}
	}
	return nil
}

// validAddr accepts only what can be safely placed in a MAIL FROM or RCPT TO
// argument: whitespace, NUL, CR, LF and address list punctuation are refused
// so a hostile address cannot smuggle extra SMTP commands.
func validAddr(a string) bool {
	if a == "" || strings.ContainsAny(a, "\r\n\x00 \t<>,") {
		return false
	}
	local, domain, ok := strings.Cut(a, "@")
	return ok && local != "" && domain != "" && !strings.Contains(domain, "@")
}

func newClient(ctx context.Context, conn net.Conn, cfg SendConfig, tlsCfg *tls.Config) (*smtp.Client, error) {
	if cfg.TLS == TLSImplicit {
		tc := tls.Client(conn, tlsCfg)
		if err := tc.HandshakeContext(ctx); err != nil {
			return nil, fmt.Errorf("tls handshake: %w", err)
		}
		return smtp.NewClient(tc), nil
	}

	// STARTTLS is mandatory: never fall back to a plaintext session.
	c, err := smtp.NewClientStartTLS(conn, tlsCfg)
	if err != nil {
		// shortcut: go-smtp exposes no sentinel for a missing STARTTLS
		// extension; upgrade when it does.
		if strings.Contains(err.Error(), "doesn't support STARTTLS") {
			return nil, errors.New("server does not support STARTTLS")
		}
		return nil, fmt.Errorf("starttls: %w", err)
	}
	return c, nil
}

func authenticate(c *smtp.Client, cfg SendConfig) error {
	if _, ok := c.TLSConnectionState(); !ok {
		return errors.New("refusing to authenticate over plaintext")
	}
	// Pick from the advertised mechanisms; "AUTH PLAIN LOGIN" is the common
	// shape. An unavailable AUTH extension leaves the choice at PLAIN and lets
	// Auth report the server's own answer.
	mechs := ""
	if ok, params := c.Extension("AUTH"); ok {
		mechs = strings.ToUpper(params)
	}
	var a sasl.Client = sasl.NewPlainClient("", cfg.Username, cfg.Password)
	if !strings.Contains(mechs, sasl.Plain) && strings.Contains(mechs, sasl.Login) {
		a = sasl.NewLoginClient(cfg.Username, cfg.Password)
	}
	if err := c.Auth(a); err != nil {
		// Only the server's reply is folded in; never the password.
		return fmt.Errorf("authentication failed: %w", err)
	}
	return nil
}

func transact(ctx context.Context, c *smtp.Client, cfg SendConfig, raw []byte, rcpts []model.Address) error {
	if err := c.Mail(cfg.From.Addr, nil); err != nil {
		return fmt.Errorf("smtpx: mail from %s: %w", cfg.From.Addr, ctxErr(ctx, err))
	}

	var failed []model.Address
	var errs []error
	for _, r := range rcpts {
		if err := c.Rcpt(r.Addr, nil); err != nil {
			failed = append(failed, r)
			errs = append(errs, fmt.Errorf("%s: %w", r.Addr, err))
		}
	}
	if len(failed) > 0 {
		return &RecipientError{Failed: failed, Err: ctxErr(ctx, errors.Join(errs...))}
	}

	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtpx: data: %w", ctxErr(ctx, err))
	}
	if _, err := w.Write(raw); err != nil {
		w.Close()
		return fmt.Errorf("smtpx: write message: %w", ctxErr(ctx, err))
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("smtpx: deliver message: %w", ctxErr(ctx, err))
	}
	if err := c.Quit(); err != nil {
		return fmt.Errorf("smtpx: quit: %w", err)
	}
	return nil
}

// ctxErr reports the context's own error in preference to the socket error
// that cancellation caused, so callers can test with errors.Is.
func ctxErr(ctx context.Context, err error) error {
	if e := ctx.Err(); e != nil {
		return e
	}
	return err
}
