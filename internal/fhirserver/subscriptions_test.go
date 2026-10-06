package fhirserver

import (
	"context"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// receiver is a subscriber's endpoint that records what it was sent and answers with a scripted status.
type receiver struct {
	mu      sync.Mutex
	bodies  []map[string]any
	headers []http.Header
	status  []int // consumed one per request; 200 once exhausted
	srv     *httptest.Server
}

func newReceiver(t *testing.T, statuses ...int) *receiver {
	r := &receiver{status: statuses}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		r.mu.Lock()
		r.bodies = append(r.bodies, body)
		r.headers = append(r.headers, req.Header.Clone())
		code := 200
		if len(r.status) > 0 {
			code, r.status = r.status[0], r.status[1:]
		}
		r.mu.Unlock()
		if code == http.StatusFound {
			// Back to this same receiver, which answers 200 next time. Following the redirect would therefore succeed,
			// so the test can only pass if the redirect is refused - a redirect to an unreachable address would fail
			// either way and prove nothing.
			w.Header().Set("Location", "http://"+req.Host+"/redirected")
		}
		w.WriteHeader(code)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *receiver) received() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]map[string]any(nil), r.bodies...)
}

type subFixture struct {
	db    *sql.DB
	store *Store
	subs  *Subscriptions
	h     http.Handler
	clock time.Time
}

func newSubFixture(t *testing.T, allowHTTP bool) *subFixture {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	return reopenSubFixture(t, db, allowHTTP)
}

// reopenSubFixture builds a new store and dispatcher over an existing database, which is what a restart is.
func reopenSubFixture(t *testing.T, db *sql.DB, allowHTTP bool) *subFixture {
	t.Helper()
	store, err := NewStore(db, fhir.R4)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	subs, err := store.EnableSubscriptions(context.Background(), SubscriptionOptions{
		AllowHTTP: allowHTTP, BaseURL: "http://example.test/fhir", Log: log,
	})
	if err != nil {
		t.Fatal(err)
	}
	f := &subFixture{db: db, store: store, subs: subs, clock: time.Now()}
	subs.now = func() time.Time { return f.clock }
	srv := NewServer(store, "http://example.test/fhir", log)
	srv.Auth = OpenAuth{}
	f.h = srv.Handler()
	return f
}

// deliver runs the dispatcher until nothing more is due, advancing the clock past any backoff each round.
func (f *subFixture) deliver(rounds int) {
	for i := 0; i < rounds; i++ {
		f.subs.DeliverDue(context.Background())
		f.clock = f.clock.Add(10 * time.Minute)
	}
}

func subscriptionJSON(endpoint, filter, payload string, headers ...string) string {
	sub := map[string]any{
		"resourceType": "Subscription",
		"status":       "requested",
		"reason":       "test",
		"criteria":     TopicEncounter,
		"channel": map[string]any{
			"type":     "rest-hook",
			"endpoint": endpoint,
			"payload":  "application/fhir+json",
			"_payload": map[string]any{"extension": []any{map[string]any{"url": extPayloadContent, "valueCode": payload}}},
			"header":   headers,
		},
	}
	if filter != "" {
		sub["_criteria"] = map[string]any{"extension": []any{map[string]any{"url": extFilterCriteria, "valueString": filter}}}
	}
	raw, _ := json.Marshal(sub)
	return string(raw)
}

func encounterJSON(id, patient, status string) string {
	return `{"resourceType":"Encounter","id":"` + id + `","status":"` + status + `",
		"class":{"system":"http://terminology.hl7.org/CodeSystem/v3-ActCode","code":"IMP"},
		"subject":{"reference":"Patient/` + patient + `"}}`
}

func (f *subFixture) put(t *testing.T, path, body string) map[string]any {
	t.Helper()
	rec := do(t, f.h, "PUT", path, body)
	if rec.Code != 200 && rec.Code != 201 {
		t.Fatalf("PUT %s = %d: %s", path, rec.Code, rec.Body.String())
	}
	return tree(t, rec)
}

func (f *subFixture) status(t *testing.T, id string) string {
	t.Helper()
	return tree(t, do(t, f.h, "GET", "/Subscription/"+id, nil))["status"].(string)
}

// statusParams returns the SubscriptionStatus parameters of a notification, by name.
func statusParams(t *testing.T, bundle map[string]any) map[string]any {
	t.Helper()
	entries := bundle["entry"].([]any)
	params := entries[0].(map[string]any)["resource"].(map[string]any)["parameter"].([]any)
	out := map[string]any{}
	for _, p := range params {
		pm := p.(map[string]any)
		name := pm["name"].(string)
		for k, v := range pm {
			if k != "name" {
				out[name] = v
			}
		}
	}
	return out
}

func eventNumber(t *testing.T, bundle map[string]any) string {
	t.Helper()
	parts := statusParams(t, bundle)["notification-event"].([]any)
	for _, p := range parts {
		pm := p.(map[string]any)
		if pm["name"] == "event-number" {
			return pm["valueString"].(string)
		}
	}
	t.Fatal("no event-number")
	return ""
}

func TestHandshakeActivatesTheSubscription(t *testing.T) {
	f := newSubFixture(t, true)
	rx := newReceiver(t)
	f.put(t, "/Subscription/s1", subscriptionJSON(rx.srv.URL, "Encounter?patient=Patient/p1", "id-only"))

	if got := f.status(t, "s1"); got != "requested" {
		t.Fatalf("status before handshake = %q, want requested", got)
	}
	f.deliver(1)

	got := rx.received()
	if len(got) != 1 || statusParams(t, got[0])["type"] != "handshake" {
		t.Fatalf("expected one handshake, got %v", got)
	}
	if s := f.status(t, "s1"); s != "active" {
		t.Errorf("status after handshake = %q, want active", s)
	}
}

// A client cannot declare its own subscription active; only a handshake the endpoint answered does that.
func TestClientCannotSetStatusActive(t *testing.T) {
	f := newSubFixture(t, true)
	rx := newReceiver(t)
	body := strings.Replace(subscriptionJSON(rx.srv.URL, "", "id-only"), `"status":"requested"`, `"status":"active"`, 1)
	stored := f.put(t, "/Subscription/s1", body)
	if stored["status"] != "requested" {
		t.Errorf("a client-supplied status of active was stored as %q", stored["status"])
	}
}

func TestMatchingEncounterIsNotifiedAndOthersAreNot(t *testing.T) {
	f := newSubFixture(t, true)
	rx := newReceiver(t)
	f.put(t, "/Subscription/s1", subscriptionJSON(rx.srv.URL, "Encounter?patient=Patient/p1", "id-only"))
	f.deliver(1)

	f.put(t, "/Encounter/e1", encounterJSON("e1", "p1", "in-progress"))
	f.put(t, "/Encounter/e2", encounterJSON("e2", "someone-else", "in-progress"))
	f.deliver(3)

	got := rx.received()
	if len(got) != 2 {
		t.Fatalf("received %d requests, want handshake + one notification", len(got))
	}
	note := got[1]
	p := statusParams(t, note)
	if p["type"] != "event-notification" || p["events-since-subscription-start"] != "1" {
		t.Errorf("status parameters = %v", p)
	}
	focus := ""
	for _, part := range p["notification-event"].([]any) {
		if pm := part.(map[string]any); pm["name"] == "focus" {
			focus = pm["valueReference"].(map[string]any)["reference"].(string)
		}
	}
	if focus != "http://example.test/fhir/Encounter/e1" {
		t.Errorf("focus = %q", focus)
	}
	// id-only: the notification says which resource changed and does not carry it.
	if n := len(note["entry"].([]any)); n != 1 {
		t.Errorf("an id-only notification has %d entries, want 1", n)
	}
}

// Delivery is in order, and a failure holds back everything behind it rather than letting a later event overtake.
func TestFailedDeliveryIsRetriedInOrder(t *testing.T) {
	f := newSubFixture(t, true)
	rx := newReceiver(t, 200, 503, 503) // handshake ok, then event 1 fails twice
	f.put(t, "/Subscription/s1", subscriptionJSON(rx.srv.URL, "", "id-only"))
	f.deliver(1)

	f.put(t, "/Encounter/e1", encounterJSON("e1", "p1", "in-progress"))
	f.put(t, "/Encounter/e1", encounterJSON("e1", "p1", "finished"))
	f.deliver(6)

	var order []string
	for _, b := range rx.received()[1:] {
		order = append(order, eventNumber(t, b))
	}
	want := "1,1,1,2"
	if strings.Join(order, ",") != want {
		t.Errorf("delivery attempts by event number = %v, want %s", order, want)
	}
}

// The property the outbox exists for: an event recorded before a restart is delivered after it.
func TestEventsSurviveARestart(t *testing.T) {
	f := newSubFixture(t, true)
	rx := newReceiver(t)
	f.put(t, "/Subscription/s1", subscriptionJSON(rx.srv.URL, "", "id-only"))
	f.deliver(1)

	// Written, and the process "dies" before any delivery runs.
	f.put(t, "/Encounter/e1", encounterJSON("e1", "p1", "in-progress"))

	g := reopenSubFixture(t, f.db, true)
	g.deliver(2)

	got := rx.received()
	if len(got) != 2 || eventNumber(t, got[1]) != "1" {
		t.Fatalf("after a restart the pending event was not delivered: %d requests", len(got))
	}
}

// A discharge notification must carry the discharge, not whatever the encounter became by the time delivery succeeded.
func TestFullResourceCarriesTheVersionTheEventDescribes(t *testing.T) {
	f := newSubFixture(t, true)
	rx := newReceiver(t)
	f.put(t, "/Subscription/s1", subscriptionJSON(rx.srv.URL, "", "full-resource"))
	f.deliver(1)

	f.put(t, "/Encounter/e1", encounterJSON("e1", "p1", "in-progress"))
	f.put(t, "/Encounter/e1", encounterJSON("e1", "p1", "finished"))
	f.deliver(4)

	got := rx.received()
	if len(got) != 3 {
		t.Fatalf("received %d, want handshake + 2", len(got))
	}
	for i, want := range []string{"in-progress", "finished"} {
		entries := got[i+1]["entry"].([]any)
		if len(entries) != 2 {
			t.Fatalf("a full-resource notification has %d entries", len(entries))
		}
		res := entries[1].(map[string]any)["resource"].(map[string]any)
		if res["status"] != want {
			t.Errorf("notification %d carries status %q, want %q", i+1, res["status"], want)
		}
	}
}

func TestRepeatedFailurePutsTheSubscriptionIntoErrorAndCountsWhatWasMissed(t *testing.T) {
	f := newSubFixture(t, true)
	statuses := []int{200}
	for i := 0; i < maxFailures; i++ {
		statuses = append(statuses, 500)
	}
	rx := newReceiver(t, statuses...)
	f.put(t, "/Subscription/s1", subscriptionJSON(rx.srv.URL, "", "id-only"))
	f.deliver(1)

	f.put(t, "/Encounter/e1", encounterJSON("e1", "p1", "in-progress"))
	f.deliver(maxFailures + 2)
	if s := f.status(t, "s1"); s != "error" {
		t.Fatalf("after %d failures the status is %q, want error", maxFailures, s)
	}

	// While in error, events are counted but not queued.
	f.put(t, "/Encounter/e2", encounterJSON("e2", "p1", "in-progress"))

	// Renewing restarts delivery from the event that was held, and the count shows the one that was missed.
	f.put(t, "/Subscription/s1", subscriptionJSON(rx.srv.URL, "", "id-only"))
	f.deliver(4)
	got := rx.received()
	last := got[len(got)-1]
	if eventNumber(t, last) != "1" || statusParams(t, last)["events-since-subscription-start"] != "2" {
		t.Errorf("after renewal: event %s with events-since %v; want event 1 and a count of 2",
			eventNumber(t, last), statusParams(t, last)["events-since-subscription-start"])
	}
}

func TestAHandshakeRefusedOnceIsTriedAgain(t *testing.T) {
	// The client that created the subscription may not be listening yet when the handshake goes out; Inferno's PAS suite
	// starts waiting a millisecond after it.
	f := newSubFixture(t, true)
	rx := newReceiver(t, 500)
	f.put(t, "/Subscription/s1", subscriptionJSON(rx.srv.URL, "", "id-only"))
	f.deliver(1)
	if st := f.status(t, "s1"); st != "requested" {
		t.Fatalf("after one refused handshake the subscription should still be requested, not %s", st)
	}
	f.deliver(1)
	if st := f.status(t, "s1"); st != "active" || len(rx.received()) != 2 {
		t.Errorf("the second handshake should have activated it: %s after %d", st, len(rx.received()))
	}
}

func TestFailedHandshakeIsAnError(t *testing.T) {
	f := newSubFixture(t, true)
	rx := newReceiver(t, 401, 401, 401, 401, 401)
	f.put(t, "/Subscription/s1", subscriptionJSON(rx.srv.URL, "", "id-only"))
	f.deliver(handshakeAttempts)
	rec := tree(t, do(t, f.h, "GET", "/Subscription/s1", nil))
	if rec["status"] != "error" || !strings.Contains(rec["error"].(string), "HTTP 401") {
		t.Errorf("status/error = %v / %v", rec["status"], rec["error"])
	}
}

// A redirect is a second destination nobody checked.
func TestRedirectIsNotFollowed(t *testing.T) {
	f := newSubFixture(t, true)
	codes := make([]int, 2*handshakeAttempts)
	for i := range codes {
		codes[i] = http.StatusFound
	}
	rx := newReceiver(t, codes...)
	f.put(t, "/Subscription/s1", subscriptionJSON(rx.srv.URL, "", "id-only"))
	f.deliver(handshakeAttempts)
	if s := f.status(t, "s1"); s != "error" {
		t.Errorf("a handshake answered with a redirect left the subscription %q", s)
	}
	// A followed redirect would be a second request per attempt.
	if n := len(rx.received()); n != handshakeAttempts {
		t.Errorf("%d requests for %d attempts: a redirect was followed", n, handshakeAttempts)
	}
}

func TestInvalidSubscriptionsAreRefused(t *testing.T) {
	f := newSubFixture(t, false)
	cases := map[string]string{
		"plain http":        subscriptionJSON("http://receiver.example/hook", "", "id-only"),
		"unknown filter":    subscriptionJSON("https://receiver.example/hook", "Encounter?location=x", "id-only"),
		"wrong filter type": subscriptionJSON("https://receiver.example/hook", "Patient?patient=p1", "id-only"),
		"header injection":  subscriptionJSON("https://receiver.example/hook", "", "id-only", "X-A: 1\r\nX-B: 2"),
		"metadata address":  subscriptionJSON("https://169.254.169.254/hook", "", "id-only"),
		"bad payload":       subscriptionJSON("https://receiver.example/hook", "", "everything"),
		"topic not offered": strings.Replace(subscriptionJSON("https://receiver.example/hook", "", "id-only"), TopicEncounter, "Encounter?", 1),
	}
	for name, body := range cases {
		rec := do(t, f.h, "PUT", "/Subscription/bad", body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400: %s", name, rec.Code, rec.Body.String())
		}
	}
}

// Summaries are shown in the web interface, so they must not show the credentials a subscriber gave the server.
func TestSummariesWithholdCredentials(t *testing.T) {
	f := newSubFixture(t, true)
	rx := newReceiver(t)
	f.put(t, "/Subscription/s1", subscriptionJSON(rx.srv.URL+"/hook?token=secret123", "", "id-only",
		"Authorization: Bearer secret456"))
	f.deliver(1)
	if h := rx.headers[0].Get("Authorization"); h != "Bearer secret456" {
		t.Errorf("the configured header was not sent: %q", h)
	}

	sums, err := f.subs.Summaries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(sums)
	if strings.Contains(string(raw), "secret") {
		t.Errorf("a summary shows a credential: %s", raw)
	}
	if len(sums) != 1 || sums[0].HeaderNames[0] != "Authorization" || sums[0].Status != "active" {
		t.Errorf("summary = %+v", sums)
	}
}

func TestCapabilityStatementNamesTheTopics(t *testing.T) {
	f := newSubFixture(t, true)
	body := do(t, f.h, "GET", "/metadata", nil).Body.String()
	for _, topic := range []string{TopicEncounter, TopicAppointment} {
		if !strings.Contains(body, topic) {
			t.Errorf("the capability statement does not name %s", topic)
		}
	}
}

func TestSubscriptionsNeedR4(t *testing.T) {
	db, _ := sql.Open("sqlite", ":memory:")
	defer db.Close()
	store, err := NewStore(db, fhir.R5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.EnableSubscriptions(context.Background(), SubscriptionOptions{}); err == nil {
		t.Error("subscriptions were enabled on an R5 server, where the backport IG does not apply")
	}
}
