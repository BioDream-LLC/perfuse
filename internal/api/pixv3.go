package api

import (
	"bytes"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/pixv3"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/xds"
)

// PIX and PDQ in HL7 version 3 over SOAP, the older IHE face of the patient index beside PIXm and PDQm: /pixv3 is the
// Patient Identifier Cross-reference Manager (the identity feed ITI-44 and the query ITI-45), /pdqv3 the Patient
// Demographics Supplier (ITI-47). They answer from the same index as everything else, take an API token like the XDS
// endpoints (the viewer role to ask, the operator role to feed), and audit every transaction. A bare v3 message posted as
// application/xml is answered in kind, for testing by hand.

// maxV3 bounds one received message: a feed or a query is a few kilobytes.
const maxV3 = 4 << 20

func (s *Server) registerPIXV3(mux *recordingMux) {
	if s.PatientIndex == nil {
		return
	}
	svc := &pixv3.Service{Index: s.PatientIndex}
	mux.Handle("POST /pixv3", s.require(store.RoleViewer, s.v3Endpoint(svc, "PIX manager", map[string]bool{
		pixv3.FeedAdd: true, pixv3.FeedRevise: true, pixv3.FeedMerge: true, pixv3.PIXQuery: true})))
	mux.Handle("POST /pdqv3", s.require(store.RoleViewer, s.v3Endpoint(svc, "PDQ supplier", map[string]bool{
		pixv3.PDQQuery: true, pixv3.PDQContinue: true, pixv3.PDQCancel: true})))
}

func (s *Server) v3Endpoint(svc *pixv3.Service, actor string, accepts map[string]bool) func(http.ResponseWriter, *http.Request, *store.Session) {
	return func(w http.ResponseWriter, r *http.Request, sess *store.Session) {
		mt, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
		soap := mt == "application/soap+xml" || mt == "multipart/related"
		fail := func(status int, why string) {
			if soap {
				b, ct := xds.Fault("", "", "Sender", why)
				w.Header().Set("Content-Type", ct)
				w.WriteHeader(status)
				_, _ = w.Write(b)
				return
			}
			http.Error(w, why, status)
		}
		var body []byte
		var messageID string
		if soap {
			env, err := xds.ReadEnvelope(r.Header.Get("Content-Type"), http.MaxBytesReader(w, r.Body, maxV3))
			if err != nil {
				fail(http.StatusBadRequest, err.Error())
				return
			}
			body, messageID = env.Body, env.MessageID
		} else {
			b, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxV3))
			if err != nil {
				fail(http.StatusRequestEntityTooLarge, "the message is larger than 4 MB")
				return
			}
			body = b
		}
		interaction := rootName(body)
		if !accepts[interaction] {
			var names []string
			for n := range accepts {
				names = append(names, n)
			}
			fail(http.StatusBadRequest, fmt.Sprintf("this %s does not answer %q; it answers %s", actor, interaction, strings.Join(names, ", ")))
			return
		}
		feed := interaction == pixv3.FeedAdd || interaction == pixv3.FeedRevise || interaction == pixv3.FeedMerge
		if feed && !sess.Role.AtLeast(store.RoleOperator) {
			fail(http.StatusForbidden, "feeding the patient index needs the operator role")
			return
		}
		ans, err := svc.Handle(r.Context(), pixv3.Request{Tenant: mpiTenant(sess), Actor: sess.Username, Body: body})
		if err != nil {
			fail(http.StatusBadRequest, err.Error())
			return
		}
		action := map[string]string{pixv3.FeedAdd: "pixv3.feed", pixv3.FeedRevise: "pixv3.feed", pixv3.FeedMerge: "pixv3.merge",
			pixv3.PIXQuery: "pixv3.query", pixv3.PDQQuery: "pdqv3.query", pixv3.PDQContinue: "pdqv3.query", pixv3.PDQCancel: "pdqv3.cancel"}[interaction]
		_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: action, Target: ans.Target, Detail: ans.Outcome,
			IP: clientIP(r)})
		if !soap {
			w.Header().Set("Content-Type", "application/xml; charset=UTF-8")
			_, _ = w.Write(ans.Body)
			return
		}
		b, ct := xds.Outgoing{Action: ans.Action, RelatesTo: messageID, Body: stripDeclaration(ans.Body)}.Write()
		w.Header().Set("Content-Type", ct)
		_, _ = w.Write(b)
	}
}

// rootName is the local name of a document's first element: a v3 message names its interaction there.
func rootName(b []byte) string {
	for {
		i := bytes.IndexByte(b, '<')
		if i < 0 || i+1 >= len(b) {
			return ""
		}
		b = b[i+1:]
		if b[0] == '?' || b[0] == '!' {
			continue
		}
		end := bytes.IndexAny(b, " \t\r\n/>")
		if end < 0 {
			return ""
		}
		name := string(b[:end])
		if _, local, ok := strings.Cut(name, ":"); ok {
			name = local
		}
		return name
	}
}

// stripDeclaration removes an XML declaration, which may not appear inside a SOAP body.
func stripDeclaration(b []byte) []byte {
	b = bytes.TrimSpace(b)
	if bytes.HasPrefix(b, []byte("<?xml")) {
		if i := bytes.Index(b, []byte("?>")); i >= 0 {
			return bytes.TrimSpace(b[i+2:])
		}
	}
	return b
}
