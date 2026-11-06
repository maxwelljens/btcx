package app

import (
	"context"
	"fmt"
	"log/slog"

	"btcx/internal/config"
	"btcx/internal/imapx"
	"btcx/internal/model"
	"btcx/internal/smtpx"
)

var _ MailClient = (*imapx.Client)(nil)

// realConnector maps config accounts onto the IMAP and SMTP clients. It
// resolves credentials per server and never logs a password.
type realConnector struct{ log *slog.Logger }

var _ Connector = realConnector{}

// DialIMAP connects and authenticates one IMAP session.
func (r realConnector) DialIMAP(ctx context.Context, a config.Account) (MailClient, error) {
	password, err := a.IMAP.ResolvePassword(ctx, a.Name)
	if err != nil {
		return nil, fmt.Errorf("app: connect %s: %w", a.Name, err)
	}
	if a.IMAP.InsecureSkipVerify {
		r.log.Warn("app: IMAP TLS certificate verification is DISABLED",
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
	})
	if err != nil {
		return nil, fmt.Errorf("app: connect %s: %w", a.Name, err)
	}
	return cl, nil
}

// Send submits raw with the account's SMTP settings. Bcc recipients travel in
// rcpts only; raw never contains them.
func (r realConnector) Send(ctx context.Context, a config.Account, raw []byte, rcpts []model.Address) error {
	var password string
	if a.SMTP.Username != "" {
		var err error
		if password, err = a.SMTP.ResolvePassword(ctx, a.Name); err != nil {
			return fmt.Errorf("app: send %s: %w", a.Name, err)
		}
	}
	if a.SMTP.InsecureSkipVerify {
		r.log.Warn("app: SMTP TLS certificate verification is DISABLED",
			"account", a.Name, "host", a.SMTP.Host)
	}
	if err := smtpx.Send(ctx, smtpx.SendConfig{
		Host:               a.SMTP.Host,
		Port:               a.SMTP.Port,
		Username:           a.SMTP.Username,
		Password:           password,
		TLS:                smtpTLS(a.SMTP.TLS),
		InsecureSkipVerify: a.SMTP.InsecureSkipVerify,
		CAFile:             a.SMTP.CAFile,
		From:               identity(a),
	}, raw, rcpts); err != nil {
		return fmt.Errorf("app: send %s: %w", a.Name, err)
	}
	return nil
}

// username falls back to the account address when none is configured.
func username(configured, email string) string {
	if configured != "" {
		return configured
	}
	return email
}

func imapTLS(mode config.TLSMode) imapx.TLSMode {
	if mode == config.TLSStartTLS {
		return imapx.TLSStartTLS
	}
	return imapx.TLSImplicit
}

func smtpTLS(mode config.TLSMode) smtpx.TLSMode {
	if mode == config.TLSImplicit {
		return smtpx.TLSImplicit
	}
	return smtpx.TLSStartTLS
}

// identity is the account's From address: RealName, falling back to
// DisplayName, with the account address.
func identity(a config.Account) model.Address {
	name := a.RealName
	if name == "" {
		name = a.DisplayName
	}
	return model.Address{Name: name, Addr: a.Email}
}

// messageIDDomain is the domain part of the account address, or "localhost".
func messageIDDomain(email string) string {
	for i := 0; i < len(email); i++ {
		if email[i] == '@' && i+1 < len(email) {
			return email[i+1:]
		}
	}
	return "localhost"
}
