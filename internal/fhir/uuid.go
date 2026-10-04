package fhir

import (
	"crypto/sha256"
	"fmt"
	"strings"
)

// DeterministicUUID derives an RFC 9562 UUID (version 8, the custom-hash form) from parts, so the same input gives the
// same UUID every time.
//
// A urn:uuid: must hold a real UUID: lowercase hex in the 8-4-4-4-12 form. Bundles used to put resource ids there
// ("urn:uuid:P68d45d5e1d881a5f"), which the HL7 validator rejects as an error on every entry and a strict server may
// refuse. It was not caught because the earlier validator runs checked the resources taken out of the bundle, not the
// bundle itself.
func DeterministicUUID(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x80 // version 8
	b[8] = (b[8] & 0x3f) | 0x80 // RFC 9562 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
