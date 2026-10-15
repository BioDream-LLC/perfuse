// Package xds is IHE cross-enterprise document sharing: an XDS.b Document Registry and Repository (ITI-41 Provide and
// Register, ITI-18 Registry Stored Query, ITI-43 Retrieve Document Set), an XCA Responding Gateway over the same store
// (ITI-38, ITI-39), and the client side of each, so a channel can publish a document to another organisation's
// repository and the console can query a registry or a community.
//
// The metadata is ebXML Registry 3.0 (ebRIM), as XDS.b defines it in IHE ITI TF-3 chapter 4. It is modelled here as Go
// structs whose XML tags carry the namespaces, so a message is read the same whichever prefixes the sender chose, and
// written namespace-correct.
package xds

import (
	"encoding/xml"
	"strings"
)

// Namespaces.
const (
	NSLCM   = "urn:oasis:names:tc:ebxml-regrep:xsd:lcm:3.0"
	NSRIM   = "urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0"
	NSRS    = "urn:oasis:names:tc:ebxml-regrep:xsd:rs:3.0"
	NSQuery = "urn:oasis:names:tc:ebxml-regrep:xsd:query:3.0"
	NSXDSB  = "urn:ihe:iti:xds-b:2007"
)

// SOAP actions of the transactions.
const (
	ActionProvideAndRegister = "urn:ihe:iti:2007:ProvideAndRegisterDocumentSet-b"
	ActionRegister           = "urn:ihe:iti:2007:RegisterDocumentSet-b"
	ActionStoredQuery        = "urn:ihe:iti:2007:RegistryStoredQuery"
	ActionRetrieve           = "urn:ihe:iti:2007:RetrieveDocumentSet"
	ActionCrossGatewayQuery  = "urn:ihe:iti:2007:CrossGatewayQuery"
	ActionCrossGatewayRetr   = "urn:ihe:iti:2007:CrossGatewayRetrieve"
)

// Object types, classification schemes and identification schemes (ITI TF-3 4.2.5).
const (
	TypeStableDocument   = "urn:uuid:7edca82f-054d-47f2-a032-9b2a5b5186c1"
	TypeOnDemandDocument = "urn:uuid:34268e47-fdf5-41a6-ba33-82133c465248"
	TypeRegistryPackage  = "urn:oasis:names:tc:ebxml-regrep:ObjectType:RegistryObject:RegistryPackage"
	TypeAssociation      = "urn:oasis:names:tc:ebxml-regrep:ObjectType:RegistryObject:Association"
	TypeClassification   = "urn:oasis:names:tc:ebxml-regrep:ObjectType:RegistryObject:Classification"
	TypeExternalID       = "urn:oasis:names:tc:ebxml-regrep:ObjectType:RegistryObject:ExternalIdentifier"

	NodeSubmissionSet = "urn:uuid:a54d6aa5-d40d-43f9-88c5-b4633d873bdd"
	NodeFolder        = "urn:uuid:d9d542f3-6cc4-48b6-8870-ea235fbc94c2"

	SchemeClassCode       = "urn:uuid:41a5887f-8865-4c09-adf7-e362475b143a"
	SchemeConfidentiality = "urn:uuid:f4f85eac-e6cb-4883-b524-f2705394840f"
	SchemeEventCode       = "urn:uuid:2c6b8cb7-8b2a-4051-b291-b1ae6a575ef4"
	SchemeFormatCode      = "urn:uuid:a09d5840-386c-46f2-b5ad-9c3699a4309d"
	SchemeFacilityType    = "urn:uuid:f33fb8ac-18af-42cc-ae0e-ed0b0bdb91e1"
	SchemePracticeSetting = "urn:uuid:cccf5598-8b07-4b77-a05e-ae952c785ead"
	SchemeTypeCode        = "urn:uuid:f0306f51-975f-434e-a61c-c59651d33983"
	SchemeDocAuthor       = "urn:uuid:93606bcf-9494-43ec-9b4e-a7748d1a838d"
	SchemeSSAuthor        = "urn:uuid:a7058bb9-b4e4-4307-ba5b-e3f0ab85e12d"
	SchemeContentType     = "urn:uuid:aa543740-bdda-424e-8c96-df4873be8500"
	SchemeFolderCode      = "urn:uuid:1ba97051-7806-41a8-a48b-8fce7af683c5"

	IDDocUniqueID    = "urn:uuid:2e82c1f6-a085-4c72-9da3-8640a32e42ab"
	IDDocPatientID   = "urn:uuid:58a6f841-87b3-4a3e-92fd-a8ffeff98427"
	IDSSUniqueID     = "urn:uuid:96fdda7c-d067-4183-912e-bf5ee74998a8"
	IDSSPatientID    = "urn:uuid:6b5aea1a-874d-4603-a4bc-96a0a7b38446"
	IDSSSourceID     = "urn:uuid:554ac39e-e3fe-47fe-b233-965d2a147832"
	IDFolderUniqueID = "urn:uuid:75df8f67-9973-4fbe-a900-df66cefecc5a"
	IDFolderPatient  = "urn:uuid:f64ffdf0-4b97-4e06-b79f-a52b38ec2f8a"

	AssocHasMember = "urn:oasis:names:tc:ebxml-regrep:AssociationType:HasMember"
	AssocReplace   = "urn:ihe:iti:2007:AssociationType:RPLC"
	AssocAppend    = "urn:ihe:iti:2007:AssociationType:APND"
	AssocTransform = "urn:ihe:iti:2007:AssociationType:XFRM"
	AssocXFRMRPLC  = "urn:ihe:iti:2007:AssociationType:XFRM_RPLC"
	AssocSigns     = "urn:ihe:iti:2007:AssociationType:signs"

	StatusApproved   = "urn:oasis:names:tc:ebxml-regrep:StatusType:Approved"
	StatusDeprecated = "urn:oasis:names:tc:ebxml-regrep:StatusType:Deprecated"

	ResponseSuccess        = "urn:oasis:names:tc:ebxml-regrep:ResponseStatusType:Success"
	ResponseFailure        = "urn:oasis:names:tc:ebxml-regrep:ResponseStatusType:Failure"
	ResponsePartialSuccess = "urn:ihe:iti:2007:ResponseStatusType:PartialSuccess"
)

// Slot is a name with string values.
type Slot struct {
	Name   string   `xml:"name,attr"`
	Values []string `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 ValueList>Value"`
}

// LocalizedString is one language's text.
type LocalizedString struct {
	Lang    string `xml:"http://www.w3.org/XML/1998/namespace lang,attr,omitempty"`
	Charset string `xml:"charset,attr,omitempty"`
	Value   string `xml:"value,attr"`
}

// International is a Name or Description: text in some languages.
type International struct {
	Strings []LocalizedString `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 LocalizedString"`
}

// Text is the first string, or "".
func (i *International) Text() string {
	if i == nil || len(i.Strings) == 0 {
		return ""
	}
	return i.Strings[0].Value
}

// Intl makes an International with one English string, or nil for "".
func Intl(s string) *International {
	if s == "" {
		return nil
	}
	return &International{Strings: []LocalizedString{{Value: s}}}
}

// Classification classifies an object: a code (classificationScheme, nodeRepresentation, the coding scheme in a slot, the
// display in Name), or, with classificationNode, what kind of package it is.
type Classification struct {
	ID                 string         `xml:"id,attr,omitempty"`
	Home               string         `xml:"home,attr,omitempty"`
	ObjectType         string         `xml:"objectType,attr,omitempty"`
	ClassificationNode string         `xml:"classificationNode,attr,omitempty"`
	Scheme             string         `xml:"classificationScheme,attr,omitempty"`
	ClassifiedObject   string         `xml:"classifiedObject,attr"`
	NodeRepresentation string         `xml:"nodeRepresentation,attr,omitempty"`
	Slots              []Slot         `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 Slot"`
	Name               *International `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 Name,omitempty"`
}

// ExternalIdentifier is an identifier of an object in a scheme: a uniqueId, a patientId, a sourceId.
type ExternalIdentifier struct {
	ID                   string         `xml:"id,attr,omitempty"`
	Home                 string         `xml:"home,attr,omitempty"`
	ObjectType           string         `xml:"objectType,attr,omitempty"`
	RegistryObject       string         `xml:"registryObject,attr"`
	IdentificationScheme string         `xml:"identificationScheme,attr"`
	Value                string         `xml:"value,attr"`
	Name                 *International `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 Name,omitempty"`
}

// VersionInfo is a registry object's version.
type VersionInfo struct {
	VersionName string `xml:"versionName,attr"`
}

// Object is the shared part of an ExtrinsicObject (a DocumentEntry), a RegistryPackage (a SubmissionSet or a Folder) and an
// Association.
type Object struct {
	ID              string               `xml:"id,attr"`
	LID             string               `xml:"lid,attr,omitempty"`
	Home            string               `xml:"home,attr,omitempty"`
	ObjectType      string               `xml:"objectType,attr,omitempty"`
	Status          string               `xml:"status,attr,omitempty"`
	MimeType        string               `xml:"mimeType,attr,omitempty"`
	IsOpaque        string               `xml:"isOpaque,attr,omitempty"`
	AssociationType string               `xml:"associationType,attr,omitempty"`
	SourceObject    string               `xml:"sourceObject,attr,omitempty"`
	TargetObject    string               `xml:"targetObject,attr,omitempty"`
	Slots           []Slot               `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 Slot"`
	Name            *International       `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 Name,omitempty"`
	Description     *International       `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 Description,omitempty"`
	VersionInfo     *VersionInfo         `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 VersionInfo,omitempty"`
	Classifications []Classification     `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 Classification"`
	ExternalIDs     []ExternalIdentifier `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 ExternalIdentifier"`
}

// Slot returns a slot's values.
func (o *Object) Slot(name string) []string {
	for _, s := range o.Slots {
		if s.Name == name {
			return s.Values
		}
	}
	return nil
}

// Slot1 returns a slot's first value, or "".
func (o *Object) Slot1(name string) string {
	if v := o.Slot(name); len(v) > 0 {
		return v[0]
	}
	return ""
}

// SetSlot replaces or adds a slot.
func (o *Object) SetSlot(name string, values ...string) {
	for i := range o.Slots {
		if o.Slots[i].Name == name {
			o.Slots[i].Values = values
			return
		}
	}
	o.Slots = append(o.Slots, Slot{Name: name, Values: values})
}

// ExternalID returns the value of an external identifier in a scheme, or "".
func (o *Object) ExternalID(scheme string) string {
	for _, e := range o.ExternalIDs {
		if e.IdentificationScheme == scheme {
			return e.Value
		}
	}
	return ""
}

// Codes returns the classifications in a scheme.
func (o *Object) Codes(scheme string) []Code {
	var out []Code
	for _, c := range o.Classifications {
		if c.Scheme == scheme {
			cd := Code{Code: c.NodeRepresentation, Display: c.Name.Text()}
			for _, s := range c.Slots {
				if s.Name == "codingScheme" && len(s.Values) > 0 {
					cd.Scheme = s.Values[0]
				}
			}
			out = append(out, cd)
		}
	}
	return out
}

// HasNode says whether a package is classified with a node (NodeSubmissionSet, NodeFolder). The classification may be inside
// the package or a sibling of it in the request; Request.Classify resolves the latter.
func (o *Object) HasNode(node string) bool {
	for _, c := range o.Classifications {
		if c.ClassificationNode == node {
			return true
		}
	}
	return false
}

// Code is a coded value: code, coding scheme and display.
type Code struct {
	Code    string `json:"code"`
	Scheme  string `json:"scheme"`
	Display string `json:"display,omitempty"`
}

// Key is the code as the stored query parameters write it: code^^^scheme.
func (c Code) Key() string { return c.Code + "^^^" + c.Scheme }

// RegistryObjectList is the list of objects in a submission or a query response.
type RegistryObjectList struct {
	Extrinsic       []Object         `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 ExtrinsicObject"`
	Packages        []Object         `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 RegistryPackage"`
	Associations    []Object         `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 Association"`
	Classifications []Classification `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 Classification"`
	ObjectRefs      []ObjectRef      `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 ObjectRef"`
}

// ObjectRef is a reference to an object, the answer to a query asking for ObjectRef rather than LeafClass.
type ObjectRef struct {
	ID   string `xml:"id,attr"`
	Home string `xml:"home,attr,omitempty"`
}

// SubmitObjectsRequest is the metadata of a submission (ITI-41, ITI-42).
type SubmitObjectsRequest struct {
	XMLName xml.Name           `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:lcm:3.0 SubmitObjectsRequest"`
	Objects RegistryObjectList `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 RegistryObjectList"`
}

// Document is a document's content in a Provide and Register request or a Retrieve response.
type Document struct {
	ID string `xml:"id,attr,omitempty"`
	// Inner is the element's content as sent: base64 text, or an xop:Include.
	Inner   string `xml:",innerxml"`
	Content []byte `xml:"-"`
}

// ProvideAndRegisterRequest is ITI-41's body.
type ProvideAndRegisterRequest struct {
	XMLName   xml.Name             `xml:"urn:ihe:iti:xds-b:2007 ProvideAndRegisterDocumentSetRequest"`
	Submit    SubmitObjectsRequest `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:lcm:3.0 SubmitObjectsRequest"`
	Documents []Document           `xml:"urn:ihe:iti:xds-b:2007 Document"`
}

// RegistryError is one error or warning in a RegistryResponse.
type RegistryError struct {
	Code     string `xml:"errorCode,attr"`
	Context  string `xml:"codeContext,attr"`
	Severity string `xml:"severity,attr"`
	Location string `xml:"location,attr,omitempty"`
}

// Severities.
const (
	SeverityError   = "urn:oasis:names:tc:ebxml-regrep:ErrorSeverityType:Error"
	SeverityWarning = "urn:oasis:names:tc:ebxml-regrep:ErrorSeverityType:Warning"
)

// ErrorList is a RegistryErrorList. Its own type, because in a Go "a>b" tag the namespace names only b, and the list
// element itself must be in the rs namespace whatever response it sits in.
type ErrorList []RegistryError

// MarshalXML writes the list, or nothing when it is empty.
func (l ErrorList) MarshalXML(e *xml.Encoder, start xml.StartElement) error {
	if len(l) == 0 {
		return nil
	}
	start.Name = xml.Name{Space: NSRS, Local: "RegistryErrorList"}
	if err := e.EncodeToken(start); err != nil {
		return err
	}
	for _, re := range l {
		if err := e.EncodeElement(re, xml.StartElement{Name: xml.Name{Space: NSRS, Local: "RegistryError"}}); err != nil {
			return err
		}
	}
	return e.EncodeToken(start.End())
}

// UnmarshalXML reads the list.
func (l *ErrorList) UnmarshalXML(d *xml.Decoder, start xml.StartElement) error {
	var v struct {
		Errors []RegistryError `xml:"RegistryError"`
	}
	if err := d.DecodeElement(&v, &start); err != nil {
		return err
	}
	*l = v.Errors
	return nil
}

// RegistryResponse is the answer to a submission, and the status part of a query or retrieve answer.
type RegistryResponse struct {
	XMLName xml.Name  `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rs:3.0 RegistryResponse"`
	Status  string    `xml:"status,attr"`
	Errors  ErrorList `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rs:3.0 RegistryErrorList"`
}

// Failed says whether the response carries an error (not only warnings).
func (r *RegistryResponse) Failed() bool {
	if r.Status == ResponseFailure {
		return true
	}
	for _, e := range r.Errors {
		if e.Severity == SeverityError {
			return r.Status != ResponsePartialSuccess
		}
	}
	return false
}

// Summary is the response in words.
func (r *RegistryResponse) Summary() string {
	var parts []string
	for _, e := range r.Errors {
		kind := "error"
		if e.Severity == SeverityWarning {
			kind = "warning"
		}
		parts = append(parts, kind+" "+e.Code+": "+e.Context)
	}
	status := r.Status[strings.LastIndex(r.Status, ":")+1:]
	if len(parts) == 0 {
		return status
	}
	return status + " (" + strings.Join(parts, "; ") + ")"
}

// Error codes (ITI TF-3 Table 4.2.4.1-2).
const (
	ErrRegistryError       = "XDSRegistryError"
	ErrRepositoryError     = "XDSRepositoryError"
	ErrMetadata            = "XDSRegistryMetadataError"
	ErrMissingDocument     = "XDSMissingDocument"
	ErrMissingMetadata     = "XDSMissingDocumentMetadata"
	ErrPatientMismatch     = "XDSPatientIdDoesNotMatch"
	ErrUnknownPatient      = "XDSUnknownPatientId"
	ErrDuplicateUniqueID   = "XDSDuplicateUniqueIdInRegistry"
	ErrNonIdenticalHash    = "XDSNonIdenticalHash"
	ErrNonIdenticalSize    = "XDSNonIdenticalSize"
	ErrUnknownStoredQuery  = "XDSUnknownStoredQuery"
	ErrQueryParamNumber    = "XDSStoredQueryParamNumber"
	ErrQueryMissingParam   = "XDSStoredQueryMissingParam"
	ErrDocumentUniqueID    = "XDSDocumentUniqueIdError"
	ErrUnknownRepository   = "XDSUnknownRepositoryId"
	ErrUnknownCommunity    = "XDSUnknownCommunity"
	ErrTooManyResults      = "XDSTooManyResults"
	ErrUnreferencedObject  = "UnresolvedReferenceException"
	ErrInvalidRequest      = "XDSRegistryBusy"
	ErrStoredQueryTooBroad = "XDSStoredQueryTooBroad"
)

// AdhocQueryRequest is ITI-18 and ITI-38.
type AdhocQueryRequest struct {
	XMLName     xml.Name `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:query:3.0 AdhocQueryRequest"`
	ResponseOpt struct {
		ReturnType            string `xml:"returnType,attr"`
		ReturnComposedObjects string `xml:"returnComposedObjects,attr,omitempty"`
	} `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:query:3.0 ResponseOption"`
	Query struct {
		ID    string `xml:"id,attr"`
		Home  string `xml:"home,attr,omitempty"`
		Slots []Slot `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 Slot"`
	} `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 AdhocQuery"`
}

// AdhocQueryResponse answers it.
type AdhocQueryResponse struct {
	XMLName xml.Name            `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:query:3.0 AdhocQueryResponse"`
	Status  string              `xml:"status,attr"`
	Errors  ErrorList           `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rs:3.0 RegistryErrorList"`
	Objects *RegistryObjectList `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rim:3.0 RegistryObjectList"`
}

// DocumentRequest is one document asked for in ITI-43 or ITI-39.
type DocumentRequest struct {
	HomeCommunityID    string `xml:"urn:ihe:iti:xds-b:2007 HomeCommunityId,omitempty"`
	RepositoryUniqueID string `xml:"urn:ihe:iti:xds-b:2007 RepositoryUniqueId"`
	DocumentUniqueID   string `xml:"urn:ihe:iti:xds-b:2007 DocumentUniqueId"`
}

// RetrieveRequest is ITI-43 and ITI-39.
type RetrieveRequest struct {
	XMLName  xml.Name          `xml:"urn:ihe:iti:xds-b:2007 RetrieveDocumentSetRequest"`
	Requests []DocumentRequest `xml:"urn:ihe:iti:xds-b:2007 DocumentRequest"`
}

// DocumentResponse is one retrieved document.
type DocumentResponse struct {
	HomeCommunityID    string   `xml:"urn:ihe:iti:xds-b:2007 HomeCommunityId,omitempty"`
	RepositoryUniqueID string   `xml:"urn:ihe:iti:xds-b:2007 RepositoryUniqueId"`
	DocumentUniqueID   string   `xml:"urn:ihe:iti:xds-b:2007 DocumentUniqueId"`
	MimeType           string   `xml:"urn:ihe:iti:xds-b:2007 mimeType"`
	Document           Document `xml:"urn:ihe:iti:xds-b:2007 Document"`
}

// RetrieveResponse answers it.
type RetrieveResponse struct {
	XMLName   xml.Name           `xml:"urn:ihe:iti:xds-b:2007 RetrieveDocumentSetResponse"`
	Status    RegistryResponse   `xml:"urn:oasis:names:tc:ebxml-regrep:xsd:rs:3.0 RegistryResponse"`
	Documents []DocumentResponse `xml:"urn:ihe:iti:xds-b:2007 DocumentResponse"`
}
