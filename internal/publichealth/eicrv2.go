package publichealth

import (
	"fmt"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/v2fhir"
)

// FromV2 converts a v2 message to FHIR R4 (eCR is an R4 guide) and builds the eICR from it. The conversion's own mapping notes
// are returned with the report's, since an agency reading a gap in the report needs to know whether the message lacked it.
func FromV2(m *hl7.Message, triggers *TriggerSet, convert v2fhir.Options, opts EICROptions) (*EICR, error) {
	// eCR is R4 and names its own profiles, so the conversion's version and US Core claims are not the caller's to choose.
	convert.Version = fhir.R4
	convert.ClaimUSCore = false
	conv, err := v2fhir.Convert(m, convert)
	if err != nil {
		return nil, err
	}
	body, err := conv.JSON()
	if err != nil {
		return nil, err
	}
	report, err := BuildEICR(body, triggers, opts)
	if report != nil {
		for _, n := range conv.Warnings() {
			report.Notes = append(report.Notes, fmt.Sprintf("conversion %s: %s", n.Source, n.Message))
		}
	}
	return report, err
}
