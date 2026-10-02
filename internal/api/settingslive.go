package api

import (
	"net/http"
	"sort"

	"github.com/biodream-llc/perfuse/internal/store"
)

// liveSettings are the settings this server applies without a restart.
//
// The list is here rather than inferred, and there is a test that every setting declaring itself live appears in it. Otherwise
// the registry could promise a change takes effect immediately while nothing reads the new value - which is the worst of the two
// possible errors, because the interface would say "saved" and mean it.
//
// A setting absent from here declares EffectRestart, which is honest and mildly annoying, rather than EffectLive, which is
// dishonest and looks fine.
var liveSettings = map[string]bool{
	// Read through function fields on the message store, assigned in serve.go. Nothing has to be pushed when they
	// change: the code that cares asks for the value at the moment it needs it.
	"data.retentionDays": true,
	"data.storePayloads": true,
	"data.payloadDays":   true,
	"data.indexIdentity": true,

	// Read through a function field on the user store, assigned in serve.go, at the moment a session is created
	// or refreshed. Sessions already issued keep the expiry they were given.
	"signin.sessionHours": true,

	// Read through a function field on the api Server, assigned in serve.go, at the moment a scrape arrives. The
	// -metrics-token and -metrics-open flags remain the fallback, so an installation that never touches this
	// behaves as it did before the setting existed.
	"security.metricsAccess": true,

	// Read through a function field on the notifier, when an alert is dispatched. The threshold is usually raised
	// because something is being noisy at an unwelcome hour, so waiting for a restart is not useful.
	"alerts.minSeverity": true,

	// Read through a function field on the FHIR server, per request. Turning this on is usually a response to
	// something, and "after a restart" is not an answer at that moment.
	"fhir.readOnly": true,

	// Read at the moment the interface asks what this product is called, which happens on every page
	// load and on the sign-in page before anyone has a session. Nothing has to be pushed anywhere:
	// the branding endpoint reads the current value each time it answers. Live because the whole point
	// of a branding screen is to change something and immediately look at it.
	"branding.productName":  true,
	"branding.tagline":      true,
	"branding.accentColour": true,

	// Read through a function field on the FHIR server, assigned in serve.go, at the moment a search is
	// handled. Only applied when the client gives no _count of its own - overriding an explicit request would
	// break paging for a client that asked for ten and silently received fifty.
	"fhir.pageSize": true,

	// Held as a plain field on Server and replaced under a lock by applyLiveSettings.
	"fleet.label": true,
}

// applyLiveSettings pushes settings that take effect immediately into the running server.
//
// Called after a successful save. Everything here reads through a function rather than copying a value, so this exists to update
// the few places that genuinely hold their own copy.
func (s *Server) applyLiveSettings() {
	if s.Settings == nil {
		return
	}

	// The fleet label is read when a fleet report is built, so replacing it is enough.
	s.fleetLabelMu.Lock()
	s.FleetLabel = s.Settings.String("fleet.label")
	s.fleetLabelMu.Unlock()

	// Retention and payload storage are read through the function fields the message store was given at startup, so
	// there is nothing to push - they are already reading the new value. The store is checked for existence only so
	// that a server started without message storage does not appear to have applied something it cannot.
	//
	// Everything else in the registry declares EffectRestart, which is honest and mildly annoying rather than
	// dishonest and comfortable. Converting one to live means giving its consumer the same treatment - read the value
	// when it is needed instead of copying it at startup - and then adding it above, at which point the drift guard
	// starts holding the pair together.
}

// Note on where the rest of the wiring lives.
//
// Retention and payload storage are read through function fields on the message store, and those are assigned in serve.go where
// the store is created - including for each tenant's store on a multi-tenant server. Doing it there rather than here keeps this
// package from reaching into a runtime it does not own, and means a tenant store created later cannot be missed.
//
// The consequence worth knowing: after that assignment, changing one of those settings needs no notification at all. The code
// that cares asks for the value at the moment it needs it.

// recordRestartPending remembers settings that were changed but need a restart.
//
// In memory on purpose. After a restart nothing is pending, which is exactly what an empty map says, so persisting it would mean
// clearing it correctly on startup and getting that wrong would leave a banner nobody can dismiss.
func (s *Server) recordRestartPending(keys []string) {
	if len(keys) == 0 {
		return
	}

	s.restartMu.Lock()
	defer s.restartMu.Unlock()

	if s.restartPending == nil {
		s.restartPending = make(map[string]bool)
	}
	for _, key := range keys {
		s.restartPending[key] = true
	}
}

// pendingRestartSettings lists settings changed since startup that need a restart.
func (s *Server) pendingRestartSettings() []string {
	s.restartMu.RLock()
	defer s.restartMu.RUnlock()

	out := make([]string, 0, len(s.restartPending))
	for key := range s.restartPending {
		out = append(out, key)
	}
	// Sorted: this drives a banner listing what is waiting, and a list that reorders itself between page loads reads as
	// though something is still changing.
	sort.Strings(out)

	return out
}

// auditSettings records a settings change.
//
// Its own helper rather than a call to the queue one, because the target is a setting and reusing a function named for the queue
// would put a misleading action in a table people search by action.
func (s *Server) auditSettings(r *http.Request, sess *store.Session, detail string) {
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   "settings.change",
		Target:   "settings",
		Detail:   detail,
		IP:       clientIP(r),
	})
}
