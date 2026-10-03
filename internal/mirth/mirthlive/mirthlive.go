// Package mirthlive finds the running Mirth-family engines that the interoperability tests talk to.
//
// Mirth Connect stopped being open source at 4.6 (March 2025). The sites leaving it go to one of three places: Mirth 4.5.2, the last
// open release, which they keep running; the Open Integration Engine, the Eclipse-hosted fork; or BridgeLink, Innovar's fork, which
// numbers its releases by year (26.9.0). A channel Perfuse exports has to load in all of them, and a channel any of them exports has to
// import here, so every live test runs once per engine that answers.
//
// The engines are found by asking, not configured: ./scripts/interop-up.sh starts them on fixed ports, and an engine that is not running
// is reported as skipped rather than silently passing. PERFUSE_MIRTH_ENGINES overrides the list, as name=url pairs separated by commas.
//
// Only tests import this. It lives outside a _test.go file because two packages share it.
package mirthlive

import (
	"crypto/tls"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Engine is one running server.
type Engine struct {
	// Name is the product, from the port it was started on: "mirth", "oie", "bridgelink".
	Name string
	// Base is the HTTPS root, without a trailing slash.
	Base string
	// Version is what the server reports, e.g. "4.5.2" or "26.9.0".
	Version string
}

// Label is how a subtest and a report name the engine.
func (e Engine) Label() string { return e.Name + "-" + e.Version }

// Known is where ./scripts/interop-up.sh puts each engine.
var Known = []Engine{
	{Name: "mirth", Base: "https://127.0.0.1:8443"},
	{Name: "oie", Base: "https://127.0.0.1:8444"},
	{Name: "bridgelink", Base: "https://127.0.0.1:8445"},
	{Name: "oie", Base: "https://127.0.0.1:8446"},
}

// InvalidChannelSentence is what every one of these engines puts in a channel's description when it could not assemble it. They do
// not refuse such a channel: they store it, without its destinations, and answer 200.
const InvalidChannelSentence = "This channel is invalid"

func client() *http.Client {
	return &http.Client{
		Timeout: 30 * time.Second,
		// Each container makes its own self-signed certificate at first start.
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}, //nolint:gosec // test containers
	}
}

// Engines returns every engine that answers, and skips the test when none does.
func Engines(t *testing.T) []Engine {
	t.Helper()

	candidates := Known
	if v := strings.TrimSpace(os.Getenv("PERFUSE_MIRTH_ENGINES")); v != "" {
		candidates = nil
		for _, pair := range strings.Split(v, ",") {
			name, base, ok := strings.Cut(strings.TrimSpace(pair), "=")
			if ok {
				candidates = append(candidates, Engine{Name: name, Base: strings.TrimRight(base, "/")})
			}
		}
	}

	var up []Engine
	for _, e := range candidates {
		code, version, err := e.request(http.MethodGet, "/api/server/version", nil, "text/plain")
		if err != nil || code != http.StatusOK {
			t.Logf("no engine on %s; start the set with ./scripts/interop-up.sh", e.Base)
			continue
		}
		e.Version = strings.TrimSpace(version)
		up = append(up, e)
	}
	if len(up) == 0 {
		t.Skip("no Mirth-family engine is running. Start them with ./scripts/interop-up.sh")
	}

	return up
}

// Do makes an authenticated request and fails the test if the engine cannot be reached.
func (e Engine) Do(t *testing.T, method, path string, body io.Reader) (int, string) {
	t.Helper()

	// No Accept header: the engines answer a channel GET in XML by default, and a POST's boolean answer is not offered as XML, so
	// asking for XML there is answered 406.
	code, out, err := e.request(method, path, body, "")
	if err != nil {
		t.Fatalf("%s: %v", e.Label(), err)
	}

	return code, out
}

func (e Engine) request(method, path string, body io.Reader, accept string) (int, string, error) {
	req, err := http.NewRequest(method, e.Base+path, body)
	if err != nil {
		return 0, "", err
	}
	// The default account every one of these images starts with.
	req.SetBasicAuth("admin", "admin")
	// Required on writes, for the reason Perfuse requires X-Perfuse-Request: a form on another site cannot set it.
	req.Header.Set("X-Requested-With", "perfuse-test")
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/xml")
	}

	res, err := client().Do(req)
	if err != nil {
		return 0, "", err
	}
	defer func() { _ = res.Body.Close() }()

	out, err := io.ReadAll(res.Body)

	return res.StatusCode, string(out), err
}

// ImportAndReadBack replaces any channel with this id, imports the document, and returns what the engine stored - which is not
// necessarily what was sent.
func (e Engine) ImportAndReadBack(t *testing.T, id string, doc []byte) string {
	t.Helper()

	// Any earlier copy goes first, or the POST is a no-op against an existing id and the test passes on stale data.
	e.Do(t, http.MethodDelete, "/api/channels/"+id, nil)

	code, body := e.Do(t, http.MethodPost, "/api/channels", strings.NewReader(string(doc)))
	if code != http.StatusOK && code != http.StatusNoContent && code != http.StatusCreated {
		t.Fatalf("%s refused the import outright with %d:\n%s", e.Label(), code, Truncate(body, 600))
	}

	code, stored := e.Do(t, http.MethodGet, "/api/channels/"+id, nil)
	if code != http.StatusOK || strings.TrimSpace(stored) == "" {
		t.Fatalf("%s accepted the import and then had no channel %s (GET %d)", e.Label(), id, code)
	}

	t.Cleanup(func() { e.Do(t, http.MethodDelete, "/api/channels/"+id, nil) })

	return stored
}

// Truncate shortens a document for a failure message.
func Truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n] + "\n... truncated"
}
