package fhirserver

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/x12"
)

// fakeUM is a utilization management system: it reads each 278 request it is sent and answers every event by its service type.
type fakeUM struct {
	got     []*x12.ServiceReview
	answers map[string]string // service type -> HCR01*HCR02*HCR03, or "AAA:<code>:<followup>"
	fail    bool
}

func (u *fakeUM) send(_ context.Context, raw []byte) ([]byte, error) {
	if u.fail {
		return nil, errors.New("connection refused")
	}
	m, err := x12.Parse(raw)
	if err != nil {
		return nil, err
	}
	review, err := m.ParseServiceReview()
	if err != nil {
		return nil, err
	}
	u.got = append(u.got, review)
	return respond278(review, u.answers), nil
}

// respond278 writes a 278 response answering each event of a request.
func respond278(review *x12.ServiceReview, answers map[string]string) []byte {
	segs := []string{"ST*278*0001*005010X217", "BHT*0007*11*" + review.ReferenceID + "*20261008*1200",
		"HL*1**20*1", "NM1*X3*2*ACME*****PI*12345", "HL*2*1*21*1", "NM1*1P*2*CLINIC*****XX*8189991234",
		"HL*3*2*22*1", "NM1*IL*1*MEMBER*****MI*12345678901"}
	for i, ev := range review.Events {
		segs = append(segs, fmt.Sprintf("HL*%d*3*EV*0", i+4), "TRN*2*"+ev.TraceNumber+"*9PAYER", "UM*HS*I*"+ev.ServiceType)
		a := answers[ev.ServiceType]
		if strings.HasPrefix(a, "AAA:") {
			p := strings.Split(a, ":")
			segs = append(segs, "AAA*N**"+p[1]+"*"+p[2])
		} else if a != "" {
			segs = append(segs, "HCR*"+a)
		}
	}
	segs = append(segs, fmt.Sprintf("SE*%d*0001", len(segs)+1))
	out := "ISA*00*          *00*          *ZZ*PAYER          *ZZ*PERFUSE        *261008*1200*^*00501*000000002*0*T*:~" +
		"GS*HI*PAYER*PERFUSE*20261008*1200*2*X*005010X217~" + strings.Join(segs, "~") + "~GE*1*2~IEA*1*000000002~"
	return []byte(out)
}

func newUMFixture(t *testing.T, um *fakeUM) *subFixture {
	t.Helper()
	f := newPASFixture(t)
	srv := NewServer(f.store, "http://example.test/fhir", nil)
	srv.Auth = OpenAuth{}
	srv.PAS = &PAS{Decide: pasRules, Now: newPASFixtureClock, UM: &PASUM{Send: um.send, SenderID: "PERFUSE", ReceiverID: "PAYER", PayerID: "12345"}}
	f.h = srv.Handler()
	return f
}

// The UM system decides, not the local rules: consultation (3) is approved by the rules but denied here, and the number the
// provider bills against is the UM system's.
func TestAForwardedRequestIsDecidedByTheUMSystem(t *testing.T) {
	um := &fakeUM{answers: map[string]string{"3": "A3**AA", "73": "A1*UM778899", "2": "A4"}}
	f := newUMFixture(t, um)
	code, b := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("U1", "3", "73", "2"))
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, b)
	}
	if len(um.got) != 1 || len(um.got[0].Events) != 3 {
		t.Fatalf("one 278 with three events expected: %+v", um.got)
	}
	ev := um.got[0].Events[0]
	if um.got[0].Patient.IDCode != "12345678901" || um.got[0].Requester.IDCode != "8189991234" || ev.ServiceType != "3" || ev.Category != "HS" {
		t.Errorf("278 content: patient %q requester %q event %+v", um.got[0].Patient.IDCode, um.got[0].Requester.IDCode, ev)
	}
	cr := claimResponseOf(t, b)
	if got := actionCodes(cr); got != "A3,A1,A4" {
		t.Fatalf("decisions %s, want the UM system's A3,A1,A4", got)
	}
	if !strings.Contains(toJSON(cr), "UM778899") || cr["preAuthRef"] != "UM778899" {
		t.Errorf("the UM system's authorization number is not the one given: %v", cr["preAuthRef"])
	}
	if !strings.Contains(toJSON(cr["processNote"]), "X12 reason AA") {
		t.Errorf("the denial reason: %v", cr["processNote"])
	}

	// Later, the UM system decides the pended surgery; its 278 response goes to $decide-278.
	later := respond278(&x12.ServiceReview{ReferenceID: "LATER", Events: []x12.ReviewEvent{
		{TraceNumber: um.got[0].Events[2].TraceNumber, ServiceType: "2"}, {TraceNumber: "NOPE-1", ServiceType: "2"}}},
		map[string]string{"2": "A1*UM990011"})
	rec := payerDo(t, f.h, "POST", "/Claim/$decide-278", string(later), map[string]string{"Content-Type": "application/edi-x12"})
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"decided"`) || !strings.Contains(rec.Body.String(), `"unknown"`) {
		t.Fatalf("$decide-278: %d %s", rec.Code, rec.Body)
	}
	id := str(cr["id"])
	_, now, _ := pasGet(t, f.h, "/ClaimResponse/"+id)
	if got := actionCodes(now); got != "A3,A1,A1" || !strings.Contains(toJSON(now), "UM990011") {
		t.Fatalf("after the later answer: %s %v", got, now)
	}
}

func TestARequestTheUMSystemCannotTakeIsPendedNotDecided(t *testing.T) {
	um := &fakeUM{fail: true}
	f := newUMFixture(t, um)
	_, b := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("U2", "3"))
	cr := claimResponseOf(t, b)
	if got := actionCodes(cr); got != "A4" {
		t.Fatalf("an unreachable UM system must pend, not fall back to local rules: %s", got)
	}
	if s := toJSON(cr["processNote"]); !strings.Contains(s, "did not answer") || strings.Contains(s, "refused") {
		t.Errorf("the provider is told it was pended, not the internal error: %s", s)
	}
}

func TestAUMRejectionIsPendedWithItsReason(t *testing.T) {
	um := &fakeUM{answers: map[string]string{"3": "AAA:72:C"}}
	f := newUMFixture(t, um)
	_, b := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("U3", "3"))
	cr := claimResponseOf(t, b)
	if actionCodes(cr) != "A4" || !strings.Contains(toJSON(cr["processNote"]), "reject reason 72 (follow-up C)") {
		t.Fatalf("%s %v", actionCodes(cr), cr["processNote"])
	}
}

// HCR01 C is a cancellation, said as one (C), not pended as an unknown code.
func TestAUMCancellationIsSaidAsOne(t *testing.T) {
	um := &fakeUM{answers: map[string]string{"3": "C**0P"}}
	f := newUMFixture(t, um)
	code, b := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("U9", "3"))
	if code != http.StatusOK {
		t.Fatalf("%d %v", code, b)
	}
	if got := actionCodes(claimResponseOf(t, b)); got != "C" {
		t.Fatalf("decision %s, want C (cancelled)", got)
	}
}

// ackUM answers every 278 with the acknowledgement it is given, or delivers it for later.
type ackUM struct {
	sent  [][]byte
	ack   func(controls string) string
	later bool
}

func (u *ackUM) send(_ context.Context, raw []byte) ([]byte, error) {
	u.sent = append(u.sent, raw)
	if u.later {
		return nil, ErrUMAnswersLater
	}
	m, _ := x12.Parse(raw)
	isa, _ := m.Segment("ISA", 1)
	return []byte(u.ack(isa.Element(13).String())), nil
}

func newAckFixture(t *testing.T, um *ackUM) (*subFixture, *Server) {
	t.Helper()
	f := newPASFixture(t)
	srv := NewServer(f.store, "http://example.test/fhir", nil)
	srv.Auth = OpenAuth{}
	srv.PAS = &PAS{Decide: pasRules, Now: newPASFixtureClock, UM: &PASUM{Send: um.send, SenderID: "PERFUSE", ReceiverID: "PAYER", PayerID: "12345"}}
	f.h = srv.Handler()
	return f, srv
}

const ackISA = "ISA*00*          *00*          *ZZ*PAYER          *ZZ*PERFUSE        *261008*1200*^*00501*000000077*0*T*:~"

// A UM system that answers a 278 with a TA1 or a 999 rejecting it has decided nothing: the items are pended, the provider is
// told the request could not be read, and the link's status records what the acknowledgement said. Each interchange goes out
// with its own control number, even two in the same second.
func TestAUMAcknowledgementInsteadOfAnAnswerPends(t *testing.T) {
	um := &ackUM{ack: func(c string) string {
		return ackISA + "TA1*" + c + "*261008*1200*R*006~IEA*0*000000077~"
	}}
	f, srv := newAckFixture(t, um)
	_, b := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("K1", "3"))
	cr := claimResponseOf(t, b)
	if actionCodes(cr) != "A4" || !strings.Contains(toJSON(cr["processNote"]), "refused the interchange (TA1 rejection)") {
		t.Fatalf("%s %v", actionCodes(cr), cr["processNote"])
	}
	um.ack = func(c string) string {
		n := strings.TrimLeft(c, "0")
		return ackISA + "GS*FA*PAYER*PERFUSE*20261008*1200*5*X*005010X231A1~ST*999*0001*005010X231A1~AK1*HI*" + n +
			"*005010X217~AK2*278*" + n[len(n)-4:] + "~IK3*NM1*8*2010C*8~IK4*9*67*7~IK5*R*5~AK9*R*1*1*0~SE*8*0001~GE*1*5~IEA*1*000000077~"
	}
	_, b = pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("K2", "3"))
	cr = claimResponseOf(t, b)
	if actionCodes(cr) != "A4" || !strings.Contains(toJSON(cr["processNote"]), "could not read the request (999 rejection)") {
		t.Fatalf("%s %v", actionCodes(cr), cr["processNote"])
	}
	m1, _ := x12.Parse(um.sent[0])
	m2, _ := x12.Parse(um.sent[1])
	i1, _ := m1.Segment("ISA", 1)
	i2, _ := m2.Segment("ISA", 1)
	if i1.Element(13).String() == i2.Element(13).String() {
		t.Errorf("two interchanges with control number %s", i1.Element(13))
	}
	st, err := srv.UMStatus(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if st.Counts["rejected"] != 2 || len(st.Recent) != 2 || !strings.Contains(st.Recent[0].Detail, "NM109 at segment 8 code 7") ||
		!strings.Contains(st.Recent[1].Detail, "TA1 note 006: invalid sender ID") {
		t.Fatalf("%+v", st)
	}
}

// Through a batch link the request is pended until the answer file comes; that file decides the item, and a 999 accepting it
// is what goes back. A 999 file rejecting an interchange is recorded against it.
func TestABatchAnswerDecidesLaterAndIsAcknowledged(t *testing.T) {
	um := &ackUM{later: true}
	f, srv := newAckFixture(t, um)
	_, b := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("B1", "3"))
	cr := claimResponseOf(t, b)
	if actionCodes(cr) != "A4" || !strings.Contains(toJSON(cr["processNote"]), "answers later") {
		t.Fatalf("%s %v", actionCodes(cr), cr["processNote"])
	}
	m, _ := x12.Parse(um.sent[0])
	review, _ := m.ParseServiceReview()
	answer := respond278(review, map[string]string{"3": "A1*UM5551212"})
	reply, summary, err := srv.ApplyUMFile(context.Background(), answer)
	if err != nil || !strings.Contains(summary, "decided") {
		t.Fatalf("%q %v", summary, err)
	}
	rm, err := x12.Parse(reply)
	if err != nil {
		t.Fatal(err)
	}
	ack, err := rm.ReadAcknowledgement()
	if err != nil || ack.Rejected() || ack.Kind != "999" || ack.Groups[0].ControlNumber != "2" {
		t.Fatalf("the 999 sent back: %+v %v\n%s", ack, err, reply)
	}
	_, now, _ := pasGet(t, f.h, "/ClaimResponse/"+str(cr["id"]))
	if actionCodes(now) != "A1" || !strings.Contains(toJSON(now), "UM5551212") {
		t.Fatalf("after the batch answer: %s", actionCodes(now))
	}

	isa, _ := m.Segment("ISA", 1)
	n := strings.TrimLeft(isa.Element(13).String(), "0")
	rejection := ackISA + "GS*FA*PAYER*PERFUSE*20261008*1200*6*X*005010X231A1~ST*999*0001*005010X231A1~AK1*HI*" + n +
		"*005010X217~AK9*R*1*1*0~SE*4*0001~GE*1*6~IEA*1*000000077~"
	if reply, _, err := srv.ApplyUMFile(context.Background(), []byte(rejection)); err != nil || reply != nil {
		t.Fatalf("%q %v", reply, err)
	}
	st, _ := srv.UMStatus(context.Background(), 10)
	if st.Counts["rejected"] != 1 {
		t.Fatalf("%+v", st)
	}
	if _, _, err := srv.ApplyUMFile(context.Background(), []byte("not x12")); err == nil {
		t.Error("garbage accepted")
	}
}
