package fhirserver

import (
	"net/http"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// handleExpand serves ValueSet/$expand.
func (s *Server) handleExpand(w http.ResponseWriter, r *http.Request) {
	if s.Tables == nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-supported",
			"this server publishes no value sets because it has no mapping tables loaded")

		return
	}

	if err := r.ParseForm(); err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid",
			"the parameters could not be read: "+err.Error())

		return
	}

	req, err := ParseExpand(r.Form)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", err.Error())

		return
	}

	table, err := FindConceptMap(s.Tables, req.Map)
	if err != nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-found", err.Error())

		return
	}

	codes, total := ExpandTable(table, req)

	s.writeResource(w, r, http.StatusOK, TableAsValueSet(table, req, codes, total))
}

// handleValidateCode serves ValueSet/$validate-code.
func (s *Server) handleValidateCode(w http.ResponseWriter, r *http.Request) {
	if s.Tables == nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-supported",
			"this server publishes no value sets because it has no mapping tables loaded")

		return
	}

	if err := r.ParseForm(); err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid",
			"the parameters could not be read: "+err.Error())

		return
	}

	req, err := ParseValidateCode(r.Form)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", err.Error())

		return
	}

	table, err := FindConceptMap(s.Tables, req.Map)
	if err != nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-found", err.Error())

		return
	}

	ok, message := ValidateCodeInTable(table, req)

	// A code that is not in the set is a 200 with result false, not a 4xx.
	//
	// The request was valid and the answer is "no". Returning an error status would make a legitimate question look like a mistake,
	// and a client cannot distinguish "your request was wrong" from "the answer is no" by status code alone.
	out := &fhir.Parameters{}
	out.SetResourceID("validate-code")
	out.Parameter = []fhir.ParametersParameter{
		{Name: "result", ValueBoolean: fhir.Bool(ok)},
		{Name: "message", ValueString: message},
	}

	s.writeResource(w, r, http.StatusOK, out)
}

// handleValueSetIntercept answers ValueSet requests that arrive at the generic handlers.
//
// Same arrangement as ConceptMap and for the same reason: a dedicated GET /ValueSet/{id} would collide with the generic
// GET /{type}/_history and stop the router from starting.
func (s *Server) valueSetIntercept(w http.ResponseWriter, r *http.Request, resourceType string) bool {
	if resourceType != "ValueSet" {
		return false
	}

	if r.Method != http.MethodGet {
		s.writeOutcome(w, r, http.StatusMethodNotAllowed, "error", "not-supported",
			"value sets cannot be written through this API because they are views of the mapping tables, "+
				"which live in a .codeset.yaml file beside the channels. Edit the table there")

		return true
	}

	// A read or a search would have to enumerate value sets, and there are two per mapping. Rather than invent a listing
	// nobody asked for, this says how to get what is actually useful.
	s.writeOutcome(w, r, http.StatusNotFound, "error", "not-supported",
		"value sets here are views of a mapping table and are reached through $expand or $validate-code, "+
			"with a url naming the mapping and which side of it: "+ConceptMapBaseURL+"<table>"+SourceSuffix+
			" for the codes accepted, or "+TargetSuffix+" for the codes produced. "+
			"The mappings themselves are listed at /ConceptMap")

	return true
}

// registerValueSets adds the value set operation routes.
func (s *Server) registerValueSets(mux *http.ServeMux) {
	mux.HandleFunc("GET /ValueSet/$expand", s.handleExpand)
	mux.HandleFunc("POST /ValueSet/$expand", s.handleExpand)
	mux.HandleFunc("GET /ValueSet/$validate-code", s.handleValidateCode)
	mux.HandleFunc("POST /ValueSet/$validate-code", s.handleValidateCode)
}

// valueSetCapability describes the value set support for the capability statement.
func (s *Server) valueSetCapability() map[string]any {
	if s.Tables == nil || len(s.Tables.Tables()) == 0 {
		return nil
	}

	return map[string]any{
		"type": "ValueSet",
		// No read and no search, which is the truth. Claiming them would be a promise answered by a 404.
		"interaction": []any{},
		"operation": []any{
			map[string]any{
				"name":       "expand",
				"definition": "http://hl7.org/fhir/OperationDefinition/ValueSet-expand",
			},
			map[string]any{
				"name":       "validate-code",
				"definition": "http://hl7.org/fhir/OperationDefinition/ValueSet-validate-code",
			},
		},
		"documentation": "Views of the channel mapping tables: the codes a mapping accepts, and the codes it can produce. " +
			"Reached by url with a :source or :target suffix.",
	}
}
