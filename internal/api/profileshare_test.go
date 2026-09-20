package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/profile"
)

// Tests for sharing a dialect profile.
//
// The machinery for this existed for some time with no caller, which is the shape this project's defect list already names: an
// implementation with no caller is indistinguishable from a feature that does not exist. These tests are what make it exist.
//
// The property most of them assert is that a profile leaving the building carries no patient data. That is true by construction today -
// the profiler keeps values only for table-constrained fields - and asserted here anyway, because the export is the last point at which
// being wrong is unrecoverable. A file is gone once it is sent.

// profileWithCodes builds a profile whose only listed values are a code table's.
func profileWithCodes() profile.Report {
	return profile.Report{
		Messages: 500,
		Types:    []profile.TypeCount{{Type: "ADT^A01", Count: 500, Rate: 1}},
		Segments: []profile.Segment{
			{
				ID: "PID", Name: "Patient Identification", Standard: true, Messages: 500, Rate: 1,
				Fields: []profile.Field{
					{
						Path: "PID-8", Name: "Administrative Sex", Present: 500, FillRate: 1, Distinct: 3,
						Table: "0001",
						Codes: []profile.CodeCount{{Code: "M", Count: 260}, {Code: "F", Count: 235}, {Code: "U", Count: 5}},
					},
					{Path: "PID-3", Name: "Patient Identifier List", Present: 500, FillRate: 1, Distinct: 500},
				},
			},
		},
	}
}

func TestAProfileWithoutASendingSystemIsRefused(t *testing.T) {
	h := newHarness(t)

	// Refused rather than defaulted. The question a stranger asks of a shared profile is "has anybody mapped this system before",
	// and an optional field invites "Unknown" as the most common answer in a shared library.
	res := h.do("viewer", http.MethodPost, "/api/profiles/export", map[string]any{
		"channel": "anything", "name": "Some feed",
	})
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "which system") {
		t.Errorf("the refusal does not say what is missing: %s", res.Body.String())
	}
}

func TestAnExportWithNoChannelIsRefused(t *testing.T) {
	h := newHarness(t)

	res := h.do("viewer", http.MethodPost, "/api/profiles/export", map[string]any{
		"report": profile.Report{}, "name": "Empty", "source": "Epic 2023",
	})
	if res.Code != http.StatusBadRequest {
		t.Errorf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
}

func TestOnlyCodeTableFieldsMayListValues(t *testing.T) {
	// The assertion that matters, tested directly on the check rather than through the endpoint.
	//
	// A field with values and no table number is either a bug in whatever produced the profile or a field the dictionary does not
	// constrain, and in both cases the values could be patient identifiers. The check runs on the way out and on the way in - out
	// because that is the last point where being wrong cannot be undone, and in because a profile from elsewhere was built by
	// somebody whose care is unknown.
	clean := profile.ExportProfile(&profile.Report{Messages: 1, Segments: profileWithCodes().Segments}, "clean", "",
		"Epic 2023", "hl7", nil)

	if bad := identifyingFields(clean); len(bad) > 0 {
		t.Errorf("a profile listing only code table values was reported as identifying: %#v", bad)
	}

	leaky := profileWithCodes()
	leaky.Segments[0].Fields[1].Codes = []profile.CodeCount{
		{Code: "MRN00412", Count: 1},
		{Code: "MRN00998", Count: 1},
	}

	shared := profile.ExportProfile(&profile.Report{Messages: 1, Segments: leaky.Segments}, "leaky", "", "Epic 2023", "hl7", nil)

	bad := identifyingFields(shared)
	if len(bad) != 1 {
		t.Fatalf("identifyingFields = %#v, want one finding for PID-3", bad)
	}

	// The path is named and the values are not, which is the mistake that would make the guard itself the disclosure.
	if !strings.Contains(bad[0], "PID-3") {
		t.Errorf("the finding does not name the field: %q", bad[0])
	}
	if strings.Contains(bad[0], "MRN00412") {
		t.Errorf("the finding quoted the identifier it was objecting to: %q", bad[0])
	}
}

func TestASharedProfileCanBeReadBack(t *testing.T) {
	h := newHarness(t)

	shared := profile.ExportProfile(&profile.Report{
		Messages: 10,
		Types:    []profile.TypeCount{{Type: "ADT^A01", Count: 10, Rate: 1}},
	}, "Somebody else's feed", "", "Cerner Millennium", "hl7", nil)

	data, err := profile.MarshalProfile(shared)
	if err != nil {
		t.Fatal(err)
	}

	var body json.RawMessage = data

	res := h.do("viewer", http.MethodPost, "/api/profiles/import", body)
	if res.Code != http.StatusOK {
		t.Fatalf("returned %d, want 200: %s", res.Code, res.Body.String())
	}

	// The response has to say nothing was applied. An import that reports only success invites the belief that the profile changed
	// something, and a profile describes a feed - what to do about it is a separate decision.
	if !strings.Contains(res.Body.String(), "not applied") {
		t.Errorf("the import did not say that nothing was applied: %s", res.Body.String())
	}
}

func TestAnImportedProfileListingIdentifiersIsRefused(t *testing.T) {
	// Checked on the way in as well as out, for a different reason: this one arrived from outside, so nobody here knows how it was
	// built. Showing somebody else's patient identifiers because they exported carelessly would be this instance's disclosure.
	shared := profile.ExportProfile(&profile.Report{
		Messages: 10,
		Segments: []profile.Segment{{
			ID: "PID", Messages: 10,
			Fields: []profile.Field{{
				Path:  "PID-3",
				Codes: []profile.CodeCount{{Code: "MRN00412", Count: 1}},
			}},
		}},
	}, "Careless", "", "Epic 2023", "hl7", nil)

	data, err := profile.MarshalProfile(shared)
	if err != nil {
		t.Fatal(err)
	}

	h := newHarness(t)

	var body json.RawMessage = data

	res := h.do("viewer", http.MethodPost, "/api/profiles/import", body)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "should be told") {
		t.Errorf("the refusal does not say to tell whoever produced it: %s", res.Body.String())
	}
	if strings.Contains(res.Body.String(), "MRN00412") {
		t.Error("the refusal quoted the identifier it was refusing to load")
	}
}

func TestSomethingThatIsNotAProfileIsRefusedWithTheReason(t *testing.T) {
	h := newHarness(t)

	var body json.RawMessage = []byte(`{"format":"something/else","name":"nope"}`)

	res := h.do("viewer", http.MethodPost, "/api/profiles/import", body)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("returned %d, want 400: %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "not a profile this can read") {
		t.Errorf("the refusal is not specific: %s", res.Body.String())
	}
}
