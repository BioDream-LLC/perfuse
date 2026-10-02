package fhirserver

import (
	"context"
	"testing"
)

// modifierFixture stores patients whose names differ only in ways the modifiers are supposed to distinguish.
func modifierFixture(t *testing.T) *Store {
	t.Helper()

	st := newTestStore(t)

	// Case differs, which is what :exact is for.
	put(t, st, `{"resourceType":"Patient","id":"p1","name":[{"family":"Dubois","given":["Marie"]}],
		"birthDate":"1963-04-11"}`)
	put(t, st, `{"resourceType":"Patient","id":"p2","name":[{"family":"DUBOIS","given":["Jean"]}],
		"birthDate":"1971-02-02"}`)
	// A longer name with the target as a prefix, which ordinary string search matches and :exact must not.
	put(t, st, `{"resourceType":"Patient","id":"p3","name":[{"family":"Duboisville"}],
		"birthDate":"1980-01-01"}`)
	// The target appears in the middle, which only :contains finds.
	put(t, st, `{"resourceType":"Patient","id":"p4","name":[{"family":"Vandubois"}],
		"birthDate":"1990-05-05"}`)
	// No birth date at all, which is what :missing is for.
	put(t, st, `{"resourceType":"Patient","id":"p5","name":[{"family":"Okafor"}]}`)

	return st
}

// ids runs a search and returns the matching resource ids.
func ids(t *testing.T, st *Store, q *SearchQuery) []string {
	t.Helper()

	q.Count = 50

	res, err := st.Search(context.Background(), q)
	if err != nil {
		t.Fatalf("search failed: %v", err)
	}

	out := make([]string, 0, len(res.Resources))
	for _, r := range res.Resources {
		out = append(out, r.ResourceID())
	}

	return out
}

// has reports whether id is in the result.
func has(got []string, id string) bool {
	for _, g := range got {
		if g == id {
			return true
		}
	}

	return false
}

// TestExactIsCaseSensitiveAndWholeValue is the reason value_raw exists.
//
// Without the unfolded value, :exact could only ever be case-insensitive - which is exactly the behaviour the modifier exists to
// switch off. A client asking for Dubois and receiving DUBOIS has been handed records it deliberately excluded, with a 200 and no
// sign the modifier was ignored.
func TestExactIsCaseSensitiveAndWholeValue(t *testing.T) {
	st := modifierFixture(t)

	got := ids(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{},
		Modified: []ModifiedCriterion{
			{Param: "family", Modifier: ModExact, Values: []string{"Dubois"}},
		},
	})

	if !has(got, "p1") {
		t.Errorf("the exactly matching patient was not found: %v", got)
	}
	if has(got, "p2") {
		t.Error("DUBOIS matched a case-sensitive search for Dubois, so :exact is not case-sensitive")
	}
	if has(got, "p3") {
		t.Error("Duboisville matched :exact, so it is still matching prefixes")
	}
	if len(got) != 1 {
		t.Errorf("expected exactly one match, got %v", got)
	}
}

// TestPlainStringSearchStillMatchesPrefixesAndFolds guards the ordinary path.
//
// Adding :exact must not change what an unmodified search means. Folding moved from six call sites into the add helper as part of
// this work, which is precisely the sort of change that quietly alters the common case.
func TestPlainStringSearchStillMatchesPrefixesAndFolds(t *testing.T) {
	st := modifierFixture(t)

	got := ids(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{"family": {"dubois"}},
	})

	for _, want := range []string{"p1", "p2", "p3"} {
		if !has(got, want) {
			t.Errorf("plain search for family=dubois did not match %s: %v", want, got)
		}
	}
	if has(got, "p4") {
		t.Error("plain string search matched Vandubois, so it is no longer a prefix search")
	}
}

// TestContainsFindsTheValueAnywhere covers the modifier a prefix search cannot express.
func TestContainsFindsTheValueAnywhere(t *testing.T) {
	st := modifierFixture(t)

	got := ids(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{},
		Modified: []ModifiedCriterion{
			{Param: "family", Modifier: ModContains, Values: []string{"DUBOIS"}},
		},
	})

	// Case-insensitive, which is what FHIR specifies for :contains - so an uppercase query finds all four.
	for _, want := range []string{"p1", "p2", "p3", "p4"} {
		if !has(got, want) {
			t.Errorf(":contains did not match %s: %v", want, got)
		}
	}
	if has(got, "p5") {
		t.Error(":contains matched a patient whose name does not contain the value")
	}
}

// TestMissingSelectsResourcesWithNoValue covers the query that cannot be written any other way.
//
// A client looking for patients with no recorded birth date cannot express that as a value comparison. Both directions are
// checked, because :missing=false must select the complement rather than everything.
func TestMissingSelectsResourcesWithNoValue(t *testing.T) {
	st := modifierFixture(t)

	absent := ids(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{},
		Modified: []ModifiedCriterion{
			{Param: "birthdate", Modifier: ModMissing, Values: []string{"true"}},
		},
	})

	if !has(absent, "p5") {
		t.Errorf("birthdate:missing=true did not find the patient with no birth date: %v", absent)
	}
	if len(absent) != 1 {
		t.Errorf("birthdate:missing=true returned %v, want only the patient with no birth date", absent)
	}

	present := ids(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{},
		Modified: []ModifiedCriterion{
			{Param: "birthdate", Modifier: ModMissing, Values: []string{"false"}},
		},
	})

	if has(present, "p5") {
		t.Error("birthdate:missing=false included the patient with no birth date")
	}
	if len(present) != 4 {
		t.Errorf("birthdate:missing=false returned %d patients, want 4: %v", len(present), present)
	}
}

// TestAModifierAndAPlainCriterionBothApply covers the interaction.
//
// A parameter can appear both plainly and with a modifier in one query. Modified criteria are appended to the where clause
// separately for this reason - folding them into Criteria would let one overwrite the other and quietly widen the search.
func TestAModifierAndAPlainCriterionBothApply(t *testing.T) {
	st := modifierFixture(t)

	got := ids(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{"family": {"dubois"}},
		Modified: []ModifiedCriterion{
			{Param: "birthdate", Modifier: ModMissing, Values: []string{"false"}},
			{Param: "family", Modifier: ModExact, Values: []string{"Dubois"}},
		},
	})

	if len(got) != 1 || got[0] != "p1" {
		t.Errorf("the combination returned %v, want only p1 - all three conditions must apply", got)
	}
}

// TestAWildcardInAValueIsNotAWildcard covers the injection-shaped mistake.
//
// A LIKE pattern built from user input treats % and _ as wildcards. In a surname that is rare; in an identifier or a device name
// it is not, and the failure is silent - it returns more records than were asked for.
func TestAWildcardInAValueIsNotAWildcard(t *testing.T) {
	st := newTestStore(t)

	put(t, st, `{"resourceType":"Patient","id":"w1","name":[{"family":"Smith"}]}`)
	put(t, st, `{"resourceType":"Patient","id":"w2","name":[{"family":"S%h"}]}`)

	got := ids(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{},
		Modified: []ModifiedCriterion{
			{Param: "family", Modifier: ModContains, Values: []string{"S%h"}},
		},
	})

	if has(got, "w1") {
		t.Error("the % in the search value acted as a wildcard and matched Smith")
	}
	if !has(got, "w2") {
		t.Errorf("the literal value S%%h was not found: %v", got)
	}
}

// TestAnUnimplementedModifierIsRefusedByName covers the refusal.
func TestAnUnimplementedModifierIsRefusedByName(t *testing.T) {
	_, _, err := parseModifier("Patient", "family:phonetic")
	if err == nil {
		t.Fatal(":phonetic was accepted")
	}

	// The message has to name what is available, or the client has been told no with no way forward.
	for _, want := range []string{":exact", ":contains", ":missing"} {
		if !contains(err.Error(), want) {
			t.Errorf("the refusal does not mention %s: %v", want, err)
		}
	}
}

// TestExactOnAReferenceIsRefused covers applying a string modifier to something that is not a string.
//
// Silently doing nothing would return every resource of the type. Silently doing a string comparison on a reference would return
// nothing. Both are wrong answers with a 200.
func TestExactOnAReferenceIsRefused(t *testing.T) {
	if _, _, err := parseModifier("Observation", "subject:exact"); err == nil {
		t.Error("subject:exact was accepted on a reference parameter")
	}
	if _, _, err := parseModifier("Patient", "birthdate:contains"); err == nil {
		t.Error("birthdate:contains was accepted on a date parameter")
	}
}

// TestMissingRefusesAValueThatIsNotTrueOrFalse covers reading an unknown value as false.
//
// "missing=yes" read as "missing=false" inverts the query and returns the opposite set of records, which is the worst available
// outcome for a typo.
func TestMissingRefusesAValueThatIsNotTrueOrFalse(t *testing.T) {
	if _, err := missingWanted("yes"); err == nil {
		t.Error(":missing accepted \"yes\"")
	}
	if _, err := missingWanted(""); err == nil {
		t.Error(":missing accepted an empty value")
	}

	for _, ok := range []string{"true", "TRUE", " false "} {
		if _, err := missingWanted(ok); err != nil {
			t.Errorf(":missing rejected %q: %v", ok, err)
		}
	}
}

// TestAnAccentedNameIsFoundRegardlessOfCase is why the folding at index time is not redundant.
//
// A plant that removed the fold broke nothing, because SQLite's LIKE is case-insensitive for ASCII by default - so "dubois"
// matched "Dubois" whether or not anything had been folded, and every test I had written passed either way. The guard was inert.
//
// It is case-insensitive *for ASCII only*. É is not ASCII, so without folding at index time a search for "évrard" does not match a
// patient recorded as "Évrard". Accented names are not an edge case in a hospital; they are Tuesday. This is the test that makes
// the fold provable, and it was found by planting rather than by reasoning.
func TestAnAccentedNameIsFoundRegardlessOfCase(t *testing.T) {
	st := newTestStore(t)

	put(t, st, `{"resourceType":"Patient","id":"e1","name":[{"family":"Évrard","given":["Chloé"]}]}`)
	put(t, st, `{"resourceType":"Patient","id":"e2","name":[{"family":"MÜLLER"}]}`)

	// Lowercase query, capitalised record.
	got := ids(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{"family": {"évrard"}},
	})
	if !has(got, "e1") {
		t.Errorf("a lowercase search did not match the accented name Évrard: %v. "+
			"SQLite LIKE folds ASCII only, so this depends on the value being folded when it is indexed.", got)
	}

	// And the other direction, where the record is uppercase and non-ASCII.
	got = ids(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{"family": {"müller"}},
	})
	if !has(got, "e2") {
		t.Errorf("a lowercase search did not match MÜLLER: %v", got)
	}

	// A given name too, since given and family are indexed through different call sites and the fold used to be written out
	// separately at each one.
	got = ids(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{"given": {"chloé"}},
	})
	if !has(got, "e1") {
		t.Errorf("a lowercase search did not match the given name Chloé: %v", got)
	}
}

// TestExactStillDistinguishesCaseOnAnAccentedName pairs with the test above.
//
// The fold must not reach value_raw, or :exact on an accented name would silently become case-insensitive - the same defect as
// before, just harder to notice.
func TestExactStillDistinguishesCaseOnAnAccentedName(t *testing.T) {
	st := newTestStore(t)

	put(t, st, `{"resourceType":"Patient","id":"x1","name":[{"family":"Évrard"}]}`)
	put(t, st, `{"resourceType":"Patient","id":"x2","name":[{"family":"ÉVRARD"}]}`)

	got := ids(t, st, &SearchQuery{
		ResourceType: "Patient",
		Criteria:     map[string][]string{},
		Modified: []ModifiedCriterion{
			{Param: "family", Modifier: ModExact, Values: []string{"Évrard"}},
		},
	})

	if len(got) != 1 || got[0] != "x1" {
		t.Errorf(":exact on an accented name returned %v, want only x1 - "+
			"the fold has reached value_raw and case sensitivity is gone", got)
	}
}

// contains is a local helper so this file does not depend on the import order of strings elsewhere.
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && indexOf(haystack, needle) >= 0
}

func indexOf(haystack, needle string) int {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return i
		}
	}

	return -1
}
