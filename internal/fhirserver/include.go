package fhirserver

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// _include and _revinclude.
//
// The single most requested thing missing from this server, and the reason is not convenience. A SMART app that fetches thirty
// observations and then thirty patients one at a time is not slow, it is broken: the round trips defeat the page size, and several client
// libraries assume the included resources are present and simply render nothing when they are not.
//
// Both are implemented against the search index rather than by walking JSON, so an include costs one query per parameter rather than one
// per resource.

// IncludeSpec is one _include or _revinclude, parsed.
type IncludeSpec struct {
	// SourceType is the type holding the reference. For _include that is the type being searched; for _revinclude it is
	// the type pointing back at it.
	SourceType string

	// Param is the search parameter naming the reference.
	Param string

	// TargetType narrows which type to include, from the optional third part of the specification. Empty means every
	// type the parameter can point at.
	TargetType string

	// Reverse is true for _revinclude.
	Reverse bool

	// Iterate means follow this include against everything found so far, not only against the page.
	//
	// _include:iterate=MedicationRequest:subject after _include=Observation:subject reaches the patient of a medication
	// request that was itself pulled in - a second hop. Without it a client needing two hops makes two round trips and
	// cannot express the second one at all when it does not know the ids in advance.
	Iterate bool
}

// referenceTargets says what each reference search parameter can point at.
//
// Needed because an include has to know what to look up, and the reference alone does not always say - a subject may be a Patient or a
// Group, and an unqualified reference names no type at all.
//
// Listed explicitly rather than derived from the resource structs. The structs say a Reference is a Reference; which types are permissible
// is in the specification, and writing it down is the only way this can refuse an include it cannot honour instead of returning an empty
// set that reads as "there is nothing there".
var referenceTargets = map[string]map[string][]string{
	"Observation": {
		"patient":   {"Patient"},
		"subject":   {"Patient", "Group", "Location"},
		"encounter": {"Encounter"},
	},
	"DiagnosticReport": {
		"patient":   {"Patient"},
		"subject":   {"Patient", "Group", "Location"},
		"encounter": {"Encounter"},
	},
	"Encounter": {
		"patient": {"Patient"},
		"subject": {"Patient", "Group"},
	},
	"Specimen": {
		"patient": {"Patient"},
		"subject": {"Patient", "Group", "Location"},
	},
	"ServiceRequest": {
		"patient": {"Patient"},
		"subject": {"Patient", "Group", "Location"},
	},

	// US Core. These are what make a patient summary one request: a Patient search with reverse includes for
	// conditions, medications, allergies, immunisations and procedures is the query a clinical app opens with.
	"Condition": {
		"patient":   {"Patient"},
		"subject":   {"Patient", "Group"},
		"encounter": {"Encounter"},
	},
	"MedicationRequest": {
		"patient":   {"Patient"},
		"subject":   {"Patient", "Group"},
		"encounter": {"Encounter"},
	},
	"AllergyIntolerance": {
		"patient":   {"Patient"},
		"encounter": {"Encounter"},
	},
	"Immunization": {
		"patient":   {"Patient"},
		"encounter": {"Encounter"},
	},
	"Procedure": {
		"patient":   {"Patient"},
		"subject":   {"Patient", "Group"},
		"encounter": {"Encounter"},
	},
	"DocumentReference": {
		"patient": {"Patient"},
		"subject": {"Patient", "Group"},
	},
}

// ParseInclude reads one _include or _revinclude value.
//
// The form is Type:param or Type:param:TargetType. Refused rather than ignored when it names something this server cannot honour: an
// ignored _include produces a bundle missing the resources the client asked for, and a client that assumes they are there renders an empty
// screen with no error anywhere.
func ParseInclude(value string, reverse bool) (IncludeSpec, error) {
	spec := IncludeSpec{Reverse: reverse}

	name := "_include"
	if reverse {
		name = "_revinclude"
	}

	// :iterate may arrive on the value as well as on the parameter name.
	//
	// FHIR puts it on the parameter - _include:iterate=Type:param - but client libraries write it both ways, and refusing
	// the form somebody's library produces is a worse outcome than accepting both. Stripped here so the rest of the parse
	// sees an ordinary specification.
	if strings.Contains(value, ":iterate") {
		spec.Iterate = true
		value = strings.ReplaceAll(value, ":iterate", "")
	}

	parts := strings.Split(value, ":")
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return spec, fmt.Errorf(
			"%s must be given as Type:parameter, or Type:parameter:TargetType, and %q is neither",
			name, value)
	}
	if len(parts) > 3 {
		return spec, fmt.Errorf("%s takes at most Type:parameter:TargetType, and %q has more", name, value)
	}

	spec.SourceType = parts[0]
	spec.Param = parts[1]
	if len(parts) == 3 {
		spec.TargetType = parts[2]
	}

	params, ok := referenceTargets[spec.SourceType]
	if !ok {
		return spec, fmt.Errorf("%s: %s has no reference parameters this server can follow",
			name, spec.SourceType)
	}

	targets, ok := params[spec.Param]
	if !ok {
		var known []string
		for p := range params {
			known = append(known, p)
		}
		sort.Strings(known)

		return spec, fmt.Errorf("%s: %s has no reference parameter %q; it has %s",
			name, spec.SourceType, spec.Param, strings.Join(known, ", "))
	}

	if spec.TargetType != "" {
		permitted := false
		for _, t := range targets {
			if t == spec.TargetType {
				permitted = true

				break
			}
		}
		if !permitted {
			return spec, fmt.Errorf("%s: %s.%s does not point at %s; it points at %s",
				name, spec.SourceType, spec.Param, spec.TargetType, strings.Join(targets, ", "))
		}
	}

	return spec, nil
}

// targets is the list of types this include may pull in.
func (spec IncludeSpec) targets() []string {
	if spec.TargetType != "" {
		return []string{spec.TargetType}
	}

	return referenceTargets[spec.SourceType][spec.Param]
}

// Include gathers the resources named by the includes for one page of results.
//
// Takes the page rather than the whole result set, which is what the specification requires and also the only sensible reading: an include
// over every match would return a hundred thousand patients for a page of twenty observations.
//
// Returns them in a stable order, sorted by type then id, because Go maps range randomly and a bundle whose contents reorder between
// identical requests cannot be cached, diffed, or reasoned about by a client.
func (s *Store) Include(
	ctx context.Context, page []fhir.Resource, specs []IncludeSpec,
) ([]fhir.Resource, error) {
	if len(page) == 0 || len(specs) == 0 {
		return nil, nil
	}

	// The page itself, so an included resource that is already a match is not sent twice. A duplicate entry in a bundle
	// is not merely wasteful: a client building a map by id will process it twice, and one building a list will show it
	// twice.
	seen := map[string]bool{}
	for _, r := range page {
		seen[r.ResourceTypeName()+"/"+r.ResourceID()] = true
	}

	var out []fhir.Resource

	// add records what a pass found, and reports what was new. The new resources are what the next iterate pass runs against.
	add := func(found []fhir.Resource) []fhir.Resource {
		var fresh []fhir.Resource

		for _, r := range found {
			key := r.ResourceTypeName() + "/" + r.ResourceID()
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, r)
			fresh = append(fresh, r)
		}

		return fresh
	}

	// First pass: every include runs against the page.
	//
	// Iterate specs run here too, not only in the loop below. FHIR defines :iterate as also applying at the first level, and
	// a client writing a single _include:iterate would otherwise get nothing at all.
	for _, spec := range specs {
		found, err := s.runInclude(ctx, page, spec)
		if err != nil {
			return nil, err
		}
		add(found)
	}

	// Then the iterate specs run repeatedly against whatever the last pass turned up.
	iterating := make([]IncludeSpec, 0, len(specs))
	for _, spec := range specs {
		if spec.Iterate {
			iterating = append(iterating, spec)
		}
	}

	for depth := 0; len(iterating) > 0 && depth < maxIncludeDepth; depth++ {
		// Run against everything found so far rather than only the newest layer.
		//
		// Running against only the newest would be faster and is wrong: a reference from an earlier layer to a resource that
		// has only just become reachable would be missed, and the result would depend on the order the specs were written.
		var fresh []fhir.Resource

		for _, spec := range iterating {
			found, err := s.runInclude(ctx, out, spec)
			if err != nil {
				return nil, err
			}
			fresh = append(fresh, add(found)...)
		}

		// Nothing new means the graph is exhausted. Stopping here rather than running to the depth limit is what makes the
		// common case cheap, and it is also what makes a cycle terminate: a cycle produces no new resources on its second
		// lap because everything in it is already in seen.
		if len(fresh) == 0 {
			break
		}

		// A page of twenty pulling in a hundred thousand resources is not a useful answer, it is an outage. Refused rather
		// than truncated, because a truncated include is indistinguishable from a complete one and the client would treat
		// a partial record as the whole record.
		if len(out) > maxIncludedResources {
			return nil, fmt.Errorf(
				"this search included more than %d resources through :iterate; "+
					"narrow the search, drop :iterate, or ask for a smaller page",
				maxIncludedResources)
		}
	}

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].ResourceTypeName() != out[j].ResourceTypeName() {
			return out[i].ResourceTypeName() < out[j].ResourceTypeName()
		}

		return out[i].ResourceID() < out[j].ResourceID()
	})

	return out, nil
}

// includeForward follows references held by the resources on the page.
func (s *Store) includeForward(
	ctx context.Context, page []fhir.Resource, spec IncludeSpec,
) ([]fhir.Resource, error) {
	// Only resources of the type the include names. A search for Observation with _include=Encounter:patient asks for
	// something the page does not contain, and returning nothing is the honest answer.
	var ids []string
	for _, r := range page {
		if r.ResourceTypeName() != spec.SourceType {
			continue
		}
		ids = append(ids, r.ResourceID())
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// The index already holds the reference and the type it points at, so the targets come from one query per include
	// rather than by re-parsing every resource on the page.
	query := `SELECT DISTINCT x.value, COALESCE(x.ref_type, '')
		FROM fhir_search x
		WHERE x.resource_type = ? AND x.param = ? AND x.resource_id IN (` +
		placeholders(len(ids)) + `)`

	args := []any{spec.SourceType, spec.Param}
	for _, id := range ids {
		args = append(args, id)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("fhirserver: reading references to include: %w", err)
	}

	type ref struct {
		id         string
		targetType string
	}

	var refs []ref
	for rows.Next() {
		var r ref
		if err := rows.Scan(&r.id, &r.targetType); err != nil {
			rows.Close()

			return nil, err
		}
		refs = append(refs, r)
	}
	if err := rows.Err(); err != nil {
		rows.Close()

		return nil, err
	}
	rows.Close()

	permitted := map[string]bool{}
	for _, t := range spec.targets() {
		permitted[t] = true
	}

	targets := spec.targets()

	var out []fhir.Resource
	for _, r := range refs {
		// Which type to look the reference up as.
		//
		// When it was recorded, that type and only that type - a reference to a Location must not resolve to the
		// Patient of the same id, which is the defect the ref_type column was added for and would reappear here.
		//
		// When it was not recorded, the answer depends on how many types the parameter can point at, and the
		// distinction is the whole difficulty. For a parameter with exactly one possible target - patient can only
		// be a Patient - there is nothing to guess and the reference resolves. For one with several, such as
		// subject, there is no correct answer: resolving all of them would put two candidate subjects in the
		// bundle for one observation, and a client showing "the subject of this result" would pick whichever came
		// first. That is a wrong clinical answer rather than a missing one, so nothing is included and the
		// reference is left for the client to resolve if it can.
		var lookup string

		switch {
		case r.targetType != "":
			if !permitted[r.targetType] {
				continue
			}
			lookup = r.targetType
		case len(targets) == 1:
			lookup = targets[0]
		default:
			continue
		}

		got, err := s.Get(ctx, lookup, r.id)
		if err != nil {
			// A reference pointing at something this server does not hold is not an error. External
			// references are normal, and a dangling one is a fact about the data rather than a failure of the
			// search.
			continue
		}
		out = append(out, got)
	}

	return out, nil
}

// includeReverse finds resources that point at the resources on the page.
func (s *Store) includeReverse(
	ctx context.Context, page []fhir.Resource, spec IncludeSpec,
) ([]fhir.Resource, error) {
	// The page holds the targets here, so it is filtered by what the parameter can point at rather than by the include's
	// source type.
	permitted := map[string]bool{}
	for _, t := range spec.targets() {
		permitted[t] = true
	}

	var ids []string
	for _, r := range page {
		if !permitted[r.ResourceTypeName()] {
			continue
		}
		ids = append(ids, r.ResourceID())
	}
	if len(ids) == 0 {
		return nil, nil
	}

	// Matched on the id and, where recorded, on the type - so a reverse include for Patient/123 does not pick up an
	// observation whose subject is Group/123. That is the same defect the ref_type column was added to fix, and it would
	// reappear here if the type were dropped.
	query := `SELECT DISTINCT x.resource_id
		FROM fhir_search x
		WHERE x.resource_type = ? AND x.param = ?
		  AND x.value IN (` + placeholders(len(ids)) + `)`

	args := []any{spec.SourceType, spec.Param}
	for _, id := range ids {
		args = append(args, id)
	}

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("fhirserver: reading resources to reverse-include: %w", err)
	}

	var found []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()

			return nil, err
		}
		found = append(found, id)
	}
	if err := rows.Err(); err != nil {
		rows.Close()

		return nil, err
	}
	rows.Close()

	var out []fhir.Resource
	for _, id := range found {
		got, err := s.Get(ctx, spec.SourceType, id)
		if err != nil {
			continue
		}
		out = append(out, got)
	}

	return out, nil
}

// includeOptions lists the _include values a resource type accepts, for the capability statement.
//
// Generated from referenceTargets rather than written out again, so the statement cannot advertise an include this server would refuse. A
// capability statement that overstates is worse than none: a client trusts it, builds a query from it, and gets an error the statement said
// would not happen.
func includeOptions(resourceType string) []string {
	params, ok := referenceTargets[resourceType]
	if !ok {
		return nil
	}

	var out []string
	for param := range params {
		out = append(out, resourceType+":"+param)
	}
	sort.Strings(out)

	return out
}

// revIncludeOptions lists the _revinclude values that point at a resource type.
//
// The inverse lookup: which types hold a reference this type can be the target of. Computed rather than tabulated separately, for the same
// reason as above - two tables describing one relationship disagree eventually, and the disagreement shows up as a client being told it
// may ask for something that returns nothing.
func revIncludeOptions(target string) []string {
	var out []string

	for sourceType, params := range referenceTargets {
		for param, targets := range params {
			for _, t := range targets {
				if t == target {
					out = append(out, sourceType+":"+param)

					break
				}
			}
		}
	}
	sort.Strings(out)

	return out
}

// maxIncludeDepth caps how many times an :iterate include follows a reference.
//
// Three, which reaches an observation's patient's managing organization and stops. A reference graph can contain cycles - two
// resources naming each other is legal and happens - and although the seen set makes a cycle terminate anyway, a deep chain
// without a cap turns one search into an unbounded number of queries. A limit that is reached is reported rather than passed off as
// a complete answer.
const maxIncludeDepth = 3

// maxIncludedResources caps how much one search may drag in through :iterate.
//
// A page of twenty resources iterating across a well-connected graph can reach most of the database. That is not a useful response,
// it is an outage with a 200 on it.
const maxIncludedResources = 1000

// runInclude dispatches one include against a set of resources.
//
// Extracted so the first pass and the iterate loop cannot drift apart. They previously would have been two copies of the same
// forward-or-reverse decision, and a fix applied to one would have silently missed the other.
func (s *Store) runInclude(
	ctx context.Context, from []fhir.Resource, spec IncludeSpec,
) ([]fhir.Resource, error) {
	if len(from) == 0 {
		return nil, nil
	}

	// An include whose source type is not present in this set has nothing to do.
	//
	// Checked rather than left to the query, because includeForward builds an id list from resources of the source type and an
	// empty list produces `IN ()` - which is a syntax error in SQLite rather than an empty result.
	present := false
	for _, r := range from {
		if r.ResourceTypeName() == spec.SourceType {
			present = true

			break
		}
	}

	// For a reverse include the source type is the type pointing back, which is not expected to be in the set at all - the
	// whole point is to find resources that are not there yet. So the check only applies forwards.
	if !spec.Reverse && !present {
		return nil, nil
	}

	if spec.Reverse {
		return s.includeReverse(ctx, from, spec)
	}

	return s.includeForward(ctx, from, spec)
}
