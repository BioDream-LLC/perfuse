package config

import "fmt"

// Which format-specific options block belongs to which data type.
//
// # Why this is a table
//
// Each format has its own block - hl7v3, x12, delimited, ncpdp, script, dicom - and a block set on a channel of another type is
// always a mistake. It is a mistake worth refusing rather than ignoring, because the block is read by code that never runs on the
// wrong format, so the channel loads, reports success, and quietly does none of what the block asked for.
//
// These refusals used to be written inside the switch on data type, one arm at a time. That shape has a specific failure: a new
// block has to be added to every arm, and the arm somebody forgets accepts it silently. Two arms never had any block refusals at all
// - hl7v3 returned early and x12 fell through to the envelope checks - so nothing there refused anything.
//
// The cost was measured rather than assumed. A probe that loaded every block against every data type found eight wrong
// acceptances: an ncpdp block was accepted on hl7, dicom, raw, delimited and x12 channels; a delimited block on dicom and x12; and
// an hl7v3 block on x12. Every one of those loaded, validated, and would have done nothing.
//
// A table cannot have that failure. Adding a block means adding one row, and every data type that is not its owner is refused by
// construction. The exhaustiveness is also tested rather than trusted, in TestEveryFormatBlockIsRefusedOnEveryOtherType.
//
// # Why the messages are still bespoke
//
// The obvious way to write this loses something. A generic "wrong data type" message would be correct and would throw away the
// explanations, and the explanations are the useful part: telling somebody that a v3 block on a SCRIPT channel is wrong because
// both are XML but address entirely different element trees is what stops them trying the next XML-shaped thing. So the table
// carries the specific reason where one exists, and falls back to a generic message where the mismatch needs no explanation.
type formatBlock struct {
	// name is the yaml key, used in the message.
	name string

	// owner is the only data type that reads this block.
	owner DataType

	// present reports whether the channel sets the block.
	present func(*Channel) bool

	// because carries the specific reason a mismatch is confusing, for the pairs where a generic message would waste the
	// chance to explain. Absent means the generic message.
	because map[DataType]string
}

// bothXMLDifferentTrees is shared because the confusion is the same for both pharmacy types.
const bothXMLDifferentTrees = "both are XML but they address entirely different element trees, so an hl7v3 filter here would " +
	"compile and never match"

// formatBlocks is every format-specific block and the type that honours it.
var formatBlocks = []formatBlock{
	{
		name:    "hl7v3",
		owner:   DataHL7v3,
		present: func(c *Channel) bool { return c.HL7v3 != nil },
		because: map[DataType]string{
			DataNCPDP:  bothXMLDifferentTrees,
			DataScript: bothXMLDifferentTrees,
		},
	},
	{
		name:    "x12",
		owner:   DataX12,
		present: func(c *Channel) bool { return c.X12 != nil },
	},
	{
		name:    "delimited",
		owner:   DataDelimited,
		present: func(c *Channel) bool { return c.Delimited != nil },
		because: map[DataType]string{
			DataRaw:    "if the payload is a CSV whose rows should each become a message, use dataType: delimited instead",
			DataNCPDP:  "the separators are part of the standard rather than something to configure",
			DataScript: "the separators are part of the standard rather than something to configure",
		},
	},
	{
		name:    "ncpdp",
		owner:   DataNCPDP,
		present: func(c *Channel) bool { return c.NCPDP != nil },
		because: map[DataType]string{
			DataScript: "a prescription is XML and has none of a claim's fields",
		},
	},
	{
		name:    "script",
		owner:   DataScript,
		present: func(c *Channel) bool { return c.Script != nil },
	},
	{
		name:    "dicom",
		owner:   DataDICOM,
		present: func(c *Channel) bool { return c.Imaging != nil },
		because: map[DataType]string{
			// Worth saying, because this is the one whose consequence is not merely a rule that does nothing. A
			// de-identify step that never runs means identifiers leave the hospital, and the channel reports success.
			DataHL7: "the imaging steps act on tags in a binary object, and an HL7 message has none - so a de-identify " +
				"step configured here would never run and nothing would report that",
		},
	},
}

// refuseForeignBlocks refuses every format block set on a channel of another type.
func (c *Channel) refuseForeignBlocks() []error {
	var errs []error

	for _, b := range formatBlocks {
		if !b.present(c) || c.Type() == b.owner {
			continue
		}

		if why, ok := b.because[c.Type()]; ok {
			errs = append(errs, fmt.Errorf("a %s block is set but dataType is %s; %s", b.name, c.Type(), why))

			continue
		}

		errs = append(errs, fmt.Errorf("a %s block is set but dataType is %q; either set dataType: %s or remove the block",
			b.name, c.Type(), b.owner))
	}

	return errs
}
