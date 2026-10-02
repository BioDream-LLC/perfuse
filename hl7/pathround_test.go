package hl7

import "testing"

// A path's canonical form has to resolve to the same thing the path did.
//
// It did not. ParsePath("PID-3(1)") kept FieldRepeat 1 and resolved to the first
// identifier, but String() rendered it as "PID-3", which resolves to every repetition
// joined together. These strings are shown in the interface and in shadow diffs and get
// pasted back into channel files, so a path that changes meaning on the way through the
// display is a configuration that quietly stops doing what it did.
//
// Found while building the equivalent for X12, where a round-trip test caught it.
func TestPathStringPreservesARepetitionOfOne(t *testing.T) {
	p, err := ParsePath("PID-3(1)")
	if err != nil {
		t.Fatal(err)
	}
	if p.FieldRepeat != 1 {
		t.Fatalf("FieldRepeat = %d, want 1", p.FieldRepeat)
	}
	if got := p.String(); got != "PID-3(1)" {
		t.Errorf("String() = %q, want PID-3(1)", got)
	}
}

func TestPathRoundTripPreservesMeaning(t *testing.T) {
	raw := []byte("MSH|^~\\&|A|B|C|D|20260819||ADT^A01|1|P|2.5.1\rPID|||ID1~ID2~ID3\r")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	for _, in := range []string{"PID-3", "PID-3(1)", "PID-3(2)", "PID-3(1).1", "PID-5(2).1"} {
		p, err := ParsePath(in)
		if err != nil {
			t.Errorf("ParsePath(%q): %v", in, err)
			continue
		}

		back, err := ParsePath(p.String())
		if err != nil {
			t.Errorf("the canonical form %q does not parse: %v", p.String(), err)
			continue
		}
		if back != p {
			t.Errorf("round trip of %q gave %+v, want %+v", in, back, p)
			continue
		}
		// And the value it resolves to must not change either, which is the reason the
		// path equality above matters at all.
		if got, want := m.ValueAt(back).String(), m.ValueAt(p).String(); got != want {
			t.Errorf("%q resolved to %q before the round trip and %q after", in, want, got)
		}
	}
}

func TestNoRepetitionStillMeansEveryRepetition(t *testing.T) {
	// The other half of the distinction. If this ever collapses to the first value,
	// a second patient identifier gets dropped with nothing to show it happened.
	raw := []byte("MSH|^~\\&|A|B|C|D|20260819||ADT^A01|1|P|2.5.1\rPID|||ID1~ID2~ID3\r")
	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}

	all, err := ParsePath("PID-3")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.ValueAt(all).String(); got != "ID1~ID2~ID3" {
		t.Errorf("PID-3 = %q, want every repetition", got)
	}

	first, err := ParsePath("PID-3(1)")
	if err != nil {
		t.Fatal(err)
	}
	if got := m.ValueAt(first).String(); got != "ID1" {
		t.Errorf("PID-3(1) = %q, want the first repetition only", got)
	}
}
