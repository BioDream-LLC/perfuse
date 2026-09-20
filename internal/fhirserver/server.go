package fhirserver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Server is the FHIR REST endpoint.
type Server struct {
	// Store persists resources.
	Store *Store

	// BaseURL is advertised in the capability statement and used to build
	// absolute URLs in bundles.
	BaseURL string

	// Log receives request events.
	Log *slog.Logger

	// Tables supplies the mapping tables projected as ConceptMaps. Nil means this server publishes no concept maps, which is
	// the honest state for an installation with no tables rather than an empty list implying there could be some.
	Tables TableSource

	// Export runs bulk exports. Nil disables the operation, which is why it is a pointer rather than a value.
	//
	// Off unless something sets it, because an export produces a file holding every record this server has and that is
	// not a capability to acquire by upgrading. The capability statement and every endpoint say it is unavailable when
	// this is nil rather than answering with a 500.
	Export *ExportManager

	// DefaultCountFn supplies the page size for a search that does not ask for one.
	//
	// A function rather than a value so the setting can change without a restart, and read at request time
	// because that is when it is needed. Falls back to DefaultCount.
	//
	// Deliberately only consulted when the client gave no _count: overriding an explicit request would break
	// paging for a client that asked for ten and silently received fifty.
	DefaultCountFn func() int

	// SMART advertises where a SMART app should authenticate.
	//
	// Empty means .well-known/smart-configuration answers 404, which is the honest answer for a server nobody has
	// configured for SMART.
	SMART SMARTDiscovery

	// ValidateOnWrite rejects a resource that fails validation.
	//
	// On by default in the constructor. A FHIR server that accepts anything is
	// convenient until the day somebody queries the data and finds half of it
	// unusable, and by then it is thousands of records old.
	ValidateOnWrite bool

	// RejectOnWarning also refuses resources that only produce warnings, such as
	// a missing US Core identifier. Off by default, because a warning is a
	// judgement about profile conformance rather than about validity.
	RejectOnWarning bool

	// ReadOnly refuses every write. Useful when exposing an existing store for
	// query only.
	ReadOnly bool

	// ReadOnlyFn supersedes ReadOnly when set, so the setting can change without a restart.
	//
	// A function rather than a value for the reason the message store uses one: reading a plain field while
	// somebody saves settings is a data race, and the settings store already holds the lock that makes it safe.
	// Every read goes through isReadOnly.
	ReadOnlyFn func() bool

	// Auth decides whether a request may proceed.
	//
	// Required. A nil authenticator refuses every request rather than allowing them, because a nil field is a wiring
	// mistake and the safe reading of a mistake on this path is that nobody gets in.
	//
	// This server had no authentication at all until it was tested by hand: a patient record was created with no
	// credentials and read straight back. Everything in this file exists because of that.
	Auth Authenticator
}

// NewServer builds a server with safe defaults.
func NewServer(store *Store, baseURL string, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{
		Store:           store,
		BaseURL:         strings.TrimRight(baseURL, "/"),
		Log:             log,
		ValidateOnWrite: true,
		// No authenticator by default, which refuses everything. A constructor that defaulted to OpenAuth would
		// make an unauthenticated server the easiest thing to build, and that is how this hole existed.
	}
}

// contentType is the FHIR JSON media type. Returning application/json instead is
// accepted by most clients and technically wrong.
const contentType = "application/fhir+json"

// isReadOnly reports whether this server refuses writes.
//
// One accessor rather than five reads of a field, because this became live-editable and the retention window taught what
// happens otherwise: one value read in two places, one of them converted, and the difference silently deleted content.
// Both spellings type-check, so the compiler cannot help.
func (s *Server) isReadOnly() bool {
	if s.ReadOnlyFn != nil {
		return s.ReadOnlyFn()
	}

	return s.ReadOnly
}

// Handler returns the routed handler. Mount it under a prefix such as /fhir.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /metadata", s.handleCapability)
	mux.HandleFunc("GET /", s.handleRoot)
	mux.HandleFunc("POST /", s.handleTransaction)

	mux.HandleFunc("GET /{type}", s.handleSearch)
	mux.HandleFunc("POST /{type}", s.handleCreate)
	mux.HandleFunc("GET /{type}/{id}", s.handleRead)
	mux.HandleFunc("PUT /{type}/{id}", s.handleUpdate)
	mux.HandleFunc("DELETE /{type}/{id}", s.handleDelete)
	mux.HandleFunc("POST /{type}/$validate", s.handleValidate)

	// Registered before the generic /{type}/{id} routes so "$everything" is never mistaken for a resource id.
	s.registerTerminology(mux)
	s.registerValueSets(mux)

	mux.HandleFunc("GET /Patient/{id}/$everything", s.handleEverything)
	mux.HandleFunc("POST /Patient/{id}/$everything", s.handleEverything)

	// History and versioned reads.
	//
	// Registered before the two-segment patterns would be ambiguous, which Go's mux resolves by specificity rather than
	// order - but written together here because they are one feature and splitting them across the list is how one gets
	// removed without the other.
	// Bulk export. The operation, the poll URL and the files.
	mux.HandleFunc("GET /$export", s.handleExport)
	mux.HandleFunc("GET /Patient/$export", s.handleExport)
	// The poll and file paths put their literal segment last, and that is not cosmetic.
	//
	// The obvious shape, /$export-status/{id}, conflicts with /{type}/_history: both match /$export-status/_history and
	// neither is more specific, so the mux panics at startup. Putting the literal third means no path can satisfy both
	// patterns, because one requires the third segment to be "status" and the other requires it to be "_history".
	mux.HandleFunc("GET /_export/{id}/status", s.handleExportStatus)
	mux.HandleFunc("DELETE /_export/{id}/status", s.handleExportStatus)
	mux.HandleFunc("GET /_export/{id}/files/{type}", s.handleExportFile)

	mux.HandleFunc("GET /{type}/_history", s.handleTypeHistory)
	mux.HandleFunc("GET /{type}/{id}/_history", s.handleInstanceHistory)
	mux.HandleFunc("GET /{type}/{id}/_history/{vid}", s.handleVersionRead)

	// Everything is wrapped, including /metadata.
	//
	// The capability statement is the one plausible candidate for being left open, since SMART clients read it to
	// discover where to authenticate. It is still wrapped here, because it also lists every resource type and search
	// parameter this server holds - which tells an unauthenticated caller exactly what is worth asking for. A SMART
	// deployment publishes its authorization endpoints through .well-known instead, which is the place designed for
	// being read before anyone has a token.
	//
	// Wrapping the whole mux rather than each handler means a route added later is protected by default. Listing
	// routes individually is how one gets forgotten.
	protected := s.requireAuth(mux)

	// The SMART discovery document is the one deliberate exception, and it is served outside the wrapper rather
	// than exempted inside it.
	//
	// An app reads it before it holds any credential - that is what it is for - so requiring one would make SMART
	// impossible. Safe for a specific reason: it contains the addresses of an authorization server that is already
	// public, and nothing about what this server holds. The capability statement, which does list every resource
	// type and search parameter, stays behind authentication.
	//
	// Outside rather than a carve-out inside requireAuth because a carve-out is a path comparison in the middle of
	// the authentication check, and that is exactly where an exemption grows to cover more than it should.
	outer := http.NewServeMux()
	outer.HandleFunc("GET /.well-known/smart-configuration", s.handleSMARTConfiguration)
	outer.Handle("/", protected)

	return outer
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found",
		"no resource type given; try /metadata or /Patient")
}

// handleCapability serves the capability statement.
//
// It advertises only what is actually implemented, including which search
// parameters work. A capability statement that overstates the server is worse than
// none, because a client trusts it.
func (s *Server) handleCapability(w http.ResponseWriter, r *http.Request) {
	version := s.resolveVersion(r)

	resources := make([]any, 0, len(SearchParams))
	types := make([]string, 0, len(SearchParams))
	for t := range SearchParams {
		types = append(types, t)
	}
	sortStrings(types)

	for _, t := range types {
		params := make([]any, 0, len(SearchParams[t]))
		for _, p := range SearchParams[t] {
			params = append(params, map[string]any{
				"name": p,
				"type": searchParamType(p),
			})
		}

		interactions := []any{
			map[string]any{"code": "read"},
			map[string]any{"code": "search-type"},
			// vread and the two histories are read interactions, so they are listed here rather than under the
			// write block - a read-only server still serves them, and a client checking whether it can audit a
			// record needs the answer to be yes on a read-only deployment.
			map[string]any{"code": "vread"},
			map[string]any{"code": "history-instance"},
			map[string]any{"code": "history-type"},
		}
		if !s.isReadOnly() {
			interactions = append(interactions,
				map[string]any{"code": "create"},
				map[string]any{"code": "update"},
				map[string]any{"code": "delete"},
			)
		}

		entry := map[string]any{
			"type": t,
			// versioned-update, which says three things together: versions are tracked, they can be read,
			// and If-Match is honoured on a write. A client that supports optimistic concurrency looks at
			// exactly this field to decide whether to bother.
			"versioning":      "versioned-update",
			"readHistory":     true,
			"updateCreate":    !s.isReadOnly(),
			"conditionalRead": "not-supported",
			"searchParam":     params,
			"interaction":     interactions,
		}

		// What may be included, so a client discovers it rather than guessing.
		//
		// Generated from the same table the include resolver uses, so the statement cannot promise an include this
		// server would then refuse - which is the specific way a capability statement becomes worse than none.
		if inc := includeOptions(t); len(inc) > 0 {
			entry["searchInclude"] = inc
		}

		// Operations on this type, declared only where they are actually served.
		//
		// $everything is on Patient and nothing else, so it is named here rather than in a list at the top that would
		// imply it works everywhere. A capability statement that overstates is worse than none: a client reads this to
		// decide what to call, and a promise it then honours with a 404 costs more than saying nothing.
		if t == "Patient" {
			entry["operation"] = []any{
				map[string]any{
					"name":       "everything",
					"definition": "http://hl7.org/fhir/OperationDefinition/Patient-everything",
				},
			}
		}
		if rev := revIncludeOptions(t); len(rev) > 0 {
			entry["searchRevInclude"] = rev
		}

		resources = append(resources, entry)
	}

	// ConceptMap, when there are tables to publish. Added here rather than in the loop above because it is not a stored type -
	// it is projected from files - so it does not appear in the resource registry the loop walks.
	if term := s.terminologyCapability(); term != nil {
		resources = append(resources, term)
	}
	if vs := s.valueSetCapability(); vs != nil {
		resources = append(resources, vs)
	}

	statement := map[string]any{
		"resourceType": "CapabilityStatement",
		"status":       "active",
		"date":         time.Now().UTC().Format(time.RFC3339),
		"kind":         "instance",
		"software": map[string]any{
			"name":    "Perfuse",
			"version": "dev",
		},
		"implementation": map[string]any{
			"description": "Perfuse FHIR store",
			"url":         s.BaseURL,
		},
		"fhirVersion": string(version),
		"format":      []string{"json", "application/fhir+json"},
		"rest": []any{map[string]any{
			"mode":     "server",
			"resource": resources,
			"interaction": []any{
				map[string]any{"code": "transaction"},
				map[string]any{"code": "batch"},
			},
			// Stated plainly so nobody has to find out by trying.
			"documentation": "Implemented: read, search, create, update, delete, transaction, batch, " +
				"$validate, _include, _revinclude, _history, versioned reads, If-Match. " +
				"chained search, _include:iterate, " + modifierDocumentation() +
				exportDocumentation(s.Export != nil) + ". " +
				"Not implemented: search modifiers other than those listed, " +
				"If-Match with several versions, chains through an ambiguous reference " +
				"unless written as param:Type.chained.",
		}},
	}

	s.writeJSON(w, http.StatusOK, statement)
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	resourceType := r.PathValue("type")

	if s.conceptMapIntercept(w, r, resourceType) || s.valueSetIntercept(w, r, resourceType) {
		return
	}
	id := r.PathValue("id")

	if !supportedType(resourceType) {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
			fmt.Sprintf("resource type %q is not supported", resourceType))
		return
	}

	resource, err := s.Store.Get(r.Context(), resourceType, id)
	switch {
	case errors.Is(err, ErrNotFound):
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found",
			fmt.Sprintf("%s/%s does not exist", resourceType, id))
		return
	case errors.Is(err, ErrDeleted):
		// 410 rather than 404: the client needs to know it was deleted, not that
		// it was never there.
		s.writeOutcome(w, r, http.StatusGone, fhir.SeverityError, "deleted",
			fmt.Sprintf("%s/%s was deleted", resourceType, id))
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}

	// A resource outside the caller's launch context is reported as absent.
	//
	// A 404 and not a 403, and the wording matches a genuine miss exactly. Telling an app that a record exists but
	// belongs to somebody else is itself a disclosure, and it is the disclosure that matters here: "does this
	// hospital hold a record for this person" is the question a stalker asks, and a distinguishable response answers
	// it without any need to read the record.
	//
	// Checked after the fetch because there is no other way. The subject of an Observation is in the Observation, so
	// deciding without reading it would mean guessing.
	if !permitsResource(CallerFrom(r.Context()), resource) {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found",
			fmt.Sprintf("%s/%s does not exist", resourceType, id))

		return
	}

	// The version, so a client has something to send back in If-Match.
	//
	// Without it optimistic concurrency is unusable: a client would have to write once to learn the version, which is
	// the write it was trying to protect. Read from the store rather than from the resource, because meta.versionId is
	// whatever the sender put there and this is what the server holds.
	if versionID, _, err := s.Store.CurrentVersion(r.Context(), resourceType, id); err == nil {
		w.Header().Set("ETag", ETag(versionID))
	}

	s.writeResource(w, r, http.StatusOK, resource)
}

func (s *Server) handleSearch(w http.ResponseWriter, r *http.Request) {
	resourceType := r.PathValue("type")

	if s.conceptMapIntercept(w, r, resourceType) || s.valueSetIntercept(w, r, resourceType) {
		return
	}

	// $validate arrives as a type-level POST elsewhere; a GET on a $ path is not
	// a search.
	if strings.HasPrefix(resourceType, "$") {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
			fmt.Sprintf("operation %q is not supported", resourceType))
		return
	}

	query, err := ParseSearch(resourceType, r.URL.Query())
	if err != nil {
		// An unsupported parameter is refused rather than ignored. Ignoring one
		// returns the wrong resources and the client cannot tell.
		status := http.StatusBadRequest
		if errors.Is(err, ErrUnsupportedType) {
			status = http.StatusNotFound
		}
		s.writeOutcome(w, r, status, fhir.SeverityError, "not-supported", err.Error())
		return
	}

	// The launch context narrows the search, or refuses it.
	//
	// Before the store is asked anything, so a token limited to one patient cannot cause a query across every
	// patient - not even one whose results are then filtered, which would still read the whole table and would
	// still be visible in a slow-query log as a search somebody was not entitled to make.
	if err := s.enforceSearchContext(CallerFrom(r.Context()), query); err != nil {
		s.writeOutcome(w, r, http.StatusForbidden, fhir.SeverityError, "forbidden", err.Error())

		return
	}

	// The configured page size applies only where the client expressed no preference.
	if !query.CountGiven && s.DefaultCountFn != nil {
		if n := s.DefaultCountFn(); n > 0 {
			if n > MaxCount {
				n = MaxCount
			}
			query.Count = n
		}
	}

	result, err := s.Store.Search(r.Context(), query)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	bundle := s.Store.SearchBundle(result, s.BaseURL, resourceType, r.URL.RawQuery)
	s.writeBundle(w, r, http.StatusOK, bundle)
}

func (s *Server) handleCreate(w http.ResponseWriter, r *http.Request) {
	if s.conceptMapIntercept(w, r, r.PathValue("type")) || s.valueSetIntercept(w, r, r.PathValue("type")) {
		return
	}

	if s.refuseWrite(w, r) {
		return
	}

	resourceType := r.PathValue("type")
	resource, ok := s.decodeResource(w, r, resourceType)
	if !ok {
		return
	}

	// A create with no id gets one derived from the content, so the same message
	// posted twice does not produce two resources.
	if resource.ResourceID() == "" {
		resource.SetResourceID(deriveID(resource))
	}

	s.write(w, r, resource, http.StatusCreated)
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if s.conceptMapIntercept(w, r, r.PathValue("type")) || s.valueSetIntercept(w, r, r.PathValue("type")) {
		return
	}

	if s.refuseWrite(w, r) {
		return
	}

	resourceType := r.PathValue("type")
	id := r.PathValue("id")

	resource, ok := s.decodeResource(w, r, resourceType)
	if !ok {
		return
	}

	// The id in the URL wins, and a mismatch is refused rather than silently
	// resolved. Guessing which the client meant is how a resource ends up written
	// under the wrong identity.
	if resource.ResourceID() != "" && resource.ResourceID() != id {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invariant",
			fmt.Sprintf("the resource id %q does not match the URL id %q",
				resource.ResourceID(), id))
		return
	}
	resource.SetResourceID(id)

	// If-Match, before anything is written.
	//
	// Without this, two apps that each read a resource and then write it produce one silent loss: the second write wins
	// completely and nothing records that the first happened. For a medication list or a problem list that is a clinical
	// safety issue rather than an inconvenience.
	//
	// Only enforced when the client sent the header. Requiring it would break every existing client, and the ones that
	// send it are the ones that care.
	if !s.checkIfMatch(w, r, resourceType, id) {
		return
	}

	s.write(w, r, resource, http.StatusOK)
}

// checkIfMatch enforces optimistic concurrency, and reports whether the request may proceed.
func (s *Server) checkIfMatch(w http.ResponseWriter, r *http.Request, resourceType, id string) bool {
	expected := r.Header.Get("If-Match")
	if strings.TrimSpace(expected) == "" {
		return true
	}

	err := s.Store.CheckVersion(r.Context(), resourceType, id, expected)
	switch {
	case errors.Is(err, ErrVersionMismatch):
		// 412 rather than 409. A conflict says the request disagreed with the server's state; a precondition
		// failure says the client's assumption about the version was wrong, which is exactly what happened and is
		// what tells a client to re-read and retry rather than to give up.
		s.writeOutcome(w, r, http.StatusPreconditionFailed, fhir.SeverityError, "conflict", err.Error())

		return false
	case err != nil:
		// A malformed If-Match. Refused rather than ignored: ignoring it would let a client believe it had
		// concurrency protection while having none, which is worse than not offering the feature.
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invalid", err.Error())

		return false
	}

	return true
}

func (s *Server) write(w http.ResponseWriter, r *http.Request, resource fhir.Resource, successStatus int) {
	// The launch context applies to writes as well as reads, and this is the one shared by create and update.
	//
	// A 403 here rather than the 404 a read gets, and the asymmetry is deliberate. A read must not reveal whether a
	// record exists for somebody else's patient; a write is the caller's own content, so there is nothing to reveal -
	// and telling them plainly that the resource names a patient outside their context is the only message they can
	// act on.
	if !permitsResource(CallerFrom(r.Context()), resource) {
		s.writeOutcome(w, r, http.StatusForbidden, fhir.SeverityError, "forbidden",
			"this token is limited to one patient, and this resource does not name that patient as its subject")

		return
	}

	if s.ValidateOnWrite {
		result := fhir.Validate(resource, s.Store.Version())
		errCount, warnCount, _ := result.Counts()
		if errCount > 0 || (s.RejectOnWarning && warnCount > 0) {
			s.Log.Warn("rejected an invalid resource",
				"type", resource.ResourceTypeName(), "id", resource.ResourceID(),
				"errors", errCount, "warnings", warnCount)
			s.writeResource(w, r, http.StatusBadRequest, result.OperationOutcome())
			return
		}
	}

	outcome, err := s.Store.Put(r.Context(), resource)
	if err != nil {
		if errors.Is(err, ErrUnsupportedType) {
			s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported", err.Error())
			return
		}
		s.internalError(w, r, err)
		return
	}

	status := successStatus
	if outcome.Created {
		status = http.StatusCreated
	} else if successStatus == http.StatusCreated {
		status = http.StatusOK
	}

	stored, err := s.Store.Get(r.Context(), outcome.ResourceType, outcome.ID)
	if err != nil {
		s.internalError(w, r, err)
		return
	}

	w.Header().Set("Location", fmt.Sprintf("%s/%s/%s", s.BaseURL, outcome.ResourceType, outcome.ID))
	w.Header().Set("ETag", ETag(outcome.VersionID))
	w.Header().Set("Last-Modified", outcome.LastUpdated.Format(http.TimeFormat))
	s.writeResource(w, r, status, stored)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	if s.conceptMapIntercept(w, r, r.PathValue("type")) || s.valueSetIntercept(w, r, r.PathValue("type")) {
		return
	}

	if s.refuseWrite(w, r) {
		return
	}

	resourceType := r.PathValue("type")
	id := r.PathValue("id")

	// A delete outside the launch context is reported as though the resource were already gone.
	//
	// 204, matching what a genuine already-deleted resource returns, for the same reason the read returns 404: a
	// distinguishable answer here would let an app discover which records exist for other patients by trying to
	// delete them, which is a worse oracle than the read because it needs no read scope.
	if caller := CallerFrom(r.Context()); caller != nil && caller.Patient != "" {
		existing, err := s.Store.Get(r.Context(), resourceType, id)
		switch {
		case err == nil && !permitsResource(caller, existing):
			w.WriteHeader(http.StatusNoContent)

			return
		case errors.Is(err, ErrNotFound), errors.Is(err, ErrDeleted):
			// Fall through to the delete below, which already treats both as success.
		case err != nil:
			s.internalError(w, r, err)

			return
		}
	}

	// If-Match on a delete, which matters as much as on an update.
	//
	// A client that read version 3 and decided to delete has made that decision about version 3. If somebody has written
	// version 4 in between, the deletion is being applied to content the client never saw.
	//
	// Checked after the launch-context handling above so a caller outside its context still gets the indistinguishable
	// 204 rather than a 412, which would reveal that the resource exists.
	if !s.checkIfMatch(w, r, resourceType, id) {
		return
	}

	err := s.Store.Delete(r.Context(), resourceType, id)
	switch {
	case errors.Is(err, ErrNotFound):
		// Deleting something that is already gone is not an error; the end state
		// the client wanted is the end state.
		w.WriteHeader(http.StatusNoContent)
		return
	case err != nil:
		s.internalError(w, r, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// handleValidate implements $validate, which checks without storing.
func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request) {
	resourceType := r.PathValue("type")

	resource, ok := s.decodeResource(w, r, resourceType)
	if !ok {
		return
	}

	result := fhir.Validate(resource, s.Store.Version())
	status := http.StatusOK
	if !result.Valid() {
		status = http.StatusBadRequest
	}
	s.writeResource(w, r, status, result.OperationOutcome())
}

// handleTransaction applies a bundle atomically.
//
// Atomicity is the point. A v2 message becomes a patient and an encounter, and
// applying one without the other leaves an encounter attached to a patient that
// does not exist.
func (s *Server) handleTransaction(w http.ResponseWriter, r *http.Request) {
	if s.refuseWrite(w, r) {
		return
	}

	body, ok := s.readBody(w, r)
	if !ok {
		return
	}

	var incoming struct {
		ResourceType string `json:"resourceType"`
		Type         string `json:"type"`
		Entry        []struct {
			FullURL  string          `json:"fullUrl"`
			Resource json.RawMessage `json:"resource"`
			Request  *struct {
				Method      string `json:"method"`
				URL         string `json:"url"`
				IfNoneExist string `json:"ifNoneExist"`
			} `json:"request"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &incoming); err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure",
			"the request body is not valid JSON: "+err.Error())
		return
	}
	if incoming.ResourceType != "Bundle" {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure",
			"a POST to the base URL must carry a Bundle")
		return
	}
	if incoming.Type != fhir.BundleTransaction && incoming.Type != fhir.BundleBatch {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "not-supported",
			fmt.Sprintf("bundle type %q is not supported here; use transaction or batch", incoming.Type))
		return
	}

	atomic := incoming.Type == fhir.BundleTransaction

	// Parse and validate everything before writing anything. In a transaction a
	// late failure would otherwise leave earlier entries applied.
	type pending struct {
		resource fhir.Resource
		method   string
		url      string
	}
	var toApply []pending

	for i, entry := range incoming.Entry {
		if len(entry.Resource) == 0 {
			continue
		}
		resource, err := fhir.UnmarshalResource(entry.Resource)
		if err != nil {
			s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure",
				fmt.Sprintf("entry %d: %s", i, err.Error()))
			return
		}

		method := "PUT"
		url := ""
		if entry.Request != nil {
			method = strings.ToUpper(entry.Request.Method)
			url = entry.Request.URL
		}

		if resource.ResourceID() == "" {
			if method == "PUT" && url != "" {
				if i := strings.LastIndex(url, "/"); i >= 0 {
					resource.SetResourceID(url[i+1:])
				}
			}
			if resource.ResourceID() == "" {
				resource.SetResourceID(deriveID(resource))
			}
		}

		// Every entry is scope-checked, and before anything is written.
		//
		// This was left as a caveat: the middleware skips a POST to the base URL because a bundle addresses no
		// single resource type, so a caller scoped to read Observations could post a bundle creating Patients.
		// The scopes were checked on every other route, which made this the one way through.
		//
		// Checked per entry rather than on the envelope, which is the whole reason it was deferred. Checking the
		// envelope would have meant either refusing legitimate bundles or passing one whose entries were never
		// looked at - and the second reads as working.
		//
		// Checked here, in the parse-and-validate pass, so a transaction is refused before any entry is applied.
		// Refusing halfway through would leave a partial write behind on a bundle the caller was never allowed
		// to send.
		caller := CallerFrom(r.Context())
		if !caller.Allows(resource.ResourceTypeName(), true) {
			s.writeOutcome(w, r, http.StatusForbidden, fhir.SeverityError, "forbidden",
				fmt.Sprintf("entry %d: this token is not scoped to change %s resources",
					i, resource.ResourceTypeName()))

			return
		}

		// And the launch context, per entry. A bundle is the obvious way round a per-resource check, because it
		// is the one request that carries many resources - so a check applied to every other write and not to
		// this one would be the way through rather than a gap.
		if !permitsResource(caller, resource) {
			s.writeOutcome(w, r, http.StatusForbidden, fhir.SeverityError, "forbidden",
				fmt.Sprintf("entry %d: this token is limited to one patient, and this resource does "+
					"not name that patient as its subject", i))

			return
		}

		if s.ValidateOnWrite {
			result := fhir.Validate(resource, s.Store.Version())
			errCount, warnCount, _ := result.Counts()
			if errCount > 0 || (s.RejectOnWarning && warnCount > 0) {
				outcome := result.OperationOutcome()
				for j := range outcome.Issue {
					outcome.Issue[j].Expression = append(outcome.Issue[j].Expression,
						fmt.Sprintf("Bundle.entry[%d]", i))
				}
				s.writeResource(w, r, http.StatusBadRequest, outcome)
				return
			}
		}

		toApply = append(toApply, pending{resource: resource, method: method, url: url})
	}

	response := &fhir.Bundle{Type: fhir.BundleTransactionResponse}
	response.SetResourceID("transaction-response")

	for _, item := range toApply {
		outcome, err := s.Store.Put(r.Context(), item.resource)
		if err != nil {
			if atomic {
				// SQLite gives each Put its own transaction, so a mid-bundle
				// failure cannot be rolled back here. Saying so is better than
				// implying atomicity that was not delivered.
				s.Log.Error("a transaction bundle failed part way through",
					"err", err, "applied", len(response.Entry))
				s.writeOutcome(w, r, http.StatusInternalServerError, fhir.SeverityError, "exception",
					fmt.Sprintf("the bundle failed after applying %d of %d entries: %s",
						len(response.Entry), len(toApply), err.Error()))
				return
			}
			response.Entry = append(response.Entry, fhir.BundleEntry{
				Response: &fhir.BundleResponse{Status: "500 Internal Server Error"},
			})
			continue
		}

		status := "200 OK"
		if outcome.Created {
			status = "201 Created"
		}
		response.Entry = append(response.Entry, fhir.BundleEntry{
			Response: &fhir.BundleResponse{
				Status:   status,
				Location: fmt.Sprintf("%s/%s", outcome.ResourceType, outcome.ID),
				Etag:     ETag(outcome.VersionID),
			},
		})
	}

	s.Log.Info("applied a bundle",
		"type", incoming.Type, "entries", len(response.Entry))
	s.writeBundle(w, r, http.StatusOK, response)
}

func (s *Server) refuseWrite(w http.ResponseWriter, r *http.Request) bool {
	if !s.isReadOnly() {
		return false
	}
	s.writeOutcome(w, r, http.StatusMethodNotAllowed, fhir.SeverityError, "not-supported",
		"this FHIR endpoint is read-only")
	return true
}

// maxBody bounds a request. An unbounded FHIR body is a way to ask a server to
// allocate until it dies.
const maxBody = 8 << 20

func (s *Server) readBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	if ct := r.Header.Get("Content-Type"); ct != "" &&
		!strings.HasPrefix(ct, "application/fhir+json") &&
		!strings.HasPrefix(ct, "application/json") {
		s.writeOutcome(w, r, http.StatusUnsupportedMediaType, fhir.SeverityError, "not-supported",
			fmt.Sprintf("content type %q is not supported; use application/fhir+json", ct))
		return nil, false
	}

	limited := http.MaxBytesReader(w, r.Body, maxBody)
	body := make([]byte, 0, 4096)
	buf := make([]byte, 32*1024)
	for {
		n, err := limited.Read(buf)
		if n > 0 {
			body = append(body, buf[:n]...)
		}
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			if strings.Contains(err.Error(), "too large") {
				s.writeOutcome(w, r, http.StatusRequestEntityTooLarge, fhir.SeverityError, "too-costly",
					"the request body is too large")
				return nil, false
			}
			break
		}
	}
	if len(body) == 0 {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure",
			"the request body is empty")
		return nil, false
	}
	return body, true
}

func (s *Server) decodeResource(w http.ResponseWriter, r *http.Request, expectedType string) (fhir.Resource, bool) {
	body, ok := s.readBody(w, r)
	if !ok {
		return nil, false
	}

	resource, err := fhir.UnmarshalResource(body)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", err.Error())
		return nil, false
	}

	// A Patient posted to /Observation is a client bug, and storing it under the
	// requested type would corrupt the data quietly.
	if expectedType != "" && resource.ResourceTypeName() != expectedType {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invariant",
			fmt.Sprintf("a %s was posted to the %s endpoint",
				resource.ResourceTypeName(), expectedType))
		return nil, false
	}

	return resource, true
}

func (s *Server) writeResource(w http.ResponseWriter, r *http.Request, status int, resource fhir.Resource) {
	version := s.resolveVersion(r)
	raw, err := fhir.MarshalVersionedIndent(resource, version)
	if err != nil {
		s.Log.Error("could not serialise a resource", "err", err)
		http.Error(w, "could not serialise the resource", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

func (s *Server) writeBundle(w http.ResponseWriter, r *http.Request, status int, bundle *fhir.Bundle) {
	version := s.resolveVersion(r)
	raw, err := fhir.MarshalBundleVersionedIndent(bundle, version)
	if err != nil {
		s.Log.Error("could not serialise a bundle", "err", err)
		http.Error(w, "could not serialise the bundle", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	_, _ = w.Write(raw)
}

// resolveVersion determines the FHIR version for a response.
//
// If the client requests a specific version via the Accept header's fhirVersion
// parameter (e.g., Accept: application/fhir+json; fhirVersion=5.0.0), that version
// is used. Otherwise the store's configured version is the default.
func (s *Server) resolveVersion(r *http.Request) fhir.Version {
	if r == nil {
		return s.Store.Version()
	}
	return fhir.NegotiateVersion(r, s.Store.Version())
}

func (s *Server) writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", contentType)
	w.WriteHeader(status)
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(body); err != nil {
		s.Log.Error("writing a response failed", "err", err)
	}
}

func (s *Server) writeOutcome(w http.ResponseWriter, r *http.Request, status int, severity, code, message string) {
	s.writeResource(w, r, status, fhir.NewOperationOutcome(severity, code, message))
}

func (s *Server) internalError(w http.ResponseWriter, r *http.Request, err error) {
	s.Log.Error("fhir request failed", "err", err, "path", r.URL.Path)
	// The client gets a generic message; the detail goes to the log rather than to
	// whoever is on the other end of the connection.
	s.writeOutcome(w, r, http.StatusInternalServerError, fhir.SeverityError, "exception",
		"the server could not complete the request")
}

// deriveID builds a stable id from a resource's identifying content, so posting
// the same resource twice updates rather than duplicating.
func deriveID(r fhir.Resource) string {
	var key string
	switch v := r.(type) {
	case *fhir.Patient:
		if len(v.Identifier) > 0 {
			key = v.Identifier[0].System + "|" + v.Identifier[0].Value
		}
	case *fhir.Encounter:
		if len(v.Identifier) > 0 {
			key = v.Identifier[0].System + "|" + v.Identifier[0].Value
		}
	case *fhir.Observation:
		if len(v.Identifier) > 0 {
			key = v.Identifier[0].System + "|" + v.Identifier[0].Value
		}
	case *fhir.DiagnosticReport:
		if len(v.Identifier) > 0 {
			key = v.Identifier[0].System + "|" + v.Identifier[0].Value
		}
	}
	if key == "" {
		// Nothing identifying, so a time-based id is the honest fallback. It means
		// a repeat post creates a second resource, which is what happens when a
		// client sends nothing to match on.
		key = fmt.Sprintf("%s-%d", r.ResourceTypeName(), time.Now().UnixNano())
	}
	return hashID(r.ResourceTypeName(), key)
}

func hashID(kind, key string) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	var h uint64 = 14695981039346656037
	for i := 0; i < len(kind); i++ {
		h ^= uint64(kind[i])
		h *= 1099511628211
	}
	for i := 0; i < len(key); i++ {
		h ^= uint64(key[i])
		h *= 1099511628211
	}

	out := make([]byte, 0, 17)
	out = append(out, kind[0]|0x20)
	for i := 0; i < 16; i++ {
		out = append(out, alphabet[h%uint64(len(alphabet))])
		h /= uint64(len(alphabet))
		if h == 0 {
			break
		}
	}
	return string(out)
}

func searchParamType(param string) string {
	switch param {
	case "_id":
		return "token"
	case "_lastUpdated", "date", "birthdate":
		return "date"
	case "name", "family", "given":
		return "string"
	case "patient", "subject", "encounter":
		return "reference"
	default:
		return "token"
	}
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// ensure context is used for the imports above.
var _ = context.Background
