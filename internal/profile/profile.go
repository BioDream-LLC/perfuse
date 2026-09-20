package profile

// What is actually in a feed.
//
// Everybody building an interface works from a specification describing what the sending system
// is supposed to send. The specification is usually years old, was written for a different
// version, and omits the three Z-segments the site added. So the real shape of a feed is
// discovered in production, one surprise at a time: a field that is populated in 96% of messages
// and empty in the rest, a repetition nobody expected, a code outside the table.
//
// This reads a corpus and reports the shape. It is the answer to "what am I actually receiving",
// and it is the foundation for two other things: noticing when a sender changes (drift), and
// inferring a mapping by finding fields whose values overlap between two feeds.
//
// # The safety constraint that shapes the whole design
//
// A profile of real traffic must not become a way to read patient data. So this reports counts,
// fill rates, cardinality and shapes - never values - with one deliberate exception: a field the
// HL7 dictionary says is constrained by a code table has its values reported, because a code
// table value is not identifying and knowing which codes a sender actually uses is the single
// most useful thing in the report.
//
// Everything else is described rather than quoted. "PID-5 is populated in 100% of messages, two
// components, always text" tells an integrator what they need without putting a patient's name
// on a screen that somebody will screenshot into a ticket.

// Shape describes what a field's values look like, without quoting them.
type Shape string

const (
	// ShapeEmpty means the field was never populated in the corpus.
	ShapeEmpty Shape = "empty"
	// ShapeNumeric means every value was digits only. Often an identifier, and worth
	// knowing because a receiver that parses it as a number will fail the day one is not.
	ShapeNumeric Shape = "numeric"
	// ShapeAlpha means letters only.
	ShapeAlpha Shape = "alphabetic"
	// ShapeAlphanumeric mixes both.
	ShapeAlphanumeric Shape = "alphanumeric"
	// ShapeDate looks like an HL7 timestamp: 8 or 14 digits.
	ShapeDate Shape = "date or timestamp"
	// ShapeMixed means the values do not share a shape, which is usually the interesting
	// answer: a field the specification calls numeric that is sometimes not.
	ShapeMixed Shape = "mixed"
)

// Field is what was observed at one path.
type Field struct {
	// Path is in the notation used everywhere else: PID-5, PID-5.1.
	Path string `json:"path"`

	// Name is from the HL7 dictionary, when it is a field the standard defines.
	Name string `json:"name,omitempty"`

	// Present is how many messages had this field populated.
	Present int `json:"present"`
	// FillRate is Present as a fraction of the messages that contained the segment. It is
	// deliberately relative to the segment rather than the corpus: OBX-5 being populated in
	// 30% of messages means something quite different from it being populated in 30% of
	// messages that have an OBX at all.
	FillRate float64 `json:"fillRate"`

	// Distinct is how many different values were seen, capped. This is the number that tells
	// an integrator what kind of field it is: two distinct values is a flag, thirty is a
	// code set, one per message is an identifier.
	Distinct int `json:"distinct"`
	// DistinctCapped says counting stopped, so Distinct is a floor.
	DistinctCapped bool `json:"distinctCapped,omitempty"`

	// Shape describes the values without quoting them.
	Shape Shape `json:"shape"`

	// MinLength and MaxLength bound what was seen. A receiver with a fixed-width column
	// needs MaxLength, and finding it here beats finding it in a truncation.
	MinLength int `json:"minLength"`
	MaxLength int `json:"maxLength"`

	// MaxRepeats is the most repetitions seen in one message. Anything above one is worth
	// knowing, because code written against a single value silently takes the first.
	MaxRepeats int `json:"maxRepeats"`

	// Components is the most components seen, for a composite field.
	Components int `json:"components,omitempty"`

	// Codes lists the values, but only for a field the dictionary says is table-constrained.
	// A code table value is not identifying, and which codes a sender actually uses is the
	// most useful single fact in a profile.
	Codes []CodeCount `json:"codes,omitempty"`

	// Table is the HL7 table number, when the dictionary names one.
	Table string `json:"table,omitempty"`
}

// CodeCount is one observed code and how often it appeared.
type CodeCount struct {
	Code  string `json:"code"`
	Count int    `json:"count"`
	// Meaning is the dictionary's description of the code, when it knows it. A code the
	// table does not define is the interesting case: it means the sender is using a local
	// value, and anything downstream mapping strictly will reject it.
	Meaning string `json:"meaning,omitempty"`
	// Known is false when the table does not define this code.
	Known bool `json:"known"`
}

// Segment is what was observed of one segment.
type Segment struct {
	// ID is the three-character segment identifier.
	ID string `json:"id"`
	// Name is from the dictionary, empty for a segment the standard does not define.
	Name string `json:"name,omitempty"`
	// Standard is false for a Z-segment or anything else not in the dictionary. These are
	// the ones a specification never mentions and an integration always has to handle.
	Standard bool `json:"standard"`

	// Messages is how many messages contained this segment at least once.
	Messages int `json:"messages"`
	// Rate is Messages as a fraction of the corpus.
	Rate float64 `json:"rate"`
	// MaxPerMessage is the most occurrences in one message.
	MaxPerMessage int `json:"maxPerMessage"`

	// Fields are the populated paths within it, in field order.
	Fields []Field `json:"fields"`
}

// Report is the profile of a corpus.
type Report struct {
	// Messages is how many were read successfully.
	Messages int `json:"messages"`
	// Unreadable is how many could not be parsed. Reported rather than hidden: a corpus with
	// many is not a corpus to draw conclusions from.
	Unreadable int `json:"unreadable"`

	// Types counts message types, which is often the first surprise. A feed described as
	// "ADT" routinely carries six trigger events, two of which nobody was told about.
	Types []TypeCount `json:"types"`

	// Segments are in the order first seen, which is roughly the order they appear in a
	// message and therefore the order somebody reads them.
	Segments []Segment `json:"segments"`

	// Notes are observations worth stating in words, because a number does not carry them.
	Notes []string `json:"notes,omitempty"`
}

// TypeCount is one message type and how often it appeared.
type TypeCount struct {
	Type  string  `json:"type"`
	Count int     `json:"count"`
	Rate  float64 `json:"rate"`
}

// maxDistinct caps distinct-value counting per field.
//
// Beyond this the answer is "effectively unique", which is all anybody needs: the difference
// between a field with 900 distinct values and one with 9,000 does not change a decision, and
// counting without a cap means holding every medical record number in the corpus in a map.
const maxDistinct = 500

// maxCodes bounds the reported code list.
const maxCodes = 40
