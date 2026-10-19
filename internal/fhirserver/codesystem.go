package fhirserver

import (
	"context"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/fhir"
	"github.com/biodream-llc/perfuse/internal/terminology"
)

// CodeLookup is a store of imported code systems (LOINC, SNOMED CT, RxNorm, ICD-10-CM, CPT, local ones).
type CodeLookup interface {
	// LookupCode finds a code: its display, whether it is active, the version found in, and whether it was found at all.
	LookupCode(ctx context.Context, system, version, code string) (display, foundVersion string, active, found bool, err error)
	// HasSystem reports whether any version of the system is imported.
	HasSystem(ctx context.Context, system string) bool
}

// registerCodeSystems adds CodeSystem/$lookup and CodeSystem/$validate-code, answered from the imported code systems.
func (s *Server) registerCodeSystems(mux *http.ServeMux) {
	for _, m := range []string{"GET", "POST"} {
		mux.HandleFunc(m+" /CodeSystem/$lookup", s.handleCodeLookup)
		mux.HandleFunc(m+" /CodeSystem/$validate-code", s.handleCodeValidate)
	}
}

// codeParams reads system, code and version, from the parameters or from a coding.
func (s *Server) codeParams(w http.ResponseWriter, r *http.Request) (system, code, version string, ok bool) {
	if s.Codes == nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-supported",
			"no code systems are imported on this server; an administrator imports them on the Tables page")
		return "", "", "", false
	}
	if err := operationForm(r); err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", "the parameters could not be read: "+err.Error())
		return "", "", "", false
	}
	system, code, version = r.Form.Get("system"), r.Form.Get("code"), r.Form.Get("version")
	if system == "" {
		system = r.Form.Get("url")
	}
	if c := r.Form.Get("coding"); c != "" && code == "" {
		// A coding given as system|code, which is how a coding travels in a query string.
		if i := strings.LastIndex(c, "|"); i > 0 {
			system, code = c[:i], c[i+1:]
		}
	}
	if system == "" || code == "" {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "required", "give system (or url) and code, or a coding")
		return "", "", "", false
	}
	return system, code, version, true
}

func (s *Server) handleCodeLookup(w http.ResponseWriter, r *http.Request) {
	system, code, version, ok := s.codeParams(w, r)
	if !ok {
		return
	}
	if !s.Codes.HasSystem(r.Context(), system) {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-found", system+" is not imported on this server")
		return
	}
	display, ver, active, found, err := s.Codes.LookupCode(r.Context(), system, version, code)
	if err != nil {
		s.writeOutcome(w, r, http.StatusInternalServerError, "error", "exception", err.Error())
		return
	}
	if !found {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-found", code+" is not a code in "+system)
		return
	}
	inactive := !active
	var designations []fhir.ParametersParameter
	if dl, ok := s.Codes.(DesignationLookup); ok {
		list, err := dl.Designations(r.Context(), system, ver, code)
		if err != nil {
			s.writeOutcome(w, r, http.StatusInternalServerError, "error", "exception", err.Error())
			return
		}
		// displayLanguage: the display in that language when the code has a preferred name in it (de-CH falls back to de).
		if want := strings.ToLower(r.Form.Get("displayLanguage")); want != "" {
			base := strings.SplitN(want, "-", 2)[0]
			pick := ""
			for _, d := range list {
				l := strings.ToLower(d.Language)
				if d.Preferred && l == want {
					pick = d.Value
					break
				}
				if d.Preferred && l == base && pick == "" {
					pick = d.Value
				}
			}
			if pick != "" {
				display = pick
			}
		}
		for _, d := range list {
			use := &fhir.Coding{System: "http://snomed.info/sct", Code: "900000000000013009", Display: "Synonym"}
			if d.Use == "fsn" {
				use = &fhir.Coding{System: "http://snomed.info/sct", Code: "900000000000003001", Display: "Fully specified name"}
			}
			designations = append(designations, fhir.ParametersParameter{Name: "designation", Part: []fhir.ParametersParameter{
				{Name: "language", ValueCode: d.Language},
				{Name: "use", ValueCoding: use},
				{Name: "value", ValueString: d.Value},
			}})
		}
	}
	out := &fhir.Parameters{Parameter: append([]fhir.ParametersParameter{
		{Name: "name", ValueString: system},
		{Name: "version", ValueString: ver},
		{Name: "display", ValueString: display},
		{Name: "inactive", ValueBoolean: &inactive},
	}, designations...)}
	s.writeResource(w, r, http.StatusOK, out)
}

// DesignationLookup is a CodeLookup that also knows a code's names in other languages (a SNOMED CT edition's).
type DesignationLookup interface {
	Designations(ctx context.Context, system, version, code string) ([]terminology.Designation, error)
}

func (s *Server) handleCodeValidate(w http.ResponseWriter, r *http.Request) {
	system, code, version, ok := s.codeParams(w, r)
	if !ok {
		return
	}
	result := false
	message := ""
	display := ""
	if !s.Codes.HasSystem(r.Context(), system) {
		message = system + " is not imported on this server, so the code cannot be checked"
	} else {
		d, _, active, found, err := s.Codes.LookupCode(r.Context(), system, version, code)
		switch {
		case err != nil:
			s.writeOutcome(w, r, http.StatusInternalServerError, "error", "exception", err.Error())
			return
		case !found:
			message = code + " is not a code in " + system
		case !active:
			result, display, message = true, d, code+" is in "+system+" but is no longer active"
		default:
			result, display = true, d
		}
		if want := r.Form.Get("display"); result && want != "" && !strings.EqualFold(want, display) {
			result, message = false, "the display "+want+" is not the display of "+code+" ("+display+")"
		}
	}
	out := &fhir.Parameters{Parameter: []fhir.ParametersParameter{{Name: "result", ValueBoolean: &result}}}
	if display != "" {
		out.Parameter = append(out.Parameter, fhir.ParametersParameter{Name: "display", ValueString: display})
	}
	if message != "" {
		out.Parameter = append(out.Parameter, fhir.ParametersParameter{Name: "message", ValueString: message})
	}
	s.writeResource(w, r, http.StatusOK, out)
}

// codeSystemCapability declares $lookup and $validate-code on CodeSystem, adding the resource entry when CodeSystem is not
// otherwise listed.
func codeSystemCapability(resources []any) []any {
	ops := []any{
		map[string]any{"name": "lookup", "definition": "http://hl7.org/fhir/OperationDefinition/CodeSystem-lookup"},
		map[string]any{"name": "validate-code", "definition": "http://hl7.org/fhir/OperationDefinition/CodeSystem-validate-code"},
	}
	for _, r := range resources {
		if m, ok := r.(map[string]any); ok && m["type"] == "CodeSystem" {
			existing, _ := m["operation"].([]any)
			m["operation"] = append(existing, ops...)
			return resources
		}
	}
	return append(resources, map[string]any{"type": "CodeSystem", "operation": ops,
		"documentation": "Answered from the code systems an administrator imported (LOINC, SNOMED CT, RxNorm, ICD-10-CM, CPT, local)."})
}
