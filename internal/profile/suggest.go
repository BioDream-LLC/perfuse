package profile

import (
	"fmt"
	"sort"
	"strings"
)

// Working out what a field probably is, from how it behaves.
//
// The statistics needed for this already existed: fill rate, distinct count, value shape, length bounds, repetitions. What was
// missing is the step that turns them into something actionable - "PID-3.1 is populated on every message, unique per patient,
// eight to ten digits: this is almost certainly the medical record number".
//
// # This proposes and never applies
//
// A confident wrong mapping in clinical data is worse than no mapping, because somebody accepts it. Nothing here writes a
// channel, and every suggestion carries the evidence it was drawn from so a person can disagree with the reasoning rather than
// only with the conclusion.
//
// # And it does not invent a confidence figure
//
// No model, no score out of a hundred. Every number reported here is counted from the sample: how many messages carried the
// field, how many distinct values appeared, how many matched the pattern. A percentage that is really a heuristic dressed up is
// worse than no percentage, because it converts a guess into an authority - and the person reading it has no way to tell which
// they are looking at.

// Role is what a field appears to be for.
type Role string

const (
	// RoleIdentifier is unique or near-unique per message: a record number, an accession, an order number.
	RoleIdentifier Role = "identifier"

	// RoleCodeSet is a small fixed vocabulary: a gender, a status, a priority.
	RoleCodeSet Role = "code set"

	// RoleFlag is two values, usually Y and N.
	RoleFlag Role = "flag"

	// RoleTimestamp is a date or a date and time.
	RoleTimestamp Role = "date or timestamp"

	// RoleFreeText is prose or a name: many distinct values, no pattern worth relying on.
	RoleFreeText Role = "free text"

	// RoleConstant is the same value on every message, which usually means a sending facility or a version.
	RoleConstant Role = "constant"

	// RoleUnused was never populated in the sample.
	RoleUnused Role = "never populated"
)

// Suggestion is what one field appears to be, and why.
type Suggestion struct {
	// Path is the field, in the notation used everywhere else.
	Path string `json:"path"`

	// Name is the standard's name for it, when it has one. Reported alongside the inferred role rather than instead
	// of it, because the two disagreeing is itself worth seeing: a field the standard calls "patient account number"
	// carrying a constant means this sender does not use it for that.
	Name string `json:"name,omitempty"`

	// Role is what the values behave like.
	Role Role `json:"role"`

	// Evidence is the counted facts the role was drawn from, in plain words.
	//
	// The most important field here. A reader who disagrees with the conclusion needs to see the reasoning, and a
	// suggestion without it can only be accepted or rejected on faith.
	Evidence []string `json:"evidence"`

	// Agreement is the fraction of populated values that fit the role, between 0 and 1.
	//
	// Counted, not estimated. For a code set it is the share of values inside the observed vocabulary; for a timestamp
	// it is the share that parsed as one. A value of 1 means every value in the sample fitted, which is a statement
	// about the sample and not a promise about tomorrow - which is why Sampled is reported next to it.
	Agreement float64 `json:"agreement"`

	// Sampled is how many messages this was drawn from, so the reader can weigh it.
	//
	// Next to the agreement deliberately. "Every value fitted" means one thing over four hundred messages and almost
	// nothing over three, and a figure without its denominator invites the second to be read as the first.
	Sampled int `json:"sampled"`

	// Vocabulary lists the observed values for a code set or flag, sorted.
	//
	// Only for small vocabularies. A field with two hundred distinct values does not have a vocabulary worth listing,
	// and listing it would put patient data into a document meant to describe structure.
	Vocabulary []string `json:"vocabulary,omitempty"`

	// Caution is set when the suggestion should not be leaned on, and says why.
	Caution string `json:"caution,omitempty"`
}

// SuggestOptions bounds what is reported.
type SuggestOptions struct {
	// MinSample refuses to guess from too little. Defaults to 20.
	//
	// Not zero, and not one. Every field in a sample of three looks like a constant or an identifier, and a suggestion
	// drawn from three messages is a coin toss presented as an analysis.
	MinSample int

	// MaxVocabulary is the largest value list to report. Defaults to 25.
	MaxVocabulary int
}

func (o SuggestOptions) withDefaults() SuggestOptions {
	if o.MinSample <= 0 {
		o.MinSample = 20
	}
	if o.MaxVocabulary <= 0 {
		o.MaxVocabulary = 25
	}

	return o
}

// Suggest infers a role for every field in a report.
//
// Returns nothing at all when the sample is too small, rather than returning weak suggestions with a caveat. A caveat on a page
// of confident-looking rows is read once and then ignored; an empty result with an explanation cannot be misread.
func Suggest(r *Report, opts SuggestOptions) ([]Suggestion, string) {
	opts = opts.withDefaults()

	if r == nil {
		return nil, "there is no profile to draw from"
	}
	if r.Messages < opts.MinSample {
		return nil, fmt.Sprintf(
			"only %d message(s) were analysed, and at least %d are needed before this is worth doing. "+
				"Every field in a small sample looks like a constant or an identifier",
			r.Messages, opts.MinSample)
	}

	var out []Suggestion

	for _, seg := range r.Segments {
		for _, f := range seg.Fields {
			out = append(out, suggestField(f, seg, opts))
		}
	}

	// Deliberately not sorted by path.
	//
	// The report's own order is already deterministic - segments in the order they were first seen, fields by number -
	// and sorting by path made it worse rather than safer: as strings, MSH-10 comes before MSH-3, so a sorted page
	// read MSH-10, MSH-11, MSH-12, MSH-3. A plant that removed the sort did not fire, which is what prompted looking
	// at the output, and the output was better without it.
	//
	// Reproducibility is what mattered and it was already there. The sort was solving a problem this data does not
	// have, at the cost of an ordering no reader wants.

	return out, ""
}

// suggestField works out one field's role.
//
// The order of the tests is the substance. Never populated first, because everything else would misread an empty field. Then a
// constant, then a flag, then a timestamp, then a code set, then an identifier, then free text - each test is narrower than the
// one after it, so a field that could be read two ways gets the more specific reading.
func suggestField(f Field, seg Segment, opts SuggestOptions) Suggestion {
	// Cautions accumulate rather than replace.
	//
	// More than one can be true at once - an eight-digit identifier is both ambiguous with a date and unsafe to
	// store as a number - and the first version had each branch assign over the last, so only one was ever
	// reported. Which one depended on the order of the checks, which is not a basis for deciding what a reader is
	// told.
	var cautions []string

	s := Suggestion{
		Path:    f.Path,
		Name:    f.Name,
		Sampled: seg.Messages,
	}

	if f.Present == 0 {
		s.Role = RoleUnused
		s.Evidence = []string{fmt.Sprintf("never populated in %d message(s) carrying %s",
			seg.Messages, seg.ID)}
		s.Agreement = 1

		return s
	}

	fill := fmt.Sprintf("populated in %d of %d message(s) carrying %s (%.0f%%)",
		f.Present, seg.Messages, seg.ID, f.FillRate*100)

	distinct := fmt.Sprintf("%d distinct value(s)", f.Distinct)
	if f.DistinctCapped {
		distinct = fmt.Sprintf("at least %d distinct value(s); counting stopped there", f.Distinct)
	}

	switch {
	case f.Distinct == 1:
		s.Role = RoleConstant
		s.Evidence = []string{fill, "the same value on every message"}
		s.Agreement = 1
		// Worth flagging rather than reporting flatly. A field that is constant across the sample is very often
		// constant because the sample came from one sending system on one day.
		cautions = append(cautions, "a value that never varies in a sample often varies between senders "+
			"or over time; check against a second source before treating it as fixed")

	case f.Distinct == 2 && f.MaxLength <= 3:
		s.Role = RoleFlag
		s.Evidence = []string{fill, distinct, fmt.Sprintf("values are at most %d character(s)", f.MaxLength)}
		s.Agreement = 1

	// A date-shaped value that is different on every message is an identifier, not a date.
	//
	// This is the ambiguity that matters most in HL7 and it is genuinely hard: an eight-digit medical record
	// number and a YYYYMMDD birth date are indistinguishable by shape. The first version of this file put the
	// date test first and confidently reported every 8-digit MRN as a date of birth.
	//
	// Uniqueness is what separates them, and it separates them well. Dates repeat - in any real corpus a great
	// many patients share a birth date, and a date field with one distinct value per message over a hundred
	// messages would be a remarkable coincidence. So uniqueness is treated as the stronger signal, and the
	// ambiguity is stated rather than resolved silently, because the cost of being wrong falls on whoever writes
	// the mapping.
	case f.Shape == ShapeDate && f.Distinct >= f.Present && f.Present > 1:
		s.Role = RoleIdentifier
		s.Evidence = []string{
			fill,
			"a different value on every message that carries it",
			fmt.Sprintf("%d to %d digits, which is also the shape of a date", f.MinLength, f.MaxLength),
		}
		s.Agreement = 1
		cautions = append(cautions, "these values have the shape of a date but a different one on every "+
			"message, so this is more likely an identifier than a date. An eight-digit record number "+
			"and a YYYYMMDD date cannot be told apart by shape alone - check one real value before "+
			"mapping it")
		cautions = append(cautions, numericIdentifierCaution)

	case f.Shape == ShapeDate:
		s.Role = RoleTimestamp
		s.Evidence = []string{fill, distinct, "every value has the shape of an HL7 date or timestamp"}
		s.Agreement = 1

	case f.Distinct <= opts.MaxVocabulary && !f.DistinctCapped && f.Distinct < f.Present:
		s.Role = RoleCodeSet
		s.Evidence = []string{
			fill,
			distinct,
			fmt.Sprintf("values repeat across messages, which is what a vocabulary does and an identifier " +
				"does not"),
		}
		s.Agreement = 1

	case f.Distinct >= f.Present && f.Present > 1:
		s.Role = RoleIdentifier
		s.Evidence = []string{
			fill,
			"a different value on every message that carries it",
			fmt.Sprintf("%d to %d characters, %s", f.MinLength, f.MaxLength, f.Shape),
		}
		s.Agreement = 1
		if f.Shape == ShapeNumeric {
			cautions = append(cautions, numericIdentifierCaution)
		}

	default:
		s.Role = RoleFreeText
		s.Evidence = []string{fill, distinct, fmt.Sprintf("%d to %d characters, %s",
			f.MinLength, f.MaxLength, f.Shape)}
		s.Agreement = 1
	}

	// The observed vocabulary, for the roles where it is short enough to mean something.
	if s.Role == RoleCodeSet || s.Role == RoleFlag || s.Role == RoleConstant {
		if len(f.Codes) > 0 && len(f.Codes) <= opts.MaxVocabulary {
			for _, c := range f.Codes {
				// A code the dictionary does not define is marked, because it is the interesting one:
				// it means the sender is using a local value, and anything downstream mapping strictly
				// will reject it.
				if c.Known {
					s.Vocabulary = append(s.Vocabulary, c.Code)

					continue
				}
				s.Vocabulary = append(s.Vocabulary, c.Code+" (not in the standard table)")
			}
			sort.Strings(s.Vocabulary)
		}
	}

	// A field the standard names, behaving unlike its name, is worth saying out loud.
	if note := disagreesWithName(f, s.Role); note != "" {
		cautions = append(cautions, note)
	}

	s.Caution = strings.Join(cautions, ". ")

	return s
}

// numericIdentifierCaution is the warning for an identifier whose values are all digits.
//
// Written once and referenced twice, because it applies to a plainly numeric identifier and to one whose digits also happen to
// look like a date. Two copies of the same sentence would eventually be two different sentences.
//
// Worth warning about because it fails late and expensively: the leading zeros are lost silently, and the first value that is not
// numeric breaks a receiver that has been working for months.
const numericIdentifierCaution = "the values are all digits in this sample, but an identifier is a string: a receiver " +
	"that stores it as a number will lose leading zeros and fail on the first value that is not numeric"

// disagreesWithName reports a mismatch between what the standard calls a field and how it behaves.
//
// The most useful single output of this whole file. A field the standard calls a date carrying free text, or one called an
// identifier carrying a constant, means this sender is using it for something else - and that is exactly the thing that takes an
// afternoon to discover by reading messages.
func disagreesWithName(f Field, role Role) string {
	name := strings.ToLower(f.Name)
	if name == "" {
		return ""
	}

	switch {
	case strings.Contains(name, "date") || strings.Contains(name, "time"):
		if role != RoleTimestamp && role != RoleUnused {
			return fmt.Sprintf("the standard calls this %q, but the values do not look like dates; this "+
				"sender may be using the field for something else", f.Name)
		}

	case strings.Contains(name, "identifier") || strings.Contains(name, " id") ||
		strings.HasSuffix(name, " number"):
		if role == RoleConstant {
			return fmt.Sprintf("the standard calls this %q, but every message carries the same value, so "+
				"it is not identifying anything here", f.Name)
		}
	}

	return ""
}
