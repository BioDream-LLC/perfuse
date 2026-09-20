package fhir

import (
	"fmt"
	"regexp"
	"strings"
)

// Validation.
//
// This checks structure and the constraints that stop a resource being accepted:
// required elements, bound value sets, identifier and reference shape, and the
// US Core "must support" elements where a profile is claimed.
//
// What it deliberately does not do is claim full profile validation. Real US Core
// conformance needs the published implementation guide, terminology expansion and
// a validator that understands slicing. Pretending otherwise would be the same
// mistake as an engine that answers AA before it has stored anything: a claim
// that is convenient and untrue. Findings say which level they came from.

// Severity of a validation finding.
type Severity string

// Finding severities.
const (
	// Error means the resource is invalid and a server should reject it.
	Error Severity = "error"
	// Warning means it is valid but likely wrong, or will fail a profile.
	Warning Severity = "warning"
	// Info is an observation worth surfacing but not a defect.
	Info Severity = "info"
)

// Finding is one validation result.
type Finding struct {
	Severity Severity `json:"severity"`
	// Path is the FHIRPath-style location, such as "Patient.identifier[0].system".
	Path string `json:"path"`
	// Message says what is wrong.
	Message string `json:"message"`
	// Rule identifies which rule produced this, so a finding can be traced to a
	// requirement rather than to an opinion.
	Rule string `json:"rule"`
}

// ValidationResult collects findings.
type ValidationResult struct {
	// Findings is never nil, so it serialises as an empty JSON array rather than null.
	//
	// A nil slice in Go marshals to null, and a caller told to expect a list then has to handle two
	// spellings of "nothing wrong". The interface read this straight off the wire and crashed on a
	// clean bundle - the one case where nothing is wrong, which is the case least likely to be tried
	// by hand. Constructing it empty means the type on the wire matches the type in the contract.
	Findings []Finding `json:"findings"`
	// Profile records what was validated against.
	Profile string  `json:"profile,omitempty"`
	Version Version `json:"version"`
}

// Valid reports whether there are no errors. Warnings do not make a resource
// invalid.
func (r *ValidationResult) Valid() bool {
	for _, f := range r.Findings {
		if f.Severity == Error {
			return false
		}
	}
	return true
}

// Counts returns the number of findings at each severity.
func (r *ValidationResult) Counts() (errors, warnings, infos int) {
	for _, f := range r.Findings {
		switch f.Severity {
		case Error:
			errors++
		case Warning:
			warnings++
		default:
			infos++
		}
	}
	return
}

// OperationOutcome converts the result into the resource a FHIR server returns.
func (r *ValidationResult) OperationOutcome() *OperationOutcome {
	out := &OperationOutcome{}
	out.ResourceType = "OperationOutcome"

	for _, f := range r.Findings {
		severity := SeverityInformation
		code := "informational"
		switch f.Severity {
		case Error:
			severity = SeverityError
			code = "invariant"
		case Warning:
			severity = SeverityWarning
			code = "business-rule"
		}
		out.Issue = append(out.Issue, OperationOutcomeIssue{
			Severity:    severity,
			Code:        code,
			Diagnostics: f.Message,
			Expression:  []string{f.Path},
			Details:     TextOnly(f.Rule),
		})
	}

	if len(out.Issue) == 0 {
		out.Issue = append(out.Issue, OperationOutcomeIssue{
			Severity: SeverityInformation, Code: "informational",
			Diagnostics: "validation found no problems",
		})
	}
	return out
}

func (r *ValidationResult) add(severity Severity, path, rule, format string, args ...any) {
	r.Findings = append(r.Findings, Finding{
		Severity: severity,
		Path:     path,
		Rule:     rule,
		Message:  fmt.Sprintf(format, args...),
	})
}

// Value sets that are required bindings. A code outside these is an error, not a
// preference: a server validating the resource will reject it.
var (
	patientGenders = map[string]bool{
		"male": true, "female": true, "other": true, "unknown": true,
	}

	observationStatuses = map[string]bool{
		"registered": true, "preliminary": true, "final": true, "amended": true,
		"corrected": true, "cancelled": true, "entered-in-error": true, "unknown": true,
	}

	diagnosticReportStatuses = map[string]bool{
		"registered": true, "partial": true, "preliminary": true, "modified": true,
		"final": true, "amended": true, "corrected": true, "appended": true,
		"cancelled": true, "entered-in-error": true, "unknown": true,
	}

	encounterStatusesR5 = map[string]bool{
		"planned": true, "in-progress": true, "on-hold": true, "discharged": true,
		"completed": true, "cancelled": true, "discontinued": true,
		"entered-in-error": true, "unknown": true,
	}

	encounterStatusesR4 = map[string]bool{
		"planned": true, "arrived": true, "triaged": true, "in-progress": true,
		"onleave": true, "finished": true, "cancelled": true,
		"entered-in-error": true, "unknown": true,
	}

	specimenStatuses = map[string]bool{
		"available": true, "unavailable": true, "unsatisfactory": true,
		"entered-in-error": true,
	}

	serviceRequestStatuses = map[string]bool{
		"draft": true, "active": true, "on-hold": true, "revoked": true,
		"completed": true, "entered-in-error": true, "unknown": true,
	}

	serviceRequestIntents = map[string]bool{
		"proposal": true, "plan": true, "directive": true, "order": true,
		"original-order": true, "reflex-order": true, "filler-order": true,
		"instance-order": true, "option": true,
	}
)

// FHIR date and dateTime formats. A malformed instant is one of the most common
// reasons a v2 to FHIR conversion is rejected, because v2 timestamps are not
// ISO 8601 and have to be converted rather than copied.
var (
	dateRe = regexp.MustCompile(`^\d{4}(-\d{2}(-\d{2})?)?$`)
	// dateTime requires a timezone once a time is present, which is the rule
	// people most often miss.
	dateTimeRe = regexp.MustCompile(
		`^\d{4}(-\d{2}(-\d{2}(T\d{2}:\d{2}(:\d{2}(\.\d+)?)?(Z|[+-]\d{2}:\d{2}))?)?)?$`)
	instantRe = regexp.MustCompile(
		`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$`)
	idRe = regexp.MustCompile(`^[A-Za-z0-9\-.]{1,64}$`)
)

// Validate checks a resource for the given version.
func Validate(r Resource, version Version) *ValidationResult {
	res := &ValidationResult{Version: version, Findings: []Finding{}}
	if !version.Valid() {
		res.add(Error, r.ResourceTypeName(), "version",
			"%q is not a supported FHIR version", version)
		return res
	}

	if id := r.ResourceID(); id != "" && !idRe.MatchString(id) {
		res.add(Error, r.ResourceTypeName()+".id", "id-shape",
			"%q is not a valid resource id: allowed are letters, digits, hyphen and dot, up to 64 characters", id)
	}

	// A choice element with two values set is invalid, and the marshaller refuses
	// it. Surfacing it here means the problem is reported rather than discovered
	// as a serialisation failure.
	if _, err := Marshal(r, version); err != nil {
		res.add(Error, r.ResourceTypeName(), "choice-element", "%s", err.Error())
	}

	switch v := r.(type) {
	case *Patient:
		validatePatient(v, version, res)
	case *Encounter:
		validateEncounter(v, version, res)
	case *Observation:
		validateObservation(v, version, res)
	case *DiagnosticReport:
		validateDiagnosticReport(v, version, res)
	case *Specimen:
		validateSpecimen(v, res)
	case *ServiceRequest:
		validateServiceRequest(v, res)
	case *Practitioner, *Organization, *Location, *OperationOutcome:
		// Nothing beyond the shared checks above is required for these.
	case *Bundle:
		validateBundle(v, version, res)
	}

	// Anything the resource claims about itself is checked last.
	//
	// The base rules above say whether this is a well-formed Patient. This says whether it is the Patient it says it is. They are
	// different questions, and only the second one is a promise made to somebody else: a consumer reads meta.profile and decides it
	// may rely on the elements that profile guarantees. A claim nobody verified is worse than no claim, because without it the
	// consumer writes defensive code and with it the consumer does not.
	//
	// Bundles are skipped: each entry is validated on its own, and a bundle claiming a resource profile is a different mistake.
	if _, isBundle := r.(*Bundle); !isBundle {
		ValidateProfiles(r, res)
	}

	return res
}

func validatePatient(p *Patient, version Version, res *ValidationResult) {
	if p.Gender != "" && !patientGenders[p.Gender] {
		res.add(Error, "Patient.gender", "required-binding",
			"%q is not in the administrative gender value set (male, female, other, unknown)", p.Gender)
	}
	checkDate(res, "Patient.birthDate", p.BirthDate)

	// US Core requires an identifier with both a system and a value, and at least
	// one name. Without them a receiving system cannot match the patient to
	// anyone, which defeats the purpose of sending it.
	if len(p.Identifier) == 0 {
		res.add(Warning, "Patient.identifier", "us-core",
			"US Core requires at least one identifier; without one this patient cannot be matched")
	}
	for i, id := range p.Identifier {
		path := fmt.Sprintf("Patient.identifier[%d]", i)
		if id.Value == "" {
			res.add(Error, path+".value", "us-core", "an identifier with no value carries no information")
		}
		if id.System == "" {
			res.add(Warning, path+".system", "us-core",
				"US Core requires a system; an identifier value without a namespace is ambiguous between facilities")
		}
	}

	if len(p.Name) == 0 {
		res.add(Warning, "Patient.name", "us-core", "US Core requires at least one name")
	}
	for i, n := range p.Name {
		if n.Family == "" && len(n.Given) == 0 && n.Text == "" {
			res.add(Warning, fmt.Sprintf("Patient.name[%d]", i), "us-core",
				"a name with no family, given or text parts is empty")
		}
	}

	_ = version
}

func validateEncounter(e *Encounter, version Version, res *ValidationResult) {
	if e.Status == "" {
		res.add(Error, "Encounter.status", "cardinality", "status is required")
	} else {
		// Both value sets are accepted, and which are named in the error depends on what is being served.
		//
		// The struct holds R5 codes and UnmarshalResource now translates R4 spellings on the way in, so by the time
		// validation runs an inbound "finished" has already become "completed". The R4 set is still consulted because
		// validation is also called directly, on resources built in code rather than parsed - and a caller that sets
		// Status to an R4 code has made a fidelity mistake, not a validity one.
		//
		// This branch previously assigned the R5 set in both cases and silenced the unused R4 set with `_ =`. The result
		// was that a server declaring R4 rejected legal R4 data, which is the opposite of the problem the R4 default was
		// meant to solve. Rejecting valid input is worse than emitting an unexpected shape, because a client can work
		// around the second and not the first.
		setName := "R5"
		if version.IsR4Family() {
			setName = "R4 or R5"
		}
		if !encounterStatusesR5[e.Status] && !encounterStatusesR4[e.Status] {
			res.add(Error, "Encounter.status", "required-binding",
				"%q is not in the %s encounter status value set", e.Status, setName)
		}
	}

	if e.Subject == nil {
		res.add(Warning, "Encounter.subject", "us-core",
			"an encounter with no subject cannot be attached to a patient")
	}
	if len(e.Class) == 0 {
		res.add(Warning, "Encounter.class", "us-core",
			"US Core requires class; without it a receiver cannot tell an inpatient stay from an outpatient visit")
	}
	if e.ActualPeriod != nil {
		checkDateTime(res, "Encounter.period.start", e.ActualPeriod.Start)
		checkDateTime(res, "Encounter.period.end", e.ActualPeriod.End)
		if e.ActualPeriod.Start != "" && e.ActualPeriod.End != "" &&
			e.ActualPeriod.End < e.ActualPeriod.Start {
			res.add(Error, "Encounter.period", "invariant",
				"the encounter ends before it starts")
		}
	}
}

func validateObservation(o *Observation, version Version, res *ValidationResult) {
	if o.Status == "" {
		res.add(Error, "Observation.status", "cardinality", "status is required")
	} else if !observationStatuses[o.Status] {
		res.add(Error, "Observation.status", "required-binding",
			"%q is not in the observation status value set", o.Status)
	}

	if o.Code == nil || (len(o.Code.Coding) == 0 && o.Code.Text == "") {
		res.add(Error, "Observation.code", "cardinality",
			"code is required: a result with no identity cannot be interpreted")
	}
	if o.Subject == nil {
		res.add(Warning, "Observation.subject", "us-core",
			"an observation with no subject cannot be attached to a patient")
	}

	// A result must carry either a value or an explicit reason it has none.
	// Silently omitting both loses the difference between "not measured" and
	// "measured as nothing".
	hasValue := o.ValueQuantity != nil || o.ValueCodeableConcept != nil ||
		o.ValueString != nil || o.ValueBoolean != nil || o.ValueInteger != nil ||
		o.ValueRange != nil || o.ValueRatio != nil || o.ValueDateTime != "" ||
		o.ValuePeriod != nil
	if !hasValue && o.DataAbsentReason == nil && len(o.Component) == 0 {
		res.add(Warning, "Observation.value[x]", "invariant",
			"no value and no dataAbsentReason: a reader cannot tell whether this was not measured or measured as absent")
	}
	if hasValue && o.DataAbsentReason != nil {
		res.add(Error, "Observation.dataAbsentReason", "invariant",
			"a value and a dataAbsentReason are mutually exclusive")
	}

	// A quantity with a unit but no UCUM code cannot be converted safely by a
	// receiver, which is the whole point of coding units.
	if o.ValueQuantity != nil {
		q := o.ValueQuantity
		if q.Unit != "" && q.Code == "" {
			res.add(Warning, "Observation.valueQuantity.code", "us-core",
				"unit %q has no UCUM code, so a receiver cannot convert or compare it safely", q.Unit)
		}
		if q.Code != "" && q.System == "" {
			res.add(Warning, "Observation.valueQuantity.system", "us-core",
				"a unit code without a system is ambiguous; use %s", SystemUCUM)
		}
		if q.Value == nil && q.Code == "" && q.Unit == "" {
			res.add(Error, "Observation.valueQuantity", "invariant", "an empty quantity carries no information")
		}
	}

	checkDateTime(res, "Observation.effectiveDateTime", o.EffectiveDateTime)
	checkInstant(res, "Observation.issued", o.Issued)

	if len(o.Category) == 0 {
		res.add(Info, "Observation.category", "us-core",
			"US Core expects a category, for example laboratory")
	}

	_ = version
}

func validateDiagnosticReport(d *DiagnosticReport, version Version, res *ValidationResult) {
	if d.Status == "" {
		res.add(Error, "DiagnosticReport.status", "cardinality", "status is required")
	} else if !diagnosticReportStatuses[d.Status] {
		res.add(Error, "DiagnosticReport.status", "required-binding",
			"%q is not in the diagnostic report status value set", d.Status)
	}
	if d.Code == nil || (len(d.Code.Coding) == 0 && d.Code.Text == "") {
		res.add(Error, "DiagnosticReport.code", "cardinality", "code is required")
	}
	if d.Subject == nil {
		res.add(Warning, "DiagnosticReport.subject", "us-core", "no subject")
	}
	if len(d.Result) == 0 && len(d.PresentedForm) == 0 && d.Conclusion == "" {
		res.add(Warning, "DiagnosticReport.result", "invariant",
			"a report with no results, conclusion or attached document is empty")
	}

	checkDateTime(res, "DiagnosticReport.effectiveDateTime", d.EffectiveDateTime)
	checkInstant(res, "DiagnosticReport.issued", d.Issued)
	_ = version
}

func validateSpecimen(s *Specimen, res *ValidationResult) {
	if s.Status != "" && !specimenStatuses[s.Status] {
		res.add(Error, "Specimen.status", "required-binding",
			"%q is not in the specimen status value set", s.Status)
	}
	if s.Collection != nil {
		checkDateTime(res, "Specimen.collection.collectedDateTime", s.Collection.CollectedDateTime)
	}
	checkInstant(res, "Specimen.receivedTime", s.ReceivedTime)
}

func validateServiceRequest(s *ServiceRequest, res *ValidationResult) {
	if s.Status == "" {
		res.add(Error, "ServiceRequest.status", "cardinality", "status is required")
	} else if !serviceRequestStatuses[s.Status] {
		res.add(Error, "ServiceRequest.status", "required-binding",
			"%q is not in the request status value set", s.Status)
	}
	if s.Intent == "" {
		res.add(Error, "ServiceRequest.intent", "cardinality", "intent is required")
	} else if !serviceRequestIntents[s.Intent] {
		res.add(Error, "ServiceRequest.intent", "required-binding",
			"%q is not in the request intent value set", s.Intent)
	}
	if s.Subject == nil {
		res.add(Error, "ServiceRequest.subject", "cardinality", "subject is required")
	}
	checkDateTime(res, "ServiceRequest.authoredOn", s.AuthoredOn)
}

func validateBundle(b *Bundle, version Version, res *ValidationResult) {
	if b.Type == "" {
		res.add(Error, "Bundle.type", "cardinality", "type is required")
	}

	// In a transaction every entry needs a request, or the server has no
	// instruction for what to do with the resource.
	if b.Type == BundleTransaction || b.Type == BundleBatch {
		for i, e := range b.Entry {
			path := fmt.Sprintf("Bundle.entry[%d]", i)
			if e.Request == nil {
				res.add(Error, path+".request", "invariant",
					"a %s entry needs a request telling the server what to do", b.Type)
				continue
			}
			if e.Request.Method == "" || e.Request.URL == "" {
				res.add(Error, path+".request", "invariant", "request needs both a method and a url")
			}
			if e.FullURL == "" {
				res.add(Warning, path+".fullUrl", "invariant",
					"without a fullUrl, references between entries in this bundle cannot be resolved")
			}
		}
	}

	// Validate the contained resources too. A bundle that is structurally fine but
	// full of invalid resources will be rejected as a whole.
	for i, e := range b.Entry {
		if e.Resource == nil {
			continue
		}
		inner := Validate(e.Resource, version)
		for _, f := range inner.Findings {
			f.Path = fmt.Sprintf("Bundle.entry[%d].%s", i, f.Path)
			res.Findings = append(res.Findings, f)
		}
	}
}

func checkDate(res *ValidationResult, path, value string) {
	if value == "" {
		return
	}
	if !dateRe.MatchString(value) {
		res.add(Error, path, "format",
			"%q is not a FHIR date: expected YYYY, YYYY-MM or YYYY-MM-DD", value)
	}
}

func checkDateTime(res *ValidationResult, path, value string) {
	if value == "" {
		return
	}
	if !dateTimeRe.MatchString(value) {
		hint := ""
		if strings.Contains(value, "T") && !strings.ContainsAny(value, "Z+") {
			// This is the mistake people actually make converting v2 timestamps.
			hint = "; a dateTime with a time must include a timezone offset"
		}
		res.add(Error, path, "format", "%q is not a FHIR dateTime%s", value, hint)
	}
}

func checkInstant(res *ValidationResult, path, value string) {
	if value == "" {
		return
	}
	if !instantRe.MatchString(value) {
		res.add(Error, path, "format",
			"%q is not a FHIR instant: a full date, time and timezone are all required", value)
	}
}
