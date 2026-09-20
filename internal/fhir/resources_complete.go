package fhir

// Complete FHIR R4 resource coverage — all 146 resource types.
//
// This file adds every R4 resource type not already defined in resources.go,
// resources_additional.go, or uscore.go. Each struct captures the fields that
// matter for integration routing, search, and indexing. Fields that are purely
// internal to a FHIR server (like contained resources and extension lists) are
// deliberately omitted — this is an integration engine, not a persistence layer.
//
// Organized by FHIR module:
//   - Foundation (infrastructure, conformance, terminology)
//   - Security & privacy
//   - Clinical (summary, diagnostics, medications, care provision, request/response)
//   - Financial (billing, payment, support)
//   - Specialized (public health, research, evidence, quality)
//   - Workflow (scheduling, orders, definitions)

// ──────────────────────────────────────────────────────────────────────────────
// Foundation — infrastructure resources
// ──────────────────────────────────────────────────────────────────────────────

// StructureDefinition defines a FHIR resource or data type structure.
type StructureDefinition struct {
	base
	URL            string `json:"url,omitempty"`
	Name           string `json:"name,omitempty"`
	Status         string `json:"status,omitempty"`
	Kind           string `json:"kind,omitempty"`
	Type           string `json:"type,omitempty"`
	BaseDefinition string `json:"baseDefinition,omitempty"`
	Derivation     string `json:"derivation,omitempty"`
	Description    string `json:"description,omitempty"`
	Version        string `json:"version,omitempty"`
	Publisher      string `json:"publisher,omitempty"`
}

// CapabilityStatement describes what a FHIR server can do.
type CapabilityStatement struct {
	base
	URL         string   `json:"url,omitempty"`
	Name        string   `json:"name,omitempty"`
	Status      string   `json:"status,omitempty"`
	Kind        string   `json:"kind,omitempty"`
	FHIRVersion string   `json:"fhirVersion,omitempty"`
	Format      []string `json:"format,omitempty"`
	Description string   `json:"description,omitempty"`
	Publisher   string   `json:"publisher,omitempty"`
	Date        string   `json:"date,omitempty"`
}

// CodeSystem defines a set of codes.
type CodeSystem struct {
	base
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Content     string `json:"content,omitempty"`
	Count       int    `json:"count,omitempty"`
	Version     string `json:"version,omitempty"`
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
}

// ValueSet and ConceptMap are defined in valueset.go and conceptmap.go.

// NamingSystem defines a naming or identity system.
type NamingSystem struct {
	base
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	Date        string `json:"date,omitempty"`
}

// ImplementationGuide defines a set of rules about how resources are used.
type ImplementationGuide struct {
	base
	URL         string   `json:"url,omitempty"`
	Name        string   `json:"name,omitempty"`
	Status      string   `json:"status,omitempty"`
	FHIRVersion []string `json:"fhirVersion,omitempty"`
	Description string   `json:"description,omitempty"`
	Publisher   string   `json:"publisher,omitempty"`
}

// SearchParameter defines a search parameter for FHIR resources.
type SearchParameter struct {
	base
	URL         string   `json:"url,omitempty"`
	Name        string   `json:"name,omitempty"`
	Status      string   `json:"status,omitempty"`
	Code        string   `json:"code,omitempty"`
	Type        string   `json:"type,omitempty"`
	Base        []string `json:"base,omitempty"`
	Expression  string   `json:"expression,omitempty"`
	Description string   `json:"description,omitempty"`
}

// OperationDefinition defines an operation that a server implements.
type OperationDefinition struct {
	base
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Code        string `json:"code,omitempty"`
	System      bool   `json:"system,omitempty"`
	Type        bool   `json:"type,omitempty"`
	Instance    bool   `json:"instance,omitempty"`
	Description string `json:"description,omitempty"`
}

// CompartmentDefinition defines a FHIR compartment.
type CompartmentDefinition struct {
	base
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Code        string `json:"code,omitempty"`
	Description string `json:"description,omitempty"`
}

// GraphDefinition defines a graph of resources.
type GraphDefinition struct {
	base
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Start       string `json:"start,omitempty"`
	Description string `json:"description,omitempty"`
}

// StructureMap defines a mapping between structures.
type StructureMap struct {
	base
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
}

// MessageDefinition defines a message that can be exchanged.
type MessageDefinition struct {
	base
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Category    string `json:"category,omitempty"`
	Focus       string `json:"focus,omitempty"`
	Description string `json:"description,omitempty"`
}

// MessageHeader is the first resource in a message bundle.
type MessageHeader struct {
	base
	Event       *Coding          `json:"eventCoding,omitempty"`
	Source      *Reference       `json:"source,omitempty"`
	Destination []Reference      `json:"destination,omitempty"`
	Reason      *CodeableConcept `json:"reason,omitempty"`
}

// Subscription represents a channel for receiving notifications.
type Subscription struct {
	base
	Status   string               `json:"status,omitempty"`
	Criteria string               `json:"criteria,omitempty"`
	Reason   string               `json:"reason,omitempty"`
	Channel  *SubscriptionChannel `json:"channel,omitempty"`
	End      string               `json:"end,omitempty"`
}

// SubscriptionChannel is the delivery mechanism for a Subscription.
type SubscriptionChannel struct {
	Type     string `json:"type,omitempty"`
	Endpoint string `json:"endpoint,omitempty"`
	Payload  string `json:"payload,omitempty"`
}

// Parameters and ParametersParameter are defined in parameters.go.

// Binary carries a raw blob of data.
type Binary struct {
	base
	ContentType string `json:"contentType,omitempty"`
	Data        string `json:"data,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Security and privacy
// ──────────────────────────────────────────────────────────────────────────────

// AuditEvent records a security-relevant event.
type AuditEvent struct {
	base
	Type     *Coding           `json:"type,omitempty"`
	Subtype  []Coding          `json:"subtype,omitempty"`
	Action   string            `json:"action,omitempty"`
	Period   *Period           `json:"period,omitempty"`
	Recorded string            `json:"recorded,omitempty"`
	Outcome  string            `json:"outcome,omitempty"`
	Agent    []AuditEventAgent `json:"agent,omitempty"`
	Source   *AuditEventSource `json:"source,omitempty"`
}

// AuditEventAgent is who did the thing.
type AuditEventAgent struct {
	Who       *Reference        `json:"who,omitempty"`
	Requestor bool              `json:"requestor,omitempty"`
	Role      []CodeableConcept `json:"role,omitempty"`
}

// AuditEventSource identifies where the event was reported from.
type AuditEventSource struct {
	Observer *Reference `json:"observer,omitempty"`
	Type     []Coding   `json:"type,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Clinical — patient care resources
// ──────────────────────────────────────────────────────────────────────────────

// AdverseEvent records a harmful event in a patient.
type AdverseEvent struct {
	base
	Identifier  *Identifier       `json:"identifier,omitempty"`
	Actuality   string            `json:"actuality,omitempty"`
	Category    []CodeableConcept `json:"category,omitempty"`
	Event       *CodeableConcept  `json:"event,omitempty"`
	Subject     *Reference        `json:"subject,omitempty"`
	Date        string            `json:"date,omitempty"`
	Seriousness *CodeableConcept  `json:"seriousness,omitempty"`
	Outcome     *CodeableConcept  `json:"outcome,omitempty"`
	Recorder    *Reference        `json:"recorder,omitempty"`
}

// DetectedIssue indicates a potential problem with a clinical action.
type DetectedIssue struct {
	base
	Identifier         []Identifier     `json:"identifier,omitempty"`
	Status             string           `json:"status,omitempty"`
	Code               *CodeableConcept `json:"code,omitempty"`
	Severity           string           `json:"severity,omitempty"`
	Patient            *Reference       `json:"patient,omitempty"`
	IdentifiedDateTime string           `json:"identifiedDateTime,omitempty"`
	Author             *Reference       `json:"author,omitempty"`
	Detail             string           `json:"detail,omitempty"`
}

// ClinicalImpression is a clinical assessment.
type ClinicalImpression struct {
	base
	Identifier []Identifier     `json:"identifier,omitempty"`
	Status     string           `json:"status,omitempty"`
	Code       *CodeableConcept `json:"code,omitempty"`
	Subject    *Reference       `json:"subject,omitempty"`
	Encounter  *Reference       `json:"encounter,omitempty"`
	Date       string           `json:"date,omitempty"`
	Assessor   *Reference       `json:"assessor,omitempty"`
	Summary    string           `json:"summary,omitempty"`
}

// RiskAssessment evaluates a risk for a patient.
type RiskAssessment struct {
	base
	Identifier         []Identifier     `json:"identifier,omitempty"`
	Status             string           `json:"status,omitempty"`
	Code               *CodeableConcept `json:"code,omitempty"`
	Subject            *Reference       `json:"subject,omitempty"`
	Encounter          *Reference       `json:"encounter,omitempty"`
	OccurrenceDateTime string           `json:"occurrenceDateTime,omitempty"`
	Condition          *Reference       `json:"condition,omitempty"`
	Performer          *Reference       `json:"performer,omitempty"`
	Basis              []Reference      `json:"basis,omitempty"`
}

// BodyStructure identifies a specific anatomical structure.
type BodyStructure struct {
	base
	Identifier  []Identifier     `json:"identifier,omitempty"`
	Active      *bool            `json:"active,omitempty"`
	Morphology  *CodeableConcept `json:"morphology,omitempty"`
	Location    *CodeableConcept `json:"location,omitempty"`
	Description string           `json:"description,omitempty"`
	Patient     *Reference       `json:"patient,omitempty"`
}

// Flag represents a warning or notification about a patient.
type Flag struct {
	base
	Identifier []Identifier      `json:"identifier,omitempty"`
	Status     string            `json:"status,omitempty"`
	Category   []CodeableConcept `json:"category,omitempty"`
	Code       *CodeableConcept  `json:"code,omitempty"`
	Subject    *Reference        `json:"subject,omitempty"`
	Period     *Period           `json:"period,omitempty"`
	Author     *Reference        `json:"author,omitempty"`
}

// List is a curated collection of resources.
type List struct {
	base
	Identifier []Identifier     `json:"identifier,omitempty"`
	Status     string           `json:"status,omitempty"`
	Mode       string           `json:"mode,omitempty"`
	Title      string           `json:"title,omitempty"`
	Code       *CodeableConcept `json:"code,omitempty"`
	Subject    *Reference       `json:"subject,omitempty"`
	Encounter  *Reference       `json:"encounter,omitempty"`
	Date       string           `json:"date,omitempty"`
	Source     *Reference       `json:"source,omitempty"`
	Entry      []ListEntry      `json:"entry,omitempty"`
}

// ListEntry is one item in a List.
type ListEntry struct {
	Flag    *CodeableConcept `json:"flag,omitempty"`
	Deleted *bool            `json:"deleted,omitempty"`
	Date    string           `json:"date,omitempty"`
	Item    *Reference       `json:"item,omitempty"`
}

// EpisodeOfCare groups encounters over time for a condition.
type EpisodeOfCare struct {
	base
	Identifier           []Identifier      `json:"identifier,omitempty"`
	Status               string            `json:"status,omitempty"`
	Type                 []CodeableConcept `json:"type,omitempty"`
	Patient              *Reference        `json:"patient,omitempty"`
	ManagingOrganization *Reference        `json:"managingOrganization,omitempty"`
	Period               *Period           `json:"period,omitempty"`
	CareManager          *Reference        `json:"careManager,omitempty"`
}

// NutritionOrder prescribes diet/nutrition for a patient.
type NutritionOrder struct {
	base
	Identifier             []Identifier      `json:"identifier,omitempty"`
	Status                 string            `json:"status,omitempty"`
	Intent                 string            `json:"intent,omitempty"`
	Patient                *Reference        `json:"patient,omitempty"`
	Encounter              *Reference        `json:"encounter,omitempty"`
	DateTime               string            `json:"dateTime,omitempty"`
	Orderer                *Reference        `json:"orderer,omitempty"`
	FoodPreferenceModifier []CodeableConcept `json:"foodPreferenceModifier,omitempty"`
}

// VisionPrescription prescribes corrective lenses.
type VisionPrescription struct {
	base
	Identifier  []Identifier `json:"identifier,omitempty"`
	Status      string       `json:"status,omitempty"`
	Created     string       `json:"created,omitempty"`
	Patient     *Reference   `json:"patient,omitempty"`
	Encounter   *Reference   `json:"encounter,omitempty"`
	DateWritten string       `json:"dateWritten,omitempty"`
	Prescriber  *Reference   `json:"prescriber,omitempty"`
}

// DeviceRequest orders use of a device.
type DeviceRequest struct {
	base
	Identifier []Identifier     `json:"identifier,omitempty"`
	Status     string           `json:"status,omitempty"`
	Intent     string           `json:"intent,omitempty"`
	Code       *CodeableConcept `json:"codeCodeableConcept,omitempty"`
	Subject    *Reference       `json:"subject,omitempty"`
	Encounter  *Reference       `json:"encounter,omitempty"`
	AuthoredOn string           `json:"authoredOn,omitempty"`
	Requester  *Reference       `json:"requester,omitempty"`
	Performer  *Reference       `json:"performer,omitempty"`
}

// DeviceUseStatement records a device being used by a patient.
type DeviceUseStatement struct {
	base
	Identifier []Identifier `json:"identifier,omitempty"`
	Status     string       `json:"status,omitempty"`
	Subject    *Reference   `json:"subject,omitempty"`
	Device     *Reference   `json:"device,omitempty"`
	RecordedOn string       `json:"recordedOn,omitempty"`
	Source     *Reference   `json:"source,omitempty"`
}

// SupplyRequest orders supplies.
type SupplyRequest struct {
	base
	Identifier []Identifier     `json:"identifier,omitempty"`
	Status     string           `json:"status,omitempty"`
	Category   *CodeableConcept `json:"category,omitempty"`
	Item       *CodeableConcept `json:"itemCodeableConcept,omitempty"`
	Quantity   *Quantity        `json:"quantity,omitempty"`
	Requester  *Reference       `json:"requester,omitempty"`
	AuthoredOn string           `json:"authoredOn,omitempty"`
	Deliverer  *Reference       `json:"deliverFrom,omitempty"`
}

// SupplyDelivery records the delivery of supplies.
type SupplyDelivery struct {
	base
	Identifier         []Identifier     `json:"identifier,omitempty"`
	Status             string           `json:"status,omitempty"`
	Patient            *Reference       `json:"patient,omitempty"`
	Type               *CodeableConcept `json:"type,omitempty"`
	Supplier           *Reference       `json:"supplier,omitempty"`
	OccurrenceDateTime string           `json:"occurrenceDateTime,omitempty"`
}

// RequestGroup organizes related requests.
type RequestGroup struct {
	base
	Identifier []Identifier     `json:"identifier,omitempty"`
	Status     string           `json:"status,omitempty"`
	Intent     string           `json:"intent,omitempty"`
	Code       *CodeableConcept `json:"code,omitempty"`
	Subject    *Reference       `json:"subject,omitempty"`
	Encounter  *Reference       `json:"encounter,omitempty"`
	AuthoredOn string           `json:"authoredOn,omitempty"`
	Author     *Reference       `json:"author,omitempty"`
}

// GuidanceResponse carries the result of decision support evaluation.
type GuidanceResponse struct {
	base
	Identifier         []Identifier `json:"identifier,omitempty"`
	Status             string       `json:"status,omitempty"`
	Subject            *Reference   `json:"subject,omitempty"`
	Encounter          *Reference   `json:"encounter,omitempty"`
	OccurrenceDateTime string       `json:"occurrenceDateTime,omitempty"`
	Performer          *Reference   `json:"performer,omitempty"`
	Result             *Reference   `json:"result,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Diagnostic resources
// ──────────────────────────────────────────────────────────────────────────────

// ImagingStudy represents a DICOM imaging study.
type ImagingStudy struct {
	base
	Identifier        []Identifier `json:"identifier,omitempty"`
	Status            string       `json:"status,omitempty"`
	Modality          []Coding     `json:"modality,omitempty"`
	Subject           *Reference   `json:"subject,omitempty"`
	Encounter         *Reference   `json:"encounter,omitempty"`
	Started           string       `json:"started,omitempty"`
	BasedOn           []Reference  `json:"basedOn,omitempty"`
	NumberOfSeries    int          `json:"numberOfSeries,omitempty"`
	NumberOfInstances int          `json:"numberOfInstances,omitempty"`
	Description       string       `json:"description,omitempty"`
	Endpoint          []Reference  `json:"endpoint,omitempty"`
}

// MolecularSequence describes a molecular sequence.
type MolecularSequence struct {
	base
	Identifier       []Identifier `json:"identifier,omitempty"`
	Type             string       `json:"type,omitempty"`
	Patient          *Reference   `json:"patient,omitempty"`
	Specimen         *Reference   `json:"specimen,omitempty"`
	Device           *Reference   `json:"device,omitempty"`
	Performer        *Reference   `json:"performer,omitempty"`
	ObservedSeq      string       `json:"observedSeq,omitempty"`
	CoordinateSystem int          `json:"coordinateSystem,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Medications — additional
// ──────────────────────────────────────────────────────────────────────────────

// MedicationKnowledge provides drug information.
type MedicationKnowledge struct {
	base
	Code         *CodeableConcept `json:"code,omitempty"`
	Status       string           `json:"status,omitempty"`
	Manufacturer *Reference       `json:"manufacturer,omitempty"`
	DoseForm     *CodeableConcept `json:"doseForm,omitempty"`
	Amount       *Quantity        `json:"amount,omitempty"`
	Synonym      []string         `json:"synonym,omitempty"`
}

// Immunization is already defined but ImmunizationEvaluation and ImmunizationRecommendation are not.

// ImmunizationEvaluation assesses whether a dose counts toward immunity.
type ImmunizationEvaluation struct {
	base
	Identifier        []Identifier     `json:"identifier,omitempty"`
	Status            string           `json:"status,omitempty"`
	Patient           *Reference       `json:"patient,omitempty"`
	Date              string           `json:"date,omitempty"`
	Authority         *Reference       `json:"authority,omitempty"`
	TargetDisease     *CodeableConcept `json:"targetDisease,omitempty"`
	ImmunizationEvent *Reference       `json:"immunizationEvent,omitempty"`
	DoseStatus        *CodeableConcept `json:"doseStatus,omitempty"`
}

// ImmunizationRecommendation provides vaccination guidance.
type ImmunizationRecommendation struct {
	base
	Identifier []Identifier `json:"identifier,omitempty"`
	Patient    *Reference   `json:"patient,omitempty"`
	Date       string       `json:"date,omitempty"`
	Authority  *Reference   `json:"authority,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Financial — billing, payment, eligibility
// ──────────────────────────────────────────────────────────────────────────────

// ClaimResponse is the adjudication response to a Claim.
type ClaimResponse struct {
	base
	Identifier  []Identifier     `json:"identifier,omitempty"`
	Status      string           `json:"status,omitempty"`
	Type        *CodeableConcept `json:"type,omitempty"`
	Use         string           `json:"use,omitempty"`
	Patient     *Reference       `json:"patient,omitempty"`
	Created     string           `json:"created,omitempty"`
	Insurer     *Reference       `json:"insurer,omitempty"`
	Requestor   *Reference       `json:"requestor,omitempty"`
	Request     *Reference       `json:"request,omitempty"`
	Outcome     string           `json:"outcome,omitempty"`
	Disposition string           `json:"disposition,omitempty"`
}

// CoverageEligibilityRequest asks whether a patient has coverage.
type CoverageEligibilityRequest struct {
	base
	Identifier []Identifier `json:"identifier,omitempty"`
	Status     string       `json:"status,omitempty"`
	Purpose    []string     `json:"purpose,omitempty"`
	Patient    *Reference   `json:"patient,omitempty"`
	Created    string       `json:"created,omitempty"`
	Provider   *Reference   `json:"provider,omitempty"`
	Insurer    *Reference   `json:"insurer,omitempty"`
}

// CoverageEligibilityResponse answers whether a patient has coverage.
type CoverageEligibilityResponse struct {
	base
	Identifier []Identifier `json:"identifier,omitempty"`
	Status     string       `json:"status,omitempty"`
	Purpose    []string     `json:"purpose,omitempty"`
	Patient    *Reference   `json:"patient,omitempty"`
	Created    string       `json:"created,omitempty"`
	Request    *Reference   `json:"request,omitempty"`
	Outcome    string       `json:"outcome,omitempty"`
	Insurer    *Reference   `json:"insurer,omitempty"`
}

// EnrollmentRequest asks to add someone to a coverage plan.
type EnrollmentRequest struct {
	base
	Identifier []Identifier `json:"identifier,omitempty"`
	Status     string       `json:"status,omitempty"`
	Created    string       `json:"created,omitempty"`
	Insurer    *Reference   `json:"insurer,omitempty"`
	Provider   *Reference   `json:"provider,omitempty"`
	Candidate  *Reference   `json:"candidate,omitempty"`
	Coverage   *Reference   `json:"coverage,omitempty"`
}

// EnrollmentResponse answers an enrollment request.
type EnrollmentResponse struct {
	base
	Identifier   []Identifier `json:"identifier,omitempty"`
	Status       string       `json:"status,omitempty"`
	Created      string       `json:"created,omitempty"`
	Organization *Reference   `json:"organization,omitempty"`
	Request      *Reference   `json:"request,omitempty"`
	Outcome      string       `json:"outcome,omitempty"`
}

// PaymentNotice reports a payment or payment status.
type PaymentNotice struct {
	base
	Identifier    []Identifier     `json:"identifier,omitempty"`
	Status        string           `json:"status,omitempty"`
	Request       *Reference       `json:"request,omitempty"`
	Response      *Reference       `json:"response,omitempty"`
	Created       string           `json:"created,omitempty"`
	Provider      *Reference       `json:"provider,omitempty"`
	Payment       *Reference       `json:"payment,omitempty"`
	PaymentDate   string           `json:"paymentDate,omitempty"`
	Payee         *Reference       `json:"payee,omitempty"`
	Recipient     *Reference       `json:"recipient,omitempty"`
	Amount        *Money           `json:"amount,omitempty"`
	PaymentStatus *CodeableConcept `json:"paymentStatus,omitempty"`
}

// PaymentReconciliation matches payment to claims.
type PaymentReconciliation struct {
	base
	Identifier    []Identifier `json:"identifier,omitempty"`
	Status        string       `json:"status,omitempty"`
	Period        *Period      `json:"period,omitempty"`
	Created       string       `json:"created,omitempty"`
	PaymentIssuer *Reference   `json:"paymentIssuer,omitempty"`
	Request       *Reference   `json:"request,omitempty"`
	Requestor     *Reference   `json:"requestor,omitempty"`
	Outcome       string       `json:"outcome,omitempty"`
	PaymentAmount *Money       `json:"paymentAmount,omitempty"`
	PaymentDate   string       `json:"paymentDate,omitempty"`
}

// Account tracks charges and transactions.
type Account struct {
	base
	Identifier    []Identifier     `json:"identifier,omitempty"`
	Status        string           `json:"status,omitempty"`
	Type          *CodeableConcept `json:"type,omitempty"`
	Name          string           `json:"name,omitempty"`
	Subject       []Reference      `json:"subject,omitempty"`
	ServicePeriod *Period          `json:"servicePeriod,omitempty"`
	Description   string           `json:"description,omitempty"`
}

// ChargeItem records a charge for a service.
type ChargeItem struct {
	base
	Identifier             []Identifier     `json:"identifier,omitempty"`
	Status                 string           `json:"status,omitempty"`
	Code                   *CodeableConcept `json:"code,omitempty"`
	Subject                *Reference       `json:"subject,omitempty"`
	Context                *Reference       `json:"context,omitempty"`
	OccurrenceDateTime     string           `json:"occurrenceDateTime,omitempty"`
	Performer              []Reference      `json:"performer,omitempty"`
	PerformingOrganization *Reference       `json:"performingOrganization,omitempty"`
	Quantity               *Quantity        `json:"quantity,omitempty"`
	EnteredDate            string           `json:"enteredDate,omitempty"`
}

// ChargeItemDefinition defines how charges are calculated.
type ChargeItemDefinition struct {
	base
	URL         string           `json:"url,omitempty"`
	Status      string           `json:"status,omitempty"`
	Title       string           `json:"title,omitempty"`
	Description string           `json:"description,omitempty"`
	Code        *CodeableConcept `json:"code,omitempty"`
	Publisher   string           `json:"publisher,omitempty"`
}

// Contract is a legally binding agreement.
type Contract struct {
	base
	Identifier []Identifier     `json:"identifier,omitempty"`
	Status     string           `json:"status,omitempty"`
	Issued     string           `json:"issued,omitempty"`
	Applies    *Period          `json:"applies,omitempty"`
	Subject    []Reference      `json:"subject,omitempty"`
	Authority  []Reference      `json:"authority,omitempty"`
	Type       *CodeableConcept `json:"type,omitempty"`
	Name       string           `json:"name,omitempty"`
	Title      string           `json:"title,omitempty"`
}

// InsurancePlan describes a health insurance plan.
type InsurancePlan struct {
	base
	Identifier     []Identifier      `json:"identifier,omitempty"`
	Status         string            `json:"status,omitempty"`
	Type           []CodeableConcept `json:"type,omitempty"`
	Name           string            `json:"name,omitempty"`
	Period         *Period           `json:"period,omitempty"`
	OwnedBy        *Reference        `json:"ownedBy,omitempty"`
	AdministeredBy *Reference        `json:"administeredBy,omitempty"`
}

// Invoice represents a financial invoice.
type Invoice struct {
	base
	Identifier []Identifier     `json:"identifier,omitempty"`
	Status     string           `json:"status,omitempty"`
	Type       *CodeableConcept `json:"type,omitempty"`
	Subject    *Reference       `json:"subject,omitempty"`
	Date       string           `json:"date,omitempty"`
	Issuer     *Reference       `json:"issuer,omitempty"`
	TotalNet   *Money           `json:"totalNet,omitempty"`
	TotalGross *Money           `json:"totalGross,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Workflow — scheduling, plan definitions, activity definitions
// ──────────────────────────────────────────────────────────────────────────────

// Schedule is a container for time slots.
type Schedule struct {
	base
	Identifier      []Identifier      `json:"identifier,omitempty"`
	Active          *bool             `json:"active,omitempty"`
	ServiceCategory []CodeableConcept `json:"serviceCategory,omitempty"`
	ServiceType     []CodeableConcept `json:"serviceType,omitempty"`
	Specialty       []CodeableConcept `json:"specialty,omitempty"`
	Actor           []Reference       `json:"actor,omitempty"`
	PlanningHorizon *Period           `json:"planningHorizon,omitempty"`
	Comment         string            `json:"comment,omitempty"`
}

// Slot represents a time period on a Schedule.
type Slot struct {
	base
	Identifier      []Identifier      `json:"identifier,omitempty"`
	ServiceCategory []CodeableConcept `json:"serviceCategory,omitempty"`
	ServiceType     []CodeableConcept `json:"serviceType,omitempty"`
	Schedule        *Reference        `json:"schedule,omitempty"`
	Status          string            `json:"status,omitempty"`
	Start           string            `json:"start,omitempty"`
	End             string            `json:"end,omitempty"`
}

// AppointmentResponse is a participant's response to an appointment.
type AppointmentResponse struct {
	base
	Identifier        []Identifier      `json:"identifier,omitempty"`
	Appointment       *Reference        `json:"appointment,omitempty"`
	Start             string            `json:"start,omitempty"`
	End               string            `json:"end,omitempty"`
	ParticipantType   []CodeableConcept `json:"participantType,omitempty"`
	Actor             *Reference        `json:"actor,omitempty"`
	ParticipantStatus string            `json:"participantStatus,omitempty"`
	Comment           string            `json:"comment,omitempty"`
}

// PlanDefinition is a pre-defined clinical protocol or order set.
type PlanDefinition struct {
	base
	URL         string           `json:"url,omitempty"`
	Identifier  []Identifier     `json:"identifier,omitempty"`
	Name        string           `json:"name,omitempty"`
	Title       string           `json:"title,omitempty"`
	Status      string           `json:"status,omitempty"`
	Type        *CodeableConcept `json:"type,omitempty"`
	Date        string           `json:"date,omitempty"`
	Publisher   string           `json:"publisher,omitempty"`
	Description string           `json:"description,omitempty"`
}

// ActivityDefinition defines a specific clinical action to take.
type ActivityDefinition struct {
	base
	URL         string           `json:"url,omitempty"`
	Identifier  []Identifier     `json:"identifier,omitempty"`
	Name        string           `json:"name,omitempty"`
	Title       string           `json:"title,omitempty"`
	Status      string           `json:"status,omitempty"`
	Kind        string           `json:"kind,omitempty"`
	Code        *CodeableConcept `json:"code,omitempty"`
	Date        string           `json:"date,omitempty"`
	Publisher   string           `json:"publisher,omitempty"`
	Description string           `json:"description,omitempty"`
}

// EventDefinition defines when a particular event should trigger action.
type EventDefinition struct {
	base
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
}

// Questionnaire is a structured set of questions.
type Questionnaire struct {
	base
	URL         string       `json:"url,omitempty"`
	Identifier  []Identifier `json:"identifier,omitempty"`
	Name        string       `json:"name,omitempty"`
	Title       string       `json:"title,omitempty"`
	Status      string       `json:"status,omitempty"`
	Date        string       `json:"date,omitempty"`
	Publisher   string       `json:"publisher,omitempty"`
	Description string       `json:"description,omitempty"`
	SubjectType []string     `json:"subjectType,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Specialized — public health, research, evidence
// ──────────────────────────────────────────────────────────────────────────────

// ResearchStudy is a scientific investigation.
type ResearchStudy struct {
	base
	Identifier            []Identifier      `json:"identifier,omitempty"`
	Title                 string            `json:"title,omitempty"`
	Status                string            `json:"status,omitempty"`
	PrimaryPurposeType    *CodeableConcept  `json:"primaryPurposeType,omitempty"`
	Phase                 *CodeableConcept  `json:"phase,omitempty"`
	Category              []CodeableConcept `json:"category,omitempty"`
	Condition             []CodeableConcept `json:"condition,omitempty"`
	Description           string            `json:"description,omitempty"`
	Sponsor               *Reference        `json:"sponsor,omitempty"`
	PrincipalInvestigator *Reference        `json:"principalInvestigator,omitempty"`
}

// ResearchSubject is a participant in a research study.
type ResearchSubject struct {
	base
	Identifier  []Identifier `json:"identifier,omitempty"`
	Status      string       `json:"status,omitempty"`
	Period      *Period      `json:"period,omitempty"`
	Study       *Reference   `json:"study,omitempty"`
	Individual  *Reference   `json:"individual,omitempty"`
	AssignedArm string       `json:"assignedArm,omitempty"`
	ActualArm   string       `json:"actualArm,omitempty"`
}

// Measure defines a clinical quality measure.
type Measure struct {
	base
	URL         string           `json:"url,omitempty"`
	Identifier  []Identifier     `json:"identifier,omitempty"`
	Name        string           `json:"name,omitempty"`
	Title       string           `json:"title,omitempty"`
	Status      string           `json:"status,omitempty"`
	Date        string           `json:"date,omitempty"`
	Publisher   string           `json:"publisher,omitempty"`
	Description string           `json:"description,omitempty"`
	Scoring     *CodeableConcept `json:"scoring,omitempty"`
}

// MeasureReport contains the results of evaluating a Measure.
type MeasureReport struct {
	base
	Identifier []Identifier `json:"identifier,omitempty"`
	Status     string       `json:"status,omitempty"`
	Type       string       `json:"type,omitempty"`
	Measure    string       `json:"measure,omitempty"`
	Subject    *Reference   `json:"subject,omitempty"`
	Date       string       `json:"date,omitempty"`
	Reporter   *Reference   `json:"reporter,omitempty"`
	Period     *Period      `json:"period,omitempty"`
}

// Library is a shareable collection of knowledge artifacts.
type Library struct {
	base
	URL         string           `json:"url,omitempty"`
	Identifier  []Identifier     `json:"identifier,omitempty"`
	Name        string           `json:"name,omitempty"`
	Title       string           `json:"title,omitempty"`
	Status      string           `json:"status,omitempty"`
	Type        *CodeableConcept `json:"type,omitempty"`
	Date        string           `json:"date,omitempty"`
	Publisher   string           `json:"publisher,omitempty"`
	Description string           `json:"description,omitempty"`
}

// Evidence captures evidence for a medical claim.
type Evidence struct {
	base
	URL         string       `json:"url,omitempty"`
	Identifier  []Identifier `json:"identifier,omitempty"`
	Name        string       `json:"name,omitempty"`
	Title       string       `json:"title,omitempty"`
	Status      string       `json:"status,omitempty"`
	Date        string       `json:"date,omitempty"`
	Publisher   string       `json:"publisher,omitempty"`
	Description string       `json:"description,omitempty"`
}

// EvidenceVariable describes a characteristic for evidence.
type EvidenceVariable struct {
	base
	URL         string       `json:"url,omitempty"`
	Identifier  []Identifier `json:"identifier,omitempty"`
	Name        string       `json:"name,omitempty"`
	Title       string       `json:"title,omitempty"`
	Status      string       `json:"status,omitempty"`
	Date        string       `json:"date,omitempty"`
	Publisher   string       `json:"publisher,omitempty"`
	Description string       `json:"description,omitempty"`
}

// RiskEvidenceSynthesis summarizes risk evidence.
type RiskEvidenceSynthesis struct {
	base
	URL         string       `json:"url,omitempty"`
	Identifier  []Identifier `json:"identifier,omitempty"`
	Name        string       `json:"name,omitempty"`
	Title       string       `json:"title,omitempty"`
	Status      string       `json:"status,omitempty"`
	Date        string       `json:"date,omitempty"`
	Publisher   string       `json:"publisher,omitempty"`
	Description string       `json:"description,omitempty"`
}

// EffectEvidenceSynthesis summarizes effect evidence.
type EffectEvidenceSynthesis struct {
	base
	URL         string       `json:"url,omitempty"`
	Identifier  []Identifier `json:"identifier,omitempty"`
	Name        string       `json:"name,omitempty"`
	Title       string       `json:"title,omitempty"`
	Status      string       `json:"status,omitempty"`
	Date        string       `json:"date,omitempty"`
	Publisher   string       `json:"publisher,omitempty"`
	Description string       `json:"description,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Additional clinical
// ──────────────────────────────────────────────────────────────────────────────

// Endpoint describes a technical connectivity point for a service.
type Endpoint struct {
	base
	Identifier           []Identifier      `json:"identifier,omitempty"`
	Status               string            `json:"status,omitempty"`
	ConnectionType       *Coding           `json:"connectionType,omitempty"`
	Name                 string            `json:"name,omitempty"`
	ManagingOrganization *Reference        `json:"managingOrganization,omitempty"`
	PayloadType          []CodeableConcept `json:"payloadType,omitempty"`
	Address              string            `json:"address,omitempty"`
}

// HealthcareService describes a service offered by an organization.
type HealthcareService struct {
	base
	Identifier []Identifier      `json:"identifier,omitempty"`
	Active     *bool             `json:"active,omitempty"`
	ProvidedBy *Reference        `json:"providedBy,omitempty"`
	Category   []CodeableConcept `json:"category,omitempty"`
	Type       []CodeableConcept `json:"type,omitempty"`
	Specialty  []CodeableConcept `json:"specialty,omitempty"`
	Location   []Reference       `json:"location,omitempty"`
	Name       string            `json:"name,omitempty"`
	Telecom    []ContactPoint    `json:"telecom,omitempty"`
}

// Group defines a set of entities.
type Group struct {
	base
	Identifier     []Identifier     `json:"identifier,omitempty"`
	Active         *bool            `json:"active,omitempty"`
	Type           string           `json:"type,omitempty"`
	Actual         bool             `json:"actual,omitempty"`
	Code           *CodeableConcept `json:"code,omitempty"`
	Name           string           `json:"name,omitempty"`
	Quantity       int              `json:"quantity,omitempty"`
	ManagingEntity *Reference       `json:"managingEntity,omitempty"`
}

// Person links multiple patient records.
type Person struct {
	base
	Identifier []Identifier   `json:"identifier,omitempty"`
	Name       []HumanName    `json:"name,omitempty"`
	Telecom    []ContactPoint `json:"telecom,omitempty"`
	Gender     string         `json:"gender,omitempty"`
	BirthDate  string         `json:"birthDate,omitempty"`
	Address    []Address      `json:"address,omitempty"`
	Active     *bool          `json:"active,omitempty"`
}

// Linkage links resources that refer to the same thing.
type Linkage struct {
	base
	Active *bool         `json:"active,omitempty"`
	Author *Reference    `json:"author,omitempty"`
	Item   []LinkageItem `json:"item,omitempty"`
}

// LinkageItem is one item in a Linkage.
type LinkageItem struct {
	Type     string     `json:"type,omitempty"`
	Resource *Reference `json:"resource,omitempty"`
}

// Basic is a resource for concepts not yet defined.
type Basic struct {
	base
	Identifier []Identifier     `json:"identifier,omitempty"`
	Code       *CodeableConcept `json:"code,omitempty"`
	Subject    *Reference       `json:"subject,omitempty"`
	Created    string           `json:"created,omitempty"`
	Author     *Reference       `json:"author,omitempty"`
}

// DeviceMetric describes a measurement or setting from a device.
type DeviceMetric struct {
	base
	Identifier []Identifier     `json:"identifier,omitempty"`
	Type       *CodeableConcept `json:"type,omitempty"`
	Unit       *CodeableConcept `json:"unit,omitempty"`
	Source     *Reference       `json:"source,omitempty"`
	Parent     *Reference       `json:"parent,omitempty"`
	Category   string           `json:"category,omitempty"`
	Color      string           `json:"color,omitempty"`
}

// DeviceDefinition provides information about a type of device.
type DeviceDefinition struct {
	base
	Identifier   []Identifier     `json:"identifier,omitempty"`
	Manufacturer string           `json:"manufacturerString,omitempty"`
	DeviceName   []DeviceDefName  `json:"deviceName,omitempty"`
	ModelNumber  string           `json:"modelNumber,omitempty"`
	Type         *CodeableConcept `json:"type,omitempty"`
	Owner        *Reference       `json:"owner,omitempty"`
}

// DeviceDefName is a name for a DeviceDefinition.
type DeviceDefName struct {
	Name string `json:"name,omitempty"`
	Type string `json:"type,omitempty"`
}

// Substance identifies a chemical or material.
type Substance struct {
	base
	Identifier  []Identifier      `json:"identifier,omitempty"`
	Status      string            `json:"status,omitempty"`
	Category    []CodeableConcept `json:"category,omitempty"`
	Code        *CodeableConcept  `json:"code,omitempty"`
	Description string            `json:"description,omitempty"`
}

// SubstanceSpecification describes a substance in detail.
type SubstanceSpecification struct {
	base
	Identifier  *Identifier      `json:"identifier,omitempty"`
	Type        *CodeableConcept `json:"type,omitempty"`
	Status      *CodeableConcept `json:"status,omitempty"`
	Domain      *CodeableConcept `json:"domain,omitempty"`
	Description string           `json:"description,omitempty"`
	Comment     string           `json:"comment,omitempty"`
}

// MedicinalProduct represents a pharmaceutical product.
type MedicinalProduct struct {
	base
	Identifier                     []Identifier     `json:"identifier,omitempty"`
	Type                           *CodeableConcept `json:"type,omitempty"`
	Domain                         *Coding          `json:"domain,omitempty"`
	CombinedPharmaceuticalDoseForm *CodeableConcept `json:"combinedPharmaceuticalDoseForm,omitempty"`
	LegalStatusOfSupply            *CodeableConcept `json:"legalStatusOfSupply,omitempty"`
}

// MedicinalProductAuthorization grants marketing permission.
type MedicinalProductAuthorization struct {
	base
	Identifier               []Identifier      `json:"identifier,omitempty"`
	Subject                  *Reference        `json:"subject,omitempty"`
	Country                  []CodeableConcept `json:"country,omitempty"`
	Status                   *CodeableConcept  `json:"status,omitempty"`
	StatusDate               string            `json:"statusDate,omitempty"`
	DateOfFirstAuthorization string            `json:"dateOfFirstAuthorization,omitempty"`
}

// ──────────────────────────────────────────────────────────────────────────────
// Communication and documentation
// ──────────────────────────────────────────────────────────────────────────────

// CommunicationRequest asks for a communication to be made.
type CommunicationRequest struct {
	base
	Identifier []Identifier      `json:"identifier,omitempty"`
	Status     string            `json:"status,omitempty"`
	Category   []CodeableConcept `json:"category,omitempty"`
	Priority   string            `json:"priority,omitempty"`
	Subject    *Reference        `json:"subject,omitempty"`
	Encounter  *Reference        `json:"encounter,omitempty"`
	Requester  *Reference        `json:"requester,omitempty"`
	Recipient  []Reference       `json:"recipient,omitempty"`
	AuthoredOn string            `json:"authoredOn,omitempty"`
}

// VerificationResult validates information about a resource.
type VerificationResult struct {
	base
	Target         []Reference      `json:"target,omitempty"`
	Status         string           `json:"status,omitempty"`
	Need           *CodeableConcept `json:"need,omitempty"`
	StatusDate     string           `json:"statusDate,omitempty"`
	ValidationType *CodeableConcept `json:"validationType,omitempty"`
	Frequency      *Timing          `json:"frequency,omitempty"`
	LastPerformed  string           `json:"lastPerformed,omitempty"`
	NextScheduled  string           `json:"nextScheduled,omitempty"`
}

// OrganizationAffiliation links organizations in a network.
type OrganizationAffiliation struct {
	base
	Identifier                []Identifier      `json:"identifier,omitempty"`
	Active                    *bool             `json:"active,omitempty"`
	Period                    *Period           `json:"period,omitempty"`
	Organization              *Reference        `json:"organization,omitempty"`
	ParticipatingOrganization *Reference        `json:"participatingOrganization,omitempty"`
	Network                   []Reference       `json:"network,omitempty"`
	Code                      []CodeableConcept `json:"code,omitempty"`
	Specialty                 []CodeableConcept `json:"specialty,omitempty"`
	Location                  []Reference       `json:"location,omitempty"`
	HealthcareService         []Reference       `json:"healthcareService,omitempty"`
	Telecom                   []ContactPoint    `json:"telecom,omitempty"`
}

// Catalog is a document that is a catalog.
type Catalog struct {
	base
	Identifier []Identifier     `json:"identifier,omitempty"`
	Status     string           `json:"status,omitempty"`
	Type       *CodeableConcept `json:"type,omitempty"`
	Date       string           `json:"date,omitempty"`
	Title      string           `json:"title,omitempty"`
}

// TestScript defines automated tests for a FHIR implementation.
type TestScript struct {
	base
	URL         string      `json:"url,omitempty"`
	Identifier  *Identifier `json:"identifier,omitempty"`
	Name        string      `json:"name,omitempty"`
	Title       string      `json:"title,omitempty"`
	Status      string      `json:"status,omitempty"`
	Date        string      `json:"date,omitempty"`
	Publisher   string      `json:"publisher,omitempty"`
	Description string      `json:"description,omitempty"`
}

// TestReport describes the results of running a TestScript.
type TestReport struct {
	base
	Identifier *Identifier `json:"identifier,omitempty"`
	Name       string      `json:"name,omitempty"`
	Status     string      `json:"status,omitempty"`
	TestScript *Reference  `json:"testScript,omitempty"`
	Result     string      `json:"result,omitempty"`
	Score      float64     `json:"score,omitempty"`
	Tester     string      `json:"tester,omitempty"`
	Issued     string      `json:"issued,omitempty"`
}

// TerminologyCapabilities describes terminology server capabilities.
type TerminologyCapabilities struct {
	base
	URL         string `json:"url,omitempty"`
	Name        string `json:"name,omitempty"`
	Status      string `json:"status,omitempty"`
	Kind        string `json:"kind,omitempty"`
	Date        string `json:"date,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	Description string `json:"description,omitempty"`
}

// ExampleScenario illustrates FHIR workflow.
type ExampleScenario struct {
	base
	URL        string       `json:"url,omitempty"`
	Identifier []Identifier `json:"identifier,omitempty"`
	Name       string       `json:"name,omitempty"`
	Status     string       `json:"status,omitempty"`
	Date       string       `json:"date,omitempty"`
	Publisher  string       `json:"publisher,omitempty"`
}

// ObservationDefinition defines characteristics for an observation.
type ObservationDefinition struct {
	base
	Category            []CodeableConcept `json:"category,omitempty"`
	Code                *CodeableConcept  `json:"code,omitempty"`
	Identifier          []Identifier      `json:"identifier,omitempty"`
	PermittedDataType   []string          `json:"permittedDataType,omitempty"`
	PreferredReportName string            `json:"preferredReportName,omitempty"`
}

// SpecimenDefinition defines specimen collection requirements.
type SpecimenDefinition struct {
	base
	Identifier         *Identifier       `json:"identifier,omitempty"`
	TypeCollected      *CodeableConcept  `json:"typeCollected,omitempty"`
	PatientPreparation []CodeableConcept `json:"patientPreparation,omitempty"`
	TimeAspect         string            `json:"timeAspect,omitempty"`
	Collection         []CodeableConcept `json:"collection,omitempty"`
}

// Resource interface implementations.

func (s *StructureDefinition) ResourceTypeName() string { return "StructureDefinition" }
func (s *StructureDefinition) ResourceID() string       { return s.ID }
func (s *StructureDefinition) SetResourceID(id string) {
	s.ID = id
	s.ResourceType = "StructureDefinition"
}

func (c *CapabilityStatement) ResourceTypeName() string { return "CapabilityStatement" }
func (c *CapabilityStatement) ResourceID() string       { return c.ID }
func (c *CapabilityStatement) SetResourceID(id string) {
	c.ID = id
	c.ResourceType = "CapabilityStatement"
}

func (c *CodeSystem) ResourceTypeName() string { return "CodeSystem" }
func (c *CodeSystem) ResourceID() string       { return c.ID }
func (c *CodeSystem) SetResourceID(id string)  { c.ID = id; c.ResourceType = "CodeSystem" }

func (n *NamingSystem) ResourceTypeName() string { return "NamingSystem" }
func (n *NamingSystem) ResourceID() string       { return n.ID }
func (n *NamingSystem) SetResourceID(id string)  { n.ID = id; n.ResourceType = "NamingSystem" }

func (i *ImplementationGuide) ResourceTypeName() string { return "ImplementationGuide" }
func (i *ImplementationGuide) ResourceID() string       { return i.ID }
func (i *ImplementationGuide) SetResourceID(id string) {
	i.ID = id
	i.ResourceType = "ImplementationGuide"
}

func (s *SearchParameter) ResourceTypeName() string { return "SearchParameter" }
func (s *SearchParameter) ResourceID() string       { return s.ID }
func (s *SearchParameter) SetResourceID(id string)  { s.ID = id; s.ResourceType = "SearchParameter" }

func (o *OperationDefinition) ResourceTypeName() string { return "OperationDefinition" }
func (o *OperationDefinition) ResourceID() string       { return o.ID }
func (o *OperationDefinition) SetResourceID(id string) {
	o.ID = id
	o.ResourceType = "OperationDefinition"
}

func (c *CompartmentDefinition) ResourceTypeName() string { return "CompartmentDefinition" }
func (c *CompartmentDefinition) ResourceID() string       { return c.ID }
func (c *CompartmentDefinition) SetResourceID(id string) {
	c.ID = id
	c.ResourceType = "CompartmentDefinition"
}

func (g *GraphDefinition) ResourceTypeName() string { return "GraphDefinition" }
func (g *GraphDefinition) ResourceID() string       { return g.ID }
func (g *GraphDefinition) SetResourceID(id string)  { g.ID = id; g.ResourceType = "GraphDefinition" }

func (s *StructureMap) ResourceTypeName() string { return "StructureMap" }
func (s *StructureMap) ResourceID() string       { return s.ID }
func (s *StructureMap) SetResourceID(id string)  { s.ID = id; s.ResourceType = "StructureMap" }

func (m *MessageDefinition) ResourceTypeName() string { return "MessageDefinition" }
func (m *MessageDefinition) ResourceID() string       { return m.ID }
func (m *MessageDefinition) SetResourceID(id string)  { m.ID = id; m.ResourceType = "MessageDefinition" }

func (m *MessageHeader) ResourceTypeName() string { return "MessageHeader" }
func (m *MessageHeader) ResourceID() string       { return m.ID }
func (m *MessageHeader) SetResourceID(id string)  { m.ID = id; m.ResourceType = "MessageHeader" }

func (s *Subscription) ResourceTypeName() string { return "Subscription" }
func (s *Subscription) ResourceID() string       { return s.ID }
func (s *Subscription) SetResourceID(id string)  { s.ID = id; s.ResourceType = "Subscription" }

func (b *Binary) ResourceTypeName() string { return "Binary" }
func (b *Binary) ResourceID() string       { return b.ID }
func (b *Binary) SetResourceID(id string)  { b.ID = id; b.ResourceType = "Binary" }

func (a *AuditEvent) ResourceTypeName() string { return "AuditEvent" }
func (a *AuditEvent) ResourceID() string       { return a.ID }
func (a *AuditEvent) SetResourceID(id string)  { a.ID = id; a.ResourceType = "AuditEvent" }

func (a *AdverseEvent) ResourceTypeName() string { return "AdverseEvent" }
func (a *AdverseEvent) ResourceID() string       { return a.ID }
func (a *AdverseEvent) SetResourceID(id string)  { a.ID = id; a.ResourceType = "AdverseEvent" }

func (d *DetectedIssue) ResourceTypeName() string { return "DetectedIssue" }
func (d *DetectedIssue) ResourceID() string       { return d.ID }
func (d *DetectedIssue) SetResourceID(id string)  { d.ID = id; d.ResourceType = "DetectedIssue" }

func (c *ClinicalImpression) ResourceTypeName() string { return "ClinicalImpression" }
func (c *ClinicalImpression) ResourceID() string       { return c.ID }
func (c *ClinicalImpression) SetResourceID(id string) {
	c.ID = id
	c.ResourceType = "ClinicalImpression"
}

func (r *RiskAssessment) ResourceTypeName() string { return "RiskAssessment" }
func (r *RiskAssessment) ResourceID() string       { return r.ID }
func (r *RiskAssessment) SetResourceID(id string)  { r.ID = id; r.ResourceType = "RiskAssessment" }

func (b *BodyStructure) ResourceTypeName() string { return "BodyStructure" }
func (b *BodyStructure) ResourceID() string       { return b.ID }
func (b *BodyStructure) SetResourceID(id string)  { b.ID = id; b.ResourceType = "BodyStructure" }

func (f *Flag) ResourceTypeName() string { return "Flag" }
func (f *Flag) ResourceID() string       { return f.ID }
func (f *Flag) SetResourceID(id string)  { f.ID = id; f.ResourceType = "Flag" }

func (l *List) ResourceTypeName() string { return "List" }
func (l *List) ResourceID() string       { return l.ID }
func (l *List) SetResourceID(id string)  { l.ID = id; l.ResourceType = "List" }

func (e *EpisodeOfCare) ResourceTypeName() string { return "EpisodeOfCare" }
func (e *EpisodeOfCare) ResourceID() string       { return e.ID }
func (e *EpisodeOfCare) SetResourceID(id string)  { e.ID = id; e.ResourceType = "EpisodeOfCare" }

func (n *NutritionOrder) ResourceTypeName() string { return "NutritionOrder" }
func (n *NutritionOrder) ResourceID() string       { return n.ID }
func (n *NutritionOrder) SetResourceID(id string)  { n.ID = id; n.ResourceType = "NutritionOrder" }

func (v *VisionPrescription) ResourceTypeName() string { return "VisionPrescription" }
func (v *VisionPrescription) ResourceID() string       { return v.ID }
func (v *VisionPrescription) SetResourceID(id string) {
	v.ID = id
	v.ResourceType = "VisionPrescription"
}

func (d *DeviceRequest) ResourceTypeName() string { return "DeviceRequest" }
func (d *DeviceRequest) ResourceID() string       { return d.ID }
func (d *DeviceRequest) SetResourceID(id string)  { d.ID = id; d.ResourceType = "DeviceRequest" }

func (d *DeviceUseStatement) ResourceTypeName() string { return "DeviceUseStatement" }
func (d *DeviceUseStatement) ResourceID() string       { return d.ID }
func (d *DeviceUseStatement) SetResourceID(id string) {
	d.ID = id
	d.ResourceType = "DeviceUseStatement"
}

func (s *SupplyRequest) ResourceTypeName() string { return "SupplyRequest" }
func (s *SupplyRequest) ResourceID() string       { return s.ID }
func (s *SupplyRequest) SetResourceID(id string)  { s.ID = id; s.ResourceType = "SupplyRequest" }

func (s *SupplyDelivery) ResourceTypeName() string { return "SupplyDelivery" }
func (s *SupplyDelivery) ResourceID() string       { return s.ID }
func (s *SupplyDelivery) SetResourceID(id string)  { s.ID = id; s.ResourceType = "SupplyDelivery" }

func (r *RequestGroup) ResourceTypeName() string { return "RequestGroup" }
func (r *RequestGroup) ResourceID() string       { return r.ID }
func (r *RequestGroup) SetResourceID(id string)  { r.ID = id; r.ResourceType = "RequestGroup" }

func (g *GuidanceResponse) ResourceTypeName() string { return "GuidanceResponse" }
func (g *GuidanceResponse) ResourceID() string       { return g.ID }
func (g *GuidanceResponse) SetResourceID(id string)  { g.ID = id; g.ResourceType = "GuidanceResponse" }

func (i *ImagingStudy) ResourceTypeName() string { return "ImagingStudy" }
func (i *ImagingStudy) ResourceID() string       { return i.ID }
func (i *ImagingStudy) SetResourceID(id string)  { i.ID = id; i.ResourceType = "ImagingStudy" }

func (m *MolecularSequence) ResourceTypeName() string { return "MolecularSequence" }
func (m *MolecularSequence) ResourceID() string       { return m.ID }
func (m *MolecularSequence) SetResourceID(id string)  { m.ID = id; m.ResourceType = "MolecularSequence" }

func (m *MedicationKnowledge) ResourceTypeName() string { return "MedicationKnowledge" }
func (m *MedicationKnowledge) ResourceID() string       { return m.ID }
func (m *MedicationKnowledge) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicationKnowledge"
}

func (i *ImmunizationEvaluation) ResourceTypeName() string { return "ImmunizationEvaluation" }
func (i *ImmunizationEvaluation) ResourceID() string       { return i.ID }
func (i *ImmunizationEvaluation) SetResourceID(id string) {
	i.ID = id
	i.ResourceType = "ImmunizationEvaluation"
}

func (i *ImmunizationRecommendation) ResourceTypeName() string { return "ImmunizationRecommendation" }
func (i *ImmunizationRecommendation) ResourceID() string       { return i.ID }
func (i *ImmunizationRecommendation) SetResourceID(id string) {
	i.ID = id
	i.ResourceType = "ImmunizationRecommendation"
}

func (c *ClaimResponse) ResourceTypeName() string { return "ClaimResponse" }
func (c *ClaimResponse) ResourceID() string       { return c.ID }
func (c *ClaimResponse) SetResourceID(id string)  { c.ID = id; c.ResourceType = "ClaimResponse" }

func (c *CoverageEligibilityRequest) ResourceTypeName() string { return "CoverageEligibilityRequest" }
func (c *CoverageEligibilityRequest) ResourceID() string       { return c.ID }
func (c *CoverageEligibilityRequest) SetResourceID(id string) {
	c.ID = id
	c.ResourceType = "CoverageEligibilityRequest"
}

func (c *CoverageEligibilityResponse) ResourceTypeName() string {
	return "CoverageEligibilityResponse"
}
func (c *CoverageEligibilityResponse) ResourceID() string { return c.ID }
func (c *CoverageEligibilityResponse) SetResourceID(id string) {
	c.ID = id
	c.ResourceType = "CoverageEligibilityResponse"
}

func (e *EnrollmentRequest) ResourceTypeName() string { return "EnrollmentRequest" }
func (e *EnrollmentRequest) ResourceID() string       { return e.ID }
func (e *EnrollmentRequest) SetResourceID(id string)  { e.ID = id; e.ResourceType = "EnrollmentRequest" }

func (e *EnrollmentResponse) ResourceTypeName() string { return "EnrollmentResponse" }
func (e *EnrollmentResponse) ResourceID() string       { return e.ID }
func (e *EnrollmentResponse) SetResourceID(id string) {
	e.ID = id
	e.ResourceType = "EnrollmentResponse"
}

func (p *PaymentNotice) ResourceTypeName() string { return "PaymentNotice" }
func (p *PaymentNotice) ResourceID() string       { return p.ID }
func (p *PaymentNotice) SetResourceID(id string)  { p.ID = id; p.ResourceType = "PaymentNotice" }

func (p *PaymentReconciliation) ResourceTypeName() string { return "PaymentReconciliation" }
func (p *PaymentReconciliation) ResourceID() string       { return p.ID }
func (p *PaymentReconciliation) SetResourceID(id string) {
	p.ID = id
	p.ResourceType = "PaymentReconciliation"
}

func (a *Account) ResourceTypeName() string { return "Account" }
func (a *Account) ResourceID() string       { return a.ID }
func (a *Account) SetResourceID(id string)  { a.ID = id; a.ResourceType = "Account" }

func (c *ChargeItem) ResourceTypeName() string { return "ChargeItem" }
func (c *ChargeItem) ResourceID() string       { return c.ID }
func (c *ChargeItem) SetResourceID(id string)  { c.ID = id; c.ResourceType = "ChargeItem" }

func (c *ChargeItemDefinition) ResourceTypeName() string { return "ChargeItemDefinition" }
func (c *ChargeItemDefinition) ResourceID() string       { return c.ID }
func (c *ChargeItemDefinition) SetResourceID(id string) {
	c.ID = id
	c.ResourceType = "ChargeItemDefinition"
}

func (c *Contract) ResourceTypeName() string { return "Contract" }
func (c *Contract) ResourceID() string       { return c.ID }
func (c *Contract) SetResourceID(id string)  { c.ID = id; c.ResourceType = "Contract" }

func (i *InsurancePlan) ResourceTypeName() string { return "InsurancePlan" }
func (i *InsurancePlan) ResourceID() string       { return i.ID }
func (i *InsurancePlan) SetResourceID(id string)  { i.ID = id; i.ResourceType = "InsurancePlan" }

func (i *Invoice) ResourceTypeName() string { return "Invoice" }
func (i *Invoice) ResourceID() string       { return i.ID }
func (i *Invoice) SetResourceID(id string)  { i.ID = id; i.ResourceType = "Invoice" }

func (s *Schedule) ResourceTypeName() string { return "Schedule" }
func (s *Schedule) ResourceID() string       { return s.ID }
func (s *Schedule) SetResourceID(id string)  { s.ID = id; s.ResourceType = "Schedule" }

func (s *Slot) ResourceTypeName() string { return "Slot" }
func (s *Slot) ResourceID() string       { return s.ID }
func (s *Slot) SetResourceID(id string)  { s.ID = id; s.ResourceType = "Slot" }

func (a *AppointmentResponse) ResourceTypeName() string { return "AppointmentResponse" }
func (a *AppointmentResponse) ResourceID() string       { return a.ID }
func (a *AppointmentResponse) SetResourceID(id string) {
	a.ID = id
	a.ResourceType = "AppointmentResponse"
}

func (p *PlanDefinition) ResourceTypeName() string { return "PlanDefinition" }
func (p *PlanDefinition) ResourceID() string       { return p.ID }
func (p *PlanDefinition) SetResourceID(id string)  { p.ID = id; p.ResourceType = "PlanDefinition" }

func (a *ActivityDefinition) ResourceTypeName() string { return "ActivityDefinition" }
func (a *ActivityDefinition) ResourceID() string       { return a.ID }
func (a *ActivityDefinition) SetResourceID(id string) {
	a.ID = id
	a.ResourceType = "ActivityDefinition"
}

func (e *EventDefinition) ResourceTypeName() string { return "EventDefinition" }
func (e *EventDefinition) ResourceID() string       { return e.ID }
func (e *EventDefinition) SetResourceID(id string)  { e.ID = id; e.ResourceType = "EventDefinition" }

func (q *Questionnaire) ResourceTypeName() string { return "Questionnaire" }
func (q *Questionnaire) ResourceID() string       { return q.ID }
func (q *Questionnaire) SetResourceID(id string)  { q.ID = id; q.ResourceType = "Questionnaire" }

func (r *ResearchStudy) ResourceTypeName() string { return "ResearchStudy" }
func (r *ResearchStudy) ResourceID() string       { return r.ID }
func (r *ResearchStudy) SetResourceID(id string)  { r.ID = id; r.ResourceType = "ResearchStudy" }

func (r *ResearchSubject) ResourceTypeName() string { return "ResearchSubject" }
func (r *ResearchSubject) ResourceID() string       { return r.ID }
func (r *ResearchSubject) SetResourceID(id string)  { r.ID = id; r.ResourceType = "ResearchSubject" }

func (m *Measure) ResourceTypeName() string { return "Measure" }
func (m *Measure) ResourceID() string       { return m.ID }
func (m *Measure) SetResourceID(id string)  { m.ID = id; m.ResourceType = "Measure" }

func (m *MeasureReport) ResourceTypeName() string { return "MeasureReport" }
func (m *MeasureReport) ResourceID() string       { return m.ID }
func (m *MeasureReport) SetResourceID(id string)  { m.ID = id; m.ResourceType = "MeasureReport" }

func (l *Library) ResourceTypeName() string { return "Library" }
func (l *Library) ResourceID() string       { return l.ID }
func (l *Library) SetResourceID(id string)  { l.ID = id; l.ResourceType = "Library" }

func (e *Evidence) ResourceTypeName() string { return "Evidence" }
func (e *Evidence) ResourceID() string       { return e.ID }
func (e *Evidence) SetResourceID(id string)  { e.ID = id; e.ResourceType = "Evidence" }

func (e *EvidenceVariable) ResourceTypeName() string { return "EvidenceVariable" }
func (e *EvidenceVariable) ResourceID() string       { return e.ID }
func (e *EvidenceVariable) SetResourceID(id string)  { e.ID = id; e.ResourceType = "EvidenceVariable" }

func (r *RiskEvidenceSynthesis) ResourceTypeName() string { return "RiskEvidenceSynthesis" }
func (r *RiskEvidenceSynthesis) ResourceID() string       { return r.ID }
func (r *RiskEvidenceSynthesis) SetResourceID(id string) {
	r.ID = id
	r.ResourceType = "RiskEvidenceSynthesis"
}

func (e *EffectEvidenceSynthesis) ResourceTypeName() string { return "EffectEvidenceSynthesis" }
func (e *EffectEvidenceSynthesis) ResourceID() string       { return e.ID }
func (e *EffectEvidenceSynthesis) SetResourceID(id string) {
	e.ID = id
	e.ResourceType = "EffectEvidenceSynthesis"
}

func (e *Endpoint) ResourceTypeName() string { return "Endpoint" }
func (e *Endpoint) ResourceID() string       { return e.ID }
func (e *Endpoint) SetResourceID(id string)  { e.ID = id; e.ResourceType = "Endpoint" }

func (h *HealthcareService) ResourceTypeName() string { return "HealthcareService" }
func (h *HealthcareService) ResourceID() string       { return h.ID }
func (h *HealthcareService) SetResourceID(id string)  { h.ID = id; h.ResourceType = "HealthcareService" }

func (g *Group) ResourceTypeName() string { return "Group" }
func (g *Group) ResourceID() string       { return g.ID }
func (g *Group) SetResourceID(id string)  { g.ID = id; g.ResourceType = "Group" }

func (p *Person) ResourceTypeName() string { return "Person" }
func (p *Person) ResourceID() string       { return p.ID }
func (p *Person) SetResourceID(id string)  { p.ID = id; p.ResourceType = "Person" }

func (l *Linkage) ResourceTypeName() string { return "Linkage" }
func (l *Linkage) ResourceID() string       { return l.ID }
func (l *Linkage) SetResourceID(id string)  { l.ID = id; l.ResourceType = "Linkage" }

func (b *Basic) ResourceTypeName() string { return "Basic" }
func (b *Basic) ResourceID() string       { return b.ID }
func (b *Basic) SetResourceID(id string)  { b.ID = id; b.ResourceType = "Basic" }

func (d *DeviceMetric) ResourceTypeName() string { return "DeviceMetric" }
func (d *DeviceMetric) ResourceID() string       { return d.ID }
func (d *DeviceMetric) SetResourceID(id string)  { d.ID = id; d.ResourceType = "DeviceMetric" }

func (d *DeviceDefinition) ResourceTypeName() string { return "DeviceDefinition" }
func (d *DeviceDefinition) ResourceID() string       { return d.ID }
func (d *DeviceDefinition) SetResourceID(id string)  { d.ID = id; d.ResourceType = "DeviceDefinition" }

func (s *Substance) ResourceTypeName() string { return "Substance" }
func (s *Substance) ResourceID() string       { return s.ID }
func (s *Substance) SetResourceID(id string)  { s.ID = id; s.ResourceType = "Substance" }

func (s *SubstanceSpecification) ResourceTypeName() string { return "SubstanceSpecification" }
func (s *SubstanceSpecification) ResourceID() string       { return s.ID }
func (s *SubstanceSpecification) SetResourceID(id string) {
	s.ID = id
	s.ResourceType = "SubstanceSpecification"
}

func (m *MedicinalProduct) ResourceTypeName() string { return "MedicinalProduct" }
func (m *MedicinalProduct) ResourceID() string       { return m.ID }
func (m *MedicinalProduct) SetResourceID(id string)  { m.ID = id; m.ResourceType = "MedicinalProduct" }

func (m *MedicinalProductAuthorization) ResourceTypeName() string {
	return "MedicinalProductAuthorization"
}
func (m *MedicinalProductAuthorization) ResourceID() string { return m.ID }
func (m *MedicinalProductAuthorization) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicinalProductAuthorization"
}

func (c *CommunicationRequest) ResourceTypeName() string { return "CommunicationRequest" }
func (c *CommunicationRequest) ResourceID() string       { return c.ID }
func (c *CommunicationRequest) SetResourceID(id string) {
	c.ID = id
	c.ResourceType = "CommunicationRequest"
}

func (v *VerificationResult) ResourceTypeName() string { return "VerificationResult" }
func (v *VerificationResult) ResourceID() string       { return v.ID }
func (v *VerificationResult) SetResourceID(id string) {
	v.ID = id
	v.ResourceType = "VerificationResult"
}

func (o *OrganizationAffiliation) ResourceTypeName() string { return "OrganizationAffiliation" }
func (o *OrganizationAffiliation) ResourceID() string       { return o.ID }
func (o *OrganizationAffiliation) SetResourceID(id string) {
	o.ID = id
	o.ResourceType = "OrganizationAffiliation"
}

func (c *Catalog) ResourceTypeName() string { return "CatalogEntry" }
func (c *Catalog) ResourceID() string       { return c.ID }
func (c *Catalog) SetResourceID(id string)  { c.ID = id; c.ResourceType = "CatalogEntry" }

func (t *TestScript) ResourceTypeName() string { return "TestScript" }
func (t *TestScript) ResourceID() string       { return t.ID }
func (t *TestScript) SetResourceID(id string)  { t.ID = id; t.ResourceType = "TestScript" }

func (t *TestReport) ResourceTypeName() string { return "TestReport" }
func (t *TestReport) ResourceID() string       { return t.ID }
func (t *TestReport) SetResourceID(id string)  { t.ID = id; t.ResourceType = "TestReport" }

func (t *TerminologyCapabilities) ResourceTypeName() string { return "TerminologyCapabilities" }
func (t *TerminologyCapabilities) ResourceID() string       { return t.ID }
func (t *TerminologyCapabilities) SetResourceID(id string) {
	t.ID = id
	t.ResourceType = "TerminologyCapabilities"
}

func (e *ExampleScenario) ResourceTypeName() string { return "ExampleScenario" }
func (e *ExampleScenario) ResourceID() string       { return e.ID }
func (e *ExampleScenario) SetResourceID(id string)  { e.ID = id; e.ResourceType = "ExampleScenario" }

func (o *ObservationDefinition) ResourceTypeName() string { return "ObservationDefinition" }
func (o *ObservationDefinition) ResourceID() string       { return o.ID }
func (o *ObservationDefinition) SetResourceID(id string) {
	o.ID = id
	o.ResourceType = "ObservationDefinition"
}

func (s *SpecimenDefinition) ResourceTypeName() string { return "SpecimenDefinition" }
func (s *SpecimenDefinition) ResourceID() string       { return s.ID }
func (s *SpecimenDefinition) SetResourceID(id string) {
	s.ID = id
	s.ResourceType = "SpecimenDefinition"
}
