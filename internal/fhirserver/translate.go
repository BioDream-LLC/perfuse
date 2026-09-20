package fhirserver

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/fhir"
)

// ConceptMap/$translate: what does this code become?
//
// The question an integration analyst answers all day, asked in the standard way. The answer comes from the same table the running
// channel uses, so what a client is told here and what the engine actually does cannot disagree - which they would if this consulted a
// copy.

// translateParams are the parameters $translate accepts here.
//
// Anything else is refused. Ignoring an unrecognised parameter on a translation is worse than it sounds: a caller who passes a target
// system and is silently given a mapping into a different one has been answered confidently and wrongly, and will not check.
var translateParams = map[string]bool{
	"code":   true,
	"system": true,
	"url":    true,
}

// TranslateRequest is a parsed $translate call.
type TranslateRequest struct {
	// Code is the value to translate.
	Code string

	// Map identifies the table, by id, name or canonical URL. Taken from url, or from the path when called on an instance.
	Map string
}

// ParseTranslate reads the parameters for $translate.
//
// conceptMapVersion, source, target, dependency and reverse are all in the specification and are not implemented. They are refused by
// name rather than ignored, for the reason above: a translation that quietly disregarded "reverse" would return the mapping backwards,
// and backwards is a plausible-looking answer.
func ParseTranslate(instanceID string, values map[string][]string) (*TranslateRequest, error) {
	req := &TranslateRequest{Map: instanceID}

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		if translateParams[name] {
			continue
		}

		switch name {
		case "reverse", "source", "target", "targetsystem", "conceptMapVersion", "dependency", "coding", "codeableConcept":
			return nil, fmt.Errorf("%s is part of the $translate specification but is not implemented by this server, "+
				"so it has been refused rather than ignored: answering as though it had not been sent would give a "+
				"confident wrong translation. Pass code, system and url", name)
		default:
			return nil, fmt.Errorf("%s is not a parameter of $translate: the supported ones are code, system and url", name)
		}
	}

	if vals := values["code"]; len(vals) > 0 {
		if len(vals) > 1 {
			return nil, fmt.Errorf("code was given more than once, and one call translates one code")
		}
		req.Code = vals[0]
	}
	if req.Code == "" {
		return nil, fmt.Errorf("code is required: $translate needs a code to translate")
	}

	if vals := values["url"]; len(vals) > 0 {
		if len(vals) > 1 {
			return nil, fmt.Errorf("url was given more than once, and one call uses one concept map")
		}
		if req.Map != "" && req.Map != vals[0] && ConceptMapID(strings.TrimPrefix(vals[0], ConceptMapBaseURL)) != req.Map {
			return nil, fmt.Errorf("this was called on concept map %q but url names %q, and it is not clear which was meant",
				req.Map, vals[0])
		}
		req.Map = vals[0]
	}

	if req.Map == "" {
		return nil, fmt.Errorf("url is required when $translate is not called on a particular concept map, " +
			"because there is no sensible default: translating against whichever table happened to be first would be a guess")
	}

	return req, nil
}

// TranslateResult is the outcome of a translation.
type TranslateResult struct {
	// Matched is whether the code was translated.
	Matched bool

	// Code is what it became, when Matched.
	Code string

	// Message explains the outcome, and is what an operator reads.
	Message string

	// Refused is set when a strict table has no entry for the code. Distinct from a plain miss because the running channel
	// would fail the message rather than pass it through, and a client needs to know that is what will happen.
	Refused bool
}

// Translate answers what a code becomes under a mapping table.
//
// Uses the table's own Lookup, so the answer is the one the engine would give. Reimplementing the lookup here would be a second
// definition of the mapping, and the first thing that happens to a second definition is that it drifts - after which the console and the
// running feed disagree about a patient's sex code and nobody knows which is right.
func Translate(t *codeset.Table, code string) TranslateResult {
	if t.Strict {
		got, err := t.Strictly(code)
		if err != nil {
			return TranslateResult{
				Refused: true,
				Message: fmt.Sprintf("%q is not in the %s table, and that table is strict: "+
					"a message carrying this code would be refused rather than passed through", code, t.Name),
			}
		}

		return TranslateResult{Matched: true, Code: got, Message: describeMatch(t, code, got)}
	}

	got, entry, mapped := t.Lookup(code)
	if mapped {
		_ = entry

		return TranslateResult{Matched: true, Code: got, Message: describeMatch(t, code, got)}
	}

	// Not in the table. The table still has an answer - a default, or the code unchanged - and saying which one is the difference
	// between a client knowing what will happen to this code and guessing.
	if t.Default != "" {
		return TranslateResult{
			Matched: true,
			Code:    got,
			Message: fmt.Sprintf("%q is not in the %s table, so the table's default %q applies", code, t.Name, t.Default),
		}
	}

	return TranslateResult{
		Matched: true,
		Code:    got,
		Message: fmt.Sprintf("%q is not in the %s table, which passes unmapped codes through unchanged", code, t.Name),
	}
}

// describeMatch says what happened, including the recorded reason when there is one.
func describeMatch(t *codeset.Table, from, to string) string {
	msg := fmt.Sprintf("%q maps to %q in the %s table", from, to, t.Name)

	for _, e := range t.Entries {
		if e.From != from {
			continue
		}
		if c := entryComment(e); c != "" {
			msg += ": " + c
		}

		break
	}

	return msg
}

// TranslateParameters renders a result as the Parameters resource $translate is defined to return.
func TranslateParameters(res TranslateResult) *fhir.Parameters {
	out := &fhir.Parameters{}
	out.SetResourceID("translate")

	out.Parameter = []fhir.ParametersParameter{
		{Name: "result", ValueBoolean: fhir.Bool(res.Matched)},
		{Name: "message", ValueString: res.Message},
	}

	if res.Matched {
		out.Parameter = append(out.Parameter, fhir.ParametersParameter{
			Name: "match",
			Part: []fhir.ParametersParameter{
				{Name: "equivalence", ValueString: "equivalent"},
				{Name: "concept", ValueCoding: &fhir.Coding{Code: res.Code}},
			},
		})
	}

	return out
}
