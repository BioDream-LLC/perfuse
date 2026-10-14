package epa

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

// QuestionSet is a SCRIPT question set: the payer's questions, and in a request the prescriber's answers.
type QuestionSet struct {
	Header    QSHeader   `xml:"Header"`
	Questions []Question `xml:"Question"`
}

// QSHeader names a question set.
type QSHeader struct {
	ID                 string `xml:"QuestionSetID"`
	Title              string `xml:"QuestionSetTitle"`
	Description        string `xml:"QuestionSetDescription,omitempty"`
	AttachmentRequired string `xml:"AttachmentRequired,omitempty"`
}

// Question is one question.
type Question struct {
	ID          string       `xml:"QuestionID"`
	Sequence    int          `xml:"SequenceNumber"`
	Text        string       `xml:"QuestionText"`
	DefaultNext string       `xml:"DefaultNextQuestionID,omitempty"`
	Type        QuestionType `xml:"QuestionType"`
}

// QuestionType is the kind of answer a question takes; exactly one is set.
type QuestionType struct {
	Select   *SelectQ   `xml:"Select,omitempty"`
	FreeText *FreeTextQ `xml:"FreeText,omitempty"`
	Numeric  *NumericQ  `xml:"Numeric,omitempty"`
	Date     *DateQ     `xml:"Date,omitempty"`
}

// SelectQ is a choice.
type SelectQ struct {
	Multiple string   `xml:"SelectMultiple"`
	Choices  []Choice `xml:"Choice"`
	Answer   *Answer  `xml:"Answer,omitempty"`
}

// Choice is one option.
type Choice struct {
	ID                 string `xml:"ChoiceID"`
	Sequence           int    `xml:"SequenceNumber"`
	Text               string `xml:"ChoiceText"`
	AdditionalFreeText string `xml:"AdditionalFreeTextIndicator"`
	Next               string `xml:"NextQuestionID,omitempty"`
}

// FreeTextQ takes text.
type FreeTextQ struct {
	IsFreeText string  `xml:"IsFreeText"`
	Answer     *Answer `xml:"Answer,omitempty"`
}

// NumericQ takes a number.
type NumericQ struct {
	IsNumeric string  `xml:"IsNumeric"`
	Answer    *Answer `xml:"Answer,omitempty"`
}

// DateQ takes a date.
type DateQ struct {
	IsDateTimeRequired string  `xml:"IsDateTimeRequired"`
	Answer             *Answer `xml:"Answer,omitempty"`
}

// Answer is what the prescriber answered.
type Answer struct {
	Provided *Provided `xml:"SubmitterProvidedAnswer,omitempty"`
	Numeric  string    `xml:"SubmitterProvidedNumericAnswer,omitempty"`
}

// Provided is a text answer, the choices picked, or a date.
type Provided struct {
	Text     string   `xml:",chardata"`
	ChoiceID []string `xml:"ChoiceID"`
	Date     string   `xml:"Date"`
	DateTime string   `xml:"DateTime"`
}

// FromQuestionnaire turns a FHIR Questionnaire (R4, as JSON) into a question set, so the questions a prescriber answers in
// SCRIPT are the ones a DTR app asks. Groups are flattened in order; display items become part of the next question's text.
// enableWhen is not carried: SCRIPT branches by choice, not by condition, so every question is asked and the answers go to
// the reviewer and the decision service as they would from DTR.
func FromQuestionnaire(q map[string]any, id string, b func(bool) string) QuestionSet {
	qs := QuestionSet{Header: QSHeader{ID: id, Title: str(q["title"]), Description: str(q["description"]), AttachmentRequired: b(false)}}
	if qs.Header.Title == "" {
		qs.Header.Title = str(q["name"])
	}
	var pending string
	var walk func(items []any)
	walk = func(items []any) {
		for _, it := range items {
			item, _ := it.(map[string]any)
			switch t := str(item["type"]); t {
			case "group":
				walk(list(item["item"]))
				continue
			case "display":
				pending = strings.TrimSpace(pending + " " + str(item["text"]))
				continue
			case "attachment":
				qs.Header.AttachmentRequired = b(true)
			}
			text := str(item["text"])
			if pending != "" {
				text, pending = pending+" "+text, ""
			}
			question := Question{ID: str(item["linkId"]), Sequence: len(qs.Questions) + 1, Text: text}
			switch str(item["type"]) {
			case "boolean":
				question.Type.Select = &SelectQ{Multiple: b(false), Choices: []Choice{
					{ID: question.ID + "-yes", Sequence: 1, Text: "Yes", AdditionalFreeText: "NA"},
					{ID: question.ID + "-no", Sequence: 2, Text: "No", AdditionalFreeText: "NA"}}}
			case "choice", "open-choice":
				sel := &SelectQ{Multiple: b(item["repeats"] == true)}
				for i, o := range list(item["answerOption"]) {
					coding, _ := o.(map[string]any)["valueCoding"].(map[string]any)
					text := str(coding["display"])
					if text == "" {
						text = str(coding["code"])
					}
					if v := str(o.(map[string]any)["valueString"]); v != "" {
						text, coding = v, map[string]any{"code": v}
					}
					sel.Choices = append(sel.Choices, Choice{ID: question.ID + "-" + str(coding["code"]), Sequence: i + 1, Text: text,
						AdditionalFreeText: map[bool]string{true: "O", false: "NA"}[str(item["type"]) == "open-choice"]})
				}
				question.Type.Select = sel
			case "integer", "decimal", "quantity":
				question.Type.Numeric = &NumericQ{IsNumeric: b(true)}
			case "date", "dateTime":
				question.Type.Date = &DateQ{IsDateTimeRequired: b(str(item["type"]) == "dateTime")}
			default:
				question.Type.FreeText = &FreeTextQ{IsFreeText: b(true)}
			}
			qs.Questions = append(qs.Questions, question)
		}
	}
	walk(list(q["item"]))
	for i := range qs.Questions {
		qs.Questions[i].DefaultNext = "END"
		if i+1 < len(qs.Questions) {
			qs.Questions[i].DefaultNext = qs.Questions[i+1].ID
		}
	}
	return qs
}

// Generic is the question set asked for a drug whose formulary entry names no questionnaire: the clinical reason, in the
// prescriber's words, which a reviewer can decide on.
func Generic(id, drug string, b func(bool) string) QuestionSet {
	return QuestionSet{
		Header: QSHeader{ID: id, Title: "Prior authorization for " + drug, AttachmentRequired: b(false),
			Description: "Describe why this patient needs this drug. Attach supporting records if you have them."},
		Questions: []Question{
			{ID: "diagnosis", Sequence: 1, Text: "What is the patient's diagnosis for this drug?", DefaultNext: "tried",
				Type: QuestionType{FreeText: &FreeTextQ{IsFreeText: b(true)}}},
			{ID: "tried", Sequence: 2, Text: "Which drugs has the patient tried for this condition, and with what result?", DefaultNext: "reason",
				Type: QuestionType{FreeText: &FreeTextQ{IsFreeText: b(true)}}},
			{ID: "reason", Sequence: 3, Text: "Why is this drug needed rather than an alternative on the formulary?", DefaultNext: "END",
				Type: QuestionType{FreeText: &FreeTextQ{IsFreeText: b(true)}}},
		},
	}
}

// Answered is one answer, read back from a question set.
type Answered struct {
	ID       string `json:"id"`
	Question string `json:"question"`
	// Text is the answer in words: the choice's text, the number, the date, or what was written.
	Text string `json:"text"`
	// Kind is boolean, choice, number, date or text, and Value the typed value for the QuestionnaireResponse.
	Kind  string `json:"kind"`
	Value any    `json:"-"`
}

// Read returns the answers in a question set, in order. Questions left unanswered are not listed.
func (qs *QuestionSet) Read() []Answered {
	var out []Answered
	if qs == nil {
		return nil
	}
	qsorted := append([]Question(nil), qs.Questions...)
	sort.SliceStable(qsorted, func(i, j int) bool { return qsorted[i].Sequence < qsorted[j].Sequence })
	for _, q := range qsorted {
		a := Answered{ID: q.ID, Question: q.Text}
		switch t := q.Type; {
		case t.Select != nil && t.Select.Answer != nil && t.Select.Answer.Provided != nil:
			var texts []string
			for _, id := range t.Select.Answer.Provided.ChoiceID {
				for _, c := range t.Select.Choices {
					if c.ID == strings.TrimSpace(id) {
						texts = append(texts, c.Text)
					}
				}
			}
			if len(texts) == 0 {
				continue
			}
			a.Text = strings.Join(texts, "; ")
			if len(t.Select.Choices) == 2 && len(texts) == 1 && isYesNo(t.Select.Choices) {
				a.Kind, a.Value = "boolean", strings.EqualFold(texts[0], "yes")
			} else {
				a.Kind, a.Value = "choice", texts
			}
		case t.Numeric != nil && t.Numeric.Answer != nil:
			v := strings.TrimSpace(t.Numeric.Answer.Numeric)
			n, err := strconv.ParseFloat(v, 64)
			if err != nil {
				continue
			}
			a.Text, a.Kind, a.Value = v, "number", n
		case t.Date != nil && t.Date.Answer != nil && t.Date.Answer.Provided != nil:
			p := t.Date.Answer.Provided
			v := strings.TrimSpace(firstNonEmpty(p.Date, p.DateTime, p.Text))
			if v == "" {
				continue
			}
			a.Text, a.Kind, a.Value = v, "date", v
		case t.FreeText != nil && t.FreeText.Answer != nil && t.FreeText.Answer.Provided != nil:
			v := strings.TrimSpace(t.FreeText.Answer.Provided.Text)
			if v == "" {
				continue
			}
			a.Text, a.Kind, a.Value = v, "text", v
		default:
			continue
		}
		out = append(out, a)
	}
	return out
}

func isYesNo(c []Choice) bool {
	return strings.EqualFold(c[0].Text, "yes") && strings.EqualFold(c[1].Text, "no")
}

// AnswersResponse turns answers into a FHIR QuestionnaireResponse for the PAS request, so a decision service reads the
// prescriber's answers where it reads DTR's.
func AnswersResponse(answers []Answered, questionnaire string, patientRef string, now time.Time) map[string]any {
	var items []any
	for _, a := range answers {
		var ans []any
		switch a.Kind {
		case "boolean":
			ans = []any{map[string]any{"valueBoolean": a.Value}}
		case "number":
			ans = []any{map[string]any{"valueDecimal": a.Value}}
		case "date":
			v := a.Value.(string)
			if len(v) > 10 {
				ans = []any{map[string]any{"valueDateTime": v}}
			} else {
				ans = []any{map[string]any{"valueDate": v}}
			}
		case "choice":
			for _, t := range a.Value.([]string) {
				ans = append(ans, map[string]any{"valueString": t})
			}
		default:
			ans = []any{map[string]any{"valueString": a.Value}}
		}
		items = append(items, map[string]any{"linkId": a.ID, "text": a.Question, "answer": ans})
	}
	qr := map[string]any{"resourceType": "QuestionnaireResponse", "status": "completed",
		"subject": map[string]any{"reference": patientRef}, "authored": now.UTC().Format(time.RFC3339), "item": items}
	if questionnaire != "" {
		qr["questionnaire"] = questionnaire
	}
	return qr
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func list(v any) []any {
	l, _ := v.([]any)
	return l
}

var _ = fmt.Sprint
