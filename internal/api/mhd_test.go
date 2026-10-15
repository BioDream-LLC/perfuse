package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/biodream-llc/perfuse/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/xds"
)

// xdsHarness is the API with an XDS registry, a FHIR store holding one patient with two identifiers, and a token per role.
func xdsHarness(t *testing.T) (*harness, string, string, *httptest.Server) {
	t.Helper()
	h := newHarness(t)
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	fs, err := fhirserver.NewStore(db, fhir.R4)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := fhir.UnmarshalResource([]byte(`{"resourceType":"Patient","id":"pt1","name":[{"family":"Doe","given":["Jane"]}],
		"gender":"female","birthDate":"1970-01-01","identifier":[{"system":"urn:oid:1.2.826.0.1.3680043.10.1337.99","value":"DWMRN1"},
		{"system":"urn:oid:2.16.840.1.113883.4.1","value":"123-45-6789"}]}`))
	if _, err := fs.Put(context.Background(), p); err != nil {
		t.Fatal(err)
	}
	h.server.XDS = &xds.Registry{DB: db, RepositoryID: "1.2.826.0.1.3680043.10.1337.99.1", HomeCommunityID: "urn:oid:1.2.826.0.1.3680043.10.1337.99.2"}
	h.server.FHIRStore = fs
	h.handler = h.server.Handler()
	srv := httptest.NewServer(h.handler)
	t.Cleanup(srv.Close)
	h.server.SelfURL = srv.URL
	reader, err := h.store.CreateAPIToken(t.Context(), "xds-reader", store.RoleViewer, "admin")
	if err != nil {
		t.Fatal(err)
	}
	writer, err := h.store.CreateAPIToken(t.Context(), "xds-writer", store.RoleOperator, "admin")
	if err != nil {
		t.Fatal(err)
	}
	return h, reader, writer, srv
}

const mhdBundle = `{"resourceType":"Bundle","type":"transaction","entry":[
 {"fullUrl":"urn:uuid:ss1","resource":{"resourceType":"List","status":"current","mode":"working","title":"Visit",
   "code":{"coding":[{"system":"https://profiles.ihe.net/ITI/MHD/CodeSystem/MHDlistTypes","code":"submissionset"}]},
   "extension":[{"url":"https://profiles.ihe.net/ITI/MHD/StructureDefinition/ihe-sourceId","valueIdentifier":{"value":"urn:oid:1.2.826.0.1.3680043.10.1337.99.7"}},
     {"url":"https://profiles.ihe.net/ITI/MHD/StructureDefinition/ihe-designationType","valueCodeableConcept":{"coding":[{"system":"http://loinc.org","code":"11488-4","display":"Consult note"}]}}],
   "subject":{"reference":"Patient/pt1"},"date":"2026-10-15T10:00:00Z","entry":[{"item":{"reference":"urn:uuid:dr1"}}]},
  "request":{"method":"POST","url":"List"}},
 {"fullUrl":"urn:uuid:dr1","resource":{"resourceType":"DocumentReference","status":"current","subject":{"reference":"Patient/pt1"},
   "masterIdentifier":{"system":"urn:ietf:rfc:3986","value":"urn:oid:1.2.826.0.1.3680043.10.1337.99.70"},
   "type":{"coding":[{"system":"http://loinc.org","code":"11488-4","display":"Consult note"}]},
   "category":[{"coding":[{"system":"http://loinc.org","code":"11488-4","display":"Consult note"}]}],
   "securityLabel":[{"coding":[{"system":"http://terminology.hl7.org/CodeSystem/v3-Confidentiality","code":"N"}]}],
   "content":[{"attachment":{"contentType":"text/plain","url":"urn:uuid:bin1","title":"Consult","creation":"2026-10-14T09:00:00Z","language":"en-US"},
     "format":{"system":"http://ihe.net/fhir/ihe.formatcode.fhir/CodeSystem/formatcode","code":"urn:ihe:iti:xds:2017:mimeTypeSufficient"}}],
   "context":{"facilityType":{"coding":[{"system":"http://snomed.info/sct","code":"225732001"}]},
     "practiceSetting":{"coding":[{"system":"http://snomed.info/sct","code":"394802001"}]}}},
  "request":{"method":"POST","url":"DocumentReference"}},
 {"fullUrl":"urn:uuid:bin1","resource":{"resourceType":"Binary","contentType":"text/plain","data":"` + "Q29uc3VsdCBub3RlIGJvZHk=" + `"},
  "request":{"method":"POST","url":"Binary"}}]}`

func fhirDo(t *testing.T, srv *httptest.Server, tok, method, path, body string) (int, map[string]any, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, srv.URL+path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Content-Type", "application/fhir+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	var out map[string]any
	_ = json.Unmarshal(buf.Bytes(), &out)
	return resp.StatusCode, out, buf.Bytes()
}

func TestADocumentProvidedOverMHDIsFoundOverMHDAndXDS(t *testing.T) {
	_, reader, writer, srv := xdsHarness(t)
	if code, _, _ := fhirDo(t, srv, reader, http.MethodPost, "/mhd", mhdBundle); code != http.StatusForbidden {
		t.Errorf("a viewer providing: %d", code)
	}
	code, out, raw := fhirDo(t, srv, writer, http.MethodPost, "/mhd", mhdBundle)
	if code != http.StatusOK || out["type"] != "transaction-response" {
		t.Fatalf("ITI-65: %d %s", code, raw)
	}
	// ITI-67 by patient.identifier, and by reference.
	for _, q := range []string{"patient.identifier=urn:oid:1.2.826.0.1.3680043.10.1337.99|DWMRN1", "patient=Patient/pt1&status=current&type=http://loinc.org|11488-4"} {
		code, out, raw := fhirDo(t, srv, reader, http.MethodGet, "/mhd/DocumentReference?"+q, "")
		if code != http.StatusOK || out["total"].(float64) != 1 {
			t.Fatalf("ITI-67 %s: %d %s", q, code, raw)
		}
		dr := out["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)
		att := dr["content"].([]any)[0].(map[string]any)["attachment"].(map[string]any)
		code, _, body := fhirDo(t, srv, reader, http.MethodGet, strings.TrimPrefix(att["url"].(string), srv.URL), "")
		if code != http.StatusOK || string(body) != "Consult note body" {
			t.Fatalf("ITI-68: %d %q", code, body)
		}
		if att["size"].(float64) != 17 || att["hash"] == "" || dr["masterIdentifier"].(map[string]any)["value"] != "urn:oid:1.2.826.0.1.3680043.10.1337.99.70" {
			t.Errorf("the DocumentReference: %+v", dr)
		}
	}
	// The same document over XDS.b SOAP.
	c := &xds.Client{Headers: map[string]string{"Authorization": "Bearer " + reader}}
	resp, err := c.Query(context.Background(), srv.URL+"/xds/registry", xds.ActionStoredQuery, xds.FindDocuments("DWMRN1^^^&1.2.826.0.1.3680043.10.1337.99&ISO", ""))
	if err != nil || len(resp.Objects.Extrinsic) != 1 || resp.Objects.Extrinsic[0].ExternalID(xds.IDDocUniqueID) != "1.2.826.0.1.3680043.10.1337.99.70" {
		t.Fatalf("ITI-18 after ITI-65: %v %+v", err, resp)
	}
	// ITI-66.
	if code, out, raw := fhirDo(t, srv, reader, http.MethodGet, "/mhd/List?patient=Patient/pt1", ""); code != http.StatusOK || out["total"].(float64) != 1 {
		t.Errorf("ITI-66: %d %s", code, raw)
	}
	// A Bundle without a sourceId is refused with the reason.
	bad := strings.Replace(mhdBundle, "ihe-sourceId", "something-else", 1)
	if code, _, raw := fhirDo(t, srv, writer, http.MethodPost, "/mhd", bad); code != http.StatusBadRequest || !bytes.Contains(raw, []byte("sourceId")) {
		t.Errorf("no sourceId: %d %s", code, raw)
	}
	// The same uniqueId again is the registry's error.
	if code, _, raw := fhirDo(t, srv, writer, http.MethodPost, "/mhd", mhdBundle); code != http.StatusUnprocessableEntity ||
		!bytes.Contains(raw, []byte(xds.ErrDuplicateUniqueID)) {
		t.Errorf("a duplicate: %d %s", code, raw)
	}
}

func TestADocumentProvidedOverXDSIsFoundOverMHD(t *testing.T) {
	_, reader, writer, srv := xdsHarness(t)
	m := xds.DocumentMeta{PatientID: "DWMRN1^^^&1.2.826.0.1.3680043.10.1337.99&ISO", SourceID: "1.2.3", Title: "Discharge", MimeType: "text/xml",
		Class: xds.Code{Code: "18842-5", Scheme: "2.16.840.1.113883.6.1"}, Type: xds.Code{Code: "18842-5", Scheme: "2.16.840.1.113883.6.1"},
		Format: xds.Code{Code: "urn:ihe:iti:xds:2017:mimeTypeSufficient", Scheme: "1.3.6.1.4.1.19376.1.2.3"}, Confidentiality: xds.Code{Code: "N", Scheme: "2.16.840.1.113883.5.25"},
		FacilityType: xds.Code{Code: "225732001", Scheme: "2.16.840.1.113883.6.96"}, PracticeSetting: xds.Code{Code: "394802001", Scheme: "2.16.840.1.113883.6.96"},
		ContentType: xds.Code{Code: "18842-5", Scheme: "2.16.840.1.113883.6.1"}}
	req, _ := xds.Build(m, time.Now())
	c := &xds.Client{Headers: map[string]string{"Authorization": "Bearer " + writer}}
	rr, err := c.ProvideAndRegister(context.Background(), srv.URL+"/xds/repository", req, map[string][]byte{"Document01": []byte("<x/>")})
	if err != nil || rr.Failed() {
		t.Fatalf("ITI-41: %v %+v", err, rr)
	}
	// A viewer token cannot submit.
	cr := &xds.Client{Headers: map[string]string{"Authorization": "Bearer " + reader}}
	if _, err := cr.ProvideAndRegister(context.Background(), srv.URL+"/xds/repository", req, map[string][]byte{"Document01": []byte("<x/>")}); err == nil ||
		!strings.Contains(err.Error(), "operator") {
		t.Errorf("a viewer submitting: %v", err)
	}
	code, out, raw := fhirDo(t, srv, reader, http.MethodGet, "/mhd/DocumentReference?patient=Patient/pt1", "")
	if code != http.StatusOK || out["total"].(float64) != 1 {
		t.Fatalf("ITI-67: %d %s", code, raw)
	}
	dr := out["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)
	if code, read, _ := fhirDo(t, srv, reader, http.MethodGet, "/mhd/DocumentReference/"+dr["id"].(string), ""); code != http.StatusOK || read["id"] != dr["id"] {
		t.Errorf("read: %d", code)
	}
}

func TestPIXmAndPDQm(t *testing.T) {
	_, reader, _, srv := xdsHarness(t)
	code, out, raw := fhirDo(t, srv, reader, http.MethodGet, "/mhd/Patient/$ihe-pix?sourceIdentifier=urn:oid:1.2.826.0.1.3680043.10.1337.99|DWMRN1", "")
	if code != http.StatusOK || !bytes.Contains(raw, []byte("123-45-6789")) || bytes.Contains(raw, []byte(`"DWMRN1"`)) {
		t.Fatalf("$ihe-pix: %d %s", code, raw)
	}
	_ = out
	code, _, raw = fhirDo(t, srv, reader, http.MethodGet, "/mhd/Patient/$ihe-pix?sourceIdentifier=urn:oid:1.2.826.0.1.3680043.10.1337.99|DWMRN1&targetSystem=urn:oid:9.9.9", "")
	if code != http.StatusOK || bytes.Contains(raw, []byte("123-45-6789")) {
		t.Errorf("$ihe-pix with another target: %d %s", code, raw)
	}
	if code, _, _ := fhirDo(t, srv, reader, http.MethodGet, "/mhd/Patient/$ihe-pix?sourceIdentifier=urn:oid:1.2.3|NOBODY", ""); code != http.StatusNotFound {
		t.Errorf("an unknown identifier: %d", code)
	}
	if code, out, raw := fhirDo(t, srv, reader, http.MethodGet, "/mhd/Patient?family=doe&birthdate=1970-01-01", ""); code != http.StatusOK || out["total"].(float64) != 1 {
		t.Errorf("PDQm: %d %s", code, raw)
	}
	if code, _, _ := fhirDo(t, srv, reader, http.MethodGet, "/mhd/Patient?favourite-colour=blue", ""); code != http.StatusBadRequest {
		t.Errorf("an unsupported parameter: %d", code)
	}
	if code, _, raw := fhirDo(t, srv, reader, http.MethodGet, "/mhd/metadata", ""); code != http.StatusOK || !bytes.Contains(raw, []byte("IHE.MHD.DocumentRecipient")) {
		t.Errorf("metadata: %d", code)
	}
	_ = base64.StdEncoding
}
