package smtpx

import (
	"context"
	"errors"
	"fmt"
)

// Verify tests submission connectivity and authentication without sending
// anything: connect (TLS as configured), greet, STARTTLS when required, EHLO,
// AUTH when Username is set, then RSET/QUIT. The error names the failing stage
// and never contains the password.
func Verify(ctx context.Context, cfg SendConfig) error {
	switch {
	case cfg.Host == "":
		return &ValidationError{Err: errors.New("empty host")}
	case cfg.TLS != TLSImplicit && cfg.TLS != TLSStartTLS:
		return &ValidationError{Err: fmt.Errorf("invalid TLS mode %d", cfg.TLS)}
	}

	c, closer, err := connect(ctx, cfg)
	if err != nil {
		return fmt.Errorf("smtpx: verify: %w", err)
	}
	defer closer()

	if cfg.Username != "" {
		if err := authenticate(c, cfg); err != nil {
			return fmt.Errorf("smtpx: verify: %w", err)
		}
	}
	if err := c.Reset(); err != nil {
		return fmt.Errorf("smtpx: verify: RSET: %w", ctxErr(ctx, err))
	}
	if err := c.Quit(); err != nil {
		return fmt.Errorf("smtpx: verify: QUIT: %w", ctxErr(ctx, err))
	}
	return nil
}
