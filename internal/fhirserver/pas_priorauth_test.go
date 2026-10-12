package fhirserver

import (
	"testing"

	"github.com/biodream-llc/perfuse/internal/priorauth"
)

// The provider-side client reads Perfuse's own PAS responses item by item. It once read the decision from a place PAS 2.x does
// not put it, found none, and reported every response, denials included, as approved.
func TestThePriorAuthClientReadsThePASServersDecisions(t *testing.T) {
	f := newPASFixture(t)
	_, bundle := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("T1", "3", "76", "2", "cancel:3"))
	resp, err := priorauth.FromFHIR(claimResponseOf(t, bundle))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, it := range resp.Items {
		got = append(got, it.Decision)
	}
	want := []string{priorauth.DecisionApproved, priorauth.DecisionDenied, priorauth.DecisionPended, priorauth.DecisionCanceled}
	if len(got) != len(want) {
		t.Fatalf("items %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("item %d: %s, want %s", i+1, got[i], want[i])
		}
	}
	if resp.Decision != priorauth.DecisionPended {
		t.Errorf("a request with an item still pended is %s, want pended", resp.Decision)
	}
}
