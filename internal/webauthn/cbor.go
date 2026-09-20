package webauthn

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// A minimal CBOR reader, because WebAuthn's attestation object and its public keys are CBOR and Go has no CBOR in its
// standard library.
//
// Hand-rolled for the same reason the LDAP BER encoder is: pulling in a general CBOR library would bring a large amount of
// code that parses untrusted input, to use perhaps a tenth of it. This reads what WebAuthn actually contains and refuses
// everything else.
//
// # Refusing is the design
//
// This input arrives from a browser and is therefore hostile. Every limit below exists because a general decoder's
// flexibility is an attack surface when the thing being decoded is a credential:
//
//   - Indefinite-length items are refused. They exist to let a producer stream without knowing the length in advance, which
//     no WebAuthn structure needs, and they are how a decoder is persuaded to allocate without bound.
//   - Nesting is capped. A deeply nested structure is the CBOR equivalent of the billion-laughs attack, and a Go stack
//     overflow cannot be recovered from - it takes the whole engine down rather than one request.
//   - Every length is checked against the bytes actually remaining before anything is allocated. A claimed length of four
//     billion must cost nothing.
//   - Trailing bytes after the top-level item are refused, because two interpretations of one payload is how a signature
//     ends up covering something different from what was read.
//
// What is not implemented: tags, indefinite lengths, half-precision floats beyond what COSE needs, and bignums. Anything
// unrecognised is an error rather than a skip, so a structure this does not fully understand can never be treated as
// verified.

// Errors from CBOR decoding.
var (
	// ErrCBOR means the bytes are not CBOR this reader accepts.
	ErrCBOR = errors.New("not valid CBOR")

	// ErrCBORTooDeep means the structure nests further than is allowed.
	ErrCBORTooDeep = errors.New("CBOR nests too deeply")

	// ErrCBORTrailing means there are bytes after the top-level item.
	ErrCBORTrailing = errors.New("trailing bytes after the CBOR value")
)

// maxCBORDepth caps nesting.
//
// WebAuthn's deepest real structure is an attestation object containing a statement containing an array of certificates,
// which is four or five levels. Sixteen is generous and still nowhere near a stack problem.
const maxCBORDepth = 16

// maxCBORElements caps how many items a single map or array may declare.
//
// A COSE key has a handful of entries and an attestation object has three or four. Ten thousand is far beyond anything real
// and stops a claimed count from driving a large allocation before any of it is read.
const maxCBORElements = 10000

// CBORValue is a decoded CBOR item.
//
// A tagged union rather than any, so that reading a field as the wrong type is a compile-time or explicit runtime concern
// rather than a silent type assertion that panics on hostile input.
type CBORValue struct {
	// Kind says which field is meaningful.
	Kind CBORKind

	// Uint and Int hold integers. COSE uses negative keys extensively - the algorithm identifier for ES256 is -7 - so
	// negatives are not an edge case here.
	Uint uint64
	Int  int64

	// Bytes holds a byte string, Text a text string.
	Bytes []byte
	Text  string

	// Array holds an array's items.
	Array []CBORValue

	// Map holds a map's entries, in the order they appeared.
	//
	// A slice rather than a Go map, for two reasons. COSE keys are integers and text in the same map, which a Go map
	// cannot key cleanly. And canonical ordering matters for anything that might be re-encoded, which a Go map destroys.
	Map []CBORPair

	// Bool and Float for the remaining simple values.
	Bool  bool
	Float float64
}

// CBORPair is one map entry.
type CBORPair struct {
	Key   CBORValue
	Value CBORValue
}

// CBORKind identifies which field of a CBORValue is set.
type CBORKind int

// The kinds.
const (
	CBORUint CBORKind = iota
	CBORInt
	CBORBytes
	CBORText
	CBORArray
	CBORMap
	CBORBool
	CBORNull
	CBORUndefined
	CBORFloat
)

// String names a kind for an error message.
func (k CBORKind) String() string {
	switch k {
	case CBORUint:
		return "unsigned integer"
	case CBORInt:
		return "negative integer"
	case CBORBytes:
		return "byte string"
	case CBORText:
		return "text string"
	case CBORArray:
		return "array"
	case CBORMap:
		return "map"
	case CBORBool:
		return "boolean"
	case CBORNull:
		return "null"
	case CBORUndefined:
		return "undefined"
	case CBORFloat:
		return "float"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// DecodeCBOR reads one CBOR item and refuses trailing bytes.
//
// Trailing bytes are refused rather than ignored because an attestation object with extra data after it has two possible
// readings, and a signature that covers one of them while the code reads the other is exactly the shape of a real WebAuthn
// vulnerability.
func DecodeCBOR(data []byte) (CBORValue, error) {
	value, rest, err := decodeCBOR(data, 0)
	if err != nil {
		return CBORValue{}, err
	}
	if len(rest) != 0 {
		return CBORValue{}, fmt.Errorf("%w: %d byte(s) remain", ErrCBORTrailing, len(rest))
	}

	return value, nil
}

// decodeCBOR reads one item and returns what is left.
func decodeCBOR(data []byte, depth int) (CBORValue, []byte, error) {
	if depth > maxCBORDepth {
		return CBORValue{}, nil, ErrCBORTooDeep
	}
	if len(data) == 0 {
		return CBORValue{}, nil, fmt.Errorf("%w: no bytes", ErrCBOR)
	}

	initial := data[0]
	major := initial >> 5
	additional := initial & 0x1f
	rest := data[1:]

	// Indefinite length, refused everywhere.
	if additional == 31 {
		return CBORValue{}, nil, fmt.Errorf("%w: indefinite-length items are not accepted, because nothing in "+
			"WebAuthn needs them and they allow unbounded allocation", ErrCBOR)
	}
	if additional > 27 {
		return CBORValue{}, nil, fmt.Errorf("%w: reserved additional information %d", ErrCBOR, additional)
	}

	// Major type 7 is the simple values and floats, whose additional information is not a length.
	if major == 7 {
		return decodeSimple(additional, rest)
	}

	argument, rest, err := readArgument(additional, rest)
	if err != nil {
		return CBORValue{}, nil, err
	}

	switch major {
	case 0:
		return CBORValue{Kind: CBORUint, Uint: argument}, rest, nil

	case 1:
		// A negative integer is encoded as -1 minus the argument, so the largest representable is beyond int64. Refused
		// rather than wrapped, because a wrapped algorithm identifier would silently select the wrong one.
		if argument > math.MaxInt64 {
			return CBORValue{}, nil, fmt.Errorf("%w: negative integer too large to represent", ErrCBOR)
		}

		return CBORValue{Kind: CBORInt, Int: -1 - int64(argument)}, rest, nil

	case 2, 3:
		// The length is checked against what remains before anything is allocated, so a claimed four billion costs
		// nothing.
		if argument > uint64(len(rest)) {
			return CBORValue{}, nil, fmt.Errorf("%w: a string claims %d byte(s) and only %d remain",
				ErrCBOR, argument, len(rest))
		}
		content := rest[:argument]
		rest = rest[argument:]

		if major == 2 {
			// Copied rather than aliased. The caller keeps a credential's public key from this, and aliasing the
			// request buffer would mean a later reuse of that buffer silently altered a stored credential.
			out := make([]byte, len(content))
			copy(out, content)

			return CBORValue{Kind: CBORBytes, Bytes: out}, rest, nil
		}

		return CBORValue{Kind: CBORText, Text: string(content)}, rest, nil

	case 4:
		if argument > maxCBORElements {
			return CBORValue{}, nil, fmt.Errorf("%w: an array declares %d items", ErrCBOR, argument)
		}
		// Each item is at least one byte, so a count larger than the bytes remaining cannot be honest. Checked before
		// allocating the slice.
		if argument > uint64(len(rest)) {
			return CBORValue{}, nil, fmt.Errorf("%w: an array declares %d items and only %d byte(s) remain",
				ErrCBOR, argument, len(rest))
		}

		items := make([]CBORValue, 0, argument)
		for i := uint64(0); i < argument; i++ {
			var item CBORValue
			item, rest, err = decodeCBOR(rest, depth+1)
			if err != nil {
				return CBORValue{}, nil, err
			}
			items = append(items, item)
		}

		return CBORValue{Kind: CBORArray, Array: items}, rest, nil

	case 5:
		if argument > maxCBORElements {
			return CBORValue{}, nil, fmt.Errorf("%w: a map declares %d entries", ErrCBOR, argument)
		}
		// Each entry is at least two bytes.
		if argument > uint64(len(rest)) {
			return CBORValue{}, nil, fmt.Errorf("%w: a map declares %d entries and only %d byte(s) remain",
				ErrCBOR, argument, len(rest))
		}

		pairs := make([]CBORPair, 0, argument)
		for i := uint64(0); i < argument; i++ {
			var key, value CBORValue
			key, rest, err = decodeCBOR(rest, depth+1)
			if err != nil {
				return CBORValue{}, nil, err
			}
			value, rest, err = decodeCBOR(rest, depth+1)
			if err != nil {
				return CBORValue{}, nil, err
			}
			pairs = append(pairs, CBORPair{Key: key, Value: value})
		}

		return CBORValue{Kind: CBORMap, Map: pairs}, rest, nil

	case 6:
		// Tags are refused. A tag changes how the value beneath it should be interpreted, and accepting one this code
		// does not act on would mean reading a value with its meaning stripped off.
		return CBORValue{}, nil, fmt.Errorf("%w: tagged values are not accepted", ErrCBOR)
	}

	return CBORValue{}, nil, fmt.Errorf("%w: unrecognised major type %d", ErrCBOR, major)
}

// readArgument reads the length or value that follows the initial byte.
func readArgument(additional byte, data []byte) (uint64, []byte, error) {
	switch {
	case additional < 24:
		return uint64(additional), data, nil
	case additional == 24:
		if len(data) < 1 {
			return 0, nil, fmt.Errorf("%w: truncated one-byte argument", ErrCBOR)
		}

		return uint64(data[0]), data[1:], nil
	case additional == 25:
		if len(data) < 2 {
			return 0, nil, fmt.Errorf("%w: truncated two-byte argument", ErrCBOR)
		}

		return uint64(binary.BigEndian.Uint16(data)), data[2:], nil
	case additional == 26:
		if len(data) < 4 {
			return 0, nil, fmt.Errorf("%w: truncated four-byte argument", ErrCBOR)
		}

		return uint64(binary.BigEndian.Uint32(data)), data[4:], nil
	case additional == 27:
		if len(data) < 8 {
			return 0, nil, fmt.Errorf("%w: truncated eight-byte argument", ErrCBOR)
		}

		return binary.BigEndian.Uint64(data), data[8:], nil
	}

	return 0, nil, fmt.Errorf("%w: additional information %d", ErrCBOR, additional)
}

// decodeSimple reads major type 7: booleans, null, undefined and floats.
func decodeSimple(additional byte, rest []byte) (CBORValue, []byte, error) {
	switch additional {
	case 20:
		return CBORValue{Kind: CBORBool, Bool: false}, rest, nil
	case 21:
		return CBORValue{Kind: CBORBool, Bool: true}, rest, nil
	case 22:
		return CBORValue{Kind: CBORNull}, rest, nil
	case 23:
		return CBORValue{Kind: CBORUndefined}, rest, nil
	case 25:
		if len(rest) < 2 {
			return CBORValue{}, nil, fmt.Errorf("%w: truncated half-precision float", ErrCBOR)
		}
		f := float64(math.Float32frombits(halfToSingle(binary.BigEndian.Uint16(rest))))

		return CBORValue{Kind: CBORFloat, Float: f}, rest[2:], nil
	case 26:
		if len(rest) < 4 {
			return CBORValue{}, nil, fmt.Errorf("%w: truncated single-precision float", ErrCBOR)
		}
		f := float64(math.Float32frombits(binary.BigEndian.Uint32(rest)))

		return CBORValue{Kind: CBORFloat, Float: f}, rest[4:], nil
	case 27:
		if len(rest) < 8 {
			return CBORValue{}, nil, fmt.Errorf("%w: truncated double-precision float", ErrCBOR)
		}
		f := math.Float64frombits(binary.BigEndian.Uint64(rest))

		return CBORValue{Kind: CBORFloat, Float: f}, rest[8:], nil
	default:
		// Simple values below 20 and the unassigned ones. Refused rather than skipped: nothing in WebAuthn uses them,
		// and accepting an item whose meaning is unknown is how a structure gets treated as understood.
		return CBORValue{}, nil, fmt.Errorf("%w: simple value %d is not accepted", ErrCBOR, additional)
	}
}

// halfToSingle converts an IEEE half-precision float to single precision bits.
func halfToSingle(h uint16) uint32 {
	sign := uint32(h&0x8000) << 16
	exponent := uint32(h>>10) & 0x1f
	mantissa := uint32(h & 0x03ff)

	switch exponent {
	case 0:
		if mantissa == 0 {
			return sign
		}
		// Subnormal: normalise it.
		exponent = 127 - 15 + 1
		for mantissa&0x0400 == 0 {
			mantissa <<= 1
			exponent--
		}
		mantissa &= 0x03ff

		return sign | exponent<<23 | mantissa<<13
	case 0x1f:
		// Infinity or not-a-number.
		return sign | 0xff<<23 | mantissa<<13
	default:
		return sign | (exponent-15+127)<<23 | mantissa<<13
	}
}

// MapEntry finds a map entry by integer key, which is how COSE keys are labelled.
func (v CBORValue) MapEntry(key int64) (CBORValue, bool) {
	if v.Kind != CBORMap {
		return CBORValue{}, false
	}

	for _, pair := range v.Map {
		switch pair.Key.Kind {
		case CBORUint:
			if pair.Key.Uint <= math.MaxInt64 && int64(pair.Key.Uint) == key {
				return pair.Value, true
			}
		case CBORInt:
			if pair.Key.Int == key {
				return pair.Value, true
			}
		}
	}

	return CBORValue{}, false
}

// MapEntryText finds a map entry by text key, which is how the attestation object is labelled.
func (v CBORValue) MapEntryText(key string) (CBORValue, bool) {
	if v.Kind != CBORMap {
		return CBORValue{}, false
	}

	for _, pair := range v.Map {
		if pair.Key.Kind == CBORText && pair.Key.Text == key {
			return pair.Value, true
		}
	}

	return CBORValue{}, false
}

// AsInt reads a value as a signed integer, whichever way it was encoded.
//
// Needed because COSE algorithm identifiers are negative and key types are positive, and a caller should not have to know
// which encoding a particular field used.
func (v CBORValue) AsInt() (int64, bool) {
	switch v.Kind {
	case CBORUint:
		if v.Uint > math.MaxInt64 {
			return 0, false
		}

		return int64(v.Uint), true
	case CBORInt:
		return v.Int, true
	default:
		return 0, false
	}
}
