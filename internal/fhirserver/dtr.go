package fhirserver

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Da Vinci DTR 2.2.0, the payer's side: $questionnaire-package, $log-questionnaire-errors and $next-question.
//
// A DTR client - the EHR's documentation app - asks for the questionnaires the payer wants completed for an order, and gets them back
// as a package: a Bundle per questionnaire holding it, a QuestionnaireResponse to fill in, the CQL Libraries it needs and the value sets
// its answers use. The questionnaires are ordinary FHIR resources in this store, loaded by the payer; which one applies to which order is
// the CRD rules file's job. So a request can name the questionnaire, send the order (which carries CRD's coverage-information), or send
// only the coverage assertion id CRD gave it.
//
// It was first written against DTR 2.1.0 while CRD and PAS here were 2.2.1, the releases that are meant to be used together. Against 2.2.0
// the package was not conformant: its parameters were named PackageBundle and Outcome (2.2.0 says packagebundle and outcome), each
// bundle lacked the QuestionnaireResponse 2.2.0 requires, a Library's own dependencies were left out, and the value sets the answers
// are drawn from were not included. Questions on chat.fhir.org about the reference implementation omitting FHIRHelpers are what
// prompted the check.

const (
	crdCoverageInfo = "http://hl7.org/fhir/us/davinci-crd/StructureDefinition/ext-coverage-information"
	dtrBase         = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/"
	dtrVersion      = "2.2.0"
)

func (s *Server) registerDTR(mux *http.ServeMux) {
	mux.HandleFunc("POST /Questionnaire/$questionnaire-package", s.handleQuestionnairePackage)
	mux.HandleFunc("POST /Questionnaire/$log-questionnaire-errors", s.handleLogQuestionnaireErrors)
	mux.HandleFunc("POST /Questionnaire/$next-question", s.handleNextQuestion)
}

type dtrParam struct {
	Name           string          `json:"name"`
	ValueCanonical string          `json:"valueCanonical"`
	ValueString    string          `json:"valueString"`
	ValueInstant   string          `json:"valueInstant"`
	Resource       json.RawMessage `json:"resource"`
}

func (s *Server) readParameters(w http.ResponseWriter, r *http.Request) ([]dtrParam, bool) {
	body, ok := s.readBody(w, r)
	if !ok {
		return nil, false
	}
	var params struct {
		ResourceType string     `json:"resourceType"`
		Parameter    []dtrParam `json:"parameter"`
	}
	if err := json.Unmarshal(body, &params); err != nil || params.ResourceType != "Parameters" {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "the body must be a Parameters resource")
		return nil, false
	}
	return params.Parameter, true
}

// QuestionnairePackage runs $questionnaire-package on a Parameters body without going through the endpoint's authentication, for the
// console of the same process, which has already checked its own user.
func (s *Server) QuestionnairePackage(ctx context.Context, params []byte) (int, []byte) {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/Questionnaire/$questionnaire-package", bytes.NewReader(params)).WithContext(ctx)
	req.Header.Set("Content-Type", "application/fhir+json")
	s.handleQuestionnairePackage(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func (s *Server) handleQuestionnairePackage(w http.ResponseWriter, r *http.Request) {
	params, ok := s.readParameters(w, r)
	if !ok {
		return
	}

	var (
		canonicals []string
		seen       = map[string]bool{}
		coverage   map[string]any
		orders     []map[string]any
		contextID  string
		since      time.Time
		issues     []map[string]any
	)
	add := func(c string) {
		c = strings.TrimSpace(c)
		if c != "" && !seen[c] {
			seen[c] = true
			canonicals = append(canonicals, c)
		}
	}
	for _, p := range params {
		switch p.Name {
		case "questionnaire":
			add(p.ValueCanonical)
		case "coverage":
			_ = json.Unmarshal(p.Resource, &coverage)
		case "order":
			var order map[string]any
			if json.Unmarshal(p.Resource, &order) == nil && order != nil {
				orders = append(orders, order)
				for _, q := range orderQuestionnaires(order) {
					add(q)
				}
			}
		case "context":
			contextID = strings.TrimSpace(p.ValueString)
		case "changedsince":
			if t, err := time.Parse(time.RFC3339, p.ValueInstant); err == nil {
				since = t
			}
		case "referenced":
			// Supporting resources for the orders. 2.2.0 dropped this parameter by mistake and a technical correction restores it
			// (FHIR-58734); it is accepted so a client sending it is not refused, and is not needed to choose questionnaires here.
		}
	}
	if contextID != "" {
		var found []string
		if s.DTRContext != nil {
			found = s.DTRContext(contextID)
		}
		for _, q := range found {
			add(q)
		}
		if len(found) == 0 {
			issues = append(issues, map[string]any{"severity": "warning", "code": "not-found", "diagnostics": "context " + contextID +
				" is not a coverage assertion this server made, or was made before it last restarted; send the order, which carries " +
				"its coverage-information, or name the questionnaire"})
		}
	}
	if coverage == nil {
		issues = append(issues, map[string]any{"severity": "warning", "code": "required",
			"diagnostics": "DTR requires the coverage parameter; without it the QuestionnaireResponse cannot name the coverage it is for"})
	}
	if len(canonicals) == 0 {
		if contextID != "" {
			// A context this server cannot resolve is source data it cannot use, which DTR answers with a 4xx and an
			// OperationOutcome (spec-130), not an empty package.
			s.writeJSON(w, http.StatusNotFound, map[string]any{"resourceType": "OperationOutcome", "issue": issues})
			return
		}
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "required",
			"name a questionnaire, send an order carrying CRD's coverage-information with one, or give CRD's coverage assertion id as context")
		return
	}

	out := []any{}
	for _, canonical := range canonicals {
		q := s.byCanonical(r, "Questionnaire", canonical)
		if q == nil {
			issues = append(issues, map[string]any{"severity": "warning", "code": "not-found",
				"diagnostics": "no Questionnaire with url " + canonical + " is loaded on this server"})
			continue
		}
		// An adaptive questionnaire goes out as an empty shell; $next-question supplies its questions one at a time.
		adaptive := isAdaptive(q)
		packaged := q
		if adaptive {
			packaged = s.adaptiveShell(q, "dtr-questionnaire-adapt-search")
		}
		entries := []any{map[string]any{"fullUrl": s.entryURL(q), "resource": packaged}}
		newest := lastUpdated(q)

		// The CQL the questionnaire pre-fills itself with, named by cqf-library, and every Library those depend on in turn. The
		// dependencies were left out, so an engine given the package could not run the logic without fetching FHIRHelpers itself.
		libs, missing := s.libraryClosure(r, questionnaireLibraries(q))
		for _, l := range libs {
			entries = append(entries, map[string]any{"fullUrl": s.entryURL(l), "resource": l})
			if t := lastUpdated(l); t.After(newest) {
				newest = t
			}
		}
		for _, m := range missing {
			issues = append(issues, map[string]any{"severity": "warning", "code": "not-found",
				"diagnostics": "the package needs the Library " + m + ", which is not loaded"})
		}
		// The value sets the answers are chosen from, so the app can render them without a terminology call.
		for _, vsURL := range answerValueSets(q) {
			if vs := s.byCanonical(r, "ValueSet", vsURL); vs != nil {
				entries = append(entries, map[string]any{"fullUrl": s.entryURL(vs), "resource": s.packagedValueSet(r, vs)})
				if t := lastUpdated(vs); t.After(newest) {
					newest = t
				}
			} else {
				issues = append(issues, map[string]any{"severity": "information", "code": "not-found",
					"diagnostics": "the questionnaire's answers use the value set " + vsURL + ", which is not loaded; the app will need to expand it"})
			}
		}
		if !since.IsZero() && !newest.IsZero() && !newest.After(since) {
			continue // changedsince: nothing in this package changed
		}
		qr := seedResponse(q, coverage, orders)
		// Pin the questionnaire's library and value set references to the versions packaged with it.
		versions := map[string]string{}
		for _, e := range entries[1:] {
			res, _ := e.(map[string]any)["resource"].(map[string]any)
			u, _ := res["url"].(string)
			if v, _ := res["version"].(string); u != "" && v != "" {
				versions[u] = v
			}
		}
		entries[0].(map[string]any)["resource"] = pinCanonicals(entries[0].(map[string]any)["resource"].(map[string]any), versions)
		if adaptive {
			shell := s.adaptiveShell(q, "dtr-questionnaire-adapt")
			id, _ := q["id"].(string)
			shell["id"] = id
			// The questionnaire the app fills in is derived from the one in the package; DTR names that by canonical.
			canonical, _ := q["url"].(string)
			if v, _ := q["version"].(string); v != "" {
				canonical += "|" + v
			}
			shell["derivedFrom"] = append(append([]any{}, listOf(q["derivedFrom"])...), canonical)
			qr["contained"] = []any{shell}
			qr["questionnaire"] = "#" + id
			qr["meta"] = map[string]any{"profile": []string{dtrBase + "dtr-questionnaireresponse-adapt|" + dtrVersion}}
		}
		entries = append(entries[:1], append([]any{map[string]any{"fullUrl": "urn:uuid:" + fhir.DeterministicUUID("QuestionnaireResponse", fullURLOf(q), refOf(coverage)), "resource": qr}}, entries[1:]...)...)
		bundle := map[string]any{"resourceType": "Bundle", "type": "collection",
			"meta":  map[string]any{"profile": []string{dtrBase + "DTR-QPackageBundle|" + dtrVersion}},
			"entry": entries}
		out = append(out, map[string]any{"name": "packagebundle", "resource": bundle})
	}
	if len(issues) > 0 {
		out = append(out, map[string]any{"name": "outcome", "resource": map[string]any{"resourceType": "OperationOutcome", "issue": issues}})
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"resourceType": "Parameters", "parameter": out})
}

// orderQuestionnaires returns the questionnaires CRD named on an order, in its coverage-information extension.
func orderQuestionnaires(order map[string]any) []string {
	var out []string
	for _, e := range listOf(order["extension"]) {
		em, _ := e.(map[string]any)
		if em["url"] != crdCoverageInfo {
			continue
		}
		for _, sub := range listOf(em["extension"]) {
			sm, _ := sub.(map[string]any)
			if sm["url"] == "questionnaire" {
				if c, _ := sm["valueCanonical"].(string); c != "" {
					out = append(out, c)
				}
			}
		}
	}
	return out
}

// seedResponse is the QuestionnaireResponse a package carries: in progress, for the patient the coverage is for, naming the coverage
// and the order, and saying what the answers are for.
func seedResponse(q, coverage map[string]any, orders []map[string]any) map[string]any {
	canonical := fullURLOf(q)
	if v, _ := q["version"].(string); v != "" {
		canonical += "|" + v
	}
	ext := []any{}
	if ref := refOf(coverage); ref != "" {
		ext = append(ext, map[string]any{"url": dtrBase + "qr-coverage", "valueReference": map[string]any{"reference": ref}})
	}
	ext = append(ext, map[string]any{"url": dtrBase + "intendedUse", "valueCodeableConcept": intendedUse(orders)})
	if len(orders) == 1 {
		if ref := refOf(orders[0]); ref != "" {
			ext = append(ext, map[string]any{"url": dtrBase + "qr-context", "valueReference": map[string]any{"reference": ref}})
		}
	}
	qr := map[string]any{
		"resourceType":  "QuestionnaireResponse",
		"meta":          map[string]any{"profile": []string{dtrBase + "dtr-questionnaireresponse|" + dtrVersion}},
		"extension":     ext,
		"questionnaire": canonical,
		"status":        "in-progress",
		"authored":      time.Now().UTC().Format(time.RFC3339),
	}
	// The subject is the member: Coverage.beneficiary, else the order's subject.
	subject, _ := coverage["beneficiary"].(map[string]any)
	if subject == nil && len(orders) > 0 {
		subject, _ = orders[0]["subject"].(map[string]any)
	}
	if subject != nil {
		qr["subject"] = subject
	}
	return qr
}

// intendedUse says what the answers are for, from CRD's coverage-information on the order: its doc-purpose when there is one,
// prior authorisation when it said authorisation is needed, and otherwise the null flavor OTH rather than a guess.
func intendedUse(orders []map[string]any) map[string]any {
	const crdCodes = "http://hl7.org/fhir/us/davinci-crd/CodeSystem/coverage-information-codes"
	for _, o := range orders {
		for _, e := range listOf(o["extension"]) {
			em, _ := e.(map[string]any)
			if em["url"] != crdCoverageInfo {
				continue
			}
			paNeeded := ""
			for _, sub := range listOf(em["extension"]) {
				sm, _ := sub.(map[string]any)
				code, _ := sm["valueCode"].(string)
				switch sm["url"] {
				case "doc-purpose":
					if code != "" {
						return map[string]any{"coding": []any{map[string]any{"system": crdCodes, "code": code}}}
					}
				case "pa-needed":
					paNeeded = code
				}
			}
			if paNeeded == "auth-needed" {
				return map[string]any{"coding": []any{map[string]any{"system": crdCodes, "code": "withpa", "display": "Include in prior authorization"}}}
			}
		}
	}
	return map[string]any{"coding": []any{map[string]any{"system": "http://terminology.hl7.org/CodeSystem/v3-NullFlavor", "code": "OTH", "display": "other"}}}
}

// questionnaireLibraries are the canonicals a questionnaire names in cqf-library.
func questionnaireLibraries(q map[string]any) []string {
	var out []string
	for _, e := range listOf(q["extension"]) {
		em, _ := e.(map[string]any)
		if em["url"] == "http://hl7.org/fhir/StructureDefinition/cqf-library" {
			if c, _ := em["valueCanonical"].(string); c != "" {
				out = append(out, c)
			}
		}
	}
	return out
}

// libraryClosure loads the named Libraries and every Library they depend on (relatedArtifact depends-on), once each.
func (s *Server) libraryClosure(r *http.Request, start []string) (libs []map[string]any, missing []string) {
	seen := map[string]bool{}
	queue := append([]string(nil), start...)
	for len(queue) > 0 && len(seen) < 200 {
		c := queue[0]
		queue = queue[1:]
		if seen[c] {
			continue
		}
		seen[c] = true
		l := s.byCanonical(r, "Library", c)
		if l == nil {
			missing = append(missing, c)
			continue
		}
		libs = append(libs, l)
		for _, ra := range listOf(l["relatedArtifact"]) {
			rm, _ := ra.(map[string]any)
			if rm["type"] != "depends-on" {
				continue
			}
			// A dependency is a Library when it says so or does not say: ModelInfo and code systems are named the same way, and
			// are not resources this package carries.
			res, _ := rm["resource"].(string)
			if res != "" && (strings.Contains(res, "/Library/") || !strings.Contains(res, "/")) {
				queue = append(queue, res)
			}
		}
	}
	return libs, missing
}

// answerValueSets are the value sets a questionnaire's items take their answers from, at any depth.
func answerValueSets(q map[string]any) []string {
	var out []string
	seen := map[string]bool{}
	var walk func(items []any)
	walk = func(items []any) {
		for _, it := range items {
			im, _ := it.(map[string]any)
			if vs, _ := im["answerValueSet"].(string); vs != "" && !strings.HasPrefix(vs, "#") && !seen[vs] {
				seen[vs] = true
				out = append(out, vs)
			}
			walk(listOf(im["item"]))
		}
	}
	walk(listOf(q["item"]))
	return out
}

// handleLogQuestionnaireErrors receives the problems a DTR app met with this payer's questionnaires. DTR 2.2.0 requires a payer to
// support it (oper-1). The questionnaires and outcomes are paired by position, which is the intent the guide's editor has stated for the
// two unordered, repeating parameters. Each is written to the server log at warning level; the guide forbids the app from sending
// PHI in them, and nothing is stored.
func (s *Server) handleLogQuestionnaireErrors(w http.ResponseWriter, r *http.Request) {
	params, ok := s.readParameters(w, r)
	if !ok {
		return
	}
	var qs []string
	var outcomes []map[string]any
	for _, p := range params {
		switch p.Name {
		case "questionnaire":
			qs = append(qs, p.ValueCanonical)
		case "operationOutcome", "outcome":
			var oo map[string]any
			if json.Unmarshal(p.Resource, &oo) == nil && oo["resourceType"] == "OperationOutcome" {
				outcomes = append(outcomes, oo)
			}
		}
	}
	if len(qs) == 0 || len(outcomes) == 0 {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "required",
			"send at least one questionnaire canonical and one operationOutcome")
		return
	}
	if len(qs) != len(outcomes) {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "invalid", fmt.Sprintf(
			"%d questionnaires and %d operationOutcomes: they are paired by position, so the counts must match", len(qs), len(outcomes)))
		return
	}
	issues := 0
	for i, q := range qs {
		for _, is := range listOf(outcomes[i]["issue"]) {
			im, _ := is.(map[string]any)
			issues++
			if s.Log != nil {
				s.Log.Warn("DTR app reported a questionnaire problem", slog.String("questionnaire", q),
					slog.Any("severity", im["severity"]), slog.Any("code", im["code"]), slog.Any("diagnostics", im["diagnostics"]),
					slog.Any("expression", im["expression"]))
			}
		}
	}
	s.writeOutcome(w, r, http.StatusOK, fhir.SeverityInformation, "informational",
		fmt.Sprintf("recorded %d issue(s) for %d questionnaire(s)", issues, len(qs)))
}

func listOf(v any) []any { l, _ := v.([]any); return l }

// fullURLOf is a resource's canonical url, or Type/id when it has none.
func fullURLOf(res map[string]any) string {
	if u, _ := res["url"].(string); u != "" {
		return u
	}
	if ref := refOf(res); ref != "" {
		return ref
	}
	return ""
}

// entryURL is a bundle entry's fullUrl. FHIR lets it equal the canonical url only when that url ends in Type/id (the validator
// refused a package whose questionnaire's url did not); otherwise it is the resource's address on this server.
func (s *Server) entryURL(res map[string]any) string {
	ref := refOf(res)
	if u, _ := res["url"].(string); u != "" && (ref == "" || strings.HasSuffix(u, "/"+ref)) {
		return u
	}
	if ref == "" {
		return fullURLOf(res)
	}
	if base := strings.TrimRight(s.BaseURL, "/"); base != "" {
		return base + "/" + ref
	}
	return "urn:uuid:" + fhir.DeterministicUUID(ref)
}

func refOf(res map[string]any) string {
	t, _ := res["resourceType"].(string)
	id, _ := res["id"].(string)
	if t == "" || id == "" {
		return ""
	}
	return t + "/" + id
}

func lastUpdated(res map[string]any) time.Time {
	m, _ := res["meta"].(map[string]any)
	s, _ := m["lastUpdated"].(string)
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

// byCanonical finds the resource with this canonical url. With a |version suffix it returns that version; without one, the newest.
func (s *Server) byCanonical(r *http.Request, resourceType, canonical string) map[string]any {
	parts := strings.SplitN(canonical, "|", 2)
	q, err := ParseSearch(resourceType, map[string][]string{"url": {parts[0]}})
	if err != nil {
		return nil
	}
	res, err := s.Store.Search(r.Context(), q)
	if err != nil || len(res.Resources) == 0 {
		return nil
	}
	var best map[string]any
	for _, found := range res.Resources {
		raw, err := fhir.Marshal(found, s.Store.Version())
		if err != nil {
			continue
		}
		var m map[string]any
		if json.Unmarshal(raw, &m) != nil {
			continue
		}
		v, _ := m["version"].(string)
		if len(parts) == 2 {
			// A version-specific request used to get whichever version the search returned first.
			if v == parts[1] {
				return m
			}
			continue
		}
		if best == nil || lastUpdated(m).After(lastUpdated(best)) {
			best = m
		}
	}
	return best
}

// pinCanonicals returns a copy of a questionnaire whose cqf-library and answerValueSet references name the exact version this
// package carries. DTR requires version-specific references, so an app runs the logic and lists the answers it was packaged
// with even after the payer publishes a newer version; the payer's stored questionnaire is left as written.
func pinCanonicals(q map[string]any, versions map[string]string) map[string]any {
	raw, _ := json.Marshal(q)
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	pin := func(v any) any {
		c, _ := v.(string)
		if c == "" || strings.Contains(c, "|") || versions[c] == "" {
			return v
		}
		return c + "|" + versions[c]
	}
	for _, e := range listOf(out["extension"]) {
		if em, _ := e.(map[string]any); em["url"] == "http://hl7.org/fhir/StructureDefinition/cqf-library" {
			em["valueCanonical"] = pin(em["valueCanonical"])
		}
	}
	var walk func([]any)
	walk = func(items []any) {
		for _, it := range items {
			im, _ := it.(map[string]any)
			if im == nil {
				continue
			}
			if v, ok := im["answerValueSet"]; ok {
				im["answerValueSet"] = pin(v)
			}
			walk(listOf(im["item"]))
		}
	}
	walk(listOf(out["item"]))
	return out
}
