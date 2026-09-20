package cda

import (
	"strings"
	"testing"
)

// The test that matters most for a generator is the round trip: generate a document, parse it back,
// and confirm the parser - which is the same code a receiver's parser has to agree with - finds what
// was put in. A generator verified only by string matching passes while emitting XML no parser reads.

func fullDocument() *Document {
	return &Document{
		ID:              "1.2.3.4^DOC1",
		Title:           "Continuity of Care",
		EffectiveTime:   "20260825120000-0500",
		Confidentiality: "N",
		LanguageCode:    "en-US",
		Patient: Patient{
			Identifiers: []Identifier{{Root: "2.16.840.1.113883.19.5", Extension: "MRN123"}},
			Family:      "Doe",
			Given:       []string{"Jane", "Q"},
			Prefix:      "Ms.",
			Gender:      "F",
			BirthTime:   "19800101",
			Phone:       "2055551234",
			Address: &Address{
				Use:    "HP",
				Lines:  []string{"123 Main St", "Apt 4"},
				City:   "Birmingham",
				State:  "AL",
				Postal: "35205",
			},
		},
		Authors: []Author{{
			Time:         "20260825120000-0500",
			Family:       "Attending",
			Given:        "Adam",
			ID:           "1.2.3.4.5",
			Organisation: "Site A",
		}},
		Encounter: &Encounter{
			ID:       "1.2.3^ENC1",
			Start:    "20260824",
			End:      "20260825",
			Location: "ICU",
		},
		Sections: []Section{
			{
				Kind: "Problems",
				Entries: []Entry{{
					Kind:          "Observation",
					Code:          "38341003",
					CodeSystem:    oidSNOMED,
					CodeName:      "Hypertension",
					StatusCode:    "completed",
					EffectiveTime: "20200115",
				}},
			},
			{
				Kind: "Results",
				Entries: []Entry{{
					Kind:          "Observation",
					Code:          "718-7",
					CodeSystem:    oidLOINC,
					CodeName:      "Hemoglobin",
					Value:         "13.5",
					Unit:          "g/dL",
					StatusCode:    "completed",
					EffectiveTime: "20260824",
				}},
			},
		},
	}
}

func generateOrFail(t *testing.T, d *Document) []byte {
	t.Helper()
	out, err := Generate(d, GenerateOptions{CustodianName: "Site A", Indent: true})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	return out
}

func TestGenerateRoundTripsThroughParse(t *testing.T) {
	out := generateOrFail(t, fullDocument())

	got, err := Parse(out)
	if err != nil {
		t.Fatalf("the generated document does not parse: %v\n\n%s", err, out)
	}

	if got.Title != "Continuity of Care" {
		t.Errorf("Title = %q", got.Title)
	}
	if got.TypeCode != ccdCode {
		t.Errorf("TypeCode = %q, want %s", got.TypeCode, ccdCode)
	}
	if got.DocumentType == "" {
		t.Error("DocumentType was not recognised, so the template identifiers did not survive")
	}
	if got.EffectiveTime != "20260825120000-0500" {
		t.Errorf("EffectiveTime = %q", got.EffectiveTime)
	}

	if got.Patient.Family != "Doe" {
		t.Errorf("Patient.Family = %q", got.Patient.Family)
	}
	if len(got.Patient.Given) != 2 || got.Patient.Given[0] != "Jane" {
		t.Errorf("Patient.Given = %v", got.Patient.Given)
	}
	if got.Patient.Gender != "F" {
		t.Errorf("Patient.Gender = %q", got.Patient.Gender)
	}
	if got.Patient.BirthTime != "19800101" {
		t.Errorf("Patient.BirthTime = %q", got.Patient.BirthTime)
	}

	if len(got.Patient.Identifiers) == 0 {
		t.Fatal("the patient identifier did not survive, so the document cannot be matched to a person")
	}
	if got.Patient.Identifiers[0].Extension != "MRN123" {
		t.Errorf("patient identifier extension = %q", got.Patient.Identifiers[0].Extension)
	}
	if got.Patient.Identifiers[0].Root != "2.16.840.1.113883.19.5" {
		t.Errorf("patient identifier root = %q", got.Patient.Identifiers[0].Root)
	}

	if got.Patient.Address == nil {
		t.Fatal("the address did not survive")
	}
	if got.Patient.Address.City != "Birmingham" {
		t.Errorf("address city = %q", got.Patient.Address.City)
	}
	if len(got.Patient.Address.Lines) != 2 {
		t.Errorf("address lines = %v", got.Patient.Address.Lines)
	}

	if len(got.Authors) == 0 {
		t.Fatal("the author did not survive")
	}
	if got.Authors[0].Family != "Attending" {
		t.Errorf("author family = %q", got.Authors[0].Family)
	}

	if got.Custodian == "" {
		t.Error("the custodian did not survive")
	}

	if got.Encounter == nil {
		t.Fatal("the encounter did not survive")
	}
	if got.Encounter.Start != "20260824" {
		t.Errorf("encounter start = %q", got.Encounter.Start)
	}

	if len(got.Sections) != 2 {
		t.Fatalf("sections = %d, want 2", len(got.Sections))
	}
}

func TestGenerateRoundTripsSectionsAndEntries(t *testing.T) {
	out := generateOrFail(t, fullDocument())
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}

	byKind := map[string]Section{}
	for _, s := range got.Sections {
		byKind[s.Kind] = s
	}

	problems, ok := byKind["Problems"]
	if !ok {
		t.Fatalf("the Problems section was not recognised; kinds present: %v", kindsOf(got.Sections))
	}
	if len(problems.Entries) != 1 {
		t.Fatalf("Problems entries = %d, want 1", len(problems.Entries))
	}
	if problems.Entries[0].Code != "38341003" {
		t.Errorf("problem code = %q", problems.Entries[0].Code)
	}
	if problems.Entries[0].CodeName != "Hypertension" {
		t.Errorf("problem displayName = %q", problems.Entries[0].CodeName)
	}
	if problems.Entries[0].StatusCode != "completed" {
		t.Errorf("problem statusCode = %q", problems.Entries[0].StatusCode)
	}

	results := byKind["Results"]
	if len(results.Entries) != 1 {
		t.Fatalf("Results entries = %d, want 1", len(results.Entries))
	}
	if results.Entries[0].Value != "13.5" {
		t.Errorf("result value = %q", results.Entries[0].Value)
	}
	if results.Entries[0].Unit != "g/dL" {
		t.Errorf("result unit = %q", results.Entries[0].Unit)
	}
}

func kindsOf(sections []Section) []string {
	var out []string
	for _, s := range sections {
		out = append(out, s.Kind+"/"+s.Code)
	}
	return out
}

// A section with entries and no narrative renders as a blank page in a viewer that reads only the
// narrative, which is most of them. The generator has to fill it in, and this is the check that
// proves it does rather than emitting an empty <text/>.
func TestGenerateFillsNarrativeFromEntries(t *testing.T) {
	d := fullDocument()
	for i := range d.Sections {
		d.Sections[i].NarrativeText = ""
	}

	out := generateOrFail(t, d)
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}

	for _, s := range got.Sections {
		if len(s.Entries) == 0 {
			continue
		}
		if strings.TrimSpace(s.NarrativeText) == "" {
			t.Errorf("section %q has %d entries but no narrative, so a viewer shows it blank",
				s.Kind, len(s.Entries))
		}
	}

	// The narrative must actually name the finding, not just exist.
	body := string(out)
	if !strings.Contains(body, "Hypertension") {
		t.Error("the generated narrative does not name the problem")
	}
	if !strings.Contains(body, "13.5") {
		t.Error("the generated narrative does not carry the result value")
	}
}

// Every entry should point at the narrative row describing it. Without the reference a viewer
// cannot connect the coded entry to the text, which is what makes a document navigable.
func TestGenerateLinksEntriesToNarrative(t *testing.T) {
	out := generateOrFail(t, fullDocument())
	body := string(out)

	if !strings.Contains(body, `<td ID="entry1">`) {
		t.Error("the narrative table has no row identifier for the first entry")
	}
	if !strings.Contains(body, `<reference value="#entry1"/>`) {
		t.Error("the entry does not reference its narrative row")
	}

	// And the parser should resolve that reference back to the row text.
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range got.Sections {
		for _, e := range s.Entries {
			if strings.TrimSpace(e.Text) == "" {
				t.Errorf("section %q entry %q did not resolve to narrative text", s.Kind, e.Code)
			}
		}
	}
}

// A negated entry says the patient does NOT have the finding. Losing negationInd on generation
// turns "no penicillin allergy" into "penicillin allergy", which is the worst outcome available here.
func TestGeneratePreservesNegation(t *testing.T) {
	d := fullDocument()
	d.Sections = []Section{{
		Kind: "Allergies",
		Entries: []Entry{{
			Kind:        "Observation",
			Code:        "91936005",
			CodeSystem:  oidSNOMED,
			CodeName:    "Penicillin allergy",
			NegationInd: true,
			StatusCode:  "completed",
		}},
	}}

	out := generateOrFail(t, d)
	if !strings.Contains(string(out), `negationInd="true"`) {
		t.Fatalf("negationInd was not written:\n%s", out)
	}

	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sections) != 1 || len(got.Sections[0].Entries) != 1 {
		t.Fatalf("expected one section with one entry, got %+v", got.Sections)
	}
	if !got.Sections[0].Entries[0].NegationInd {
		t.Error("negation did not survive the round trip, so an absent allergy now reads as present")
	}
}

// A stated absence ("no known allergies") is different from an empty section ("nobody looked").
func TestGeneratePreservesNilFlavor(t *testing.T) {
	d := fullDocument()
	d.Sections = []Section{{Kind: "Allergies", NilFlavor: "NI"}}

	out := generateOrFail(t, d)
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sections) != 1 {
		t.Fatalf("sections = %d", len(got.Sections))
	}
	if got.Sections[0].NilFlavor != "NI" {
		t.Errorf("NilFlavor = %q, want NI", got.Sections[0].NilFlavor)
	}
}

// A dose is a child of the medication it belongs to. Flattening the nesting loses which dose goes
// with which drug.
func TestGeneratePreservesChildEntries(t *testing.T) {
	d := fullDocument()
	d.Sections = []Section{{
		Kind: "Medications",
		Entries: []Entry{{
			Kind:       "SubstanceAdministration",
			Code:       "1191",
			CodeSystem: oidRxNorm,
			CodeName:   "Aspirin",
			StatusCode: "active",
			Children: []Entry{{
				Kind:       "Observation",
				Code:       "dose",
				CodeSystem: oidLOINC,
				Value:      "81",
				Unit:       "mg",
			}},
		}},
	}}

	out := generateOrFail(t, d)
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sections) != 1 || len(got.Sections[0].Entries) != 1 {
		t.Fatalf("expected one section with one entry: %+v", got.Sections)
	}
	entry := got.Sections[0].Entries[0]
	if entry.Kind != "SubstanceAdministration" {
		t.Errorf("entry kind = %q, want SubstanceAdministration", entry.Kind)
	}
	if len(entry.Children) == 0 {
		t.Fatal("the dose child entry did not survive")
	}
	if entry.Children[0].Value != "81" {
		t.Errorf("dose value = %q", entry.Children[0].Value)
	}
}

// The generated document must satisfy the validator in this same package. Two independent pieces of
// code agreeing is a stronger claim than either alone.
func TestGeneratedDocumentIsConformant(t *testing.T) {
	d := fullDocument()
	// A CCD requires these four sections, so supply the two the fixture lacks.
	d.Sections = append(d.Sections,
		Section{Kind: "Allergies", NilFlavor: "NI"},
		Section{Kind: "Medications", NilFlavor: "NI"},
	)

	out := generateOrFail(t, d)
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}

	report := Validate(got)
	if !report.Conformant() {
		for _, f := range report.Findings {
			if f.Severity == "error" {
				t.Errorf("generated document fails validation: [%s] %s (%s)", f.Rule, f.Message, f.Path)
			}
		}
		t.Fatalf("generated document is not conformant:\n%s", out)
	}
}

// ── Refusals ──────────────────────────────────────────────────────────────────
//
// Generation refuses rather than emitting a document a receiver will reject, because the receiver's
// error names an element while this names the decision that led to it.

func TestGenerateRefusesWithoutCustodian(t *testing.T) {
	d := fullDocument()
	d.Custodian = ""
	if _, err := Generate(d, GenerateOptions{}); err == nil {
		t.Fatal("a document with no custodian must be refused")
	}
}

func TestGenerateRefusesWithoutPatientName(t *testing.T) {
	d := fullDocument()
	d.Patient.Family = ""
	if _, err := Generate(d, GenerateOptions{CustodianName: "Site A"}); err == nil {
		t.Fatal("a document with no patient family name must be refused")
	}
}

func TestGenerateRefusesWithoutPatientIdentifier(t *testing.T) {
	d := fullDocument()
	d.Patient.Identifiers = nil
	if _, err := Generate(d, GenerateOptions{CustodianName: "Site A"}); err == nil {
		t.Fatal("a document with no patient identifier must be refused")
	}
}

func TestGenerateRefusesUnknownDocumentType(t *testing.T) {
	if _, err := Generate(fullDocument(), GenerateOptions{
		CustodianName: "Site A",
		DocumentType:  "Operative Note",
	}); err == nil {
		t.Fatal("an unsupported document type must be refused rather than guessed at")
	}
}

func TestGenerateRefusesNilDocument(t *testing.T) {
	if _, err := Generate(nil, GenerateOptions{CustodianName: "Site A"}); err == nil {
		t.Fatal("a nil document must be refused")
	}
}

// ── Escaping ──────────────────────────────────────────────────────────────────

// A patient named O'Brien & Sons, or a narrative containing "<", must not break the document.
func TestGenerateEscapesMetacharacters(t *testing.T) {
	d := fullDocument()
	d.Patient.Family = "O'Brien & <Sons>"
	d.Sections = []Section{{
		Kind:          "Problems",
		NarrativeText: `Weight < 50kg & falling`,
	}}

	out := generateOrFail(t, d)
	got, err := Parse(out)
	if err != nil {
		t.Fatalf("a document with metacharacters does not parse: %v\n\n%s", err, out)
	}
	if got.Patient.Family != "O'Brien & <Sons>" {
		t.Errorf("Patient.Family = %q, want the original with metacharacters intact", got.Patient.Family)
	}
	if !strings.Contains(got.Sections[0].NarrativeText, "< 50kg & falling") {
		t.Errorf("narrative = %q", got.Sections[0].NarrativeText)
	}
}

// ── Identifier splitting ──────────────────────────────────────────────────────

// Parse joins a root and extension as "root^extension". Generation has to split them back, or the
// receiver gets an identifier it cannot match against the one it issued.
func TestGenerateSplitsRootAndExtension(t *testing.T) {
	out := generateOrFail(t, fullDocument())
	body := string(out)
	if !strings.Contains(body, `<id root="1.2.3.4" extension="DOC1"/>`) {
		t.Errorf("the document id was not split into root and extension:\n%s", body)
	}
	if strings.Contains(body, `root="1.2.3.4^DOC1"`) {
		t.Error("the joined identifier was written as a root, which no receiver can match")
	}
}

func TestGeneratableSectionsIsSorted(t *testing.T) {
	got := GeneratableSections()
	if len(got) == 0 {
		t.Fatal("no generatable sections reported")
	}
	for i := 1; i < len(got); i++ {
		if got[i-1] > got[i] {
			t.Fatalf("not sorted at %d: %q before %q", i, got[i-1], got[i])
		}
	}
}

// A section kind the generator does not know still has to produce something a parser reads, using
// whatever code the caller supplied.
func TestGenerateUnknownSectionKindUsesSuppliedCode(t *testing.T) {
	d := fullDocument()
	d.Sections = []Section{{
		Kind:          "Something bespoke",
		Title:         "Local Section",
		Code:          "99999-9",
		CodeSystem:    oidLOINC,
		NarrativeText: "Local content",
	}}

	out := generateOrFail(t, d)
	got, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Sections) != 1 {
		t.Fatalf("sections = %d", len(got.Sections))
	}
	if got.Sections[0].Code != "99999-9" {
		t.Errorf("Code = %q, want the supplied 99999-9", got.Sections[0].Code)
	}
	if got.Sections[0].Title != "Local Section" {
		t.Errorf("Title = %q", got.Sections[0].Title)
	}
}
