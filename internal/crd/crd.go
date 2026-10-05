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
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"slices"
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

	// DocPurpose says what the documentation is for: withclaim, withorder, retain-doc or OTH. withpa is refused: CRD 2.2.1's
	// invariant crd-ci-q4 fails every coverage-information carrying it, so it could never validate.
	DocPurpose []string `yaml:"doc_purpose,omitempty" json:"docPurpose,omitempty"`
	// BillingCodes are the codes to bill this under, when they differ from the order's.
	BillingCodes []Code `yaml:"billing_codes,omitempty" json:"billingCodes,omitempty"`
	// Details are CRD's name-value details: limits (allowed-quantity, allowed-period), costs (in-network-copay), and so on.
	Details []Detail `yaml:"details,omitempty" json:"details,omitempty"`
	// Contact is who to ask at the payer about this assertion.
	Contact *Contact `yaml:"contact,omitempty" json:"contact,omitempty"`
	// ExpiryDays is how long the assertion holds, from the day it is made.
	ExpiryDays int `yaml:"expiry_days,omitempty" json:"expiryDays,omitempty"`
	// DependsOn are codes of other orders this answer depends on: when one is in the same request, it is named as a
	// dependency, so the EHR knows to ask again if that order changes.
	DependsOn []string `yaml:"depends_on,omitempty" json:"dependsOn,omitempty"`

	// satisfiedPAID is set per answer, never from the file: the prior authorization already approved for this patient.
	satisfiedPAID string
}

// Code is a coding in a rules file.
type Code struct {
	System  string `yaml:"system" json:"system"`
	Code    string `yaml:"code" json:"code"`
	Display string `yaml:"display,omitempty" json:"display,omitempty"`
}

// Detail is one of CRD's coverage details.
type Detail struct {
	// Code is from HL7's crd-coverage-detail code system: allowed-quantity, allowed-period, in-network-copay,
	// out-network-copay, concurrent-review, appropriate-use-needed, policy-link.
	Code string `yaml:"code" json:"code"`
	// Value is what the detail says, as text, or true/false.
	Value string `yaml:"value" json:"value"`
	// Qualification adds a condition on it.
	Qualification string `yaml:"qualification,omitempty" json:"qualification,omitempty"`
}

// Contact is a payer contact for an assertion.
type Contact struct {
	Name  string `yaml:"name,omitempty" json:"name,omitempty"`
	Phone string `yaml:"phone,omitempty" json:"phone,omitempty"`
	Email string `yaml:"email,omitempty" json:"email,omitempty"`
	URL   string `yaml:"url,omitempty" json:"url,omitempty"`
}

// detailCategory is the category CRD expects for each detail code.
var detailCategory = map[string]string{"allowed-quantity": "cat-limitation", "allowed-period": "cat-limitation",
	"in-network-copay": "cat-decisional", "out-network-copay": "cat-decisional", "concurrent-review": "cat-decisional",
	"appropriate-use-needed": "cat-decisional", "policy-link": "cat-other"}

var docPurposes = map[string]bool{"withclaim": true, "withorder": true, "retain-doc": true, "OTH": true}

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
	// Members says where the payer's own member records are, to resolve the coverage an EHR sends: "fhir" is this server's
	// FHIR store, the one $member-match reads. Empty: not checked, and only the Coverage's own status and period are read.
	Members string `yaml:"members,omitempty" json:"members,omitempty"`

	// Check resolves the EHR's Coverage and Patient against the payer's records, when Members names them. Set by the server.
	Check MemberCheck `yaml:"-" json:"-"`
	// Authorized finds a prior authorization already approved for this patient and one of these codes, by its number, from
	// the payer's records. Set by the server with Members: fhir.
	Authorized func(ctx context.Context, coverage, patient map[string]any, codes []string, now time.Time) (string, error) `yaml:"-" json:"-"`
	// Client fetches what the EHR did not prefetch, from its fhirServer. Nil uses a client with a timeout.
	Client *http.Client `yaml:"-" json:"-"`

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

// Membership is what the payer's records say about the coverage an EHR sent: CRD's reasons for not-covered.
type Membership string

const (
	// MemberActive: the member and coverage were found and the coverage is in force.
	MemberActive Membership = ""
	// NoMemberFound: no member matches the patient (CRD reason no-member-found).
	NoMemberFound Membership = "no-member-found"
	// CoverageNotFound: the member is known but the coverage sent does not resolve to one of theirs (coverage-not-found).
	CoverageNotFound Membership = "coverage-not-found"
	// NoActiveCoverage: the coverage was found and is not in force now (no-active-coverage).
	NoActiveCoverage Membership = "no-active-coverage"
)

// MemberCheck resolves the EHR's Coverage and Patient against the payer's member records, with a sentence saying why.
type MemberCheck func(ctx context.Context, coverage, patient map[string]any, now time.Time) (Membership, string, error)

var reasonDisplay = map[Membership]string{NoMemberFound: "Member not found", CoverageNotFound: "Coverage not found",
	NoActiveCoverage: "Coverage not active", "technical": "Technical issues"}

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
	if r.Members != "" && r.Members != "fhir" {
		problems = append(problems, fmt.Sprintf("members %q: the one source of member records is fhir, this server's FHIR store", r.Members))
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
		for _, d := range rule.DocPurpose {
			if d == "withpa" {
				problems = append(problems, where+": doc_purpose withpa fails CRD 2.2.1's own invariant crd-ci-q4 on every response, so it is not sent; documentation for prior authorization is implied by pa: auth-needed")
			} else if !docPurposes[d] {
				problems = append(problems, fmt.Sprintf("%s: doc_purpose %q is not withclaim, withorder, retain-doc or OTH", where, d))
			}
		}
		if len(rule.DocPurpose) > 0 && len(rule.Documentation) == 0 {
			problems = append(problems, where+" says what documentation is for and asks for none")
		}
		if len(rule.DocPurpose) > 0 && strings.TrimSpace(rule.Reason) == "" {
			problems = append(problems, where+" gives a documentation purpose and no reason (crd-ci-q8)")
		}
		for _, d := range rule.Details {
			if detailCategory[d.Code] == "" {
				problems = append(problems, fmt.Sprintf("%s: detail %q is not a crd-coverage-detail code", where, d.Code))
			}
			if strings.TrimSpace(d.Value) == "" {
				problems = append(problems, fmt.Sprintf("%s: detail %q has no value", where, d.Code))
			}
		}
		if rule.Contact != nil && rule.Contact.Phone == "" && rule.Contact.Email == "" && rule.Contact.URL == "" {
			problems = append(problems, where+": contact needs a phone, email or url")
		}
		if rule.ExpiryDays < 0 {
			problems = append(problems, where+": expiry_days cannot be negative")
		}
		if len(rule.Documentation) > 0 && rule.Questionnaire == "" && len(rule.DocPurpose) == 0 {
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
			"prefetch": map[string]string{"coverage": "Coverage?patient={{context.patientId}}&status=active",
				"patient": "Patient/{{context.patientId}}"},
			// CRD 2.2.1 dev-1 and dev-4/dev-5: the CRD versions each service speaks, as major.minor, and a boolean option for each
			// kind of response it returns, which a client sets per call. The Inferno CRD test kit found both missing: the
			// options had been published under the request-side name with nothing in them.
			"extension": map[string]any{
				"davinci-crd.version":               []string{versionMajorMinor()},
				"davinci-crd.configuration-options": configurationOptions,
			},
		})
	}
	return map[string]any{"services": services}
}

// configurationOptions are what a client may switch off per call. This service returns one kind of response, coverage
// information, so that is the one option.
var configurationOptions = []map[string]any{{
	"code": "coverage-info", "type": "boolean", "name": "Coverage Information",
	"description": "Whether the order is covered, needs prior authorization, and what documentation is required, recorded on the order and shown on a card",
	"default":     true,
}}

func versionMajorMinor() string {
	parts := strings.SplitN(Version, ".", 3)
	if len(parts) < 2 {
		return Version
	}
	return parts[0] + "." + parts[1]
}

// Request is a CDS Hooks service request.
type Request struct {
	Hook         string `json:"hook"`
	HookInstance string `json:"hookInstance"`
	FHIRServer   string `json:"fhirServer,omitempty"`
	// FHIRAuthorization is the token for reading the EHR's FHIR server when prefetch does not carry what is needed.
	FHIRAuthorization *struct {
		AccessToken string `json:"access_token"`
	} `json:"fhirAuthorization,omitempty"`
	Context  map[string]json.RawMessage `json:"context"`
	Prefetch map[string]json.RawMessage `json:"prefetch,omitempty"`
	// Extension carries davinci-crd.configuration (the client's choice of options) and davinci-crd.requestedVersion.
	Extension map[string]json.RawMessage `json:"extension,omitempty"`
}

// wants reports whether the client left a boolean configuration option on, which is its default.
func (req *Request) wants(option string) bool {
	var conf map[string]any
	if json.Unmarshal(req.Extension["davinci-crd.configuration"], &conf) != nil {
		return true
	}
	v, ok := conf[option].(bool)
	return !ok || v
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
	// raw is the resource as sent, with only the coverage-information changed and every other byte, including the order of
	// its keys, as the EHR wrote it. Marshalled in place of Resource when set.
	raw json.RawMessage
}

// MarshalJSON writes the resource as the EHR sent it, plus the extension. Re-encoding from a map sorted every object's keys,
// and the Inferno CRD test kit, which compares arrays sorted by their JSON text, then read the reordered participants of an
// Appointment as a change outside the coverage-information.
func (a Action) MarshalJSON() ([]byte, error) {
	type plain struct {
		Type        string          `json:"type"`
		Description string          `json:"description,omitempty"`
		Resource    json.RawMessage `json:"resource"`
	}
	res := a.raw
	if len(res) == 0 {
		var err error
		if res, err = json.Marshal(a.Resource); err != nil {
			return nil, err
		}
	}
	return json.Marshal(plain{a.Type, a.Description, res})
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

// Evaluate answers a hook from what the request carries.
func (r *Rules) Evaluate(req *Request, now time.Time) (*Response, error) {
	return r.EvaluateContext(context.Background(), req, now)
}

// EvaluateContext answers a hook, fetching from the EHR's FHIR server what prefetch left out.
func (r *Rules) EvaluateContext(ctx context.Context, req *Request, now time.Time) (*Response, error) {
	var bundleRaw json.RawMessage
	switch req.Hook {
	case "order-sign", "order-select":
		bundleRaw = req.Context["draftOrders"]
	case "appointment-book":
		bundleRaw = req.Context["appointments"]
	case "order-dispatch":
		// order-dispatch names the orders (context.dispatchedOrders) rather than carrying them. Each is found in whatever
		// prefetch holds it, or read from the EHR's FHIR server. Only a prefetch key called "order" was read before, so the
		// Inferno CRD test kit's dispatches, prefetched by type, found nothing to answer about.
		bundleRaw = r.dispatched(ctx, req)
	default:
		return nil, fmt.Errorf("the hook %q is not one this service answers", req.Hook)
	}
	orders := resourcesIn(bundleRaw)
	raws := rawResourcesIn(bundleRaw)
	if len(orders) == 0 {
		return nil, ErrNoOrders
	}
	// coverage-info switched off: this service returns nothing else, so the answer is empty, as dev-5 requires.
	if !req.wants("coverage-info") {
		return &Response{Cards: []Card{}}, nil
	}

	source := map[string]any{"label": r.Payer, "topic": map[string]string{
		"system": "http://terminology.hl7.org/CodeSystem/cdshooks-card-type", "code": "coverage-info", "display": "Coverage Information"}}
	if r.URL != "" {
		source["url"] = r.URL
	}
	resp := &Response{Cards: []Card{}}

	// The coverage: from prefetch, or, when the EHR did not prefetch it, from its FHIR server. A failure to read it is a
	// technical problem, answered as indeterminate rather than guessed (CRD's technical reason).
	coverageRaw, patientRaw := req.Prefetch["coverage"], req.Prefetch["patient"]
	var technical string
	if coverageRaw == nil {
		raw, err := r.fetch(ctx, req, "Coverage?patient="+url.QueryEscape(contextString(req, "patientId"))+"&status=active")
		if err != nil {
			technical = err.Error()
		}
		coverageRaw = raw
	}
	cov := firstResource(coverageRaw, "Coverage")
	coverage := ""
	if cov != nil {
		coverage = "Coverage/" + str(cov["id"])
	}
	if coverage == "" && technical == "" {
		resp.Cards = append(resp.Cards, Card{UUID: newUUID(), Indicator: "warning", Source: source,
			Summary: "No coverage was sent for this patient",
			Detail:  "Coverage requirements depend on the plan. Send the patient's Coverage in the prefetch, or check eligibility first."})
		return resp, nil
	}

	// What the payer's records say about that coverage, when it keeps them here; otherwise only the Coverage's own status
	// and period, which say whether it is in force.
	membership, why := MemberActive, ""
	if technical == "" {
		membership, why = coverageInForce(cov, now)
	}
	if technical == "" && membership == MemberActive && r.Check != nil {
		if patientRaw == nil {
			patientRaw, _ = r.fetch(ctx, req, "Patient/"+url.PathEscape(contextString(req, "patientId")))
		}
		m, w, err := r.Check(ctx, cov, firstResource(patientRaw, "Patient"), now)
		switch {
		case err != nil:
			technical = "the payer's member records could not be read: " + err.Error()
		default:
			membership, why = m, w
		}
	}

	for i, o := range orders {
		var raw json.RawMessage
		if i < len(raws) {
			raw = raws[i]
		}
		kind, _ := o["resourceType"].(string)
		if _, ok := orderTypes[kind]; !ok {
			continue
		}
		var ext map[string]any
		var card Card
		ref := kind
		if id, ok := o["id"].(string); ok && id != "" {
			ref += "/" + id
		}
		switch {
		case technical != "":
			ext = r.coverageExplained(coverage, "indeterminate", "technical",
				"Coverage could not be determined because of a technical problem: "+technical+". Try again, or contact "+r.Payer+".", now)
			card = r.explainedCard(ref, source, "Coverage could not be determined (technical problem)", technical)
		case membership != MemberActive:
			ext = r.coverageExplained(coverage, "not-covered", string(membership), why, now)
			card = r.explainedCard(ref, source, reasonDisplay[membership], why)
		}
		if ext != nil {
			resp.SystemActions = append(resp.SystemActions, Action{Type: "update", Resource: cloneWith(o, ext),
				Description: "Record the coverage information on the order", raw: withExtension(raw, ext)})
			resp.Cards = append(resp.Cards, card)
			continue
		}
		rule, matched := r.match(o)
		// A prior authorization this payer already approved for this patient and service satisfies the requirement
		// (CRD's satisfied, with the authorization's number).
		if matched && rule.PA == "auth-needed" && r.Authorized != nil {
			if patientRaw == nil {
				patientRaw, _ = r.fetch(ctx, req, "Patient/"+url.PathEscape(contextString(req, "patientId")))
			}
			var codes []string
			for _, c := range codingsOf(o) {
				codes = append(codes, c.code)
			}
			if id, err := r.Authorized(ctx, cov, firstResource(patientRaw, "Patient"), codes, now); err == nil && id != "" {
				rule.PA, rule.satisfiedPAID = "satisfied", id
			}
		}
		ext = r.coverageInformation(rule, matched, coverage, now, dependencies(rule, orders, o)...)
		updated := cloneWith(o, ext)
		resp.SystemActions = append(resp.SystemActions, Action{Type: "update", Resource: updated,
			Description: "Record the coverage information on the order", raw: withExtension(raw, ext)})
		resp.Cards = append(resp.Cards, r.card(rule, matched, ref, source))
	}
	return resp, nil
}

func (r *Rules) match(o map[string]any) (Rule, bool) {
	for _, code := range codingsOf(o) {
		for _, rule := range r.Rules {
			if rule.System != "" && !sameSystem(rule.System, code.system) {
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

func (r *Rules) coverageInformation(rule Rule, matched bool, coverage string, now time.Time, deps ...string) map[string]any {
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
	if pa == "satisfied" && rule.satisfiedPAID != "" {
		ext = append(ext, map[string]any{"url": "satisfied-pa-id", "valueString": rule.satisfiedPAID})
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
	for _, d := range rule.DocPurpose {
		ext = append(ext, map[string]any{"url": "doc-purpose", "valueCode": d})
	}
	for _, c := range rule.BillingCodes {
		coding := map[string]any{"system": c.System, "code": c.Code}
		if c.Display != "" {
			coding["display"] = c.Display
		}
		ext = append(ext, map[string]any{"url": "billingCode", "valueCoding": coding})
	}
	for _, d := range rule.Details {
		parts := []any{
			map[string]any{"url": "category", "valueCode": detailCategory[d.Code]},
			map[string]any{"url": "code", "valueCodeableConcept": map[string]any{"coding": []any{map[string]any{
				"system": "http://terminology.hl7.org/CodeSystem/crd-coverage-detail", "code": d.Code}}}},
		}
		switch d.Value {
		case "true", "false":
			parts = append(parts, map[string]any{"url": "value", "valueBoolean": d.Value == "true"})
		default:
			parts = append(parts, map[string]any{"url": "value", "valueString": d.Value})
		}
		if d.Qualification != "" {
			parts = append(parts, map[string]any{"url": "qualification", "valueString": d.Qualification})
		}
		ext = append(ext, map[string]any{"url": "detail", "extension": parts})
	}
	for _, dep := range deps {
		ext = append(ext, map[string]any{"url": "dependency", "valueReference": map[string]any{"reference": dep}})
	}
	if c := rule.Contact; c != nil {
		detail := map[string]any{}
		if c.Name != "" {
			detail["name"] = c.Name
		}
		var tel []any
		for _, t := range []struct{ sys, v string }{{"phone", c.Phone}, {"email", c.Email}, {"url", c.URL}} {
			if t.v != "" {
				tel = append(tel, map[string]any{"system": t.sys, "value": t.v})
			}
		}
		detail["telecom"] = tel
		ext = append(ext, map[string]any{"url": "contact", "valueContactDetail": detail})
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
	if rule.ExpiryDays > 0 {
		ext = append(ext, map[string]any{"url": "expiry-date", "valueDate": now.UTC().AddDate(0, 0, rule.ExpiryDays).Format("2006-01-02")})
	}
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
	case rule.PA == "satisfied":
		c.Summary = name + ": covered, prior authorization already approved (" + rule.satisfiedPAID + ")"
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

// sameSystem compares code systems, treating HCPCS's two URIs as one. HL7 Terminology prefers http://, while CRD 2.2.1 and
// CARIN Blue Button 2.2.0 use https://, so an EHR may send either.
func sameSystem(a, b string) bool {
	norm := func(s string) string {
		return strings.Replace(s, "https://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets", 1)
	}
	return norm(a) == norm(b)
}

// dependencies are the other orders in the request carrying a code this rule depends on.
func dependencies(rule Rule, orders []map[string]any, self map[string]any) []string {
	var out []string
	for _, o := range orders {
		if str(o["id"]) == "" || (str(o["id"]) == str(self["id"]) && o["resourceType"] == self["resourceType"]) {
			continue
		}
		for _, c := range codingsOf(o) {
			if slices.Contains(rule.DependsOn, c.code) {
				out = append(out, str(o["resourceType"])+"/"+str(o["id"]))
				break
			}
		}
	}
	return out
}

// dispatched gathers the orders an order-dispatch names, as a Bundle, from prefetch or the EHR's FHIR server.
func (r *Rules) dispatched(ctx context.Context, req *Request) json.RawMessage {
	var refs []string
	_ = json.Unmarshal(req.Context["dispatchedOrders"], &refs)
	if len(refs) == 0 {
		return req.Prefetch["order"]
	}
	byRef := map[string]json.RawMessage{}
	for _, v := range req.Prefetch {
		raws := rawResourcesIn(v)
		for i, res := range resourcesIn(v) {
			if i < len(raws) {
				byRef[str(res["resourceType"])+"/"+str(res["id"])] = raws[i]
			}
		}
	}
	var entries []map[string]json.RawMessage
	for _, ref := range refs {
		raw, ok := byRef[ref]
		if !ok {
			raw, _ = r.fetch(ctx, req, ref)
		}
		if len(raw) > 0 {
			entries = append(entries, map[string]json.RawMessage{"resource": raw})
		}
	}
	out, _ := json.Marshal(map[string]any{"resourceType": "Bundle", "type": "collection", "entry": entries})
	return out
}

// rawResourcesIn is resourcesIn, as the bytes that arrived.
func rawResourcesIn(raw json.RawMessage) []json.RawMessage {
	var b struct {
		ResourceType string `json:"resourceType"`
		Entry        []struct {
			Resource json.RawMessage `json:"resource"`
		} `json:"entry"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &b) != nil {
		return nil
	}
	if b.ResourceType != "Bundle" {
		return []json.RawMessage{raw}
	}
	var out []json.RawMessage
	for _, e := range b.Entry {
		if len(e.Resource) > 0 && string(e.Resource) != "null" {
			out = append(out, e.Resource)
		}
	}
	return out
}

// withExtension puts a coverage-information extension on a resource's raw JSON, replacing any it had, and leaves every other
// member as it arrived. Nil when the raw form cannot be read, so the map is used instead.
func withExtension(raw json.RawMessage, ext map[string]any) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil
	}
	type member struct {
		key string
		val json.RawMessage
	}
	var members []member
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil
		}
		members = append(members, member{k, v})
	}
	extRaw, err := json.Marshal(ext)
	if err != nil {
		return nil
	}
	found := false
	for i, m := range members {
		if m.key != "extension" {
			continue
		}
		found = true
		var list []json.RawMessage
		if json.Unmarshal(m.val, &list) != nil {
			return nil
		}
		kept := list[:0]
		for _, e := range list {
			var head struct {
				URL string `json:"url"`
			}
			if json.Unmarshal(e, &head) == nil && head.URL == ExtCoverageInformation {
				continue
			}
			kept = append(kept, e)
		}
		kept = append(kept, extRaw)
		members[i].val, _ = json.Marshal(kept)
	}
	if !found {
		v, _ := json.Marshal([]json.RawMessage{extRaw})
		members = append(members, member{"extension", v})
	}
	var buf bytes.Buffer
	buf.WriteByte('{')
	for i, m := range members {
		if i > 0 {
			buf.WriteByte(',')
		}
		k, _ := json.Marshal(m.key)
		buf.Write(k)
		buf.WriteByte(':')
		buf.Write(m.val)
	}
	buf.WriteByte('}')
	return buf.Bytes()
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

// firstResource is the first resource of a type in prefetch, whether that is the resource or a search Bundle.
func firstResource(raw json.RawMessage, kind string) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var one map[string]any
	if json.Unmarshal(raw, &one) == nil && one["resourceType"] == kind && str(one["id"]) != "" {
		return one
	}
	for _, r := range resourcesIn(raw) {
		if r["resourceType"] == kind && str(r["id"]) != "" {
			return r
		}
	}
	return nil
}

func str(v any) string { s, _ := v.(string); return s }

func contextString(req *Request, key string) string {
	var v string
	_ = json.Unmarshal(req.Context[key], &v)
	return v
}

// coverageInForce reads the Coverage's own status and period, which say whether it is in force now.
func coverageInForce(cov map[string]any, now time.Time) (Membership, string) {
	if st := str(cov["status"]); st != "" && st != "active" {
		return NoActiveCoverage, "The coverage sent is " + st + ", not active."
	}
	p, _ := cov["period"].(map[string]any)
	today := now.UTC().Format("2006-01-02")
	if start := str(p["start"]); start != "" && start[:min(10, len(start))] > today {
		return NoActiveCoverage, "The coverage sent does not start until " + start[:min(10, len(start))] + "."
	}
	if end := str(p["end"]); end != "" && end[:min(10, len(end))] < today {
		return NoActiveCoverage, "The coverage sent ended on " + end[:min(10, len(end))] + "."
	}
	return MemberActive, ""
}

// fetch reads a FHIR path from the EHR's server with the token it gave, for what prefetch did not carry.
func (r *Rules) fetch(ctx context.Context, req *Request, path string) (json.RawMessage, error) {
	if req.FHIRServer == "" {
		return nil, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(req.FHIRServer, "/")+"/"+path, nil)
	if err != nil {
		return nil, err
	}
	hreq.Header.Set("Accept", "application/fhir+json")
	if req.FHIRAuthorization != nil && req.FHIRAuthorization.AccessToken != "" {
		hreq.Header.Set("Authorization", "Bearer "+req.FHIRAuthorization.AccessToken)
	}
	client := r.Client
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	res, err := client.Do(hreq)
	if err != nil {
		return nil, fmt.Errorf("the EHR's FHIR server could not be reached")
	}
	defer func() { _ = res.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if res.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if res.StatusCode >= 300 {
		return nil, fmt.Errorf("the EHR's FHIR server answered %d to %s", res.StatusCode, strings.SplitN(path, "?", 2)[0])
	}
	return body, nil
}

// coverageExplained is coverage-information that says why coverage could not be determined or does not apply, with CRD's
// reason code and the sentence a clinician reads.
func (r *Rules) coverageExplained(coverage, covered, reason, text string, now time.Time) map[string]any {
	// coverage is required. When the Coverage could not be read at all, the reference can only describe it.
	ref := map[string]any{"reference": coverage}
	if coverage == "" {
		ref = map[string]any{"type": "Coverage", "display": "The patient's coverage, which could not be read from the EHR"}
	}
	ext := []any{map[string]any{"url": "coverage", "valueReference": ref}}
	ext = append(ext,
		map[string]any{"url": "covered", "valueCode": covered},
		map[string]any{"url": "reason", "valueCodeableConcept": map[string]any{
			"coding": []any{map[string]any{"system": "http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp", "code": reason,
				"display": reasonDisplay[Membership(reason)]}},
			"text": text,
		}},
		map[string]any{"url": "date", "valueDate": now.UTC().Format("2006-01-02")},
		map[string]any{"url": "coverage-assertion-id", "valueString": newUUID()},
	)
	return map[string]any{"url": ExtCoverageInformation, "extension": ext}
}

func (r *Rules) explainedCard(ref string, source map[string]any, summary, detail string) Card {
	return Card{UUID: newUUID(), Source: source, Indicator: "warning", Summary: summary, Detail: detail,
		Extension: map[string]any{"davinci-crd.associated-resource": []any{map[string]string{"reference": ref}}}}
}
