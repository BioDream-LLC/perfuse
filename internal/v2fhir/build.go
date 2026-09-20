package v2fhir

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Resource builders. Each one takes the v2 segments it needs and produces one
// resource, recording any decision that required judgement.

func (c *converter) buildPatient() *fhir.Patient {
	pid, ok := c.msg.Segment("PID", 1)
	if !ok {
		return nil
	}

	p := &fhir.Patient{}

	// PID-3 is the identifier list and repeats. Every repetition is kept: a
	// patient commonly has an MRN and a national identifier, and dropping either
	// breaks matching for whoever needed that one.
	identifiers := c.patientIdentifiers()
	p.Identifier = identifiers
	if len(identifiers) == 0 {
		c.note("error", "PID-3", "Patient.identifier",
			"no patient identifier, so this patient cannot be matched to an existing record")
	}

	// The id is derived from the first identifier so that repeat messages about
	// the same patient produce the same resource id.
	idKey := ""
	if len(identifiers) > 0 {
		idKey = identifiers[0].System + "|" + identifiers[0].Value
	}
	p.SetResourceID(c.deterministicID("Patient", idKey))

	// PID-5 also repeats: a legal name plus an alias or maiden name.
	for i := 1; i <= c.repeatCount("PID-5"); i++ {
		if name := c.humanName(fmt.Sprintf("PID-5(%d)", i)); name != nil {
			if i > 1 {
				// Later repetitions are aliases unless the sender said otherwise.
				if name.Use == "" {
					name.Use = "old"
				}
			} else if name.Use == "" {
				name.Use = "official"
			}
			p.Name = append(p.Name, *name)
		}
	}

	if gender := c.get("PID-8"); gender != "" {
		code := strings.ToUpper(gender)
		if mapped, ok := genderMap[code]; ok {
			p.Gender = mapped
			if code == "A" || code == "N" {
				c.note("info", "PID-8", "Patient.gender",
					"v2 %q means %s; FHIR has no distinct code, so \"other\" was used",
					code, map[string]string{"A": "ambiguous", "N": "not applicable"}[code])
			}
		} else {
			c.note("warning", "PID-8", "Patient.gender",
				"%q is not in HL7 table 0001, so gender was left unset rather than guessed", gender)
		}
	}

	p.BirthDate = c.v2Date(c.get("PID-7"), "PID-7")

	// PID-11 repeats for home, business and mailing addresses.
	for i := 1; i <= c.repeatCount("PID-11"); i++ {
		if addr := c.address(fmt.Sprintf("PID-11(%d)", i)); addr != nil {
			p.Address = append(p.Address, *addr)
		}
	}

	// PID-13 is home phone, PID-14 business.
	for i := 1; i <= c.repeatCount("PID-13"); i++ {
		if cp := c.contactPoint(fmt.Sprintf("PID-13(%d)", i), "home"); cp != nil {
			p.Telecom = append(p.Telecom, *cp)
		}
	}
	for i := 1; i <= c.repeatCount("PID-14"); i++ {
		if cp := c.contactPoint(fmt.Sprintf("PID-14(%d)", i), "work"); cp != nil {
			p.Telecom = append(p.Telecom, *cp)
		}
	}

	if ms := c.codedValue("PID-16", "PID-16"); ms != nil {
		p.MaritalStatus = ms
	}

	// PID-30 is the deceased indicator and PID-29 the date. Setting both halves of
	// deceased[x] is invalid, so the date wins when present because it carries
	// more information.
	deceasedDate := c.v2DateTime(c.get("PID-29"), "PID-29")
	deceasedFlag := strings.ToUpper(c.get("PID-30"))
	switch {
	case deceasedDate != "":
		p.DeceasedDateTime = deceasedDate
	case deceasedFlag == "Y":
		p.DeceasedBoolean = fhir.Bool(true)
	case deceasedFlag == "N":
		p.DeceasedBoolean = fhir.Bool(false)
	}

	// A28 and A31 carry person information with no visit; A23 and A29 delete.
	// An A03 does not mean the patient is inactive, so active is only set when the
	// message actually says something about it.
	if strings.ToUpper(c.res.TriggerEvent) == "A29" {
		p.Active = fhir.Bool(false)
		c.note("info", "MSH-9.2", "Patient.active",
			"A29 deletes person information; the patient was marked inactive rather than deleted")
	}

	if next := c.nextOfKin(); len(next) > 0 {
		p.Contact = next
	}

	_ = pid
	return p
}

func (c *converter) patientIdentifiers() []fhir.Identifier {
	var out []fhir.Identifier

	count := c.repeatCount("PID-3")
	for i := 1; i <= count; i++ {
		prefix := fmt.Sprintf("PID-3(%d)", i)
		value := c.get(prefix + ".1")
		if value == "" {
			continue
		}

		authority := c.get(prefix + ".4")
		typeCode := c.get(prefix + ".5")

		id := fhir.Identifier{Value: value}

		// The system is what makes an identifier unambiguous. A bare MRN means
		// nothing outside the facility that issued it.
		switch {
		case authority != "" && c.opts.AssigningAuthoritySystems[authority] != "":
			id.System = c.opts.AssigningAuthoritySystems[authority]
		case authority != "":
			id.System = placeholderSystem(authority)
			c.note("warning", prefix+".4", "Patient.identifier.system",
				"assigning authority %q has no configured system URI, so %s was used; configure a real namespace before sending this anywhere",
				authority, id.System)
		case c.opts.DefaultIdentifierSystem != "":
			id.System = c.opts.DefaultIdentifierSystem
		default:
			c.note("warning", prefix, "Patient.identifier.system",
				"identifier %q has no assigning authority and no default system, so it is ambiguous between facilities", value)
		}

		if typeCode != "" {
			id.Type = fhir.NewCodeableConcept(fhir.SystemIdentifierType, typeCode,
				identifierTypeDisplay(typeCode))
			if typeCode == "MR" {
				id.Use = "usual"
			}
		}

		// A social security number is an identifier a system may be obliged not to
		// store. Flagging it lets a deployment decide rather than discover it.
		if typeCode == "SS" || typeCode == "SSN" {
			c.note("warning", prefix, "Patient.identifier",
				"this identifier is a social security number; confirm it should be transmitted and stored")
		}

		out = append(out, id)
	}

	// PID-2 and PID-4 are older single-identifier fields still used by some
	// senders.
	if v := c.get("PID-2.1"); v != "" && !containsIdentifier(out, v) {
		out = append(out, fhir.Identifier{
			Value:  v,
			System: c.opts.DefaultIdentifierSystem,
			Type: fhir.NewCodeableConcept(fhir.SystemIdentifierType, "MR",
				"Medical record number"),
		})
		c.note("info", "PID-2", "Patient.identifier",
			"PID-2 is deprecated but was populated, so it was kept as an identifier")
	}

	return out
}

func containsIdentifier(ids []fhir.Identifier, value string) bool {
	for _, id := range ids {
		if id.Value == value {
			return true
		}
	}
	return false
}

func identifierTypeDisplay(code string) string {
	switch strings.ToUpper(code) {
	case "MR":
		return "Medical record number"
	case "SS", "SSN":
		return "Social Security number"
	case "DL":
		return "Driver's license number"
	case "PI":
		return "Patient internal identifier"
	case "PT":
		return "Patient external identifier"
	case "AN":
		return "Account number"
	case "VN":
		return "Visit number"
	case "NPI":
		return "National provider identifier"
	default:
		return ""
	}
}

func (c *converter) nextOfKin() []fhir.PatientContact {
	var out []fhir.PatientContact

	for i, seg := range c.msg.Segments("NK1") {
		_ = seg
		prefix := fmt.Sprintf("NK1(%d)", i+1)

		contact := fhir.PatientContact{}
		if name := c.humanName(prefix + "-2"); name != nil {
			contact.Name = name
		}
		if rel := c.codedValue(prefix+"-3", prefix+"-3"); rel != nil {
			contact.Relationship = []fhir.CodeableConcept{*rel}
		}
		if addr := c.address(prefix + "-4"); addr != nil {
			contact.Address = addr
		}
		if cp := c.contactPoint(prefix+"-5", "home"); cp != nil {
			contact.Telecom = append(contact.Telecom, *cp)
		}

		if contact.Name == nil && len(contact.Telecom) == 0 && contact.Address == nil {
			continue
		}
		out = append(out, contact)
	}
	return out
}

func (c *converter) buildEncounter(patient *fhir.Patient) *fhir.Encounter {
	pv1, ok := c.msg.Segment("PV1", 1)
	if !ok {
		return nil
	}

	e := &fhir.Encounter{}

	// PV1-19 is the visit number, which is what makes an encounter identifiable
	// across messages. Without it every update creates a new encounter.
	visitNumber := c.get("PV1-19.1")
	if visitNumber != "" {
		system := c.opts.DefaultIdentifierSystem
		if authority := c.get("PV1-19.4"); authority != "" {
			if configured := c.opts.AssigningAuthoritySystems[authority]; configured != "" {
				system = configured
			} else {
				system = placeholderSystem(authority)
			}
		}
		e.Identifier = []fhir.Identifier{{
			System: system,
			Value:  visitNumber,
			Type: fhir.NewCodeableConcept(fhir.SystemIdentifierType, "VN",
				"Visit number"),
		}}
	} else {
		c.note("warning", "PV1-19", "Encounter.identifier",
			"no visit number, so this encounter cannot be updated by a later message and may duplicate")
	}

	idKey := visitNumber
	if idKey == "" {
		idKey = patient.ID + "|" + c.msg.ControlID()
	}
	e.SetResourceID(c.deterministicID("Encounter", idKey))
	e.Subject = fhir.Ref("Patient", patient.ID)

	// Status comes from the trigger event, not from a status field: v2 has no
	// encounter status, only a description of what just happened.
	event := strings.ToUpper(c.res.TriggerEvent)
	if status, ok := encounterStatusForEvent[event]; ok {
		e.Status = status
	} else {
		e.Status = "unknown"
		c.note("warning", "MSH-9.2", "Encounter.status",
			"trigger event %q has no defined encounter status, so \"unknown\" was used rather than a guess", event)
	}

	// Class is patient class, and it is what tells a receiver whether this is an
	// inpatient stay or a clinic visit.
	if pc := strings.ToUpper(c.get("PV1-2")); pc != "" {
		if mapped, ok := encounterClassMap[pc]; ok && mapped.Code != "" {
			e.Class = []fhir.CodeableConcept{*fhir.NewCodeableConcept(
				fhir.SystemActCode, mapped.Code, mapped.Display)}
		} else {
			e.Class = []fhir.CodeableConcept{*fhir.TextOnly(pc)}
			c.note("warning", "PV1-2", "Encounter.class",
				"patient class %q has no v3 ActCode equivalent, so it was kept as text", pc)
		}
	} else {
		c.note("warning", "PV1-2", "Encounter.class", "no patient class")
	}

	if t := c.codedValue("PV1-4", "PV1-4"); t != nil {
		e.Type = []fhir.CodeableConcept{*t}
	}
	if p := c.codedValue("PV1-18", "PV1-18"); p != nil {
		// PV1-18 is patient type, which is closer to encounter type than priority.
		e.Type = append(e.Type, *p)
	}

	start := c.v2DateTime(c.get("PV1-44"), "PV1-44")
	end := c.v2DateTime(c.get("PV1-45"), "PV1-45")
	if start != "" || end != "" {
		e.ActualPeriod = &fhir.Period{Start: start, End: end}
	} else if evn := c.get("EVN-2"); evn != "" {
		// Falling back to the event time is better than an encounter with no
		// period at all, and the note says where it came from.
		if converted := c.v2DateTime(evn, "EVN-2"); converted != "" {
			e.ActualPeriod = &fhir.Period{Start: converted}
			c.note("info", "EVN-2", "Encounter.period.start",
				"PV1-44 was empty, so the event time was used as the start")
		}
	}

	// PV1-3 is the assigned location: point of care, room, bed, facility.
	if loc := c.buildLocation(); loc != nil {
		c.addEntry(loc, "Location", "")
		e.Location = []fhir.EncounterLocation{{
			Location: fhir.Ref("Location", loc.ID),
			Status:   locationStatusForEvent(event),
		}}
	}

	// PV1-7 attending, PV1-8 referring, PV1-9 consulting, PV1-17 admitting.
	participants := []struct {
		path, code, display string
	}{
		{"PV1-7", "ATND", "attender"},
		{"PV1-8", "REF", "referrer"},
		{"PV1-9", "CON", "consultant"},
		{"PV1-17", "ADM", "admitter"},
	}
	for _, p := range participants {
		prac := c.buildPractitioner(p.path)
		if prac == nil {
			continue
		}
		c.addEntry(prac, "Practitioner", "")
		e.Participant = append(e.Participant, fhir.EncounterParticipant{
			Type: []fhir.CodeableConcept{*fhir.NewCodeableConcept(
				"http://terminology.hl7.org/CodeSystem/v3-ParticipationType",
				p.code, p.display)},
			Actor: fhir.Ref("Practitioner", prac.ID),
		})
	}

	// PV1-36 is discharge disposition, which belongs under admission.
	if dd := c.codedValue("PV1-36", "PV1-36"); dd != nil {
		e.Admission = &fhir.EncounterAdmission{DischargeDisposition: dd}
	}
	if src := c.codedValue("PV1-14", "PV1-14"); src != nil {
		if e.Admission == nil {
			e.Admission = &fhir.EncounterAdmission{}
		}
		e.Admission.AdmitSource = src
	}

	_ = pv1
	return e
}

// locationStatusForEvent says whether the patient is still at the location.
func locationStatusForEvent(event string) string {
	switch event {
	case "A03":
		return "completed"
	case "A02":
		return "active"
	default:
		return "active"
	}
}

func (c *converter) buildLocation() *fhir.Location {
	pointOfCare := c.get("PV1-3.1")
	room := c.get("PV1-3.2")
	bed := c.get("PV1-3.3")
	facility := c.get("PV1-3.4")

	if pointOfCare == "" && room == "" && bed == "" && facility == "" {
		return nil
	}

	parts := fhir.NonEmpty(pointOfCare, room, bed)
	name := strings.Join(parts, " ")

	l := &fhir.Location{
		Name:   name,
		Status: "active",
	}
	l.SetResourceID(c.deterministicID("Location", facility+"|"+strings.Join(parts, "-")))

	// The most specific component present determines what kind of place this is.
	physical := ""
	switch {
	case bed != "":
		physical = "bd"
	case room != "":
		physical = "ro"
	case pointOfCare != "":
		physical = "wa"
	}
	if physical != "" {
		l.PhysicalType = fhir.NewCodeableConcept(
			"http://terminology.hl7.org/CodeSystem/location-physical-type",
			physical, map[string]string{"bd": "Bed", "ro": "Room", "wa": "Ward"}[physical])
	}

	if facility != "" {
		org := &fhir.Organization{Name: facility}
		org.SetResourceID(c.deterministicID("Organization", facility))
		c.addEntry(org, "Organization", "")
		l.PartOf = fhir.Ref("Organization", org.ID)
	}

	return l
}

func (c *converter) buildPractitioner(path string) *fhir.Practitioner {
	id := c.get(path + ".1")
	family := c.get(path + ".2")
	given := c.get(path + ".3")

	if id == "" && family == "" {
		return nil
	}

	p := &fhir.Practitioner{}
	p.SetResourceID(c.deterministicID("Practitioner", id+"|"+family+"|"+given))

	if id != "" {
		system := c.opts.DefaultIdentifierSystem
		if authority := c.get(path + ".9"); authority != "" {
			if configured := c.opts.AssigningAuthoritySystems[authority]; configured != "" {
				system = configured
			}
		}
		p.Identifier = []fhir.Identifier{{System: system, Value: id}}
	}

	if family != "" || given != "" {
		name := fhir.HumanName{
			Family: family,
			Given:  fhir.NonEmpty(given, c.get(path+".4")),
			Prefix: fhir.NonEmpty(c.get(path + ".6")),
			Suffix: fhir.NonEmpty(c.get(path + ".5")),
		}
		p.Name = []fhir.HumanName{name}
	}

	return p
}

func (c *converter) buildDiagnosticReport(obrIndex int, patient, encounter *fhir.Reference) *fhir.DiagnosticReport {
	prefix := fmt.Sprintf("OBR(%d)", obrIndex)

	code := c.codedValue(prefix+"-4", prefix+"-4")
	if code == nil {
		c.note("error", prefix+"-4", "DiagnosticReport.code",
			"the order has no universal service identifier, so the report has no identity")
		return nil
	}

	d := &fhir.DiagnosticReport{
		Code:      code,
		Subject:   patient,
		Encounter: encounter,
	}

	fillerOrder := c.get(prefix + "-3.1")
	placerOrder := c.get(prefix + "-2.1")
	d.SetResourceID(c.deterministicID("DiagnosticReport", fillerOrder+"|"+placerOrder))

	if fillerOrder != "" {
		d.Identifier = append(d.Identifier, fhir.Identifier{
			Value: fillerOrder,
			Type: fhir.NewCodeableConcept(fhir.SystemIdentifierType, "FILL",
				"Filler identifier"),
			System: c.opts.DefaultIdentifierSystem,
		})
	}
	if placerOrder != "" {
		d.Identifier = append(d.Identifier, fhir.Identifier{
			Value: placerOrder,
			Type: fhir.NewCodeableConcept(fhir.SystemIdentifierType, "PLAC",
				"Placer identifier"),
			System: c.opts.DefaultIdentifierSystem,
		})
	}

	// OBR-25 is the result status.
	if rs := strings.ToUpper(c.get(prefix + "-25")); rs != "" {
		if mapped, ok := reportStatusMap[rs]; ok {
			d.Status = mapped
		} else {
			d.Status = "unknown"
			c.note("warning", prefix+"-25", "DiagnosticReport.status",
				"result status %q is not in HL7 table 0123, so \"unknown\" was used", rs)
		}
	} else {
		// A report with no status is most likely final, but guessing that would
		// assert something the sender did not.
		d.Status = "unknown"
		c.note("warning", prefix+"-25", "DiagnosticReport.status",
			"no result status was sent, so \"unknown\" was used rather than assuming final")
	}

	d.Category = []fhir.CodeableConcept{*fhir.NewCodeableConcept(
		"http://terminology.hl7.org/CodeSystem/v2-0074", "LAB", "Laboratory")}

	// OBR-7 is the observation date, OBR-22 when the report was released.
	d.EffectiveDateTime = c.v2DateTime(c.get(prefix+"-7"), prefix+"-7")
	d.Issued = c.v2Instant(c.get(prefix+"-22"), prefix+"-22")

	if prac := c.buildPractitioner(prefix + "-16"); prac != nil {
		c.addEntry(prac, "Practitioner", "")
		d.Performer = []fhir.Reference{*fhir.Ref("Practitioner", prac.ID)}
	}

	return d
}

func (c *converter) buildObservation(obxIndex int, patient, encounter, specimen *fhir.Reference) *fhir.Observation {
	prefix := fmt.Sprintf("OBX(%d)", obxIndex)

	code := c.codedValue(prefix+"-3", prefix+"-3")
	if code == nil {
		c.note("error", prefix+"-3", "Observation.code",
			"result %d has no observation identifier, so it cannot be interpreted and was skipped", obxIndex)
		return nil
	}

	o := &fhir.Observation{
		Code:      code,
		Subject:   patient,
		Encounter: encounter,
		Specimen:  specimen,
	}

	subID := c.get(prefix + "-4")
	setID := c.get(prefix + "-1")
	o.SetResourceID(c.deterministicID("Observation",
		c.msg.ControlID()+"|"+setID+"|"+subID+"|"+codeKey(code)))

	o.Category = []fhir.CodeableConcept{*fhir.NewCodeableConcept(
		fhir.SystemObservationCategory, "laboratory", "Laboratory")}

	// OBX-11 is the observation result status.
	if rs := strings.ToUpper(c.get(prefix + "-11")); rs != "" {
		if mapped, ok := observationStatusMap[rs]; ok {
			o.Status = mapped
		} else {
			o.Status = "unknown"
			c.note("warning", prefix+"-11", "Observation.status",
				"result status %q is not in HL7 table 0085, so \"unknown\" was used", rs)
		}
	} else {
		o.Status = "unknown"
		c.note("warning", prefix+"-11", "Observation.status",
			"no result status was sent, so \"unknown\" was used rather than assuming final")
	}

	o.EffectiveDateTime = c.v2DateTime(c.get(prefix+"-14"), prefix+"-14")

	// OBX-2 says what type OBX-5 holds. Honouring it is what keeps a numeric
	// result numeric and a textual one textual.
	valueType := strings.ToUpper(c.get(prefix + "-2"))
	rawValue := c.get(prefix + "-5")
	unit := c.get(prefix + "-6.1")
	if unit == "" {
		unit = c.get(prefix + "-6")
	}

	c.setObservationValue(o, prefix, valueType, rawValue, unit)

	// OBX-8 is the abnormal flag, and it repeats.
	for i := 1; i <= c.repeatCount(prefix+"-8"); i++ {
		flag := strings.ToUpper(c.get(fmt.Sprintf("%s-8(%d)", prefix, i)))
		if flag == "" {
			continue
		}
		if mapped, ok := interpretationMap[flag]; ok {
			o.Interpretation = append(o.Interpretation,
				*fhir.NewCodeableConcept(systemInterpretation, mapped.Code, mapped.Display))
		} else {
			o.Interpretation = append(o.Interpretation, *fhir.TextOnly(flag))
			c.note("warning", prefix+"-8", "Observation.interpretation",
				"abnormal flag %q is not in HL7 table 0078, so it was kept as text", flag)
		}
	}

	// OBX-7 is the reference range, sent as free text such as "70-110".
	if rr := c.get(prefix + "-7"); rr != "" {
		o.ReferenceRange = []fhir.ReferenceRange{c.parseReferenceRange(rr, unit, prefix)}
	}

	if method := c.codedValue(prefix+"-17", prefix+"-17"); method != nil {
		// The method matters clinically: the same analyte measured two ways is not
		// always comparable, which is exactly the trap in lab data.
		o.Method = method
		c.note("info", prefix+"-17", "Observation.method",
			"a method was specified; results measured by different methods are not always comparable")
	}

	// OBX-16 is the responsible observer.
	if prac := c.buildPractitioner(prefix + "-16"); prac != nil {
		c.addEntry(prac, "Practitioner", "")
		o.Performer = []fhir.Reference{*fhir.Ref("Practitioner", prac.ID)}
	}

	// NTE segments after an OBX are comments on that result. Attaching them to the
	// right observation needs segment order, so this is approximate and says so.
	return o
}

func (c *converter) setObservationValue(o *fhir.Observation, prefix, valueType, rawValue, unit string) {
	if rawValue == "" {
		o.DataAbsentReason = fhir.NewCodeableConcept(
			fhir.SystemDataAbsentReason, "unknown", "Unknown")
		c.note("info", prefix+"-5", "Observation.dataAbsentReason",
			"no value was sent, so dataAbsentReason records that rather than leaving the result silently empty")
		return
	}

	switch valueType {
	case "NM":
		value, comparator, ok := parseDecimal(rawValue)
		if !ok {
			// The sender said numeric and sent something else. Keeping the text is
			// honest; coercing it would invent a number.
			o.ValueString = fhir.Str(rawValue)
			c.note("warning", prefix+"-5", "Observation.valueString",
				"OBX-2 says numeric but %q is not a number, so it was kept as text", rawValue)
			return
		}
		o.ValueQuantity = c.quantity(value, comparator, unit, prefix)

	case "SN":
		// A structured numeric: comparator, number, separator, number.
		c.setStructuredNumeric(o, prefix, rawValue, unit)

	case "CE", "CWE", "CNE", "ID", "IS":
		if concept := c.codedValue(prefix+"-5", prefix+"-5"); concept != nil {
			o.ValueCodeableConcept = concept
		} else {
			o.ValueString = fhir.Str(rawValue)
		}

	case "ST", "TX", "FT", "":
		// Free text. A numeric-looking string stays a string, because OBX-2 is the
		// sender's statement about the type and second-guessing it changes meaning.
		o.ValueString = fhir.Str(rawValue)
		if valueType == "" {
			c.note("warning", prefix+"-2", "Observation.value[x]",
				"no value type was sent, so the result was kept as text")
		}

	case "DT":
		if converted := c.v2Date(rawValue, prefix+"-5"); converted != "" {
			o.ValueDateTime = converted
		}

	case "TS", "DTM":
		if converted := c.v2DateTime(rawValue, prefix+"-5"); converted != "" {
			o.ValueDateTime = converted
		}

	default:
		o.ValueString = fhir.Str(rawValue)
		c.note("warning", prefix+"-2", "Observation.valueString",
			"value type %q is not mapped, so the value was kept as text", valueType)
	}
}

func (c *converter) setStructuredNumeric(o *fhir.Observation, prefix, rawValue, unit string) {
	// SN components are comparator ^ num1 ^ separator ^ num2.
	comparator := c.get(prefix + "-5.1")
	num1 := c.get(prefix + "-5.2")
	separator := c.get(prefix + "-5.3")
	num2 := c.get(prefix + "-5.4")

	if num1 == "" {
		o.ValueString = fhir.Str(rawValue)
		return
	}

	value, _, ok := parseDecimal(num1)
	if !ok {
		o.ValueString = fhir.Str(rawValue)
		return
	}

	switch separator {
	case "-":
		// A range, such as a titre range.
		high, _, ok := parseDecimal(num2)
		if ok {
			o.ValueRange = &fhir.Range{
				Low:  c.quantity(value, "", unit, prefix),
				High: c.quantity(high, "", unit, prefix),
			}
			return
		}
	case ":", "/":
		den, _, ok := parseDecimal(num2)
		if ok {
			o.ValueRatio = &fhir.Ratio{
				Numerator:   c.quantity(value, "", "", prefix),
				Denominator: c.quantity(den, "", "", prefix),
			}
			return
		}
	}

	o.ValueQuantity = c.quantity(value, comparator, unit, prefix)
}

// quantity builds a quantity, coding the unit only when the mapping is certain.
func (c *converter) quantity(value float64, comparator, unit, prefix string) *fhir.Quantity {
	q := &fhir.Quantity{Value: fhir.Float(value)}

	switch comparator {
	case "<", "<=", ">", ">=":
		q.Comparator = comparator
	}

	if unit == "" {
		c.note("warning", prefix+"-6", "Observation.valueQuantity.unit",
			"a numeric result arrived with no unit; a bare number cannot be interpreted safely")
		return q
	}

	q.Unit = unit
	if code, ok := mapUCUM(unit); ok {
		q.System = fhir.SystemUCUM
		q.Code = code
	} else {
		// Leaving the code out is the safe failure. Guessing a UCUM code can turn
		// a normal result into an alarming one.
		c.note("warning", prefix+"-6", "Observation.valueQuantity.code",
			"unit %q has no known UCUM code, so it was kept as text only; a receiver cannot convert it", unit)
	}
	return q
}

// parseReferenceRange turns OBX-7 free text into a structured range where it can.
func (c *converter) parseReferenceRange(text, unit, prefix string) fhir.ReferenceRange {
	rr := fhir.ReferenceRange{Text: text}
	trimmed := strings.TrimSpace(text)

	switch {
	case strings.HasPrefix(trimmed, "<="), strings.HasPrefix(trimmed, "<"):
		if v, _, ok := parseDecimal(trimmed); ok {
			rr.High = c.rangeQuantity(v, unit)
			return rr
		}
	case strings.HasPrefix(trimmed, ">="), strings.HasPrefix(trimmed, ">"):
		if v, _, ok := parseDecimal(trimmed); ok {
			rr.Low = c.rangeQuantity(v, unit)
			return rr
		}
	}

	// A hyphenated range, taking care not to split a negative lower bound.
	if idx := strings.LastIndex(trimmed, "-"); idx > 0 {
		lowText := strings.TrimSpace(trimmed[:idx])
		highText := strings.TrimSpace(trimmed[idx+1:])
		low, _, lowOK := parseDecimal(lowText)
		high, _, highOK := parseDecimal(highText)
		if lowOK && highOK {
			rr.Low = c.rangeQuantity(low, unit)
			rr.High = c.rangeQuantity(high, unit)
			return rr
		}
	}

	// Anything else stays as text. A reference range is safe to leave uninterpreted
	// and dangerous to guess at.
	c.note("info", prefix+"-7", "Observation.referenceRange.text",
		"reference range %q could not be parsed into low and high, so it was kept as text", text)
	return rr
}

func (c *converter) rangeQuantity(value float64, unit string) *fhir.Quantity {
	q := &fhir.Quantity{Value: fhir.Float(value)}
	if unit != "" {
		q.Unit = unit
		if code, ok := mapUCUM(unit); ok {
			q.System = fhir.SystemUCUM
			q.Code = code
		}
	}
	return q
}

func (c *converter) buildSpecimen(obrIndex int, patient *fhir.Reference) *fhir.Specimen {
	prefix := fmt.Sprintf("OBR(%d)", obrIndex)

	// OBR-15 is the specimen source, OBR-14 when the sample reached the lab.
	source := c.codedValue(prefix+"-15", prefix+"-15")
	received := c.v2Instant(c.get(prefix+"-14"), prefix+"-14")
	collected := c.v2DateTime(c.get(prefix+"-7"), prefix+"-7")
	accession := c.get(prefix + "-3.1")

	if source == nil && received == "" && accession == "" {
		return nil
	}

	s := &fhir.Specimen{
		Type:         source,
		Subject:      patient,
		ReceivedTime: received,
		Status:       "available",
	}
	s.SetResourceID(c.deterministicID("Specimen", accession))

	if accession != "" {
		s.AccessionIdentifier = &fhir.Identifier{
			Value:  accession,
			System: c.opts.DefaultIdentifierSystem,
		}
	}
	if collected != "" {
		s.Collection = &fhir.SpecimenCollection{CollectedDateTime: collected}
	}

	return s
}

func (c *converter) buildServiceRequest(obrIndex int, patient, encounter *fhir.Reference) *fhir.ServiceRequest {
	prefix := fmt.Sprintf("OBR(%d)", obrIndex)

	code := c.codedValue(prefix+"-4", prefix+"-4")
	if code == nil {
		c.note("error", prefix+"-4", "ServiceRequest.code", "the order has no service identifier")
		return nil
	}

	placer := c.get(prefix + "-2.1")
	filler := c.get(prefix + "-3.1")

	sr := &fhir.ServiceRequest{
		Code:      &fhir.CodeableReference{Concept: code},
		Subject:   patient,
		Encounter: encounter,
		Intent:    "order",
		Status:    "active",
	}
	sr.SetResourceID(c.deterministicID("ServiceRequest", placer+"|"+filler))

	if placer != "" {
		sr.Identifier = append(sr.Identifier, fhir.Identifier{
			Value:  placer,
			System: c.opts.DefaultIdentifierSystem,
			Type: fhir.NewCodeableConcept(fhir.SystemIdentifierType, "PLAC",
				"Placer identifier"),
		})
	}

	// ORC-1 is the order control code, which is what says whether this is a new
	// order or a cancellation.
	if orc := strings.ToUpper(c.get("ORC-1")); orc != "" {
		switch orc {
		case "NW":
			sr.Status = "active"
		case "CA":
			sr.Status = "revoked"
		case "OC":
			sr.Status = "revoked"
		case "DC":
			sr.Status = "revoked"
		case "HD":
			sr.Status = "on-hold"
		case "CM":
			sr.Status = "completed"
		default:
			sr.Status = "unknown"
			c.note("warning", "ORC-1", "ServiceRequest.status",
				"order control code %q is not mapped, so status is \"unknown\"", orc)
		}
	}

	if priority := strings.ToUpper(c.get(prefix + "-27.6")); priority != "" {
		switch priority {
		case "S":
			sr.Priority = "stat"
		case "A":
			sr.Priority = "asap"
		case "R":
			sr.Priority = "routine"
		case "P":
			sr.Priority = "urgent"
		}
	}

	sr.AuthoredOn = c.v2DateTime(c.get("ORC-9"), "ORC-9")
	if sr.AuthoredOn == "" {
		sr.AuthoredOn = c.v2DateTime(c.get(prefix+"-6"), prefix+"-6")
	}

	if prac := c.buildPractitioner("ORC-12"); prac != nil {
		c.addEntry(prac, "Practitioner", "")
		sr.Requester = fhir.Ref("Practitioner", prac.ID)
	}

	return sr
}

// Field helpers.

func (c *converter) humanName(path string) *fhir.HumanName {
	family := c.get(path + ".1")
	given := c.get(path + ".2")
	middle := c.get(path + ".3")
	suffix := c.get(path + ".4")
	prefix := c.get(path + ".5")
	use := c.get(path + ".7")

	if family == "" && given == "" {
		return nil
	}

	name := &fhir.HumanName{
		Family: family,
		Given:  fhir.NonEmpty(given, middle),
		Prefix: fhir.NonEmpty(prefix),
		Suffix: fhir.NonEmpty(suffix),
	}

	switch strings.ToUpper(use) {
	case "L":
		name.Use = "official"
	case "M":
		name.Use = "maiden"
	case "N":
		name.Use = "nickname"
	case "A":
		name.Use = "anonymous"
	case "D":
		name.Use = "usual"
	}

	return name
}

func (c *converter) address(path string) *fhir.Address {
	line1 := c.get(path + ".1")
	line2 := c.get(path + ".2")
	city := c.get(path + ".3")
	state := c.get(path + ".4")
	postal := c.get(path + ".5")
	country := c.get(path + ".6")
	use := c.get(path + ".7")

	if line1 == "" && city == "" && postal == "" {
		return nil
	}

	addr := &fhir.Address{
		Line:       fhir.NonEmpty(line1, line2),
		City:       city,
		State:      state,
		PostalCode: postal,
		Country:    country,
	}

	switch strings.ToUpper(use) {
	case "H":
		addr.Use = "home"
	case "B", "O":
		addr.Use = "work"
	case "M":
		addr.Use = "temp"
		addr.Type = "postal"
	case "BDL":
		addr.Use = "temp"
	}

	return addr
}

func (c *converter) contactPoint(path, defaultUse string) *fhir.ContactPoint {
	// XTN carries the number in component 1 in older versions and in 12 in newer
	// ones, plus an email address in component 4.
	number := c.get(path + ".1")
	if number == "" {
		number = c.get(path + ".12")
	}
	email := c.get(path + ".4")
	equipment := strings.ToUpper(c.get(path + ".3"))

	switch {
	case email != "":
		return &fhir.ContactPoint{System: "email", Value: email, Use: defaultUse}
	case number == "":
		return nil
	}

	cp := &fhir.ContactPoint{Value: number, Use: defaultUse}
	switch equipment {
	case "CP":
		cp.System = "phone"
		cp.Use = "mobile"
	case "FX":
		cp.System = "fax"
	case "BP":
		cp.System = "pager"
	case "INTERNET", "X.400":
		cp.System = "email"
	default:
		cp.System = "phone"
	}
	return cp
}

// repeatCount returns how many repetitions a field has, or 1 when it is present
// but does not repeat, or 0 when absent.
func (c *converter) repeatCount(path string) int {
	v, err := c.msg.Value(path)
	if err != nil || !v.Exists() {
		return 0
	}
	return v.RepeatCount()
}

func codeKey(concept *fhir.CodeableConcept) string {
	if concept == nil {
		return ""
	}
	if len(concept.Coding) > 0 {
		return concept.Coding[0].System + "|" + concept.Coding[0].Code
	}
	return concept.Text
}

// ensure hl7 import is used even if helpers change.
var _ = hl7.DefaultSeparators

// placeholderSystem names an identifier namespace for an assigning authority nobody has mapped.
//
// A placeholder is unavoidable here: an identifier without a system is ambiguous, and a bare MRN means nothing outside the facility
// that issued it. What it must not do is lie about what it is.
//
// It used to be a urn-oid prefix followed by the authority name, which announces a registered object identifier and carries a word.
// RFC 3061 requires a dotted numeric OID in that namespace, so the old value was malformed. A real Keycloak assertion is not the only place a specification
// gets read loosely; HAPI FHIR accepted this without complaint, and a stricter receiver would not have.
//
// The .invalid domain is reserved by RFC 2606 for exactly this: it is a valid URI, it can never resolve, and it cannot collide with
// somebody's real namespace. Which also makes it obvious in a message that it needs configuring.
func placeholderSystem(authority string) string {
	return "http://unmapped.invalid/authority/" + strings.ToLower(authority)
}
