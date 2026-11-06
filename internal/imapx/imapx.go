// Package imapx is the IMAP transport for btcx.
//
// It deals exclusively in raw RFC 5322 message bytes and IMAP metadata: it
// never parses message bodies and depends on no other btcx package besides
// model. Parsing belongs to mailparse; this package only moves messages,
// folders and flags around.
//
// Connections are TLS-only, either implicit TLS (IMAPS) or STARTTLS. Server
// certificates are verified against the system roots plus an optional CA file.
// InsecureSkipVerify exists for self-hosted servers with private CAs and is
// dangerous: it disables the only authentication the transport has.
package imapx

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"btcx/internal/model"
)

const (
	// DefaultMaxMessageBytes is the size limit applied by RawMessage.
	DefaultMaxMessageBytes = 64 << 20

	defaultImplicitPort = 993
	defaultStartTLSPort = 143
	defaultDialTimeout  = 30 * time.Second
)

// ErrBusy is returned by a command issued while Watch owns the connection.
var ErrBusy = errors.New("imapx: client busy watching")

// ErrMessageTooLarge is returned by RawMessage when the server announces or
// delivers a message above the configured limit.
var ErrMessageTooLarge = errors.New("imapx: message too large")

// TLSMode selects how the connection is protected.
type TLSMode int

const (
	// TLSImplicit connects over TLS from the first byte (IMAPS, port 993).
	TLSImplicit TLSMode = iota + 1
	// TLSStartTLS connects in cleartext and upgrades with STARTTLS (port 143).
	TLSStartTLS
)

// DialConfig describes how to reach an IMAP server.
type DialConfig struct {
	Host     string
	Port     int // 0 selects the protocol default (993 or 143)
	Username string
	Password string
	TLS      TLSMode

	// InsecureSkipVerify disables certificate and hostname verification.
	// DANGEROUS: never the default, and a loud warning is logged when set.
	InsecureSkipVerify bool
	// CAFile is an optional PEM bundle appended to the system roots.
	CAFile string
	// DialTimeout bounds TCP connect and TLS handshake; 0 means 30s.
	DialTimeout time.Duration
}

// Client is an IMAP connection.
//
// It is safe for concurrent use: commands are serialized internally. Watch
// takes exclusive ownership of the connection for its whole duration; every
// other command then fails with ErrBusy.
type Client struct {
	c      *imapclient.Client
	notify chan struct{} // EXISTS notifications, coalesced

	// maxMessageBytes is the RawMessage cap; fixed after Dial.
	maxMessageBytes int64

	mu       sync.Mutex // serializes commands
	watchMu  sync.Mutex // guards watching
	watching bool
}

// Dial connects, upgrades the transport and authenticates.
//
// Certificates are verified unless cfg.InsecureSkipVerify is set. Cancelling
// ctx aborts the connection attempt; cancelling the context of a later
// command closes the connection, because an interrupted IMAP dialogue cannot
// be resumed safely.
func Dial(ctx context.Context, cfg DialConfig) (*Client, error) {
	if cfg.Host == "" {
		return nil, errors.New("imapx: dial: empty host")
	}
	switch cfg.TLS {
	case TLSImplicit, TLSStartTLS:
	default:
		return nil, fmt.Errorf("imapx: dial: unsupported TLS mode %d", cfg.TLS)
	}
	port := cfg.Port
	if port == 0 {
		if cfg.TLS == TLSImplicit {
			port = defaultImplicitPort
		} else {
			port = defaultStartTLSPort
		}
	}
	if port < 0 || port > 65535 {
		return nil, fmt.Errorf("imapx: dial: invalid port %d", port)
	}
	timeout := cfg.DialTimeout
	if timeout <= 0 {
		timeout = defaultDialTimeout
	}
	tlsConf, err := cfg.tlsConfig()
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(cfg.Host, fmt.Sprint(port))
	dialer := &net.Dialer{Timeout: timeout}

	cl := &Client{
		notify:          make(chan struct{}, 1),
		maxMessageBytes: DefaultMaxMessageBytes,
	}
	opts := &imapclient.Options{
		TLSConfig:   tlsConf,
		Dialer:      dialer,
		WordDecoder: wordDecoder,
		UnilateralDataHandler: &imapclient.UnilateralDataHandler{
			Mailbox: func(data *imapclient.UnilateralDataMailbox) {
				// Runs on the decoder goroutine: must never block.
				if data.NumMessages != nil {
					select {
					case cl.notify <- struct{}{}:
					default:
					}
				}
			},
		},
	}

	var conn net.Conn
	switch cfg.TLS {
	case TLSImplicit:
		raw, err := dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("imapx: dial %s: %w", addr, err)
		}
		tlsConn := tls.Client(raw, tlsConf)
		if err := tlsConn.HandshakeContext(ctx); err != nil {
			raw.Close()
			return nil, fmt.Errorf("imapx: tls handshake %s: %w", addr, err)
		}
		cl.c = imapclient.New(tlsConn, opts)
	case TLSStartTLS:
		conn, err = dialer.DialContext(ctx, "tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("imapx: dial %s: %w", addr, err)
		}
		var ic *imapclient.Client
		if err := waitCtx(ctx, func() { conn.Close() }, func() error {
			var err error
			ic, err = imapclient.NewStartTLS(conn, opts)
			return err
		}); err != nil {
			conn.Close()
			return nil, fmt.Errorf("imapx: starttls %s: %w", addr, err)
		}
		cl.c = ic
	}

	if err := waitCtx(ctx, func() { cl.c.Close() }, func() error {
		return cl.c.Login(cfg.Username, cfg.Password).Wait()
	}); err != nil {
		cl.c.Close()
		return nil, fmt.Errorf("imapx: login %s as %q: %w", addr, cfg.Username, err)
	}
	return cl, nil
}

// Close drops the connection immediately.
//
// A graceful LOGOUT is not attempted: it blocks while IDLE is running, and no
// uncommitted state is lost by disconnecting (STORE and APPEND are committed by
// the server before their tagged response).
func (c *Client) Close() error {
	if err := c.c.Close(); err != nil && !errors.Is(err, net.ErrClosed) {
		return fmt.Errorf("imapx: close: %w", err)
	}
	return nil
}

// tlsConfig builds the verified TLS configuration for cfg.
func (cfg DialConfig) tlsConfig() (*tls.Config, error) {
	conf := &tls.Config{
		MinVersion: tls.VersionTLS12,
		ServerName: cfg.Host,
	}
	if cfg.InsecureSkipVerify {
		slog.Warn("imapx: TLS certificate verification is DISABLED",
			"host", cfg.Host, "reason", "insecure_skip_verify")
		conf.InsecureSkipVerify = true
	}
	if cfg.CAFile != "" {
		pem, err := os.ReadFile(cfg.CAFile)
		if err != nil {
			return nil, fmt.Errorf("imapx: read CA file %s: %w", cfg.CAFile, err)
		}
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		if !pool.AppendCertsFromPEM(pem) {
			return nil, fmt.Errorf("imapx: CA file %s: no certificates found", cfg.CAFile)
		}
		conf.RootCAs = pool
	}
	return conf, nil
}

// wordDecoder decodes RFC 2047 header words, including the ISO-8859 and
// Windows-1252 subjects older mailers still send.
var wordDecoder = &mime.WordDecoder{CharsetReader: charsetReader}

func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	switch strings.ToLower(charset) {
	case "", "utf-8", "us-ascii", "ascii":
		return input, nil
	case "iso-8859-1", "iso-8859-15", "windows-1252":
		b, err := io.ReadAll(io.LimitReader(input, 1<<20))
		if err != nil {
			return nil, err
		}
		runes := make([]rune, len(b))
		for i, ch := range b {
			runes[i] = rune(ch)
		}
		return strings.NewReader(string(runes)), nil
	default:
		return nil, fmt.Errorf("imapx: unsupported header charset %q", charset)
	}
}

// do runs fn with exclusive use of the connection (unless Watch owns it) and
// honours ctx by closing the connection: a half-executed IMAP command leaves
// the stream out of sync, so the connection cannot be reused.
func (c *Client) do(ctx context.Context, fn func() error) error {
	c.watchMu.Lock()
	busy := c.watching
	if !busy {
		c.mu.Lock()
	}
	c.watchMu.Unlock()
	if busy {
		return ErrBusy
	}
	defer c.mu.Unlock()
	return waitCtx(ctx, func() { c.c.Close() }, fn)
}

// waitCtx runs fn in a goroutine and waits for it, aborting via abort when ctx
// is cancelled first.
func waitCtx(ctx context.Context, abort func(), fn func() error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		abort()
		<-done
		return ctx.Err()
	}
}

// selectMailbox selects mailbox for the following command.
func (c *Client) selectMailbox(mailbox string) (*imap.SelectData, error) {
	if mailbox == "" {
		return nil, errors.New("imapx: empty mailbox name")
	}
	sel, err := c.c.Select(mailbox, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("select %q: %w", mailbox, err)
	}
	return sel, nil
}

// envelopeFrom converts a FETCH result into a model envelope.
func envelopeFrom(mailbox string, uidValidity uint32, buf *imapclient.FetchMessageBuffer) model.Envelope {
	env := model.Envelope{
		Ref:   model.Ref{Mailbox: mailbox, UIDValidity: uidValidity, UID: uint32(buf.UID)},
		Size:  buf.RFC822Size,
		Flags: flagsFromIMAP(buf.Flags),
	}
	if e := buf.Envelope; e != nil {
		env.MessageID = e.MessageID // already bare: go-imap strips <>
		env.Subject = e.Subject
		env.From = addressesFromIMAP(e.From)
		env.To = addressesFromIMAP(e.To)
		env.Cc = addressesFromIMAP(e.Cc)
		env.Date = e.Date
	}
	env.HasAttachments = hasAttachments(buf.BodyStructure)
	return env
}

func addressesFromIMAP(list []imap.Address) []model.Address {
	out := make([]model.Address, 0, len(list))
	for _, a := range list {
		addr := a.Addr()
		if addr == "" {
			continue // group start/end markers carry no address
		}
		out = append(out, model.Address{Name: a.Name, Addr: addr})
	}
	return out
}

// hasAttachments reports whether the body structure holds anything beyond
// plain text: a non-text part, or any part marked as an attachment.
func hasAttachments(bs imap.BodyStructure) bool {
	if bs == nil {
		return false
	}
	found := false
	bs.Walk(func(_ []int, part imap.BodyStructure) bool {
		if _, ok := part.(*imap.BodyStructureMultiPart); ok {
			return true
		}
		if d := part.Disposition(); d != nil && strings.EqualFold(d.Value, "attachment") {
			found = true
			return true
		}
		if !strings.HasPrefix(strings.ToLower(part.MediaType()), "text/") {
			found = true
		}
		return true
	})
	return found
}

func flagsFromIMAP(flags []imap.Flag) model.Flags {
	var f model.Flags
	for _, flag := range flags {
		switch flag {
		case imap.FlagSeen:
			f.Seen = true
		case imap.FlagAnswered:
			f.Answered = true
		case imap.FlagFlagged:
			f.Flagged = true
		case imap.FlagDeleted:
			f.Deleted = true
		case imap.FlagDraft:
			f.Draft = true
		case imap.Flag("\\Recent"):
			f.Recent = true
		}
	}
	return f
}

// flagsToIMAP converts writable flags. \Recent is server-assigned and must
// never be sent by a client, so model.Flags.Recent is ignored.
func flagsToIMAP(f model.Flags) []imap.Flag {
	var out []imap.Flag
	if f.Seen {
		out = append(out, imap.FlagSeen)
	}
	if f.Answered {
		out = append(out, imap.FlagAnswered)
	}
	if f.Flagged {
		out = append(out, imap.FlagFlagged)
	}
	if f.Deleted {
		out = append(out, imap.FlagDeleted)
	}
	if f.Draft {
		out = append(out, imap.FlagDraft)
	}
	return out
}

// maxUIDs bounds the expansion of server-supplied number sets so a hostile
// server cannot exhaust memory with one response.
const maxUIDs = 1_000_000

// uidsFrom converts a SEARCH result into unambiguous, sorted, unique UIDs.
func uidsFrom(ns imap.NumSet) ([]uint32, error) {
	set, ok := ns.(imap.UIDSet)
	if !ok {
		return nil, fmt.Errorf("imapx: unexpected search result type %T", ns)
	}
	var uids []uint32
	for _, r := range set {
		if r.Stop == 0 {
			return nil, errors.New("imapx: unbounded search result")
		}
		for u := r.Start; ; u++ {
			if len(uids) >= maxUIDs {
				return nil, errors.New("imapx: too many search results")
			}
			uids = append(uids, uint32(u))
			if u == r.Stop {
				break
			}
		}
	}
	slices.Sort(uids)
	return slices.Compact(uids), nil
}
