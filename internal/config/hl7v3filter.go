package config

import (
	"github.com/biodream-llc/perfuse/internal/hl7v3"
)

// validateV3Filter checks an HL7 v3 filter expression at load time.
//
// A thin wrapper, and it exists rather than the caller reaching for the parser directly so that the import lives in one place. The
// direction is safe - hl7v3 knows nothing about configuration - but a package that validates channel files gaining scattered
// dependencies on the packages that implement them is how a cycle eventually arrives.
//
// The point of parsing here at all: a bad path or an uncompilable pattern stops the channel loading. Left to the engine, it would fail
// on every message of a channel that was already running and already trusted, which is the difference between a configuration mistake
// and an outage.
func validateV3Filter(expr string) error {
	_, err := hl7v3.ParseFilter(expr)

	return err
}
