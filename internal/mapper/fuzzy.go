package mapper

import "strings"

// levenshtein computes the edit distance between two strings.
//
// This is the standard dynamic programming implementation with O(m*n) time and O(min(m,n))
// space. Good enough for field names which are short strings.
//
// Operates on runes (Unicode code points), not bytes. Field names in healthcare can contain
// accented characters (e.g. "José", "naïve") and byte-level comparison gives wrong distances.
func levenshtein(a, b string) int {
	a = strings.ToLower(a)
	b = strings.ToLower(b)

	if a == b {
		return 0
	}

	ra := []rune(a)
	rb := []rune(b)

	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}

	// Use the shorter string for the working row to save memory.
	if len(ra) < len(rb) {
		ra, rb = rb, ra
	}

	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)

	for j := range prev {
		prev[j] = j
	}

	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			ins := curr[j-1] + 1
			del := prev[j] + 1
			sub := prev[j-1] + cost
			curr[j] = min3(ins, del, sub)
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

// levenshteinSimilarity returns a similarity score in [0.0, 1.0] based on edit distance.
func levenshteinSimilarity(a, b string) float64 {
	ra := []rune(a)
	rb := []rune(b)
	if len(ra) == 0 && len(rb) == 0 {
		return 1.0
	}
	dist := levenshtein(a, b)
	maxLen := len(ra)
	if len(rb) > maxLen {
		maxLen = len(rb)
	}
	return 1.0 - float64(dist)/float64(maxLen)
}

// jaroWinkler computes the Jaro-Winkler similarity between two strings.
//
// Jaro-Winkler favors strings that match from the beginning, which is useful for field
// names that share a common prefix (e.g. "patient_mrn" vs "patient_id"). The result is
// in [0.0, 1.0] where 1.0 is identical.
func jaroWinkler(a, b string) float64 {
	a = strings.ToLower(a)
	b = strings.ToLower(b)

	if a == b {
		return 1.0
	}

	j := jaro(a, b)
	if j == 0 {
		return 0
	}

	// Winkler modification: boost for common prefix up to 4 characters.
	ra := []rune(a)
	rb := []rune(b)
	prefix := 0
	limit := 4
	if len(ra) < limit {
		limit = len(ra)
	}
	if len(rb) < limit {
		limit = len(rb)
	}
	for i := 0; i < limit; i++ {
		if ra[i] == rb[i] {
			prefix++
		} else {
			break
		}
	}

	return j + float64(prefix)*0.1*(1.0-j)
}

// jaro computes the Jaro similarity between two strings.
func jaro(a, b string) float64 {
	ra := []rune(strings.ToLower(a))
	rb := []rune(strings.ToLower(b))

	if len(ra) == 0 && len(rb) == 0 {
		return 1.0
	}
	if len(ra) == 0 || len(rb) == 0 {
		return 0
	}

	// The match window is max(|a|, |b|) / 2 - 1, but at least 0.
	window := len(ra)
	if len(rb) > window {
		window = len(rb)
	}
	window = window/2 - 1
	if window < 0 {
		window = 0
	}

	aMatched := make([]bool, len(ra))
	bMatched := make([]bool, len(rb))

	matches := 0
	transpositions := 0

	// Find matches.
	for i := 0; i < len(ra); i++ {
		lo := i - window
		if lo < 0 {
			lo = 0
		}
		hi := i + window + 1
		if hi > len(rb) {
			hi = len(rb)
		}
		for j := lo; j < hi; j++ {
			if bMatched[j] || ra[i] != rb[j] {
				continue
			}
			aMatched[i] = true
			bMatched[j] = true
			matches++
			break
		}
	}

	if matches == 0 {
		return 0
	}

	// Count transpositions.
	k := 0
	for i := 0; i < len(ra); i++ {
		if !aMatched[i] {
			continue
		}
		for !bMatched[k] {
			k++
		}
		if ra[i] != rb[k] {
			transpositions++
		}
		k++
	}

	m := float64(matches)
	return (m/float64(len(ra)) + m/float64(len(rb)) + (m-float64(transpositions)/2.0)/m) / 3.0
}

// normalizedName strips common prefixes/suffixes and normalizes for comparison.
// E.g. "patient_mrn" -> "patient mrn", "PatientMRN" -> "patient mrn".
func normalizedName(s string) string {
	// Convert camelCase/PascalCase to spaces, keeping acronyms together.
	// "PatientMRN" -> "Patient MRN", "dateOfBirth" -> "date Of Birth"
	runes := []rune(s)
	var buf strings.Builder
	for i, r := range runes {
		if i > 0 && r >= 'A' && r <= 'Z' {
			prev := runes[i-1]
			// Insert space before uppercase if previous was lowercase,
			// or if previous was uppercase but next is lowercase (end of acronym).
			if prev >= 'a' && prev <= 'z' {
				buf.WriteByte(' ')
			} else if prev >= 'A' && prev <= 'Z' && i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z' {
				buf.WriteByte(' ')
			}
		}
		buf.WriteRune(r)
	}
	s = buf.String()

	// Replace underscores, hyphens, dots with spaces.
	s = strings.NewReplacer("_", " ", "-", " ", ".", " ").Replace(s)
	s = strings.ToLower(s)
	s = strings.TrimSpace(s)

	// Collapse multiple spaces.
	for strings.Contains(s, "  ") {
		s = strings.ReplaceAll(s, "  ", " ")
	}
	return s
}

// nameSimilarity computes the best similarity score between two field names,
// considering both normalized forms and token overlap.
func nameSimilarity(a, b string) float64 {
	na := normalizedName(a)
	nb := normalizedName(b)

	// Direct Jaro-Winkler on normalized names.
	jw := jaroWinkler(na, nb)

	// Levenshtein similarity on normalized names.
	lev := levenshteinSimilarity(na, nb)

	// Token overlap: how many words are shared.
	tokA := strings.Fields(na)
	tokB := strings.Fields(nb)
	token := tokenOverlap(tokA, tokB)

	// Take the best signal. Different name styles benefit from different metrics.
	best := jw
	if lev > best {
		best = lev
	}
	if token > best {
		best = token
	}
	return best
}

// tokenOverlap computes the fraction of tokens shared between two token lists.
func tokenOverlap(a, b []string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	set := make(map[string]bool, len(a))
	for _, t := range a {
		set[t] = true
	}
	shared := 0
	for _, t := range b {
		if set[t] {
			shared++
		}
	}
	total := len(a)
	if len(b) > total {
		total = len(b)
	}
	return float64(shared) / float64(total)
}

func min3(a, b, c int) int {
	if a < b {
		if a < c {
			return a
		}
		return c
	}
	if b < c {
		return b
	}
	return c
}
