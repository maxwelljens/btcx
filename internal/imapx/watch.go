package imapx

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// idleRestart is how often IDLE is cycled. Servers drop an idle connection
// after 30 minutes; re-issuing also re-checks for messages whose EXISTS
// notification was lost.
const idleRestart = 25 * time.Minute

// Watch blocks observing mailbox with IDLE, calling onNew with the UIDs of
// newly appeared messages. It returns nil when ctx is cancelled, and holds the
// connection exclusively for its lifetime: other commands fail with ErrBusy.
// onNew runs on the watching goroutine, so it must not block for long; it can
// call back into the client, which yields ErrBusy rather than deadlocking.
func (c *Client) Watch(ctx context.Context, mailbox string, onNew func(uids []uint32)) error {
	if onNew == nil {
		return errors.New("imapx: watch: nil callback")
	}
	c.watchMu.Lock()
	if c.watching {
		c.watchMu.Unlock()
		return ErrBusy
	}
	c.watching = true
	c.watchMu.Unlock()
	defer func() {
		c.watchMu.Lock()
		c.watching = false
		c.watchMu.Unlock()
	}()

	c.mu.Lock()
	defer c.mu.Unlock()

	// Drop notifications queued before IDLE started: they are covered by the
	// initial UID scan below.
	select {
	case <-c.notify:
	default:
	}

	sel, err := c.selectMailbox(mailbox)
	if err != nil {
		return fmt.Errorf("imapx: watch %q: %w", mailbox, err)
	}
	var lastUID uint32
	if sel.UIDNext > 0 {
		lastUID = uint32(sel.UIDNext) - 1
	} else {
		// Pre-RFC 3501 servers may omit UIDNEXT.
		if lastUID, err = c.lastUID(); err != nil {
			return fmt.Errorf("imapx: watch %q: scan UIDs: %w", mailbox, err)
		}
	}

	for {
		// shortcut: a server that never acknowledges IDLE stalls Watch until
		// the connection dies; go-imap offers no cancellation there.
		idle, err := c.c.Idle()
		if err != nil {
			return fmt.Errorf("imapx: watch %q: idle: %w", mailbox, err)
		}
		timer := time.NewTimer(idleRestart)
		select {
		case <-ctx.Done():
			timer.Stop()
			_ = c.stopIdle(idle) // best-effort: the caller is shutting down
			return nil
		case <-c.notify:
			timer.Stop()
		case <-timer.C:
		}
		if err := c.stopIdle(idle); err != nil {
			return fmt.Errorf("imapx: watch %q: stop idle: %w", mailbox, err)
		}

		uids, err := c.uidsAbove(lastUID)
		if err != nil {
			return fmt.Errorf("imapx: watch %q: fetch new UIDs: %w", mailbox, err)
		}
		if len(uids) == 0 {
			continue
		}
		lastUID = uids[len(uids)-1]
		onNew(uids)
	}
}

// stopIdle terminates an IDLE command. Errors are meaningful only when the
// caller still cares about the connection.
func (c *Client) stopIdle(idle *imapclient.IdleCommand) error {
	if err := idle.Close(); err != nil {
		return err
	}
	return idle.Wait()
}

// lastUID returns the highest UID currently in the selected mailbox, or 0.
func (c *Client) lastUID() (uint32, error) {
	var set imap.UIDSet
	set.AddRange(1, 0)
	uids, err := c.searchUIDs(set)
	if err != nil || len(uids) == 0 {
		return 0, err
	}
	return uids[len(uids)-1], nil
}

// uidsAbove returns the selected mailbox's UIDs greater than last.
func (c *Client) uidsAbove(last uint32) ([]uint32, error) {
	if last == ^uint32(0) {
		return nil, nil
	}
	var set imap.UIDSet
	set.AddRange(imap.UID(last+1), 0)
	uids, err := c.searchUIDs(set)
	if err != nil {
		return nil, err
	}
	out := uids[:0]
	for _, uid := range uids {
		if uid > last {
			out = append(out, uid) // n:* also matches the last message
		}
	}
	return out, nil
}

// searchUIDs runs UID SEARCH on the selected mailbox.
func (c *Client) searchUIDs(set imap.UIDSet) ([]uint32, error) {
	data, err := c.c.UIDSearch(&imap.SearchCriteria{UID: []imap.UIDSet{set}}, nil).Wait()
	if err != nil {
		return nil, err
	}
	return uidsFrom(data.All)
}
