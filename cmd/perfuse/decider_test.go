package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/pas"
)

func deciderRequest() (*pas.Request, pas.Service) {
	req := &pas.Request{Bundle: map[string]any{"resourceType": "Bundle", "type": "collection", "id": "b1"}}
	return req, pas.Service{Sequence: "2"}
}

// deciderAgainst runs one call against a stand-in decision service that answers status and body, and returns what the
// service was sent along with the answer.
func deciderAgainst(t *testing.T, status int, body string) (fhirserver.PASAnswer, bool, *http.Request, map[string]any) {
	t.Helper()
	var seen *http.Request
	var sent map[string]any
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &sent)
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	defer ts.Close()
	t.Setenv("DECIDER_TOKEN", "s3cret")
	decide, err := newPASDecider(ts.URL+"/api/pas/decide", "DECIDER_TOKEN", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	req, svc := deciderRequest()
	a, ok := decide(fhirserver.WithPASRequestID(context.Background(), "resp-1"), req, svc)
	return a, ok, seen, sent
}

func TestDeciderSendsTheBundleForOneItem(t *testing.T) {
	_, _, seen, sent := deciderAgainst(t, 200, `{"decision":"approve","why":"Criteria met."}`)
	if seen.URL.Query().Get("item") != "2" {
		t.Errorf("item = %q, want 2", seen.URL.Query().Get("item"))
	}
	if seen.Header.Get("Authorization") != "Bearer s3cret" || seen.Header.Get("Content-Type") != "application/fhir+json" {
		t.Errorf("headers: %v", seen.Header)
	}
	if seen.Header.Get("X-PAS-Request-Id") != "resp-1" {
		t.Errorf("X-PAS-Request-Id = %q, want the response's id", seen.Header.Get("X-PAS-Request-Id"))
	}
	if sent["id"] != "b1" {
		t.Errorf("the service was sent %v, not the request Bundle", sent)
	}
}

func TestDeciderAnswers(t *testing.T) {
	cases := []struct {
		name, body string
		status     int
		want       string // decision
		decided    bool
		why        string // substring
	}{
		{"approve", `{"decision":"approve","why":"Criteria met."}`, 200, "approve", true, "Criteria met."},
		{"deny with a reason", `{"decision":"deny","why":"Repeat study within six months."}`, 200, "deny", true, "Repeat study"},
		{"deny without a reason is pended", `{"decision":"deny","why":"  "}`, 200, "pend", true, "reviewer"},
		{"pend says what is needed", `{"decision":"pend","requested":["red-flag findings","prior imaging date"]}`, 200, "pend", true,
			"Information needed: red-flag findings; prior imaging date."},
		{"unknown decision is pended", `{"decision":"approved"}`, 200, "pend", true, "reviewer"},
		{"pend asks for a questionnaire and documents", `{"decision":"pend","questionnaire":"https://payer.example/Questionnaire/q",` +
			`"attachments":["11506-3","not a code","18748-4"]}`, 200, "pend", true, "reviewer"},
		{"not JSON is pended", `<html>`, 200, "pend", true, "reviewer"},
		{"a server error is pended", `boom`, 500, "pend", true, "reviewer"},
		{"404 decides nothing", `{"error":"no policy"}`, 404, "", false, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			a, ok, _, _ := deciderAgainst(t, c.status, c.body)
			if ok != c.decided || a.Decision != c.want || !strings.Contains(a.Why, c.why) {
				t.Fatalf("got %+v ok=%v; want decision %q ok=%v why containing %q", a, ok, c.want, c.decided, c.why)
			}
			if strings.Contains(c.body, "attachments") && (a.Questionnaire != "https://payer.example/Questionnaire/q" ||
				strings.Join(a.Attachments, ",") != "11506-3,18748-4") {
				t.Fatalf("asks %q %q: want the questionnaire, and the two LOINC codes only", a.Questionnaire, a.Attachments)
			}
		})
	}
}

func TestDeciderUnreachableIsPended(t *testing.T) {
	ts := httptest.NewServer(http.NotFoundHandler())
	url := ts.URL
	ts.Close()
	decide, err := newPASDecider(url, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	req, svc := deciderRequest()
	if a, ok := decide(context.Background(), req, svc); !ok || a.Decision != "pend" {
		t.Fatalf("got %+v ok=%v, want pend", a, ok)
	}
}

func TestDeciderRefusesBadConfiguration(t *testing.T) {
	for _, u := range []string{"ftp://x", "not a url", "http://x/decide?item=1", ""} {
		if _, err := newPASDecider(u, "", nil); err == nil {
			t.Errorf("%q accepted", u)
		}
	}
	t.Setenv("EMPTY_TOKEN", "")
	if _, err := newPASDecider("https://x/decide", "EMPTY_TOKEN", nil); err == nil {
		t.Error("an empty token variable was accepted")
	}
}

func TestChainLeavesTheRulesCoveragePolicyAlone(t *testing.T) {
	asked := 0
	decider := func(context.Context, *pas.Request, pas.Service) (fhirserver.PASAnswer, bool) {
		asked++
		return fhirserver.PASAnswer{Decision: "pend", Why: "Information needed: x.", Attachments: []string{"11506-3", "18748-4"}}, true
	}
	rules := func(_ context.Context, _ *pas.Request, svc pas.Service) fhirserver.PASAnswer {
		switch svc.Sequence {
		case "1":
			return fhirserver.PASAnswer{Decision: "approve", Why: "No authorization needed."}
		case "2":
			return fhirserver.PASAnswer{Decision: "deny", Why: "Not covered."}
		}
		return fhirserver.PASAnswer{Decision: "pend", Questionnaire: "https://payer.example/Questionnaire/mri", Attachments: []string{"18748-4"}}
	}
	decide := chainPASDecide(rules, decider)
	req, _ := deciderRequest()
	if a := decide(context.Background(), req, pas.Service{Sequence: "1"}); a.Decision != "approve" {
		t.Errorf("rule approval overruled: %+v", a)
	}
	if a := decide(context.Background(), req, pas.Service{Sequence: "2"}); a.Decision != "deny" {
		t.Errorf("rule denial overruled: %+v", a)
	}
	if asked != 0 {
		t.Fatalf("the decision service was asked %d times about services the rules decide", asked)
	}
	a := decide(context.Background(), req, pas.Service{Sequence: "3"})
	if asked != 1 || a.Decision != "pend" || a.Questionnaire == "" || strings.Join(a.Attachments, ",") != "18748-4,11506-3" {
		t.Fatalf("got %+v after %d asks; want the service's pend with the rule's questionnaire, and both lists of attachments", a, asked)
	}

	// With no rules file, everything goes to the service; a 404 leaves it pended.
	none := chainPASDecide(nil, func(context.Context, *pas.Request, pas.Service) (fhirserver.PASAnswer, bool) {
		return fhirserver.PASAnswer{}, false
	})
	if a := none(context.Background(), req, pas.Service{Sequence: "9"}); a.Decision != "pend" {
		t.Fatalf("no rules and no decision: %+v, want pend", a)
	}
}

func TestADeciderOverPlainHTTPMustBeOnThisMachine(t *testing.T) {
	if _, err := newPASDecider("http://vaglio.example/api/pas/decide", "", nil); err == nil {
		t.Error("plain http to another host was accepted")
	}
	for _, ok := range []string{"http://127.0.0.1:9000/d", "http://localhost/d", "http://[::1]:9000/d", "https://vaglio.example/d"} {
		if _, err := newPASDecider(ok, "", nil); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
}
