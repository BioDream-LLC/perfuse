package fhir

// Parameters is the wrapper FHIR operations use for their input and output.
//
// Needed because an operation like $translate does not return a resource - it returns an answer about one, and FHIR has exactly one
// shape for that. Returning something of our own invention instead would make every client special-case this server, which defeats the
// purpose of speaking the standard at all.
type Parameters struct {
	base

	Parameter []ParametersParameter `json:"parameter,omitempty"`
}

// ParametersParameter is one named value, or a group of them.
//
// The value fields are separate rather than one any, because FHIR names the type in the field - valueString and valueBoolean are
// different keys, not one key with different contents - and a client parsing valueBoolean will not find a value hidden under valueString.
type ParametersParameter struct {
	Name string `json:"name"`

	ValueString  string  `json:"valueString,omitempty"`
	ValueBoolean *bool   `json:"valueBoolean,omitempty"`
	ValueCode    string  `json:"valueCode,omitempty"`
	ValueUri     string  `json:"valueUri,omitempty"`
	ValueInteger *int    `json:"valueInteger,omitempty"`
	ValueCoding  *Coding `json:"valueCoding,omitempty"`

	// Part holds nested parameters, which is how a match with several components is expressed.
	Part []ParametersParameter `json:"part,omitempty"`
}

// ResourceTypeName implements Resource.
func (p *Parameters) ResourceTypeName() string { return "Parameters" }

// ResourceID implements Resource.
func (p *Parameters) ResourceID() string { return p.ID }

// SetResourceID implements Resource.
func (p *Parameters) SetResourceID(id string) { p.ID = id; p.ResourceType = "Parameters" }
