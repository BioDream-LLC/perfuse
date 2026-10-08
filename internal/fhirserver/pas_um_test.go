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
