package profile

import (
	"fmt"
	"strings"
	"testing"
)

// suggestCorpus builds a set of messages varying the fields these tests reason about.
//
// Real-shaped rather than minimal, because the whole point of inference is that it reads behaviour across a corpus - and a corpus
// of two identical messages would make every field look like a constant, which is the failure the minimum-sample rule exists for.
func suggestCorpus(t *testing.T, n int) [][]byte {
	t.Helper()

	genders := []string{"M", "F"}
	out := make([][]byte, 0, n)

	for i := 0; i < n; i++ {
		msg := fmt.Sprintf(
			"MSH|^~\\&|EPIC|SITEA|PERFUSE|RFAC|20260822120000||ADT^A01|CTRL%04d|P|2.5.1\r"+
				"PID|1||%08d^^^SITEA^MR||SMITH^PATIENT%d||19%02d0101|%s|||1 High Street^^Lyon\r",
			i, 1000000+i, i, 50+(i%40), genders[i%2])
		out = append(out, []byte(msg))
	}

	return out
}

// find returns the suggestion for a path.
func find(t *testing.T, all []Suggestion, path string) Suggestion {
	t.Helper()

	for _, s := range all {
		if s.Path == path {
			return s
		}
	}
	t.Fatalf("no suggestion for %s", path)

	return Suggestion{}
}

// TestSuggestReadsRolesFromBehaviour is the point of the whole file.
func TestSuggestReadsRolesFromBehaviour(t *testing.T) {
	report := Build(suggestCorpus(t, 100))

	all, why := Suggest(report, SuggestOptions{})
	if why != "" {
		t.Fatalf("refused to suggest: %s", why)
	}
	if len(all) == 0 {
		t.Fatal("no suggestions were produced")
	}

	t.Run("a unique value per message is an identifier", func(t *testing.T) {
		// PID-3, the field. The profile reports fields rather than components, which is worth knowing: the
		// first version of this test asked for PID-3.1 and found nothing.
		got := find(t, all, "PID-3")
		if got.Role != RoleIdentifier {
			t.Errorf("PID-3 is %q, want an identifier", got.Role)
		}
	})

	t.Run("a purely numeric identifier warns about being stored as a number", func(t *testing.T) {
		// The warning that matters, because it fails late and expensively: leading zeros are lost and the
		// first non-numeric value breaks the receiver. Its own case with its own corpus, because a real MRN
		// field carries components and is therefore not purely numeric.
		var numeric [][]byte
		for i := 0; i < 40; i++ {
			numeric = append(numeric, []byte(fmt.Sprintf(
				"MSH|^~\\&|EPIC|SITEA|PERFUSE|RFAC|20260822120000||ADT^A01|CTRL%04d|P|2.5.1\r"+
					"PID|1||%08d||SMITH^JOHN||19700101|M\r", i, 1000000+i)))
		}

		only, why := Suggest(Build(numeric), SuggestOptions{})
		if why != "" {
			t.Fatal(why)
		}
		got := find(t, only, "PID-3")
		if got.Role != RoleIdentifier {
			t.Fatalf("PID-3 is %q", got.Role)
		}
		if !strings.Contains(got.Caution, "leading zeros") {
			t.Errorf("a numeric identifier does not warn about being stored as a number: %q", got.Caution)
		}
	})

	t.Run("two short values are a flag or a code set", func(t *testing.T) {
		got := find(t, all, "PID-8")
		if got.Role != RoleFlag && got.Role != RoleCodeSet {
			t.Errorf("PID-8 with two values is %q", got.Role)
		}
		// The observed vocabulary, which is what somebody needs to write a mapping table.
		if len(got.Vocabulary) != 2 {
			t.Errorf("the vocabulary is %v, want both observed values", got.Vocabulary)
		}
	})

	t.Run("a date is recognised as one", func(t *testing.T) {
		got := find(t, all, "PID-7")
		if got.Role != RoleTimestamp {
			t.Errorf("PID-7 is %q, want a date", got.Role)
		}
	})

	t.Run("a value that never varies is a constant, with a warning", func(t *testing.T) {
		got := find(t, all, "MSH-3")
		if got.Role != RoleConstant {
			t.Errorf("MSH-3 is %q, want a constant", got.Role)
		}
		// The caution is the substance here: a field constant across one sample is very often constant because
		// the sample came from one sender on one day.
		if got.Caution == "" {
			t.Error("a constant is reported without warning that a single sample may be why")
		}
	})

	t.Run("every suggestion carries its evidence", func(t *testing.T) {
		for _, s := range all {
			if len(s.Evidence) == 0 {
				t.Errorf("%s has no evidence, so it can only be accepted on faith", s.Path)
			}
			for _, e := range s.Evidence {
				if strings.TrimSpace(e) == "" {
					t.Errorf("%s has an empty evidence line", s.Path)
				}
			}
		}
	})

	t.Run("the sample size is reported next to the agreement", func(t *testing.T) {
		got := find(t, all, "PID-3")
		if got.Sampled == 0 {
			t.Error("the sample size is not reported, so the agreement figure has no denominator and " +
				"a perfect score over three messages reads like one over four hundred")
		}
	})
}

// TestSuggestRefusesASampleTooSmallToMeanAnything pins the guard.
//
// Every field in a sample of three looks like a constant or an identifier. Returning weak suggestions with a caveat attached would
// be worse than returning none: a caveat on a page of confident-looking rows is read once and then ignored.
func TestSuggestRefusesASampleTooSmallToMeanAnything(t *testing.T) {
	report := Build(suggestCorpus(t, 3))

	all, why := Suggest(report, SuggestOptions{})
	if len(all) != 0 {
		t.Errorf("produced %d suggestions from 3 messages", len(all))
	}
	if why == "" {
		t.Fatal("refused without saying why")
	}
	if !strings.Contains(why, "constant or an identifier") {
		t.Errorf("the refusal does not explain what goes wrong with a small sample: %q", why)
	}
}

// TestSuggestNamesAFieldBehavingUnlikeItsName is the most useful single output.
//
// A field the standard calls a date carrying something else means this sender is using it for another purpose, and that is exactly
// what takes an afternoon to find by reading messages.
func TestSuggestNamesAFieldBehavingUnlikeItsName(t *testing.T) {
	// PID-7 is the date of birth. Here it carries a word.
	var corpus [][]byte
	for i := 0; i < 50; i++ {
		corpus = append(corpus, []byte(fmt.Sprintf(
			"MSH|^~\\&|EPIC|SITEA|PERFUSE|RFAC|20260822120000||ADT^A01|CTRL%04d|P|2.5.1\r"+
				"PID|1||%08d^^^SITEA^MR||SMITH^JOHN||UNKNOWN%d|M\r", i, 1000000+i, i)))
	}

	all, why := Suggest(Build(corpus), SuggestOptions{})
	if why != "" {
		t.Fatal(why)
	}

	got := find(t, all, "PID-7")
	if got.Caution == "" {
		t.Fatalf("a field the standard calls a date, carrying %q values, produced no caution", got.Role)
	}
	if !strings.Contains(got.Caution, "do not look like dates") {
		t.Errorf("the caution does not say the values are not dates: %q", got.Caution)
	}
}

// TestSuggestIsReproducibleAndInFieldOrder covers both properties, which turned out to conflict.
//
// Reproducibility is what matters: a page that reorders itself between runs cannot be compared with the last one, which is the main
// thing somebody does with a report like this.
//
// Sorting by path was the obvious way to get it and made the output worse. As strings MSH-10 comes before MSH-3, so a sorted page
// read MSH-10, MSH-11, MSH-12, MSH-3. The report's own order was already deterministic and already in field-number order, so the
// sort was solving a problem this data does not have at the cost of an ordering no reader wants.
func TestSuggestIsReproducibleAndInFieldOrder(t *testing.T) {
	report := Build(suggestCorpus(t, 40))

	first, _ := Suggest(report, SuggestOptions{})
	for i := 0; i < 20; i++ {
		again, _ := Suggest(report, SuggestOptions{})
		if len(again) != len(first) {
			t.Fatalf("run %d produced %d suggestions rather than %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j].Path != first[j].Path {
				t.Fatalf("run %d ordered differently: %q then %q", i, first[j].Path, again[j].Path)
			}
		}
	}

	// And within a segment, by field number rather than as strings.
	var mshFields []int
	for _, s := range first {
		if !strings.HasPrefix(s.Path, "MSH-") {
			continue
		}
		var n int
		if _, err := fmt.Sscanf(s.Path, "MSH-%d", &n); err != nil {
			continue
		}
		mshFields = append(mshFields, n)
	}
	if len(mshFields) < 3 {
		t.Fatalf("expected several MSH fields, got %v", mshFields)
	}
	for i := 1; i < len(mshFields); i++ {
		if mshFields[i] < mshFields[i-1] {
			t.Errorf("MSH fields are out of numeric order: %v - sorting by path puts MSH-10 before "+
				"MSH-3, which is not an order anybody reads in", mshFields)

			break
		}
	}
}

// TestSuggestDoesNotListALargeVocabulary keeps patient data out of a structural report.
//
// A field with two hundred distinct values has no vocabulary worth listing, and listing it would put names and identifiers into a
// document whose purpose is to describe shape.
func TestSuggestDoesNotListALargeVocabulary(t *testing.T) {
	all, why := Suggest(Build(suggestCorpus(t, 100)), SuggestOptions{})
	if why != "" {
		t.Fatal(why)
	}

	for _, s := range all {
		if len(s.Vocabulary) > 25 {
			t.Errorf("%s lists %d values", s.Path, len(s.Vocabulary))
		}
		if s.Role == RoleIdentifier && len(s.Vocabulary) > 0 {
			t.Errorf("%s is an identifier and its values are listed, which puts patient identifiers into "+
				"a report about structure: %v", s.Path, s.Vocabulary)
		}
		if s.Role == RoleFreeText && len(s.Vocabulary) > 0 {
			t.Errorf("%s is free text and its values are listed: %v", s.Path, s.Vocabulary)
		}
	}
}

// TestADateShapedIdentifierIsNotReportedAsADate pins the hardest ambiguity in HL7 field inference.
//
// An eight-digit medical record number and a YYYYMMDD date of birth are indistinguishable by shape. The first version of this file
// tested the shape before the uniqueness and confidently reported every 8-digit MRN as a date of birth - which is exactly the
// failure mode that makes automatic mapping worse than none, because it is wrong in a way that looks right.
func TestADateShapedIdentifierIsNotReportedAsADate(t *testing.T) {
	// A field carrying a different 8-digit value on every message.
	var corpus [][]byte
	for i := 0; i < 60; i++ {
		corpus = append(corpus, []byte(fmt.Sprintf(
			"MSH|^~\\&|EPIC|SITEA|PERFUSE|RFAC|20260822120000||ADT^A01|CTRL%04d|P|2.5.1\r"+
				"PID|1||%08d||SMITH^JOHN||19700101|M\r", i, 20200101+i)))
	}

	all, why := Suggest(Build(corpus), SuggestOptions{})
	if why != "" {
		t.Fatal(why)
	}

	got := find(t, all, "PID-3")
	if got.Role == RoleTimestamp {
		t.Fatal("a value that differs on every message is reported as a date; dates repeat, and a birth date " +
			"field with one distinct value per message over sixty messages would be a remarkable coincidence")
	}
	if got.Role != RoleIdentifier {
		t.Errorf("PID-3 is %q, want an identifier", got.Role)
	}

	// And the ambiguity is stated rather than resolved silently, because being wrong costs whoever writes the mapping.
	if !strings.Contains(got.Caution, "cannot be told apart by shape alone") {
		t.Errorf("the ambiguity is not stated: %q", got.Caution)
	}

	// Both cautions apply at once, and the first version had each branch overwrite the last.
	if !strings.Contains(got.Caution, "leading zeros") {
		t.Errorf("the numeric-identifier warning was lost when the date-ambiguity one was added: %q", got.Caution)
	}

	// A real date of birth is still a date. Checked against a corpus where it varies and repeats, which is what a
	// birth date does - in the corpus above every message carried the same one, so it reads as a constant and
	// correctly so.
	var dated [][]byte
	for i := 0; i < 60; i++ {
		dated = append(dated, []byte(fmt.Sprintf(
			"MSH|^~\\&|EPIC|SITEA|PERFUSE|RFAC|20260822120000||ADT^A01|CTRL%04d|P|2.5.1\r"+
				"PID|1||MRN%04d^^^SITEA^MR||SMITH^JOHN||19%02d0101|M\r", i, i, 50+(i%20))))
	}

	ordinary, why := Suggest(Build(dated), SuggestOptions{})
	if why != "" {
		t.Fatal(why)
	}
	if dob := find(t, ordinary, "PID-7"); dob.Role != RoleTimestamp {
		t.Errorf("a birth date that varies and repeats is %q rather than a date - the uniqueness rule has "+
			"broken the ordinary case", dob.Role)
	}
}

// TestAnIdentifiersValuesAreNeverListed makes a guard about patient data provable.
//
// The vocabulary list exists for code sets and flags, where the values are a small fixed set describing structure. For an identifier
// they are patient identifiers, and putting them into a report about shape would be a quiet disclosure - the report gets pasted into
// tickets and emails precisely because it is supposed to be safe to share.
//
// Written because a plant that removed the role check did not fire: the profile does not populate Codes for a high-cardinality field,
// so the guard could not be reached through Suggest and was protecting nothing observable. Calling suggestField directly with a Field
// that has both makes it live, so the guard is now proven rather than assumed.
func TestAnIdentifiersValuesAreNeverListed(t *testing.T) {
	seg := Segment{ID: "PID", Messages: 100}

	identifier := Field{
		Path:      "PID-3",
		Name:      "Patient Identifier List",
		Present:   100,
		FillRate:  1,
		Distinct:  100,
		Shape:     ShapeAlphanumeric,
		MinLength: 8,
		MaxLength: 12,
		// Deliberately populated, which the profile would not do for a field this varied - the point is that
		// the guard holds even if that ever changes.
		Codes: []CodeCount{
			{Code: "MRN00000001", Count: 1, Known: false},
			{Code: "MRN00000002", Count: 1, Known: false},
		},
	}

	got := suggestField(identifier, seg, SuggestOptions{}.withDefaults())

	if got.Role != RoleIdentifier {
		t.Fatalf("the field is %q, want an identifier", got.Role)
	}
	if len(got.Vocabulary) != 0 {
		t.Errorf("an identifier's values are listed in a structural report: %v", got.Vocabulary)
	}

	// And the same values on a code set are listed, so the guard is discriminating rather than simply off.
	codeSet := identifier
	codeSet.Path = "PID-8"
	codeSet.Name = "Administrative Sex"
	codeSet.Distinct = 2
	codeSet.MaxLength = 1
	codeSet.Codes = []CodeCount{{Code: "F", Count: 50, Known: true}, {Code: "M", Count: 50, Known: true}}

	if got := suggestField(codeSet, seg, SuggestOptions{}.withDefaults()); len(got.Vocabulary) != 2 {
		t.Errorf("a code set's vocabulary is not listed: %v", got.Vocabulary)
	}
}
