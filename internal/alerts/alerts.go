// Package alerts watches the metrics and says when something is wrong.
//
// The point is that nobody should have to be looking at the dashboard. "Failed
// transactions remain queued without timely investigation" is a documented
// operational failure in this class of software, and the reason is always the
// same: the information was on a screen nobody was in front of at four in the
// morning.
//
// Three decisions shape it.
//
// **An alert must clear itself.** A notification that fires and never resolves
// trains people to ignore notifications. Every rule here is evaluated against
// current state, so a receiver coming back closes the alert it opened.
//
// **Alerts are debounced by duration, not by count.** "Error rate above 5% right
// now" fires on a single unlucky message during a quiet hour. "Above 5% for two
// minutes" does not. Every rule carries a For duration and nothing fires until
// the condition has held that long.
//
// **What is sent contains no patient data.** An alert carries a channel name, a
// destination name, a number and a threshold. Those come from configuration and
// from counting. A webhook goes to a chat room, a chat room has a scrollback, and
// a scrollback is not somewhere a patient identifier should end up.
package alerts

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Severity is how much attention something needs.
type Severity string

const (
	// Warning is worth knowing about in the morning.
	Warning Severity = "warning"
	// Critical means messages are not being delivered now.
	Critical Severity = "critical"
)

// Kind identifies what a rule watches. Kept as a small closed set so the
// interface can group and explain them rather than printing rule names.
type Kind string

const (
	KindErrorRate  Kind = "error-rate"
	KindQueueDepth Kind = "queue-depth"
	KindQueueAge   Kind = "queue-age"
	KindQueueStuck Kind = "queue-stuck"
	KindNoTraffic  Kind = "no-traffic"

	// KindBelowRhythm fires when a channel falls well below what it normally carries at this hour of this weekday.
	//
	// KindNoTraffic above is a fixed threshold, and its own help text admits the limit: only use it on feeds that never
	// go quiet. Almost nothing in a clinic qualifies. The lab feed stops at six, the referral feed is dead all weekend,
	// and a threshold that tolerates Sunday 3am cannot catch a real outage on Tuesday afternoon. A threshold that
	// catches Tuesday pages every Saturday until somebody switches it off, and then Tuesday is unwatched too.
	//
	// This rule compares against the channel's own history for that hour of the week instead, so a clinic feed going
	// quiet at 2pm on a Tuesday is an alert and the same silence on Sunday is not. It is the only rule here that can
	// report the absence of messages on a feed that is legitimately silent most of the time - and a lab feed that
	// stopped is a patient-safety problem that raises no error anywhere.
	KindBelowRhythm Kind = "below-rhythm"
	KindChannelDown Kind = "channel-down"
	KindScriptError Kind = "script-errors"

	// KindQuarantined fires when a database source abandons a row.
	KindQuarantined Kind = "rows-quarantined"

	KindSlowness Kind = "slow-delivery"

	// KindContract fires when a feed stops looking the way its contract says it must.
	//
	// Different in nature from every other kind here: the others are about the engine not coping, and this
	// one is about the far end having changed. Nothing is failing when it fires - the messages parse, the
	// deliveries succeed - which is precisely why it needs an alert of its own. It is the only condition on
	// this list that a healthy-looking system can be in for weeks.
	KindContract Kind = "contract"
)

// KnownKinds lists every kind a rules file may use.
//
// Checked at load, because a misspelled kind decodes into a valid-looking rule that
// simply never fires. That is the worst possible outcome for an alert: somebody has
// written down what they want to be told about, the file loads, nothing complains,
// and the condition they were worried about goes unreported.
var KnownKinds = []Kind{
	KindErrorRate,
	KindQueueDepth,
	KindQueueAge,
	KindQueueStuck,
	KindNoTraffic,
	KindBelowRhythm,
	KindChannelDown,
	KindScriptError,
	KindQuarantined,
	KindSlowness,
	KindContract,
}

// Validate checks one rule.
func (r Rule) Validate() error {
	known := false
	for _, k := range KnownKinds {
		if r.Kind == k {
			known = true
			break
		}
	}
	if !known {
		names := make([]string, 0, len(KnownKinds))
		for _, k := range KnownKinds {
			names = append(names, string(k))
		}
		return fmt.Errorf("%q is not an alert kind; use one of %s",
			r.Kind, strings.Join(names, ", "))
	}
	if r.Threshold < 0 {
		return fmt.Errorf("a negative threshold (%v) can never be crossed", r.Threshold)
	}
	if r.For < 0 {
		return fmt.Errorf("a negative duration (%s) is not a grace period", r.For)
	}
	return nil
}

// Alert is one thing currently wrong, or recently wrong.
type Alert struct {
	// Key identifies the alert across evaluations so it can be updated rather
	// than duplicated.
	Key string `json:"key"`

	Kind        Kind     `json:"kind"`
	Severity    Severity `json:"severity"`
	Channel     string   `json:"channel,omitempty"`
	Destination string   `json:"destination,omitempty"`

	// Summary is one line, written for somebody woken up by it.
	Summary string `json:"summary"`

	// Detail says what to do, or what it means. An alert that only states a fact
	// leaves the reader to work out whether it matters.
	Detail string `json:"detail,omitempty"`

	Value     float64 `json:"value"`
	Threshold float64 `json:"threshold"`

	// FiringSince is when the condition was first met, which is not the same as
	// when the alert fired: the difference is the debounce.
	FiringSince time.Time `json:"firingSince"`
	FiredAt     time.Time `json:"firedAt"`
	// ResolvedAt is set when the condition stopped holding.
	ResolvedAt time.Time `json:"resolvedAt,omitzero"`

	// Acknowledged suppresses notification without hiding the alert. Somebody who
	// knows a receiver is down for maintenance should be able to stop the paging
	// without pretending the queue is empty.
	Acknowledged   bool      `json:"acknowledged"`
	AcknowledgedBy string    `json:"acknowledgedBy,omitempty"`
	AcknowledgedAt time.Time `json:"acknowledgedAt,omitzero"`
}

// Firing reports whether the alert is currently active.
func (a Alert) Firing() bool { return a.ResolvedAt.IsZero() }

// Rule is one threshold to watch.
type Rule struct {
	// Kind selects what is measured.
	Kind Kind `yaml:"kind" json:"kind"`

	// Channel and Destination narrow the rule. Empty means every one.
	Channel     string `yaml:"channel,omitempty" json:"channel,omitempty"`
	Destination string `yaml:"destination,omitempty" json:"destination,omitempty"`

	// Threshold is the value above which the rule fires. For KindNoTraffic it is
	// the value below which it fires, because the problem there is silence.
	Threshold float64 `yaml:"threshold" json:"threshold"`

	// For is how long the condition must hold. Without it a single unlucky
	// message during a quiet hour fires a page.
	For time.Duration `yaml:"for,omitempty" json:"for,omitempty"`

	// Severity defaults to warning.
	Severity Severity `yaml:"severity,omitempty" json:"severity,omitempty"`

	// Disabled turns a rule off without deleting it, so the threshold somebody
	// tuned is not lost.
	Disabled bool `yaml:"disabled,omitempty" json:"disabled,omitempty"`
}

// DefaultFor is the debounce applied when a rule does not set one.
const DefaultFor = 2 * time.Minute

func (r Rule) severity() Severity {
	if r.Severity == "" {
		return Warning
	}
	return r.Severity
}

// threshold is the fraction of normal traffic that must be missing, defaulted.
//
// 0.8 rather than something tighter, because this rule is watching for a feed that has stopped or been re-pointed, not
// for a quiet afternoon. A feed down a third is usually a quiet afternoon.
func (r Rule) threshold() float64 {
	if r.Threshold <= 0 {
		return 0.8
	}
	return r.Threshold
}

func (r Rule) debounce() time.Duration {
	if r.For <= 0 {
		return DefaultFor
	}
	return r.For
}

func (r Rule) key() string {
	return fmt.Sprintf("%s|%s|%s", r.Kind, r.Channel, r.Destination)
}

// DefaultRules are what a deployment gets without configuring anything.
//
// They are deliberately conservative. A default set that fires often gets
// switched off wholesale, and then the one that mattered is off too. Each of
// these describes a situation where messages are demonstrably not arriving.
func DefaultRules() []Rule {
	return []Rule{
		{
			Kind: KindErrorRate, Threshold: 0.05, For: 2 * time.Minute,
			Severity: Warning,
		},
		{
			Kind: KindErrorRate, Threshold: 0.25, For: time.Minute,
			Severity: Critical,
		},
		{
			// A queue this deep means an outage rather than a blip.
			Kind: KindQueueDepth, Threshold: 500, For: time.Minute,
			Severity: Warning,
		},
		{
			// Age is the better signal. Fifteen minutes of undelivered admissions
			// is a clinical problem regardless of how many there are.
			Kind: KindQueueAge, Threshold: 900, For: time.Minute,
			Severity: Critical,
		},
		{
			// Retried repeatedly without progress: the receiver is reachable and
			// refusing, which will not fix itself.
			Kind: KindQueueStuck, Threshold: 10, For: 2 * time.Minute,
			Severity: Critical,
		},
		{
			Kind: KindChannelDown, Threshold: 0, For: time.Minute,
			Severity: Critical,
		},
		{
			Kind: KindScriptError, Threshold: 1, For: 5 * time.Minute,
			Severity: Warning,
		},
		{
			// Included by default, and that is a deliberate decision rather than a convenience. A channel with
			// a contract and no rule to alert on it would be a check that runs, finds a problem and tells
			// nobody - which is the "looks configured and is not" failure this codebase refuses everywhere
			// else. Attaching a contract has to be sufficient.
			//
			// Costs nothing on an installation with no contracts: the reading carries zero violations and the
			// rule never fires.
			//
			// Warning rather than critical, and a long debounce. Nothing is broken when this fires - the
			// messages parse and the deliveries succeed - so it is not a reason to wake somebody at night, and
			// half an hour of confirmation avoids firing on a quiet period that happened to contain an odd
			// batch.
			Kind: KindContract, Threshold: 0, For: 30 * time.Minute,
			Severity: Warning,
		},
	}
}

// Reading is the state one evaluation looks at.
//
// The evaluator is given numbers rather than a metrics collector so it can be
// tested without one, and so the same rules could later be evaluated against a
// remote server's readings.
type Reading struct {
	At time.Time

	// Channels is keyed by channel name.
	Channels map[string]ChannelReading

	// Queues is keyed by "channel/destination".
	Queues map[string]QueueReading

	// ChannelsConfigured and ChannelsRunning drive the channel-down rule.
	ChannelsConfigured int
	ChannelsRunning    int
	// NotRunning names the channels that should be running and are not.
	NotRunning []string

	// Rhythms is what each channel normally carries, keyed by channel name.
	//
	// An interface rather than the concrete type, so this package does not depend on the message store. A rule that
	// needed the store would make the alert rules untestable without a database, and these are the rules somebody
	// most needs to be able to reason about.
	//
	// Absent for a channel with too little history, which is the honest state rather than a defaulted one.
	Rhythms map[string]RhythmSource
}

// RhythmSource is what a channel normally does, as the alert rules need it.
type RhythmSource interface {
	// Shortfall reports how far below normal this hour has fallen, as a fraction of the usual.
	//
	// The bool is false when nothing can honestly be said - too little history, or an hour where silence is normal.
	// A rule that guessed here would fire every quiet Sunday, get switched off, and take the useful alerts with it.
	Shortfall(at time.Time, received int64) (fraction float64, ok bool)

	// Describe explains the hour in a sentence, for somebody reading an alert at 2am.
	Describe(at time.Time) string
}

// ChannelReading is one channel's recent behaviour.
type ChannelReading struct {
	Received  float64
	Delivered float64
	Failed    float64
	// Unparseable is counted separately from failed: it means a sender has
	// started emitting rubbish, which is their bug rather than ours.
	Unparseable  float64
	ScriptErrors float64
	// Quarantined counts rows a database source abandoned in the window.
	//
	// Any increase needs a person. Perfuse quarantines rather than retrying forever,
	// which is what stops one bad row halting a feed, and this number is the visible
	// cost of that choice: without an alert on it, the rows would be dropped quietly
	// and the design would be worse than the stall it replaced.
	Quarantined float64
	// P99Seconds is end-to-end message duration. A mean would hide the tail that
	// makes senders time out.
	P99Seconds float64

	// ContractViolations is how many of this channel's expectations did not hold.
	//
	// Carried on the reading rather than checked in a separate loop so it goes through the same evaluator,
	// debounce and acknowledgement path as everything else. An alert somebody cannot acknowledge is an alert
	// they will silence by deleting the rule.
	ContractViolations float64

	// ContractDetail is the worst violation, in words, for the alert body.
	//
	// The worst one rather than all of them: an alert is one line plus a sentence, and a list of nine
	// findings in a page it is delivered to is a list nobody reads. The full set is in the interface.
	ContractDetail string
}

// ErrorRate is failures over messages that arrived, in the window.
func (c ChannelReading) ErrorRate() float64 {
	if c.Received <= 0 {
		return 0
	}
	return (c.Failed + c.Unparseable) / c.Received
}

// QueueReading is one destination's queue.
type QueueReading struct {
	Channel       string
	Destination   string
	Pending       int
	Failed        int
	OldestSeconds float64
	MaxAttempts   int
}

// Evaluator holds rule state between evaluations, which is what makes debouncing
// and self-clearing possible.
type Evaluator struct {
	mu    sync.Mutex
	rules []Rule

	// pending records when each rule's condition was first met.
	pending map[string]time.Time
	// active is the alert currently firing for each rule key.
	active map[string]*Alert
	// resolved keeps recently cleared alerts so the interface can show that
	// something happened and recovered, which is often the answer to "why did
	// the overnight batch look odd".
	resolved []Alert

	// notify is called once when an alert fires and once when it resolves.
	notify func(Alert)

	maxResolved int
}

// NewEvaluator builds an evaluator. Nil rules means DefaultRules.
func NewEvaluator(rules []Rule, notify func(Alert)) *Evaluator {
	if rules == nil {
		rules = DefaultRules()
	}
	return &Evaluator{
		rules:       rules,
		pending:     map[string]time.Time{},
		active:      map[string]*Alert{},
		notify:      notify,
		maxResolved: 100,
	}
}

// Rules returns the configured rules.
func (e *Evaluator) Rules() []Rule {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]Rule(nil), e.rules...)
}

// SetRules replaces the rules, keeping the state of any that still exist.
func (e *Evaluator) SetRules(rules []Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = rules

	// A rule that no longer exists must not leave an alert firing forever with
	// nothing able to clear it.
	valid := map[string]bool{}
	for _, r := range rules {
		valid[r.key()] = true
	}
	for key := range e.active {
		if !strings.Contains(key, "|") {
			continue
		}
		if !valid[ruleKeyOf(key)] {
			delete(e.active, key)
			delete(e.pending, key)
		}
	}
}

// Evaluate applies every rule to a reading and returns the alerts firing now.
func (e *Evaluator) Evaluate(r Reading) []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()

	if r.At.IsZero() {
		r.At = time.Now()
	}

	// Collect every (rule, subject) pair that is currently in breach.
	breaches := map[string]Alert{}
	for _, rule := range e.rules {
		if rule.Disabled {
			continue
		}
		for _, a := range e.evaluateRule(rule, r) {
			breaches[a.Key] = a
		}
	}

	// Open or confirm.
	for key, candidate := range breaches {
		since, seen := e.pending[key]
		if !seen {
			e.pending[key] = r.At
			since = r.At
		}

		if existing, firing := e.active[key]; firing {
			// Update the numbers so the interface shows current state rather than
			// what it was when the alert opened.
			existing.Value = candidate.Value
			existing.Summary = candidate.Summary
			existing.Detail = candidate.Detail
			existing.Severity = candidate.Severity
			continue
		}

		if r.At.Sub(since) < e.debounceFor(key) {
			continue // still within the grace period
		}

		alert := candidate
		alert.FiringSince = since
		alert.FiredAt = r.At
		e.active[key] = &alert
		if e.notify != nil {
			e.notify(alert)
		}
	}

	// Clear anything no longer in breach.
	for key, alert := range e.active {
		if _, still := breaches[key]; still {
			continue
		}
		resolved := *alert
		resolved.ResolvedAt = r.At
		e.resolved = append(e.resolved, resolved)
		if len(e.resolved) > e.maxResolved {
			e.resolved = e.resolved[len(e.resolved)-e.maxResolved:]
		}
		delete(e.active, key)
		delete(e.pending, key)
		if e.notify != nil {
			// Sent as well, because an alert that never resolves trains people to
			// ignore alerts.
			e.notify(resolved)
		}
	}
	// Forget grace-period state for conditions that stopped before firing.
	for key := range e.pending {
		if _, still := breaches[key]; !still {
			if _, firing := e.active[key]; !firing {
				delete(e.pending, key)
			}
		}
	}

	return e.firingLocked()
}

// Firing returns the alerts currently active, worst first.
func (e *Evaluator) Firing() []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.firingLocked()
}

func (e *Evaluator) firingLocked() []Alert {
	out := make([]Alert, 0, len(e.active))
	for _, a := range e.active {
		out = append(out, *a)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Severity != out[j].Severity {
			return out[i].Severity == Critical
		}
		return out[i].FiredAt.Before(out[j].FiredAt)
	})
	return out
}

// Resolved returns recently cleared alerts, newest first.
func (e *Evaluator) Resolved() []Alert {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Alert, len(e.resolved))
	for i, a := range e.resolved {
		out[len(e.resolved)-1-i] = a
	}
	return out
}

// Acknowledge suppresses notification for an alert without hiding it.
func (e *Evaluator) Acknowledge(key, who string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	a, ok := e.active[key]
	if !ok {
		return false
	}
	a.Acknowledged = true
	a.AcknowledgedBy = who
	a.AcknowledgedAt = time.Now()
	return true
}

func (e *Evaluator) debounceFor(alertKey string) time.Duration {
	want := ruleKeyOf(alertKey)
	for _, r := range e.rules {
		if r.key() == want {
			return r.debounce()
		}
	}
	return DefaultFor
}

// alertKey is the rule key plus the subject, so one rule covering every channel
// produces one alert per channel rather than one that flips between them.
func alertKey(rule Rule, channel, destination string) string {
	return fmt.Sprintf("%s|%s|%s#%s/%s",
		rule.Kind, rule.Channel, rule.Destination, channel, destination)
}

func ruleKeyOf(alertKey string) string {
	if i := strings.IndexByte(alertKey, '#'); i >= 0 {
		return alertKey[:i]
	}
	return alertKey
}

func (r Rule) matches(channel, destination string) bool {
	if r.Channel != "" && r.Channel != channel {
		return false
	}
	if r.Destination != "" && r.Destination != destination {
		return false
	}
	return true
}
