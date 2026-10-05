package fhirserver

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// DTR 2.2.0 adaptive questionnaires: the payer asks one question at a time, choosing the next from the answers so far.
//
// The payer writes the whole questionnaire once, as an ordinary Questionnaire carrying SDC's questionnaireAdaptive extension, with
// enableWhen on the items that only apply sometimes. $questionnaire-package hands out an empty shell of it (DTR's adapt-search
// profile). Each $next-question call then appends the next top-level item whose enableWhen the answers satisfy to the Questionnaire
// contained in the response, and marks the response completed when nothing is left to ask. Items that do not apply are passed over, so
// the app never shows a question the payer's own rules say is irrelevant.
//
// The order and the branching are the payer's, written as standard enableWhen; nothing is decided by a model or a script. A server that
// picked questions some other way (a CQL engine, say) would fit behind the same operation.

const sdcAdaptive = "http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-questionnaireAdaptive"

// isAdaptive reports whether a stored questionnaire is served one question at a time.
func isAdaptive(q map[string]any) bool {
	for _, e := range listOf(q["extension"]) {
		if em, _ := e.(map[string]any); em["url"] == sdcAdaptive && em["valueBoolean"] != false {
			return true
		}
	}
	return false
}

// adaptiveShell is the questionnaire without its questions, as the package and a first $next-question call carry it.
func adaptiveShell(q map[string]any, profile string) map[string]any {
	out := map[string]any{}
	for k, v := range q {
		if k != "item" && k != "meta" && k != "text" {
			out[k] = v
		}
	}
	out["meta"] = map[string]any{"profile": []string{dtrBase + profile + "|" + dtrVersion}}
	return out
}

// handleNextQuestion is $next-question.
func (s *Server) handleNextQuestion(w http.ResponseWriter, r *http.Request) {
	params, ok := s.readParameters(w, r)
	if !ok {
		return
	}
	var raw json.RawMessage
	for _, p := range params {
		if p.Name == "questionnaire-response" {
			raw = p.Resource
		}
	}
	if len(raw) == 0 {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "required",
			"$next-question needs a questionnaire-response: a QuestionnaireResponse with the adaptive Questionnaire contained in it")
		return
	}
	var qr map[string]any
	if json.Unmarshal(raw, &qr) != nil || qr["resourceType"] != "QuestionnaireResponse" {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "questionnaire-response must be a QuestionnaireResponse")
		return
	}
	next, status, msg := s.nextQuestion(r, qr)
	if status != http.StatusOK {
		s.writeOutcome(w, r, status, fhir.SeverityError, "processing", msg)
		return
	}
	s.writeJSON(w, http.StatusOK, map[string]any{"resourceType": "Parameters",
		"parameter": []any{map[string]any{"name": "return", "resource": next}}})
}

// nextQuestion advances a response by one question, or completes it.
func (s *Server) nextQuestion(r *http.Request, qr map[string]any) (map[string]any, int, string) {
	contained := containedQuestionnaire(qr)
	if contained == nil {
		return nil, http.StatusBadRequest, "the QuestionnaireResponse carries no contained Questionnaire, which DTR requires for an adaptive questionnaire " +
			"(start from the shell $questionnaire-package returned)"
	}
	canonical, _ := contained["url"].(string)
	if v, _ := contained["version"].(string); v != "" && canonical != "" {
		canonical += "|" + v
	}
	source := s.byCanonical(r, "Questionnaire", canonical)
	if source == nil {
		return nil, http.StatusNotFound, "no Questionnaire with url " + canonical + " is loaded on this server, so there is nothing to ask next"
	}
	if !isAdaptive(source) {
		return nil, http.StatusUnprocessableEntity, canonical + " is a standard questionnaire; fetch it whole with $questionnaire-package"
	}
	if qr["status"] == "completed" {
		return qr, http.StatusOK, ""
	}

	answers := map[string][]any{}
	collectAnswers(listOf(qr["item"]), answers)

	asked := map[string]bool{}
	for _, it := range listOf(contained["item"]) {
		if im, _ := it.(map[string]any); im != nil {
			id, _ := im["linkId"].(string)
			asked[id] = true
		}
	}
	// A required question already asked must be answered before the next is chosen: the choice may depend on it.
	for _, it := range listOf(contained["item"]) {
		im, _ := it.(map[string]any)
		if missing := unansweredRequired(im, answers); missing != "" {
			return nil, http.StatusUnprocessableEntity, "the required question " + missing + " has no answer yet; answer it before asking for the next one"
		}
	}

	var next map[string]any
	for _, it := range listOf(source["item"]) {
		im, _ := it.(map[string]any)
		id, _ := im["linkId"].(string)
		if im == nil || asked[id] {
			continue
		}
		if !enabled(im, answers) {
			continue // the payer's rules say this one does not apply
		}
		next = im
		break
	}

	out := map[string]any{}
	for k, v := range qr {
		out[k] = v
	}
	q := map[string]any{}
	for k, v := range contained {
		q[k] = v
	}
	q["meta"] = map[string]any{"profile": []string{dtrBase + "dtr-questionnaire-adapt|" + dtrVersion}}
	if q["id"] == nil || q["id"] == "" {
		q["id"] = "adaptive"
	}
	out["questionnaire"] = "#" + q["id"].(string)
	out["meta"] = map[string]any{"profile": []string{dtrBase + "dtr-questionnaireresponse-adapt|" + dtrVersion}}
	if next == nil {
		out["status"] = "completed"
	} else {
		q["item"] = append(append([]any{}, listOf(contained["item"])...), next)
		out["status"] = "in-progress"
	}
	out["contained"] = replaceContained(listOf(qr["contained"]), contained, q)
	out["authored"] = time.Now().UTC().Format(time.RFC3339)
	return out, http.StatusOK, ""
}

func containedQuestionnaire(qr map[string]any) map[string]any {
	ref, _ := qr["questionnaire"].(string)
	var first map[string]any
	for _, c := range listOf(qr["contained"]) {
		cm, _ := c.(map[string]any)
		if cm["resourceType"] != "Questionnaire" {
			continue
		}
		if first == nil {
			first = cm
		}
		if id, _ := cm["id"].(string); ref == "#"+id {
			return cm
		}
	}
	return first
}

func replaceContained(list []any, old, replacement map[string]any) []any {
	out := make([]any, 0, len(list))
	for _, c := range list {
		if cm, _ := c.(map[string]any); cm != nil && fmt.Sprint(cm["id"]) == fmt.Sprint(old["id"]) && cm["resourceType"] == "Questionnaire" {
			out = append(out, replacement)
			continue
		}
		out = append(out, c)
	}
	return out
}

// collectAnswers indexes every answer in a response by its question's linkId, nested groups and answers included.
func collectAnswers(items []any, into map[string][]any) {
	for _, it := range items {
		im, _ := it.(map[string]any)
		if im == nil {
			continue
		}
		id, _ := im["linkId"].(string)
		for _, a := range listOf(im["answer"]) {
			into[id] = append(into[id], a)
			if am, _ := a.(map[string]any); am != nil {
				collectAnswers(listOf(am["item"]), into)
			}
		}
		collectAnswers(listOf(im["item"]), into)
	}
}

// unansweredRequired names a required, applicable question under this item with no answer.
func unansweredRequired(item map[string]any, answers map[string][]any) string {
	if item == nil || !enabled(item, answers) {
		return ""
	}
	id, _ := item["linkId"].(string)
	if item["required"] == true && item["type"] != "group" && item["type"] != "display" && len(answers[id]) == 0 {
		if t, _ := item["text"].(string); t != "" {
			return id + " (" + t + ")"
		}
		return id
	}
	for _, c := range listOf(item["item"]) {
		cm, _ := c.(map[string]any)
		if m := unansweredRequired(cm, answers); m != "" {
			return m
		}
	}
	return ""
}

// enabled evaluates an item's enableWhen against the answers given, by enableBehavior (all when absent with one condition).
func enabled(item map[string]any, answers map[string][]any) bool {
	conds := listOf(item["enableWhen"])
	if len(conds) == 0 {
		return true
	}
	anyOf := item["enableBehavior"] == "any"
	for _, c := range conds {
		cm, _ := c.(map[string]any)
		ok := conditionHolds(cm, answers)
		if anyOf && ok {
			return true
		}
		if !anyOf && !ok {
			return false
		}
	}
	return !anyOf
}

func conditionHolds(c map[string]any, answers map[string][]any) bool {
	q, _ := c["question"].(string)
	op, _ := c["operator"].(string)
	given := answers[q]
	if op == "exists" {
		want, _ := c["answerBoolean"].(bool)
		return (len(given) > 0) == want
	}
	var want any
	var kind string
	for k, v := range c {
		if strings.HasPrefix(k, "answer") {
			want, kind = v, strings.TrimPrefix(k, "answer")
		}
	}
	if op == "!=" {
		for _, a := range given {
			if compareAnswer(a, kind, want) == 0 {
				return false
			}
		}
		return true
	}
	for _, a := range given {
		cmp := compareAnswer(a, kind, want)
		switch op {
		case "=":
			if cmp == 0 {
				return true
			}
		case ">":
			if cmp == 1 {
				return true
			}
		case "<":
			if cmp == -1 {
				return true
			}
		case ">=":
			if cmp == 0 || cmp == 1 {
				return true
			}
		case "<=":
			if cmp == 0 || cmp == -1 {
				return true
			}
		}
	}
	return false
}

// compareAnswer compares an answer with an enableWhen value of the same kind: -1, 0, 1, or 2 when they cannot be compared.
func compareAnswer(answer any, kind string, want any) int {
	am, _ := answer.(map[string]any)
	got, ok := am["value"+kind]
	if !ok {
		return 2
	}
	switch kind {
	case "Coding":
		g, _ := got.(map[string]any)
		w, _ := want.(map[string]any)
		if g["code"] == w["code"] && (w["system"] == nil || g["system"] == w["system"]) {
			return 0
		}
		return 2
	case "Quantity":
		g, _ := got.(map[string]any)
		w, _ := want.(map[string]any)
		if g["code"] != w["code"] || g["system"] != w["system"] {
			return 2
		}
		return order(fmt.Sprint(g["value"]), fmt.Sprint(w["value"]), true)
	case "Reference":
		g, _ := got.(map[string]any)
		w, _ := want.(map[string]any)
		if g["reference"] == w["reference"] {
			return 0
		}
		return 2
	case "Decimal", "Integer":
		return order(fmt.Sprint(got), fmt.Sprint(want), true)
	default: // Boolean, String, Date, DateTime, Time: same-precision ISO strings order lexically
		return order(fmt.Sprint(got), fmt.Sprint(want), false)
	}
}

func order(a, b string, numeric bool) int {
	if numeric {
		x, err1 := strconv.ParseFloat(a, 64)
		y, err2 := strconv.ParseFloat(b, 64)
		if err1 != nil || err2 != nil {
			return 2
		}
		switch {
		case x < y:
			return -1
		case x > y:
			return 1
		}
		return 0
	}
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
