// Package pas reads Da Vinci Prior Authorization Support (PAS) request Bundles.
//
// It is the reader Perfuse's own PAS server uses, published so that whatever decides a prior authorization - a payer's
// rules, a utilization management system, a decision service such as Vaglio - reads a request with the same code, and so
// the same answers about what a request contains. Two readers of one message format are two opinions about it.
//
// It has no dependencies outside the standard library. A request is kept as decoded JSON (map[string]any) rather than
// as typed resources, because a PAS Bundle carries whatever supporting resources the provider chose to send, and a reader
// that dropped what it did not model would hide evidence from the decision.
//
//	req, problems := pas.Read(bundle, false)
//	if len(problems) > 0 {
//		// refuse: the request cannot be answered correctly
//	}
//	for _, svc := range req.Services() {
//		// svc.Codes, svc.Item, and req.Resolve for anything the item references
//	}
package pas

import (
	"fmt"
	"strings"
)

// Base is the canonical base of the PAS StructureDefinitions.
const Base = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/"

// Request is a PAS request Bundle, read: its Claim and how its references resolve.
type Request struct {
	Bundle   map[string]any
	Claim    map[string]any
	ClaimURL string // fullUrl of the Claim's entry, the base relative references resolve against
	Entries  []map[string]any
}

// Read checks what is needed to answer a request and reports everything missing at once. It does not repeat the profile
// validation a client should have done; it refuses what could not be answered correctly.
//
// inquiry relaxes the requirement that the Claim have items, which a $inquire does not need. A nil Request means the
// Bundle could not be read at all; a Request with problems was read but must not be answered.
func Read(b map[string]any, inquiry bool) (*Request, []string) {
	var problems []string
	if b["resourceType"] != "Bundle" {
		return nil, []string{"the request must be a PAS Request Bundle, and this is not a Bundle"}
	}
	if b["type"] != "collection" {
		problems = append(problems, fmt.Sprintf("Bundle.type must be collection, not %q", Str(b["type"])))
	}
	req := &Request{Bundle: b}
	for _, e := range Slice(b["entry"]) {
		req.Entries = append(req.Entries, Map(e))
	}
	if len(req.Entries) == 0 {
		return nil, append(problems, "the Bundle has no entries; its first entry must be the Claim")
	}
	req.Claim = Map(req.Entries[0]["resource"])
	req.ClaimURL = Str(req.Entries[0]["fullUrl"])
	if req.Claim["resourceType"] != "Claim" {
		return nil, append(problems, "the Bundle's first entry must be the Claim")
	}
	if req.ClaimURL == "" {
		problems = append(problems, "the Claim's entry has no fullUrl")
	}
	if req.Claim["use"] != "preauthorization" {
		problems = append(problems, fmt.Sprintf("Claim.use must be preauthorization, not %q", Str(req.Claim["use"])))
	}
	for _, field := range []string{"patient", "insurer", "provider"} {
		ref := Str(Map(req.Claim[field])["reference"])
		if ref == "" {
			problems = append(problems, "Claim."+field+" is missing")
		} else if req.Resolve(ref) == nil {
			problems = append(problems, fmt.Sprintf("Claim.%s %q is not in the Bundle", field, ref))
		}
	}
	if !inquiry && len(Slice(req.Claim["item"])) == 0 {
		problems = append(problems, "the Claim has no items: there is nothing to authorize")
	}
	return req, problems
}

// Resolve finds the entry a reference names: by fullUrl, or relative to the Claim's base as FHIR resolves a relative
// reference inside a Bundle. It returns the entry, not the resource, so a caller can cite the entry's fullUrl.
func (r *Request) Resolve(ref string) map[string]any {
	if ref == "" {
		return nil
	}
	full := ref
	if !strings.Contains(ref, ":") && strings.Count(ref, "/") == 1 {
		if base := baseOf(r.ClaimURL); base != "" {
			full = base + "/" + ref
		}
	}
	for _, e := range r.Entries {
		u := Str(e["fullUrl"])
		if u == ref || u == full {
			return e
		}
		res := Map(e["resource"])
		if u == "" && ref == Str(res["resourceType"])+"/"+Str(res["id"]) {
			return e
		}
	}
	return nil
}

// Service is one requested service: a Claim item and the codes that say what it is.
type Service struct {
	Sequence string           // Claim.item.sequence, as text
	Item     map[string]any   // the Claim.item itself
	Codes    []map[string]any // CodeableConcepts naming the service; empty when the item names none
}

// Services returns every item of the Claim, in order.
func (r *Request) Services() []Service {
	var out []Service
	for _, it := range Slice(r.Claim["item"]) {
		item := Map(it)
		out = append(out, Service{Sequence: fmt.Sprint(item["sequence"]), Item: item, Codes: r.ServiceCodes(item)})
	}
	return out
}

// Service returns the item with the given sequence.
func (r *Request) Service(sequence string) (Service, bool) {
	for _, s := range r.Services() {
		if s.Sequence == sequence {
			return s, true
		}
	}
	return Service{}, false
}

// ServiceCodes returns what an item says it is: its productOrService, and the code of every order its requestedService
// extension references. A CodeableConcept that says it has no code (a data-absent-reason) is not a code.
func (r *Request) ServiceCodes(item map[string]any) []map[string]any {
	var codes []map[string]any
	if cc := Map(item["productOrService"]); UsableCode(cc) {
		codes = append(codes, cc)
	}
	for _, e := range Slice(item["extension"]) {
		em := Map(e)
		if Str(em["url"]) != Base+"extension-requestedService" {
			continue
		}
		if order := r.Resolve(Str(Map(em["valueReference"])["reference"])); order != nil {
			res := Map(order["resource"])
			for _, f := range []string{"code", "codeCodeableConcept", "medicationCodeableConcept"} {
				if cc := Map(res[f]); UsableCode(cc) {
					codes = append(codes, cc)
				}
			}
		}
	}
	return codes
}

// Resources returns every resource in the Bundle of the given type, with the entry each came from.
func (r *Request) Resources(resourceType string) []map[string]any {
	var out []map[string]any
	for _, e := range r.Entries {
		if Str(Map(e["resource"])["resourceType"]) == resourceType {
			out = append(out, e)
		}
	}
	return out
}

// DataAbsentReason is the code system a CodeableConcept uses to say it has no value.
const DataAbsentReason = "http://terminology.hl7.org/CodeSystem/data-absent-reason"

// UsableCode reports whether a CodeableConcept names something, rather than saying it has no code.
func UsableCode(cc map[string]any) bool {
	for _, c := range Slice(cc["coding"]) {
		cm := Map(c)
		if Str(cm["code"]) != "" && Str(cm["system"]) != DataAbsentReason {
			return true
		}
	}
	return false
}

// Map is v as a JSON object. Anything else is an empty object, never nil, so a chain of lookups into a sparse resource
// reads as absent rather than panicking.
func Map(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

// Slice is v as a JSON array, or nil.
func Slice(v any) []any {
	s, _ := v.([]any)
	return s
}

// Str is v as a JSON string, or "".
func Str(v any) string {
	s, _ := v.(string)
	return s
}

// baseOf is the base of an absolute resource URL: https://x/fhir/Claim/1 is https://x/fhir.
func baseOf(u string) string {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return ""
	}
	parts := strings.Split(strings.TrimRight(u, "/"), "/")
	if len(parts) < 5 {
		return ""
	}
	return strings.Join(parts[:len(parts)-2], "/")
}
