package config

import "testing"

// The schema enumeration's guardrails.
//
// # What this file is and is not
//
// AllSettings walks Channel by reflection and returns every leaf setting. It was built as the denominator for an audit that
// checked whether each setting had a reader outside this package, and that check was withdrawn: it flagged eleven settings of
// which ten were false positives, because reading a field through an accessor defined next to it is this codebase's dominant
// convention rather than an exception. A check at nine percent precision gets ignored, and an ignored red test is worse than none.
//
// What survived is the enumeration itself, which is worth keeping for a duller reason: it is the only way to know how large the
// configuration surface is. That number is what makes it possible to say a coverage claim is partial, and before this it was not
// known - 398 unique fields, reached by 608 yaml paths.
//
// So these tests check the walk works, not that the settings are wired. The wiring question is answered per feature by
// featureSupport, which is a much smaller surface and a much stronger check.

// TestTheSchemaWalkReachesTheWholeChannel guards against the walk terminating early.
//
// A reflection walk that stops short does not fail, it just returns less - and every number derived from it silently shrinks with
// it. So the floor is checked against the quantity the walk actually returns.
func TestTheSchemaWalkReachesTheWholeChannel(t *testing.T) {
	settings := AllSettings()

	// AllSettings dedupes by owner and field, so this counts unique struct fields - 398 when written - not yaml paths, of which
	// there were 608. The two differ because one struct reached by several paths is one setting to audit but several places to
	// write it. A floor rather than equality: gaining settings is normal, losing a hundred means the walk broke.
	const floor = 390

	if len(settings) < floor {
		t.Fatalf("the schema walk found %d settings and there were 398 when this was written; a drop this large means the "+
			"walk is terminating early, and every number derived from it shrinks silently with it", len(settings))
	}
}

// TestTheSchemaWalkReachesNestedDepths spot-checks settings at varying depth.
//
// The floor above would still pass if the walk covered the shallow half of the schema twice over, so these name specific fields
// that only exist several levels down.
func TestTheSchemaWalkReachesNestedDepths(t *testing.T) {
	settings := AllSettings()

	seen := make(map[string]bool, len(settings))
	for _, s := range settings {
		seen[s.Owner+"."+s.Field] = true
	}

	// Owner and field rather than yaml path, because that is what the walk dedupes by and therefore what it can promise. An
	// earlier version of this test named yaml paths and failed on destinations.tcp.framing - not because the walk missed the
	// field but because one struct reached by several paths has no single path to name.
	for _, want := range []string{
		"Channel.Name",             // depth 1
		"HTTPSource.Listen",        // inside source
		"TCPFraming.Framing",       // inside a destination's framing block
		"FHIRDestination.URL",      // inside a destination
		"BrokerSource.Reconnect",   // the field behind ResolvedReconnect
		"QueueConfig.RetainHours",  // the field behind Retention
		"DICOMQuerySource.Overlap", // the field behind ResolvedOverlap
	} {
		if !seen[want] {
			t.Errorf("the walk did not reach %s, so it is not covering that part of the schema", want)
		}
	}
}

// TestTheSchemaWalkDedupesByOwnerAndField documents why the count is what it is.
//
// Without the dedupe the same struct reached by two paths counts twice, and the total stops meaning "settings to reason about"
// and starts meaning "places to write one". Both numbers are useful; conflating them is what broke the floor above.
func TestTheSchemaWalkDedupesByOwnerAndField(t *testing.T) {
	settings := AllSettings()

	seen := make(map[string]int, len(settings))
	for _, s := range settings {
		seen[s.Owner+"."+s.Field]++
	}

	for key, n := range seen {
		if n > 1 {
			t.Errorf("%s appears %d times; AllSettings is meant to dedupe by owner and field", key, n)
		}
	}
}
