// Package equiv proves that two engines agree, or says exactly where they do not.
//
// This is the answer to the only question that actually blocks a migration. An interface team does not
// refuse a new engine because it lacks features; they refuse because they cannot be sure it will do the
// same thing to the traffic they already have, and being wrong means lab results stop reaching doctors.
// No amount of feature comparison answers that. Evidence does.
//
// The intended use is a parallel run. Both engines receive the same production messages; the incumbent
// keeps delivering and Perfuse delivers to nobody, which is what shadow channels already are. After two
// weeks the outputs are compared here, and the conversation changes from "trust this" to "here are the
// three messages out of four hundred thousand where we differed, and why".
//
// # Grouping is the whole feature
//
// A naive diff of 400,000 messages produces 400,000 findings and gets closed unread. Almost every real
// difference has one cause: a date format, a facility code, one transformation step nobody carried
// across. So differences are grouped by cause - the path in the message and the kind of difference -
// and reported once with a count. Three thousand differences from one cause is one finding, not three
// thousand. That single decision is what makes the report readable, and a readable report is the
// difference between a migration that proceeds and one that stalls.
//
// # Values are withheld by default
//
// Real traffic is patient data, so a comparison report is a place PHI could leak into a ticket, an
// email or a slide. By default this reports paths, kinds and counts - never values. That is enough to
// find the cause: "MSH-7 differs in 3,000 of 3,000 messages" tells an analyst it is the date format
// without showing one date.
//
// Values can be enabled deliberately, because eventually somebody does need to see one, and pretending
// otherwise would only push them to a worse tool. It is an explicit opt-in and the report says on its
// face that it contains message content.
package equiv

import (
	"fmt"
	"sort"
	"strings"
)

// DiffKind classifies why two messages differ at one path.
type DiffKind string

const (
	// ValueDiffers means both sides have the field and disagree on its content. The commonest kind and
	// nearly always a formatting or code-set cause.
	ValueDiffers DiffKind = "value differs"

	// MissingOnRight means the left side populated a field the right side left absent.
	MissingOnRight DiffKind = "missing on the right"

	// MissingOnLeft means the right side populated a field the left side left absent.
	MissingOnLeft DiffKind = "missing on the left"

	// EmptyOnRight and EmptyOnLeft distinguish present-but-empty from absent, because in an update
	// message an empty field means "no change" and an absent one means "never sent". Collapsing them
	// would hide the difference that matters most on exactly the messages where it matters most.
	EmptyOnRight DiffKind = "empty on the right"
	EmptyOnLeft  DiffKind = "empty on the left"

	// RepeatCountDiffers means the field or segment repeats a different number of times. Reported
	// separately from a value difference because the fix is different: a value difference is usually a
	// mapping, a repeat difference is usually a loop.
	RepeatCountDiffers DiffKind = "repeats a different number of times"

	// SegmentMissingOnRight and SegmentMissingOnLeft are whole segments. Kept apart from field
	// differences because one missing segment would otherwise appear as twenty missing fields, which
	// is the same cause reported twenty times.
	SegmentMissingOnRight DiffKind = "segment missing on the right"
	SegmentMissingOnLeft  DiffKind = "segment missing on the left"

	// Unparseable means one side produced something that is not readable as HL7. Always worth its own
	// kind: it is not a mapping problem, it is a broken output, and it is the most serious finding
	// this package can report.
	Unparseable DiffKind = "one side is not parseable"
)

// Serious reports whether a kind means something is broken rather than merely mapped differently.
//
// Used to order the report, because a page that opens with fifty date-format differences buries the one
// message that came out unparseable.
func (k DiffKind) Serious() bool {
	switch k {
	case Unparseable, SegmentMissingOnLeft, SegmentMissingOnRight:
		return true
	default:
		return false
	}
}

// Finding is one cause, however many messages it affected.
type Finding struct {
	// Path is where the difference is, in the same notation filters and transformations use, so a
	// finding can be pasted straight into the fix.
	Path string

	// Kind is why they differ.
	Kind DiffKind

	// Count is how many messages showed this difference.
	Count int

	// Examples are the control IDs of a few affected messages - identifiers, not content - so somebody
	// can go and look at the originals. Capped, because a list of 3,000 ids is not an example.
	Examples []string

	// DistinctPairs is how many different left/right value combinations were seen at this path. One
	// means a single consistent substitution, which is the signature of a code-set or constant
	// difference and usually a one-line fix. Many means it is data-dependent and needs a rule.
	DistinctPairs int

	// Left and Right are sample values, populated only when the comparison was asked to include
	// content. Empty otherwise, and the report says so rather than leaving the reader to wonder.
	Left  string
	Right string
}

// Summary is the headline, which is what most people will read and all they should need to.
type Summary struct {
	// Compared is how many messages were present on both sides and read.
	Compared int

	// Identical is how many produced exactly the same output. The number the whole exercise exists to
	// produce.
	Identical int

	// Differing is how many showed at least one difference.
	Differing int

	// OnlyLeft and OnlyRight are messages one side produced and the other did not. Counted separately
	// from differences because a missing message is a different problem from a wrong one - usually a
	// filter that does not agree, which is worse than a mapping fault and easy to overlook when it is
	// mixed in with field diffs.
	OnlyLeft  []string
	OnlyRight []string
}

// Report is the whole comparison.
type Report struct {
	Summary Summary

	// Findings are causes, ordered with the serious kinds first and then by how many messages each
	// affected. Not ordered by path: a report sorted by path reads like a data structure, and a report
	// sorted by impact reads like advice.
	Findings []Finding

	// IncludesContent records whether values were kept, so a report cannot be circulated without its
	// reader knowing it holds message content.
	IncludesContent bool

	// LeftName and RightName are what to call the two sides in prose, so the report can say "Mirth"
	// and "Perfuse" rather than "A" and "B".
	LeftName  string
	RightName string
}

// Agreed reports whether the two sides matched on every message.
func (r *Report) Agreed() bool {
	return r.Summary.Differing == 0 &&
		len(r.Summary.OnlyLeft) == 0 &&
		len(r.Summary.OnlyRight) == 0
}

// Headline is the one sentence somebody will quote in a meeting.
//
// Deliberately states the denominator. "Three differences" means nothing; "three out of four hundred
// thousand" is the entire argument.
func (r *Report) Headline() string {
	if r.Summary.Compared == 0 {
		return "nothing was compared: no message appeared on both sides"
	}

	if r.Agreed() {
		return fmt.Sprintf("%s and %s produced identical output on all %s messages",
			r.LeftName, r.RightName, count(r.Summary.Compared))
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s and %s agreed on %s of %s messages",
		r.LeftName, r.RightName, count(r.Summary.Identical), count(r.Summary.Compared))

	if r.Summary.Differing > 0 {
		fmt.Fprintf(&b, "; %s differed, from %s",
			count(r.Summary.Differing), plural(len(r.Findings), "cause", "causes"))
	}
	if n := len(r.Summary.OnlyLeft); n > 0 {
		fmt.Fprintf(&b, "; %s produced %s the other did not", r.LeftName, plural(n, "message", "messages"))
	}
	if n := len(r.Summary.OnlyRight); n > 0 {
		fmt.Fprintf(&b, "; %s produced %s the other did not", r.RightName, plural(n, "message", "messages"))
	}

	return b.String()
}

// sortFindings orders findings by seriousness, then by how many messages each affected.
//
// Ties break on path so the output is stable. These reports get diffed between runs to see whether a
// fix worked, and an unstable order makes that diff useless.
func sortFindings(findings []Finding) {
	sort.SliceStable(findings, func(i, j int) bool {
		a, b := findings[i], findings[j]
		if a.Kind.Serious() != b.Kind.Serious() {
			return a.Kind.Serious()
		}
		if a.Count != b.Count {
			return a.Count > b.Count
		}
		if a.Path != b.Path {
			return a.Path < b.Path
		}
		return a.Kind < b.Kind
	})
}

func count(n int) string {
	// Thousands separators, because the headline is meant to be read aloud and 399997 is not.
	s := fmt.Sprintf("%d", n)
	if len(s) <= 3 {
		return s
	}

	var parts []string
	for len(s) > 3 {
		parts = append([]string{s[len(s)-3:]}, parts...)
		s = s[:len(s)-3]
	}
	return s + "," + strings.Join(parts, ",")
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%s %s", count(n), many)
}
