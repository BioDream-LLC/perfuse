package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/mpi"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/xds"
)

func pixv3Sample(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "pixv3", "testdata", "ihe", name))
	if err != nil {
		t.Fatal(err)
	}
	if i := bytes.Index(b, []byte("?>")); bytes.HasPrefix(b, []byte("<?xml")) && i > 0 {
		b = b[i+2:]
	}
	return bytes.TrimSpace(b)
}

func TestPIXAndPDQV3OverSOAP(t *testing.T) {
	h := newHarness(t)
	pool, err := sqlitedb.Open(filepath.Join(t.TempDir(), "mpi.db"), sqlitedb.Options{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = pool.Close() })
	idx, err := mpi.Open(context.Background(), pool.Write)
	if err != nil {
		t.Fatal(err)
	}
	set, _ := idx.Settings(context.Background(), mpi.DefaultTenant)
	set.Trusted = []string{"urn:oid:1.2.840.114350.1.13.99997.2.3412"}
	_ = idx.SaveSettings(context.Background(), mpi.DefaultTenant, "test", set)
	h.server.PatientIndex = idx
	h.handler = h.server.Handler()
	srv := httptest.NewServer(h.handler)
	t.Cleanup(srv.Close)
	h.server.SelfURL = srv.URL
	reader, _ := h.store.CreateAPIToken(t.Context(), "pix-consumer", store.RoleViewer, "admin")
	writer, _ := h.store.CreateAPIToken(t.Context(), "pix-source", store.RoleOperator, "admin")

	post := func(path, token, interaction string, body []byte) (int, string) {
		t.Helper()
		b, ct := xds.Outgoing{Action: "urn:hl7-org:v3:" + interaction, Body: body}.Write()
		req, _ := http.NewRequest(http.MethodPost, srv.URL+path, bytes.NewReader(b))
		req.Header.Set("Content-Type", ct)
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		out, _ := io.ReadAll(res.Body)
		return res.StatusCode, string(out)
	}
	feed := pixv3Sample(t, "01_PatientRegistryRecordAdded1_20110504.xml")
	if code, body := post("/pixv3", reader, "PRPA_IN201301UV02", feed); code != http.StatusForbidden || !strings.Contains(body, "operator") {
		t.Errorf("a viewer fed the index: %d %s", code, body)
	}
	for _, f := range []string{"01_PatientRegistryRecordAdded1_20110504.xml", "03_PatientRegistryRecordAdded2_20110328.xml"} {
		code, body := post("/pixv3", writer, "PRPA_IN201301UV02", pixv3Sample(t, f))
		if code != http.StatusOK || !strings.Contains(body, "urn:hl7-org:v3:MCCI_IN000002UV01") || !strings.Contains(body, `<typeCode code="AA"/>`) {
			t.Fatalf("feed %s: %d %s", f, code, body)
		}
	}
	code, body := post("/pixv3", reader, "PRPA_IN201309UV02", pixv3Sample(t, "06_PIXQuery1_20110328.xml"))
	if code != http.StatusOK || !strings.Contains(body, "urn:hl7-org:v3:PRPA_IN201310UV02") || !strings.Contains(body, `extension="34827G409"`) ||
		!strings.Contains(body, "<wsa:RelatesTo>") {
		t.Fatalf("query: %d %s", code, body)
	}
	// The record fed in v3 is the one PIXm finds over FHIR.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/mhd/Patient/$ihe-pix?sourceIdentifier=urn:oid:1.2.840.114350.1.13.99998.8734|34827G409", nil)
	req.Header.Set("Authorization", "Bearer "+reader)
	if res, err := http.DefaultClient.Do(req); err != nil || res.StatusCode != http.StatusOK {
		t.Errorf("PIXm after a v3 feed: %v %v", err, res)
	} else {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if !strings.Contains(string(b), "38273N237") {
			t.Errorf("PIXm: %s", b)
		}
	}
	// A PDQ query at the PIX endpoint is refused, and a bare v3 message is answered as bare XML.
	if code, _ := post("/pixv3", reader, "PRPA_IN201305UV02", pixv3Sample(t, "01_PDQQuery1_20110328.xml")); code != http.StatusBadRequest {
		t.Errorf("a PDQ query at the PIX manager: %d", code)
	}
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/pdqv3", bytes.NewReader(pixv3Sample(t, "01_PDQQuery1_20110328.xml")))
	req.Header.Set("Content-Type", "application/xml")
	req.Header.Set("Authorization", "Bearer "+reader)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusOK || !bytes.Contains(b, []byte("<PRPA_IN201306UV02")) {
		t.Errorf("bare PDQ: %d %s", res.StatusCode, b)
	}
	// Every transaction is audited, with an identifier and never a name.
	entries, _ := h.store.ListAudit(t.Context(), 50)
	seen := map[string]bool{}
	for _, e := range entries {
		seen[e.Action] = true
		if strings.Contains(e.Target, "Dean") {
			t.Errorf("an audit line carries a name: %+v", e)
		}
	}
	if !seen["pixv3.feed"] || !seen["pixv3.query"] || !seen["pdqv3.query"] {
		t.Errorf("audit: %v", seen)
	}
}
