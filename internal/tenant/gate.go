package tenant

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// Gate admits received messages for one tenant within its limits.
//
// One per tenant, shared by all of its channels, so a customer's limit is on the customer and not on each feed. It is
// asked once per received message and answers in microseconds: the limits and the stored size are read at most once a
// minute, because asking the database on every message would make the limiter the slowest thing on the path it guards.
type Gate struct {
	// Limits returns the tenant's current limits. Read at most every refresh interval.
	Limits func() (Limits, error)
	// StoredBytes returns how much message content the tenant holds now. Read at most every refresh interval, and only
	// when a storage limit is set.
	StoredBytes func() (int64, error)

	now func() time.Time

	mu          sync.Mutex
	readAt      time.Time
	limits      Limits
	stored      int64
	windowStart time.Time
	inWindow    int
}

// ErrOverRate and ErrOverStorage say why a message was not admitted. Both mean "send it again later", never "drop it".
var (
	ErrOverRate    = errors.New("this organisation's message rate limit is reached")
	ErrOverStorage = errors.New("this organisation's storage limit is reached")
)

// refresh is how often limits and stored size are re-read: a raised limit takes effect within a minute.
const refresh = time.Minute

// Admit reports whether one more message may be received now. A failure to read the limits admits the message: a
// broken limiter must not stop a hospital's feeds.
func (g *Gate) Admit() error {
	if g == nil {
		return nil
	}
	now := time.Now()
	if g.now != nil {
		now = g.now()
	}

	g.mu.Lock()
	defer g.mu.Unlock()

	if g.readAt.IsZero() || now.Sub(g.readAt) >= refresh {
		g.readAt = now
		if g.Limits != nil {
			if l, err := g.Limits(); err == nil {
				g.limits = l
			}
		}
		if g.limits.MaxStorageMB > 0 && g.StoredBytes != nil {
			if b, err := g.StoredBytes(); err == nil {
				g.stored = b
			}
		}
	}

	if max := g.limits.MaxStorageMB; max > 0 && g.stored >= int64(max)*1024*1024 {
		return fmt.Errorf("%w (%d MB); messages are refused until stored messages are pruned or the limit is raised", ErrOverStorage, max)
	}

	if max := g.limits.MaxMessagesPerMinute; max > 0 {
		if now.Sub(g.windowStart) >= time.Minute {
			g.windowStart, g.inWindow = now, 0
		}
		if g.inWindow >= max {
			return fmt.Errorf("%w (%d a minute); the sender should resend shortly", ErrOverRate, max)
		}
		g.inWindow++
	}
	return nil
}
