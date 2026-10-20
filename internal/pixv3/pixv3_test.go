package pixv3

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/hl7v3"
	"github.com/biodream-llc/perfuse/internal/mpi"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
)

const tenant = "main"

func rig(t *testing.T) (*Service, *mpi.Store) {
	t.Helper()
	pool, err := sqlitedb.Open(filepath.Join(t.TempDir(), "mpi.db"), sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	idx, err := mpi.Open(context.Background(), pool.Write)
	if err != nil {
		t.Fatal(err)
	}
	// The second hospital's number (asOtherIDs in IHE's samples) identifies one person, so records sharing it link; the
	// enterprise identifier has an OID, so v3 answers carry it.
	set, _ := idx.Settings(context.Background(), tenant)
	set.Trusted = []string{"urn:oid:1.2.840.114350.1.13.99997.2.3412"}
	set.EUIDSystem = "urn:oid:2.16.840.1.113883.3.9999.7"
	if err := idx.SaveSettings(context.Background(), tenant, "test", set); err != nil {
		t.Fatal(err)
	}
	return &Service{Index: idx}, idx
}

func sample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "ihe", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func handle(t *testing.T, s *Service, body []byte) *Answer {
	t.Helper()
	a, err := s.Handle(context.Background(), Request{Tenant: tenant, Actor: "test", Body: body})
	if err != nil {
		t.Fatal(err)
	}
	validate(t, a.Body)
	return a
}

// validate checks an answer against IHE's HL7 V3 schemas with xmllint, when PERFUSE_HL7V3_SCHEMA names the
// multicacheschemas directory (IHE/ITI-Info schema/HL7V3/NE2008/multicacheschemas).
func validate(t *testing.T, b []byte) {
	t.Helper()
	dir := os.Getenv("PERFUSE_HL7V3_SCHEMA")
	if dir == "" {
		return
	}
	if _, err := exec.LookPath("xmllint"); err != nil {
		t.Skip("no xmllint")
	}
	m, err := hl7v3.Parse(b)
	if err != nil {
		t.Fatalf("the answer does not parse: %v\n%s", err, b)
	}
	f := filepath.Join(t.TempDir(), "answer.xml")
	_ = os.WriteFile(f, b, 0o600)
	out, err := exec.Command("xmllint", "--noout", "--schema", filepath.Join(dir, m.InteractionID+".xsd"), f).CombinedOutput()
	if err != nil {
		t.Errorf("%s is not valid against its schema: %s\n%s", m.InteractionID, out, b)
	}
}

func ackType(t *testing.T, b []byte) string {
	t.Helper()
	m, err := hl7v3.Parse(b)
	if err != nil {
		t.Fatal(err)
	}
	return m.Root.Find("acknowledgement").First("typeCode").AttrValue("code")
}

func feedAll(t *testing.T, s *Service) {
	t.Helper()
	for _, f := range []string{"01_PatientRegistryRecordAdded1_20110504.xml", "03_PatientRegistryRecordAdded2_20110328.xml",
		"04_PatientRegistryRecordRevised2_20110328.xml"} {
		a := handle(t, s, sample(t, f))
		if got := ackType(t, a.Body); got != "AA" || a.Action != "urn:hl7-org:v3:MCCI_IN000002UV01" {
			t.Fatalf("%s: %s %s\n%s", f, got, a.Action, a.Body)
		}
	}
}

// IHE's own sequence: two records added, one revised, the PIX query asked and answered.
func TestIHEsSampleFeedThenQuery(t *testing.T) {
	s, idx := rig(t)
	feedAll(t, s)
	p, err := idx.ByIdentifier(context.Background(), tenant, "urn:oid:1.2.840.114350.1.13.99998.8734|34827G409")
	if err != nil || len(p.Records) != 2 {
		t.Fatalf("the two records sharing the trusted number were not one person: %v %+v", err, p)
	}
	a := handle(t, s, sample(t, "06_PIXQuery1_20110328.xml"))
	body := string(a.Body)
	if ackType(t, a.Body) != "AA" || !strings.Contains(body, `<queryResponseCode code="OK"/>`) {
		t.Fatalf("query: %s", body)
	}
	for _, want := range []string{`root="1.2.840.114350.1.13.99998.8734" extension="34827G409"`, `root="2.16.840.1.113883.4.1" extension="999-99-4452"`,
		`root="2.16.840.1.113883.3.9999.7" extension="` + p.EUID + `"`, `<queryId root="1.2.840.114350.1.13.99999.4567.34" extension="33452"/>`} {
		if !strings.Contains(body, want) {
			t.Errorf("the answer lacks %s:\n%s", want, body)
		}
	}
	// The queried-for identifier is given back only in the query it repeats (ITI-45 case 1).
	if strings.Count(body, `extension="38273N237"`) != 1 {
		t.Errorf("the queried-for identifier was returned as a cross-reference:\n%s", body)
	}
	if a.Target != "urn:oid:1.2.840.114350.1.13.99997.2.3412|38273N237" {
		t.Errorf("audit target %q", a.Target)
	}
}

func TestADataSourceLimitsTheAnswer(t *testing.T) {
	s, _ := rig(t)
	feedAll(t, s)
	q := string(sample(t, "06_PIXQuery1_20110328.xml"))
	ds := `<dataSource><value root="2.16.840.1.113883.4.1"/><semanticsText>DataSource.id</semanticsText></dataSource>`
	one := strings.Replace(q, "<patientIdentifier>", ds+"<patientIdentifier>", 1)
	body := string(handle(t, s, []byte(one)).Body)
	if !strings.Contains(body, `extension="999-99-4452"`) || strings.Contains(body, `extension="34827G409"`) {
		t.Errorf("a data source did not limit the answer to its domain:\n%s", body)
	}
	// A domain the index has never seen is an error naming the parameter (case 5).
	unknown := strings.Replace(q, "<patientIdentifier>", strings.Replace(ds, "2.16.840.1.113883.4.1", "1.2.3.4.5", 1)+"<patientIdentifier>", 1)
	a := handle(t, s, []byte(unknown))
	body = string(a.Body)
	if ackType(t, a.Body) != "AE" || !strings.Contains(body, `code="204"`) || !strings.Contains(body, "dataSource[1]/value") ||
		strings.Contains(body, "<registrationEvent") {
		t.Errorf("an unknown domain:\n%s", body)
	}
}

func TestAnUnknownPatientIsAnError(t *testing.T) {
	s, _ := rig(t)
	feedAll(t, s)
	q := strings.Replace(string(sample(t, "06_PIXQuery1_20110328.xml")), `extension="38273N237"`, `extension="NOBODY"`, 1)
	a := handle(t, s, []byte(q))
	body := string(a.Body)
	if ackType(t, a.Body) != "AE" || !strings.Contains(body, `<queryResponseCode code="AE"/>`) || !strings.Contains(body, `code="204"`) ||
		a.Outcome != "not-found" {
		t.Errorf("an unknown patient (case 4):\n%s", body)
	}
}

func TestADuplicatesResolvedMessageMerges(t *testing.T) {
	s, idx := rig(t)
	// Without the trusted number, the two records would be two people until somebody said otherwise; the merge says so.
	set, _ := idx.Settings(context.Background(), tenant)
	set.Trusted = nil
	_ = idx.SaveSettings(context.Background(), tenant, "test", set)
	feedAll(t, s)
	a, _ := idx.ByIdentifier(context.Background(), tenant, "urn:oid:1.2.840.114350.1.13.99998.8734|34827G234")
	b, _ := idx.ByIdentifier(context.Background(), tenant, "urn:oid:1.2.840.114350.1.13.99998.8734|34827G409")
	if a == nil || b == nil || a.EUID == b.EUID {
		t.Fatalf("expected two people before the merge")
	}
	ans := handle(t, s, sample(t, "05_PatientRegistryDuplicatesResolved_20110504.xml"))
	if ackType(t, ans.Body) != "AA" {
		t.Fatalf("merge: %s", ans.Body)
	}
	a, _ = idx.ByIdentifier(context.Background(), tenant, "urn:oid:1.2.840.114350.1.13.99998.8734|34827G234")
	b, _ = idx.ByIdentifier(context.Background(), tenant, "urn:oid:1.2.840.114350.1.13.99998.8734|34827G409")
	if a == nil || b == nil || a.EUID != b.EUID {
		t.Errorf("after the merge the two numbers are still two people")
	}
	// A merge that does not say what it replaces is refused, not guessed.
	broken := strings.Replace(string(sample(t, "05_PatientRegistryDuplicatesResolved_20110504.xml")), `extension="34827G234"`, ``, 1)
	if got := handle(t, s, []byte(broken)); ackType(t, got.Body) != "AE" {
		t.Errorf("a merge with no prior record: %s", got.Body)
	}
}

func TestAFeedWithNoIdentifierIsRefused(t *testing.T) {
	s, _ := rig(t)
	f := strings.Replace(string(sample(t, "01_PatientRegistryRecordAdded1_20110504.xml")), `extension="34827G234"`, ``, 1)
	a := handle(t, s, []byte(f))
	if ackType(t, a.Body) != "AE" || !strings.Contains(string(a.Body), `code="101"`) {
		t.Errorf("%s", a.Body)
	}
}

// IHE's PDQ sample asks for two at a time, continues, and cancels.
func TestPDQWithContinuation(t *testing.T) {
	s, idx := rig(t)
	// IHE's query looks for Jimmy Jones, male, born 4 August 1963: three people close to that, one far from it.
	for i, r := range []mpi.Record{
		{System: "urn:oid:1.2.840.114350.1.13.99998.8734", Value: "34827K410", Family: "Jones", Given: "Jimmy", Sex: "M", BirthDate: "1963-08-04", City: "Some City"},
		{System: "urn:oid:1.2.840.114350.1.13.99998.8734", Value: "34827R534", Family: "Jones", Given: "Jim", Sex: "M", BirthDate: "1963-08-04"},
		{System: "urn:oid:1.2.840.114350.1.13.99998.8734", Value: "34827R999", Family: "Jones", Given: "James", Sex: "M", BirthDate: "1963-08-04"},
		{System: "urn:oid:1.2.840.114350.1.13.99998.8734", Value: "34827Z001", Family: "Smith", Given: "Ann", Sex: "F", BirthDate: "1990-01-01"},
	} {
		r.Identifiers = []string{"urn:oid:1.2.840.114350.1.13.99997.2.3412|38273D43" + string(rune('0'+i))}
		if _, err := idx.RegisterWithSettings(context.Background(), tenant, "test", r); err != nil {
			t.Fatal(err)
		}
	}
	q := sample(t, "01_PDQQuery1_20110328.xml")
	// The sample names a third domain (a national number); the index has never seen it, which is an error (ITI-47 case 4).
	a := handle(t, s, q)
	if ackType(t, a.Body) != "AE" || !strings.Contains(string(a.Body), "otherIDsScopingOrganization[2]/value") {
		t.Fatalf("an unknown domain in the PDQ query: %s", a.Body)
	}
	q = []byte(strings.Replace(string(q), `<otherIDsScopingOrganization>
                    <value root="2.16.840.1.113883.4.1"/>
                    <semanticsText>OtherIDs.scopingOrganization.id</semanticsText>
                </otherIDsScopingOrganization>`, "", 1))
	// The degree of match asked for is 75: lowered, so the near names count too.
	q = []byte(strings.Replace(string(q), `value="75"`, `value="50"`, 1))
	a = handle(t, s, q)
	body := string(a.Body)
	if ackType(t, a.Body) != "AA" || strings.Count(body, "<registrationEvent") != 2 || !strings.Contains(body, `extension="34827K410"`) ||
		!strings.Contains(body, `<resultCurrentQuantity value="2"/>`) || !strings.Contains(body, "<given>Jimmy</given>") ||
		strings.Contains(body, "Smith") {
		t.Fatalf("first page:\n%s", body)
	}
	if !strings.Contains(body, `<resultRemainingQuantity value="1"/>`) {
		t.Fatalf("remaining:\n%s", body)
	}
	c := handle(t, s, sample(t, "03_PDQQuery1Continuation-1_20110328.xml"))
	if strings.Count(string(c.Body), "<registrationEvent") != 1 || !strings.Contains(string(c.Body), `<resultRemainingQuantity value="0"/>`) {
		t.Fatalf("continuation:\n%s", c.Body)
	}
	// Nothing is held after the last page, and a cancel is acknowledged regardless.
	if again := handle(t, s, sample(t, "03_PDQQuery1Continuation-1_20110328.xml")); ackType(t, again.Body) != "AE" {
		t.Errorf("a finished query continued: %s", again.Body)
	}
	if x := handle(t, s, sample(t, "05_PDQQuery1Cancel_20110328.xml")); ackType(t, x.Body) != "AA" || x.Action != "urn:hl7-org:v3:MCCI_IN000002UV01" {
		t.Errorf("cancel: %s", x.Body)
	}
}

func TestContinuationsExpire(t *testing.T) {
	s, idx := rig(t)
	now := time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }
	for _, v := range []string{"A1", "A2", "A3"} {
		_, _ = idx.RegisterWithSettings(context.Background(), tenant, "test", mpi.Record{System: "urn:oid:1.2.840.114350.1.13.99998.8734",
			Value: v, Family: "Jones", Given: "Jimmy", Sex: "M", BirthDate: "1963-08-04"})
	}
	q := regexp.MustCompile(`(?s)<otherIDsScopingOrganization>.*?</otherIDsScopingOrganization>`).
		ReplaceAllString(string(sample(t, "01_PDQQuery1_20110328.xml")), "")
	if a := handle(t, s, []byte(q)); !strings.Contains(string(a.Body), `<resultRemainingQuantity value="1"/>`) {
		t.Fatalf("%s", a.Body)
	}
	now = now.Add(11 * time.Minute)
	if a := handle(t, s, sample(t, "03_PDQQuery1Continuation-1_20110328.xml")); ackType(t, a.Body) != "AE" {
		t.Errorf("a continuation after ten minutes was answered: %s", a.Body)
	}
}

func TestAQueryWithNoDemographicsIsAnError(t *testing.T) {
	s, _ := rig(t)
	q := `<PRPA_IN201305UV02 xmlns="urn:hl7-org:v3" ITSVersion="XML_1.0"><id root="1.2.3" extension="1"/>
<creationTime value="20261019120000"/><interactionId root="2.16.840.1.113883.1.6" extension="PRPA_IN201305UV02"/>
<processingCode code="P"/><processingModeCode code="T"/><acceptAckCode code="AL"/>
<receiver typeCode="RCV"><device classCode="DEV" determinerCode="INSTANCE"><id root="1.2.3.9"/></device></receiver>
<sender typeCode="SND"><device classCode="DEV" determinerCode="INSTANCE"><id root="1.2.3.8"/></device></sender>
<controlActProcess classCode="CACT" moodCode="EVN"><code code="PRPA_TE201305UV02" codeSystem="2.16.840.1.113883.1.18"/>
<queryByParameter><queryId root="1.2.3.7" extension="9"/><statusCode code="new"/><responseModalityCode code="R"/>
<responsePriorityCode code="I"/><parameterList></parameterList></queryByParameter></controlActProcess></PRPA_IN201305UV02>`
	a := handle(t, s, []byte(q))
	if ackType(t, a.Body) != "AE" || !strings.Contains(string(a.Body), `code="101"`) {
		t.Errorf("%s", a.Body)
	}
}

func TestOtherInteractionsAreNotAnswered(t *testing.T) {
	s, _ := rig(t)
	if _, err := s.Handle(context.Background(), Request{Tenant: tenant, Body: []byte(`<MCCI_IN000002UV01 xmlns="urn:hl7-org:v3"/>`)}); err == nil {
		t.Error("an acknowledgement was treated as a request")
	}
	if _, err := s.Handle(context.Background(), Request{Tenant: tenant, Body: []byte(`not xml`)}); err == nil {
		t.Error("not XML was answered")
	}
}
