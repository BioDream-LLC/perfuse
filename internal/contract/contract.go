// Package contract turns what a feed looks like into what a feed must look like.
//
// A profile answers "what am I receiving". That is useful once. The expensive problem is the day it changes:
// a vendor upgrade drops a field, a new code appears, a segment starts repeating. None of those is an error -
// the messages are still valid HL7 and every engine in the world accepts them - so nothing notices until a
// receiver falls over, usually weeks later, and the investigation starts from the wrong end.
//
// A contract is the profile written down as an expectation and then checked. It is contract testing, which is
// a solved idea in software generally and almost entirely absent from healthcare integration.
//
// # Why the unit is a rate and not a message
//
// The obvious design is to check each message and complain when one fails. That produces a system nobody can
// use. Real feeds contain a proportion of genuinely odd messages - a test message from the vendor, a patient
// with no recorded sex, a manual entry - and alerting on each one trains people to ignore the alerts, at which
// point the feature is worse than nothing because it has consumed the attention it needed.
//
// So an expectation is about a proportion over a window: "PID-3 is populated in at least 99% of messages". One
// odd message never fires. Five percent of messages losing their medical record number does.
//
// # A contract describes what arrives, not what leaves
//
// This is the distinction people get wrong first, including me while integration-testing it. Perfuse stores the
// message as it arrived - that is deliberate, because the stored message is evidence of what a sender sent - so a
// contract is checked against the incoming feed and not against the output of the channel's transformations.
//
// That is the right default by a wide margin: the thing that changes without warning is the sending system, and
// the point of a contract is to notice. But it means an expectation written about a mapped value ("after mapping,
// only the lab's codes should remain") will fail immediately and confusingly, because the codes it names never
// appear in the traffic being profiled. Anything about what leaves belongs in a channel test, which runs the real
// transformations.
//
// # Why a violation names the tolerance it broke
//
// "PID-3 failed" tells somebody nothing. "PID-3 was populated in 94.2% of the last 500 messages; the contract
// requires 99%" tells them how bad it is, how confident to be, and whether it is worth waking somebody. The
// numbers are the message.
package contract

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/profile"
)

// Rule is what is expected of a path.
type Rule string

const (
	// Populated requires the field to carry a value in at least MinRate of messages.
	//
	// The commonest expectation by a wide margin: nearly every real incident is a field that used to be
	// there and stopped.
	Populated Rule = "populated"

	// Absent requires the field to be empty or missing in at least MinRate of messages.
	//
	// Worth having because a field that starts arriving is as much of a change as one that stops. A sender
	// that begins populating PID-19 with a national identifier has changed what the site is holding, and
	// somebody should know.
	Absent Rule = "absent"

	// OneOf requires the value, when present, to be in Values.
	//
	// The value set is where a strict downstream mapping breaks: a new sex code, a new patient class, a new
	// order status. Those reject at the far end and the rejection rarely says which value caused it.
	OneOf Rule = "one of"

	// SegmentPresent requires the segment to appear in at least MinRate of messages.
	SegmentPresent Rule = "segment present"

	// MaxRepeats requires the field or segment to repeat no more than Limit times.
	//
	// A repetition count that grows is the failure that overflows a fixed-width downstream table, and it
	// happens without any field changing shape.
	MaxRepeats Rule = "at most"
)

// Expectation is one thing that must hold.
type Expectation struct {
	// Path is the field or segment, in the same notation filters and transformations use, so an expectation
	// can be pasted into a filter and vice versa.
	Path string `yaml:"path" json:"path"`

	// Rule is what is expected.
	Rule Rule `yaml:"rule" json:"rule"`

	// MinRate is the proportion of messages that must satisfy the rule, between 0 and 1.
	//
	// Defaults to 0.99 rather than 1.0, deliberately. An expectation of "always" is almost never what
	// somebody means: real feeds contain test messages and manual entries, and a contract that fires on one
	// of them in ten thousand is a contract that gets switched off.
	MinRate float64 `yaml:"min_rate,omitempty" json:"minRate,omitempty"`

	// Values is the permitted set for OneOf.
	Values []string `yaml:"values,omitempty" json:"values,omitempty"`

	// Limit is the maximum for MaxRepeats.
	Limit int `yaml:"limit,omitempty" json:"limit,omitempty"`

	// Why records who decided this and when.
	//
	// This is the domain-tax field. Most of what makes an interface hard to maintain is that nobody knows
	// why a decision was made, and the person who knew has left. An expectation without a reason becomes an
	// expectation nobody dares change and nobody dares delete, which is how a contract turns into
	// superstition.
	Why string `yaml:"why,omitempty" json:"why,omitempty"`

	// Relaxed records that this expectation was deliberately loosened, and why.
	//
	// Separate from simply editing MinRate, because an assertion silently weakened is worse than one that
	// was never written: it still looks like a guarantee. Recording the relaxation keeps the history visible
	// in the file that carries it.
	Relaxed string `yaml:"relaxed,omitempty" json:"relaxed,omitempty"`
}

// defaultMinRate is used when an expectation does not state one.
const defaultMinRate = 0.99

func (e Expectation) minRate() float64 {
	if e.MinRate <= 0 {
		return defaultMinRate
	}
	return e.MinRate
}

// Validate checks an expectation makes sense before it is ever used.
func (e Expectation) Validate() error {
	if strings.TrimSpace(e.Path) == "" {
		return fmt.Errorf("an expectation needs a path")
	}

	switch e.Rule {
	case Populated, Absent, SegmentPresent:
	case OneOf:
		if len(e.Values) == 0 {
			// Refused rather than treated as "no values permitted", which would fail every message and read
			// as a broken feed rather than a broken contract.
			return fmt.Errorf("%s: a %q expectation needs the list of permitted values", e.Path, OneOf)
		}
	case MaxRepeats:
		if e.Limit <= 0 {
			return fmt.Errorf("%s: a %q expectation needs a limit above zero", e.Path, MaxRepeats)
		}
	default:
		return fmt.Errorf("%s: unknown rule %q; use populated, absent, one of, segment present or at most",
			e.Path, e.Rule)
	}

	if e.MinRate < 0 || e.MinRate > 1 {
		return fmt.Errorf("%s: min_rate is %.2f; it is a proportion between 0 and 1", e.Path, e.MinRate)
	}

	return nil
}

// Contract is a set of expectations about one feed.
type Contract struct {
	// Expectations are checked independently. Order does not affect the result but is preserved, because a
	// file somebody wrote should read back the way they wrote it.
	Expectations []Expectation `yaml:"expectations" json:"expectations"`

	// MinMessages is how many messages must be seen before any expectation is judged.
	//
	// Without this a contract fires on the first message of the morning, when one absent field is 100% of the
	// evidence. Defaults to 100, which is enough for a 99% threshold to mean something.
	MinMessages int `yaml:"min_messages,omitempty" json:"minMessages,omitempty"`

	// DerivedFrom records the profile this came from, when it was promoted rather than hand-written.
	DerivedFrom string `yaml:"derived_from,omitempty" json:"derivedFrom,omitempty"`
}

const defaultMinMessages = 100

func (c *Contract) minMessages() int {
	if c.MinMessages <= 0 {
		return defaultMinMessages
	}
	return c.MinMessages
}

// Validate checks the whole contract.
func (c *Contract) Validate() []error {
	var errs []error

	if len(c.Expectations) == 0 {
		errs = append(errs, fmt.Errorf("a contract with no expectations checks nothing"))
	}

	// A duplicate path with the same rule is almost always a mistake - two expectations that disagree, where
	// the stricter one is silently doing all the work.
	seen := map[string]bool{}
	for _, e := range c.Expectations {
		if err := e.Validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		key := e.Path + "\x00" + string(e.Rule)
		if seen[key] {
			errs = append(errs, fmt.Errorf(
				"%s has two %q expectations; one of them is doing nothing and it is not obvious which",
				e.Path, e.Rule))
		}
		seen[key] = true
	}

	if c.MinMessages < 0 {
		errs = append(errs, fmt.Errorf("min_messages cannot be negative"))
	}

	return errs
}

// Violation is one expectation that did not hold.
type Violation struct {
	Expectation Expectation `json:"expectation"`

	// Actual is the observed rate, or the observed repeat count for MaxRepeats.
	Actual float64 `json:"actual"`

	// Messages is how many messages the observation rests on. Reported because a rate from 120 messages and
	// the same rate from 120,000 justify very different responses.
	Messages int `json:"messages"`

	// Unexpected lists values seen that the contract does not permit, for OneOf. Codes rather than free text,
	// so this does not become a way to read patient data - a rule with a permitted value list is by
	// definition about a code set.
	Unexpected []string `json:"unexpected,omitempty"`

	// Says is the sentence to show somebody. Built here rather than at the point of display so every caller -
	// the CLI, the API, an alert webhook - says the same thing.
	Says string `json:"says"`
}

// Result is the outcome of checking a contract.
type Result struct {
	// Messages is how many were examined.
	Messages int `json:"messages"`

	// Judged is false when there were too few messages to conclude anything. Distinguished from "no
	// violations", because they mean opposite things and look identical in a summary.
	Judged bool `json:"judged"`

	// Note explains a result that cannot be trusted.
	Note string `json:"note,omitempty"`

	// Violations are ordered worst first, by how far from the threshold they fell.
	Violations []Violation `json:"violations"`

	// Checked is how many expectations were evaluated, so a passing result says what it proved rather than
	// only that nothing was wrong.
	Checked int `json:"checked"`

	// Unjudged names expectations that could not be evaluated at all, each with the reason.
	//
	// Separate from Violations because they are not findings about the feed, they are findings about the
	// contract - and separate from Checked because counting them there is the lie this exists to correct. An
	// expectation nothing could be judged against was previously counted as checked and produced no violation,
	// so it read as a passing check and had never run once.
	Unjudged []string `json:"unjudged,omitempty"`
}

// Holds reports whether the contract held.
//
// A result that could not be judged does not hold, because saying it does would turn "we have no evidence"
// into "everything is fine" - which is the failure mode of every monitoring system that reports green when
// its input has stopped.
func (r *Result) Holds() bool {
	// An expectation that could not be evaluated counts against holding, for the same reason as too few messages:
	// it is an absence of evidence, and the whole point of the sentence above is that an absence of evidence must
	// not be reported as a pass. This does mean a contract with an unjudgeable expectation stops reading as green -
	// which is correct and overdue, because it was never being checked.
	return r.Judged && len(r.Violations) == 0 && len(r.Unjudged) == 0
}

// Summary is the one line to log or show.
func (r *Result) Summary() string {
	if !r.Judged {
		return fmt.Sprintf("not judged: %s", r.Note)
	}
	// The unjudged count goes in the summary rather than only in the detail, because the summary is what somebody
	// reads. "12 expectations held" beside two that could not be evaluated is a different statement from "12 held".
	unjudged := ""
	if len(r.Unjudged) > 0 {
		unjudged = fmt.Sprintf(", and %d could not be evaluated", len(r.Unjudged))
	}

	if len(r.Violations) == 0 {
		return fmt.Sprintf("%d expectation(s) held over %d message(s)%s", r.Checked, r.Messages, unjudged)
	}
	return fmt.Sprintf("%d of %d expectation(s) did not hold over %d message(s)%s",
		len(r.Violations), r.Checked, r.Messages, unjudged)
}

// Check evaluates a contract against a profile.
//
// Against a profile rather than against messages, deliberately. The profile already counts fill rates,
// cardinality and code sets over a corpus, so checking against it reuses that work, means a contract and a
// profile can never disagree about what was observed, and keeps this package away from message bytes entirely -
// which is also why it cannot leak patient data.
func Check(c *Contract, p *profile.Report) *Result {
	result := &Result{Messages: p.Messages}

	if p.Messages < c.minMessages() {
		result.Note = fmt.Sprintf(
			"only %d message(s), and this contract asks for at least %d before judging; a rate from too "+
				"few messages is noise",
			p.Messages, c.minMessages())
		return result
	}

	result.Judged = true

	// An index of what the profile observed, so each expectation is a lookup rather than a scan.
	fields := map[string]profile.Field{}
	segments := map[string]profile.Segment{}
	for _, seg := range p.Segments {
		segments[seg.ID] = seg
		for _, f := range seg.Fields {
			fields[f.Path] = f
		}
	}

	for _, e := range c.Expectations {
		v, unjudged := checkOne(e, fields, segments, p)
		if unjudged != "" {
			result.Unjudged = append(result.Unjudged, unjudged)
			continue
		}

		result.Checked++
		if v != nil {
			result.Violations = append(result.Violations, *v)
		}
	}

	// Worst first, measured by how far short of the threshold the observation fell. Sorting by path would read
	// like a data structure; sorting by severity reads like advice.
	sort.SliceStable(result.Violations, func(i, j int) bool {
		a, b := result.Violations[i], result.Violations[j]
		shortfallA := a.Expectation.minRate() - a.Actual
		shortfallB := b.Expectation.minRate() - b.Actual
		if shortfallA != shortfallB {
			return shortfallA > shortfallB
		}
		return a.Expectation.Path < b.Expectation.Path
	})

	return result
}

// checkOne evaluates one expectation.
//
// Returns a violation, or a reason it could not be evaluated at all. Two returns rather than treating "cannot evaluate" as "no
// violation", because those were the same value and that is precisely how an expectation that had never run once read as a
// passing check.
func checkOne(
	e Expectation, fields map[string]profile.Field, segments map[string]profile.Segment,
	p *profile.Report,
) (*Violation, string) {
	switch e.Rule {
	case Populated:
		f, ok := fields[e.Path]
		rate := 0.0
		if ok {
			rate = f.FillRate
		}
		if rate >= e.minRate() {
			return nil, ""
		}
		return &Violation{
			Expectation: e, Actual: rate, Messages: p.Messages,
			Says: fmt.Sprintf(
				"%s was populated in %.1f%% of %d messages; the contract requires %.1f%%",
				e.Path, rate*100, p.Messages, e.minRate()*100),
		}, ""

	case Absent:
		f, ok := fields[e.Path]
		absent := 1.0
		if ok {
			absent = 1 - f.FillRate
		}
		if absent >= e.minRate() {
			return nil, ""
		}
		return &Violation{
			Expectation: e, Actual: absent, Messages: p.Messages,
			Says: fmt.Sprintf(
				"%s was expected to be empty but carried a value in %.1f%% of %d messages; something has "+
					"started sending it",
				e.Path, (1-absent)*100, p.Messages),
		}, ""

	case OneOf:
		f, ok := fields[e.Path]
		if !ok {
			// A field that is not present at all cannot have a wrong value. Reporting it here would double up
			// with the Populated expectation that almost certainly exists beside it, and two findings for one
			// cause is what makes a report unreadable.
			return nil, ""
		}

		// No vocabulary was collected for this path, so there is nothing to compare the permitted list against.
		//
		// Reported rather than passed. This branch used to fall through to the loop below, which found no
		// unexpected values because there were no values at all, and returned no violation - so the expectation
		// was counted as checked and could never fail. Somebody who wrote "one of" against a field with no code
		// table believed they had a control on the sender's vocabulary and had nothing whatsoever.
		//
		// A profile collects values only where they are a code set by definition, which is what keeps patient
		// data out of it. So this is usually not a bug in the profiler but a path that is not a coded field, and
		// the message says so, because that is the thing the author needs to know.
		// Reached only when the path was observed, because the absent case returned above. Written to rely on
		// that rather than repeating it as a Present check: a plant showed the two were interchangeable, so
		// either could be deleted with no test failing, and a pair of guards where one would do is how a
		// deletion later looks safe.
		if len(f.Codes) == 0 {
			return nil, fmt.Sprintf(
				"%s: a %q expectation cannot be judged, because no values are recorded for this path - "+
					"a profile collects them only where the field is a code set, so this is most "+
					"likely not one",
				e.Path, OneOf)
		}

		permitted := map[string]bool{}
		for _, v := range e.Values {
			permitted[strings.TrimSpace(v)] = true
		}

		var unexpected []string
		unexpectedCount := 0
		total := 0
		for _, code := range f.Codes {
			total += code.Count
			if !permitted[code.Code] {
				unexpected = append(unexpected, code.Code)
				unexpectedCount += code.Count
			}
		}

		if len(unexpected) == 0 {
			return nil, ""
		}

		rate := 1.0
		if total > 0 {
			rate = float64(total-unexpectedCount) / float64(total)
		}
		if rate >= e.minRate() {
			return nil, ""
		}

		sort.Strings(unexpected)
		return &Violation{
			Expectation: e, Actual: rate, Messages: p.Messages, Unexpected: unexpected,
			Says: fmt.Sprintf(
				"%s sent %s, which the contract does not permit; %.1f%% of values were expected ones and "+
					"the contract requires %.1f%%. A strict mapping downstream will reject these",
				e.Path, strings.Join(quoteAll(unexpected), ", "), rate*100, e.minRate()*100),
		}, ""

	case SegmentPresent:
		seg, ok := segments[e.Path]
		rate := 0.0
		if ok {
			rate = seg.Rate
		}
		if rate >= e.minRate() {
			return nil, ""
		}
		return &Violation{
			Expectation: e, Actual: rate, Messages: p.Messages,
			Says: fmt.Sprintf(
				"%s appeared in %.1f%% of %d messages; the contract requires %.1f%%",
				e.Path, rate*100, p.Messages, e.minRate()*100),
		}, ""

	case MaxRepeats:
		// Repeats are a maximum rather than a rate: one message with forty repetitions is the one that
		// overflows a fixed-width downstream table, and averaging it away would hide exactly the case worth
		// catching.
		observed := 0
		if f, ok := fields[e.Path]; ok {
			observed = f.MaxRepeats
		} else if seg, ok := segments[e.Path]; ok {
			observed = seg.MaxPerMessage
		}

		if observed <= e.Limit {
			return nil, ""
		}
		return &Violation{
			Expectation: e, Actual: float64(observed), Messages: p.Messages,
			Says: fmt.Sprintf(
				"%s repeated up to %d times; the contract allows %d. A fixed-width table downstream will "+
					"overflow before anything reports an error",
				e.Path, observed, e.Limit),
		}, ""
	}

	return nil, ""
}

func quoteAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}
