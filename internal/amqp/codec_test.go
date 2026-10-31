package amqp

import (
	"errors"
	"testing"
)

// TestDeeplyNestedInputIsRefused guards the decoder's recursion against a hostile broker.
func TestDeeplyNestedInputIsRefused(t *testing.T) {
	descriptors := make([]byte, 100000) // 0x00 0x00 ...: a described type whose descriptor is a described type ...
	if _, _, err := Decode(descriptors); !errors.Is(err, errTooDeep) {
		t.Fatalf("a run of nested descriptors: err = %v, want errTooDeep", err)
	}

	// A list of one element whose element is a list of one element, nested past the limit.
	var nested []byte
	for range maxDepth + 5 {
		nested = append(nested, 0xc0, 0x00, 0x01) // list8: size (patched below), count 1
	}
	nested = append(nested, 0x40)
	for i := len(nested) - 1; i >= 0; i-- {
		if nested[i] == 0xc0 && i+2 < len(nested) {
			nested[i+1] = byte(min(255, len(nested)-i-2))
		}
	}
	if _, _, err := Decode(nested); !errors.Is(err, errTooDeep) {
		t.Fatalf("lists nested past the limit: err = %v, want errTooDeep", err)
	}
}
