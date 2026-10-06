package fhirserver

import (
	"net/url"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Granular SMART scopes (SMART App Launch 2): patient/Observation.rs?category=laboratory reads only the Observations a search
// with that query would find. A caller whose only read grant for a type is filtered gets the filters applied to every search
// and every read; one unfiltered grant for the type, or for *, lifts them.

// scopeFilters returns the caller's filters for a resource type, and whether reads of it are filtered at all.
func (c *Caller) scopeFilters(resourceType string) ([]map[string][]string, bool) {
	if c == nil || c.AllScopes {
		return nil, false
	}
	grants := parseSMARTScopes(c.Scopes)
	var raw []string
	for _, key := range []string{"*", resourceType} {
		g := grants[key]
		if g.Read {
			return nil, false
		}
		raw = append(raw, g.Filters...)
	}
	if len(raw) == 0 {
		return nil, false
	}
	var out []map[string][]string
	for _, q := range raw {
		v, err := url.ParseQuery(q)
		if err != nil || len(v) == 0 {
			// An unreadable filter grants nothing, rather than everything.
			continue
		}
		out = append(out, v)
	}
	return out, true
}

// narrowSearchToScopes applies the caller's granular scopes to a search. A filtered caller whose filters were all unreadable
// matches nothing, through a filter no resource satisfies.
func narrowSearchToScopes(caller *Caller, q *SearchQuery) {
	filters, filtered := caller.scopeFilters(q.ResourceType)
	if !filtered {
		return
	}
	if len(filters) == 0 {
		filters = []map[string][]string{{"_perfuse-no-match": {"x"}}}
	}
	q.ScopeFilters = filters
}

// permitsScopes reports whether a resource is within the caller's granular scopes: for each filter, every parameter must
// match the resource's own search index. Only token parameters are compared; any other kind fails the filter, so a filter
// this cannot evaluate refuses the read rather than allowing it.
func permitsScopes(caller *Caller, r fhir.Resource) bool {
	filters, filtered := caller.scopeFilters(r.ResourceTypeName())
	if !filtered {
		return true
	}
	entries := indexEntries(r)
	for _, f := range filters {
		if filterMatches(f, entries) {
			return true
		}
	}
	return false
}

func filterMatches(f map[string][]string, entries []indexEntry) bool {
	for param, occurrences := range f {
		if isDateParam(param) || isReferenceParam(param) || isStringParam(param) || strings.HasPrefix(param, "_") ||
			strings.Contains(param, ":") {
			return false
		}
		for _, occ := range occurrences {
			if !anyTokenMatches(param, splitOr(occ), entries) {
				return false
			}
		}
	}
	return true
}

func anyTokenMatches(param string, values []string, entries []indexEntry) bool {
	for _, v := range values {
		system, code := splitToken(v)
		for _, e := range entries {
			if e.param != param {
				continue
			}
			switch {
			case system != "" && code == "" && e.system == system:
				return true
			case system != "" && e.system == system && e.value == code:
				return true
			case system == "" && !strings.Contains(v, "|") && e.value == code:
				return true
			case strings.HasPrefix(v, "|") && e.system == "" && e.value == code:
				return true
			}
		}
	}
	return false
}
