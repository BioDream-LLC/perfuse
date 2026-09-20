package profile

import (
	"bytes"
	"encoding/json"
	"fmt"
	"time"
)

// Shareable profiles and mapping recipes.
//
// The network effect idea: every site that maps a messy Epic or Cerner feed makes
// the next site's job easier. A profile says "this is what Site A's ADT feed actually
// looks like", and a recipe says "here is how we mapped it to our internal format".
// Both are portable JSON files that carry no patient data (profiles report shapes and
// counts, never values; recipes carry field paths and transformation rules).

// SharedProfile is a portable feed profile that can be exported and imported.
// It contains no patient data — only field shapes, fill rates, and code distributions.
type SharedProfile struct {
	// Format identifies what kind of profile this is.
	Format string `json:"format"` // "perfuse/profile/v1"

	// Name is the human-readable name for this profile.
	Name string `json:"name"`

	// Description explains what feed this profile represents.
	Description string `json:"description,omitempty"`

	// Source identifies where this came from (e.g. "Epic ADT", "Cerner Lab").
	Source string `json:"source,omitempty"`

	// Version of the sending system, if known.
	SourceVersion string `json:"sourceVersion,omitempty"`

	// CreatedAt is when this profile was built.
	CreatedAt time.Time `json:"createdAt"`

	// MessageCount is how many messages were profiled.
	MessageCount int `json:"messageCount"`

	// DataType is what kind of data: "hl7", "x12", "hl7v3".
	DataType string `json:"dataType"`

	// MessageTypes lists the message types seen (e.g. ADT^A01, ORU^R01).
	MessageTypes []TypeCount `json:"messageTypes,omitempty"`

	// Segments carries the per-segment field observations.
	Segments []Segment `json:"segments"`

	// Tags are user-defined labels for categorisation and search.
	Tags []string `json:"tags,omitempty"`
}

// MappingRecipe is a portable set of field mappings that can be shared between sites.
// It says "when you see field X in a feed that looks like profile P, map it to Y".
type MappingRecipe struct {
	// Format identifies what kind of recipe this is.
	Format string `json:"format"` // "perfuse/recipe/v1"

	// Name is the human-readable name.
	Name string `json:"name"`

	// Description explains what this recipe does.
	Description string `json:"description,omitempty"`

	// ForProfile names the profile this recipe was built against.
	// Not a hard dependency — the recipe can be applied to any feed — but helps
	// users understand what it was designed for.
	ForProfile string `json:"forProfile,omitempty"`

	// SourceSystem identifies the sending system (e.g. "Epic 2024", "Cerner Millennium").
	SourceSystem string `json:"sourceSystem,omitempty"`

	// TargetSystem identifies the receiving system (e.g. "Internal FHIR", "Downstream Lab").
	TargetSystem string `json:"targetSystem,omitempty"`

	// CreatedAt is when this recipe was first authored.
	CreatedAt time.Time `json:"createdAt"`

	// Author is who wrote or approved this mapping.
	Author string `json:"author,omitempty"`

	// Mappings are the individual field transformations.
	Mappings []FieldMapping `json:"mappings"`

	// Tags for categorisation.
	Tags []string `json:"tags,omitempty"`
}

// FieldMapping is one mapping rule: take a source path and produce a target.
type FieldMapping struct {
	// SourcePath is the field in the incoming message (e.g. "PID-3.1", "OBX-5").
	SourcePath string `json:"sourcePath"`

	// TargetPath is where the value goes in the output (e.g. "PID-3.1", or a FHIR path).
	TargetPath string `json:"targetPath"`

	// Transform describes what happens to the value. Empty means copy as-is.
	// Other values: "map" (use a code table), "format" (date/time reformatting),
	// "split" (break a composite), "concatenate", "truncate", "default" (use value if empty).
	Transform string `json:"transform,omitempty"`

	// TransformArgs carries parameters for the transform (e.g. the table name for "map").
	TransformArgs map[string]string `json:"transformArgs,omitempty"`

	// Condition is an optional expression that must be true for this mapping to apply.
	// Uses the same filter expression syntax as channel filters.
	Condition string `json:"condition,omitempty"`

	// Confidence is how sure we are this mapping is correct (0-100).
	// Set by the AI mapper when it suggests a mapping, or 100 for human-authored.
	Confidence int `json:"confidence"`

	// Reasoning explains why this mapping exists (human or AI-authored).
	Reasoning string `json:"reasoning,omitempty"`

	// Approved indicates a human reviewed and accepted this mapping.
	Approved bool `json:"approved"`
}

// ExportProfile converts a Report into a portable SharedProfile.
func ExportProfile(r *Report, name, description, source, dataType string, tags []string) *SharedProfile {
	return &SharedProfile{
		Format:       "perfuse/profile/v1",
		Name:         name,
		Description:  description,
		Source:       source,
		CreatedAt:    time.Now().UTC(),
		MessageCount: r.Messages,
		DataType:     dataType,
		MessageTypes: r.Types,
		Segments:     r.Segments,
		Tags:         tags,
	}
}

// MarshalProfile serialises a SharedProfile to JSON.
func MarshalProfile(p *SharedProfile) ([]byte, error) {
	return json.MarshalIndent(p, "", "  ")
}

// UnmarshalProfile deserialises a SharedProfile from JSON.
//
// Unknown fields are rejected rather than silently dropped. The house rule (docs/queue.md)
// is that an unknown key is always an error: an imported profile that silently loses a field
// produces a channel that differs from the one that was shared, which defeats the purpose of
// sharing.
func UnmarshalProfile(data []byte) (*SharedProfile, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var p SharedProfile
	if err := dec.Decode(&p); err != nil {
		return nil, fmt.Errorf("profile: %w", err)
	}
	if p.Format != "perfuse/profile/v1" {
		return nil, fmt.Errorf("profile: unrecognised format %q (expected perfuse/profile/v1)", p.Format)
	}
	return &p, nil
}

// MarshalRecipe serialises a MappingRecipe to JSON.
func MarshalRecipe(r *MappingRecipe) ([]byte, error) {
	return json.MarshalIndent(r, "", "  ")
}

// UnmarshalRecipe deserialises a MappingRecipe from JSON.
//
// Unknown fields are rejected. Same house rule as UnmarshalProfile: an imported recipe
// that silently drops a field is a recipe that behaves differently from what was shared.
func UnmarshalRecipe(data []byte) (*MappingRecipe, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var r MappingRecipe
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("recipe: %w", err)
	}
	if r.Format != "perfuse/recipe/v1" {
		return nil, fmt.Errorf("recipe: unrecognised format %q (expected perfuse/recipe/v1)", r.Format)
	}
	return &r, nil
}

// NewRecipe creates a new empty recipe.
func NewRecipe(name, description, sourceSystem, targetSystem, author string) *MappingRecipe {
	return &MappingRecipe{
		Format:       "perfuse/recipe/v1",
		Name:         name,
		Description:  description,
		SourceSystem: sourceSystem,
		TargetSystem: targetSystem,
		Author:       author,
		CreatedAt:    time.Now().UTC(),
		Mappings:     []FieldMapping{},
	}
}

// AddMapping adds a mapping to a recipe.
func (r *MappingRecipe) AddMapping(m FieldMapping) {
	r.Mappings = append(r.Mappings, m)
}

// ApprovedMappings returns only the mappings that have been human-approved.
func (r *MappingRecipe) ApprovedMappings() []FieldMapping {
	var out []FieldMapping
	for _, m := range r.Mappings {
		if m.Approved {
			out = append(out, m)
		}
	}
	return out
}

// PendingMappings returns mappings that are suggested but not yet approved.
func (r *MappingRecipe) PendingMappings() []FieldMapping {
	var out []FieldMapping
	for _, m := range r.Mappings {
		if !m.Approved {
			out = append(out, m)
		}
	}
	return out
}

// HighConfidence returns mappings above a confidence threshold.
func (r *MappingRecipe) HighConfidence(threshold int) []FieldMapping {
	var out []FieldMapping
	for _, m := range r.Mappings {
		if m.Confidence >= threshold {
			out = append(out, m)
		}
	}
	return out
}
