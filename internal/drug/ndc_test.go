package drug

import "testing"

func TestAHyphenatedNDCKeepsItsSegments(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Format
	}{
		{"1234-5678-90", Format442},
		{"12345-678-90", Format532},
		{"12345-6789-0", Format541},
		{"12345-6789-01", Format542},
	} {
		got, err := ParseNDC(tc.in)
		if err != nil {
			t.Fatalf("ParseNDC(%q): %v", tc.in, err)
		}
		if got.Format != tc.want {
			t.Errorf("ParseNDC(%q) format = %v, want %v", tc.in, got.Format, tc.want)
		}
		if got.String() != tc.in {
			t.Errorf("ParseNDC(%q) round trip = %q", tc.in, got.String())
		}
	}
}

// The reason this package exists.
//
// Ten digits with no hyphens have three valid readings and each one bills as a different medicine. A
// library that picks one is not usually wrong in a way anybody sees: the claim is well-formed, the
// payer accepts it, and the money goes against a drug that was never dispensed.
func TestTenDigitsWithNoHyphensIsRefusedRatherThanGuessed(t *testing.T) {
	_, err := ParseNDC("1234567890")
	if err == nil {
		t.Fatal("ten unhyphenated digits were divided up without being told how, which is a guess between three medicines")
	}
	// The message has to offer the way out, or the caller's only option is to guess somewhere else.
	for _, want := range []string{"4-4-2", "5-3-2", "5-4-1"} {
		if !contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %s, so it does not say what the choices are: %v", want, err)
		}
	}
}

func TestElevenDigitsNeedNoHyphensBecauseThereIsOnlyOneReading(t *testing.T) {
	got, err := ParseNDC("12345678901")
	if err != nil {
		t.Fatalf("eleven digits are unambiguous and were refused: %v", err)
	}
	if got.String() != "12345-6789-01" {
		t.Errorf("got %q, want 12345-6789-01", got.String())
	}
	if got.Format != Format542 {
		t.Errorf("format = %v, want %v", got.Format, Format542)
	}
}

// Each format pads a different segment, which is why the segments are tracked rather than the digits.
// Padding on the left would turn every one of these into the same wrong answer.
func TestPaddingToElevenInsertsTheZeroWhereTheSegmentIsShort(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"1234-5678-90", "01234-5678-90"},
		{"12345-678-90", "12345-0678-90"},
		{"12345-6789-0", "12345-6789-00"},
	} {
		n, err := ParseNDC(tc.in)
		if err != nil {
			t.Fatalf("ParseNDC(%q): %v", tc.in, err)
		}
		got := n.Eleven()
		if got.String() != tc.want {
			t.Errorf("%q padded to %q, want %q", tc.in, got.String(), tc.want)
		}
		if !got.Padded {
			t.Errorf("%q was padded but does not say so, so nothing downstream can tell it apart from an assigned code", tc.in)
		}
		if len(got.Digits()) != 11 {
			t.Errorf("%q padded to %d digits", tc.in, len(got.Digits()))
		}
	}
}

// The three readings produce three different codes. Stated as a test because it is the whole argument
// for refusing to guess, and a reader who does not already believe it will not believe the comment.
func TestTheThreeReadingsOfTheSameDigitsBillAsDifferentDrugs(t *testing.T) {
	const digits = "1234567890"
	seen := map[string]Format{}
	for _, f := range []Format{Format442, Format532, Format541} {
		n, err := ParseNDCWithFormat(digits, f)
		if err != nil {
			t.Fatalf("ParseNDCWithFormat(%q, %v): %v", digits, f, err)
		}
		eleven := n.Eleven().Digits()
		if other, dup := seen[eleven]; dup {
			t.Fatalf("%v and %v both produce %s, so the ambiguity would be harmless", f, other, eleven)
		}
		seen[eleven] = f
	}
	if len(seen) != 3 {
		t.Fatalf("got %d distinct billing codes, want 3", len(seen))
	}
}

func TestAnAlreadyElevenDigitCodeIsNotPaddedAgain(t *testing.T) {
	n, err := ParseNDC("12345-6789-01")
	if err != nil {
		t.Fatal(err)
	}
	got := n.Eleven()
	if got.String() != "12345-6789-01" {
		t.Errorf("got %q, want unchanged", got.String())
	}
	if got.Padded {
		t.Error("an assigned eleven-digit code is marked as padded, which would make it look derived")
	}
}

func TestNonsenseIsRefused(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"abcde-1234-56",
		"123-456-78",     // no such assignment
		"12345-6789-012", // package too long
		"12345",          // too short
		"123456789012",   // too long
		"12345-6789",     // two parts
		"1-2-3-4",        // four parts
		"12345--01",      // empty middle
	} {
		if _, err := ParseNDC(in); err == nil {
			t.Errorf("ParseNDC(%q) was accepted", in)
		}
	}
}

// A format is not a way to reinterpret an eleven-digit code, and offering it as one would let a caller
// silently re-divide a billing code into a different drug.
func TestTheBillingFormatCannotBeUsedToDivideTenDigits(t *testing.T) {
	if _, err := ParseNDCWithFormat("1234567890", Format542); err == nil {
		t.Error("5-4-2 was accepted as a way to divide ten digits")
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && func() bool {
		for i := 0; i+len(needle) <= len(hay); i++ {
			if hay[i:i+len(needle)] == needle {
				return true
			}
		}
		return false
	}()
}
