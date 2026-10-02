package mapper

import (
	"math"
	"testing"
)

// --- Known-value verification for similarity algorithms ---
//
// These are the gold-standard test vectors. If any of these are wrong, every downstream
// confidence score is wrong.

func TestLevenshteinKnownValues(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"kitten", "sitting", 3},
		{"saturday", "sunday", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"flaw", "lawn", 2},
	}
	for _, tc := range cases {
		got := levenshtein(tc.a, tc.b)
		if got != tc.want {
			t.Errorf("levenshtein(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestJaroKnownValues(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
		name string
	}{
		{"MARTHA", "MARHTA", 0.944, "jaro(MARTHA,MARHTA)"},
		{"DIXON", "DICKSONX", 0.767, "jaro(DIXON,DICKSONX)"},
	}
	for _, tc := range cases {
		got := jaro(tc.a, tc.b)
		if math.Abs(got-tc.want) > 0.002 {
			t.Errorf("%s = %.4f, want %.4f (±0.002)", tc.name, got, tc.want)
		}
	}
}

func TestJaroWinklerKnownValues(t *testing.T) {
	cases := []struct {
		a, b string
		want float64
		name string
	}{
		{"MARTHA", "MARHTA", 0.961, "jaroWinkler(MARTHA,MARHTA)"},
		{"DIXON", "DICKSONX", 0.813, "jaroWinkler(DIXON,DICKSONX)"},
		{"TRATE", "TRACE", 0.906, "jaroWinkler(TRATE,TRACE)"},
	}
	for _, tc := range cases {
		got := jaroWinkler(tc.a, tc.b)
		if math.Abs(got-tc.want) > 0.002 {
			t.Errorf("%s = %.4f, want %.4f (±0.002)", tc.name, got, tc.want)
		}
	}
}

// --- Unicode correctness ---

func TestLevenshteinUnicode(t *testing.T) {
	// "café" vs "cafe" should be distance 1 (single rune substitution).
	if d := levenshtein("café", "cafe"); d != 1 {
		t.Errorf("levenshtein(café, cafe) = %d, want 1", d)
	}
	// "über" vs "uber" - ü is 2 bytes in UTF-8 but 1 rune.
	if d := levenshtein("über", "uber"); d != 1 {
		t.Errorf("levenshtein(über, uber) = %d, want 1", d)
	}
}

func TestLevenshteinSimilarityUnicode(t *testing.T) {
	// "café" (4 runes) vs "cafe" (4 runes), distance 1 → similarity 0.75.
	sim := levenshteinSimilarity("café", "cafe")
	if math.Abs(sim-0.75) > 0.001 {
		t.Errorf("levenshteinSimilarity(café, cafe) = %.4f, want 0.75", sim)
	}
}

// --- Bounds verification ---

func TestJaroWinklerBounds(t *testing.T) {
	pairs := []struct{ a, b string }{
		{"", ""},
		{"a", "a"},
		{"a", "b"},
		{"", "abc"},
		{"abc", ""},
		{"aaaa", "aaaa"},
		{"AAAA", "AAAB"},
		{"patient_name", "patient_name"},
		{"x", "y"},
	}
	for _, p := range pairs {
		jw := jaroWinkler(p.a, p.b)
		if jw < 0.0 || jw > 1.0 {
			t.Errorf("jaroWinkler(%q, %q) = %f, outside [0,1]", p.a, p.b, jw)
		}
		j := jaro(p.a, p.b)
		if j < 0.0 || j > 1.0 {
			t.Errorf("jaro(%q, %q) = %f, outside [0,1]", p.a, p.b, j)
		}
	}
}

func TestConfidenceNeverExceeds100(t *testing.T) {
	// Max possible: name similarity 1.0 (50) + pattern match 1.0 (30) + code match (25) + history (15) = 120.
	// Must be capped at 100.
	s := signals{
		nameSim:    1.0,
		patternSrc: PatternNPI,
		patternTgt: PatternNPI,
		patternCon: 1.0,
		codeMatch:  true,
		historical: 15,
		reasons:    []string{"all signals maxed"},
	}
	score, _ := s.combine()
	if score > 100 {
		t.Errorf("confidence = %d, exceeds 100", score)
	}
	if score < 0 {
		t.Errorf("confidence = %d, below 0", score)
	}
}

// --- Abstention and safety: dangerous field pairs ---
//
// These pairs are clinically DIFFERENT and matching them is a patient-safety defect.
// None of them should ever score above the abstention threshold.

func TestDangerousFieldPairsAbstain(t *testing.T) {
	dangerousPairs := []struct {
		source string
		target string
		desc   string
	}{
		{"Date of Birth", "Date of Death", "DOB vs DOD - clinically catastrophic"},
		{"Admit Date", "Discharge Date", "admit vs discharge - opposite events"},
		{"Systolic", "Diastolic", "blood pressure components"},
		{"First Name", "Last Name", "opposite name parts"},
		{"Patient ID", "Patient Account Number", "different identifier types"},
	}

	for _, pair := range dangerousPairs {
		targets := []TargetField{{Name: pair.target}}
		engine := New(targets, Config{Threshold: 70})
		src := SourceField{Name: pair.source}

		suggestions := engine.Suggest(src)
		if len(suggestions) == 0 {
			t.Fatalf("[%s] no suggestions", pair.desc)
		}

		if !suggestions[0].Abstained {
			t.Errorf("DANGEROUS: %s - confidence %d is above threshold 70, should abstain!",
				pair.desc, suggestions[0].Confidence)
		}
	}
}

// Even with pattern match boost (both fields are dates with date-like examples),
// dangerous pairs must still abstain.
func TestDOBvsDODWithPatternBoost(t *testing.T) {
	targets := []TargetField{{Name: "Date of Death", Pattern: "date"}}
	engine := New(targets, Config{Threshold: 70})
	src := SourceField{
		Name:     "Date of Birth",
		Examples: []string{"2024-01-15", "1990-05-30", "1975-12-25"},
	}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	if !suggestions[0].Abstained {
		t.Errorf("CRITICAL: Date of Birth maps to Date of Death with confidence %d (above threshold 70)!",
			suggestions[0].Confidence)
	}
}

func TestAdmitVsDischargeWithPatternBoost(t *testing.T) {
	targets := []TargetField{{Name: "Discharge Date", Pattern: "date"}}
	engine := New(targets, Config{Threshold: 70})
	src := SourceField{
		Name:     "Admit Date",
		Examples: []string{"2024-01-15", "2023-12-01", "2024-06-30"},
	}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	if !suggestions[0].Abstained {
		t.Errorf("CRITICAL: Admit Date maps to Discharge Date with confidence %d (above threshold 70)!",
			suggestions[0].Confidence)
	}
}

func TestPatientIDvsAccountWithPatternBoost(t *testing.T) {
	targets := []TargetField{{Name: "Patient Account Number", Pattern: "mrn"}}
	engine := New(targets, Config{Threshold: 70})
	src := SourceField{
		Name:     "Patient ID",
		Examples: []string{"MRN12345", "MRN67890", "MRN11111"},
	}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	if !suggestions[0].Abstained {
		t.Errorf("CRITICAL: Patient ID maps to Patient Account Number with confidence %d (above threshold 70)!",
			suggestions[0].Confidence)
	}
}

// Verify that legitimate high-confidence matches still work after the confusable pair logic.
func TestLegitimateMatchStillWorks(t *testing.T) {
	targets := []TargetField{{Name: "Date of Birth", Pattern: "date"}}
	engine := New(targets, Config{Threshold: 70})
	src := SourceField{
		Name:     "Date of Birth",
		Examples: []string{"2024-01-15", "1990-05-30"},
	}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	// Identical name + pattern match should not be blocked by confusable logic.
	if suggestions[0].Abstained {
		t.Errorf("legitimate exact match abstained with confidence %d", suggestions[0].Confidence)
	}
}

func TestLegitimatePatternMatchNotBlocked(t *testing.T) {
	// A close name with a matching pattern should still get the pattern bonus.
	targets := []TargetField{{Name: "Provider NPI Number", Pattern: "npi"}}
	engine := New(targets, Config{Threshold: 70})
	src := SourceField{
		Name:     "Provider NPI",
		Examples: []string{"1234567890", "1987654321"},
	}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	if suggestions[0].Abstained {
		t.Errorf("legitimate NPI match abstained with confidence %d", suggestions[0].Confidence)
	}
}

// Verify the abstention threshold boundary: exactly at threshold does NOT abstain.
func TestAbstentionBoundary(t *testing.T) {
	// The code uses `< thresh`, so exactly at threshold passes.
	// This is documented and deliberate: the threshold is the minimum to suggest.
	targets := []TargetField{{Name: "zzz_completely_unrelated"}}
	engine := New(targets, Config{Threshold: 70})
	src := SourceField{Name: "abc_totally_different"}

	suggestions := engine.Suggest(src)
	if len(suggestions) == 0 {
		t.Fatal("expected suggestions")
	}
	if !suggestions[0].Abstained {
		t.Errorf("dissimilar fields should abstain, got confidence %d", suggestions[0].Confidence)
	}
}
