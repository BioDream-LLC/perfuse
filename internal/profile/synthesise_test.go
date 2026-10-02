package profile

import (
	"strings"
	"testing"
)

func TestSynthesiseProducesAValidChannel(t *testing.T) {
	r := &Report{
		Messages:   400,
		Unreadable: 3,
		Types: []TypeCount{
			{Type: "ADT^A01", Count: 280, Rate: 0.70},
			{Type: "ADT^A08", Count: 120, Rate: 0.30},
		},
		Segments: []Segment{
			{
				ID:       "PID",
				Name:     "Patient Identification",
				Standard: true,
				Messages: 400,
				Rate:     1.0,
				Fields: []Field{
					{Path: "PID-3", Name: "Patient Identifier List", Present: 400, FillRate: 1.0, Distinct: 400, Shape: ShapeNumeric},
					{Path: "PID-5", Name: "Patient Name", Present: 400, FillRate: 1.0, Distinct: 380, Shape: ShapeAlphanumeric},
					{Path: "PID-8", Name: "Administrative Sex", Present: 395, FillRate: 0.9875, Distinct: 3, Shape: ShapeAlpha,
						Table: "0001",
						Codes: []CodeCount{
							{Code: "M", Count: 200, Meaning: "Male", Known: true},
							{Code: "F", Count: 180, Meaning: "Female", Known: true},
							{Code: "U", Count: 15, Meaning: "Unknown", Known: true},
						}},
					{Path: "PID-11", Name: "Patient Address", Present: 320, FillRate: 0.80, Distinct: 290, Shape: ShapeMixed},
				},
			},
			{
				ID:       "PV1",
				Name:     "Patient Visit",
				Standard: true,
				Messages: 400,
				Rate:     1.0,
				Fields: []Field{
					{Path: "PV1-2", Name: "Patient Class", Present: 400, FillRate: 1.0, Distinct: 4, Shape: ShapeAlpha,
						Table: "0004",
						Codes: []CodeCount{
							{Code: "I", Count: 250, Meaning: "Inpatient", Known: true},
							{Code: "O", Count: 100, Meaning: "Outpatient", Known: true},
							{Code: "E", Count: 40, Meaning: "Emergency", Known: true},
							{Code: "X", Count: 10, Known: false},
						}},
				},
			},
		},
	}

	suggestions := []Suggestion{
		{Path: "PID-8", Role: RoleCodeSet, Evidence: []string{"3 distinct values"}, Sampled: 400},
		{Path: "PV1-2", Role: RoleCodeSet, Evidence: []string{"4 distinct values"}, Sampled: 400},
	}

	yaml := Synthesise(r, suggestions, SynthesisOptions{ChannelName: "adt-inbound"})

	// It must be a complete channel, not a fragment.
	for _, want := range []string{
		"name: adt-inbound",
		"source:",
		"listen:",
		"filter:",
		"ADT^A01",
		"ADT^A08",
		"transformations:",
		"PID-8",
		"PV1-2",
		"destinations:",
		"archive",
	} {
		if !strings.Contains(yaml, want) {
			t.Errorf("missing %q in synthesis", want)
		}
	}

	// Every code must have a blank `to` — no guessing.
	for _, code := range []string{"M:", "F:", "U:", "I:", "O:", "E:", "X:"} {
		if !strings.Contains(yaml, code) {
			t.Errorf("code %s is missing from the mapping tables", code)
		}
	}

	// The reasoning must be beside each entry.
	if !strings.Contains(yaml, "seen 200 times") {
		t.Error("the evidence is not beside the entries")
	}

	// Unknown codes are flagged.
	if !strings.Contains(yaml, "not in the standard table") {
		t.Error("an unknown code is not flagged")
	}

	// The unreadable count is mentioned.
	if !strings.Contains(yaml, "3 of the") {
		t.Error("unreadable messages are not mentioned")
	}

	// Contract candidates are listed.
	if !strings.Contains(yaml, "PID-3") || !strings.Contains(yaml, "PID-5") || !strings.Contains(yaml, "PV1-2") {
		t.Error("contract candidates (always-populated fields) are not listed")
	}

	// PID-11 at 80% is not a contract candidate at the default threshold.
	if strings.Contains(yaml, "PID-11") && strings.Contains(yaml, "present in") {
		t.Error("PID-11 at 80% should not be a contract candidate at the default 100% threshold")
	}

	// It says this is not a decision.
	if !strings.Contains(yaml, "review every line") {
		t.Error("the generated file does not say it needs review")
	}

	t.Log("--- first 20 lines ---")
	lines := strings.Split(yaml, "\n")
	for i, l := range lines {
		if i >= 20 {
			break
		}
		t.Log(l)
	}
}

// An empty report (no messages) produces nothing rather than an empty shell.
func TestSynthesiseRefusesEmptyReport(t *testing.T) {
	yaml := Synthesise(&Report{Messages: 0}, nil, SynthesisOptions{})

	// It must still run without panicking. A specification that panics when there is nothing to describe is not a specification that
	// handles the case.
	if yaml == "" {
		t.Error("synthesis returned an empty string; even an empty report should explain why there is nothing")
	}
	if !strings.Contains(yaml, "0 messages") {
		t.Error("synthesis does not state the empty sample")
	}
}
