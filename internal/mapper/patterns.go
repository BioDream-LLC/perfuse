package mapper

import (
	"regexp"
	"strings"
)

// PatternType identifies a class of healthcare identifier or data format.
type PatternType string

const (
	PatternNPI   PatternType = "npi"
	PatternSSN   PatternType = "ssn"
	PatternMRN   PatternType = "mrn"
	PatternPhone PatternType = "phone"
	PatternDate  PatternType = "date"
	PatternEmail PatternType = "email"
	PatternNone  PatternType = ""
)

// patternDef describes how to detect a pattern.
type patternDef struct {
	typ     PatternType
	re      *regexp.Regexp
	nameHit []string // substrings in field name that suggest this pattern

	// broad marks a pattern whose shape describes most other patterns' values too.
	//
	// Only MRN is one, and it has to be: a medical record number is whatever an institution decided it was, so the pattern can be
	// no narrower than "some letters and digits". That makes it a poor winner of a tie, because matching it says almost nothing
	// while matching a date says something specific.
	broad bool
}

var patterns = []patternDef{
	{
		typ: PatternNPI,
		// NPI: exactly 10 digits, starts with 1 or 2 (Type 1 individual or Type 2 organization).
		re:      regexp.MustCompile(`^[12]\d{9}$`),
		nameHit: []string{"npi", "national provider"},
	},
	{
		typ: PatternSSN,
		// SSN: 9 digits or XXX-XX-XXXX format. Area numbers 000, 666, 900-999 are invalid
		// but we do not enforce that here - pattern detection is about format, not validation.
		re:      regexp.MustCompile(`^(\d{9}|\d{3}-\d{2}-\d{4})$`),
		nameHit: []string{"ssn", "social security", "social_security"},
	},
	{
		typ: PatternMRN,
		// MRN: alphanumeric, 6-12 characters. Very institution-specific but this covers most.
		//
		// It also covers an eight-digit HL7 date and a ten-digit phone number, which is why it is marked broad. See detectPattern.
		re:      regexp.MustCompile(`^[A-Za-z0-9]{6,12}$`),
		nameHit: []string{"mrn", "medical record", "medical_record", "chart number"},
		broad:   true,
	},
	{
		typ: PatternPhone,
		// Phone: US format variants. Intentionally loose.
		re:      regexp.MustCompile(`^(\+?1?[-.\s]?)?\(?\d{3}\)?[-.\s]?\d{3}[-.\s]?\d{4}$`),
		nameHit: []string{"phone", "telephone", "tel", "fax", "mobile", "cell"},
	},
	{
		typ: PatternDate,
		// Date: common formats. YYYY-MM-DD, MM/DD/YYYY, YYYYMMDD.
		re:      regexp.MustCompile(`^(\d{4}[-/]\d{2}[-/]\d{2}|\d{2}[-/]\d{2}[-/]\d{4}|\d{8})$`),
		nameHit: []string{"date", "dob", "birth", "admit", "discharge", "effective"},
	},
	{
		typ:     PatternEmail,
		re:      regexp.MustCompile(`^[a-zA-Z0-9._%+\-]+@[a-zA-Z0-9.\-]+\.[a-zA-Z]{2,}$`),
		nameHit: []string{"email", "e-mail", "e_mail"},
	},
}

// detectPattern examines sample values and field name to identify what kind of data this is.
//
// Returns the detected pattern type and a confidence multiplier (0.0-1.0) indicating how
// many of the examples matched. A field where 10/10 examples match gets 1.0; a field where
// 3/10 match gets 0.3 - which might mean the pattern is right but there is dirty data, or
// it might mean the pattern is wrong and those three were coincidental.
func detectPattern(name string, examples []string) (PatternType, float64) {
	nameLower := strings.ToLower(name)

	// First try regex matching on examples if we have any.
	if len(examples) > 0 {
		// Every pattern that matches this well, not just the first.
		//
		// Taking the first was a real defect and a dangerous one. Several patterns match the same value perfectly - 19800101 is a
		// date and also six-to-twelve alphanumeric characters - and keeping the earliest meant table order decided what a birth
		// date was. It decided MRN, because MRN is listed above date. The date pattern includes \d{8} precisely for HL7's own date
		// format, and could never win with it.
		//
		// The effect was that a birth date offered against a medical record number scored higher than the same birth date offered
		// against a date field: the wrong mapping earned a pattern match and the right one earned nothing. Only the abstention
		// threshold kept that off the screen.
		var tied []patternDef

		bestFrac := 0.0

		for _, p := range patterns {
			matched := 0
			for _, ex := range examples {
				ex = strings.TrimSpace(ex)
				if ex == "" {
					continue
				}
				if p.re.MatchString(ex) {
					matched++
				}
			}

			frac := float64(matched) / float64(len(examples))
			switch {
			case frac > bestFrac:
				bestFrac = frac
				tied = []patternDef{p}
			case frac == bestFrac && frac > 0:
				tied = append(tied, p)
			}
		}

		// If regex matched well, return it.
		if bestFrac >= 0.5 {
			return chooseAmong(tied, nameLower), bestFrac
		}
	}

	// Fall back to name-based detection.
	for _, p := range patterns {
		for _, hint := range p.nameHit {
			if strings.Contains(nameLower, hint) {
				return p.typ, 0.6 // name-based is less certain than value-based
			}
		}
	}

	return PatternNone, 0
}

// patternMatch returns true if two pattern types are compatible for mapping.
//
// Pattern compatibility is necessary but not sufficient for a mapping to be correct.
// Two fields can share a pattern (both are dates, both are identifiers) and still be
// entirely different things (admit date vs discharge date, patient ID vs account number).
// The pattern match signal is therefore just one input to the confidence score, and must
// not alone push a mapping over the abstention threshold.
func patternMatch(a, b PatternType) bool {
	if a == PatternNone || b == PatternNone {
		return false
	}
	return a == b
}

// confusableFieldPair returns true when two normalised names look very similar but
// refer to clinically DIFFERENT concepts. Matching these is a patient-safety defect.
//
// The list is deliberately explicit rather than heuristic: a heuristic can be fooled, and
// a false negative here means a wrong mapping in production. A false positive here means
// we abstain on something a human will approve in seconds - the safe side.
//
// If the pair is confusable, the pattern-match bonus is suppressed because pattern agreement
// (both are dates, both are identifiers) is exactly what makes the confusion dangerous.
func confusableFieldPair(srcNorm, tgtNorm string) bool {
	// Each pair lists words that, when one appears in one name and the other in the second,
	// signal a dangerous confusion. Order does not matter.
	type exclusion struct {
		wordA string
		wordB string
	}

	pairs := []exclusion{
		{"birth", "death"},
		{"admit", "discharge"},
		{"admission", "discharge"},
		{"systolic", "diastolic"},
		{"first", "last"},
		{"start", "end"},
		{"begin", "end"},
		{"onset", "offset"},
		{"send", "receive"},
		{"sender", "receiver"},
		{"source", "destination"},
		{"min", "max"},
		{"minimum", "maximum"},
		{"primary", "secondary"},
		{"pre", "post"},
		{"before", "after"},
	}

	for _, p := range pairs {
		aHasA := strings.Contains(srcNorm, p.wordA)
		aHasB := strings.Contains(srcNorm, p.wordB)
		bHasA := strings.Contains(tgtNorm, p.wordA)
		bHasB := strings.Contains(tgtNorm, p.wordB)

		// One name has wordA and the other has wordB, or vice versa.
		if (aHasA && bHasB) || (aHasB && bHasA) {
			return true
		}
	}

	// Also catch: "X id" vs "X account number", "X id" vs "X number" where X matches
	// but the identifier semantics differ.
	idWords := []string{" id", " identifier", " number", " account", " mrn", " accession"}
	srcIDs := 0
	tgtIDs := 0
	for _, w := range idWords {
		if strings.Contains(srcNorm, w) {
			srcIDs++
		}
		if strings.Contains(tgtNorm, w) {
			tgtIDs++
		}
	}
	// If both contain identifier-type words but they are DIFFERENT words, it is confusable.
	if srcIDs > 0 && tgtIDs > 0 {
		// Check that they don't have the same identifier words.
		for _, w := range idWords {
			srcHas := strings.Contains(srcNorm, w)
			tgtHas := strings.Contains(tgtNorm, w)
			if srcHas != tgtHas {
				return true
			}
		}
	}

	return false
}

// chooseAmong picks one pattern from several that matched the examples equally well.
//
// The field's name decides first. A name is weaker evidence than a value in general, which is why name detection is only a fallback
// below - but when two patterns fit the values identically the values have stopped discriminating, and "DateOfBirth" is then the best
// evidence available. Discarding it there was the bug: the name matched the date pattern's hints all along and was never consulted,
// because a wrong value match had already succeeded.
//
// Failing that, a narrow pattern beats a broad one. Matching "six to twelve alphanumeric characters" says almost nothing; matching a
// date says something. Preferring the specific claim is both more often right and safer when wrong, because a date wrongly called a
// date goes to a date field.
func chooseAmong(tied []patternDef, nameLower string) PatternType {
	if len(tied) == 0 {
		return PatternNone
	}

	if len(tied) == 1 {
		return tied[0].typ
	}

	for _, p := range tied {
		for _, hint := range p.nameHit {
			if strings.Contains(nameLower, hint) {
				return p.typ
			}
		}
	}

	for _, p := range tied {
		if !p.broad {
			return p.typ
		}
	}

	return tied[0].typ
}
