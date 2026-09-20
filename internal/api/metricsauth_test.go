package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/metrics"
	"github.com/biodream-llc/perfuse/internal/store"
)

// scrape asks the exposition endpoint with a bearer credential, which is what a scraper sends.
func scrape(h *harness, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// scrapeAsSession asks with a session cookie, which is what a signed-in person has.
//
// Separate from scrape because the two credentials travel differently and the endpoint has to accept both: a scraper
// cannot hold a cookie, and a person in a browser does not have a scrape token.
func scrapeAsSession(t *testing.T, h *harness, tenantID string, role store.Role) *httptest.ResponseRecorder {
	t.Helper()

	// Borrowing doAs to establish the session, then reusing its cookie against a path outside the API.
	username := "u-" + tenantID + "-" + string(role)
	if _, ok := h.tokens[username]; !ok {
		h.doAs(t, tenantID, role, http.MethodGet, "/api/channels", nil)
	}
	cookieValue, ok := h.tokens[username]
	if !ok {
		t.Fatalf("no session was established for %s", username)
	}

	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: cookieValue})

	rec := httptest.NewRecorder()
	h.handler.ServeHTTP(rec, req)
	return rec
}

// withMetrics gives the harness a real collector.
//
// The message harness has none, and without one every test here would get 503 and pass the anonymous case while proving
// nothing about the authenticated ones. A separate helper rather than changing newMessageHarness, because the tests that
// use that harness are about stored messages and do not need a collector.
func withMetrics(t *testing.T, h *harness) *harness {
	t.Helper()

	if h.server.Runtime == nil {
		t.Fatal("this harness has no runtime, so there is nowhere to put a collector")
	}
	h.server.Runtime.Metrics = metrics.New(metrics.Options{})

	return h
}

// TestTheScrapeEndpointRefusesAnAnonymousCaller is the fix itself.
//
// This endpoint served anybody who could reach the port. That is the convention, and it was defensible when Perfuse ran one
// operator's own channels - the numbers described traffic they already knew about. It stopped being defensible when the
// same process began serving several customers, because a metric label names a channel and a channel name usually names the
// system at the other end.
func TestTheScrapeEndpointRefusesAnAnonymousCaller(t *testing.T) {
	h := withMetrics(t, newMessageHarness(t))

	rec := scrape(h, "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("an anonymous caller got %d rather than 401: %s", rec.Code, rec.Body.String())
	}

	// A scraper reads the status code, but the person wondering why their dashboard went empty reads the body.
	if !strings.Contains(rec.Body.String(), "metrics-token") {
		t.Errorf("the refusal does not say how to fix it:\n%s", rec.Body.String())
	}

	// And a challenge header, so a client knows what kind of credential to offer.
	if got := rec.Header().Get("WWW-Authenticate"); !strings.Contains(got, "Bearer") {
		t.Errorf("no bearer challenge was issued: %q", got)
	}
}

// TestTheScrapeTokenWorks covers the credential Prometheus can actually send.
//
// A scraper cannot follow a login redirect or hold a cookie, so without this the endpoint would be unusable by the only
// thing it exists for.
func TestTheScrapeTokenWorks(t *testing.T) {
	h := withMetrics(t, newMessageHarness(t))
	h.server.MetricsToken = "scrape-me"

	rec := scrape(h, "Bearer scrape-me")
	if rec.Code != http.StatusOK {
		t.Fatalf("the scrape token was refused: %d %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "# HELP") {
		t.Errorf("no exposition was written:\n%s", rec.Body.String())
	}

	// Case in the scheme, because "bearer" is written every way in the wild.
	if rec := scrape(h, "bearer scrape-me"); rec.Code != http.StatusOK {
		t.Errorf("a lowercase scheme was refused: %d", rec.Code)
	}

	// A wrong value must not work, which is the half of this that matters.
	if rec := scrape(h, "Bearer scrape-mf"); rec.Code != http.StatusUnauthorized {
		t.Errorf("a wrong scrape token was accepted: %d", rec.Code)
	}

	// Nor a prefix of the right one, which is what a comparison stopping early would admit.
	if rec := scrape(h, "Bearer scrape"); rec.Code != http.StatusUnauthorized {
		t.Errorf("a prefix of the scrape token was accepted: %d", rec.Code)
	}
}

// TestMetricsOpenStillServesAnybody covers the deliberate exemption.
//
// Plenty of installations really do bind this port to a private interface, and breaking them would push people towards
// not upgrading. The flag is named rather than implied so it appears in the command line of any server running this way.
func TestMetricsOpenStillServesAnybody(t *testing.T) {
	h := withMetrics(t, newMessageHarness(t))
	h.server.MetricsOpen = true

	if rec := scrape(h, ""); rec.Code != http.StatusOK {
		t.Errorf("metrics-open did not open the endpoint: %d %s", rec.Code, rec.Body.String())
	}
}

// TestASignedInOperatorCanReadMetrics covers the case with no flags set at all.
//
// Somebody who has already signed in should not need a second credential to look at numbers they can see on the dashboard
// anyway. Without this, an installation that sets neither flag would have an endpoint nobody can read, and the fix for
// that would be -metrics-open.
func TestASignedInOperatorCanReadMetrics(t *testing.T) {
	h := withMetrics(t, newMessageHarness(t))

	rec := scrapeAsSession(t, h, string(store.DefaultTenant), store.RoleViewer)
	if rec.Code != http.StatusOK {
		t.Errorf("a signed-in viewer could not read metrics: %d %s", rec.Code, rec.Body.String())
	}
}

// TestOneTenantsOperatorDoesNotScrapeAnothersChannels is the disclosure this was opened to fix.
//
// The endpoint is reachable by a tenant's own operator, so closing it to anonymous callers is not sufficient on its own:
// the exposition still had to stop naming other customers' channels.
func TestOneTenantsOperatorDoesNotScrapeAnothersChannels(t *testing.T) {
	h := withMetrics(t, newMessageHarness(t))

	// Two tenants' traffic, recorded through the metrics collector so it reaches the exposition.
	h.server.Runtime.Metrics.Add(metrics.MessagesReceived, 1, "clinic-a", "a-cardiology-feed")
	h.server.Runtime.Metrics.Add(metrics.MessagesReceived, 1, "clinic-b", "b-radiology-feed")

	rec := scrapeAsSession(t, h, "clinic-b", store.RoleAdmin)
	if rec.Code != http.StatusOK {
		t.Fatalf("clinic-b could not scrape at all: %d %s", rec.Code, rec.Body.String())
	}

	body := rec.Body.String()
	if strings.Contains(body, "a-cardiology-feed") {
		t.Errorf("clinic-b scraped the name of clinic-a's channel:\n%s", body)
	}
	if !strings.Contains(body, "b-radiology-feed") {
		t.Errorf("clinic-b cannot see its own channel, so the filter is too strict:\n%s", body)
	}
}

// TestTheScrapeTokenSeesEverything covers the operator's own view.
//
// An operator running a multi-tenant server needs one scrape that covers the whole installation, otherwise they would have
// to configure Prometheus per customer and would notice a sick tenant last. This is also why that token must not be handed
// to a customer, and the flag help says so.
func TestTheScrapeTokenSeesEverything(t *testing.T) {
	h := withMetrics(t, newMessageHarness(t))
	h.server.MetricsToken = "operator-scrape"

	h.server.Runtime.Metrics.Add(metrics.MessagesReceived, 1, "clinic-a", "a-cardiology-feed")
	h.server.Runtime.Metrics.Add(metrics.MessagesReceived, 1, "clinic-b", "b-radiology-feed")

	rec := scrape(h, "Bearer operator-scrape")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}

	for _, want := range []string{"a-cardiology-feed", "b-radiology-feed"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("the operator's own scrape is missing %s, so a sick tenant would go unnoticed", want)
		}
	}
}

// TestTheMetricsAccessModeIsHonoured covers the live security.metricsAccess setting.
//
// Each mode gets its own case because this is an access control, and the failure that matters is a mode that reads as
// tighter than it behaves. Session mode in particular has to stop the scrape token working - that is its entire meaning,
// and it is the one a reasonable implementation gets wrong by only handling the open case.
func TestTheMetricsAccessModeIsHonoured(t *testing.T) {
	t.Run("token mode accepts the scrape token", func(t *testing.T) {
		h := withMetrics(t, newMessageHarness(t))
		h.server.MetricsToken = "scrape-me"
		h.server.MetricsAccessFn = func() string { return "token" }

		if rec := scrape(h, "Bearer scrape-me"); rec.Code != http.StatusOK {
			t.Errorf("the scrape token was refused in token mode: %d", rec.Code)
		}
	})

	t.Run("session mode refuses the scrape token", func(t *testing.T) {
		h := withMetrics(t, newMessageHarness(t))
		h.server.MetricsToken = "scrape-me"
		h.server.MetricsAccessFn = func() string { return "session" }

		rec := scrape(h, "Bearer scrape-me")
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("the scrape token still worked in session mode: %d %s - session mode means no "+
				"unattended scraping, so the credential a scraper uses has to stop working",
				rec.Code, rec.Body.String())
		}
	})

	t.Run("session mode still refuses an anonymous caller", func(t *testing.T) {
		h := withMetrics(t, newMessageHarness(t))
		h.server.MetricsAccessFn = func() string { return "session" }

		if rec := scrape(h, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("an anonymous caller got %d in session mode", rec.Code)
		}
	})

	t.Run("open mode serves anybody", func(t *testing.T) {
		h := withMetrics(t, newMessageHarness(t))
		h.server.MetricsAccessFn = func() string { return "open" }

		if rec := scrape(h, ""); rec.Code != http.StatusOK {
			t.Errorf("an anonymous caller was refused in open mode: %d %s", rec.Code, rec.Body.String())
		}
	})

	t.Run("open mode does not survive the setting being changed back", func(t *testing.T) {
		// The point of reading per request rather than at startup: tightening it has to take effect at once.
		h := withMetrics(t, newMessageHarness(t))
		mode := "open"
		h.server.MetricsAccessFn = func() string { return mode }

		if rec := scrape(h, ""); rec.Code != http.StatusOK {
			t.Fatalf("open mode refused an anonymous caller: %d", rec.Code)
		}

		mode = "session"
		if rec := scrape(h, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("the endpoint stayed open after the mode was tightened: %d - the mode is being read "+
				"once rather than per request", rec.Code)
		}
	})

	t.Run("an unrecognised mode is treated as the most restrictive", func(t *testing.T) {
		// Unreachable through the interface, because the registry refuses an unknown choice on save. A
		// hand-edited settings file is not, and a typo must not open the endpoint.
		h := withMetrics(t, newMessageHarness(t))
		h.server.MetricsToken = "scrape-me"
		h.server.MetricsAccessFn = func() string { return "opne" }

		if rec := scrape(h, ""); rec.Code != http.StatusUnauthorized {
			t.Errorf("a typo opened the endpoint: %d", rec.Code)
		}
		if rec := scrape(h, "Bearer scrape-me"); rec.Code != http.StatusUnauthorized {
			t.Errorf("a typo left the scrape token working: %d", rec.Code)
		}
	})

	t.Run("an empty mode falls back to the flags", func(t *testing.T) {
		// A server upgraded to a build with this setting, whose settings file predates it, must keep behaving
		// as its command line says.
		h := withMetrics(t, newMessageHarness(t))
		h.server.MetricsOpen = true
		h.server.MetricsAccessFn = func() string { return "" }

		if rec := scrape(h, ""); rec.Code != http.StatusOK {
			t.Errorf("-metrics-open stopped working when the setting was unset: %d", rec.Code)
		}
	})
}
