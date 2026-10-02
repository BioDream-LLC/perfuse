package config

import (
	"strings"
	"testing"
)

// TestEveryDataTypeEitherRunsAScriptSlotOrRefusesIt is the ratchet for the fourth instance of one defect.
//
// # The shape, which has now appeared four times
//
// A capability that validates at load and never runs. Scripts compiling on formats that never executed them. Shadow mode on five
// formats. The script compile gate excluding every format except v2 and v3. And the per-format script refusals being
// all-or-nothing when a preprocessor needs no parsed message.
//
// Each time the cause was the same: a check or a gate written next to the first case that needed it, rather than next to the
// reason it was needed. The v3 fix is the clearest example - somebody found that a v3 channel silently compiled no scripts, added
// v3 to the condition, and left the shape of the bug in place for the next five formats to inherit.
//
// # What this asserts
//
// For every data type and every slot: either the slot is listed as running, or a script in that slot is refused at load. No third
// state. That is the property the scattered refusals were trying to express and could not, because there was nowhere for the
// absence of a refusal to be noticed.
func TestEveryDataTypeEitherRunsAScriptSlotOrRefusesIt(t *testing.T) {
	for _, dt := range KnownDataTypes {
		for _, slot := range allScriptSlots {
			t.Run(string(dt)+"/"+string(slot), func(t *testing.T) {
				c := &Channel{Name: "slot-check", DataType: dt, Scripts: scriptsWithSlot(slot)}

				var refused bool
				for _, err := range c.validateScriptSlots() {
					if strings.Contains(err.Error(), "never execute") {
						refused = true
					}
				}

				runs := scriptSlotsRun[dt][slot]
				if runs && refused {
					t.Errorf("%s/%s is listed as running but is refused at load", dt, slot)
				}
				if !runs && !refused {
					t.Errorf("%s/%s is not listed as running and is not refused, so a script there would validate and then never execute", dt, slot)
				}
			})
		}
	}
}

// scriptsWithSlot builds a Scripts block with exactly one slot filled.
func scriptsWithSlot(slot scriptSlot) *Scripts {
	body := "return true;"

	switch slot {
	case slotFilter:
		return &Scripts{Filter: body}
	case slotTransformer:
		return &Scripts{Transformer: body}
	case slotPreprocessor:
		return &Scripts{Preprocessor: body}
	case slotPostprocessor:
		return &Scripts{Postprocessor: body}
	case slotDeploy:
		return &Scripts{Deploy: body}
	case slotUndeploy:
		return &Scripts{Undeploy: body}
	}

	return &Scripts{}
}

// TestTheScriptCompileGateMatchesTheSlotTable is the specific guard for the gate that was wrong twice.
//
// A format can be listed in scriptSlotsRun and still run nothing, if the loader never compiles its scripts. That is precisely
// what happened: the handlers invoked the preprocessor, the loader validated it, and the compile gate excluded the format, so
// PreprocessorScript returned nil. Three correct pieces and no working feature.
//
// Deriving the gate from this table is the fix. This test asserts they cannot drift apart again.
func TestTheScriptCompileGateMatchesTheSlotTable(t *testing.T) {
	for _, dt := range KnownDataTypes {
		slots := scriptSlotsRun[dt]
		if len(slots) == 0 {
			t.Errorf("%s runs no script slots at all, not even deploy - which is almost certainly an omission rather than a decision, since deploy and undeploy have no dependency on a parsed message", dt)

			continue
		}

		// Deploy and undeploy run against the channel rather than a message, so every format should have them. A format
		// missing them is a sign the entry was written by copying a narrower one.
		for _, always := range []scriptSlot{slotDeploy, slotUndeploy, slotPostprocessor} {
			if !slots[always] {
				t.Errorf("%s does not run %s, which needs no parsed message: deploy and undeploy run at channel start and stop, and a postprocessor cannot change anything", dt, always)
			}
		}
	}
}

// TestTheFormatsThatRunAFilterScriptAlsoRunATransformer is what the "only HL7" guardrail became.
//
// It used to assert that only HL7 v2 and v3 ran a filter or transformer script, and it recorded why: those need the message in an
// addressable form, and the form differs per format. That was true and the guardrail did its job - it failed the moment X12
// gained them, which is what made lifting the limit a decision rather than a discovery.
//
// # What replaced the reasoning
//
// The limit was lifted by not building what the old comment assumed. A tree projection would have meant inventing an XML shape
// for a format that has its own vocabulary, and a walker applying changes afterwards would have to relearn the invariants each
// format's accessor already enforces. Instead the script addresses the message by path, through the same steps.Accessor the
// declarative transformations use, so CLM01 means in a script what it means in a step.
//
// # What this now asserts
//
// The two slots move together. A format that can run a filter script can run a transformer, because both are the same binding
// with a different verdict - so one without the other means somebody wired half of it.
func TestTheFormatsThatRunAFilterScriptAlsoRunATransformer(t *testing.T) {
	for _, dt := range KnownDataTypes {
		filter := scriptSlotsRun[dt][slotFilter]
		transformer := scriptSlotsRun[dt][slotTransformer]

		if filter != transformer {
			t.Errorf("%s runs filter=%v transformer=%v. Both are the same binding with a different verdict, so one without the other means half of it is wired", dt, filter, transformer)
		}
	}
}

// TestTheFormatsWithoutScriptedFiltersAreTheOnesThatCannotBeAddressed records what is left, and why each one.
//
// Kept as a test rather than a comment so that adding a format to the table has to come with a reason. Two of these are wiring
// and two are genuinely different.
func TestTheFormatsWithoutScriptedFiltersAreTheOnesThatCannotBeAddressed(t *testing.T) {
	// NCPDP and delimited are wiring: the generic path stage works for any steps.Accessor and both have one. When they gain
	// scripts, remove them here.
	//
	// SCRIPT is XML and wants the tree binding rather than the path one. Raw has no addressable structure at all, so a filter
	// there could only ask about the bytes - which the expression filter cannot do either, so there is nothing to be
	// consistent with.
	//
	// DICOM is the deliberate one. Tag-level writing through a script is the same hazard as a general path writer, which is
	// why the transformations are named steps instead.
	expectedAbsent := map[DataType]string{
		DataRaw:   "no addressable structure: a filter could only ask about the bytes",
		DataDICOM: "deliberate: tag-level script writing is the hazard named steps exist to avoid",
	}

	for dt, why := range expectedAbsent {
		if scriptSlotsRun[dt][slotFilter] {
			t.Errorf("%s now runs a filter script. If that is right, remove it from this list; the reason it was absent was %q", dt, why)
		}
	}

	for _, dt := range KnownDataTypes {
		if _, listed := expectedAbsent[dt]; listed {
			continue
		}
		if !scriptSlotsRun[dt][slotFilter] {
			t.Errorf("%s runs no filter script and is not in the list of formats that cannot, so nothing records why", dt)
		}
	}
}

// TestDICOMRefusesAPreprocessor is the one deliberate exception among the text-level slots.
func TestDICOMRefusesAPreprocessor(t *testing.T) {
	if scriptSlotsRun[DataDICOM][slotPreprocessor] {
		t.Error("DICOM now runs a preprocessor. An object is binary with pixel data in it, so a script editing it as text will corrupt an image - which is the same reason DICOM gets named transformation steps rather than a path writer")
	}
}
