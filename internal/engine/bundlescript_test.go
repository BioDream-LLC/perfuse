package engine

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/script"
)

// A FHIR destination's bundle_script edits each converted Bundle before it is validated and sent: here it adds what a site's
// own systems know (an organisation identifier) and what the v2 message cannot say. A script that throws fails the message.
func TestABundleScriptEditsTheConvertedBundleBeforeItIsSent(t *testing.T) {
	var mu sync.Mutex
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		got = string(b)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/fhir+json")
		_, _ = w.Write([]byte(`{"resourceType":"Bundle","type":"transaction-response"}`))
	}))
	defer srv.Close()

	sender, err := NewFHIRSender(config.Destination{Name: "fhir", Type: config.DestinationFHIR,
		FHIR: &config.FHIRDestination{URL: srv.URL + "/fhir", Version: "4.0.1", DefaultIdentifierSystem: "http://hospital.test/mrn"}},
		slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	eng := script.New(script.Options{Timeout: 2 * time.Second})
	src := `bundle.entry.forEach(function (e) {
		if (e.resource.resourceType === "Organization") {
			e.resource.identifier = [{system: "http://fhir.de/sid/arge-ik/iknr", value: "260326822"}];
		}
		if (e.resource.resourceType === "Patient") { e.resource.extension = (e.resource.extension || []).concat(
			[{url: "http://hospital.test/fhir/flag", valueString: "kept"}]); }
	});`
	if err := sender.SetBundleScript(eng, "test bundle", src); err != nil {
		t.Fatal(err)
	}
	adt := "MSH|^~\\&|EHR|HOSP|FHIR|HOSP|20261024120000||ADT^A01^ADT_A01|C1|P|2.5\rEVN|A01\r" +
		"PID|1||4711^^^HOSP^MR||Musterfrau^Erika||19720811|F\rPV1|1|I|STN^1^1^HOSP||||||||||||||||V1^^^HOSP^VN\r"
	if err := sender.Send(context.Background(), []byte(adt)); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	body := got
	mu.Unlock()
	for _, want := range []string{`"system":"http://fhir.de/sid/arge-ik/iknr"`, `"valueString":"kept"`, `"resourceType":"Encounter"`} {
		if !strings.Contains(body, want) {
			t.Errorf("the posted Bundle lacks %s:\n%s", want, body)
		}
	}

	if err := sender.SetBundleScript(eng, "throws", `throw new Error("no IK number configured");`); err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), []byte(adt)); err == nil || !strings.Contains(err.Error(), "no IK number configured") {
		t.Errorf("a script that throws should fail the message: %v", err)
	}
}

// A channel file's bundle_script reaches the FHIR destination: the channel gets a script engine for it, as for a response
// transformer, and a script that does not compile is refused when the channel is built.
func TestABundleScriptInAChannelFileIsCompiledForItsDestination(t *testing.T) {
	yaml := func(script string) string {
		return `name: adt-to-fhir
source: {type: mllp, listen: "127.0.0.1:0"}
destinations:
  - name: fhir
    type: fhir
    fhir:
      url: https://fhir.example.test/fhir
      version: "4.0.1"
      default_identifier_system: http://hospital.test/mrn
      bundle_script: |
        ` + script + `
`
	}
	cfg, err := config.Load(strings.NewReader(yaml(`bundle.type = "transaction";`)), "adt.yaml")
	if err != nil {
		t.Fatal(err)
	}
	ch, err := NewChannel(cfg, DefaultSenderFactory, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer ch.closeSenders()
	if fs, ok := ch.dests[0].sender.(*FHIRSender); !ok || fs.bundleScript == nil {
		t.Fatalf("the FHIR destination has no bundle script: %T", ch.dests[0].sender)
	}
	cfg, err = config.Load(strings.NewReader(yaml(`bundle.entry.forEach(function (e) {`)), "adt.yaml")
	if err == nil {
		if _, err = NewChannel(cfg, DefaultSenderFactory, quiet()); err == nil {
			t.Fatal("a bundle script that does not compile should be refused")
		}
	}
}
