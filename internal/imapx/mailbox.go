package imapx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"btcx/internal/model"
)

// maxMailboxes bounds a LIST response so a hostile server cannot exhaust
// memory with an unbounded folder list.
const maxMailboxes = 4096

// errTooManyMailboxes is reported when a LIST response exceeds maxMailboxes.
var errTooManyMailboxes = errors.New("imapx: too many mailboxes")

// Mailboxes lists all folders with LIST "" "*", preserving server order.
func (c *Client) Mailboxes(ctx context.Context) ([]model.Mailbox, error) {
	var out []model.Mailbox
	err := c.do(ctx, func() error {
		var opts *imap.ListOptions
		if c.c.Caps().Has(imap.CapSpecialUse) {
			// Ask for \Sent, \Drafts, ... so the app can find the role folders.
			opts = &imap.ListOptions{ReturnSpecialUse: true}
		}
		cmd := c.c.List("", "*", opts)
		for data := cmd.Next(); data != nil; data = cmd.Next() {
			if len(out) >= maxMailboxes {
				cmd.Close() // drain so the decoder stays in sync
				return errTooManyMailboxes
			}
			out = append(out, mailboxFromList(data))
		}
		return cmd.Close()
	})
	if err != nil {
		if errors.Is(err, errTooManyMailboxes) {
			return nil, err
		}
		return nil, fmt.Errorf("imapx: mailboxes: %w", err)
	}
	return out, nil
}

// mailboxFromList converts a LIST response into a model mailbox.
func mailboxFromList(data *imap.ListData) model.Mailbox {
	mbox := model.Mailbox{Name: data.Mailbox}
	if data.Delim != 0 {
		mbox.Delimiter = string(data.Delim)
	}
	for _, attr := range data.Attrs {
		mbox.Attributes = append(mbox.Attributes, string(attr))
	}
	return mbox
}

// Envelopes fetches summaries for messages with UID >= startUID (ascending),
// at most count.
func (c *Client) Envelopes(ctx context.Context, mailbox string, startUID uint32, count int) ([]model.Envelope, error) {
	if count <= 0 {
		return nil, nil
	}
	if startUID == 0 {
		startUID = 1
	}
	var out []model.Envelope
	err := c.do(ctx, func() error {
		sel, err := c.selectMailbox(mailbox)
		if err != nil {
			return err
		}
		var set imap.UIDSet
		set.AddRange(imap.UID(startUID), 0) // n:* - filtered below for empty tail

		cmd := c.c.Fetch(set, &imap.FetchOptions{
			Envelope:      true,
			Flags:         true,
			RFC822Size:    true,
			BodyStructure: &imap.FetchItemBodyStructure{Extended: true},
		})
		for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
			buf, err := msg.Collect()
			if err != nil {
				cmd.Close()
				return fmt.Errorf("collect: %w", err)
			}
			// IMAP resolves n:* to the highest message when n is past the
			// end, so the server may return a message below startUID.
			if uint32(buf.UID) < startUID {
				continue
			}
			out = append(out, envelopeFrom(mailbox, sel.UIDValidity, buf))
			if len(out) >= count {
				break
			}
		}
		return cmd.Close()
	})
	if err != nil {
		return nil, fmt.Errorf("imapx: envelopes %q: %w", mailbox, err)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref.UID < out[j].Ref.UID })
	return out, nil
}

// RawMessage fetches a complete message without setting \Seen.
func (c *Client) RawMessage(ctx context.Context, mailbox string, uid uint32) ([]byte, error) {
	if uid == 0 {
		return nil, errors.New("imapx: raw message: invalid UID 0")
	}
	var raw []byte
	err := c.do(ctx, func() error {
		if _, err := c.selectMailbox(mailbox); err != nil {
			return err
		}
		section := &imap.FetchItemBodySection{Peek: true} // BODY.PEEK[]
		cmd := c.c.Fetch(imap.UIDSetNum(imap.UID(uid)), &imap.FetchOptions{
			BodySection: []*imap.FetchItemBodySection{section},
		})
		for msg := cmd.Next(); msg != nil; msg = cmd.Next() {
			for item := msg.Next(); item != nil; item = msg.Next() {
				data, ok := item.(imapclient.FetchItemDataBodySection)
				if !ok || data.Literal == nil {
					continue
				}
				if data.Literal.Size() > c.maxMessageBytes {
					cmd.Close()
					return ErrMessageTooLarge
				}
				b, err := io.ReadAll(io.LimitReader(data.Literal, c.maxMessageBytes+1))
				if err != nil {
					cmd.Close()
					return fmt.Errorf("read body: %w", err)
				}
				if int64(len(b)) > c.maxMessageBytes {
					cmd.Close()
					return ErrMessageTooLarge
				}
				raw = b
			}
		}
		return cmd.Close()
	})
	if err != nil {
		if errors.Is(err, ErrMessageTooLarge) {
			return nil, ErrMessageTooLarge
		}
		return nil, fmt.Errorf("imapx: raw message %s/%d: %w", mailbox, uid, err)
	}
	if raw == nil {
		return nil, fmt.Errorf("imapx: raw message %s/%d: not found", mailbox, uid)
	}
	return raw, nil
}

// SetFlags adds and removes flags on a single message.
func (c *Client) SetFlags(ctx context.Context, mailbox string, uid uint32, add, remove model.Flags) error {
	if uid == 0 {
		return errors.New("imapx: set flags: invalid UID 0")
	}
	err := c.do(ctx, func() error {
		if _, err := c.selectMailbox(mailbox); err != nil {
			return err
		}
		set := imap.UIDSetNum(imap.UID(uid))
		if flags := flagsToIMAP(add); len(flags) > 0 {
			store := &imap.StoreFlags{Op: imap.StoreFlagsAdd, Silent: true, Flags: flags}
			if err := c.c.Store(set, store, nil).Close(); err != nil {
				return fmt.Errorf("add flags: %w", err)
			}
		}
		if flags := flagsToIMAP(remove); len(flags) > 0 {
			store := &imap.StoreFlags{Op: imap.StoreFlagsDel, Silent: true, Flags: flags}
			if err := c.c.Store(set, store, nil).Close(); err != nil {
				return fmt.Errorf("remove flags: %w", err)
			}
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("imapx: set flags %s/%d: %w", mailbox, uid, err)
	}
	return nil
}

// Search looks for text in SUBJECT, FROM, TO or BODY and returns the matching
// UIDs in ascending order.
func (c *Client) Search(ctx context.Context, mailbox, text string) ([]uint32, error) {
	if strings.TrimSpace(text) == "" {
		return nil, errors.New("imapx: search: empty query")
	}
	var uids []uint32
	err := c.do(ctx, func() error {
		if _, err := c.selectMailbox(mailbox); err != nil {
			return err
		}
		data, err := c.c.UIDSearch(searchCriteria(text), nil).Wait()
		if err != nil {
			return err
		}
		uids, err = uidsFrom(data.All)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("imapx: search %q: %w", mailbox, err)
	}
	return uids, nil
}

// searchCriteria builds OR(SUBJECT, OR(FROM, OR(TO, BODY))).
func searchCriteria(text string) *imap.SearchCriteria {
	header := func(key string) imap.SearchCriteria {
		return imap.SearchCriteria{Header: []imap.SearchCriteriaHeaderField{{Key: key, Value: text}}}
	}
	crit := imap.SearchCriteria{Body: []string{text}}
	for _, key := range []string{"To", "From", "Subject"} {
		crit = imap.SearchCriteria{Or: [][2]imap.SearchCriteria{{header(key), crit}}}
	}
	return &crit
}

// Append stores raw bytes in mailbox with the given flags and returns the new
// message reference. UID is 0 on servers without UIDPLUS.
func (c *Client) Append(ctx context.Context, mailbox string, raw []byte, flags model.Flags) (model.Ref, error) {
	var ref model.Ref
	err := c.do(ctx, func() error {
		cmd := c.c.Append(mailbox, int64(len(raw)), &imap.AppendOptions{Flags: flagsToIMAP(flags)})
		if _, err := cmd.Write(raw); err != nil {
			cmd.Close()
			return fmt.Errorf("write literal: %w", err)
		}
		if err := cmd.Close(); err != nil {
			return fmt.Errorf("close literal: %w", err)
		}
		data, err := cmd.Wait()
		if err != nil {
			return err
		}
		ref = model.Ref{Mailbox: mailbox, UIDValidity: data.UIDValidity, UID: uint32(data.UID)}
		return nil
	})
	if err != nil {
		return model.Ref{}, fmt.Errorf("imapx: append to %q: %w", mailbox, err)
	}
	return ref, nil
}

// Move relocates a message into dest. UID MOVE is used when available,
// otherwise go-imap falls back to COPY + STORE \Deleted + UID EXPUNGE.
func (c *Client) Move(ctx context.Context, mailbox string, uid uint32, dest string) error {
	if uid == 0 {
		return errors.New("imapx: move: invalid UID 0")
	}
	if dest == "" {
		return errors.New("imapx: move: empty destination")
	}
	err := c.do(ctx, func() error {
		if _, err := c.selectMailbox(mailbox); err != nil {
			return err
		}
		_, err := c.c.Move(imap.UIDSetNum(imap.UID(uid)), dest).Wait()
		return err
	})
	if err != nil {
		return fmt.Errorf("imapx: move %s/%d to %q: %w", mailbox, uid, dest, err)
	}
	return nil
}
