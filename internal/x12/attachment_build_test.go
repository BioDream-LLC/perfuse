package x12

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"
)

func sampleAttachmentRequest() AttachmentRequest {
	return AttachmentRequest{
		SenderID: "PROVIDER01", ReceiverID: "PAYER01", ControlNumber: 1234,
		Reference: "REF-0001", TraceNumber: "ACN-778899",
		Payer:     AttachmentParty{Name: "EXAMPLE HEALTH PLAN", ID: "12345"},
		Submitter: AttachmentParty{Name: "EXAMPLE CLINIC", ID: "ETIN001"},
		Provider:  AttachmentParty{Name: "EXAMPLE CLINIC", ID: "1234567893"},
		Patient:   AttachmentParty{Name: "DOE", FirstName: "JANE", ID: "MEMBER001"},

		ProviderClaimID: "CLAIM-42", ServiceDate: "20261001",
		// A document containing every X12 delimiter, so a builder that forgot to count would corrupt the interchange.
		Document:    []byte("%PDF-1.4 ~ * : ^ \x00\xff binary %%EOF"),
		ContentType: "application/pdf", Filename: "operative-note.pdf",
		Now: time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC),
	}
}

// Whatever is built must read back as the same document. This is the property a payer depends on.
func TestBuiltAttachmentRoundTrips(t *testing.T) {
	req := sampleAttachmentRequest()
	raw, err := BuildAttachment6020(req)
	if err != nil {
		t.Fatal(err)
	}
	m, err := Parse(raw)
	if err != nil {
		t.Fatalf("the built interchange does not parse: %v\n%s", err, raw)
	}
	if v := m.Validate(); len(v.Problems) > 0 {
		t.Errorf("the built interchange has envelope problems: %+v", v.Problems)
	}
	a, err := m.ParseAttachment()
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Documents) != 1 {
		t.Fatalf("documents = %d", len(a.Documents))
	}
	d := a.Documents[0]
	if !bytes.Equal(d.Document, req.Document) {
		t.Errorf("the document changed:\n got %q\nwant %q", d.Document, req.Document)
	}
	if d.ContentType != "application/pdf" || d.Filename != "operative-note.pdf" {
		t.Errorf("content type / filename = %q / %q", d.ContentType, d.Filename)
	}
	if d.TraceNumber != "ACN-778899" || d.TraceType != "1" {
		t.Errorf("trace = %s %q", d.TraceType, d.TraceNumber)
	}
	if a.Payer.IDCode != "12345" || a.Provider.IDCode != "1234567893" || a.Patient.IDCode != "MEMBER001" {
		t.Errorf("parties = %+v / %+v / %+v", a.Payer, a.Provider, a.Patient)
	}
	if a.ProviderClaimID != "CLAIM-42" {
		t.Errorf("REF*X1 = %q", a.ProviderClaimID)
	}
}

// Each rule, with the guide it came from. A change to any of these should be a change somebody made on purpose.
func TestAttachment6020FollowsThePublishedGuides(t *testing.T) {
	raw, err := BuildAttachment6020(sampleAttachmentRequest())
	if err != nil {
		t.Fatal(err)
	}
	s := string(raw)
	for rule, want := range map[string]string{
		"ST03 and GS08 name the adopted version (both)": "ST*275*1234*006020X314~",
		"GS08 (both)": "*X*006020X314~",
		"BGN01 02 for an unsolicited attachment (UHC, esMD)": "BGN*02*REF-0001*20261002~",
		"payer under PI (both)":                              "NM1*PR*2*EXAMPLE HEALTH PLAN*****PI*12345~",
		"submitter under 46 (esMD)":                          "NM1*41*2*EXAMPLE CLINIC*****46*ETIN001~",
		"provider NPI under XX (UHC, esMD sample)":           "NM1*1P*2*EXAMPLE CLINIC*****XX*1234567893~",
		"patient under MI (both)":                            "NM1*QC*1*DOE*JANE****MI*MEMBER001~",
		"provider claim identifier (esMD)":                   "REF*X1*CLAIM-42~",
		"claim service date (UHC)":                           "DTP*472*D8*20261001~",
		"TRN01 1 with PWK06 when unsolicited (UHC)":          "TRN*1*ACN-778899~",
		"submitted date (esMD)":                              "DTP*368*D8*20261002~",
		"CAT02 IA for an image (UHC)":                        "CAT*AE*IA~",
		"object type (esMD)":                                 "OOI*1*47*ATTACHMENT~",
		"BDS01 B64 (UHC; the only form from 8020)":           "BDS*B64*",
		"ISA version 00602 and ISA16 component separator":    "*00602*000001234*0*T*:~",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("%s: %q not found in\n%s", rule, want, s)
		}
	}

	req := sampleAttachmentRequest()
	req.Solicited, req.TraceNumber = true, "PAYERTRN-5"
	req.ContentType, req.Document = "text/xml", []byte("<ClinicalDocument/>")
	raw, _ = BuildAttachment6020(req)
	for rule, want := range map[string]string{
		"BGN01 11 when answering a 277 (UHC)": "BGN*11*",
		"TRN01 2 with the 277's TRN02 (UHC)":  "TRN*2*PAYERTRN-5~",
		"CAT02 HL for an HL7 document (esMD)": "CAT*AE*HL~",
	} {
		if !strings.Contains(string(raw), want) {
			t.Errorf("%s: %q not found", rule, want)
		}
	}
}

func TestSegmentCountIsRight(t *testing.T) {
	raw, _ := BuildAttachment6020(sampleAttachmentRequest())
	s := string(raw)
	st := strings.Index(s, "ST*275")
	se := strings.Index(s, "SE*")
	body := s[st:se]
	// Counting tildes works only because BDS03 is base64 and holds none; the builder counts segments, not tildes.
	count := strings.Count(body, "~") + 1
	if !strings.HasPrefix(s[se:], "SE*"+strconv.Itoa(count)+"*") {
		t.Errorf("SE01 does not count ST through SE (%d): %s", count, s[se:se+15])
	}
}

func TestMissingFieldsAreNamed(t *testing.T) {
	_, err := BuildAttachment6020(AttachmentRequest{})
	if err == nil || !strings.Contains(err.Error(), "payer identifier") || !strings.Contains(err.Error(), "the document") {
		t.Errorf("err = %v", err)
	}
}

// A mistyped NPI is an attachment that never matches its claim.
func TestBadNPIIsRefused(t *testing.T) {
	req := sampleAttachmentRequest()
	req.Provider.ID = "1234567890"
	if _, err := BuildAttachment6020(req); err == nil || !strings.Contains(err.Error(), "check digit") {
		t.Errorf("err = %v", err)
	}
}

func TestDelimiterInAFieldIsRefused(t *testing.T) {
	req := sampleAttachmentRequest()
	req.Patient.Name = "DOE*SMITH"
	if _, err := BuildAttachment6020(req); err == nil {
		t.Error("a name containing * was written into NM1, which would shift every element after it")
	}
}
