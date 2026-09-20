package fhirserver

import (
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// The HTTP surface for bulk export.
//
// The asynchronous pattern the specification defines: the client asks, gets 202 and a URL to poll, polls until 200, then fetches the files
// named in the manifest.

// handleExport answers GET /$export and GET /Patient/$export.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	if s.Export == nil {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
			"bulk export is not enabled on this server")

		return
	}

	// The header the specification requires, checked rather than assumed.
	//
	// Prefer: respond-async is how a client says it understands it will get a 202 and must poll. A client that did not
	// send it is expecting a body, and handing it a 202 with an empty body looks like a broken endpoint. Saying what is
	// missing costs one line and saves an afternoon.
	if !strings.Contains(strings.ToLower(r.Header.Get("Prefer")), "respond-async") {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invalid",
			"bulk export is asynchronous, so this needs the header Prefer: respond-async; "+
				"the response will be a 202 with a URL to poll")

		return
	}

	req := ExportRequest{RequestURL: r.URL.String()}

	if raw := strings.TrimSpace(r.URL.Query().Get("_type")); raw != "" {
		for _, t := range strings.Split(raw, ",") {
			if t = strings.TrimSpace(t); t != "" {
				req.Types = append(req.Types, t)
			}
		}
	}

	if since := strings.TrimSpace(r.URL.Query().Get("_since")); since != "" {
		// Validated here rather than passed into a comparison. An unparseable _since compared as a string would
		// silently match everything or nothing, and the client would draw conclusions from the result.
		if _, err := time.Parse(time.RFC3339, since); err != nil {
			s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invalid",
				fmt.Sprintf("_since must be an instant such as 2026-01-01T00:00:00Z, and %q is not", since))

			return
		}
		req.Since = since
	}

	// A patient-context token exports that patient and nothing else.
	//
	// Narrowed rather than refused, because a single-patient export is a legitimate and common request - it is how an app
	// gives somebody their own record. Refusing would push the client into paging every resource type by hand, which
	// produces the same data over a great many more requests.
	if caller := CallerFrom(r.Context()); callerLimitedToOnePatient(caller) {
		req.PatientID = caller.Patient
	}

	job, err := s.Export.Start(r.Context(), req)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invalid", err.Error())

		return
	}

	status := fmt.Sprintf("%s/_export/%s/status", strings.TrimRight(s.BaseURL, "/"), job.ID)
	w.Header().Set("Content-Location", status)
	w.WriteHeader(http.StatusAccepted)
}

// handleExportStatus answers GET and DELETE on the poll URL.
func (s *Server) handleExportStatus(w http.ResponseWriter, r *http.Request) {
	if s.Export == nil {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
			"bulk export is not enabled on this server")

		return
	}

	id := r.PathValue("id")

	if r.Method == http.MethodDelete {
		err := s.Export.Delete(r.Context(), id)
		if errors.Is(err, ErrNotFound) {
			s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found",
				"no such export")

			return
		}
		if err != nil {
			s.internalError(w, r, err)

			return
		}

		w.WriteHeader(http.StatusNoContent)

		return
	}

	job, err := s.Export.Job(r.Context(), id)
	if errors.Is(err, ErrNotFound) {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found", "no such export")

		return
	}
	if err != nil {
		s.internalError(w, r, err)

		return
	}

	switch job.Status {
	case ExportRunning:
		// 202 with a Retry-After, so a client is not left choosing a poll interval. Without it clients settle on
		// either one second, which is a denial of service against a running export, or a minute, which makes a
		// two-second export take a minute.
		w.Header().Set("X-Progress", "running")
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(http.StatusAccepted)

		return

	case ExportFailed:
		// 500 with the reason, which is what the specification asks for. A failed export reported as a 200 with an
		// empty manifest is a client concluding the server holds no data.
		s.writeOutcome(w, r, http.StatusInternalServerError, fhir.SeverityError, "exception",
			"the export failed: "+job.Error)

		return
	}

	base := strings.TrimRight(s.BaseURL, "/")

	type outputFile struct {
		Type  string `json:"type"`
		URL   string `json:"url"`
		Count int    `json:"count"`
	}

	manifest := struct {
		TransactionTime     string       `json:"transactionTime"`
		Request             string       `json:"request"`
		RequiresAccessToken bool         `json:"requiresAccessToken"`
		Output              []outputFile `json:"output"`
		Deleted             []outputFile `json:"deleted,omitempty"`
		Error               []outputFile `json:"error"`
	}{
		// The time the export describes, not the time it finished. A consumer uses this as the _since of its next
		// export, so reporting the completion time would silently skip everything written while the export ran.
		TransactionTime: job.RequestedAt.UTC().Format(time.RFC3339),
		Request:         job.Request,
		// True, and it is true: the file endpoints go through the same authentication as everything else. Saying
		// false would tell a client the URLs are public, and it would then hand them to something that does not
		// authenticate.
		RequiresAccessToken: true,
		// Never nil, because the specification requires the array and a client written against it will fail on
		// null. An empty export is a legitimate answer and must still parse.
		Output: []outputFile{},
		Error:  []outputFile{},
	}

	for _, f := range job.Files {
		manifest.Output = append(manifest.Output, outputFile{
			Type:  f.ResourceType,
			URL:   fmt.Sprintf("%s/_export/%s/files/%s", base, job.ID, f.ResourceType),
			Count: f.Count,
		})
	}

	s.writeJSON(w, http.StatusOK, manifest)
}

// handleExportFile serves one NDJSON output.
func (s *Server) handleExportFile(w http.ResponseWriter, r *http.Request) {
	if s.Export == nil {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-supported",
			"bulk export is not enabled on this server")

		return
	}

	body, err := s.Export.File(r.Context(), r.PathValue("id"), r.PathValue("type"))
	if errors.Is(err, ErrNotFound) {
		s.writeOutcome(w, r, http.StatusNotFound, fhir.SeverityError, "not-found",
			"no such export file")

		return
	}
	if err != nil {
		s.internalError(w, r, err)

		return
	}

	// The NDJSON content type, which is what makes the file usable without guessing.
	w.Header().Set("Content-Type", "application/fhir+ndjson")
	// Named for the type, because a browser or a script otherwise saves every file under the same name and the second
	// overwrites the first.
	w.Header().Set("Content-Disposition",
		fmt.Sprintf("attachment; filename=%q", r.PathValue("type")+".ndjson"))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}

// exportDocumentation says whether bulk export is available on this server.
//
// Conditional rather than a fixed string, because the operation is off by default. A capability statement that lists $export on a server where
// it is disabled sends a client to an endpoint that refuses it, and the client has no way to tell that from a defect.
func exportDocumentation(enabled bool) string {
	if enabled {
		return ", $export (bulk, NDJSON)"
	}

	return ". $export is compiled in but not enabled on this server; start it with -fhir-bulk-export"
}
