package fhir

import (
	"encoding/json"

	"strings"
	"testing"
)

// TestEveryRegisteredTypeSurvivesARoundTrip walks resourceConstructors rather than a list written by hand.
//
// # Why this test has to iterate the registry
//
// resourceConstructors is deliberately the single source of truth: it drives the router and the capability statement from one
// fact, which was itself a fix for those two having drifted apart. That solved the worst version of the problem - advertising
// a type the router then 404s - but it created a quieter one. Adding a line to the table makes a type *advertised* instantly.
// It does not make it work.
//
// So 126 types were reachable and named in a document clients cache, and nothing checked as a set that any of them could
// actually be read back. A type whose struct has a bad json tag, or a name that does not match its ResourceTypeName, would be
// promised in the metadata and fail on first use - which is the same shape as the R5/R4 defect this file's sibling closed:
// the server describing itself more confidently than it can behave.
//
// A hand-written list of types to check would drift from the registry, and drift is the thing being guarded against.
func TestEveryRegisteredTypeSurvivesARoundTrip(t *testing.T) {
	if len(resourceConstructors) == 0 {
		t.Fatal("resourceConstructors is empty, so this test is checking nothing")
	}

	for name, construct := range resourceConstructors {
		t.Run(name, func(t *testing.T) {
			r := construct()
			if r == nil {
				t.Fatalf("constructor for %q returned nil", name)
			}

			// The registry key has to match what the type says it is, or a resource stored under one name reads back as
			// another. Marshal writes resourceType from the method, and UnmarshalResource dispatches on the key.
			if got := r.ResourceTypeName(); got != name {
				t.Fatalf("registered as %q but ResourceTypeName() is %q: a resource written under one name would be looked up under the other", name, got)
			}

			raw, err := Marshal(r, R4)
			if err != nil {
				t.Fatalf("Marshal: %v", err)
			}

			// resourceType is what every FHIR consumer dispatches on, including this package.
			var probe struct {
				ResourceType string `json:"resourceType"`
			}
			if err := json.Unmarshal(raw, &probe); err != nil {
				t.Fatalf("the marshalled form is not valid JSON: %v", err)
			}
			if probe.ResourceType != name {
				t.Fatalf("marshalled resourceType is %q, want %q", probe.ResourceType, name)
			}

			back, err := UnmarshalResource(raw)
			if err != nil {
				t.Fatalf("UnmarshalResource on this package's own output: %v", err)
			}
			if got := back.ResourceTypeName(); got != name {
				t.Fatalf("round trip changed the type from %q to %q", name, got)
			}
		})
	}
}

// TestTheCapabilityStatementListMatchesTheRegistry checks the two answers cannot diverge again.
//
// The comment on resourceConstructors records that a switch and a slice had drifted, so six types were advertised and then
// 404ed. This asserts the property that fix was for, rather than trusting that one table stays the only source.
func TestTheCapabilityStatementListMatchesTheRegistry(t *testing.T) {
	advertised := SupportedResourceTypes()
	if len(advertised) != len(resourceConstructors) {
		t.Fatalf("SupportedResourceTypes reports %d types, registry has %d", len(advertised), len(resourceConstructors))
	}

	for _, name := range advertised {
		if _, ok := resourceConstructors[name]; !ok {
			t.Errorf("%s is advertised but has no constructor, so the router will refuse it", name)
		}
	}

	// Sorted, because this list reaches a document clients cache and diff. Unsorted output would appear to change on every
	// request even though nothing had.
	for i := 1; i < len(advertised); i++ {
		if advertised[i-1] > advertised[i] {
			t.Fatalf("not sorted at %d: %q before %q", i, advertised[i-1], advertised[i])
		}
	}
}

// TestTheClinicallyEssentialTypesArePresent is the one deliberately hand-written list here.
//
// Counting types answers the wrong question. 126 of roughly 145 sounds like near-complete coverage and would still be useless
// if the missing nineteen were Patient, Observation and MedicationRequest. What matters is whether the resources a hospital
// integration actually carries are present, so those are named.
//
// The list is US Core's clinical core plus the administrative types a real feed cannot work without. It is allowed to grow. If
// a name here ever has to be removed to make this pass, that is a decision to argue about, not a test to edit.
//
// # ValueSet and ConceptMap are deliberately not here
//
// Writing this test, I put them in the list and it failed. They are essential, and they are not in the registry - which looked
// like the terminology work having been marked done while the resources it operates on could not be read.
//
// It is not. Both are *projected* from the channel mapping tables rather than stored, so they have operation routes and no
// storage, and the capability statement says so: an empty interaction array with a comment that claiming read would be a
// promise answered by a 404. That is the honest arrangement and the test was wrong, not the server.
//
// TestTheProjectedTerminologyTypesDoNotClaimStorage below guards the property that makes it honest.
func TestTheClinicallyEssentialTypesArePresent(t *testing.T) {
	essential := []string{
		// US Core clinical
		"AllergyIntolerance", "CarePlan", "CareTeam", "Condition", "Device", "DiagnosticReport",
		"DocumentReference", "Encounter", "Goal", "Immunization", "Location", "Medication",
		"MedicationDispense", "MedicationRequest", "Observation", "Organization", "Patient",
		"Practitioner", "PractitionerRole", "Procedure", "Provenance", "RelatedPerson",
		"ServiceRequest", "Specimen",
		// Administrative and infrastructure a real feed needs
		"Appointment", "Bundle", "CapabilityStatement", "Coverage", "OperationOutcome",
		"Schedule", "Slot", "Subscription", "Task",
		// CodeSystem is stored. ValueSet and ConceptMap are projected from mapping tables - see the doc comment.
		"CodeSystem",
	}

	var missing []string
	for _, name := range essential {
		if _, ok := resourceConstructors[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("%d clinically essential resource type(s) are not served: %s", len(missing), strings.Join(missing, ", "))
	}

	t.Logf("%d resource types stored in total; all %d essential ones present", len(resourceConstructors), len(essential))
}

// TestTheProjectedTerminologyTypesAreNotStored records the arrangement, so that changing it is a decision.
//
// ValueSet and ConceptMap are views of the channel mapping tables. If somebody later adds either to resourceConstructors -
// which is the obvious thing to do on noticing they are absent - they become storable, and then there are two sources for the
// same resource: the files a channel maps against, and whatever a client POSTed. A $translate answered from one while a read
// returns the other is a hard bug to see, because both answers look reasonable in isolation.
//
// This does not forbid making them storable. It forbids doing so by accident, which is how it would happen.
func TestTheProjectedTerminologyTypesAreNotStored(t *testing.T) {
	for _, name := range []string{"ValueSet", "ConceptMap"} {
		if _, ok := resourceConstructors[name]; ok {
			t.Errorf("%s is now in the resource registry. It is served as a projection of the mapping tables, so storing it too creates two sources for one resource - a $translate answered from the tables while a read returns a POSTed copy. If this is intended, the capability statement's empty interaction array has to change with it.", name)
		}
	}
}
