// Package epa is NCPDP SCRIPT electronic prior authorization, the payer's side: PAInitiationRequest, PARequest,
// PAAppealRequest and PACancelRequest in, and their responses out.
//
// CMS-0062-P proposes that impacted payers support it from 1 October 2027, and Medicare Part D already requires it. A
// request is decided the way every prior authorization on this server is: it becomes a Da Vinci PAS request (a Claim, the
// MedicationRequest and the prescriber's answers as a QuestionnaireResponse) and goes to the same rules file, decision
// service and review queue. The formulary says which drugs need one and which questions to ask, from the same FHIR
// Questionnaire Da Vinci DTR fills in.
//
// NCPDP's schemas are licensed. Element names and order here follow the public Surescripts ePA companion guide and its
// examples; two elements the public material never shows (the reason inside Closed and inside Denied) follow the SCRIPT
// pattern used elsewhere and are named in the manual so a payer can check them against its own copy of the schema.
package epa

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// Kind is which ePA transaction a message is.
type Kind string

// The transactions.
const (
	InitiationRequest  Kind = "PAInitiationRequest"
	InitiationResponse Kind = "PAInitiationResponse"
	Request            Kind = "PARequest"
	Response           Kind = "PAResponse"
	AppealRequest      Kind = "PAAppealRequest"
	AppealResponse     Kind = "PAAppealResponse"
	CancelRequest      Kind = "PACancelRequest"
	CancelResponse     Kind = "PACancelResponse"
)

// Message is a SCRIPT message carrying one ePA transaction.
type Message struct {
	XMLName xml.Name   `xml:"Message"`
	Attrs   []xml.Attr `xml:",any,attr"`
	Header  Header     `xml:"Header"`
	Body    Body       `xml:"Body"`
}

// Header is the SCRIPT header.
type Header struct {
	To                 Endpoint `xml:"To"`
	From               Endpoint `xml:"From"`
	MessageID          string   `xml:"MessageID"`
	RelatesToMessageID string   `xml:"RelatesToMessageID,omitempty"`
	SentTime           string   `xml:"SentTime"`
}

// Endpoint is a routing address; Qualifier says what kind (D prescriber, P pharmacy, PY payer, ZZZ other).
type Endpoint struct {
	Qualifier string `xml:"Qualifier,attr,omitempty"`
	Value     string `xml:",chardata"`
}

// Body holds the transaction.
type Body struct {
	InitiationRequest  *PA `xml:"PAInitiationRequest"`
	InitiationResponse *PA `xml:"PAInitiationResponse"`
	Request            *PA `xml:"PARequest"`
	Response           *PA `xml:"PAResponse"`
	AppealRequest      *PA `xml:"PAAppealRequest"`
	AppealResponse     *PA `xml:"PAAppealResponse"`
	CancelRequest      *PA `xml:"PACancelRequest"`
	CancelResponse     *PA `xml:"PACancelResponse"`
}

// PA is the shared body of every ePA transaction: who, which drug, and the request or response.
type PA struct {
	PAReferenceID        string                 `xml:"PAReferenceID"`
	BenefitsCoordination []BenefitsCoordination `xml:"BenefitsCoordination"`
	Patient              Patient                `xml:"Patient"`
	Pharmacy             *Raw                   `xml:"Pharmacy"`
	Prescriber           Prescriber             `xml:"Prescriber"`
	Medication           Medication             `xml:"MedicationPrescribed"`
	Provider             *Raw                   `xml:"Provider"`
	Attachments          []Attachment           `xml:"Attachment"`
	Request              *RequestDetail         `xml:"Request"`
	Response             *ResponseDetail        `xml:"Response"`
	// PACaseID at the top level, where some senders put it on a cancel or an appeal.
	PACaseID string `xml:"PACaseID"`
}

// Raw keeps an element as it arrived, so a response echoes it unchanged.
type Raw struct {
	Inner string `xml:",innerxml"`
}

// BenefitsCoordination is the member's coverage as the prescriber's system knows it.
type BenefitsCoordination struct {
	PayerIdentification struct {
		PayerID                       string `xml:"PayerID"`
		ProcessorIdentificationNumber string `xml:"ProcessorIdentificationNumber"`
		IINNumber                     string `xml:"IINNumber"`
	} `xml:"PayerIdentification"`
	PayerName    string `xml:"PayerName"`
	CardholderID string `xml:"CardholderID"`
	GroupID      string `xml:"GroupID"`
	PBMMemberID  string `xml:"PBMMemberID"`
	Inner        string `xml:",innerxml"`
}

// Name is a person's name.
type Name struct {
	LastName   string `xml:"LastName"`
	FirstName  string `xml:"FirstName"`
	MiddleName string `xml:"MiddleName"`
}

// Patient is the member.
type Patient struct {
	Human struct {
		Identification struct {
			MedicareBeneficiaryID string `xml:"MedicareBeneficiaryID"`
			MedicalRecord         string `xml:"MedicalRecordIdentificationNumberEHR"`
		} `xml:"Identification"`
		Names struct {
			Name []Name `xml:"Name"`
		} `xml:"Names"`
		Gender      string `xml:"GenderAndSex>AdministrativeGender"`
		DateOfBirth string `xml:"DateOfBirth>Date"`
		Address     struct {
			Line1      string `xml:"AddressLine1"`
			City       string `xml:"City"`
			State      string `xml:"StateProvince"`
			PostalCode string `xml:"PostalCode"`
		} `xml:"Address"`
	} `xml:"HumanPatient"`
	Inner string `xml:",innerxml"`
}

// Prescriber is the clinician asking.
type Prescriber struct {
	NonVeterinarian struct {
		NPI   string `xml:"Identification>NPI"`
		Names struct {
			Name []Name `xml:"Name"`
		} `xml:"Names"`
	} `xml:"NonVeterinarian"`
	Inner string `xml:",innerxml"`
}

// Medication is the drug asked for.
type Medication struct {
	DrugDescription string `xml:"DrugDescription"`
	Product         struct {
		DrugCoded struct {
			NDC         string `xml:"NDC"`
			ProductCode struct {
				Code      string `xml:"Code"`
				Qualifier string `xml:"Qualifier"`
			} `xml:"ProductCode"`
		} `xml:"DrugCoded"`
	} `xml:"Product"`
	Quantity struct {
		Value string `xml:"Value"`
		Unit  string `xml:"QuantityUnitOfMeasure>Code"`
	} `xml:"Quantity"`
	DaysSupply string      `xml:"DaysSupply"`
	Diagnosis  []Diagnosis `xml:"Diagnosis"`
	Sig        string      `xml:"Sig>SigText"`
	Inner      string      `xml:",innerxml"`
}

// Diagnosis is a coded reason for the drug; Qualifier ABF is ICD-10-CM.
type Diagnosis struct {
	Qualifier   string `xml:"Qualifier"`
	Value       string `xml:"Value"`
	Description string `xml:"Description"`
}

// Attachment is a document the prescriber sent.
type Attachment struct {
	Source   string `xml:"AttachmentSource"`
	Data     string `xml:"AttachmentData"`
	MIMEType string `xml:"MIMEType"`
}

// RequestDetail is a request's Request element: the case it belongs to, the answered question set, and for an appeal or a
// cancellation their own parts.
type RequestDetail struct {
	Solicited        *Solicited   `xml:"SolicitedModel"`
	PACaseID         string       `xml:"PACaseID"`
	AppealCaseID     string       `xml:"AppealCaseID"`
	QuestionSet      *QuestionSet `xml:"QuestionSet"`
	PANote           string       `xml:"PANote"`
	CancelReasonCode string       `xml:"CancelReasonCode"`
}

// Solicited is a PARequest answering a question set the payer sent.
type Solicited struct {
	PACaseID    string       `xml:"PACaseID"`
	QuestionSet *QuestionSet `xml:"QuestionSet"`
}

// CaseID is the payer's case the request belongs to, wherever the sender put it.
func (p *PA) CaseID() string {
	if r := p.Request; r != nil {
		if r.Solicited != nil && strings.TrimSpace(r.Solicited.PACaseID) != "" {
			return strings.TrimSpace(r.Solicited.PACaseID)
		}
		if strings.TrimSpace(r.PACaseID) != "" {
			return strings.TrimSpace(r.PACaseID)
		}
	}
	return strings.TrimSpace(p.PACaseID)
}

// Answers is the question set the request carries answered, if any.
func (p *PA) Answers() *QuestionSet {
	if r := p.Request; r != nil {
		if r.Solicited != nil && r.Solicited.QuestionSet != nil {
			return r.Solicited.QuestionSet
		}
		return r.QuestionSet
	}
	return nil
}

// Member is the member ID the payer knows the patient by: the PBM member ID, else the cardholder ID.
func (p *PA) Member() (id, group string) {
	for _, b := range p.BenefitsCoordination {
		if v := strings.TrimSpace(b.PBMMemberID); v != "" {
			return v, strings.TrimSpace(b.GroupID)
		}
	}
	for _, b := range p.BenefitsCoordination {
		if v := strings.TrimSpace(b.CardholderID); v != "" {
			return v, strings.TrimSpace(b.GroupID)
		}
	}
	return "", ""
}

// ResponseDetail is a response's Response element.
type ResponseDetail struct {
	Status struct {
		Open     *Open     `xml:"Open"`
		Approved *Approved `xml:"Approved"`
		Denied   *Denied   `xml:"Denied"`
		Closed   *Closed   `xml:"Closed"`
	} `xml:"ResponseStatus"`
}

// Open is a case still open: a question set to answer, or under review.
type Open struct {
	PACaseID                string       `xml:"PACaseID"`
	DeadlineForReply        *DateTime    `xml:"DeadlineForReply,omitempty"`
	QuestionSet             *QuestionSet `xml:"QuestionSet,omitempty"`
	MoreInformationRequired *QuestionSet `xml:"MoreInformationRequired,omitempty"`
	PANote                  string       `xml:"PANote,omitempty"`
}

// Approved is an approval.
type Approved struct {
	PACaseID            string     `xml:"PACaseID"`
	AuthorizationNumber string     `xml:"AuthorizationNumber,omitempty"`
	AuthorizationPeriod *Period    `xml:"AuthorizationPeriod,omitempty"`
	PANote              string     `xml:"PANote,omitempty"`
	Appeal              *AppealInf `xml:"Appeal,omitempty"`
}

// Denied is a denial. DenialReason is the reason the prescriber reads, which CMS requires to be specific.
type Denied struct {
	PACaseID     string     `xml:"PACaseID"`
	DenialReason string     `xml:"DenialReason,omitempty"`
	PANote       string     `xml:"PANote,omitempty"`
	Appeal       *AppealInf `xml:"Appeal,omitempty"`
}

// Closed is a request closed without a decision; ReasonCode says why (CC: no prior authorization is needed).
type Closed struct {
	PACaseID   string `xml:"PACaseID,omitempty"`
	ReasonCode string `xml:"ReasonCode"`
	PANote     string `xml:"PANote,omitempty"`
}

// AppealInf says whether the decision can be appealed electronically.
type AppealInf struct {
	IsEAppealSupported string `xml:"IsEAppealSupported"`
}

// DateTime is SCRIPT's wrapped date-time.
type DateTime struct {
	DateTime string `xml:"DateTime"`
}

// Period is an authorization's effective and expiration dates.
type Period struct {
	EffectiveDate  DateTime `xml:"EffectiveDate"`
	ExpirationDate DateTime `xml:"ExpirationDate"`
}

// Kind returns which transaction the message carries. More than one, or none, is an error.
func (m *Message) Kind() (Kind, *PA, error) {
	var kinds []Kind
	var body *PA
	for _, c := range []struct {
		k Kind
		p *PA
	}{
		{InitiationRequest, m.Body.InitiationRequest}, {InitiationResponse, m.Body.InitiationResponse},
		{Request, m.Body.Request}, {Response, m.Body.Response}, {AppealRequest, m.Body.AppealRequest},
		{AppealResponse, m.Body.AppealResponse}, {CancelRequest, m.Body.CancelRequest}, {CancelResponse, m.Body.CancelResponse},
	} {
		if c.p != nil {
			kinds = append(kinds, c.k)
			body = c.p
		}
	}
	switch len(kinds) {
	case 0:
		return "", nil, fmt.Errorf("the Body carries no electronic prior authorization transaction")
	case 1:
		return kinds[0], body, nil
	}
	return "", nil, fmt.Errorf("the Body carries %d transactions (%v); a message carries one", len(kinds), kinds)
}

// Parse reads a SCRIPT ePA message. Element names are matched without their namespace, since senders differ on it.
func Parse(raw []byte) (*Message, error) {
	var m Message
	if err := xml.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("this is not a SCRIPT message: %w", err)
	}
	if m.XMLName.Local != "Message" {
		return nil, fmt.Errorf("the root element is <%s>; a SCRIPT message is <Message>", m.XMLName.Local)
	}
	if strings.TrimSpace(m.Header.MessageID) == "" {
		return nil, fmt.Errorf("the Header has no MessageID, which a response must quote back")
	}
	if _, _, err := m.Kind(); err != nil {
		return nil, err
	}
	return &m, nil
}

// version is the TransactionVersion the message declares, "" when it declares none.
func (m *Message) version() string {
	for _, a := range m.Attrs {
		if a.Name.Local == "TransactionVersion" || a.Name.Local == "version" {
			return a.Value
		}
	}
	return ""
}

// boolean writes a yes or no the way the message's version does: SCRIPT 2017071 wrote Y and N, 2023011 writes true and false.
func (m *Message) boolean(v bool) string {
	old := strings.HasPrefix(m.version(), "2017") || strings.HasPrefix(m.version(), "2016") || strings.HasPrefix(m.version(), "2015")
	switch {
	case old && v:
		return "Y"
	case old:
		return "N"
	case v:
		return "true"
	}
	return "false"
}
