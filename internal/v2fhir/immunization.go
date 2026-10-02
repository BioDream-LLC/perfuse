package v2fhir

import (
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// VXU: immunizations.
//
// Every US state immunization registry receives VXU, and a registry's record is what a school, a pharmacy or the next
// clinic relies on to decide whether a child gets a dose. So the mapping is conservative in one specific direction: a
// dose is never reported as given when the message says it was refused, not administered or deleted, and a historical
// record transcribed from a card is never reported as the provider's own administration.

const (
	systemCVX = "http://hl7.org/fhir/sid/cvx"
	systemMVX = "http://terminology.hl7.org/CodeSystem/MVX"
)

func (c *converter) convertVXU() {
	patient := c.buildPatient()
	if patient == nil {
		c.note("error", "PID", "Immunization.patient", "no PID segment, so the immunizations have no patient")
		return
	}
	c.addEntry(patient, "Patient", patientConditionalURL(patient))
	patientRef := fhir.Ref("Patient", patient.ID)

	var encRef *fhir.Reference
	if enc := c.buildEncounter(patient); enc != nil {
		c.addEntry(enc, "Encounter", encounterConditionalURL(enc))
		encRef = fhir.Ref("Encounter", enc.ID)
	}

	rxas := c.msg.Segments("RXA")
	if len(rxas) == 0 {
		c.note("warning", "RXA", "Immunization", "no RXA segment, so no immunization was produced")
		return
	}

	orcs, rxrs := c.groupByRXA()
	for i := range rxas {
		if imm := c.buildImmunization(i+1, orcs[i], rxrs[i], patientRef, encRef); imm != nil {
			c.addEntry(imm, "Immunization", "")
		}
	}
}

// groupByRXA finds, for each RXA, the ORC before it and the RXR after it.
//
// In VXU_V04 each order group is ORC, TQ1, RXA, RXR, OBX in that order, so position is the only reliable association -
// the same reason OBX is grouped under OBR by position in ORU. Zero means none.
func (c *converter) groupByRXA() (orcs, rxrs []int) {
	orcSeen, rxaSeen, rxrSeen, lastORC := 0, 0, 0, 0
	for i := 0; i < c.msg.SegmentCount(); i++ {
		seg, ok := c.msg.SegmentAt(i)
		if !ok {
			continue
		}
		switch seg.Name() {
		case "ORC":
			orcSeen++
			lastORC = orcSeen
		case "RXA":
			rxaSeen++
			orcs = append(orcs, lastORC)
			rxrs = append(rxrs, 0)
			lastORC = 0 // an ORC belongs to one RXA
		case "RXR":
			rxrSeen++
			if rxaSeen > 0 && rxrs[rxaSeen-1] == 0 {
				rxrs[rxaSeen-1] = rxrSeen
			}
		}
	}
	return orcs, rxrs
}

func (c *converter) buildImmunization(n, orc, rxr int, patient, encounter *fhir.Reference) *fhir.Immunization {
	p := "RXA(" + strconv.Itoa(n) + ")"

	vaccine := c.codedValue(p+"-5", p+"-5")
	if vaccine == nil {
		c.note("error", p+"-5", "Immunization.vaccineCode", "the administration has no vaccine code")
		return nil
	}

	filler := ""
	if orc > 0 {
		filler = c.get("ORC(" + strconv.Itoa(orc) + ")-3.1")
	}

	imm := &fhir.Immunization{
		VaccineCode: vaccine,
		Patient:     patient,
		Encounter:   encounter,
		Status:      "completed",
	}
	// Keyed on the filler order number where there is one, so the registry's update (RXA-21 = U) or deletion (D)
	// of a dose lands on that dose.
	key := filler
	if key == "" {
		key = c.idSeed + "|" + strconv.Itoa(n)
	} else {
		imm.Identifier = []fhir.Identifier{{System: c.opts.DefaultIdentifierSystem, Value: filler}}
	}
	imm.SetResourceID(c.deterministicID("Immunization", key))

	imm.OccurrenceDateTime = c.v2DateTime(c.get(p+"-3"), p+"-3")
	if imm.OccurrenceDateTime == "" {
		c.note("error", p+"-3", "Immunization.occurrence", "the administration has no date, which FHIR requires")
	}

	// RXA-9: NIP001 "00" is a new record from the administering provider; "01" onwards are historical records
	// taken from some other source, and presenting one as a primary record is how a transcription error becomes a
	// registry fact.
	switch src := c.get(p + "-9.1"); src {
	case "":
	case "00":
		t := true
		imm.PrimarySource = &t
	default:
		f := false
		imm.PrimarySource = &f
	}

	if amount := strings.TrimSpace(c.get(p + "-6")); amount != "" && amount != "999" {
		if v, err := strconv.ParseFloat(amount, 64); err == nil {
			q := &fhir.Quantity{Value: &v, Unit: c.get(p + "-7.1")}
			if code, ok := mapUCUM(q.Unit); ok {
				q.System, q.Code = fhir.SystemUCUM, code
			}
			imm.DoseQuantity = q
		}
	} else if amount == "999" {
		// 999 is the CDC guide's "amount unknown", not a dose of 999.
		c.note("info", p+"-6", "Immunization.doseQuantity", "amount 999 means unknown, so no dose quantity was set")
	}

	imm.LotNumber = c.get(p + "-15")

	c.immunizationStatus(imm, p)

	if rxr > 0 {
		r := "RXR(" + strconv.Itoa(rxr) + ")"
		imm.Route = c.codedValue(r+"-1", r+"-1")
		imm.Site = c.codedValue(r+"-2", r+"-2")
	}

	return imm
}

// immunizationStatus applies RXA-20 (completion) and RXA-21 (action), in that order of weakness: a deletion overrides
// everything, and a refusal or non-administration overrides "completed".
func (c *converter) immunizationStatus(imm *fhir.Immunization, p string) {
	switch completion := strings.ToUpper(c.get(p + "-20")); completion {
	case "", "CP":
	case "PA":
		c.note("warning", p+"-20", "Immunization.status",
			"the dose was partially administered; FHIR has no partial status, so it is recorded as completed with this note")
	case "RE", "NA":
		imm.Status = "not-done"
		if reason := c.codedValue(p+"-18", p+"-18"); reason != nil {
			imm.StatusReason = reason
		}
	default:
		c.note("warning", p+"-20", "Immunization.status",
			"completion status %q is not in HL7 table 0322, so the dose is recorded as completed", completion)
	}

	if strings.EqualFold(c.get(p+"-21"), "D") {
		imm.Status = "entered-in-error"
	}
}
