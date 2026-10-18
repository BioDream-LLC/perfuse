package mpi

import (
	"math"
	"strings"
	"unicode"
)

// accents maps the Latin letters with diacritics that registration systems write, upper case, to their base letters.
var accents = func() map[rune]string {
	m := map[rune]string{}
	for base, list := range map[string]string{
		"A": "ÀÁÂÃÄÅĀĂĄ", "C": "ÇĆĈĊČ", "D": "ĎĐ", "E": "ÈÉÊËĒĔĖĘĚ", "G": "ĜĞĠĢ", "H": "ĤĦ", "I": "ÌÍÎÏĨĪĬĮİ", "J": "Ĵ",
		"K": "Ķ", "L": "ĹĻĽĿŁ", "N": "ÑŃŅŇ", "O": "ÒÓÔÕÖØŌŎŐ", "R": "ŔŖŘ", "S": "ŚŜŞŠ", "T": "ŢŤŦ", "U": "ÙÚÛÜŨŪŬŮŰŲ",
		"W": "Ŵ", "Y": "ÝŶŸ", "Z": "ŹŻŽ", "AE": "Æ", "OE": "Œ", "SS": "ß", "TH": "Þ",
	} {
		for _, r := range list {
			m[r] = base
		}
	}
	return m
}()

// fold makes a name comparable: accents removed, case folded, punctuation and spaces dropped, so "O'Brien", "OBRIEN" and
// "o brien" compare equal and "José" matches "Jose".
func fold(s string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(s) {
		if base, ok := accents[r]; ok {
			b.WriteString(base)
		} else if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// digits keeps only the digits of s.
func digits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// phoneKey is the last seven digits, which survive a changed country or area code written differently.
func phoneKey(s string) string {
	d := digits(s)
	if len(d) < 7 {
		return ""
	}
	return d[len(d)-7:]
}

// postalKey is the first five characters of a postal code, folded: a US ZIP+4 and its ZIP agree.
func postalKey(s string) string {
	f := fold(s)
	if len(f) > 5 {
		f = f[:5]
	}
	return f
}

// soundex is American Soundex, for blocking: names that sound alike share a key.
func soundex(s string) string {
	s = fold(s)
	if s == "" {
		return ""
	}
	code := func(r byte) byte {
		switch r {
		case 'B', 'F', 'P', 'V':
			return '1'
		case 'C', 'G', 'J', 'K', 'Q', 'S', 'X', 'Z':
			return '2'
		case 'D', 'T':
			return '3'
		case 'L':
			return '4'
		case 'M', 'N':
			return '5'
		case 'R':
			return '6'
		}
		return 0
	}
	out := []byte{s[0]}
	last := code(s[0])
	for i := 1; i < len(s) && len(out) < 4; i++ {
		c := code(s[i])
		if c != 0 && c != last {
			out = append(out, c)
		}
		if s[i] != 'H' && s[i] != 'W' {
			last = c
		}
	}
	for len(out) < 4 {
		out = append(out, '0')
	}
	return string(out)
}

// jaroWinkler is the Jaro-Winkler similarity of two folded strings, 0 to 1: the usual measure for names, forgiving of
// typing errors and transpositions.
func jaroWinkler(a, b string) float64 {
	if a == b {
		if a == "" {
			return 0
		}
		return 1
	}
	la, lb := len(a), len(b)
	if la == 0 || lb == 0 {
		return 0
	}
	window := max(la, lb)/2 - 1
	if window < 0 {
		window = 0
	}
	am, bm := make([]bool, la), make([]bool, lb)
	matches := 0
	for i := 0; i < la; i++ {
		lo, hi := max(0, i-window), min(lb-1, i+window)
		for j := lo; j <= hi; j++ {
			if !bm[j] && a[i] == b[j] {
				am[i], bm[j] = true, true
				matches++
				break
			}
		}
	}
	if matches == 0 {
		return 0
	}
	trans, k := 0, 0
	for i := 0; i < la; i++ {
		if !am[i] {
			continue
		}
		for !bm[k] {
			k++
		}
		if a[i] != b[k] {
			trans++
		}
		k++
	}
	m := float64(matches)
	jaro := (m/float64(la) + m/float64(lb) + (m-float64(trans)/2)/m) / 3
	prefix := 0
	for i := 0; i < min(4, la, lb) && a[i] == b[i]; i++ {
		prefix++
	}
	return jaro + float64(prefix)*0.1*(1-jaro)
}

// Weights are the Fellegi-Sunter agreement probabilities for one field: M, that the field agrees when two records are the
// same person; U, that it agrees by chance when they are not. Agreement adds log2(M/U); disagreement adds log2((1-M)/(1-U)).
type Weights struct {
	M float64 `json:"m"`
	U float64 `json:"u"`
}

// valid is a usable pair: both strict probabilities, and agreement more likely among matches than by chance.
func (w Weights) valid() bool { return w.M > 0 && w.M < 1 && w.U > 0 && w.U < 1 && w.M > w.U }

func (w Weights) agree() float64    { return math.Log2(w.M / w.U) }
func (w Weights) disagree() float64 { return math.Log2((1 - w.M) / (1 - w.U)) }

// DefaultWeights are reasonable starting values from the record-linkage literature for hospital registration data. A site
// with its own linked sample can measure better ones.
var DefaultWeights = map[string]Weights{
	"family":     {M: 0.95, U: 0.01},
	"given":      {M: 0.90, U: 0.02},
	"birth_date": {M: 0.97, U: 0.0003},
	"sex":        {M: 0.98, U: 0.5},
	"postal":     {M: 0.85, U: 0.05},
	"phone":      {M: 0.80, U: 0.0001},
	"line":       {M: 0.80, U: 0.001},
}

// FieldResult is how one field compared, for the explanation shown to a reviewer.
type FieldResult struct {
	Field  string  `json:"field"`
	Result string  `json:"result"` // agree, partial, disagree, missing
	Weight float64 `json:"weight"`
}

// Comparison is a scored pair of records.
type Comparison struct {
	Score  float64       `json:"score"` // 0 to 1: the evidence found as a share of the evidence possible
	Fields []FieldResult `json:"fields"`
	// Safe is whether the pair may be linked without a person: family name and birth date both agree.
	Safe bool `json:"safe"`
}

// Compare scores two records' demographics. Only fields present on both count, so a sparse record is judged on what it has.
func Compare(a, b *Record, weights map[string]Weights) Comparison {
	if weights == nil {
		weights = DefaultWeights
	}
	var total, possible float64
	var out Comparison
	add := func(field, result string, frac float64) {
		w := weights[field]
		var v float64
		switch result {
		case "agree":
			v = w.agree()
		case "partial":
			v = w.agree() * frac
		case "disagree":
			v = w.disagree()
		}
		if result != "missing" {
			possible += w.agree()
			total += v
		}
		out.Fields = append(out.Fields, FieldResult{Field: field, Result: result, Weight: math.Round(v*100) / 100})
	}
	name := func(field, x, y string) string {
		fx, fy := fold(x), fold(y)
		if fx == "" || fy == "" {
			add(field, "missing", 0)
			return "missing"
		}
		switch jw := jaroWinkler(fx, fy); {
		case jw >= 0.94:
			add(field, "agree", 1)
			return "agree"
		case jw >= 0.85 || soundex(fx) == soundex(fy):
			add(field, "partial", 0.5)
			return "partial"
		default:
			add(field, "disagree", 0)
			return "disagree"
		}
	}
	family := name("family", a.Family, b.Family)
	given := name("given", a.Given, b.Given)
	// A swapped family and given name, a common registration error, counts as partial agreement on both.
	if family == "disagree" && given == "disagree" && fold(a.Family) != "" && fold(a.Family) == fold(b.Given) && fold(a.Given) == fold(b.Family) {
		out.Fields = out.Fields[:len(out.Fields)-2]
		total -= weights["family"].disagree() + weights["given"].disagree()
		possible -= weights["family"].agree() + weights["given"].agree()
		add("family", "partial", 0.5)
		add("given", "partial", 0.5)
	}

	dob := "missing"
	da, db := digits(a.BirthDate), digits(b.BirthDate)
	if len(da) >= 8 && len(db) >= 8 {
		da, db = da[:8], db[:8]
		ya, ma, dda := da[:4], da[4:6], da[6:8]
		yb, mb, ddb := db[:4], db[4:6], db[6:8]
		same := 0
		for _, p := range [][2]string{{ya, yb}, {ma, mb}, {dda, ddb}} {
			if p[0] == p[1] {
				same++
			}
		}
		switch {
		case da == db:
			dob = "agree"
			add("birth_date", "agree", 1)
		case ya == yb && ma == ddb && dda == mb:
			// Day and month swapped: the US and European orders.
			dob = "partial"
			add("birth_date", "partial", 0.6)
		case same == 2:
			dob = "partial"
			add("birth_date", "partial", 0.4)
		default:
			dob = "disagree"
			add("birth_date", "disagree", 0)
		}
	} else {
		add("birth_date", "missing", 0)
	}

	if sa, sb := strings.ToUpper(strings.TrimSpace(a.Sex)), strings.ToUpper(strings.TrimSpace(b.Sex)); sa == "" || sb == "" || sa == "U" || sb == "U" {
		add("sex", "missing", 0)
	} else if sa[:1] == sb[:1] {
		add("sex", "agree", 1)
	} else {
		add("sex", "disagree", 0)
	}
	if pa, pb := postalKey(a.Postal), postalKey(b.Postal); pa == "" || pb == "" {
		add("postal", "missing", 0)
	} else if pa == pb {
		add("postal", "agree", 1)
	} else {
		add("postal", "disagree", 0)
	}
	if pa, pb := phoneKey(a.Phone), phoneKey(b.Phone); pa == "" || pb == "" {
		add("phone", "missing", 0)
	} else if pa == pb {
		add("phone", "agree", 1)
	} else {
		// People change numbers; a different one is weak evidence against.
		out.Fields = append(out.Fields, FieldResult{Field: "phone", Result: "disagree", Weight: 0})
	}
	if la, lb := fold(a.Line), fold(b.Line); la == "" || lb == "" {
		add("line", "missing", 0)
	} else if jw := jaroWinkler(la, lb); jw >= 0.92 {
		add("line", "agree", 1)
	} else {
		out.Fields = append(out.Fields, FieldResult{Field: "line", Result: "disagree", Weight: 0})
	}

	if possible > 0 {
		out.Score = math.Max(0, math.Min(1, total/possible))
		out.Score = math.Round(out.Score*1000) / 1000
	}
	out.Safe = family == "agree" && dob == "agree"
	return out
}
