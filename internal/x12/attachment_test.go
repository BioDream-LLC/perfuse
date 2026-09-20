package x12

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"testing"
)

// Tests for the attachment transactions, and for the binary splitting they depend on.
//
// The binary tests come first because nothing else here can be trusted without them: an attachment whose payload was cut into fragments
// still parses into a plausible-looking Attachment with a short document in it, and every assertion about the fields would pass.

const attachmentISA = "ISA*00*          *00*          *ZZ*PROVIDER       *ZZ*PAYER          " +
	"*260916*1200*^*00501*000000101*0*P*:~"

// binSegment builds a BIN segment with a correct declared length.
func binSegment(payload string) string {
	return "BIN*" + strconv.Itoa(len(payload)) + "*" + payload
}

// attachment275 builds a complete 275 interchange around one payload.
func attachment275(payload string) string {
	body := []string{
		"GS*PI*PROVIDER*PAYER*20260916*1200*101*X*006020X316",
		"ST*275*0001",
		"BGN*11*ATT-2026-0916-01*20260916*1200",
		"NM1*41*2*RIVERSIDE CLINIC*****46*RC123",
		"NM1*40*2*ACME HEALTH PLAN*****PI*ACME01",
		"NM1*QC*1*TURNER*ROSALIND****MI*MEM88771",
		"LX*1",
		"TRN*2*TRACE-55501",
		"REF*BLT*CLM-90210",
		"REF*XX9*ACN-4471",
		"DTP*472*D8*20260901",
		"CAT*OZ*AC",
		"EFI*05*PDF",
		binSegment(payload),
		"SE*13*0001",
		"GE*1*101",
		"IEA*1*000000101",
	}

	return attachmentISA + strings.Join(body, "~") + "~"
}

func TestABinaryPayloadContainingDelimitersSurvivesIntact(t *testing.T) {
	// The test this whole feature rests on.
	//
	// Before the splitter honoured declared lengths, this payload was cut at its first tilde and the fragments became segments named
	// after whatever followed - so the attachment was silently truncated and every segment after it was misaligned. Nothing reported
	// an error, because each fragment is a structurally valid segment.
	//
	// A real PDF contains all of these bytes. The point is not that this string is realistic; it is that every delimiter appears in
	// it, so a splitter that scans for any of them fails here.
	payload := "%PDF-1.4~obj*stream:component~endstream*trailer~%%EOF"

	m, err := Parse([]byte(attachment275(payload)))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	// The segment list first, because a corrupted payload shows up here as invented segments and that is the clearer failure.
	var ids []string
	for _, s := range m.segs {
		ids = append(ids, s.ID)
	}
	want := []string{"ISA", "GS", "ST", "BGN", "NM1", "NM1", "NM1", "LX", "TRN", "REF", "REF", "DTP", "CAT", "EFI", "BIN", "SE", "GE", "IEA"}
	if strings.Join(ids, " ") != strings.Join(want, " ") {
		t.Fatalf("the payload was split into segments:\n got %s\nwant %s", strings.Join(ids, " "), strings.Join(want, " "))
	}

	a, err := m.ParseAttachment()
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Documents) != 1 {
		t.Fatalf("got %d documents, want 1", len(a.Documents))
	}

	got := string(a.Documents[0].Payload)
	if got != payload {
		t.Errorf("the payload came back changed:\n got %q\nwant %q", got, payload)
	}
	if a.Documents[0].DeclaredLength != len(payload) {
		t.Errorf("declared length %d, want %d", a.Documents[0].DeclaredLength, len(payload))
	}
}

func TestAPayloadOfArbitraryBytesSurvivesIntact(t *testing.T) {
	// Every byte value, which is what a compressed document or a TIFF actually contains. A string round trip is safe in Go, so this is
	// checking the splitter and the element joiner rather than the type - specifically that neither treats a byte as meaningful
	// because it happens to equal a delimiter.
	var payload bytes.Buffer
	for b := 0; b < 256; b++ {
		payload.WriteByte(byte(b))
	}

	raw := attachmentISA + "GS*PI*PROVIDER*PAYER*20260916*1200*101*X*006020X316~ST*275*0001~LX*1~" +
		"BIN*" + strconv.Itoa(payload.Len()) + "*" + payload.String() + "~SE*4*0001~GE*1*101~IEA*1*000000101~"

	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	a, err := m.ParseAttachment()
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Documents) != 1 {
		t.Fatalf("got %d documents, want 1", len(a.Documents))
	}
	if !bytes.Equal(a.Documents[0].Payload, payload.Bytes()) {
		t.Errorf("a payload of all 256 byte values did not survive: got %d bytes, want %d",
			len(a.Documents[0].Payload), payload.Len())
	}
}

func TestThePayloadDoesNotAliasTheInterchange(t *testing.T) {
	// A document must not hold the whole interchange alive, and must not change when the caller reuses the buffer they parsed from.
	// Both follow from copying, and the second is what a test can see.
	raw := []byte(attachment275("original-content"))

	m, err := Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	a, err := m.ParseAttachment()
	if err != nil {
		t.Fatal(err)
	}

	before := string(a.Documents[0].Payload)
	for i := range raw {
		raw[i] = 'X'
	}

	if after := string(a.Documents[0].Payload); after != before {
		t.Errorf("the payload changed when the input buffer was overwritten: %q became %q", before, after)
	}
}

func TestADeclaredLengthThatIsWrongIsRefused(t *testing.T) {
	// Refused rather than recovered from, because a wrong length means the splitter cannot tell where the payload ends. Reading on
	// would take part of the document as segments and misalign the rest of the interchange, and every fragment of a PDF is a valid
	// segment - so nothing after this point would complain.
	payload := "twelve chars"

	for _, tc := range []struct {
		name    string
		declare string
	}{
		{"longer than the data", strconv.Itoa(len(payload) + 40)},
		{"shorter than the data", "4"},
		{"not a number", "lots"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := attachmentISA + "GS*PI*P*R*20260916*1200*1*X*006020X316~ST*275*0001~LX*1~" +
				"BIN*" + tc.declare + "*" + payload + "~SE*4*0001~GE*1*1~IEA*1*000000101~"

			_, err := Parse([]byte(raw))
			if !errors.Is(err, ErrMalformedBinarySegment) {
				t.Fatalf("got %v, want ErrMalformedBinarySegment", err)
			}

			// The message has to say which numbers disagreed, because the useful question is whether the sender lied about the
			// length or the file was truncated in transit, and only the two numbers answer it.
			if tc.declare != "lots" && !strings.Contains(err.Error(), tc.declare) && !strings.Contains(err.Error(), strconv.Itoa(len(payload))) {
				t.Errorf("the error names neither length: %v", err)
			}
		})
	}
}

func TestABinarySegmentWeCannotSplitIsRefusedWithAReason(t *testing.T) {
	// BDS also carries raw bytes and this package has not verified where its byte count lives. Refusing is deliberate: guessing an
	// element position would produce a plausible-looking wrong length rather than an error, which is the worst outcome available.
	raw := attachmentISA + "GS*PI*P*R*20260916*1200*1*X*006020X316~ST*275*0001~LX*1~" +
		"BDS*AC*1*12*some content~SE*4*0001~GE*1*1~IEA*1*000000101~"

	_, err := Parse([]byte(raw))
	if !errors.Is(err, ErrUnsupportedBinarySegment) {
		t.Fatalf("got %v, want ErrUnsupportedBinarySegment", err)
	}

	// A refusal that does not say what to do instead is a dead end, and this one has an obvious next step: send a sample.
	if !strings.Contains(err.Error(), "sample") {
		t.Errorf("the refusal offers no way forward: %v", err)
	}
}

func TestOrdinaryInterchangesAreUnaffectedByBinaryAwareness(t *testing.T) {
	// The splitter was rewritten, so the thing to prove is that nothing else changed. An 837 has no binary segment and must split
	// exactly as before, including the readability whitespace many partners send.
	raw := attachmentISA + "GS*HC*PROVIDER*PAYER*20260916*1200*101*X*005010X222A1~\n" +
		"  ST*837*0001~\n  BHT*0019*00*REF01*20260916*1200*CH~\n  SE*3*0001~\nGE*1*101~\nIEA*1*000000101~\n"

	m, err := Parse([]byte(raw))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	var ids []string
	for _, s := range m.segs {
		ids = append(ids, s.ID)
	}
	if got, want := strings.Join(ids, " "), "ISA GS ST BHT SE GE IEA"; got != want {
		t.Errorf("segments came out as %q, want %q", got, want)
	}
}

func TestAnAttachmentIsReadIntoTheFieldsAClaimsOfficeUses(t *testing.T) {
	m, err := Parse([]byte(attachment275("%PDF-1.4 operative note")))
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsAttachment() {
		t.Fatal("a 275 interchange was not recognised as an attachment")
	}

	a, err := m.ParseAttachment()
	if err != nil {
		t.Fatal(err)
	}

	if a.TransactionID != "ATT-2026-0916-01" {
		t.Errorf("transaction id %q", a.TransactionID)
	}
	if a.Purpose != "11" {
		t.Errorf("purpose %q, want 11 for an original submission", a.Purpose)
	}
	if a.Submitter.Name != "RIVERSIDE CLINIC" || a.Receiver.Name != "ACME HEALTH PLAN" {
		t.Errorf("submitter %q receiver %q", a.Submitter.Name, a.Receiver.Name)
	}
	if a.Patient.Name != "TURNER" || a.Patient.FirstName != "ROSALIND" {
		t.Errorf("patient %q %q", a.Patient.FirstName, a.Patient.Name)
	}

	doc := a.Documents[0]

	// The two identifiers that decide whether this document is ever associated with the claim it supports. A payer holding an
	// attachment with neither cannot file it against anything, and the claim ages while somebody works out why.
	if doc.AttachmentControlNumber != "ACN-4471" {
		t.Errorf("attachment control number %q, want ACN-4471", doc.AttachmentControlNumber)
	}
	if doc.ClaimControlNumber != "CLM-90210" {
		t.Errorf("claim control number %q", doc.ClaimControlNumber)
	}
	if doc.TraceNumber != "TRACE-55501" {
		t.Errorf("trace number %q", doc.TraceNumber)
	}
	if doc.ServiceDate != "20260901" {
		t.Errorf("service date %q", doc.ServiceDate)
	}
	if doc.Format != "PDF" {
		t.Errorf("format %q", doc.Format)
	}
}

func TestSeveralDocumentsInOneAttachmentStayApart(t *testing.T) {
	// Each LX opens a document, and fields after it belong to that one. Getting this wrong puts the second document's control number
	// on the first, which associates a document with the wrong claim - and both claims then look answered.
	body := []string{
		"GS*PI*PROVIDER*PAYER*20260916*1200*101*X*006020X316",
		"ST*275*0001",
		"BGN*11*MULTI*20260916*1200",
		"LX*1",
		"REF*XX9*ACN-FIRST",
		binSegment("first document"),
		"LX*2",
		"REF*XX9*ACN-SECOND",
		binSegment("second document, which is longer"),
		"SE*9*0001",
		"GE*1*101",
		"IEA*1*000000101",
	}

	m, err := Parse([]byte(attachmentISA + strings.Join(body, "~") + "~"))
	if err != nil {
		t.Fatal(err)
	}

	a, err := m.ParseAttachment()
	if err != nil {
		t.Fatal(err)
	}
	if len(a.Documents) != 2 {
		t.Fatalf("got %d documents, want 2", len(a.Documents))
	}

	if a.Documents[0].AttachmentControlNumber != "ACN-FIRST" || string(a.Documents[0].Payload) != "first document" {
		t.Errorf("first document is %+q with control number %q", a.Documents[0].Payload, a.Documents[0].AttachmentControlNumber)
	}
	if a.Documents[1].AttachmentControlNumber != "ACN-SECOND" || string(a.Documents[1].Payload) != "second document, which is longer" {
		t.Errorf("second document is %+q with control number %q", a.Documents[1].Payload, a.Documents[1].AttachmentControlNumber)
	}
}

func TestAskingForAnAttachmentInAnInterchangeWithoutOneIsAnError(t *testing.T) {
	// An error rather than an empty attachment. A caller receiving an Attachment with no documents cannot tell "no attachment here"
	// from "an attachment carrying nothing", and those call for different actions.
	m, err := Parse([]byte(attachmentISA + "GS*HC*P*R*20260916*1200*1*X*005010X222A1~ST*837*0001~SE*2*0001~GE*1*1~IEA*1*000000101~"))
	if err != nil {
		t.Fatal(err)
	}

	if m.IsAttachment() {
		t.Error("an 837 interchange was reported as containing an attachment")
	}
	if _, err := m.ParseAttachment(); err == nil {
		t.Error("parsing an attachment out of an 837 succeeded")
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// 277 status
// ─────────────────────────────────────────────────────────────────────────────

// stc builds an STC segment with the free-form message in STC12.
//
// Written from named positions rather than by counting asterisks. Two attempts at this by eye put the message in element 11, which read
// back as empty and looked like a parser fault - the fixture was wrong, not the code. A segment builder that says which element it is
// filling cannot make that mistake.
func stc(status, date, amount, message string) string {
	e := make([]string, 13) // index 0 is the segment ID
	e[0] = "STC"
	e[1] = status
	e[2] = date
	e[4] = amount
	e[12] = message

	return strings.Join(e, "*")
}

var status277 = "ISA*00*          *00*          *ZZ*PAYER          *ZZ*PROVIDER       " +
	"*260916*1300*^*00501*000000202*0*P*:~" +
	"GS*HN*PAYER*PROVIDER*20260916*1300*202*X*005010X212~" +
	"ST*277*0001~" +
	"BHT*0010*08*STATUS-1*20260916*1300*DG~" +
	"HL*1**20*1~" +
	"NM1*PR*2*ACME HEALTH PLAN*****PI*ACME01~" +
	"HL*2*1*21*1~" +
	"NM1*41*2*RIVERSIDE CLINIC*****46*RC123~" +
	"HL*3*2*19*1~" +
	"NM1*1P*2*RIVERSIDE ORTHOPAEDICS*****XX*1234567893~" +
	"HL*4*3*22*0~" +
	"NM1*IL*1*TURNER*ROSALIND****MI*MEM88771~" +
	"TRN*2*TRACE-55501~" +
	stc("R4:252:PR", "20260916", "450.00", "Operative note required before adjudication") + "~" +
	"REF*1K*PAYERCLM-771~" +
	"REF*D9*OURCLM-90210~" +
	"DTP*472*RD8*20260901-20260903~" +
	"SVC*HC:29881*450.00*0.00~" +
	"STC*R4:252:PR*20260916~" +
	"SE*16*0001~GE*1*202~IEA*1*000000202~"

func TestAStatusReportIsReadIntoTheFieldsABillingOfficeUses(t *testing.T) {
	m, err := Parse([]byte(status277))
	if err != nil {
		t.Fatal(err)
	}
	if !m.IsStatusReport() {
		t.Fatal("a 277 interchange was not recognised as a status report")
	}

	r, err := m.ParseStatusReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Statuses) != 1 {
		t.Fatalf("got %d statuses, want 1", len(r.Statuses))
	}

	s := r.Statuses[0]

	// The participants come from the hierarchy above the claim, which is the whole reason a flat list is worth building: a reader
	// asking what happened to a claim should not have to walk up three levels to find out whose it was.
	if s.Payer.Name != "ACME HEALTH PLAN" {
		t.Errorf("payer %q", s.Payer.Name)
	}
	if s.Provider.Name != "RIVERSIDE ORTHOPAEDICS" {
		t.Errorf("provider %q", s.Provider.Name)
	}
	if s.Patient.Name != "TURNER" {
		t.Errorf("patient %q", s.Patient.Name)
	}

	if s.Category != "R4" || s.Code != "252" || s.Entity != "PR" {
		t.Errorf("status parts %q %q %q", s.Category, s.Code, s.Entity)
	}
	if s.PayerClaimControlNumber != "PAYERCLM-771" || s.ProviderClaimNumber != "OURCLM-90210" {
		t.Errorf("claim numbers %q and %q", s.PayerClaimControlNumber, s.ProviderClaimNumber)
	}

	// A range, kept as two dates. Reporting the whole "20260901-20260903" string as the from date would be wrong in a way that
	// looks right until somebody sorts on it.
	if s.ServiceDateFrom != "20260901" || s.ServiceDateTo != "20260903" {
		t.Errorf("service dates %q to %q", s.ServiceDateFrom, s.ServiceDateTo)
	}

	if s.Description != "Operative note required before adjudication" {
		t.Errorf("description %q", s.Description)
	}
	if len(s.Lines) != 1 || s.Lines[0].ProcedureCode != "HC:29881" {
		t.Errorf("service lines %+v", s.Lines)
	}
}

func TestARequestForDocumentationIsRecognisedAsOne(t *testing.T) {
	// The question an integration engine is actually asked about a 277, and the reason recognising it belongs here rather than in
	// every channel that reads one.
	m, err := Parse([]byte(status277))
	if err != nil {
		t.Fatal(err)
	}

	r, err := m.ParseStatusReport()
	if err != nil {
		t.Fatal(err)
	}
	if !r.RequestsAdditionalInformation() {
		t.Error("a status asking for an operative note was not recognised as a request for documentation")
	}
}

func TestAnOrdinaryStatusIsNotMistakenForADocumentationRequest(t *testing.T) {
	// The other direction, which matters more. A false positive here puts every accepted claim in front of a person as needing
	// documentation, and a billing office that learns to ignore the queue is worse off than one that never had it.
	accepted := strings.Replace(status277,
		stc("R4:252:PR", "20260916", "450.00", "Operative note required before adjudication"),
		stc("F1:65:PR", "20260916", "450.00", "Finalised, payment made"), 1)
	accepted = strings.Replace(accepted, "STC*R4:252:PR*20260916~", "STC*F1:65:PR*20260916~", 1)

	m, err := Parse([]byte(accepted))
	if err != nil {
		t.Fatal(err)
	}

	r, err := m.ParseStatusReport()
	if err != nil {
		t.Fatal(err)
	}
	if r.RequestsAdditionalInformation() {
		t.Error("a finalised claim was reported as needing documentation")
	}
}

func TestASecondProviderDoesNotInheritTheFirstOnesName(t *testing.T) {
	// Opening a hierarchical level has to clear everything below it. Without that, the second provider's claims are reported under
	// the first provider's name - which is entirely plausible on screen and sends somebody to ring the wrong practice.
	two := strings.Replace(status277, "SE*16*0001~",
		"HL*5*2*19*1~"+
			"NM1*1P*2*LAKESIDE PHYSIOTHERAPY*****XX*9876543210~"+
			"TRN*2*TRACE-99902~"+
			"STC*F1:65:PR*20260916~"+
			"SE*20*0001~", 1)

	m, err := Parse([]byte(two))
	if err != nil {
		t.Fatal(err)
	}

	r, err := m.ParseStatusReport()
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Statuses) != 2 {
		t.Fatalf("got %d statuses, want 2", len(r.Statuses))
	}

	if r.Statuses[1].Provider.Name != "LAKESIDE PHYSIOTHERAPY" {
		t.Errorf("the second claim's provider is %q, want LAKESIDE PHYSIOTHERAPY", r.Statuses[1].Provider.Name)
	}

	// And the patient must not carry over either: the second claim is under a different provider with no subscriber loop of its own.
	if r.Statuses[1].Patient.Name != "" {
		t.Errorf("the second claim inherited the first claim's patient %q", r.Statuses[1].Patient.Name)
	}
}

func TestTheAttachmentAndStatusTransactionsAreRecognisedSeparately(t *testing.T) {
	// A 275 is not a 277 and neither is an 835. Worth asserting because all three are read from the same interchange shape, and a
	// dispatcher that answered yes to two of them would hand a claims office the wrong parser without any error.
	attachment, err := Parse([]byte(attachment275("content")))
	if err != nil {
		t.Fatal(err)
	}
	status, err := Parse([]byte(status277))
	if err != nil {
		t.Fatal(err)
	}

	if !attachment.IsAttachment() || attachment.IsStatusReport() {
		t.Error("a 275 was not recognised only as an attachment")
	}
	if !status.IsStatusReport() || status.IsAttachment() {
		t.Error("a 277 was not recognised only as a status report")
	}
}
