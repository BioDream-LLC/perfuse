package v2fhir

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/fhir"
)

// The two messages in testdata/hard were posted on chat.fhir.org (#v2 to FHIR, September 2026) by Marco Volpe as deliberately
// awkward test cases for v2 converters. Perfuse got a dozen of their details wrong or dropped them without a note; each test
// below holds one of those fixed.

func hardFixture(t *testing.T, name string) (*Result, map[string]any) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "hard", name))
	if err != nil {
		t.Fatal(err)
	}
	// No timezone configured, as with the CLI default, so the sender's MSH-7 offset is what applies.
	res, err := Convert(mustParse(t, string(raw)), Options{})
	if err != nil {
		t.Fatal(err)
	}
	out, err := res.JSON()
	if err != nil {
		t.Fatal(err)
	}
	var bundle map[string]any
	if err := json.Unmarshal(out, &bundle); err != nil {
		t.Fatal(err)
	}
	return res, bundle
}

func noteWith(res *Result, source, text string) bool {
	for _, n := range res.Notes {
		if strings.HasPrefix(n.Source, source) && strings.Contains(n.Message, text) {
			return true
		}
	}
	return false
}

func resourcesOf(bundle map[string]any, typ string) []map[string]any {
	var out []map[string]any
	for _, e := range bundle["entry"].([]any) {
		r := e.(map[string]any)["resource"].(map[string]any)
		if r["resourceType"] == typ {
			out = append(out, r)
		}
	}
	return out
}

var uuidURN = regexp.MustCompile(`^urn:uuid:[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

func TestBundleFullURLsAreRealUUIDs(t *testing.T) {
	// The HL7 validator rejected every entry: "urn:uuid:P68d45d5e1d881a5f" is not a UUID.
	for _, f := range []string{"oru-awkward.hl7", "adt-a08-awkward.hl7"} {
		_, b := hardFixture(t, f)
		for _, e := range b["entry"].([]any) {
			if u := e.(map[string]any)["fullUrl"].(string); !uuidURN.MatchString(u) {
				t.Errorf("%s: fullUrl %q is not a urn:uuid holding a UUID", f, u)
			}
		}
		if v := b["identifier"].(map[string]any)["value"].(string); !uuidURN.MatchString(v) {
			t.Errorf("%s: Bundle.identifier %q is not a urn:uuid holding a UUID", f, v)
		}
	}
	if fhir.DeterministicUUID("a", "b") != fhir.DeterministicUUID("a", "b") || fhir.DeterministicUUID("a", "b") == fhir.DeterministicUUID("ab") {
		t.Error("DeterministicUUID is not deterministic, or does not separate its parts")
	}
}

func TestAwkwardORUIdentifiers(t *testing.T) {
	res, b := hardFixture(t, "oru-awkward.hl7")
	p := resourcesOf(b, "Patient")[0]
	ids := map[string]map[string]any{}
	for _, x := range p["identifier"].([]any) {
		id := x.(map[string]any)
		ids[id["value"].(string)+"|"+strOf(id["system"])] = id
	}
	mc, ok := ids["112233|urn:oid:2.16.840.1.113883.3.879"]
	if !ok {
		t.Fatalf("the HD's ISO OID did not become urn:oid: %v", ids)
	}
	if per, _ := mc["period"].(map[string]any); per["start"] != "2020-01-01" || per["end"] != "2030-12-31" {
		t.Errorf("CX.7/CX.8 did not become Identifier.period: %v", mc["period"])
	}
	if !noteWith(res, "PID-3(1).4", "the V2-to-FHIR IG would use HD.1") {
		t.Error("the deviation from the IG's HD.1 rule is not stated")
	}
	for _, v := range []string{"999888", "777666"} {
		found := false
		for k, id := range ids {
			if strings.HasPrefix(k, v+"|") {
				found = true
				if id["type"] != nil {
					t.Errorf("%s from PID-2/PID-4 was given a type the sender did not send: %v", v, id["type"])
				}
			}
		}
		if !found {
			t.Errorf("identifier %s (PID-2 or PID-4) was dropped", v)
		}
	}
	if !noteWith(res, "PID-4", "withdrawn") || !noteWith(res, "PID-4", "ambiguous") {
		t.Error("PID-4 being withdrawn, and having no system, is not reported")
	}
}

func TestAwkwardORUValues(t *testing.T) {
	res, b := hardFixture(t, "oru-awkward.hl7")
	byCode := map[string]map[string]any{}
	for _, o := range resourcesOf(b, "Observation") {
		code := o["code"].(map[string]any)["coding"].([]any)[0].(map[string]any)["code"].(string)
		byCode[code] = o
	}
	if q := byCode["1234-5"]["valueQuantity"].(map[string]any); q["code"] != "ug/L" || q["system"] != "http://unitsofmeasure.org" {
		t.Errorf("ug/L is valid UCUM and should be coded: %v", q)
	}
	if q := byCode["4567-8"]["valueQuantity"].(map[string]any); q["comparator"] != nil || q["value"] != 5.0 {
		t.Errorf("SN =^5: %v", q)
	}
	if !noteWith(res, "OBX(4)-5.1", `"=" is not an R4 comparator`) {
		t.Error("dropping the = comparator is not reported")
	}
	if got := byCode["7890-1"]["valueString"]; got != "Line one\nLine two" {
		t.Errorf("TX repeats: got %q", got)
	}
	ed := byCode["6789-0"]
	if ed["valueString"] != nil {
		t.Errorf("the ED value is still copied as text: %v", ed["valueString"])
	}
	docs := resourcesOf(b, "DocumentReference")
	if len(docs) != 1 {
		t.Fatalf("want one DocumentReference for the ED result, got %d", len(docs))
	}
	att := docs[0]["content"].([]any)[0].(map[string]any)["attachment"].(map[string]any)
	if att["contentType"] != "application/pdf" || att["data"] != "JVBERi0xLjQK" {
		t.Errorf("attachment: %v", att)
	}
	if ref := ed["derivedFrom"].([]any)[0].(map[string]any)["reference"]; ref != "DocumentReference/"+docs[0]["id"].(string) {
		t.Errorf("the Observation does not point at the document: %v", ref)
	}
	if !noteWith(res, "ZLB", "Z segment") {
		t.Error("the unmapped ZLB segment is not reported")
	}
}

func TestAwkwardA08(t *testing.T) {
	res, b := hardFixture(t, "adt-a08-awkward.hl7")
	pats := resourcesOf(b, "Patient")
	if len(pats) != 1 {
		t.Fatalf("an A08 is not a merge, so only one Patient should be sent; got %d", len(pats))
	}
	p := pats[0]
	if p["link"] != nil {
		t.Errorf("an A08 carrying MRG was treated as a merge: %v", p["link"])
	}
	if !noteWith(res, "MRG", `prior identifier "998877" is still live`) {
		t.Error("MRG on an A08 is not reported")
	}
	ext := p["_birthDate"].(map[string]any)["extension"].([]any)[0].(map[string]any)
	if ext["url"] != "http://hl7.org/fhir/StructureDefinition/patient-birthTime" || ext["valueDateTime"] != "1980-01-01T12:00:00+10:00" {
		t.Errorf("PID-7's time of birth, in the sender's MSH-7 offset: %v", ext)
	}
	for _, x := range p["name"].([]any) {
		n := x.(map[string]any)
		if n["family"] == "NGUYEN" && n["use"] != nil {
			t.Errorf("XPN.7 B has no name-use equivalent, but use %v was set", n["use"])
		}
	}
	if !noteWith(res, "PID-5(2).7", `name type "B"`) {
		t.Error("the unmapped name type is not reported")
	}
	enc := resourcesOf(b, "Encounter")[0]
	if enc["status"] != "finished" {
		t.Errorf("an A08 with a discharge date in PV1-45: status %v, want finished (R4)", enc["status"])
	}
	if !noteWith(res, "ZPD", "Z segment") {
		t.Error("the unmapped ZPD segment is not reported")
	}
}

func TestA40MergeLinksTheRetiredRecord(t *testing.T) {
	msg := strings.Join([]string{
		"MSH|^~\\&|PAS|RIVERLAND|GW|RCVFAC|20260918060000+1000||ADT^A40^ADT_A39|TEST003|P|2.5",
		"EVN|A40|20260918055900+1000",
		"PID|1||441122^^^RIVERLAND^MR||TESTPATIENT^ALEX||19800101|F",
		"MRG|998877^^^RIVERLAND^MR",
	}, "\r")
	res, err := Convert(mustParse(t, msg), Options{AssigningAuthoritySystems: map[string]string{"RIVERLAND": "http://riverland.example/mrn"}})
	if err != nil {
		t.Fatal(err)
	}
	var survivor, retired *fhir.Patient
	for _, e := range res.Bundle.Entry {
		if p, ok := e.Resource.(*fhir.Patient); ok {
			if p.Identifier[0].Value == "441122" {
				survivor = p
			} else {
				retired = p
			}
		}
	}
	if survivor == nil || retired == nil {
		t.Fatal("a merge should send both the surviving and the retired patient")
	}
	if retired.Active == nil || *retired.Active || retired.Identifier[0].Value != "998877" || retired.Identifier[0].Use != "old" {
		t.Errorf("retired record: active %v, identifier %+v", retired.Active, retired.Identifier)
	}
	if len(retired.Link) != 1 || retired.Link[0].Type != "replaced-by" || retired.Link[0].Other.Reference != "Patient/"+survivor.ID {
		t.Errorf("retired link: %+v", retired.Link)
	}
	if len(survivor.Link) != 1 || survivor.Link[0].Type != "replaces" || survivor.Link[0].Other.Reference != "Patient/"+retired.ID {
		t.Errorf("survivor link: %+v", survivor.Link)
	}
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

func TestATitreIsNotWarnedForHavingNoUnit(t *testing.T) {
	// A titre is a ratio and has no unit by nature, and a range missing its unit is one result missing one unit: both used to
	// warn twice, once per number, which buried real warnings in a feed of serology results.
	for _, tc := range []struct {
		value string
		want  int
	}{{"^1^:^64", 0}, {"^5^-^10", 1}} {
		msg := "MSH|^~\\&|LAB|F|EHR|F|20260101120000||ORU^R01^ORU_R01|T1|P|2.5.1\rPID|1||1^^^F^MR||TEST^A\rOBR|1||L1|5196-1^HBsAg^LN\rOBX|1|SN|5196-1^HBsAg^LN||" + tc.value + "||||||F\r"
		m, err := hl7.Parse([]byte(msg))
		if err != nil {
			t.Fatal(err)
		}
		res, err := Convert(m, Options{})
		if err != nil {
			t.Fatal(err)
		}
		n := 0
		for _, note := range res.Notes {
			if strings.Contains(note.Message, "no unit") {
				n++
			}
		}
		if n != tc.want {
			t.Errorf("%s: %d missing-unit notes, want %d: %+v", tc.value, n, tc.want, res.Notes)
		}
	}
}

func TestLOINCCheckDigits(t *testing.T) {
	for code, want := range map[string]bool{"18748-4": true, "11502-2": true, "34117-2": true, "18748-5": false, "ABC-1": false, "187484": false} {
		if validLOINC(code) != want {
			t.Errorf("%s: want %v", code, want)
		}
	}
}

func TestADateThatIsNotAV2DateIsDropped(t *testing.T) {
	// From the NHS Wales sample set: a slashed date and a nine-digit one, which became 0110-19-48 and 1962-03-52.
	for _, dob := range []string{"01/10/1948", "196203520", "19620352"} {
		msg := "MSH|^~\\&|A|B|C|D|20260101120000||ADT^A04^ADT_A01|T1|P|2.5.1\rPID|1||1^^^F^MR||TEST^A||" + dob + "|F\r"
		m, _ := hl7.Parse([]byte(msg))
		res, err := Convert(m, Options{})
		if err != nil {
			t.Fatal(err)
		}
		body, _ := res.JSON()
		if strings.Contains(string(body), "birthDate") {
			t.Errorf("%s became a birth date: %s", dob, body)
		}
	}
}

func TestWhatTheNHSWalesSamplesExposed(t *testing.T) {
	msg := "MSH|^~\\&|A|B|C|D|20260101120000||VXU^V04^VXU_V04|T1|P|2.5.1\r" +
		"PID|1||1^^^F^MR||TEST^A||19800101|F||2106-3^White^CDCREC||||||||||||N^Not Hispanic or Latino^HL70189\r" +
		"NK1|1|TEST^B|SPOUSE^^HL70063\r" +
		"RXA|0|1|20250101||3^MMR^CVX|0.5|mL\r"
	m, _ := hl7.Parse([]byte(msg))
	res, err := Convert(m, Options{})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := res.JSON()
	s := string(body)
	for _, want := range []string{`"code": "2186-5"`, `"code": "03"`} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(s, "v2-0063") {
		t.Error("SPOUSE is not in table 0063 and must not be labelled with it")
	}
}

func TestAnAppointmentWithAStartAndADurationGetsAnEnd(t *testing.T) {
	msg := "MSH|^~\\&|A|B|C|D|20110601120000||SIU^S12^SIU_S12|T1|P|2.3\r" +
		"SCH|1|1|||1|OFFICE^Office visit|reason|OFFICE|60|m|||||||||||||||||Booked\r" +
		"PID|1||1^^^F^MR||TEST^A||19800101|F\r" +
		"AIL|1|A|OFFICE^^^OFFICE|^Main Office||20110614084500|||45|m^Minutes||Booked\r"
	m, _ := hl7.Parse([]byte(msg))
	res, _ := Convert(m, Options{})
	body, _ := res.JSON()
	if !strings.Contains(string(body), `"end": "2011-06-14T09:30:00Z"`) {
		t.Errorf("%s", body)
	}
}

// A time of birth with no offset on PID-7 or MSH-7, and none configured. Each answer is checked against the same input,
// 01:00 on 1 January, where a wrong offset moves the birth to another day and year.
func TestATimeOfBirthWithNoOffsetAnywhere(t *testing.T) {
	const msg = "MSH|^~\\&|A|B|C|D|20260916120000||ADT^A08^ADT_A01|1|P|2.5\rPID|1||123^^^H^MR||DOE^JANE||19800101010000|F\rPV1|1|O\r"
	pat := func(opts Options) (*Result, map[string]any) {
		res, err := Convert(mustParse(t, msg), opts)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := res.JSON()
		var b map[string]any
		_ = json.Unmarshal(out, &b)
		return res, resourcesOf(b, "Patient")[0]
	}

	// Nothing says what zone 01:00 is in, so no time is written, birthDate stays, and the loss is a warning.
	res, p := pat(Options{})
	if p["birthDate"] != "1980-01-01" || p["_birthDate"] != nil {
		t.Errorf("birthDate %v, birthTime %v: a time with no known offset should not be labelled", p["birthDate"], p["_birthDate"])
	}
	if !noteWith(res, "PID-7", "neither MSH-7 nor the configuration gives one") {
		t.Errorf("the dropped time of birth is not reported: %v", res.Notes)
	}

	// A configured timezone is evidence, and is used.
	syd, err := time.LoadLocation("Australia/Sydney")
	if err != nil {
		t.Skip(err)
	}
	_, p = pat(Options{Timezone: syd})
	ext := p["_birthDate"].(map[string]any)["extension"].([]any)[0].(map[string]any)
	if ext["valueDateTime"] != "1980-01-01T01:00:00+11:00" || p["birthDate"] != "1980-01-01" {
		t.Errorf("with a configured zone: %v, birthDate %v", ext, p["birthDate"])
	}
}
