package fhir

// US Core resource types.
//
// These six are what US Core requires beyond what this server already held, and US Core is the actual bar for saying "FHIR" to an
// American hospital. Without Condition and MedicationRequest in particular, a clinical app has no problem list and no medication list,
// which is most of what a clinician looks at.
//
// Deliberately not the complete R5 definition of each. The fields here are the ones US Core marks as must-support plus the references and
// codes that make a resource findable, and adding a field nobody populates costs a schema somebody has to read. Unknown JSON fields are
// preserved by the base type, so a sender's extra content survives a round trip rather than being silently dropped - which is what makes
// a partial definition safe rather than lossy.

// Condition is a problem, diagnosis or concern.
//
// The problem list. Also the resource whose status fields cause the most confusion in practice: clinicalStatus says whether the patient
// still has the condition and verificationStatus says whether anyone is sure of it, and they are routinely conflated. Both are carried
// because a resolved condition and a refuted one are different clinical statements.
type Condition struct {
	base

	Identifier         []Identifier      `json:"identifier,omitempty"`
	ClinicalStatus     *CodeableConcept  `json:"clinicalStatus,omitempty"`
	VerificationStatus *CodeableConcept  `json:"verificationStatus,omitempty"`
	Category           []CodeableConcept `json:"category,omitempty"`
	Severity           *CodeableConcept  `json:"severity,omitempty"`
	Code               *CodeableConcept  `json:"code,omitempty"`
	BodySite           []CodeableConcept `json:"bodySite,omitempty"`
	Subject            *Reference        `json:"subject,omitempty"`
	Encounter          *Reference        `json:"encounter,omitempty"`

	// onset[x] and abatement[x]. Strings rather than a parsed date, matching how the rest of this package treats
	// FHIR dates: a partial date is legal and a parsed one would have to invent a day.
	OnsetDateTime     string  `json:"onsetDateTime,omitempty"`
	OnsetPeriod       *Period `json:"onsetPeriod,omitempty"`
	OnsetString       string  `json:"onsetString,omitempty"`
	AbatementDateTime string  `json:"abatementDateTime,omitempty"`
	AbatementString   string  `json:"abatementString,omitempty"`

	RecordedDate string       `json:"recordedDate,omitempty"`
	Note         []Annotation `json:"note,omitempty"`
}

// ResourceTypeName implements Resource.
func (c *Condition) ResourceTypeName() string { return "Condition" }

// ResourceID implements Resource.
func (c *Condition) ResourceID() string { return c.ID }

// SetResourceID implements Resource.
func (c *Condition) SetResourceID(id string) { c.ID = id; c.ResourceType = "Condition" }

// MedicationRequest is an order or prescription for a medication.
//
// The medication list. medication[x] is carried in both forms because both are common and they are not interchangeable: a
// CodeableConcept names a drug from a code system, and a Reference points at a Medication resource with its own strength and form.
type MedicationRequest struct {
	base

	Identifier []Identifier      `json:"identifier,omitempty"`
	Status     string            `json:"status,omitempty"`
	Intent     string            `json:"intent,omitempty"`
	Category   []CodeableConcept `json:"category,omitempty"`

	// medication[x].
	MedicationCodeableConcept *CodeableConcept `json:"medicationCodeableConcept,omitempty"`
	MedicationReference       *Reference       `json:"medicationReference,omitempty"`

	Subject           *Reference        `json:"subject,omitempty"`
	Encounter         *Reference        `json:"encounter,omitempty"`
	AuthoredOn        string            `json:"authoredOn,omitempty"`
	Requester         *Reference        `json:"requester,omitempty"`
	ReasonCode        []CodeableConcept `json:"reasonCode,omitempty"`
	DosageInstruction []Dosage          `json:"dosageInstruction,omitempty"`
	Note              []Annotation      `json:"note,omitempty"`

	// DoNotPerform means this is an instruction not to give the medication.
	//
	// A pointer rather than a bool, because false and absent differ here in a way that matters: absent means nobody
	// said, and false is a positive statement that the medication should be given. Flattening them would turn an
	// unstated field into an instruction.
	DoNotPerform *bool `json:"doNotPerform,omitempty"`
}

// ResourceTypeName implements Resource.
func (m *MedicationRequest) ResourceTypeName() string { return "MedicationRequest" }

// ResourceID implements Resource.
func (m *MedicationRequest) ResourceID() string { return m.ID }

// SetResourceID implements Resource.
func (m *MedicationRequest) SetResourceID(id string) { m.ID = id; m.ResourceType = "MedicationRequest" }

// AllergyIntolerance is a propensity for an adverse reaction to a substance.
//
// The allergy list, and the resource where an absent value is most dangerous. "No known allergies" is a positive clinical statement and
// must be recorded as a Condition-like code, not as an empty list - an empty list means nobody asked. Nothing here can enforce that, but
// it is why code is carried even when reaction is present.
type AllergyIntolerance struct {
	base

	Identifier         []Identifier      `json:"identifier,omitempty"`
	ClinicalStatus     *CodeableConcept  `json:"clinicalStatus,omitempty"`
	VerificationStatus *CodeableConcept  `json:"verificationStatus,omitempty"`
	Type               string            `json:"type,omitempty"`
	Category           []string          `json:"category,omitempty"`
	Criticality        string            `json:"criticality,omitempty"`
	Code               *CodeableConcept  `json:"code,omitempty"`
	Patient            *Reference        `json:"patient,omitempty"`
	Encounter          *Reference        `json:"encounter,omitempty"`
	OnsetDateTime      string            `json:"onsetDateTime,omitempty"`
	RecordedDate       string            `json:"recordedDate,omitempty"`
	Reaction           []AllergyReaction `json:"reaction,omitempty"`
	Note               []Annotation      `json:"note,omitempty"`
}

// ResourceTypeName implements Resource.
func (a *AllergyIntolerance) ResourceTypeName() string { return "AllergyIntolerance" }

// ResourceID implements Resource.
func (a *AllergyIntolerance) ResourceID() string { return a.ID }

// SetResourceID implements Resource.
func (a *AllergyIntolerance) SetResourceID(id string) {
	a.ID = id
	a.ResourceType = "AllergyIntolerance"
}

// AllergyReaction is one recorded adverse reaction event.
type AllergyReaction struct {
	Substance     *CodeableConcept  `json:"substance,omitempty"`
	Manifestation []CodeableConcept `json:"manifestation,omitempty"`
	Description   string            `json:"description,omitempty"`
	Onset         string            `json:"onset,omitempty"`
	Severity      string            `json:"severity,omitempty"`
	ExposureRoute *CodeableConcept  `json:"exposureRoute,omitempty"`
	Note          []Annotation      `json:"note,omitempty"`
}

// Immunization is a vaccination that was administered or was not.
//
// status matters more here than elsewhere: "not-done" is a record that a vaccine was deliberately not given, which is a different fact
// from no record at all, and a system that filters it out loses the refusal.
type Immunization struct {
	base

	Identifier         []Identifier     `json:"identifier,omitempty"`
	Status             string           `json:"status,omitempty"`
	StatusReason       *CodeableConcept `json:"statusReason,omitempty"`
	VaccineCode        *CodeableConcept `json:"vaccineCode,omitempty"`
	Patient            *Reference       `json:"patient,omitempty"`
	Encounter          *Reference       `json:"encounter,omitempty"`
	OccurrenceDateTime string           `json:"occurrenceDateTime,omitempty"`
	OccurrenceString   string           `json:"occurrenceString,omitempty"`
	PrimarySource      *bool            `json:"primarySource,omitempty"`
	Location           *Reference       `json:"location,omitempty"`
	LotNumber          string           `json:"lotNumber,omitempty"`
	Site               *CodeableConcept `json:"site,omitempty"`
	Route              *CodeableConcept `json:"route,omitempty"`
	DoseQuantity       *Quantity        `json:"doseQuantity,omitempty"`
	Note               []Annotation     `json:"note,omitempty"`
}

// ResourceTypeName implements Resource.
func (i *Immunization) ResourceTypeName() string { return "Immunization" }

// ResourceID implements Resource.
func (i *Immunization) ResourceID() string { return i.ID }

// SetResourceID implements Resource.
func (i *Immunization) SetResourceID(id string) { i.ID = id; i.ResourceType = "Immunization" }

// Procedure is an action performed on a patient.
type Procedure struct {
	base

	Identifier []Identifier      `json:"identifier,omitempty"`
	Status     string            `json:"status,omitempty"`
	Category   []CodeableConcept `json:"category,omitempty"`
	Code       *CodeableConcept  `json:"code,omitempty"`
	Subject    *Reference        `json:"subject,omitempty"`
	Encounter  *Reference        `json:"encounter,omitempty"`

	// performed[x].
	PerformedDateTime string  `json:"performedDateTime,omitempty"`
	PerformedPeriod   *Period `json:"performedPeriod,omitempty"`
	PerformedString   string  `json:"performedString,omitempty"`

	ReasonCode []CodeableConcept `json:"reasonCode,omitempty"`
	BodySite   []CodeableConcept `json:"bodySite,omitempty"`
	Outcome    *CodeableConcept  `json:"outcome,omitempty"`
	Note       []Annotation      `json:"note,omitempty"`
}

// ResourceTypeName implements Resource.
func (p *Procedure) ResourceTypeName() string { return "Procedure" }

// ResourceID implements Resource.
func (p *Procedure) ResourceID() string { return p.ID }

// SetResourceID implements Resource.
func (p *Procedure) SetResourceID(id string) { p.ID = id; p.ResourceType = "Procedure" }

// DocumentReference points at a document, most often a C-CDA or a PDF.
//
// This is where the v3 work meets the FHIR work: a C-CDA produced by the CDA destination is exactly what a DocumentReference points at,
// and it is how a discharge summary is exchanged in the United States.
type DocumentReference struct {
	base

	Identifier  []Identifier      `json:"identifier,omitempty"`
	Status      string            `json:"status,omitempty"`
	DocStatus   string            `json:"docStatus,omitempty"`
	Type        *CodeableConcept  `json:"type,omitempty"`
	Category    []CodeableConcept `json:"category,omitempty"`
	Subject     *Reference        `json:"subject,omitempty"`
	Date        string            `json:"date,omitempty"`
	Author      []Reference       `json:"author,omitempty"`
	Description string            `json:"description,omitempty"`
	Content     []DocumentContent `json:"content,omitempty"`
	Context     *DocumentContext  `json:"context,omitempty"`
}

// ResourceTypeName implements Resource.
func (d *DocumentReference) ResourceTypeName() string { return "DocumentReference" }

// ResourceID implements Resource.
func (d *DocumentReference) ResourceID() string { return d.ID }

// SetResourceID implements Resource.
func (d *DocumentReference) SetResourceID(id string) { d.ID = id; d.ResourceType = "DocumentReference" }

// DocumentContent is one representation of the document.
type DocumentContent struct {
	Attachment *Attachment       `json:"attachment,omitempty"`
	Format     *Coding           `json:"format,omitempty"`
	Profile    []DocumentProfile `json:"profile,omitempty"`
}

// DocumentProfile names the profile a content representation follows.
type DocumentProfile struct {
	ValueCoding *Coding `json:"valueCoding,omitempty"`
	ValueURI    string  `json:"valueUri,omitempty"`
}

// DocumentContext is the clinical context the document was produced in.
type DocumentContext struct {
	Encounter []Reference       `json:"encounter,omitempty"`
	Event     []CodeableConcept `json:"event,omitempty"`
	Period    *Period           `json:"period,omitempty"`
}

// Dosage is how a medication is to be taken.
//
// Text is carried alongside the structured fields and is the field that matters most in practice: every real prescription has a
// human-readable sig, and a receiver that renders only the structured form will show a blank line for the many senders who populate the
// text and nothing else.
type Dosage struct {
	Sequence              *int              `json:"sequence,omitempty"`
	Text                  string            `json:"text,omitempty"`
	PatientInstruction    string            `json:"patientInstruction,omitempty"`
	Timing                *Timing           `json:"timing,omitempty"`
	Route                 *CodeableConcept  `json:"route,omitempty"`
	Method                *CodeableConcept  `json:"method,omitempty"`
	DoseAndRate           []DoseAndRate     `json:"doseAndRate,omitempty"`
	AsNeeded              *bool             `json:"asNeeded,omitempty"`
	AsNeededFor           []CodeableConcept `json:"asNeededFor,omitempty"`
	AdditionalInstruction []CodeableConcept `json:"additionalInstruction,omitempty"`
}

// DoseAndRate is the amount and how fast.
type DoseAndRate struct {
	Type         *CodeableConcept `json:"type,omitempty"`
	DoseQuantity *Quantity        `json:"doseQuantity,omitempty"`
	DoseRange    *Range           `json:"doseRange,omitempty"`
	RateQuantity *Quantity        `json:"rateQuantity,omitempty"`
	RateRatio    *Ratio           `json:"rateRatio,omitempty"`
}

// Timing is when and how often.
//
// Only the parts a prescription uses. The full FHIR Timing carries an event list and a bounds duration as well, and neither appears in a
// medication order often enough to justify the reading.
type Timing struct {
	Event  []string         `json:"event,omitempty"`
	Repeat *TimingRepeat    `json:"repeat,omitempty"`
	Code   *CodeableConcept `json:"code,omitempty"`
}

// TimingRepeat is the recurrence rule.
type TimingRepeat struct {
	Frequency    *int     `json:"frequency,omitempty"`
	Period       *float64 `json:"period,omitempty"`
	PeriodUnit   string   `json:"periodUnit,omitempty"`
	DayOfWeek    []string `json:"dayOfWeek,omitempty"`
	TimeOfDay    []string `json:"timeOfDay,omitempty"`
	When         []string `json:"when,omitempty"`
	Duration     *float64 `json:"duration,omitempty"`
	DurationUnit string   `json:"durationUnit,omitempty"`
	BoundsPeriod *Period  `json:"boundsPeriod,omitempty"`
}
