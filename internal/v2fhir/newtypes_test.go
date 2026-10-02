package v2fhir

import (
	"bytes"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Scheduling, documents and immunizations.
//
// Each test states the property that would hurt somebody if it broke, rather than restating the field table.

func siu(event string, sch map[int]string, extra ...string) string {
	segs := []string{
		segment("MSH", mshFields("SIU^"+event+"^SIU_S12", "CTRL-"+event, "20261001090000-0500")),
		segment("SCH", sch),
		segment("PID", map[int]string{1: "1", 3: "MRN123456^^^SITEA^MR", 5: "Doe^Jane", 7: "19800101", 8: "F"}),
	}
	return message(append(segs, extra...)...)
}

var schBooked = map[int]string{
	1: "PLC-100", 2: "FIL-200", 7: "FU^Follow-up visit", 25: "Booked",
}

var aisWithStart = segment("AIS", map[int]string{
	1: "1", 3: "99213^Office visit", 4: "20261015143000-0500", 7: "30", 8: "MIN",
})

var aipDoctor = segment("AIP", map[int]string{1: "1", 3: "1234^Attending^Adam"})

func validationErrors(t *testing.T, res *Result) {
	t.Helper()
	v := fhir.Validate(res.Bundle, fhir.R4)
	errs, _, _ := v.Counts()
	if errs > 0 {
		t.Fatalf("bundle does not validate: %+v", v)
	}
}

func TestSIUProducesABookedAppointmentThatValidates(t *testing.T) {
	res := convert(t, siu("S12", schBooked, aisWithStart, aipDoctor), Options{})
	appt := find[*fhir.Appointment](t, res)

	if appt.Status != "booked" {
		t.Errorf("status = %q, want booked", appt.Status)
	}
	if appt.Start != "2026-10-15T14:30:00-05:00" {
		t.Errorf("start = %q", appt.Start)
	}
	if appt.MinutesDuration == nil || *appt.MinutesDuration != 30 {
		t.Errorf("minutesDuration = %v, want 30", appt.MinutesDuration)
	}
	if len(appt.Participant) != 2 {
		t.Errorf("participants = %d, want patient and practitioner", len(appt.Participant))
	}
	if len(appt.Identifier) != 2 {
		t.Errorf("identifiers = %d, want placer and filler", len(appt.Identifier))
	}
	validationErrors(t, res)
}

// The property that matters most: a cancellation must land on the booking, not beside it. A second appointment would
// show the patient booked into a slot that no longer exists.
func TestCancellationUpdatesTheSameAppointment(t *testing.T) {
	booked := find[*fhir.Appointment](t, convert(t, siu("S12", schBooked, aisWithStart), Options{}))

	cancelSCH := map[int]string{1: "PLC-100", 2: "FIL-200", 25: "Cancelled"}
	cancelled := find[*fhir.Appointment](t, convert(t, siu("S15", cancelSCH, aisWithStart), Options{}))

	if booked.ID != cancelled.ID {
		t.Fatalf("booking %s and cancellation %s are different appointments", booked.ID, cancelled.ID)
	}
	if cancelled.Status != "cancelled" {
		t.Errorf("status = %q, want cancelled", cancelled.Status)
	}
}

func TestTriggerEventGivesStatusWhenSCH25IsEmpty(t *testing.T) {
	for event, want := range map[string]string{"S15": "cancelled", "S17": "entered-in-error", "S26": "noshow"} {
		sch := map[int]string{1: "PLC-100", 2: "FIL-200"}
		appt := find[*fhir.Appointment](t, convert(t, siu(event, sch, aisWithStart), Options{}))
		if appt.Status != want {
			t.Errorf("%s: status = %q, want %q", event, appt.Status, want)
		}
	}
}

func TestBlockedSlotIsNotAnAppointment(t *testing.T) {
	sch := map[int]string{1: "PLC-100", 2: "FIL-200", 25: "Blocked"}
	res := convert(t, siu("S12", sch, aisWithStart), Options{})
	if got := findAll[*fhir.Appointment](res); len(got) != 0 {
		t.Fatalf("a blocked slot produced %d appointments", len(got))
	}
}

func TestStartIsReadFromSCH11WhenResourceSegmentsAreSilent(t *testing.T) {
	sch := map[int]string{1: "PLC-100", 2: "FIL-200", 11: "^^^20261015143000-0500^20261015150000-0500", 25: "Booked"}
	appt := find[*fhir.Appointment](t, convert(t, siu("S12", sch), Options{}))
	if appt.Start != "2026-10-15T14:30:00-05:00" || appt.End != "2026-10-15T15:00:00-05:00" {
		t.Errorf("start/end = %q / %q", appt.Start, appt.End)
	}
}

func TestUnknownDurationUnitIsLeftOutNotGuessed(t *testing.T) {
	ais := segment("AIS", map[int]string{1: "1", 4: "20261015143000-0500", 7: "2", 8: "FORTNIGHT"})
	appt := find[*fhir.Appointment](t, convert(t, siu("S12", schBooked, ais), Options{}))
	if appt.MinutesDuration != nil {
		t.Errorf("minutesDuration = %d from an unknown unit", *appt.MinutesDuration)
	}
}

// MDM

func mdm(event string, txa map[int]string, obx ...string) string {
	segs := []string{
		segment("MSH", mshFields("MDM^"+event+"^MDM_T02", "CTRL-"+event, "20261001090000-0500")),
		segment("EVN", map[int]string{1: event, 2: "20261001090000-0500"}),
		segment("PID", map[int]string{1: "1", 3: "MRN123456^^^SITEA^MR", 5: "Doe^Jane", 7: "19800101", 8: "F"}),
		segment("TXA", txa),
	}
	return message(append(segs, obx...)...)
}

var txaDischarge = map[int]string{
	1: "1", 2: "DS", 3: "TX", 4: "20261001083000-0500", 9: "1234^Attending^Adam",
	12: "DOC-555", 17: "AU", 19: "AV",
}

func decoded(t *testing.T, doc *fhir.DocumentReference) []byte {
	t.Helper()
	if len(doc.Content) != 1 || doc.Content[0].Attachment == nil {
		t.Fatalf("content = %+v", doc.Content)
	}
	b, err := base64.StdEncoding.DecodeString(doc.Content[0].Attachment.Data)
	if err != nil {
		t.Fatalf("attachment is not base64: %v", err)
	}
	return b
}

func TestMDMTextNoteKeepsItsParagraphBreaks(t *testing.T) {
	res := convert(t, mdm("T02", txaDischarge,
		segment("OBX", map[int]string{1: "1", 2: "TX", 5: "Admitted with pneumonia.~~Discharged home."}),
		segment("OBX", map[int]string{1: "2", 2: "TX", 5: "Follow up in 1 week."}),
	), Options{})
	doc := find[*fhir.DocumentReference](t, res)

	want := "Admitted with pneumonia.\n\nDischarged home.\nFollow up in 1 week."
	if got := string(decoded(t, doc)); got != want {
		t.Errorf("text = %q, want %q", got, want)
	}
	if doc.Type == nil || doc.Type.Coding[0].Code != "18842-5" {
		t.Errorf("type = %+v, want LOINC 18842-5 discharge summary", doc.Type)
	}
	if doc.DocStatus != "final" || doc.Status != "current" {
		t.Errorf("status/docStatus = %q/%q", doc.Status, doc.DocStatus)
	}
	validationErrors(t, res)
}

// A scanned PDF must arrive as the same bytes it left as. Anything else is a different document.
func TestEncapsulatedPDFIsCarriedByteForByte(t *testing.T) {
	pdf := []byte("%PDF-1.4\n\x00\x01\x02binary\xff\xfe\n%%EOF")
	enc := base64.StdEncoding.EncodeToString(pdf)
	// v2 senders routinely wrap base64 at 76 columns.
	wrapped := enc[:10] + "\\X0D\\\\X0A\\" + enc[10:]

	res := convert(t, mdm("T02", txaDischarge,
		segment("OBX", map[int]string{1: "1", 2: "ED", 5: "EHR^AP^PDF^Base64^" + wrapped}),
	), Options{})
	doc := find[*fhir.DocumentReference](t, res)

	if doc.Content[0].Attachment.ContentType != "application/pdf" {
		t.Errorf("contentType = %q", doc.Content[0].Attachment.ContentType)
	}
	if got := decoded(t, doc); !bytes.Equal(got, pdf) {
		t.Errorf("PDF bytes changed in transit:\n got %q\nwant %q", got, pdf)
	}
	validationErrors(t, res)
}

func TestInvalidBase64IsRefusedNotPassedOn(t *testing.T) {
	res := convert(t, mdm("T02", txaDischarge,
		segment("OBX", map[int]string{1: "1", 2: "ED", 5: "EHR^AP^PDF^Base64^not*base64!"}),
	), Options{})
	if got := findAll[*fhir.DocumentReference](res); len(got) != 0 {
		t.Fatalf("a document with corrupt content was published")
	}
}

// A notification without content must not become a DocumentReference that looks like the note and says nothing.
func TestNotificationWithoutContentProducesNoDocument(t *testing.T) {
	res := convert(t, mdm("T01", txaDischarge), Options{})
	if got := findAll[*fhir.DocumentReference](res); len(got) != 0 {
		t.Fatalf("T01 without content produced %d documents", len(got))
	}
	found := false
	for _, n := range res.Notes {
		found = found || strings.Contains(n.Message, "no document content")
	}
	if !found {
		t.Errorf("the missing document was not reported: %+v", res.Notes)
	}
}

func TestCancelledDocumentIsEnteredInErrorAndSameResource(t *testing.T) {
	obx := segment("OBX", map[int]string{1: "1", 2: "TX", 5: "text"})
	original := find[*fhir.DocumentReference](t, convert(t, mdm("T02", txaDischarge, obx), Options{}))
	cancelled := find[*fhir.DocumentReference](t, convert(t, mdm("T11", txaDischarge, obx), Options{}))
	if original.ID != cancelled.ID {
		t.Fatalf("cancellation %s is not the document %s", cancelled.ID, original.ID)
	}
	if cancelled.Status != "entered-in-error" {
		t.Errorf("status = %q", cancelled.Status)
	}
}

func TestUnsignedNoteIsPreliminary(t *testing.T) {
	txa := map[int]string{2: "PN", 12: "DOC-9", 17: "DI"}
	doc := find[*fhir.DocumentReference](t, convert(t, mdm("T02", txa,
		segment("OBX", map[int]string{1: "1", 2: "TX", 5: "dictated"})), Options{}))
	if doc.DocStatus != "preliminary" {
		t.Errorf("a dictated, unsigned note has docStatus %q", doc.DocStatus)
	}
}

// VXU

func vxu(groups ...string) string {
	segs := []string{
		segment("MSH", mshFields("VXU^V04^VXU_V04", "CTRL-VXU", "20261001090000-0500")),
		segment("PID", map[int]string{1: "1", 3: "MRN123456^^^SITEA^MR", 5: "Doe^Jimmy", 7: "20200101", 8: "M"}),
	}
	return message(append(segs, groups...)...)
}

func rxa(fields map[int]string) string {
	base := map[int]string{1: "0", 2: "1", 3: "20261001", 5: "08^Hep B, adolescent or pediatric^CVX", 6: "0.5", 7: "mL", 9: "00"}
	for k, v := range fields {
		base[k] = v
	}
	return segment("RXA", base)
}

func TestVXUAdministeredDose(t *testing.T) {
	res := convert(t, vxu(
		segment("ORC", map[int]string{1: "RE", 3: "IMM-1"}),
		rxa(map[int]string{15: "LOT123", 20: "CP"}),
		segment("RXR", map[int]string{1: "IM^Intramuscular^HL70162", 2: "LT^Left thigh^HL70163"}),
	), Options{})
	imm := find[*fhir.Immunization](t, res)

	if imm.Status != "completed" {
		t.Errorf("status = %q", imm.Status)
	}
	if imm.VaccineCode.Coding[0].System != systemCVX || imm.VaccineCode.Coding[0].Code != "08" {
		t.Errorf("vaccineCode = %+v, want CVX 08", imm.VaccineCode)
	}
	if imm.PrimarySource == nil || !*imm.PrimarySource {
		t.Errorf("an administration record must be a primary source")
	}
	if imm.LotNumber != "LOT123" || imm.Route == nil || imm.Site == nil {
		t.Errorf("lot/route/site = %q/%v/%v", imm.LotNumber, imm.Route, imm.Site)
	}
	if imm.DoseQuantity == nil || *imm.DoseQuantity.Value != 0.5 {
		t.Errorf("dose = %+v", imm.DoseQuantity)
	}
	validationErrors(t, res)
}

// A refused dose reported as given is a child who is never offered the vaccine again.
func TestRefusedDoseIsNotDone(t *testing.T) {
	imm := find[*fhir.Immunization](t, convert(t, vxu(
		rxa(map[int]string{6: "999", 18: "00^Parental decision^NIP002", 20: "RE"}),
	), Options{}))
	if imm.Status != "not-done" {
		t.Errorf("a refused dose has status %q", imm.Status)
	}
	if imm.DoseQuantity != nil {
		t.Errorf("amount 999 (unknown) became a dose quantity")
	}
	if imm.StatusReason == nil {
		t.Errorf("the refusal reason was dropped")
	}
}

func TestHistoricalRecordIsNotPrimarySource(t *testing.T) {
	imm := find[*fhir.Immunization](t, convert(t, vxu(rxa(map[int]string{9: "01^Historical^NIP001"})), Options{}))
	if imm.PrimarySource == nil || *imm.PrimarySource {
		t.Errorf("a historical record was reported as the provider's own administration")
	}
}

func TestDeletedDoseIsEnteredInError(t *testing.T) {
	imm := find[*fhir.Immunization](t, convert(t, vxu(rxa(map[int]string{20: "CP", 21: "D"})), Options{}))
	if imm.Status != "entered-in-error" {
		t.Errorf("a deleted dose has status %q", imm.Status)
	}
}

func TestEachRXAGetsItsOwnRoute(t *testing.T) {
	res := convert(t, vxu(
		segment("ORC", map[int]string{1: "RE", 3: "IMM-1"}),
		rxa(nil),
		segment("RXR", map[int]string{1: "IM^Intramuscular^HL70162"}),
		segment("ORC", map[int]string{1: "RE", 3: "IMM-2"}),
		rxa(map[int]string{5: "03^MMR^CVX"}),
		segment("RXR", map[int]string{1: "SC^Subcutaneous^HL70162"}),
	), Options{})
	imms := findAll[*fhir.Immunization](res)
	if len(imms) != 2 {
		t.Fatalf("immunizations = %d, want 2", len(imms))
	}
	if imms[0].Route.Coding[0].Code != "IM" || imms[1].Route.Coding[0].Code != "SC" {
		t.Errorf("routes = %s, %s; the second dose took the first dose's route",
			imms[0].Route.Coding[0].Code, imms[1].Route.Coding[0].Code)
	}
	if imms[0].ID == imms[1].ID {
		t.Errorf("two doses share one id")
	}
	validationErrors(t, res)
}
