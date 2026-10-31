package config

import (
	"fmt"
	"strings"
)

// TEFCADestination asks the TEFCA Facilitated FHIR partner for the records of each message's patient.
//
// Who this server is on the network, its certificate and the partner are server settings (Settings, TEFCA), because they
// belong to the organisation and its agreement with a QHIN. What a channel decides is when to ask and why: the purpose
// of use is part of the channel, not of whoever is signed in, so every query a feed makes states the same reason and the
// audit trail can be read against it.
type TEFCADestination struct {
	// Purpose is the purpose of use: treatment, payment, operations, public_health or individual_access. It must be
	// one the participant declares.
	Purpose string `yaml:"purpose"`

	// ResultsTo names a channel in this server that receives each Bundle the partner returns, as a message of its
	// own. Empty keeps the Bundle as this destination's reply only, for a response transformer to read.
	ResultsTo string `yaml:"results_to,omitempty"`
}

// tefcaPurposes are the purposes of use a TEFCA query may state. Kept here rather than imported, because the
// configuration package does not depend on the exchange; a test in internal/engine ties the two lists together.
var tefcaPurposes = []string{"treatment", "payment", "operations", "public_health", "individual_access"}

// TEFCAPurposes lists the purposes a tefca destination may name.
func TEFCAPurposes() []string { return append([]string(nil), tefcaPurposes...) }

func validateTEFCADest(d *Destination, dataType DataType) []error {
	var errs []error
	if dataType != DataHL7 && dataType != DataRaw {
		errs = append(errs, fmt.Errorf("destination %q: a TEFCA query reads the patient from an HL7 v2 PID or a FHIR Patient "+
			"(on a raw channel), and this is a %s channel", d.Name, dataType))
	}
	if d.TEFCA == nil || strings.TrimSpace(d.TEFCA.Purpose) == "" {
		return append(errs, fmt.Errorf("destination %q: a TEFCA query states its purpose of use (tefca.purpose: %s)",
			d.Name, strings.Join(tefcaPurposes, ", ")))
	}
	known := false
	for _, p := range tefcaPurposes {
		if d.TEFCA.Purpose == p {
			known = true
		}
	}
	if !known {
		errs = append(errs, fmt.Errorf("destination %q: tefca.purpose %q is not a TEFCA purpose of use; use one of %s",
			d.Name, d.TEFCA.Purpose, strings.Join(tefcaPurposes, ", ")))
	}
	// Whether results_to names a channel that exists, and whether it makes a loop, is checked with every other route
	// in ValidateRouting.
	return errs
}
