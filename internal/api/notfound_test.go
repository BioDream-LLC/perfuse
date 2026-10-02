package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// An API path nobody registered answers 404 in JSON, not the web application.
//
// Found by opening the users screen and reading what it said, which was "the server sent a response that could not be read". The cause was
// /api/passkeys being registered only when passkeys are configured; unregistered, it fell through to the handler that serves the web application
// and answered 200 with index.html. The browser asked for JSON and received an entire web page.
//
// The instance mattered because it was on a screen everybody visits. The class matters more: before this, every mistyped path, every route removed
// while some client still called it, and every probe received 200 and HTML. To anything that does not inspect the content type, that is success.

func TestAnUnknownAPIPathIs404AndJSON(t *testing.T) {
	h := newHarness(t)

	// A handler that serves the web application, as production does. Without one mounted the fallthrough cannot happen and the test would pass
	// while proving nothing - which is how this went unnoticed in the first place.
	serveTheApp(h)

	for _, path := range []string{
		"/api/nosuchthing",
		"/api/passkeys/nonsense",
		"/api/channels/labs/somethingnew",
		"/api/v2/messages",
	} {
		t.Run(path, func(t *testing.T) {
			rec := h.do("admin", http.MethodGet, path, nil)

			if rec.Code != http.StatusNotFound {
				t.Errorf("%s answered %d rather than 404", path, rec.Code)
			}

			// The content type is the part that made this invisible. A body of HTML with a 200 is indistinguishable from success to a client that
			// only checks the status.
			if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "json") {
				t.Errorf("%s answered with content type %q, so a client asking for JSON gets something else", path, ct)
			}

			if strings.Contains(rec.Body.String(), "<!doctype") || strings.Contains(rec.Body.String(), "<title>") {
				t.Errorf("%s answered with the web application instead of an error: %s", path, rec.Body.String()[:80])
			}

			// And it says which path, because "404" alone leaves somebody guessing whether they mistyped the path or lost their session.
			var body struct {
				Error string `json:"error"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("the 404 body is not JSON: %v", err)
			}
			if !strings.Contains(body.Error, path) {
				t.Errorf("the error does not name the path that was not found: %q", body.Error)
			}
		})
	}
}

func TestTheWebApplicationIsStillServedEverywhereElse(t *testing.T) {
	h := newHarness(t)

	serveTheApp(h)

	// The other half of the change. A catch-all that was too greedy would return 404 for the application itself, which is a blank browser window
	// and a much louder bug than the one being fixed - but worth a test rather than an assumption.
	for _, path := range []string{"/", "/channels", "/settings/users"} {
		rec := h.do("admin", http.MethodGet, path, nil)
		if rec.Code != http.StatusOK {
			t.Errorf("%s answered %d; the web application has to be served on paths the router does not know", path, rec.Code)
		}
	}
}

func TestListingPasskeysWorksWhenTheyAreNotConfigured(t *testing.T) {
	h := newHarness(t)

	// The specific case. The harness does not set a relying party identifier, which is the same state as an installation that has not configured
	// passkeys - and that is most of them.
	if h.server.PasskeyRPID != "" {
		t.Skip("this harness configures passkeys, so the unconfigured path cannot be tested here")
	}

	rec := h.do("admin", http.MethodGet, "/api/passkeys", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("listing passkeys answered %d with passkeys unconfigured; the users screen calls this on every visit", rec.Code)
	}

	var body struct {
		Passkeys   []map[string]any `json:"passkeys"`
		Configured bool             `json:"configured"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("not JSON: %v", err)
	}

	if body.Configured {
		t.Error("configured is true with no relying party identifier set, so the screen cannot tell an empty list from an unavailable feature")
	}

	// Empty rather than absent, per the house rule, so the browser does not have to distinguish null from none.
	if body.Passkeys == nil {
		t.Error("passkeys is null rather than an empty list")
	}
}

// serveTheApp mounts a stand-in for the web application and rebuilds the router.
//
// Rebuilding is the point. The routes are assembled once when the handler is built, so setting the static handler afterwards without rebuilding
// would leave nothing mounted - and the fallthrough being tested could not happen. The test would pass while proving nothing, which is the failure
// this whole file is about.
func serveTheApp(h *harness) {
	h.server.StaticHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte("<!doctype html><title>Perfuse</title>"))
	})
	h.handler = h.server.Handler()
}
