package config

import (
	"fmt"
	"sort"
	"strings"
)

// scriptSlot names one of the places a script can be attached.
type scriptSlot string

const (
	slotFilter        scriptSlot = "filter"
	slotTransformer   scriptSlot = "transformer"
	slotPreprocessor  scriptSlot = "preprocessor"
	slotPostprocessor scriptSlot = "postprocessor"
	slotDeploy        scriptSlot = "deploy"
	slotUndeploy      scriptSlot = "undeploy"
)

// allScriptSlots is every slot, in the order a message meets them.
//
// Ordered rather than alphabetical so an error listing several reads in the sequence somebody thinks about them: repair the text,
// decide whether to keep it, change it, react to what happened. Deploy and undeploy sit outside a message entirely and come last.
var allScriptSlots = []scriptSlot{
	slotPreprocessor, slotFilter, slotTransformer, slotPostprocessor, slotDeploy, slotUndeploy,
}

// scriptSlotsRun records which script slots each data type actually executes.
//
// # Why this table exists
//
// It replaces per-format refusals written by hand, and the reason is a defect found in shadow mode with exactly this shape.
// There, some data type branches refused a shadow and others did not, the omissions were the branches nobody had thought about,
// and five formats accepted a shadow block that validated and never compared anything.
//
// Scripts had the same problem in a subtler form: the refusals were all-or-nothing. A channel either ran every script or was
// told it could run none. But a preprocessor needs no parsed message - it reads text and returns text, before parsing, which is
// the entire reason sites need one. So an X12 interchange from a partner who prefixes a byte order mark could not be repaired,
// not because repairing it was hard, but because the refusal was coarser than the truth. One refusal said so in as many words:
// "A preprocessor would be meaningful on a raw payload."
//
// # What a name here means
//
// A slot listed for a data type means that format's handler really invokes it, and TestEveryDataTypeEitherRunsAScriptSlotOrRefusesIt
// asserts there is no third state. Anything absent is refused at load, naming the slots that do work.
//
// # Why deploy and undeploy are everywhere
//
// They run against the channel rather than a message, at start and stop, so they have no dependency on a parsed form. A site
// warming a lookup cache or checking a partner endpoint is reachable needs that on a claims channel as much as an admissions one.
var scriptSlotsRun = map[DataType]map[scriptSlot]bool{
	// HL7 v2 runs everything. It is the format the script layer was built for.
	DataHL7: {
		slotPreprocessor: true, slotFilter: true, slotTransformer: true,
		slotPostprocessor: true, slotDeploy: true, slotUndeploy: true,
	},

	// v3 runs everything too. Its documents are already XML trees, which is what the script API works on, so the filter and
	// transformer needed no conversion - see runTreeScriptStage.
	DataHL7v3: {
		slotPreprocessor: true, slotFilter: true, slotTransformer: true,
		slotPostprocessor: true, slotDeploy: true, slotUndeploy: true,
	},

	// X12 runs everything.
	//
	// The filter and transformer address it by path - CLM01, the same paths the declarative filter and steps use - rather than
	// as a tree. That was the thing missing, and the reason it took a while is that a tree was the wrong answer: it would have
	// meant inventing an XML shape for a format that has its own vocabulary, and a tree walker applying changes afterwards
	// would have to relearn the invariants x12.Set already enforces, like re-padding a fixed-width ISA element.
	DataX12: {
		slotPreprocessor: true, slotFilter: true, slotTransformer: true,
		slotPostprocessor: true, slotDeploy: true, slotUndeploy: true,
	},

	// NCPDP and delimited run everything too, through the same path binding.
	//
	// A claim is addressed by the standard's own two-character field identifiers - A3, 07-D7 - and a delimited row by column
	// name or #position. Both already had a steps.Accessor for the declarative transformations, and the script stage is generic
	// over that interface, so this was wiring rather than design.
	DataNCPDP: {
		slotPreprocessor: true, slotFilter: true, slotTransformer: true,
		slotPostprocessor: true, slotDeploy: true, slotUndeploy: true,
	},
	DataDelimited: {
		slotPreprocessor: true, slotFilter: true, slotTransformer: true,
		slotPostprocessor: true, slotDeploy: true, slotUndeploy: true,
	},

	// SCRIPT runs everything, through the tree binding rather than the path one.
	//
	// A prescription is XML, so it gets what v3 gets: the script sees the document itself and addresses it the way Mirth's
	// scripts do. Paths were the wrong offer here - there are no two-character field identifiers in a prescription - and that
	// is why this waited for the tree binding instead of following X12 and NCPDP.
	//
	// The binding turned out to already exist. runTreeScriptStage takes an xtree.Node and knows nothing about the format; it
	// was called runV3ScriptStage while v3 was its only caller, which read as though the stage were v3-specific. That name is
	// the most likely reason this gap stayed open as long as it did.
	DataScript: {
		slotPreprocessor: true, slotFilter: true, slotTransformer: true,
		slotPostprocessor: true, slotDeploy: true, slotUndeploy: true,
	},

	// Raw runs the text-level and lifecycle scripts only. It has no addressable structure at all, so a filter there could only
	// ask about the bytes, which the expression filter cannot do either. Unlike SCRIPT this is not waiting on a binding: there
	// is nothing to bind to.
	DataRaw: {
		slotPreprocessor: true, slotPostprocessor: true, slotDeploy: true, slotUndeploy: true,
	},

	// DICOM gets no preprocessor. An object is binary with pixel data in it, and a script editing it as text will corrupt an
	// image - the same reason DICOM gets named transformation steps rather than a path writer. Postprocessing is safe because
	// it cannot change anything.
	DataDICOM: {
		slotPostprocessor: true, slotDeploy: true, slotUndeploy: true,
	},
}

// scriptSlotsSet reports which slots a channel has filled.
func (s *Scripts) scriptSlotsSet() []scriptSlot {
	if s == nil {
		return nil
	}

	filled := map[scriptSlot]string{
		slotFilter:        s.Filter,
		slotTransformer:   s.Transformer,
		slotPreprocessor:  s.Preprocessor,
		slotPostprocessor: s.Postprocessor,
		slotDeploy:        s.Deploy,
		slotUndeploy:      s.Undeploy,
	}

	var out []scriptSlot
	for _, slot := range allScriptSlots {
		if strings.TrimSpace(filled[slot]) != "" {
			out = append(out, slot)
		}
	}

	return out
}

// validateScriptSlots refuses a script in a slot this data type does not run.
//
// Named per slot rather than all-or-nothing, so a channel with a working preprocessor and an unsupported transformer is told
// about the transformer instead of being refused wholesale.
func (c *Channel) validateScriptSlots() []error {
	set := c.Scripts.scriptSlotsSet()
	if len(set) == 0 {
		return nil
	}

	runs := scriptSlotsRun[c.Type()]

	var refused []string
	for _, slot := range set {
		if !runs[slot] {
			refused = append(refused, string(slot))
		}
	}
	if len(refused) == 0 {
		return nil
	}
	sort.Strings(refused)

	var supported []string
	for _, slot := range allScriptSlots {
		if runs[slot] {
			supported = append(supported, string(slot))
		}
	}

	subject := "scripts are"
	if len(refused) == 1 {
		subject = "a script is"
	}

	if len(supported) == 0 {
		return []error{fmt.Errorf("%s set in %s, but dataType %q runs no scripts at all: they would validate here and then "+
			"never execute", subject, strings.Join(refused, ", "), c.Type())}
	}

	return []error{fmt.Errorf("%s set in %s, but dataType %q does not run scripts there: they would validate here and then "+
		"never execute. On this data type the scripts that run are %s. A filter and a transformer need the message as a tree, "+
		"which differs per format, so use the declarative filter and transformations instead",
		subject, strings.Join(refused, ", "), c.Type(), strings.Join(supported, ", "))}
}
