package webauthn

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

// The CBOR reader takes bytes straight from a browser, so these are about what it refuses rather than what it reads.

func TestCBORReadsWhatWebAuthnContains(t *testing.T) {
	cases := map[string]struct {
		input []byte
		check func(*testing.T, CBORValue)
	}{
		"a small unsigned integer": {
			input: []byte{0x01},
			check: func(t *testing.T, v CBORValue) {
				if v.Kind != CBORUint || v.Uint != 1 {
					t.Errorf("got %+v", v)
				}
			},
		},
		"a negative integer, as COSE algorithms are": {
			input: []byte{0x26}, // -7
			check: func(t *testing.T, v CBORValue) {
				got, ok := v.AsInt()
				if !ok || got != -7 {
					t.Errorf("got %v %v", got, ok)
				}
			},
		},
		"a byte string": {
			input: []byte{0x43, 0x01, 0x02, 0x03},
			check: func(t *testing.T, v CBORValue) {
				if !bytes.Equal(v.Bytes, []byte{1, 2, 3}) {
					t.Errorf("got %v", v.Bytes)
				}
			},
		},
		"a text string": {
			input: []byte{0x64, 'n', 'o', 'n', 'e'},
			check: func(t *testing.T, v CBORValue) {
				if v.Text != "none" {
					t.Errorf("got %q", v.Text)
				}
			},
		},
		"a map with an integer key": {
			input: []byte{0xa1, 0x01, 0x02},
			check: func(t *testing.T, v CBORValue) {
				entry, ok := v.MapEntry(1)
				if !ok || entry.Uint != 2 {
					t.Errorf("got %+v %v", entry, ok)
				}
			},
		},
		"a map with a text key": {
			input: []byte{0xa1, 0x63, 'f', 'm', 't', 0x64, 'n', 'o', 'n', 'e'},
			check: func(t *testing.T, v CBORValue) {
				entry, ok := v.MapEntryText("fmt")
				if !ok || entry.Text != "none" {
					t.Errorf("got %+v %v", entry, ok)
				}
			},
		},
		"an empty map, as an attestation statement is": {
			input: []byte{0xa0},
			check: func(t *testing.T, v CBORValue) {
				if v.Kind != CBORMap || len(v.Map) != 0 {
					t.Errorf("got %+v", v)
				}
			},
		},
	}

	for name, c := range cases {
		value, err := DecodeCBOR(c.input)
		if err != nil {
			t.Errorf("%s: %v", name, err)

			continue
		}
		c.check(t, value)
	}
}

// TestCBORRefusesWhatItDoesNotNeed is the design.
//
// A general decoder's flexibility is an attack surface when the thing being decoded is a credential.
func TestCBORRefusesWhatItDoesNotNeed(t *testing.T) {
	refused := map[string][]byte{
		// Indefinite length: allows unbounded allocation and nothing in WebAuthn needs it.
		"an indefinite-length byte string": {0x5f, 0x41, 0x01, 0xff},
		"an indefinite-length array":       {0x9f, 0x01, 0xff},
		"an indefinite-length map":         {0xbf, 0x01, 0x02, 0xff},

		// A tag changes the meaning of what is under it, so accepting one this code ignores means reading a value with
		// its meaning stripped off.
		"a tagged value": {0xc1, 0x01},

		// Reserved and unassigned.
		"reserved additional information": {0x1c},
		"an unassigned simple value":      {0xf8, 0x01},

		// Truncation everywhere.
		"nothing at all":             {},
		"a truncated argument":       {0x19, 0x01},
		"a byte string past the end": {0x45, 0x01, 0x02},
		"a map missing its value":    {0xa1, 0x01},
		"an array missing an item":   {0x82, 0x01},

		// A length that could not possibly be honest, which must cost nothing to refuse.
		"a byte string claiming four gigabytes": {0x5a, 0xff, 0xff, 0xff, 0xff},
		"a map claiming a billion entries":      {0xba, 0x3b, 0x9a, 0xca, 0x00},
		"an array claiming a billion items":     {0x9a, 0x3b, 0x9a, 0xca, 0x00},
	}

	for name, input := range refused {
		if _, err := DecodeCBOR(input); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

// TestTrailingBytesAreRefused covers two readings of one payload.
//
// An attestation object with extra data after it has two possible interpretations, and a signature covering one while the code
// reads the other is the shape of a real WebAuthn vulnerability.
func TestTrailingBytesAreRefused(t *testing.T) {
	_, err := DecodeCBOR([]byte{0x01, 0x02})
	if err == nil {
		t.Fatal("trailing bytes were accepted")
	}
	if !errors.Is(err, ErrCBORTrailing) {
		t.Errorf("the refusal was %v", err)
	}
}

// TestDeepNestingIsRefusedRatherThanOverflowingTheStack is the billion-laughs equivalent.
//
// A Go stack overflow cannot be recovered from - it takes the whole engine down rather than one request - so this has to be a
// refusal rather than a panic somebody catches.
func TestDeepNestingIsRefusedRatherThanOverflowingTheStack(t *testing.T) {
	// Ten thousand nested single-element arrays.
	deep := bytes.Repeat([]byte{0x81}, 10000)
	deep = append(deep, 0x01)

	_, err := DecodeCBOR(deep)
	if err == nil {
		t.Fatal("a ten-thousand-deep structure was accepted")
	}
	if !errors.Is(err, ErrCBORTooDeep) {
		// Any refusal is acceptable, but the depth error is the one that says why.
		t.Logf("refused with %v rather than the depth error", err)
	}

	// And the same for maps, which nest through both keys and values.
	deepMap := bytes.Repeat([]byte{0xa1, 0x01}, 10000)
	deepMap = append(deepMap, 0x01)
	if _, err := DecodeCBOR(deepMap); err == nil {
		t.Error("a deeply nested map was accepted")
	}
}

// TestByteStringsAreCopiedNotAliased covers a subtle correctness hazard.
//
// A credential's public key comes out of this and is stored. Aliasing the request buffer would mean a later reuse of that
// buffer silently altered a stored credential - which would present as a passkey that worked once and then never again.
func TestByteStringsAreCopiedNotAliased(t *testing.T) {
	buffer := []byte{0x43, 0x01, 0x02, 0x03}

	value, err := DecodeCBOR(buffer)
	if err != nil {
		t.Fatal(err)
	}

	// Overwrite the source the way a reused request buffer would.
	buffer[1] = 0xff
	buffer[2] = 0xff
	buffer[3] = 0xff

	if !bytes.Equal(value.Bytes, []byte{1, 2, 3}) {
		t.Errorf("the decoded bytes changed with the buffer: %v", value.Bytes)
	}
}

// TestAsIntHandlesBothEncodings covers COSE's mixed use.
func TestAsIntHandlesBothEncodings(t *testing.T) {
	// The key type is positive and the algorithm negative, in the same map.
	value, err := DecodeCBOR([]byte{0xa2, 0x01, 0x02, 0x03, 0x26})
	if err != nil {
		t.Fatal(err)
	}

	keyType, ok := value.MapEntry(1)
	if !ok {
		t.Fatal("no key type")
	}
	if got, ok := keyType.AsInt(); !ok || got != 2 {
		t.Errorf("key type is %v %v", got, ok)
	}

	algorithm, ok := value.MapEntry(3)
	if !ok {
		t.Fatal("no algorithm")
	}
	if got, ok := algorithm.AsInt(); !ok || got != -7 {
		t.Errorf("algorithm is %v %v", got, ok)
	}
}

// TestAnEnormousNegativeIntegerIsRefused covers wraparound.
//
// A negative integer is encoded as -1 minus the argument, so the largest representable is beyond int64. Wrapping it would
// silently produce a different algorithm identifier from the one sent.
func TestAnEnormousNegativeIntegerIsRefused(t *testing.T) {
	// Major type 1 with an eight-byte argument of all ones.
	input := []byte{0x3b, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff}

	if _, err := DecodeCBOR(input); err == nil {
		t.Error("a negative integer too large to represent was accepted")
	}
}

// TestTheKindNamesAreUseful covers error messages, which somebody reads at three in the morning.
func TestTheKindNamesAreUseful(t *testing.T) {
	for kind, want := range map[CBORKind]string{
		CBORUint:  "unsigned integer",
		CBORBytes: "byte string",
		CBORMap:   "map",
		CBORText:  "text string",
	} {
		if got := kind.String(); got != want {
			t.Errorf("kind %d is named %q, expected %q", int(kind), got, want)
		}
	}

	// And an unrecognised one says its number rather than nothing.
	if got := CBORKind(99).String(); !strings.Contains(got, "99") {
		t.Errorf("an unknown kind is named %q", got)
	}
}
