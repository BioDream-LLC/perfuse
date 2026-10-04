// Package crd is a Da Vinci Coverage Requirements Discovery service (CRD 2.2.1): the payer's side of the CDS Hooks conversation that
// tells a clinician, while the order is still a draft, whether it is covered, whether it needs prior authorisation, and what
// documentation the payer will want.
//
// CMS-0057's Prior Authorization API is in practice three Da Vinci guides together: CRD to say what is required, DTR to gather it, PAS
// to submit it. Perfuse had PAS. This is the first of the other two; DTR's $questionnaire-package is on the FHIR endpoint.
//
// The coverage decisions come from a rules file, because they are the payer's policy and change without software releases: a code (or
// a list of codes) and what applies to it - covered or not, prior authorisation needed or not, documentation, and the DTR questionnaire
// that collects it. An order matching no rule is answered "conditional" and "indeterminate", which is what CRD says to return when the
// service cannot decide - never a guess that it is covered.
//
// Responses follow the guide: a systemAction updating each order with the coverage-information extension, which the EHR stores on the
// order and later sends on with the prior authorisation; and a card per order, so a clinician sees it.
package crd

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
)

// ExtCoverageInformation is the CRD extension carrying a coverage decision on an order.
const ExtCoverageInformation = "http://hl7.org/fhir/us/davinci-crd/StructureDefinition/ext-coverage-information"

// Version is the CRD guide this implements.
const Version = "2.2.1"

// Rule is one coverage policy.
type Rule struct {
	// Codes are the procedure, device or service codes it applies to: CPT, HCPCS, SNOMED.
	Codes []string `yaml:"codes" json:"codes"`
	// System, when set, limits the match to codings in that system.
	System string `yaml:"system,omitempty" json:"system,omitempty"`
	// Description names the policy, for the card a clinician reads.
	Description string `yaml:"description" json:"description"`
	// Covered is covered, not-covered or conditional.
	Covered string `yaml:"covered" json:"covered"`
	// PA is no-auth, auth-needed, satisfied, performpa or conditional.
	PA string `yaml:"pa,omitempty" json:"pa,omitempty"`
	// Documentation is what must be gathered: clinical, admin, patient, conditional.
	Documentation []string `yaml:"documentation,omitempty" json:"documentation,omitempty"`
	// Questionnaire is the canonical URL of the DTR questionnaire that gathers it.
	Questionnaire string `yaml:"questionnaire,omitempty" json:"questionnaire,omitempty"`
	// Reason is shown to the clinician.
	Reason string `yaml:"reason,omitempty" json:"reason,omitempty"`
	// Links are policy documents.
	Links []Link `yaml:"links,omitempty" json:"links,omitempty"`
}

// Link is a card link.
type Link struct {
	Label string `yaml:"label" json:"label"`
	URL   string `yaml:"url" json:"url"`
	Type  string `yaml:"type,omitempty" json:"type"`
}

// Rules is the rules file.
type Rules struct {
	// Payer names who is answering, on every card.
	Payer string `yaml:"payer" json:"payer"`
	// URL is the payer's site, on every card's source.
	URL   string `yaml:"url,omitempty" json:"url,omitempty"`
	Rules []Rule `yaml:"rules" json:"rules"`

	// The coverage assertions made, by id, for DTR's context parameter.
	mu         sync.Mutex
	assertions map[string]string
	order      []string
}

var (
	coveredCodes = map[string]bool{"covered": true, "not-covered": true, "conditional": true}
	paCodes      = map[string]bool{"": true, "no-auth": true, "auth-needed": true, "satisfied": true, "performpa": true, "conditional": true}
	docCodes     = map[string]bool{"clinical": true, "admin": true, "patient": true, "conditional": true}
)

// LoadRules reads and checks a rules file.
func LoadRules(path string) (*Rules, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var r Rules
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	dec.KnownFields(true)
	if err := dec.Decode(&r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &r, r.Validate()
}

// Validate checks every rule uses CRD's codes, so a typo is refused at start rather than sent to an EHR.
func (r *Rules) Validate() error {
	var problems []string
	if strings.TrimSpace(r.Payer) == "" {
		problems = append(problems, "payer is required: it names who is answering on every card")
	}
	for i, rule := range r.Rules {
		where := fmt.Sprintf("rule %d", i+1)
		if len(rule.Codes) == 0 {
			problems = append(problems, where+" has no codes")
		}
		if !coveredCodes[rule.Covered] {
			problems = append(problems, fmt.Sprintf("%s: covered %q is not covered, not-covered or conditional", where, rule.Covered))
		}
		if !paCodes[rule.PA] {
			problems = append(problems, fmt.Sprintf("%s: pa %q is not one of CRD's codes", where, rule.PA))
		}
		for _, d := range rule.Documentation {
			if !docCodes[d] {
				problems = append(problems, fmt.Sprintf("%s: documentation %q is not clinical, admin, patient or conditional", where, d))
			}
		}
		if len(rule.Documentation) > 0 && rule.Questionnaire == "" {
			problems = append(problems, where+" asks for documentation and names no questionnaire to gather it")
		}
		// CRD's own invariants on coverage-information, refused here rather than sent: the HL7 validator rejects a response
		// that breaks them, and a rules file is where each one is decided.
		if rule.Questionnaire != "" && len(rule.Documentation) == 0 {
			problems = append(problems, where+" names a questionnaire but no documentation; CRD allows a questionnaire only with doc-needed (crd-ci-q1)")
		}
		if rule.Covered == "not-covered" && rule.PA != "" {
			problems = append(problems, where+" is not-covered and also says pa; CRD forbids pa-needed on a service that is not covered (crd-ci-q2)")
		}
		if rule.PA == "satisfied" {
			problems = append(problems, where+": pa satisfied needs the authorisation's own id (crd-ci-q5), which belongs to one patient's case, not a rule")
		}
		conditional := rule.Covered == "conditional" || rule.PA == "conditional"
		for _, d := range rule.Documentation {
			conditional = conditional || d == "conditional"
		}
		if conditional && strings.TrimSpace(rule.Reason) == "" {
			problems = append(problems, where+" is conditional and gives no reason; CRD requires one to say what information is needed (crd-ci-q3, crd-ci-q6)")
		}
		if rule.PA == "auth-needed" && len(rule.Documentation) > 0 && strings.TrimSpace(rule.Reason) == "" {
			problems = append(problems, where+" needs documentation for prior authorization and gives no reason (crd-ci-q8)")
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// Hooks are the CDS Hooks this service answers.
var Hooks = []string{"order-sign", "order-select", "order-dispatch", "appointment-book"}

// Discovery is the CDS Hooks discovery document.
func (r *Rules) Discovery() map[string]any {
	var services []map[string]any
	for _, h := range Hooks {
		services = append(services, map[string]any{
			"hook":        h,
			"id":          "crd-" + h,
			"title":       r.Payer + " coverage requirements (" + h + ")",
			"description": "Da Vinci CRD " + Version + ": coverage, prior authorization and documentation requirements for the orders in context",
			"prefetch":    map[string]string{"coverage": "Coverage?patient={{context.patientId}}&status=active"},
			"extension":   map[string]any{"davinci-crd.configuration": []any{}},
		})
	}
	return map[string]any{"services": services}
}

// Request is a CDS Hooks service request.
type Request struct {
	Hook         string                     `json:"hook"`
	HookInstance string                     `json:"hookInstance"`
	FHIRServer   string                     `json:"fhirServer,omitempty"`
	Context      map[string]json.RawMessage `json:"context"`
	Prefetch     map[string]json.RawMessage `json:"prefetch,omitempty"`
}

// Response is a CDS Hooks response.
type Response struct {
	Cards         []Card   `json:"cards"`
	SystemActions []Action `json:"systemActions,omitempty"`
}

// Card is what a clinician sees.
type Card struct {
	UUID      string         `json:"uuid"`
	Summary   string         `json:"summary"`
	Detail    string         `json:"detail,omitempty"`
	Indicator string         `json:"indicator"`
	Source    map[string]any `json:"source"`
	Links     []Link         `json:"links,omitempty"`
	Extension map[string]any `json:"extension,omitempty"`
}

// Action is a systemAction: the EHR applies it without asking.
type Action struct {
	Type        string         `json:"type"`
	Description string         `json:"description,omitempty"`
	Resource    map[string]any `json:"resource"`
}

func newUUID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// orderTypes are the resources CRD answers about.
var orderTypes = map[string]string{"ServiceRequest": "code", "DeviceRequest": "codeCodeableConcept", "MedicationRequest": "medicationCodeableConcept",
	"NutritionOrder": "", "VisionPrescription": "", "CommunicationRequest": "", "Appointment": "serviceType"}

// ErrNoOrders means the request named nothing to answer about.
var ErrNoOrders = errors.New("the hook context carries no orders or appointments to answer about")

// Evaluate answers a hook.
func (r *Rules) Evaluate(req *Request, now time.Time) (*Response, error) {
	var bundleRaw json.RawMessage
	switch req.Hook {
	case "order-sign", "order-select":
		bundleRaw = req.Context["draftOrders"]
	case "appointment-book":
		bundleRaw = req.Context["appointments"]
	case "order-dispatch":
		// order-dispatch names the order rather than carrying it; the order arrives in prefetch.
		bundleRaw = req.Prefetch["order"]
	default:
		return nil, fmt.Errorf("the hook %q is not one this service answers", req.Hook)
	}
	orders := resourcesIn(bundleRaw)
	if len(orders) == 0 {
		return nil, ErrNoOrders
	}

	coverage := firstCoverage(req.Prefetch["coverage"])
	source := map[string]any{"label": r.Payer, "topic": map[string]string{
		"system": "http://terminology.hl7.org/CodeSystem/cdshooks-card-type", "code": "coverage-info", "display": "Coverage Information"}}
	if r.URL != "" {
		source["url"] = r.URL
	}

	resp := &Response{Cards: []Card{}}
	if coverage == "" {
		resp.Cards = append(resp.Cards, Card{UUID: newUUID(), Indicator: "warning", Source: source,
			Summary: "No active coverage was found for this patient",
			Detail:  "Coverage requirements depend on the plan. Send the patient's Coverage in the prefetch, or check eligibility first."})
		return resp, nil
	}

	for _, o := range orders {
		kind, _ := o["resourceType"].(string)
		if _, ok := orderTypes[kind]; !ok {
			continue
		}
		rule, matched := r.match(o)
		ext := r.coverageInformation(rule, matched, coverage, now)
		updated := cloneWith(o, ext)
		ref := kind
		if id, ok := o["id"].(string); ok && id != "" {
			ref += "/" + id
		}
		resp.SystemActions = append(resp.SystemActions, Action{Type: "update", Resource: updated,
			Description: "Record the coverage information on the order"})
		resp.Cards = append(resp.Cards, r.card(rule, matched, ref, source))
	}
	return resp, nil
}

func (r *Rules) match(o map[string]any) (Rule, bool) {
	for _, code := range codingsOf(o) {
		for _, rule := range r.Rules {
			if rule.System != "" && rule.System != code.system {
				continue
			}
			for _, c := range rule.Codes {
				if c == code.code {
					return rule, true
				}
			}
		}
	}
	return Rule{Covered: "conditional", PA: "conditional"}, false
}

type coding struct{ system, code string }

func codingsOf(o map[string]any) []coding {
	var out []coding
	collect := func(cc any) {
		m, _ := cc.(map[string]any)
		list, _ := m["coding"].([]any)
		for _, c := range list {
			cm, _ := c.(map[string]any)
			s, _ := cm["system"].(string)
			code, _ := cm["code"].(string)
			if code != "" {
				out = append(out, coding{s, code})
			}
		}
	}
	for _, field := range []string{"code", "codeCodeableConcept", "medicationCodeableConcept"} {
		collect(o[field])
	}
	if types, ok := o["serviceType"].([]any); ok {
		for _, t := range types {
			collect(t)
		}
	}
	return out
}

func (r *Rules) coverageInformation(rule Rule, matched bool, coverage string, now time.Time) map[string]any {
	ext := []any{
		map[string]any{"url": "coverage", "valueReference": map[string]any{"reference": coverage}},
		map[string]any{"url": "covered", "valueCode": rule.Covered},
	}
	pa := rule.PA
	if !matched {
		// Not "covered": a service that does not know says so. CRD's code for it is conditional, with the reason given.
		pa = "conditional"
	}
	if pa != "" {
		ext = append(ext, map[string]any{"url": "pa-needed", "valueCode": pa})
	}
	for _, d := range rule.Documentation {
		ext = append(ext, map[string]any{"url": "doc-needed", "valueCode": d})
	}
	// doc-purpose is not sent. "withpa" is the one value a rules file implies, but CRD 2.2.1's invariant crd-ci-q4 fails
	// every instance carrying it, whatever pa-needed is: its left side is a where() with no exists(), which is empty when
	// pa-needed is auth-needed, and "empty implies false" is not true. The element is optional, so leaving it out is
	// conformant; DTR derives the same purpose from pa-needed.
	if rule.Questionnaire != "" {
		ext = append(ext, map[string]any{"url": "questionnaire", "valueCanonical": rule.Questionnaire})
	}
	conditional := rule.Covered == "conditional" || pa == "conditional"
	for _, d := range rule.Documentation {
		conditional = conditional || d == "conditional"
	}
	reason := strings.TrimSpace(rule.Reason)
	if !matched {
		// crd-ci-q6: info-needed OTH must carry a reason. Without one, the HL7 validator rejected every answer for an order
		// the rules did not recognise.
		reason = "No coverage rule matches this order's codes, so " + r.Payer + " could not determine coverage or prior authorization from it."
	}
	if conditional {
		ext = append(ext, map[string]any{"url": "info-needed", "valueCode": "OTH"})
	}
	if reason != "" {
		ext = append(ext, map[string]any{"url": "reason", "valueCodeableConcept": map[string]any{"text": reason}})
	}
	id := newUUID()
	ext = append(ext,
		map[string]any{"url": "date", "valueDate": now.UTC().Format("2006-01-02")},
		map[string]any{"url": "coverage-assertion-id", "valueString": id},
	)
	if rule.Questionnaire != "" {
		r.remember(id, rule.Questionnaire)
	}
	return map[string]any{"url": ExtCoverageInformation, "extension": ext}
}

// maxAssertions bounds the coverage assertions remembered for DTR. Oldest go first.
const maxAssertions = 10000

// remember records which questionnaire a coverage assertion asked for, so DTR's $questionnaire-package can answer from
// the assertion id alone (its context parameter). Kept in memory: a restart forgets them, and the client can still name
// the questionnaire or send the order, which DTR also accepts.
func (r *Rules) remember(id, questionnaire string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.assertions == nil {
		r.assertions = map[string]string{}
	}
	r.assertions[id] = questionnaire
	r.order = append(r.order, id)
	for len(r.order) > maxAssertions {
		delete(r.assertions, r.order[0])
		r.order = r.order[1:]
	}
}

// QuestionnairesFor returns the questionnaires a coverage assertion this service made asked for, for DTR's context
// parameter.
func (r *Rules) QuestionnairesFor(assertionID string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if q, ok := r.assertions[assertionID]; ok {
		return []string{q}
	}
	return nil
}

func cloneWith(o map[string]any, ext map[string]any) map[string]any {
	raw, _ := json.Marshal(o)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	var exts []any
	if existing, ok := out["extension"].([]any); ok {
		for _, e := range existing {
			if m, ok := e.(map[string]any); ok && m["url"] == ExtCoverageInformation {
				continue // replaced, not stacked: one assertion per order from this payer
			}
			exts = append(exts, e)
		}
	}
	out["extension"] = append(exts, ext)
	return out
}

func (r *Rules) card(rule Rule, matched bool, ref string, source map[string]any) Card {
	c := Card{UUID: newUUID(), Source: source, Indicator: "info", Links: rule.Links,
		Extension: map[string]any{"davinci-crd.associated-resource": []any{map[string]string{"reference": ref}}}}
	name := rule.Description
	if name == "" {
		name = "This order"
	}
	switch {
	case !matched:
		c.Summary = "Coverage could not be determined automatically"
		c.Detail = r.Payer + " has no coverage rule for this order's code. Coverage and prior authorization are conditional on review."
	case rule.Covered == "not-covered":
		c.Indicator = "warning"
		c.Summary = name + ": not covered"
	case rule.PA == "auth-needed":
		c.Indicator = "warning"
		c.Summary = name + ": covered, prior authorization required"
	default:
		c.Summary = name + ": " + strings.ReplaceAll(rule.Covered, "-", " ") + map[bool]string{true: ", no prior authorization needed"}[rule.PA == "no-auth"]
	}
	if rule.Reason != "" {
		c.Detail = rule.Reason
	}
	if len(rule.Documentation) > 0 {
		c.Detail = strings.TrimSpace(c.Detail + " Documentation required (" + strings.Join(rule.Documentation, ", ") +
			"): complete the questionnaire " + rule.Questionnaire + ".")
	}
	for i := range c.Links {
		if c.Links[i].Type == "" {
			c.Links[i].Type = "absolute"
		}
	}
	return c
}

func resourcesIn(raw json.RawMessage) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var b struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource map[string]any `json:"resource"`
		} `json:"entry"`
	}
	if json.Unmarshal(raw, &b) != nil {
		return nil
	}
	if b.ResourceType != "Bundle" {
		var single map[string]any
		if json.Unmarshal(raw, &single) == nil && single["resourceType"] != nil {
			return []map[string]any{single}
		}
		return nil
	}
	var out []map[string]any
	for _, e := range b.Entry {
		if e.Resource != nil {
			out = append(out, e.Resource)
		}
	}
	return out
}

func firstCoverage(raw json.RawMessage) string {
	for _, r := range resourcesIn(raw) {
		if r["resourceType"] == "Coverage" {
			if id, ok := r["id"].(string); ok && id != "" {
				return "Coverage/" + id
			}
		}
	}
	return ""
}
