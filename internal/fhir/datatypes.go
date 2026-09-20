package fhir

import "strings"

// FHIR datatypes.
//
// Every optional field is a pointer or a slice so that "absent" and "present but
// empty" stay distinguishable. In clinical data that difference carries meaning:
// an absent field means the sender said nothing, and an empty one means the
// sender said there is nothing. Collapsing them loses information a downstream
// system may act on.

// Element is the base of every datatype: an id and extensions.
type Element struct {
	ID        string      `json:"id,omitempty"`
	Extension []Extension `json:"extension,omitempty"`
}

// Extension carries data the specification does not define. Exactly one value
// field is set.
type Extension struct {
	URL string `json:"url"`

	ValueString    *string          `json:"valueString,omitempty"`
	ValueCode      *string          `json:"valueCode,omitempty"`
	ValueBoolean   *bool            `json:"valueBoolean,omitempty"`
	ValueInteger   *int             `json:"valueInteger,omitempty"`
	ValueDecimal   *float64         `json:"valueDecimal,omitempty"`
	ValueDateTime  *string          `json:"valueDateTime,omitempty"`
	ValueCoding    *Coding          `json:"valueCoding,omitempty"`
	ValueCodeable  *CodeableConcept `json:"valueCodeableConcept,omitempty"`
	ValueQuantity  *Quantity        `json:"valueQuantity,omitempty"`
	ValueReference *Reference       `json:"valueReference,omitempty"`

	Extension []Extension `json:"extension,omitempty"`
}

// Meta holds resource metadata, including the profiles it claims to conform to.
type Meta struct {
	VersionID   string   `json:"versionId,omitempty"`
	LastUpdated string   `json:"lastUpdated,omitempty"`
	Source      string   `json:"source,omitempty"`
	Profile     []string `json:"profile,omitempty"`
	Security    []Coding `json:"security,omitempty"`
	Tag         []Coding `json:"tag,omitempty"`
}

// Narrative is the human-readable form of a resource.
type Narrative struct {
	Status string `json:"status"`
	Div    string `json:"div"`
}

// Coding is one code from one code system.
type Coding struct {
	System       string `json:"system,omitempty"`
	Version      string `json:"version,omitempty"`
	Code         string `json:"code,omitempty"`
	Display      string `json:"display,omitempty"`
	UserSelected *bool  `json:"userSelected,omitempty"`
}

// CodeableConcept is a concept expressed as codings plus the original text.
//
// Text matters. When a v2 message carries a local code that cannot be mapped, the
// honest result is the original text with no coding, not an invented code.
type CodeableConcept struct {
	Coding []Coding `json:"coding,omitempty"`
	Text   string   `json:"text,omitempty"`
}

// NewCodeableConcept builds a concept from one code.
func NewCodeableConcept(system, code, display string) *CodeableConcept {
	if code == "" && display == "" {
		return nil
	}
	if code == "" {
		return &CodeableConcept{Text: display}
	}
	return &CodeableConcept{
		Coding: []Coding{{System: system, Code: code, Display: display}},
		Text:   display,
	}
}

// TextOnly builds a concept carrying only original text, for a value that could
// not be coded.
func TextOnly(text string) *CodeableConcept {
	if text == "" {
		return nil
	}
	return &CodeableConcept{Text: text}
}

// Identifier is a business identifier such as a medical record number.
type Identifier struct {
	Use      string           `json:"use,omitempty"`
	Type     *CodeableConcept `json:"type,omitempty"`
	System   string           `json:"system,omitempty"`
	Value    string           `json:"value,omitempty"`
	Period   *Period          `json:"period,omitempty"`
	Assigner *Reference       `json:"assigner,omitempty"`
}

// Reference points at another resource.
type Reference struct {
	Reference  string      `json:"reference,omitempty"`
	Type       string      `json:"type,omitempty"`
	Identifier *Identifier `json:"identifier,omitempty"`
	Display    string      `json:"display,omitempty"`
}

// Ref builds a relative reference, which is what belongs inside a bundle.
func Ref(resourceType, id string) *Reference {
	if id == "" {
		return nil
	}
	return &Reference{Reference: resourceType + "/" + id, Type: resourceType}
}

// Period is a time range. Either end may be absent.
type Period struct {
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`
}

// HumanName is a name, with the parts kept separate.
type HumanName struct {
	// Extension carries, among other things, the data absent reason - which US Core invariant us-core-6 names as the sanctioned way
	// to record that a patient has neither a family nor a given name. That happens: an unidentified patient in an emergency
	// department. Without this field there was no way to represent it, so a legitimate resource could not be expressed.
	Extension []Extension `json:"extension,omitempty"`

	Use    string   `json:"use,omitempty"`
	Text   string   `json:"text,omitempty"`
	Family string   `json:"family,omitempty"`
	Given  []string `json:"given,omitempty"`
	Prefix []string `json:"prefix,omitempty"`
	Suffix []string `json:"suffix,omitempty"`
	Period *Period  `json:"period,omitempty"`
}

// Address is a postal address.
type Address struct {
	Use        string   `json:"use,omitempty"`
	Type       string   `json:"type,omitempty"`
	Text       string   `json:"text,omitempty"`
	Line       []string `json:"line,omitempty"`
	City       string   `json:"city,omitempty"`
	District   string   `json:"district,omitempty"`
	State      string   `json:"state,omitempty"`
	PostalCode string   `json:"postalCode,omitempty"`
	Country    string   `json:"country,omitempty"`
	Period     *Period  `json:"period,omitempty"`
}

// ContactPoint is a phone number, email address or similar.
type ContactPoint struct {
	System string  `json:"system,omitempty"`
	Value  string  `json:"value,omitempty"`
	Use    string  `json:"use,omitempty"`
	Rank   *int    `json:"rank,omitempty"`
	Period *Period `json:"period,omitempty"`
}

// Quantity is a measured amount with a unit.
//
// Unit, system and code are separate on purpose. A lab result carrying "mg/dL" as
// text is not the same as one carrying the UCUM code for it, and a receiving
// system can only convert safely when the coded form is present.
type Quantity struct {
	Value      *float64 `json:"value,omitempty"`
	Comparator string   `json:"comparator,omitempty"`
	Unit       string   `json:"unit,omitempty"`
	System     string   `json:"system,omitempty"`
	Code       string   `json:"code,omitempty"`
}

// Range is a low to high interval, used for reference ranges.
type Range struct {
	Low  *Quantity `json:"low,omitempty"`
	High *Quantity `json:"high,omitempty"`
}

// Ratio is a numerator over a denominator, used for titres.
type Ratio struct {
	Numerator   *Quantity `json:"numerator,omitempty"`
	Denominator *Quantity `json:"denominator,omitempty"`
}

// Annotation is a free-text note with optional authorship.
type Annotation struct {
	AuthorString    string     `json:"authorString,omitempty"`
	AuthorReference *Reference `json:"authorReference,omitempty"`
	Time            string     `json:"time,omitempty"`
	Text            string     `json:"text"`
}

// Attachment carries data or a link to it.
type Attachment struct {
	ContentType string `json:"contentType,omitempty"`
	Language    string `json:"language,omitempty"`
	Data        string `json:"data,omitempty"`
	URL         string `json:"url,omitempty"`
	Size        *int64 `json:"size,omitempty"`
	Title       string `json:"title,omitempty"`
	Creation    string `json:"creation,omitempty"`
}

// CodeableReference is R5's combined code-or-reference type. In R4 output it is
// flattened to whichever half is populated, because R4 has no such datatype.
type CodeableReference struct {
	Concept   *CodeableConcept `json:"concept,omitempty"`
	Reference *Reference       `json:"reference,omitempty"`
}

// Helpers for the pointer-heavy shape above. They exist so mapping code reads as
// mapping rather than as pointer plumbing.

// Str returns a pointer to s, or nil when s is empty.
func Str(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// Bool returns a pointer to b.
func Bool(b bool) *bool { return &b }

// Int returns a pointer to n.
func Int(n int) *int { return &n }

// Float returns a pointer to f.
func Float(f float64) *float64 { return &f }

// NonEmpty filters empty strings out of a slice, so an absent name part does not
// become an empty array entry.
func NonEmpty(values ...string) []string {
	var out []string
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			out = append(out, v)
		}
	}
	return out
}
