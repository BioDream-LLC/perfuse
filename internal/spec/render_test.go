package spec

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/hl7v3"
)

// TestAV3ChannelWithStepsDoesNotClaimToChangeNothing pins a contradiction found by reading real output.
//
// The "passed through exactly as they arrive" sentence is decided by the v2 field lists, which a v3 step never appears in.
// So the generated document stated, two lines apart, that the channel masks a birth date and that it changes nothing.
//
// This matters more than a tidy-up: that sentence is what somebody pastes into a change request as evidence that no patient
// data is altered, and it was false on every v3 channel that had a transformation.
func TestAV3ChannelWithStepsDoesNotClaimToChangeNothing(t *testing.T) {
	acknowledge := true
	c := &config.Channel{
		Name:     "pdq",
		DataType: config.DataHL7v3,
		HL7v3: &config.HL7v3Options{
			Acknowledge:  &acknowledge,
			SenderDevice: "PERFUSE",
			SenderOID:    "2.16.840.1.113883.3.999",
			Transformations: []hl7v3.Step{{
				Description: "mask the birth date",
				NullFlavor:  &hl7v3.V3NullFlavorStep{Path: "//birthTime", Reason: "MSK"},
			}},
		},
		Source: config.Source{
			Type: config.SourceHTTP,
			HTTP: &config.HTTPSource{Listen: "127.0.0.1:0", Path: "/pdq"},
		},
		Destinations: []config.Destination{
			{Name: "onward", Type: config.DestinationFile, Dir: t.TempDir()},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("the channel is not valid: %v", err)
	}

	doc := Build(c)
	joined := strings.Join(doc.Caveats, "\n")

	if strings.Contains(joined, "does not read or change any field") {
		t.Errorf("the document claims this channel changes nothing while also listing a step that masks a "+
			"birth date:\n%s", joined)
	}
	if !strings.Contains(joined, "withheld deliberately") {
		t.Errorf("the document does not explain what the null flavour means:\n%s", joined)
	}
}
