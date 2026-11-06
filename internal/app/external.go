package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"btcx/internal/contract"
	"btcx/internal/editor"
	"btcx/internal/model"
)

// StartExternalEdit opens the draft in the configured external editor on a
// controller goroutine and returns the correlation id. The result arrives
// through OnExternalEditDone; the temporary file is removed before that.
func (c *Controller) StartExternalEdit(draft contract.ComposeDraft) contract.EditRequestID {
	c.mu.Lock()
	c.nextEdit++
	id := c.nextEdit
	c.mu.Unlock()

	c.goRun(func(ctx context.Context) {
		updated, err := c.externalEdit(ctx, draft)
		c.events.OnExternalEditDone(id, updated, err)
	})
	return id
}

// externalEdit writes the draft, waits for the editor and merges the result.
func (c *Controller) externalEdit(ctx context.Context, draft contract.ComposeDraft) (contract.ComposeDraft, error) {
	template, err := editor.DetectEditor(c.cfg.Editor)
	if err != nil {
		return draft, fmt.Errorf("app: detect editor: %w", err)
	}
	if template == "" {
		return draft, errors.New("app: no external editor configured (set editor in config.toml)")
	}
	tmp, err := editor.NewDraft(draft.Subject, draftText(draft))
	if err != nil {
		return draft, fmt.Errorf("app: create draft: %w", err)
	}
	defer tmp.Cleanup()

	session, err := editor.Start(ctx, template, tmp)
	if err != nil {
		return draft, fmt.Errorf("app: start editor: %w", err)
	}
	if err := session.Wait(); err != nil {
		return draft, err
	}
	text, err := tmp.Read()
	if err != nil {
		return draft, err
	}
	parsed, err := editor.ParseDraft(text)
	if err != nil {
		return draft, err
	}
	return mergeEdit(draft, parsed), nil
}

// draftText renders the draft in the editor-friendly header-block format.
func draftText(draft contract.ComposeDraft) string {
	var b strings.Builder
	for _, field := range []struct {
		key string
		val string
	}{
		{"To", model.FormatAddressList(draft.To)},
		{"Cc", model.FormatAddressList(draft.Cc)},
		{"Bcc", model.FormatAddressList(draft.Bcc)},
		{"Subject", draft.Subject},
	} {
		fmt.Fprintf(&b, "%s: %s\n", field.key, field.val)
	}
	b.WriteString("\n")
	b.WriteString(draft.Plain)
	return b.String()
}

// mergeEdit applies the edited headers and body, keeping everything the
// editor cannot represent (account, threading reference, HTML, attachments).
func mergeEdit(draft contract.ComposeDraft, edited *model.Message) contract.ComposeDraft {
	draft.To = edited.To()
	draft.Cc = edited.Cc()
	draft.Bcc = edited.Bcc()
	draft.Subject = edited.Subject()
	draft.Plain = edited.Plain
	return draft
}
