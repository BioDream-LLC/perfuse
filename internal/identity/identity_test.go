package identity

import (
	"os"
	"strings"
	"testing"
)

// An HL7 v2 admission with the identity fields populated the way a real feed populates them:
// two identifiers in PID-3, a delimited name, an HL7 timestamp rather than a date, an account
// number, and an order.
const admission = "MSH|^~\\&|EPIC|HOSP|PERFUSE|DEST|20260921120000||ADT^A01^ADT_A01|MSG00001|P|2.5.1\r" +
	"EVN|A01|20260921120000\r" +
	"PID|1||MRN0012345^^^HOSP^MR~E99887766^^^ENTERPRISE^EPI||SAMPLESON^BRAVO^Q||19700101|M\r" +
	"PV1|1|I|ICU^01^A||||||||||||||||VISIT778899\r" +
	"ORC|NW|PLACER5551|FILLER7771\r" +
	"OBR|1|PLACER5551|FILLER7771|CBC^COMPLETE BLOOD COUNT\r"

func find(t *testing.T, id Identity, k Kind, want string) {
	t.Helper()
	for _, v := range id.All(k) {
		if v == want {
			return
		}
	}
	t.Errorf("no %s equal to %q; got %q", k, want, id.All(k))
}

func TestHL7IdentityIsFound(t *testing.T) {
	id := Extract([]byte(admission))

	if id.Format != FormatHL7 {
		t.Fatalf("format = %q, want hl7", id.Format)
	}

	find(t, id, KindPatientID, "MRN0012345")
	// The second repetition of PID-3. A feed that carries the enterprise number after the
	// MRN is ordinary, and reading only the first repetition is how a search for a number
	// that is demonstrably in the message returns nothing.
	find(t, id, KindPatientID, "E99887766")

	// Assembled in reading order rather than left as SAMPLESON^BRAVO^Q, because the caret
	// form is not what anybody types into a search box.
	find(t, id, KindPatientName, "SAMPLESON BRAVO")

	// Converted to ISO so a date of birth means the same thing here as it does in a FHIR
	// resource or a DICOM header, and one search finds all three.
	find(t, id, KindBirthDate, "1970-01-01")

	find(t, id, KindAccount, "VISIT778899")
	find(t, id, KindAccession, "PLACER5551")
	find(t, id, KindAccession, "FILLER7771")
}

// MLLP framing must not hide a message.
//
// The file destination frames HL7 deliberately so an archive replays through perfuse send, so
// a stored payload beginning with 0x0b is normal rather than damaged. A prefix test for "MSH|"
// fails on it, and the failure is silent: the message records with no identity and is
// unfindable, which looks like the search being broken rather than the detection.
func TestFramedHL7IsStillRecognised(t *testing.T) {
	framed := append([]byte{0x0b}, append([]byte(admission), 0x1c, '\r')...)

	if got := Detect(framed); got != FormatHL7 {
		t.Fatalf("Detect on a framed message = %q, want hl7", got)
	}
	find(t, Extract(framed), KindPatientID, "MRN0012345")
}

// A FHIR Patient, and a Bundle wrapping one, must both yield the patient.
func TestFHIRIdentityIsFound(t *testing.T) {
	patient := `{
	  "resourceType": "Patient",
	  "identifier": [{"system": "urn:oid:1.2.3", "value": "FHIR-778899"}],
	  "name": [{"family": "Sampleson", "given": ["Bravo", "Q"]}],
	  "birthDate": "1970-01-01"
	}`

	id := Extract([]byte(patient))
	if id.Format != FormatFHIR {
		t.Fatalf("format = %q, want fhir", id.Format)
	}
	find(t, id, KindPatientID, "FHIR-778899")
	find(t, id, KindPatientName, "Sampleson Bravo")
	find(t, id, KindBirthDate, "1970-01-01")

	// Nested inside a Bundle, which is how FHIR actually arrives. A walk that only looked at
	// the top-level resource would find nothing here and would find nothing for most real
	// traffic.
	bundle := `{"resourceType":"Bundle","type":"transaction","entry":[{"resource":` + patient + `}]}`
	find(t, Extract([]byte(bundle)), KindPatientID, "FHIR-778899")
}

// An 837 claim, with the patient in an NM1*IL loop and other NM1 loops around it.
const claim837 = "ISA*00*          *00*          *ZZ*SUBMITTER      *ZZ*RECEIVER       " +
	"*260921*1200*^*00501*000000001*0*P*:~" +
	"GS*HC*SENDER*RECEIVER*20260921*1200*1*X*005010X222A1~" +
	"ST*837*0001*005010X222A1~" +
	"BHT*0019*00*0123*20260921*1200*CH~" +
	"NM1*41*2*SUBMITTER NAME*****46*999888777~" +
	"NM1*85*2*BILLING PROVIDER*****XX*1234567893~" +
	"NM1*IL*1*SAMPLESON*BRAVO****MI*MEMBER4455~" +
	"DMG*D8*19700101*M~" +
	"CLM*CLAIM90210*500***11:B:1*Y*A*Y*Y~" +
	"SE*9*0001~GE*1*1~IEA*1*000000001~"

func TestX12IdentityIsFound(t *testing.T) {
	id := Extract([]byte(claim837))

	if id.Format != FormatX12 {
		t.Fatalf("format = %q, want x12", id.Format)
	}
	find(t, id, KindPatientID, "MEMBER4455")
	find(t, id, KindPatientName, "SAMPLESON BRAVO")
	find(t, id, KindBirthDate, "1970-01-01")
	find(t, id, KindClaim, "CLAIM90210")
}

// The qualifier on an NM1 loop decides whether it is a person, and ignoring it indexes the
// wrong field.
//
// NM1*41 is the submitter and NM1*85 the billing provider; both carry an identifier in
// element 9 exactly where the patient's member ID sits. Reading NM109 from whichever NM1 comes
// first would index a submitter ID and an NPI as patient identifiers - wrong answers to a
// patient search, and a provider's tax ID written into a patient index, which is the sort of
// thing a PHI audit asks about.
func TestOnlyPatientLoopsAreTreatedAsPatients(t *testing.T) {
	id := Extract([]byte(claim837))

	for _, notAPatient := range []string{"999888777", "1234567893"} {
		for _, got := range id.All(KindPatientID) {
			if got == notAPatient {
				t.Errorf("%q was indexed as a patient identifier; it is a submitter or "+
					"provider ID from an NM1 loop that is not about a person", got)
			}
		}
	}
	for _, got := range id.All(KindPatientName) {
		if strings.Contains(got, "BILLING") || strings.Contains(got, "SUBMITTER") {
			t.Errorf("%q was indexed as a patient name", got)
		}
	}
}

// DICOM, against the real file the dicom package tests with.
func TestDICOMIdentityIsFound(t *testing.T) {
	raw, err := os.ReadFile("../dicom/testdata/dcmtk-explicit-le.dcm")
	if err != nil {
		t.Skipf("no DICOM fixture: %v", err)
	}

	id := Extract(raw)
	if id.Format != FormatDICOM {
		t.Fatalf("format = %q, want dicom", id.Format)
	}
	// Asserted as "something was found" rather than against fixed values, because the
	// fixture belongs to another package and its contents are not this package's to depend
	// on. What matters here is that a real file produces identity at all.
	if len(id.Values) == 0 {
		t.Error("a real DICOM file produced no identity at all")
	}
	t.Logf("extracted %d values from the DICOM fixture", len(id.Values))
}

// Nothing recognisable must produce nothing, rather than nonsense from a hopeful parse.
func TestUnrecognisedPayloadsYieldNothing(t *testing.T) {
	for _, tc := range []struct{ name, body string }{
		{"empty", ""},
		{"prose", "this is a note somebody pasted into a file"},
		{"html", "<html><body>not a message</body></html>"},
		{"json that is not FHIR", `{"hello":"world"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			id := Extract([]byte(tc.body))
			if id.Format != FormatUnknown {
				t.Errorf("format = %q, want unknown", id.Format)
			}
			if len(id.Values) != 0 {
				t.Errorf("got %d values from something that is not a message: %+v",
					len(id.Values), id.Values)
			}
		})
	}
}

// Normalisation has to make the realistic retyping match, and must not merge distinct people.
func TestNormalisationMatchesWhatPeopleType(t *testing.T) {
	same := [][2]string{
		{"SAMPLESON^BRAVO", "Sampleson Bravo"},
		{"Sampleson, Bravo", "sampleson bravo"},
		{"MRN-001234", "mrn001234"},
		{"1970-01-01", "19700101"},
	}
	for _, p := range same {
		if Normalise(p[0]) != Normalise(p[1]) {
			t.Errorf("%q and %q normalise differently (%q vs %q); a search typed the second "+
				"way would not find a message written the first way",
				p[0], p[1], Normalise(p[0]), Normalise(p[1]))
		}
	}

	// Leading zeros are significant. Two patients whose MRNs differ only by a leading zero
	// are two patients, and quietly making them one is worse than a search that needs the
	// zero typed.
	if Normalise("0012345") == Normalise("12345") {
		t.Error("leading zeros were stripped, so two different MRNs now match each other")
	}
}

// Repeated identity within one message must be indexed once.
//
// A message states the MRN in PID-3 and again in every order segment. Indexing each
// occurrence multiplies the table by the number of segments and returns the same message
// several times in one result, for no additional way of finding it.
func TestRepeatedValuesAreIndexedOnce(t *testing.T) {
	id := Extract([]byte(admission))

	seen := map[string]int{}
	for _, v := range id.Values {
		seen[string(v.Kind)+"/"+v.Norm]++
	}
	for key, n := range seen {
		if n > 1 {
			t.Errorf("%s was indexed %d times", key, n)
		}
	}
}
