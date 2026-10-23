package fhirserver

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/x12"
)

// inquiryUM is a UM system that decides 278 requests like fakeUM and answers 278 inquiries from what it holds: one
// authorization asked for by fax (never through Perfuse), the one Perfuse forwarded, and an event it found nothing for.
type inquiryUM struct {
	fakeUM
	inquiries []*x12.ServiceReview
	down      bool
}

func (u *inquiryUM) send(ctx context.Context, raw []byte) ([]byte, error) {
	m, err := x12.Parse(raw)
	if err != nil {
		return nil, err
	}
	review, err := m.ParseServiceReview()
	if err != nil {
		return nil, err
	}
	if !review.Inquiry {
		return u.fakeUM.send(ctx, raw)
	}
	if u.down {
		return nil, errors.New("connection refused")
	}
	u.inquiries = append(u.inquiries, review)
	trace := review.Events[0].TraceNumber
	segs := []string{"ST*278*0001*005010X215", "BHT*0007*49*" + review.ReferenceID + "*20261008*1200*18",
		"HL*1**20*1", "NM1*X3*2*ACME*****PI*12345", "HL*2*1*21*1", "NM1*1P*2*CLINIC*****XX*8189991234",
		"HL*3*2*22*1", "NM1*IL*1*MEMBER*PAT****MI*12345678901", "DMG*D8*19800101*F",
		// Asked for by fax: certified, its number only in REF BB, twelve sessions of 97110 over three months.
		"HL*4*3*EV*1", "TRN*1*FAXTRACE1*9ACME", "TRN*2*" + trace + "*9PERFUSE00", "UM*HS*I*PT", "HCR*A1",
		"REF*BB*FAX-2201", "DTP*AAH*RD8*20261001-20261231",
		"HL*5*4*SS*0", "UM*HS", "HCR*A1", "SV1*HC:97110**UN*12",
		// The request Perfuse forwarded, which it already holds.
		"HL*6*3*EV*0", "TRN*1*UMOWN2*9ACME", "UM*HS*I*73", "HCR*A1*UM778899",
		// And one not found.
		"HL*7*3*EV*0", "TRN*2*" + trace + "*9PERFUSE00", "UM*HS", "AAA*Y**AA*N"}
	segs = append(segs, "SE*30*0001")
	out := "ISA*00*          *00*          *ZZ*PAYER          *ZZ*PERFUSE        *261008*1200*^*00501*000000003*0*T*:~" +
		"GS*HI*PAYER*PERFUSE*20261008*1200*3*X*005010X215~" + strings.Join(segs, "~") + "~GE*1*3~IEA*1*000000003~"
	return []byte(out), nil
}

func newInquiryFixture(t *testing.T, um *inquiryUM, inquire bool) *subFixture {
	t.Helper()
	f := newPASFixture(t)
	srv := NewServer(f.store, "http://example.test/fhir", nil)
	srv.Auth = OpenAuth{}
	srv.PAS = &PAS{Decide: pasRules, Now: newPASFixtureClock, UM: &PASUM{Send: um.send, SenderID: "PERFUSE", ReceiverID: "PAYER",
		PayerID: "12345", Inquire: inquire}}
	f.h = srv.Handler()
	return f
}

// The provider asks what is on file: what came through Perfuse is answered from its own records, and what the UM system holds
// beyond that (the fax) comes back too, once, with no Claim made up for it.
func TestInquiryAlsoFindsAuthorizationsAskedForAnotherWay(t *testing.T) {
	um := &inquiryUM{fakeUM: fakeUM{answers: map[string]string{"73": "A1*UM778899"}}}
	f := newInquiryFixture(t, um, true)
	if code, _ := pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("Q1", "73")); code != 200 {
		t.Fatalf("submit: %d", code)
	}
	code, out := pasPost(t, f.h, "/Claim/$inquire", pasRequestJSON("Q1"))
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if len(um.inquiries) != 1 {
		t.Fatalf("one 278 inquiry expected, got %d", len(um.inquiries))
	}
	q := um.inquiries[0]
	if q.Patient.IDCode != "12345678901" || q.Requester.IDCode != "8189991234" || q.Events[0].ServiceDate == "" ||
		q.Events[0].ServiceEndDate == "" || q.Purpose != "28" {
		t.Errorf("the inquiry sent: %+v %+v", q, q.Events[0])
	}
	params := asSliceAny(out["parameter"])
	if len(params) != 2 {
		t.Fatalf("Perfuse's own answer and the fax's expected, got %d: %s", len(params), toJSON(out))
	}
	fax := asMapAny(asMapAny(params[1])["resource"])
	cr := claimResponseOf(t, fax)
	j := toJSON(cr)
	if cr["preAuthRef"] != "FAX-2201" || actionCodes(cr) != "A1" || !strings.Contains(j, "97110") ||
		!strings.Contains(j, "data-absent-reason") || !strings.Contains(j, "2026-12-31") {
		t.Errorf("the fax's authorization: %s", j)
	}
	if !strings.Contains(toJSON(fax["meta"]), "profile-pas-inquiry-response-bundle") || !strings.Contains(j, "profile-claiminquiryresponse") {
		t.Errorf("profiles: %v", fax["meta"])
	}
	for _, e := range asSliceAny(fax["entry"]) {
		if asMapAny(asMapAny(e)["resource"])["resourceType"] == "Claim" {
			t.Error("no Claim may be made up for an authorization nobody submitted in FHIR")
		}
	}
	if strings.Contains(toJSON(out), "UMOWN2") {
		t.Error("the request Perfuse forwarded was returned twice")
	}
}

// The UM system down, or the link not asked to inquire: the provider still gets what Perfuse holds.
func TestInquiryAnswersFromItsOwnRecordsWhenTheUMSystemCannot(t *testing.T) {
	for name, tc := range map[string]struct {
		down, inquire bool
		sent          int
	}{"down": {true, true, 0}, "not asked": {false, false, 0}} {
		um := &inquiryUM{fakeUM: fakeUM{answers: map[string]string{"73": "A1*UM778899"}}, down: tc.down}
		f := newInquiryFixture(t, um, tc.inquire)
		pasPost(t, f.h, "/Claim/$submit", pasRequestJSON("Q2", "73"))
		code, out := pasPost(t, f.h, "/Claim/$inquire", pasRequestJSON("Q2"))
		if code != 200 || len(asSliceAny(out["parameter"])) != 1 || len(um.inquiries) != tc.sent {
			t.Errorf("%s: %d, %d returned, %d inquiries", name, code, len(asSliceAny(out["parameter"])), len(um.inquiries))
		}
	}
}
