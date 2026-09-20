package engine

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// TestADestinationFilterIsNeverSilentlySkipped is the guardrail for the shape of bug that produced this file.
//
// Destination filters used to be evaluated inside deliver, which read the HL7 expression off the destination. A channel
// whose data type has no parsed HL7 form passed nil, and the whole block was skipped - which was correct only because
// those data types refused destination filters at load.
//
// When X12 learned to filter, that stopped being true. The filter compiled, the channel loaded, and the block was skipped
// because the HL7 expression was nil. Every message would have gone to a destination the configuration said to exclude,
// silently.
//
// So the decision is now the caller's, and a destination carrying a filter the channel cannot evaluate is an error rather
// than a pass. This test asserts the fallback, not the happy path: the happy paths are covered per format.
func TestADestinationFilterIsNeverSilentlySkipped(t *testing.T) {
	d := config.Destination{Name: "out", Filter: `CLM01 == "X"`}

	if !d.HasFilter() {
		t.Fatal("a destination with a filter string must report having one, or the guard cannot fire")
	}

	// The HL7 evaluator on a destination whose filter did not compile as HL7 - which is what an X12 filter looks like
	// from here - must refuse rather than pass.
	pass, err := hl7DestFilter(nil)(&d)
	if err == nil {
		t.Fatal("a filter that did not compile for this data type should be an error, not a pass")
	}
	if pass {
		t.Fatal("a filter that could not be evaluated must never report passing")
	}
	if !strings.Contains(err.Error(), "compiled") && !strings.Contains(err.Error(), "parsed") {
		t.Errorf("the error should say why it could not be evaluated, got: %v", err)
	}

	// And a destination with no filter at all passes, so an unfiltered destination needs no special case.
	none := config.Destination{Name: "out"}
	if none.HasFilter() {
		t.Fatal("a destination with no filter must not report having one")
	}
}
