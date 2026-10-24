package fhir

import (
	"bytes"
	"testing"
)

// Whatever this package writes as R4 it must read back as the same resource.
//
// The failure this guards against was found by posting Perfuse's own ADT conversion to Perfuse's own R4 FHIR server,
// which answered 400: the downgrade reshaped Encounter.class on the way out and nothing reversed it on the way in. Each
// resource here populates every field downgradeToR4 rewrites, so a rule added to one direction and not the other fails.
func TestR4OutputIsAcceptedAsR4Input(t *testing.T) {
	concept := NewCodeableConcept(SystemLOINC, "1234-5", "x")
	ref := Ref("Patient", "p1")

	enc := &Encounter{
		Status:       "completed",
		Class:        []CodeableConcept{*NewCodeableConcept(SystemActCode, "IMP", "inpatient")},
		ServiceType:  []CodeableReference{{Concept: concept}},
		ActualPeriod: &Period{Start: "2026-10-01T09:00:00Z"},
		Participant:  []EncounterParticipant{{Actor: Ref("Practitioner", "d1")}},
		Location:     []EncounterLocation{{Location: Ref("Location", "l1"), Form: concept}},
		Admission:    &EncounterAdmission{AdmitSource: concept},
		Subject:      ref,
		Diagnosis:    []EncounterDiagnosis{{Condition: []CodeableReference{{Reference: Ref("Condition", "c1")}}, Use: []CodeableConcept{*concept}}},
	}
	enc.SetResourceID("e1")

	sr := &ServiceRequest{Status: "active", Intent: "order", Code: &CodeableReference{Concept: concept}, Subject: ref}
	sr.SetResourceID("s1")

	for _, r := range []Resource{enc, sr} {
		first, err := Marshal(r, R4)
		if err != nil {
			t.Fatal(err)
		}
		back, err := UnmarshalResource(first)
		if err != nil {
			t.Fatalf("%s: R4 output was refused as input: %v\n%s", r.ResourceTypeName(), err, first)
		}
		second, err := Marshal(back, R4)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(first, second) {
			t.Errorf("%s changed in an R4 round trip:\n first %s\nsecond %s", r.ResourceTypeName(), first, second)
		}
	}
}

// The internal form must still be accepted unchanged: the stored resources are in it.
func TestInternalFormIsNotReshaped(t *testing.T) {
	enc := &Encounter{Status: "in-progress", Class: []CodeableConcept{*NewCodeableConcept(SystemActCode, "AMB", "")}}
	enc.SetResourceID("e1")
	raw, err := Marshal(enc, CanonicalVersion)
	if err != nil {
		t.Fatal(err)
	}
	back, err := UnmarshalResource(raw)
	if err != nil {
		t.Fatal(err)
	}
	again, _ := Marshal(back, CanonicalVersion)
	if !bytes.Equal(raw, again) {
		t.Errorf("the internal form changed on reading:\n%s\n%s", raw, again)
	}
}

// R4's Procedure.category is one CodeableConcept, not a list: it is read and written as one.
func TestAnR4ProcedureCategoryIsOneCodeableConcept(t *testing.T) {
	in := `{"resourceType":"Procedure","id":"p","status":"completed","subject":{"reference":"Patient/x"},
	"category":{"coding":[{"system":"http://snomed.info/sct","code":"387713003"}]}}`
	r, err := UnmarshalResource([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	p := r.(*Procedure)
	if p.Category == nil || p.Category.Coding[0].Code != "387713003" {
		t.Fatalf("category = %+v", p.Category)
	}
	out, err := Marshal(p, R4)
	if err != nil || !bytes.Contains(out, []byte(`"category":{"coding"`)) {
		t.Errorf("%s %v", out, err)
	}
}

// A Bundle read back with UnmarshalBundle keeps each entry's resource as its own type, and members the model has no field for,
// nested ones included: a FHIR destination's bundle script relies on both.
func TestABundleIsReadBackWithItsEntriesAndUnmodelledMembers(t *testing.T) {
	in := `{"resourceType":"Bundle","type":"transaction","entry":[
	 {"fullUrl":"urn:uuid:1","resource":{"resourceType":"Patient","id":"p","identifier":[{"system":"s","value":"v","x-site":"kept"}]},
	  "request":{"method":"PUT","url":"Patient/p"}},
	 {"fullUrl":"urn:uuid:2","resource":{"resourceType":"Procedure","id":"q","status":"unknown","subject":{"reference":"Patient/p"},
	  "category":{"coding":[{"system":"http://snomed.info/sct","code":"387713003"}]}}}]}`
	b, err := UnmarshalBundle([]byte(in))
	if err != nil {
		t.Fatal(err)
	}
	if len(b.Entry) != 2 || b.Entry[0].Request == nil || b.Entry[1].Resource.(*Procedure).Category == nil {
		t.Fatalf("%+v", b)
	}
	out, err := MarshalBundle(b, R4)
	if err != nil || !bytes.Contains(out, []byte(`"x-site":"kept"`)) {
		t.Errorf("%s %v", out, err)
	}
	if _, err := UnmarshalBundle([]byte(`{"resourceType":"Patient"}`)); err == nil {
		t.Error("a Patient is not a Bundle")
	}
}
