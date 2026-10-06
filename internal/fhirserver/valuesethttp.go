package fhirserver

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// handleExpand serves ValueSet/$expand.
func (s *Server) handleExpand(w http.ResponseWriter, r *http.Request) {
	if err := operationForm(r); err == nil && storedValueSetURL(r.Form.Get("url")) {
		s.handleExpandStored(w, r, r.Form.Get("url"))
		return
	}
	if s.Tables == nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-supported",
			"this server publishes no value sets because it has no mapping tables loaded")

		return
	}

	if err := operationForm(r); err != nil {
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
	if err := operationForm(r); err == nil && storedValueSetURL(r.Form.Get("url")) {
		s.handleValidateStored(w, r, r.Form.Get("url"))
		return
	}
	if s.Tables == nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-supported",
			"this server publishes no value sets because it has no mapping tables loaded")

		return
	}

	if err := operationForm(r); err != nil {
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

// registerValueSets adds the value set operation routes.
func (s *Server) registerValueSets(mux *http.ServeMux) {
	mux.HandleFunc("GET /ValueSet/$expand", s.handleExpand)
	mux.HandleFunc("POST /ValueSet/$expand", s.handleExpand)
	mux.HandleFunc("GET /ValueSet/$validate-code", s.handleValidateCode)
	mux.HandleFunc("POST /ValueSet/$validate-code", s.handleValidateCode)
}

// operationForm reads an operation's parameters into r.Form, from the query, a form body, or - as FHIR clients usually send
// them - a Parameters resource. The Inferno DTR suite POSTs a Parameters body to $expand, which was refused as unreadable.
func operationForm(r *http.Request) error {
	if r.Form != nil {
		return nil
	}
	ct := r.Header.Get("Content-Type")
	if r.Method != http.MethodPost || !(strings.Contains(ct, "json") || ct == "") {
		return r.ParseForm()
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		return err
	}
	r.Form = url.Values{}
	for k, v := range r.URL.Query() {
		r.Form[k] = v
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil
	}
	var params struct {
		ResourceType string           `json:"resourceType"`
		Parameter    []map[string]any `json:"parameter"`
	}
	if err := json.Unmarshal(body, &params); err != nil || params.ResourceType != "Parameters" {
		return fmt.Errorf("the body must be a Parameters resource")
	}
	for _, p := range params.Parameter {
		name, _ := p["name"].(string)
		for k, v := range p {
			if strings.HasPrefix(k, "value") {
				r.Form.Add(name, fmt.Sprint(v))
			}
		}
	}
	return nil
}
