package pas

import (
	"encoding/json"
	"strings"
	"testing"
)

const bundleJSON = `{
  "resourceType": "Bundle", "type": "collection",
  "entry": [
    {"fullUrl": "http://provider.example/fhir/Claim/c1", "resource": {
      "resourceType": "Claim", "id": "c1", "use": "preauthorization",
      "patient": {"reference": "Patient/p1"}, "insurer": {"reference": "http://provider.example/fhir/Organization/plan"},
      "provider": {"reference": "Organization/clinic"},
      "item": [
        {"sequence": 1, "productOrService": {"coding": [{"system": "http://www.ama-assn.org/go/cpt", "code": "72148"}]}},
        {"sequence": 2, "productOrService": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/data-absent-reason", "code": "not-applicable"}]},
         "extension": [{"url": "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/extension-requestedService",
                        "valueReference": {"reference": "ServiceRequest/sr1"}}]}
      ]}},
    {"fullUrl": "http://provider.example/fhir/Patient/p1", "resource": {"resourceType": "Patient", "id": "p1"}},
    {"fullUrl": "http://provider.example/fhir/Organization/plan", "resource": {"resourceType": "Organization", "id": "plan"}},
    {"resource": {"resourceType": "Organization", "id": "clinic"}},
    {"fullUrl": "http://provider.example/fhir/ServiceRequest/sr1", "resource": {"resourceType": "ServiceRequest", "id": "sr1",
      "code": {"coding": [{"system": "http://www.ama-assn.org/go/cpt", "code": "72158"}]}}},
    {"fullUrl": "urn:uuid:cond", "resource": {"resourceType": "Condition", "id": "k1"}}
  ]}`

func bundle(t *testing.T, src string) map[string]any {
	t.Helper()
	var b map[string]any
	if err := json.Unmarshal([]byte(src), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReadAndResolve(t *testing.T) {
	req, problems := Read(bundle(t, bundleJSON), false)
	if len(problems) > 0 {
		t.Fatalf("problems: %v", problems)
	}
	for ref, want := range map[string]string{
		"Patient/p1": "p1", // relative, resolved against the Claim's base
		"http://provider.example/fhir/Organization/plan": "plan",   // absolute
		"Organization/clinic":                            "clinic", // an entry with no fullUrl, by type and id
	} {
		e := req.Resolve(ref)
		if e == nil || Str(Map(e["resource"])["id"]) != want {
			t.Errorf("Resolve(%q) = %v, want %s", ref, e, want)
		}
	}
	if req.Resolve("Patient/nobody") != nil || req.Resolve("") != nil {
		t.Error("an unknown reference resolved")
	}
	if n := len(req.Resources("Condition")); n != 1 {
		t.Errorf("Resources(Condition) found %d, want 1", n)
	}
}

func TestServices(t *testing.T) {
	req, _ := Read(bundle(t, bundleJSON), false)
	svcs := req.Services()
	if len(svcs) != 2 {
		t.Fatalf("%d services, want 2", len(svcs))
	}
	if len(svcs[0].Codes) != 1 || svcs[0].Sequence != "1" {
		t.Errorf("item 1: %+v", svcs[0])
	}
	// Item 2's own code says it has none; its code is the ServiceRequest's.
	s2, ok := req.Service("2")
	if !ok || len(s2.Codes) != 1 || Str(Map(Slice(s2.Codes[0]["coding"])[0])["code"]) != "72158" {
		t.Errorf("item 2: %+v", s2)
	}
	if _, ok := req.Service("3"); ok {
		t.Error("an item that does not exist was found")
	}
}

func TestReadRefuses(t *testing.T) {
	cases := map[string]struct {
		src     string
		nilReq  bool
		problem string
	}{
		"not a Bundle":     {`{"resourceType": "Claim"}`, true, "not a Bundle"},
		"no entries":       {`{"resourceType": "Bundle", "type": "collection"}`, true, "no entries"},
		"Claim not first":  {`{"resourceType": "Bundle", "type": "collection", "entry": [{"resource": {"resourceType": "Patient"}}]}`, true, "first entry must be the Claim"},
		"wrong use":        {strings.Replace(bundleJSON, `"use": "preauthorization"`, `"use": "claim"`, 1), false, "Claim.use"},
		"missing patient":  {strings.Replace(bundleJSON, `"patient": {"reference": "Patient/p1"}, `, ``, 1), false, "Claim.patient is missing"},
		"dangling insurer": {strings.Replace(bundleJSON, `/Organization/plan"},`, `/Organization/other"},`, 1), false, "is not in the Bundle"},
		"no items":         {strings.Replace(bundleJSON, `"item": [`, `"items": [`, 1), false, "no items"},
		"wrong type":       {strings.Replace(bundleJSON, `"type": "collection"`, `"type": "batch"`, 1), false, "Bundle.type"},
	}
	for name, c := range cases {
		req, problems := Read(bundle(t, c.src), false)
		if (req == nil) != c.nilReq || !strings.Contains(strings.Join(problems, "|"), c.problem) {
			t.Errorf("%s: req nil=%v problems=%v", name, req == nil, problems)
		}
	}
	// An inquiry needs no items.
	if _, problems := Read(bundle(t, strings.Replace(bundleJSON, `"item": [`, `"items": [`, 1)), true); len(problems) > 0 {
		t.Errorf("inquiry: %v", problems)
	}
}

func TestMapNeverNil(t *testing.T) {
	if Map(nil) == nil || Map("x") == nil {
		t.Fatal("Map returned nil")
	}
	if Str(Map(Map(nil)["a"])["b"]) != "" {
		t.Fatal("a lookup into nothing found something")
	}
}
