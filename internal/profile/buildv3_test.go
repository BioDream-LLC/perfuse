package profile

import (
	"fmt"
	"strings"
	"testing"
)

// A v3 patient registry document, close enough to a real one that the paths and the places values live are genuine.
const v3Sample = `<?xml version="1.0" encoding="UTF-8"?>
<PRPA_IN201305UV02 xmlns="urn:hl7-org:v3">
  <id root="2.16.840.1.113883.3.1" extension="MSG%d"/>
  <creationTime value="20260822120000"/>
  <controlActProcess classCode="CACT" moodCode="EVN">
    <subject>
      <registrationEvent classCode="REG" moodCode="EVN">
        <statusCode code="active"/>
        <subject1>
          <patient classCode="PAT">
            <id root="2.16.840.1.113883.3.1.4" extension="MRN%06d"/>
            <statusCode code="active"/>
            <patientPerson>
              <name use="L">
                <given>%s</given>
                <family>%s</family>
              </name>
              <administrativeGenderCode code="%s" codeSystem="2.16.840.1.113883.5.1"/>
              <birthTime value="%s"/>
              <addr use="H">
                <streetAddressLine>%d Elm Street</streetAddressLine>
                <city>Birmingham</city>
                <state>AL</state>
              </addr>
            </patientPerson>
          </patient>
        </subject1>
      </registrationEvent>
    </subject>
  </controlActProcess>
</PRPA_IN201305UV02>`

// v3Corpus builds n documents that vary the way a real feed varies.
func v3Corpus(n int) [][]byte {
	genders := []string{"M", "F", "M", "F", "UN"}
	given := []string{"Anne", "Robert", "Mary", "James", "Sarah"}
	family := []string{"Dubois", "Nkemelu", "O'Hara", "Smith", "Zhang"}

	var out [][]byte
	for i := 0; i < n; i++ {
		doc := fmt.Sprintf(v3Sample,
			i, i, given[i%len(given)], family[i%len(family)],
			genders[i%len(genders)], fmt.Sprintf("1955%02d%02d", i%12+1, i%28+1), 100+i)
		out = append(out, []byte(doc))
	}

	return out
}

// fieldAt finds a path in the report, so a test asserts about the path it names rather than an index.
func fieldAt(t *testing.T, rep *Report, path string) Field {
	t.Helper()

	for _, seg := range rep.Segments {
		for _, f := range seg.Fields {
			if f.Path == path {
				return f
			}
		}
	}

	var paths []string
	for _, seg := range rep.Segments {
		for _, f := range seg.Fields {
			paths = append(paths, f.Path)
		}
	}
	t.Fatalf("no field at %s; the report has %d paths, for example %v", path, len(paths), first(paths, 8))

	return Field{}
}

func first(s []string, n int) []string {
	if len(s) < n {
		return s
	}

	return s[:n]
}

// TestTheV3ProfilerReadsAttributes is the whole reason this is not the v2 profiler with a different parser.
//
// In v3 nearly every value lives in an attribute: the identifiers, the codes, the gender, the birth date. A profiler reading only
// element text would report a document full of clinical data as having almost nothing populated, and would do it while appearing to
// work - which is the worst kind of wrong for a tool whose entire output is "here is what your feed contains".
func TestTheV3ProfilerReadsAttributes(t *testing.T) {
	rep := BuildV3(v3Corpus(200))

	if rep.Messages != 200 {
		t.Fatalf("read %d documents, want 200 (unreadable: %d)", rep.Messages, rep.Unreadable)
	}

	// The gender code, which lives in an attribute and nowhere else.
	gender := fieldAt(t, rep,
		"/PRPA_IN201305UV02/controlActProcess/subject/registrationEvent/subject1/patient/patientPerson/administrativeGenderCode@code")
	if gender.FillRate != 1 {
		t.Errorf("the gender code was populated in %.2f of documents, want 1", gender.FillRate)
	}
	if gender.Distinct != 3 {
		t.Errorf("counted %d distinct gender codes, want 3 (M, F, UN)", gender.Distinct)
	}

	// The birth date, likewise.
	birth := fieldAt(t, rep,
		"/PRPA_IN201305UV02/controlActProcess/subject/registrationEvent/subject1/patient/patientPerson/birthTime@value")
	if birth.FillRate != 1 {
		t.Errorf("the birth date was populated in %.2f of documents, want 1", birth.FillRate)
	}
	// Shape matters: an 8-digit birth date must read as a date, which is what makes the mapping suggestions usable
	// on a v3 feed.
	if birth.Shape != ShapeDate {
		t.Errorf("a YYYYMMDD birth date was profiled as %q rather than a date, and the mapping suggestions "+
			"read this to tell a date from an identifier", birth.Shape)
	}
}

// TestTheV3ProfilerKeepsPatientDataOut covers the line that matters more than any number here.
//
// Vocabulary is a list of the values actually seen. Collecting it for the wrong path puts patient data into a report that gets pasted
// into tickets and emailed to vendors. In v3 the identifiers live in root and extension, and names and addresses live in element text -
// so those are precisely the paths that must never carry a vocabulary.
func TestTheV3ProfilerKeepsPatientDataOut(t *testing.T) {
	rep := BuildV3(v3Corpus(200))

	base := "/PRPA_IN201305UV02/controlActProcess/subject/registrationEvent/subject1/patient"

	for _, path := range []string{
		base + "/id@extension",                         // the medical record number
		base + "/id@root",                              // the assigning authority, not identifying but not a code set either
		base + "/patientPerson/name/family",            // element text
		base + "/patientPerson/name/given",             // element text
		base + "/patientPerson/addr/streetAddressLine", // element text
		base + "/patientPerson/birthTime@value",        // a date is not a code set
		"/PRPA_IN201305UV02/id@extension",              // the message identifier
	} {
		f := fieldAt(t, rep, path)
		if len(f.Codes) > 0 {
			t.Errorf("%s carries %d recorded values, and a profile is not a place to keep them: %v",
				path, len(f.Codes), f.Codes)
		}
	}

	// And the other side, so the guard above is not simply "collect nothing": a coded attribute does carry its
	// vocabulary, which is what lets a contract check it.
	gender := fieldAt(t, rep, base+"/patientPerson/administrativeGenderCode@code")
	if len(gender.Codes) == 0 {
		t.Error("the gender code carries no vocabulary, so a 'one of' expectation on it could never be judged")
	}
}

// TestTheV3ProfilerCountsRepeatsRatherThanAttributes covers a mistake with an obvious wrong answer.
//
// Occurrences of an element are repetitions. Its attributes are not - an element with four attributes is one element. Folding them
// together would report every identifier as repeating four times, and MaxRepeats is what a receiver sizes a column from.
func TestTheV3ProfilerCountsRepeatsRatherThanAttributes(t *testing.T) {
	// Two names, the second with three attributes, so a wrong count is unambiguous.
	doc := []byte(`<PRPA_IN201305UV02 xmlns="urn:hl7-org:v3">
  <patient>
    <name use="L"><family>Dubois</family></name>
    <name use="P" qualifier="BR" code="X"><family>Nkemelu</family></name>
  </patient>
</PRPA_IN201305UV02>`)

	var corpus [][]byte
	for i := 0; i < 150; i++ {
		corpus = append(corpus, doc)
	}
	rep := BuildV3(corpus)

	// Asserted on an attribute path, not an element one, and a plant is why.
	//
	// The first version of this test read name(1)/family, which has no attributes - so folding attributes into their
	// element changed nothing about it and the test passed with the fault present. The count only goes wrong for a
	// path that has attributes to fold, and name carries three on its second occurrence.
	use := fieldAt(t, rep, "/PRPA_IN201305UV02/patient/name(1)@use")
	if use.MaxRepeats != 2 {
		t.Errorf("the name element reports %d repetitions, want 2; attributes are not repetitions of their "+
			"element, and folding them in reports every element as repeating once per attribute",
			use.MaxRepeats)
	}

	// And the element side, which must agree.
	f := fieldAt(t, rep, "/PRPA_IN201305UV02/patient/name(1)/family")
	if f.MaxRepeats != 2 {
		t.Errorf("the family element reports %d repetitions, want 2", f.MaxRepeats)
	}
}

// TestTheV3ProfilerReportsMoreThanOneInteraction covers a fact that changes how every rate should be read.
//
// A path populated in half the corpus may be populated in all of one interaction and none of another, which is a completely different
// statement from a field that is sometimes filled. Left unsaid, somebody tunes a contract threshold to 50% and it means nothing.
func TestTheV3ProfilerReportsMoreThanOneInteraction(t *testing.T) {
	corpus := v3Corpus(100)
	other := strings.ReplaceAll(string(corpus[0]), "PRPA_IN201305UV02", "PRPA_IN201306UV02")
	for i := 0; i < 100; i++ {
		corpus = append(corpus, []byte(other))
	}

	rep := BuildV3(corpus)

	if len(rep.Types) != 2 {
		t.Fatalf("reported %d interactions, want 2: %+v", len(rep.Types), rep.Types)
	}
	// Commonest first, and ties broken by name, because Go maps range randomly and a report that reorders itself
	// between runs cannot be diffed.
	if rep.Types[0].Count < rep.Types[1].Count {
		t.Errorf("interactions are not ordered commonest first: %+v", rep.Types)
	}

	found := false
	for _, n := range rep.Notes {
		if strings.Contains(n, "2 interactions") {
			found = true
		}
	}
	if !found {
		t.Errorf("nothing said the corpus mixes interactions, so every rate below reads as a fill rate: %v",
			rep.Notes)
	}
}

// TestAnUnparseableDocumentIsCountedNotSwallowed keeps the denominator honest.
func TestAnUnparseableDocumentIsCountedNotSwallowed(t *testing.T) {
	corpus := v3Corpus(150)
	corpus = append(corpus, []byte("this is not XML at all"), []byte("<unclosed>"))

	rep := BuildV3(corpus)

	if rep.Messages != 150 {
		t.Errorf("counted %d readable documents, want 150", rep.Messages)
	}
	if rep.Unreadable != 2 {
		t.Errorf("counted %d unreadable, want 2", rep.Unreadable)
	}
	found := false
	for _, n := range rep.Notes {
		if strings.Contains(n, "could not be parsed") {
			found = true
		}
	}
	if !found {
		t.Errorf("the unreadable documents are not mentioned, so a corpus half of which failed to parse would "+
			"look like a clean profile: %v", rep.Notes)
	}
}

// TestPathsAreSortedSoTheReportCanBeDiffed covers the ordering decision.
//
// The v2 profiler keeps segments in the order first seen, because a message's segments genuinely have a reading order. A v3 element
// tree does not give one across a corpus - first-seen order is whichever document happened to be first - so these are sorted, and a
// report that reorders itself between runs cannot be compared with the last one.
func TestPathsAreSortedSoTheReportCanBeDiffed(t *testing.T) {
	rep := BuildV3(v3Corpus(150))

	if len(rep.Segments) != 1 {
		t.Fatalf("expected one container standing for the document, got %d", len(rep.Segments))
	}

	fields := rep.Segments[0].Fields
	for i := 1; i < len(fields); i++ {
		if fields[i-1].Path > fields[i].Path {
			t.Fatalf("paths are not sorted: %q came before %q", fields[i-1].Path, fields[i].Path)
		}
	}
}
