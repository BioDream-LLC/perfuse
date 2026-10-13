package fhirserver

import (
	"context"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// WriteLimits keeps a server open to strangers from being filled by one of them.
//
// A public test server hands out credentials that can write, because writing is what testing a PAS or a subscription needs.
// Without a ceiling, one client in a loop fills the disk and the server stops for everybody. These are for that kind of
// server; a hospital's own endpoint has neither set.
type WriteLimits struct {
	// PerMinute is how many writes one client address may make a minute. 0 is no limit.
	PerMinute int

	// MaxResources is how many resources the server holds before it refuses new ones. 0 is no limit. Updates and deletes
	// are still accepted when it is full, so data can be corrected and the server emptied.
	MaxResources int
}

type writeGate struct {
	limits WriteLimits
	count  func() (int, error)
	now    func() time.Time

	mu      sync.Mutex
	window  time.Time
	writes  map[string]int
	total   int
	totalAt time.Time
}

// changesData says whether a request changes data. A search by POST does not.
func changesData(r *http.Request) bool {
	switch r.Method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	}
	return !strings.HasSuffix(r.URL.Path, "/_search")
}

// creates says whether a write can add a resource (an update can create one, so PUT counts; a delete cannot).
func creates(r *http.Request) bool { return r.Method == http.MethodPost || r.Method == http.MethodPut }

func clientAddress(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// admit returns the status and message to refuse a write with, or 0 to let it through.
func (g *writeGate) admit(r *http.Request) (int, string) {
	now := g.now()
	g.mu.Lock()
	defer g.mu.Unlock()

	if g.limits.PerMinute > 0 {
		if now.Sub(g.window) >= time.Minute {
			g.window, g.writes = now, map[string]int{}
		}
		who := clientAddress(r)
		if g.writes[who] >= g.limits.PerMinute {
			return http.StatusTooManyRequests, "this server accepts a limited number of writes a minute from one client; try again in a minute"
		}
		g.writes[who]++
	}

	if g.limits.MaxResources > 0 && creates(r) {
		// Counted at most every 30 seconds: a count per write would make every write a full-table query.
		if now.Sub(g.totalAt) >= 30*time.Second {
			if n, err := g.count(); err == nil {
				g.total, g.totalAt = n, now
			}
		}
		if g.total >= g.limits.MaxResources {
			return http.StatusInsufficientStorage, "this server holds as many resources as it is allowed to; it accepts updates and deletes, not new resources"
		}
		g.total++
	}
	return 0, ""
}

// limitWrites applies WriteLimits in front of h. With no limits set it is h.
func (s *Server) limitWrites(h http.Handler) http.Handler {
	if s.WriteLimits.PerMinute <= 0 && s.WriteLimits.MaxResources <= 0 {
		return h
	}
	g := &writeGate{limits: s.WriteLimits, now: time.Now, writes: map[string]int{}, count: func() (int, error) {
		counts, err := s.Store.Counts(context.Background())
		total := 0
		for _, n := range counts {
			total += n
		}
		return total, err
	}}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if changesData(r) {
			if status, msg := g.admit(r); status != 0 {
				if status == http.StatusTooManyRequests {
					w.Header().Set("Retry-After", "60")
				}
				code := "throttled"
				if status == http.StatusInsufficientStorage {
					code = "too-costly"
				}
				s.writeOutcome(w, r, status, "error", code, msg)
				return
			}
		}
		h.ServeHTTP(w, r)
	})
}
