package app

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"btcx/internal/contract"
)

// startWatch launches the new-mail watcher for an account once.
func (c *Controller) startWatch(name string) {
	st := c.state(name)
	if st == nil {
		return
	}
	c.mu.Lock()
	if st.watching {
		c.mu.Unlock()
		return
	}
	st.watching = true
	c.mu.Unlock()

	c.goRun(func(ctx context.Context) {
		defer func() {
			c.mu.Lock()
			st.watching = false
			c.mu.Unlock()
		}()
		c.watchLoop(ctx, st)
	})
}

// watchLoop keeps an IDLE connection to the inbox, reconnecting with bounded
// backoff. It owns its connection: the controller's command client stays
// usable while IDLE holds the watcher's.
func (c *Controller) watchLoop(ctx context.Context, st *accountState) {
	backoff := initialBackoff
	for ctx.Err() == nil {
		cl, err := c.conn.DialIMAP(ctx, st.cfg)
		if err == nil {
			backoff = initialBackoff
			err = cl.Watch(ctx, inboxName, func(uids []uint32) {
				c.onNewMail(st, uids)
			})
			if cerr := cl.Close(); cerr != nil {
				c.log.Debug("app: close watch connection", "account", st.cfg.Name, "err", cerr)
			}
			if ctx.Err() != nil {
				return
			}
			if err == nil {
				err = errors.New("watch ended")
			}
			c.fail(fmt.Errorf("app: watch %s: %w", st.cfg.Name, err))
		} else {
			c.fail(fmt.Errorf("app: watch %s: %w", st.cfg.Name, err))
		}
		if !c.sleep(ctx, backoff) {
			return
		}
		backoff = min(backoff*2, maxBackoff)
	}
}

// onNewMail runs on the watching goroutine, which must not block, so the
// fetch happens on a controller goroutine.
func (c *Controller) onNewMail(st *accountState, uids []uint32) {
	if len(uids) == 0 {
		return
	}
	c.goRun(func(ctx context.Context) { c.handleNewMail(ctx, st, uids) })
}

// handleNewMail fetches the new envelopes, announces them, runs the
// on_new_mail script hooks and refreshes the list when the inbox is shown.
func (c *Controller) handleNewMail(ctx context.Context, st *accountState, uids []uint32) {
	cl, err := c.clientFor(ctx, st)
	if err != nil {
		c.fail(fmt.Errorf("app: new mail %s: %w", st.cfg.Name, err))
		return
	}
	sort.Slice(uids, func(i, j int) bool { return uids[i] < uids[j] })
	envs, err := envelopesForUIDs(ctx, cl, inboxName, uids)
	if err != nil {
		c.dropClient(st)
		c.fail(fmt.Errorf("app: new mail %s: %w", st.cfg.Name, err))
		return
	}
	if len(envs) == 0 {
		return
	}

	c.mu.Lock()
	// A search result list is a subset of the folder; merging new mail into it
	// would mix folder rows into the search and corrupt the counts.
	viewing := st.folder == inboxName && !st.searching
	var rows []contract.MessageRow
	if viewing {
		st.rows = mergeRows(rowsFromEnvelopes(envs), st.rows)
		st.counts[inboxName] = countRows(st.rows)
		// Copy under the lock: flag updates mutate st.rows in place.
		rows = append([]contract.MessageRow(nil), st.rows...)
	}
	c.mu.Unlock()

	for _, env := range envs {
		c.events.OnNewMail(st.cfg.Name, rowFromEnvelope(env))
		if eng := c.engine(); eng != nil {
			c.reportWarnings("on_new_mail", eng.OnNewMail(env))
		}
	}
	if viewing {
		c.events.OnMessages(st.cfg.Name, inboxName, rows)
		c.events.OnFolders(st.cfg.Name, c.folderInfos(st))
	}
}
