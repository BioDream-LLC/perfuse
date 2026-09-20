package tomirth

import (
	"crypto/sha256"
	"fmt"
)

// deterministicUUID derives a channel id from its name.
//
// Mirth keys channels by UUID and refuses an import whose id already exists as a different channel. A random id would mean exporting the
// same channel twice produced two channels in Mirth, which is the wrong behaviour for a tool somebody will run more than once while
// getting a migration right: the second export should update the first.
//
// A hash of the name rather than a random value, formatted as a version 5 style UUID. Not a real namespace UUID - the namespace here is
// Perfuse itself - but the shape Mirth expects and stable for a given name.
func deterministicUUID(name string) string {
	sum := sha256.Sum256([]byte("perfuse-channel:" + name))

	// Version and variant bits, so the result is a well formed UUID rather than sixteen arbitrary bytes that happen to have dashes in.
	b := sum[:16]
	b[6] = (b[6] & 0x0f) | 0x50
	b[8] = (b[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
