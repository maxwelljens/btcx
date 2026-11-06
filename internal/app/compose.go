package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"btcx/internal/buildinfo"
	"btcx/internal/compose"
	"btcx/internal/contract"
	"btcx/internal/model"
	"btcx/internal/script"
)

// NewCompose returns an empty draft for the account.
func (c *Controller) NewCompose(account string) contract.ComposeDraft {
	return contract.ComposeDraft{Account: account}
}

// ReplyTo builds a reply draft. It may fetch the original message from the
// server when it is not cached, so it can block briefly when called from the
// UI thread; the reader pane has normally cached it already.
func (c *Controller) ReplyTo(account string, ref model.Ref, replyAll bool) contract.ComposeDraft {
	st := c.state(account)
	if st == nil {
		c.fail(fmt.Errorf("app: reply: unknown account %q", account))
		return contract.ComposeDraft{Account: account}
	}
	msg, err := c.messageSync(st, ref)
	if err != nil {
		c.fail(fmt.Errorf("app: reply to %s: %w", ref, err))
		return contract.ComposeDraft{Account: account}
	}
	d := draftFrom(account, compose.Reply(msg, identity(st.cfg), ""))
	d.ReplyTo = ref
	if replyAll {
		d.To = replyAllRecipients(msg, st.cfg.Email)
	}
	return d
}

// Forward builds a forward draft carrying the original as an attachment.
func (c *Controller) Forward(account string, ref model.Ref) contract.ComposeDraft {
	st := c.state(account)
	if st == nil {
		c.fail(fmt.Errorf("app: forward: unknown account %q", account))
		return contract.ComposeDraft{Account: account}
	}
	msg, raw, err := c.messageSyncRaw(st, ref)
	if err != nil {
		c.fail(fmt.Errorf("app: forward %s: %w", ref, err))
		return contract.ComposeDraft{Account: account}
	}
	d := draftFrom(account, compose.Forward(msg, identity(st.cfg), ""))
	// compose.Forward re-serializes the enclosed message, minting a fresh
	// Message-ID and Date; keep the bytes from the server instead so the
	// recipient sees the original message unchanged.
	for i := range d.Attachments {
		if len(raw) > 0 && d.Attachments[i].ContentType == "message/rfc822" {
			d.Attachments[i].Data = raw
		}
	}
	return d
}

// Send delivers the draft in the background; the outcome arrives through
// OnSendResult.
func (c *Controller) Send(draft contract.ComposeDraft) {
	st := c.state(draft.Account)
	if st == nil {
		c.sendError(fmt.Errorf("app: send: unknown account %q", draft.Account))
		return
	}
	c.goRun(func(ctx context.Context) { c.send(ctx, st, draft) })
}

// send builds, vets, submits and records the draft.
func (c *Controller) send(ctx context.Context, st *accountState, draft contract.ComposeDraft) {
	msg, err := c.buildMessage(ctx, st, draft)
	if err != nil {
		c.sendError(fmt.Errorf("app: send: %w", err))
		return
	}

	eng := c.engine()
	if eng != nil {
		ok, warnings := eng.BeforeSend(msg)
		c.reportWarnings("before_send", warnings)
		if !ok {
			c.sendError(errors.New("sending was cancelled by a script"))
			return
		}
	}

	// Bcc recipients belong in the SMTP envelope only; leaving the header in
	// the transmitted copy would disclose them to every recipient.
	rcpts := make([]model.Address, 0, len(draft.To)+len(draft.Cc)+len(draft.Bcc))
	rcpts = append(rcpts, draft.To...)
	rcpts = append(rcpts, draft.Cc...)
	rcpts = append(rcpts, draft.Bcc...)
	msg.Header.Delete("Bcc")

	raw, err := compose.Build(msg, compose.Options{
		From:            identity(st.cfg),
		MessageIDDomain: messageIDDomain(st.cfg.Email),
		UserAgent:       "btcx " + buildinfo.Version(),
	})
	if err != nil {
		c.afterSend(eng, msg, false, err.Error())
		c.sendError(fmt.Errorf("app: send: %w", err))
		return
	}

	err = c.conn.Send(ctx, st.cfg, raw, rcpts)
	c.afterSend(eng, msg, err == nil, errorText(err))
	if err != nil {
		c.sendError(fmt.Errorf("app: send: %w", err))
		return
	}
	c.appendSent(ctx, st, raw)
	c.events.OnSendResult(true, "Message sent")
}

// SaveDraft appends the draft to the account's Drafts mailbox.
func (c *Controller) SaveDraft(draft contract.ComposeDraft) {
	st := c.state(draft.Account)
	if st == nil {
		c.fail(fmt.Errorf("app: save draft: unknown account %q", draft.Account))
		return
	}
	c.goRun(func(ctx context.Context) {
		if st.cfg.DraftsMailbox == "" {
			c.fail(errors.New("app: save draft: no drafts mailbox configured"))
			return
		}
		msg, err := c.buildMessage(ctx, st, draft)
		if err != nil {
			c.fail(fmt.Errorf("app: save draft: %w", err))
			return
		}
		raw, err := compose.Build(msg, compose.Options{
			From:            identity(st.cfg),
			MessageIDDomain: messageIDDomain(st.cfg.Email),
			UserAgent:       "btcx " + buildinfo.Version(),
		})
		if err != nil {
			c.fail(fmt.Errorf("app: save draft: %w", err))
			return
		}
		cl, err := c.clientFor(ctx, st)
		if err != nil {
			c.fail(fmt.Errorf("app: save draft: %w", err))
			return
		}
		if _, err := cl.Append(ctx, st.cfg.DraftsMailbox, raw, model.Flags{Draft: true}); err != nil {
			c.dropClient(st)
			c.fail(fmt.Errorf("app: save draft: %w", err))
			return
		}
		c.events.OnStatus("Draft saved")
	})
}

// buildMessage turns an editable draft into a model message. It never fails on
// thread headers: a message that cannot be fetched is sent without them.
func (c *Controller) buildMessage(ctx context.Context, st *accountState, draft contract.ComposeDraft) (*model.Message, error) {
	if len(draft.To)+len(draft.Cc)+len(draft.Bcc) == 0 {
		return nil, errors.New("no recipients")
	}
	msg := &model.Message{
		Header: model.NewHeader(),
		Plain:  withSignature(draft.Plain, st.cfg.Signature),
		HTML:   draft.HTML,
	}
	for _, field := range []struct {
		key  string
		list []model.Address
	}{{"To", draft.To}, {"Cc", draft.Cc}, {"Bcc", draft.Bcc}} {
		if v := model.FormatAddressList(field.list); v != "" {
			msg.Header.Set(field.key, v)
		}
	}
	if draft.Subject != "" {
		msg.Header.Set("Subject", draft.Subject)
	}
	for _, a := range draft.Attachments {
		msg.Attachments = append(msg.Attachments, model.Attachment{
			Filename:    safeFilename(a.Filename),
			ContentType: a.ContentType,
			Data:        a.Data,
		})
	}
	if draft.ReplyTo != (model.Ref{}) {
		orig, err := c.message(ctx, st, draft.ReplyTo)
		if err != nil {
			c.log.Warn("app: sending without thread headers",
				"account", st.cfg.Name, "ref", draft.ReplyTo.String(), "err", err)
		} else {
			setThreadHeaders(msg, orig)
		}
	}
	return msg, nil
}

// draftFrom converts a composed message into an editable draft.
func draftFrom(account string, m *model.Message) contract.ComposeDraft {
	d := contract.ComposeDraft{
		Account: account,
		To:      m.To(),
		Cc:      m.Cc(),
		Bcc:     m.Bcc(),
		Subject: m.Subject(),
		Plain:   m.Plain,
		HTML:    m.HTML,
	}
	for _, a := range m.Attachments {
		d.Attachments = append(d.Attachments, contract.DraftAttachment{
			Filename:    safeFilename(a.Filename),
			ContentType: a.ContentType,
			Data:        a.Data,
		})
	}
	return d
}

// replyAllRecipients lists the reply target plus everyone else on the original,
// minus the account itself.
func replyAllRecipients(orig *model.Message, self string) []model.Address {
	var out []model.Address
	seen := map[string]bool{strings.ToLower(self): true}
	add := func(list []model.Address) {
		for _, a := range list {
			key := strings.ToLower(a.Addr)
			if a.IsZero() || key == "" || seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, a)
		}
	}
	replyTo := orig.Addresses("Reply-To")
	if len(replyTo) == 0 {
		replyTo = orig.From()
	}
	add(replyTo)
	add(orig.To())
	add(orig.Cc())
	return out
}

// setThreadHeaders threads msg under orig.
func setThreadHeaders(msg, orig *model.Message) {
	id := cleanID(orig.MessageID())
	if id == "" {
		return
	}
	refs := orig.References()
	if len(refs) == 0 || refs[len(refs)-1] != id {
		refs = append(refs, id)
	}
	parts := make([]string, 0, len(refs))
	for _, r := range refs {
		if r = cleanID(r); r != "" {
			parts = append(parts, "<"+r+">")
		}
	}
	msg.Header.Set("In-Reply-To", "<"+id+">")
	msg.Header.Set("References", strings.Join(parts, " "))
}

// cleanID strips angle brackets and whitespace from a message identifier.
func cleanID(id string) string {
	id = strings.TrimSpace(id)
	id = strings.TrimPrefix(id, "<")
	if i := strings.IndexByte(id, '>'); i >= 0 {
		id = id[:i]
	}
	return strings.TrimSpace(id)
}

// withSignature appends the account signature once, separated by the standard
// "-- " delimiter. A body that already carries it is left alone.
func withSignature(body, signature string) string {
	if signature == "" {
		return body
	}
	block := "-- \n" + signature
	if strings.Contains(body, block) {
		return body
	}
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return block
	}
	return body + "\n\n" + block
}

// afterSend runs the after_send hooks.
func (c *Controller) afterSend(eng *script.Engine, msg *model.Message, sent bool, errMsg string) {
	if eng == nil {
		return
	}
	c.reportWarnings("after_send", eng.AfterSend(msg, sent, errMsg))
}

// appendSent stores a copy in the Sent mailbox; failure is logged, never
// reported as a send failure.
func (c *Controller) appendSent(ctx context.Context, st *accountState, raw []byte) {
	if st.cfg.SentMailbox == "" {
		return
	}
	cl, err := c.clientFor(ctx, st)
	if err != nil {
		c.log.Warn("app: sent copy skipped", "account", st.cfg.Name, "err", err)
		return
	}
	if _, err := cl.Append(ctx, st.cfg.SentMailbox, raw, model.Flags{Seen: true}); err != nil {
		c.log.Warn("app: sent copy skipped", "account", st.cfg.Name, "err", err)
	}
}

// sendError reports a failed send to the user and the log.
func (c *Controller) sendError(err error) {
	c.log.Warn("app: send failed", "err", err)
	c.events.OnSendResult(false, err.Error())
}

// errorText renders err for script hooks without dereferencing nil.
func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
