package fhir

// Resources.
//
// The structs are R5-shaped, because R5 is the latest published release. Where R4
// differs the difference is applied when serialising, in json.go, rather than by
// keeping two parallel definitions of Patient that will eventually disagree.
//
// Only the fields that HL7 v2 messages actually populate are modelled. A struct
// covering all 1,200 elements of Patient would be mostly zero values and would
// hide which parts of the resource this software really produces.

// Resource is anything with a type and an id.
type Resource interface {
	// ResourceTypeName returns the FHIR resource type, such as "Patient".
	ResourceTypeName() string
	// ResourceID returns the logical id.
	ResourceID() string
	// SetResourceID assigns the logical id.
	SetResourceID(id string)
	// ResourceMeta returns the resource metadata, or nil if it has none.
	//
	// On the interface because meta.profile is a claim about the resource that anything validating or persisting it needs to read.
	// Without it, every caller wanting the profile list had to type-switch over every resource type - which applyProfile in the v2
	// converter did, and which is one more list to forget to extend.
	ResourceMeta() *Meta
	// SetResourceMeta replaces the resource metadata.
	SetResourceMeta(m *Meta)
}

// ResourceMeta returns the metadata every resource may carry.
func (b *base) ResourceMeta() *Meta { return b.Meta }

// SetResourceMeta replaces the metadata.
func (b *base) SetResourceMeta(m *Meta) { b.Meta = m }

// base carries the fields every resource has.
type base struct {
	ResourceType string     `json:"resourceType"`
	ID           string     `json:"id,omitempty"`
	Meta         *Meta      `json:"meta,omitempty"`
	Text         *Narrative `json:"text,omitempty"`
}

// Patient is a person receiving care.
type Patient struct {
	base

	Identifier    []Identifier           `json:"identifier,omitempty"`
	Active        *bool                  `json:"active,omitempty"`
	Name          []HumanName            `json:"name,omitempty"`
	Telecom       []ContactPoint         `json:"telecom,omitempty"`
	Gender        string                 `json:"gender,omitempty"`
	BirthDate     string                 `json:"birthDate,omitempty"`
	Address       []Address              `json:"address,omitempty"`
	MaritalStatus *CodeableConcept       `json:"maritalStatus,omitempty"`
	Communication []PatientCommunication `json:"communication,omitempty"`
	Contact       []PatientContact       `json:"contact,omitempty"`
	ManagingOrg   *Reference             `json:"managingOrganization,omitempty"`
	Extension     []Extension            `json:"extension,omitempty"`

	// DeceasedBoolean and DeceasedDateTime are the two halves of deceased[x].
	// Only one may be set; the marshaller enforces that rather than emitting an
	// invalid resource.
	DeceasedBoolean  *bool  `json:"deceasedBoolean,omitempty"`
	DeceasedDateTime string `json:"deceasedDateTime,omitempty"`

	// MultipleBirthBoolean and MultipleBirthInteger are multipleBirth[x].
	MultipleBirthBoolean *bool `json:"multipleBirthBoolean,omitempty"`
	MultipleBirthInteger *int  `json:"multipleBirthInteger,omitempty"`
}

// PatientCommunication is a language a patient can use.
type PatientCommunication struct {
	Language  *CodeableConcept `json:"language,omitempty"`
	Preferred *bool            `json:"preferred,omitempty"`
}

// PatientContact is a next of kin or emergency contact.
type PatientContact struct {
	Relationship []CodeableConcept `json:"relationship,omitempty"`
	Name         *HumanName        `json:"name,omitempty"`
	Telecom      []ContactPoint    `json:"telecom,omitempty"`
	Address      *Address          `json:"address,omitempty"`
	Organization *Reference        `json:"organization,omitempty"`
	Period       *Period           `json:"period,omitempty"`
}

// ResourceTypeName implements Resource.
func (p *Patient) ResourceTypeName() string { return "Patient" }

// ResourceID implements Resource.
func (p *Patient) ResourceID() string { return p.ID }

// SetResourceID implements Resource.
func (p *Patient) SetResourceID(id string) { p.ID = id; p.ResourceType = "Patient" }

// Encounter is an interaction between a patient and a provider: an admission, a
// clinic visit, an emergency attendance.
type Encounter struct {
	base

	Identifier []Identifier `json:"identifier,omitempty"`

	// Status uses the R5 value set. The marshaller maps it to the R4 set when
	// producing R4, because the two are not the same list.
	Status string `json:"status,omitempty"`

	// Class is a CodeableConcept in R5 and a Coding in R4. Held in the R5 form
	// and converted when serialising.
	Class []CodeableConcept `json:"class,omitempty"`

	Type        []CodeableConcept   `json:"type,omitempty"`
	ServiceType []CodeableReference `json:"serviceType,omitempty"`
	Priority    *CodeableConcept    `json:"priority,omitempty"`
	Subject     *Reference          `json:"subject,omitempty"`

	// ActualPeriod is R5's name; R4 calls it period.
	ActualPeriod *Period `json:"actualPeriod,omitempty"`

	Participant []EncounterParticipant `json:"participant,omitempty"`
	Location    []EncounterLocation    `json:"location,omitempty"`

	// CareTeam and other R5 additions are omitted until something populates them.
	ServiceProvider *Reference        `json:"serviceProvider,omitempty"`
	ReasonCode      []CodeableConcept `json:"-"`
	Extension       []Extension       `json:"extension,omitempty"`

	// Hospitalization is R4's name; R5 calls it admission.
	Admission *EncounterAdmission `json:"admission,omitempty"`
}

// EncounterParticipant is a person involved in an encounter.
type EncounterParticipant struct {
	Type   []CodeableConcept `json:"type,omitempty"`
	Period *Period           `json:"period,omitempty"`
	Actor  *Reference        `json:"actor,omitempty"`
}

// EncounterLocation is where an encounter took place.
type EncounterLocation struct {
	Location *Reference       `json:"location,omitempty"`
	Status   string           `json:"status,omitempty"`
	Form     *CodeableConcept `json:"form,omitempty"`
	Period   *Period          `json:"period,omitempty"`
}

// EncounterAdmission holds admission and discharge details.
type EncounterAdmission struct {
	PreAdmitIdentifier   *Identifier      `json:"preAdmitIdentifier,omitempty"`
	Origin               *Reference       `json:"origin,omitempty"`
	AdmitSource          *CodeableConcept `json:"admitSource,omitempty"`
	ReAdmission          *CodeableConcept `json:"reAdmission,omitempty"`
	Destination          *Reference       `json:"destination,omitempty"`
	DischargeDisposition *CodeableConcept `json:"dischargeDisposition,omitempty"`
}

// ResourceTypeName implements Resource.
func (e *Encounter) ResourceTypeName() string { return "Encounter" }

// ResourceID implements Resource.
func (e *Encounter) ResourceID() string { return e.ID }

// SetResourceID implements Resource.
func (e *Encounter) SetResourceID(id string) { e.ID = id; e.ResourceType = "Encounter" }

// Observation is a measurement or finding: a lab result, a vital sign.
type Observation struct {
	base

	Identifier []Identifier      `json:"identifier,omitempty"`
	Status     string            `json:"status,omitempty"`
	Category   []CodeableConcept `json:"category,omitempty"`
	Code       *CodeableConcept  `json:"code,omitempty"`
	Subject    *Reference        `json:"subject,omitempty"`
	Encounter  *Reference        `json:"encounter,omitempty"`

	// effective[x].
	EffectiveDateTime string  `json:"effectiveDateTime,omitempty"`
	EffectivePeriod   *Period `json:"effectivePeriod,omitempty"`

	Issued    string      `json:"issued,omitempty"`
	Performer []Reference `json:"performer,omitempty"`

	// value[x]. At most one may be set, which the marshaller enforces.
	ValueQuantity        *Quantity        `json:"valueQuantity,omitempty"`
	ValueCodeableConcept *CodeableConcept `json:"valueCodeableConcept,omitempty"`
	ValueString          *string          `json:"valueString,omitempty"`
	ValueBoolean         *bool            `json:"valueBoolean,omitempty"`
	ValueInteger         *int             `json:"valueInteger,omitempty"`
	ValueRange           *Range           `json:"valueRange,omitempty"`
	ValueRatio           *Ratio           `json:"valueRatio,omitempty"`
	ValueDateTime        string           `json:"valueDateTime,omitempty"`
	ValuePeriod          *Period          `json:"valuePeriod,omitempty"`

	// DataAbsentReason explains a missing value. A result that arrived without a
	// value is different from one nobody sent, and saying which is the point.
	DataAbsentReason *CodeableConcept       `json:"dataAbsentReason,omitempty"`
	Interpretation   []CodeableConcept      `json:"interpretation,omitempty"`
	Note             []Annotation           `json:"note,omitempty"`
	BodySite         *CodeableConcept       `json:"bodySite,omitempty"`
	Method           *CodeableConcept       `json:"method,omitempty"`
	Specimen         *Reference             `json:"specimen,omitempty"`
	ReferenceRange   []ReferenceRange       `json:"referenceRange,omitempty"`
	HasMember        []Reference            `json:"hasMember,omitempty"`
	DerivedFrom      []Reference            `json:"derivedFrom,omitempty"`
	Component        []ObservationComponent `json:"component,omitempty"`
	Extension        []Extension            `json:"extension,omitempty"`
}

// ReferenceRange is the normal range for an observation.
type ReferenceRange struct {
	Low       *Quantity         `json:"low,omitempty"`
	High      *Quantity         `json:"high,omitempty"`
	Type      *CodeableConcept  `json:"type,omitempty"`
	AppliesTo []CodeableConcept `json:"appliesTo,omitempty"`
	Age       *Range            `json:"age,omitempty"`
	Text      string            `json:"text,omitempty"`
}

// ObservationComponent is a part of a multi-part observation, such as the two
// halves of a blood pressure.
type ObservationComponent struct {
	Code                 *CodeableConcept  `json:"code,omitempty"`
	ValueQuantity        *Quantity         `json:"valueQuantity,omitempty"`
	ValueCodeableConcept *CodeableConcept  `json:"valueCodeableConcept,omitempty"`
	ValueString          *string           `json:"valueString,omitempty"`
	DataAbsentReason     *CodeableConcept  `json:"dataAbsentReason,omitempty"`
	Interpretation       []CodeableConcept `json:"interpretation,omitempty"`
	ReferenceRange       []ReferenceRange  `json:"referenceRange,omitempty"`
}

// ResourceTypeName implements Resource.
func (o *Observation) ResourceTypeName() string { return "Observation" }

// ResourceID implements Resource.
func (o *Observation) ResourceID() string { return o.ID }

// SetResourceID implements Resource.
func (o *Observation) SetResourceID(id string) { o.ID = id; o.ResourceType = "Observation" }

// DiagnosticReport groups observations into a report, which is what an ORU
// message actually represents.
type DiagnosticReport struct {
	base

	Identifier []Identifier      `json:"identifier,omitempty"`
	BasedOn    []Reference       `json:"basedOn,omitempty"`
	Status     string            `json:"status,omitempty"`
	Category   []CodeableConcept `json:"category,omitempty"`
	Code       *CodeableConcept  `json:"code,omitempty"`
	Subject    *Reference        `json:"subject,omitempty"`
	Encounter  *Reference        `json:"encounter,omitempty"`

	EffectiveDateTime string  `json:"effectiveDateTime,omitempty"`
	EffectivePeriod   *Period `json:"effectivePeriod,omitempty"`

	Issued             string       `json:"issued,omitempty"`
	Performer          []Reference  `json:"performer,omitempty"`
	ResultsInterpreter []Reference  `json:"resultsInterpreter,omitempty"`
	Specimen           []Reference  `json:"specimen,omitempty"`
	Result             []Reference  `json:"result,omitempty"`
	Conclusion         string       `json:"conclusion,omitempty"`
	PresentedForm      []Attachment `json:"presentedForm,omitempty"`
	Extension          []Extension  `json:"extension,omitempty"`
}

// ResourceTypeName implements Resource.
func (d *DiagnosticReport) ResourceTypeName() string { return "DiagnosticReport" }

// ResourceID implements Resource.
func (d *DiagnosticReport) ResourceID() string { return d.ID }

// SetResourceID implements Resource.
func (d *DiagnosticReport) SetResourceID(id string) {
	d.ID = id
	d.ResourceType = "DiagnosticReport"
}

// Practitioner is a clinician.
type Practitioner struct {
	base

	Identifier []Identifier   `json:"identifier,omitempty"`
	Active     *bool          `json:"active,omitempty"`
	Name       []HumanName    `json:"name,omitempty"`
	Telecom    []ContactPoint `json:"telecom,omitempty"`
	Gender     string         `json:"gender,omitempty"`
}

// ResourceTypeName implements Resource.
func (p *Practitioner) ResourceTypeName() string { return "Practitioner" }

// ResourceID implements Resource.
func (p *Practitioner) ResourceID() string { return p.ID }

// SetResourceID implements Resource.
func (p *Practitioner) SetResourceID(id string) { p.ID = id; p.ResourceType = "Practitioner" }

// Organization is a hospital, laboratory or department.
type Organization struct {
	base

	Identifier []Identifier      `json:"identifier,omitempty"`
	Active     *bool             `json:"active,omitempty"`
	Type       []CodeableConcept `json:"type,omitempty"`
	Name       string            `json:"name,omitempty"`
	Alias      []string          `json:"alias,omitempty"`
	Telecom    []ContactPoint    `json:"-"`
	Address    []Address         `json:"address,omitempty"`
}

// ResourceTypeName implements Resource.
func (o *Organization) ResourceTypeName() string { return "Organization" }

// ResourceID implements Resource.
func (o *Organization) ResourceID() string { return o.ID }

// SetResourceID implements Resource.
func (o *Organization) SetResourceID(id string) { o.ID = id; o.ResourceType = "Organization" }

// Location is a ward, bed, room or building.
type Location struct {
	base

	Identifier   []Identifier      `json:"identifier,omitempty"`
	Status       string            `json:"status,omitempty"`
	Name         string            `json:"name,omitempty"`
	Description  string            `json:"description,omitempty"`
	Mode         string            `json:"mode,omitempty"`
	Type         []CodeableConcept `json:"type,omitempty"`
	Address      *Address          `json:"address,omitempty"`
	PhysicalType *CodeableConcept  `json:"physicalType,omitempty"`
	PartOf       *Reference        `json:"partOf,omitempty"`
}

// ResourceTypeName implements Resource.
func (l *Location) ResourceTypeName() string { return "Location" }

// ResourceID implements Resource.
func (l *Location) ResourceID() string { return l.ID }

// SetResourceID implements Resource.
func (l *Location) SetResourceID(id string) { l.ID = id; l.ResourceType = "Location" }

// Specimen is the sample a result was measured on.
type Specimen struct {
	base

	Identifier          []Identifier        `json:"identifier,omitempty"`
	AccessionIdentifier *Identifier         `json:"accessionIdentifier,omitempty"`
	Status              string              `json:"status,omitempty"`
	Type                *CodeableConcept    `json:"type,omitempty"`
	Subject             *Reference          `json:"subject,omitempty"`
	ReceivedTime        string              `json:"receivedTime,omitempty"`
	Collection          *SpecimenCollection `json:"collection,omitempty"`
}

// SpecimenCollection describes how and when a specimen was taken.
type SpecimenCollection struct {
	Collector         *Reference         `json:"collector,omitempty"`
	CollectedDateTime string             `json:"collectedDateTime,omitempty"`
	CollectedPeriod   *Period            `json:"collectedPeriod,omitempty"`
	Quantity          *Quantity          `json:"quantity,omitempty"`
	Method            *CodeableConcept   `json:"method,omitempty"`
	BodySite          *CodeableReference `json:"bodySite,omitempty"`
}

// ResourceTypeName implements Resource.
func (s *Specimen) ResourceTypeName() string { return "Specimen" }

// ResourceID implements Resource.
func (s *Specimen) ResourceID() string { return s.ID }

// SetResourceID implements Resource.
func (s *Specimen) SetResourceID(id string) { s.ID = id; s.ResourceType = "Specimen" }

// ServiceRequest is an order, which is what an ORM or OML message carries.
type ServiceRequest struct {
	base

	Identifier []Identifier       `json:"identifier,omitempty"`
	Status     string             `json:"status,omitempty"`
	Intent     string             `json:"intent,omitempty"`
	Category   []CodeableConcept  `json:"category,omitempty"`
	Priority   string             `json:"priority,omitempty"`
	Code       *CodeableReference `json:"code,omitempty"`
	Subject    *Reference         `json:"subject,omitempty"`
	Encounter  *Reference         `json:"encounter,omitempty"`
	AuthoredOn string             `json:"authoredOn,omitempty"`
	Requester  *Reference         `json:"requester,omitempty"`
	Specimen   []Reference        `json:"specimen,omitempty"`
	Note       []Annotation       `json:"note,omitempty"`
}

// ResourceTypeName implements Resource.
func (s *ServiceRequest) ResourceTypeName() string { return "ServiceRequest" }

// ResourceID implements Resource.
func (s *ServiceRequest) ResourceID() string { return s.ID }

// SetResourceID implements Resource.
func (s *ServiceRequest) SetResourceID(id string) { s.ID = id; s.ResourceType = "ServiceRequest" }

// Bundle groups resources. A transaction bundle is how a v2 message becomes a
// single atomic write to a FHIR server: either every resource lands or none does,
// which matters when a patient and their encounter arrive together.
type Bundle struct {
	base

	Identifier *Identifier   `json:"identifier,omitempty"`
	Type       string        `json:"type"`
	Timestamp  string        `json:"timestamp,omitempty"`
	Total      *int          `json:"total,omitempty"`
	Link       []BundleLink  `json:"link,omitempty"`
	Entry      []BundleEntry `json:"entry,omitempty"`
}

// BundleLink is a navigation link, used for search paging.
type BundleLink struct {
	Relation string `json:"relation"`
	URL      string `json:"url"`
}

// BundleEntry is one resource in a bundle plus how to apply it.
type BundleEntry struct {
	FullURL  string             `json:"fullUrl,omitempty"`
	Resource Resource           `json:"resource,omitempty"`
	Search   *BundleEntrySearch `json:"search,omitempty"`
	Request  *BundleRequest     `json:"request,omitempty"`
	Response *BundleResponse    `json:"response,omitempty"`
}

// BundleEntrySearch describes why an entry is in a search result.
type BundleEntrySearch struct {
	Mode  string   `json:"mode,omitempty"`
	Score *float64 `json:"score,omitempty"`
}

// BundleRequest is the operation to perform for a transaction entry.
type BundleRequest struct {
	Method      string `json:"method"`
	URL         string `json:"url"`
	IfNoneExist string `json:"ifNoneExist,omitempty"`
	IfMatch     string `json:"ifMatch,omitempty"`
}

// BundleResponse is the outcome of a transaction entry.
type BundleResponse struct {
	Status   string `json:"status"`
	Location string `json:"location,omitempty"`
	Etag     string `json:"etag,omitempty"`

	// LastModified is when the version in this entry was written.
	//
	// Carried for history bundles, where it is the column somebody actually reads: an audit trail whose entries have
	// version numbers and no times answers "in what order" and not "when", and "when" is the question.
	LastModified string `json:"lastModified,omitempty"`
}

// ResourceTypeName implements Resource.
func (b *Bundle) ResourceTypeName() string { return "Bundle" }

// ResourceID implements Resource.
func (b *Bundle) ResourceID() string { return b.ID }

// SetResourceID implements Resource.
func (b *Bundle) SetResourceID(id string) { b.ID = id; b.ResourceType = "Bundle" }

// Bundle types.
const (
	BundleTransaction         = "transaction"
	BundleTransactionResponse = "transaction-response"
	BundleBatch               = "batch"
	BundleCollection          = "collection"
	BundleSearchset           = "searchset"
	BundleHistory             = "history"
	BundleMessage             = "message"
	BundleDocument            = "document"
)

// OperationOutcome reports errors and warnings, and is what a FHIR server returns
// when something is wrong.
type OperationOutcome struct {
	base

	Issue []OperationOutcomeIssue `json:"issue"`
}

// OperationOutcomeIssue is one problem.
type OperationOutcomeIssue struct {
	Severity    string           `json:"severity"`
	Code        string           `json:"code"`
	Details     *CodeableConcept `json:"details,omitempty"`
	Diagnostics string           `json:"diagnostics,omitempty"`
	Expression  []string         `json:"expression,omitempty"`
}

// ResourceTypeName implements Resource.
func (o *OperationOutcome) ResourceTypeName() string { return "OperationOutcome" }

// ResourceID implements Resource.
func (o *OperationOutcome) ResourceID() string { return o.ID }

// SetResourceID implements Resource.
func (o *OperationOutcome) SetResourceID(id string) {
	o.ID = id
	o.ResourceType = "OperationOutcome"
}

// Issue severities and codes used by this package.
const (
	SeverityFatal       = "fatal"
	SeverityError       = "error"
	SeverityWarning     = "warning"
	SeverityInformation = "information"
)

// NewOperationOutcome builds an outcome from one issue.
func NewOperationOutcome(severity, code, diagnostics string) *OperationOutcome {
	o := &OperationOutcome{
		Issue: []OperationOutcomeIssue{{
			Severity: severity, Code: code, Diagnostics: diagnostics,
		}},
	}
	o.ResourceType = "OperationOutcome"
	return o
}
