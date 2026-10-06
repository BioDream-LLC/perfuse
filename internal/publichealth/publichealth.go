// Package publichealth implements bidirectional public health reporting:
// electronic case reporting (eCR), electronic lab reporting (ELR), and
// immunization registry queries.
//
// Every hospital is legally required to report certain conditions — tuberculosis,
// measles, hepatitis, COVID-19, sexually transmitted infections, foodborne
// illness — to state and local public health agencies. The reporting happens
// through three distinct interfaces:
//
//   - ELR: HL7 2.5.1 ORU^R01 messages carrying lab results to public health agencies (elr.go).
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
	// Lab tests whose result is the trigger, as the RCTC lists them for ELR and eCR.
	{"94500-6", "LOINC", "COVID-19", "CDC", "immediate"},
	{"94309-2", "LOINC", "COVID-19", "CDC", "immediate"},
	{"840533007", "SNOMED", "COVID-19", "CDC", "immediate"}, // SARS-CoV-2 (organism), as a coded result
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
