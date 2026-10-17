package config

import (
	"fmt"
	"strings"
)

// MPIDestination registers each message's patient in this server's patient index: an HL7 v2 PID (an A40 merge merges),
// or a FHIR Patient on a raw channel. The index decides which records are the same person; see the Patient index screen.
type MPIDestination struct {
	// Authorities maps an assigning authority's namespace (PID-3.4.1) to its identifier system, for senders that give no
	// universal identifier. Unmapped ones become urn:perfuse:aa:<namespace>.
	Authorities map[string]string `yaml:"authorities,omitempty"`
	// IDType is the identifier type (PID-3.5) of the record's own number. Default MR.
	IDType string `yaml:"id_type,omitempty"`
	// SSNSystem names PID-19 as an identifier. Default http://hl7.org/fhir/sid/us-ssn.
	SSNSystem string `yaml:"ssn_system,omitempty"`
	// NoMerge ignores A40, A34, A18 and A39 merges, for a feed whose merges the index should not follow.
	NoMerge bool `yaml:"no_merge,omitempty"`
}

func validateMPIDest(d *Destination, dataType DataType) []error {
	var errs []error
	if dataType != DataHL7 && dataType != DataRaw {
		errs = append(errs, fmt.Errorf("destination %q: a patient index destination reads an HL7 v2 PID or a FHIR Patient (on a raw "+
			"channel), and this is a %s channel", d.Name, dataType))
	}
	if d.MPI != nil {
		for ns, sys := range d.MPI.Authorities {
			if strings.TrimSpace(ns) == "" || strings.TrimSpace(sys) == "" {
				errs = append(errs, fmt.Errorf("destination %q: mpi.authorities has an empty namespace or system", d.Name))
			}
		}
	}
	return errs
}
