package fhir

// The nineteen R4 resource types that completed the set.
//
// # Why they were missing, and why that was defensible
//
// Coverage here grew by need: a type was added when a channel had to carry one. That is the right order to build in, and it left
// nineteen of R4's hundred and forty-six unimplemented - almost all of them from two families that a hospital integration engine
// genuinely never sees. The MedicinalProduct family is drug *regulatory dossier* data, submitted by manufacturers to agencies
// like the EMA. The Substance family describes chemical and biological substances at the level of nucleic acid sequences and
// polymer geometry. Neither travels in clinical traffic.
//
// # Why implement them anyway
//
// Because "we do not support that resource type" and "that resource type is not clinically relevant" are different statements,
// and only the server can make the first one. Until now a POST of a MedicinalProductPackaged was refused by the router with no
// way for a caller to tell a deliberate scope decision from a gap. Completeness removes a class of question rather than a
// feature: there is no longer any R4 resource type Perfuse cannot accept, store and return.
//
// It also makes the guard in resources_r4complete_test.go possible, and that guard is the durable part of this work. A test that
// says "every R4 resource type has a constructor" only has meaning once the answer is yes; before that it is a list of excuses.
//
// # Scope of the modelling, stated plainly
//
// Top-level fields, with the nested backbone elements that carry identity or a code, matching how the rest of this package
// models resources - Linkage has three fields, not the specification's full tree. Unmodelled elements survive a round trip
// because the JSON layer preserves unknown members; they are simply not addressable by name. For these nineteen that is the
// right trade, and for a resource that later turns out to matter it is a matter of adding fields rather than adding a type.
//
// Three of the nineteen are not from those families and are worth naming, because they can appear in real traffic:
// CoverageEligibilityResponse completes the eligibility request/response pair, DocumentManifest groups documents in an exchange,
// and BiologicallyDerivedProduct covers blood, tissue and cellular products.

// ─────────────────────────────────────────────────────────────────────────────
// Clinically reachable
// ─────────────────────────────────────────────────────────────────────────────

// BiologicallyDerivedProduct is a material substance from a biological entity, such as blood, tissue or a cellular product.
type BiologicallyDerivedProduct struct {
	base
	Identifier      []Identifier                          `json:"identifier,omitempty"`
	ProductCategory string                                `json:"productCategory,omitempty"`
	ProductCode     *CodeableConcept                      `json:"productCode,omitempty"`
	Status          string                                `json:"status,omitempty"`
	Request         []Reference                           `json:"request,omitempty"`
	Quantity        *int                                  `json:"quantity,omitempty"`
	Parent          []Reference                           `json:"parent,omitempty"`
	Collection      *BiologicallyDerivedProductCollection `json:"collection,omitempty"`
	Processing      []BiologicallyDerivedProductProcessed `json:"processing,omitempty"`
	Manipulation    *BiologicallyDerivedProductManipulate `json:"manipulation,omitempty"`
	Storage         []BiologicallyDerivedProductStorage   `json:"storage,omitempty"`
}

// BiologicallyDerivedProductCollection records who collected the product and when.
type BiologicallyDerivedProductCollection struct {
	Collector         *Reference `json:"collector,omitempty"`
	Source            *Reference `json:"source,omitempty"`
	CollectedDateTime string     `json:"collectedDateTime,omitempty"`
	CollectedPeriod   *Period    `json:"collectedPeriod,omitempty"`
}

// BiologicallyDerivedProductProcessed is one processing step applied to the product.
type BiologicallyDerivedProductProcessed struct {
	Description  string           `json:"description,omitempty"`
	Procedure    *CodeableConcept `json:"procedure,omitempty"`
	Additive     *Reference       `json:"additive,omitempty"`
	TimeDateTime string           `json:"timeDateTime,omitempty"`
	TimePeriod   *Period          `json:"timePeriod,omitempty"`
}

// BiologicallyDerivedProductManipulate is any manipulation performed before storage or use.
type BiologicallyDerivedProductManipulate struct {
	Description  string  `json:"description,omitempty"`
	TimeDateTime string  `json:"timeDateTime,omitempty"`
	TimePeriod   *Period `json:"timePeriod,omitempty"`
}

// BiologicallyDerivedProductStorage records storage conditions, which are what make a product usable or not.
type BiologicallyDerivedProductStorage struct {
	Description string   `json:"description,omitempty"`
	Temperature *float64 `json:"temperature,omitempty"`
	Scale       string   `json:"scale,omitempty"`
	Duration    *Period  `json:"duration,omitempty"`
}

// DocumentManifest is a set of documents exchanged together.
type DocumentManifest struct {
	base
	MasterIdentifier *Identifier               `json:"masterIdentifier,omitempty"`
	Identifier       []Identifier              `json:"identifier,omitempty"`
	Status           string                    `json:"status,omitempty"`
	Type             *CodeableConcept          `json:"type,omitempty"`
	Subject          *Reference                `json:"subject,omitempty"`
	Created          string                    `json:"created,omitempty"`
	Author           []Reference               `json:"author,omitempty"`
	Recipient        []Reference               `json:"recipient,omitempty"`
	Source           string                    `json:"source,omitempty"`
	Description      string                    `json:"description,omitempty"`
	Content          []Reference               `json:"content,omitempty"`
	Related          []DocumentManifestRelated `json:"related,omitempty"`
}

// DocumentManifestRelated links the manifest to another identifier or resource.
type DocumentManifestRelated struct {
	Identifier *Identifier `json:"identifier,omitempty"`
	Ref        *Reference  `json:"ref,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Medicinal product family — regulatory dossier data
// ─────────────────────────────────────────────────────────────────────────────

// MedicinalProductContraindication records a disease or condition the product must not be used for.
type MedicinalProductContraindication struct {
	base
	Subject               []Reference       `json:"subject,omitempty"`
	Disease               *CodeableConcept  `json:"disease,omitempty"`
	DiseaseStatus         *CodeableConcept  `json:"diseaseStatus,omitempty"`
	Comorbidity           []CodeableConcept `json:"comorbidity,omitempty"`
	TherapeuticIndication []Reference       `json:"therapeuticIndication,omitempty"`
}

// MedicinalProductIndication records what the product is authorised to treat.
type MedicinalProductIndication struct {
	base
	Subject                 []Reference       `json:"subject,omitempty"`
	DiseaseSymptomProcedure *CodeableConcept  `json:"diseaseSymptomProcedure,omitempty"`
	DiseaseStatus           *CodeableConcept  `json:"diseaseStatus,omitempty"`
	Comorbidity             []CodeableConcept `json:"comorbidity,omitempty"`
	IntendedEffect          *CodeableConcept  `json:"intendedEffect,omitempty"`
	Duration                *Quantity         `json:"duration,omitempty"`
	UndesirableEffect       []Reference       `json:"undesirableEffect,omitempty"`
}

// MedicinalProductIngredient is one ingredient of a medicinal product.
type MedicinalProductIngredient struct {
	base
	Identifier          *Identifier      `json:"identifier,omitempty"`
	Role                *CodeableConcept `json:"role,omitempty"`
	AllergenicIndicator *bool            `json:"allergenicIndicator,omitempty"`
	Manufacturer        []Reference      `json:"manufacturer,omitempty"`
}

// MedicinalProductInteraction records an interaction between the product and something else.
type MedicinalProductInteraction struct {
	base
	Subject     []Reference                              `json:"subject,omitempty"`
	Description string                                   `json:"description,omitempty"`
	Interactant []MedicinalProductInteractionInteractant `json:"interactant,omitempty"`
	Type        *CodeableConcept                         `json:"type,omitempty"`
	Effect      *CodeableConcept                         `json:"effect,omitempty"`
	Incidence   *CodeableConcept                         `json:"incidence,omitempty"`
	Management  *CodeableConcept                         `json:"management,omitempty"`
}

// MedicinalProductInteractionInteractant is one party to an interaction.
type MedicinalProductInteractionInteractant struct {
	ItemReference       *Reference       `json:"itemReference,omitempty"`
	ItemCodeableConcept *CodeableConcept `json:"itemCodeableConcept,omitempty"`
}

// MedicinalProductManufactured is a manufactured item forming part of a packaged medicinal product.
type MedicinalProductManufactured struct {
	base
	ManufacturedDoseForm *CodeableConcept  `json:"manufacturedDoseForm,omitempty"`
	UnitOfPresentation   *CodeableConcept  `json:"unitOfPresentation,omitempty"`
	Quantity             *Quantity         `json:"quantity,omitempty"`
	Manufacturer         []Reference       `json:"manufacturer,omitempty"`
	Ingredient           []Reference       `json:"ingredient,omitempty"`
	OtherCharacteristics []CodeableConcept `json:"otherCharacteristics,omitempty"`
}

// MedicinalProductPackaged is a medicinal product as packaged for supply.
type MedicinalProductPackaged struct {
	base
	Identifier          []Identifier                      `json:"identifier,omitempty"`
	Subject             []Reference                       `json:"subject,omitempty"`
	Description         string                            `json:"description,omitempty"`
	LegalStatusOfSupply *CodeableConcept                  `json:"legalStatusOfSupply,omitempty"`
	MarketingStatus     []CodeableConcept                 `json:"marketingStatus,omitempty"`
	BatchIdentifier     []MedicinalProductPackagedBatchID `json:"batchIdentifier,omitempty"`
	PackageItem         []MedicinalProductPackagedItem    `json:"packageItem,omitempty"`
}

// MedicinalProductPackagedBatchID is the outer and immediate packaging identifiers for a batch.
type MedicinalProductPackagedBatchID struct {
	OuterPackaging     *Identifier `json:"outerPackaging,omitempty"`
	ImmediatePackaging *Identifier `json:"immediatePackaging,omitempty"`
}

// MedicinalProductPackagedItem is one item within the package.
type MedicinalProductPackagedItem struct {
	Identifier []Identifier      `json:"identifier,omitempty"`
	Type       *CodeableConcept  `json:"type,omitempty"`
	Quantity   *Quantity         `json:"quantity,omitempty"`
	Material   []CodeableConcept `json:"material,omitempty"`
}

// MedicinalProductPharmaceutical is a pharmaceutical product with its route of administration.
type MedicinalProductPharmaceutical struct {
	base
	Identifier            []Identifier                        `json:"identifier,omitempty"`
	AdministrableDoseForm *CodeableConcept                    `json:"administrableDoseForm,omitempty"`
	UnitOfPresentation    *CodeableConcept                    `json:"unitOfPresentation,omitempty"`
	Ingredient            []Reference                         `json:"ingredient,omitempty"`
	Device                []Reference                         `json:"device,omitempty"`
	RouteOfAdministration []MedicinalProductPharmRouteOfAdmin `json:"routeOfAdministration,omitempty"`
}

// MedicinalProductPharmRouteOfAdmin is one authorised route of administration with its dose limits.
type MedicinalProductPharmRouteOfAdmin struct {
	Code               *CodeableConcept `json:"code,omitempty"`
	FirstDose          *Quantity        `json:"firstDose,omitempty"`
	MaxSingleDose      *Quantity        `json:"maxSingleDose,omitempty"`
	MaxDosePerDay      *Quantity        `json:"maxDosePerDay,omitempty"`
	MaxTreatmentPeriod *Quantity        `json:"maxTreatmentPeriod,omitempty"`
}

// MedicinalProductUndesirableEffect records a known undesirable effect of the product.
type MedicinalProductUndesirableEffect struct {
	base
	Subject                []Reference      `json:"subject,omitempty"`
	SymptomConditionEffect *CodeableConcept `json:"symptomConditionEffect,omitempty"`
	Classification         *CodeableConcept `json:"classification,omitempty"`
	FrequencyOfOccurrence  *CodeableConcept `json:"frequencyOfOccurrence,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Research definitional artifacts
// ─────────────────────────────────────────────────────────────────────────────

// ResearchDefinition describes a population, exposure and outcome for a research question.
type ResearchDefinition struct {
	base
	URL                 string       `json:"url,omitempty"`
	Identifier          []Identifier `json:"identifier,omitempty"`
	Version             string       `json:"version,omitempty"`
	Name                string       `json:"name,omitempty"`
	Title               string       `json:"title,omitempty"`
	ShortTitle          string       `json:"shortTitle,omitempty"`
	Status              string       `json:"status,omitempty"`
	Experimental        *bool        `json:"experimental,omitempty"`
	Date                string       `json:"date,omitempty"`
	Publisher           string       `json:"publisher,omitempty"`
	Description         string       `json:"description,omitempty"`
	Population          *Reference   `json:"population,omitempty"`
	Exposure            *Reference   `json:"exposure,omitempty"`
	ExposureAlternative *Reference   `json:"exposureAlternative,omitempty"`
	Outcome             *Reference   `json:"outcome,omitempty"`
}

// ResearchElementDefinition is one element - a population, exposure or outcome - of a research definition.
type ResearchElementDefinition struct {
	base
	URL            string                                    `json:"url,omitempty"`
	Identifier     []Identifier                              `json:"identifier,omitempty"`
	Version        string                                    `json:"version,omitempty"`
	Name           string                                    `json:"name,omitempty"`
	Title          string                                    `json:"title,omitempty"`
	Status         string                                    `json:"status,omitempty"`
	Experimental   *bool                                     `json:"experimental,omitempty"`
	Date           string                                    `json:"date,omitempty"`
	Publisher      string                                    `json:"publisher,omitempty"`
	Description    string                                    `json:"description,omitempty"`
	Type           string                                    `json:"type,omitempty"`
	VariableType   string                                    `json:"variableType,omitempty"`
	Characteristic []ResearchElementDefinitionCharacteristic `json:"characteristic,omitempty"`
}

// ResearchElementDefinitionCharacteristic is one defining characteristic of the element.
type ResearchElementDefinitionCharacteristic struct {
	DefinitionCodeableConcept *CodeableConcept `json:"definitionCodeableConcept,omitempty"`
	DefinitionCanonical       string           `json:"definitionCanonical,omitempty"`
	DefinitionExpression      *Expression      `json:"definitionExpression,omitempty"`
	UsageContext              []Coding         `json:"usageContext,omitempty"`
	Exclude                   *bool            `json:"exclude,omitempty"`
	UnitOfMeasure             *CodeableConcept `json:"unitOfMeasure,omitempty"`
}

// Expression is a computable expression, used by the definitional resources.
//
// Declared here because these are the first resources in this package to need one. It is a general R4 datatype, not specific to
// research definitions, so a later resource wanting one should use this rather than declaring a second.
type Expression struct {
	Description string `json:"description,omitempty"`
	Name        string `json:"name,omitempty"`
	Language    string `json:"language,omitempty"`
	Expression  string `json:"expression,omitempty"`
	Reference   string `json:"reference,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Substance family — chemical and biological detail
// ─────────────────────────────────────────────────────────────────────────────

// SubstanceNucleicAcid describes a nucleic acid substance by its subunit sequences.
type SubstanceNucleicAcid struct {
	base
	SequenceType        *CodeableConcept              `json:"sequenceType,omitempty"`
	NumberOfSubunits    *int                          `json:"numberOfSubunits,omitempty"`
	AreaOfHybridisation string                        `json:"areaOfHybridisation,omitempty"`
	OligoNucleotideType *CodeableConcept              `json:"oligoNucleotideType,omitempty"`
	Subunit             []SubstanceNucleicAcidSubunit `json:"subunit,omitempty"`
}

// SubstanceNucleicAcidSubunit is one subunit of a nucleic acid.
type SubstanceNucleicAcidSubunit struct {
	Subunit            *int             `json:"subunit,omitempty"`
	Sequence           string           `json:"sequence,omitempty"`
	Length             *int             `json:"length,omitempty"`
	SequenceAttachment *Attachment      `json:"sequenceAttachment,omitempty"`
	FivePrime          *CodeableConcept `json:"fivePrime,omitempty"`
	ThreePrime         *CodeableConcept `json:"threePrime,omitempty"`
}

// SubstancePolymer describes a polymeric substance by class, geometry and modification.
type SubstancePolymer struct {
	base
	Class                 *CodeableConcept  `json:"class,omitempty"`
	Geometry              *CodeableConcept  `json:"geometry,omitempty"`
	CopolymerConnectivity []CodeableConcept `json:"copolymerConnectivity,omitempty"`
	Modification          []string          `json:"modification,omitempty"`
}

// SubstanceProtein describes a protein substance by its subunit sequences and disulfide linkages.
type SubstanceProtein struct {
	base
	SequenceType     *CodeableConcept          `json:"sequenceType,omitempty"`
	NumberOfSubunits *int                      `json:"numberOfSubunits,omitempty"`
	DisulfideLinkage []string                  `json:"disulfideLinkage,omitempty"`
	Subunit          []SubstanceProteinSubunit `json:"subunit,omitempty"`
}

// SubstanceProteinSubunit is one subunit of a protein.
type SubstanceProteinSubunit struct {
	Subunit                 *int        `json:"subunit,omitempty"`
	Sequence                string      `json:"sequence,omitempty"`
	Length                  *int        `json:"length,omitempty"`
	SequenceAttachment      *Attachment `json:"sequenceAttachment,omitempty"`
	NTerminalModificationID *Identifier `json:"nTerminalModificationId,omitempty"`
	NTerminalModification   string      `json:"nTerminalModification,omitempty"`
	CTerminalModificationID *Identifier `json:"cTerminalModificationId,omitempty"`
	CTerminalModification   string      `json:"cTerminalModification,omitempty"`
}

// SubstanceReferenceInformation carries gene, classification and target information about a substance.
type SubstanceReferenceInformation struct {
	base
	Comment        string                                     `json:"comment,omitempty"`
	Gene           []SubstanceReferenceInformationGene        `json:"gene,omitempty"`
	GeneElement    []SubstanceReferenceInformationGeneElement `json:"geneElement,omitempty"`
	Classification []SubstanceReferenceInformationClassify    `json:"classification,omitempty"`
	Target         []SubstanceReferenceInformationTarget      `json:"target,omitempty"`
}

// SubstanceReferenceInformationGene names a gene and its sequence origin.
type SubstanceReferenceInformationGene struct {
	GeneSequenceOrigin *CodeableConcept `json:"geneSequenceOrigin,omitempty"`
	Gene               *CodeableConcept `json:"gene,omitempty"`
	Source             []Reference      `json:"source,omitempty"`
}

// SubstanceReferenceInformationGeneElement names an element within a gene.
type SubstanceReferenceInformationGeneElement struct {
	Type    *CodeableConcept `json:"type,omitempty"`
	Element *Identifier      `json:"element,omitempty"`
	Source  []Reference      `json:"source,omitempty"`
}

// SubstanceReferenceInformationClassify classifies a substance within a domain.
type SubstanceReferenceInformationClassify struct {
	Domain         *CodeableConcept  `json:"domain,omitempty"`
	Classification *CodeableConcept  `json:"classification,omitempty"`
	Subtype        []CodeableConcept `json:"subtype,omitempty"`
	Source         []Reference       `json:"source,omitempty"`
}

// SubstanceReferenceInformationTarget is a biological target of the substance.
type SubstanceReferenceInformationTarget struct {
	Target         *Identifier      `json:"target,omitempty"`
	Type           *CodeableConcept `json:"type,omitempty"`
	Interaction    *CodeableConcept `json:"interaction,omitempty"`
	Organism       *CodeableConcept `json:"organism,omitempty"`
	OrganismType   *CodeableConcept `json:"organismType,omitempty"`
	AmountQuantity *Quantity        `json:"amountQuantity,omitempty"`
	AmountRange    *Range           `json:"amountRange,omitempty"`
	AmountString   string           `json:"amountString,omitempty"`
	Source         []Reference      `json:"source,omitempty"`
}

// SubstanceSourceMaterial describes the raw material a substance was derived from.
type SubstanceSourceMaterial struct {
	base
	SourceMaterialClass  *CodeableConcept  `json:"sourceMaterialClass,omitempty"`
	SourceMaterialType   *CodeableConcept  `json:"sourceMaterialType,omitempty"`
	SourceMaterialState  *CodeableConcept  `json:"sourceMaterialState,omitempty"`
	OrganismID           *Identifier       `json:"organismId,omitempty"`
	OrganismName         string            `json:"organismName,omitempty"`
	ParentSubstanceID    []Identifier      `json:"parentSubstanceId,omitempty"`
	ParentSubstanceName  []string          `json:"parentSubstanceName,omitempty"`
	CountryOfOrigin      []CodeableConcept `json:"countryOfOrigin,omitempty"`
	GeographicalLocation []string          `json:"geographicalLocation,omitempty"`
	DevelopmentStage     *CodeableConcept  `json:"developmentStage,omitempty"`
}

// ─────────────────────────────────────────────────────────────────────────────
// Resource interface
// ─────────────────────────────────────────────────────────────────────────────

func (b *BiologicallyDerivedProduct) ResourceTypeName() string { return "BiologicallyDerivedProduct" }
func (b *BiologicallyDerivedProduct) ResourceID() string       { return b.ID }
func (b *BiologicallyDerivedProduct) SetResourceID(id string) {
	b.ID = id
	b.ResourceType = "BiologicallyDerivedProduct"
}

func (d *DocumentManifest) ResourceTypeName() string { return "DocumentManifest" }
func (d *DocumentManifest) ResourceID() string       { return d.ID }
func (d *DocumentManifest) SetResourceID(id string)  { d.ID = id; d.ResourceType = "DocumentManifest" }

func (m *MedicinalProductContraindication) ResourceTypeName() string {
	return "MedicinalProductContraindication"
}
func (m *MedicinalProductContraindication) ResourceID() string { return m.ID }
func (m *MedicinalProductContraindication) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicinalProductContraindication"
}

func (m *MedicinalProductIndication) ResourceTypeName() string { return "MedicinalProductIndication" }
func (m *MedicinalProductIndication) ResourceID() string       { return m.ID }
func (m *MedicinalProductIndication) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicinalProductIndication"
}

func (m *MedicinalProductIngredient) ResourceTypeName() string { return "MedicinalProductIngredient" }
func (m *MedicinalProductIngredient) ResourceID() string       { return m.ID }
func (m *MedicinalProductIngredient) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicinalProductIngredient"
}

func (m *MedicinalProductInteraction) ResourceTypeName() string { return "MedicinalProductInteraction" }
func (m *MedicinalProductInteraction) ResourceID() string       { return m.ID }
func (m *MedicinalProductInteraction) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicinalProductInteraction"
}

func (m *MedicinalProductManufactured) ResourceTypeName() string {
	return "MedicinalProductManufactured"
}
func (m *MedicinalProductManufactured) ResourceID() string { return m.ID }
func (m *MedicinalProductManufactured) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicinalProductManufactured"
}

func (m *MedicinalProductPackaged) ResourceTypeName() string { return "MedicinalProductPackaged" }
func (m *MedicinalProductPackaged) ResourceID() string       { return m.ID }
func (m *MedicinalProductPackaged) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicinalProductPackaged"
}

func (m *MedicinalProductPharmaceutical) ResourceTypeName() string {
	return "MedicinalProductPharmaceutical"
}
func (m *MedicinalProductPharmaceutical) ResourceID() string { return m.ID }
func (m *MedicinalProductPharmaceutical) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicinalProductPharmaceutical"
}

func (m *MedicinalProductUndesirableEffect) ResourceTypeName() string {
	return "MedicinalProductUndesirableEffect"
}
func (m *MedicinalProductUndesirableEffect) ResourceID() string { return m.ID }
func (m *MedicinalProductUndesirableEffect) SetResourceID(id string) {
	m.ID = id
	m.ResourceType = "MedicinalProductUndesirableEffect"
}

func (r *ResearchDefinition) ResourceTypeName() string { return "ResearchDefinition" }
func (r *ResearchDefinition) ResourceID() string       { return r.ID }
func (r *ResearchDefinition) SetResourceID(id string) {
	r.ID = id
	r.ResourceType = "ResearchDefinition"
}

func (r *ResearchElementDefinition) ResourceTypeName() string { return "ResearchElementDefinition" }
func (r *ResearchElementDefinition) ResourceID() string       { return r.ID }
func (r *ResearchElementDefinition) SetResourceID(id string) {
	r.ID = id
	r.ResourceType = "ResearchElementDefinition"
}

func (s *SubstanceNucleicAcid) ResourceTypeName() string { return "SubstanceNucleicAcid" }
func (s *SubstanceNucleicAcid) ResourceID() string       { return s.ID }
func (s *SubstanceNucleicAcid) SetResourceID(id string) {
	s.ID = id
	s.ResourceType = "SubstanceNucleicAcid"
}

func (s *SubstancePolymer) ResourceTypeName() string { return "SubstancePolymer" }
func (s *SubstancePolymer) ResourceID() string       { return s.ID }
func (s *SubstancePolymer) SetResourceID(id string)  { s.ID = id; s.ResourceType = "SubstancePolymer" }

func (s *SubstanceProtein) ResourceTypeName() string { return "SubstanceProtein" }
func (s *SubstanceProtein) ResourceID() string       { return s.ID }
func (s *SubstanceProtein) SetResourceID(id string)  { s.ID = id; s.ResourceType = "SubstanceProtein" }

func (s *SubstanceReferenceInformation) ResourceTypeName() string {
	return "SubstanceReferenceInformation"
}
func (s *SubstanceReferenceInformation) ResourceID() string { return s.ID }
func (s *SubstanceReferenceInformation) SetResourceID(id string) {
	s.ID = id
	s.ResourceType = "SubstanceReferenceInformation"
}

func (s *SubstanceSourceMaterial) ResourceTypeName() string { return "SubstanceSourceMaterial" }
func (s *SubstanceSourceMaterial) ResourceID() string       { return s.ID }
func (s *SubstanceSourceMaterial) SetResourceID(id string) {
	s.ID = id
	s.ResourceType = "SubstanceSourceMaterial"
}
