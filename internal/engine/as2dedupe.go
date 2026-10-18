package engine

import (
	"context"
	"strings"
	"sync"
	"time"
)

// as2Duplicates remembers which partner messages a channel has already passed on.
//
// RFC 4130 section 7.5: a partner that never received our receipt sends the same message again, with the same Message-ID,
// and the receiver answers it with a receipt but must not process it twice. Without this an invoice whose receipt was lost
// on the way back became two invoices. The record is the channel's state store (the database, so it survives a restart and
// is shared by every node on one), or memory when there is none.
type as2Duplicates struct {
	mu        sync.Mutex
	busy      map[string]*as2KeyLock
	memory    map[string]time.Time
	lastPrune time.Time
}

type as2KeyLock struct {
	sync.Mutex
	users int
}

// as2DuplicateKey names one partner message. The Message-ID is unique to its sender, not across senders.
func as2DuplicateKey(from, messageID string) string {
	return "as2|" + from + "|" + strings.Trim(strings.TrimSpace(messageID), "<>")
}

// lock serialises work on one key, so a resend arriving while the first copy is still being processed waits for it and then
// sees it as done, rather than both copies going through.
func (d *as2Duplicates) lock(key string) func() {
	d.mu.Lock()
	if d.busy == nil {
		d.busy = map[string]*as2KeyLock{}
	}
	l := d.busy[key]
	if l == nil {
		l = &as2KeyLock{}
		d.busy[key] = l
	}
	l.users++
	d.mu.Unlock()
	l.Lock()
	return func() {
		l.Unlock()
		d.mu.Lock()
		if l.users--; l.users == 0 {
			delete(d.busy, key)
		}
		d.mu.Unlock()
	}
}

// seen reports whether the message was passed on already.
func (c *Channel) as2Seen(ctx context.Context, key string, window time.Duration) (bool, error) {
	if c.queryState != nil {
		unseen, err := c.queryState.Unseen(ctx, c.tenantID, c.cfg.Name, []string{key})
		if err != nil {
			return false, err
		}
		return len(unseen) == 0, nil
	}
	d := &c.as2Dup
	d.mu.Lock()
	defer d.mu.Unlock()
	at, ok := d.memory[key]
	return ok && time.Since(at) < window, nil
}

// as2Remember records a message as passed on, and now and then forgets those older than the window.
func (c *Channel) as2Remember(ctx context.Context, key string, window time.Duration) error {
	now := time.Now()
	d := &c.as2Dup
	d.mu.Lock()
	prune := now.Sub(d.lastPrune) > time.Hour
	if prune {
		d.lastPrune = now
	}
	if c.queryState == nil {
		if d.memory == nil {
			d.memory = map[string]time.Time{}
		}
		d.memory[key] = now
		if prune {
			for k, at := range d.memory {
				if now.Sub(at) >= window {
					delete(d.memory, k)
				}
			}
		}
	}
	d.mu.Unlock()
	if c.queryState == nil {
		return nil
	}
	if err := c.queryState.MarkSeen(ctx, c.tenantID, c.cfg.Name, []string{key}, now); err != nil {
		return err
	}
	if prune {
		if _, err := c.queryState.Prune(ctx, c.tenantID, c.cfg.Name, window, now); err != nil {
			c.log.Warn("as2: old Message-IDs could not be forgotten", "error", err)
		}
	}
	return nil
}
