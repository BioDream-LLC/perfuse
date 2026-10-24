package fhirserver

import (
	"strings"

	"github.com/biodream-llc/perfuse/internal/davinci"
)

// PAS 2.1.0 for a client that speaks it.
//
// Every PAS answer is built, decided, stored and notified in PAS 2.2.1's form; a client of PAS 2.1.0 gets it rewritten into
// 2.1.0's at the moment it is sent. One form inside keeps one implementation of the decisions, the 278 mapping, the review
// queue and the metrics, and the rewrite only touches what the two releases spell differently. The list is what the 2.1.0
// and 2.2.1 profiles differ in for the elements a response carries, checked by the HL7 validator against PAS 2.1.0
// (conformance/davinci21):
//
//   - profiles claim |2.1.0;
//   - extensions 2.1.0 does not define go: claimResponseReviewer, TransmissionIdentifiers, admissionDates, dischargeDate,
//     itemAuthorizedProvider's qualification and role, contentModifier on a Task input;
//   - errorElement is a string in 2.1.0 (2.2.1 splits it into error and processNote);
//   - a Task input's line number is extension-paLineNumber in 2.1.0, and a CommunicationRequest's goes on its payload.

// pas21Dropped are the extensions PAS 2.1.0 does not define, by the URL after pasBase.
var pas21Dropped = map[string]bool{
	"extension-claimResponseReviewer":   true,
	"extension-TransmissionIdentifiers": true,
	"extension-admissionDates":          true,
	"extension-dischargeDate":           true,
	"extension-documentInformation":     true,
	"extension-itemCategory":            true,
}

// pasForVersion is a PAS answer (a Bundle, or one resource of it) as a client of version v reads it. The stored answer is never changed.
func pasForVersion(answer map[string]any, v string) map[string]any {
	if v != davinci.V21 || answer == nil {
		return answer
	}
	out := deepCopy(answer)
	pas21Resource(out, "")
	return out
}

// pas21Resource rewrites one node; parent is the FHIR path of the element holding it, enough to know a Task input or a
// CommunicationRequest.
func pas21Resource(node any, parent string) {
	switch v := node.(type) {
	case map[string]any:
		if meta := asMapAny(v["meta"]); meta != nil {
			if list := asSliceAny(meta["profile"]); list != nil {
				for i, p := range list {
					if s := str(p); strings.HasPrefix(s, pasBase) {
						base, _, _ := strings.Cut(s, "|")
						list[i] = base + "|2.1.0"
					}
				}
			}
		}
		here := parent
		if rt := str(v["resourceType"]); rt != "" {
			here = rt
		}
		if ext := asSliceAny(v["extension"]); ext != nil {
			v["extension"] = pas21Extensions(ext, here)
			if len(asSliceAny(v["extension"])) == 0 {
				delete(v, "extension")
			}
		}
		if here == "CommunicationRequest" {
			// 2.1.0 puts the service line number on the payload, not on the request.
			var line []any
			var rest []any
			for _, e := range asSliceAny(v["extension"]) {
				if str(asMapAny(e)["url"]) == pasBase+"extension-serviceLineNumber" {
					line = append(line, e)
				} else {
					rest = append(rest, e)
				}
			}
			if line != nil {
				if rest == nil {
					delete(v, "extension")
				} else {
					v["extension"] = rest
				}
				for _, p := range asSliceAny(v["payload"]) {
					pm := asMapAny(p)
					pm["extension"] = append(asSliceAny(pm["extension"]), line...)
				}
			}
		}
		for k, c := range v {
			if k == "meta" || k == "extension" {
				continue
			}
			path := here
			if k == "input" && here == "Task" {
				path = "Task.input"
			} else if k == "payload" && here == "CommunicationRequest" {
				path = "CommunicationRequest.payload"
			} else if k == "resource" || k == "entry" {
				path = ""
			}
			pas21Resource(c, path)
		}
	case []any:
		for _, c := range v {
			pas21Resource(c, parent)
		}
	}
}

func pas21Extensions(list []any, where string) []any {
	var out []any
	for _, e := range list {
		em := asMapAny(e)
		url := str(em["url"])
		name := strings.TrimPrefix(url, pasBase)
		switch {
		case name != url && pas21Dropped[name]:
			continue
		case name == "extension-errorElement":
			// 2.1.0: the X12 element as the value. 2.2.1 holds it in the error sub-extension.
			for _, sub := range asSliceAny(em["extension"]) {
				if sm := asMapAny(sub); str(sm["url"]) == "error" {
					e = map[string]any{"url": url, "valueString": sm["valueString"]}
				}
			}
			if _, ok := asMapAny(e)["valueString"]; !ok {
				continue
			}
		case name == "extension-itemAuthorizedProvider":
			var parts []any
			for _, sub := range asSliceAny(em["extension"]) {
				if u := str(asMapAny(sub)["url"]); u != "qualification" && u != "role" {
					parts = append(parts, sub)
				}
			}
			e = map[string]any{"url": url, "extension": parts}
		case where == "Task.input" && name == "extension-serviceLineNumber":
			e = map[string]any{"url": pasBase + "extension-paLineNumber", "valueInteger": em["valuePositiveInt"]}
		case where == "Task.input" && name == "extension-contentModifier":
			continue
		}
		out = append(out, e)
	}
	return out
}

// mergeInquiryAnswers is several inquiry response Bundles as one: every entry once, by fullUrl, the first Bundle's metadata.
func mergeInquiryAnswers(answers []map[string]any) map[string]any {
	out := deepCopy(answers[0])
	seen := map[string]bool{}
	var entries []any
	for _, b := range answers {
		for _, e := range asSliceAny(b["entry"]) {
			u := str(asMapAny(e)["fullUrl"])
			if u != "" && seen[u] {
				continue
			}
			seen[u] = true
			entries = append(entries, deepCopy(asMapAny(e)))
		}
	}
	out["entry"] = entries
	return out
}
