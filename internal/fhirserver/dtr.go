package fhirserver

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Da Vinci DTR 2.1.0: $questionnaire-package.
//
// A DTR client - the EHR's documentation app - asks for the questionnaires the payer wants completed for an order, and gets them back
// as a package: a Bundle per questionnaire holding it and the CQL Libraries it names, so the app can pre-fill answers from the chart. The
// questionnaires are ordinary FHIR resources in this store, loaded by the payer; which one applies to which order is the CRD rules
// file's job, and the order carries it in its coverage-information extension. So a request can name the questionnaire directly or just
// send the order.

const crdCoverageInfo = "http://hl7.org/fhir/us/davinci-crd/StructureDefinition/ext-coverage-information"

func (s *Server) registerDTR(mux *http.ServeMux) {
	mux.HandleFunc("POST /Questionnaire/$questionnaire-package", s.handleQuestionnairePackage)
}

func (s *Server) handleQuestionnairePackage(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var params struct {
		ResourceType string `json:"resourceType"`
		Parameter    []struct {
			Name           string          `json:"name"`
			ValueCanonical string          `json:"valueCanonical"`
			Resource       json.RawMessage `json:"resource"`
		} `json:"parameter"`
	}
	if err := json.Unmarshal(body, &params); err != nil || params.ResourceType != "Parameters" {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "the body must be a Parameters resource")
		return
	}

	var canonicals []string
	seen := map[string]bool{}
	add := func(c string) {
		c = strings.TrimSpace(c)
		if c != "" && !seen[c] {
			seen[c] = true
			canonicals = append(canonicals, c)
		}
	}
	for _, p := range params.Parameter {
		switch p.Name {
		case "questionnaire":
			add(p.ValueCanonical)
		case "order":
			// The questionnaire CRD named on the order, in its coverage-information extension.
			var order struct {
				Extension []struct {
					URL       string `json:"url"`
					Extension []struct {
						URL            string `json:"url"`
						ValueCanonical string `json:"valueCanonical"`
					} `json:"extension"`
				} `json:"extension"`
			}
			if json.Unmarshal(p.Resource, &order) == nil {
				for _, e := range order.Extension {
					if e.URL != crdCoverageInfo {
						continue
					}
					for _, sub := range e.Extension {
						if sub.URL == "questionnaire" {
							add(sub.ValueCanonical)
						}
					}
				}
			}
		}
	}
	if len(canonicals) == 0 {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "required",
			"name a questionnaire, or send an order carrying CRD's coverage-information with one")
		return
	}

	out := map[string]any{"resourceType": "Parameters", "parameter": []any{}}
	var issues []map[string]any
	for _, canonical := range canonicals {
		q := s.byCanonical(r, "Questionnaire", canonical)
		if q == nil {
			issues = append(issues, map[string]any{"severity": "error", "code": "not-found",
				"diagnostics": "no Questionnaire with url " + canonical + " is loaded on this server"})
			continue
		}
		// The resource's own url, not the requested canonical: a |version suffix belongs in a reference, not in a fullUrl.
		full, _ := q["url"].(string)
		entries := []any{map[string]any{"fullUrl": full, "resource": q}}
		// The CQL the questionnaire uses to pre-fill itself, named by cqf-library.
		if exts, ok := q["extension"].([]any); ok {
			for _, e := range exts {
				em, _ := e.(map[string]any)
				if em["url"] != "http://hl7.org/fhir/StructureDefinition/cqf-library" {
					continue
				}
				lib, _ := em["valueCanonical"].(string)
				if l := s.byCanonical(r, "Library", lib); l != nil {
					libURL, _ := l["url"].(string)
					entries = append(entries, map[string]any{"fullUrl": libURL, "resource": l})
				} else {
					issues = append(issues, map[string]any{"severity": "warning", "code": "not-found",
						"diagnostics": "the questionnaire names the Library " + lib + ", which is not loaded"})
				}
			}
		}
		bundle := map[string]any{"resourceType": "Bundle", "type": "collection",
			"meta":  map[string]any{"profile": []string{"http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/DTR-QPackageBundle"}},
			"entry": entries}
		out["parameter"] = append(out["parameter"].([]any), map[string]any{"name": "PackageBundle", "resource": bundle})
	}
	if len(issues) > 0 {
		out["parameter"] = append(out["parameter"].([]any), map[string]any{"name": "Outcome",
			"resource": map[string]any{"resourceType": "OperationOutcome", "issue": issues}})
	}
	s.writeJSON(w, http.StatusOK, out)
}

// byCanonical finds the resource with this canonical url, ignoring a |version suffix.
func (s *Server) byCanonical(r *http.Request, resourceType, canonical string) map[string]any {
	url := strings.SplitN(canonical, "|", 2)[0]
	q, err := ParseSearch(resourceType, map[string][]string{"url": {url}})
	if err != nil {
		return nil
	}
	res, err := s.Store.Search(r.Context(), q)
	if err != nil || len(res.Resources) == 0 {
		return nil
	}
	raw, err := fhir.Marshal(res.Resources[0], s.Store.Version())
	if err != nil {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}
