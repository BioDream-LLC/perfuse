package fhir

// ConceptMap is a mapping from one set of codes to another.
//
// Present because Perfuse already holds mappings - the codeset tables a channel uses to turn a sending system's sex codes, or
// department codes, or result codes into the ones the receiver expects. That is the daily work of an integration analyst, and it was
// only reachable as YAML beside the channels or as a panel in the console.
//
// A ConceptMap is what that work is called in FHIR. Projecting the tables into one means any FHIR client can ask what a code becomes,
// and $translate answers it, without anybody exporting a spreadsheet.
//
// Read-only, and derived from the table files rather than stored. The tables are configuration - they travel with the channels into
// version control - and the standing rule here is that the file wins. A ConceptMap that could be written through the API would give a
// mapping two sources of truth, and the one somebody edited would not be the one running.
type ConceptMap struct {
	base

	// URL is the canonical identity of this map.
	URL string `json:"url,omitempty"`

	// Name is the computable name, and Title the human one.
	Name  string `json:"name,omitempty"`
	Title string `json:"title,omitempty"`

	// Status is draft, active or retired. A table in use is active.
	Status string `json:"status,omitempty"`

	// Date is when the mapping was decided, where the table records it.
	Date string `json:"date,omitempty"`

	// Publisher carries who decided the mapping, which the table records as decided_by.
	//
	// Worth carrying across rather than dropping. "Because you asked us to, in 2019" is the answer to most arguments about a
	// mapping, and it is only available if somebody wrote it down and nothing threw it away in between.
	Publisher string `json:"publisher,omitempty"`

	// Description says what the table is for, in words.
	Description string `json:"description,omitempty"`

	// Purpose carries the table's recorded source - a specification, a conversation, an observed feed.
	Purpose string `json:"purpose,omitempty"`

	// Group holds the mappings.
	Group []ConceptMapGroup `json:"group,omitempty"`
}

// ConceptMapGroup maps codes from one system to another.
type ConceptMapGroup struct {
	Source  string              `json:"source,omitempty"`
	Target  string              `json:"target,omitempty"`
	Element []ConceptMapElement `json:"element,omitempty"`

	// Unmapped says what happens to a code with no entry.
	Unmapped *ConceptMapUnmapped `json:"unmapped,omitempty"`
}

// ConceptMapElement is one source code and what it becomes.
type ConceptMapElement struct {
	Code    string             `json:"code,omitempty"`
	Display string             `json:"display,omitempty"`
	Target  []ConceptMapTarget `json:"target,omitempty"`
}

// ConceptMapTarget is the code a source code maps to.
type ConceptMapTarget struct {
	Code    string `json:"code,omitempty"`
	Display string `json:"display,omitempty"`

	// Equivalence describes how close the match is. R4 spelling; R5 renamed it to relationship.
	Equivalence string `json:"equivalence,omitempty"`

	// Comment carries the per-entry reason the table records as why.
	//
	// Kept because it is the most valuable field in the table and the least recoverable. A table of twelve sex codes has one strange
	// row, and that row is the one somebody will want to delete in three years.
	Comment string `json:"comment,omitempty"`
}

// ConceptMapUnmapped says what to do with a code that has no mapping.
//
// mode is "fixed" when the table has a default, "provided" when it passes the code through unchanged, and absent when the table is
// strict and an unmapped code is an error. Those are the three things a codeset table can do, and they are distinguishable here rather
// than collapsed - a client needs to know whether an unrecognised code will be translated, passed through, or refused.
type ConceptMapUnmapped struct {
	Mode string `json:"mode,omitempty"`
	Code string `json:"code,omitempty"`
}

// ResourceTypeName implements Resource.
func (c *ConceptMap) ResourceTypeName() string { return "ConceptMap" }

// ResourceID implements Resource.
func (c *ConceptMap) ResourceID() string { return c.ID }

// SetResourceID implements Resource.
func (c *ConceptMap) SetResourceID(id string) { c.ID = id; c.ResourceType = "ConceptMap" }
