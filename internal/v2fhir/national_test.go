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

// HL7 Europe's Laboratory Report wants every result timed. v2 sends OBX-14 only when a result's time differs from its order's,
// so a result without one is timed by its OBR-7. And a SNOMED CT version given as a bare date (CWE-7) names no edition, which
// a terminology server rejects: it is left out.
func TestResultsTakeTheirOrdersTimeAndSnomedVersionsAreURIsOrNothing(t *testing.T) {
	raw := message(
		segment("MSH", mshFields("ORU^R01^ORU_R01", "CTRL-EU", "20261020083000+0200")),
		segment("PID", map[int]string{1: "1", 3: "4711^^^KH^PI", 5: "Mustermann^Max", 7: "19700101"}),
		segment("OBR", map[int]string{1: "1", 4: "58410-2^CBC^LN", 7: "20261020071500+0200", 25: "F"}),
		segment("OBX", map[int]string{1: "1", 2: "NM", 3: "718-7^Hemoglobin^LN", 5: "14.2", 6: "g/dL", 11: "F"}),
		segment("OBX", map[int]string{1: "2", 2: "NM", 3: "6690-2^WBC^LN", 5: "6.1", 6: "10*3/uL", 11: "F", 14: "20261020080000+0200"}),
		segment("SPM", map[int]string{1: "1", 4: "119297000^Blood specimen^SCT^^^^20140131"}),
	)
	res := convert(t, raw, Options{})
	times := map[string]string{}
	for _, o := range findAll[*fhir.Observation](res) {
		times[o.Code.Coding[0].Code] = o.EffectiveDateTime
	}
	if !strings.HasPrefix(times["718-7"], "2026-10-20T07:15") || !strings.HasPrefix(times["6690-2"], "2026-10-20T08:00") {
		t.Errorf("result times = %v", times)
	}
	typed := 0
	for _, s := range findAll[*fhir.Specimen](res) {
		if s.Type != nil && len(s.Type.Coding) > 0 {
			typed++
			if s.Type.Coding[0].Version != "" {
				t.Errorf("a bare date as a SNOMED CT version: %+v", s.Type.Coding[0])
			}
		}
	}
	if typed != 1 {
		t.Errorf("%d typed specimens, want 1", typed)
	}
}

// OBX-23 (with OBX-24) or else OBX-15 names the laboratory that produced a result, and OBX-25 its medical director: each is a
// performer, as HL7's v2-to-FHIR OBX map has it. One laboratory on several results is one Organization.
func TestResultsNameTheLaboratoryThatProducedThem(t *testing.T) {
	raw := message(
		segment("MSH", mshFields("ORU^R01^ORU_R01", "CTRL-LAB", "20261020083000+0200")),
		segment("PID", map[int]string{1: "1", 3: "4711^^^KH^PI", 5: "Mustermann^Max"}),
		segment("OBR", map[int]string{1: "1", 4: "58410-2^CBC^LN", 7: "20261020071500+0200", 25: "F"}),
		segment("OBX", map[int]string{1: "1", 2: "NM", 3: "718-7^Hemoglobin^LN", 5: "14.2", 11: "F",
			23: "Zentrallabor Musterstadt^^^^^^^^^LAB-01", 24: "Laborweg 1^^Musterstadt^^12345^DE", 25: "998^Musterfrau^Erika"}),
		segment("OBX", map[int]string{1: "2", 2: "NM", 3: "6690-2^WBC^LN", 5: "6.1", 11: "F",
			23: "Zentrallabor Musterstadt^^^^^^^^^LAB-01", 24: "Laborweg 1^^Musterstadt^^12345^DE"}),
		segment("OBX", map[int]string{1: "3", 2: "NM", 3: "777-3^Platelets^LN", 5: "250", 11: "F", 15: "MYFAC^MyFake Hospital^L"}),
	)
	res := convert(t, raw, Options{})
	orgs := map[string]*fhir.Organization{}
	for _, o := range findAll[*fhir.Organization](res) {
		orgs[o.Name] = o
	}
	lab, fac := orgs["Zentrallabor Musterstadt"], orgs["MyFake Hospital"]
	if lab == nil || fac == nil || len(lab.Identifier) != 1 || lab.Identifier[0].Value != "LAB-01" ||
		len(lab.Address) != 1 || lab.Address[0].City != "Musterstadt" || fac.Identifier[0].Value != "MYFAC" {
		t.Fatalf("organisations = %+v", orgs)
	}
	for _, o := range findAll[*fhir.Observation](res) {
		want := map[string]int{"718-7": 2, "6690-2": 1, "777-3": 1}[o.Code.Coding[0].Code]
		if len(o.Performer) != want {
			t.Errorf("%s: %d performers, want %d", o.Code.Coding[0].Code, len(o.Performer), want)
		}
	}
}

// DG1 is also an Encounter.diagnosis naming the Condition, with DG1-6 A as the admission diagnosis (the v2-to-FHIR maps); PR1 is
// a Procedure with its performers' roles, and status unknown, which is what the map says when the message does not tell.
func TestADischargeCarriesItsDiagnosesAndProcedures(t *testing.T) {
	raw := message(
		segment("MSH", mshFields("ADT^A03^ADT_A03", "CTRL-DIS", "20261024160000+0200")),
		segment("EVN", map[int]string{1: "A03"}),
		segment("PID", map[int]string{1: "1", 3: "4711^^^KH^PI", 5: "Musterfrau^Erika"}),
		segment("PV1", map[int]string{1: "1", 2: "I", 19: "V1^^^KH^VN", 44: "20261019091500+0200", 45: "20261024155500+0200"}),
		segment("DG1", map[int]string{1: "1", 3: "K35.8^Akute Appendizitis^I10G2026", 6: "A"}),
		segment("DG1", map[int]string{1: "2", 3: "K35.32^Appendizitis mit Abszess^I10G2026", 6: "F"}),
		segment("PR1", map[int]string{1: "1", 3: "5-470.11^Appendektomie^OPS2026", 5: "20261019143000+0200", 6: "P",
			8: "111^Narkose^Nina", 11: "222^Beispiel^Jonas"}),
	)
	res := convert(t, raw, Options{})
	enc := find[*fhir.Encounter](t, res)
	if len(enc.Diagnosis) != 2 || len(enc.Diagnosis[0].Use) != 1 || enc.Diagnosis[0].Use[0].Coding[0].Code != "AD" || len(enc.Diagnosis[1].Use) != 0 {
		t.Errorf("Encounter.diagnosis = %+v", enc.Diagnosis)
	}
	proc := find[*fhir.Procedure](t, res)
	if proc.Status != "unknown" || proc.Code.Coding[0].Code != "5-470.11" || proc.Code.Coding[0].System != "http://fhir.de/CodeSystem/bfarm/ops" ||
		!strings.HasPrefix(proc.PerformedDateTime, "2026-10-19T14:30") || len(proc.Performer) != 2 ||
		proc.Performer[0].Function.Coding[0].Code != "88189002" || proc.Performer[1].Function.Coding[0].Code != "304292004" ||
		proc.Category == nil || len(proc.Category.Coding) != 2 || proc.Category.Coding[0].Code != "P" ||
		proc.Category.Coding[1].Code != "387713003" || proc.Encounter == nil {
		t.Errorf("Procedure = %+v", proc)
	}
	out, err := fhir.MarshalVersioned(enc, fhir.R4)
	if err != nil || !strings.Contains(string(out), `"diagnosis":[{"condition":{"reference":"Condition/`) {
		t.Errorf("R4 Encounter.diagnosis: %s %v", out, err)
	}
}
