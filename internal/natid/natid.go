// Package natid knows the national patient and person identifiers of the UK and Europe: their formats, their check
// digits, the identifier system each country's FHIR guides name them by, and how much of one may be shown.
//
// It exists for the patient index. A national number is the strongest evidence two records are one person, and also the
// easiest to get wrong: a digit keyed twice gives a number that belongs to somebody else, and one source sends the NHS
// number under an OID while another sends it under its FHIR URL. So every identifier the index stores goes through here:
// its check digit is tested (a wrong one is never used to link), its spacing removed, and its system written one way.
//
// What is verified and what is not is said per scheme. The algorithms are public; the identifier system URIs are the
// country's own where a national FHIR guide names one (SystemVerified), and a documented default otherwise, which a site
// changes to its national value under Settings → Patient index. Nothing here decides anything clinical.
package natid

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Scheme is one national identifier.
type Scheme struct {
	// ID is a short stable name, such as uk-nhs or de-kvnr.
	ID string `json:"id"`
	// Country is the ISO 3166-1 alpha-2 code (GB, DE); ISO3 the alpha-3 code HL7 v2 uses in the NN<country> identifier type.
	Country string `json:"country"`
	ISO3    string `json:"iso3"`
	// Region narrows a country (Scotland, Northern Ireland), when the number is not the whole country's.
	Region string `json:"region,omitempty"`
	// Name is the identifier's own name, Local in the country's language.
	Name  string `json:"name"`
	Local string `json:"local,omitempty"`
	// System is the FHIR Identifier.system the index stores it under. SystemVerified says it comes from the country's own
	// FHIR guide (SystemSource); false means a default that a site should check against its national guide.
	System         string `json:"system"`
	SystemVerified bool   `json:"systemVerified"`
	SystemSource   string `json:"systemSource,omitempty"`
	// Aliases are other systems the same number arrives under (an OID, an older URL): they are rewritten to System, so a
	// record sent each way still matches.
	Aliases []string `json:"aliases,omitempty"`
	// TypeSystem and TypeCode are the Identifier.type a country's FHIR guides expect beside the system, when they name one
	// (fhir.de's KVZ10 for the statutory insurance number).
	TypeSystem string `json:"typeSystem,omitempty"`
	TypeCode   string `json:"typeCode,omitempty"`
	// Primary marks the country's main health identifier, the one an HL7 v2 NN<ISO3> identifier type stands for.
	Primary bool `json:"primary,omitempty"`
	// Format describes what is accepted, in words; Check the check-digit rule, in words ("none" when there is none).
	Format string `json:"format"`
	Check  string `json:"check"`
	// Restricted says what the country's law says about using or showing it, when it says something particular.
	Restricted string `json:"restricted,omitempty"`
	// Example is a number that passes the check: made up, or from a published test range (the NHS synthetic 999 range).
	Example string `json:"example"`

	normalise func(string) string
	validate  func(string) error
	display   func(string) string
}

// Normalise removes what is only presentation: spaces, dots, dashes where they are not part of the number, and case.
func (s *Scheme) Normalise(v string) string {
	if s.normalise != nil {
		return s.normalise(v)
	}
	return stripSep(v)
}

// Validate says what is wrong with a number, or nil. It normalises first.
func (s *Scheme) Validate(v string) error {
	n := s.Normalise(v)
	if n == "" {
		return errors.New("the number is empty")
	}
	return s.validate(n)
}

// Display writes a normalised number the way it is usually printed (NHS 999 000 0018, AHV 756.1234.5678.97).
func (s *Scheme) Display(v string) string {
	n := s.Normalise(v)
	if s.display != nil {
		return s.display(n)
	}
	return n
}

// Mask hides all but the last few characters of a number. The characters shown are never enough to find the person, and
// in numbers that begin with the date of birth (the Nordic numbers, CHI, PESEL) the date is among what is hidden.
func Mask(v string) string {
	r := []rune(stripSep(v))
	keep := 4
	if len(r) <= 6 {
		keep = 2
	}
	if len(r) <= keep {
		return strings.Repeat("•", len(r))
	}
	return strings.Repeat("•", len(r)-keep) + string(r[len(r)-keep:])
}

var (
	schemes  []*Scheme
	bySystem = map[string]*Scheme{}
	byID     = map[string]*Scheme{}
)

func register(s *Scheme) {
	if s.validate == nil || s.ID == "" || s.System == "" || s.Country == "" || s.ISO3 == "" {
		panic("natid: incomplete scheme " + s.ID)
	}
	if _, dup := byID[s.ID]; dup {
		panic("natid: duplicate scheme " + s.ID)
	}
	schemes = append(schemes, s)
	byID[s.ID] = s
	for _, sys := range append([]string{s.System}, s.Aliases...) {
		k := systemKey(sys)
		if prev, dup := bySystem[k]; dup {
			panic("natid: system " + sys + " named by " + prev.ID + " and " + s.ID)
		}
		bySystem[k] = s
	}
}

// systemKey compares systems without case, without a trailing slash, and urn:oid: with or without its prefix.
func systemKey(sys string) string {
	k := strings.TrimRight(strings.ToLower(strings.TrimSpace(sys)), "/")
	if isOID(k) {
		k = "urn:oid:" + k
	}
	return k
}

func isOID(s string) bool {
	if s == "" || !strings.Contains(s, ".") {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && c != '.' {
			return false
		}
	}
	return true
}

// All lists every scheme, by country then name.
func All() []*Scheme {
	out := append([]*Scheme(nil), schemes...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Country != out[j].Country {
			return out[i].Country < out[j].Country
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// ByID finds a scheme by its ID.
func ByID(id string) (*Scheme, bool) { s, ok := byID[id]; return s, ok }

// ForSystem finds the scheme an identifier system names, by its System or an alias.
func ForSystem(sys string) (*Scheme, bool) { s, ok := bySystem[systemKey(sys)]; return s, ok }

// ForHL7Type finds the country's primary scheme for an HL7 v2 identifier type NN<ISO3> (table 0203, "national person
// identifier where the xxx is the ISO table 3166 3-character country code"), and the NHS number for type NH sent by the
// NHS assigning authority.
func ForHL7Type(typ, authority string) (*Scheme, bool) {
	typ = strings.ToUpper(strings.TrimSpace(typ))
	if typ == "NH" && strings.EqualFold(strings.TrimSpace(authority), "NHS") {
		return byID["uk-nhs"], true
	}
	if len(typ) == 5 && strings.HasPrefix(typ, "NN") {
		for _, s := range schemes {
			if s.Primary && s.ISO3 == typ[2:] {
				return s, true
			}
		}
	}
	return nil, false
}

// Canonical is an identifier as the index stores it.
type Canonical struct {
	System string
	Value  string
	// Scheme is the national scheme, or nil for an identifier that is not one.
	Scheme *Scheme
	// Problem is why a national number failed its check, or "".
	Problem string
}

// Canonicalise rewrites a national number to its scheme's system and normalised value and checks it. An identifier that is
// not a national one comes back as given, trimmed. sysOverride replaces a scheme's System (a site's own national URI).
func Canonicalise(system, value string, sysOverride map[string]string) Canonical {
	system, value = strings.TrimSpace(system), strings.TrimSpace(value)
	s, ok := ForSystem(system)
	if !ok {
		for id, over := range sysOverride {
			if systemKey(over) == systemKey(system) {
				s, ok = byID[id], byID[id] != nil
				break
			}
		}
	}
	if !ok {
		return Canonical{System: system, Value: value}
	}
	c := Canonical{System: s.System, Value: s.Normalise(value), Scheme: s}
	if over := sysOverride[s.ID]; over != "" {
		c.System = over
	}
	if err := s.validate(c.Value); err != nil {
		c.Problem = fmt.Sprintf("%s %s: %v", s.Name, Mask(c.Value), err)
	}
	return c
}

// ---- helpers -------------------------------------------------------------------------------------------------------

func stripSep(v string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(strings.TrimSpace(v)) {
		switch r {
		case ' ', '.', '-', '/', '\u00a0', '\t':
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func needDigits(n int) func(string) error {
	return func(s string) error {
		if len(s) != n || !allDigits(s) {
			return fmt.Errorf("should be %d digits", n)
		}
		return nil
	}
}

func d(s string, i int) int { return int(s[i] - '0') }

func weighted(s string, w []int) int {
	t := 0
	for i, x := range w {
		t += d(s, i) * x
	}
	return t
}

// luhnValid tests a number whose last digit is a Luhn check digit.
func luhnValid(s string) bool {
	sum, dbl := 0, false
	for i := len(s) - 1; i >= 0; i-- {
		x := d(s, i)
		if dbl {
			x *= 2
			if x > 9 {
				x -= 9
			}
		}
		sum += x
		dbl = !dbl
	}
	return sum%10 == 0
}

// mod11Weights computes 11 - (sum mod 11) with 11 → 0, and reports 10 as impossible (no number is issued with it).
func mod11Weights(s string, w []int) (int, bool) {
	c := 11 - weighted(s, w)%11
	switch c {
	case 11:
		return 0, true
	case 10:
		return 0, false
	}
	return c, true
}

var errCheck = errors.New("the check digit does not match: a digit is probably wrong")

// dateOK says whether yy mm dd make a real date in some century (two-digit years cannot say which).
func dateOK(dd, mm, yy int) bool {
	for _, century := range []int{1800, 1900, 2000} {
		t := time.Date(century+yy, time.Month(mm), dd, 0, 0, 0, 0, time.UTC)
		if t.Day() == dd && int(t.Month()) == mm && mm >= 1 && mm <= 12 {
			return true
		}
	}
	return false
}

func atoi(s string) int { n, _ := strconv.Atoi(s); return n }

func groups(sizes ...int) func(string) string {
	return func(s string) string {
		var parts []string
		i := 0
		for _, n := range sizes {
			if i+n > len(s) {
				break
			}
			parts = append(parts, s[i:i+n])
			i += n
		}
		if i < len(s) {
			parts = append(parts, s[i:])
		}
		return strings.Join(parts, " ")
	}
}

func currentYY() int { return time.Now().Year() % 100 }
