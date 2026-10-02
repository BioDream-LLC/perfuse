package x12

import (
	"strings"
	"testing"
)

// The filter example printed in the manual has to work. A documented expression that does not compile is worse than no example: it
// will be copied into a channel, refused at load, and the person copying it will reasonably conclude the feature is broken.
func TestTheDocumentedAttachmentFilterCompilesAndMatches(t *testing.T) {
	const documented = `STC-1.2 in ("227", "233", "252", "287")`

	f, err := ParseFilter(documented)
	if err != nil {
		t.Fatalf("the filter printed in the manual does not compile: %v", err)
	}

	requesting, err := Parse([]byte(status277))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := f.Eval(requesting)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Error("the documented filter does not match a 277 that asks for an operative note")
	}

	finalised, err := Parse([]byte(strings.Replace(status277,
		stc("R4:252:PR", "20260916", "450.00", "Operative note required before adjudication"),
		stc("F1:65:PR", "20260916", "450.00", "Finalised, payment made"), 1)))
	if err != nil {
		t.Fatal(err)
	}
	// The direction that matters more: a false positive puts every paid claim in a chase queue, and a queue that is always wrong gets
	// ignored - which is worse than not having one.
	ok, err = f.Eval(finalised)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Error("the documented filter matches a finalised claim")
	}
}
