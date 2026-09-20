package engine

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/eprescribe"
)

// newFilteredScriptChannel is newPharmacyChannel with a filter, kept separate rather than adding a parameter because the
// interesting assertion is about what reaches the destination and that needs the sink either way.
func newFilteredScriptChannel(t *testing.T, filter string, sink *recordingSender) *Channel {
	t.Helper()

	sink.name = "out"
	yaml := fmt.Sprintf(`
name: script-filtered
dataType: script
filter: '%s'
source:
  type: http
  http:
    listen: "127.0.0.1:0"
    path: /in
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`, filter)

	cfg, err := config.Load(strings.NewReader(yaml), "script.yaml")
	if err != nil {
		t.Fatalf("loading the config: %v", err)
	}

	e, err := New([]*config.Channel{cfg}, func(config.Destination) (Sender, error) { return sink, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Start(); err != nil {
		t.Fatalf("starting: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = e.Stop(ctx)
	})

	chans := e.Channels()
	if len(chans) != 1 {
		t.Fatalf("got %d channels, want 1", len(chans))
	}

	return chans[0]
}

// prescription marshals a SCRIPT message with the given DEA schedule.
//
// Marshalled rather than hand-written, so the bytes are what this package actually produces. A hand-written document could
// drift from the real element names and the test would then prove nothing about real traffic.
func prescription(t *testing.T, schedule string) []byte {
	t.Helper()

	m := eprescribe.Message{
		Version: eprescribe.DefaultVersion,
		Release: eprescribe.DefaultRelease,
		Header: eprescribe.Header{
			To:                    eprescribe.Endpoint{Qualifier: "P", Value: "1234567"},
			From:                  eprescribe.Endpoint{Qualifier: "D", Value: "9876543"},
			MessageID:             "MSG-0001",
			SentTime:              "2026-08-30T10:00:00Z",
			PrescriberOrderNumber: "ORD-1",
			SenderSoftware:        eprescribe.SenderSoftware{Developer: "Perfuse", Product: "Perfuse", VersionRelease: "1"},
		},
		Body: eprescribe.Body{NewRx: &eprescribe.Prescription{
			Patient: eprescribe.Patient{
				Name:        eprescribe.Name{Last: "OKONKWO", First: "ADAEZE"},
				Gender:      "F",
				DateOfBirth: eprescribe.Date{Date: "1980-01-01"},
			},
			Pharmacy:   eprescribe.Pharmacy{BusinessName: "MAIN STREET PHARMACY", NCPDPID: "1234567"},
			Prescriber: eprescribe.Prescriber{Name: eprescribe.Name{Last: "NAKAMURA", First: "KENJI"}, NPI: "1234567893"},
			Medication: &eprescribe.Medication{
				Description:     "Oxycodone 5 MG Oral Tablet",
				Coded:           &eprescribe.Coded{ProductCode: "00093-1036-01", ProductCodeQualifier: "ND", DEASchedule: schedule},
				Quantity:        &eprescribe.Quantity{Value: "30", UnitOfMeasure: &eprescribe.UnitOfMeasure{Code: "C48542"}},
				DaysSupply:      "30",
				Substitutions:   "0",
				NumberOfRefills: "0",
				Sig:             &eprescribe.Sig{Text: "Take one tablet by mouth every six hours as needed"},
				WrittenDate:     &eprescribe.Date{Date: "2026-08-30"},
			},
		}},
	}

	raw, err := xml.Marshal(m)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	return raw
}

// TestAControlledPrescriptionPassesTheFilterAndAnOrdinaryOneDoesNot is the whole feature, asserted on delivery.
//
// This drives the real handler through the real engine, so the filter, the outcome, the statistics and the destination are all
// exercised together. A unit test on the expression can only prove the expression; it cannot prove that anything consults it,
// and an implementation with no caller is indistinguishable from a feature that does not exist.
func TestAControlledPrescriptionPassesTheFilterAndAnOrdinaryOneDoesNot(t *testing.T) {
	sink := &recordingSender{}
	c := newFilteredScriptChannel(t, `//DEASchedule == "CII"`, sink)

	// The CII prescription is delivered.
	if _, err := c.handlePharmacy(context.Background(), prescription(t, "CII")); err != nil {
		t.Fatalf("handling the controlled prescription: %v", err)
	}
	if got := sink.count(); got != 1 {
		t.Fatalf("the CII prescription produced %d delivery(ies), want 1", got)
	}
	if !strings.Contains(sink.last(), "CII") {
		t.Error("what was delivered is not the prescription that was sent")
	}

	// An uncontrolled one is excluded, and nothing further reaches the destination.
	if _, err := c.handlePharmacy(context.Background(), prescription(t, "")); err != nil {
		t.Fatalf("handling the uncontrolled prescription: %v", err)
	}
	if got := sink.count(); got != 1 {
		t.Fatalf("after an excluded prescription the destination has %d delivery(ies), want 1", got)
	}

	// Counted as filtered rather than failed or delivered. This is what a site sees on the dashboard, and "excluded" and
	// "broken" have to be different numbers or the filter looks like an outage.
	stats := c.Stats()
	if stats.Filtered != 1 {
		t.Errorf("Filtered = %d, want 1", stats.Filtered)
	}
	if stats.Failed != 0 {
		t.Errorf("Failed = %d, want 0 - an excluded prescription is not a failure", stats.Failed)
	}
}

// TestAnExcludedPrescriptionIsRecordedRatherThanDropped matters more here than for most formats.
//
// A prescriber asking why a prescription never reached the pharmacy needs an answer, and for a controlled substance that
// question may come from a regulator. Silence is not an acceptable record.
func TestAnExcludedPrescriptionIsRecordedRatherThanDropped(t *testing.T) {
	sink := &recordingSender{}
	c := newFilteredScriptChannel(t, `//DEASchedule == "CII"`, sink)

	if _, err := c.handlePharmacy(context.Background(), prescription(t, "")); err != nil {
		t.Fatalf("handling: %v", err)
	}

	if got := c.lastOutcome.get(); got != Filtered {
		t.Errorf("the last outcome is %q, want %q", got, Filtered)
	}
}

// TestAFilterThatCannotBeEvaluatedFailsRatherThanDelivers is the direction that matters.
//
// A filter exists to keep messages out. If an evaluation error were treated as a pass, the failure mode would be delivering
// exactly the traffic somebody wrote the filter to stop - and for e-prescribing that could mean a controlled substance taking
// an unaudited route. Failing is noisy and safe; passing is quiet and wrong.
func TestAFilterThatCannotBeEvaluatedFailsRatherThanDelivers(t *testing.T) {
	sink := &recordingSender{}
	c := newFilteredScriptChannel(t, `//DEASchedule == "CII"`, sink)

	// Well-formed XML that is not a prescription. The tree parse refuses it, which is the path that must not deliver.
	if _, err := c.handlePharmacy(context.Background(), []byte(`<ClinicalDocument><recordTarget/></ClinicalDocument>`)); err != nil {
		t.Logf("handling returned %v, which is acceptable", err)
	}

	if got := sink.count(); got != 0 {
		t.Errorf("%d message(s) were delivered despite the filter being unable to read them", got)
	}
}
