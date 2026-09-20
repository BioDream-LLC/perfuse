package mapper

import (
	"strings"
	"testing"
)

// Tests for what happens when several value patterns fit equally well.
//
// This was a live defect rather than a hypothetical. Patterns were tried in table order and the first with the best match fraction
// won, so where two fitted identically the order of a slice decided what a clinical value was. It decided that an HL7 date was a
// medical record number, because MRN is listed above date and "6-12 alphanumeric characters" describes an eight-digit date.
//
// The consequence was an inverted ranking on the most safety-critical pair there is.

func TestAnHL7DateIsADateAndNotAnIdentifier(t *testing.T) {
	// 19800101 is HL7 v2's own date format and the commonest date representation in the messages this engine exists to map. It also
	// matches MRN's pattern exactly, and MRN used to win.
	got, frac := detectPattern("DateOfBirth", []string{"19800101", "19750612"})

	if got != PatternDate {
		t.Errorf("an HL7 date was detected as %q, want date", got)
	}
	if frac != 1 {
		t.Errorf("match fraction %v, want 1", frac)
	}
}

func TestATenDigitPhoneNumberIsAPhoneNumber(t *testing.T) {
	// The same collision: ten digits is within MRN's six-to-twelve alphanumeric range.
	if got, _ := detectPattern("PhoneNumber", []string{"5551234567"}); got != PatternPhone {
		t.Errorf("a phone number was detected as %q, want phone", got)
	}
}

func TestABirthDateDoesNotOutScoreItsCorrectTargetWhenOfferedToAnIdentifier(t *testing.T) {
	// The assertion this fix exists for, and the one worth breaking to check.
	//
	// Before, a birth date offered against a medical record number scored 53 with a pattern match credited, while the same birth
	// date offered against a date field scored 50 with no pattern match at all. The engine ranked putting a date of birth into an
	// identifier field above putting it into a date field.
	//
	// Nothing was confidently wrong, because both abstained at the default threshold of 70. The abstention design was the only thing
	// containing it - and since the panel abstained on nearly everything, the natural response was to lower the threshold, which
	// would have surfaced the dangerous mapping ranked above the correct one.
	source := SourceField{Name: "BirthDate", Examples: []string{"19800101", "19750612"}}

	toIdentifier := New([]TargetField{{Name: "MedicalRecordNumber", Pattern: "mrn"}}, Config{}).Suggest(source)
	toDate := New([]TargetField{{Name: "BirthDate", Pattern: "date"}}, Config{}).Suggest(source)

	if len(toIdentifier) == 0 || len(toDate) == 0 {
		t.Fatal("one of the two mappings produced no suggestion at all, so this test proves nothing")
	}

	if toIdentifier[0].Confidence >= toDate[0].Confidence {
		t.Errorf("a birth date scores %d toward an identifier and %d toward a date field: the wrong mapping ranks at least as high",
			toIdentifier[0].Confidence, toDate[0].Confidence)
	}

	// And the wrong one must not be credited with a pattern match, which is the specific thing that used to lift it.
	if strings.Contains(toIdentifier[0].Reasoning, "pattern match") {
		t.Errorf("a date offered to an identifier field is still credited with a pattern match: %q", toIdentifier[0].Reasoning)
	}

	// The correct mapping has to be usable, not merely better. A fix that made both useless would pass the comparison above.
	if toDate[0].Abstained {
		t.Errorf("a birth date mapped to a birth date still abstains at %d: %s", toDate[0].Confidence, toDate[0].Reasoning)
	}
}

func TestTheFieldNameBreaksATieBeforeAnythingElse(t *testing.T) {
	// A name is weaker evidence than a value in general, which is why name detection is only a fallback. But when two patterns fit
	// the values identically the values have stopped discriminating, and the name is then the best evidence there is.
	//
	// Discarding it was the heart of the bug: "DateOfBirth" matched the date pattern's name hints all along, and was never consulted
	// because a wrong value match had already succeeded.
	if got, _ := detectPattern("PatientMRN", []string{"12345678"}); got != PatternMRN {
		t.Errorf("an eight-digit value in a field named PatientMRN was detected as %q, want mrn", got)
	}

	// The same eight digits, named as a date, goes the other way. Same values, different answer, decided by the name.
	if got, _ := detectPattern("AdmitDate", []string{"12345678"}); got != PatternDate {
		t.Errorf("an eight-digit value in a field named AdmitDate was detected as %q, want date", got)
	}
}

func TestAnEightDigitValueWithNoNamingClueIsGuessedAsADate(t *testing.T) {
	// A recorded decision rather than a claim about correctness. Eight digits with nothing in the name to go on is genuinely
	// undecidable - an account number and a date are indistinguishable - so the narrower pattern wins, because matching "six to
	// twelve alphanumeric characters" says almost nothing while matching a date says something.
	//
	// This is why the assertion above matters more than this one: a naming clue always wins, and only a field with no clue at all
	// lands here. Written down so the behaviour is a decision somebody made rather than a side effect of slice order, which is
	// exactly what it used to be.
	if got, _ := detectPattern("AccountNumber", []string{"87654321"}); got != PatternDate {
		t.Errorf("detected %q; if this changed deliberately, update this test and say why", got)
	}
}
