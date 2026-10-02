package fhirserver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/fhir"
)

// The mapping tables, projected as FHIR ConceptMaps.
//
// An integration analyst's real work is deciding what a sending system's codes mean in the receiver's vocabulary, and recording it so
// the next person can tell why. Perfuse already held that: codeset tables in YAML beside the channels, with a reason per entry and
// provenance per table. It was reachable as a file or as a panel in the console and nowhere else.
//
// ConceptMap is what FHIR calls the same thing, and $translate is how a client asks. So the work already being done becomes queryable
// by anything that speaks FHIR, without an export, a spreadsheet, or a second copy to fall out of step.

// ConceptMapBaseURL is the canonical prefix for a projected table.
//
// Local rather than an hl7.org URL, because these mappings are this site's decisions and claiming a canonical URL in somebody else's
// namespace would assert an authority we do not have.
const ConceptMapBaseURL = "urn:perfuse:codeset:"

// TableSource supplies the mapping tables to project.
//
// An interface rather than the concrete set, so the FHIR server does not need to know how channels are loaded. It also means the table
// files stay the single source: nothing here caches them into the database, because a mapping with two sources of truth ends with
// somebody editing the copy that is not running.
type TableSource interface {
	// Tables returns the tables currently loaded, keyed by name.
	Tables() map[string]*codeset.Table
}

// ConceptMapID is the resource id for a table name.
//
// Table names come from YAML and may contain characters a FHIR id may not, so they are constrained here rather than trusted. A
// resource id that FHIR would reject makes the resource unreadable through its own URL.
func ConceptMapID(tableName string) string {
	var b strings.Builder

	for _, r := range tableName {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
			b.WriteRune(r)
		default:
			// Anything else becomes a hyphen. Underscores are the common case in a table name and are not legal in a FHIR id.
			b.WriteRune('-')
		}
	}

	id := strings.Trim(b.String(), "-.")
	if id == "" {
		return "table"
	}
	if len(id) > 64 {
		id = id[:64]
	}

	return id
}

// TableAsConceptMap projects one mapping table.
//
// Every field the table records is carried across where FHIR has somewhere to put it. The per-entry reason becomes the target comment
// and the table's provenance becomes publisher, date and purpose - dropped, they would be unrecoverable, and they are the fields that
// settle arguments about a mapping years later.
func TableAsConceptMap(t *codeset.Table) *fhir.ConceptMap {
	cm := &fhir.ConceptMap{
		URL:         ConceptMapBaseURL + t.Name,
		Name:        t.Name,
		Title:       t.Name,
		Status:      "active",
		Description: t.Describes,
		Publisher:   t.DecidedBy,
		Date:        t.DecidedOn,
		Purpose:     t.Source,
	}
	cm.SetResourceID(ConceptMapID(t.Name))

	group := fhir.ConceptMapGroup{}

	// Entries in the order the table declares them, which is the order somebody wrote and reviewed.
	for _, e := range t.Entries {
		group.Element = append(group.Element, fhir.ConceptMapElement{
			Code: e.From,
			Target: []fhir.ConceptMapTarget{{
				Code: e.To,
				// equivalent, not equal. These are administrative decisions about what a code means here, not assertions
				// that two code systems define the same concept, and overstating that is how a mapping gets reused
				// somewhere it does not hold.
				Equivalence: "equivalent",
				Comment:     entryComment(e),
			}},
		})
	}

	// What happens to a code with no entry, which is the question a client most needs answered and the one a plain list of
	// mappings cannot answer.
	switch {
	case t.Strict:
		// No unmapped element at all: a code outside the table is refused rather than translated or passed through.
		group.Unmapped = nil
	case t.Default != "":
		group.Unmapped = &fhir.ConceptMapUnmapped{Mode: "fixed", Code: t.Default}
	default:
		group.Unmapped = &fhir.ConceptMapUnmapped{Mode: "provided"}
	}

	cm.Group = []fhir.ConceptMapGroup{group}

	return cm
}

// entryComment combines the reason and the date an entry was added.
//
// Both, when both exist. "Because the lab started sending this in 2021" is a more useful answer than either half.
func entryComment(e codeset.Entry) string {
	switch {
	case e.Why != "" && e.Since != "":
		return e.Why + " (since " + e.Since + ")"
	case e.Why != "":
		return e.Why
	case e.Since != "":
		return "added " + e.Since
	default:
		return ""
	}
}

// ConceptMaps projects every table, sorted by id.
//
// Sorted because Go maps range randomly and this is a bundle a client may cache and compare. A list that reorders itself between
// requests looks like the mappings changed.
func ConceptMaps(src TableSource) []*fhir.ConceptMap {
	if src == nil {
		return nil
	}

	tables := src.Tables()

	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)

	out := make([]*fhir.ConceptMap, 0, len(names))
	for _, name := range names {
		out = append(out, TableAsConceptMap(tables[name]))
	}

	return out
}

// FindConceptMap returns the table behind a ConceptMap id, or the URL.
//
// Accepts either, because a client that read a bundle has the id and a client working from a specification has the canonical URL, and
// making them convert between the two is a way of being unhelpful for no reason.
func FindConceptMap(src TableSource, idOrURL string) (*codeset.Table, error) {
	if src == nil {
		return nil, fmt.Errorf("no mapping tables are loaded, so there are no concept maps to translate with")
	}

	want := strings.TrimPrefix(idOrURL, ConceptMapBaseURL)

	tables := src.Tables()

	// The exact table name first.
	if t, ok := tables[want]; ok {
		return t, nil
	}

	// Then by projected id, since that is what appears in a bundle.
	names := make([]string, 0, len(tables))
	for name := range tables {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if ConceptMapID(name) == want {
			return tables[name], nil
		}
	}

	return nil, fmt.Errorf("no mapping table is called %q: the tables loaded are %s",
		idOrURL, strings.Join(names, ", "))
}
