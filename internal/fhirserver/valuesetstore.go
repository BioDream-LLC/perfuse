package fhirserver

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Stored value sets: the payer's own, loaded so a DTR questionnaire's answer lists travel with it.
//
// Until this, a ValueSet could not be stored at all - every value set was a view of a mapping table - so a payer whose questionnaire
// used answerValueSet had to contain the set inside the questionnaire. Stored value sets live beside the table views without sharing
// a name: the urn:perfuse:codeset: namespace is the tables', and a write into it is refused, so there is never a stored copy and a
// projected one answering to the same url.
//
// $expand works for what can be expanded without a terminology server: concepts listed in compose, other stored value sets included
// by url, enumerated excludes, or an expansion the resource already carries. A rule over a whole code system (a filter, or a system
// with no concept list) is refused by name, because an expansion that silently left those codes out would tell an app they are not
// valid answers.

// tableViewWrite refuses a ValueSet whose url is in the mapping tables' namespace.
func tableViewWrite(resourceType, url string) string {
	if resourceType == "ValueSet" && strings.HasPrefix(url, ConceptMapBaseURL) {
		return "the url " + url + " names a view of a mapping table, which lives in a .codeset.yaml file beside the channels; " +
			"a stored copy would be a second answer to the same url. Edit the table there, or give the value set its own url"
	}
	return ""
}

// expandLimit bounds how deep value sets may include each other, so a cycle is reported instead of followed.
const expandLimit = 10

// expandStored resolves a stored value set to its members.
func (s *Server) expandStored(r *http.Request, vs map[string]any, depth int) ([]map[string]any, error) {
	url, _ := vs["url"].(string)
	if depth > expandLimit {
		return nil, fmt.Errorf("value sets include each other more than %d deep at %s, which is almost certainly a cycle", expandLimit, url)
	}
	compose, _ := vs["compose"].(map[string]any)
	if compose == nil {
		exp, _ := vs["expansion"].(map[string]any)
		if exp == nil {
			return nil, fmt.Errorf("the value set %s has neither a compose nor an expansion, so it names no codes", url)
		}
		var out []map[string]any
		flattenContains(listOf(exp["contains"]), &out)
		return out, nil
	}

	var members []map[string]any
	seen := map[string]bool{}
	for i, inc := range listOf(compose["include"]) {
		im, _ := inc.(map[string]any)
		got, err := s.expandRule(r, im, depth, url, "include", i)
		if err != nil {
			return nil, err
		}
		for _, c := range got {
			if k := conceptKey(c); !seen[k] {
				seen[k] = true
				members = append(members, c)
			}
		}
	}
	for i, exc := range listOf(compose["exclude"]) {
		em, _ := exc.(map[string]any)
		got, err := s.expandRule(r, em, depth, url, "exclude", i)
		if err != nil {
			return nil, err
		}
		drop := map[string]bool{}
		for _, c := range got {
			drop[conceptKey(c)] = true
		}
		kept := members[:0]
		for _, c := range members {
			if !drop[conceptKey(c)] {
				kept = append(kept, c)
			}
		}
		members = kept
	}
	return members, nil
}

// expandRule resolves one include or exclude: its listed concepts, intersected with any value sets it names.
func (s *Server) expandRule(r *http.Request, rule map[string]any, depth int, url, kind string, i int) ([]map[string]any, error) {
	system, _ := rule["system"].(string)
	version, _ := rule["version"].(string)
	if len(listOf(rule["filter"])) > 0 {
		return nil, fmt.Errorf("%s %d of %s filters %s by a property; expanding that needs the whole code system, "+
			"which this server does not hold. List the concepts in the value set, or expand it with a terminology server", kind, i, url, system)
	}
	concepts := listOf(rule["concept"])
	if system != "" && len(concepts) == 0 {
		return nil, fmt.Errorf("%s %d of %s takes every code in %s; expanding that needs the whole code system, "+
			"which this server does not hold. List the concepts in the value set, or expand it with a terminology server", kind, i, url, system)
	}

	var listed []map[string]any
	for _, c := range concepts {
		cm, _ := c.(map[string]any)
		code, _ := cm["code"].(string)
		if code == "" {
			continue
		}
		entry := map[string]any{"system": system, "code": code}
		if version != "" {
			entry["version"] = version
		}
		if d, _ := cm["display"].(string); d != "" {
			entry["display"] = d
		}
		listed = append(listed, entry)
	}

	// Each named value set narrows the rule: a concept must be in all of them (and in the listed concepts, when there are any).
	var result []map[string]any
	if system != "" {
		result = listed
	}
	for j, ref := range listOf(rule["valueSet"]) {
		canonical, _ := ref.(string)
		inner := s.byCanonical(r, "ValueSet", canonical)
		if inner == nil {
			return nil, fmt.Errorf("%s %d of %s names the value set %s, which is not loaded on this server", kind, i, url, canonical)
		}
		got, err := s.expandStored(r, inner, depth+1)
		if err != nil {
			return nil, err
		}
		if system == "" && j == 0 {
			result = got
			continue
		}
		in := map[string]bool{}
		for _, c := range got {
			in[conceptKey(c)] = true
		}
		kept := []map[string]any{}
		for _, c := range result {
			if in[conceptKey(c)] {
				kept = append(kept, c)
			}
		}
		result = kept
	}
	return result, nil
}

func conceptKey(c map[string]any) string {
	sys, _ := c["system"].(string)
	code, _ := c["code"].(string)
	return sys + "|" + code
}

// flattenContains lists the codes of an expansion, walking nested contains and leaving out abstract groupers.
func flattenContains(list []any, out *[]map[string]any) {
	for _, c := range list {
		cm, _ := c.(map[string]any)
		if cm == nil {
			continue
		}
		if code, _ := cm["code"].(string); code != "" && cm["abstract"] != true {
			e := map[string]any{"code": code}
			for _, k := range []string{"system", "version", "display"} {
				if v, _ := cm[k].(string); v != "" {
					e[k] = v
				}
			}
			*out = append(*out, e)
		}
		flattenContains(listOf(cm["contains"]), out)
	}
}

// storedValueSetURL reports whether an $expand or $validate-code url should be answered from the store rather than the tables.
func storedValueSetURL(url string) bool {
	return url != "" && !strings.HasPrefix(url, ConceptMapBaseURL)
}

// handleExpandStored answers $expand for a stored value set.
func (s *Server) handleExpandStored(w http.ResponseWriter, r *http.Request, url string) {
	if err := checkParams(r.Form, expandParams, "$expand", "url, filter and count"); err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", err.Error())
		return
	}
	filter, _ := single(r.Form, "filter")
	count := 0
	if raw, _ := single(r.Form, "count"); raw != "" {
		n, err := atoiNonNegative(raw, "count")
		if err != nil {
			s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", err.Error())
			return
		}
		count = n
	}
	vs := s.byCanonical(r, "ValueSet", url)
	if vs == nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-found", "no ValueSet with url "+url+" is loaded on this server")
		return
	}
	members, err := s.expandStored(r, vs, 0)
	if err != nil {
		s.writeOutcome(w, r, http.StatusUnprocessableEntity, "error", "too-costly", err.Error())
		return
	}
	if filter != "" {
		f := strings.ToLower(filter)
		kept := members[:0]
		for _, c := range members {
			code, _ := c["code"].(string)
			display, _ := c["display"].(string)
			if strings.Contains(strings.ToLower(code), f) || strings.Contains(strings.ToLower(display), f) {
				kept = append(kept, c)
			}
		}
		members = kept
	}
	sort.SliceStable(members, func(i, j int) bool { return conceptKey(members[i]) < conceptKey(members[j]) })
	total := len(members)
	page := members
	if count > 0 && len(page) > count {
		page = page[:count]
	}
	contains := make([]any, 0, len(page))
	for _, c := range page {
		contains = append(contains, c)
	}

	out := map[string]any{}
	for _, k := range []string{"resourceType", "id", "url", "version", "name", "title", "status", "date", "publisher", "description"} {
		if v, ok := vs[k]; ok {
			out[k] = v
		}
	}
	exp := map[string]any{
		"identifier": "urn:uuid:" + newUUID(),
		"timestamp":  time.Now().UTC().Format(time.RFC3339),
		"total":      total,
	}
	if len(contains) > 0 {
		exp["contains"] = contains
	}
	// The parameters that shaped this expansion, so a client can tell whether it may reuse it: the filter, and every code system
	// drawn on (the HL7 validator warns when an expansion names neither).
	var params []any
	if filter != "" {
		params = append(params, map[string]any{"name": "filter", "valueString": filter})
	}
	if count > 0 {
		params = append(params, map[string]any{"name": "count", "valueInteger": count})
	}
	used := map[string]bool{}
	for _, c := range members {
		sys, _ := c["system"].(string)
		if v, _ := c["version"].(string); v != "" {
			sys += "|" + v
		}
		if sys != "" && !used[sys] {
			used[sys] = true
			params = append(params, map[string]any{"name": "used-codesystem", "valueUri": sys})
		}
	}
	if len(params) > 0 {
		exp["parameter"] = params
	}
	out["expansion"] = exp
	s.writeJSON(w, http.StatusOK, out)
}

// handleValidateStored answers $validate-code against a stored value set.
func (s *Server) handleValidateStored(w http.ResponseWriter, r *http.Request, url string) {
	allowed := map[string]bool{"url": true, "code": true, "system": true, "display": true}
	if err := checkParams(r.Form, allowed, "$validate-code", "url, code, system and display"); err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", err.Error())
		return
	}
	code, _ := single(r.Form, "code")
	system, _ := single(r.Form, "system")
	if code == "" {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", "code is required: $validate-code needs a code to check")
		return
	}
	vs := s.byCanonical(r, "ValueSet", url)
	if vs == nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-found", "no ValueSet with url "+url+" is loaded on this server")
		return
	}
	members, err := s.expandStored(r, vs, 0)
	if err != nil {
		s.writeOutcome(w, r, http.StatusUnprocessableEntity, "error", "too-costly", err.Error())
		return
	}
	result, message, display := false, "", ""
	for _, c := range members {
		if c["code"] != code {
			continue
		}
		if sys, _ := c["system"].(string); system != "" && sys != system {
			continue
		}
		result = true
		display, _ = c["display"].(string)
		break
	}
	if result {
		message = fmt.Sprintf("%q is in %s", code, url)
	} else if system == "" {
		message = fmt.Sprintf("%q is not in %s", code, url)
	} else {
		message = fmt.Sprintf("%s#%s is not in %s", system, code, url)
	}
	params := []any{
		map[string]any{"name": "result", "valueBoolean": result},
		map[string]any{"name": "message", "valueString": message},
	}
	if display != "" {
		params = append(params, map[string]any{"name": "display", "valueString": display})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"resourceType": "Parameters", "parameter": params})
}
