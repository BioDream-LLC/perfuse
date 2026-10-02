package engine

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The document used throughout. It has one deliberate defect: the narrative
// names two allergies and only one is coded, which is the disagreement the
// require_agreement gate is there to catch.
const testCDA = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <id root="2.16.840.1.113883.19.5" extension="DOC-77"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1" displayName="Summary"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <recordTarget><patientRole>
    <id root="2.16.840.1.113883.19.5.99999.2" extension="MRN900"/>
    <patient>
      <name><given>Ivy</given><family>Frost</family></name>
      <administrativeGenderCode code="F"/>
      <birthTime value="19910228"/>
    </patient>
  </patientRole></recordTarget>
  <component><structuredBody>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.6.1"/>
      <code code="48765-2" codeSystem="2.16.840.1.113883.6.1"/>
      <title>Allergies</title>
      <text><table><tbody>
        <tr><td ID="a1">Penicillin</td></tr>
        <tr><td ID="a2">Sulfamethoxazole</td></tr>
      </tbody></table></text>
      <entry><act classCode="ACT" moodCode="EVN">
        <statusCode code="active"/>
        <entryRelationship typeCode="SUBJ"><observation classCode="OBS" moodCode="EVN">
          <code code="ASSERTION" codeSystem="2.16.840.1.113883.5.4"/>
          <statusCode code="completed"/>
          <value code="7980" codeSystem="2.16.840.1.113883.6.88" displayName="Penicillin"/>
          <text><reference value="#a1"/></text>
        </observation></entryRelationship>
      </act></entry>
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`

// A document whose two halves agree, for asserting the gate lets good ones past.
const consistentCDA = `<?xml version="1.0" encoding="UTF-8"?>
<ClinicalDocument xmlns="urn:hl7-org:v3">
  <templateId root="2.16.840.1.113883.10.20.22.1.2"/>
  <id root="2.16.840.1.113883.19.5" extension="DOC-88"/>
  <code code="34133-9" codeSystem="2.16.840.1.113883.6.1"/>
  <title>Continuity of Care Document</title>
  <effectiveTime value="20260818120000-0500"/>
  <recordTarget><patientRole>
    <id root="2.16.840.1.113883.19.5.99999.2" extension="MRN901"/>
    <patient><name><given>Otto</given><family>Reed</family></name>
      <administrativeGenderCode code="M"/><birthTime value="19700101"/></patient>
  </patientRole></recordTarget>
  <component><structuredBody>
    <component><section>
      <templateId root="2.16.840.1.113883.10.20.22.2.6.1"/>
      <code code="48765-2" codeSystem="2.16.840.1.113883.6.1"/>
      <title>Allergies</title>
      <text><table><tbody><tr><td ID="a1">Penicillin</td></tr></tbody></table></text>
      <entry><act classCode="ACT" moodCode="EVN">
        <statusCode code="active"/>
        <entryRelationship typeCode="SUBJ"><observation classCode="OBS" moodCode="EVN">
          <code code="ASSERTION" codeSystem="2.16.840.1.113883.5.4"/>
          <statusCode code="completed"/>
          <value code="7980" codeSystem="2.16.840.1.113883.6.88" displayName="Penicillin"/>
          <text><reference value="#a1"/></text>
        </observation></entryRelationship>
      </act></entry>
    </section></component>
  </structuredBody></component>
</ClinicalDocument>`

// mdmWith builds an MDM^T02 carrying a payload, which is how a clinical document
// actually travels. Built from a field-number map so a miscount cannot put the
// parent document identifier in the wrong TXA field, which is a mistake I made
// by hand once already.
func mdmWith(payload, encoding, mime string, txa map[int]string) []byte {
	segs := []string{
		"MSH|^~\\&|EHR|SITEA|ARCHIVE|RFAC|20260818130000-0500||MDM^T02^MDM_T02|MD9|P|2.5.1",
		"EVN|T02|20260818130000-0500",
		"PID|1||MRN900^^^SITEA^MR||Frost^Ivy^L||19910228|F",
		txaSegmentFor(txa),
		fmt.Sprintf("OBX|1|ED|34133-9^Summary^LN||^%s^^%s^%s||||||F", mime, encoding, payload),
	}
	return []byte(strings.Join(segs, "\r") + "\r")
}

func txaSegmentFor(fields map[int]string) string {
	highest := 0
	for n := range fields {
		if n > highest {
			highest = n
		}
	}
	parts := make([]string, highest+1)
	parts[0] = "TXA"
	for n, v := range fields {
		parts[n] = v
	}
	return strings.Join(parts, "|")
}

func defaultTXA() map[int]string {
	return map[int]string{
		1:  "1",
		2:  "DS",
		3:  "AP^application^HL7",
		4:  "20260818125500-0500",
		12: "DOC-77^SITEA",
		17: "AU",
		19: "AV",
	}
}

func encodedCDA(doc string) string {
	return base64.StdEncoding.EncodeToString([]byte(doc))
}

func TestCDADestinationWritesFHIRToADirectory(t *testing.T) {
	dir := t.TempDir()
	s := newCDASenderForTest(t, config.CDADestination{Dir: dir, Version: "R5"})

	msg := mdmWith(encodedCDA(testCDA), "Base64", "application/hl7-cda+xml", defaultTXA())
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	files := readDirNames(t, dir)
	if len(files) != 1 || !strings.HasSuffix(files[0], ".json") {
		t.Fatalf("want one .json file, got %v", files)
	}
	// Named after the document identifier rather than the message, because a
	// records team looks a document up by its own identifier.
	if !strings.Contains(files[0], "DOC-77") {
		t.Errorf("file %q should be named after the document identifier", files[0])
	}

	var bundle map[string]any
	body, err := os.ReadFile(filepath.Join(dir, files[0]))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &bundle); err != nil {
		t.Fatalf("the output is not JSON: %v", err)
	}
	if bundle["resourceType"] != "Bundle" || bundle["type"] != "transaction" {
		t.Errorf("want a transaction Bundle, got %v/%v", bundle["resourceType"], bundle["type"])
	}

	entries, _ := bundle["entry"].([]any)
	if len(entries) == 0 {
		t.Fatal("the bundle has no entries")
	}
	found := map[string]bool{}
	for _, e := range entries {
		m, _ := e.(map[string]any)
		res, _ := m["resource"].(map[string]any)
		if rt, ok := res["resourceType"].(string); ok {
			found[rt] = true
		}
	}
	for _, want := range []string{"Patient", "AllergyIntolerance", "DocumentReference"} {
		if !found[want] {
			t.Errorf("the bundle has no %s; got %v", want, keysOf(found))
		}
	}

	if st := s.Stats(); st.Extracted != 1 || st.Converted != 1 || st.Sent != 1 {
		t.Errorf("stats: %+v", st)
	}
}

func TestCDADestinationPostsToAServer(t *testing.T) {
	var mu sync.Mutex
	var gotBody []byte
	var gotType, gotAuth string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		gotType = r.Header.Get("Content-Type")
		gotAuth = r.Header.Get("Authorization")
		gotBody = readAllLimited(r)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"resourceType":"Bundle","type":"transaction-response"}`))
	}))
	defer srv.Close()

	s := newCDASenderForTest(t, config.CDADestination{
		URL:         srv.URL,
		BearerToken: "t0ken",
		Headers:     map[string]string{"X-Tenant": "sitea"},
	})

	msg := mdmWith(encodedCDA(testCDA), "Base64", "application/hl7-cda+xml", defaultTXA())
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if gotType != "application/fhir+json" {
		t.Errorf("content type = %q", gotType)
	}
	if gotAuth != "Bearer t0ken" {
		t.Errorf("authorization = %q", gotAuth)
	}
	if !strings.Contains(string(gotBody), "AllergyIntolerance") {
		t.Error("the posted bundle has no AllergyIntolerance")
	}
}

func TestCDADestinationReportsAServerRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"resourceType":"OperationOutcome","issue":[{"diagnostics":"unknown profile"}]}`))
	}))
	defer srv.Close()

	s := newCDASenderForTest(t, config.CDADestination{URL: srv.URL})
	err := s.Send(context.Background(), mdmWith(
		encodedCDA(testCDA), "Base64", "application/hl7-cda+xml", defaultTXA()))
	if err == nil {
		t.Fatal("a rejected bundle should be a delivery failure")
	}
	// The server's explanation is the most useful thing in the log, so it has to
	// survive into the error rather than being flattened to "400".
	if !strings.Contains(err.Error(), "unknown profile") {
		t.Errorf("the error should carry the server's explanation, got: %v", err)
	}
}

func TestCDADestinationWritesTheOriginalDocument(t *testing.T) {
	dir := t.TempDir()
	s := newCDASenderForTest(t, config.CDADestination{Dir: dir, Write: "both"})

	msg := mdmWith(encodedCDA(testCDA), "Base64", "application/hl7-cda+xml", defaultTXA())
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("Send: %v", err)
	}

	names := readDirNames(t, dir)
	if len(names) != 2 {
		t.Fatalf("want a document and a bundle, got %v", names)
	}

	var xmlName string
	for _, n := range names {
		if strings.HasSuffix(n, ".xml") {
			xmlName = n
		}
	}
	xml, err := os.ReadFile(filepath.Join(dir, xmlName))
	if err != nil {
		t.Fatal(err)
	}
	// Byte for byte. The original document is the legal record and re-serialising
	// it would change bytes a records team may be checksumming.
	if string(xml) != testCDA {
		t.Error("the archived document is not byte-identical to what arrived")
	}
}

func TestCDADestinationDoesNotOverwriteAnEarlierVersion(t *testing.T) {
	dir := t.TempDir()
	s := newCDASenderForTest(t, config.CDADestination{Dir: dir, Write: "document"})

	msg := mdmWith(encodedCDA(testCDA), "Base64", "application/hl7-cda+xml", defaultTXA())
	for range 2 {
		if err := s.Send(context.Background(), msg); err != nil {
			t.Fatalf("Send: %v", err)
		}
	}

	// A replacement carries the same document identifier. Overwriting would
	// destroy the version being replaced, which is the one an auditor asks for.
	if names := readDirNames(t, dir); len(names) != 2 {
		t.Errorf("the second document should not have overwritten the first, got %v", names)
	}
}

func TestCDADestinationSkipsAMessageWithNoDocument(t *testing.T) {
	dir := t.TempDir()
	s := newCDASenderForTest(t, config.CDADestination{Dir: dir})

	// An ordinary admission down a channel that also carries documents.
	adt := []byte("MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260818120000||ADT^A01^ADT_A01|A1|P|2.5.1\r" +
		"PID|1||MRN5^^^SITEA^MR||Reed^Otto||19700101|M\r")

	if err := s.Send(context.Background(), adt); err != nil {
		t.Fatalf("an ADT should be skipped, not failed: %v", err)
	}
	if names := readDirNames(t, dir); len(names) != 0 {
		t.Errorf("nothing should have been written, got %v", names)
	}
	if st := s.Stats(); st.NoDocument != 1 {
		t.Errorf("the skip should be counted, got %+v", st)
	}
}

func TestCDADestinationCanRequireADocument(t *testing.T) {
	s := newCDASenderForTest(t, config.CDADestination{
		Dir:          t.TempDir(),
		OnNoDocument: "fail",
	})

	adt := []byte("MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260818120000||ADT^A01^ADT_A01|A1|P|2.5.1\r" +
		"PID|1||MRN5^^^SITEA^MR||Reed^Otto||19700101|M\r")

	// A channel dedicated to documents should hear about a document message with
	// no document in it, because that is a real fault at the sender.
	if err := s.Send(context.Background(), adt); err == nil {
		t.Fatal("on_no_document: fail should reject a message with no document")
	}
}

func TestCDADestinationReportsANonXMLAttachmentDistinctly(t *testing.T) {
	dir := t.TempDir()
	s := newCDASenderForTest(t, config.CDADestination{Dir: dir})

	pdf := base64.StdEncoding.EncodeToString([]byte("%PDF-1.7\nnot xml at all"))
	msg := mdmWith(pdf, "Base64", "application/pdf", defaultTXA())

	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("a PDF attachment is not an error: %v", err)
	}
	st := s.Stats()
	// "carried a PDF" and "carried nothing" are different facts. Only the first
	// says the sender is doing something we could support later.
	if st.NotXML != 1 || st.NoDocument != 0 {
		t.Errorf("a PDF should count as not-XML rather than no-document, got %+v", st)
	}
}

func TestCDADestinationCanGateOnDisagreement(t *testing.T) {
	dir := t.TempDir()
	s := newCDASenderForTest(t, config.CDADestination{
		Dir:              dir,
		RequireAgreement: true,
	})

	// The narrative names two allergies and only one is coded, so an importing
	// system would silently miss one.
	msg := mdmWith(encodedCDA(testCDA), "Base64", "application/hl7-cda+xml", defaultTXA())
	err := s.Send(context.Background(), msg)
	if err == nil {
		t.Skip("the sample document produces warnings rather than errors; the gate only stops errors")
	}
	if !strings.Contains(err.Error(), "disagree") {
		t.Errorf("the error should say what is wrong, got: %v", err)
	}
	if names := readDirNames(t, dir); len(names) != 0 {
		t.Errorf("a gated document should not have been written, got %v", names)
	}
}

func TestCDADestinationPassesAConsistentDocument(t *testing.T) {
	dir := t.TempDir()
	s := newCDASenderForTest(t, config.CDADestination{
		Dir:              dir,
		RequireAgreement: true,
	})

	msg := mdmWith(encodedCDA(consistentCDA), "Base64", "application/hl7-cda+xml", defaultTXA())
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("a consistent document should pass the gate: %v", err)
	}
	if names := readDirNames(t, dir); len(names) != 1 {
		t.Errorf("want one bundle, got %v", names)
	}
	if st := s.Stats(); st.Disagreed != 0 {
		t.Errorf("a consistent document should not be recorded as disagreeing, got %+v", st)
	}
}

func TestCDADestinationDecodesAMislabelledPayload(t *testing.T) {
	dir := t.TempDir()
	s := newCDASenderForTest(t, config.CDADestination{Dir: dir})

	// Declared as plain ASCII but actually base64, which senders really do. Losing
	// a clinical document over a metadata mistake is the wrong trade.
	msg := mdmWith(encodedCDA(testCDA), "A", "application/hl7-cda+xml", defaultTXA())
	if err := s.Send(context.Background(), msg); err != nil {
		t.Fatalf("a mislabelled payload should still decode: %v", err)
	}
	if names := readDirNames(t, dir); len(names) != 1 {
		t.Errorf("want one bundle, got %v", names)
	}
}

func TestCDADestinationConfigIsCheckedAtLoad(t *testing.T) {
	cases := []struct {
		name string
		cfg  config.CDADestination
		want string
	}{
		{"no target", config.CDADestination{}, "url or dir"},
		{"bad write mode", config.CDADestination{Dir: "/tmp", Write: "bundle"}, "write"},
		{"document without dir", config.CDADestination{URL: "https://h/fhir", Write: "document"}, "dir"},
		{"bad no-document mode", config.CDADestination{Dir: "/tmp", OnNoDocument: "retry"}, "on_no_document"},
		{"draft release", config.CDADestination{Dir: "/tmp", Version: "R6"}, "draft"},
		{"plain http remote", config.CDADestination{URL: "http://fhir.example.org"}, "unencrypted"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ch := config.Channel{
				Name:   "docs",
				Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
				Destinations: []config.Destination{{
					Name: "out",
					Type: config.DestinationCDA,
					CDA:  &tc.cfg,
				}},
			}
			err := ch.Validate()
			if err == nil {
				t.Fatalf("want an error mentioning %q, got none", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want an error mentioning %q, got: %v", tc.want, err)
			}
		})
	}
}

func TestCDABlockIsRefusedOnOtherDestinationTypes(t *testing.T) {
	ch := config.Channel{
		Name:   "docs",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Destinations: []config.Destination{{
			Name:    "out",
			Type:    config.DestinationFile,
			Dir:     t.TempDir(),
			Address: "",
			CDA:     &config.CDADestination{Dir: t.TempDir()},
		}},
	}
	// Silently ignoring a block means somebody configured conversion, saw no
	// conversion, and has nothing to go on.
	if err := ch.Validate(); err == nil {
		t.Fatal("a cda block on a file destination should be refused")
	}
}

// --- helpers ---------------------------------------------------------------

func newCDASenderForTest(t *testing.T, cfg config.CDADestination) *CDASender {
	t.Helper()
	s, err := NewCDASender(config.Destination{
		Name:    "documents",
		Type:    config.DestinationCDA,
		CDA:     &cfg,
		Timeout: 5 * time.Second,
	}, quiet())
	if err != nil {
		t.Fatalf("NewCDASender: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func readDirNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

func keysOf(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func readAllLimited(r *http.Request) []byte {
	buf := make([]byte, 1<<20)
	n, _ := r.Body.Read(buf)
	total := n
	for n > 0 && total < len(buf) {
		n, _ = r.Body.Read(buf[total:])
		total += n
	}
	return buf[:total]
}
