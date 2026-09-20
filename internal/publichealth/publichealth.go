// Package publichealth implements bidirectional public health reporting:
// electronic case reporting (eCR), electronic lab reporting (ELR), and
// immunization registry queries.
//
// Every hospital is legally required to report certain conditions — tuberculosis,
// measles, hepatitis, COVID-19, sexually transmitted infections, foodborne
// illness — to state and local public health agencies. The reporting happens
// through three distinct interfaces:
//
//   - ELR: HL7 2.5.1 ORU^R01 messages carrying lab results to public health labs.
//   - eCR: FHIR-based electronic initial case reports (eICR) when a reportable
//     condition is diagnosed.
//   - Immunization registry: VXU^V04 to report vaccinations administered, and
//     VXQ^V01 to query a patient's immunization history.
//
// The package is bidirectional: it reports outward (ELR, eCR, VXU) and queries
// back (VXQ/VXR). All operations are real-time rather than nightly batch.
//
// Thread safety: SubmissionLog is safe for concurrent use. The builder functions
// are pure and safe to call from any goroutine.
package publichealth

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// hl7Seq is a global atomic counter for generating unique control IDs.
var hl7Seq uint64

// hl7Escape escapes HL7 v2 special characters in a field value where component
// separators (^) are expected as structural delimiters (e.g., names, coded values).
// It escapes: | -> \F\, ~ -> \R\, \ -> \E\, & -> \T\
// It preserves ^ since the caller uses it as the component separator.
func hl7Escape(s string) string {
	// Backslash must be escaped first to avoid double-escaping.
	s = strings.ReplaceAll(s, `\`, `\E\`)
	s = strings.ReplaceAll(s, "|", `\F\`)
	s = strings.ReplaceAll(s, "~", `\R\`)
	s = strings.ReplaceAll(s, "&", `\T\`)
	return s
}

// hl7EscapeAll escapes ALL HL7 v2 special characters including ^.
// Use this for subcomponent-level text that must not contain any delimiter.
func hl7EscapeAll(s string) string {
	s = strings.ReplaceAll(s, `\`, `\E\`)
	s = strings.ReplaceAll(s, "|", `\F\`)
	s = strings.ReplaceAll(s, "^", `\S\`)
	s = strings.ReplaceAll(s, "~", `\R\`)
	s = strings.ReplaceAll(s, "&", `\T\`)
	return s
}

// ---------------------------------------------------------------------------
// Reportable condition detection
// ---------------------------------------------------------------------------

// CodeValue represents a coded value from a clinical message.
type CodeValue struct {
	Code    string
	System  string
	Display string
}

// MatchResult describes a reportable condition that was detected.
type MatchResult struct {
	Condition    string // human-readable condition name
	Code         string
	CodeSystem   string
	Jurisdiction string // e.g. "state", "local", "CDC"
	Urgency      string // "immediate" or "routine"
}

// reportableEntry is an internal record used to build the default list.
type reportableEntry struct {
	Code         string
	System       string
	Condition    string
	Jurisdiction string
	Urgency      string
}

// ReportableConditions is the default set of conditions that trigger reporting.
// It contains ~20 common reportable conditions covering TB, measles, hepatitis,
// COVID, STIs, and foodborne illness.
var ReportableConditions = []reportableEntry{
	// Tuberculosis
	{"56717001", "SNOMED", "Tuberculosis", "state", "immediate"},
	{"A15", "ICD-10", "Tuberculosis", "state", "immediate"},
	// Measles
	{"14189004", "SNOMED", "Measles", "state", "immediate"},
	{"B05", "ICD-10", "Measles", "state", "immediate"},
	// Hepatitis A
	{"40468003", "SNOMED", "Hepatitis A", "state", "immediate"},
	{"B15", "ICD-10", "Hepatitis A", "state", "immediate"},
	// Hepatitis B
	{"66071002", "SNOMED", "Hepatitis B", "state", "routine"},
	{"B16", "ICD-10", "Hepatitis B", "state", "routine"},
	// Hepatitis C
	{"50711007", "SNOMED", "Hepatitis C", "state", "routine"},
	{"B17.1", "ICD-10", "Hepatitis C", "state", "routine"},
	// COVID-19
	{"840539006", "SNOMED", "COVID-19", "CDC", "immediate"},
	{"U07.1", "ICD-10", "COVID-19", "CDC", "immediate"},
	// Gonorrhea
	{"15628003", "SNOMED", "Gonorrhea", "state", "routine"},
	{"A54", "ICD-10", "Gonorrhea", "state", "routine"},
	// Chlamydia
	{"240589008", "SNOMED", "Chlamydia", "state", "routine"},
	{"A56", "ICD-10", "Chlamydia", "state", "routine"},
	// Syphilis
	{"76272004", "SNOMED", "Syphilis", "state", "routine"},
	{"A51", "ICD-10", "Syphilis", "state", "routine"},
	// HIV
	{"86406008", "SNOMED", "HIV", "state", "routine"},
	{"B20", "ICD-10", "HIV", "state", "routine"},
	// Salmonellosis
	{"302231008", "SNOMED", "Salmonellosis", "local", "routine"},
	{"A02", "ICD-10", "Salmonellosis", "local", "routine"},
	// E. coli (STEC)
	{"398565003", "SNOMED", "E. coli infection", "state", "immediate"},
	{"A04.3", "ICD-10", "E. coli infection", "state", "immediate"},
	// Pertussis
	{"27836007", "SNOMED", "Pertussis", "state", "immediate"},
	{"A37", "ICD-10", "Pertussis", "state", "immediate"},
	// Meningococcal disease
	{"23511006", "SNOMED", "Meningococcal disease", "state", "immediate"},
	{"A39", "ICD-10", "Meningococcal disease", "state", "immediate"},
	// Mumps
	{"36989005", "SNOMED", "Mumps", "state", "immediate"},
	{"B26", "ICD-10", "Mumps", "state", "immediate"},
	// Rubella
	{"36653000", "SNOMED", "Rubella", "state", "immediate"},
	{"B06", "ICD-10", "Rubella", "state", "immediate"},
	// Legionellosis
	{"80345001", "SNOMED", "Legionellosis", "state", "immediate"},
	{"A48.1", "ICD-10", "Legionellosis", "state", "immediate"},
	// Anthrax
	{"409498004", "SNOMED", "Anthrax", "CDC", "immediate"},
	{"A22", "ICD-10", "Anthrax", "CDC", "immediate"},
}

// IsReportable returns true if the given code/system pair matches a known
// reportable condition. The code system is compared case-insensitively.
func IsReportable(code, codeSystem string) bool {
	sys := strings.ToUpper(codeSystem)
	for _, entry := range ReportableConditions {
		if entry.Code == code && strings.ToUpper(entry.System) == sys {
			return true
		}
	}
	return false
}

// Detect scans a slice of coded values and returns all matches against the
// reportable conditions list. A single code can match at most once.
func Detect(codes []CodeValue) []MatchResult {
	var results []MatchResult
	for _, cv := range codes {
		sys := strings.ToUpper(cv.System)
		for _, entry := range ReportableConditions {
			if entry.Code == cv.Code && strings.ToUpper(entry.System) == sys {
				results = append(results, MatchResult{
					Condition:    entry.Condition,
					Code:         entry.Code,
					CodeSystem:   entry.System,
					Jurisdiction: entry.Jurisdiction,
					Urgency:      entry.Urgency,
				})
				break
			}
		}
	}
	return results
}

// ---------------------------------------------------------------------------
// Electronic Lab Reporting (ELR) — HL7 2.5.1 ORU^R01
// ---------------------------------------------------------------------------

// LabResult holds a single observation within an ELR message.
type LabResult struct {
	Code           string
	CodeSystem     string
	Display        string
	Value          string
	Units          string
	ReferenceRange string
	AbnormalFlag   string
	Status         string // F=final, P=preliminary, C=corrected
}

// ELRMessage holds the data needed to build an ORU^R01 for public health.
type ELRMessage struct {
	PatientID        string
	PatientName      string
	DOB              string // YYYYMMDD
	OrderingProvider string
	PerformingLab    string
	Results          []LabResult
	CollectionDate   time.Time
}

// ValidateELR checks an ELRMessage for completeness and returns any errors found.
func ValidateELR(msg ELRMessage) []string {
	var errs []string
	if msg.PatientID == "" {
		errs = append(errs, "PatientID is required")
	}
	if msg.PatientName == "" {
		errs = append(errs, "PatientName is required")
	}
	if msg.DOB == "" {
		errs = append(errs, "DOB is required")
	}
	if msg.OrderingProvider == "" {
		errs = append(errs, "OrderingProvider is required")
	}
	if msg.PerformingLab == "" {
		errs = append(errs, "PerformingLab is required")
	}
	if len(msg.Results) == 0 {
		errs = append(errs, "at least one LabResult is required")
	}
	if msg.CollectionDate.IsZero() {
		errs = append(errs, "CollectionDate is required")
	}
	for i, r := range msg.Results {
		if r.Code == "" {
			errs = append(errs, fmt.Sprintf("Results[%d].Code is required", i))
		}
		if r.Value == "" {
			errs = append(errs, fmt.Sprintf("Results[%d].Value is required", i))
		}
		if r.Status == "" {
			errs = append(errs, fmt.Sprintf("Results[%d].Status is required", i))
		}
	}
	return errs
}

// BuildORU generates an HL7 2.5.1 ORU^R01 message for electronic lab reporting
// to public health. Returns the serialized HL7 message bytes.
func BuildORU(msg ELRMessage) ([]byte, error) {
	if errs := ValidateELR(msg); len(errs) > 0 {
		return nil, fmt.Errorf("validation failed: %s", strings.Join(errs, "; "))
	}

	now := time.Now().Format("20060102150405")
	seq := atomic.AddUint64(&hl7Seq, 1)
	controlID := fmt.Sprintf("ELR%s.%d", now, seq)
	collDate := msg.CollectionDate.Format("20060102150405")

	var b strings.Builder

	// MSH — Message Header
	b.WriteString("MSH|^~\\&|EHR|FACILITY|PH_LAB|PUBLIC_HEALTH|")
	b.WriteString(now)
	b.WriteString("||ORU^R01^ORU_R01|")
	b.WriteString(controlID)
	b.WriteString("|P|2.5.1|||NE|AL|||||PHLabReport-NoAck^ELR251R1_Rcvr_Prof^2.16.840.1.113883.9.11^ISO\r")

	// PID — Patient Identification
	b.WriteString("PID|1||")
	b.WriteString(hl7Escape(msg.PatientID))
	b.WriteString("^^^FACILITY^MR||")
	b.WriteString(hl7Escape(msg.PatientName))
	b.WriteString("||")
	b.WriteString(hl7Escape(msg.DOB))
	b.WriteString("||||||||||||||||||||||\r")

	// OBR — Observation Request
	b.WriteString("OBR|1|||^^^")
	b.WriteString(hl7Escape(msg.Results[0].CodeSystem))
	b.WriteString("^")
	b.WriteString(hl7Escape(msg.Results[0].Display))
	b.WriteString("|||")
	b.WriteString(collDate)
	b.WriteString("|||||||||")
	b.WriteString(hl7Escape(msg.OrderingProvider))
	b.WriteString("||||||||")
	b.WriteString(now)
	b.WriteString("|||F|||||||")
	b.WriteString(hl7Escape(msg.PerformingLab))
	b.WriteString("\r")

	// OBX — Observation segments
	for i, r := range msg.Results {
		b.WriteString(fmt.Sprintf("OBX|%d|", i+1))
		// Value type: CE for coded, NM for numeric, ST for string
		vt := "ST"
		if r.Units != "" {
			vt = "NM"
		}
		b.WriteString(vt)
		b.WriteString("|")
		b.WriteString(hl7Escape(r.Code))
		b.WriteString("^")
		b.WriteString(hl7Escape(r.Display))
		b.WriteString("^")
		b.WriteString(hl7Escape(r.CodeSystem))
		b.WriteString("||")
		b.WriteString(hl7Escape(r.Value))
		if r.Units != "" {
			b.WriteString("|")
			b.WriteString(hl7Escape(r.Units))
		} else {
			b.WriteString("|")
		}
		b.WriteString("|")
		b.WriteString(hl7Escape(r.ReferenceRange))
		b.WriteString("|")
		b.WriteString(r.AbnormalFlag)
		b.WriteString("|||")
		b.WriteString(r.Status)
		b.WriteString("|||")
		b.WriteString(collDate)
		b.WriteString("\r")
	}

	return []byte(b.String()), nil
}

// ---------------------------------------------------------------------------
// Electronic Case Reporting (eCR) — FHIR eICR Bundle
// ---------------------------------------------------------------------------

// CaseReport holds the data needed to generate a FHIR eICR bundle.
type CaseReport struct {
	PatientID         string
	Condition         string // SNOMED or ICD-10 code
	DiagnosisDate     string // YYYY-MM-DD
	Jurisdiction      string
	ReportingFacility string
	Encounter         string // encounter ID
	Provider          string // provider name
}

// ValidateECR checks a CaseReport for completeness and returns any errors found.
func ValidateECR(report CaseReport) []string {
	var errs []string
	if report.PatientID == "" {
		errs = append(errs, "PatientID is required")
	}
	if report.Condition == "" {
		errs = append(errs, "Condition is required")
	}
	if report.DiagnosisDate == "" {
		errs = append(errs, "DiagnosisDate is required")
	}
	if report.ReportingFacility == "" {
		errs = append(errs, "ReportingFacility is required")
	}
	if report.Encounter == "" {
		errs = append(errs, "Encounter is required")
	}
	if report.Provider == "" {
		errs = append(errs, "Provider is required")
	}
	return errs
}

// BuildECR generates a FHIR Bundle (type=document) representing an electronic
// initial case report (eICR). The bundle contains Patient, Condition,
// Encounter, Practitioner, and Organization resources.
func BuildECR(report CaseReport) (map[string]interface{}, error) {
	if errs := ValidateECR(report); len(errs) > 0 {
		return nil, fmt.Errorf("validation failed: %s", strings.Join(errs, "; "))
	}

	patient := map[string]interface{}{
		"resourceType": "Patient",
		"id":           report.PatientID,
	}

	condition := map[string]interface{}{
		"resourceType": "Condition",
		"id":           "condition-1",
		"code": map[string]interface{}{
			"coding": []map[string]interface{}{
				{
					"system": "http://snomed.info/sct",
					"code":   report.Condition,
				},
			},
		},
		"subject": map[string]interface{}{
			"reference": "Patient/" + report.PatientID,
		},
		"onsetDateTime": report.DiagnosisDate,
	}

	encounter := map[string]interface{}{
		"resourceType": "Encounter",
		"id":           report.Encounter,
		"status":       "finished",
		"subject": map[string]interface{}{
			"reference": "Patient/" + report.PatientID,
		},
	}

	practitioner := map[string]interface{}{
		"resourceType": "Practitioner",
		"id":           "practitioner-1",
		"name": []map[string]interface{}{
			{"text": report.Provider},
		},
	}

	organization := map[string]interface{}{
		"resourceType": "Organization",
		"id":           "reporting-facility",
		"name":         report.ReportingFacility,
	}

	composition := map[string]interface{}{
		"resourceType": "Composition",
		"id":           "eicr-composition",
		"meta": map[string]interface{}{
			"profile": []string{
				"http://hl7.org/fhir/us/ecr/StructureDefinition/eicr-composition",
			},
		},
		"status": "final",
		"type": map[string]interface{}{
			"coding": []map[string]interface{}{
				{
					"system":  "http://loinc.org",
					"code":    "55751-2",
					"display": "Public Health Case Report",
				},
			},
		},
		"subject": map[string]interface{}{
			"reference": "Patient/" + report.PatientID,
		},
		"encounter": map[string]interface{}{
			"reference": "Encounter/" + report.Encounter,
		},
		"author": []map[string]interface{}{
			{"reference": "Practitioner/practitioner-1"},
		},
		"custodian": map[string]interface{}{
			"reference": "Organization/reporting-facility",
		},
		"date": report.DiagnosisDate,
		"section": []map[string]interface{}{
			{
				"title": "Reportable Condition",
				"code": map[string]interface{}{
					"coding": []map[string]interface{}{
						{
							"system":  "http://loinc.org",
							"code":    "29762-2",
							"display": "Social history",
						},
					},
				},
				"entry": []map[string]interface{}{
					{"reference": "Condition/condition-1"},
				},
			},
		},
	}

	bundle := map[string]interface{}{
		"resourceType": "Bundle",
		"type":         "document",
		"entry": []map[string]interface{}{
			{"fullUrl": "urn:uuid:composition-1", "resource": composition},
			{"fullUrl": "urn:uuid:patient-1", "resource": patient},
			{"fullUrl": "urn:uuid:condition-1", "resource": condition},
			{"fullUrl": "urn:uuid:encounter-1", "resource": encounter},
			{"fullUrl": "urn:uuid:practitioner-1", "resource": practitioner},
			{"fullUrl": "urn:uuid:organization-1", "resource": organization},
		},
	}

	return bundle, nil
}

// ---------------------------------------------------------------------------
// Immunization Registry — HL7 VXU^V04 / VXQ^V01 / VXR
// ---------------------------------------------------------------------------

// ImmunizationRecord holds vaccination data for registry reporting or query
// responses.
type ImmunizationRecord struct {
	PatientID        string
	VaccineCode      string
	VaccineName      string
	AdminDate        string // YYYYMMDD
	LotNumber        string
	Site             string
	Route            string
	Manufacturer     string
	Provider         string
	DoseNumber       int
	CompletionStatus string // CP=Complete, RE=Refused, NA=Not Administered, PA=Partially Administered
	RefusalReason    string // NIP002 code (e.g., "00"=Parental decision)
	ActionCode       string // A=Add, D=Delete, U=Update
}

// BuildVXU generates an HL7 2.5.1 VXU^V04 message to report a vaccination
// administration to an immunization registry.
func BuildVXU(record ImmunizationRecord) ([]byte, error) {
	if record.PatientID == "" {
		return nil, fmt.Errorf("PatientID is required")
	}
	if record.VaccineCode == "" {
		return nil, fmt.Errorf("VaccineCode is required")
	}
	if record.AdminDate == "" {
		return nil, fmt.Errorf("AdminDate is required")
	}

	now := time.Now().Format("20060102150405")
	seq := atomic.AddUint64(&hl7Seq, 1)
	controlID := fmt.Sprintf("VXU%s.%d", now, seq)

	// Default completion status to CP (complete) if not specified.
	completionStatus := record.CompletionStatus
	if completionStatus == "" {
		completionStatus = "CP"
	}

	// Default action code to A (Add) if not specified.
	actionCode := record.ActionCode
	if actionCode == "" {
		actionCode = "A"
	}

	var b strings.Builder

	// MSH
	b.WriteString("MSH|^~\\&|EHR|FACILITY|IIS|REGISTRY|")
	b.WriteString(now)
	b.WriteString("||VXU^V04^VXU_V04|")
	b.WriteString(controlID)
	b.WriteString("|P|2.5.1|||ER|AL\r")

	// PID
	b.WriteString("PID|1||")
	b.WriteString(hl7Escape(record.PatientID))
	b.WriteString("^^^FACILITY^MR\r")

	// ORC — Order Control
	b.WriteString("ORC|RE\r")

	// RXA — Pharmacy/Treatment Administration
	// RXA fields (1-based): 1=GiveSubIDCounter, 2=AdminSubIDCounter,
	// 3=DateTimeStart, 4=DateTimeEnd, 5=AdminCode, 6=AdminAmount,
	// 7=AdminUnits, 8=AdminNotes, 9=AdminNotes, 10=AdminProvider,
	// 11=AdminAddress, 12=AdminPerUnit, 13=AdminStrength,
	// 14=AdminStrengthUnits, 15=SubstanceLotNumber, 16=SubstanceExpiration,
	// 17=SubstanceManufacturer, 18=SubstanceRefusalReason,
	// 19=Indication, 20=CompletionStatus, 21=ActionCode
	b.WriteString("RXA|0|1|")
	b.WriteString(record.AdminDate)
	b.WriteString("|")
	b.WriteString(record.AdminDate)
	b.WriteString("|")
	b.WriteString(hl7Escape(record.VaccineCode))
	b.WriteString("^")
	b.WriteString(hl7Escape(record.VaccineName))
	b.WriteString("^CVX")
	b.WriteString("|999|") // RXA-6: amount (999=unknown)
	b.WriteString("|")     // RXA-7: units (empty)
	b.WriteString("|")     // RXA-8: dosage form (empty)
	b.WriteString("|")     // RXA-9: admin notes (empty)
	// RXA-10: administering provider
	if record.Provider != "" {
		b.WriteString("01^")
		b.WriteString(hl7Escape(record.Provider))
	}
	b.WriteString("|") // end of RXA-10
	// RXA-11: admin location
	if record.Site != "" {
		b.WriteString(hl7Escape(record.Site))
	}
	b.WriteString("|") // end of RXA-11
	b.WriteString("|") // RXA-12: admin per unit
	b.WriteString("|") // RXA-13: admin strength
	b.WriteString("|") // RXA-14: admin strength units
	// RXA-15: lot number
	if record.LotNumber != "" {
		b.WriteString(hl7Escape(record.LotNumber))
	}
	b.WriteString("|") // end of RXA-15
	b.WriteString("|") // RXA-16: substance expiration
	// RXA-17: manufacturer
	if record.Manufacturer != "" {
		b.WriteString(hl7Escape(record.Manufacturer))
	}
	b.WriteString("|") // end of RXA-17
	// RXA-18: refusal reason
	if record.RefusalReason != "" {
		b.WriteString(record.RefusalReason)
	}
	b.WriteString("|") // end of RXA-18
	b.WriteString("|") // RXA-19: indication
	// RXA-20: completion status
	b.WriteString(completionStatus)
	b.WriteString("|") // end of RXA-20
	// RXA-21: action code
	b.WriteString(actionCode)
	b.WriteString("\r")

	// RXR — Route
	if record.Route != "" {
		b.WriteString("RXR|")
		b.WriteString(hl7Escape(record.Route))
		b.WriteString("\r")
	}

	return []byte(b.String()), nil
}

// BuildVXQ generates an HL7 2.5.1 VXQ^V01 message to query an immunization
// registry for a patient's vaccination history.
func BuildVXQ(patientID, patientName, dob string) ([]byte, error) {
	if patientID == "" {
		return nil, fmt.Errorf("patientID is required")
	}

	now := time.Now().Format("20060102150405")
	seq := atomic.AddUint64(&hl7Seq, 1)
	controlID := fmt.Sprintf("VXQ%s.%d", now, seq)

	var b strings.Builder

	// MSH
	b.WriteString("MSH|^~\\&|EHR|FACILITY|IIS|REGISTRY|")
	b.WriteString(now)
	b.WriteString("||VXQ^V01^VXQ_V01|")
	b.WriteString(controlID)
	b.WriteString("|P|2.5.1|||ER|AL\r")

	// QRD — Query Definition
	b.WriteString("QRD|")
	b.WriteString(now)
	b.WriteString("|R|I|")
	b.WriteString(controlID)
	b.WriteString("|||RD|")
	b.WriteString(hl7Escape(patientID))
	b.WriteString("^")
	if patientName != "" {
		b.WriteString(hl7Escape(patientName))
	}
	b.WriteString("|VXI^Vaccine Information^HL70048\r")

	// QRF — Query Filter
	if dob != "" {
		b.WriteString("QRF|")
		b.WriteString(hl7Escape(dob))
		b.WriteString("\r")
	}

	return []byte(b.String()), nil
}

// ParseVXR parses an HL7 VXR (immunization registry response) and extracts
// immunization records from RXA segments.
func ParseVXR(data []byte) ([]ImmunizationRecord, error) {
	msg := string(data)
	segments := strings.Split(msg, "\r")
	if len(segments) == 0 {
		return nil, fmt.Errorf("empty message")
	}

	// Verify this looks like a VXR response.
	if len(segments) < 1 || !strings.HasPrefix(segments[0], "MSH|") {
		return nil, fmt.Errorf("invalid HL7 message: missing MSH segment")
	}

	var records []ImmunizationRecord
	var currentPatientID string

	for _, seg := range segments {
		fields := strings.Split(seg, "|")
		if len(fields) < 2 {
			continue
		}

		switch fields[0] {
		case "PID":
			// PID|1||PATIENTID^^^...
			if len(fields) > 3 {
				idParts := strings.Split(fields[3], "^")
				if len(idParts) > 0 {
					currentPatientID = idParts[0]
				}
			}
		case "RXA":
			// RXA|0|1|AdminDate|AdminDate|Code^Name^CVX|Amount|Units|
			//     AdmMethod|AdmSite|Provider|...(several)...|LotNumber|Manufacturer
			// Field indices (0-based after split on |):
			//   0=RXA, 3=AdminDate, 5=VaccineCode, 17=LotNumber, 18=Manufacturer
			rec := ImmunizationRecord{PatientID: currentPatientID}
			if len(fields) > 3 {
				rec.AdminDate = fields[3]
			}
			if len(fields) > 5 {
				codeParts := strings.Split(fields[5], "^")
				if len(codeParts) > 0 {
					rec.VaccineCode = codeParts[0]
				}
				if len(codeParts) > 1 {
					rec.VaccineName = codeParts[1]
				}
			}
			if len(fields) > 17 {
				rec.LotNumber = fields[17]
			}
			if len(fields) > 18 {
				rec.Manufacturer = fields[18]
			}
			records = append(records, rec)
		}
	}

	return records, nil
}

// ---------------------------------------------------------------------------
// Submission tracking
// ---------------------------------------------------------------------------

// Submission records a public health report or query that was sent.
type Submission struct {
	ID          string
	Type        string // "elr", "ecr", "vxu", "vxq"
	Timestamp   time.Time
	Destination string
	Status      string // "sent", "accepted", "rejected"
	ErrorDetail string
}

// SubmissionLog tracks all public health submissions. Thread-safe.
type SubmissionLog struct {
	mu      sync.RWMutex
	entries []Submission
	nextID  int
}

// NewSubmissionLog returns an empty submission log.
func NewSubmissionLog() *SubmissionLog {
	return &SubmissionLog{}
}

// Record adds a submission entry and returns its assigned ID.
func (l *SubmissionLog) Record(subType, destination, status, errorDetail string) string {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.nextID++
	id := fmt.Sprintf("SUB-%06d", l.nextID)
	l.entries = append(l.entries, Submission{
		ID:          id,
		Type:        subType,
		Timestamp:   time.Now(),
		Destination: destination,
		Status:      status,
		ErrorDetail: errorDetail,
	})
	return id
}

// Query returns all submissions matching the given type. Pass "" to get all.
func (l *SubmissionLog) Query(subType string) []Submission {
	l.mu.RLock()
	defer l.mu.RUnlock()
	if subType == "" {
		result := make([]Submission, len(l.entries))
		copy(result, l.entries)
		return result
	}
	var result []Submission
	for _, s := range l.entries {
		if s.Type == subType {
			result = append(result, s)
		}
	}
	return result
}
