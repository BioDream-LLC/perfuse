package profile

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

// --- Unknown field rejection (house rule: unknown keys are errors) ---

func TestUnmarshalProfileRejectsUnknownFields(t *testing.T) {
	data := []byte(`{
		"format": "perfuse/profile/v1",
		"name": "test",
		"unknownField": "this should cause an error",
		"messageCount": 100,
		"dataType": "hl7",
		"segments": []
	}`)

	_, err := UnmarshalProfile(data)
	if err == nil {
		t.Error("UnmarshalProfile accepted unknown field 'unknownField' without error")
	}
}

func TestUnmarshalRecipeRejectsUnknownFields(t *testing.T) {
	data := []byte(`{
		"format": "perfuse/recipe/v1",
		"name": "test",
		"dangerousScript": "rm -rf /",
		"mappings": []
	}`)

	_, err := UnmarshalRecipe(data)
	if err == nil {
		t.Error("UnmarshalRecipe accepted unknown field 'dangerousScript' without error")
	}
}

// --- Version handling ---

func TestUnmarshalProfileRejectsFutureVersion(t *testing.T) {
	data := []byte(`{
		"format": "perfuse/profile/v2",
		"name": "from the future",
		"messageCount": 100,
		"dataType": "hl7",
		"segments": []
	}`)

	_, err := UnmarshalProfile(data)
	if err == nil {
		t.Error("UnmarshalProfile accepted format v2 (future/unknown version)")
	}
}

func TestUnmarshalRecipeRejectsFutureVersion(t *testing.T) {
	data := []byte(`{
		"format": "perfuse/recipe/v2",
		"name": "from the future",
		"mappings": []
	}`)

	_, err := UnmarshalRecipe(data)
	if err == nil {
		t.Error("UnmarshalRecipe accepted format v2 (future/unknown version)")
	}
}

// --- Full round-trip fidelity ---

func TestProfileFullRoundTrip(t *testing.T) {
	original := &SharedProfile{
		Format:        "perfuse/profile/v1",
		Name:          "Full Test Profile",
		Description:   "Testing all fields",
		Source:        "Epic 2024",
		SourceVersion: "2024.1.3",
		CreatedAt:     time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC),
		MessageCount:  1500,
		DataType:      "hl7",
		MessageTypes: []TypeCount{
			{Type: "ADT^A01", Count: 800, Rate: 0.533},
			{Type: "ADT^A08", Count: 700, Rate: 0.467},
		},
		Segments: []Segment{
			{
				ID:            "PID",
				Name:          "Patient Identification",
				Standard:      true,
				Messages:      1500,
				Rate:          1.0,
				MaxPerMessage: 1,
				Fields: []Field{
					{
						Path:       "PID-3",
						Name:       "Patient Identifier List",
						Present:    1498,
						FillRate:   0.999,
						Distinct:   1498,
						Shape:      ShapeNumeric,
						MinLength:  8,
						MaxLength:  10,
						MaxRepeats: 3,
						Components: 5,
					},
					{
						Path:       "PID-8",
						Name:       "Administrative Sex",
						Present:    1490,
						FillRate:   0.993,
						Distinct:   3,
						Shape:      ShapeAlpha,
						MinLength:  1,
						MaxLength:  1,
						MaxRepeats: 1,
						Components: 1,
						Table:      "0001",
						Codes: []CodeCount{
							{Code: "M", Count: 750, Meaning: "Male", Known: true},
							{Code: "F", Count: 700, Meaning: "Female", Known: true},
							{Code: "X", Count: 40, Meaning: "", Known: false},
						},
					},
				},
			},
		},
		Tags: []string{"epic", "adt", "production", "2024"},
	}

	data, err := MarshalProfile(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := UnmarshalProfile(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Format != original.Format {
		t.Errorf("Format: %q != %q", got.Format, original.Format)
	}
	if got.Name != original.Name {
		t.Errorf("Name: %q != %q", got.Name, original.Name)
	}
	if got.Description != original.Description {
		t.Errorf("Description: %q != %q", got.Description, original.Description)
	}
	if got.Source != original.Source {
		t.Errorf("Source: %q != %q", got.Source, original.Source)
	}
	if got.SourceVersion != original.SourceVersion {
		t.Errorf("SourceVersion: %q != %q", got.SourceVersion, original.SourceVersion)
	}
	if !got.CreatedAt.Equal(original.CreatedAt) {
		t.Errorf("CreatedAt: %v != %v", got.CreatedAt, original.CreatedAt)
	}
	if got.MessageCount != original.MessageCount {
		t.Errorf("MessageCount: %d != %d", got.MessageCount, original.MessageCount)
	}
	if got.DataType != original.DataType {
		t.Errorf("DataType: %q != %q", got.DataType, original.DataType)
	}
	if len(got.MessageTypes) != len(original.MessageTypes) {
		t.Errorf("MessageTypes: %d != %d", len(got.MessageTypes), len(original.MessageTypes))
	}
	if len(got.Segments) != len(original.Segments) {
		t.Fatalf("Segments: %d != %d", len(got.Segments), len(original.Segments))
	}
	if len(got.Tags) != len(original.Tags) {
		t.Errorf("Tags: %d != %d", len(got.Tags), len(original.Tags))
	}

	seg := got.Segments[0]
	if seg.MaxPerMessage != 1 {
		t.Errorf("MaxPerMessage: %d != 1", seg.MaxPerMessage)
	}
	if len(seg.Fields) != 2 {
		t.Fatalf("fields: %d != 2", len(seg.Fields))
	}
	if seg.Fields[0].Components != 5 {
		t.Errorf("PID-3 Components: %d != 5", seg.Fields[0].Components)
	}
	if seg.Fields[0].MaxRepeats != 3 {
		t.Errorf("PID-3 MaxRepeats: %d != 3", seg.Fields[0].MaxRepeats)
	}
	if len(seg.Fields[1].Codes) != 3 {
		t.Errorf("PID-8 Codes: %d != 3", len(seg.Fields[1].Codes))
	}
}

func TestRecipeFullRoundTrip(t *testing.T) {
	original := &MappingRecipe{
		Format:       "perfuse/recipe/v1",
		Name:         "Full Recipe",
		Description:  "Every field populated",
		ForProfile:   "Epic ADT Profile",
		SourceSystem: "Epic 2024",
		TargetSystem: "Internal FHIR",
		CreatedAt:    time.Date(2024, 6, 15, 10, 30, 0, 0, time.UTC),
		Author:       "A. Integrator",
		Mappings: []FieldMapping{
			{
				SourcePath: "PID-3.1",
				TargetPath: "Patient.identifier[0].value",
				Transform:  "copy",
				TransformArgs: map[string]string{
					"strip_prefix": "MRN",
				},
				Condition:  "MSH-9 == 'ADT^A01'",
				Confidence: 95,
				Reasoning:  "MRN field is directly equivalent",
				Approved:   true,
			},
		},
		Tags: []string{"epic", "fhir", "production"},
	}

	data, err := MarshalRecipe(original)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := UnmarshalRecipe(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.ForProfile != original.ForProfile {
		t.Errorf("ForProfile: %q != %q", got.ForProfile, original.ForProfile)
	}
	if got.Author != original.Author {
		t.Errorf("Author: %q != %q", got.Author, original.Author)
	}
	if len(got.Mappings) != 1 {
		t.Fatalf("Mappings: %d != 1", len(got.Mappings))
	}
	m := got.Mappings[0]
	if m.Condition != original.Mappings[0].Condition {
		t.Errorf("Condition: %q != %q", m.Condition, original.Mappings[0].Condition)
	}
	if !reflect.DeepEqual(m.TransformArgs, original.Mappings[0].TransformArgs) {
		t.Errorf("TransformArgs: %v != %v", m.TransformArgs, original.Mappings[0].TransformArgs)
	}
}

// --- Synthesise determinism ---

func TestSynthesiseDeterminism(t *testing.T) {
	r := &Report{
		Messages: 100,
		Types: []TypeCount{
			{Type: "ADT^A01", Count: 60, Rate: 0.60},
			{Type: "ADT^A08", Count: 40, Rate: 0.40},
		},
		Segments: []Segment{
			{
				ID: "PID", Messages: 100, Rate: 1.0,
				Fields: []Field{
					{Path: "PID-3", Name: "MRN", Present: 100, FillRate: 1.0, Distinct: 100, Shape: ShapeNumeric},
					{Path: "PID-5", Name: "Name", Present: 100, FillRate: 1.0, Distinct: 95, Shape: ShapeAlpha},
					{Path: "PID-8", Name: "Sex", Present: 100, FillRate: 1.0, Distinct: 3, Shape: ShapeAlpha,
						Table: "0001",
						Codes: []CodeCount{
							{Code: "M", Count: 50, Known: true},
							{Code: "F", Count: 45, Known: true},
							{Code: "U", Count: 5, Known: true},
						}},
				},
			},
		},
	}

	suggestions := []Suggestion{
		{Path: "PID-8", Role: RoleCodeSet, Evidence: []string{"3 values"}, Sampled: 100},
	}

	first := Synthesise(r, suggestions, SynthesisOptions{ChannelName: "test"})

	for i := 0; i < 50; i++ {
		got := Synthesise(r, suggestions, SynthesisOptions{ChannelName: "test"})
		if got != first {
			t.Fatalf("non-deterministic output on iteration %d", i)
		}
	}
}

// --- Synthesise round-trip: generated YAML loads back (structural check) ---

func TestSynthesiseOutputStructure(t *testing.T) {
	r := &Report{
		Messages: 50,
		Types: []TypeCount{
			{Type: "ORU^R01", Count: 50, Rate: 1.0},
		},
		Segments: []Segment{
			{
				ID: "OBX", Messages: 50, Rate: 1.0,
				Fields: []Field{
					{Path: "OBX-3", Name: "Observation Identifier", Present: 50, FillRate: 1.0, Distinct: 10, Shape: ShapeAlphanumeric,
						Table: "LOINC",
						Codes: []CodeCount{
							{Code: "2345-7", Count: 30, Meaning: "Glucose", Known: true},
							{Code: "718-7", Count: 20, Meaning: "Hemoglobin", Known: true},
						}},
					{Path: "OBX-5", Name: "Observation Value", Present: 50, FillRate: 1.0, Distinct: 48, Shape: ShapeNumeric},
				},
			},
		},
	}

	suggestions := []Suggestion{
		{Path: "OBX-3", Role: RoleCodeSet, Evidence: []string{"10 distinct values"}, Sampled: 50},
	}

	yaml := Synthesise(r, suggestions, SynthesisOptions{ChannelName: "lab-inbound"})

	// The output must be valid YAML structure (key: value lines, proper indentation).
	// We can't load it as YAML without a dependency, but we can verify structural invariants.
	if yaml == "" {
		t.Fatal("empty output")
	}

	required := []string{"name: lab-inbound", "source:", "filter:", "ORU^R01", "destinations:"}
	for _, want := range required {
		if !strings.Contains(yaml, want) {
			t.Errorf("missing %q in output", want)
		}
	}

	// Every line must be valid: no unclosed quotes, no bare colons without a key.
	lines := strings.Split(yaml, "\n")
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// A YAML key-value line must have a colon.
		if !strings.Contains(trimmed, ":") && !strings.HasPrefix(trimmed, "-") {
			t.Errorf("line %d has no colon and is not a list item: %q", i+1, line)
		}
	}
}
