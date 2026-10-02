// Package parity compares what Perfuse produces against what the engine being replaced produced.
//
// # Why this exists
//
// Perfuse can import a Mirth channel and can trace what it would do with a message. Neither of those answers the
// question somebody actually has before moving a live feed, which is: will this produce the same output as the thing
// that has been running for six years?
//
// No amount of design quality answers that. The only honest answer is evidence, and the evidence has to come from the
// old engine's own output on the site's own traffic.
//
// # Why the pairs have to be supplied
//
// This cannot reach into Mirth, and pretending otherwise would be the worst kind of feature. What it takes is pairs: a
// message as it arrived, and the message the old engine produced from it. Both are in Mirth's own message store and can
// be exported from its message browser, which is a thing an administrator already knows how to do.
//
// That constraint is stated rather than hidden. A tool that claimed to verify a migration without ever seeing the old
// engine's output would be verifying nothing.
//
// # Why differences are grouped rather than listed
//
// A run over fifty thousand messages that reports four thousand differences has told somebody nothing they can act on.
// The same run reporting that PID-8 differs in every message because the old engine mapped M to Male has told them
// exactly one thing to decide about.
//
// So findings are grouped by field, with a count, an example, and the distinct value pairs seen. One decision per
// finding is the unit somebody can work through.
package parity

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/shadow"
)

// Pair is one message and what the old engine made of it.
type Pair struct {
	// Input is the message as it arrived at the old engine.
	Input []byte

	// Expected is what the old engine produced.
	Expected []byte

	// Reference is the old engine's identifier for this message, so a finding can be traced back.
	//
	// Optional but strongly worth supplying. Without it a report can say a difference occurred and not which message to
	// look at in the system somebody still has open.
	Reference string
}

// Transformer is whatever produces Perfuse's output for a message.
//
// An interface so this package does not depend on the engine, which would make it untestable without a running channel.
type Transformer interface {
	Transform(raw []byte) ([]byte, error)
}

// Finding is one field that differed, across every message where it did.
type Finding struct {
	// Path is the field, in the usual notation.
	Path string `json:"path"`

	// Messages is how many messages differed at this path.
	Messages int `json:"messages"`

	// Examples are up to a handful of the differing value pairs.
	Examples []ValuePair `json:"examples"`

	// DistinctPairs is how many different from/to combinations occurred.
	//
	// The number that says what kind of problem this is. One distinct pair across four thousand messages is a single
	// systematic difference - a mapping, a date format, a hard-coded value - and one decision fixes all of it. Four
	// thousand distinct pairs is the field's content differing, which is a different and much worse problem.
	DistinctPairs int `json:"distinctPairs"`

	// Systematic is true when every occurrence had the same from and to.
	Systematic bool `json:"systematic"`
}

// ValuePair is one observed difference at a field.
type ValuePair struct {
	// Expected is what the old engine produced.
	Expected string `json:"expected"`
	// Got is what Perfuse produced.
	Got string `json:"got"`
	// Reference identifies a message where this occurred.
	Reference string `json:"reference,omitempty"`
	// Count is how many messages had this exact pair.
	Count int `json:"count"`
}

// Report is the outcome of a parity run.
type Report struct {
	// Compared is how many pairs were examined.
	Compared int `json:"compared"`

	// Identical is how many produced byte-for-byte the same output.
	//
	// Byte-for-byte, not equivalent. A weaker test would let a difference in segment order or trailing separators
	// through, and those are exactly the things a fussy downstream system rejects.
	Identical int `json:"identical"`

	// Equivalent is how many differed only in ways that carry no information.
	//
	// Kept separate from Identical rather than counted with it. Trailing empty fields and a final segment terminator
	// genuinely do not change meaning, and treating them as failures would bury real findings under thousands of
	// cosmetic ones. But they are not the same as identical, and a receiver doing its own strict parsing may disagree,
	// so the number is reported on its own.
	Equivalent int `json:"equivalent"`

	// Differing is how many differed in a way that matters.
	Differing int `json:"differing"`

	// Failed is how many could not be processed at all.
	//
	// Counted separately from Differing, because a channel that errors on a message is a different problem from one
	// that produces the wrong output, and the first is usually a configuration gap the importer could not fill.
	Failed int `json:"failed"`

	// FailureReasons groups the errors, most common first.
	FailureReasons []Reason `json:"failureReasons"`

	// Findings are the differing fields, most affected first.
	Findings []Finding `json:"findings"`

	// Verdict is one sentence somebody can put in a change request.
	Verdict string `json:"verdict"`
}

// Reason is one failure message and how often it occurred.
type Reason struct {
	Error    string `json:"error"`
	Messages int    `json:"messages"`
	// Reference identifies a message it happened to.
	Reference string `json:"reference,omitempty"`
}

// maxExamples bounds the value pairs kept per finding.
//
// Three is enough to see a pattern. More is a wall of text, and the count and distinct-pair figures carry the scale.
const maxExamples = 3

// Compare runs every pair through the transformer and reports how the output differed.
func Compare(t Transformer, pairs []Pair) (*Report, error) {
	if t == nil {
		return nil, fmt.Errorf("there is no channel to compare against")
	}
	if len(pairs) == 0 {
		return nil, fmt.Errorf("no message pairs were supplied. This needs the message as it arrived and the message " +
			"the old engine produced from it - both are in its own message store and can be exported from the message " +
			"browser. Without the old engine's output there is nothing to compare against, and a tool that claimed " +
			"otherwise would be verifying nothing")
	}

	r := &Report{Compared: len(pairs), Findings: []Finding{}, FailureReasons: []Reason{}}

	byPath := map[string]map[ValuePair]int{}
	pathRefs := map[string]map[ValuePair]string{}
	byReason := map[string]*Reason{}

	for _, p := range pairs {
		got, err := t.Transform(p.Input)
		if err != nil {
			r.Failed++
			key := err.Error()
			if existing, ok := byReason[key]; ok {
				existing.Messages++
			} else {
				byReason[key] = &Reason{Error: key, Messages: 1, Reference: p.Reference}
			}
			continue
		}

		if string(got) == string(p.Expected) {
			r.Identical++
			continue
		}

		// Field-level comparison, which is what makes a difference actionable. Comparing whole messages says only that
		// they differ, and the next question is always which field.
		// shadow.Diff, not DiffXML. DiffXML parses its input as XML and returns nothing when handed raw HL7 - so the
		// first version of this counted every real difference as cosmetic and reported perfect parity on messages
		// whose fields had changed. Found by a test asserting 4,000 differences and getting zero.
		diffs := shadow.Diff(p.Expected, got, shadow.DiffOptions{})
		if len(diffs) == 0 {
			// The bytes differ but no field does: trailing empty fields, a final terminator, separator padding.
			// Reported on its own rather than as a failure, because burying real findings under thousands of cosmetic
			// ones is how a report stops being read.
			r.Equivalent++
			continue
		}

		r.Differing++
		for _, d := range diffs {
			if byPath[d.Path] == nil {
				byPath[d.Path] = map[ValuePair]int{}
				pathRefs[d.Path] = map[ValuePair]string{}
			}
			vp := ValuePair{Expected: d.Live, Got: d.Candidate}
			byPath[d.Path][vp]++
			if _, seen := pathRefs[d.Path][vp]; !seen {
				pathRefs[d.Path][vp] = p.Reference
			}
		}
	}

	r.Findings = summarise(byPath, pathRefs)
	r.FailureReasons = summariseReasons(byReason)
	r.Verdict = verdict(r)

	return r, nil
}

func summarise(byPath map[string]map[ValuePair]int, refs map[string]map[ValuePair]string) []Finding {
	out := make([]Finding, 0, len(byPath))

	for path, pairs := range byPath {
		f := Finding{Path: path, DistinctPairs: len(pairs)}

		ordered := make([]ValuePair, 0, len(pairs))
		for key, count := range pairs {
			// key is the pair as stored: expected and got only, no count and no reference. Looked up in that shape,
			// because a struct used as a map key must be identical in every field.
			vp := key
			vp.Count = count
			vp.Reference = refs[path][key]
			ordered = append(ordered, vp)
			f.Messages += count
		}

		// Most frequent first, so the biggest single cause is the first thing read.
		sort.Slice(ordered, func(i, j int) bool {
			if ordered[i].Count != ordered[j].Count {
				return ordered[i].Count > ordered[j].Count
			}
			return ordered[i].Expected < ordered[j].Expected
		})

		f.Systematic = len(pairs) == 1
		if len(ordered) > maxExamples {
			ordered = ordered[:maxExamples]
		}
		f.Examples = ordered

		out = append(out, f)
	}

	// Most affected first. A field differing in every message is a bigger decision than one differing in three, and the
	// order should not depend on map iteration.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Messages != out[j].Messages {
			return out[i].Messages > out[j].Messages
		}
		return out[i].Path < out[j].Path
	})

	return out
}

func summariseReasons(byReason map[string]*Reason) []Reason {
	out := make([]Reason, 0, len(byReason))
	for _, r := range byReason {
		out = append(out, *r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Messages != out[j].Messages {
			return out[i].Messages > out[j].Messages
		}
		return out[i].Error < out[j].Error
	})
	return out
}

// verdict states the outcome in a sentence.
//
// Written to be quotable in a change request, because that is where this number ends up. It leads with the count that
// matters and never rounds to something flattering: 49,993 of 50,000 is a different statement from "over 99%", and the
// seven are the whole point.
func verdict(r *Report) string {
	switch {
	case r.Compared == 0:
		return "Nothing was compared."

	case r.Failed == r.Compared:
		return fmt.Sprintf("None of the %d messages could be processed at all. This is a configuration gap rather "+
			"than a difference in output - look at the failure reasons before reading anything else.", r.Compared)

	case r.Differing == 0 && r.Failed == 0 && r.Equivalent == 0:
		return fmt.Sprintf("All %d messages produced byte-for-byte identical output.", r.Compared)

	case r.Differing == 0 && r.Failed == 0:
		return fmt.Sprintf("%d of %d messages produced identical output and %d differed only in ways that carry no "+
			"information, such as trailing empty fields. No field differed in value.",
			r.Identical, r.Compared, r.Equivalent)

	default:
		parts := []string{fmt.Sprintf("%d of %d messages produced identical output", r.Identical, r.Compared)}
		if r.Equivalent > 0 {
			parts = append(parts, fmt.Sprintf("%d differed only cosmetically", r.Equivalent))
		}
		if r.Differing > 0 {
			systematic := 0
			for _, f := range r.Findings {
				if f.Systematic {
					systematic++
				}
			}
			s := fmt.Sprintf("%d differed across %d field(s)", r.Differing, len(r.Findings))
			if systematic > 0 {
				// The most useful thing the report can say. A systematic difference is one decision, not thousands.
				s += fmt.Sprintf(", %d of which differ the same way every time and are one decision each", systematic)
			}
			parts = append(parts, s)
		}
		if r.Failed > 0 {
			parts = append(parts, fmt.Sprintf("%d could not be processed at all", r.Failed))
		}
		return strings.Join(parts, ", ") + "."
	}
}
