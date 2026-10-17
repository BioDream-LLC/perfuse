package mpi

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/sqlitedb"
)

func openTest(t *testing.T) *Store {
	t.Helper()
	pool, err := sqlitedb.Open(filepath.Join(t.TempDir(), "mpi.db"), sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	s, err := Open(context.Background(), pool.Write)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const (
	hospital = "urn:oid:1.2.3.4.1"
	clinic   = "urn:oid:1.2.3.4.2"
	lab      = "urn:oid:1.2.3.4.3"
	ssn      = "http://hl7.org/fhir/sid/us-ssn"
)

func TestCompare(t *testing.T) {
	a := &Record{Family: "O'Brien", Given: "Siobhán", BirthDate: "1980-04-03", Sex: "F", Postal: "02139-1234", Phone: "(617) 555-0142"}
	same := Compare(a, &Record{Family: "OBRIEN", Given: "Siobhan", BirthDate: "19800403", Sex: "F", Postal: "02139", Phone: "+1 617 555 0142"}, nil)
	if same.Score < 0.95 || !same.Safe {
		t.Errorf("the same person written differently: %+v", same)
	}
	typo := Compare(a, &Record{Family: "O'Brian", Given: "Siobhan", BirthDate: "1980-03-04", Sex: "F", Postal: "02139"}, nil)
	if typo.Score < 0.6 || typo.Score >= same.Score || typo.Safe {
		t.Errorf("a typo and a swapped day and month: %+v", typo)
	}
	other := Compare(a, &Record{Family: "Smith", Given: "John", BirthDate: "1975-11-30", Sex: "M", Postal: "90210"}, nil)
	if other.Score > 0.05 {
		t.Errorf("someone else: %+v", other)
	}
	swapped := Compare(&Record{Family: "Nguyen", Given: "Anh", BirthDate: "1990-01-02"}, &Record{Family: "Anh", Given: "Nguyen", BirthDate: "1990-01-02"}, nil)
	if swapped.Score < 0.5 || swapped.Safe {
		t.Errorf("family and given swapped: %+v", swapped)
	}
	if jaroWinkler("MARTHA", "MARHTA") < 0.96 || soundex("Robert") != "R163" || soundex("Rupert") != "R163" || soundex("Tymczak") != "T522" {
		t.Error("string measures")
	}
}

func TestRegisterLinksReviewsAndUnlinks(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	set := Settings{Trusted: []string{ssn}}
	reg := func(r Record) *Outcome {
		t.Helper()
		o, err := s.Register(ctx, "default", "test", r, set)
		if err != nil {
			t.Fatal(err)
		}
		return o
	}
	first := reg(Record{System: hospital, Value: "MRN100", Family: "Garcia", Given: "Maria", BirthDate: "1972-08-15", Sex: "F",
		Postal: "60614", Phone: "312-555-0100", Identifiers: []string{ssn + "|123-45-6789"}})
	if first.Action != "new" || first.Record.EUID == "" {
		t.Fatalf("first: %+v", first)
	}
	// The clinic's record of her, written differently: linked by score.
	second := reg(Record{System: clinic, Value: "C-77", Family: "GARCÍA", Given: "María", BirthDate: "08/15/1972", Sex: "female",
		Postal: "60614-2201", Phone: "+1 (312) 555 0100"})
	_ = second
	// 08/15/1972 is not YYYYMMDD: its digits are 08151972, so the date disagrees and the record goes to review instead.
	if second.Action == "linked" {
		t.Errorf("a date in another order was linked without review: %+v", second)
	}
	third := reg(Record{System: clinic, Value: "C-78", Family: "Garcia", Given: "Maria", BirthDate: "1972-08-15", Sex: "F", Postal: "60614"})
	if third.Action != "linked" || third.How != "score" || third.Record.EUID != first.Record.EUID {
		t.Errorf("a clear match: %+v", third)
	}
	// The lab knows only her SSN, which the site trusts: linked on it alone.
	fourth := reg(Record{System: lab, Value: "L9", Family: "Garcia-Lopez", Identifiers: []string{ssn + "|123-45-6789"}})
	if fourth.Action != "linked" || !strings.HasPrefix(fourth.How, "identifier") || fourth.Record.EUID != first.Record.EUID {
		t.Errorf("a trusted identifier: %+v", fourth)
	}
	// A possible match, not certain: a new person, queued for review.
	fifth := reg(Record{System: clinic, Value: "C-79", Family: "Garcia", Given: "Mario", BirthDate: "1972-08-15", Sex: "M"})
	if fifth.Action != "review" || fifth.ReviewID == 0 || fifth.Record.EUID == first.Record.EUID {
		t.Fatalf("a possible match: %+v", fifth)
	}
	// Someone else entirely.
	if o := reg(Record{System: hospital, Value: "MRN200", Family: "Okafor", Given: "Chidi", BirthDate: "2001-02-03", Sex: "M"}); o.Action != "new" {
		t.Errorf("someone else: %+v", o)
	}
	// Re-registering a known record updates it and keeps its link.
	if o := reg(Record{System: hospital, Value: "MRN100", Family: "Garcia", Given: "Maria", BirthDate: "1972-08-15", Sex: "F", Line: "1 New St"}); o.Action != "updated" || o.Record.EUID != first.Record.EUID {
		t.Errorf("update: %+v", o)
	}

	reviews, err := s.Reviews(ctx, "default", 10)
	if err != nil {
		t.Fatal(err)
	}
	var mario *Review
	for i := range reviews {
		if reviews[i].ID == fifth.ReviewID {
			mario = &reviews[i]
		}
	}
	if mario == nil || mario.Candidate.EUID != first.Record.EUID || len(mario.Why.Fields) == 0 {
		t.Fatalf("reviews %+v", reviews)
	}
	if err := s.Decide(ctx, "default", "reviewer", fifth.ReviewID, false); err != nil {
		t.Fatal(err)
	}
	if err := s.Decide(ctx, "default", "reviewer", fifth.ReviewID, true); err == nil {
		t.Error("a review was decided twice")
	}

	p, err := s.Person(ctx, "default", first.Record.EUID)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Records) != 3 {
		t.Fatalf("Maria has %d records", len(p.Records))
	}
	if g := p.Golden(); g.Line != "1 New St" || g.Family == "" {
		t.Errorf("golden %+v", g)
	}
	ids := p.Identifiers("urn:perfuse:mpi")
	if ids[0] != "urn:perfuse:mpi|"+p.EUID || !containsStr(ids, ssn+"|123-45-6789") || !containsStr(ids, lab+"|L9") {
		t.Errorf("identifiers %v", ids)
	}
	byID, err := s.ByIdentifier(ctx, "default", lab+"|L9")
	if err != nil || byID.EUID != p.EUID {
		t.Errorf("by identifier: %v", err)
	}

	// The lab record was wrong after all: unlinked into a person of its own.
	newEUID, err := s.Unlink(ctx, "default", "reviewer", fourth.Record.ID, "different person, same SSN keyed in error")
	if err != nil || newEUID == p.EUID {
		t.Fatalf("unlink: %v", err)
	}
	// Then merged back, as an A40 would.
	if err := s.Merge(ctx, "default", "adt", newEUID, p.EUID, "A40"); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Person(ctx, "default", newEUID); err != ErrNotFound {
		t.Errorf("the merged-away person still exists: %v", err)
	}
	hist, _ := s.History(ctx, "default", p.EUID, 50)
	actions := map[string]bool{}
	for _, e := range hist {
		actions[e.Action] = true
	}
	for _, a := range []string{"register", "link", "merge", "not-same"} {
		if !actions[a] {
			t.Errorf("history has no %s: %+v", a, hist)
		}
	}
	c, _ := s.Count(ctx, "default")
	if c.Records != 6 || c.Linked < 1 {
		t.Errorf("counts %+v", c)
	}
	// Another tenant sees none of it.
	if c, _ := s.Count(ctx, "other"); c.Records != 0 {
		t.Errorf("tenant isolation: %+v", c)
	}
}

func TestFind(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)
	for _, r := range []Record{
		{System: hospital, Value: "1", Family: "Johnson", Given: "Emily", BirthDate: "1985-06-21", Sex: "F", Postal: "10001"},
		{System: hospital, Value: "2", Family: "Johnston", Given: "Emilie", BirthDate: "1985-06-21", Sex: "F"},
		{System: hospital, Value: "3", Family: "Lee", Given: "David", BirthDate: "1985-06-21", Sex: "M"},
	} {
		if _, err := s.Register(ctx, "default", "test", r, Settings{AutoLink: 0.99}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := s.Find(ctx, "default", Record{Family: "Johnson", Given: "Emily", BirthDate: "1985-06-21", Sex: "F"}, Settings{}, 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) < 2 || got[0].Person.Records[0].Value != "1" || got[0].Grade != "certain" || got[1].Grade == "certain" {
		for _, m := range got {
			t.Logf("%s %s %.3f %s", m.Person.EUID, m.Person.Records[0].Value, m.Score, m.Grade)
		}
		t.Fatal("the best match first, certain; the near one after it, not certain")
	}
	for _, m := range got {
		if m.Person.Records[0].Value == "3" {
			t.Error("a different person with the same birth date was offered")
		}
	}
	byID, _ := s.Find(ctx, "default", Record{Identifiers: []string{hospital + "|3"}}, Settings{}, 5)
	if len(byID) != 1 || byID[0].Grade != "certain" || byID[0].Person.Records[0].Value != "3" {
		t.Errorf("by identifier: %+v", byID)
	}
}

func containsStr(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func TestFromHL7AndFHIR(t *testing.T) {
	adt := "MSH|^~\\&|REG|HOSP|MPI|HOSP|20261017120000||ADT^A01|1|P|2.5\r" +
		"PID|1||556677^^^HOSP^MR~998877^^^&2.16.840.1.113883.4.1&ISO^SS||DOE&&Doe^JANE^Q||19800403|F|||12 Main St^^Springfield^IL^62701||(217)555-0101||||||123-45-6789\r"
	e, err := FromHL7([]byte(adt), FromOptions{Authorities: map[string]string{"HOSP": "urn:oid:1.2.3"}})
	if err != nil {
		t.Fatal(err)
	}
	r := e.Record
	if r.System != "urn:oid:1.2.3" || r.Value != "556677" || r.Family != "DOE" || r.Given != "JANE" || r.BirthDate != "19800403" ||
		r.Sex != "F" || r.Postal != "62701" || r.City != "Springfield" || r.Phone != "(217)555-0101" || e.MergeFrom != "" {
		t.Errorf("%+v", r)
	}
	if !containsStr(r.Identifiers, "urn:oid:2.16.840.1.113883.4.1|998877") || !containsStr(r.Identifiers, "http://hl7.org/fhir/sid/us-ssn|123-45-6789") {
		t.Errorf("identifiers %v", r.Identifiers)
	}
	a40 := "MSH|^~\\&|REG|HOSP|MPI|HOSP|20261017120000||ADT^A40|2|P|2.5\rPID|1||556677^^^HOSP^MR||DOE^JANE\rMRG|111222^^^HOSP^MR\r"
	if e, err := FromHL7([]byte(a40), FromOptions{}); err != nil || e.MergeFrom != "urn:perfuse:aa:HOSP|111222" {
		t.Errorf("merge: %+v %v", e, err)
	}
	if _, err := FromHL7([]byte("MSH|^~\\&|A|B|C|D|20261017||ORU^R01|3|P|2.5\rOBR|1\r"), FromOptions{}); err == nil {
		t.Error("a message with no PID")
	}
	fh, err := FromFHIR([]byte(`{"resourceType":"Patient","identifier":[{"system":"urn:oid:1.2.3","value":"9"},{"system":"urn:x","value":"Q"}],
		"name":[{"family":"Roe","given":["Ann"]}],"birthDate":"1990-01-02","gender":"female","telecom":[{"system":"phone","value":"555"}],
		"address":[{"line":["1 A St"],"city":"X","postalCode":"11111"}]}`))
	if err != nil || fh.Record.Key() != "urn:oid:1.2.3|9" || fh.Record.Sex != "F" || fh.Record.Line != "1 A St" || len(fh.Record.Identifiers) != 1 {
		t.Errorf("fhir: %+v %v", fh, err)
	}
	p := &Person{EUID: "E1", Records: []*Record{&fh.Record}}
	pat := ToFHIR(p, "urn:perfuse:mpi")
	if pat["id"] != "E1" || pat["birthDate"] != "1990-01-02" || pat["gender"] != "female" {
		t.Errorf("to fhir %v", pat)
	}
}
