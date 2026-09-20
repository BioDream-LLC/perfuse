package alerts

import "testing"

// TestCatalogueCoversEveryEvaluatedKind pairs the catalogue against the evaluator table in both directions.
//
// The catalogue is what an editor reads to decide what to offer and how to label it, so a kind missing from here is a kind nobody can
// create from the interface - the same defect as being missing from KnownKinds, arriving by a different route. And an entry here for a
// kind that is not evaluated offers somebody a rule that will load and never fire, which is worse: they will believe they are being
// watched.
func TestCatalogueCoversEveryEvaluatedKind(t *testing.T) {
	described := map[Kind]bool{}
	for _, info := range KindCatalogue() {
		described[info.Kind] = true
	}

	for kind := range ruleFuncs {
		if !described[kind] {
			t.Errorf("%s can be evaluated and is not in KindCatalogue, so no editor can offer it", kind)
		}
	}

	for _, info := range KindCatalogue() {
		if _, ok := ruleFuncs[info.Kind]; !ok {
			t.Errorf("%s is described in KindCatalogue and has no evaluator, so a rule using it would load and never fire", info.Kind)
		}
	}
}

// TestCatalogueEntriesAreUsable checks each entry says enough to choose a threshold by.
//
// A blank label or an empty help string passes any structural test and leaves an operator guessing at a number that decides whether
// they get woken up. The unit check is the important one: a share and a count are different questions, and the whole reason this
// catalogue exists is that the threshold field does not say which it is.
func TestCatalogueEntriesAreUsable(t *testing.T) {
	units := map[Unit]bool{UnitShare: true, UnitMessages: true, UnitSeconds: true, UnitAttempts: true, UnitNone: true}

	for _, info := range KindCatalogue() {
		t.Run(string(info.Kind), func(t *testing.T) {
			if info.Label == "" {
				t.Error("no label, so a list of rules would show a bare identifier")
			}
			if info.Summary == "" {
				t.Error("no summary, so nothing explains what the rule watches")
			}
			if info.Detail == "" {
				t.Error("no detail, so nothing explains how to choose a threshold")
			}
			if !units[info.Unit] {
				t.Errorf("unit %q is not one of the known units", info.Unit)
			}
			if info.Direction != Above && info.Direction != Below {
				t.Errorf("direction %q is neither above nor below", info.Direction)
			}

			// A share outside nought to one is the mistake this catalogue exists to prevent, so the defaults must not make it.
			if info.Unit == UnitShare && (info.Default <= 0 || info.Default > 1) {
				t.Errorf("default %v is not a share between 0 and 1, which is the very confusion this is meant to prevent", info.Default)
			}
			if info.Unit != UnitNone && info.Default <= 0 {
				t.Errorf("default %v gives an editor nothing sensible to start from", info.Default)
			}

			// The default has to be a rule the loader accepts. A suggested value that fails validation is a form that refuses its
			// own starting point.
			if err := (Rule{Kind: info.Kind, Threshold: info.Default}).Validate(); err != nil {
				t.Errorf("the suggested default does not validate: %v", err)
			}
		})
	}
}

// TestSilenceRulesFireBelow checks the two rules about absence are described as firing below their threshold.
//
// Getting this backwards in the catalogue would put the words "fires above" next to a rule that fires below, which states the opposite
// of what the code does. Somebody tuning a quiet-feed alert would move the number the wrong way and conclude the feature is broken.
func TestSilenceRulesFireBelow(t *testing.T) {
	for _, kind := range []Kind{KindNoTraffic, KindBelowRhythm} {
		info, ok := InfoFor(kind)
		if !ok {
			t.Fatalf("%s is not in the catalogue", kind)
		}
		if info.Direction != Below {
			t.Errorf("%s is about silence and is described as firing %s", kind, info.Direction)
		}
	}
}
