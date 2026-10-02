package fhirserver

import (
	"database/sql"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/v2fhir"
)

// Perfuse's own v2 conversions, posted to Perfuse's own R4 server, must be accepted.
//
// Nothing did this until subscriptions needed Encounters on an R4 server, and the first attempt answered 400: every ADT
// carries Encounter.class, which the outbound downgrade reshaped and the inbound path did not reverse. The pipeline the
// product exists for - v2 in, FHIR out, stored - had never been run end to end against its own store.
func TestOwnConversionsAreAcceptedByOwnR4Server(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	store, err := NewStore(db, fhir.R4)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(store, "http://example.test/fhir", slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv.Auth = OpenAuth{}
	h := srv.Handler()

	pid := "PID|1||MRN1^^^SITEA^MR||Doe^Jane||19800101|F"
	pv1 := "PV1|1|I|ICU^01^01||||1234^Attending^Adam||||||||||||V1^^^SITEA^VN|||||||||||||||||||||||||20261001090000-0500"
	msh := func(t string) string {
		return "MSH|^~\\&|EHR|SITEA|PERFUSE|SITEA|20261001090000-0500||" + t + "|C-" + t + "|P|2.5.1"
	}
	messages := map[string][]string{
		"ADT^A01": {msh("ADT^A01"), pid, pv1},
		"SIU^S12": {msh("SIU^S12"), "SCH|PLC1|FIL1||||||||||||||||||||||||Booked", pid,
			"AIS|1||99213^Office visit|20261015143000-0500|||30|MIN", "AIP|1||1234^Attending^Adam"},
		"MDM^T02": {msh("MDM^T02"), pid, pv1, "TXA|1|DS|TX|20261001083000-0500|||||1234^Attending^Adam|||DOC1|||||AU||AV",
			"OBX|1|TX|||Discharged home."},
		"VXU^V04": {msh("VXU^V04"), pid, "ORC|RE||IMM1", "RXA|0|1|20261001||08^Hep B^CVX|0.5|mL||00||||||LOT1|||||CP",
			"RXR|IM^Intramuscular^HL70162"},
	}
	for name, segs := range messages {
		m, err := hl7.Parse([]byte(strings.Join(segs, "\r")))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		res, err := v2fhir.Convert(m, v2fhir.Options{Version: fhir.R4, DefaultIdentifierSystem: "urn:example:ids"})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		body, err := fhir.MarshalBundle(res.Bundle, fhir.R4)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		rec := do(t, h, "POST", "/", string(body))
		if rec.Code != 200 {
			t.Errorf("%s: Perfuse's R4 server refused Perfuse's R4 conversion: %d %s", name, rec.Code, rec.Body.String())
		}
	}
}
