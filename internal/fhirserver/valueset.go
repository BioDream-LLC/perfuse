package fhirserver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/fhir"
)

// ValueSet/$expand and ValueSet/$validate-code, over the codes a mapping recognises.
//
// The question behind both is the one an analyst asks before turning a feed on: what will this engine accept, and what will it do with
// this particular code? A mapping table answers it, and until now the only way to ask was to send a message and look at what came out.
//
// Two value sets fall out of each mapping, and they are not the same set. The codes it accepts on the way in, and the codes it can
// produce on the way out. Conflating them would answer the wrong question half the time - "does this engine handle F" and "can this
// engine emit female" are different, and a client asking the first and being answered the second learns nothing useful.

// ValueSetSide says which end of a mapping a value set describes.
type ValueSetSide string

const (
	// SideSource is the codes a mapping accepts.
	SideSource ValueSetSide = "source"

	// SideTarget is the codes a mapping can produce.
	SideTarget ValueSetSide = "target"
)

// ValueSetSuffix is appended to a map's identity to name one of its two sides.
const (
	SourceSuffix = ":source"
	TargetSuffix = ":target"
)

// expandParams are the parameters $expand accepts here.
var expandParams = map[string]bool{
	"url":    true,
	"filter": true,
	"count":  true,
}

// validateCodeParams are the parameters $validate-code accepts here.
var validateCodeParams = map[string]bool{
	"url":  true,
	"code": true,
}

// ValueSetRequest identifies one side of one mapping.
type ValueSetRequest struct {
	// Map is the table, by name, id or canonical URL.
	Map string

	// Side is which end of it.
	Side ValueSetSide

	// Filter narrows an expansion to codes containing this text, case-insensitively.
	Filter string

	// Count bounds an expansion. Zero means no bound beyond the table's size.
	Count int

	// Code is the code to check, for $validate-code.
	Code string
}

// ParseValueSetURL splits a value set identity into a map and a side.
//
// The side is required rather than defaulted. Defaulting to source would answer "what does this engine accept" to somebody who asked
// "what can it produce", confidently and wrongly, and they would have no way to tell.
func ParseValueSetURL(raw string) (mapName string, side ValueSetSide, err error) {
	trimmed := strings.TrimPrefix(raw, ConceptMapBaseURL)

	switch {
	case strings.HasSuffix(trimmed, SourceSuffix):
		return strings.TrimSuffix(trimmed, SourceSuffix), SideSource, nil
	case strings.HasSuffix(trimmed, TargetSuffix):
		return strings.TrimSuffix(trimmed, TargetSuffix), SideTarget, nil
	default:
		return "", "", fmt.Errorf("a value set url must say which side of the mapping it means, "+
			"as %s<table>%s for the codes accepted or %s<table>%s for the codes produced. "+
			"%q says neither, and guessing would answer a different question from the one asked",
			ConceptMapBaseURL, SourceSuffix, ConceptMapBaseURL, TargetSuffix, raw)
	}
}

// ParseExpand reads the parameters for $expand.
//
// The specification defines many more - offset, includeDesignations, activeOnly, displayLanguage and others. They are refused by name
// rather than ignored, because an expansion that quietly disregarded activeOnly would list retired codes as usable.
func ParseExpand(values map[string][]string) (*ValueSetRequest, error) {
	req := &ValueSetRequest{}

	if err := checkParams(values, expandParams, "$expand", "url, filter and count"); err != nil {
		return nil, err
	}

	url, err := single(values, "url")
	if err != nil {
		return nil, err
	}
	if url == "" {
		return nil, fmt.Errorf("url is required: $expand needs to know which value set to expand")
	}

	name, side, err := ParseValueSetURL(url)
	if err != nil {
		return nil, err
	}
	req.Map, req.Side = name, side

	if req.Filter, err = single(values, "filter"); err != nil {
		return nil, err
	}

	countRaw, err := single(values, "count")
	if err != nil {
		return nil, err
	}
	if countRaw != "" {
		n, err := atoiNonNegative(countRaw, "count")
		if err != nil {
			return nil, err
		}
		req.Count = n
	}

	return req, nil
}

// ParseValidateCode reads the parameters for $validate-code.
func ParseValidateCode(values map[string][]string) (*ValueSetRequest, error) {
	req := &ValueSetRequest{}

	if err := checkParams(values, validateCodeParams, "$validate-code", "url and code"); err != nil {
		return nil, err
	}

	url, err := single(values, "url")
	if err != nil {
		return nil, err
	}
	if url == "" {
		return nil, fmt.Errorf("url is required: $validate-code needs to know which value set to check against")
	}

	name, side, err := ParseValueSetURL(url)
	if err != nil {
		return nil, err
	}
	req.Map, req.Side = name, side

	if req.Code, err = single(values, "code"); err != nil {
		return nil, err
	}
	if req.Code == "" {
		return nil, fmt.Errorf("code is required: $validate-code needs a code to check")
	}

	return req, nil
}

// ExpandTable lists the codes on one side of a mapping.
//
// Sorted and deduplicated. Deduplicated because a mapping is not injective - several v2 sex codes become other - and a value set
// listing other three times would be wrong about how many codes it contains.
// Returns the codes to publish and the size of the whole set, which are not the same number when count bounds the result. Returning
// only the list would force the caller to report the page size as the total - which is what this did, contradicting the comment on the
// field, until a test named after the distinction failed to check it.
func ExpandTable(t *codeset.Table, req *ValueSetRequest) (codes []string, total int) {
	seen := map[string]bool{}
	out := make([]string, 0, len(t.Entries))

	for _, e := range t.Entries {
		code := e.From
		if req.Side == SideTarget {
			code = e.To
		}

		// An empty target is a code deliberately not mapped, not a member of the produced set.
		if code == "" || seen[code] {
			continue
		}

		if req.Filter != "" && !strings.Contains(strings.ToLower(code), strings.ToLower(req.Filter)) {
			continue
		}

		seen[code] = true
		out = append(out, code)
	}

	// The table's default is a code this mapping can produce, and it will not appear among the entries.
	//
	// Omitting it would make the produced set wrong in exactly the case that matters: an unrecognised code arrives, the default is
	// emitted, and a receiver validating against this expansion rejects a value we told them we would never send.
	if req.Side == SideTarget && t.Default != "" && !seen[t.Default] {
		if req.Filter == "" || strings.Contains(strings.ToLower(t.Default), strings.ToLower(req.Filter)) {
			out = append(out, t.Default)
		}
	}

	sort.Strings(out)

	total = len(out)

	if req.Count > 0 && len(out) > req.Count {
		out = out[:req.Count]
	}

	return out, total
}

// ValidateCodeInTable reports whether a code is in one side of a mapping, and says what that means.
func ValidateCodeInTable(t *codeset.Table, req *ValueSetRequest) (bool, string) {
	for _, e := range t.Entries {
		switch req.Side {
		case SideSource:
			if e.From != req.Code {
				continue
			}

			// A code mapped to nothing is recognised and deliberately not translated, which is a third answer and not
			// the same as either of the other two.
			//
			// Reported as such because the obvious rendering - "becomes \"\"" - reads like a fault in the mapping
			// rather than a decision about it. HL7 v2 patient class U means unknown, and inventing a class from it
			// would assert something about the visit that nobody said. The table records that reasoning and it is
			// worth passing on.
			if e.To == "" {
				reason := e.Why
				if reason == "" {
					reason = "this code is recognised but deliberately carries no mapping"
				}

				return true, fmt.Sprintf("%q is recognised by the %s mapping and is deliberately not translated: %s",
					req.Code, t.Name, reason)
			}

			return true, fmt.Sprintf("%q is a code the %s mapping recognises, and becomes %q",
				req.Code, t.Name, e.To)
		case SideTarget:
			if e.To == req.Code {
				return true, fmt.Sprintf("%q is a code the %s mapping can produce", req.Code, t.Name)
			}
		}
	}

	if req.Side == SideTarget {
		if t.Default != "" && t.Default == req.Code {
			return true, fmt.Sprintf("%q is the default of the %s mapping, so it is produced whenever an "+
				"unrecognised code arrives", req.Code, t.Name)
		}

		return false, fmt.Sprintf("%q is not a code the %s mapping produces", req.Code, t.Name)
	}

	// Not a listed source code. What happens next depends entirely on the table, and that is the answer somebody needs.
	switch {
	case t.Strict:
		return false, fmt.Sprintf("%q is not in the %s mapping, and that mapping is strict, "+
			"so a message carrying this code would be refused", req.Code, t.Name)
	case t.Default != "":
		return false, fmt.Sprintf("%q is not in the %s mapping, so it would become the default %q. "+
			"Accepted, but not recognised - which is worth knowing before a feed goes live",
			req.Code, t.Name, t.Default)
	default:
		return false, fmt.Sprintf("%q is not in the %s mapping, which passes unmapped codes through unchanged",
			req.Code, t.Name)
	}
}

// TableAsValueSet projects one side of a mapping as an expanded ValueSet.
func TableAsValueSet(t *codeset.Table, req *ValueSetRequest, codes []string, total int) *fhir.ValueSet {
	suffix := SourceSuffix
	describes := "codes the " + t.Name + " mapping accepts"

	if req.Side == SideTarget {
		suffix = TargetSuffix
		describes = "codes the " + t.Name + " mapping can produce"
	}

	vs := &fhir.ValueSet{
		URL:         ConceptMapBaseURL + t.Name + suffix,
		Name:        t.Name + suffix,
		Status:      "active",
		Description: describes + ". " + t.Describes,
		Publisher:   t.DecidedBy,
		Date:        t.DecidedOn,
	}
	vs.SetResourceID(ConceptMapID(t.Name) + "-" + string(req.Side))

	vs.Expansion = &fhir.ValueSetExpansion{
		Total:      fhir.Int(total),
		Identifier: vs.URL,
	}

	for _, code := range codes {
		vs.Expansion.Contains = append(vs.Expansion.Contains, fhir.ValueSetContains{
			Code:    code,
			Display: displayFor(t, req.Side, code),
		})
	}

	return vs
}

// displayFor returns a human-readable name for a code, when the mapping records one.
//
// Empty when it does not. Inventing a display from the code itself would put a guess in a field clients render to clinicians, and a
// plausible wrong label is worse than a bare code that sends somebody to look it up.
func displayFor(t *codeset.Table, side ValueSetSide, code string) string {
	for _, e := range t.Entries {
		if side == SideSource && e.From == code {
			return e.Why
		}
		if side == SideTarget && e.To == code {
			return ""
		}
	}

	return ""
}

// checkParams refuses any parameter not in the allowed set.
func checkParams(values map[string][]string, allowed map[string]bool, operation, supported string) error {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if allowed[name] {
			continue
		}

		return fmt.Errorf("%s is not a parameter this server implements for %s, and has been refused rather than "+
			"ignored: an operation that quietly disregards a parameter answers a different question from the one "+
			"asked. The supported ones are %s", name, operation, supported)
	}

	return nil
}

// single returns a parameter given at most once.
func single(values map[string][]string, name string) (string, error) {
	vals := values[name]
	if len(vals) == 0 {
		return "", nil
	}
	if len(vals) > 1 {
		return "", fmt.Errorf("%s was given more than once, and two values cannot both be meant", name)
	}

	return vals[0], nil
}

// atoiNonNegative parses a count.
func atoiNonNegative(raw, name string) (int, error) {
	n := 0
	for _, r := range raw {
		if r < '0' || r > '9' {
			return 0, fmt.Errorf("%s must be a non-negative number, got %q", name, raw)
		}
		n = n*10 + int(r-'0')
		if n > 1_000_000 {
			return 0, fmt.Errorf("%s is implausibly large: %q", name, raw)
		}
	}

	return n, nil
}
