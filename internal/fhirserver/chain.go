package fhirserver

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// Chained search: Observation?patient.family=Dubois.
//
// A query about a resource expressed as a condition on something it points at. Common in real use because it is how a question is naturally
// asked - "observations for patients named Dubois" - and because a client that cannot chain has to search patients, collect the ids, and
// then search observations once per id, which is the same N+1 problem _include solves for the response.
//
// Resolved by running the chained half as its own search and then constraining the outer one by the ids it found. That is not the cheapest
// possible plan, but it reuses the whole search implementation - parameter validation, token handling, string prefixing, dates - rather
// than growing a second one in SQL that would accept and reject different things than the first.

// maxChainedMatches bounds the inner search.
//
// A chain on a common name would otherwise load every matching patient before the outer query starts. The bound is stated in an error rather
// than applied silently: a chain that quietly used the first thousand patients would return a subset of the right answer, which is worse than
// a refusal because nothing about the response says it is incomplete.
const maxChainedMatches = 1000

// ChainedCriterion is one chained condition.
type ChainedCriterion struct {
	// Param is the reference parameter on the resource being searched, such as "patient".
	Param string

	// TargetType is the type the chain resolves against. Taken from an explicit modifier when the client gave one -
	// subject:Patient.name - and otherwise from the single type the parameter can point at.
	TargetType string

	// Chained is the parameter on the target, such as "family".
	Chained string

	// Values are what the chained parameter must match.
	Values []string
}

// parseChain reads a chained parameter key such as "patient.family" or "subject:Patient.name".
//
// Returns ok=false for a key with no dot, which is every ordinary parameter.
func parseChain(resourceType, key string) (ChainedCriterion, bool, error) {
	dot := strings.IndexByte(key, '.')
	if dot < 0 {
		return ChainedCriterion{}, false, nil
	}

	head, chained := key[:dot], key[dot+1:]

	crit := ChainedCriterion{Chained: strings.TrimSpace(chained)}
	if crit.Chained == "" {
		return crit, true, fmt.Errorf("%q names no parameter after the dot", key)
	}

	// An explicit target type, as subject:Patient.name.
	if colon := strings.IndexByte(head, ':'); colon >= 0 {
		crit.Param = head[:colon]
		crit.TargetType = head[colon+1:]
	} else {
		crit.Param = head
	}

	params, ok := referenceTargets[resourceType]
	if !ok {
		return crit, true, fmt.Errorf(
			"%s has no reference parameters, so %q cannot be chained", resourceType, crit.Param)
	}

	targets, ok := params[crit.Param]
	if !ok {
		var known []string
		for p := range params {
			known = append(known, p)
		}
		sort.Strings(known)

		return crit, true, fmt.Errorf("%s cannot chain through %q; it can chain through %s",
			resourceType, crit.Param, strings.Join(known, ", "))
	}

	if crit.TargetType != "" {
		permitted := false
		for _, t := range targets {
			if t == crit.TargetType {
				permitted = true

				break
			}
		}
		if !permitted {
			return crit, true, fmt.Errorf("%s.%s does not point at %s; it points at %s",
				resourceType, crit.Param, crit.TargetType, strings.Join(targets, ", "))
		}
	} else if len(targets) == 1 {
		crit.TargetType = targets[0]
	} else {
		// Ambiguous, and refused rather than resolved against each candidate in turn.
		//
		// A chain on subject could mean a Patient, a Group or a Location, and those have different parameters -
		// family means nothing to a Location. Trying each and unioning the results would answer a question the
		// client did not ask, and the union would look like a valid answer.
		return crit, true, fmt.Errorf(
			"%s.%s is ambiguous because %s can point at %s; write it as %s:Type.%s",
			crit.Param, crit.Chained, crit.Param, strings.Join(targets, ", "),
			crit.Param, crit.Chained)
	}

	// Checked here rather than when the chain runs, so an unsupported chained parameter is refused as part of reading the
	// query - the same treatment as any other unsupported parameter, and for the same reason.
	supported := false
	for _, p := range SearchParams[crit.TargetType] {
		if p == crit.Chained {
			supported = true

			break
		}
	}
	if !supported {
		available := append([]string(nil), SearchParams[crit.TargetType]...)
		sort.Strings(available)

		return crit, true, fmt.Errorf("%s does not support the search parameter %q; supported: %s",
			crit.TargetType, crit.Chained, strings.Join(available, ", "))
	}

	return crit, true, nil
}

// resolveChains turns the chained criteria into ordinary reference criteria.
//
// Run before the main query so the rest of Search needs to know nothing about chaining. Each chain becomes a reference criterion listing the
// ids the inner search found, which is exactly what the client would have had to send by hand.
func (s *Store) resolveChains(ctx context.Context, q *SearchQuery) error {
	for _, crit := range q.Chains {
		inner := &SearchQuery{
			ResourceType: crit.TargetType,
			Criteria:     map[string][]string{crit.Chained: crit.Values},
			// One more than the bound, so exceeding it is detectable rather than looking like an exact fit.
			Count:    maxChainedMatches + 1,
			SortDesc: true,
		}

		res, err := s.Search(ctx, inner)
		if err != nil {
			return fmt.Errorf("resolving %s.%s: %w", crit.Param, crit.Chained, err)
		}

		if len(res.Resources) > maxChainedMatches {
			return fmt.Errorf(
				"%s.%s matches more than %d %s resources; narrow the chained condition, because a "+
					"chain resolved against more than that would return part of the answer without "+
					"saying so",
				crit.Param, crit.Chained, maxChainedMatches, crit.TargetType)
		}

		if len(res.Resources) == 0 {
			// Nothing matched the chain, so nothing can match the outer query.
			//
			// Expressed as a criterion that cannot match rather than by returning early, so the caller gets an
			// ordinary empty result with the right total and links. Returning a special case here is how the
			// paging metadata ends up wrong for one query shape.
			q.Criteria[crit.Param] = []string{chainMatchesNothing}

			continue
		}

		// Type-qualified, so the outer query compares the type as well as the id. Without this a chain through
		// subject could match a resource pointing at the Location that shares an id with the Patient the chain
		// found, which is the same defect the ref_type column exists to prevent.
		values := make([]string, 0, len(res.Resources))
		for _, r := range res.Resources {
			values = append(values, crit.TargetType+"/"+r.ResourceID())
		}

		// Appended rather than assigned, so a chain and an explicit reference on the same parameter both apply. The
		// two OR together within the parameter, which is what FHIR specifies for repeated values - and is why a
		// client sending both gets the union rather than one silently replacing the other.
		q.Criteria[crit.Param] = append(q.Criteria[crit.Param], values...)
	}

	return nil
}

// chainMatchesNothing is a reference value no resource can have.
//
// Used when a chain matched nothing, so the outer query returns an ordinary empty result rather than needing a special case. The value
// contains characters no FHIR id may contain, so it cannot collide with a real one - an id is limited to letters, digits, hyphens and dots.
const chainMatchesNothing = "__no_such_reference__/__no_such_id__"
