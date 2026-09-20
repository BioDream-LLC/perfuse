package tomirth

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/mirth"
)

// A channel authored in Perfuse, imported into a running Mirth.
//
// This is the only test here that means anything, for the reason the exporter it depends on was broken for a month: Mirth does not refuse
// a channel it cannot assemble. It stores one whose description has been replaced with "This channel is invalid. Verify all required
// extensions are loaded correctly" and whose destinations are gone, and returns success. Nine tests that compared this package's output
// against this repository's own parser all passed while the server threw every one of those documents away.
//
// Start Mirth with ./scripts/interop-up.sh. Skipped, not failed, when it is not there.

const mirthBase = "https://127.0.0.1:8443"

// theInvalidChannelSentence is Mirth's answer to a channel it could not assemble, and the thing worth asserting on.
const theInvalidChannelSentence = "This channel is invalid"

func mirthDo(t *testing.T, method, path string, body io.Reader) (int, string) {
	t.Helper()

	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}},
	}

	req, err := http.NewRequest(method, mirthBase+path, body)
	if err != nil {
		t.Fatalf("building the %s request: %v", method, err)
	}
	req.SetBasicAuth("admin", "admin")
	req.Header.Set("X-Requested-With", "perfuse-test")
	if body != nil {
		req.Header.Set("Content-Type", "application/xml")
	}

	res, err := client.Do(req)
	if err != nil {
		t.Skipf("no Mirth on %s (%v). Start it with ./scripts/interop-up.sh", mirthBase, err)
	}
	defer res.Body.Close()

	out, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	return res.StatusCode, string(out)
}

// importAndReadBack sends a document and returns what Mirth stored, which is not always what was sent.
func importAndReadBack(t *testing.T, ch *config.Channel) (stored string, notes []string) {
	t.Helper()

	res, err := Channel(ch)
	if err != nil {
		t.Fatalf("converting %q: %v", ch.Name, err)
	}

	var doc bytes.Buffer
	if err := mirth.Export(&doc, res.Channel); err != nil {
		t.Fatalf("exporting %q: %v", ch.Name, err)
	}

	id := res.Channel.ID
	mirthDo(t, http.MethodDelete, "/api/channels/"+id, nil)

	code, body := mirthDo(t, http.MethodPost, "/api/channels", bytes.NewReader(doc.Bytes()))
	if code != http.StatusOK && code != http.StatusNoContent && code != http.StatusCreated {
		t.Fatalf("Mirth refused the import with %d:\n%s\n\nWhat we sent:\n%s", code, body, doc.String())
	}

	code, stored = mirthDo(t, http.MethodGet, "/api/channels/"+id, nil)
	if code != http.StatusOK || strings.TrimSpace(stored) == "" {
		t.Fatalf("Mirth accepted the import and then had no channel %s (GET %d)", id, code)
	}

	t.Cleanup(func() { mirthDo(t, http.MethodDelete, "/api/channels/"+id, nil) })

	return stored, res.Notes
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "\n... truncated"
}

// anMLLPChannel is the shape most of a real estate is: listen on MLLP, send on MLLP.
func anMLLPChannel(name string) *config.Channel {
	return &config.Channel{
		Name:        name,
		Description: "Admissions from the ward system",
		Source: config.Source{
			Type:   config.SourceMLLP,
			Listen: ":6661",
		},
		Destinations: []config.Destination{
			{Name: "To the registry", Type: config.DestinationMLLP, Address: "registry.example.invalid:6662"},
		},
	}
}

func TestAPerfuseChannelImportsIntoRealMirth(t *testing.T) {
	stored, _ := importAndReadBack(t, anMLLPChannel("perfuse-export-mllp"))

	if strings.Contains(stored, theInvalidChannelSentence) {
		t.Errorf("Mirth stored the channel as invalid.\nWhat it kept:\n%s", truncate(stored, 2000))
	}

	// The transport names too, checked separately because Mirth does not validate them on import.
	//
	// Found by breaking it: with the source transport set to "TCP Transmitter", a name Mirth has never heard of, the channel was still
	// stored as valid. XStream resolves the properties class and evidently takes transportName on trust, so the mistake would surface
	// only when somebody tried to deploy. Asserting on it here is the difference between finding that now and finding it on a cutover.
	for _, want := range []string{"TCP Listener", "TCP Sender"} {
		if !strings.Contains(stored, want) {
			t.Errorf("the stored channel does not name the %q transport. Mirth accepts an unknown transport name without "+
				"complaint, so this has to be checked rather than assumed.\nStored:\n%s", want, truncate(stored, 2000))
		}
	}
}

func TestTheExportedChannelKeepsItsAddresses(t *testing.T) {
	ch := anMLLPChannel("perfuse-export-addresses")
	stored, _ := importAndReadBack(t, ch)

	// The addresses are the whole point. A channel that imports cleanly and listens on the wrong port is worse than one that fails, so
	// these are checked in what Mirth stored rather than in what we sent.
	for _, want := range []string{"6661", "registry.example.invalid", "6662"} {
		if !strings.Contains(stored, want) {
			t.Errorf("%q is not in the channel Mirth stored, so the address did not survive.\nStored:\n%s",
				want, truncate(stored, 2000))
		}
	}
}

func TestTheExportedChannelKeepsItsDestinationNames(t *testing.T) {
	ch := anMLLPChannel("perfuse-export-names")
	ch.Destinations = append(ch.Destinations, config.Destination{
		Name: "To the warehouse", Type: config.DestinationFile, Dir: "/var/spool/warehouse",
	})

	stored, _ := importAndReadBack(t, ch)

	// Destinations are the first thing Mirth discards when a connector is not what it expects, and it discards them all together.
	for _, want := range []string{"To the registry", "To the warehouse", "TCP Sender", "File Writer"} {
		if !strings.Contains(stored, want) {
			t.Errorf("%q is missing from the stored channel.\nStored:\n%s", want, truncate(stored, 2500))
		}
	}
}

// TestTheDescriptionCarriesTheWarning is the control for every absence assertion above.
//
// Those tests pass if Mirth stored nothing at all, or if the GET returned some other channel. This one requires a string we chose to come
// back, so the document is known to have arrived.
func TestTheDescriptionCarriesTheWarning(t *testing.T) {
	ch := anMLLPChannel("perfuse-export-description")
	ch.Description = fmt.Sprintf("marker %d", time.Now().UnixNano())

	stored, _ := importAndReadBack(t, ch)

	if !strings.Contains(stored, ch.Description) {
		t.Fatalf("the description did not survive, so the absence assertions in this file prove nothing.\nStored:\n%s",
			truncate(stored, 1500))
	}

	// And the warning, because somebody opening this channel in Mirth has to know the transformations did not come with it.
	if !strings.Contains(stored, "Exported from Perfuse") {
		t.Errorf("the exported channel does not say it came from Perfuse, so a reader has no reason to check it before starting it")
	}
}

func TestEveryConvertibleTransportImportsIntoRealMirth(t *testing.T) {
	// One channel per supported transport pair, because a template that is wrong for one connector is invisible while the tests only use
	// two. Each is imported on its own so a failure names the transport.
	cases := []struct {
		name string
		src  config.Source
		dst  config.Destination
	}{
		{"mllp-to-mllp", config.Source{Type: config.SourceMLLP, Listen: ":7001"},
			config.Destination{Name: "d", Type: config.DestinationMLLP, Address: "host.invalid:7002"}},
		{"http-to-http", config.Source{Type: config.SourceHTTP, Listen: ":7003"},
			config.Destination{Name: "d", Type: config.DestinationHTTP, Address: "http://host.invalid/x"}},
		{"file-to-file", config.Source{Type: config.SourceFile, Listen: "/var/spool/in"},
			config.Destination{Name: "d", Type: config.DestinationFile, Dir: "/var/spool/out"}},
		{"mllp-to-db", config.Source{Type: config.SourceMLLP, Listen: ":7005"},
			config.Destination{Name: "d", Type: config.DestinationDatabase}},
		{"mllp-to-smtp", config.Source{Type: config.SourceMLLP, Listen: ":7006"},
			config.Destination{Name: "d", Type: config.DestinationSMTP}},
		{"mllp-to-soap", config.Source{Type: config.SourceMLLP, Listen: ":7007"},
			config.Destination{Name: "d", Type: config.DestinationSOAP}},
		{"mllp-to-js", config.Source{Type: config.SourceMLLP, Listen: ":7008"},
			config.Destination{Name: "d", Type: config.DestinationJavaScript}},
		{"dicom-to-dicom", config.Source{Type: config.SourceDICOM, Listen: ":7009"},
			config.Destination{Name: "d", Type: config.DestinationDICOM, Address: "pacs.invalid:104"}},
		{"tcp-to-tcp", config.Source{Type: config.SourceTCP, Listen: ":7010"},
			config.Destination{Name: "d", Type: config.DestinationTCP, Address: "host.invalid:7011"}},
		{"db-to-mllp", config.Source{Type: config.SourceDatabase},
			config.Destination{Name: "d", Type: config.DestinationMLLP, Address: "host.invalid:7012"}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ch := &config.Channel{
				Name:         "perfuse-export-" + c.name,
				Source:       c.src,
				Destinations: []config.Destination{c.dst},
			}

			stored, _ := importAndReadBack(t, ch)

			if strings.Contains(stored, theInvalidChannelSentence) {
				t.Errorf("Mirth stored %s as invalid.\nStored:\n%s", c.name, truncate(stored, 1800))
			}
		})
	}
}

// TestAnUnsupportedTransportIsRefused guards the decision not to approximate.
func TestAnUnsupportedTransportIsRefused(t *testing.T) {
	ch := &config.Channel{
		Name:   "perfuse-export-s3",
		Source: config.Source{Type: config.SourceMLLP, Listen: ":7100"},
		Destinations: []config.Destination{
			{Name: "To the bucket", Type: config.DestinationS3},
		},
	}

	_, err := Channel(ch)
	if err == nil {
		t.Fatal("an S3 destination was converted to something. Mirth has no S3 connector, and a channel that imports cleanly while " +
			"delivering somewhere else is worse than a refused export")
	}
	if !strings.Contains(err.Error(), "To the bucket") {
		t.Errorf("the refusal does not name the destination, so a reader has to guess which one: %v", err)
	}
}

// TestAPropertyMirthDoesNotHaveIsRefused checks the guard that makes the templates safe.
func TestAPropertyMirthDoesNotHaveIsRefused(t *testing.T) {
	ch := anMLLPChannel("perfuse-export-guard")
	res, err := Channel(ch)
	if err != nil {
		t.Fatalf("converting: %v", err)
	}

	err = res.Channel.Source.SetRawProperty("listenerConnectorProperties.invented", "x")
	if err == nil {
		t.Fatal("setting a property Mirth's own template does not contain was allowed. That is the mistake that produces a channel " +
			"the server discards while reporting nothing")
	}
	if !strings.Contains(err.Error(), "invented") {
		t.Errorf("the error does not name the path: %v", err)
	}
}

// TestLossesAreReported checks that what cannot cross is said out loud.
func TestLossesAreReported(t *testing.T) {
	ch := anMLLPChannel("perfuse-export-losses")
	ch.Filter = "PID-3 != ''"
	ch.Group = "Admissions"

	_, notes := importAndReadBack(t, ch)

	if len(notes) == 0 {
		t.Fatal("a channel with a filter and a group converted without a single note, so an operator would not know either was lost")
	}

	joined := strings.Join(notes, "\n")
	for _, want := range []string{"filter", "group"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no note mentions the %s. Notes:\n%s", want, joined)
		}
	}
}
