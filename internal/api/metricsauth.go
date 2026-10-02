package api

import (
	"crypto/subtle"
	"net/http"

	"github.com/biodream-llc/perfuse/internal/store"
)

// The scrape endpoint used to take no credentials at all.
//
// That is the conventional arrangement and it is defensible for one operator scraping their own installation: the numbers
// describe traffic they already know about. It stops being defensible on a shared server, because a metric label names a
// channel and a channel name usually names the system at the other end - so an unauthenticated scrape tells one customer
// about another's interfaces, and tells anybody who can reach the port about all of them.
//
// Requiring a credential rather than stripping the labels, because labels naming channels are the entire value of the
// endpoint. Metrics with the interesting parts removed would be monitoring nobody looks at.

// Metrics access modes, matching the security.metricsAccess setting.
const (
	// metricsAccessToken accepts the dedicated scrape token, and any session or API token.
	metricsAccessToken = "token"

	// metricsAccessSession accepts only a session or API token, so no unattended scraping.
	metricsAccessSession = "session"

	// metricsAccessOpen accepts anybody who can reach the port.
	metricsAccessOpen = "open"
)

// metricsAccess reports which mode is configured.
//
// Falls back to the flags this predates, so a server started with -metrics-open or -metrics-token behaves as it did
// before the setting existed. Read at request time rather than copied, so changing it in the interface takes effect on
// the next scrape.
//
// An unrecognised value is treated as the most restrictive mode rather than the most permissive. The registry refuses an
// unknown choice on save, so this is unreachable through the interface - but a hand-edited settings file is not, and the
// failure that matters is a typo silently opening the endpoint.
func (s *Server) metricsAccess() string {
	if s.MetricsAccessFn != nil {
		switch mode := s.MetricsAccessFn(); mode {
		case metricsAccessToken, metricsAccessSession, metricsAccessOpen:
			return mode
		case "":
			// Nothing configured, so fall through to the flags.
		default:
			return metricsAccessSession
		}
	}

	if s.MetricsOpen {
		return metricsAccessOpen
	}

	return metricsAccessToken
}

// metricsCaller reports who may scrape, and as whom.
//
// Returns a session when the caller presented a real credential, so the numbers can be filtered to that caller's tenant.
// A nil session with allowed=true means the endpoint is deliberately open and there is nobody to filter to.
func (s *Server) metricsCaller(r *http.Request) (sess *store.Session, allowed bool) {
	mode := s.metricsAccess()
	presented, present := bearerToken(r)

	// A dedicated scrape token first, because it is the only thing Prometheus can actually send: a scraper cannot
	// follow a login redirect or hold a cookie. The configuration field for it is bearer_token_file.
	//
	// Compared in constant time. A scrape endpoint is polled continuously, which is the ideal condition for measuring
	// a comparison that stops at the first wrong byte.
	//
	// Not accepted in session mode, which is the whole meaning of that mode: metrics are for a person to look at
	// and not for an unattended scraper, so the credential a scraper would use has to stop working.
	if mode == metricsAccessToken && s.MetricsToken != "" && present {
		if subtle.ConstantTimeCompare([]byte(presented), []byte(s.MetricsToken)) == 1 {
			// No session, so no filtering: this token is the operator's own and sees the whole installation. That
			// is the point of it on a single-tenant server and the reason it should not be handed to a customer.
			return nil, true
		}
	}

	// Otherwise any credential that works elsewhere in the API, so an operator already signed in can just look. Viewer
	// is enough, because scraping is reading.
	if present {
		if session, err := s.Store.LookupAPIToken(r.Context(), presented); err == nil && session != nil {
			return session, true
		}
	}
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if session, err := s.Store.Lookup(r.Context(), cookie.Value); err == nil && session != nil {
			return session, true
		}
	}

	// Explicitly opened, for a deployment where the port is genuinely private. Named so it appears in the command line
	// of any server running this way, because "we thought it was firewalled" is how this becomes a finding.
	if mode == metricsAccessOpen {
		return nil, true
	}

	return nil, false
}
