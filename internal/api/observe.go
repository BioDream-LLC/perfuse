package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/cda"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Endpoints for metrics and clinical documents.
//
// The metrics endpoint has an authenticated JSON form for the dashboard and an
// unauthenticated Prometheus form for a scraper. Splitting them is deliberate: a
// scraper cannot hold a session cookie, and the alternative - putting the whole
// dashboard behind a token - would mean nobody could use a browser. What the
// Prometheus endpoint exposes is counts and timings by channel and destination,
// never message content, so it carries no patient data. That property is the
// reason it can be left open at all, and it is why label values here come only
// from configuration.

// handleMetrics returns the series the dashboard draws.
func (s *Server) handleMetrics(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.Runtime == nil || s.Runtime.Metrics == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "metrics are not being collected; this server is running without an engine")
		return
	}
	collector := s.Runtime.Metrics

	since := time.Time{}
	if window := r.URL.Query().Get("window"); window != "" {
		d, err := time.ParseDuration(window)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("window %q is not a duration; use something like 15m, 1h or 6h", window))
			return
		}
		if d > collector.Window() {
			// Asking for more history than is kept is answered with what there is,
			// plus a statement of the limit, rather than an error. A dashboard
			// defaulting to 24h against a 6h collector should still draw.
			d = collector.Window()
		}
		since = time.Now().Add(-d)
	}

	if name := r.URL.Query().Get("metric"); name != "" {
		s.ok(w, map[string]any{
			"metric": name,
			"series": collector.Query(name, since),
		})
		return
	}

	s.ok(w, collector.SnapshotSince(since))
}

// handleMetricNames lists what is available, so the dashboard does not hard-code
// metric names and silently show nothing when one is renamed.
func (s *Server) handleMetricNames(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.Runtime == nil || s.Runtime.Metrics == nil {
		s.fail(w, r, http.StatusServiceUnavailable, "metrics are not being collected")
		return
	}
	s.ok(w, map[string]any{
		"metrics":           s.Runtime.Metrics.Definitions(),
		"resolutionSeconds": s.Runtime.Metrics.Resolution().Seconds(),
		"windowSeconds":     s.Runtime.Metrics.Window().Seconds(),
		"uptimeSeconds":     s.Runtime.Metrics.Uptime().Seconds(),
	})
}

// handlePrometheus serves the exposition format.
//
// It is left unauthenticated because a scraper cannot log in, and because what it
// exposes is counts and timings labelled by channel and destination names taken
// from configuration - no message content and no patient data. Anyone deploying
// somewhere this still matters can put it behind a proxy.
func (s *Server) handlePrometheus(w http.ResponseWriter, r *http.Request) {
	// Authenticated as of the security audit. A metric label names a channel, and a channel name usually names the
	// system at the other end, so an open scrape endpoint on a shared server discloses one customer's interfaces to
	// anybody who can reach the port.
	sess, allowed := s.metricsCaller(r)
	if !allowed {
		// A scraper reads the status code, not the body, so the explanation is for the person reading a log after
		// wondering why their dashboard went empty. Comment-prefixed because this is a Prometheus text endpoint.
		w.Header().Set("WWW-Authenticate", `Bearer realm="perfuse metrics"`)
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte("# this endpoint needs a credential: set -metrics-token and give the scraper the " +
			"same value in bearer_token_file, or pass -metrics-open if the port is genuinely private\n"))
		return
	}

	if s.Runtime == nil || s.Runtime.Metrics == nil {
		http.Error(w, "# metrics are not being collected\n", http.StatusServiceUnavailable)
		return
	}

	// A tenant's own operator sees their own numbers. The scrape token and -metrics-open both yield a nil session and
	// see the whole installation, which is what an operator monitoring their own server wants and is the reason the
	// scrape token should not be handed to a customer.
	// s.Repos being non-nil is how the rest of the server recognises a multi-tenant installation, matching
	// requirePlatform rather than inventing a second way to ask the same question.
	if sess != nil && s.Repos != nil && sess.Role != store.RolePlatform {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := s.Runtime.Metrics.WritePrometheusForTenant(w, string(sess.TenantID)); err != nil && s.Log != nil {
			s.Log.Warn("could not write metrics", "err", err)
		}
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	if err := s.Runtime.Metrics.WritePrometheus(w); err != nil && s.Log != nil {
		s.Log.Warn("could not write metrics", "err", err)
	}
}

// documentRequest is a clinical document to inspect.
type documentRequest struct {
	// Document is the CDA XML.
	Document string `json:"document"`

	// Message is an HL7 v2 message that may carry a document. Supplying this
	// instead of Document exercises the extraction path, which is how documents
	// actually arrive.
	Message string `json:"message"`

	// Version is the FHIR release to convert to.
	Version string `json:"version"`

	// IdentifierSystems maps an OID root to a URI.
	IdentifierSystems map[string]string `json:"identifierSystems"`

	// Convert asks for the FHIR conversion as well as the reading. It is separate
	// because converting is the slower half and a viewer often does not need it.
	Convert bool `json:"convert"`
}

// handleInspectDocument reads a clinical document and reports what it contains.
func (s *Server) handleInspectDocument(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var req documentRequest
	if !s.decode(w, r, &req) {
		return
	}

	var (
		doc       *cda.Document
		original  []byte
		attached  []cda.Embedded
		transport *cda.DocumentInfo
	)

	switch {
	case strings.TrimSpace(req.Message) != "":
		raw := normaliseMessage(req.Message)
		m, err := hl7.Parse([]byte(raw))
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "that is not a parseable HL7 message: "+err.Error())
			return
		}

		parsed, found, err := cda.ParseEmbedded(m)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		attached = found

		if parsed == nil {
			// Not an error. A message with no document, or with an attachment that
			// is a PDF, is a normal thing to paste in while working out what a feed
			// contains, and the notes say what was found.
			s.ok(w, map[string]any{
				"found":             false,
				"attachments":       describeAttachments(found),
				"isDocumentMessage": cda.IsDocumentMessage(m),
				"message":           "no clinical document was found in this message",
			})
			return
		}
		doc = parsed
		transport = parsed.Transport
		for _, a := range found {
			if len(a.Data) > 0 {
				original = a.Data
				break
			}
		}

	case strings.TrimSpace(req.Document) != "":
		original = []byte(req.Document)
		parsed, err := cda.Parse(original)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, err.Error())
			return
		}
		doc = parsed

	default:
		s.fail(w, r, http.StatusBadRequest, "supply either a clinical document or an HL7 message carrying one")
		return
	}

	response := map[string]any{
		"found":     true,
		"document":  doc,
		"summary":   doc.Summarise(),
		"notes":     doc.SortedNotes(),
		"agreement": cda.CheckAgreement(doc),

		// Conformance against the implementation guide, which is a different question from
		// whether the narrative agrees with the entries.
		//
		// This has been able to run since it was written and nothing called it. Agreement
		// asks "does the prose match the data"; validation asks "would a receiver reject
		// this". A document can pass either and fail the other, and the failure people
		// actually meet is the second: the exchange partner refuses it and says only that
		// it is non-conformant.
		"validation":  cda.Validate(doc),
		"attachments": describeAttachments(attached),
	}
	if transport != nil {
		response["transport"] = map[string]any{
			"info":          transport,
			"statusMeaning": transport.StatusMeaning(),
			"replaces":      transport.Replaces(),
		}
	}

	if req.Convert {
		version := req.Version
		if version == "" {
			version = "R5"
		}
		result, err := doc.ToFHIR(cda.FHIROptions{
			Version:           version,
			IdentifierSystems: req.IdentifierSystems,
			Original:          original,
		})
		if err != nil {
			// A refused version is the caller's mistake and worth saying plainly,
			// but the reading above is still valid and is returned with it.
			response["conversionError"] = err.Error()
		} else {
			response["fhir"] = result
		}
	}

	s.ok(w, response)
}

// describeAttachments summarises what was found in a message without returning the
// bytes, which for a scanned document could be megabytes.
func describeAttachments(found []cda.Embedded) []map[string]any {
	out := make([]map[string]any, 0, len(found))
	for _, a := range found {
		entry := map[string]any{
			"segment":  a.Segment,
			"encoding": a.Encoding,
			"mimeType": a.MimeType,
			"bytes":    len(a.Data),
			"notes":    a.Notes,
			"isXML":    len(a.Data) > 0 && strings.HasPrefix(strings.TrimSpace(string(a.Data)), "<"),
		}
		// A short preview, so somebody can tell what it is without downloading it.
		if n := len(a.Data); n > 0 {
			limit := 200
			if n < limit {
				limit = n
			}
			preview := a.Data[:limit]
			if entry["isXML"] == true {
				entry["preview"] = string(preview)
			} else {
				entry["preview"] = base64.StdEncoding.EncodeToString(preview)
				entry["previewIsBase64"] = true
			}
		}
		out = append(out, entry)
	}
	return out
}

// handleDocumentTypes lists what the reader recognises, so the interface can say
// what it supports rather than leaving somebody to find out by trial.
func (s *Server) handleDocumentTypes(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	s.ok(w, map[string]any{
		"documents": cda.RecognisedDocuments(),
		"sections":  cda.RecognisedSections(),
		"note": "Reading, checking and converting are supported. Generating a conformant C-CDA is not: " +
			"it means satisfying several hundred template rules, and a document that is almost right " +
			"fails certification in ways that are miserable to debug.",
	})
}

// normaliseMessage accepts a message whose segments are separated by line feeds,
// which is how one looks after a trip through a text editor.
func normaliseMessage(s string) string {
	s = strings.TrimSpace(s)
	if strings.Contains(s, "\r") {
		return s
	}
	s = strings.ReplaceAll(s, "\n", "\r")
	if !strings.HasSuffix(s, "\r") {
		s += "\r"
	}
	return s
}
