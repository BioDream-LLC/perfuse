package profile

import (
	"testing"
	"time"
)

func TestExportAndUnmarshalProfile(t *testing.T) {
	report := &Report{
		Messages: 500,
		Types:    []TypeCount{{Type: "ADT^A01", Count: 300}, {Type: "ADT^A08", Count: 200}},
		Segments: []Segment{
			{ID: "MSH", Messages: 500, Fields: []Field{{Path: "MSH-9", Present: 500, FillRate: 1.0, Shape: ShapeAlpha}}},
			{ID: "PID", Messages: 500, Fields: []Field{{Path: "PID-3", Present: 498, FillRate: 0.996, Shape: ShapeAlphanumeric}}},
		},
	}

	sp := ExportProfile(report, "Epic ADT", "ADT feed from Epic 2024", "Epic", "hl7", []string{"adt", "epic"})

	if sp.Format != "perfuse/profile/v1" {
		t.Errorf("format = %q", sp.Format)
	}
	if sp.Name != "Epic ADT" {
		t.Errorf("name = %q", sp.Name)
	}
	if sp.MessageCount != 500 {
		t.Errorf("messageCount = %d", sp.MessageCount)
	}
	if len(sp.Tags) != 2 {
		t.Errorf("tags = %v", sp.Tags)
	}

	// Marshal and unmarshal.
	data, err := MarshalProfile(sp)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := UnmarshalProfile(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Name != "Epic ADT" {
		t.Errorf("after round-trip: name = %q", got.Name)
	}
	if got.MessageCount != 500 {
		t.Errorf("after round-trip: messageCount = %d", got.MessageCount)
	}
	if len(got.Segments) != 2 {
		t.Errorf("after round-trip: segments = %d", len(got.Segments))
	}
	if got.DataType != "hl7" {
		t.Errorf("after round-trip: dataType = %q", got.DataType)
	}
}

func TestUnmarshalProfileRejectsBadFormat(t *testing.T) {
	data := []byte(`{"format":"wrong/v1","name":"test"}`)
	_, err := UnmarshalProfile(data)
	if err == nil {
		t.Fatal("expected error for wrong format")
	}
}

func TestRecipeRoundTrip(t *testing.T) {
	recipe := NewRecipe("Epic to Internal", "Maps Epic ADT to our internal format", "Epic 2024", "Internal EHR", "A. Integrator")
	recipe.Tags = []string{"adt", "epic", "production"}
	recipe.AddMapping(FieldMapping{
		SourcePath: "PID-3.1",
		TargetPath: "PID-3.1",
		Transform:  "",
		Confidence: 100,
		Reasoning:  "MRN passes through unchanged",
		Approved:   true,
	})
	recipe.AddMapping(FieldMapping{
		SourcePath: "PID-5",
		TargetPath: "PID-5",
		Transform:  "format",
		TransformArgs: map[string]string{
			"case": "upper",
		},
		Confidence: 95,
		Reasoning:  "Epic sends mixed case, downstream requires uppercase",
		Approved:   true,
	})
	recipe.AddMapping(FieldMapping{
		SourcePath: "ZPI-1",
		TargetPath: "PID-14",
		Transform:  "copy",
		Confidence: 60,
		Reasoning:  "AI suggested: ZPI-1 looks like a phone number based on pattern",
		Approved:   false,
	})

	data, err := MarshalRecipe(recipe)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	got, err := UnmarshalRecipe(data)
	if err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got.Name != "Epic to Internal" {
		t.Errorf("name = %q", got.Name)
	}
	if got.SourceSystem != "Epic 2024" {
		t.Errorf("sourceSystem = %q", got.SourceSystem)
	}
	if got.Author != "A. Integrator" {
		t.Errorf("author = %q", got.Author)
	}
	if len(got.Mappings) != 3 {
		t.Fatalf("mappings = %d, want 3", len(got.Mappings))
	}

	approved := got.ApprovedMappings()
	if len(approved) != 2 {
		t.Errorf("approved = %d, want 2", len(approved))
	}

	pending := got.PendingMappings()
	if len(pending) != 1 {
		t.Errorf("pending = %d, want 1", len(pending))
	}
	if pending[0].SourcePath != "ZPI-1" {
		t.Errorf("pending[0] source = %q", pending[0].SourcePath)
	}

	high := got.HighConfidence(70)
	if len(high) != 2 {
		t.Errorf("high confidence = %d, want 2", len(high))
	}
}

func TestUnmarshalRecipeRejectsBadFormat(t *testing.T) {
	data := []byte(`{"format":"wrong/v1","name":"test","mappings":[]}`)
	_, err := UnmarshalRecipe(data)
	if err == nil {
		t.Fatal("expected error for wrong format")
	}
}

func TestProfileTimestamp(t *testing.T) {
	report := &Report{Messages: 1, Segments: []Segment{}}
	before := time.Now().UTC()
	sp := ExportProfile(report, "test", "", "", "hl7", nil)
	after := time.Now().UTC()

	if sp.CreatedAt.Before(before) || sp.CreatedAt.After(after) {
		t.Errorf("createdAt %v not between %v and %v", sp.CreatedAt, before, after)
	}
}
