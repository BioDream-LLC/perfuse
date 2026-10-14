package epa

import (
	"context"
	"database/sql"
	"encoding/xml"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	_ "modernc.org/sqlite"

	"github.com/biodream-llc/perfuse/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/formulary"
	"github.com/biodream-llc/perfuse/pas"
)

// rig is a payer: the sample formulary, a FHIR server with PAS and a rule that decides on the prescriber's answers, and the
// ePA service on top, with every unsolicited response it sends kept.
type rig struct {
	svc  *Service
	fhir *fhirserver.Server
	mu   sync.Mutex
	sent [][]byte
}

func newRig(t *testing.T) *rig {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })
	store, err := fhirserver.NewStore(db, fhir.R4)
	if err != nil {
		t.Fatal(err)
	}
	srv := fhirserver.NewServer(store, "http://payer.test/fhir", nil)
	srv.Auth = fhirserver.OpenAuth{}
	srv.PAS = &fhirserver.PAS{Decide: func(_ context.Context, req *pas.Request, _ pas.Service) fhirserver.PASAnswer {
		// The rule a decision service would apply, reading the answers the way it reads DTR's.
		text := ""
		for _, e := range req.Resources("QuestionnaireResponse") {
			for _, it := range pas.Slice(pas.Map(e["resource"])["item"]) {
				for _, a := range pas.Slice(pas.Map(it)["answer"]) {
					text += " " + pas.Str(pas.Map(a)["valueString"])
				}
			}
		}
		switch {
		case strings.Contains(text, "warfarin"):
			return fhirserver.PASAnswer{Decision: "approve", Why: "Warfarin was tried first."}
		case strings.Contains(text, "nothing"):
			return fhirserver.PASAnswer{Decision: "deny", Why: "The plan covers this drug after a trial of warfarin, and none is recorded."}
		}
		return fhirserver.PASAnswer{Decision: "pend", Why: "A pharmacist reviews this one."}
	}}
	f, err := os.Open("../../examples/formulary/formulary.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	file, err := formulary.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	r := &rig{fhir: srv}
	r.svc = &Service{Formulary: file, DB: db, PayerID: "EXAMPLEPBM01", Submit: srv.SubmitPAS,
		Review: func(ctx context.Context, id, decision, reason string) error {
			_, err := srv.Review(ctx, id, fhirserver.PASReview{Decision: decision, Reason: reason})
			return err
		},
		Questionnaire: func(ctx context.Context, url string) map[string]any {
			return srv.ByCanonical(ctx, "Questionnaire", url)
		},
		Send: func(_ context.Context, b []byte) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			r.sent = append(r.sent, b)
			return nil
		}}
	srv.PAS.Decided = r.svc.Decided
	return r
}

func fixture(t *testing.T) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/initiation.xml")
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// as rewrites the fixture into another transaction with another drug or member.
func as(raw []byte, element string, edits ...string) []byte {
	s := strings.ReplaceAll(string(raw), "PAInitiationRequest>", element+">")
	for i := 0; i+1 < len(edits); i += 2 {
		s = strings.ReplaceAll(s, edits[i], edits[i+1])
	}
	return []byte(s)
}

func handle(t *testing.T, r *rig, raw []byte) (*Message, *PA) {
	t.Helper()
	out, err := r.svc.Handle(context.Background(), raw)
	if err != nil {
		t.Fatal(err)
	}
	m, err := parseAny(out)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	_, pa, _ := m.Kind()
	return m, pa
}

// parseAny reads a response, which Parse also accepts.
func parseAny(b []byte) (*Message, error) { return Parse(b) }

// answer fills in the question set as a prescriber would: every question answered with text.
func answer(qs *QuestionSet, text string) string {
	var b strings.Builder
	b.WriteString("<Request><SolicitedModel><PACaseID>CASE</PACaseID><QuestionSet>")
	x, _ := xml.Marshal(qs.Header)
	b.Write([]byte(strings.Replace(string(x), "QSHeader", "Header", 2)))
	for _, q := range qs.Questions {
		q.Type.FreeText = &FreeTextQ{IsFreeText: "true", Answer: &Answer{Provided: &Provided{Text: text}}}
		q.Type.Select, q.Type.Numeric, q.Type.Date = nil, nil, nil
		x, _ := xml.Marshal(q)
		b.Write(x)
	}
	b.WriteString("</QuestionSet></SolicitedModel></Request>")
	return b.String()
}

func TestAnInitiationGetsTheQuestionsAndARequestIsDecided(t *testing.T) {
	r := newRig(t)
	m, pa := handle(t, r, fixture(t))
	if m.Header.RelatesToMessageID != "INIT-TEST-1" || m.Header.To.Value != "6842917100001" || m.Header.From.Value != "EXAMPLEPBM01" {
		t.Errorf("header %+v", m.Header)
	}
	open := pa.Response.Status.Open
	if open == nil || open.PACaseID == "" || open.QuestionSet == nil || len(open.QuestionSet.Questions) != 3 {
		t.Fatalf("expected an open case with the general questions: %+v", pa.Response.Status)
	}
	if pa.Patient.Human.Names.Name[0].LastName != "DOE" || pa.Medication.Product.DrugCoded.NDC != "00075062001" {
		t.Error("the response does not echo the patient and the drug")
	}
	caseID := open.PACaseID

	// A second initiation for the same drug while it is open is a duplicate.
	if _, pa := handle(t, r, fixture(t)); pa.Response.Status.Closed == nil || pa.Response.Status.Closed.ReasonCode != ClosedDuplicate {
		t.Errorf("duplicate: %+v", pa.Response.Status)
	}

	// The prescriber answers; the rule approves.
	req := as(fixture(t), "PARequest", "</MedicationPrescribed>", "</MedicationPrescribed>"+strings.ReplaceAll(answer(open.QuestionSet, "tried warfarin for 3 months"), "CASE", caseID))
	_, pa = handle(t, r, req)
	ap := pa.Response.Status.Approved
	if ap == nil || ap.PACaseID != caseID || ap.AuthorizationNumber == "" || ap.AuthorizationPeriod == nil {
		t.Fatalf("approval: %+v", pa.Response.Status)
	}
	c, err := r.svc.Get(context.Background(), caseID)
	if err != nil || c.Outcome != "approved" || c.PASID == "" || len(c.Answers) != 3 {
		t.Fatalf("case %+v %v", c, err)
	}
	// The PAS case is a drug, so it is held to the drug timeframe.
	pc, err := r.fhir.Case(context.Background(), c.PASID)
	if err != nil || !pc.Drug || pc.Due.Sub(pc.Created) != 72*time.Hour {
		t.Errorf("PAS case: drug %v, due in %v", pc.Drug, pc.Due.Sub(pc.Created))
	}

	// Now it is on file: initiation closes FQ, and RTPB will say AB8.
	if _, pa := handle(t, r, fixture(t)); pa.Response.Status.Closed == nil || pa.Response.Status.Closed.ReasonCode != ClosedActiveOnFile {
		t.Errorf("on file: %+v", pa.Response.Status)
	}
	if !r.svc.PAOnFile("MBR1001", "854236") {
		t.Error("PAOnFile does not see the approval")
	}
}

func TestAPendedRequestIsDecidedByAReviewerAndThePrescriberIsTold(t *testing.T) {
	r := newRig(t)
	_, pa := handle(t, r, fixture(t))
	caseID := pa.Response.Status.Open.PACaseID
	req := as(fixture(t), "PARequest", "</MedicationPrescribed>", "</MedicationPrescribed>"+strings.ReplaceAll(answer(pa.Response.Status.Open.QuestionSet, "please review"), "CASE", caseID))
	_, pa = handle(t, r, req)
	if o := pa.Response.Status.Open; o == nil || !strings.Contains(o.PANote, "under review") {
		t.Fatalf("pended: %+v", pa.Response.Status)
	}
	c, _ := r.svc.Get(context.Background(), caseID)
	if c.State != "submitted" {
		t.Fatalf("state %s", c.State)
	}
	if _, err := r.fhir.Review(context.Background(), c.PASID, fhirserver.PASReview{Decision: "deny", Reason: "No trial of warfarin is recorded."}); err != nil {
		t.Fatal(err)
	}
	if len(r.sent) != 1 {
		t.Fatalf("%d unsolicited responses", len(r.sent))
	}
	m, _ := Parse(r.sent[0])
	kind, rpa, _ := m.Kind()
	if kind != Response || rpa.Response.Status.Denied == nil || !strings.Contains(rpa.Response.Status.Denied.DenialReason, "warfarin") ||
		m.Header.To.Value != "6842917100001" {
		t.Fatalf("unsolicited: %s", r.sent[0])
	}
	c, _ = r.svc.Get(context.Background(), caseID)
	if c.Outcome != "denied" || !c.ReplySent {
		t.Errorf("case %+v", c)
	}

	// An appeal without a reason is asked for one; with one, it is decided again.
	appeal := as(fixture(t), "PAAppealRequest", "</MedicationPrescribed>", "</MedicationPrescribed><Request><PACaseID>"+caseID+"</PACaseID></Request>")
	if _, pa := handle(t, r, appeal); pa.Response.Status.Open == nil {
		t.Errorf("appeal without a note: %+v", pa.Response.Status)
	}
	appeal = as(fixture(t), "PAAppealRequest", "</MedicationPrescribed>", "</MedicationPrescribed><Request><PACaseID>"+caseID+
		"</PACaseID><PANote>Bleeding on warfarin in 2025; see chart.</PANote></Request>")
	m, pa = handle(t, r, appeal)
	if k, _, _ := m.Kind(); k != AppealResponse || pa.Response.Status.Open == nil {
		t.Fatalf("appeal: %s %+v", k, pa.Response.Status)
	}
	c, _ = r.svc.Get(context.Background(), caseID)
	var appealPAS string
	_ = r.svc.DB.QueryRow(`SELECT appeal_pas_id FROM epa_cases WHERE case_id = ?`, caseID).Scan(&appealPAS)
	if _, err := r.fhir.Review(context.Background(), appealPAS, fhirserver.PASReview{Decision: "approve", Reason: "Approved on appeal."}); err != nil {
		t.Fatal(err)
	}
	m, _ = Parse(r.sent[len(r.sent)-1])
	if k, apa, _ := m.Kind(); k != AppealResponse || apa.Response.Status.Approved == nil {
		t.Fatalf("appeal decision: %s", r.sent[len(r.sent)-1])
	}
	met, err := r.svc.Metrics(context.Background(), time.Now().Add(-time.Hour))
	if err != nil || met.Requests != 1 || met.Appealed != 1 || met.ApprovedOnAppeal != 1 {
		t.Errorf("metrics %+v %v", met, err)
	}
}

func TestWhatIsNotAPriorAuthorizationIsClosedWithTheReason(t *testing.T) {
	r := newRig(t)
	for _, c := range []struct {
		name  string
		edits []string
		code  string
	}{
		{"no prior authorization", []string{"00075062001", "00378035205"}, ClosedNotRequired},
		{"excluded", []string{"00075062001", "63481062370"}, ClosedNotCovered},
		{"not listed", []string{"00075062001", "00000000001"}, ClosedNotCovered},
		{"unknown member", []string{"MBR1001", "NOBODY", "GRP-EXAMPLE-1", "OTHER"}, ClosedNoPatient},
	} {
		_, pa := handle(t, r, as(fixture(t), "PAInitiationRequest", c.edits...))
		if cl := pa.Response.Status.Closed; cl == nil || cl.ReasonCode != c.code || cl.PANote == "" {
			t.Errorf("%s: %+v", c.name, pa.Response.Status)
		}
	}
}

func TestACaseCanBeCancelledWhileOpen(t *testing.T) {
	r := newRig(t)
	_, pa := handle(t, r, fixture(t))
	caseID := pa.Response.Status.Open.PACaseID
	cancel := as(fixture(t), "PACancelRequest", "</MedicationPrescribed>", "</MedicationPrescribed><Request><PACaseID>"+caseID+
		"</PACaseID><CancelReasonCode>B</CancelReasonCode></Request>")
	m, pa := handle(t, r, cancel)
	if k, _, _ := m.Kind(); k != CancelResponse || pa.Response.Status.Approved == nil {
		t.Fatalf("%s %+v", k, pa.Response.Status)
	}
	if _, pa := handle(t, r, cancel); pa.Response.Status.Denied == nil {
		t.Errorf("a second cancel: %+v", pa.Response.Status)
	}
}

// A questionnaire on file is the question set, so SCRIPT asks what DTR asks.
func TestTheDrugsQuestionnaireBecomesTheQuestionSet(t *testing.T) {
	r := newRig(t)
	q := `{"resourceType":"Questionnaire","id":"enox","url":"https://plan.example/fhir/Questionnaire/enoxaparin-pa","status":"active",
	  "title":"Enoxaparin prior authorization","item":[
	   {"linkId":"self","type":"boolean","text":"Is the patient self-administering at home?"},
	   {"linkId":"use","type":"choice","text":"What is it for?","answerOption":[{"valueCoding":{"code":"prophylaxis","display":"Prophylaxis"}},{"valueCoding":{"code":"dvt","display":"Treatment of DVT"}}]},
	   {"linkId":"g","type":"group","item":[{"linkId":"weight","type":"decimal","text":"Weight in pounds?"},{"linkId":"surgery","type":"date","text":"Date of surgery?"}]},
	   {"linkId":"warfarin","type":"text","text":"Result of warfarin use?"}]}`
	res, err := fhir.UnmarshalResource([]byte(q))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.fhir.Store.Put(context.Background(), res); err != nil {
		t.Fatal(err)
	}
	_, pa := handle(t, r, fixture(t))
	qs := pa.Response.Status.Open.QuestionSet
	if qs.Header.Title != "Enoxaparin prior authorization" || len(qs.Questions) != 5 {
		t.Fatalf("%+v", qs)
	}
	got := []string{}
	for _, q := range qs.Questions {
		switch {
		case q.Type.Select != nil:
			got = append(got, q.ID+":select"+string(rune('0'+len(q.Type.Select.Choices))))
		case q.Type.Numeric != nil:
			got = append(got, q.ID+":numeric")
		case q.Type.Date != nil:
			got = append(got, q.ID+":date")
		default:
			got = append(got, q.ID+":text")
		}
	}
	if strings.Join(got, " ") != "self:select2 use:select2 weight:numeric surgery:date warfarin:text" || qs.Questions[4].DefaultNext != "END" {
		t.Errorf("%v", got)
	}

	// Answers read back typed.
	qs.Questions[0].Type.Select.Answer = &Answer{Provided: &Provided{ChoiceID: []string{"self-yes"}}}
	qs.Questions[2].Type.Numeric.Answer = &Answer{Numeric: "164"}
	qs.Questions[3].Type.Date.Answer = &Answer{Provided: &Provided{Date: "2026-09-30"}}
	a := qs.Read()
	if len(a) != 3 || a[0].Value != true || a[1].Value != 164.0 || a[2].Value != "2026-09-30" {
		t.Errorf("%+v", a)
	}
}

func TestParseRefusesWhatIsNotOneTransaction(t *testing.T) {
	for _, bad := range []string{"", "<x/>", "<Message><Header/><Body/></Message>",
		"<Message><Header><MessageID>1</MessageID></Header><Body/></Message>",
		"<Message><Header><MessageID>1</MessageID></Header><Body><PARequest/><PACancelRequest/></Body></Message>"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func FuzzHandle(f *testing.F) {
	raw, _ := os.ReadFile("testdata/initiation.xml")
	f.Add(raw)
	f.Add(as(raw, "PARequest"))
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := Parse(b)
		if err != nil {
			return
		}
		_, pa, _ := m.Kind()
		_ = pa.CaseID()
		_ = pa.Answers().Read()
	})
}
