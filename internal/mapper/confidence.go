package mapper

import "fmt"

// signals holds the individual scoring components before they are combined.
type signals struct {
	nameSim    float64     // 0.0-1.0 name similarity
	patternSrc PatternType // detected pattern in source
	patternTgt PatternType // expected pattern in target
	patternCon float64     // pattern match confidence 0.0-1.0
	confusable bool        // true when pattern match is suppressed due to confusable pair
	codeMatch  bool        // code systems match
	historical int         // historical boost points
	reasons    []string    // human-readable explanations
}

// scoreSignals computes all signals for a source->target pair.
func scoreSignals(src SourceField, target TargetField) signals {
	var s signals

	// Name similarity, against what a target path means rather than how it is spelled.
	//
	// PID-7 is the date of birth field. That is a published fact, and without it the engine was comparing "DateOfBirth" against the
	// literal string "PID-7", which share almost nothing - so a correct mapping between a descriptive field name and an HL7 path
	// capped around sixty and abstained. The engine could not be confident about the one thing it exists to do.
	//
	// The dictionary that knows this was already in the tree, written for the message viewer. Both spellings are scored and the
	// better kept, because a target may be a plain name rather than a path, and a Z segment has no published meaning at all.
	s.nameSim = nameSimilarity(src.Name, target.Name)
	targetMeaning := pathMeaning(target.Name)

	if targetMeaning != "" {
		if bySense := nameSimilarity(src.Name, targetMeaning); bySense > s.nameSim {
			s.nameSim = bySense
			s.reasons = append(s.reasons, fmt.Sprintf("%s is %s", target.Name, targetMeaning))
		}
	}

	if s.nameSim >= 0.7 {
		s.reasons = append(s.reasons, fmt.Sprintf("name similarity %.0f%%", s.nameSim*100))
	}

	// Pattern detection on source.
	srcPattern, srcConf := detectPattern(src.Name, src.Examples)
	s.patternSrc = srcPattern
	s.patternCon = srcConf

	// Target pattern comes from its declared Pattern field, or detect from name.
	tgtPattern := PatternType(target.Pattern)
	if tgtPattern == PatternNone {
		tgtPattern, _ = detectPattern(target.Name, nil)
	}
	s.patternTgt = tgtPattern

	// Only credit pattern match when the pair is not confusable.
	// Two fields can share a pattern (both dates, both identifiers) while being clinically
	// different (admit vs discharge, patient ID vs account number). The pattern match in
	// that case is exactly what makes the confusion dangerous — it adds confidence to a
	// wrong mapping — so we suppress it.
	srcNorm := normalizedName(src.Name)
	tgtNorm := normalizedName(target.Name)
	if patternMatch(srcPattern, tgtPattern) && !confusableFieldPair(srcNorm, tgtNorm) {
		s.reasons = append(s.reasons, fmt.Sprintf("pattern match (%s)", srcPattern))
	} else if patternMatch(srcPattern, tgtPattern) && confusableFieldPair(srcNorm, tgtNorm) {
		s.confusable = true
		s.reasons = append(s.reasons, fmt.Sprintf("pattern match (%s) suppressed: confusable pair", srcPattern))
	}

	// Code system match.
	if src.CodeSystem != "" && target.CodeSystem != "" && src.CodeSystem == target.CodeSystem {
		s.codeMatch = true
		s.reasons = append(s.reasons, "code system match")
	}

	return s
}

// combine merges all signals into a final confidence score (0-100).
//
// The scoring model:
//   - Name similarity: up to 50 points (primary signal)
//   - Pattern match: up to 30 points
//   - Code system match: 25 points (very strong in healthcare - same terminology = same concept)
//   - Historical boost: up to configured max (default 15)
//
// The total is capped at 100.
func (s signals) combine() (int, string) {
	score := 0.0

	// Name similarity contributes up to 50 points.
	namePoints := s.nameSim * 50.0
	score += namePoints

	// Pattern match contributes up to 30 points, scaled by confidence.
	// Suppressed for confusable pairs where the pattern agreement makes the wrong
	// mapping MORE dangerous, not less.
	if patternMatch(s.patternSrc, s.patternTgt) && !s.confusable {
		patternPoints := 30.0 * s.patternCon
		score += patternPoints
	}

	// Code system match is a very strong signal - same terminology means same concept.
	if s.codeMatch {
		score += 25.0
	}

	// Historical boost.
	score += float64(s.historical)

	// Cap at 100.
	final := int(score + 0.5) // round
	if final > 100 {
		final = 100
	}
	if final < 0 {
		final = 0
	}

	reasoning := "no strong signals"
	if len(s.reasons) > 0 {
		reasoning = joinReasons(s.reasons)
	}

	return final, reasoning
}

// joinReasons combines multiple reason strings into a single explanation.
func joinReasons(reasons []string) string {
	if len(reasons) == 0 {
		return ""
	}
	if len(reasons) == 1 {
		return reasons[0]
	}
	result := reasons[0]
	for i := 1; i < len(reasons); i++ {
		result += "; " + reasons[i]
	}
	return result
}
