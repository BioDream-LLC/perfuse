// Binding the filter grammar to a message format.
//
// # Why this exists
//
// The grammar - and, or, not, parentheses, ==, !=, >, <, =~, matches, exists, empty, in - has nothing to do with HL7. It
// is the same grammar a person wants for X12, for a pharmacy claim, for a delimited row. Only one operation differs
// between formats: given a path, produce the values it addresses.
//
// Before this, that grammar existed twice. internal/expr implemented it for v2 and internal/hl7v3 implemented it again
// for v3, with a comment explaining that teaching the first about v3 would mean changing a signature everywhere it
// appeared. That was true, and the cost of the workaround was a second 700-line evaluator that has to be kept in step by
// hand. A third and fourth copy for X12 and the pharmacy formats would have been worse.
//
// # Why the path is prepared once rather than parsed per evaluation
//
// The obvious generic interface takes a path string and resolves it on the spot. That would parse "MSH-9.1" again for
// every message. Measured on the existing benchmark, evaluation of a three-path expression is about 180ns, and parsing
// the paths inside it would roughly triple that.
//
// So a path is prepared at parse time and the expression holds a closure. The cost is one indirect call per path per
// message, and the benchmark is unchanged.
package expr

// Value is one value a path addresses.
type Value struct {
	// Text is the value as the message carries it.
	Text string

	// Empty reports whether the format considers it to have no content.
	//
	// Kept separate from Text being empty rather than folded into it, because HL7's explicit null is two double quote
	// characters: it has text and it has no content. A filter has to be able to tell, and normalising it to an empty
	// string would silently change what == "" matches.
	Empty bool
}

// Prepared is a path compiled against one message format.
type Prepared[M any] struct {
	// Candidates returns every value the path addresses.
	//
	// More than one because a path that names no repetition addresses all of them. PID-3.4 asks about the assigning
	// authority of the patient identifier list, and a message carrying both an MRN and a social security number has
	// two. Reading only the first would make a filter miss the identifier it was written to find.
	//
	// An empty result means the path addressed nothing, which the comparison operators treat as the empty string so
	// that a test for a missing field needs no separate keyword.
	Candidates func(M) []Value

	// Presence answers the exists and empty questions without materialising any text.
	//
	// count is how many values the path addresses; allEmpty reports whether every one of them has no content, and is
	// true when there are none.
	//
	// Its own closure rather than deriving both from Candidates, because neither question needs the text and making them
	// pay for it was measurable: on the existing three-path benchmark, folding them into Candidates cost 16% and two
	// allocations per message. A filter runs on every message of every channel, so that is worth an extra field.
	Presence func(M) (count int, allEmpty bool)

	// Single returns the one value the path addresses, for ordering comparisons.
	//
	// Separate from Candidates because greater-than over a repeating field has no useful meaning: a field carrying two
	// numbers is not greater or less than anything. Equality considers every repetition, ordering considers one.
	Single func(M) string
}

// Operator is a comparison keyword one format adds to the shared grammar.
//
// # Why this exists
//
// The v2, v3 and X12 grammars are otherwise identical. v3 has exactly one keyword the others do not: nullflavor, which
// asks whether an element carries a particular HL7 v3 null flavour - ASKU for asked but unknown, NI for no information.
// It is genuinely v3-only. X12 has no such concept and never will.
//
// The choices were to put a v3 keyword in the shared grammar where it means nothing to anyone else, to drop it and make
// every existing v3 filter that uses it stop parsing, or to let a format contribute one. This is the third.
//
// Deliberately narrow: a keyword, a path on its left, a quoted value on its right. That is the shape of every operator
// in the grammar, so an extension cannot introduce syntax a reader has not already seen.
type Operator[M any] struct {
	// Keyword introduces the operator. Matched after the shared keywords, so a format cannot redefine exists or in.
	Keyword string

	// Compile builds the test from the path on the left and the quoted value on the right.
	//
	// Called at parse time, so a bad path or value refuses the channel rather than failing per message.
	Compile func(path, value string) (func(M) (bool, error), error)
}

// Extender is an optional interface a Resolver may implement to add operators.
//
// Optional rather than part of Resolver, so a format with nothing to add - which is most of them - writes nothing.
type Extender[M any] interface {
	Operators() []Operator[M]
}

// Semantics are the places where formats legitimately disagree about what a comparison means.
//
// Every field here was found by unifying the v2 and v3 grammars, which were otherwise identical. Each is a deliberate,
// tested choice in one of them, and none could be dropped without changing the behaviour of filters already written. They
// are collected in one struct rather than spread across interfaces so that the whole of the disagreement is visible in one
// place, and so adding a format does not add another interface.
//
// The zero value is the v2 and X12 behaviour, so a format with no opinion writes nothing.
type Semantics struct {
	// OrderAsText compares >, <, >= and <= as text rather than as numbers.
	//
	// Numeric is right for v2 and X12, where an ordered value is a quantity - a charge, a count, an age.
	//
	// v3 needs text: nearly every ordered value in v3 is a timestamp written YYYYMMDD or YYYYMMDDHHMMSS, where text order
	// and time order are the same thing. Numeric comparison would break the moment a value carried a timezone offset,
	// because 19551014-0500 is not a number and the comparison would silently answer false for that sender's messages.
	OrderAsText bool

	// AbsentMatchesNothing makes a path that addresses nothing fail every comparison except !=.
	//
	// v2 does the opposite on purpose: an unaddressed path compares as the empty string, so PID-8 == "" tests for a
	// missing field without needing another keyword.
	//
	// v3 rejects that, also on purpose. An absent element is a different clinical fact from an empty one - the whole
	// reason the hl7v3 package exists - so a missing deceasedTime must not match == "" or matches ".*". != stays true,
	// because whatever an absent field holds is certainly not the value asked about, and anybody who means "present and
	// not F" writes it as two clauses.
	AbsentMatchesNothing bool

	// RequireQuotedValues refuses a bare word on the right of a comparison.
	//
	// v2 allows MSH-9.2 == A08 as a convenience and has a test that says so. v3 requires quotes, which catches the case
	// where somebody meant a path and wrote a word, or misspelled a keyword - both of which otherwise compile into a
	// comparison against a literal and a filter that never matches.
	RequireQuotedValues bool
}

// Opinionated is an optional interface a Resolver may implement to state its comparison semantics.
//
// Optional rather than part of Resolver, so a format that agrees with the defaults - which is most of them - writes
// nothing.
type Opinionated interface {
	Semantics() Semantics
}

// Resolver binds the grammar to a message format by compiling its paths.
type Resolver[M any] interface {
	// Prepare compiles one path, or reports why it cannot.
	//
	// Called at parse time, so a filter naming a path the format cannot address refuses to load rather than failing on
	// the first message - which for a source connector means the first message off the wire, with the operator who
	// deployed it no longer watching.
	Prepare(path string) (Prepared[M], error)
}
