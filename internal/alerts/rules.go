package alerts

import (
	"fmt"
	"sort"
	"time"
)

// The rules themselves.
//
// Each one produces a summary written for somebody who has just been woken up by
// it, and a detail that says what it means or what to do. An alert that states
// only a fact leaves the reader to work out whether it matters, at the worst
// possible moment for that.

// ruleFuncs maps each kind to what evaluates it.
//
// A map rather than a switch, and this is the whole reason: with a switch there were two lists - the Kind constants and
// the cases - and a kind added to one and forgotten in the other produces a rule that loads, appears in the interface,
// and silently does nothing. There is now one list, and AllKinds is derived from it, so the two cannot disagree.
var ruleFuncs = map[Kind]func(*Evaluator, Rule, Reading) []Alert{
	KindErrorRate:   (*Evaluator).errorRate,
	KindQueueDepth:  (*Evaluator).queueDepth,
	KindQueueAge:    (*Evaluator).queueAge,
	KindQueueStuck:  (*Evaluator).queueStuck,
	KindNoTraffic:   (*Evaluator).noTraffic,
	KindBelowRhythm: (*Evaluator).belowRhythm,
	KindChannelDown: (*Evaluator).channelDown,
	KindScriptError: (*Evaluator).scriptErrors,
	KindQuarantined: (*Evaluator).quarantined,
	KindSlowness:    (*Evaluator).slowness,
	KindContract:    (*Evaluator).contractViolations,
}

// AllKinds lists every kind that can actually be evaluated, in a stable order.
//
// Derived from the dispatch map, so a kind that appears here is one that demonstrably runs.
func AllKinds() []Kind {
	out := make([]Kind, 0, len(ruleFuncs))
	for k := range ruleFuncs {
		out = append(out, k)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

// Handles reports whether a kind has an implementation.
func Handles(k Kind) bool {
	_, ok := ruleFuncs[k]
	return ok
}

func (e *Evaluator) evaluateRule(rule Rule, r Reading) []Alert {
	fn, ok := ruleFuncs[rule.Kind]
	if !ok {
		// An unknown kind produces nothing rather than an error. A configuration
		// file from a newer version should not stop alerting altogether.
		return nil
	}
	return fn(e, rule, r)
}

func (e *Evaluator) errorRate(rule Rule, r Reading) []Alert {
	var out []Alert
	for name, c := range r.Channels {
		if !rule.matches(name, "") {
			continue
		}
		// A rate needs a denominator worth dividing by. One failure out of one
		// message is a 100% error rate and almost never worth waking somebody for.
		if c.Received < 10 {
			continue
		}
		rate := c.ErrorRate()
		if rate <= rule.Threshold {
			continue
		}

		detail := fmt.Sprintf("%.0f of %.0f messages in the window did not get through.",
			c.Failed+c.Unparseable, c.Received)
		if c.Unparseable > 0 && c.Unparseable >= c.Failed {
			// Worth separating: this is the sender's problem, not ours, and the fix
			// is a phone call rather than a restart.
			detail += fmt.Sprintf(
				" %.0f of those could not be parsed as HL7 at all, which usually means "+
					"the sending system has changed what it emits rather than anything "+
					"being wrong here.", c.Unparseable)
		}

		out = append(out, Alert{
			Key:      alertKey(rule, name, ""),
			Kind:     rule.Kind,
			Severity: rule.severity(),
			Channel:  name,
			Summary: fmt.Sprintf("%s is failing %.0f%% of messages",
				name, rate*100),
			Detail:    detail,
			Value:     rate,
			Threshold: rule.Threshold,
		})
	}
	return out
}

func (e *Evaluator) queueDepth(rule Rule, r Reading) []Alert {
	var out []Alert
	for _, q := range r.Queues {
		if !rule.matches(q.Channel, q.Destination) {
			continue
		}
		if float64(q.Pending) <= rule.Threshold {
			continue
		}
		out = append(out, Alert{
			Key:         alertKey(rule, q.Channel, q.Destination),
			Kind:        rule.Kind,
			Severity:    rule.severity(),
			Channel:     q.Channel,
			Destination: q.Destination,
			Summary: fmt.Sprintf("%d messages are waiting for %s",
				q.Pending, q.Destination),
			Detail: fmt.Sprintf(
				"The oldest has been waiting %s. Nothing is lost — they are on disk and "+
					"will go out in order when the receiver takes them — but a backlog this "+
					"size means the receiver has been unavailable rather than briefly slow.",
				formatDuration(q.OldestSeconds)),
			Value:     float64(q.Pending),
			Threshold: rule.Threshold,
		})
	}
	return out
}

func (e *Evaluator) queueAge(rule Rule, r Reading) []Alert {
	var out []Alert
	for _, q := range r.Queues {
		if !rule.matches(q.Channel, q.Destination) {
			continue
		}
		if q.Pending == 0 || q.OldestSeconds <= rule.Threshold {
			continue
		}
		out = append(out, Alert{
			Key:         alertKey(rule, q.Channel, q.Destination),
			Kind:        rule.Kind,
			Severity:    rule.severity(),
			Channel:     q.Channel,
			Destination: q.Destination,
			Summary: fmt.Sprintf("%s has not accepted anything for %s",
				q.Destination, formatDuration(q.OldestSeconds)),
			Detail: fmt.Sprintf(
				"%d message(s) are waiting. Age matters more than depth here: a large "+
					"queue that is draining is fine, and this one is not draining.",
				q.Pending),
			Value:     q.OldestSeconds,
			Threshold: rule.Threshold,
		})
	}
	return out
}

func (e *Evaluator) queueStuck(rule Rule, r Reading) []Alert {
	var out []Alert
	for _, q := range r.Queues {
		if !rule.matches(q.Channel, q.Destination) {
			continue
		}
		if q.Pending == 0 || float64(q.MaxAttempts) <= rule.Threshold {
			continue
		}
		out = append(out, Alert{
			Key:         alertKey(rule, q.Channel, q.Destination),
			Kind:        rule.Kind,
			Severity:    rule.severity(),
			Channel:     q.Channel,
			Destination: q.Destination,
			Summary: fmt.Sprintf("%s has refused the same message %d times",
				q.Destination, q.MaxAttempts),
			Detail: "Retrying is not going to fix this. A receiver that is reachable and " +
				"rejecting is a different problem from one that is down: look at what it " +
				"is answering, because the message at the head of the queue may be the " +
				"thing it objects to, and everything behind it is waiting on that one.",
			Value:     float64(q.MaxAttempts),
			Threshold: rule.Threshold,
		})
	}
	return out
}

func (e *Evaluator) noTraffic(rule Rule, r Reading) []Alert {
	var out []Alert
	for name, c := range r.Channels {
		if !rule.matches(name, "") {
			continue
		}
		// Fires below the threshold, because here the problem is silence.
		if c.Received > rule.Threshold {
			continue
		}
		out = append(out, Alert{
			Key:      alertKey(rule, name, ""),
			Kind:     rule.Kind,
			Severity: rule.severity(),
			Channel:  name,
			Summary:  fmt.Sprintf("%s has received nothing", name),
			Detail: "A silent channel looks identical to a healthy one on every other " +
				"measure, which is what makes this worth watching: the sender may have " +
				"been pointed somewhere else, or stopped. Only enable this on feeds that " +
				"genuinely never go quiet.",
			Value:     c.Received,
			Threshold: rule.Threshold,
		})
	}
	return out
}

// belowRhythm fires when a channel has fallen well below its own normal for this hour of this weekday.
//
// The threshold is a fraction lost, not a message count: 0.8 means four fifths of the usual traffic is missing. A count
// cannot work here, because the whole point is that the right count differs between Tuesday 2pm and Sunday 3am on the
// same feed.
func (e *Evaluator) belowRhythm(rule Rule, r Reading) []Alert {
	var out []Alert
	for name, c := range r.Channels {
		if !rule.matches(name, "") {
			continue
		}
		rhythm := r.Rhythms[name]
		if rhythm == nil {
			// No history, so no claim. A channel created this morning must not alert this afternoon for having no
			// past to be compared against.
			continue
		}

		lost, ok := rhythm.Shortfall(r.At, int64(c.Received))
		if !ok || lost < rule.threshold() {
			continue
		}

		out = append(out, Alert{
			Key:      alertKey(rule, name, ""),
			Kind:     rule.Kind,
			Severity: rule.severity(),
			Channel:  name,
			Summary: fmt.Sprintf("%s is carrying %.0f%% less than it normally does now",
				name, lost*100),
			Detail: rhythm.Describe(r.At) + ". " +
				"Nothing has errored, because a feed that stops produces no error - the sender simply goes quiet, " +
				"and every other measure looks healthy. Worth checking whether the sending system is still pointed " +
				"here, and whether anybody is waiting on results that are not arriving.",
			Value:     c.Received,
			Threshold: rule.threshold(),
		})
	}
	return out
}

func (e *Evaluator) channelDown(rule Rule, r Reading) []Alert {
	if len(r.NotRunning) == 0 {
		return nil
	}
	var out []Alert
	for _, name := range r.NotRunning {
		if !rule.matches(name, "") {
			continue
		}
		out = append(out, Alert{
			Key:      alertKey(rule, name, ""),
			Kind:     rule.Kind,
			Severity: rule.severity(),
			Channel:  name,
			Summary:  fmt.Sprintf("%s is enabled but not running", name),
			Detail: "It is not listening, so senders are being refused at the network " +
				"layer rather than acknowledged. Check the log for why it failed to start: " +
				"a port already in use is the usual cause.",
			Value:     0,
			Threshold: 1,
		})
	}
	return out
}

func (e *Evaluator) scriptErrors(rule Rule, r Reading) []Alert {
	var out []Alert
	for name, c := range r.Channels {
		if !rule.matches(name, "") {
			continue
		}
		if c.ScriptErrors < rule.Threshold {
			continue
		}
		out = append(out, Alert{
			Key:      alertKey(rule, name, ""),
			Kind:     rule.Kind,
			Severity: rule.severity(),
			Channel:  name,
			Summary: fmt.Sprintf("%s had %.0f script error(s)",
				name, c.ScriptErrors),
			Detail: "A transformer threw or timed out. The messages it touched were " +
				"answered AE and not delivered, so they will not arrive without being " +
				"resent. The channel log names the line.",
			Value:     c.ScriptErrors,
			Threshold: rule.Threshold,
		})
	}
	return out
}

// quarantined fires when a database source abandons a row.
//
// The default threshold is effectively one, because unlike an error rate there is
// no acceptable background level. A quarantined row is a message that a sending
// system believes it handed over and that will never arrive, and nothing else in
// the system will mention it again.
func (e *Evaluator) quarantined(rule Rule, r Reading) []Alert {
	var out []Alert
	for name, c := range r.Channels {
		if !rule.matches(name, "") {
			continue
		}
		threshold := rule.Threshold
		if threshold <= 0 {
			threshold = 1
		}
		if c.Quarantined < threshold {
			continue
		}
		out = append(out, Alert{
			Key:      alertKey(rule, name, ""),
			Kind:     rule.Kind,
			Severity: rule.severity(),
			Channel:  name,
			Summary: fmt.Sprintf("%s abandoned %.0f database row(s)",
				name, c.Quarantined),
			Detail: "A row could not be turned into a message after every attempt, so " +
				"the channel quarantined it and carried on with the rows behind it. " +
				"Nothing else is blocked, and that is the point: the alternative is one " +
				"bad row stopping the feed. But this row will not be retried and has not " +
				"been marked as processed, so it needs a person. The channel log names " +
				"the key and the reason.",
			Value:     c.Quarantined,
			Threshold: threshold,
		})
	}
	return out
}

func (e *Evaluator) slowness(rule Rule, r Reading) []Alert {
	var out []Alert
	for name, c := range r.Channels {
		if !rule.matches(name, "") {
			continue
		}
		if c.P99Seconds <= rule.Threshold {
			continue
		}
		out = append(out, Alert{
			Key:      alertKey(rule, name, ""),
			Kind:     rule.Kind,
			Severity: rule.severity(),
			Channel:  name,
			Summary: fmt.Sprintf("%s is taking %s for the slowest messages",
				name, formatDuration(c.P99Seconds)),
			// The p99 rather than the mean, because the mean stays comfortable
			// while the tail is what makes a sender give up and resend.
			Detail: "This is the 99th percentile, not the average: most messages are " +
				"probably fine. The tail is what matters, because a sender with a timeout " +
				"shorter than this will resend messages that did in fact arrive.",
			Value:     c.P99Seconds,
			Threshold: rule.Threshold,
		})
	}
	return out
}

func formatDuration(seconds float64) string {
	d := time.Duration(seconds * float64(time.Second))
	switch {
	case d < time.Second:
		return "under a second"
	case d < time.Minute:
		return fmt.Sprintf("%.0fs", d.Seconds())
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	default:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	}
}

// contractViolations fires when a feed stops looking the way its contract says it must.
//
// Unlike every other alert here, nothing is failing when this fires. The messages parse, the deliveries
// succeed, the error rate is zero. That is exactly why it needs to exist: it is the only condition on the list
// that a healthy-looking system can be in for weeks, and the usual way it surfaces is a receiver falling over
// long after the change that caused it.
func (e *Evaluator) contractViolations(rule Rule, r Reading) []Alert {
	var out []Alert

	for name, c := range r.Channels {
		if !rule.matches(name, "") {
			continue
		}
		if c.ContractViolations <= rule.Threshold {
			continue
		}

		// Deliberately not mentioning failure or errors in the summary. Somebody woken by this will go
		// looking at the engine, find everything green, and distrust the alert. Saying the sender changed
		// points them at the right end.
		summary := fmt.Sprintf("%s: the feed has changed shape", name)
		if c.ContractViolations == 1 {
			summary = fmt.Sprintf("%s: one thing about this feed has changed", name)
		}

		detail := c.ContractDetail
		if detail == "" {
			detail = fmt.Sprintf("%.0f expectation(s) no longer hold.", c.ContractViolations)
		}
		detail += " Nothing is failing: the messages are valid and the deliveries are succeeding. " +
			"Something at the sending end is different from when this contract was written."

		out = append(out, Alert{
			Key:       string(KindContract) + ":" + name,
			Kind:      KindContract,
			Severity:  rule.Severity,
			Channel:   name,
			Summary:   summary,
			Detail:    detail,
			Value:     c.ContractViolations,
			Threshold: rule.Threshold,
		})
	}

	return out
}
