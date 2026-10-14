package rtpb

import (
	"encoding/xml"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/formulary"
)

func load(t *testing.T) (*formulary.File, *Request) {
	t.Helper()
	f, err := os.Open("../../examples/formulary/formulary.yaml")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	file, err := formulary.Load(f)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("testdata/request.xml")
	if err != nil {
		t.Fatal(err)
	}
	req, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return file, req
}

func ask(t *testing.T, file *formulary.File, req *Request, o Options, ndc string, qty string, days string) Answer {
	t.Helper()
	body := *req.Body.Request
	body.RequestedProduct.Product.DrugCoded.NDC = ndc
	q := *body.RequestedProduct.Quantity
	q.Value = qty
	body.RequestedProduct.Quantity = &q
	body.RequestedProduct.DaysSupply = days
	return o.Answer(file, &body)
}

func TestPrices(t *testing.T) {
	file, req := load(t)
	o := Options{}

	// Lovenox: prior authorization, a quantity limit, $45 copay, a known plan cost.
	a := ask(t, file, req, o, "00075062001", "30", "30")
	p := a.Requested
	if !a.Processed || p.Status != StatusCoveredRestrictions || strings.Join(p.Restrictions, ",") != RestrictionPriorAuth {
		t.Fatalf("lovenox: %+v", p)
	}
	if p.PatientPays == nil || *p.PatientPays != 45 || p.PlanCost == nil || *p.PlanCost != 365 {
		t.Errorf("lovenox costs %v %v", p.PatientPays, p.PlanCost)
	}

	// Over the limit as well.
	if p := ask(t, file, req, o, "00075062001", "90", "30").Requested; strings.Join(p.Restrictions, ",") != "75,76" {
		t.Errorf("over the limit: %v %s", p.Restrictions, p.Message)
	}

	// With an approval on file the restriction is AB8 and the drug is covered outright.
	o2 := Options{PAOnFile: func(member, rx string) bool { return member == "MBR1001" && rx == "854236" }}
	if p := ask(t, file, req, o2, "00075062001", "30", "30").Requested; p.Status != StatusCovered || strings.Join(p.Restrictions, ",") != RestrictionPAOnFile {
		t.Errorf("approval on file: %+v", p)
	}

	// Humira: 25% coinsurance of a stated cost.
	if p := ask(t, file, req, o, "00074055402", "2", "28").Requested; p.PatientPays == nil || *p.PatientPays != 1615.13 {
		t.Errorf("humira coinsurance: %+v", p)
	}

	// Invokana: step therapy, with metformin as the cheaper alternative.
	a = ask(t, file, req, o, "50458-0140-30", "30", "30")
	if strings.Join(a.Requested.Restrictions, ",") != RestrictionStepTherapy || len(a.Alternatives) != 1 || a.Alternatives[0].RxNorm != "860975" ||
		*a.Alternatives[0].PatientPays != 10 || a.Alternatives[0].Status != StatusCovered {
		t.Errorf("invokana: %+v %+v", a.Requested, a.Alternatives)
	}

	// A 90-day supply of metformin by mail is its mail copay.
	body := *req.Body.Request
	ph := *body.Pharmacy
	ph.PharmacyType = "M"
	body.Pharmacy = &ph
	body.RequestedProduct.Product.DrugCoded.NDC = "00378035205"
	body.RequestedProduct.DaysSupply = "90"
	if p := o.Answer(file, &body).Requested; p.Benefit != "3-month-in-mail" || *p.PatientPays != 20 {
		t.Errorf("metformin by mail: %+v", p)
	}
	// And Lovenox, which is retail only, is restricted by mail.
	body.RequestedProduct.Product.DrugCoded.NDC = "00075062001"
	if p := o.Answer(file, &body).Requested; !strings.Contains(strings.Join(p.Restrictions, ","), RestrictionDaysSupply) {
		t.Errorf("lovenox by mail: %+v", p)
	}

	// Excluded, unknown and unpriceable.
	if p := ask(t, file, req, o, "63481062370", "30", "30").Requested; p.Status != StatusNotCovered || p.Restrictions[0] != RestrictionNotCovered {
		t.Errorf("percocet: %+v", p)
	}
	if p := ask(t, file, req, o, "00000000001", "30", "30").Requested; p.Status != StatusNotCovered {
		t.Errorf("not listed: %+v", p)
	}
	if a := ask(t, file, req, o, "00075062001", "0", "30"); a.Processed || a.Reject != RejectQuantity {
		t.Errorf("no quantity: %+v", a)
	}
	body = *req.Body.Request
	body.BenefitsCoordination.PBMMemberID = "NOBODY"
	if a := o.Answer(file, &body); a.Processed || a.Reject != RejectCardholder {
		t.Errorf("unknown member: %+v", a)
	}
	body.BenefitsCoordination.GroupID = "GRP-EXAMPLE-1"
	if a := o.Answer(file, &body); !a.Processed || a.Plan != "GOLD2026" {
		t.Errorf("found by group: %+v", a)
	}
	body.BenefitsCoordination.GroupID = ""
	o3 := Options{Member: func(id string) string { return map[string]string{"NOBODY": "GOLD2026"}[id] }}
	if a := o3.Answer(file, &body); !a.Processed {
		t.Errorf("found through Coverage: %+v", a)
	}
}

// The response is well-formed XML that quotes the request's MessageID and carries the answer.
func TestTheResponseIsWellFormed(t *testing.T) {
	file, req := load(t)
	a := Options{}.Answer(file, req.Body.Request)
	out := Respond(req, a, "EXAMPLEPBM01", time.Date(2026, 10, 14, 12, 0, 0, 0, time.UTC))
	var back struct {
		Header Header `xml:"RTPBHeader"`
		Body   struct {
			Response struct {
				Processed *struct{ Note string } `xml:"Response>Processed"`
				Product   struct {
					NDC      string   `xml:"Product>DrugCoded>NDC"`
					Status   string   `xml:"PricingAndCoverages>PricingAndCoverage>CoverageStatusCode"`
					Pays     string   `xml:"PricingAndCoverages>PricingAndCoverage>EstimatedPatientFinancialResponsibility"`
					Restrict []string `xml:"PricingAndCoverages>PricingAndCoverage>CoverageRestriction>CoverageRestrictionCode"`
				} `xml:"ResponseProduct"`
			} `xml:"RTPBResponse"`
		} `xml:"RTPBBody"`
	}
	if err := xml.Unmarshal(out, &back); err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	r := back.Body.Response
	if back.Header.RelatesToMessageID != "RTPBREQ-TEST-1" || back.Header.To.Primary.Identification != "D00000000000001" || r.Processed == nil ||
		r.Product.NDC != "00075062001" || r.Product.Status != "CR" || r.Product.Pays != "45.00" || r.Product.Restrict[0] != "75" {
		t.Errorf("%+v\n%s", back, out)
	}

	body := *req.Body.Request
	body.BenefitsCoordination.PBMMemberID = "NOBODY & CO"
	out = Respond(req, Options{}.Answer(file, &body), "EXAMPLEPBM01", time.Now())
	if !strings.Contains(string(out), "<RejectCode>52</RejectCode>") {
		t.Errorf("%s", out)
	}
	if err := xml.Unmarshal(out, new(struct{})); err != nil {
		t.Error(err)
	}
}

func TestParseRefusesWhatIsNotARequest(t *testing.T) {
	for _, bad := range []string{"", "<x/>", "<Message><RTPBHeader><MessageID>1</MessageID></RTPBHeader></Message>",
		"<Message><RTPBBody><RTPBRequest/></RTPBBody></Message>"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("%q was accepted", bad)
		}
	}
}

func FuzzParse(f *testing.F) {
	raw, _ := os.ReadFile("testdata/request.xml")
	f.Add(raw)
	fh, err := os.Open("../../examples/formulary/formulary.yaml")
	if err != nil {
		f.Fatal(err)
	}
	file, err := formulary.Load(fh)
	fh.Close()
	if err != nil {
		f.Fatal(err)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		req, err := Parse(b)
		if err != nil {
			return
		}
		out := Respond(req, Options{}.Answer(file, req.Body.Request), "X", time.Now())
		if err := xml.Unmarshal(out, new(struct{})); err != nil {
			t.Fatalf("the response is not XML: %v\n%s", err, out)
		}
	})
}
