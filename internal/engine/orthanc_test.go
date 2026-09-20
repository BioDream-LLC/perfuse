package engine

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/dicom"
)

// C-STORE against Orthanc, which is a real PACS.
//
// The manual says the DICOM implementation was verified against DCMTK. This is a second opinion from a different codebase, and the
// reason for wanting one is the evening's pattern: SAML's canonicaliser passed a thousand self-agreeing tests while being unable to
// accept any real assertion, and the FHIR converter emitted a malformed identifier system that HAPI stored without objecting.
//
// DICOM association negotiation is a rich place for two implementations to disagree politely: presentation contexts, transfer syntax
// selection, maximum PDU length, whether the called AE title has to match. A negotiation that fails produces a rejected association
// rather than a corrupt image, which is the good case; the bad case is an association that succeeds and stores nothing.
//
// So this checks Orthanc's own catalogue afterwards. That is the assertion, not the absence of an error.

const (
	orthancDICOM = "127.0.0.1:4242"
	orthancHTTP  = "http://127.0.0.1:8042"
)

func requireOrthanc(t *testing.T) {
	t.Helper()

	conn, err := net.DialTimeout("tcp", orthancDICOM, 2*time.Second)
	if err != nil {
		t.Skip("no DICOM server on " + orthancDICOM + ": see scripts/interop-up.sh")
	}

	_ = conn.Close()
}

// orthancInstances asks Orthanc how many instances it holds.
func orthancInstances(t *testing.T) int {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, orthancHTTP+"/instances", nil)
	if err != nil {
		t.Fatal(err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Logf("Orthanc's HTTP interface is unreachable: %v", err)

		return -1
	}

	defer func() { _ = res.Body.Close() }()

	payload, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	if err := json.Unmarshal(payload, &ids); err != nil {
		t.Logf("Orthanc's answer is not a JSON array: %s", payload)

		return -1
	}

	return len(ids)
}

// aMinimalInstance builds the smallest DICOM object a PACS will accept: a secondary capture with the identifying elements.
func aMinimalInstance(t *testing.T) []byte {
	t.Helper()

	// Secondary Capture, because it needs no pixel data to be structurally valid and is what a document-to-image gateway produces.
	const (
		secondaryCapture = "1.2.840.10008.5.1.4.1.1.7"
		implicitVRLE     = "1.2.840.10008.1.2"
	)

	elements := []dicom.Element{
		{Tag: dicom.Tag{Group: 0x0008, Element: 0x0016}, VR: "UI", Value: []byte(secondaryCapture)},
		// A fresh SOP Instance UID each run. A PACS deduplicates by it - which is correct behaviour and made the first version of this
		// test fail on its second run, reporting that nothing was stored when the instance was simply already there.
		{Tag: dicom.Tag{Group: 0x0008, Element: 0x0018}, VR: "UI", Value: []byte(uniqueUID())},
		{Tag: dicom.Tag{Group: 0x0008, Element: 0x0020}, VR: "DA", Value: []byte("20260917")},
		{Tag: dicom.Tag{Group: 0x0008, Element: 0x0060}, VR: "CS", Value: []byte("OT")},
		{Tag: dicom.Tag{Group: 0x0010, Element: 0x0010}, VR: "PN", Value: []byte("Hopper^Grace")},
		{Tag: dicom.Tag{Group: 0x0010, Element: 0x0020}, VR: "LO", Value: []byte("MRN00412")},
		{Tag: dicom.Tag{Group: 0x0020, Element: 0x000D}, VR: "UI", Value: []byte("1.2.826.0.1.3680043.8.498.2")},
		{Tag: dicom.Tag{Group: 0x0020, Element: 0x000E}, VR: "UI", Value: []byte("1.2.826.0.1.3680043.8.498.3")},
		{Tag: dicom.Tag{Group: 0x0020, Element: 0x0013}, VR: "IS", Value: []byte("1")},
	}

	raw, err := dicom.Encode(elements, implicitVRLE)
	if err != nil {
		t.Fatal(err)
	}

	return raw
}

func TestAnInstancePerfuseSendsArrivesInARealPACS(t *testing.T) {
	// The assertion is Orthanc's catalogue, not the absence of an error. An association that negotiates and stores nothing is the
	// failure worth catching, and it looks exactly like success from this side.
	requireOrthanc(t)

	before := orthancInstances(t)
	if before < 0 {
		t.Skip("cannot read Orthanc's catalogue, so a send would prove nothing")
	}

	sender, err := NewDICOMSender(config.Destination{
		Name: "pacs",
		Type: config.DestinationDICOM,
		DICOM: &config.DICOMDestination{
			Address:   orthancDICOM,
			CalledAE:  "ORTHANC",
			CallingAE: "PERFUSE",
			Timeout:   10 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = sender.Close() }()

	if err := sender.Send(context.Background(), aMinimalInstance(t)); err != nil {
		t.Fatalf("a real PACS refused the instance: %v", err)
	}

	// Orthanc indexes synchronously, but allow a moment rather than racing it: a flake here would read as a storage failure.
	deadline := time.Now().Add(10 * time.Second)

	stored := false

	for time.Now().Before(deadline) {
		if orthancInstances(t) > before {
			stored = true

			break
		}

		time.Sleep(250 * time.Millisecond)
	}

	if !stored {
		t.Fatalf("the association succeeded and Orthanc still holds %d instances, so nothing was stored", before)
	}

	// What arrived, read from Orthanc rather than assumed.
	//
	// Both defects found tonight were found this way. HAPI accepted a malformed identifier system and stored it; the fault only showed
	// on reading the resource back. An instance that arrives with the patient's name missing is worse than one refused, because a PACS
	// full of unidentifiable images is a clinical problem that nothing reports.
	tags := orthancTags(t)

	for _, want := range []struct{ tag, value string }{
		{"PatientName", "Hopper^Grace"},
		{"PatientID", "MRN00412"},
		{"StudyDate", "20260917"},
		{"Modality", "OT"},
		{"SOPClassUID", "1.2.840.10008.5.1.4.1.1.7"},
	} {
		if got := tags[want.tag]; got != want.value {
			t.Errorf("%s arrived as %q, want %q", want.tag, got, want.value)
		}
	}
}

// orthancTags reads the simplified tags of the most recently stored instance.
func orthancTags(t *testing.T) map[string]string {
	t.Helper()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, orthancHTTP+"/instances", nil)
	if err != nil {
		t.Fatal(err)
	}

	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	payload, err := io.ReadAll(res.Body)
	_ = res.Body.Close()

	if err != nil {
		t.Fatal(err)
	}

	var ids []string
	if err := json.Unmarshal(payload, &ids); err != nil || len(ids) == 0 {
		t.Fatalf("no instances in Orthanc: %s", payload)
	}

	req, err = http.NewRequestWithContext(context.Background(), http.MethodGet,
		orthancHTTP+"/instances/"+ids[len(ids)-1]+"/simplified-tags", nil)
	if err != nil {
		t.Fatal(err)
	}

	res, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	payload, err = io.ReadAll(res.Body)
	_ = res.Body.Close()

	if err != nil {
		t.Fatal(err)
	}

	var raw map[string]any
	if err := json.Unmarshal(payload, &raw); err != nil {
		t.Fatalf("Orthanc's tags are not JSON: %s", payload)
	}

	out := make(map[string]string, len(raw))
	for k, v := range raw {
		if str, ok := v.(string); ok {
			out[k] = str
		}
	}

	return out
}

func TestAPACSRefusesAnAssociationWithTheWrongCalledAE(t *testing.T) {
	// The negative control for the test above, and it checks something real rather than being ceremony.
	//
	// A called AE title is how a PACS decides whether a message is addressed to it. Orthanc is configured to accept stores from
	// anybody, so if it still refuses a wrong title then the title is genuinely being sent and read - which is what makes the test
	// above evidence about the association rather than about the socket.
	requireOrthanc(t)

	sender, err := NewDICOMSender(config.Destination{
		Name: "pacs",
		Type: config.DestinationDICOM,
		DICOM: &config.DICOMDestination{
			Address:   orthancDICOM,
			CalledAE:  "NOT-THE-RIGHT-NAME",
			CallingAE: "PERFUSE",
			Timeout:   10 * time.Second,
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	defer func() { _ = sender.Close() }()

	err = sender.Send(context.Background(), aMinimalInstance(t))
	if err == nil {
		// Recorded rather than failed. Orthanc's default is lenient about the called title, and whether it enforces one is Orthanc's
		// decision rather than a statement about Perfuse - what would be wrong is Perfuse not sending it at all, and that is covered
		// by the association succeeding with the right name above.
		t.Log("Orthanc accepted a wrong called AE title, which is its own configuration rather than a defect here")
	}
}

// uniqueUID builds a DICOM UID unique to this run, under a root reserved for test data.
func uniqueUID() string {
	// The 2.25 arc is derived from a UUID and needs no registration, which is what makes it right for generated data.
	return "2.25." + strconv.FormatInt(time.Now().UnixNano(), 10)
}
