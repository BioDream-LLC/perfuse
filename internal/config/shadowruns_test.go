package config

import (
	"strings"
	"testing"
)

// TestEveryDataTypeEitherRunsAShadowOrRefusesIt is the ratchet for a defect that was silent for five formats.
//
// # What was wrong
//
// Shadow mode was refused in some data type branches and simply absent from others. The omissions were not decisions - they
// were the branches nobody had thought about - and the effect was a configuration option that validated, appeared in
// perfuse check, appeared in the interface, and never compared anything.
//
// The mechanism: Channel.handle sets up the observation as a deferred call and dispatches to the format-specific handlers
// before reaching it. So handleDelimited, handlePharmacy and the rest returned first and the defer was never evaluated.
//
// # What this asserts
//
// Every known data type is either in shadowRuns, and therefore claims to work, or refuses a shadow at load with a reason. There
// is no third state. A format added later cannot quietly inherit the silent option, because the default for anything absent
// from shadowRuns is a refusal.
func TestEveryDataTypeEitherRunsAShadowOrRefusesIt(t *testing.T) {
	for _, dt := range KnownDataTypes {
		t.Run(string(dt), func(t *testing.T) {
			c := &Channel{
				Name:     "shadow-check",
				DataType: dt,
				Shadow:   &Shadow{Channel: "candidate.yaml"},
			}

			var refused bool
			for _, err := range c.validateDataType() {
				if strings.Contains(err.Error(), "does not run a shadow") {
					refused = true
				}
			}

			if shadowRuns[dt] && refused {
				t.Errorf("%s is listed in shadowRuns but its shadow is refused at load", dt)
			}
			if !shadowRuns[dt] && !refused {
				t.Errorf("%s is not in shadowRuns and does not refuse a shadow, so a shadow on this channel would validate and then never compare anything", dt)
			}
		})
	}
}

// TestTheShadowRefusalNamesWhatDoesWork keeps the error actionable.
//
// "This does not work" leaves somebody guessing. Naming the formats that do work turns it into a decision they can make.
func TestTheShadowRefusalNamesWhatDoesWork(t *testing.T) {
	c := &Channel{Name: "x", DataType: DataDICOM, Shadow: &Shadow{Channel: "c.yaml"}}

	var msg string
	for _, err := range c.validateDataType() {
		if strings.Contains(err.Error(), "does not run a shadow") {
			msg = err.Error()
		}
	}
	if msg == "" {
		t.Fatal("a DICOM shadow was not refused")
	}

	for _, want := range shadowingDataTypes() {
		if !strings.Contains(msg, string(want)) {
			t.Errorf("the refusal does not name %s, which does work: %s", want, msg)
		}
	}
}

// TestShadowRunsIsNotEmpty catches the degenerate case where a refactor empties the map and every shadow starts being refused.
//
// That failure would look like a working ratchet: every data type refuses, the test above passes, and shadow mode is quietly
// dead everywhere.
func TestShadowRunsIsNotEmpty(t *testing.T) {
	if len(shadowRuns) == 0 {
		t.Fatal("shadowRuns is empty, so every shadow is refused and the feature is dead - which the paired test would report as success")
	}

	// HL7 v2 is the one that has always worked. If it is missing, something has gone badly wrong rather than subtly.
	if !shadowRuns[DataHL7] {
		t.Error("HL7 v2 is not in shadowRuns, and it is the format shadow mode was built for")
	}
}
