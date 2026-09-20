package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The FHIR destination against HAPI FHIR, which is the reference implementation most sites actually run.
//
// Every other test of this converter compares what Perfuse produced against what Perfuse expected to produce. That is agreement with
// oneself, and tonight it was shown to be capable of hiding a total failure: the SAML canonicaliser passed a thousand such tests while
// being unable to accept any real assertion. A FHIR resource is a bigger surface than a canonical form, and the consequence of getting
// it wrong is a receiving system rejecting clinical data.
//
// HAPI is a good judge because it validates structure, cardinality and value sets, and refuses what it cannot parse. If a resource is
// accepted here, a real server is likely to accept it too. If it is refused, the message says what is wrong in FHIR's own terms.
//
// Skipped when HAPI is not running: a check that needs a container is a check people stop running, so make check has to pass without
// one.

const hapiBase = "http://127.0.0.1:8090/fhir"

func requireHAPI(t *testing.T) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, hapiBase+"/metadata", nil)
	if err != nil {
		t.Fatal(err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Skip("HAPI FHIR is not running: docker run -d --name hapi -p 8090:8080 hapiproject/hapi:latest")
	}

	defer func() { _ = res.Body.Close() }()

	if res.StatusCode != http.StatusOK {
		t.Skipf("HAPI answered %d for its capability statement", res.StatusCode)
	}

	return hapiBase
}

// hl7ADT is a small but complete A01, with the fields a Patient resource is built from.
const hl7ADT = "MSH|^~\\&|PERFUSE|TEST|HAPI|TEST|20260917210000||ADT^A01|MSG00001|P|2.5\r" +
	"EVN|A01|20260917210000\r" +
	"PID|1||MRN00412^^^HOSP^MR||Hopper^Grace^B||19061209|F|||1 Navy Way^^Arlington^VA^22204^USA||^PRN^PH^^^703^5550142\r" +
	"PV1|1|I|ICU^101^A||||1234^Smith^John^A|||SUR||||ADM|A0\r"

func TestAPatientResourcePerfuseBuildsIsAcceptedByHAPI(t *testing.T) {
	// The assertion that matters: a real FHIR server accepts what the converter produces. Anything else here is detail.
	base := requireHAPI(t)

	sender, err := NewFHIRSender(config.Destination{
		Name: "hapi",
		Type: config.DestinationFHIR,
		FHIR: &config.FHIRDestination{
			URL:                     base,
			Version:                 "R4",
			DefaultIdentifierSystem: "http://hospital.test/mrn",
			IdentifierSystems:       map[string]string{"HOSP": "http://hospital.test/mrn"},
		},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = sender.Close() }()

	if err := sender.Send(context.Background(), []byte(hl7ADT)); err != nil {
		// HAPI's rejection carries the reason in FHIR's own terms, and it is the useful half of any failure here.
		t.Fatalf("HAPI refused what the converter produced: %v", err)
	}
}

func TestTheResourceHAPIStoredHasThePatientWeSent(t *testing.T) {
	// Acceptance is not enough. A server can accept a resource that has lost the fields somebody cared about - a Patient with no name
	// is valid FHIR - so this reads the resource back out of HAPI and checks the identity survived the round trip.
	base := requireHAPI(t)

	sender, err := NewFHIRSender(config.Destination{
		Name: "hapi",
		Type: config.DestinationFHIR,
		FHIR: &config.FHIRDestination{
			URL:                     base,
			Version:                 "R4",
			DefaultIdentifierSystem: "http://hospital.test/mrn",
			IdentifierSystems:       map[string]string{"HOSP": "http://hospital.test/mrn"},
		},
	}, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = sender.Close() }()

	if err := sender.Send(context.Background(), []byte(hl7ADT)); err != nil {
		t.Fatalf("HAPI refused the resource: %v", err)
	}

	// Searched by the identifier the message carried, which is the only handle a receiving system would have.
	found := searchHAPI(t, base, "/Patient?identifier=http://hospital.test/mrn|MRN00412")

	total, _ := found["total"].(float64)
	if total < 1 {
		t.Fatalf("HAPI accepted the resource and cannot find it by the identifier that was sent: %v", found["total"])
	}

	entries, _ := found["entry"].([]any)
	if len(entries) == 0 {
		t.Fatal("no entries in the search result")
	}

	first, _ := entries[0].(map[string]any)
	resource, _ := first["resource"].(map[string]any)

	// The name, because a Patient without one is valid FHIR and useless clinically - exactly the kind of loss that acceptance alone
	// would not reveal.
	names, _ := resource["name"].([]any)
	if len(names) == 0 {
		t.Error("the stored Patient has no name")
	} else {
		name, _ := names[0].(map[string]any)
		if family, _ := name["family"].(string); family != "Hopper" {
			t.Errorf("family name is %q, want Hopper", family)
		}
	}

	// The birth date, which is the field most likely to be mangled by a format difference: HL7 writes 19061209 and FHIR wants
	// 1906-12-09, and a converter that passed the digits through unchanged would be refused by a strict server and accepted by a
	// lenient one.
	if bd, _ := resource["birthDate"].(string); bd != "1906-12-09" {
		t.Errorf("birthDate is %q, want 1906-12-09", bd)
	}

	if gender, _ := resource["gender"].(string); gender != "female" {
		// HL7 sends F. FHIR's value set is female, and a server that validates value sets refuses anything else.
		t.Errorf("gender is %q, want female", gender)
	}
}

func TestHAPIRefusesAResourceThatIsNotValidFHIR(t *testing.T) {
	// The positive control for the two tests above, and it is worth as much as they are.
	//
	// Without it a HAPI that had validation switched off would accept anything, and both tests would pass while proving that a server
	// exists rather than that the resource is right.
	base := requireHAPI(t)

	body := `{"resourceType":"Patient","gender":"definitely-not-a-fhir-gender","birthDate":"the ninth of December"}`

	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, base+"/Patient", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Content-Type", "application/fhir+json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	if res.StatusCode < 400 {
		payload, _ := io.ReadAll(res.Body)
		t.Fatalf("HAPI accepted an invalid Patient with status %d, so it is not validating and the tests beside this prove little: %s",
			res.StatusCode, summarise(payload))
	}
}

func searchHAPI(t *testing.T, base, path string) map[string]any {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, base+path, nil)
	if err != nil {
		t.Fatal(err)
	}

	req.Header.Set("Accept", "application/fhir+json")

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = res.Body.Close() }()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	if res.StatusCode != http.StatusOK {
		t.Fatalf("searching HAPI returned %d: %s", res.StatusCode, summarise(payload))
	}

	var out map[string]any
	if err := json.Unmarshal(payload, &out); err != nil {
		t.Fatalf("HAPI's answer is not JSON: %v", err)
	}

	return out
}

func summarise(payload []byte) string {
	text := string(payload)
	if len(text) > 400 {
		return text[:400] + "…"
	}

	return fmt.Sprint(text)
}
