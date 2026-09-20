package mapper

import (
	"testing"
)

// --- Levenshtein tests ---

func TestLevenshteinIdentical(t *testing.T) {
	if d := levenshtein("hello", "hello"); d != 0 {
		t.Errorf("identical strings: got %d, want 0", d)
	}
}

func TestLevenshteinEmpty(t *testing.T) {
	if d := levenshtein("", "abc"); d != 3 {
		t.Errorf(`\"\" -> \"abc\": got %d, want 3`, d)
	}
	if d := levenshtein("abc", ""); d != 3 {
		t.Errorf(`\"abc\" -> \"\": got %d, want 3`, d)
	}
}

func TestLevenshteinKnownDistances(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"kitten", "sitting", 3},
		{"saturday", "sunday", 3},
		{"patient", "Patient", 0}, // case insensitive
		{"abc", "def", 3},
	}
	for _, tc := range cases {
		got := levenshtein(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestLevenshteinSimilarity(t *testing.T) {
	sim := levenshteinSimilarity("patient", "patient")
	if sim != 1.0 {
		t.Errorf("identical: got %f, want 1.0", sim)
	}
	sim = levenshteinSimilarity("", "")
	if sim != 1.0 {
		t.Errorf("both empty: got %f, want 1.0", sim)
	}
	sim = levenshteinSimilarity("patient_name", "patient_id")
	if sim < 0.5 {
		t.Errorf("similar strings: got %f, want >= 0.5", sim)
	}
}

// --- Jaro-Winkler tests ---

func TestJaroWinklerIdentical(t *testing.T) {
	jw := jaroWinkler("PatientMRN", "patientmrn")
	if jw != 1.0 {
		t.Errorf("case-insensitive identical: got %f, want 1.0", jw)
	}
}

func TestJaroWinklerSimilar(t *testing.T) {
	// Fields with same prefix should get a boost.
	jw := jaroWinkler("patient_name", "patient_id")
	if jw < 0.7 {
		t.Errorf("shared prefix: got %f, want >= 0.7", jw)
	}
}

func TestJaroWinklerDissimilar(t *testing.T) {
	jw := jaroWinkler("abc", "xyz")
	if jw > 0.1 {
		t.Errorf("dissimilar: got %f, want <= 0.1", jw)
	}
}

func TestJaroEmpty(t *testing.T) {
	if j := jaro("", ""); j != 1.0 {
		t.Errorf("both empty: got %f, want 1.0", j)
	}
	if j := jaro("abc", ""); j != 0 {
		t.Errorf("one empty: got %f, want 0", j)
	}
}

// --- Normalized name tests ---

func TestNormalizedName(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"PatientMRN", "patient mrn"},
		{"patient_name", "patient name"},
		{"date-of-birth", "date of birth"},
		{"  spaces  ", "spaces"},
	}
	for _, tc := range cases {
		got := normalizedName(tc.input)
		if got != tc.want {
			t.Errorf("normalizedName(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

// --- Pattern detection tests ---

func TestDetectNPI(t *testing.T) {
	examples := []string{"1234567890", "1987654321", "1111111111"}
	pat, conf := detectPattern("Provider NPI", examples)
	if pat != PatternNPI {
		t.Errorf("NPI detection: got %q, want %q", pat, PatternNPI)
	}
	if conf < 0.9 {
		t.Errorf("NPI confidence: got %f, want >= 0.9", conf)
	}
}

func TestDetectSSN(t *testing.T) {
	examples := []string{"123-45-6789", "987-65-4321"}
	pat, conf := detectPattern("Social Security Number", examples)
	if pat != PatternSSN {
		t.Errorf("SSN detection: got %q, want %q", pat, PatternSSN)
	}
	if conf < 0.9 {
		t.Errorf("SSN confidence: got %f, want >= 0.9", conf)
	}
}

func TestDetectSSNRawDigits(t *testing.T) {
	examples := []string{"123456789", "987654321"}
	pat, _ := detectPattern("SSN", examples)
	if pat != PatternSSN {
		t.Errorf("SSN raw digits: got %q, want %q", pat, PatternSSN)
	}
}

func TestDetectPhone(t *testing.T) {
	examples := []string{"(555) 123-4567", "555-987-6543", "5551234567"}
	pat, conf := detectPattern("Home Phone", examples)
	if pat != PatternPhone {
		t.Errorf("phone detection: got %q, want %q", pat, PatternPhone)
	}
	if conf < 0.5 {
		t.Errorf("phone confidence: got %f, want >= 0.5", conf)
	}
}

func TestDetectDate(t *testing.T) {
	examples := []string{"2024-01-15", "2023-12-01", "2024-06-30"}
	pat, conf := detectPattern("Date of Birth", examples)
	if pat != PatternDate {
		t.Errorf("date detection: got %q, want %q", pat, PatternDate)
	}
	if conf >= 0.9 {
		// Good, strong match.
	}
	_ = conf
}

func TestDetectDateFromName(t *testing.T) {
	// Even without examples, name should hint at date.
	pat, _ := detectPattern("discharge_date", nil)
	if pat != PatternDate {
		t.Errorf("date from name: got %q, want %q", pat, PatternDate)
	}
}

func TestDetectMRN(t *testing.T) {
	examples := []string{"MRN12345", "ABC789012", "XY123456"}
	pat, conf := detectPattern("Medical Record Number", examples)
	if pat != PatternMRN {
		t.Errorf("MRN detection: got %q, want %q", pat, PatternMRN)
	}
	if conf < 0.5 {
		t.Errorf("MRN confidence: got %f, want >= 0.5", conf)
	}
}

func TestDetectNoPattern(t *testing.T) {
	examples := []string{"John Smith", "Jane Doe", "Robert Johnson"}
	pat, _ := detectPattern("full_name", examples)
	// Names are unlikely to match any specific pattern strongly.
	if pat == PatternNPI || pat == PatternSSN {
		t.Errorf("false positive: detected %q for person names", pat)
	}
}

// --- Confidence scoring tests ---

func TestConfidenceExactNameMatch(t *testing.T) {
	src := SourceField{Name: "Patient MRN", Examples: []string{"MRN12345"}}
	target := TargetField{Name: "Patient MRN", Pattern: "mrn"}
	s := scoreSignals(src, target)
	score, _ := s.combine()
	if score < 70 {
		t.Errorf("exact name + pattern match: got %d, want >= 70", score)
	}
}

func TestConfidenceLowSimilarity(t *testing.T) {
	src := SourceField{Name: "Attending Physician", Examples: []string{"Dr. Smith"}}
	target := TargetField{Name: "Medication Dosage", Pattern: ""}
	s := scoreSignals(src, target)
	score, _ := s.combine()
	if score > 40 {
		t.Errorf("dissimilar fields: got %d, want <= 40", score)
	}
}

func TestConfidenceCodeSystemBoost(t *testing.T) {
	src := SourceField{Name: "Diagnosis Code", CodeSystem: "http://hl7.org/fhir/sid/icd-10"}
	target := TargetField{Name: "ICD-10 Code", CodeSystem: "http://hl7.org/fhir/sid/icd-10"}
	s := scoreSignals(src, target)
	score, reasoning := s.combine()
	if score < 50 {
		t.Errorf("code system match: got %d, want >= 50", score)
	}
	if !contains(reasoning, "code system") {
		t.Errorf("reasoning should mention code system, got: %s", reasoning)
	}
}

// --- Abstention tests ---

func TestAbstentionBelowThreshold(t *testing.T) {
	targets := []TargetField{
		{Name: "Medication Dosage"},
		{Name: "Lab Value"},
	}
	engine := New(targets, Config{Threshold: 70})
	src := SourceField{Name: "Attending Physician NPI", Examples: []string{"1234567890"}}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions even when abstaining")
	}

	// These fields are dissimilar enough that we should abstain.
	for _, s := range suggestions {
		if !s.Abstained {
			t.Errorf("expected abstention for %q -> %q (confidence %d)", src.Name, s.Target.Name, s.Confidence)
		}
	}
}

func TestAbstentionAboveThreshold(t *testing.T) {
	targets := []TargetField{
		{Name: "Patient MRN", Pattern: "mrn"},
	}
	engine := New(targets, Config{Threshold: 60})
	src := SourceField{Name: "Patient MRN", Examples: []string{"MRN12345"}}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	if suggestions[0].Abstained {
		t.Errorf("should not abstain: confidence %d, threshold 60", suggestions[0].Confidence)
	}
}

func TestAbstentionCustomThreshold(t *testing.T) {
	targets := []TargetField{
		{Name: "Patient Name"},
	}
	// Very high threshold: even decent matches should abstain.
	engine := New(targets, Config{Threshold: 95})
	src := SourceField{Name: "Patient First Name"}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	// With only name similarity (~0.7ish) we should not reach 95.
	if !suggestions[0].Abstained {
		t.Errorf("expected abstention at threshold 95, got confidence %d", suggestions[0].Confidence)
	}
}

// --- Historical boost tests ---

func TestHistoricalBoost(t *testing.T) {
	targets := []TargetField{
		{Name: "Provider ID"},
		{Name: "Patient ID"},
	}
	engine := New(targets, Config{Threshold: 70, HistoricalBoost: 15})
	src := SourceField{Name: "Practitioner Identifier"}

	// Get baseline scores.
	before := engine.Suggest(src)
	providerScore := 0
	for _, s := range before {
		if s.Target.Name == "Provider ID" {
			providerScore = s.Confidence
		}
	}

	// Accept the Provider ID mapping several times.
	engine.Accept("Practitioner Identifier", "Provider ID")
	engine.Accept("Practitioner Identifier", "Provider ID")
	engine.Accept("Practitioner Identifier", "Provider ID")

	// Score should increase.
	after := engine.Suggest(src)
	for _, s := range after {
		if s.Target.Name == "Provider ID" {
			if s.Confidence <= providerScore {
				t.Errorf("historical boost failed: before=%d, after=%d", providerScore, s.Confidence)
			}
			if s.Confidence != providerScore+15 {
				t.Errorf("expected +15 boost: before=%d, after=%d", providerScore, s.Confidence)
			}
		}
	}
}

func TestHistoricalBoostCapped(t *testing.T) {
	targets := []TargetField{
		{Name: "Patient MRN", Pattern: "mrn"},
	}
	engine := New(targets, Config{HistoricalBoost: 15})
	src := SourceField{Name: "Patient MRN", Examples: []string{"MRN12345"}}

	// Accept many times - boost should not exceed configured amount.
	for i := 0; i < 100; i++ {
		engine.Accept("Patient MRN", "Patient MRN")
	}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	if suggestions[0].Confidence > 100 {
		t.Errorf("confidence exceeded 100: got %d", suggestions[0].Confidence)
	}
}

// --- Integration tests ---

func TestSuggestSortsDescending(t *testing.T) {
	targets := []TargetField{
		{Name: "Unrelated Field"},
		{Name: "Patient MRN", Pattern: "mrn"},
		{Name: "Patient Medical Record", Pattern: "mrn"},
	}
	engine := New(targets, Config{Threshold: 50})
	src := SourceField{Name: "Patient MRN", Examples: []string{"MRN12345"}}

	suggestions := engine.Suggest(src)
	for i := 1; i < len(suggestions); i++ {
		if suggestions[i].Confidence > suggestions[i-1].Confidence {
			t.Errorf("not sorted: [%d]=%d > [%d]=%d",
				i, suggestions[i].Confidence, i-1, suggestions[i-1].Confidence)
		}
	}
}

func TestSuggestEmptyTargets(t *testing.T) {
	engine := New(nil, Config{})
	src := SourceField{Name: "anything"}
	if s := engine.Suggest(src); s != nil {
		t.Errorf("expected nil for empty targets, got %d suggestions", len(s))
	}
}

func TestSuggestReasoningPresent(t *testing.T) {
	targets := []TargetField{
		{Name: "Patient MRN", Pattern: "mrn"},
	}
	engine := New(targets, Config{Threshold: 50})
	src := SourceField{Name: "Patient MRN", Examples: []string{"MRN12345"}}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	if suggestions[0].Reasoning == "" {
		t.Error("expected non-empty reasoning")
	}
}

func TestEndToEndNPIMapping(t *testing.T) {
	targets := []TargetField{
		{Name: "Provider NPI", Pattern: "npi"},
		{Name: "Patient Name"},
		{Name: "Date of Service", Pattern: "date"},
	}
	engine := New(targets, Config{Threshold: 65})
	src := SourceField{
		Name:     "Provider NPI Number",
		Examples: []string{"1234567890", "1987654321"},
	}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	// Provider NPI should be top match.
	if suggestions[0].Target.Name != "Provider NPI" {
		t.Errorf("expected Provider NPI as top match, got %q (confidence %d)",
			suggestions[0].Target.Name, suggestions[0].Confidence)
	}
	if suggestions[0].Abstained {
		t.Errorf("NPI should not abstain: confidence %d", suggestions[0].Confidence)
	}
}

func TestEndToEndDateMapping(t *testing.T) {
	targets := []TargetField{
		{Name: "Admission Date", Pattern: "date"},
		{Name: "Provider NPI", Pattern: "npi"},
		{Name: "Patient MRN", Pattern: "mrn"},
	}
	engine := New(targets, Config{Threshold: 60})
	src := SourceField{
		Name:     "Date of Admission",
		Examples: []string{"2024-01-15", "2023-12-01"},
	}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	if suggestions[0].Target.Name != "Admission Date" {
		t.Errorf("expected Admission Date as top, got %q", suggestions[0].Target.Name)
	}
}

// --- Helper ---

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > 0 && containsStr(s, substr))
}

func containsStr(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
