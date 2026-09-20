package fhir

// Additional FHIR R4 resource types for comprehensive healthcare integration.
//
// These complete the set needed for real-world data exchange: insurance and
// billing (Coverage, Claim, ExplanationOfBenefit), care coordination (CarePlan,
// CareTeam, Goal, Task), medications beyond ordering (Medication,
// MedicationStatement, MedicationDispense, MedicationAdministration),
// scheduling (Appointment, Schedule, Slot), consent and provenance, devices,
// and clinical documentation (Composition, Communication, Media,
// QuestionnaireResponse, FamilyMemberHistory, RelatedPerson).

// ──────────────────────────────────────────────────────────────────────────────
// Medication (standalone drug definition, referenced by MedicationRequest et al)
// ──────────────────────────────────────────────────────────────────────────────

// Medication identifies a medication product.
type Medication struct {
	base

	Code         *CodeableConcept `json:"code,omitempty"`
	Status       string           `json:"status,omitempty"`
	Manufacturer *Reference       `json:"manufacturer,omitempty"`
	Form         *CodeableConcept `json:"form,omitempty"`
	Amount       *Ratio           `json:"amount,omitempty"`
	Ingredient   []MedIngredient  `json:"ingredient,omitempty"`
	Batch        *MedicationBatch `json:"batch,omitempty"`
}

// MedIngredient is one component of a compound medication.
type MedIngredient struct {
	Item     *CodeableReference `json:"item,omitempty"`
	IsActive *bool              `json:"isActive,omitempty"`
	Strength *Ratio             `json:"strength,omitempty"`
}

// MedicationBatch identifies a production batch.
type MedicationBatch struct {
	LotNumber      string `json:"lotNumber,omitempty"`
	ExpirationDate string `json:"expirationDate,omitempty"`
}

func (m *Medication) ResourceTypeName() string { return "Medication" }
func (m *Medication) ResourceID() string       { return m.ID }
func (m *Medication) SetResourceID(id string)  { m.ID = id; m.ResourceType = "Medication" }

// ──────────────────────────────────────────────────────────────────────────────
// MedicationStatement
// ──────────────────────────────────────────────────────────────────────────────

// MedicationStatement records that a patient is taking or has taken a medication.
type MedicationStatement struct {
	base

	Status            string             `json:"status,omitempty"`
	StatusReason      []CodeableConcept  `json:"statusReason,omitempty"`
	Category          *CodeableConcept   `json:"category,omitempty"`
	Medication        *CodeableReference `json:"medication,omitempty"`
	Subject           *Reference         `json:"subject,omitempty"`
	Context           *Reference         `json:"context,omitempty"`
	EffectiveStart    string             `json:"effectiveDateTime,omitempty"`
	EffectivePeriod   *Period            `json:"effectivePeriod,omitempty"`
	DateAsserted      string             `json:"dateAsserted,omitempty"`
	InformationSource *Reference         `json:"informationSource,omitempty"`
	Dosage            []Dosage           `json:"dosage,omitempty"`
	Note              []Annotation       `json:"note,omitempty"`
}

func (m *MedicationStatement) ResourceTypeName() string { return "MedicationStatement" }
func (m *MedicationStatement) ResourceID() string       { return m.ID }
func (m *MedicationStatement) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicationStatement"
}

// ──────────────────────────────────────────────────────────────────────────────
// MedicationDispense
// ──────────────────────────────────────────────────────────────────────────────

// MedicationDispense records the supply of a medication to a patient.
type MedicationDispense struct {
	base

	Status                  string             `json:"status,omitempty"`
	Medication              *CodeableReference `json:"medication,omitempty"`
	Subject                 *Reference         `json:"subject,omitempty"`
	Performer               []MedPerformer     `json:"performer,omitempty"`
	AuthorizingPrescription []Reference        `json:"authorizingPrescription,omitempty"`
	Quantity                *Quantity          `json:"quantity,omitempty"`
	DaysSupply              *Quantity          `json:"daysSupply,omitempty"`
	WhenPrepared            string             `json:"whenPrepared,omitempty"`
	WhenHandedOver          string             `json:"whenHandedOver,omitempty"`
	Dosage                  []Dosage           `json:"dosageInstruction,omitempty"`
}

// MedPerformer identifies who dispensed the medication.
type MedPerformer struct {
	Function *CodeableConcept `json:"function,omitempty"`
	Actor    *Reference       `json:"actor,omitempty"`
}

func (m *MedicationDispense) ResourceTypeName() string { return "MedicationDispense" }
func (m *MedicationDispense) ResourceID() string       { return m.ID }
func (m *MedicationDispense) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicationDispense"
}

// ──────────────────────────────────────────────────────────────────────────────
// MedicationAdministration
// ──────────────────────────────────────────────────────────────────────────────

// MedicationAdministration records a medication being given to a patient.
type MedicationAdministration struct {
	base

	Status          string             `json:"status,omitempty"`
	Category        *CodeableConcept   `json:"category,omitempty"`
	Medication      *CodeableReference `json:"medication,omitempty"`
	Subject         *Reference         `json:"subject,omitempty"`
	Context         *Reference         `json:"context,omitempty"`
	EffectiveStart  string             `json:"effectiveDateTime,omitempty"`
	EffectivePeriod *Period            `json:"effectivePeriod,omitempty"`
	Performer       []MedPerformer     `json:"performer,omitempty"`
	Request         *Reference         `json:"request,omitempty"`
	Dosage          *MedAdminDosage    `json:"dosage,omitempty"`
}

// MedAdminDosage describes how the medication was administered.
type MedAdminDosage struct {
	Text  string           `json:"text,omitempty"`
	Route *CodeableConcept `json:"route,omitempty"`
	Dose  *Quantity        `json:"dose,omitempty"`
	Rate  *Quantity        `json:"rateQuantity,omitempty"`
}

func (m *MedicationAdministration) ResourceTypeName() string { return "MedicationAdministration" }
func (m *MedicationAdministration) ResourceID() string       { return m.ID }
func (m *MedicationAdministration) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicationAdministration"
}

// ──────────────────────────────────────────────────────────────────────────────
// Coverage (insurance)
// ──────────────────────────────────────────────────────────────────────────────

// Coverage describes an insurance plan or other financial coverage.
type Coverage struct {
	base

	Status       string           `json:"status,omitempty"`
	Type         *CodeableConcept `json:"type,omitempty"`
	PolicyHolder *Reference       `json:"policyHolder,omitempty"`
	Subscriber   *Reference       `json:"subscriber,omitempty"`
	SubscriberID string           `json:"subscriberId,omitempty"`
	Beneficiary  *Reference       `json:"beneficiary,omitempty"`
	Dependent    string           `json:"dependent,omitempty"`
	Relationship *CodeableConcept `json:"relationship,omitempty"`
	Period       *Period          `json:"period,omitempty"`
	Payor        []Reference      `json:"payor,omitempty"`
	Class        []CoverageClass  `json:"class,omitempty"`
	Order        *int             `json:"order,omitempty"`
	Network      string           `json:"network,omitempty"`
}

// CoverageClass identifies a plan tier, group, or subplan.
type CoverageClass struct {
	Type  *CodeableConcept `json:"type,omitempty"`
	Value string           `json:"value,omitempty"`
	Name  string           `json:"name,omitempty"`
}

func (c *Coverage) ResourceTypeName() string { return "Coverage" }
func (c *Coverage) ResourceID() string       { return c.ID }
func (c *Coverage) SetResourceID(id string)  { c.ID = id; c.ResourceType = "Coverage" }

// ──────────────────────────────────────────────────────────────────────────────
// Claim
// ──────────────────────────────────────────────────────────────────────────────

// Claim represents a request for payment or preauthorization.
type Claim struct {
	base

	Status    string           `json:"status,omitempty"`
	Type      *CodeableConcept `json:"type,omitempty"`
	Use       string           `json:"use,omitempty"`
	Patient   *Reference       `json:"patient,omitempty"`
	Created   string           `json:"created,omitempty"`
	Insurer   *Reference       `json:"insurer,omitempty"`
	Provider  *Reference       `json:"provider,omitempty"`
	Priority  *CodeableConcept `json:"priority,omitempty"`
	Insurance []ClaimInsurance `json:"insurance,omitempty"`
	Item      []ClaimItem      `json:"item,omitempty"`
	Total     *Money           `json:"total,omitempty"`
}

// ClaimInsurance identifies the coverage applied to the claim.
type ClaimInsurance struct {
	Sequence int        `json:"sequence,omitempty"`
	Focal    bool       `json:"focal,omitempty"`
	Coverage *Reference `json:"coverage,omitempty"`
}

// ClaimItem is a service or product billed.
type ClaimItem struct {
	Sequence         int              `json:"sequence,omitempty"`
	ProductOrService *CodeableConcept `json:"productOrService,omitempty"`
	ServicedDate     string           `json:"servicedDate,omitempty"`
	Quantity         *Quantity        `json:"quantity,omitempty"`
	UnitPrice        *Money           `json:"unitPrice,omitempty"`
	Net              *Money           `json:"net,omitempty"`
}

// Money represents a monetary amount.
type Money struct {
	Value    float64 `json:"value,omitempty"`
	Currency string  `json:"currency,omitempty"`
}

func (c *Claim) ResourceTypeName() string { return "Claim" }
func (c *Claim) ResourceID() string       { return c.ID }
func (c *Claim) SetResourceID(id string)  { c.ID = id; c.ResourceType = "Claim" }

// ──────────────────────────────────────────────────────────────────────────────
// ExplanationOfBenefit
// ──────────────────────────────────────────────────────────────────────────────

// ExplanationOfBenefit describes the adjudication of a claim.
type ExplanationOfBenefit struct {
	base

	Status    string           `json:"status,omitempty"`
	Type      *CodeableConcept `json:"type,omitempty"`
	Use       string           `json:"use,omitempty"`
	Patient   *Reference       `json:"patient,omitempty"`
	Created   string           `json:"created,omitempty"`
	Insurer   *Reference       `json:"insurer,omitempty"`
	Provider  *Reference       `json:"provider,omitempty"`
	Outcome   string           `json:"outcome,omitempty"`
	Insurance []EOBInsurance   `json:"insurance,omitempty"`
	Total     []EOBTotal       `json:"total,omitempty"`
}

// EOBInsurance ties to a Coverage resource.
type EOBInsurance struct {
	Focal    bool       `json:"focal,omitempty"`
	Coverage *Reference `json:"coverage,omitempty"`
}

// EOBTotal is an adjudication total.
type EOBTotal struct {
	Category *CodeableConcept `json:"category,omitempty"`
	Amount   *Money           `json:"amount,omitempty"`
}

func (e *ExplanationOfBenefit) ResourceTypeName() string { return "ExplanationOfBenefit" }
func (e *ExplanationOfBenefit) ResourceID() string       { return e.ID }
func (e *ExplanationOfBenefit) SetResourceID(id string) {
	e.ID = id
	e.ResourceType = "ExplanationOfBenefit"
}

// ──────────────────────────────────────────────────────────────────────────────
// CarePlan
// ──────────────────────────────────────────────────────────────────────────────

// CarePlan describes intended care for a patient.
type CarePlan struct {
	base

	Status      string             `json:"status,omitempty"`
	Intent      string             `json:"intent,omitempty"`
	Category    []CodeableConcept  `json:"category,omitempty"`
	Title       string             `json:"title,omitempty"`
	Description string             `json:"description,omitempty"`
	Subject     *Reference         `json:"subject,omitempty"`
	Encounter   *Reference         `json:"encounter,omitempty"`
	Period      *Period            `json:"period,omitempty"`
	Author      *Reference         `json:"author,omitempty"`
	CareTeam    []Reference        `json:"careTeam,omitempty"`
	Goal        []Reference        `json:"goal,omitempty"`
	Activity    []CarePlanActivity `json:"activity,omitempty"`
	Note        []Annotation       `json:"note,omitempty"`
}

// CarePlanActivity is a planned action.
type CarePlanActivity struct {
	Reference *Reference      `json:"reference,omitempty"`
	Detail    *ActivityDetail `json:"detail,omitempty"`
}

// ActivityDetail describes a care plan activity inline.
type ActivityDetail struct {
	Kind   string           `json:"kind,omitempty"`
	Code   *CodeableConcept `json:"code,omitempty"`
	Status string           `json:"status,omitempty"`
}

func (c *CarePlan) ResourceTypeName() string { return "CarePlan" }
func (c *CarePlan) ResourceID() string       { return c.ID }
func (c *CarePlan) SetResourceID(id string)  { c.ID = id; c.ResourceType = "CarePlan" }

// ──────────────────────────────────────────────────────────────────────────────
// CareTeam
// ──────────────────────────────────────────────────────────────────────────────

// CareTeam identifies the people involved in a patient's care.
type CareTeam struct {
	base

	Status      string            `json:"status,omitempty"`
	Category    []CodeableConcept `json:"category,omitempty"`
	Name        string            `json:"name,omitempty"`
	Subject     *Reference        `json:"subject,omitempty"`
	Encounter   *Reference        `json:"encounter,omitempty"`
	Period      *Period           `json:"period,omitempty"`
	Participant []CareTeamMember  `json:"participant,omitempty"`
	ManagingOrg []Reference       `json:"managingOrganization,omitempty"`
}

// CareTeamMember identifies one team member and their role.
type CareTeamMember struct {
	Role   []CodeableConcept `json:"role,omitempty"`
	Member *Reference        `json:"member,omitempty"`
	Period *Period           `json:"period,omitempty"`
}

func (c *CareTeam) ResourceTypeName() string { return "CareTeam" }
func (c *CareTeam) ResourceID() string       { return c.ID }
func (c *CareTeam) SetResourceID(id string)  { c.ID = id; c.ResourceType = "CareTeam" }

// ──────────────────────────────────────────────────────────────────────────────
// Goal
// ──────────────────────────────────────────────────────────────────────────────

// Goal describes a desired health outcome for a patient.
type Goal struct {
	base

	LifecycleStatus string            `json:"lifecycleStatus,omitempty"`
	Category        []CodeableConcept `json:"category,omitempty"`
	Priority        *CodeableConcept  `json:"priority,omitempty"`
	Description     *CodeableConcept  `json:"description,omitempty"`
	Subject         *Reference        `json:"subject,omitempty"`
	StartDate       string            `json:"startDate,omitempty"`
	Target          []GoalTarget      `json:"target,omitempty"`
	StatusDate      string            `json:"statusDate,omitempty"`
	Note            []Annotation      `json:"note,omitempty"`
}

// GoalTarget describes a measurable criterion for the goal.
type GoalTarget struct {
	Measure *CodeableConcept `json:"measure,omitempty"`
	Detail  *Quantity        `json:"detailQuantity,omitempty"`
	DueDate string           `json:"dueDate,omitempty"`
}

func (g *Goal) ResourceTypeName() string { return "Goal" }
func (g *Goal) ResourceID() string       { return g.ID }
func (g *Goal) SetResourceID(id string)  { g.ID = id; g.ResourceType = "Goal" }

// ──────────────────────────────────────────────────────────────────────────────
// Device
// ──────────────────────────────────────────────────────────────────────────────

// Device describes a physical device used in healthcare.
type Device struct {
	base

	Identifier     []Identifier     `json:"identifier,omitempty"`
	Status         string           `json:"status,omitempty"`
	Type           *CodeableConcept `json:"type,omitempty"`
	Manufacturer   string           `json:"manufacturer,omitempty"`
	Model          string           `json:"modelNumber,omitempty"`
	SerialNumber   string           `json:"serialNumber,omitempty"`
	DeviceName     []DeviceName     `json:"deviceName,omitempty"`
	LotNumber      string           `json:"lotNumber,omitempty"`
	ExpirationDate string           `json:"expirationDate,omitempty"`
	Patient        *Reference       `json:"patient,omitempty"`
	Owner          *Reference       `json:"owner,omitempty"`
	UDICarrier     []UDICarrier     `json:"udiCarrier,omitempty"`
}

// DeviceName is one of possibly several names for a device.
type DeviceName struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

// UDICarrier holds the Unique Device Identifier barcode data.
type UDICarrier struct {
	DeviceIdentifier string `json:"deviceIdentifier,omitempty"`
	CarrierHRF       string `json:"carrierHRF,omitempty"`
}

func (d *Device) ResourceTypeName() string { return "Device" }
func (d *Device) ResourceID() string       { return d.ID }
func (d *Device) SetResourceID(id string)  { d.ID = id; d.ResourceType = "Device" }

// ──────────────────────────────────────────────────────────────────────────────
// RelatedPerson
// ──────────────────────────────────────────────────────────────────────────────

// RelatedPerson identifies a person involved in a patient's care who is not
// themselves a patient (family member, guardian, emergency contact).
type RelatedPerson struct {
	base

	Identifier   []Identifier      `json:"identifier,omitempty"`
	Active       *bool             `json:"active,omitempty"`
	Patient      *Reference        `json:"patient,omitempty"`
	Relationship []CodeableConcept `json:"relationship,omitempty"`
	Name         []HumanName       `json:"name,omitempty"`
	Telecom      []ContactPoint    `json:"telecom,omitempty"`
	Gender       string            `json:"gender,omitempty"`
	BirthDate    string            `json:"birthDate,omitempty"`
	Address      []Address         `json:"address,omitempty"`
	Period       *Period           `json:"period,omitempty"`
}

func (r *RelatedPerson) ResourceTypeName() string { return "RelatedPerson" }
func (r *RelatedPerson) ResourceID() string       { return r.ID }
func (r *RelatedPerson) SetResourceID(id string)  { r.ID = id; r.ResourceType = "RelatedPerson" }

// ──────────────────────────────────────────────────────────────────────────────
// PractitionerRole
// ──────────────────────────────────────────────────────────────────────────────

// PractitionerRole describes a practitioner's credentials, specialties, and
// the organization where they practice.
type PractitionerRole struct {
	base

	Identifier   []Identifier      `json:"identifier,omitempty"`
	Active       *bool             `json:"active,omitempty"`
	Period       *Period           `json:"period,omitempty"`
	Practitioner *Reference        `json:"practitioner,omitempty"`
	Organization *Reference        `json:"organization,omitempty"`
	Code         []CodeableConcept `json:"code,omitempty"`
	Specialty    []CodeableConcept `json:"specialty,omitempty"`
	Location     []Reference       `json:"location,omitempty"`
	Telecom      []ContactPoint    `json:"telecom,omitempty"`
}

func (p *PractitionerRole) ResourceTypeName() string { return "PractitionerRole" }
func (p *PractitionerRole) ResourceID() string       { return p.ID }
func (p *PractitionerRole) SetResourceID(id string)  { p.ID = id; p.ResourceType = "PractitionerRole" }

// ──────────────────────────────────────────────────────────────────────────────
// Appointment
// ──────────────────────────────────────────────────────────────────────────────

// Appointment represents a scheduled healthcare event.
type Appointment struct {
	base

	Status          string                   `json:"status,omitempty"`
	ServiceType     []CodeableConcept        `json:"serviceType,omitempty"`
	Specialty       []CodeableConcept        `json:"specialty,omitempty"`
	AppointmentType *CodeableConcept         `json:"appointmentType,omitempty"`
	ReasonCode      []CodeableConcept        `json:"reasonCode,omitempty"`
	Priority        *int                     `json:"priority,omitempty"`
	Description     string                   `json:"description,omitempty"`
	Start           string                   `json:"start,omitempty"`
	End             string                   `json:"end,omitempty"`
	MinutesDuration *int                     `json:"minutesDuration,omitempty"`
	Created         string                   `json:"created,omitempty"`
	Comment         string                   `json:"comment,omitempty"`
	Participant     []AppointmentParticipant `json:"participant,omitempty"`
}

// AppointmentParticipant is a person or resource involved in the appointment.
type AppointmentParticipant struct {
	Type     []CodeableConcept `json:"type,omitempty"`
	Actor    *Reference        `json:"actor,omitempty"`
	Required string            `json:"required,omitempty"`
	Status   string            `json:"status,omitempty"`
}

func (a *Appointment) ResourceTypeName() string { return "Appointment" }
func (a *Appointment) ResourceID() string       { return a.ID }
func (a *Appointment) SetResourceID(id string)  { a.ID = id; a.ResourceType = "Appointment" }

// ──────────────────────────────────────────────────────────────────────────────
// Consent
// ──────────────────────────────────────────────────────────────────────────────

// Consent records a patient's choices about how their data is used.
type Consent struct {
	base

	Status       string            `json:"status,omitempty"`
	Scope        *CodeableConcept  `json:"scope,omitempty"`
	Category     []CodeableConcept `json:"category,omitempty"`
	Patient      *Reference        `json:"patient,omitempty"`
	DateTime     string            `json:"dateTime,omitempty"`
	Performer    []Reference       `json:"performer,omitempty"`
	Organization []Reference       `json:"organization,omitempty"`
	Policy       []ConsentPolicy   `json:"policy,omitempty"`
}

// ConsentPolicy identifies the regulatory basis.
type ConsentPolicy struct {
	Authority string `json:"authority,omitempty"`
	URI       string `json:"uri,omitempty"`
}

func (c *Consent) ResourceTypeName() string { return "Consent" }
func (c *Consent) ResourceID() string       { return c.ID }
func (c *Consent) SetResourceID(id string)  { c.ID = id; c.ResourceType = "Consent" }

// ──────────────────────────────────────────────────────────────────────────────
// Composition
// ──────────────────────────────────────────────────────────────────────────────

// Composition is a clinical document: a structured set of resource references
// with a narrative. It is the FHIR equivalent of a CDA document.
type Composition struct {
	base

	Status    string               `json:"status,omitempty"`
	Type      *CodeableConcept     `json:"type,omitempty"`
	Category  []CodeableConcept    `json:"category,omitempty"`
	Subject   *Reference           `json:"subject,omitempty"`
	Encounter *Reference           `json:"encounter,omitempty"`
	Date      string               `json:"date,omitempty"`
	Author    []Reference          `json:"author,omitempty"`
	Title     string               `json:"title,omitempty"`
	Section   []CompositionSection `json:"section,omitempty"`
}

// CompositionSection is one section of a clinical document.
type CompositionSection struct {
	Title   string               `json:"title,omitempty"`
	Code    *CodeableConcept     `json:"code,omitempty"`
	Text    *Narrative           `json:"text,omitempty"`
	Entry   []Reference          `json:"entry,omitempty"`
	Section []CompositionSection `json:"section,omitempty"`
}

func (c *Composition) ResourceTypeName() string { return "Composition" }
func (c *Composition) ResourceID() string       { return c.ID }
func (c *Composition) SetResourceID(id string)  { c.ID = id; c.ResourceType = "Composition" }

// ──────────────────────────────────────────────────────────────────────────────
// FamilyMemberHistory
// ──────────────────────────────────────────────────────────────────────────────

// FamilyMemberHistory records significant health events for a relative.
type FamilyMemberHistory struct {
	base

	Status       string           `json:"status,omitempty"`
	Patient      *Reference       `json:"patient,omitempty"`
	Date         string           `json:"date,omitempty"`
	Name         string           `json:"name,omitempty"`
	Relationship *CodeableConcept `json:"relationship,omitempty"`
	Sex          *CodeableConcept `json:"sex,omitempty"`
	BornDate     string           `json:"bornDate,omitempty"`
	DeceasedBool *bool            `json:"deceasedBoolean,omitempty"`
	DeceasedDate string           `json:"deceasedDate,omitempty"`
	Condition    []FMHCondition   `json:"condition,omitempty"`
}

// FMHCondition is a condition reported in a family member's history.
type FMHCondition struct {
	Code  *CodeableConcept `json:"code,omitempty"`
	Onset string           `json:"onsetString,omitempty"`
	Note  []Annotation     `json:"note,omitempty"`
}

func (f *FamilyMemberHistory) ResourceTypeName() string { return "FamilyMemberHistory" }
func (f *FamilyMemberHistory) ResourceID() string       { return f.ID }
func (f *FamilyMemberHistory) SetResourceID(id string) {
	f.ID = id
	f.ResourceType = "FamilyMemberHistory"
}

// ──────────────────────────────────────────────────────────────────────────────
// Communication
// ──────────────────────────────────────────────────────────────────────────────

// Communication records a message between participants in care.
type Communication struct {
	base

	Status    string            `json:"status,omitempty"`
	Category  []CodeableConcept `json:"category,omitempty"`
	Priority  string            `json:"priority,omitempty"`
	Subject   *Reference        `json:"subject,omitempty"`
	Encounter *Reference        `json:"encounter,omitempty"`
	Sent      string            `json:"sent,omitempty"`
	Received  string            `json:"received,omitempty"`
	Sender    *Reference        `json:"sender,omitempty"`
	Recipient []Reference       `json:"recipient,omitempty"`
	Payload   []CommPayload     `json:"payload,omitempty"`
}

// CommPayload is the content of the communication.
type CommPayload struct {
	ContentString     string      `json:"contentString,omitempty"`
	ContentAttachment *Attachment `json:"contentAttachment,omitempty"`
	ContentReference  *Reference  `json:"contentReference,omitempty"`
}

func (c *Communication) ResourceTypeName() string { return "Communication" }
func (c *Communication) ResourceID() string       { return c.ID }
func (c *Communication) SetResourceID(id string)  { c.ID = id; c.ResourceType = "Communication" }

// ──────────────────────────────────────────────────────────────────────────────
// Task
// ──────────────────────────────────────────────────────────────────────────────

// Task represents an actionable item in a workflow.
type Task struct {
	base

	Status       string           `json:"status,omitempty"`
	Intent       string           `json:"intent,omitempty"`
	Priority     string           `json:"priority,omitempty"`
	Code         *CodeableConcept `json:"code,omitempty"`
	Description  string           `json:"description,omitempty"`
	Focus        *Reference       `json:"focus,omitempty"`
	For          *Reference       `json:"for,omitempty"`
	Encounter    *Reference       `json:"encounter,omitempty"`
	AuthoredOn   string           `json:"authoredOn,omitempty"`
	LastModified string           `json:"lastModified,omitempty"`
	Requester    *Reference       `json:"requester,omitempty"`
	Owner        *Reference       `json:"owner,omitempty"`
	Note         []Annotation     `json:"note,omitempty"`
}

func (t *Task) ResourceTypeName() string { return "Task" }
func (t *Task) ResourceID() string       { return t.ID }
func (t *Task) SetResourceID(id string)  { t.ID = id; t.ResourceType = "Task" }

// ──────────────────────────────────────────────────────────────────────────────
// Provenance
// ──────────────────────────────────────────────────────────────────────────────

// Provenance records who created or changed a resource.
type Provenance struct {
	base

	Target   []Reference       `json:"target,omitempty"`
	Occurred string            `json:"occurredDateTime,omitempty"`
	Recorded string            `json:"recorded,omitempty"`
	Activity *CodeableConcept  `json:"activity,omitempty"`
	Agent    []ProvenanceAgent `json:"agent,omitempty"`
}

// ProvenanceAgent identifies a participant in the provenance event.
type ProvenanceAgent struct {
	Type       *CodeableConcept `json:"type,omitempty"`
	Who        *Reference       `json:"who,omitempty"`
	OnBehalfOf *Reference       `json:"onBehalfOf,omitempty"`
}

func (p *Provenance) ResourceTypeName() string { return "Provenance" }
func (p *Provenance) ResourceID() string       { return p.ID }
func (p *Provenance) SetResourceID(id string)  { p.ID = id; p.ResourceType = "Provenance" }

// ──────────────────────────────────────────────────────────────────────────────
// QuestionnaireResponse
// ──────────────────────────────────────────────────────────────────────────────

// QuestionnaireResponse captures answers to a structured form.
type QuestionnaireResponse struct {
	base

	Questionnaire string     `json:"questionnaire,omitempty"`
	Status        string     `json:"status,omitempty"`
	Subject       *Reference `json:"subject,omitempty"`
	Encounter     *Reference `json:"encounter,omitempty"`
	Authored      string     `json:"authored,omitempty"`
	Author        *Reference `json:"author,omitempty"`
	Source        *Reference `json:"source,omitempty"`
	Item          []QRItem   `json:"item,omitempty"`
}

// QRItem is one answered question.
type QRItem struct {
	LinkID string     `json:"linkId,omitempty"`
	Text   string     `json:"text,omitempty"`
	Answer []QRAnswer `json:"answer,omitempty"`
	Item   []QRItem   `json:"item,omitempty"`
}

// QRAnswer is one answer value.
type QRAnswer struct {
	ValueString  string   `json:"valueString,omitempty"`
	ValueCoding  *Coding  `json:"valueCoding,omitempty"`
	ValueBoolean *bool    `json:"valueBoolean,omitempty"`
	ValueDecimal *float64 `json:"valueDecimal,omitempty"`
	ValueInteger *int     `json:"valueInteger,omitempty"`
	ValueDate    string   `json:"valueDate,omitempty"`
	Item         []QRItem `json:"item,omitempty"`
}

func (q *QuestionnaireResponse) ResourceTypeName() string { return "QuestionnaireResponse" }
func (q *QuestionnaireResponse) ResourceID() string       { return q.ID }
func (q *QuestionnaireResponse) SetResourceID(id string) {
	q.ID = id
	q.ResourceType = "QuestionnaireResponse"
}

// ──────────────────────────────────────────────────────────────────────────────
// Media
// ──────────────────────────────────────────────────────────────────────────────

// Media represents a photo, video, or audio recording.
type Media struct {
	base

	Identifier []Identifier     `json:"identifier,omitempty"`
	Status     string           `json:"status,omitempty"`
	Type       *CodeableConcept `json:"type,omitempty"`
	Modality   *CodeableConcept `json:"modality,omitempty"`
	Subject    *Reference       `json:"subject,omitempty"`
	Encounter  *Reference       `json:"encounter,omitempty"`
	CreatedDT  string           `json:"createdDateTime,omitempty"`
	Operator   *Reference       `json:"operator,omitempty"`
	Content    *Attachment      `json:"content,omitempty"`
	Note       []Annotation     `json:"note,omitempty"`
}

func (m *Media) ResourceTypeName() string { return "Media" }
func (m *Media) ResourceID() string       { return m.ID }
func (m *Media) SetResourceID(id string)  { m.ID = id; m.ResourceType = "Media" }

// ──────────────────────────────────────────────────────────────────────────────
// Dosage, DoseAndRate, Timing — defined in uscore.go
// ──────────────────────────────────────────────────────────────────────────────
