package fhirserver

import (
	"net/http"
	"testing"
)

func TestDateRanges(t *testing.T) {
	for in, want := range map[string][2]string{
		"2026":                      {"2026-01-01T00:00:00.000Z", "2026-12-31T23:59:59.999Z"},
		"2026-02":                   {"2026-02-01T00:00:00.000Z", "2026-02-28T23:59:59.999Z"},
		"2026-10-05":                {"2026-10-05T00:00:00.000Z", "2026-10-05T23:59:59.999Z"},
		"2026-10-05T08:00:00+10:00": {"2026-10-04T22:00:00.000Z", "2026-10-04T22:00:00.999Z"},
		"2026-10-05T08:00:00.25Z":   {"2026-10-05T08:00:00.250Z", "2026-10-05T08:00:00.250Z"},
		"2026-10-05T08:00Z":         {"2026-10-05T08:00:00.000Z", "2026-10-05T08:00:59.999Z"},
	} {
		lo, hi, err := dateRange(in)
		if err != nil || lo != want[0] || hi != want[1] {
			t.Errorf("%s: %s..%s %v, want %v", in, lo, hi, err, want)
		}
	}
	if _, _, err := dateRange("yesterday"); err == nil {
		t.Error("a word was read as a date")
	}
}

// chat.fhir.org #implementers "Period Search" (2026-07): an Encounter from 29 July to 2 August 1950.
func TestADateSearchComparesRangesNotStrings(t *testing.T) {
	_, h := newTestServer(t)
	do(t, h, http.MethodPost, "/Encounter", `{"resourceType":"Encounter","status":"finished","class":{"code":"AMB"},
		"period":{"start":"1950-07-29T08:11:00+02:00","end":"1950-08-02T02:44:00+02:00"}}`)
	do(t, h, http.MethodPost, "/Encounter", `{"resourceType":"Encounter","status":"in-progress","class":{"code":"IMP"},
		"period":{"start":"2026-12-30T10:00:00Z"}}`)
	for q, want := range map[string]float64{
		"date=ge1950-08-01":                   2, // the 1950 stay reaches past it (its start alone used to say no), and so does the open one
		"date=le1950-07-30":                   1,
		"date=1950-07-30":                     0, // eq: one day cannot contain a five-day stay
		"date=ge1950-07-30&date=le1950-07-30": 1, // the stay was going on that day
		"date=1950":                           1,
		"date=sa1950-07-28":                   2,
		"date=eb1950-08-03":                   1,
		"date=gt1950-08-03":                   1,
		"date=ap1950-08-01":                   1,
		"date=ge2027-06-01":                   1, // no end: still going
		"date=2026":                           0, // and so not wholly within 2026
		"date=1950-07-29T06:11:00Z":           0,
		"date=le1950-07-29T06:11:01Z":         1, // 08:11+02:00 is 06:11 UTC
		"date=le1950-07-29T06:10:59Z":         0,
	} {
		if total := tree(t, do(t, h, http.MethodGet, "/Encounter?"+q, nil))["total"].(float64); total != want {
			t.Errorf("%s: total %v, want %v", q, total, want)
		}
	}
	if rec := do(t, h, http.MethodGet, "/Encounter?date=soon", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("an unreadable date answered %d", rec.Code)
	}
}

func TestTwoContainedResourcesCannotShareAnID(t *testing.T) {
	_, h := newTestServer(t)
	rec := do(t, h, http.MethodPost, "/Patient", `{"resourceType":"Patient","contained":[{"resourceType":"Organization","id":"o1","name":"A"},
		{"resourceType":"Practitioner","id":"o1"}],"managingOrganization":{"reference":"#o1"}}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("duplicate contained ids: %d", rec.Code)
	}
	rec = do(t, h, http.MethodPost, "/Patient", `{"resourceType":"Patient","contained":[{"resourceType":"Organization","id":"o1","name":"A"},
		{"resourceType":"Practitioner","id":"p1"}],"managingOrganization":{"reference":"#o1"}}`)
	if rec.Code != http.StatusCreated {
		t.Errorf("distinct contained ids: %d %s", rec.Code, rec.Body)
	}
}
