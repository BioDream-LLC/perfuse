package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"

	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/metrics"
	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tenant"
)

// TenantRuntimes hands out one engine per tenant.
//
// This is the half of tenancy that channel isolation did not cover. Isolating the channel *files* stopped one tenant
// reading another's definitions; it did nothing about the engine, so start and stop, metrics, the queue and the
// message store were all shared. One tenant stopping a channel could stop another's, and the metrics endpoint reported
// the sum of everybody.
//
// Runtimes are created on demand rather than up front, because a platform with fifty tenants where three are active
// should not hold fifty engines, and a tenant that never signs in should cost nothing.
type TenantRuntimes struct {
	repos *TenantRepos

	// Shared by every runtime. These are safe to share because they are already keyed per channel and per tenant
	// inside: the message store records the channel name, and a metrics collector labels by tenant. Splitting them
	// would mean one database file per tenant, which is a different and much larger decision.
	messages *msgstore.Store
	fhir     *fhirserver.Store
	metrics  *metrics.Collector
	log      *slog.Logger

	mu       sync.Mutex
	runtimes map[tenant.ID]*Runtime
}

// NewTenantRuntimes prepares the registry.
func NewTenantRuntimes(
	repos *TenantRepos,
	messages *msgstore.Store,
	fhirStore *fhirserver.Store,
	collector *metrics.Collector,
	log *slog.Logger,
) *TenantRuntimes {
	return &TenantRuntimes{
		repos:    repos,
		messages: messages,
		fhir:     fhirStore,
		metrics:  collector,
		log:      log,
		runtimes: map[tenant.ID]*Runtime{},
	}
}

// For returns the runtime for a tenant, creating it if this is the first request.
func (tr *TenantRuntimes) For(id tenant.ID) (*Runtime, error) {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	if rt, ok := tr.runtimes[id]; ok {
		return rt, nil
	}

	repo, err := tr.repos.Repo(id)
	if err != nil {
		return nil, err
	}

	rt := NewRuntime(repo, tr.messages, tr.fhir)
	rt.Metrics = tr.metrics
	// Set before anything can start a channel, because a runtime that starts channels while claiming the wrong
	// tenant would label its metrics and traces for somebody else - and a label is how those get attributed.
	rt.Tenant = id

	tr.runtimes[id] = rt
	return rt, nil
}

// Started lists the tenants with a live runtime, sorted.
//
// Only those that have been touched. A tenant with no runtime has no channels running, which is different from having
// channels stopped, and the platform view says so rather than inventing rows.
func (tr *TenantRuntimes) Started() []tenant.ID {
	tr.mu.Lock()
	defer tr.mu.Unlock()

	out := make([]tenant.ID, 0, len(tr.runtimes))
	for id := range tr.runtimes {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// StopAll stops every tenant's channels.
//
// Called on shutdown. Sequential rather than concurrent: shutdown already has a deadline, and a hundred engines
// draining queues in parallel makes the slowest one slower rather than the whole thing faster.
func (tr *TenantRuntimes) StopAll(ctx context.Context) {
	tr.mu.Lock()
	runtimes := make([]*Runtime, 0, len(tr.runtimes))
	for _, rt := range tr.runtimes {
		runtimes = append(runtimes, rt)
	}
	tr.mu.Unlock()

	for _, rt := range runtimes {
		rt.StopAll(ctx)
	}
}

// runtimeFor returns the engine a request may act on, or writes the failure.
//
// The only way a handler may reach a runtime. Handlers used to read s.Runtime directly, which in multi-tenant
// operation meant every one of them acting on whichever engine happened to be on the server - so one tenant could
// start and stop another's channels, and the metrics endpoint returned everybody's numbers.
//
// A test fails if any handler reads s.Runtime while it has a session available, because that is the mistake that
// cannot be caught by review once there are sixty handlers.
func (s *Server) runtimeFor(w http.ResponseWriter, r *http.Request, sess *store.Session) (*Runtime, bool) {
	if s.Runtimes == nil {
		// Single-tenant operation. One runtime, and the session's tenant is not consulted because there is only one
		// place it could point.
		if s.Runtime == nil {
			s.fail(w, r, http.StatusServiceUnavailable,
				"this server can edit channels but not run them, because it was started without an engine")
			return nil, false
		}
		return s.Runtime, true
	}

	if sess == nil {
		// Refused rather than defaulted. A handler that lost its session must not silently act on the default
		// tenant's engine, which is what a fallback would do and what the guard exists to prevent.
		s.fail(w, r, http.StatusInternalServerError,
			"this request reached the engine without a session, which is a bug rather than a permission problem")
		return nil, false
	}

	rt, err := s.Runtimes.For(sess.TenantID)
	if err != nil {
		s.failErr(w, r, fmt.Errorf("preparing the engine for %s: %w", sess.TenantID, err))
		return nil, false
	}
	return rt, true
}

// optionalRuntime returns the tenant's engine, or nil, without writing anything.
//
// For handlers that work perfectly well without an engine and only want to enrich their answer if one is there - the
// mapping table view, for instance, which describes channel files and merely adds "and this one is running now".
//
// Separate from runtimeFor because that one writes a failure response, and a caller that ignored its second return
// value would emit an error body and then carry on to write a success body after it.
func (s *Server) optionalRuntime(sess *store.Session) *Runtime {
	if s.Runtimes == nil {
		return s.Runtime
	}
	if sess == nil {
		return nil
	}
	rt, err := s.Runtimes.For(sess.TenantID)
	if err != nil {
		return nil
	}
	return rt
}

// requireRuntimeFor is runtimeFor for handlers that only need to know an engine exists.
func (s *Server) requireRuntimeFor(w http.ResponseWriter, r *http.Request, sess *store.Session) bool {
	_, ok := s.runtimeFor(w, r, sess)
	return ok
}
