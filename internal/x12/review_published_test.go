package x12

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The 278s CMS publishes in its esMD X12N 278 Companion Guide (a US government work, testdata/esmd; see README there) are read
// as their own text says. These are the first 278s behind this package written by someone else, and they found three faults:
// U, the level-of-service code for urgent, was never read as urgent; HCR01 C, cancelled, was reported as unknown; and a
// service line's UM overwrote its event's category.
func TestTheCMSPublished278sAreReadAsTheySay(t *testing.T) {
	cases := []struct {
		file, outcome, auth, reason string
		urgent                      bool
		response                    bool
	}{
		{"request-urgent.x12", "request", "", "", true, false},
		{"response-pended.x12", "pending", "", "0U", true, true},
		{"response-certified.x12", "certified", "UTN567", "", false, true},
		{"response-denied.x12", "denied", "", "13", false, true},
		{"response-modified.x12", "modified", "UTN3456", "", false, true},
		{"response-cancelled.x12", "cancelled", "", "0P", false, true},
		{"response-rejected.x12", "not-considered", "", "", false, true},
	}
	for _, c := range cases {
		raw, err := os.ReadFile(filepath.Join("testdata", "esmd", c.file))
		if err != nil {
			t.Fatal(err)
		}
		m, err := Parse(raw)
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		r, err := m.ParseServiceReview()
		if err != nil {
			t.Fatalf("%s: %v", c.file, err)
		}
		if r.IsResponse != c.response || len(r.Events) != 1 {
			t.Fatalf("%s: response %v, %d events", c.file, r.IsResponse, len(r.Events))
		}
		ev := r.Events[0]
		if string(ev.Outcome) != c.outcome || ev.AuthorisationNumber != c.auth || ev.ReasonCode != c.reason || ev.Urgent != c.urgent {
			t.Errorf("%s: outcome %s auth %q reason %q urgent %v; want %s %q %q %v", c.file, ev.Outcome, ev.AuthorisationNumber,
				ev.ReasonCode, ev.Urgent, c.outcome, c.auth, c.reason, c.urgent)
		}
	}

	// Two service lines, each certified with its own number.
	raw, _ := os.ReadFile(filepath.Join("testdata", "esmd", "response-lines.x12"))
	m, _ := Parse(raw)
	r, _ := m.ParseServiceReview()
	if len(r.Events) != 1 || len(r.Events[0].Lines) != 2 || r.Events[0].Lines[1].AuthorisationNumber != "UTN980" {
		t.Fatalf("lines: %+v", r.Events)
	}
}

// An admission review (UM01 AR) whose surgery is a service line with its own UM01 HS is still an admission review.
func TestAServiceLineDoesNotChangeItsEventsCategory(t *testing.T) {
	raw := "ISA*00*          *00*          *ZZ*CLINIC         *ZZ*PAYER          *261020*1200*^*00501*000000001*0*T*:~" +
		"GS*HI*CLINIC*PAYER*20261020*1200*1*X*005010X217~ST*278*0001*005010X217~BHT*0007*13*R1*20261020*1200~" +
		"HL*1**20*1~NM1*X3*2*PAYER*****PI*12345~HL*2*1*21*1~NM1*1P*2*CLINIC*****XX*1234567893~" +
		"HL*3*2*22*1~NM1*IL*1*DOE*JANE****MI*M1~HL*4*3*EV*1~TRN*1*T1*9CLINIC0000~UM*AR*I*2*21:B**E~DTP*435*D8*20261102~" +
		"HL*5*4*SS*0~UM*HS*I*2~SV2**HC:33510~SE*16*0001~GE*1*1~IEA*1*000000001~"
	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.ParseServiceReview()
	if err != nil {
		t.Fatal(err)
	}
	if ev := r.Events[0]; ev.Category != "AR" || ev.ServiceType != "2" || ev.Urgent {
		t.Fatalf("event %+v: want AR, service type 2, elective", ev)
	}
	if p := r.Events[0].Lines[0].ProcedureCode; p != "HC:33510" {
		t.Errorf("an SV2 line's procedure read as %q, not SV202", p)
	}
}

// A 278 request carries what payers' companion guides ask for, in the TR3's order: UM04 place of service, UM06 level of
// service (U for urgent, never 03, which is emergency), the admission and discharge dates of an admission review, DTP before
// HI, the service provider's name loop last in the event, and an institutional service as SV2.
func TestA278RequestCarriesWhatPayersAskFor(t *testing.T) {
	req := ServiceReviewRequest{
		Envelope:   Envelope{SenderID: "CLINIC", ReceiverID: "PAYER", Now: time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)},
		Payer:      Person{LastName: "PAYER", ID: "12345"},
		Provider:   Person{LastName: "CLINIC", ID: "1234567893"},
		Subscriber: Person{LastName: "DOE", FirstName: "JANE", ID: "M1"},
		Events: []ReviewRequestEvent{
			{Trace: "T1", Category: "AR", ServiceType: "2", Location: "21", LevelOfService: "E", AdmissionDate: "20261102",
				DischargeDate: "20261105", Diagnoses: []string{"C18.9"}, Procedure: "33510", Institutional: true,
				Provider: Person{LastName: "WATSON", FirstName: "SUSAN", ID: "1245319599"}},
			{Trace: "T2", Category: "SC", ServiceType: "3", Location: "11", Urgent: true, ServiceDate: "20261021"},
		},
	}
	raw, err := BuildServiceReviewRequest(req)
	if err != nil {
		t.Fatal(err)
	}
	got := string(raw)
	for _, want := range []string{
		"~UM*AR*I*2*21:B**E~DTP*435*D8*20261102~DTP*096*D8*20261105~HI*ABK:C189~NM1*SJ*1*WATSON*SUSAN****XX*1245319599~HL*5*4*SS*0~UM*HS*I*2~SV2**HC:33510**UN*1~",
		"~UM*SC*I*3*11:B**U~DTP*AAH*D8*20261021~",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s in\n%s", want, strings.ReplaceAll(got, "~", "~\n"))
		}
	}
	if strings.Contains(got, "**03") {
		t.Errorf("urgent sent as 03 (emergency)")
	}

	// And it reads back as it was meant.
	m, _ := Parse(raw)
	r, _ := m.ParseServiceReview()
	if r.Events[0].Category != "AR" || r.Events[0].Urgent || !r.Events[1].Urgent {
		t.Fatalf("read back: %+v", r.Events)
	}

	bad := req
	bad.Events = []ReviewRequestEvent{{Trace: "T1", ServiceType: "1", LevelOfService: "X"}}
	if _, err := BuildServiceReviewRequest(bad); err == nil {
		t.Error("a level of service that is not E, U or 03 was sent")
	}
}
