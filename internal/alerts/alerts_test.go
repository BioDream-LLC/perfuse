package alerts

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func reading(at time.Time) Reading {
	return Reading{
		At:       at,
		Channels: map[string]ChannelReading{},
		Queues:   map[string]QueueReading{},
	}
}

func TestNothingFiresUntilTheConditionHolds(t *testing.T) {
	// The debounce is the difference between a useful alert and one people mute.
	// "Error rate above 5% right now" fires on a single unlucky message during a
	// quiet hour.
	e := NewEvaluator([]Rule{{
		Kind: KindErrorRate, Threshold: 0.05, For: 2 * time.Minute,
	}}, nil)

	start := time.Now()
	bad := reading(start)
	bad.Channels["adt"] = ChannelReading{Received: 100, Failed: 20}

	if firing := e.Evaluate(bad); len(firing) != 0 {
		t.Fatalf("nothing should fire on the first breach, got %d", len(firing))
	}

	// Still inside the grace period.
	bad.At = start.Add(90 * time.Second)
	if firing := e.Evaluate(bad); len(firing) != 0 {
		t.Errorf("nothing should fire after 90s of a 2m rule, got %d", len(firing))
	}

	bad.At = start.Add(2*time.Minute + time.Second)
	firing := e.Evaluate(bad)
	if len(firing) != 1 {
		t.Fatalf("the alert should fire once the condition has held, got %d", len(firing))
	}
	if firing[0].Channel != "adt" {
		t.Errorf("channel = %q", firing[0].Channel)
	}
	// FiringSince is when the condition started, not when the alert fired. The
	// difference is the debounce, and somebody reading the alert wants the former.
	if !firing[0].FiringSince.Equal(start) {
		t.Errorf("FiringSince = %v, want %v", firing[0].FiringSince, start)
	}
}

func TestABriefBlipNeverFires(t *testing.T) {
	e := NewEvaluator([]Rule{{
		Kind: KindErrorRate, Threshold: 0.05, For: 2 * time.Minute,
	}}, nil)

	start := time.Now()
	bad := reading(start)
	bad.Channels["adt"] = ChannelReading{Received: 100, Failed: 20}
	e.Evaluate(bad)

	// Recovered before the grace period was up.
	good := reading(start.Add(30 * time.Second))
	good.Channels["adt"] = ChannelReading{Received: 100, Delivered: 100}
	e.Evaluate(good)

	// Now it goes bad again. The clock has to have restarted, or a channel that
	// flaps every minute would eventually fire as though it had been broken
	// continuously.
	bad.At = start.Add(time.Minute)
	e.Evaluate(bad)
	bad.At = start.Add(2 * time.Minute)
	if firing := e.Evaluate(bad); len(firing) != 0 {
		t.Error("the grace period should restart after a recovery")
	}
}

func TestAnAlertClearsItself(t *testing.T) {
	var mu sync.Mutex
	var sent []Alert
	e := NewEvaluator([]Rule{{
		Kind: KindErrorRate, Threshold: 0.05, For: 0,
	}}, func(a Alert) {
		mu.Lock()
		defer mu.Unlock()
		sent = append(sent, a)
	})

	start := time.Now()
	bad := reading(start)
	bad.Channels["adt"] = ChannelReading{Received: 100, Failed: 20}
	e.Evaluate(bad)
	// For: 0 falls back to the default, so step past it.
	bad.At = start.Add(DefaultFor + time.Second)
	if len(e.Evaluate(bad)) != 1 {
		t.Fatal("expected the alert to fire")
	}

	good := reading(bad.At.Add(time.Minute))
	good.Channels["adt"] = ChannelReading{Received: 100, Delivered: 100}
	if firing := e.Evaluate(good); len(firing) != 0 {
		t.Errorf("the alert should have cleared, %d still firing", len(firing))
	}

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 2 {
		t.Fatalf("want a firing and a resolved notification, got %d", len(sent))
	}
	// A notification that fires and never resolves trains people to ignore
	// notifications.
	if sent[0].Firing() != true || sent[1].Firing() != false {
		t.Error("want one firing then one resolved")
	}

	resolved := e.Resolved()
	if len(resolved) != 1 {
		t.Errorf("the resolved alert should be kept for the interface, got %d", len(resolved))
	}
}

func TestAnAlertFiresOnlyOnce(t *testing.T) {
	var count int
	e := NewEvaluator([]Rule{{Kind: KindErrorRate, Threshold: 0.05, For: 0}},
		func(a Alert) { count++ })

	start := time.Now()
	bad := reading(start)
	bad.Channels["adt"] = ChannelReading{Received: 100, Failed: 20}
	e.Evaluate(bad)

	for i := range 5 {
		bad.At = start.Add(DefaultFor + time.Duration(i+1)*time.Minute)
		e.Evaluate(bad)
	}

	// Repeating the same alert every evaluation is how a chat room gets muted.
	if count != 1 {
		t.Errorf("notified %d times for one continuous condition, want 1", count)
	}
}

func TestTheAlertUpdatesWhileItIsFiring(t *testing.T) {
	e := NewEvaluator([]Rule{{Kind: KindQueueDepth, Threshold: 10, For: 0}}, nil)

	start := time.Now()
	r := reading(start)
	r.Queues["adt/registry"] = QueueReading{
		Channel: "adt", Destination: "registry", Pending: 20, OldestSeconds: 60,
	}
	e.Evaluate(r)
	r.At = start.Add(DefaultFor + time.Second)
	e.Evaluate(r)

	// The backlog grows. The interface should show the current number, not the
	// one from when the alert opened.
	r.Queues["adt/registry"] = QueueReading{
		Channel: "adt", Destination: "registry", Pending: 900, OldestSeconds: 600,
	}
	r.At = r.At.Add(time.Minute)
	firing := e.Evaluate(r)
	if len(firing) != 1 {
		t.Fatalf("firing = %d", len(firing))
	}
	if firing[0].Value != 900 {
		t.Errorf("value = %v, want the current depth of 900", firing[0].Value)
	}
}

func TestErrorRateNeedsADenominatorWorthDividing(t *testing.T) {
	e := NewEvaluator([]Rule{{Kind: KindErrorRate, Threshold: 0.05, For: 0}}, nil)

	start := time.Now()
	r := reading(start)
	// One failure out of one message is a 100% error rate and almost never worth
	// waking somebody for.
	r.Channels["adt"] = ChannelReading{Received: 1, Failed: 1}
	e.Evaluate(r)
	r.At = start.Add(time.Hour)
	if firing := e.Evaluate(r); len(firing) != 0 {
		t.Errorf("a single failure in a quiet window should not fire, got %d", len(firing))
	}
}

func TestUnparseableMessagesAreExplainedAsTheSendersProblem(t *testing.T) {
	e := NewEvaluator([]Rule{{Kind: KindErrorRate, Threshold: 0.05, For: 0}}, nil)

	start := time.Now()
	r := reading(start)
	r.Channels["adt"] = ChannelReading{Received: 100, Unparseable: 30}
	e.Evaluate(r)
	r.At = start.Add(DefaultFor + time.Second)

	firing := e.Evaluate(r)
	if len(firing) != 1 {
		t.Fatal("expected an alert")
	}
	// The fix is a phone call to the sender rather than a restart here, and the
	// alert should say so at four in the morning.
	if !strings.Contains(firing[0].Detail, "sending system") {
		t.Errorf("the detail should point at the sender, got %q", firing[0].Detail)
	}
}

func TestQueueAgeFiresEvenOnAShallowQueue(t *testing.T) {
	// Age matters more than depth: two undelivered admissions from an hour ago is
	// a clinical problem, and a depth rule would never notice.
	e := NewEvaluator([]Rule{{Kind: KindQueueAge, Threshold: 900, For: 0}}, nil)

	start := time.Now()
	r := reading(start)
	r.Queues["adt/registry"] = QueueReading{
		Channel: "adt", Destination: "registry", Pending: 2, OldestSeconds: 3600,
	}
	e.Evaluate(r)
	r.At = start.Add(DefaultFor + time.Second)

	firing := e.Evaluate(r)
	if len(firing) != 1 {
		t.Fatalf("a shallow but stale queue should fire, got %d", len(firing))
	}
	if !strings.Contains(firing[0].Detail, "not draining") {
		t.Errorf("detail = %q", firing[0].Detail)
	}
}

func TestQueueAgeIgnoresAnEmptyQueue(t *testing.T) {
	e := NewEvaluator([]Rule{{Kind: KindQueueAge, Threshold: 900, For: 0}}, nil)

	start := time.Now()
	r := reading(start)
	// A drained destination reports a stale age until the next poll. Firing on it
	// would be an alert about nothing.
	r.Queues["adt/registry"] = QueueReading{
		Channel: "adt", Destination: "registry", Pending: 0, OldestSeconds: 5000,
	}
	e.Evaluate(r)
	r.At = start.Add(time.Hour)
	if firing := e.Evaluate(r); len(firing) != 0 {
		t.Error("an empty queue should not fire an age alert")
	}
}

func TestStuckQueueSaysRetryingWillNotHelp(t *testing.T) {
	e := NewEvaluator([]Rule{{Kind: KindQueueStuck, Threshold: 10, For: 0}}, nil)

	start := time.Now()
	r := reading(start)
	r.Queues["adt/registry"] = QueueReading{
		Channel: "adt", Destination: "registry", Pending: 5, MaxAttempts: 40,
	}
	e.Evaluate(r)
	r.At = start.Add(DefaultFor + time.Second)

	firing := e.Evaluate(r)
	if len(firing) != 1 {
		t.Fatal("expected an alert")
	}
	// A receiver that is reachable and rejecting needs a different response from
	// one that is down, and the alert has to distinguish them.
	if !strings.Contains(firing[0].Detail, "not going to fix") {
		t.Errorf("detail = %q", firing[0].Detail)
	}
}

func TestChannelDownFiresPerChannel(t *testing.T) {
	e := NewEvaluator([]Rule{{Kind: KindChannelDown, For: 0}}, nil)

	start := time.Now()
	r := reading(start)
	r.NotRunning = []string{"adt", "orders"}
	e.Evaluate(r)
	r.At = start.Add(DefaultFor + time.Second)

	firing := e.Evaluate(r)
	if len(firing) != 2 {
		t.Fatalf("want one alert per stopped channel, got %d", len(firing))
	}
}

func TestOneRuleProducesOneAlertPerSubject(t *testing.T) {
	// A rule with no channel named covers every channel. It has to produce a
	// separate alert for each, or one alert would flip between them and the
	// resolution of one would clear the other.
	e := NewEvaluator([]Rule{{Kind: KindErrorRate, Threshold: 0.05, For: 0}}, nil)

	start := time.Now()
	r := reading(start)
	r.Channels["adt"] = ChannelReading{Received: 100, Failed: 20}
	r.Channels["orders"] = ChannelReading{Received: 100, Failed: 30}
	e.Evaluate(r)
	r.At = start.Add(DefaultFor + time.Second)

	firing := e.Evaluate(r)
	if len(firing) != 2 {
		t.Fatalf("want an alert per channel, got %d", len(firing))
	}

	// One recovers; the other must stay firing.
	r.Channels["adt"] = ChannelReading{Received: 100, Delivered: 100}
	r.At = r.At.Add(time.Minute)
	firing = e.Evaluate(r)
	if len(firing) != 1 || firing[0].Channel != "orders" {
		t.Errorf("want only orders still firing, got %v", firing)
	}
}

func TestCriticalSortsFirst(t *testing.T) {
	e := NewEvaluator([]Rule{
		{Kind: KindErrorRate, Threshold: 0.05, For: 0, Severity: Warning},
		{Kind: KindQueueAge, Threshold: 60, For: 0, Severity: Critical},
	}, nil)

	start := time.Now()
	r := reading(start)
	r.Channels["adt"] = ChannelReading{Received: 100, Failed: 20}
	r.Queues["adt/registry"] = QueueReading{
		Channel: "adt", Destination: "registry", Pending: 3, OldestSeconds: 600,
	}
	e.Evaluate(r)
	r.At = start.Add(DefaultFor + time.Second)

	firing := e.Evaluate(r)
	if len(firing) != 2 {
		t.Fatalf("firing = %d", len(firing))
	}
	// Somebody scanning the list at four in the morning reads the top of it.
	if firing[0].Severity != Critical {
		t.Error("critical alerts should sort first")
	}
}

func TestRemovingARuleClearsItsAlert(t *testing.T) {
	e := NewEvaluator([]Rule{{Kind: KindErrorRate, Threshold: 0.05, For: 0}}, nil)

	start := time.Now()
	r := reading(start)
	r.Channels["adt"] = ChannelReading{Received: 100, Failed: 20}
	e.Evaluate(r)
	r.At = start.Add(DefaultFor + time.Second)
	if len(e.Evaluate(r)) != 1 {
		t.Fatal("expected the alert to fire")
	}

	// Otherwise the alert would sit there forever with nothing able to clear it.
	e.SetRules([]Rule{{Kind: KindQueueDepth, Threshold: 10}})
	if firing := e.Firing(); len(firing) != 0 {
		t.Errorf("removing the rule should have dropped its alert, %d remain", len(firing))
	}
}

func TestDisabledRulesDoNotFire(t *testing.T) {
	e := NewEvaluator([]Rule{{
		Kind: KindErrorRate, Threshold: 0.05, For: 0, Disabled: true,
	}}, nil)

	start := time.Now()
	r := reading(start)
	r.Channels["adt"] = ChannelReading{Received: 100, Failed: 90}
	e.Evaluate(r)
	r.At = start.Add(time.Hour)
	if firing := e.Evaluate(r); len(firing) != 0 {
		t.Error("a disabled rule should not fire")
	}
}

func TestAcknowledgeStopsPagingWithoutHidingTheAlert(t *testing.T) {
	e := NewEvaluator([]Rule{{Kind: KindQueueAge, Threshold: 60, For: 0}}, nil)

	start := time.Now()
	r := reading(start)
	r.Queues["adt/registry"] = QueueReading{
		Channel: "adt", Destination: "registry", Pending: 5, OldestSeconds: 600,
	}
	e.Evaluate(r)
	r.At = start.Add(DefaultFor + time.Second)
	firing := e.Evaluate(r)
	if len(firing) != 1 {
		t.Fatal("expected an alert")
	}

	if !e.Acknowledge(firing[0].Key, "testuser") {
		t.Fatal("Acknowledge should have found the alert")
	}

	// Still visible: somebody who knows a receiver is down for maintenance should
	// be able to stop the noise without pretending the queue is empty.
	after := e.Firing()
	if len(after) != 1 {
		t.Fatalf("the alert should still be listed, got %d", len(after))
	}
	if !after[0].Acknowledged || after[0].AcknowledgedBy != "testuser" {
		t.Error("the acknowledgement should be recorded against the person")
	}
}

func TestUnknownRuleKindIsIgnoredRatherThanFatal(t *testing.T) {
	// A configuration file from a newer version should not stop alerting
	// altogether.
	e := NewEvaluator([]Rule{
		{Kind: Kind("something-new"), Threshold: 1},
		{Kind: KindErrorRate, Threshold: 0.05, For: 0},
	}, nil)

	start := time.Now()
	r := reading(start)
	r.Channels["adt"] = ChannelReading{Received: 100, Failed: 20}
	e.Evaluate(r)
	r.At = start.Add(DefaultFor + time.Second)
	if len(e.Evaluate(r)) != 1 {
		t.Error("the known rule should still work")
	}
}

func TestDefaultRulesDoNotFireOnAHealthySystem(t *testing.T) {
	// A default set that fires often gets switched off wholesale, and then the one
	// that mattered is off too.
	e := NewEvaluator(nil, nil)

	start := time.Now()
	r := reading(start)
	r.Channels["adt"] = ChannelReading{Received: 5000, Delivered: 5000, P99Seconds: 0.05}
	r.Channels["orders"] = ChannelReading{Received: 200, Delivered: 199, Failed: 1}
	r.Queues["adt/registry"] = QueueReading{
		Channel: "adt", Destination: "registry", Pending: 3, OldestSeconds: 4, MaxAttempts: 1,
	}
	r.ChannelsConfigured = 2
	r.ChannelsRunning = 2

	e.Evaluate(r)
	r.At = start.Add(time.Hour)
	if firing := e.Evaluate(r); len(firing) != 0 {
		for _, a := range firing {
			t.Errorf("unexpected alert on a healthy system: %s", a.Summary)
		}
	}
}

// --- notifier --------------------------------------------------------------

func TestNotifierPostsFiringAndResolved(t *testing.T) {
	var mu sync.Mutex
	var got []Payload

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var p Payload
		if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
			t.Error(err)
			return
		}
		mu.Lock()
		got = append(got, p)
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n := &Notifier{URL: srv.URL, Headers: map[string]string{"X-Token": "t"}}

	n.Notify(Alert{
		Kind: KindQueueAge, Severity: Critical, Channel: "adt",
		Destination: "registry", Summary: "registry has not accepted anything for 20m",
		Value: 1200, Threshold: 900, FiringSince: time.Now().Add(-20 * time.Minute),
	})
	n.Notify(Alert{
		Kind: KindQueueAge, Severity: Critical, Channel: "adt",
		Destination: "registry", Summary: "registry has not accepted anything for 20m",
		FiringSince: time.Now().Add(-20 * time.Minute), ResolvedAt: time.Now(),
	})

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := len(got)
		mu.Unlock()
		if n == 2 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(got) != 2 {
		t.Fatalf("received %d payloads", len(got))
	}

	statuses := map[string]bool{}
	for _, p := range got {
		statuses[p.Status] = true
		if p.Text == "" {
			t.Error("the payload should carry a rendered line for chat integrations")
		}
	}
	if !statuses["firing"] || !statuses["resolved"] {
		t.Errorf("want both a firing and a resolved payload, got %v", statuses)
	}
}

func TestNotifierCarriesNoPatientData(t *testing.T) {
	// A webhook goes to a chat room, a chat room has a scrollback, and a
	// scrollback is not somewhere a patient identifier belongs. The payload is
	// asserted field by field rather than by inspection so a future addition has
	// to be a deliberate one.
	body, err := json.Marshal(Payload{})
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}

	allowed := map[string]bool{
		"status": true, "kind": true, "severity": true, "channel": true,
		"destination": true, "summary": true, "detail": true, "value": true,
		"threshold": true, "firingSince": true, "at": true, "text": true,
	}
	for name := range fields {
		if !allowed[name] {
			t.Errorf("the alert payload has a new field %q; confirm it cannot carry "+
				"message content before allowing it", name)
		}
	}
}

func TestNotifierRespectsMinSeverity(t *testing.T) {
	var mu sync.Mutex
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
	}))
	defer srv.Close()

	n := &Notifier{URL: srv.URL, MinSeverity: Critical}
	n.Notify(Alert{Kind: KindErrorRate, Severity: Warning, Summary: "meh"})

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if count != 0 {
		t.Error("a warning should not be sent when the minimum is critical")
	}
}

func TestNotifierSkipsAnAcknowledgedAlert(t *testing.T) {
	var mu sync.Mutex
	var count int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		count++
		mu.Unlock()
	}))
	defer srv.Close()

	n := &Notifier{URL: srv.URL}
	n.Notify(Alert{
		Kind: KindQueueAge, Severity: Critical, Summary: "known about",
		Acknowledged: true,
	})

	time.Sleep(200 * time.Millisecond)
	mu.Lock()
	defer mu.Unlock()
	if count != 0 {
		t.Error("an acknowledged alert should not page")
	}
}

func TestNotifierWithNoURLDoesNothing(t *testing.T) {
	// Alerting is off until somebody configures somewhere to send it.
	n := &Notifier{}
	n.Notify(Alert{Summary: "x"})
}

func TestAWebhookThatIsDownDoesNotBlockEvaluation(t *testing.T) {
	// A webhook endpoint that has itself gone down must not stop us noticing the
	// next problem.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(2 * time.Second)
	}))
	defer srv.Close()

	n := &Notifier{URL: srv.URL, Client: &http.Client{Timeout: 50 * time.Millisecond}}

	start := time.Now()
	n.Notify(Alert{Kind: KindErrorRate, Severity: Critical, Summary: "x"})
	if elapsed := time.Since(start); elapsed > 250*time.Millisecond {
		t.Errorf("Notify blocked for %s; it should send in the background", elapsed)
	}
}

func TestRuleValidateRefusesAnUnknownKind(t *testing.T) {
	// A misspelled kind decodes into a rule that looks entirely valid and never
	// fires. Somebody wrote down what they wanted to be told about, nothing
	// complained, and the condition goes unreported.
	err := Rule{Kind: "queue-dept", Threshold: 100}.Validate()
	if err == nil {
		t.Fatal("an unknown kind should be refused")
	}
	// The list matters more than the refusal: the commonest cause is a near miss.
	if !strings.Contains(err.Error(), "queue-depth") {
		t.Errorf("the error should list the real kinds: %v", err)
	}
}

func TestRuleValidateAcceptsEveryKindTheEvaluatorHandles(t *testing.T) {
	for _, k := range KnownKinds {
		if err := (Rule{Kind: k, Threshold: 1}).Validate(); err != nil {
			t.Errorf("kind %q should be valid: %v", k, err)
		}
	}
}

func TestEveryKnownKindIsActuallyEvaluated(t *testing.T) {
	// The pairing that would otherwise drift: a kind listed as known but with no
	// branch in the evaluator would pass validation and still never fire, which is
	// exactly what the validation exists to prevent.
	e := &Evaluator{}
	r := Reading{
		Channels: map[string]ChannelReading{
			"c": {
				Received: 100, Failed: 100, Unparseable: 50,
				ScriptErrors: 10, Quarantined: 5, P99Seconds: 30,
				ContractViolations: 3, ContractDetail: "PID-3 was populated in 40% of messages.",
			},
		},
		Queues: map[string]QueueReading{
			"c/d": {Channel: "c", Destination: "d", Pending: 500, Failed: 5, OldestSeconds: 3600, MaxAttempts: 50},
		},
		NotRunning: []string{"c"},
	}

	// no-traffic inverts: it fires on the absence of messages, so the busy reading
	// above is correctly silent for it and it needs its own.
	quiet := Reading{Channels: map[string]ChannelReading{"c": {Received: 0}}}

	// below-rhythm needs history as well as a reading, because it compares a channel against what that channel normally carries at
	// this hour of this weekday. A busy reading cannot trip it and neither can a silent one: without a rhythm to compare against
	// there is nothing to be below.
	belowNormal := Reading{
		At:       time.Date(2026, 8, 25, 14, 30, 0, 0, time.UTC), // a Tuesday afternoon
		Channels: map[string]ChannelReading{"c": {Received: 5}},
		Rhythms:  map[string]RhythmSource{"c": fakeRhythm{lost: 0.95, ok: true}},
	}

	for _, k := range KnownKinds {
		rule := Rule{Kind: k, Threshold: 1}
		reading := r
		switch k {
		case KindNoTraffic:
			reading = quiet
		case KindBelowRhythm:
			// A share, not a count: this threshold is the fraction of normal below which the rule fires.
			rule.Threshold = 0.8
			reading = belowNormal
		}
		if got := e.evaluateRule(rule, reading); len(got) == 0 {
			t.Errorf("kind %q produced no alert against a reading that should trip it, "+
				"so it is listed as known but not evaluated", k)
		}
	}
}

func TestRuleValidateRefusesAnUncrossableThreshold(t *testing.T) {
	if err := (Rule{Kind: KindQueueDepth, Threshold: -1}).Validate(); err == nil {
		t.Error("a negative threshold can never be crossed and should be refused")
	}
	if err := (Rule{Kind: KindQueueDepth, For: -time.Second}).Validate(); err == nil {
		t.Error("a negative grace period should be refused")
	}
}

// TestTheSeverityThresholdIsReadWhenAnAlertIsSent covers the live alerts.minSeverity setting.
//
// Read at dispatch rather than copied at startup, because the threshold gets raised in response to something being noisy at
// an unwelcome hour, and "after a restart" is not a useful answer then.
func TestTheSeverityThresholdIsReadWhenAnAlertIsSent(t *testing.T) {
	var (
		mu   sync.Mutex
		sent int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent++
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	count := func() int {
		mu.Lock()
		defer mu.Unlock()

		return sent
	}
	waitFor := func(t *testing.T, want int) {
		t.Helper()
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if count() >= want {
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}

	threshold := Warning
	n := &Notifier{
		URL: srv.URL,
		// Deliberately Critical, so a leftover read of the field rather than the function is visible: it
		// would suppress the warning below.
		MinSeverity:   Critical,
		MinSeverityFn: func() Severity { return threshold },
	}

	warning := Alert{
		Kind: KindQueueAge, Severity: Warning, Channel: "adt", Destination: "registry",
		Summary: "registry is behind", FiringSince: time.Now().Add(-5 * time.Minute),
	}

	n.Notify(warning)
	waitFor(t, 1)
	if count() != 1 {
		t.Fatalf("a warning was suppressed while the live threshold was warning: sent %d - the field is "+
			"being read instead of the function", count())
	}

	// Raised between two alerts, which is the whole point of reading it at dispatch.
	threshold = Critical
	n.Notify(warning)
	time.Sleep(150 * time.Millisecond)
	if count() != 1 {
		t.Errorf("a warning was sent after the threshold was raised to critical: sent %d - the threshold is "+
			"being read once rather than per alert", count())
	}

	// And a critical still goes.
	n.Notify(Alert{
		Kind: KindQueueAge, Severity: Critical, Channel: "adt", Destination: "registry",
		Summary: "registry has stopped", FiringSince: time.Now().Add(-20 * time.Minute),
	})
	waitFor(t, 2)
	if count() != 2 {
		t.Errorf("a critical alert was suppressed: sent %d", count())
	}
}

// TestAnUnrecognisedThresholdDoesNotMeanSilence pins the direction of the fallback.
//
// A threshold nobody recognises must produce more alerts than wanted rather than none. The other choice fails as a queue
// backing up unannounced, which is precisely the failure alerting exists to prevent - so the safe default here is noise.
func TestAnUnrecognisedThresholdDoesNotMeanSilence(t *testing.T) {
	var (
		mu   sync.Mutex
		sent int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		sent++
		mu.Unlock()
	}))
	defer srv.Close()

	n := &Notifier{URL: srv.URL, MinSeverityFn: func() Severity { return Severity("criticl") }}

	n.Notify(Alert{
		Kind: KindQueueAge, Severity: Warning, Channel: "adt", Destination: "registry",
		Summary: "registry is behind", FiringSince: time.Now().Add(-5 * time.Minute),
	})

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		n := sent
		mu.Unlock()
		if n > 0 {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}

	t.Error("a typo in the threshold silenced a warning - an unrecognised value must mean more alerts than " +
		"wanted rather than none")
}
