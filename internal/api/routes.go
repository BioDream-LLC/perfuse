package api

import (
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/biodream-llc/perfuse/internal/store"
)

// Knowing which routes exist, so a test can check all of them.
//
// The route-protection test used to walk a list written by hand. That made its central claim untrue:
// it said an endpoint registered without an authorisation check would fail the test immediately, but a
// new endpoint was invisible to it until somebody remembered to add a line - and somebody remembering
// is precisely what the test exists to replace. Adding the branding endpoints demonstrated it, because
// the test went on passing while covering none of them.
//
// So registration records itself. The mux is wrapped rather than every call site being edited: there
// are around ninety registrations, most of them authorisation decisions, and a mechanical edit across
// all of them is the last place to want a slip. Wrapping means the recording cannot disagree with what
// is served, because it is the same call.

// routeRecord is one registered route.
type routeRecord struct {
	Method string
	// Pattern is the path as registered, wildcards included.
	Pattern string
	// Role is the minimum role required, empty for a route served without authentication.
	Role string
}

// Authenticated reports whether the route is behind an authorisation check.
func (r routeRecord) Authenticated() bool { return r.Role != "" }

// authedHandler is what require returns.
//
// It carries the role it enforces so the recording mux can see it. Implements http.Handler, so every
// existing registration keeps compiling unchanged.
type authedHandler struct {
	http.Handler
	role store.Role
}

// routeRegistry collects routes during handler construction.
//
// Guarded because tests build servers in parallel, and appending to a shared slice from several
// goroutines is a race the detector will find at an inconvenient moment.
type routeRegistry struct {
	mu      sync.Mutex
	records []routeRecord
}

func (rr *routeRegistry) add(rec routeRecord) {
	if rr == nil {
		return
	}
	rr.mu.Lock()
	rr.records = append(rr.records, rec)
	rr.mu.Unlock()
}

// snapshot returns the recorded routes in a stable order.
//
// Sorted because it is compared and printed, and registration order is an implementation detail that
// would make a diff of the output meaningless.
func (rr *routeRegistry) snapshot() []routeRecord {
	rr.mu.Lock()
	out := append([]routeRecord(nil), rr.records...)
	rr.mu.Unlock()

	sort.Slice(out, func(i, j int) bool {
		if out[i].Pattern != out[j].Pattern {
			return out[i].Pattern < out[j].Pattern
		}
		return out[i].Method < out[j].Method
	})
	return out
}

// recordingMux registers routes and notes what protects each one.
//
// Deliberately not embedding *http.ServeMux: embedding would expose every other method and let a
// future registration bypass the recording by calling one of them. Two explicit methods mean anything
// that registers a route must go through here.
type recordingMux struct {
	mux      *http.ServeMux
	registry *routeRegistry
}

func newRecordingMux(reg *routeRegistry) *recordingMux {
	return &recordingMux{mux: http.NewServeMux(), registry: reg}
}

func (m *recordingMux) Handle(pattern string, h http.Handler) {
	role := ""
	if a, ok := h.(authedHandler); ok {
		role = string(a.role)
	}
	m.registry.add(recordOf(pattern, role))
	m.mux.Handle(pattern, h)
}

func (m *recordingMux) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	m.registry.add(recordOf(pattern, ""))
	m.mux.HandleFunc(pattern, h)
}

func (m *recordingMux) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	m.mux.ServeHTTP(w, r)
}

func recordOf(pattern, role string) routeRecord {
	method, path := "", pattern
	if i := strings.IndexByte(pattern, ' '); i > 0 {
		method, path = pattern[:i], strings.TrimSpace(pattern[i+1:])
	}
	return routeRecord{Method: method, Pattern: path, Role: role}
}

// Routes returns every route this server registers, with the role each requires.
//
// Exported so a test can assert over the whole surface rather than over a list of what somebody
// remembered to write down. Building the handler is cheap and its only side effect is the recording.
func (s *Server) Routes() []routeRecord {
	reg := &routeRegistry{}
	s.routes = reg
	_ = s.Handler()
	s.routes = nil
	return reg.snapshot()
}
