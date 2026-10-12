package fhirserver

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// Reverse chaining: _has.
//
// Practitioner?_has:PractitionerRole:practitioner:location=Location/x finds the practitioners some PractitionerRole names in
// its practitioner element, among the PractitionerRoles whose location is Location/x. Da Vinci Plan-Net needs it to find,
// say, the practitioners who work at a place; Inferno's Plan-Net suite was refused with a 400.

// HasCriterion is one _has condition.
type HasCriterion struct {
	// SourceType is the type that refers to the one being searched (PractitionerRole).
	SourceType string
	// RefParam is its reference parameter that points back (practitioner).
	RefParam string
	// Param is the condition on SourceType (location), and Values what it must match.
	Param  string
	Values []string
}

// parseHas reads a _has:Type:refParam:param key. The bool says whether key is a _has at all.
func parseHas(resourceType, key string) (HasCriterion, bool, error) {
	rest, ok := strings.CutPrefix(key, "_has:")
	if !ok {
		return HasCriterion{}, false, nil
	}
	parts := strings.SplitN(rest, ":", 3)
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return HasCriterion{}, true, fmt.Errorf("%q: _has is written _has:Type:reference:parameter=value", key)
	}
	h := HasCriterion{SourceType: parts[0], RefParam: parts[1], Param: parts[2]}

	targets, ok := referenceTargets[h.SourceType][h.RefParam]
	if !ok || !slices.Contains(targets, resourceType) {
		return h, true, fmt.Errorf("%s.%s does not refer to %s, so it cannot be used in _has", h.SourceType, h.RefParam, resourceType)
	}
	// Nested _has (the condition itself a _has) is not supported, and is refused rather than misread.
	if strings.HasPrefix(h.Param, "_has") {
		return h, true, fmt.Errorf("%q: a _has inside a _has is not supported", key)
	}
	base, _, _ := strings.Cut(h.Param, ":")
	if !slices.Contains(SearchParams[h.SourceType], base) {
		return h, true, fmt.Errorf("%s does not support the search parameter %q, so it cannot be used in _has", h.SourceType, base)
	}
	return h, true, nil
}

// resolveHas turns each _has condition into an _id condition on the type being searched: the ids the matching source
// resources refer to.
func (s *Store) resolveHas(ctx context.Context, q *SearchQuery) error {
	for _, h := range q.Has {
		inner := &SearchQuery{
			ResourceType: h.SourceType,
			Criteria:     map[string][]string{h.Param: h.Values},
			Count:        maxChainedMatches + 1,
			SortDesc:     true,
		}
		res, err := s.Search(ctx, inner)
		if err != nil {
			return fmt.Errorf("resolving _has:%s:%s:%s: %w", h.SourceType, h.RefParam, h.Param, err)
		}
		if len(res.Resources) > maxChainedMatches {
			return fmt.Errorf("_has:%s:%s:%s matches more than %d %s resources; narrow the condition, because resolving "+
				"more than that would return part of the answer without saying so", h.SourceType, h.RefParam, h.Param,
				maxChainedMatches, h.SourceType)
		}

		ids, err := s.referencedIDs(ctx, h.SourceType, h.RefParam, q.ResourceType, res)
		if err != nil {
			return err
		}
		if len(ids) == 0 {
			q.Criteria["_id"] = append(q.Criteria["_id"], chainMatchesNothingID)
			continue
		}
		// One value, the ids joined by commas: any of them. A second _has adds another value, which is ANDed.
		q.Criteria["_id"] = append(q.Criteria["_id"], strings.Join(ids, ","))
	}
	return nil
}

// referencedIDs reads, from the index, the ids of targetType that refParam of the found resources refers to.
func (s *Store) referencedIDs(ctx context.Context, sourceType, refParam, targetType string, found *SearchResult) ([]string, error) {
	if len(found.Resources) == 0 {
		return nil, nil
	}
	args := []any{sourceType, refParam, targetType}
	for _, r := range found.Resources {
		args = append(args, r.ResourceID())
	}
	rows, err := s.db.QueryContext(ctx, `SELECT DISTINCT value FROM fhir_search
		WHERE resource_type = ? AND param = ? AND (ref_type = ? OR ref_type IS NULL OR ref_type = '')
		  AND resource_id IN (`+placeholders(len(found.Resources))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("fhirserver: reading references for _has: %w", err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// chainMatchesNothingID is an _id no resource has, for a _has that found no source resources.
const chainMatchesNothingID = "__no_such_id__"
