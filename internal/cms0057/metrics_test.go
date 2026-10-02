package cms0057

import (
	"os"
	"strings"
	"testing"
)

func loadDecisions(t *testing.T) []Decision {
	t.Helper()
	f, err := os.Open("testdata/pa-decisions.csv")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	d, err := ParseDecisionsCSV(f)
	if err != nil {
		t.Fatal(err)
	}

	return d
}

func TestMetricsArithmetic(t *testing.T) {
	r, err := BuildMetrics(loadDecisions(t), MetricsOptions{Year: 2025, Organization: "Springfield Health Plan"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Lines) != 2 {
		t.Fatalf("want 2 lines of business, got %d", len(r.Lines))
	}
	ma := r.Lines[1]
	if ma.Name != "Medicare Advantage H1234" {
		t.Fatalf("order: %s", ma.Name)
	}
	s := ma.Standard
	if s.Requests != 4 || s.Approved.Count != 2 || s.Denied.Count != 2 || s.Approved.Percent != 50 {
		t.Fatalf("standard counts: %+v", s)
	}
	// PA2 took 11 days and PA4 10, against seven: two of four were outside the deadline.
	if s.ApprovedWithinDeadline.Count != 1 || s.DeniedWithinDeadline.Count != 1 {
		t.Fatalf("within-deadline counts: %+v %+v", s.ApprovedWithinDeadline, s.DeniedWithinDeadline)
	}
	if s.ApprovedAfterExtension.Count != 1 || s.DeniedAfterExtension.Count != 1 {
		t.Fatalf("extension counts: %+v %+v", s.ApprovedAfterExtension, s.DeniedAfterExtension)
	}
	// Appeal percentages are out of appeals, not out of requests.
	if s.ApprovedAfterAppeal.Count != 1 || s.ApprovedAfterAppeal.Of != 2 || s.ApprovedAfterAppeal.Percent != 50 {
		t.Fatalf("appeal: %+v", s.ApprovedAfterAppeal)
	}
	// 54h, 264h, 48h, 240h: mean 151.5h, median (54+240)/2 = 147h.
	if s.Turnaround.MeanHours != 151.5 || s.Turnaround.MedianHours != 147 {
		t.Fatalf("turnaround: %+v", s.Turnaround)
	}
	if s.Turnaround.Median != "6.13 calendar days" {
		t.Fatalf("median written as %q", s.Turnaround.Median)
	}
	e := ma.Expedited
	if e.Requests != 3 || e.Approved.Count != 2 || e.PartlyApproved != 1 {
		t.Fatalf("expedited: %+v", e)
	}
	// 6h, 84h, 2h: the median is under a day and must be in hours, not "0 days".
	if e.Turnaround.Median != "6 hours" {
		t.Fatalf("a median under a day was written as %q", e.Turnaround.Median)
	}
	if ma.ExtendedAndApproved.Count != 1 || ma.ExtendedAndApproved.Of != 7 {
		t.Fatalf("extended and approved: %+v", ma.ExtendedAndApproved)
	}
	if r.Excluded["still pending, so not decided in the year"] != 1 || r.Excluded["decided outside 2025"] != 1 {
		t.Fatalf("exclusions: %v", r.Excluded)
	}
}

func TestMetricsRefuseDrugs(t *testing.T) {
	_, err := ParseDecisionsCSV(strings.NewReader("priority,received,decided,decision,service_code\nstandard,2025-01-01,2025-01-02,approved,0002-8215-01\n"))
	if err == nil || !strings.Contains(err.Error(), "drug") {
		t.Fatalf("a drug row was accepted: %v", err)
	}
}

func TestMetricsReportProblemsByLine(t *testing.T) {
	_, err := ParseDecisionsCSV(strings.NewReader("priority,received,decided,decision\nsoon,2025-01-01,2025-01-02,approved\nstandard,yesterday,2025-01-02,approved\n"))
	if err == nil || !strings.Contains(err.Error(), "line 2") || !strings.Contains(err.Error(), "line 3") {
		t.Fatalf("problems not reported by line: %v", err)
	}
}

func TestMetricsHTMLAndCSV(t *testing.T) {
	r, err := BuildMetrics(loadDecisions(t), MetricsOptions{Year: 2025, Services: []Service{{Category: "Imaging", Code: "70553", Description: "MRI brain"}}})
	if err != nil {
		t.Fatal(err)
	}
	h, err := MetricsHTML(r)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Standard (non-urgent)", "Expedited (urgent)", "6 hours", "Request approved only after appeal", "MRI brain", `lang="en"`} {
		if !strings.Contains(string(h), want) {
			t.Errorf("HTML lacks %q", want)
		}
	}
	if strings.Contains(string(h), "<script") || strings.Contains(string(h), "http") && strings.Contains(string(h), "<link") {
		t.Error("the public page loads something from elsewhere")
	}
	c := string(MetricsCSV(r))
	if !strings.Contains(c, "2025,Medicare Advantage H1234,standard,approved,2,4,50.0") {
		t.Errorf("CSV row missing:\n%s", c)
	}
}
