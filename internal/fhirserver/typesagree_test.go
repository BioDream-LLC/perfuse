package fhirserver

import (
	"sort"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// TestEveryServedTypeHasSearchParametersAndViceVersa is a drift guard, and it exists because the drift happened.
//
// Two lists decided what this server does. fhir.SupportedResourceTypes gated the router, and fhirserver.SearchParams drove the capability
// statement. Six types were added to the second and not the first, so the server advertised them in its own metadata and then answered 404
// to every request for them - which is the worst combination available, because a client trusts the statement and the failure looks like the
// client's fault.
//
// Neither direction is acceptable, so both are checked. A type served with no search parameters cannot be found by anything except its id,
// and a type with parameters that is not served is a promise the router will not keep.
func TestEveryServedTypeHasSearchParametersAndViceVersa(t *testing.T) {
	// Bundle and OperationOutcome are parseable but never stored, so they are not served and have no parameters. They
	// are named here rather than filtered by a rule, because a rule would quietly excuse a third one added later.
	notServed := map[string]bool{"Bundle": true, "OperationOutcome": true}

	served := map[string]bool{}
	for _, t := range fhir.SupportedResourceTypes() {
		if notServed[t] {
			continue
		}
		served[t] = true
	}

	var missingParams []string
	for name := range served {
		if _, ok := SearchParams[name]; !ok {
			missingParams = append(missingParams, name)
		}
	}
	sort.Strings(missingParams)

	if len(missingParams) > 0 {
		t.Errorf("these types are served but have no search parameters, so nothing can find them except by "+
			"id: %s", strings.Join(missingParams, ", "))
	}

	var notInRouter []string
	for name := range SearchParams {
		if !served[name] {
			notInRouter = append(notInRouter, name)
		}
	}
	sort.Strings(notInRouter)

	if len(notInRouter) > 0 {
		t.Errorf("these types have search parameters and appear in the capability statement, but the router "+
			"does not serve them, so a client that trusts the statement gets a 404: %s",
			strings.Join(notInRouter, ", "))
	}
}

// TestEverySearchableTypeCanBeParsed closes the third way these lists can disagree.
//
// A type in SearchParams that the unmarshaller does not know would accept a search and then fail to read back anything it stored.
func TestEverySearchableTypeCanBeParsed(t *testing.T) {
	known := map[string]bool{}
	for _, name := range fhir.SupportedResourceTypes() {
		known[name] = true
	}

	for name := range SearchParams {
		if !known[name] {
			t.Errorf("%s has search parameters but the parser does not know it", name)
		}
	}
}

// TestEveryReferenceParameterIsAlsoASearchParameter guards the include table against the same drift.
//
// An include names a search parameter. If the include table knows one the search parameter list does not, the capability statement
// advertises an include for a parameter that cannot be searched - and the resolver would look for index rows nothing ever writes.
func TestEveryReferenceParameterIsAlsoASearchParameter(t *testing.T) {
	for resourceType, params := range referenceTargets {
		declared := map[string]bool{}
		for _, p := range SearchParams[resourceType] {
			declared[p] = true
		}

		for param := range params {
			if !declared[param] {
				t.Errorf("%s:%s can be included but %s is not a search parameter of %s, so nothing "+
					"indexes it", resourceType, param, param, resourceType)
			}
		}
	}
}
