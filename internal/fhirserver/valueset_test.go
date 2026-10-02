package fhirserver

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/codeset"
)

// A value set url must say which side of the mapping it means.
//
// Defaulting to one would answer "what does this engine accept" to somebody who asked "what can it produce". Both are plausible
// answers, which is what makes the confusion dangerous: nothing about the response would look wrong.
func TestAValueSetURLMustNameASide(t *testing.T) {
	name, side, err := ParseValueSetURL("urn:perfuse:codeset:sex:source")
	if err != nil || name != "sex" || side != SideSource {
		t.Errorf("source url parsed as (%q, %q, %v)", name, side, err)
	}

	if name, side, err = ParseValueSetURL("urn:perfuse:codeset:sex:target"); err != nil || name != "sex" || side != SideTarget {
		t.Errorf("target url parsed as (%q, %q, %v)", name, side, err)
	}

	_, _, err = ParseValueSetURL("urn:perfuse:codeset:sex")
	if err == nil {
		t.Fatal("a url naming no side was accepted, so the server would guess which question was being asked")
	}
	if !strings.Contains(err.Error(), "which side") {
		t.Errorf("the refusal does not explain what is missing: %v", err)
	}
}

// The two sides are different sets, and both must be right.
func TestExpandDistinguishesAcceptedFromProduced(t *testing.T) {
	table := sexTable()

	source := mustExpand(table, &ValueSetRequest{Side: SideSource})
	if strings.Join(source, ",") != "A,F,M,O" {
		t.Errorf("accepted codes = %v, want the four v2 codes sorted", source)
	}

	target := mustExpand(table, &ValueSetRequest{Side: SideTarget})

	// Deduplicated: two source codes map to other, and a set listing it twice is wrong about its own size.
	joined := strings.Join(target, ",")
	if strings.Count(joined, "other") != 1 {
		t.Errorf("produced codes contain other more than once: %v", target)
	}

	// And the default is included, because it is a code this mapping emits and it appears in no entry.
	//
	// Omitting it is wrong in exactly the case that matters: an unrecognised code arrives, the default goes out, and a receiver
	// validating against this expansion rejects a value we told them we would never send.
	if !strings.Contains(joined, "unknown") {
		t.Errorf("the table's default is missing from the produced set: %v", target)
	}
}

// An unmapped target must not appear in the produced set.
func TestExpandOmitsDeliberatelyUnmappedTargets(t *testing.T) {
	table := strictTable()

	// strictTable maps ICU to CRIT and nothing else, so the produced set is exactly that.
	if got := mustExpand(table, &ValueSetRequest{Side: SideTarget}); strings.Join(got, ",") != "CRIT" {
		t.Errorf("produced codes = %v, want CRIT", got)
	}
}

// Filtering and bounding must both apply.
func TestExpandFilterAndCount(t *testing.T) {
	table := sexTable()

	if got := mustExpand(table, &ValueSetRequest{Side: SideSource, Filter: "f"}); strings.Join(got, ",") != "F" {
		t.Errorf("filter f gave %v, want F", got)
	}

	got := mustExpand(table, &ValueSetRequest{Side: SideSource, Count: 2})
	if len(got) != 2 {
		t.Errorf("count 2 gave %d codes", len(got))
	}
}

// Validation must say what will actually happen, not merely yes or no.
func TestValidateCodeSaysWhatHappensNext(t *testing.T) {
	t.Run("a recognised code says what it becomes", func(t *testing.T) {
		ok, msg := ValidateCodeInTable(sexTable(), &ValueSetRequest{Side: SideSource, Code: "F"})
		if !ok {
			t.Fatalf("F was not recognised: %s", msg)
		}
		if !strings.Contains(msg, "female") {
			t.Errorf("the message does not say what F becomes: %s", msg)
		}
	})

	t.Run("an unrecognised code on a defaulting table says it is accepted anyway", func(t *testing.T) {
		ok, msg := ValidateCodeInTable(sexTable(), &ValueSetRequest{Side: SideSource, Code: "Q"})
		if ok {
			t.Fatalf("Q was reported as recognised: %s", msg)
		}

		// The distinction that matters: not in the set, but the message will still be accepted. Somebody planning a
		// feed needs both halves of that.
		if !strings.Contains(msg, "default") {
			t.Errorf("the message does not say the code would be defaulted: %s", msg)
		}
	})

	t.Run("an unrecognised code on a strict table says the message would be refused", func(t *testing.T) {
		ok, msg := ValidateCodeInTable(strictTable(), &ValueSetRequest{Side: SideSource, Code: "WARD9"})
		if ok {
			t.Fatal("a strict table recognised a code it does not hold")
		}
		if !strings.Contains(msg, "refused") {
			t.Errorf("the message does not say the message would be refused: %s", msg)
		}
	})

	t.Run("a recognised but deliberately unmapped code says so", func(t *testing.T) {
		// HL7 v2 patient class U means unknown and is mapped to nothing on purpose. Reporting that it "becomes \"\""
		// reads like a fault in the mapping rather than a decision about it, and the decision is the useful part.
		table := &codeset.Table{
			Name:      "class",
			Describes: "patient class",
			Entries: []codeset.Entry{
				{From: "I", To: "IMP"},
				{From: "U", To: "", Why: "unknown, and guessing a class would invent a fact about the visit"},
			},
		}
		table.Compile()

		ok, msg := ValidateCodeInTable(table, &ValueSetRequest{Side: SideSource, Code: "U"})
		if !ok {
			t.Fatalf("U is in the table and was reported as absent: %s", msg)
		}
		if !strings.Contains(msg, "deliberately not translated") {
			t.Errorf("the message does not say the code is deliberately unmapped: %s", msg)
		}
		if !strings.Contains(msg, "invent a fact") {
			t.Errorf("the recorded reason was not passed on: %s", msg)
		}
	})

	t.Run("the default counts as a produced code", func(t *testing.T) {
		ok, msg := ValidateCodeInTable(sexTable(), &ValueSetRequest{Side: SideTarget, Code: "unknown"})
		if !ok {
			t.Errorf("the default is not reported as producible, though it is emitted for every unknown code: %s", msg)
		}
	})
}

// Parameters these operations do not implement must be refused.
func TestValueSetOperationsRefuseWhatTheyCannotDo(t *testing.T) {
	const url = "urn:perfuse:codeset:sex:source"

	expandCases := []struct {
		name   string
		params map[string][]string
		expect string
	}{
		{"activeOnly is not honoured", map[string][]string{"url": {url}, "activeOnly": {"true"}}, "refused rather than ignored"},
		{"offset is not honoured", map[string][]string{"url": {url}, "offset": {"10"}}, "refused rather than ignored"},
		{"no url", map[string][]string{}, "url is required"},
		{"count must be a number", map[string][]string{"url": {url}, "count": {"lots"}}, "non-negative number"},
		{"url twice", map[string][]string{"url": {url, url}}, "more than once"},
	}

	for _, tc := range expandCases {
		t.Run("expand: "+tc.name, func(t *testing.T) {
			_, err := ParseExpand(tc.params)
			if err == nil {
				t.Fatal("accepted, and an ignored parameter answers a different question from the one asked")
			}
			if !strings.Contains(err.Error(), tc.expect) {
				t.Errorf("the refusal does not mention %q: %v", tc.expect, err)
			}
		})
	}

	t.Run("validate-code needs a code", func(t *testing.T) {
		if _, err := ParseValidateCode(map[string][]string{"url": {url}}); err == nil {
			t.Fatal("validate-code was accepted with no code to check")
		}
	})

	t.Run("validate-code refuses a system it does not honour", func(t *testing.T) {
		_, err := ParseValidateCode(map[string][]string{"url": {url}, "code": {"F"}, "system": {"http://x"}})
		if err == nil {
			t.Fatal("a system parameter was accepted but is not honoured, so the answer would be about a different set")
		}
	})
}

// The projected ValueSet must carry a total that reflects the set, not the page.
func TestValueSetTotalDescribesTheSetNotThePage(t *testing.T) {
	table := sexTable()

	req := &ValueSetRequest{Side: SideSource, Count: 2}
	codes, total := ExpandTable(table, req)

	// Four codes in the set, two returned. The total must be four.
	//
	// This test was named after that distinction and did not check it, while the code reported the page size as the total -
	// contradicting the comment on the field. A client paging on total would have stopped after the first page believing it had
	// everything, which is the exact failure the separate field exists to prevent.
	if total != 4 {
		t.Errorf("total = %d, want 4: the whole set, not the page", total)
	}

	vs := TableAsValueSet(table, req, codes, total)
	if vs.Expansion != nil && vs.Expansion.Total != nil && *vs.Expansion.Total != 4 {
		t.Errorf("the published total is %d, want 4", *vs.Expansion.Total)
	}
	if vs.Expansion == nil {
		t.Fatal("no expansion was produced")
	}
	if len(vs.Expansion.Contains) != 2 {
		t.Errorf("got %d codes in the expansion, want 2", len(vs.Expansion.Contains))
	}
	if vs.Expansion.Identifier == "" {
		t.Error("the expansion does not say which value set it is of")
	}
	if vs.ResourceID() == "" {
		t.Error("the value set has no id")
	}
}

// mustExpand drops the total for the tests that only care about the codes.
func mustExpand(t *codeset.Table, req *ValueSetRequest) []string {
	codes, _ := ExpandTable(t, req)

	return codes
}
