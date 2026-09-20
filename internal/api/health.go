package api

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// Liveness and readiness are different questions, and conflating them is how a
// rolling deploy turns into an outage.
//
// Liveness asks: is this process broken beyond recovery, and should the
// supervisor kill it? Almost nothing should make it answer no. A liveness probe
// that fails because a database blipped causes a restart, which drops in-flight
// messages and reconnects every session, which makes the blip worse. So liveness
// here checks that the process is running and its request path is responsive, and
// nothing else.
//
// Readiness asks: should traffic be sent to this instance right now? That answer
// legitimately changes over the life of a process - false while channels are
// starting, true in steady state, and false again the moment shutdown begins.
//
// The last of those is the one that actually earns its keep. Without it, a rolling
// update stops the process while the load balancer is still sending it messages,
// and a sender that had a healthy connection gets a refused write it will report as
// an interface outage.

// draining reports whether shutdown has begun.
//
// Held on the Server rather than passed in, because every handler and probe needs
// to see the same answer the instant it changes.
type lifecycle struct {
	draining atomic.Bool
	ready    atomic.Bool
}

// BeginDraining marks the instance as no longer accepting new traffic.
//
// Called at the top of shutdown, before anything is actually stopped, so there is
// a window in which the load balancer can notice and stop routing while the
// process is still perfectly able to finish what it already accepted.
func (s *Server) BeginDraining() {
	s.life.draining.Store(true)
}

// MarkReady records that startup finished and channels have been started.
//
// Until this is called, readiness answers no. Reporting ready before channels are
// up would let a load balancer send messages to an instance with nothing listening,
// which presents to the sender as a connection refused rather than as a deploy in
// progress.
func (s *Server) MarkReady() {
	s.life.ready.Store(true)
}

// Draining reports whether shutdown has begun.
func (s *Server) Draining() bool { return s.life.draining.Load() }

// healthResponse is deliberately more informative than the status code.
//
// The status code is what a probe reads. The body is for the person looking at
// why a pod will not come up, and telling them which check failed saves an hour.
type healthResponse struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
	// Version lets somebody confirm which build is actually answering, which
	// during a rollback is the only question that matters.
	Version string `json:"version,omitempty"`
}

// handleLiveness answers whether the process should keep running.
//
// It does no I/O on purpose. Any dependency check here is a way for an external
// outage to become a restart loop.
func (s *Server) handleLiveness(w http.ResponseWriter, r *http.Request) {
	s.ok(w, healthResponse{Status: "alive", Version: s.Version})
}

// handleReadiness answers whether traffic should be sent here.
func (s *Server) handleReadiness(w http.ResponseWriter, r *http.Request) {
	checks := map[string]string{}
	ready := true

	switch {
	case s.life.draining.Load():
		// Reported separately from a failure, because "shutting down as
		// instructed" and "broken" call for completely different reactions from
		// whoever is reading.
		checks["lifecycle"] = "draining"
		ready = false
	case !s.life.ready.Load():
		checks["lifecycle"] = "starting"
		ready = false
	default:
		checks["lifecycle"] = "running"
	}

	// The database backs sessions, the audit log and the delivery queue. Without
	// it this instance cannot accept a message it would be able to account for
	// later, so it should not be sent one.
	if s.Store != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		if err := s.Store.DB().PingContext(ctx); err != nil {
			checks["database"] = "unreachable: " + err.Error()
			ready = false
		} else {
			checks["database"] = "ok"
		}
	}

	// Deliberately NOT a readiness input: whether individual channels are
	// healthy.
	//
	// A misconfigured channel is an alert, not an unreadiness. Failing the probe
	// because one channel of fifty will not start would pull the instance out of
	// rotation and stop the other forty-nine - turning one broken interface into
	// a total outage. Channel state is reported here for the human reading, and
	// it never changes the verdict.
	if s.Runtime != nil {
		running, failed := s.Runtime.channelCounts()
		checks["channels"] = strconv.Itoa(running) + " running"
		if failed > 0 {
			checks["channels_failed"] = strconv.Itoa(failed) + " failed to start (does not affect readiness)"
		}
	}

	body := healthResponse{Status: "ready", Checks: checks, Version: s.Version}
	if !ready {
		body.Status = "not ready"
		// 503 rather than 500: this is a state, not a fault, and it is expected
		// to change without anybody intervening.
		s.writeJSON(w, http.StatusServiceUnavailable, body)
		return
	}
	s.ok(w, body)
}

// channelCounts reports how many channels are running and how many failed to
// start.
func (rt *Runtime) channelCounts() (running, failed int) {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return len(rt.running), len(rt.failed)
}
