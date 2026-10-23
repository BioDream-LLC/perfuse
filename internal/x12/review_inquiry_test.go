package x12

import (
	"strings"
	"testing"
	"time"
)

func inquiryFixture() ServiceReviewInquiry {
	return ServiceReviewInquiry{
		Envelope:   Envelope{SenderID: "PERFUSE", ReceiverID: "PAYER", ControlNumber: 42, Now: time.Date(2026, 10, 23, 9, 30, 0, 0, time.UTC)},
		Payer:      Person{LastName: "ACME HEALTH PLAN", ID: "12345"},
		Provider:   Person{LastName: "CLINIC", ID: "8189991234"},
		Subscriber: Person{LastName: "FROST", FirstName: "ROBIN", ID: "W123456789", DOB: "19800214", Gender: "F"},
		Trace:      "INQ1",
		From:       "20260101", To: "20261023",
	}
}

func TestA278InquiryHasTheShapeThePayersGuidesPrint(t *testing.T) {
	out, err := BuildServiceReviewInquiry(inquiryFixture())
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{
		"GS*HI*PERFUSE*PAYER*20261023*0930*42*X*005010X215~",
		"ST*278*0042*005010X215~",
		"BHT*0007*28*", // query, as Cigna and Blue Cross Blue Shield of Massachusetts take it
		"HL*1**20*1~NM1*X3*2*ACME HEALTH PLAN*****PI*12345~",
		"HL*2*1*21*1~NM1*1P*2*CLINIC*****XX*8189991234~",
		"HL*3*2*22*1~NM1*IL*1*FROST*ROBIN****MI*W123456789~DMG*D8*19800214*F~",
		"HL*4*3*EV*0~TRN*1*INQ1*9PERFUSE00~UM*HS~DTP*AAH*RD8*20260101-20261023~",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in\n%s", want, s)
		}
	}
	m, err := Parse(out)
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.ParseServiceReview()
	if err != nil || !r.Inquiry || r.IsResponse || r.Purpose != "28" || len(r.Events) != 1 || r.Events[0].ServiceEndDate != "20261023" {
		t.Fatalf("read back: %+v %v", r, err)
	}
}

func TestA278InquiryByNumberAndForAnAdmission(t *testing.T) {
	q := inquiryFixture()
	q.Category, q.AuthorizationNumber, q.AdministrativeReference, q.To = "AR", "UM778899", "ADM-1", ""
	out, err := BuildServiceReviewInquiry(q)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(out); !strings.Contains(s, "UM*AR~REF*BB*UM778899~REF*NT*ADM-1~DTP*435*D8*20260101~") {
		t.Fatalf("admission by number:\n%s", s)
	}
}

func TestA278InquirySaysWhatIsMissing(t *testing.T) {
	for name, change := range map[string]func(*ServiceReviewInquiry){
		"subscriber member ID":                       func(q *ServiceReviewInquiry) { q.Subscriber.ID = "" },
		"subscriber name or date of birth":           func(q *ServiceReviewInquiry) { q.Subscriber.LastName, q.Subscriber.DOB = "", "" },
		"an authorization number or the event dates": func(q *ServiceReviewInquiry) { q.From, q.To = "", "" },
		"fails the NPI check digit":                  func(q *ServiceReviewInquiry) { q.Provider.ID = "1234567890" },
		"run backwards":                              func(q *ServiceReviewInquiry) { q.From, q.To = "20261023", "20260101" },
		"is not HS, AR or SC":                        func(q *ServiceReviewInquiry) { q.Category = "IN" },
	} {
		q := inquiryFixture()
		change(&q)
		if _, err := BuildServiceReviewInquiry(q); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// An inquiry's answer, in the shape UnitedHealthcare's and Blue Cross Blue Shield of Massachusetts' companion guides print
// (invented people and numbers): BHT02 52 or 49, the payer's trace beside the requester's, the number as HCR02 or only as REF BB,
// a provider loop whose own REF must not be read as the event's, and a certified period as RD8.
func TestA278InquiryAnswerIsRead(t *testing.T) {
	answer := "ISA*00*          *00*          *ZZ*PAYER          *ZZ*PERFUSE        *261023*0931*^*00501*000000043*0*T*:~" +
		"GS*HI*PAYER*PERFUSE*20261023*0931*43*X*005010X215~ST*278*0001*005010X215~BHT*0007*52*REF1*20261023*0931*18~" +
		"HL*1**20*1~NM1*X3*2*ACME HEALTH PLAN*****PI*12345~HL*2*1*21*1~NM1*1P*2*CLINIC*****XX*8189991234~" +
		"HL*3*2*22*1~NM1*IL*1*FROST*ROBIN****MI*W123456789~DMG*D8*19800214*F~" +
		"HL*4*3*EV*1~TRN*1*PAYERTRACE7*9PAYER~TRN*2*INQ1*9PERFUSE00~UM*HS*I*1~HCR*A1~REF*BB*FAX-2201~REF*NT*ADM-9~" +
		"DTP*AAH*RD8*20260301-20260531~NM1*SJ*1*REED*ALEX****XX*1234567893~REF*EI*376000511~" +
		"HL*5*4*SS*0~UM*HS~HCR*A1~DTP*472*RD8*20260301-20260531~SV1*HC:97110**UN*12~" +
		"HL*6*3*EV*0~TRN*1*PAYERTRACE8*9PAYER~UM*SC~HCR*A3**AA~SE*24*0001~GE*1*43~IEA*1*000000043~"
	m, err := Parse([]byte(answer))
	if err != nil {
		t.Fatal(err)
	}
	r, err := m.ParseServiceReview()
	if err != nil {
		t.Fatal(err)
	}
	if !r.IsResponse || !r.Inquiry || r.Purpose != "52" || len(r.Events) != 2 {
		t.Fatalf("%+v", r)
	}
	ev := r.Events[0]
	if ev.AuthorisationNumber != "FAX-2201" || ev.References["NT"] != "ADM-9" || ev.References["EI"] != "" {
		t.Errorf("numbers: %q %v", ev.AuthorisationNumber, ev.References)
	}
	if len(ev.TraceNumbers) != 2 || ev.TraceNumbers[0] != "PAYERTRACE7" || ev.ServiceDate != "20260301" || ev.ServiceEndDate != "20260531" {
		t.Errorf("traces and dates: %v %s-%s", ev.TraceNumbers, ev.ServiceDate, ev.ServiceEndDate)
	}
	if l := ev.Lines[0]; l.ProcedureQualifier != "HC" || l.Procedure != "97110" || l.Quantity != "12" || l.ServiceEndDate != "20260531" {
		t.Errorf("line: %+v", l)
	}
	if r.Events[1].Outcome != OutcomeDenied || r.Events[1].AuthorisationNumber != "" {
		t.Errorf("second: %+v", r.Events[1])
	}
	// Blue Cross Blue Shield of Massachusetts answers 49.
	if m2, _ := Parse([]byte(strings.Replace(answer, "BHT*0007*52", "BHT*0007*49", 1))); m2 != nil {
		if r2, _ := m2.ParseServiceReview(); r2 == nil || !r2.IsResponse {
			t.Error("49 is a response too")
		}
	}
}
