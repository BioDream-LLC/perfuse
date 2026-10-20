package xds

import (
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client sends XDS.b and XCA transactions.
type Client struct {
	HTTP    *http.Client
	Headers map[string]string
	// SOAPHeaders is more SOAP header XML for every message, such as a Direct addressing block for XDR.
	SOAPHeaders string
}

func (c *Client) do(ctx context.Context, url string, out Outgoing) (*Envelope, error) {
	out.To = url
	out.Headers = c.SOAPHeaders
	body, ct := out.Write()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", ct)
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 2 * time.Minute}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	env, err := ReadEnvelope(resp.Header.Get("Content-Type"), resp.Body)
	if err != nil {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("HTTP %d, and the answer is not SOAP: %v %s", resp.StatusCode, err, strings.TrimSpace(string(b)))
	}
	if bytes.Contains(env.Body[:min(len(env.Body), 200)], []byte("Fault")) {
		var f struct {
			Reason string `xml:"Reason>Text"`
			Code   string `xml:"Code>Value"`
		}
		_ = xml.Unmarshal(env.Body, &f)
		return nil, fmt.Errorf("SOAP fault %s: %s", strings.TrimSpace(f.Code), strings.TrimSpace(f.Reason))
	}
	return env, nil
}

// ProvideAndRegister sends ITI-41: the metadata and each document, as MTOM.
func (c *Client) ProvideAndRegister(ctx context.Context, url string, sub *SubmitObjectsRequest, docs map[string][]byte) (*RegistryResponse, error) {
	req := ProvideAndRegisterRequest{Submit: *sub}
	var parts []Attachment
	for _, eo := range sub.Objects.Extrinsic {
		content, ok := docs[eo.ID]
		if !ok {
			return nil, fmt.Errorf("DocumentEntry %s has no document", eo.ID)
		}
		cid := strings.TrimPrefix(eo.ID, "urn:uuid:") + "." + newUUID() + "@perfuse.health"
		parts = append(parts, Attachment{ContentID: cid, MimeType: eo.MimeType, Content: content})
		req.Documents = append(req.Documents, Document{ID: eo.ID, Inner: Include(cid)})
	}
	body, err := xml.Marshal(req)
	if err != nil {
		return nil, err
	}
	env, err := c.do(ctx, url, Outgoing{Action: ActionProvideAndRegister, Body: body, Attachments: parts, MTOM: true})
	if err != nil {
		return nil, err
	}
	var rr RegistryResponse
	if err := xml.Unmarshal(env.Body, &rr); err != nil {
		return nil, fmt.Errorf("the answer is not a RegistryResponse: %w", err)
	}
	return &rr, nil
}

// Query sends ITI-18 (action ActionStoredQuery) or ITI-38 (ActionCrossGatewayQuery).
func (c *Client) Query(ctx context.Context, url, action string, q *AdhocQueryRequest) (*AdhocQueryResponse, error) {
	body, err := xml.Marshal(q)
	if err != nil {
		return nil, err
	}
	env, err := c.do(ctx, url, Outgoing{Action: action, Body: body})
	if err != nil {
		return nil, err
	}
	var resp AdhocQueryResponse
	if err := xml.Unmarshal(env.Body, &resp); err != nil {
		return nil, fmt.Errorf("the answer is not an AdhocQueryResponse: %w", err)
	}
	return &resp, nil
}

// Retrieve sends ITI-43 (ActionRetrieve) or ITI-39 (ActionCrossGatewayRetr) and returns each document with its content.
func (c *Client) Retrieve(ctx context.Context, url, action string, req *RetrieveRequest) (*RetrieveResponse, error) {
	body, err := xml.Marshal(req)
	if err != nil {
		return nil, err
	}
	env, err := c.do(ctx, url, Outgoing{Action: action, Body: body, MTOM: true})
	if err != nil {
		return nil, err
	}
	var resp RetrieveResponse
	if err := xml.Unmarshal(env.Body, &resp); err != nil {
		return nil, fmt.Errorf("the answer is not a RetrieveDocumentSetResponse: %w", err)
	}
	for i := range resp.Documents {
		if err := env.Resolve(&resp.Documents[i].Document); err != nil {
			return nil, err
		}
	}
	return &resp, nil
}

// FindDocuments builds the FindDocuments stored query for a patient's approved documents.
func FindDocuments(patientID string, returnType string) *AdhocQueryRequest {
	q := &AdhocQueryRequest{}
	if returnType == "" {
		returnType = "LeafClass"
	}
	q.ResponseOpt.ReturnType = returnType
	q.Query.ID = QFindDocuments
	q.Query.Slots = []Slot{
		{Name: "$XDSDocumentEntryPatientId", Values: []string{"'" + strings.ReplaceAll(patientID, "'", "''") + "'"}},
		{Name: "$XDSDocumentEntryStatus", Values: []string{"('" + StatusApproved + "')"}},
	}
	return q
}

// DocumentMeta is what a submission of one document says about it.
type DocumentMeta struct {
	PatientID         string // CX: id^^^&oid&ISO, in the affinity domain
	SourcePatientID   string // CX in the source's own domain; defaults to PatientID
	SourceID          string // the submitting system's OID
	UniqueID          string // the document's OID; defaults to a new 2.25 UUID OID
	SetUniqueID       string // the SubmissionSet's OID; defaults likewise
	Title             string
	MimeType          string
	CreationTime      time.Time
	ServiceStart      time.Time
	ServiceStop       time.Time
	Language          string
	Class             Code
	Type              Code
	Format            Code
	Confidentiality   Code
	FacilityType      Code
	PracticeSetting   Code
	ContentType       Code
	AuthorPerson      string // XCN
	AuthorInstitution string // XON
	// Replaces is the entryUUID of a document this one replaces (RPLC), deprecating it.
	Replaces string
	// IntendedRecipients go in the SubmissionSet's intendedRecipient slot (XON|XCN|XTN), which XDR uses to say who the
	// documents are for.
	IntendedRecipients []string
}

// UUIDOID makes an OID under 2.25 from a new UUID, the form an OID is given when nobody has assigned one.
func UUIDOID() string {
	h := strings.ReplaceAll(newUUID(), "-", "")
	var n [16]byte
	for i := 0; i < 16; i++ {
		fmt.Sscanf(h[2*i:2*i+2], "%02x", &n[i])
	}
	// The decimal of a 128-bit number, by long division.
	digits := []byte{}
	num := n[:]
	for {
		zero := true
		rem := 0
		for i := range num {
			v := rem*256 + int(num[i])
			num[i] = byte(v / 10)
			rem = v % 10
			if num[i] != 0 {
				zero = false
			}
		}
		digits = append([]byte{byte('0' + rem)}, digits...)
		if zero {
			break
		}
	}
	return "2.25." + strings.TrimLeft(string(digits), "0")
}

func classification(scheme, object string, c Code, extraSlots ...Slot) Classification {
	// A coded attribute's Name is required (ITI TF-3 4.2.3.1.7): with no display given, the code itself, which a registry
	// accepts where it refuses a classification with none.
	display := c.Display
	if strings.TrimSpace(display) == "" {
		display = c.Code
	}
	return Classification{ID: "urn:uuid:" + newUUID(), ObjectType: TypeClassification, Scheme: scheme, ClassifiedObject: object,
		NodeRepresentation: c.Code, Slots: append([]Slot{{Name: "codingScheme", Values: []string{c.Scheme}}}, extraSlots...),
		Name: Intl(display)}
}

func extID(scheme, object, value, name string) ExternalIdentifier {
	return ExternalIdentifier{ID: "urn:uuid:" + newUUID(), ObjectType: TypeExternalID, RegistryObject: object,
		IdentificationScheme: scheme, Value: value, Name: Intl(name)}
}

// ErrIncomplete is metadata missing something a registry requires.
var ErrIncomplete = errors.New("the document metadata is incomplete")

// Build writes the metadata of a submission of one document: a SubmissionSet, a DocumentEntry and the HasMember
// association between them (and a replacement association when Replaces is set), with symbolic ids. The document's id
// in the returned request is "Document01".
func Build(m DocumentMeta, now time.Time) (*SubmitObjectsRequest, error) {
	var missing []string
	for _, f := range []struct{ v, name string }{{m.PatientID, "patient ID"}, {m.SourceID, "source ID"}, {m.MimeType, "mime type"},
		{m.Class.Code, "class code"}, {m.Type.Code, "type code"}, {m.Format.Code, "format code"},
		{m.Confidentiality.Code, "confidentiality code"}, {m.FacilityType.Code, "healthcare facility type code"},
		{m.PracticeSetting.Code, "practice setting code"}, {m.ContentType.Code, "content type code"}} {
		if strings.TrimSpace(f.v) == "" {
			missing = append(missing, f.name)
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("%w: %s", ErrIncomplete, strings.Join(missing, ", "))
	}
	if !cxOK(m.PatientID) {
		return nil, fmt.Errorf("%w: the patient ID %q is not id^^^&OID&ISO", ErrIncomplete, m.PatientID)
	}
	if m.SourcePatientID == "" {
		m.SourcePatientID = m.PatientID
	}
	if m.UniqueID == "" {
		m.UniqueID = UUIDOID()
	}
	if m.SetUniqueID == "" {
		m.SetUniqueID = UUIDOID()
	}
	if m.Language == "" {
		m.Language = "en-US"
	}
	if m.CreationTime.IsZero() {
		m.CreationTime = now
	}
	const doc, ss = "Document01", "SubmissionSet01"
	dtm := func(t time.Time) string { return t.UTC().Format("20060102150405") }
	de := Object{ID: doc, ObjectType: TypeStableDocument, MimeType: m.MimeType, Name: Intl(m.Title),
		Slots: []Slot{{Name: "creationTime", Values: []string{dtm(m.CreationTime)}}, {Name: "languageCode", Values: []string{m.Language}},
			{Name: "sourcePatientId", Values: []string{m.SourcePatientID}}}}
	if !m.ServiceStart.IsZero() {
		de.SetSlot("serviceStartTime", dtm(m.ServiceStart))
	}
	if !m.ServiceStop.IsZero() {
		de.SetSlot("serviceStopTime", dtm(m.ServiceStop))
	}
	if m.AuthorPerson != "" || m.AuthorInstitution != "" {
		a := Classification{ID: "urn:uuid:" + newUUID(), ObjectType: TypeClassification, Scheme: SchemeDocAuthor, ClassifiedObject: doc}
		if m.AuthorPerson != "" {
			a.Slots = append(a.Slots, Slot{Name: "authorPerson", Values: []string{m.AuthorPerson}})
		}
		if m.AuthorInstitution != "" {
			a.Slots = append(a.Slots, Slot{Name: "authorInstitution", Values: []string{m.AuthorInstitution}})
		}
		de.Classifications = append(de.Classifications, a)
	}
	de.Classifications = append(de.Classifications,
		classification(SchemeClassCode, doc, m.Class), classification(SchemeConfidentiality, doc, m.Confidentiality),
		classification(SchemeFormatCode, doc, m.Format), classification(SchemeFacilityType, doc, m.FacilityType),
		classification(SchemePracticeSetting, doc, m.PracticeSetting), classification(SchemeTypeCode, doc, m.Type))
	de.ExternalIDs = []ExternalIdentifier{extID(IDDocPatientID, doc, m.PatientID, "XDSDocumentEntry.patientId"),
		extID(IDDocUniqueID, doc, m.UniqueID, "XDSDocumentEntry.uniqueId")}

	set := Object{ID: ss, ObjectType: TypeRegistryPackage, Name: Intl(m.Title),
		Slots: []Slot{{Name: "submissionTime", Values: []string{dtm(now)}}}}
	if len(m.IntendedRecipients) > 0 {
		set.SetSlot("intendedRecipient", m.IntendedRecipients...)
	}
	if m.AuthorPerson != "" || m.AuthorInstitution != "" {
		a := Classification{ID: "urn:uuid:" + newUUID(), ObjectType: TypeClassification, Scheme: SchemeSSAuthor, ClassifiedObject: ss}
		if m.AuthorPerson != "" {
			a.Slots = append(a.Slots, Slot{Name: "authorPerson", Values: []string{m.AuthorPerson}})
		}
		if m.AuthorInstitution != "" {
			a.Slots = append(a.Slots, Slot{Name: "authorInstitution", Values: []string{m.AuthorInstitution}})
		}
		set.Classifications = append(set.Classifications, a)
	}
	set.Classifications = append(set.Classifications, classification(SchemeContentType, ss, m.ContentType))
	set.ExternalIDs = []ExternalIdentifier{extID(IDSSUniqueID, ss, m.SetUniqueID, "XDSSubmissionSet.uniqueId"),
		extID(IDSSSourceID, ss, m.SourceID, "XDSSubmissionSet.sourceId"), extID(IDSSPatientID, ss, m.PatientID, "XDSSubmissionSet.patientId")}

	req := &SubmitObjectsRequest{}
	req.Objects.Extrinsic = []Object{de}
	req.Objects.Packages = []Object{set}
	req.Objects.Classifications = []Classification{{ID: "urn:uuid:" + newUUID(), ObjectType: TypeClassification,
		ClassificationNode: NodeSubmissionSet, ClassifiedObject: ss}}
	req.Objects.Associations = []Object{{ID: "urn:uuid:" + newUUID(), ObjectType: TypeAssociation, AssociationType: AssocHasMember,
		SourceObject: ss, TargetObject: doc, Slots: []Slot{{Name: "SubmissionSetStatus", Values: []string{"Original"}}}}}
	if m.Replaces != "" {
		req.Objects.Associations = append(req.Objects.Associations, Object{ID: "urn:uuid:" + newUUID(), ObjectType: TypeAssociation,
			AssociationType: AssocReplace, SourceObject: doc, TargetObject: m.Replaces})
	}
	return req, nil
}
