package publichealth

import (
	"strings"
	"testing"
	"time"
)

// ---------------------------------------------------------------------------
// Reportable condition detection
// ---------------------------------------------------------------------------

func TestIsReportable_TB(t *testing.T) {
	if !IsReportable("56717001", "SNOMED") {
		t.Fatal("TB SNOMED code should be reportable")
	}
	if !IsReportable("A15", "ICD-10") {
		t.Fatal("TB ICD-10 code should be reportable")
	}
}

func TestIsReportable_COVID(t *testing.T) {
	if !IsReportable("840539006", "SNOMED") {
		t.Fatal("COVID-19 SNOMED code should be reportable")
	}
	if !IsReportable("U07.1", "ICD-10") {
		t.Fatal("COVID-19 ICD-10 code should be reportable")
	}
}

func TestIsReportable_Hepatitis(t *testing.T) {
	if !IsReportable("40468003", "SNOMED") {
		t.Fatal("Hepatitis A SNOMED code should be reportable")
	}
	if !IsReportable("66071002", "SNOMED") {
		t.Fatal("Hepatitis B SNOMED code should be reportable")
	}
	if !IsReportable("B17.1", "ICD-10") {
		t.Fatal("Hepatitis C ICD-10 code should be reportable")
	}
}

func TestIsReportable_CommonCold(t *testing.T) {
	// Common cold (J00) is NOT reportable.
	if IsReportable("J00", "ICD-10") {
		t.Fatal("common cold should not be reportable")
	}
	if IsReportable("82272006", "SNOMED") {
		t.Fatal("common cold SNOMED should not be reportable")
	}
}

func TestIsReportable_CaseInsensitiveSystem(t *testing.T) {
	if !IsReportable("56717001", "snomed") {
		t.Fatal("code system comparison should be case-insensitive")
	}
	if !IsReportable("A15", "icd-10") {
		t.Fatal("code system comparison should be case-insensitive")
	}
}

func TestDetect_MultipleMatches(t *testing.T) {
	codes := []CodeValue{
		{Code: "56717001", System: "SNOMED", Display: "Tuberculosis"},
		{Code: "840539006", System: "SNOMED", Display: "COVID-19"},
		{Code: "J00", System: "ICD-10", Display: "Common cold"},
	}
	results := Detect(codes)
	if len(results) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(results))
	}
	conditions := map[string]bool{}
	for _, r := range results {
		conditions[r.Condition] = true
	}
	if !conditions["Tuberculosis"] {
		t.Error("expected Tuberculosis in results")
	}
	if !conditions["COVID-19"] {
		t.Error("expected COVID-19 in results")
	}
}

func TestDetect_Urgency(t *testing.T) {
	codes := []CodeValue{
		{Code: "56717001", System: "SNOMED", Display: "TB"},        // immediate
		{Code: "15628003", System: "SNOMED", Display: "Gonorrhea"}, // routine
	}
	results := Detect(codes)
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	urgencies := map[string]string{}
	for _, r := range results {
		urgencies[r.Condition] = r.Urgency
	}
	if urgencies["Tuberculosis"] != "immediate" {
		t.Errorf("TB urgency: want immediate, got %s", urgencies["Tuberculosis"])
	}
	if urgencies["Gonorrhea"] != "routine" {
		t.Errorf("Gonorrhea urgency: want routine, got %s", urgencies["Gonorrhea"])
	}
}

func TestDetect_NoMatches(t *testing.T) {
	codes := []CodeValue{
		{Code: "J00", System: "ICD-10", Display: "Common cold"},
		{Code: "R05", System: "ICD-10", Display: "Cough"},
	}
	results := Detect(codes)
	if len(results) != 0 {
		t.Fatalf("expected no matches, got %d", len(results))
	}
}

func TestDetect_Jurisdiction(t *testing.T) {
	codes := []CodeValue{
		{Code: "840539006", System: "SNOMED", Display: "COVID-19"},   // CDC
		{Code: "302231008", System: "SNOMED", Display: "Salmonella"}, // local
	}
	results := Detect(codes)
	jurisdictions := map[string]string{}
	for _, r := range results {
		jurisdictions[r.Condition] = r.Jurisdiction
	}
	if jurisdictions["COVID-19"] != "CDC" {
		t.Errorf("COVID jurisdiction: want CDC, got %s", jurisdictions["COVID-19"])
	}
	if jurisdictions["Salmonellosis"] != "local" {
		t.Errorf("Salmonellosis jurisdiction: want local, got %s", jurisdictions["Salmonellosis"])
	}
}

// ---------------------------------------------------------------------------
// ELR — ORU^R01
// ---------------------------------------------------------------------------

func validELR() ELRMessage {
	return ELRMessage{
		PatientID:        "MRN12345",
		PatientName:      "DOE^JOHN",
		DOB:              "19800115",
		OrderingProvider: "SMITH^JANE",
		PerformingLab:    "STATE_PH_LAB",
		CollectionDate:   time.Date(2026, 8, 20, 10, 30, 0, 0, time.UTC),
		Results: []LabResult{
			{
				Code:           "11585-7",
				CodeSystem:     "LN",
				Display:        "M. tuberculosis DNA",
				Value:          "Detected",
				Units:          "",
				ReferenceRange: "Not Detected",
				AbnormalFlag:   "A",
				Status:         "F",
			},
		},
	}
}

func TestBuildORU_ValidMessage(t *testing.T) {
	data, err := BuildORU(validELR())
	if err != nil {
		t.Fatalf("BuildORU failed: %v", err)
	}
	msg := string(data)

	// Must have MSH segment with correct message type.
	if !strings.Contains(msg, "MSH|^~\\&|") {
		t.Error("missing MSH segment")
	}
	if !strings.Contains(msg, "ORU^R01^ORU_R01") {
		t.Error("missing ORU^R01 message type")
	}
	if !strings.Contains(msg, "|2.5.1|") {
		t.Error("missing HL7 version 2.5.1")
	}

	// Must have PID segment.
	if !strings.Contains(msg, "PID|1||MRN12345") {
		t.Error("missing PID segment with patient ID")
	}
	if !strings.Contains(msg, "DOE^JOHN") {
		t.Error("missing patient name in PID")
	}

	// Must have OBR segment.
	if !strings.Contains(msg, "OBR|1|") {
		t.Error("missing OBR segment")
	}

	// Must have OBX segment.
	if !strings.Contains(msg, "OBX|1|") {
		t.Error("missing OBX segment")
	}
	if !strings.Contains(msg, "11585-7") {
		t.Error("missing test code in OBX")
	}
	if !strings.Contains(msg, "Detected") {
		t.Error("missing result value in OBX")
	}
}

func TestBuildORU_MultipleOBX(t *testing.T) {
	elr := validELR()
	elr.Results = append(elr.Results, LabResult{
		Code:       "5671-3",
		CodeSystem: "LN",
		Display:    "Lead BPb Qn",
		Value:      "4.2",
		Units:      "ug/dL",
		Status:     "F",
	})
	data, err := BuildORU(elr)
	if err != nil {
		t.Fatalf("BuildORU failed: %v", err)
	}
	msg := string(data)
	if !strings.Contains(msg, "OBX|1|") {
		t.Error("missing OBX|1|")
	}
	if !strings.Contains(msg, "OBX|2|") {
		t.Error("missing OBX|2|")
	}
}

func TestBuildORU_NumericValueType(t *testing.T) {
	elr := validELR()
	elr.Results = []LabResult{
		{
			Code:       "5671-3",
			CodeSystem: "LN",
			Display:    "Lead BPb Qn",
			Value:      "4.2",
			Units:      "ug/dL",
			Status:     "F",
		},
	}
	data, err := BuildORU(elr)
	if err != nil {
		t.Fatalf("BuildORU failed: %v", err)
	}
	msg := string(data)
	// When units are present, value type should be NM.
	if !strings.Contains(msg, "OBX|1|NM|") {
		t.Error("expected NM value type for numeric result")
	}
}

func TestValidateELR_MissingFields(t *testing.T) {
	errs := ValidateELR(ELRMessage{})
	if len(errs) == 0 {
		t.Fatal("expected validation errors for empty message")
	}
	// Should catch the main required fields.
	joined := strings.Join(errs, " ")
	for _, field := range []string{"PatientID", "PatientName", "DOB", "OrderingProvider", "PerformingLab", "LabResult", "CollectionDate"} {
		if !strings.Contains(joined, field) {
			t.Errorf("expected error mentioning %s", field)
		}
	}
}

func TestValidateELR_MissingResultFields(t *testing.T) {
	msg := validELR()
	msg.Results = []LabResult{{}} // empty result
	errs := ValidateELR(msg)
	joined := strings.Join(errs, " ")
	if !strings.Contains(joined, "Results[0].Code") {
		t.Error("expected error for missing result code")
	}
	if !strings.Contains(joined, "Results[0].Value") {
		t.Error("expected error for missing result value")
	}
	if !strings.Contains(joined, "Results[0].Status") {
		t.Error("expected error for missing result status")
	}
}

func TestBuildORU_ValidationFails(t *testing.T) {
	_, err := BuildORU(ELRMessage{})
	if err == nil {
		t.Fatal("expected error for invalid message")
	}
	if !strings.Contains(err.Error(), "validation failed") {
		t.Errorf("unexpected error: %v", err)
	}
}

// ---------------------------------------------------------------------------
// eCR — FHIR eICR Bundle
// ---------------------------------------------------------------------------

func validCaseReport() CaseReport {
	return CaseReport{
		PatientID:         "PAT-001",
		Condition:         "840539006",
		DiagnosisDate:     "2026-08-20",
		Jurisdiction:      "CDC",
		ReportingFacility: "General Hospital",
		Encounter:         "ENC-001",
		Provider:          "Dr. Jane Smith",
	}
}

func TestBuildECR_ValidBundle(t *testing.T) {
	bundle, err := BuildECR(validCaseReport())
	if err != nil {
		t.Fatalf("BuildECR failed: %v", err)
	}

	// Must be a Bundle resource.
	if bundle["resourceType"] != "Bundle" {
		t.Errorf("resourceType: want Bundle, got %v", bundle["resourceType"])
	}
	if bundle["type"] != "document" {
		t.Errorf("type: want document, got %v", bundle["type"])
	}

	// Must have entries.
	entries, ok := bundle["entry"].([]map[string]interface{})
	if !ok {
		t.Fatal("entries is not the expected type")
	}
	if len(entries) < 4 {
		t.Errorf("expected at least 4 entries, got %d", len(entries))
	}

	// First entry should be Composition.
	firstResource := entries[0]["resource"].(map[string]interface{})
	if firstResource["resourceType"] != "Composition" {
		t.Errorf("first entry: want Composition, got %v", firstResource["resourceType"])
	}

	// Check that Patient resource is present.
	found := false
	for _, entry := range entries {
		res := entry["resource"].(map[string]interface{})
		if res["resourceType"] == "Patient" && res["id"] == "PAT-001" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Patient resource not found in bundle")
	}

	// Check that Condition resource is present.
	found = false
	for _, entry := range entries {
		res := entry["resource"].(map[string]interface{})
		if res["resourceType"] == "Condition" {
			found = true
			break
		}
	}
	if !found {
		t.Error("Condition resource not found in bundle")
	}
}

func TestValidateECR_MissingFields(t *testing.T) {
	errs := ValidateECR(CaseReport{})
	if len(errs) == 0 {
		t.Fatal("expected validation errors for empty report")
	}
	joined := strings.Join(errs, " ")
	for _, field := range []string{"PatientID", "Condition", "DiagnosisDate", "ReportingFacility", "Encounter", "Provider"} {
		if !strings.Contains(joined, field) {
			t.Errorf("expected error mentioning %s", field)
		}
	}
}

func TestBuildECR_ValidationFails(t *testing.T) {
	_, err := BuildECR(CaseReport{})
	if err == nil {
		t.Fatal("expected error for empty case report")
	}
}

func TestBuildECR_ContainsOrganization(t *testing.T) {
	bundle, err := BuildECR(validCaseReport())
	if err != nil {
		t.Fatalf("BuildECR failed: %v", err)
	}
	entries := bundle["entry"].([]map[string]interface{})
	found := false
	for _, entry := range entries {
		res := entry["resource"].(map[string]interface{})
		if res["resourceType"] == "Organization" {
			if res["name"] == "General Hospital" {
				found = true
			}
		}
	}
	if !found {
		t.Error("Organization resource with facility name not found")
	}
}

// ---------------------------------------------------------------------------
// Immunization registry — VXU / VXQ / VXR
// ---------------------------------------------------------------------------

func TestBuildVXU_Valid(t *testing.T) {
	rec := ImmunizationRecord{
		PatientID:    "PAT-001",
		VaccineCode:  "08",
		VaccineName:  "Hepatitis B",
		AdminDate:    "20260815",
		LotNumber:    "LOT123",
		Site:         "LA",
		Route:        "IM",
		Manufacturer: "MSD",
		Provider:     "Dr. Smith",
		DoseNumber:   1,
	}
	data, err := BuildVXU(rec)
	if err != nil {
		t.Fatalf("BuildVXU failed: %v", err)
	}
	msg := string(data)

	if !strings.Contains(msg, "MSH|^~\\&|") {
		t.Error("missing MSH segment")
	}
	if !strings.Contains(msg, "VXU^V04^VXU_V04") {
		t.Error("missing VXU^V04 message type")
	}
	if !strings.Contains(msg, "|2.5.1|") {
		t.Error("missing HL7 version")
	}
	if !strings.Contains(msg, "PID|1||PAT-001") {
		t.Error("missing PID segment")
	}
	if !strings.Contains(msg, "RXA|") {
		t.Error("missing RXA segment")
	}
	if !strings.Contains(msg, "08^Hepatitis B^CVX") {
		t.Error("missing vaccine code in RXA")
	}
	if !strings.Contains(msg, "RXR|IM") {
		t.Error("missing RXR route segment")
	}
}

func TestBuildVXU_MissingPatientID(t *testing.T) {
	_, err := BuildVXU(ImmunizationRecord{VaccineCode: "08", AdminDate: "20260815"})
	if err == nil {
		t.Fatal("expected error for missing PatientID")
	}
}

func TestBuildVXU_MissingVaccineCode(t *testing.T) {
	_, err := BuildVXU(ImmunizationRecord{PatientID: "P1", AdminDate: "20260815"})
	if err == nil {
		t.Fatal("expected error for missing VaccineCode")
	}
}

func TestBuildVXQ_Valid(t *testing.T) {
	data, err := BuildVXQ("PAT-001", "DOE^JOHN", "19800115")
	if err != nil {
		t.Fatalf("BuildVXQ failed: %v", err)
	}
	msg := string(data)

	if !strings.Contains(msg, "MSH|^~\\&|") {
		t.Error("missing MSH segment")
	}
	if !strings.Contains(msg, "VXQ^V01^VXQ_V01") {
		t.Error("missing VXQ^V01 message type")
	}
	if !strings.Contains(msg, "QRD|") {
		t.Error("missing QRD segment")
	}
	if !strings.Contains(msg, "PAT-001") {
		t.Error("missing patient ID in QRD")
	}
	if !strings.Contains(msg, "QRF|19800115") {
		t.Error("missing QRF with DOB")
	}
}

func TestBuildVXQ_MissingPatientID(t *testing.T) {
	_, err := BuildVXQ("", "DOE^JOHN", "19800115")
	if err == nil {
		t.Fatal("expected error for missing patientID")
	}
}

func TestBuildVXQ_NoDOB(t *testing.T) {
	data, err := BuildVXQ("PAT-001", "DOE^JOHN", "")
	if err != nil {
		t.Fatalf("BuildVXQ failed: %v", err)
	}
	msg := string(data)
	// No QRF segment when DOB is empty.
	if strings.Contains(msg, "QRF|") {
		t.Error("QRF should not be present when DOB is empty")
	}
}

func TestParseVXR_Valid(t *testing.T) {
	// Simulated VXR response with two immunizations.
	vxr := "MSH|^~\\&|IIS|REGISTRY|EHR|FACILITY|20260825||VXR^V03|MSG001|P|2.5.1\r" +
		"PID|1||PAT-001^^^REGISTRY^MR\r" +
		"RXA|0|1|20260101|20260101|08^Hepatitis B^CVX|999|||01^Dr Smith||||||||LOT-A|MFG-1\r" +
		"RXA|0|1|20260315|20260315|03^MMR^CVX|999|||01^Dr Smith||||||||LOT-B|MFG-2\r"

	records, err := ParseVXR([]byte(vxr))
	if err != nil {
		t.Fatalf("ParseVXR failed: %v", err)
	}
	if len(records) != 2 {
		t.Fatalf("expected 2 records, got %d", len(records))
	}

	if records[0].PatientID != "PAT-001" {
		t.Errorf("record[0] PatientID: want PAT-001, got %s", records[0].PatientID)
	}
	if records[0].VaccineCode != "08" {
		t.Errorf("record[0] VaccineCode: want 08, got %s", records[0].VaccineCode)
	}
	if records[0].VaccineName != "Hepatitis B" {
		t.Errorf("record[0] VaccineName: want Hepatitis B, got %s", records[0].VaccineName)
	}
	if records[0].LotNumber != "LOT-A" {
		t.Errorf("record[0] LotNumber: want LOT-A, got %s", records[0].LotNumber)
	}
	if records[0].Manufacturer != "MFG-1" {
		t.Errorf("record[0] Manufacturer: want MFG-1, got %s", records[0].Manufacturer)
	}
	if records[1].VaccineCode != "03" {
		t.Errorf("record[1] VaccineCode: want 03, got %s", records[1].VaccineCode)
	}
}

func TestParseVXR_Empty(t *testing.T) {
	_, err := ParseVXR([]byte(""))
	if err == nil {
		t.Fatal("expected error for empty input")
	}
}

func TestParseVXR_InvalidMessage(t *testing.T) {
	_, err := ParseVXR([]byte("not an hl7 message"))
	if err == nil {
		t.Fatal("expected error for invalid message")
	}
}

// ---------------------------------------------------------------------------
// Submission log
// ---------------------------------------------------------------------------

func TestSubmissionLog_Record(t *testing.T) {
	log := NewSubmissionLog()
	id := log.Record("elr", "STATE_PH_LAB", "sent", "")
	if id == "" {
		t.Fatal("Record should return an ID")
	}
	if !strings.HasPrefix(id, "SUB-") {
		t.Errorf("ID should start with SUB-, got %s", id)
	}
}

func TestSubmissionLog_QueryByType(t *testing.T) {
	log := NewSubmissionLog()
	log.Record("elr", "STATE_PH_LAB", "sent", "")
	log.Record("ecr", "CDC", "accepted", "")
	log.Record("elr", "COUNTY_LAB", "rejected", "invalid format")
	log.Record("vxu", "IIS_REGISTRY", "sent", "")

	elrSubs := log.Query("elr")
	if len(elrSubs) != 2 {
		t.Fatalf("expected 2 ELR submissions, got %d", len(elrSubs))
	}

	ecrSubs := log.Query("ecr")
	if len(ecrSubs) != 1 {
		t.Fatalf("expected 1 eCR submission, got %d", len(ecrSubs))
	}
	if ecrSubs[0].Status != "accepted" {
		t.Errorf("eCR status: want accepted, got %s", ecrSubs[0].Status)
	}

	allSubs := log.Query("")
	if len(allSubs) != 4 {
		t.Fatalf("expected 4 total submissions, got %d", len(allSubs))
	}
}

func TestSubmissionLog_ErrorDetail(t *testing.T) {
	log := NewSubmissionLog()
	log.Record("elr", "LAB", "rejected", "missing PID segment")
	subs := log.Query("elr")
	if len(subs) != 1 {
		t.Fatal("expected 1 submission")
	}
	if subs[0].ErrorDetail != "missing PID segment" {
		t.Errorf("error detail: want 'missing PID segment', got %q", subs[0].ErrorDetail)
	}
}

func TestSubmissionLog_Concurrent(t *testing.T) {
	log := NewSubmissionLog()
	done := make(chan struct{})
	for i := 0; i < 10; i++ {
		go func() {
			log.Record("elr", "LAB", "sent", "")
			log.Query("")
			done <- struct{}{}
		}()
	}
	for i := 0; i < 10; i++ {
		<-done
	}
	all := log.Query("")
	if len(all) != 10 {
		t.Fatalf("expected 10 entries, got %d", len(all))
	}
}

func TestSubmissionLog_Timestamp(t *testing.T) {
	log := NewSubmissionLog()
	before := time.Now()
	log.Record("vxu", "IIS", "sent", "")
	after := time.Now()

	subs := log.Query("vxu")
	if len(subs) != 1 {
		t.Fatal("expected 1 submission")
	}
	if subs[0].Timestamp.Before(before) || subs[0].Timestamp.After(after) {
		t.Error("timestamp should be between before and after")
	}
}

// ---------------------------------------------------------------------------
// Condition list coverage — verify we have the claimed ~20 conditions
// ---------------------------------------------------------------------------

func TestReportableConditions_Count(t *testing.T) {
	// Each condition has at least one entry. Count unique condition names.
	seen := map[string]bool{}
	for _, entry := range ReportableConditions {
		seen[entry.Condition] = true
	}
	if len(seen) < 18 {
		t.Errorf("expected at least 18 distinct reportable conditions, got %d", len(seen))
	}
}

// ---------------------------------------------------------------------------
// DEFECT PROVING TESTS — these tests should FAIL against defective code and
// PASS after fixes.
// ---------------------------------------------------------------------------

// DEFECT 1: HL7 v2 field values are not escaped.
// A patient name containing | (pipe) will shift all subsequent fields.
// HL7 escaping: | -> \F\, ~ -> \R\, \ -> \E\, & -> \T\
// Note: ^ is the component separator and is intentionally preserved in
// composite fields like names (LAST^FIRST). Only |, ~, \, & are data-breaking.
func TestBuildORU_EscapesSpecialCharsInPatientName(t *testing.T) {
	elr := validELR()
	elr.PatientName = "O'BRIEN|JONES^MARY~ANN"
	data, err := BuildORU(elr)
	if err != nil {
		t.Fatalf("BuildORU failed: %v", err)
	}
	msg := string(data)
	// The pipe in the name must be escaped as \F\
	if strings.Contains(msg, "O'BRIEN|JONES") {
		t.Error("DEFECT: pipe in patient name is NOT escaped — shifts all subsequent fields")
	}
	// The ~ must be escaped as \R\
	if strings.Contains(msg, "MARY~ANN") {
		t.Error("DEFECT: tilde in patient name is NOT escaped")
	}
	// Check correct escaping is present
	if !strings.Contains(msg, `O'BRIEN\F\JONES`) {
		t.Error("expected pipe escaped as \\F\\ in patient name")
	}
	if !strings.Contains(msg, `MARY\R\ANN`) {
		t.Error("expected tilde escaped as \\R\\ in patient name")
	}
}

func TestBuildORU_EscapesBackslash(t *testing.T) {
	elr := validELR()
	elr.PatientName = `SMITH\JONES^JOHN`
	data, err := BuildORU(elr)
	if err != nil {
		t.Fatalf("BuildORU failed: %v", err)
	}
	msg := string(data)
	// Backslash must be escaped as \E\
	// The raw name "SMITH\JONES^JOHN" should appear as "SMITH\E\JONES^JOHN" in the message
	if !strings.Contains(msg, `SMITH\E\JONES^JOHN`) {
		t.Error("DEFECT: backslash in patient name is NOT escaped as \\E\\")
	}
}

func TestBuildVXU_EscapesSpecialChars(t *testing.T) {
	rec := ImmunizationRecord{
		PatientID:   "PAT-001",
		VaccineCode: "08",
		VaccineName: "Hep B",
		AdminDate:   "20260815",
		Provider:    "Dr. Smith & Jones",
	}
	data, err := BuildVXU(rec)
	if err != nil {
		t.Fatalf("BuildVXU failed: %v", err)
	}
	msg := string(data)
	// Ampersand in provider name must be escaped as \T\
	if strings.Contains(msg, "Smith & Jones") {
		t.Error("DEFECT: ampersand in provider name is NOT escaped as \\T\\")
	}
}

// DEFECT 2: VXU is missing RXA-20 (completion status) and RXA-21 (action code).
// A VXU without RXA-21 may be treated as ADD when meant as DELETE/UPDATE.
func TestBuildVXU_HasCompletionStatusAndActionCode(t *testing.T) {
	rec := ImmunizationRecord{
		PatientID:   "PAT-001",
		VaccineCode: "08",
		VaccineName: "Hepatitis B",
		AdminDate:   "20260815",
	}
	data, err := BuildVXU(rec)
	if err != nil {
		t.Fatalf("BuildVXU failed: %v", err)
	}
	msg := string(data)
	// Find the RXA segment
	segments := strings.Split(msg, "\r")
	var rxaSegment string
	for _, seg := range segments {
		if strings.HasPrefix(seg, "RXA|") {
			rxaSegment = seg
			break
		}
	}
	if rxaSegment == "" {
		t.Fatal("no RXA segment found")
	}
	fields := strings.Split(rxaSegment, "|")
	// RXA-20 is field index 20, RXA-21 is field index 21 (1-based field numbering,
	// but field[0]="RXA", so RXA-20 = fields[20], RXA-21 = fields[21])
	if len(fields) < 22 {
		t.Fatalf("DEFECT: RXA segment has only %d fields, need at least 22 for RXA-21 (action code)", len(fields))
	}
	// RXA-20: completion status — must be "CP" for complete
	if fields[20] == "" {
		t.Error("DEFECT: RXA-20 (completion status) is empty — must be CP for administered dose")
	}
	// RXA-21: action code — must be "A" (Add) for new vaccinations
	if fields[21] == "" {
		t.Error("DEFECT: RXA-21 (action code) is empty — without it, registry behavior is undefined")
	}
}

// DEFECT 3: No handling of refused/not-administered immunizations.
// Recording a refusal as an administered dose is the dangerous inversion.
// NOTE: This test proves the struct is missing CompletionStatus, RefusalReason,
// ActionCode fields. The compile error above (before fix) is the proof.
// After fixing, this test verifies correct behavior.
func TestBuildVXU_RefusedImmunization(t *testing.T) {
	rec := ImmunizationRecord{
		PatientID:        "PAT-001",
		VaccineCode:      "998",
		VaccineName:      "No vaccine given",
		AdminDate:        "20260815",
		CompletionStatus: "RE",
		RefusalReason:    "00",
		ActionCode:       "A",
	}
	data, err := BuildVXU(rec)
	if err != nil {
		t.Fatalf("BuildVXU failed: %v", err)
	}
	msg := string(data)
	segments := strings.Split(msg, "\r")
	var rxaSegment string
	for _, seg := range segments {
		if strings.HasPrefix(seg, "RXA|") {
			rxaSegment = seg
			break
		}
	}
	if rxaSegment == "" {
		t.Fatal("no RXA segment found")
	}
	fields := strings.Split(rxaSegment, "|")
	if len(fields) < 22 {
		t.Fatalf("RXA has only %d fields, need at least 22", len(fields))
	}
	// RXA-20 must be "RE" for refused
	if fields[20] != "RE" {
		t.Errorf("DEFECT: RXA-20 for refused immunization: want RE, got %q", fields[20])
	}
	// RXA-18 should carry the refusal reason
	if len(fields) > 18 && !strings.Contains(fields[18], "00") {
		t.Errorf("DEFECT: RXA-18 (refusal reason) not populated for refused immunization")
	}
}

// DEFECT 4: Cross-system code match - a SNOMED code that happens to match
// an ICD-10 code value should NOT trigger if the system is wrong.
func TestIsReportable_CrossSystemNoFalsePositive(t *testing.T) {
	// "A15" is reportable as ICD-10 (TB). Verify it does NOT match as SNOMED.
	if IsReportable("A15", "SNOMED") {
		t.Error("DEFECT: A15 should only be reportable under ICD-10, not SNOMED")
	}
	// "56717001" is reportable as SNOMED (TB). Verify it does NOT match as ICD-10.
	if IsReportable("56717001", "ICD-10") {
		t.Error("DEFECT: 56717001 should only be reportable under SNOMED, not ICD-10")
	}
}

// DEFECT 5: eCR Composition missing eICR profile/template identifier.
func TestBuildECR_HasEICRProfile(t *testing.T) {
	bundle, err := BuildECR(validCaseReport())
	if err != nil {
		t.Fatalf("BuildECR failed: %v", err)
	}
	entries := bundle["entry"].([]map[string]interface{})
	var composition map[string]interface{}
	for _, entry := range entries {
		res := entry["resource"].(map[string]interface{})
		if res["resourceType"] == "Composition" {
			composition = res
			break
		}
	}
	if composition == nil {
		t.Fatal("no Composition resource found")
	}
	// eICR Composition must declare its profile (meta.profile) with the eICR template
	meta, hasMeta := composition["meta"]
	if !hasMeta {
		t.Fatal("DEFECT: Composition missing meta element with eICR profile URL")
	}
	metaMap, ok := meta.(map[string]interface{})
	if !ok {
		t.Fatal("meta is not a map")
	}
	profiles, hasProfile := metaMap["profile"]
	if !hasProfile {
		t.Fatal("DEFECT: Composition meta missing profile array")
	}
	profileList, ok := profiles.([]string)
	if !ok {
		t.Fatal("profiles is not []string")
	}
	found := false
	for _, p := range profileList {
		if strings.Contains(p, "eicr") || strings.Contains(p, "ecr") || strings.Contains(p, "2.16.840.1.113883.10.20.15") {
			found = true
			break
		}
	}
	if !found {
		t.Error("DEFECT: Composition does not declare eICR profile")
	}
}

// DEFECT 6: MSH-10 control ID not unique — same-second messages collide.
func TestBuildORU_ControlIDUniqueness(t *testing.T) {
	elr := validELR()
	data1, err := BuildORU(elr)
	if err != nil {
		t.Fatal(err)
	}
	data2, err := BuildORU(elr)
	if err != nil {
		t.Fatal(err)
	}
	// Extract MSH-10 from each
	getControlID := func(msg string) string {
		segments := strings.Split(msg, "\r")
		for _, seg := range segments {
			if strings.HasPrefix(seg, "MSH|") {
				fields := strings.Split(seg, "|")
				if len(fields) > 9 {
					return fields[9]
				}
			}
		}
		return ""
	}
	id1 := getControlID(string(data1))
	id2 := getControlID(string(data2))
	if id1 == "" || id2 == "" {
		t.Fatal("could not extract control IDs")
	}
	if id1 == id2 {
		t.Errorf("DEFECT: two messages generated in same second have identical MSH-10 control ID: %s", id1)
	}
}

// DEFECT 7: OBX result date vs collection date — the code uses collDate for
// OBX-14 (date/time of observation) which is the collection date. But OBX-14
// should be the result/analysis date, which may differ from collection date.
// This test verifies the field structure is at least present.
func TestBuildORU_OBXResultDateFieldPresent(t *testing.T) {
	elr := validELR()
	data, err := BuildORU(elr)
	if err != nil {
		t.Fatal(err)
	}
	msg := string(data)
	segments := strings.Split(msg, "\r")
	for _, seg := range segments {
		if strings.HasPrefix(seg, "OBX|") {
			fields := strings.Split(seg, "|")
			// OBX has: 0=OBX, 1=SetID, 2=ValueType, 3=ObsID, 4=SubID, 5=Value,
			//          6=Units, 7=RefRange, 8=AbnFlags, 9=Prob, 10=Nature, 11=Status,
			//          12=EffDateLastNormal, 13=UserDefinedAccess, 14=DateTimeObs
			// Actually per HL7 2.5.1 OBX: field 14 is date/time of observation
			if len(fields) < 14 {
				t.Errorf("OBX segment has only %d fields, need at least 14 for date/time of observation", len(fields))
			}
		}
	}
}

// Verify segment terminators are \r (CR), not \n (LF).
func TestBuildORU_SegmentTerminatorsAreCR(t *testing.T) {
	data, err := BuildORU(validELR())
	if err != nil {
		t.Fatal(err)
	}
	msg := string(data)
	if strings.Contains(msg, "\n") {
		t.Error("DEFECT: message contains LF (\\n) — HL7 v2 requires CR (\\r) as segment terminator")
	}
	if !strings.Contains(msg, "\r") {
		t.Error("message does not contain CR segment terminators")
	}
}

func TestBuildVXU_SegmentTerminatorsAreCR(t *testing.T) {
	rec := ImmunizationRecord{
		PatientID:   "PAT-001",
		VaccineCode: "08",
		VaccineName: "Hep B",
		AdminDate:   "20260815",
	}
	data, err := BuildVXU(rec)
	if err != nil {
		t.Fatal(err)
	}
	msg := string(data)
	if strings.Contains(msg, "\n") {
		t.Error("DEFECT: VXU message contains LF — HL7 v2 requires CR")
	}
}

// Verify OBX-11 (result status) uses valid HL7 Table 0085 values.
func TestBuildORU_OBX11ValidStatus(t *testing.T) {
	// Valid OBX-11 values: C, D, F, I, N, O, P, R, S, U, W, X
	validStatuses := map[string]bool{
		"C": true, "D": true, "F": true, "I": true,
		"N": true, "O": true, "P": true, "R": true,
		"S": true, "U": true, "W": true, "X": true,
	}
	elr := validELR()
	elr.Results[0].Status = "F"
	data, err := BuildORU(elr)
	if err != nil {
		t.Fatal(err)
	}
	msg := string(data)
	segments := strings.Split(msg, "\r")
	for _, seg := range segments {
		if strings.HasPrefix(seg, "OBX|") {
			fields := strings.Split(seg, "|")
			if len(fields) > 11 {
				status := fields[11]
				if status != "" && !validStatuses[status] {
					t.Errorf("OBX-11 has invalid result status %q (not in HL7 Table 0085)", status)
				}
			}
		}
	}
}

// Verify OBX-8 (abnormal flags) uses valid HL7 Table 0078 values.
func TestBuildORU_OBX8ValidAbnormalFlags(t *testing.T) {
	// Valid OBX-8 values from Table 0078
	validFlags := map[string]bool{
		"L": true, "H": true, "LL": true, "HH": true,
		"<": true, ">": true, "N": true, "A": true,
		"AA": true, "U": true, "D": true, "B": true,
		"W": true, "S": true, "R": true, "I": true,
		"MS": true, "VS": true, "": true,
	}
	elr := validELR()
	elr.Results[0].AbnormalFlag = "A"
	data, err := BuildORU(elr)
	if err != nil {
		t.Fatal(err)
	}
	msg := string(data)
	segments := strings.Split(msg, "\r")
	for _, seg := range segments {
		if strings.HasPrefix(seg, "OBX|") {
			fields := strings.Split(seg, "|")
			if len(fields) > 8 {
				flag := fields[8]
				if !validFlags[flag] {
					t.Errorf("OBX-8 has invalid abnormal flag %q (not in HL7 Table 0078)", flag)
				}
			}
		}
	}
}

// Verify performing lab / sending facility is populated in the message.
func TestBuildORU_PerformingLabPopulated(t *testing.T) {
	elr := validELR()
	data, err := BuildORU(elr)
	if err != nil {
		t.Fatal(err)
	}
	msg := string(data)
	if !strings.Contains(msg, "STATE_PH_LAB") {
		t.Error("performing lab not found in message")
	}
}
