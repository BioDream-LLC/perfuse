package fhirserver

import (
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// handleConceptMapSearch lists the mapping tables as ConceptMaps.
func (s *Server) handleConceptMapSearch(w http.ResponseWriter, r *http.Request) {
	if s.Tables == nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-supported",
			"this server has no mapping tables loaded, so it publishes no concept maps. "+
				"Tables are declared on a channel with tables: and live in a .codeset.yaml file beside it")

		return
	}

	maps := ConceptMaps(s.Tables)

	bundle := &fhir.Bundle{
		Type:      fhir.BundleSearchset,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
		Total:     fhir.Int(len(maps)),
	}
	bundle.SetResourceID("conceptmaps")
	bundle.Link = []fhir.BundleLink{{Relation: "self", URL: s.BaseURL + "/ConceptMap"}}

	for _, cm := range maps {
		bundle.Entry = append(bundle.Entry, fhir.BundleEntry{
			FullURL:  s.BaseURL + "/ConceptMap/" + cm.ResourceID(),
			Resource: cm,
			Search:   &fhir.BundleEntrySearch{Mode: "match"},
		})
	}

	s.writeResource(w, r, http.StatusOK, bundle)
}

// handleConceptMapRead returns one projected table.
func (s *Server) handleConceptMapRead(w http.ResponseWriter, r *http.Request) {
	table, err := FindConceptMap(s.Tables, r.PathValue("id"))
	if err != nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-found", err.Error())

		return
	}

	s.writeResource(w, r, http.StatusOK, TableAsConceptMap(table))
}

// handleConceptMapWrite refuses writes and says where the mapping actually lives.
//
// A plain 405 would be correct and useless. The person hitting this wants to change a mapping, and the useful answer is which file to
// edit - the same reasoning as everywhere else here: a refusal that does not say what to do instead costs somebody an afternoon.
func (s *Server) handleConceptMapWrite(w http.ResponseWriter, r *http.Request) {
	s.writeOutcome(w, r, http.StatusMethodNotAllowed, "error", "not-supported",
		"concept maps cannot be written through this API because they are projections of the mapping tables, "+
			"which live in a .codeset.yaml file beside the channels and travel with them into version control. "+
			"Edit the table there: a mapping with two sources of truth ends with somebody editing the copy that is not running")
}

// handleTranslate serves ConceptMap/$translate.
func (s *Server) handleTranslate(w http.ResponseWriter, r *http.Request) {
	if s.Tables == nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-supported",
			"this server has no mapping tables loaded, so there is nothing to translate with")

		return
	}

	if err := r.ParseForm(); err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid",
			"the parameters could not be read: "+err.Error())

		return
	}

	req, err := ParseTranslate(r.PathValue("id"), r.Form)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, "error", "invalid", err.Error())

		return
	}

	table, err := FindConceptMap(s.Tables, req.Map)
	if err != nil {
		s.writeOutcome(w, r, http.StatusNotFound, "error", "not-found", err.Error())

		return
	}

	result := Translate(table, req.Code)

	// A strict table with no entry is reported as a failed translation with the reason, not as an error.
	//
	// The request was valid and the answer is "this code has no mapping and would be refused". That is information the caller
	// asked for, so it comes back as an answer rather than a 4xx - and it says what the running channel would do, which is the
	// part somebody needs before they send the message.
	s.writeResource(w, r, http.StatusOK, TranslateParameters(result))
}

// registerTerminology adds the concept map operation routes.
//
// Only $translate is registered as its own route. Read, search, write and history for ConceptMap are intercepted inside the generic
// handlers instead, by conceptMapIntercept below.
//
// Not a preference. GET /ConceptMap/{id} and the generic GET /{type}/_history both match /ConceptMap/_history and neither is more
// specific, so Go's ServeMux refuses to start - correctly, because the alternative is silently picking one. Rather than bend the
// generic routes around one projected type, ConceptMap is handled where the request already arrives, which also means it inherits the
// same authentication as every other read instead of needing its own.
func (s *Server) registerTerminology(mux *http.ServeMux) {
	mux.HandleFunc("GET /ConceptMap/$translate", s.handleTranslate)
	mux.HandleFunc("POST /ConceptMap/$translate", s.handleTranslate)
	mux.HandleFunc("GET /ConceptMap/{id}/$translate", s.handleTranslate)
	mux.HandleFunc("POST /ConceptMap/{id}/$translate", s.handleTranslate)
}

// conceptMapIntercept serves ConceptMap requests that arrive at the generic handlers.
//
// Returns true when it has answered. One function rather than a branch in each handler, so there is a single place that decides what
// ConceptMap does and no chance of read being intercepted while delete is not.
func (s *Server) conceptMapIntercept(w http.ResponseWriter, r *http.Request, resourceType string) bool {
	if resourceType != "ConceptMap" {
		return false
	}

	switch r.Method {
	case http.MethodGet:
		if strings.HasSuffix(r.URL.Path, "/_history") {
			s.handleConceptMapNoHistory(w, r)

			return true
		}
		if id := r.PathValue("id"); id != "" {
			s.handleConceptMapRead(w, r)

			return true
		}

		s.handleConceptMapSearch(w, r)

		return true

	default:
		s.handleConceptMapWrite(w, r)

		return true
	}
}

// terminologyCapability describes the concept map support for the capability statement.
//
// Returns nothing at all when no tables are loaded. A statement advertising ConceptMap on a server with no tables would be a promise
// answered by a 404, and a capability statement that overstates is worse than none.
func (s *Server) terminologyCapability() map[string]any {
	if s.Tables == nil || len(s.Tables.Tables()) == 0 {
		return nil
	}

	return map[string]any{
		"type": "ConceptMap",
		// Read and search only, which is the truth: the tables are files.
		"interaction": []any{
			map[string]any{"code": "read"},
			map[string]any{"code": "search-type"},
		},
		"operation": []any{
			map[string]any{
				"name":       "translate",
				"definition": "http://hl7.org/fhir/OperationDefinition/ConceptMap-translate",
			},
		},
		"documentation": "Projected from the channel mapping tables, which are files. " +
			"Read-only here: the file is the source of truth.",
	}
}

// handleConceptMapNoHistory explains why a projected map has no versions.
//
// A concept map here is a view of a file, so its history is the file's history - which lives in version control, not in this database.
// Saying so is more use than a 404, which would suggest the map does not exist.
func (s *Server) handleConceptMapNoHistory(w http.ResponseWriter, r *http.Request) {
	s.writeOutcome(w, r, http.StatusNotFound, "error", "not-supported",
		"concept maps have no version history here because they are projections of a mapping table file. "+
			"The history of the mapping is the history of that file, in whatever version control holds the channels")
}
