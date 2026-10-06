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
// profile). Each $next-question call then shows every question that applies, in order, as far as the answers given allow: it stops
// at the first question whose condition depends on an answer not yet given. The response is completed when a call adds nothing and
// nothing waits on an answer. Questions that do not apply are never shown.
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
//
// questionnaireAdaptive says where $next-question is served. A payer that wrote it as true (adaptive, served here) gets it
// written as this server's base URL, which is the form DTR's adaptive profiles require.
func (s *Server) adaptiveShell(q map[string]any, profile string) map[string]any {
	out := map[string]any{}
	for k, v := range q {
		if k != "item" && k != "meta" && k != "text" {
			out[k] = v
		}
	}
	var ext []any
	for _, e := range listOf(q["extension"]) {
		em, _ := e.(map[string]any)
		if em["url"] == sdcAdaptive && em["valueBoolean"] == true && s.BaseURL != "" {
			e = map[string]any{"url": sdcAdaptive, "valueUrl": strings.TrimRight(s.BaseURL, "/")}
		}
		ext = append(ext, e)
	}
	if ext != nil {
		out["extension"] = ext
	}
	out["meta"] = map[string]any{"profile": []string{dtrBase + profile + "|" + dtrVersion}}
	return out
}

// handleNextQuestion is $next-question. The body is DTR's Parameters, or - as FHIR allows for an operation whose only input is one
// resource - the QuestionnaireResponse itself.
func (s *Server) handleNextQuestion(w http.ResponseWriter, r *http.Request) {
	body, ok := s.readBody(w, r)
	if !ok {
		return
	}
	var probe struct {
		ResourceType string     `json:"resourceType"`
		Parameter    []dtrParam `json:"parameter"`
	}
	if json.Unmarshal(body, &probe) != nil {
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure", "the body is not JSON")
		return
	}
	var raw json.RawMessage
	switch probe.ResourceType {
	case "QuestionnaireResponse":
		raw = body
	case "Parameters":
		for _, p := range probe.Parameter {
			if p.Name == "questionnaire-response" {
				raw = p.Resource
			}
		}
	default:
		s.writeOutcome(w, r, http.StatusBadRequest, fhir.SeverityError, "structure",
			"the body must be a Parameters resource or a QuestionnaireResponse")
		return
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
	if problem := checkAnswers(listOf(qr["item"]), questionIndex(listOf(contained["item"]), map[string]map[string]any{})); problem != "" {
		return nil, http.StatusBadRequest, "the QuestionnaireResponse does not fit its contained Questionnaire: " + problem
	}
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

	items, stopped := projectAdaptive(listOf(source["item"]), answers)
	grew := countItems(items) > countItems(listOf(contained["item"]))

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
	q["item"] = items
	if !grew && !stopped {
		out["status"] = "completed" // every question that applies has been asked, and none waits on an answer
	} else {
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

// projectAdaptive is the part of the questionnaire to show now: every item, nested ones included, whose enableWhen holds,
// in order, up to the first item whose enableWhen depends on a question not answered yet. That item cannot be decided, so
// the walk stops there and stopped is true; the next call, with that answer, goes further.
func projectAdaptive(items []any, answers map[string][]any) (out []any, stopped bool) {
	for _, it := range items {
		im, _ := it.(map[string]any)
		if im == nil {
			continue
		}
		if waitsOnAnswer(im, answers) {
			return out, true
		}
		if !enabled(im, answers) {
			continue
		}
		copied := map[string]any{}
		for k, v := range im {
			copied[k] = v
		}
		if children := listOf(im["item"]); len(children) > 0 {
			kids, stop := projectAdaptive(children, answers)
			if len(kids) > 0 {
				copied["item"] = kids
			} else {
				delete(copied, "item")
			}
			if stop {
				if len(kids) > 0 || im["type"] != "group" {
					out = append(out, copied)
				}
				return out, true
			}
		}
		out = append(out, copied)
	}
	return out, false
}

// waitsOnAnswer reports whether an item's enableWhen names a question that has no answer yet.
func waitsOnAnswer(item map[string]any, answers map[string][]any) bool {
	for _, c := range listOf(item["enableWhen"]) {
		cm, _ := c.(map[string]any)
		q, _ := cm["question"].(string)
		if cm["operator"] != "exists" && len(answers[q]) == 0 {
			return true
		}
	}
	return false
}

func countItems(items []any) int {
	n := 0
	for _, it := range items {
		n++
		if im, _ := it.(map[string]any); im != nil {
			n += countItems(listOf(im["item"]))
		}
	}
	return n
}

// questionIndex lists a questionnaire's items by linkId, nested ones included.
func questionIndex(items []any, into map[string]map[string]any) map[string]map[string]any {
	for _, it := range items {
		if im, _ := it.(map[string]any); im != nil {
			id, _ := im["linkId"].(string)
			into[id] = im
			questionIndex(listOf(im["item"]), into)
		}
	}
	return into
}

// answerKind is the value[x] an item type takes.
var answerKind = map[string]string{
	"boolean": "valueBoolean", "decimal": "valueDecimal", "integer": "valueInteger", "date": "valueDate",
	"dateTime": "valueDateTime", "time": "valueTime", "string": "valueString", "text": "valueString",
	"url": "valueUri", "choice": "valueCoding", "open-choice": "", "attachment": "valueAttachment",
	"reference": "valueReference", "quantity": "valueQuantity",
}

// checkAnswers says what in a response breaks its questionnaire's rules: a question it does not have, an answer to a group or
// display item, an answer of the wrong type, or a choice outside the options offered. Empty when nothing does.
func checkAnswers(items []any, questions map[string]map[string]any) string {
	for _, it := range items {
		im, _ := it.(map[string]any)
		if im == nil {
			continue
		}
		id, _ := im["linkId"].(string)
		q := questions[id]
		if q == nil {
			return "it answers " + id + ", which is not a question in the Questionnaire"
		}
		typ, _ := q["type"].(string)
		answers := listOf(im["answer"])
		if len(answers) > 0 && (typ == "group" || typ == "display") {
			return id + " is a " + typ + " item and cannot have an answer"
		}
		if len(answers) > 1 && q["repeats"] != true {
			return id + " does not repeat but has " + strconv.Itoa(len(answers)) + " answers"
		}
		for _, a := range answers {
			am, _ := a.(map[string]any)
			if want := answerKind[typ]; want != "" && am[want] == nil {
				return id + " is a " + typ + " question, so its answer must be " + want
			}
			if typ == "choice" && len(listOf(q["answerOption"])) > 0 && !offered(q, am["valueCoding"]) {
				return id + "'s answer is not one of the options it offers"
			}
			if m := checkAnswers(listOf(am["item"]), questions); m != "" {
				return m
			}
		}
		if m := checkAnswers(listOf(im["item"]), questions); m != "" {
			return m
		}
	}
	return ""
}

func offered(q map[string]any, coding any) bool {
	c, _ := coding.(map[string]any)
	for _, o := range listOf(q["answerOption"]) {
		om, _ := o.(map[string]any)
		vc, _ := om["valueCoding"].(map[string]any)
		if vc != nil && vc["code"] == c["code"] && (vc["system"] == nil || vc["system"] == c["system"]) {
			return true
		}
	}
	return false
}
