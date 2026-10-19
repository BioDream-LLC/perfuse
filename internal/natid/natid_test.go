package natid

import (
	"strings"
	"testing"
)

// Every scheme's example passes its own check, and changing one digit of it fails it (where there is a check).
func TestEveryExamplePassesAndATypoFails(t *testing.T) {
	for _, s := range All() {
		if err := s.Validate(s.Example); err != nil {
			t.Errorf("%s: example %q: %v", s.ID, s.Example, err)
			continue
		}
		if strings.HasPrefix(s.Check, "none") {
			continue
		}
		n := s.Normalise(s.Example)
		caught := 0
		tries := 0
		for i := 0; i < len(n); i++ {
			if n[i] < '0' || n[i] > '9' {
				continue
			}
			typo := []byte(n)
			typo[i] = '0' + (typo[i]-'0'+1)%10
			tries++
			if s.Validate(string(typo)) != nil {
				caught++
			}
		}
		// A changed digit is caught nearly always. Digits a check does not cover (the century in a Swedish number or a
		// kennitala) and the rare collision of a two-stage modulus 11 are the exceptions, so two may slip through.
		if tries > 0 && caught < tries-2 {
			t.Errorf("%s: only %d of %d one-digit typos of %s were caught", s.ID, caught, tries, n)
		}
	}
}

// Numbers published as valid by their issuers or in standard references.
func TestPublishedNumbers(t *testing.T) {
	for id, v := range map[string]string{
		"uk-nhs":   "943 476 5919",
		"nl-bsn":   "111222333",
		"pl-pesel": "44051401359",
		"fi-hetu":  "131052-308T",
		"es-dni":   "12345678Z",
		"ie-ppsn":  "1234567FA",
	} {
		s, _ := ByID(id)
		if err := s.Validate(v); err != nil {
			t.Errorf("%s %s: %v", id, v, err)
		}
	}
}

func TestCanonicaliseRewritesAliasesAndSpacing(t *testing.T) {
	c := Canonicalise("2.16.840.1.113883.2.1.4.1", "943 476 5919", nil)
	if c.System != "https://fhir.nhs.uk/Id/nhs-number" || c.Value != "9434765919" || c.Problem != "" {
		t.Fatalf("%+v", c)
	}
	c = Canonicalise("urn:oid:2.16.840.1.113883.2.1.4.1", "943 476 5918", nil)
	if c.Problem == "" || !strings.Contains(c.Problem, "••••••5918") {
		t.Fatalf("a wrong check digit was not reported (masked): %+v", c)
	}
	c = Canonicalise("http://example.org/mrn", " 42 ", nil)
	if c.Scheme != nil || c.Value != "42" {
		t.Fatalf("a local identifier was changed: %+v", c)
	}
	// A site's own URI for a scheme is used, and recognised when it comes back.
	over := map[string]string{"ie-ppsn": "https://example.ie/ppsn"}
	if c := Canonicalise("urn:perfuse:id:ie:ppsn", "1234567FA", over); c.System != "https://example.ie/ppsn" {
		t.Fatalf("override not applied: %+v", c)
	}
	if c := Canonicalise("https://example.ie/ppsn", "1234567FA", over); c.Scheme == nil || c.Scheme.ID != "ie-ppsn" {
		t.Fatalf("override not recognised: %+v", c)
	}
}

func TestSwedishNumbersMatchWithAndWithoutCentury(t *testing.T) {
	s, _ := ByID("se-pnr")
	if a, b := s.Normalise("811218-9876"), s.Normalise("198112189876"); a != b {
		t.Fatalf("%s != %s", a, b)
	}
}

func TestHL7Types(t *testing.T) {
	if s, ok := ForHL7Type("NNDEU", ""); !ok || s.ID != "de-kvnr" {
		t.Fatal("NNDEU")
	}
	if s, ok := ForHL7Type("NH", "NHS"); !ok || s.ID != "uk-nhs" {
		t.Fatal("NH")
	}
	for _, c := range []string{"GBR", "DEU", "NLD", "FRA", "BEL", "AUT", "CHE", "ITA", "ESP", "PRT", "POL", "SWE", "DNK", "NOR", "FIN", "IRL", "LUX", "ISL", "EST", "LTU", "LVA", "CZE", "SVK", "HUN", "ROU", "BGR", "HRV", "SVN", "GRC"} {
		n := 0
		for _, s := range schemes {
			if s.Primary && s.ISO3 == c {
				n++
			}
		}
		if n != 1 {
			t.Errorf("%s has %d primary schemes", c, n)
		}
	}
}

func TestMask(t *testing.T) {
	if got := Mask("943 476 5919"); got != "••••••5919" {
		t.Fatal(got)
	}
}
