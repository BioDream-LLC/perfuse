package v2fhir

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/fhir"
)

// European feeds, as UK Core, FR Core and ISiK read them (conformance/national runs the HL7 validator over real samples;
// these pin what those runs found).

func adtWith(pid map[int]string, extra ...string) string {
	segs := []string{
		segment("MSH", mshFields("ADT^A01^ADT_A01", "CTRL-EU", "20261020083000+0200")),
		segment("EVN", map[int]string{1: "A01"}),
		segment("PID", pid),
		segment("PV1", map[int]string{1: "1", 2: "I", 7: "123456^Schmidt^Anna^^^Dr.^^^^L", 19: "2026100001^^^KH^VN"}),
	}
	return message(append(segs, extra...)...)
}

func identifierBySystem(p *fhir.Patient, system string) *fhir.Identifier {
	for i := range p.Identifier {
		if p.Identifier[i].System == system {
			return &p.Identifier[i]
		}
	}
	return nil
}

// A German statutory insurance number arrives under gematik's OID with type NNDEU. ISiK and fhir.de's base profiles know it
// by http://fhir.de/sid/gkv/kvid-10 and type KVZ10; under the OID it matched nothing.
func TestKVNRIsWrittenUnderItsFHIRSystemAndType(t *testing.T) {
	res := convert(t, adtWith(map[int]string{1: "1", 3: "4711^^^KH^PI~A123456780^^^&1.2.276.0.76.4.8&ISO^NNDEU", 5: "Mustermann^Max^^^^^L"}), Options{})
	p := find[*fhir.Patient](t, res)
	id := identifierBySystem(p, "http://fhir.de/sid/gkv/kvid-10")
	if id == nil || id.Value != "A123456780" {
		t.Fatalf("KVNR not under kvid-10: %+v", p.Identifier)
	}
	have := false
	for _, c := range id.Type.Coding {
		have = have || (c.System == "http://fhir.de/CodeSystem/identifier-type-de-basis" && c.Code == "KVZ10")
	}
	if !have {
		t.Errorf("KVNR type lacks KVZ10: %+v", id.Type)
	}
	// The facility's own number, sent as PI, is the patient's usual identifier (FR Core requires it).
	for _, i := range p.Identifier {
		if i.Value == "4711" && i.Use != "usual" {
			t.Errorf("PI identifier use = %q, want usual", i.Use)
		}
	}
}

// A national number whose check digit fails is somebody else's, or nobody's: it is kept as sent, under its system, and
// never given the type that would let a receiver treat it as checked.
func TestNationalNumberFailingItsCheckIsKeptButNotTyped(t *testing.T) {
	res := convert(t, adtWith(map[int]string{1: "1", 3: "A123456781^^^&1.2.276.0.76.4.8&ISO^NNDEU", 5: "Mustermann^Max"}), Options{})
	p := find[*fhir.Patient](t, res)
	id := identifierBySystem(p, "http://fhir.de/sid/gkv/kvid-10")
	if id == nil || id.Value != "A123456781" {
		t.Fatalf("number not kept: %+v", p.Identifier)
	}
	for _, c := range id.Type.Coding {
		if c.Code == "KVZ10" {
			t.Errorf("a failing number was typed KVZ10")
		}
	}
	if !hasNote(res, "fails its check") {
		t.Errorf("no note about the failed check")
	}
}

func hasNote(res *Result, text string) bool {
	for _, n := range res.Notes {
		if strings.Contains(n.Message, text) {
			return true
		}
	}
	return false
}

// Table 0396 names the German classifications with their year (I10G2006, ICD10GM2016, O3012006, OPS2016), and senders keep
// the pattern for later years. They were written to a placeholder system; ISiK needs ICD-10-GM with its version.
func TestGermanCodeSystemsKeepTheirYearAsVersion(t *testing.T) {
	for _, tc := range []struct{ id, system, version string }{
		{"I10G2026", "http://fhir.de/CodeSystem/bfarm/icd-10-gm", "2026"},
		{"ICD10GM2016", "http://fhir.de/CodeSystem/bfarm/icd-10-gm", "2016"},
		{"O3012006", "http://fhir.de/CodeSystem/bfarm/ops", "2006"},
		{"OPS2025", "http://fhir.de/CodeSystem/bfarm/ops", "2025"},
		{"I10GM", "http://fhir.de/CodeSystem/bfarm/icd-10-gm", ""},
	} {
		res := convert(t, adtWith(map[int]string{1: "1", 3: "4711^^^KH^PI", 5: "Mustermann^Max"},
			segment("DG1", map[int]string{1: "1", 3: "I21.0^Akuter Myokardinfarkt^" + tc.id, 5: "20261020", 6: "A"})), Options{})
		cond := find[*fhir.Condition](t, res)
		if len(cond.Code.Coding) == 0 {
			t.Fatalf("%s: no coding", tc.id)
		}
		got := cond.Code.Coding[0]
		if got.System != tc.system || got.Version != tc.version || got.Code != "I21.0" {
			t.Errorf("%s: coding = %+v, want %s version %q", tc.id, got, tc.system, tc.version)
		}
	}
	// CWE.7 gives the version when the system name does not.
	res := convert(t, adtWith(map[int]string{1: "1", 3: "4711^^^KH^PI", 5: "Mustermann^Max"},
		segment("DG1", map[int]string{1: "1", 3: "I21.0^Akuter Myokardinfarkt^I10GM^^^^2026", 6: "A"})), Options{})
	if got := find[*fhir.Condition](t, res).Code.Coding[0]; got.Version != "2026" {
		t.Errorf("CWE.7 version = %q, want 2026", got.Version)
	}
}

// XCN.10 is the practitioner's name type: L, the legal name, is FHIR's official (ISiK and UK Core look for it). XCN.13
// IDNPS is France's national professional number, which FR Core slices on.
func TestPractitionerNameTypeAndFrenchNationalType(t *testing.T) {
	res := convert(t, adtWith(map[int]string{1: "1", 3: "4711^^^KH^PI", 5: "Dupont^Marie"}), Options{})
	var named bool
	for _, p := range findAll[*fhir.Practitioner](res) {
		named = named || (len(p.Name) == 1 && p.Name[0].Use == "official")
	}
	if !named {
		t.Errorf("attending's legal name not official")
	}

	raw := message(
		segment("MSH", mshFields("ADT^A01^ADT_A01", "CTRL-FR", "20261020083000+0200")),
		segment("EVN", map[int]string{1: "A01"}),
		segment("PID", map[int]string{1: "1", 3: "123^^^HOP^PI", 5: "Dupont^Marie^^^^^L"}),
		segment("PV1", map[int]string{1: "1", 2: "I", 7: "801234567897^Hoda^Adam^^^^^^ASIP-SANTE-PS&1.2.250.1.71.4.2.1&ISO^D^^^IDNPS"}),
	)
	res = convert(t, raw, Options{})
	p := find[*fhir.Practitioner](t, res)
	id := p.Identifier[0]
	if id.System != "urn:oid:1.2.250.1.71.4.2.1" || id.Type == nil || len(id.Type.Coding) != 1 ||
		id.Type.Coding[0].System != "https://hl7.fr/ig/fhir/core/CodeSystem/fr-core-cs-v2-0203" || id.Type.Coding[0].Code != "IDNPS" {
		t.Errorf("French national professional identifier = %+v", id)
	}
}
