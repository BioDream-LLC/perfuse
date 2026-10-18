package xds

import (
	"bytes"
	"context"
	"encoding/xml"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

const patient = "DWMRN1^^^&1.2.826.0.1.3680043.10.1337.99&ISO"

func rig(t *testing.T) (*Registry, *httptest.Server) {
	t.Helper()
	db, err := sqlitedb.OpenSQL(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	reg := &Registry{DB: db, RepositoryID: "1.2.826.0.1.3680043.10.1337.99.1", HomeCommunityID: "urn:oid:1.2.826.0.1.3680043.10.1337.99.2"}
	mux := http.NewServeMux()
	mux.Handle("/repository", &Handler{Registry: reg, Actor: ActorRepository})
	mux.Handle("/registry", &Handler{Registry: reg, Actor: ActorRegistry})
	mux.Handle("/xca", &Handler{Registry: reg, Actor: ActorRespondingGateway})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return reg, srv
}

func meta() DocumentMeta {
	return DocumentMeta{PatientID: patient, SourceID: "1.2.826.0.1.3680043.10.1337.99.3", Title: "Discharge summary", MimeType: "text/xml",
		Class: Code{"11490-0", "2.16.840.1.113883.6.1", "Discharge summary"}, Type: Code{"18842-5", "2.16.840.1.113883.6.1", "Discharge summary"},
		Format:          Code{"urn:hl7-org:sdwg:ccda-structuredBody:2.1", "1.3.6.1.4.1.19376.1.2.3", "C-CDA"},
		Confidentiality: Code{"N", "2.16.840.1.113883.5.25", "Normal"}, FacilityType: Code{"225732001", "2.16.840.1.113883.6.96", "Community hospital"},
		PracticeSetting: Code{"394802001", "2.16.840.1.113883.6.96", "General medicine"}, ContentType: Code{"18842-5", "2.16.840.1.113883.6.1", "Discharge summary"},
		AuthorPerson: "^SMITH^ALEX^^^", CreationTime: time.Date(2026, 10, 1, 9, 30, 0, 0, time.UTC)}
}

func submit(t *testing.T, srv *httptest.Server, m DocumentMeta, content string) *RegistryResponse {
	t.Helper()
	req, err := Build(m, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	rr, err := (&Client{}).ProvideAndRegister(context.Background(), srv.URL+"/repository", req, map[string][]byte{"Document01": []byte(content)})
	if err != nil {
		t.Fatal(err)
	}
	return rr
}

func TestAProvidedDocumentIsFoundAndRetrieved(t *testing.T) {
	reg, srv := rig(t)
	m := meta()
	if rr := submit(t, srv, m, "<ClinicalDocument>one</ClinicalDocument>"); rr.Failed() {
		t.Fatalf("ITI-41: %s", rr.Summary())
	}
	c := &Client{}
	resp, err := c.Query(context.Background(), srv.URL+"/registry", ActionStoredQuery, FindDocuments(patient, ""))
	if err != nil || resp.Status != ResponseSuccess || len(resp.Objects.Extrinsic) != 1 {
		t.Fatalf("ITI-18: %v %+v", err, resp)
	}
	de := resp.Objects.Extrinsic[0]
	uid := de.ExternalID(IDDocUniqueID)
	if de.Slot1("repositoryUniqueId") != reg.RepositoryID || de.Slot1("size") != "40" || len(de.Slot1("hash")) != 40 ||
		!strings.HasPrefix(de.ID, "urn:uuid:") || de.Status != StatusApproved {
		t.Errorf("the entry as registered: %+v", de)
	}
	got, err := c.Retrieve(context.Background(), srv.URL+"/repository", ActionRetrieve, &RetrieveRequest{Requests: []DocumentRequest{
		{RepositoryUniqueID: reg.RepositoryID, DocumentUniqueID: uid}, {RepositoryUniqueID: reg.RepositoryID, DocumentUniqueID: "1.2.3.nope"}}})
	if err != nil {
		t.Fatal(err)
	}
	if got.Status.Status != ResponsePartialSuccess || len(got.Documents) != 1 || string(got.Documents[0].Document.Content) != "<ClinicalDocument>one</ClinicalDocument>" ||
		got.Status.Errors[0].Code != ErrMissingDocument {
		t.Errorf("ITI-43: %+v", got)
	}

	// Filters: a class the document has finds it; another does not; a creation time window.
	q := FindDocuments(patient, "ObjectRef")
	q.Query.Slots = append(q.Query.Slots, Slot{Name: "$XDSDocumentEntryClassCode", Values: []string{"('11490-0^^^2.16.840.1.113883.6.1')"}},
		Slot{Name: "$XDSDocumentEntryCreationTimeFrom", Values: []string{"20261001"}}, Slot{Name: "$XDSDocumentEntryCreationTimeTo", Values: []string{"20261002"}},
		Slot{Name: "$XDSDocumentEntryAuthorPerson", Values: []string{"('%SMITH%')"}})
	if resp, _ := c.Query(context.Background(), srv.URL+"/registry", ActionStoredQuery, q); len(resp.Objects.ObjectRefs) != 1 {
		t.Errorf("matching filters: %+v", resp)
	}
	q.Query.Slots[2].Values = []string{"('34133-9^^^2.16.840.1.113883.6.1')"}
	if resp, _ := c.Query(context.Background(), srv.URL+"/registry", ActionStoredQuery, q); len(resp.Objects.ObjectRefs) != 0 {
		t.Errorf("another class: %+v", resp)
	}

	// GetSubmissionSetAndContents and GetAll return the set, the document and their association.
	all := &AdhocQueryRequest{}
	all.ResponseOpt.ReturnType = "LeafClass"
	all.Query.ID = QGetAll
	all.Query.Slots = []Slot{{Name: "$patientId", Values: []string{"'" + patient + "'"}}, {Name: "$XDSDocumentEntryStatus", Values: []string{"('" + StatusApproved + "')"}},
		{Name: "$XDSSubmissionSetStatus", Values: []string{"('" + StatusApproved + "')"}}, {Name: "$XDSFolderStatus", Values: []string{"('" + StatusApproved + "')"}}}
	if resp, _ := c.Query(context.Background(), srv.URL+"/registry", ActionStoredQuery, all); len(resp.Objects.Extrinsic) != 1 ||
		len(resp.Objects.Packages) != 1 || len(resp.Objects.Associations) != 1 || !resp.Objects.Packages[0].HasNode(NodeSubmissionSet) {
		t.Errorf("GetAll: %+v", resp.Objects)
	}
}

func TestAReplacementDeprecatesTheOriginal(t *testing.T) {
	_, srv := rig(t)
	submit(t, srv, meta(), "first")
	c := &Client{}
	resp, _ := c.Query(context.Background(), srv.URL+"/registry", ActionStoredQuery, FindDocuments(patient, ""))
	old := resp.Objects.Extrinsic[0].ID
	m := meta()
	m.Replaces = old
	if rr := submit(t, srv, m, "second"); rr.Failed() {
		t.Fatalf("RPLC: %s", rr.Summary())
	}
	resp, _ = c.Query(context.Background(), srv.URL+"/registry", ActionStoredQuery, FindDocuments(patient, ""))
	if len(resp.Objects.Extrinsic) != 1 || resp.Objects.Extrinsic[0].ID == old {
		t.Fatalf("approved after RPLC: %+v", resp.Objects.Extrinsic)
	}
	rel := &AdhocQueryRequest{}
	rel.ResponseOpt.ReturnType = "LeafClass"
	rel.Query.ID = QGetRelatedDocuments
	rel.Query.Slots = []Slot{{Name: "$XDSDocumentEntryEntryUUID", Values: []string{"'" + old + "'"}},
		{Name: "$AssociationTypes", Values: []string{"('" + AssocReplace + "')"}}}
	if resp, _ := c.Query(context.Background(), srv.URL+"/registry", ActionStoredQuery, rel); len(resp.Objects.Extrinsic) != 2 ||
		len(resp.Objects.Associations) != 1 {
		t.Errorf("GetRelatedDocuments: %+v", resp.Objects)
	}
	// Replacing it again is refused: it is deprecated now.
	if rr := submit(t, srv, m, "third"); !rr.Failed() {
		t.Error("replacing a deprecated document was accepted")
	}
}

func TestSubmissionsAreCheckedWithTheIHEErrorCodes(t *testing.T) {
	_, srv := rig(t)
	c := &Client{}
	send := func(m DocumentMeta, edit func(*SubmitObjectsRequest), docs map[string][]byte) *RegistryResponse {
		req, err := Build(m, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if edit != nil {
			edit(req)
		}
		if docs == nil {
			docs = map[string][]byte{"Document01": []byte("x")}
		}
		rr, err := c.ProvideAndRegister(context.Background(), srv.URL+"/repository", req, docs)
		if err != nil {
			t.Fatal(err)
		}
		return rr
	}
	codes := func(rr *RegistryResponse) string {
		var c []string
		for _, e := range rr.Errors {
			c = append(c, e.Code)
		}
		return strings.Join(c, " ")
	}
	m := meta()
	m.UniqueID = "1.2.826.0.1.3680043.10.1337.99.50"
	if rr := send(m, nil, nil); rr.Failed() {
		t.Fatalf("first: %s", rr.Summary())
	}
	if rr := send(m, nil, nil); !strings.Contains(codes(rr), ErrDuplicateUniqueID) {
		t.Errorf("duplicate: %s", rr.Summary())
	}
	if rr := send(m, nil, map[string][]byte{"Document01": []byte("different")}); !strings.Contains(codes(rr), ErrNonIdenticalHash) {
		t.Errorf("same uniqueId, other content: %s", rr.Summary())
	}
	m.UniqueID = ""
	if rr := send(m, func(r *SubmitObjectsRequest) {
		for i, e := range r.Objects.Extrinsic[0].ExternalIDs {
			if e.IdentificationScheme == IDDocPatientID {
				r.Objects.Extrinsic[0].ExternalIDs[i].Value = "OTHER^^^&1.2.3&ISO"
			}
		}
	}, nil); !strings.Contains(codes(rr), ErrPatientMismatch) {
		t.Errorf("patient mismatch: %s", rr.Summary())
	}
	if rr := send(m, func(r *SubmitObjectsRequest) { r.Objects.Extrinsic[0].SetSlot("hash", strings.Repeat("0", 40)) }, nil); !strings.Contains(codes(rr), ErrNonIdenticalHash) {
		t.Errorf("wrong hash: %s", rr.Summary())
	}
	if rr := send(m, func(r *SubmitObjectsRequest) {
		r.Objects.Extrinsic[0].Classifications = r.Objects.Extrinsic[0].Classifications[:1]
	}, nil); !strings.Contains(codes(rr), ErrMetadata) {
		t.Errorf("no codes: %s", rr.Summary())
	}
	if rr := send(m, func(r *SubmitObjectsRequest) { r.Objects.Associations = nil }, nil); !strings.Contains(rr.Summary(), "not a member") {
		t.Errorf("no HasMember: %s", rr.Summary())
	}
	// The client refuses to send a document with no content, so a request missing it is built by hand.
	req, _ := Build(m, time.Now())
	body, _ := xml.Marshal(ProvideAndRegisterRequest{Submit: *req})
	env, err := c.do(context.Background(), srv.URL+"/repository", Outgoing{Action: ActionProvideAndRegister, Body: body, MTOM: true})
	if err != nil {
		t.Fatal(err)
	}
	var rr RegistryResponse
	_ = xml.Unmarshal(env.Body, &rr)
	if !strings.Contains(codes(&rr), ErrMissingDocument) {
		t.Errorf("no document: %s", rr.Summary())
	}
	// A query with a missing parameter, an unknown query, and an action this endpoint does not answer.
	q := FindDocuments(patient, "")
	q.Query.Slots = q.Query.Slots[:1]
	if resp, _ := c.Query(context.Background(), srv.URL+"/registry", ActionStoredQuery, q); resp.Status != ResponseFailure || resp.Errors[0].Code != ErrQueryMissingParam {
		t.Errorf("missing status: %+v", resp)
	}
	q.Query.ID = "urn:uuid:00000000-0000-0000-0000-000000000000"
	if resp, _ := c.Query(context.Background(), srv.URL+"/registry", ActionStoredQuery, q); resp.Errors[0].Code != ErrUnknownStoredQuery {
		t.Errorf("unknown query: %+v", resp)
	}
	if _, err := c.Query(context.Background(), srv.URL+"/repository", ActionStoredQuery, FindDocuments(patient, "")); err == nil || !strings.Contains(err.Error(), "fault") {
		t.Errorf("a query to the repository endpoint: %v", err)
	}
}

func TestTheRespondingGatewayAnswersForItsCommunity(t *testing.T) {
	reg, srv := rig(t)
	submit(t, srv, meta(), "for XCA")
	c := &Client{}
	resp, err := c.Query(context.Background(), srv.URL+"/xca", ActionCrossGatewayQuery, FindDocuments(patient, ""))
	if err != nil || len(resp.Objects.Extrinsic) != 1 || resp.Objects.Extrinsic[0].Home != reg.HomeCommunityID {
		t.Fatalf("ITI-38: %v %+v", err, resp)
	}
	uid := resp.Objects.Extrinsic[0].ExternalID(IDDocUniqueID)
	got, err := c.Retrieve(context.Background(), srv.URL+"/xca", ActionCrossGatewayRetr, &RetrieveRequest{Requests: []DocumentRequest{
		{HomeCommunityID: reg.HomeCommunityID, RepositoryUniqueID: reg.RepositoryID, DocumentUniqueID: uid}}})
	if err != nil || len(got.Documents) != 1 || got.Documents[0].HomeCommunityID != reg.HomeCommunityID || string(got.Documents[0].Document.Content) != "for XCA" {
		t.Fatalf("ITI-39: %v %+v", err, got)
	}
	got, _ = c.Retrieve(context.Background(), srv.URL+"/xca", ActionCrossGatewayRetr, &RetrieveRequest{Requests: []DocumentRequest{
		{HomeCommunityID: "urn:oid:9.9.9", RepositoryUniqueID: reg.RepositoryID, DocumentUniqueID: uid}}})
	if len(got.Documents) != 0 || got.Status.Errors[0].Code != ErrUnknownCommunity {
		t.Errorf("another community: %+v", got)
	}
}

func TestQueryValuesAreReadAsIHEWritesThem(t *testing.T) {
	for in, want := range map[string]string{
		"('a','b')": "a|b", "'single'": "single", "20040101": "20040101", "('it''s', 'x')": "it's|x", "( 'a' , 'b' )": "a|b",
		"('A^^^&1.2&ISO')": "A^^^&1.2&ISO",
	} {
		if got := strings.Join(values([]string{in}), "|"); got != want {
			t.Errorf("%s: %q, want %q", in, got, want)
		}
	}
	if o := UUIDOID(); !strings.HasPrefix(o, "2.25.") || len(o) < 20 || strings.ContainsAny(o[5:], ".-abcdef") {
		t.Errorf("UUID OID %s", o)
	}
}

func nowForTest() time.Time { return time.Now() }

func FuzzReadEnvelope(f *testing.F) {
	req, _ := Build(meta(), time.Now())
	body, _ := xml.Marshal(ProvideAndRegisterRequest{Submit: *req, Documents: []Document{{ID: "Document01", Inner: Include("a@b")}}})
	mtom, ct := Outgoing{Action: ActionProvideAndRegister, Body: body, Attachments: []Attachment{{ContentID: "a@b", Content: []byte("x")}}, MTOM: true}.Write()
	plain, pct := Outgoing{Action: ActionStoredQuery, Body: []byte(`<q/>`)}.Write()
	f.Add(ct, mtom)
	f.Add(pct, plain)
	f.Fuzz(func(t *testing.T, contentType string, b []byte) {
		env, err := ReadEnvelope(contentType, bytes.NewReader(b))
		if err != nil {
			return
		}
		var p ProvideAndRegisterRequest
		if xml.Unmarshal(env.Body, &p) == nil {
			for i := range p.Documents {
				_ = env.Resolve(&p.Documents[i])
			}
		}
		var q AdhocQueryRequest
		if xml.Unmarshal(env.Body, &q) == nil {
			_ = readParams(q.Query.Slots)
		}
	})
}
