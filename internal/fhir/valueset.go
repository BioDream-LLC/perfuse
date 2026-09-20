package fhir

// ValueSet is a set of codes drawn from one or more code systems.
//
// Present so Perfuse can answer what a mapping accepts and what it can produce. Those are two different sets and a client asking about
// one should never be handed the other.
//
// Only ever returned already expanded. A ValueSet in FHIR may describe its membership by rules - include this system, exclude these
// codes - and leave the expansion to the server. Here the membership is a finite list read from a mapping table, so the rules would be a
// less precise way of saying the same thing, and a client would have to ask a second time to find out what it meant.
type ValueSet struct {
	base

	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	Title       string `json:"title,omitempty"`
	Status      string `json:"status,omitempty"`
	Date        string `json:"date,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	Description string `json:"description,omitempty"`

	// Expansion is the resolved membership.
	Expansion *ValueSetExpansion `json:"expansion,omitempty"`
}

// ValueSetExpansion is the enumerated membership of a value set.
type ValueSetExpansion struct {
	// Identifier says which value set this expansion is of, so an expansion separated from its request can still be placed.
	Identifier string `json:"identifier,omitempty"`

	// Total is how many codes the set contains.
	//
	// Carried separately from the list because a bounded expansion returns fewer entries than the set holds, and a client counting
	// the entries it received would conclude the set is smaller than it is.
	Total *int `json:"total,omitempty"`

	Contains []ValueSetContains `json:"contains,omitempty"`
}

// ValueSetContains is one code in an expansion.
type ValueSetContains struct {
	System  string `json:"system,omitempty"`
	Code    string `json:"code,omitempty"`
	Display string `json:"display,omitempty"`
}

// ResourceTypeName implements Resource.
func (v *ValueSet) ResourceTypeName() string { return "ValueSet" }

// ResourceID implements Resource.
func (v *ValueSet) ResourceID() string { return v.ID }

// SetResourceID implements Resource.
func (v *ValueSet) SetResourceID(id string) { v.ID = id; v.ResourceType = "ValueSet" }
