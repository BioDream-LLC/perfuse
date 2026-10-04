package engine

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

func TestACaseReportingDestinationSendsOnlyWhatIsReportable(t *testing.T) {
	var mu sync.Mutex
	var paths []string
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var b map[string]any
		_ = json.Unmarshal(raw, &b)
		mu.Lock()
		paths, bodies = append(paths, r.URL.Path), append(bodies, b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write([]byte(`{"resourceType":"Bundle","type":"message"}`))
	}))
	defer srv.Close()

	sender, err := NewFHIRSender(config.Destination{
		Name: "ph", Type: config.DestinationFHIR,
		FHIR: &config.FHIRDestination{
			URL:                     srv.URL + "/fhir",
			DefaultIdentifierSystem: "http://hospital.test/mrn",
			ECR: &config.ECRDestination{Source: "https://ehr.test/fhir", Facility: config.ECRFacility{
				Name: "Springfield General Hospital", Phone: "+1-217-555-0100", City: "Springfield", State: "IL",
			}},
		},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../publichealth/testdata/adt-covid.hl7")
	if err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), raw); err != nil {
		t.Fatal(err)
	}
	asthma := strings.ReplaceAll(string(raw), "840539006^COVID-19", "195967001^Asthma")
	if err := sender.Send(context.Background(), []byte(asthma)); err != nil {
		t.Fatalf("a message with nothing reportable is not a failure: %v", err)
	}
	if len(paths) != 1 || paths[0] != "/fhir/$process-message" || bodies[0]["type"] != "message" {
		t.Fatalf("posted %v", paths)
	}
	if st := sender.ECRStats(); st == nil || st.Reported != 1 || st.NotReportable != 1 {
		t.Errorf("stats %+v", st)
	}
}
