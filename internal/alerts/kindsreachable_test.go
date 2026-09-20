package alerts

import "testing"

// TestEveryEvaluatedKindIsConfigurable pairs the evaluator table against the list of kinds a rules file may use.
//
// These two facts are written in different files and nothing connected them. A kind can therefore be fully implemented, have an
// evaluator registered and a suite of tests passing, and still be impossible to configure - because Validate rejects any kind absent
// from KnownKinds, and the rejection happens when the file loads rather than when the code is written.
//
// That is exactly what had happened to below-rhythm. It is the only rule that can report a feed going quiet when that feed is
// legitimately silent most of the week, which is the case of a stopped lab feed - a patient-safety problem that raises no error
// anywhere else. It had an evaluator, seven tests, and a long comment explaining why it mattered, and any attempt to use it was
// refused with "below-rhythm is not an alert kind".
//
// Both directions are checked. A kind that is offered but never evaluated is the worse of the two failures: the file loads, nothing
// complains, and the condition somebody was worried about goes unwatched.
func TestEveryEvaluatedKindIsConfigurable(t *testing.T) {
	for kind := range ruleFuncs {
		if err := (Rule{Kind: kind, Threshold: 1}).Validate(); err != nil {
			t.Errorf("%s has an evaluator but cannot be configured: %v\n\nAdd it to KnownKinds. A rule that cannot be named in a rules file is a rule nobody can use.", kind, err)
		}
	}
}

// TestEveryConfigurableKindIsEvaluated is the other direction, and the more dangerous one.
func TestEveryConfigurableKindIsEvaluated(t *testing.T) {
	for _, kind := range KnownKinds {
		if _, ok := ruleFuncs[kind]; !ok {
			t.Errorf("%s may be written in a rules file and has no evaluator, so a rule using it loads without complaint and never fires", kind)
		}
	}
}
