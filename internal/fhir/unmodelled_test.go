package fhir

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestUnmodelledMembersSurviveARoundTrip(t *testing.T) {
	in := `{"resourceType":"ExplanationOfBenefit","id":"x","status":"active","use":"claim",
	"item":[{"sequence":1,"adjudication":[{"category":{"coding":[{"code":"submitted"}]},"amount":{"value":10,"currency":"USD"}}]}],
	"total":[{"category":{"coding":[{"code":"submitted"}]},"amount":{"value":10,"currency":"USD"},
	          "extension":[{"url":"http://example.org/u","valueQuantity":{"value":1}}]}],
	"_status":{"extension":[{"url":"http://example.org/s","valueString":"kept"}]}}`
	r, err := UnmarshalResource([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Marshal(r, R4)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"item"`, `"adjudication"`, `"valueQuantity"`, `"_status"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("%s was dropped on the way through the model:\n%s", want, out)
		}
	}
	var tree map[string]any
	_ = json.Unmarshal(out, &tree)
	if tree["status"] != "active" {
		t.Fatalf("a modelled field was disturbed: %v", tree["status"])
	}
}

func TestClearedModelledFieldStaysCleared(t *testing.T) {
	r, err := UnmarshalResource([]byte(`{"resourceType":"Patient","id":"p","identifier":[{"value":"MRN1"}],"birthDate":"1980-01-01","photo":[{"url":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	p := r.(*Patient)
	p.Identifier = nil
	p.BirthDate = ""
	out, _ := Marshal(p, R4)
	if strings.Contains(string(out), "MRN1") || strings.Contains(string(out), "1980") {
		t.Fatalf("a field the program cleared came back from the original: %s", out)
	}
	if !strings.Contains(string(out), `"photo"`) {
		t.Fatalf("the unmodelled member was lost: %s", out)
	}
}
