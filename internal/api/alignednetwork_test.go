package api

import (
	"encoding/json"
	"testing"
)

func TestAlignedNetworkReadinessFollowsWhatIsOn(t *testing.T) {
	h := newHarness(t)
	read := func() map[int]alignedCriterion {
		rr := h.do("viewer", "GET", "/api/aligned-network", nil)
		if rr.Code != 200 {
			t.Fatalf("%d %s", rr.Code, rr.Body)
		}
		var got struct {
			Criteria []alignedCriterion `json:"criteria"`
		}
		_ = json.Unmarshal(rr.Body.Bytes(), &got)
		if len(got.Criteria) != 26 {
			t.Fatalf("%d criteria", len(got.Criteria))
		}
		m := map[int]alignedCriterion{}
		for _, c := range got.Criteria {
			m[c.Number] = c
		}
		return m
	}
	m := read()
	if m[15].Status != alignedOff || len(m[15].Missing) != 2 || !m[15].InEffect {
		t.Errorf("subscriptions with nothing on: %+v", m[15])
	}
	if m[8].Status != alignedNotBuilt || m[26].Status != alignedYours {
		t.Errorf("not built %s, yours %s", m[8].Status, m[26].Status)
	}
	h.server.CMS0057 = CMS0057Status{FHIR: true, SMART: true, Subscriptions: true, BulkExport: true}
	m = read()
	if m[15].Status != alignedReady || m[13].Status != alignedPartial || m[1].Status != alignedPartial {
		t.Errorf("15 %s, 13 %s, 1 %s", m[15].Status, m[13].Status, m[1].Status)
	}
	if rr := h.do("", "GET", "/api/aligned-network", nil); rr.Code != 401 {
		t.Errorf("signed out: %d", rr.Code)
	}
}
