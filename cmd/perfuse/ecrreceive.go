package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/publichealth"
)

// agencyConfig is -ecr-agency's file: the public health agency a test receiver plays.
type agencyConfig struct {
	publichealth.Agency `yaml:",inline"`
	// RCTC is the trigger codes the agency decides with; Perfuse's built-in sample without it.
	RCTC string `yaml:"rctc,omitempty"`
	// Reply sends each Reportability Response to the eICR's source endpoint's $process-message, as AIMS does, besides
	// answering with it. ReplyBearerToken authorises that request; ${VAR} reads it from the environment.
	Reply            bool   `yaml:"reply,omitempty"`
	ReplyBearerToken string `yaml:"reply_bearer_token,omitempty"`
}

func loadAgency(path string) (*agencyConfig, *publichealth.TriggerSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	var c agencyConfig
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, nil, fmt.Errorf("%s: %w", path, err)
	}
	if missing := c.Missing(); len(missing) > 0 {
		return nil, nil, fmt.Errorf("%s: eCR requires the agency's %s", path, strings.Join(missing, ", "))
	}
	c.ReplyBearerToken = os.ExpandEnv(c.ReplyBearerToken)
	triggers := publichealth.BuiltinTriggers()
	if c.RCTC != "" {
		if triggers, err = publichealth.LoadTriggers(c.RCTC); err != nil {
			return nil, nil, err
		}
	}
	return &c, triggers, nil
}

// ecrMessages handles $process-message for eCR: Reportability Responses when receive is set (this is the hospital), and case
// reports when agency is (this plays the public health agency, for testing the exchange without one).
type ecrMessages struct {
	store    *fhirserver.Store
	receive  bool
	agency   *agencyConfig
	triggers *publichealth.TriggerSet
	client   *http.Client
	log      *slog.Logger
	now      func() time.Time
}

func (m *ecrMessages) handle(ctx context.Context, body []byte) (map[string]any, error) {
	event, err := publichealth.MessageEvent(body)
	if err != nil {
		return nil, &fhirserver.MessageError{Status: http.StatusBadRequest, Code: "invalid", Message: err.Error()}
	}
	switch {
	case event == publichealth.EventReportabilityResponse && m.receive:
		return nil, m.receiveRR(ctx, body)
	case event == publichealth.EventCaseReport && m.agency != nil:
		return m.answerCaseReport(ctx, body)
	}
	return nil, &fhirserver.MessageError{Status: http.StatusUnprocessableEntity, Code: "not-supported",
		Message: fmt.Sprintf("this server does not take %q messages", event)}
}

// receiveRR stores a Reportability Response under the id derived from the eICR it answers, so the response to a report sent
// from here is at DocumentReference/<id>, the address logged when the report went.
func (m *ecrMessages) receiveRR(ctx context.Context, body []byte) error {
	rr, err := publichealth.ParseRR(body)
	if err != nil {
		return &fhirserver.MessageError{Status: http.StatusBadRequest, Code: "invalid", Message: err.Error()}
	}
	if rr.EICR == "" {
		return &fhirserver.MessageError{Status: http.StatusBadRequest, Code: "invalid",
			Message: "the Reportability Response does not name the eICR it answers"}
	}
	id := publichealth.RRStorageID(rr.EICR)
	var conditions []string
	for _, c := range rr.Conditions {
		for _, d := range c.Determinations {
			conditions = append(conditions, fmt.Sprintf("%s: %s (%s)", c.Display, d.Display, d.Agency))
		}
	}
	verdict := "no condition reportable"
	if rr.Reportable() {
		verdict = "reportable"
	}
	description := fmt.Sprintf("Reportability Response to eICR %s: %s, %s", rr.EICR, rr.StatusDisplay, verdict)
	if len(conditions) > 0 {
		description += "; " + strings.Join(conditions, "; ")
	}
	if err := m.put(ctx, id, "88085-6", "Reportability Response", description, rr.Document); err != nil {
		return err
	}
	// What the agency decided, not the patient: the conditions and determinations are what an audit and a clinician need.
	m.log.Info("reportability response received", "eicr", rr.EICR, "stored", "DocumentReference/"+id, "status", rr.Status,
		"reportable", rr.Reportable(), "conditions", strings.Join(conditions, "; "))
	return nil
}

func (m *ecrMessages) answerCaseReport(ctx context.Context, body []byte) (map[string]any, error) {
	reply, rr, err := publichealth.BuildRR(body, m.triggers, m.agency.Agency, m.now())
	if err != nil {
		return nil, &fhirserver.MessageError{Status: http.StatusBadRequest, Code: "invalid", Message: err.Error()}
	}
	// The agency keeps the case report, as an agency would: the eICR document, under an id from its identifier.
	var msg map[string]any
	_ = json.Unmarshal(body, &msg)
	for _, e := range msg["entry"].([]any)[1:] {
		if r, ok := e.(map[string]any)["resource"].(map[string]any); ok && r["resourceType"] == "Bundle" && r["type"] == "document" {
			if err := m.put(ctx, fhir.DeterministicUUID("eicr-received", rr.EICR), "55751-2", "Initial Public Health Case Report",
				"eICR "+rr.EICR+" as received", r); err != nil {
				return nil, err
			}
			break
		}
	}
	m.log.Info("case report answered", "eicr", rr.EICR, "reportable", rr.Reportable(), "conditions", len(rr.Conditions))
	if m.agency.Reply {
		header := reply["entry"].([]any)[0].(map[string]any)["resource"].(map[string]any)
		to := header["destination"].([]any)[0].(map[string]any)["endpoint"].(string)
		raw, _ := json.Marshal(reply)
		go m.send(strings.TrimRight(to, "/")+"/$process-message", raw, rr.EICR)
	}
	return reply, nil
}

// send posts the RR to the sender after answering, as AIMS returns one later rather than on the eICR's connection.
func (m *ecrMessages) send(url string, body []byte, eicr string) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		m.log.Warn("reportability response not sent", "eicr", eicr, "error", err)
		return
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	if m.agency.ReplyBearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+m.agency.ReplyBearerToken)
	}
	resp, err := m.client.Do(req)
	if err != nil {
		m.log.Warn("reportability response not sent", "eicr", eicr, "to", url, "error", err)
		return
	}
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		m.log.Warn("reportability response refused", "eicr", eicr, "to", url, "status", resp.StatusCode)
		return
	}
	m.log.Info("reportability response sent", "eicr", eicr, "to", url)
}

// put keeps a document exactly as received, as a DocumentReference whose attachment is the document's JSON. A Bundle does not
// survive the typed store intact, and the agency's or the hospital's copy must be the bytes that were sent.
func (m *ecrMessages) put(ctx context.Context, id, loincType, title, description string, document map[string]any) error {
	raw, err := json.Marshal(document)
	if err != nil {
		return err
	}
	ref := &fhir.DocumentReference{
		Status:      "current",
		Type:        &fhir.CodeableConcept{Coding: []fhir.Coding{{System: "http://loinc.org", Code: loincType}}, Text: title},
		Date:        m.now().UTC().Format(time.RFC3339),
		Description: description,
		Content: []fhir.DocumentContent{{Attachment: &fhir.Attachment{ContentType: "application/fhir+json",
			Data: base64.StdEncoding.EncodeToString(raw), Title: title}}},
	}
	if idv, ok := document["identifier"].(map[string]any); ok {
		if v, _ := idv["value"].(string); v != "" {
			ref.Identifier = []fhir.Identifier{{System: "urn:ietf:rfc:3986", Value: v}}
		}
	}
	ref.SetResourceID(id)
	if _, err := m.store.Put(ctx, ref); err != nil {
		return fmt.Errorf("storing the document: %w", err)
	}
	return nil
}
