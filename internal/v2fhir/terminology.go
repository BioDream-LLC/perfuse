package v2fhir

import (
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// Terminology mapping.
//
// The rule throughout: map only where the mapping is defined by a standard or is
// unambiguous, and otherwise keep the original value as text and say so. A local
// code turned into a plausible standard code is the worst possible outcome,
// because the receiving system has no way to know it was invented.

// genderMap maps HL7 v2 table 0001 to the FHIR administrative gender value set.
//
// A, N and O all mean something FHIR expresses as "other", and U means unknown.
// The distinction between "ambiguous" and "other" is lost in FHIR, which is a
// property of the standard rather than of this mapping, so it is noted.
var genderMap = map[string]string{
	"M": "male",
	"F": "female",
	"O": "other",
	"A": "other",
	"N": "other",
	"U": "unknown",
}

// encounterClassMap maps HL7 v2 table 0004, patient class, to v3 ActCode.
//
// This mapping is defined by HL7 and is one of the few that can be applied with
// confidence.
var encounterClassMap = map[string]struct{ Code, Display string }{
	"I": {"IMP", "inpatient encounter"},
	"O": {"AMB", "ambulatory"},
	"E": {"EMER", "emergency"},
	"P": {"PRENC", "pre-admission"},
	"R": {"AMB", "ambulatory"},
	"B": {"OBSENC", "observation encounter"},
	"C": {"AMB", "ambulatory"},
	"N": {"NONAC", "inpatient non-acute"},
	"U": {"", ""},
}

// encounterStatusForEvent maps an ADT trigger event to an encounter status, in the
// R5 value set. The converter holds R5 codes and the serialiser maps them to R4.
//
// This is the mapping that most often goes wrong, because the trigger event says
// what happened rather than what state the visit is now in.
var encounterStatusForEvent = map[string]string{
	"A01": "in-progress", // admit
	"A02": "in-progress", // transfer
	"A03": "completed",   // discharge
	"A04": "in-progress", // register outpatient
	"A05": "planned",     // pre-admit
	"A06": "in-progress", // change outpatient to inpatient
	"A07": "in-progress", // change inpatient to outpatient
	"A08": "in-progress", // update
	"A11": "cancelled",   // cancel admit
	"A12": "in-progress", // cancel transfer
	"A13": "in-progress", // cancel discharge: the visit is open again
	"A14": "planned",     // pending admit
	"A15": "in-progress", // pending transfer
	"A16": "in-progress", // pending discharge
	"A17": "in-progress", // swap patients
	"A21": "on-hold",     // leave of absence
	"A22": "in-progress", // return from leave
	"A38": "cancelled",   // cancel pre-admit
	"A44": "in-progress", // move account information
}

// observationStatusMap maps HL7 v2 table 0085, observation result status, to the
// FHIR observation status value set.
var observationStatusMap = map[string]string{
	"C": "corrected",
	"D": "cancelled",
	"F": "final",
	"I": "registered",
	"P": "preliminary",
	"R": "preliminary",
	"S": "partial",
	"U": "final",
	"W": "entered-in-error",
	"X": "cancelled",
}

// reportStatusMap maps HL7 v2 table 0123, result status, to the diagnostic report
// status value set.
var reportStatusMap = map[string]string{
	"O": "registered",
	"I": "registered",
	"S": "partial",
	"A": "partial",
	"P": "preliminary",
	"C": "corrected",
	"R": "final",
	"F": "final",
	"X": "cancelled",
	"Y": "cancelled",
	"Z": "unknown",
}

// interpretationMap maps HL7 v2 table 0078, abnormal flags, to the observation
// interpretation code system.
var interpretationMap = map[string]struct{ Code, Display string }{
	"L":   {"L", "Low"},
	"H":   {"H", "High"},
	"LL":  {"LL", "Critical low"},
	"HH":  {"HH", "Critical high"},
	"N":   {"N", "Normal"},
	"A":   {"A", "Abnormal"},
	"AA":  {"AA", "Critical abnormal"},
	"S":   {"S", "Susceptible"},
	"R":   {"R", "Resistant"},
	"I":   {"I", "Intermediate"},
	"MS":  {"MS", "Moderately susceptible"},
	"VS":  {"VS", "Very susceptible"},
	"U":   {"U", "Significant change up"},
	"D":   {"D", "Significant change down"},
	"B":   {"B", "Better"},
	"W":   {"W", "Worse"},
	"<":   {"<", "Off scale low"},
	">":   {">", "Off scale high"},
	"POS": {"POS", "Positive"},
	"NEG": {"NEG", "Negative"},
	"IND": {"IND", "Indeterminate"},
	"DET": {"DET", "Detected"},
	"ND":  {"ND", "Not detected"},
}

const systemInterpretation = "http://terminology.hl7.org/CodeSystem/v3-ObservationInterpretation"

// codingSystemMap maps HL7 v2 coding system identifiers, which appear in the
// third component of a coded field, to FHIR system URIs.
var codingSystemMap = map[string]string{
	"LN":      fhir.SystemLOINC,
	"LOINC":   fhir.SystemLOINC,
	"SCT":     fhir.SystemSNOMED,
	"SNM":     fhir.SystemSNOMED,
	"SNOMED":  fhir.SystemSNOMED,
	"SNM3":    fhir.SystemSNOMED,
	"UCUM":    fhir.SystemUCUM,
	"RXNORM":  fhir.SystemRxNorm,
	"RXN":     fhir.SystemRxNorm,
	"ICD10":   "http://hl7.org/fhir/sid/icd-10",
	"I10":     "http://hl7.org/fhir/sid/icd-10-cm",
	"ICD9":    "http://hl7.org/fhir/sid/icd-9-cm",
	"I9":      "http://hl7.org/fhir/sid/icd-9-cm",
	"ICD10CM": "http://hl7.org/fhir/sid/icd-10-cm",
	"CVX":     systemCVX,
	"MVX":     systemMVX,
	// The NCI Thesaurus, which CDC's immunization guide uses for routes of administration.
	"NCIT": "http://ncicb.nci.nih.gov/xml/owl/EVS/Thesaurus.owl",
	// CPT is deliberately absent. CPT is licensed by the AMA and redistributing a
	// mapping to it is a licensing question, not a technical one.
}

// ucumUnits maps the unit strings laboratories actually send to UCUM codes.
//
// Only unambiguous conversions are listed. A unit that is not here keeps its
// original text with no code, and the conversion says so, because a wrong unit
// code turns a normal result into an alarming one or the reverse.
var ucumUnits = map[string]string{
	"mg/dl":     "mg/dL",
	"mg/dL":     "mg/dL",
	"MG/DL":     "mg/dL",
	"g/dl":      "g/dL",
	"g/dL":      "g/dL",
	"G/DL":      "g/dL",
	"mmol/l":    "mmol/L",
	"mmol/L":    "mmol/L",
	"MMOL/L":    "mmol/L",
	"umol/l":    "umol/L",
	"umol/L":    "umol/L",
	"mcmol/L":   "umol/L",
	"meq/l":     "meq/L",
	"mEq/L":     "meq/L",
	"MEQ/L":     "meq/L",
	"ng/ml":     "ng/mL",
	"ng/mL":     "ng/mL",
	"pg/ml":     "pg/mL",
	"pg/mL":     "pg/mL",
	"ug/ml":     "ug/mL",
	"ug/mL":     "ug/mL",
	"iu/l":      "[IU]/L",
	"IU/L":      "[IU]/L",
	"U/L":       "U/L",
	"u/l":       "U/L",
	"%":         "%",
	"fL":        "fL",
	"fl":        "fL",
	"pg":        "pg",
	"g/L":       "g/L",
	"mm/hr":     "mm/h",
	"mm/h":      "mm/h",
	"sec":       "s",
	"s":         "s",
	"min":       "min",
	"mL/min":    "mL/min",
	"ml/min":    "mL/min",
	"kg":        "kg",
	"g":         "g",
	"cm":        "cm",
	"mm":        "mm",
	"mmHg":      "mm[Hg]",
	"mm[Hg]":    "mm[Hg]",
	"mmhg":      "mm[Hg]",
	"/min":      "/min",
	"bpm":       "/min",
	"beats/min": "/min",
	"Cel":       "Cel",
	"degC":      "Cel",
	"C":         "Cel",
	"degF":      "[degF]",
	"F":         "[degF]",
	// Cell counts are the ones most often mangled, because the same value is
	// written half a dozen ways.
	"10*3/uL":   "10*3/uL",
	"K/uL":      "10*3/uL",
	"K/mcL":     "10*3/uL",
	"th/uL":     "10*3/uL",
	"x10E3/uL":  "10*3/uL",
	"10*6/uL":   "10*6/uL",
	"M/uL":      "10*6/uL",
	"mil/uL":    "10*6/uL",
	"x10E6/uL":  "10*6/uL",
	"10*9/L":    "10*9/L",
	"10*12/L":   "10*12/L",
	"cells/uL":  "/uL",
	"/uL":       "/uL",
	"/mm3":      "/mm3",
	"copies/mL": "{copies}/mL",
	// Further units that are already valid UCUM as written. "ug/L" (troponin, among others) was reported as having no
	// UCUM code, so the quantity went out as text only.
	"ug/L":     "ug/L",
	"mcg/L":    "ug/L",
	"ng/L":     "ng/L",
	"mg/L":     "mg/L",
	"ug/dL":    "ug/dL",
	"mcg/dL":   "ug/dL",
	"ng/dL":    "ng/dL",
	"nmol/L":   "nmol/L",
	"pmol/L":   "pmol/L",
	"mmol/mol": "mmol/mol",
	"mIU/L":    "m[IU]/L",
	"mIU/mL":   "m[IU]/mL",
	"uIU/mL":   "u[IU]/mL",
	"U/mL":     "U/mL",
	"mL":       "mL",
	"L":        "L",
	"mg/g":     "mg/g",
	"/HPF":     "/[HPF]",
	"/LPF":     "/[LPF]",
}

// mapUCUM returns the UCUM code for a unit string, and whether it was recognised.
func mapUCUM(unit string) (string, bool) {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		return "", false
	}
	if code, ok := ucumUnits[unit]; ok {
		return code, true
	}
	if code, ok := ucumUnits[strings.ToLower(unit)]; ok {
		return code, true
	}
	return "", false
}

// mapCodingSystem returns the FHIR system URI for an HL7 coding system id.
func mapCodingSystem(id string) (string, bool) {
	id = strings.ToUpper(strings.TrimSpace(id))
	if id == "" {
		return "", false
	}
	if uri, ok := codingSystemMap[id]; ok {
		return uri, true
	}
	// HL7-defined tables, named HL7 and four digits (HL70002, HL70163), are code systems on terminology.hl7.org.
	if len(id) == 7 && strings.HasPrefix(id, "HL7") && strings.Trim(id[3:], "0123456789") == "" {
		return fhir.SystemV2Table + id[3:], true
	}
	// A local code system is real and common. Naming it honestly beats pretending
	// it is a standard one.
	return "", false
}

// codedValue converts an HL7 coded element into a CodeableConcept.
//
// The components are identifier, text, coding system, and then the alternate
// triplet. Both are used when present, so a local code paired with a LOINC code
// keeps both rather than losing one.
func (c *converter) codedValue(path, sourceLabel string) *fhir.CodeableConcept {
	code := c.get(path + ".1")
	text := c.get(path + ".2")
	system := c.get(path + ".3")
	altCode := c.get(path + ".4")
	altText := c.get(path + ".5")
	altSystem := c.get(path + ".6")

	if code == "" && text == "" && altCode == "" && altText == "" {
		return nil
	}

	concept := &fhir.CodeableConcept{}
	if text != "" {
		concept.Text = text
	} else if altText != "" {
		concept.Text = altText
	}

	add := func(code, display, systemID string) {
		if code == "" {
			return
		}
		uri, known := mapCodingSystem(systemID)
		if !known {
			if systemID == "" {
				c.note("warning", sourceLabel, "code.coding",
					"code %q has no coding system, so it is recorded without one; a receiver cannot interpret it reliably", code)
			} else {
				// A local system gets a namespaced URI derived from its name so
				// the code is at least attributable, and the note says it is local.
				//
				// Not a urn-oid prefix, which would announce a registered object identifier and carry a word instead. See
				// placeholderSystem.
				uri = placeholderSystem(systemID)
				c.note("warning", sourceLabel, "code.coding.system",
					"coding system %q is not a standard system; recorded as %s", systemID, uri)
			}
		}
		// A code claimed to be from an HL7 table it is not in. An NHS Wales sample sends "SPOUSE^^HL70063" where table 0063
		// says SPO; labelling it 0063 states something false, which the HL7 validator rejects. It is kept, without the
		// table, and the note says why.
		if known {
			if members, ok := checkedTables[uri]; ok && !members[strings.ToUpper(code)] {
				c.note("warning", sourceLabel, "code.coding.system",
					"code %q is not in %s, which the sender named, so it is recorded without a coding system", code, systemID)
				uri, known = "", false
			}
		}
		// CVX codes below ten are two digits ("03", MMR). A sender writing "3" means the same code, and CVX has no "3", so the
		// HL7 validator rejected it; it is padded, and the note says so.
		if uri == "http://hl7.org/fhir/sid/cvx" && len(code) == 1 && code[0] >= '0' && code[0] <= '9' {
			c.note("info", sourceLabel, "code.coding.code", "CVX code %q was written as 0%s, the form CVX defines", code, code)
			code = "0" + code
		}
		// ICD-9-CM and ICD-10-CM codes have a dot after the category (715.96, E11.9). v2 feeds commonly leave it out
		// ("71596^...^I9"); the undotted form is not a code in either system, and the HL7 validator rejects it.
		if dotted, ok := dotICD(uri, code); ok {
			c.note("info", sourceLabel, "code.coding.code", "ICD code %q was written as %s, the form the code system defines", code, dotted)
			code = dotted
		}
		// The sender's text is not the code system's display. For a standard system - LOINC, SNOMED, CVX, RxNorm, ICD - a
		// display that differs from the system's own is an error to a terminology-aware validator, and a v2 sender's text
		// usually does differ ("Comprehensive metabolic panel" for LOINC's "Comprehensive metabolic 2000 panel - Serum or
		// Plasma"). The text is kept as the concept's text, which is what it is. A local code keeps it as the display too,
		// since there is no system display for it to contradict.
		if known && display != "" {
			if concept.Text == "" {
				concept.Text = display
			}
			display = ""
		}
		concept.Coding = append(concept.Coding, fhir.Coding{
			System: uri, Code: code, Display: display,
		})
	}

	add(code, text, system)
	add(altCode, altText, altSystem)

	if len(concept.Coding) == 0 && concept.Text == "" {
		return nil
	}
	return concept
}

// checkedTables are the HL7 tables whose codes are checked before a coding is labelled with one.
var checkedTables = map[string]map[string]bool{
	fhir.SystemV2Table + "0063": setOf(`ASC BRO CGV CHD DEP DOM EMC EME EMR EXF FCH FND FTH GCH GRD GRP LIF MGR MTH NCH NON OAD OTH
		OWN PAR SCH SEL SIB SIS SPO TRA UNK WRD`),
	fhir.SystemV2Table + "0131": setOf(`BP C CP E EP F I N O PR S U`),
}

func setOf(words string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.Fields(words) {
		out[w] = true
	}
	return out
}

// v2 timestamp conversion.
//
// HL7 v2 timestamps are YYYYMMDDHHMMSS[.S...][+/-ZZZZ] with any suffix optional,
// which means a bare date, a local time with no offset, or a full offset. FHIR
// requires an offset once a time is present. Copying the string through is the
// most common cause of a rejected resource, so this converts and reports when it
// had to supply a timezone.

// v2Date converts a v2 timestamp to a FHIR date, dropping any time component.
func (c *converter) v2Date(value, source string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	digits := digitsOnly(value)
	if bad := badV2Date(value, digits); bad != "" {
		c.note("warning", source, "", "%q is not a v2 date (%s) and was dropped rather than guessed at", value, bad)
		return ""
	}
	switch {
	case len(digits) >= 8:
		return fmt.Sprintf("%s-%s-%s", digits[0:4], digits[4:6], digits[6:8])
	case len(digits) >= 6:
		return fmt.Sprintf("%s-%s", digits[0:4], digits[4:6])
	case len(digits) >= 4:
		return digits[0:4]
	default:
		c.note("warning", source, "", "%q is not a usable date and was dropped", value)
		return ""
	}
}

// badV2Date says why a v2 DT or DTM is not one, or "" when it is. Two senders in the NHS Wales sample set sent
// "01/10/1948" and "196203520"; the digits were taken anyway and became the FHIR dates 0110-19-48 and 1962-03-52, which the
// HL7 validator rejects and which no reader should be shown. A v2 date is YYYY[MM[DD[HH[MM[SS[.S...]]]]]], digits only.
func badV2Date(raw, digits string) string {
	for _, r := range raw {
		if (r < '0' || r > '9') && r != '.' {
			return "only digits belong in it"
		}
	}
	n := len(strings.SplitN(raw, ".", 2)[0])
	if n != 4 && n != 6 && n != 8 && n != 10 && n != 12 && n != 14 {
		return fmt.Sprintf("%d digits fit none of its precisions", n)
	}
	if len(digits) >= 6 {
		if m := digits[4:6]; m < "01" || m > "12" {
			return "there is no month " + m
		}
	}
	if len(digits) >= 8 {
		if _, err := time.Parse("20060102", digits[:8]); err != nil {
			return "there is no day " + digits[6:8] + " in that month"
		}
	}
	if len(digits) >= 12 {
		if _, err := time.Parse("200601021504", digits[:12]); err != nil {
			return "the time is not a time of day"
		}
	}
	return ""
}

// v2DateTime converts a v2 timestamp to a FHIR dateTime.
func (c *converter) v2DateTime(value, source string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}

	// Split off an explicit offset if there is one.
	offset := ""
	body := value
	for i := 1; i < len(value); i++ {
		if value[i] == '+' || value[i] == '-' {
			body = value[:i]
			offset = value[i:]
			break
		}
	}

	digits := digitsOnly(body)
	if len(digits) < 4 {
		c.note("warning", source, "", "%q is not a usable timestamp and was dropped", value)
		return ""
	}
	if bad := badV2Date(body, digits); bad != "" {
		c.note("warning", source, "", "%q is not a v2 timestamp (%s) and was dropped rather than guessed at", value, bad)
		return ""
	}

	// A date with no time needs no offset, and adding one would assert a precision
	// the sender did not provide.
	if len(digits) < 10 {
		switch {
		case len(digits) >= 8:
			return fmt.Sprintf("%s-%s-%s", digits[0:4], digits[4:6], digits[6:8])
		case len(digits) >= 6:
			return fmt.Sprintf("%s-%s", digits[0:4], digits[4:6])
		default:
			return digits[0:4]
		}
	}

	year, month, day := digits[0:4], digits[4:6], digits[6:8]
	hour, minute := digits[8:10], "00"
	second := "00"
	if len(digits) >= 12 {
		minute = digits[10:12]
	}
	if len(digits) >= 14 {
		second = digits[12:14]
	}

	if offset == "" {
		// v2 permits a local time with no offset; FHIR does not. Something has to
		// supply one, and saying which was supplied is the difference between a
		// conversion a clinician can trust and one they cannot.
		//
		// v2 presumes such a time is the sender's local time. With no timezone configured, the offset the sender put on
		// MSH-7 is the best evidence of what that is, so it is used before falling back to UTC: an Australian feed
		// sending MSH-7 with +1000 and a birth time without one used to be read ten hours out.
		zone, why := c.opts.location(), ""
		if c.opts.Timezone == nil && c.senderZone != nil {
			zone, why = c.senderZone, " (the sender's offset on MSH-7)"
		}
		t, err := time.ParseInLocation("20060102150405",
			year+month+day+hour+minute+second, zone)
		if err != nil {
			c.note("warning", source, "", "%q could not be parsed as a timestamp", value)
			return ""
		}
		c.note("info", source, "",
			"timestamp %q had no timezone; %s%s was applied", value, zone.String(), why)
		return t.Format(time.RFC3339)
	}

	normalised := normaliseOffset(offset)
	if normalised == "" {
		c.note("warning", source, "", "offset %q in %q is not valid", offset, value)
		return ""
	}
	return fmt.Sprintf("%s-%s-%sT%s:%s:%s%s",
		year, month, day, hour, minute, second, normalised)
}

// v2Instant converts a v2 timestamp to a FHIR instant, which requires a full
// date, time and offset.
func (c *converter) v2Instant(value, source string) string {
	dt := c.v2DateTime(value, source)
	if dt == "" {
		return ""
	}
	// An instant needs seconds and an offset. A date-only result cannot be one.
	if len(dt) < 20 {
		c.note("info", source, "",
			"%q is not precise enough for an instant, so it was omitted", value)
		return ""
	}
	return dt
}

func normaliseOffset(offset string) string {
	sign := offset[0]
	if sign != '+' && sign != '-' {
		return ""
	}
	digits := digitsOnly(offset)
	if len(digits) < 4 {
		return ""
	}
	return fmt.Sprintf("%c%s:%s", sign, digits[0:2], digits[2:4])
}

func digitsOnly(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] >= '0' && s[i] <= '9' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// dotICD puts the dot into an ICD-9-CM or ICD-10-CM code sent without one. It changes nothing else: a code that already has a
// dot, is too short to need one, or is not shaped like the system's codes is left alone.
func dotICD(system, code string) (string, bool) {
	if strings.Contains(code, ".") {
		return "", false
	}
	alnum := func(s string) bool {
		for _, r := range s {
			if !(r >= '0' && r <= '9' || r >= 'A' && r <= 'Z') {
				return false
			}
		}
		return s != ""
	}
	digits := func(s string) bool { return s != "" && strings.Trim(s, "0123456789") == "" }
	code = strings.ToUpper(code)
	switch system {
	case "http://hl7.org/fhir/sid/icd-9-cm":
		switch {
		case len(code) > 3 && digits(code):
			return code[:3] + "." + code[3:], true
		case len(code) > 3 && code[0] == 'V' && digits(code[1:]):
			return code[:3] + "." + code[3:], true
		case len(code) > 4 && code[0] == 'E' && digits(code[1:]):
			return code[:4] + "." + code[4:], true
		}
	case "http://hl7.org/fhir/sid/icd-10-cm":
		if len(code) > 3 && code[0] >= 'A' && code[0] <= 'Z' && alnum(code) && digits(code[1:2]) {
			return code[:3] + "." + code[3:], true
		}
	}
	return "", false
}
