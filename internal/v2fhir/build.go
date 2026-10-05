package v2fhir

import (
	"encoding/base64"
	"fmt"
	"net/url"
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
				// Later repetitions are aliases unless the sender said otherwise. A type the sender did give, even one
				// with no FHIR equivalent, is not overridden.
				if name.Use == "" && c.get(fmt.Sprintf("PID-5(%d).7", i)) == "" {
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
	// birthDate is a date, so a time of birth in PID-7 used to vanish without a note. The core patient-birthTime extension on
	// birthDate is where FHIR puts it.
	//
	// A time of birth with no offset anywhere - not on PID-7, not on MSH-7, and no -timezone configured - is not written. A
	// dateTime with a time must carry an offset, and UTC would state one the message never gave: 01:00 local on 1 January
	// labelled Z is ten hours out for an Australian sender. birthDate is kept, and the dropped time is a warning.
	if raw := c.get("PID-7"); len(digitsOnly(strings.FieldsFunc(raw+" ", func(r rune) bool { return r == '+' || r == '-' })[0])) > 8 && p.BirthDate != "" {
		if !hasV2Offset(raw) && c.opts.Timezone == nil && c.senderZone == nil {
			c.note("warning", "PID-7", "Patient.birthDate.extension(patient-birthTime)",
				"PID-7 carries a time of birth with no timezone, and neither MSH-7 nor the configuration gives one, so the time "+
					"was not written (birthDate %s is kept). Set a timezone for this feed to keep it", p.BirthDate)
		} else if dt := c.v2DateTime(raw, "PID-7"); dt != "" && strings.Contains(dt, "T") {
			p.BirthDateElement = &fhir.Element{Extension: []fhir.Extension{{
				URL:           "http://hl7.org/fhir/StructureDefinition/patient-birthTime",
				ValueDateTime: &dt,
			}}}
			c.note("info", "PID-7", "Patient.birthDate.extension(patient-birthTime)",
				"PID-7 carries a time of birth; birthDate is a date, so the time went to the patient-birthTime extension")
		}
	}

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

	// USCDI race, ethnicity and preferred language.
	if ext := c.omb("PID-10", "http://hl7.org/fhir/us/core/StructureDefinition/us-core-race", ombRace); ext != nil {
		p.Extension = append(p.Extension, *ext)
	}
	if ext := c.omb("PID-22", "http://hl7.org/fhir/us/core/StructureDefinition/us-core-ethnicity", ombEthnicity); ext != nil {
		p.Extension = append(p.Extension, *ext)
	}
	if lang := c.language("PID-15"); lang != nil {
		p.Communication = append(p.Communication, fhir.PatientCommunication{Language: lang})
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
	for i := 1; i <= c.repeatCount("PID-3"); i++ {
		if id, ok := c.cxIdentifier(fmt.Sprintf("PID-3(%d)", i), "PID-3"); ok {
			out = append(out, id)
		}
	}

	// PID-2 (external id) and PID-4 (alternate id) were withdrawn after v2.7, but real senders still fill them. They are
	// CX like PID-3 and are read the same way. PID-4 used to be dropped without a note, and PID-2 was labelled an MRN, which
	// it need not be.
	for _, field := range []string{"PID-2", "PID-4"} {
		for i := 1; i <= c.repeatCount(field); i++ {
			prefix := fmt.Sprintf("%s(%d)", field, i)
			if id, ok := c.cxIdentifier(prefix, field); ok && !containsIdentifierExact(out, id) {
				out = append(out, id)
				c.note("info", prefix, "Patient.identifier",
					"%s is withdrawn after v2.7 but was populated, so it was kept as an identifier", field)
			}
		}
	}
	return out
}

// cxIdentifier reads one CX: value (.1), assigning authority (.4), type (.5) and validity (.7, .8).
func (c *converter) cxIdentifier(prefix, field string) (fhir.Identifier, bool) {
	value := c.get(prefix + ".1")
	if value == "" {
		return fhir.Identifier{}, false
	}
	id := fhir.Identifier{Value: value}

	// The system is what makes an identifier unambiguous. A bare MRN means nothing outside the facility that issued it.
	authority := c.get(prefix + ".4")
	switch system, how := c.authoritySystem(prefix+".4", authority); {
	case system != "":
		id.System = system
		if how != "" {
			c.note("info", prefix+".4", "Patient.identifier.system", "%s", how)
		}
	case c.opts.DefaultIdentifierSystem != "":
		id.System = c.opts.DefaultIdentifierSystem
	default:
		c.note("warning", prefix, "Patient.identifier.system",
			"identifier %q has no assigning authority and no default system, so it is ambiguous between facilities", value)
	}

	if typeCode := c.get(prefix + ".5"); typeCode != "" {
		if isTable0203(typeCode) {
			id.Type = fhir.NewCodeableConcept(fhir.SystemIdentifierType, typeCode, identifierTypeDisplay(typeCode))
		} else {
			// A site's own type code ("PATNUMBER") was labelled as HL7 table 0203, which the validator rejects as an
			// unknown code. It is kept as text.
			id.Type = fhir.TextOnly(typeCode)
			c.note("warning", prefix+".5", "Identifier.type",
				"identifier type %q is not in HL7 table 0203, so it was kept as text", typeCode)
		}
		if typeCode == "MR" && field == "PID-3" {
			id.Use = "usual"
		}
		// A social security number is an identifier a system may be obliged not to store. Flagging it lets a deployment
		// decide rather than discover it.
		if typeCode == "SS" || typeCode == "SSN" {
			c.note("warning", prefix, "Patient.identifier",
				"this identifier is a social security number; confirm it should be transmitted and stored")
		}
	}

	// CX.7 and CX.8 bound when the identifier is valid. They matter for an MRN retired by a merge, and were dropped.
	start, end := c.v2Date(c.get(prefix+".7"), prefix+".7"), c.v2Date(c.get(prefix+".8"), prefix+".8")
	if start != "" || end != "" {
		id.Period = &fhir.Period{Start: start, End: end}
	}
	return id, true
}

// authoritySystem turns an HD assigning authority into an identifier system. The second result, when not empty, says how
// a system was derived when it was not configured.
//
// A configured mapping wins, looked up by the whole HD, then HD.1, then HD.2. Without one, an HD that carries an ISO OID or
// a UUID in HD.2/HD.3 becomes urn:oid: or urn:uuid:, because that names the authority globally. The V2-to-FHIR IG's
// ConceptMaps would put HD.1 into the system verbatim instead, which is not a URI; that deviation is stated in the note.
// Anything else gets a placeholder that says it needs configuring.
func (c *converter) authoritySystem(source, authority string) (string, string) {
	if authority == "" {
		return "", ""
	}
	parts := strings.Split(authority, string(c.msg.Separators().Subcomponent))
	hd1, hd2, hd3 := parts[0], "", ""
	if len(parts) > 1 {
		hd2 = parts[1]
	}
	if len(parts) > 2 {
		hd3 = strings.ToUpper(parts[2])
	}
	for _, key := range []string{authority, hd1, hd2} {
		if key != "" && c.opts.AssigningAuthoritySystems[key] != "" {
			return c.opts.AssigningAuthoritySystems[key], ""
		}
	}
	// The national identifiers have FHIR systems of their own, which every US receiver expects in place of the OID.
	if sys, ok := wellKnownAuthorities[hd2]; ok && hd2 != "" {
		return sys, ""
	}
	if sys, ok := wellKnownAuthorities[strings.ToUpper(hd1)]; ok && hd2 == "" {
		return sys, ""
	}
	switch {
	case hd3 == "ISO" && isOID(hd2):
		return "urn:oid:" + hd2, fmt.Sprintf("assigning authority %q carries the OID %s, so urn:oid:%s was used; the V2-to-FHIR IG would use HD.1 (%q) verbatim, which is not a URI", authority, hd2, hd2, hd1)
	case hd3 == "UUID" && isUUID(hd2):
		return "urn:uuid:" + strings.ToLower(hd2), fmt.Sprintf("assigning authority %q carries a UUID, so urn:uuid:%s was used", authority, strings.ToLower(hd2))
	}
	name := hd1
	if name == "" {
		name = hd2
	}
	system := placeholderSystem(name)
	c.note("warning", source, "Patient.identifier.system",
		"assigning authority %q has no configured system URI, so %s was used; configure a real namespace before sending this anywhere",
		authority, system)
	return system, ""
}

// wellKnownAuthorities are the US national identifier namespaces, by OID and by the HD.1 name senders commonly use for them.
var wellKnownAuthorities = map[string]string{
	"2.16.840.1.113883.4.6": "http://hl7.org/fhir/sid/us-npi",
	"NPI":                   "http://hl7.org/fhir/sid/us-npi",
	"2.16.840.1.113883.4.1": "http://hl7.org/fhir/sid/us-ssn",
	"SSA":                   "http://hl7.org/fhir/sid/us-ssn",
}

func isOID(s string) bool {
	if s == "" || s[0] == '.' || s[len(s)-1] == '.' || strings.Contains(s, "..") {
		return false
	}
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' {
			return false
		}
	}
	return strings.Contains(s, ".")
}

func isUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i, r := range strings.ToLower(s) {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
				return false
			}
		}
	}
	return true
}

func containsIdentifierExact(ids []fhir.Identifier, id fhir.Identifier) bool {
	for _, x := range ids {
		if x.Value == id.Value && x.System == id.System {
			return true
		}
	}
	return false
}

func containsIdentifier(ids []fhir.Identifier, value string) bool {
	for _, id := range ids {
		if id.Value == value {
			return true
		}
	}
	return false
}

// unknownClass is an Encounter.class for a patient class that is absent or has no ActCode equivalent: the v3 null flavor
// UNK, which an R4 Coding can carry, with the sender's code kept as text where R5's CodeableConcept can hold it.
func unknownClass(text string) fhir.CodeableConcept {
	cc := fhir.NewCodeableConcept("http://terminology.hl7.org/CodeSystem/v3-NullFlavor", "UNK", "unknown")
	cc.Text = text
	return *cc
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
			system, _ = c.authoritySystem("PV1-19.4", authority)
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

	// PV2-3, the admit reason, is the V2-to-FHIR IG's Encounter.reasonCode: why the patient came, in the clinician's words.
	if c.get("PV2-3.1") != "" || c.get("PV2-3.2") != "" {
		if cc := c.codedValue("PV2-3", "PV2-3"); cc != nil {
			e.Reason = []fhir.EncounterReason{{Value: []fhir.CodeableReference{{Concept: cc}}}}
		}
	}

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

	// An update can describe a visit that has already ended. Taking the status from the event alone said "in-progress" for
	// an A08 carrying a discharge date in PV1-45, which contradicts the period the same Encounter carries.
	if e.Status == "in-progress" && event != "A13" && c.get("PV1-45") != "" {
		e.Status = "completed"
		c.note("info", "PV1-45", "Encounter.status",
			"PV1-45 has a discharge date, so the encounter is completed although %s alone would mean in progress", event)
	}

	// Class is patient class, and it is what tells a receiver whether this is an
	// inpatient stay or a clinic visit.
	if pc := strings.ToUpper(c.get("PV1-2")); pc != "" {
		if mapped, ok := encounterClassMap[pc]; ok && mapped.Code != "" {
			e.Class = []fhir.CodeableConcept{*fhir.NewCodeableConcept(
				fhir.SystemActCode, mapped.Code, mapped.Display)}
		} else {
			e.Class = []fhir.CodeableConcept{unknownClass(pc)}
			c.note("warning", "PV1-2", "Encounter.class",
				"patient class %q has no v3 ActCode equivalent, so the class is the null flavor UNK with the sender's code as text", pc)
		}
	} else {
		// R4 Encounter.class is 1..1 and a Coding, so an absent class cannot simply be left out: three of the 70 messages
		// in a public test set produced Encounters the HL7 validator rejected for exactly that. The null flavor says
		// "unknown" without inventing a class.
		e.Class = []fhir.CodeableConcept{unknownClass("")}
		c.note("warning", "PV1-2", "Encounter.class", "no patient class was sent, so the class is the null flavor UNK")
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
		// A facility named on a current message is in operation, which is what US Core's required active asserts.
		active := true
		org := &fhir.Organization{Name: facility, Active: &active}
		org.SetResourceID(c.deterministicID("Organization", facility))
		c.addEntry(org, "Organization", "")
		// The facility runs the place: managingOrganization. partOf is for a Location inside another Location, and an
		// Organization there is invalid FHIR - which the HL7 validator reported and this package's own checks did not.
		l.ManagingOrganization = fhir.Ref("Organization", org.ID)
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
		// XCN.9 is the assigning authority and XCN.13 the identifier type; either can say the number is an NPI.
		system := c.opts.DefaultIdentifierSystem
		if authority := c.get(path + ".9"); authority != "" {
			if sys, how := c.authoritySystem(path+".9", authority); sys != "" {
				system = sys
				if how != "" {
					c.note("info", path+".9", "Practitioner.identifier.system", "%s", how)
				}
			}
		} else if strings.EqualFold(c.get(path+".13"), "NPI") {
			system = "http://hl7.org/fhir/sid/us-npi"
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
		// A coded type whose OBX-5 is shaped like ED (Base64 in the fourth component) is an embedded document labelled
		// wrongly. Read as a code, a PDF went into Coding.display - over a megabyte, past FHIR's string limit.
		if strings.EqualFold(c.get(prefix+"-5.4"), "Base64") && len(c.get(prefix+"-5.5")) > 64 {
			c.note("warning", prefix+"-2", "Observation.value[x]",
				"OBX-2 says %s but OBX-5 is encapsulated Base64 data, so it was read as ED", valueType)
			if c.encapsulatedData(o, prefix) {
				return
			}
		}
		if concept := c.codedValue(prefix+"-5", prefix+"-5"); concept != nil {
			o.ValueCodeableConcept = concept
		} else {
			o.ValueString = fhir.Str(rawValue)
		}

	case "ST", "TX", "FT", "":
		// Free text. A numeric-looking string stays a string, because OBX-2 is the
		// sender's statement about the type and second-guessing it changes meaning.
		//
		// OBX-5 repeats, and a multi-line TX result is sent as one repeat per line. The raw field used to be copied, so
		// "Line one~Line two" arrived with the repeat separator in the text. The repeats are joined with line breaks.
		if n := c.repeatCount(prefix + "-5"); n > 1 {
			lines := make([]string, 0, n)
			for i := 1; i <= n; i++ {
				lines = append(lines, c.get(fmt.Sprintf("%s-5(%d)", prefix, i)))
			}
			rawValue = strings.Join(lines, "\n")
		}
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

	case "ED":
		if c.encapsulatedData(o, prefix) {
			return
		}
		o.ValueString = fhir.Str(rawValue)

	default:
		o.ValueString = fhir.Str(rawValue)
		c.note("warning", prefix+"-2", "Observation.valueString",
			"value type %q is not mapped, so the value was kept as text", valueType)
	}
}

// encapsulatedData maps an OBX-5 of type ED (an embedded document such as a PDF report) to a DocumentReference, which the
// V2-to-FHIR IG's OBX-to-DocumentReference map allows and which is where consumers look for documents. R4
// Observation.value[x] has no Attachment, so the Observation keeps no value and points at the document through derivedFrom.
// It used to keep the raw field as text, delimiters included, with the PDF's Base64 inside it.
//
// It reports false, leaving the caller to keep the text, when the data is not Base64: hex and other encodings are rare
// and are not decoded here.
func (c *converter) encapsulatedData(o *fhir.Observation, prefix string) bool {
	kind, subtype := strings.ToLower(c.get(prefix+"-5.2")), strings.ToLower(c.get(prefix+"-5.3"))
	encoding, data := c.get(prefix+"-5.4"), c.get(prefix+"-5.5")
	if !strings.EqualFold(encoding, "Base64") || data == "" {
		c.note("warning", prefix+"-5", "Observation.valueString",
			"encapsulated data with encoding %q is not decoded, so it was kept as text", encoding)
		return false
	}
	if _, err := base64.StdEncoding.DecodeString(data); err != nil {
		c.note("warning", prefix+"-5.5", "Observation.valueString", "encapsulated data is marked Base64 but does not decode, so it was kept as text")
		return false
	}
	contentType := "application/octet-stream"
	switch {
	case strings.Contains(subtype, "/"):
		// Some senders put the whole MIME type in ED.3 ("application/pdf") and something else in ED.2.
		contentType = subtype
	case strings.Contains(kind, "/"):
		contentType = kind
	case subtype != "":
		switch kind {
		case "application", "audio", "image", "text", "video", "multipart", "model", "font":
			contentType = kind + "/" + subtype
		}
	}
	if contentType == "application/octet-stream" {
		c.note("warning", prefix+"-5.2", "DocumentReference.content.attachment.contentType",
			"type of data %q / %q is not a MIME type, so application/octet-stream was used", kind, subtype)
	}
	doc := &fhir.DocumentReference{
		Status:  "current",
		Type:    o.Code,
		Subject: o.Subject,
		Content: []fhir.DocumentContent{{Attachment: &fhir.Attachment{ContentType: contentType, Data: data}}},
	}
	doc.SetResourceID(c.deterministicID("DocumentReference", o.ID))
	c.addEntry(doc, "DocumentReference", "")
	o.DerivedFrom = append(o.DerivedFrom, *fhir.Ref("DocumentReference", doc.ID))
	c.note("info", prefix+"-5", "DocumentReference",
		"OBX-2 is ED: R4 Observation.value[x] has no Attachment, so the %s document went to a DocumentReference that the Observation references in derivedFrom", contentType)
	return true
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
			// One note for the range, not one per end: it is one result missing one unit.
			o.ValueRange = &fhir.Range{
				Low:  c.quantity(value, "", unit, prefix),
				High: c.unitlessQuantity(high, unit),
			}
			return
		}
	case ":", "/":
		den, _, ok := parseDecimal(num2)
		if ok {
			// A titre or ratio has no unit by nature (1:64), so its parts carry none and need no note saying so.
			o.ValueRatio = &fhir.Ratio{
				Numerator:   c.unitlessQuantity(value, ""),
				Denominator: c.unitlessQuantity(den, ""),
			}
			return
		}
	}

	// R4 Quantity.comparator admits only <, <=, >= and >. "=" says the value is exact, which a bare quantity already means,
	// so it is dropped with a note. "<>" means "not equal to", which no quantity can say; turning it into the number would
	// state the opposite, so the text is kept.
	switch comparator {
	case "=":
		comparator = ""
		c.note("info", prefix+"-5.1", "Observation.valueQuantity.comparator",
			"comparator \"=\" is not an R4 comparator; it means the value is exact, so the quantity carries none")
	case "<>":
		o.ValueString = fhir.Str(rawValue)
		c.note("warning", prefix+"-5.1", "Observation.valueString",
			"comparator \"<>\" (not equal) has no FHIR Quantity equivalent, so the result was kept as text")
		return
	}
	o.ValueQuantity = c.quantity(value, comparator, unit, prefix)
}

// unitlessQuantity is quantity without the missing-unit note, for a value whose note is already given or not wanted.
func (c *converter) unitlessQuantity(value float64, unit string) *fhir.Quantity {
	q := &fhir.Quantity{Value: fhir.Float(value), Unit: unit}
	if code, ok := mapUCUM(unit); ok && unit != "" {
		q.System = fhir.SystemUCUM
		q.Code = code
	}
	return q
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

	// OBR-15 is the specimen source, OBR-14 when the sample reached the lab. From v2.5 the SPM segment carries the specimen
	// instead, and OBR-15 is withdrawn; a 2.5.1 lab result sends SPM, so SPM wins where both are present.
	source := c.codedValue(prefix+"-15", prefix+"-15")
	received := c.v2Instant(c.get(prefix+"-14"), prefix+"-14")
	collected := c.v2DateTime(c.get(prefix+"-7"), prefix+"-7")
	accession := c.get(prefix + "-3.1")
	if spm := c.spmFor(obrIndex); spm != "" {
		if t := c.codedValue(spm+"-4", spm+"-4"); t != nil {
			source = t
		}
		if v := c.v2Instant(c.get(spm+"-18"), spm+"-18"); v != "" {
			received = v
		}
		if v := c.v2DateTime(c.get(spm+"-17.1"), spm+"-17"); v != "" {
			collected = v
		}
	}

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

// spmFor is the path prefix of the SPM segment belonging to an OBR, or "" when there is none. In ORU^R01 the specimen follows its
// order: the first SPM after the OBR, before the next one.
func (c *converter) spmFor(obrIndex int) string {
	obr, spm := 0, 0
	found := ""
	for _, name := range c.msg.SegmentNames() {
		switch name {
		case "OBR":
			obr++
		case "SPM":
			spm++
			if obr == obrIndex && found == "" {
				found = fmt.Sprintf("SPM(%d)", spm)
			}
		}
	}
	return found
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
	case "":
	default:
		// B (birth name), C (adopted), U (unspecified) and site codes have no counterpart in FHIR name-use. Use is left
		// empty rather than guessed, and the note says so, because an empty use, a dropped name and an invented "maiden"
		// otherwise look the same in the output.
		c.note("info", path+".7", "HumanName.use",
			"name type %q has no FHIR name-use equivalent, so use was left empty and the name was kept", use)
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
	if number == "" {
		// From v2.5 the number is in parts: country code, area code, local number and extension (components 5 to 8). Reading only
		// the whole-number components dropped every phone number a 2.5.1 sender split this way.
		number = joinPhone(c.get(path+".5"), c.get(path+".6"), c.get(path+".7"), c.get(path+".8"))
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

// joinPhone renders the parts of an XTN number as one: +1 217 555 0100, with the extension after an x.
func joinPhone(country, area, local, ext string) string {
	if local == "" {
		return ""
	}
	if len(local) == 7 && strings.Trim(local, "0123456789") == "" {
		local = local[:3] + " " + local[3:]
	}
	parts := []string{}
	if country != "" {
		parts = append(parts, "+"+strings.TrimPrefix(country, "+"))
	}
	if area != "" {
		parts = append(parts, area)
	}
	parts = append(parts, local)
	out := strings.Join(parts, " ")
	if ext != "" {
		out += " x" + ext
	}
	return out
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
//
// The name is escaped. A coding system sent as "PANEL: R112.1 - Factor II deficiency v1.0" made a URI with spaces in it,
// which the validator rejects.
func placeholderSystem(authority string) string {
	return "http://unmapped.invalid/authority/" + url.PathEscape(strings.ToLower(authority))
}

// hasV2Offset reports whether a v2 timestamp carries its own +hhmm or -hhmm offset.
func hasV2Offset(value string) bool {
	value = strings.TrimSpace(value)
	return strings.IndexAny(value[min(1, len(value)):], "+-") >= 0
}
