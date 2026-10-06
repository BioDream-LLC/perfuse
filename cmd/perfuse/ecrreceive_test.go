package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/publichealth"
	"github.com/biodream-llc/perfuse/internal/v2fhir"
)

func ecrServer(t *testing.T, h *ecrMessages) (*httptest.Server, *fhirserver.Store) {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	st, err := fhirserver.NewStore(db, fhir.R4)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(nil)
	t.Cleanup(ts.Close)
	srv := fhirserver.NewServer(st, ts.URL, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.Auth = fhirserver.OpenAuth{}
	h.store, h.log, h.now, h.client = st, slog.New(slog.NewTextHandler(io.Discard, nil)), time.Now, ts.Client()
	srv.Messages = h.handle
	ts.Config.Handler = srv.Handler()
	return ts, st
}

// The whole exchange between two Perfuse servers: a hospital's case report goes to a test agency, which answers it and sends
// the Reportability Response back to the hospital, where it is stored under the id the hospital logged when it sent the report.
func TestACaseReportIsAnsweredAndTheResponseLinkedBack(t *testing.T) {
	hospital, hospitalStore := ecrServer(t, &ecrMessages{receive: true})
	agency := &agencyConfig{Agency: publichealth.Agency{Name: "Illinois Department of Public Health", Phone: "+1-217-555-0199",
		Line: "535 W Jefferson St", City: "Springfield", State: "IL", PostalCode: "62761", Endpoint: "https://ph.test/fhir"}, Reply: true}
	ph, _ := ecrServer(t, &ecrMessages{agency: agency, triggers: publichealth.BuiltinTriggers()})

	raw, err := os.ReadFile("../../internal/publichealth/testdata/adt-covid.hl7")
	if err != nil {
		t.Fatal(err)
	}
	m, err := hl7.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	e, err := publichealth.FromV2(m, nil, v2fhir.Options{}, publichealth.EICROptions{Facility: publichealth.Facility{
		Name: "Springfield General Hospital", NPI: "1234567893", Phone: "+1-217-555-0100", Line: "100 Main St",
		City: "Springfield", State: "IL", PostalCode: "62701"}})
	if err != nil {
		t.Fatal(err)
	}
	msg, err := publichealth.ReportingBundle(e, publichealth.ReportingOptions{Destination: ph.URL, Source: hospital.URL})
	if err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(msg)
	resp, err := http.Post(ph.URL+"/$process-message", "application/fhir+json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	reply, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("agency answered %d: %s", resp.StatusCode, reply)
	}
	rr, err := publichealth.ParseRR(reply)
	if err != nil || !rr.Reportable() {
		t.Fatalf("the agency's answer: %v %+v", err, rr)
	}

	eicrID := e.Bundle["identifier"].(map[string]any)["value"].(string)
	id := publichealth.RRStorageID(eicrID)
	deadline := time.Now().Add(5 * time.Second)
	for {
		if r, err := hospitalStore.Get(t.Context(), "DocumentReference", id); err == nil && r != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the Reportability Response never reached the hospital")
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, err := http.Get(hospital.URL + "/DocumentReference/" + id)
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Content []struct {
			Attachment struct{ Data []byte } `json:"attachment"`
		} `json:"content"`
	}
	_ = json.NewDecoder(got.Body).Decode(&doc)
	got.Body.Close()
	if len(doc.Content) == 0 {
		t.Fatal("the stored response has no content")
	}
	back, err := publichealth.ParseRR(doc.Content[0].Attachment.Data)
	if err != nil || back.EICR != eicrID || !back.Reportable() {
		t.Fatalf("stored RR: %v %+v", err, back)
	}

	// A hospital does not answer case reports, and an agency does not take Reportability Responses.
	if resp, _ := http.Post(hospital.URL+"/$process-message", "application/fhir+json", bytes.NewReader(body)); resp.StatusCode != 422 {
		t.Errorf("the hospital took a case report: %d", resp.StatusCode)
	}
	if resp, _ := http.Post(ph.URL+"/$process-message", "application/fhir+json", bytes.NewReader(reply)); resp.StatusCode != 422 {
		t.Errorf("the agency took an RR: %d", resp.StatusCode)
	}
}
