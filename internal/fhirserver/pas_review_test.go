package fhirserver

import (
	"context"
	"strings"
	"testing"
	"time"
)

func TestTheReviewerQueueListsWhatIsPendedSoonestDueFirstAndDecidesIt(t *testing.T) {
	f := newPASFixture(t)
	pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("Q1", "3"))
	_, b := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("Q2", "2*10"))
	urgent := strings.Replace(pasRequestJSON("Q3", "2"), `"code":"normal"`, `"code":"stat"`, 1)
	pasPost(t, f.h, "/Claim/$submit", urgent)
	srv := NewServer(f.store, "http://example.test/fhir", nil)
	srv.PAS = &PAS{Now: newPASFixtureClock}
	ctx := context.Background()

	cases, err := srv.Cases(ctx, true, 0)
	if err != nil || len(cases) != 2 {
		t.Fatalf("two pended requests expected: %v %+v", err, cases)
	}
	if cases[0].Trace != "Q3" || !cases[0].Expedited || cases[0].Due.Sub(cases[0].Created).Hours() != 72 {
		t.Errorf("the expedited request is due first, in 72 hours: %+v", cases[0])
	}
	if c := cases[1]; c.Member != "Pat Member" || c.MemberID != "12345678901" || !strings.Contains(c.Provider, "NPI 8189991234") ||
		len(c.Items) != 1 || c.Items[0].Code != "A4" || c.Items[0].Quantity != 10 || len(c.Asked) != 2 || c.Request != nil {
		t.Errorf("case: %+v", c)
	}
	one, err := srv.Case(ctx, str(claimResponseOf(t, b)["id"]))
	if err != nil || one.Request == nil || one.Response == nil {
		t.Fatalf("a single case is read whole: %v", err)
	}
	if _, err := srv.Review(ctx, one.ID, PASReview{Decision: "modify"}); err == nil {
		t.Error("modify with nothing modified was accepted")
	}
	out, err := srv.Review(ctx, one.ID, PASReview{Decision: "modify", Quantity: 4, Reason: "Four, then review.", ReviewerNPI: "1234567893"})
	if err != nil || actionCodes(claimResponseOf(t, out)) != "A6" {
		t.Fatalf("%v %v", err, out)
	}
	if left, _ := srv.Cases(ctx, true, 0); len(left) != 1 {
		t.Errorf("one left after the decision: %d", len(left))
	}
	if all, _ := srv.Cases(ctx, false, 0); len(all) != 3 || !all[0].Pended {
		t.Errorf("all requests, pended first: %+v", all)
	}
}

func TestThePriorAuthorizationFiguresCountTimelinessAndDecisions(t *testing.T) {
	f := newPASFixture(t)
	pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("F1", "3"))
	pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("F2", "2*10"))
	urgent := strings.Replace(pasRequestJSON("F3", "2"), `"code":"normal"`, `"code":"stat"`, 1)
	pasPost(t, f.h, "/Claim/$submit", urgent)
	srv := NewServer(f.store, "http://example.test/fhir", nil)
	srv.PAS = &PAS{Now: newPASFixtureClock}
	fig, err := srv.Figures(context.Background(), newPASFixtureClock().AddDate(0, 0, -30))
	if err != nil {
		t.Fatal(err)
	}
	if fig.Standard.Requests != 2 || fig.Expedited.Requests != 1 || fig.Items != 3 {
		t.Errorf("requests by priority: %+v", fig)
	}
	if fig.Standard.DecidedInTime != 1 || fig.Standard.PendingInTime != 1 || fig.Expedited.PendingInTime != 1 || fig.Standard.Overdue != 0 {
		t.Errorf("timeliness: %+v %+v", fig.Standard, fig.Expedited)
	}
	if fig.Decisions["approved"] != 1 || fig.Decisions["pended"] != 2 {
		t.Errorf("decisions: %v", fig.Decisions)
	}
	late := &PAS{Now: func() time.Time { return newPASFixtureClock().Add(8 * 24 * time.Hour) }}
	srv.PAS = late
	fig, _ = srv.Figures(context.Background(), newPASFixtureClock().AddDate(0, 0, -30))
	if fig.Standard.Overdue != 1 || fig.Expedited.Overdue != 1 {
		t.Errorf("eight days on, both pended requests are overdue: %+v %+v", fig.Standard, fig.Expedited)
	}
}
