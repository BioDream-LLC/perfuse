package fhirserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/fhir"
)

// A Binary read in its own format, and a document posted to the base URL.

// binaryNative writes a Binary as its own content when the client asks for that content type rather than FHIR: FHIR
// says a Binary read with an Accept that is not a FHIR format answers with the content itself. It reports whether it did.
func (s *Server) binaryNative(w http.ResponseWriter, r *http.Request, resource fhir.Resource) bool {
	b, ok := resource.(*fhir.Binary)
	if !ok || b.ContentType == "" {
		return false
	}
	accept := strings.ToLower(r.Header.Get("Accept"))
	if accept == "" || r.URL.Query().Get("_format") != "" || strings.Contains(accept, "fhir") ||
		strings.Contains(accept, "json") || strings.Contains(accept, "xml") || strings.TrimSpace(accept) == "*/*" {
		return false
	}
	ct := strings.ToLower(strings.TrimSpace(strings.SplitN(b.ContentType, ";", 2)[0]))
	matched := false
	for _, part := range strings.Split(accept, ",") {
		mt := strings.TrimSpace(strings.SplitN(part, ";", 2)[0])
		if mt == ct || mt == "*/*" || (strings.HasSuffix(mt, "/*") && strings.HasPrefix(ct, strings.TrimSuffix(mt, "*"))) {
			matched = true
		}
	}
	if !matched {
		return false
	}
	data, err := base64.StdEncoding.DecodeString(b.Data)
	if err != nil {
		return false
	}
	w.Header().Set("Content-Type", b.ContentType)
	// Served as a download, never rendered: the content is whatever the sender stored.
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Security-Policy", "sandbox")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return true
}

// postDocument keeps a document Bundle posted to the base URL, as ISiK's document consumers must: its Composition must
// carry a narrative, and the patient and encounter it names must be ones this server already holds, found by their
// identifiers. A document about someone the server does not know is refused (422), not filed under a stranger.
func (s *Server) postDocument(w http.ResponseWriter, r *http.Request, body []byte) {
	var doc struct {
		ID    string `json:"id"`
		Entry []struct {
			FullURL  string         `json:"fullUrl"`
			Resource map[string]any `json:"resource"`
		} `json:"entry"`
	}
	if err := json.Unmarshal(body, &doc); err != nil || len(doc.Entry) == 0 {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "a document Bundle needs entries, the first a Composition")
		return
	}
	comp := doc.Entry[0].Resource
	if comp["resourceType"] != "Composition" {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "the first entry of a document must be its Composition")
		return
	}
	if text, _ := comp["text"].(map[string]any); text == nil || text["div"] == nil {
		s.writeOutcome(w, r, http.StatusUnprocessableEntity, fhir.SeverityError, "required",
			"the Composition has no narrative (text): a document must be readable as it was written")
		return
	}
	byURL := map[string]map[string]any{}
	for _, e := range doc.Entry {
		if e.FullURL != "" {
			byURL[e.FullURL] = e.Resource
		}
	}
	for _, ref := range []struct{ field, want string }{{"subject", "Patient"}, {"encounter", "Encounter"}} {
		target, _ := comp[ref.field].(map[string]any)
		if target == nil {
			continue
		}
		refStr, _ := target["reference"].(string)
		res := byURL[refStr]
		if res == nil {
			continue // a reference to a resource on this server is checked on store
		}
		if err := s.knownByIdentifier(r, ref.want, res); err != nil {
			s.writeOutcome(w, r, http.StatusUnprocessableEntity, fhir.SeverityError, "not-found", err.Error())
			return
		}
	}

	bundle, err := fhir.UnmarshalBundle(body)
	if err != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", err.Error())
		return
	}
	if bundle.ResourceID() == "" {
		bundle.SetResourceID(deriveID(bundle))
	}
	w.Header().Set("Location", s.BaseURL+"/Bundle/"+bundle.ResourceID())
	s.write(w, r, bundle, http.StatusCreated)
}

// knownByIdentifier is nil when this server holds a resource of type t with one of res's identifiers.
func (s *Server) knownByIdentifier(r *http.Request, t string, res map[string]any) error {
	ids, _ := res["identifier"].([]any)
	for _, i := range ids {
		im, _ := i.(map[string]any)
		system, _ := im["system"].(string)
		value, _ := im["value"].(string)
		if value == "" {
			continue
		}
		found, err := s.Store.FindByIdentifier(r.Context(), t, system, value)
		if err == nil && found != nil || err == ErrConflict {
			return nil
		}
	}
	return fmt.Errorf("the document's %s is not known to this server by any of its identifiers", t)
}
