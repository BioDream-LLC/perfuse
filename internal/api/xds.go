package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/cda"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/xds"
)

// IHE document sharing: an XDS.b Document Registry and Repository, and an XCA Responding Gateway over the same store, at
// /xds/repository, /xds/registry and /xca/gateway. They take an API token (most affinity domains add mutual TLS in front):
// querying and retrieving need the viewer role, submitting the operator role. Every transaction is audited.

func (s *Server) registerXDS(mux *recordingMux) {
	mux.Handle("GET /api/xds", s.require(store.RoleViewer, s.handleXDSStatus))
	if s.XDS == nil {
		return
	}
	for path, actor := range map[string]xds.Actor{"/xds/repository": xds.ActorRepository, "/xds/registry": xds.ActorRegistry,
		"/xca/gateway": xds.ActorRespondingGateway} {
		mux.Handle("POST "+path, s.require(store.RoleViewer, s.xdsEndpoint(actor)))
	}
	mux.Handle("POST /api/xds/query", s.require(store.RoleViewer, s.handleXDSQuery))
	mux.Handle("GET /api/xds/documents/{uid}", s.require(store.RoleViewer, s.handleXDSDocument))
	mux.Handle("POST /api/xds/publish", s.require(store.RoleEditor, s.handleXDSPublish))
}

func (s *Server) xdsEndpoint(actor xds.Actor) func(http.ResponseWriter, *http.Request, *store.Session) {
	return func(w http.ResponseWriter, r *http.Request, sess *store.Session) {
		if sess.TenantID != store.DefaultTenant || len(sess.ChannelGroups) > 0 {
			http.Error(w, "the document registry belongs to the whole server; an account limited to a tenant or to channel groups "+
				"cannot use it", http.StatusForbidden)
			return
		}
		h := &xds.Handler{Registry: s.XDS, Actor: actor, Log: s.log(),
			Allow: func(_ *http.Request, action string) error {
				if (action == xds.ActionProvideAndRegister || action == xds.ActionRegister) && !sess.Role.AtLeast(store.RoleOperator) {
					return errors.New("submitting documents needs the operator role")
				}
				return nil
			},
			Audit: func(r *http.Request, action, target string, ok bool) {
				name := map[string]string{xds.ActionProvideAndRegister: "xds.submit", xds.ActionRegister: "xds.register",
					xds.ActionStoredQuery: "xds.query", xds.ActionRetrieve: "xds.retrieve", xds.ActionCrossGatewayQuery: "xca.query",
					xds.ActionCrossGatewayRetr: "xca.retrieve"}[action]
				detail := "ok"
				if !ok {
					detail = "refused"
				}
				_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: name, Target: target, Detail: detail, IP: clientIP(r)})
			}}
		h.ServeHTTP(w, r)
	}
}

// xdsDoc is a DocumentEntry as the screen shows it.
type xdsDoc struct {
	EntryUUID       string   `json:"entryUUID"`
	UniqueID        string   `json:"uniqueId"`
	PatientID       string   `json:"patientId"`
	Title           string   `json:"title"`
	Status          string   `json:"status"`
	Class           xds.Code `json:"class"`
	Type            xds.Code `json:"type"`
	Format          xds.Code `json:"format"`
	Confidentiality string   `json:"confidentiality"`
	CreationTime    string   `json:"creationTime"`
	MimeType        string   `json:"mimeType"`
	Size            string   `json:"size"`
	Author          string   `json:"author,omitempty"`
	Repository      string   `json:"repositoryUniqueId"`
	Home            string   `json:"homeCommunityId,omitempty"`
}

func first(c []xds.Code) xds.Code {
	if len(c) > 0 {
		return c[0]
	}
	return xds.Code{}
}

func summarize(o xds.Object) xdsDoc {
	d := xdsDoc{EntryUUID: o.ID, UniqueID: o.ExternalID(xds.IDDocUniqueID), PatientID: o.ExternalID(xds.IDDocPatientID),
		Title: o.Name.Text(), Status: o.Status[strings.LastIndex(o.Status, ":")+1:], Class: first(o.Codes(xds.SchemeClassCode)),
		Type: first(o.Codes(xds.SchemeTypeCode)), Format: first(o.Codes(xds.SchemeFormatCode)),
		Confidentiality: first(o.Codes(xds.SchemeConfidentiality)).Code, CreationTime: o.Slot1("creationTime"), MimeType: o.MimeType,
		Size: o.Slot1("size"), Repository: o.Slot1("repositoryUniqueId"), Home: o.Home}
	for _, c := range o.Classifications {
		if c.Scheme == xds.SchemeDocAuthor {
			for _, sl := range c.Slots {
				if sl.Name == "authorPerson" && len(sl.Values) > 0 {
					d.Author = xcnName(sl.Values[0])
				}
			}
		}
	}
	return d
}

// xcnName reads ^Family^Given^^^ as "Given Family".
func xcnName(xcn string) string {
	p := strings.Split(xcn, "^")
	if len(p) >= 3 {
		return strings.TrimSpace(p[2] + " " + p[1])
	}
	return xcn
}

func (s *Server) handleXDSStatus(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.XDS == nil {
		s.ok(w, map[string]any{"enabled": false, "hint": "Start the server with -xds REPOSITORY-OID (Settings › Data › Document " +
			"sharing) to keep documents here and answer IHE XDS.b and XCA: ITI-41, ITI-18, ITI-43, ITI-38 and ITI-39."})
		return
	}
	counts, err := s.XDS.Count(r.Context())
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	patient := strings.TrimSpace(r.URL.Query().Get("patient"))
	docs, err := s.XDS.Documents(r.Context(), patient, 100)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	out := make([]xdsDoc, 0, len(docs))
	for _, d := range docs {
		out = append(out, summarize(d))
	}
	base := s.selfURL()
	s.ok(w, map[string]any{"enabled": true, "counts": counts, "documents": out, "repositoryUniqueId": s.XDS.RepositoryID,
		"homeCommunityId": s.XDS.HomeCommunityID, "endpoints": map[string]string{
			"repository": base + "/xds/repository", "registry": base + "/xds/registry", "gateway": base + "/xca/gateway"}})
}

type xdsQueryRequest struct {
	// URL is a remote registry (ITI-18) or responding gateway (ITI-38); empty asks this server's own registry.
	URL     string `json:"url"`
	Gateway bool   `json:"gateway"`
	Patient string `json:"patient"`
	Token   string `json:"token,omitempty"`
}

func (s *Server) handleXDSQuery(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var in xdsQueryRequest
	if !s.decode(w, r, &in) {
		return
	}
	in.Patient = strings.TrimSpace(in.Patient)
	if in.Patient == "" {
		s.fail(w, r, http.StatusBadRequest, "a patient ID (id^^^&OID&ISO) is required")
		return
	}
	q := xds.FindDocuments(in.Patient, "LeafClass")
	var resp *xds.AdhocQueryResponse
	where := "this server"
	if strings.TrimSpace(in.URL) == "" {
		res, ps := s.XDS.Query(r.Context(), q)
		qr := xds.QueryResponse(res, ps, "LeafClass", "")
		resp = &qr
	} else {
		u, err := url.Parse(strings.TrimSpace(in.URL))
		if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			s.fail(w, r, http.StatusBadRequest, "the registry or gateway address must be an http or https URL, such as "+
				"https://hie.example/xds/registry")
			return
		}
		// The outbound client holds the dial to the egress policy, after DNS, as for every address a user types.
		c := &xds.Client{HTTP: s.OutboundClient(), Headers: map[string]string{}}
		if in.Token != "" {
			c.Headers["Authorization"] = "Bearer " + in.Token
		}
		action := xds.ActionStoredQuery
		if in.Gateway {
			action = xds.ActionCrossGatewayQuery
		}
		ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
		defer cancel()
		if resp, err = c.Query(ctx, in.URL, action, q); err != nil {
			// The address the person typed did not answer as a registry. A 422 about the address, rather than a 502: nothing on
			// this server failed, and the fix is in what was typed (or at the other end).
			s.fail(w, r, http.StatusUnprocessableEntity, "the registry at "+u.Host+" did not answer the query: "+err.Error())
			return
		}
		where = in.URL
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "xds.query", Target: in.Patient + " at " + where, IP: clientIP(r)})
	docs := []xdsDoc{}
	for _, o := range resp.Objects.Extrinsic {
		docs = append(docs, summarize(o))
	}
	errs := []string{}
	for _, e := range resp.Errors {
		errs = append(errs, e.Code+": "+e.Context)
	}
	s.ok(w, map[string]any{"status": resp.Status[strings.LastIndex(resp.Status, ":")+1:], "errors": errs, "documents": docs})
}

func (s *Server) handleXDSDocument(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	uid := r.PathValue("uid")
	b, mt, err := s.XDS.Content(r.Context(), uid)
	if errors.Is(err, xds.ErrNoDocument) {
		s.fail(w, r, http.StatusNotFound, "no document "+uid+" is in this repository")
		return
	}
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "xds.read", Target: uid, IP: clientIP(r)})
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.%s"`, strings.NewReplacer("^", "_", "/", "_").Replace(uid),
		map[bool]string{true: "xml", false: "bin"}[strings.Contains(mt, "xml")]))
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(b)
}

type xdsPublishRequest struct {
	Document        string    `json:"document"`
	PatientDomain   string    `json:"patientDomain"`
	SourceID        string    `json:"sourceId"`
	FacilityType    xds.Code  `json:"facilityType"`
	PracticeSetting xds.Code  `json:"practiceSetting"`
	Class           *xds.Code `json:"class,omitempty"`
}

// handleXDSPublish registers a CDA document in this server's repository, building its metadata as a channel's xds
// destination would: for trying the metadata a document gets, and for loading a document by hand.
func (s *Server) handleXDSPublish(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var in xdsPublishRequest
	if !s.decode(w, r, &in) {
		return
	}
	d, err := cda.Parse([]byte(strings.TrimSpace(in.Document)))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, "the document is not CDA: "+err.Error())
		return
	}
	if in.SourceID == "" {
		in.SourceID = s.XDS.RepositoryID
	}
	meta, err := xds.MetaFromCDA(d, []byte(in.Document), xds.CDAOptions{SourceID: in.SourceID, PatientDomain: strings.TrimSpace(in.PatientDomain),
		FacilityType: in.FacilityType, PracticeSetting: in.PracticeSetting, Class: in.Class})
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	req, err := xds.Build(meta, time.Now())
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	sub, ps := s.XDS.Register(r.Context(), req, map[string][]byte{"Document01": []byte(in.Document)})
	if sub == nil {
		var problems []string
		for _, p := range ps {
			problems = append(problems, p.Code+": "+p.Context)
		}
		s.fail(w, r, http.StatusUnprocessableEntity, "the registry refused the document", problems...)
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "xds.submit", Target: meta.PatientID + " " + meta.UniqueID, IP: clientIP(r)})
	s.ok(w, map[string]any{"document": summarize(sub.Documents[0])})
}
