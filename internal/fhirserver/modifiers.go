package fhirserver

import (
	"fmt"
	"sort"
	"strings"
)

// Search modifiers: :exact, :contains and :missing.
//
// Previously every modifier was refused by name, which was the right failure - silently dropping one changes what a query means,
// and a client asking for :exact and getting prefix matches gets more records than it asked for and cannot tell.
//
// These three are the ones a real client sends. They are handled as their own criterion type rather than folded into
// SearchQuery.Criteria, for the same reason chains are: the rest of Search does not have to learn about them, and the launch
// context narrowing that writes into Criteria cannot be confused by an entry that means something different.

// Modifier is a search parameter modifier.
type Modifier string

const (
	// ModExact matches the whole value, case-sensitively.
	//
	// FHIR defines :exact as no prefix matching, no case folding and no accent folding. The first two are implemented against
	// the unfolded value kept in value_raw. Accent folding is not performed anywhere in this store, so there is nothing to
	// switch off - a value with an accent is stored and compared as written.
	ModExact Modifier = "exact"

	// ModContains matches anywhere in the value rather than at the start.
	//
	// Case-insensitive, which is what FHIR specifies for :contains. It cannot use an index - a leading wildcard defeats a
	// B-tree - so it scans the index rows for that parameter. Acceptable because the alternative is refusing a modifier
	// clients genuinely send, and the scan is over one parameter's rows rather than the resource table.
	ModContains Modifier = "contains"

	// ModMissing selects resources where the parameter is absent, or present, depending on the value.
	//
	// :missing=true means the resource has no value for this parameter at all. Useful and impossible to express otherwise: a
	// client looking for patients with no recorded birth date cannot write that as a value comparison.
	ModMissing Modifier = "missing"
)

// supportedModifiers is the set this store implements.
//
// An allow-list rather than a check for the ones known to be unsupported. A modifier nobody has thought about must be refused,
// because the alternative is treating it as absent - and a query with a modifier ignored returns a different set of records than
// the client asked for, with a 200 and no indication anything was dropped.
var supportedModifiers = map[Modifier]bool{
	ModExact:    true,
	ModContains: true,
	ModMissing:  true,
}

// ModifiedCriterion is one parameter with a modifier applied.
type ModifiedCriterion struct {
	Param    string
	Modifier Modifier
	Values   []string
}

// parseModifier splits a search key into a parameter and a modifier.
//
// Reports whether the key carried a modifier at all, so the caller can tell "no modifier" from "a modifier that failed to
// parse". Those need different handling and a single error return cannot express the difference.
func parseModifier(resourceType, key string) (crit ModifiedCriterion, modified bool, err error) {
	i := strings.IndexByte(key, ':')
	if i < 0 {
		return ModifiedCriterion{}, false, nil
	}

	param := key[:i]
	mod := Modifier(key[i+1:])

	if !supportedModifiers[mod] {
		names := make([]string, 0, len(supportedModifiers))
		for m := range supportedModifiers {
			names = append(names, ":"+string(m))
		}
		// Sorted, because Go maps range randomly and an error message that reorders itself between runs is one nobody can
		// match against a test or a support ticket.
		sort.Strings(names)

		return ModifiedCriterion{}, true, fmt.Errorf(
			"the search modifier %q is not supported; supported modifiers are %s",
			":"+string(mod), strings.Join(names, ", "))
	}

	// The parameter has to be one this resource type actually has, checked here rather than left to the unmodified path.
	// Otherwise `Patient?nonsense:exact=x` would be refused for the wrong reason, naming the modifier instead of the
	// parameter, and the client would go looking for a modifier problem that does not exist.
	allowed := SearchParams[resourceType]
	found := false
	for _, p := range allowed {
		if p == param {
			found = true

			break
		}
	}
	if !found {
		sorted := append([]string(nil), allowed...)
		sort.Strings(sorted)

		return ModifiedCriterion{}, true, fmt.Errorf(
			"%s does not support the search parameter %q; supported: %s",
			resourceType, param, strings.Join(sorted, ", "))
	}

	// :exact and :contains are string operations. Applying them to a reference or a date would either silently do something
	// else or return nothing, and both are worse than saying so.
	if mod == ModExact || mod == ModContains {
		if isReferenceParam(param) || isDateParam(param) {
			return ModifiedCriterion{}, true, fmt.Errorf(
				"%s cannot be used with %q, which is a %s parameter rather than a string one",
				":"+string(mod), param, kindOf(param))
		}
	}

	return ModifiedCriterion{Param: param, Modifier: mod}, true, nil
}

// kindOf names what sort of parameter this is, for an error message.
func kindOf(param string) string {
	switch {
	case isReferenceParam(param):
		return "reference"
	case isDateParam(param):
		return "date"
	case isStringParam(param):
		return "string"
	default:
		return "token"
	}
}

// missingWanted reads the value of a :missing modifier.
//
// FHIR specifies true and false and nothing else. Anything else is refused rather than read as false, because "missing=yes"
// treated as "missing=false" inverts the query and returns the opposite set of records.
func missingWanted(value string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "true":
		return true, nil
	case "false":
		return false, nil
	default:
		return false, fmt.Errorf(":missing takes true or false, not %q", value)
	}
}

// modifiedClause builds the SQL for one modified criterion, and the arguments it needs.
//
// Returns a clause beginning with " AND " so it appends to the where builder the same way the plain criteria do.
func modifiedClause(crit ModifiedCriterion) (string, []any, error) {
	var (
		b    strings.Builder
		args []any
	)

	switch crit.Modifier {
	case ModMissing:
		// Only the first value is used, and more than one is refused rather than reconciled.
		//
		// missing=true&missing=false asks for resources that both have and do not have the parameter, which is empty by
		// construction. Answering it with an empty page would be technically correct and useless; the client has a bug and
		// should be told.
		if len(crit.Values) > 1 {
			return "", nil, fmt.Errorf(
				"%s:missing was given %d values; it takes one", crit.Param, len(crit.Values))
		}

		want, err := missingWanted(crit.Values[0])
		if err != nil {
			return "", nil, fmt.Errorf("%s:missing: %w", crit.Param, err)
		}

		// NOT EXISTS for missing=true, EXISTS for missing=false.
		//
		// Note this asks whether the *index* has a row, which is the same question as whether the resource has a value only
		// because the index is rebuilt from the resource on every write. That is why the migrations reindex rather than
		// backfill: a stale index would make this answer confidently wrong.
		if want {
			b.WriteString(" AND NOT EXISTS (")
		} else {
			b.WriteString(" AND EXISTS (")
		}
		b.WriteString(`SELECT 1 FROM fhir_search x
			WHERE x.resource_type = fhir_resources.resource_type
			  AND x.resource_id = fhir_resources.resource_id
			  AND x.param = ?)`)
		args = append(args, crit.Param)

		return b.String(), args, nil

	case ModExact:
		// Compared against value_raw, which holds the value as written.
		//
		// value is case-folded for string parameters, so comparing against it would make :exact case-insensitive - which is
		// precisely the behaviour :exact exists to switch off. A client asking for "Smith" and receiving "SMITH" has been
		// given records it excluded, with a 200 and nothing to indicate the modifier was ignored.
		b.WriteString(` AND EXISTS (SELECT 1 FROM fhir_search x
			WHERE x.resource_type = fhir_resources.resource_type
			  AND x.resource_id = fhir_resources.resource_id
			  AND x.param = ? AND (`)
		args = append(args, crit.Param)

		for i, v := range crit.Values {
			if i > 0 {
				b.WriteString(" OR ")
			}
			// COALESCE, so a row written before value_raw existed still compares against something rather than
			// disappearing. The migration reindexes, so there should be none - but a query that silently returns fewer
			// records than it should is the wrong way to discover the migration did not run.
			b.WriteString("COALESCE(x.value_raw, x.value) = ?")
			args = append(args, v)
		}
		b.WriteString("))")

		return b.String(), args, nil

	case ModContains:
		// Case-insensitive substring, which is what FHIR specifies for :contains.
		//
		// Matched against the folded value with a leading wildcard. That cannot use an index, so it scans - but it scans the
		// rows for one parameter rather than the resource table, and the alternative is refusing something clients send.
		b.WriteString(` AND EXISTS (SELECT 1 FROM fhir_search x
			WHERE x.resource_type = fhir_resources.resource_type
			  AND x.resource_id = fhir_resources.resource_id
			  AND x.param = ? AND (`)
		args = append(args, crit.Param)

		for i, v := range crit.Values {
			if i > 0 {
				b.WriteString(" OR ")
			}
			// ESCAPE attaches to each LIKE rather than to the group. Placing it after the closing parenthesis is a
			// syntax error, which is how I learned this.
			b.WriteString(`x.value LIKE ? ESCAPE '\'`)
			// The folded value is what x.value holds for a string parameter, so the pattern is folded to match. escapeLike
			// keeps a literal % or _ in a patient's name from becoming a wildcard.
			args = append(args, "%"+escapeLike(strings.ToLower(v))+"%")
		}
		b.WriteString("))")

		return b.String(), args, nil

	default:
		// Unreachable through ParseSearch, which refuses anything not in supportedModifiers. Present because a modifier added
		// to that map and not here would otherwise apply no clause at all - returning every resource of the type, which is
		// the widest possible wrong answer.
		return "", nil, fmt.Errorf("the search modifier %q is recognised but not implemented", ":"+string(crit.Modifier))
	}
}

// escapeLike neutralises the LIKE wildcards in a literal value.
//
// Without this a search for a name containing % or _ becomes a wildcard search. Rare in a surname and not rare at all in an
// identifier or a device name, and the failure is silent: it returns more records than were asked for.
func escapeLike(s string) string {
	var b strings.Builder

	for _, r := range s {
		switch r {
		case '%', '_', '\\':
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}

	return b.String()
}

// modifierDocumentation names the modifiers this server implements, for the capability statement.
//
// Generated from supportedModifiers rather than written out, so the statement cannot claim a modifier that was removed or omit one
// that was added. A capability statement that overstates is worse than none: a client trusts it, builds a query from it, and gets
// an error the statement said could not happen.
func modifierDocumentation() string {
	names := make([]string, 0, len(supportedModifiers))
	for m := range supportedModifiers {
		names = append(names, ":"+string(m))
	}
	sort.Strings(names)

	return "search modifiers " + strings.Join(names, ", ")
}
