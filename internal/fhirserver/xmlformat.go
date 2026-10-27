package fhirserver

import (
	"bytes"
	"net/http"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/fhir"
)

// Answers in FHIR XML, for clients that ask for it.
//
// FHIR servers are expected to offer XML as well as JSON, and German hospital systems (ISiK's test suite reads most
// of its answers as XML) ask for it. Every handler writes JSON; this wrapper rewrites a JSON answer as XML when the
// request asked for XML, so no handler can forget to. Bodies sent in XML are not read: they answer 415.

func wantsXML(r *http.Request) bool {
	if f := strings.ToLower(r.URL.Query().Get("_format")); f != "" {
		return f == "xml" || strings.HasSuffix(f, "/xml") || strings.HasSuffix(f, "+xml")
	}
	for _, part := range strings.Split(r.Header.Get("Accept"), ",") {
		mt := strings.ToLower(strings.TrimSpace(strings.SplitN(part, ";", 2)[0]))
		switch mt {
		case "application/fhir+json", "application/json", "json":
			return false // the first a client names is the one it prefers
		case "application/fhir+xml", "application/xml", "text/xml", "xml":
			return true
		}
	}
	return false
}

func sentXML(r *http.Request) bool {
	mt := strings.ToLower(strings.TrimSpace(strings.SplitN(r.Header.Get("Content-Type"), ";", 2)[0]))
	return mt == "application/fhir+xml" || mt == "application/xml" || mt == "text/xml"
}

func (s *Server) xmlAnswers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if sentXML(r) && r.ContentLength != 0 && r.Method != http.MethodGet {
			out, xw := w, (*xmlResponse)(nil)
			if wantsXML(r) {
				xw = &xmlResponse{ResponseWriter: w, version: s.resolveVersion(r)}
				out = xw
			}
			s.writeOutcome(out, r, http.StatusUnsupportedMediaType, fhir.SeverityError, "not-supported",
				"this server reads FHIR JSON only; it answers in XML when asked (Accept: application/fhir+xml)")
			if xw != nil {
				xw.finish()
			}
			return
		}
		if !wantsXML(r) {
			next.ServeHTTP(w, r)
			return
		}
		xw := &xmlResponse{ResponseWriter: w, version: s.resolveVersion(r)}
		next.ServeHTTP(xw, r)
		xw.finish()
	})
}

// xmlResponse holds a JSON answer back and writes it as XML; anything else (NDJSON, files, an event stream) passes
// through as it was written.
type xmlResponse struct {
	http.ResponseWriter
	version  fhir.Version
	status   int
	decided  bool
	convert  bool
	body     bytes.Buffer
	finished bool
}

func (x *xmlResponse) WriteHeader(status int) {
	if x.decided {
		return
	}
	x.decided, x.status = true, status
	ct := strings.ToLower(x.Header().Get("Content-Type"))
	x.convert = strings.HasPrefix(ct, "application/fhir+json") || strings.HasPrefix(ct, "application/json")
	if !x.convert {
		x.ResponseWriter.WriteHeader(status)
	}
}

func (x *xmlResponse) Write(p []byte) (int, error) {
	if !x.decided {
		x.WriteHeader(http.StatusOK)
	}
	if !x.convert {
		return x.ResponseWriter.Write(p)
	}
	return x.body.Write(p)
}

func (x *xmlResponse) Flush() {
	if !x.convert {
		if f, ok := x.ResponseWriter.(http.Flusher); ok {
			f.Flush()
		}
	}
}

func (x *xmlResponse) finish() {
	if x.finished || !x.decided || !x.convert {
		return
	}
	x.finished = true
	h := x.Header()
	out, err := fhir.JSONToXML(x.body.Bytes(), x.version)
	if err != nil || x.body.Len() == 0 {
		// Not a resource (an empty 204, a plain JSON answer such as a bulk status): send it as it was.
		x.ResponseWriter.WriteHeader(x.status)
		_, _ = x.ResponseWriter.Write(x.body.Bytes())
		return
	}
	h.Set("Content-Type", "application/fhir+xml; charset=utf-8")
	h.Set("Content-Length", strconv.Itoa(len(out)))
	x.ResponseWriter.WriteHeader(x.status)
	_, _ = x.ResponseWriter.Write(out)
}
