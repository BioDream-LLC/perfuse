package contract

import (
	"fmt"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/profile"
)

// A v3 registry document. Values live in attributes, which is the whole reason a v2 profiler could not serve this.
const v3Doc = `<PRPA_IN201305UV02 xmlns="urn:hl7-org:v3">
  <id root="2.16.840.1.113883.3.1" extension="MSG%d"/>
  <controlActProcess classCode="CACT" moodCode="EVN">
    <subject>
      <registrationEvent classCode="REG" moodCode="EVN">
        <subject1>
          <patient classCode="PAT">
            <id root="2.16.840.1.113883.3.1.4" extension="MRN%06d"/>
            <patientPerson>
              <name use="L"><family>Dubois</family></name>
              <administrativeGenderCode code="%s" codeSystem="2.16.840.1.113883.5.1"/>
              <birthTime value="19551014"/>
            </patientPerson>
          </patient>
        </subject1>
      </registrationEvent>
    </subject>
  </controlActProcess>
</PRPA_IN201305UV02>`

const v3Patient = "/PRPA_IN201305UV02/controlActProcess/subject/registrationEvent/subject1/patient"

// v3Traffic builds a corpus, with genders drawn from the list given so a test can introduce an unexpected one.
func v3Traffic(n int, genders []string) [][]byte {
	var out [][]byte
	for i := 0; i < n; i++ {
		out = append(out, []byte(fmt.Sprintf(v3Doc, i, i, genders[i%len(genders)])))
	}

	return out
}

// TestAV3ContractIsJudgedAgainstV3Traffic is the end-to-end proof that the refusal is gone for a reason.
//
// A contract used to be refused at load on a v3 channel. The stated reason was that a contract is judged against a profile and the
// profiler read v2 segments and fields, which was true and was the entire obstacle: the contract machinery never cared whether a path
// named a segment and field or an element and attribute. It wants a path, a fill rate, a distinct count and a vocabulary.
func TestAV3ContractIsJudgedAgainstV3Traffic(t *testing.T) {
	rep := profile.BuildFor("hl7v3", v3Traffic(200, []string{"M", "F"}))

	c := &Contract{Expectations: []Expectation{
		// The medical record number, in an attribute.
		{Path: v3Patient + "/id@extension", Rule: Populated, Why: "the receiver keys on it"},
		// The gender code, likewise, and constrained to a vocabulary.
		{
			Path:   v3Patient + "/patientPerson/administrativeGenderCode@code",
			Rule:   OneOf,
			Values: []string{"M", "F", "UN"},
			Why:    "the receiver has three columns and no default",
		},
		// A path the sender does not send, which must read as absent rather than as unjudgeable.
		{Path: v3Patient + "/patientPerson/deceasedInd@value", Rule: Absent},
	}}

	res := Check(c, rep)

	if !res.Judged {
		t.Fatalf("a 200-document corpus was not judged: %s", res.Note)
	}
	if len(res.Unjudged) != 0 {
		t.Fatalf("expectations could not be evaluated against a v3 profile: %v", res.Unjudged)
	}
	if !res.Holds() {
		t.Fatalf("a contract that describes this feed correctly did not hold: %s", res.Summary())
	}
	if res.Checked != 3 {
		t.Errorf("checked %d expectations, want 3", res.Checked)
	}
}

// TestAV3ContractCatchesAChangeOfVocabulary is what a contract is for.
//
// Shadow mode and replay answer "did my change alter the output". Neither answers "has the sender started sending something new", and
// that is the question a contract exists to keep asking.
func TestAV3ContractCatchesAChangeOfVocabulary(t *testing.T) {
	// One in five documents now carries a gender code nobody agreed to.
	rep := profile.BuildFor("hl7v3", v3Traffic(200, []string{"M", "F", "M", "F", "A"}))

	c := &Contract{Expectations: []Expectation{{
		Path:   v3Patient + "/patientPerson/administrativeGenderCode@code",
		Rule:   OneOf,
		Values: []string{"M", "F", "UN"},
	}}}

	res := Check(c, rep)

	if res.Holds() {
		t.Fatalf("a new gender code in a fifth of documents did not break the contract: %s", res.Summary())
	}
	if len(res.Violations) != 1 {
		t.Fatalf("expected one violation, got %d", len(res.Violations))
	}

	v := res.Violations[0]
	// The unexpected value is named, which is safe here and only here: a permitted-value list is by definition about a
	// code set, so this cannot become a way to read patient data out of a report.
	if len(v.Unexpected) != 1 || v.Unexpected[0] != "A" {
		t.Errorf("the unexpected code was not named: %v", v.Unexpected)
	}
	if !strings.Contains(v.Says, v3Patient) {
		t.Errorf("the sentence does not name the path, so nobody can act on it: %q", v.Says)
	}
}

// TestAV3ContractCatchesAFieldFallingAway covers the other common sender change.
func TestAV3ContractCatchesAFieldFallingAway(t *testing.T) {
	// Half the documents stop carrying the medical record number.
	//
	// Built by regenerating without the element rather than by a literal string replacement, and the first version got
	// that wrong: the medical record number varies per document, so replacing one fixed spelling of it edited exactly
	// one document out of a hundred. The fill rate stayed at 99.5%, above the 99% default, and the test reported that
	// a field falling out of half the traffic did not break the contract - which was true of the corpus it had
	// actually built.
	docs := v3Traffic(100, []string{"M", "F"})
	for i := 0; i < 100; i++ {
		doc := fmt.Sprintf(v3Doc, i, i, "M")
		open := strings.Index(doc, `<id root="2.16.840.1.113883.3.1.4"`)
		if open < 0 {
			t.Fatal("the sample document no longer contains the patient identifier this strips")
		}
		end := strings.Index(doc[open:], "/>")
		docs = append(docs, []byte(doc[:open]+doc[open+end+2:]))
	}

	rep := profile.BuildFor("hl7v3", docs)

	res := Check(&Contract{Expectations: []Expectation{{
		Path: v3Patient + "/id@extension", Rule: Populated,
	}}}, rep)

	if res.Holds() {
		t.Fatalf("the medical record number falling out of half the traffic did not break the contract: %s",
			res.Summary())
	}
}

// TestAV3ProfileIsNotReadByTheV2Profiler pins the dispatch, which is the quiet failure in all of this.
//
// A v3 corpus read by the v2 profiler finds no segments, so the report has no fields. Every expectation then reads as violated, the
// contract goes red, and the operator goes looking at the sender - who has changed nothing. That is worse than the refusal this
// replaced, so the dispatch is asserted rather than assumed.
func TestAV3ProfileIsNotReadByTheV2Profiler(t *testing.T) {
	docs := v3Traffic(200, []string{"M", "F"})

	v3 := profile.BuildFor("hl7v3", docs)
	v2 := profile.BuildFor("", docs)

	if len(v3.Segments) == 0 || len(v3.Segments[0].Fields) == 0 {
		t.Fatal("the v3 profiler found no paths in v3 documents")
	}

	// The v2 reader on XML is the case that must not be mistaken for a working profile.
	v2Fields := 0
	for _, seg := range v2.Segments {
		v2Fields += len(seg.Fields)
	}
	if v2Fields >= len(v3.Segments[0].Fields) {
		t.Errorf("the v2 profiler found %d fields in XML and the v3 profiler found %d; if these are "+
			"comparable then the dispatch is not doing anything",
			v2Fields, len(v3.Segments[0].Fields))
	}
}
