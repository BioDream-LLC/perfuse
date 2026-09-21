package identity

import (
	"encoding/json"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/dicom"
	"github.com/biodream-llc/perfuse/internal/x12"
)

// maxRepeats bounds how many repetitions of a field are read.
//
// A patient identifier list with four entries is normal; one with four hundred is either a
// merge gone wrong or a payload built to make this loop expensive. Neither is worth reading
// to the end, and the identifiers that matter are at the front.
const maxRepeats = 8

// fromHL7 reads identity out of an HL7 v2 message.
//
// The paths are the ones an interface analyst would name if asked where the patient is: PID-3
// for the identifier list, PID-5 for the name, PID-7 for the date of birth, PID-18 for the
// account and PV1-19 for the visit. Orders add ORC-2 and ORC-3, the placer's and filler's
// numbers, which is what a radiology or laboratory question arrives quoting.
func fromHL7(body []byte) []Value {
	msg, err := hl7.Parse(body)
	if err != nil {
		return nil
	}

	var vals []Value

	// PID-3 repeats, and the repetitions are where a second identifier lives - the MRN in
	// the first and the enterprise number or the payer's member ID after it. Reading only
	// the first is how "that number is definitely in there" turns into a search that fails.
	for i := 1; i <= maxRepeats; i++ {
		v, err := msg.Get("PID-3(" + strconv.Itoa(i) + ").1")
		if err != nil || strings.TrimSpace(v) == "" {
			break
		}
		vals = add(vals, KindPatientID, v)
	}
	// PID-2 and PID-4 are the older external and alternate identifier fields. Still populated
	// by systems old enough to have been installed before PID-3 became the list.
	for _, p := range []string{"PID-2.1", "PID-4.1"} {
		if v, err := msg.Get(p); err == nil {
			vals = add(vals, KindPatientID, v)
		}
	}

	// The name is assembled rather than taken whole, because PID-5 as written is
	// SMITH^JOHN^Q and nobody searches for that. Family and given in reading order is what
	// somebody types.
	family, _ := msg.Get("PID-5.1")
	given, _ := msg.Get("PID-5.2")
	if name := strings.TrimSpace(strings.TrimSpace(family) + " " + strings.TrimSpace(given)); name != "" {
		vals = add(vals, KindPatientName, name)
	}

	if v, err := msg.Get("PID-7.1"); err == nil {
		vals = add(vals, KindBirthDate, hl7Date(v))
	}

	for _, p := range []string{"PID-18.1", "PV1-19.1", "PV1-50.1"} {
		if v, err := msg.Get(p); err == nil {
			vals = add(vals, KindAccount, v)
		}
	}

	// Orders. ORC-2 is the placer's number and ORC-3 the filler's; OBR repeats them, and a
	// message with several OBRs has several accessions, each of which somebody may quote.
	for i := 1; i <= maxRepeats; i++ {
		n := strconv.Itoa(i)
		found := false
		for _, p := range []string{"ORC(" + n + ")-2.1", "ORC(" + n + ")-3.1",
			"OBR(" + n + ")-2.1", "OBR(" + n + ")-3.1", "OBR(" + n + ")-18.1"} {
			if v, err := msg.Get(p); err == nil && strings.TrimSpace(v) != "" {
				vals = add(vals, KindAccession, v)
				found = true
			}
		}
		if !found {
			break
		}
	}

	return vals
}

// hl7Date trims an HL7 timestamp to its date and writes it as ISO.
//
// HL7 writes 19700101 and optionally appends a time and an offset. An ISO date is what a
// person types and what every other format in here produces, so the formats agree on what a
// date of birth looks like and a search for one works across all of them.
func hl7Date(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 8 {
		return ""
	}
	d := s[:8]
	for _, r := range d {
		if r < '0' || r > '9' {
			return ""
		}
	}
	return d[:4] + "-" + d[4:6] + "-" + d[6:8]
}

// fromDICOM reads identity out of a DICOM data set.
//
// The named tags already exist in the dicom package, so this is the one format where the
// mapping needs no interpretation. PatientName is delimited the same way HL7 delimits it,
// with a caret, so it gets the same treatment.
func fromDICOM(raw []byte) []Value {
	ds, err := dicom.Parse(raw)
	if err != nil {
		return nil
	}

	var vals []Value
	str := func(t dicom.Tag) string {
		el, ok := ds.Get(t)
		if !ok {
			return ""
		}
		return strings.TrimSpace(string(el.Value))
	}

	vals = add(vals, KindPatientID, str(dicom.TagPatientID))

	if n := str(dicom.TagPatientName); n != "" {
		parts := strings.Split(n, "^")
		name := strings.TrimSpace(parts[0])
		if len(parts) > 1 {
			name = strings.TrimSpace(name + " " + strings.TrimSpace(parts[1]))
		}
		vals = add(vals, KindPatientName, name)
	}

	vals = add(vals, KindBirthDate, hl7Date(str(dicom.TagPatientBirthDate)))
	vals = add(vals, KindAccession, str(dicom.TagAccessionNumber))
	vals = add(vals, KindStudyUID, str(dicom.TagStudyInstanceUID))
	vals = add(vals, KindAccount, str(dicom.TagStudyID))

	return vals
}

// patientQualifiers are the NM101 codes that mean the loop is about a person.
//
// IL is the insured or subscriber, QC the patient when they differ from the subscriber, and
// 74 a corrected insured. Reading NM109 from whichever NM1 happens to come first instead
// would return the billing provider's tax ID as though it were a patient identifier, which
// is both wrong and a disclosure of the wrong field into an index.
var patientQualifiers = map[string]bool{"IL": true, "QC": true, "74": true}

// fromX12 reads identity out of an X12 transaction.
//
// X12 keeps names in NM1 loops discriminated by a qualifier in the first element, and the
// path syntax addresses segments by occurrence rather than by qualifier, so the loops are
// walked and filtered here.
func fromX12(body []byte) []Value {
	msg, err := x12.Parse(body)
	if err != nil {
		return nil
	}

	var vals []Value

	// Walked to a bound rather than to the end. An 837 can carry hundreds of claims and
	// this is running on the recording path, where the cost is paid by the message being
	// delivered.
	for i := 1; i <= 64; i++ {
		n := strconv.Itoa(i)
		qual, err := msg.Get("NM1(" + n + ")-1")
		if err != nil {
			break
		}
		if !patientQualifiers[strings.ToUpper(strings.TrimSpace(qual))] {
			continue
		}
		if v, err := msg.Get("NM1(" + n + ")-9"); err == nil {
			vals = add(vals, KindPatientID, v)
		}
		last, _ := msg.Get("NM1(" + n + ")-3")
		first, _ := msg.Get("NM1(" + n + ")-4")
		if name := strings.TrimSpace(strings.TrimSpace(last) + " " + strings.TrimSpace(first)); name != "" {
			vals = add(vals, KindPatientName, name)
		}
	}

	if v, err := msg.Get("DMG-2"); err == nil {
		vals = add(vals, KindBirthDate, x12Date(v))
	}

	// CLM01 is the submitter's claim identifier, which is the number on every piece of
	// correspondence about that claim. REF-2 under a 2300 loop carries the payer's.
	for i := 1; i <= 64; i++ {
		v, err := msg.Get("CLM(" + strconv.Itoa(i) + ")-1")
		if err != nil {
			break
		}
		vals = add(vals, KindClaim, v)
	}

	return vals
}

// x12Date converts CCYYMMDD to ISO.
//
// X12 writes dates as eight digits in DMG02 when DMG01 is D8, which it almost always is.
// Reusing the HL7 conversion would be wrong in principle - different standards - and
// identical in practice, so it is reused deliberately with this note rather than duplicated.
func x12Date(s string) string { return hl7Date(s) }

// fromFHIR reads identity out of a FHIR resource.
//
// Walked generically rather than unmarshalled into a type, because FHIR has more than a
// hundred and fifty resource types and identity appears under the same handful of key names
// in nearly all of them. A typed extractor would need a case per resource and would silently
// find nothing for the resources nobody remembered - and finding nothing is indistinguishable
// from a message that had no patient in it.
//
// The cost is that a key named "identifier" anywhere in the document is treated as an
// identifier, including on a contained or referenced resource. For finding a message that is
// the behaviour wanted: the question is "is this message about that number", not "which
// element of it was".
func fromFHIR(body []byte) []Value {
	var doc any
	if err := json.Unmarshal(body, &doc); err != nil {
		return nil
	}
	var vals []Value
	walkFHIR(doc, &vals, 0)
	return vals
}

// maxFHIRDepth bounds the walk.
//
// A Bundle of Bundles nests, and a hand-built document can nest as deeply as it likes. The
// bound is well past anything real and stops a payload from being able to choose how long
// recording takes.
const maxFHIRDepth = 24

func walkFHIR(node any, vals *[]Value, depth int) {
	if depth > maxFHIRDepth {
		return
	}
	switch n := node.(type) {
	case map[string]any:
		for key, child := range n {
			switch key {
			case "identifier":
				collectFHIRIdentifier(child, vals, depth)
			case "name":
				collectFHIRName(child, vals)
			case "birthDate":
				if s, ok := child.(string); ok {
					*vals = add(*vals, KindBirthDate, s)
				}
			case "accession":
				collectFHIRIdentifier(child, vals, depth)
			}
			walkFHIR(child, vals, depth+1)
		}
	case []any:
		for _, child := range n {
			walkFHIR(child, vals, depth+1)
		}
	}
}

// collectFHIRIdentifier reads the value out of an Identifier, which may be one or a list.
func collectFHIRIdentifier(node any, vals *[]Value, depth int) {
	switch n := node.(type) {
	case map[string]any:
		if s, ok := n["value"].(string); ok {
			*vals = add(*vals, KindPatientID, s)
		}
	case []any:
		for _, child := range n {
			collectFHIRIdentifier(child, vals, depth)
		}
	case string:
		// Some profiles write a bare string where an Identifier is expected. Taking it is
		// more useful than rejecting it, since the alternative is not finding the message.
		*vals = add(*vals, KindPatientID, n)
	}
}

// collectFHIRName reads a HumanName, which is family plus given, or a plain string.
func collectFHIRName(node any, vals *[]Value) {
	switch n := node.(type) {
	case map[string]any:
		family, _ := n["family"].(string)
		var given string
		if gs, ok := n["given"].([]any); ok && len(gs) > 0 {
			given, _ = gs[0].(string)
		}
		if name := strings.TrimSpace(strings.TrimSpace(family) + " " + strings.TrimSpace(given)); name != "" {
			*vals = add(*vals, KindPatientName, name)
		}
		if text, ok := n["text"].(string); ok {
			*vals = add(*vals, KindPatientName, text)
		}
	case []any:
		for _, child := range n {
			collectFHIRName(child, vals)
		}
	case string:
		// An Organization's name is a plain string, and so is a Practitioner's in some
		// profiles. Indexed as a name because a search for it should find the message.
		*vals = add(*vals, KindPatientName, n)
	}
}
