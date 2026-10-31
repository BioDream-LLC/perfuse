package engine

import (
	"context"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/tefca"
)

func TestATEFCAQueryIsReadFromThePatientInTheMessage(t *testing.T) {
	hl7 := "MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5\rPID|1||MRN7^^^HOSP&2.16.840.1.113883.3.9999&ISO^MR||DOE^JANE||19700203|F\r"
	q, err := tefcaQueryFor([]byte(hl7), "treatment")
	if err != nil {
		t.Fatal(err)
	}
	if q.PatientID != "urn:oid:2.16.840.1.113883.3.9999|MRN7" || q.PatientName != "DOE" || q.DOB != "1970-02-03" ||
		q.Purpose != "treatment" {
		t.Errorf("from HL7 v2: %+v", q)
	}
	fhirPatient := `{"resourceType":"Patient","identifier":[{"system":"https://hosp.example/mrn","value":"42"}],
		"name":[{"family":"Roe","given":["Ann"]}],"birthDate":"1981-05-06"}`
	q, err = tefcaQueryFor([]byte(fhirPatient), "treatment")
	if err != nil {
		t.Fatal(err)
	}
	if q.PatientID != "https://hosp.example/mrn|42" || q.DOB != "1981-05-06" {
		t.Errorf("from a FHIR Patient: %+v", q)
	}
	if _, err := tefcaQueryFor([]byte("not a message"), "treatment"); err == nil {
		t.Error("a message with no patient made a query")
	}
}

func TestATEFCADestinationExplainsWhatIsMissing(t *testing.T) {
	saved := TEFCA
	t.Cleanup(func() { TEFCA = saved })
	s, err := NewTEFCASender(config.Destination{Name: "records", Type: config.DestinationTEFCA,
		TEFCA: &config.TEFCADestination{Purpose: "treatment"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	hl7 := []byte("MSH|^~\\&|A|B|C|D|20260101||ADT^A01|1|P|2.5\rPID|1||MRN7^^^HOSP^MR||DOE^JANE||19700203|F\r")

	TEFCA = nil
	if err := s.Send(context.Background(), hl7); err == nil || !strings.Contains(err.Error(), "not a TEFCA participant") {
		t.Errorf("no participant: %v", err)
	}

	// A participant with no partner: refused, and the refusal is in its audit trail.
	log := &tefca.AuditLog{}
	p, err := tefca.NewParticipant(tefca.TEFCAConfig{OrganizationName: "H", OrganizationOID: "1.2.3", QHINEndpoint: "https://q.example",
		ParticipantType: "provider", CertificatePath: "c.pem", KeyPath: "k.pem", SupportedPurposes: []string{"treatment"}}, log)
	if err != nil {
		t.Fatal(err)
	}
	TEFCA = p
	if err := s.Send(context.Background(), hl7); err == nil || !strings.Contains(err.Error(), "no Facilitated FHIR partner") {
		t.Errorf("no partner: %v", err)
	}
	if entries := log.Query(time.Time{}, time.Now().Add(time.Hour)); len(entries) != 1 || entries[0].Success {
		t.Errorf("the refused query was not audited: %+v", entries)
	}

	if _, err := NewTEFCASender(config.Destination{Name: "records", Type: config.DestinationTEFCA,
		TEFCA: &config.TEFCADestination{Purpose: "treatment", ResultsTo: "records-in"}}, nil); err == nil {
		t.Error("results_to was accepted with nothing to route through")
	}
}

// TestTheConfigurationAndTheExchangeAgreeOnPurposes ties the two lists of purposes of use together.
func TestTheConfigurationAndTheExchangeAgreeOnPurposes(t *testing.T) {
	got := config.TEFCAPurposes()
	sort.Strings(got)
	for _, p := range got {
		if !tefca.ValidPurpose(p) {
			t.Errorf("a tefca destination may name %q, which the exchange does not know", p)
		}
	}
	for _, p := range []string{tefca.PurposeTreatment, tefca.PurposePayment, tefca.PurposeOperations, tefca.PurposePublicHealth,
		tefca.PurposeIndividualAccess} {
		if sort.SearchStrings(got, p) == len(got) || got[sort.SearchStrings(got, p)] != p {
			t.Errorf("the exchange knows %q and a tefca destination cannot name it", p)
		}
	}
}
