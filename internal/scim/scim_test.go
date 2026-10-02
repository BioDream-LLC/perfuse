package scim

import (
	"encoding/json"
	"errors"
	"testing"
)

// TestTheLookupProvidersActuallySend is the filter that matters most.
//
// Every identity provider performs this before creating an account: does one already exist with this username. If it comes
// back empty when the account does exist, the provider creates a second, disables the one it just made, and leaves the
// original enabled. A broken filter is a deprovisioning failure wearing a provisioning costume.
func TestTheLookupProvidersActuallySend(t *testing.T) {
	f, err := ParseFilter(`userName eq "rturner@example.org"`)
	if err != nil {
		t.Fatal(err)
	}

	name, ok := f.UserNameEquals()
	if !ok {
		t.Fatal("the commonest filter in the protocol was not recognised as a username lookup")
	}
	if name != "rturner@example.org" {
		t.Errorf("looking for %q", name)
	}

	// The casing of the attribute varies between providers and between versions of the same provider.
	for _, spelling := range []string{
		`userName eq "rturner@example.org"`,
		`username eq "rturner@example.org"`,
		`UserName eq "rturner@example.org"`,
		`USERNAME EQ "rturner@example.org"`,
	} {
		f, err := ParseFilter(spelling)
		if err != nil {
			t.Errorf("%s: %v", spelling, err)

			continue
		}
		if _, ok := f.UserNameEquals(); !ok {
			t.Errorf("%s was not recognised as a username lookup", spelling)
		}
	}
}

// TestAUsernameContainingTheWordAndIsNotSplit covers a real trap.
//
// "brandon@example.org" contains "and". Splitting on the conjunction without regard for quotes turns one comparison into two
// nonsensical ones, the lookup returns nothing, and the provider creates a duplicate account.
func TestAUsernameContainingTheWordAndIsNotSplit(t *testing.T) {
	for _, name := range []string{
		"brandon@example.org",
		"alexander@example.org", // contains "and"
		"corduroy or bust",      // contains " or "
		"sandy and co",          // contains " and "
	} {
		f, err := ParseFilter(`userName eq "` + name + `"`)
		if err != nil {
			t.Errorf("%q was refused: %v", name, err)

			continue
		}
		if len(f.Comparisons) != 1 {
			t.Errorf("%q was split into %d comparisons", name, len(f.Comparisons))

			continue
		}
		got, ok := f.UserNameEquals()
		if !ok || got != name {
			t.Errorf("%q came back as %q (recognised: %v)", name, got, ok)
		}
	}
}

// TestEqualityIsCaseInsensitive covers the duplicate-account hazard from the other direction.
//
// SCIM says usernames are case-insensitive. A provider sending "RTurner" for an account stored as "rturner" that finds
// nothing creates a second account.
func TestEqualityIsCaseInsensitive(t *testing.T) {
	f, err := ParseFilter(`userName eq "RTurner@Example.ORG"`)
	if err != nil {
		t.Fatal(err)
	}

	if !f.Matches(map[string]string{"username": "rturner@example.org"}) {
		t.Error("a username differing only in case did not match, which produces a duplicate account")
	}
	if f.Matches(map[string]string{"username": "someone.else@example.org"}) {
		t.Error("a different username matched")
	}
}

// TestAnUnsupportedFilterIsRefusedRatherThanGuessed is the design of this file.
//
// A misparsed filter returns the wrong users, and returning the wrong users to a provisioning system means it acts on the
// wrong accounts - disabling somebody who is still employed, or leaving somebody who is not. Refusing gives the provider a
// 400 with invalidFilter, which it understands and logs.
func TestAnUnsupportedFilterIsRefusedRatherThanGuessed(t *testing.T) {
	unsupported := []string{
		`(userName eq "a") or (userName eq "b")`,          // grouping changes precedence
		`emails[type eq "work"].value eq "a@example.org"`, // complex attribute
		`not (userName eq "a")`,                           // negation
		`userName eq "a" and active eq true or id pr`,     // three conditions
		`meta.lastModified gt "2026-01-01T00:00:00Z"`,     // ordering
		`userName xx "a"`,                                 // unknown operator
		`userName`,                                        // not a comparison
	}

	for _, expression := range unsupported {
		_, err := ParseFilter(expression)
		if err == nil {
			t.Errorf("%s was accepted; a filter this does not understand must be refused rather than guessed at",
				expression)

			continue
		}

		// It has to be the typed error, so the handler can map it to invalidFilter rather than a generic 500. A provider
		// seeing 500 retries for ever; one seeing invalidFilter logs it and stops.
		var unsupportedErr ErrUnsupportedFilter
		if !errors.As(err, &unsupportedErr) {
			t.Errorf("%s gave %T rather than ErrUnsupportedFilter", expression, err)
		}
		if unsupportedErr.Why == "" {
			t.Errorf("%s was refused with no explanation, so a provisioning log says nothing useful", expression)
		}
	}
}

// TestTheOperatorsProvidersUseWork covers the supported subset.
func TestTheOperatorsProvidersUseWork(t *testing.T) {
	attributes := map[string]string{
		"username":   "rturner@example.org",
		"externalid": "okta-00u1abcd",
		"active":     "true",
	}

	cases := map[string]bool{
		`userName eq "rturner@example.org"`: true,
		`userName ne "somebody@else.org"`:   true,
		`userName co "turner"`:              true,
		`userName sw "rturner"`:             true,
		`userName ew "example.org"`:         true,
		`userName pr`:                       true,
		`externalId eq "okta-00u1abcd"`:     true,
		`userName eq "wrong@example.org"`:   false,
		`userName sw "zzz"`:                 false,
		`nickname pr`:                       false,
	}

	for expression, want := range cases {
		f, err := ParseFilter(expression)
		if err != nil {
			t.Errorf("%s: %v", expression, err)

			continue
		}
		if got := f.Matches(attributes); got != want {
			t.Errorf("%s matched %v, expected %v", expression, got, want)
		}
	}

	// Two conditions joined, both directions.
	and, err := ParseFilter(`userName eq "rturner@example.org" and active eq "true"`)
	if err != nil {
		t.Fatal(err)
	}
	if !and.Matches(attributes) {
		t.Error("an and of two true conditions did not match")
	}

	or, err := ParseFilter(`userName eq "nobody@example.org" or externalId eq "okta-00u1abcd"`)
	if err != nil {
		t.Fatal(err)
	}
	if !or.Matches(attributes) {
		t.Error("an or with one true condition did not match")
	}

	// An and where only one holds must not match, which is the case a lazy implementation gets wrong.
	partial, err := ParseFilter(`userName eq "rturner@example.org" and externalId eq "wrong"`)
	if err != nil {
		t.Fatal(err)
	}
	if partial.Matches(attributes) {
		t.Error("an and with one false condition matched, so it is behaving as an or")
	}
}

// TestAnEmptyFilterMatchesEverything covers listing.
//
// A provider listing all users sends no filter, and that has to mean everything rather than nothing - a reconciliation run
// that sees no users concludes every account has been deleted upstream.
func TestAnEmptyFilterMatchesEverything(t *testing.T) {
	for _, expression := range []string{"", "   "} {
		f, err := ParseFilter(expression)
		if err != nil {
			t.Fatalf("an empty filter was refused: %v", err)
		}
		if !f.Matches(map[string]string{"username": "anybody"}) {
			t.Error("an empty filter matched nothing; a reconciliation run would conclude every account was deleted")
		}
	}
}

// TestDisablingAnAccountIsRecognisedInEveryShapeProvidersSend is the most security-relevant test here.
//
// PATCH is how an identity provider disables somebody. Okta sends a single replace of active rather than a PUT. Providers
// disagree about whether the value is a JSON boolean, a quoted word, or a wrapped array, and the capitalisation of the quoted
// word varies.
//
// A parser that only accepts the JSON boolean treats the others as unparseable. If an unparseable disable request is answered
// with a 200 - which is what a permissive handler does - the provider records the deprovisioning as complete and never tries
// again. That is a terminated employee with working access to a clinical integration engine.
func TestDisablingAnAccountIsRecognisedInEveryShapeProvidersSend(t *testing.T) {
	disables := map[string]string{
		"JSON boolean":          `{"op":"replace","path":"active","value":false}`,
		"quoted lower case":     `{"op":"replace","path":"active","value":"false"}`,
		"quoted capitalised":    `{"op":"replace","path":"active","value":"False"}`,
		"quoted upper case":     `{"op":"replace","path":"active","value":"FALSE"}`,
		"capitalised operation": `{"op":"Replace","path":"active","value":false}`,
		"pathless object":       `{"op":"replace","value":{"active":false}}`,
		"pathless capitalised":  `{"op":"replace","value":{"Active":false}}`,
		"wrapped array":         `{"op":"replace","path":"active","value":[{"value":false}]}`,
		"capitalised attribute": `{"op":"replace","path":"Active","value":false}`,
	}

	for name, body := range disables {
		var op PatchOperation
		if err := json.Unmarshal([]byte(body), &op); err != nil {
			t.Fatalf("%s: %v", name, err)
		}

		active, isChange := op.ActiveChange()
		if !isChange {
			t.Errorf("%s was not recognised as a change to active; a disable request in this shape would be "+
				"answered without disabling anybody", name)

			continue
		}
		if active {
			t.Errorf("%s was read as enabling rather than disabling the account", name)
		}
	}
}

// TestEnablingAnAccountIsAlsoRecognised covers the other direction.
//
// Somebody returning from leave is re-enabled, and reading that as a disable locks out an employee who is back at work.
func TestEnablingAnAccountIsAlsoRecognised(t *testing.T) {
	enables := []string{
		`{"op":"replace","path":"active","value":true}`,
		`{"op":"replace","path":"active","value":"true"}`,
		`{"op":"replace","path":"active","value":"True"}`,
		`{"op":"add","path":"active","value":true}`,
		`{"op":"replace","value":{"active":true}}`,
	}

	for _, body := range enables {
		var op PatchOperation
		if err := json.Unmarshal([]byte(body), &op); err != nil {
			t.Fatal(err)
		}

		active, isChange := op.ActiveChange()
		if !isChange {
			t.Errorf("%s was not recognised as a change to active", body)

			continue
		}
		if !active {
			t.Errorf("%s was read as disabling rather than enabling the account", body)
		}
	}
}

// TestSomethingThatIsNotAnActiveChangeIsNotMistakenForOne is the other half.
//
// Reading an unrelated patch as a disable would lock somebody out because their display name changed.
func TestSomethingThatIsNotAnActiveChangeIsNotMistakenForOne(t *testing.T) {
	others := []string{
		`{"op":"replace","path":"displayName","value":"Robert Turner"}`,
		`{"op":"replace","path":"name.givenName","value":"Robert"}`,
		`{"op":"add","path":"emails","value":[{"value":"r@example.org"}]}`,
		`{"op":"remove","path":"active"}`,
		`{"op":"replace","value":{"displayName":"Robert Turner"}}`,
		`{"op":"replace","path":"active","value":"maybe"}`,
	}

	for _, body := range others {
		var op PatchOperation
		if err := json.Unmarshal([]byte(body), &op); err != nil {
			t.Fatal(err)
		}
		if _, isChange := op.ActiveChange(); isChange {
			t.Errorf("%s was read as a change to active, which would lock somebody out for changing their name", body)
		}
	}

	// A remove of active is a removal, and the handler has to treat it as its own case rather than as a value change.
	var removal PatchOperation
	_ = json.Unmarshal([]byte(`{"op":"remove","path":"active"}`), &removal)
	if !removal.IsRemoval() {
		t.Error("a remove operation was not recognised as one")
	}
}

// TestAbsentActiveIsNotFalse covers the field's pointer type.
//
// A PUT that omits active must not be read as a request to disable, and one that omits it must not be read as a request to
// enable. Both mistakes are one-line bugs with a person's job attached.
func TestAbsentActiveIsNotFalse(t *testing.T) {
	var user User
	if err := json.Unmarshal([]byte(`{"userName":"rturner","schemas":["`+SchemaUser+`"]}`), &user); err != nil {
		t.Fatal(err)
	}

	if user.Active != nil {
		t.Error("an absent active field came through as a value, so a PUT omitting it would change the account's state")
	}

	// Whereas an explicit false is a value.
	if err := json.Unmarshal([]byte(`{"userName":"rturner","active":false}`), &user); err != nil {
		t.Fatal(err)
	}
	if user.Active == nil {
		t.Fatal("an explicit false came through as absent, so a disable request would be ignored")
	}
	if *user.Active {
		t.Error("an explicit false came through as true")
	}
}

// TestAPasswordIsNeverEchoed covers a leak into somebody else's logs.
//
// Some providers send a password on creation. Echoing it back - even the one just supplied - puts it in the provider's own
// request log, which is a system Perfuse does not control.
func TestAPasswordIsNeverEchoed(t *testing.T) {
	user := User{
		Schemas:  []string{SchemaUser},
		UserName: "rturner",
		Password: "the-password-that-was-sent",
	}

	// Cleared before marshalling is the handler's job; what this asserts is that the field is not required to be present,
	// so clearing it produces valid JSON with no password key at all.
	user.Password = ""

	encoded, err := json.Marshal(user)
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) == "" {
		t.Fatal("nothing was encoded")
	}
	if containsKey(t, encoded, "password") {
		t.Errorf("a password key survived into the response: %s", encoded)
	}
}

// containsKey reports whether a JSON object has a key.
func containsKey(t *testing.T, encoded []byte, key string) bool {
	t.Helper()

	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		t.Fatal(err)
	}
	_, ok := object[key]

	return ok
}

// TestTheErrorShapeIsWhatProvidersExpect covers the response format.
//
// Status is a string in this protocol despite being a number, and the keyword matters: a provider seeing "uniqueness" knows
// the account exists and stops retrying, while one seeing nothing retries a conflict for ever.
func TestTheErrorShapeIsWhatProvidersExpect(t *testing.T) {
	e := NewError(409, ErrUniqueness, "an account with that username already exists")

	encoded, err := json.Marshal(e)
	if err != nil {
		t.Fatal(err)
	}

	var decoded map[string]any
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}

	if status, ok := decoded["status"].(string); !ok || status != "409" {
		t.Errorf("status is %v (%T); the specification says a string", decoded["status"], decoded["status"])
	}
	if decoded["scimType"] != ErrUniqueness {
		t.Errorf("scimType is %v", decoded["scimType"])
	}
	schemas, ok := decoded["schemas"].([]any)
	if !ok || len(schemas) != 1 || schemas[0] != SchemaError {
		t.Errorf("schemas is %v", decoded["schemas"])
	}
}
