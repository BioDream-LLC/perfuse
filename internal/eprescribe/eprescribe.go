// Package eprescribe reads and writes NCPDP SCRIPT, the standard prescriptions travel in.
//
// Named for what it does rather than for the standard, because internal/script is already the JavaScript
// engine and two packages called script in one tree is a mistake waiting to be made at an import line.
//
// SCRIPT is XML, unlike the Telecommunication Standard that carries the resulting claim. A prescriber
// sends a NEWRX to a pharmacy; the pharmacy sends back RXFILL to say what happened, REFREQ to ask for a
// refill, or RXCHG to ask for a change. Every one of those is a message here.
//
// # Where the danger is
//
// Two fields in a prescription are dangerous when misread rather than when missing.
//
// Substitutions is the first. A zero means substitution is permitted and a one means dispense as written.
// It reads backwards to anybody expecting a flag where one means yes, and getting it wrong dispenses a
// generic where the prescriber required the brand - or refuses a substitution that was allowed, which is
// the same error costing the patient money instead. So this package refuses to guess: an absent
// Substitutions element is an error rather than a default, because both defaults are wrong.
//
// Refill count is the second. A schedule II prescription may not be refilled at all, and a system
// carrying a refill count across from a non-controlled template produces a valid-looking prescription
// that a pharmacy must refuse.
//
// # What has not been verified
//
// The message structure follows the published SCRIPT element names. No message from this package has been
// sent through a real routing network such as Surescripts, and certification by one is a commercial
// process rather than a technical one. This parses and builds the format; it does not claim certification.
package eprescribe

import (
	"encoding/xml"
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/drug"
)

// MessageType is the kind of SCRIPT message.
type MessageType string

const (
	// NewRx is a new prescription, prescriber to pharmacy.
	NewRx MessageType = "NEWRX"
	// RxFill reports what the pharmacy did with one.
	RxFill MessageType = "RXFILL"
	// RefillRequest asks the prescriber to authorise more, pharmacy to prescriber.
	RefillRequest MessageType = "REFREQ"
	// RefillResponse answers one.
	RefillResponse MessageType = "REFRES"
	// ChangeRequest asks the prescriber to change something, usually because the plan will not cover it.
	ChangeRequest MessageType = "RXCHG"
	// ChangeResponse answers one.
	ChangeResponse MessageType = "CHGRES"
	// CancelRx withdraws a prescription.
	CancelRx MessageType = "CANRX"
	// CancelResponse answers one.
	CancelResponse MessageType = "CANRES"
	// HistoryRequest asks for a patient's medication history.
	HistoryRequest MessageType = "RXHREQ"
	// HistoryResponse answers one.
	HistoryResponse MessageType = "RXHRES"
	// Verify acknowledges receipt at the application level.
	Verify MessageType = "VERIFY"
	// StatusMessage reports transport-level progress.
	StatusMessage MessageType = "STATUS"
	// ErrorMessage reports a failure.
	ErrorMessage MessageType = "ERROR"
)

// TypeName describes a message type in words.
func TypeName(t MessageType) string {
	switch t {
	case NewRx:
		return "new prescription"
	case RxFill:
		return "fill notification"
	case RefillRequest:
		return "refill request"
	case RefillResponse:
		return "refill response"
	case ChangeRequest:
		return "change request"
	case ChangeResponse:
		return "change response"
	case CancelRx:
		return "cancellation"
	case CancelResponse:
		return "cancellation response"
	case HistoryRequest:
		return "medication history request"
	case HistoryResponse:
		return "medication history response"
	case Verify:
		return "verification"
	case StatusMessage:
		return "status"
	case ErrorMessage:
		return "error"
	default:
		return ""
	}
}

// Substitution is whether the pharmacy may dispense an equivalent.
//
// A named type rather than an int, because the wire values read backwards and an int would let a caller
// write 1 meaning yes. There is no zero value that is safe, so there is no zero value: Substitution must
// be set from the wire or the message is refused.
type Substitution int

const (
	// SubstitutionUnset is the zero value and is never valid. Present so that a message whose Substitutions
	// element was missing cannot be mistaken for one that permitted or forbade substitution.
	SubstitutionUnset Substitution = -1
	// SubstitutionAllowed is wire value 0. The pharmacy may dispense a therapeutic equivalent.
	SubstitutionAllowed Substitution = 0
	// SubstitutionNotAllowed is wire value 1, "dispense as written". The prescriber requires this exact product.
	SubstitutionNotAllowed Substitution = 1
)

// Allowed reports whether an equivalent may be dispensed.
func (s Substitution) Allowed() bool { return s == SubstitutionAllowed }

// String describes the value in words rather than as a digit, because the digit is the confusing part.
func (s Substitution) String() string {
	switch s {
	case SubstitutionAllowed:
		return "substitution allowed"
	case SubstitutionNotAllowed:
		return "dispense as written"
	default:
		return "substitution not stated"
	}
}

// ParseSubstitution reads the wire value.
func ParseSubstitution(s string) (Substitution, error) {
	switch strings.TrimSpace(s) {
	case "0":
		return SubstitutionAllowed, nil
	case "1":
		return SubstitutionNotAllowed, nil
	case "":
		return SubstitutionUnset, fmt.Errorf(
			"the prescription does not say whether substitution is allowed; there is no safe default, because " +
				"assuming it is allowed dispenses a generic where the prescriber required the brand, and assuming it " +
				"is not refuses a substitution that was permitted")
	default:
		return SubstitutionUnset, fmt.Errorf(
			"substitution value %q is not 0 or 1; 0 means substitution is allowed and 1 means dispense as written, "+
				"which reads backwards and is why this is not guessed at", s)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// The wire structures
// ──────────────────────────────────────────────────────────────────────────────
//
// Element names only, with no namespace given, so a message qualified with the SCRIPT namespace and one
// without both parse. Real senders differ on this and rejecting one of them would reject valid
// prescriptions over a prefix.

// Message is a SCRIPT message.
type Message struct {
	XMLName xml.Name `xml:"Message"`
	Version string   `xml:"version,attr,omitempty"`
	Release string   `xml:"release,attr,omitempty"`
	Header  Header   `xml:"Header"`
	Body    Body     `xml:"Body"`
}

// Header carries routing and identity.
type Header struct {
	To   Endpoint `xml:"To"`
	From Endpoint `xml:"From"`
	// MessageID identifies this message, and is what a VERIFY or ERROR quotes back.
	MessageID string `xml:"MessageID"`
	// RelatesToMessageID ties a response to what it answers.
	RelatesToMessageID string `xml:"RelatesToMessageID,omitempty"`
	SentTime           string `xml:"SentTime"`
	// PrescriberOrderNumber is the prescriber's own identifier for the prescription.
	PrescriberOrderNumber string `xml:"PrescriberOrderNumber,omitempty"`
	// RxReferenceNumber is the pharmacy's.
	RxReferenceNumber string         `xml:"RxReferenceNumber,omitempty"`
	SenderSoftware    SenderSoftware `xml:"SenderSoftware,omitempty"`
}

// Endpoint is a routing address. The qualifier says what kind of identifier it is.
type Endpoint struct {
	Qualifier string `xml:"Qualifier,attr,omitempty"`
	Value     string `xml:",chardata"`
}

// SenderSoftware identifies the sending system, which routing networks require.
type SenderSoftware struct {
	Developer      string `xml:"SenderSoftwareDeveloper,omitempty"`
	Product        string `xml:"SenderSoftwareProduct,omitempty"`
	VersionRelease string `xml:"SenderSoftwareVersionRelease,omitempty"`
}

// Body holds exactly one message. Every field is a pointer so an absent one is distinguishable from an
// empty one, which is what lets the type be determined from what is present.
type Body struct {
	NewRx           *Prescription `xml:"NewRx,omitempty"`
	RxFill          *Prescription `xml:"RxFill,omitempty"`
	RefillRequest   *Prescription `xml:"RefillRequest,omitempty"`
	RefillResponse  *Prescription `xml:"RefillResponse,omitempty"`
	ChangeRequest   *Prescription `xml:"RxChangeRequest,omitempty"`
	ChangeResponse  *Prescription `xml:"RxChangeResponse,omitempty"`
	CancelRx        *Prescription `xml:"CancelRx,omitempty"`
	CancelResponse  *Prescription `xml:"CancelRxResponse,omitempty"`
	HistoryRequest  *Prescription `xml:"RxHistoryRequest,omitempty"`
	HistoryResponse *Prescription `xml:"RxHistoryResponse,omitempty"`
	Verify          *Status       `xml:"Verify,omitempty"`
	Status          *Status       `xml:"Status,omitempty"`
	Error           *ErrorBody    `xml:"Error,omitempty"`
}

// Status is a VERIFY or STATUS body.
type Status struct {
	Code        string `xml:"Code,omitempty"`
	Description string `xml:"Description,omitempty"`
}

// ErrorBody is an ERROR body.
type ErrorBody struct {
	Code        string `xml:"Code,omitempty"`
	Description string `xml:"DescriptionCode,omitempty"`
	Detail      string `xml:"Description,omitempty"`
}

// Prescription is the shared body shape. Not every message uses every part.
type Prescription struct {
	Patient    Patient    `xml:"Patient"`
	Pharmacy   Pharmacy   `xml:"Pharmacy"`
	Prescriber Prescriber `xml:"Prescriber"`
	// Medication is what is being prescribed. Named MedicationPrescribed on the wire.
	Medication *Medication `xml:"MedicationPrescribed,omitempty"`
	// Dispensed is what was actually given out, on a fill notification.
	Dispensed *Medication `xml:"MedicationDispensed,omitempty"`
	// Response carries an approval or denial on a refill or change response.
	Response *ResponseBody `xml:"Response,omitempty"`
}

// ResponseBody is an approval or denial.
type ResponseBody struct {
	Approved            *struct{} `xml:"Approved,omitempty"`
	ApprovedWithChanges *struct{} `xml:"ApprovedWithChanges,omitempty"`
	Denied              *Denial   `xml:"Denied,omitempty"`
	ReferralToOther     *struct{} `xml:"Replace,omitempty"`
}

// Denial says why a request was refused.
type Denial struct {
	ReasonCode string `xml:"DenialReasonCode,omitempty"`
	Reason     string `xml:"DenialReason,omitempty"`
}

// Patient identifies the person.
type Patient struct {
	Name           Name            `xml:"Name"`
	Gender         string          `xml:"Gender,omitempty"`
	DateOfBirth    Date            `xml:"DateOfBirth"`
	Address        *Address        `xml:"Address,omitempty"`
	Identification *Identification `xml:"Identification,omitempty"`
}

// Prescriber identifies the clinician.
type Prescriber struct {
	Name           Name            `xml:"Name"`
	Address        *Address        `xml:"Address,omitempty"`
	Identification *Identification `xml:"Identification,omitempty"`
	// DEANumber authorises controlled substances. Its absence is what makes a controlled prescription invalid.
	DEANumber   string `xml:"DEANumber,omitempty"`
	NPI         string `xml:"NPI,omitempty"`
	PhoneNumber string `xml:"PhoneNumber,omitempty"`
}

// Pharmacy identifies where it goes.
type Pharmacy struct {
	BusinessName string   `xml:"BusinessName,omitempty"`
	NCPDPID      string   `xml:"NCPDPID,omitempty"`
	NPI          string   `xml:"NPI,omitempty"`
	Address      *Address `xml:"Address,omitempty"`
}

// Identification carries whatever identifiers a party has.
type Identification struct {
	NPI             string `xml:"NPI,omitempty"`
	DEANumber       string `xml:"DEANumber,omitempty"`
	MedicalRecordID string `xml:"MedicalRecordIdentificationNumberEHR,omitempty"`
	SocialSecurity  string `xml:"SocialSecurity,omitempty"`
}

// Name is a person's name.
type Name struct {
	Last   string `xml:"LastName,omitempty"`
	First  string `xml:"FirstName,omitempty"`
	Middle string `xml:"MiddleName,omitempty"`
	Suffix string `xml:"Suffix,omitempty"`
	Prefix string `xml:"Prefix,omitempty"`
}

// Address is a postal address.
type Address struct {
	Line1      string `xml:"AddressLine1,omitempty"`
	Line2      string `xml:"AddressLine2,omitempty"`
	City       string `xml:"City,omitempty"`
	State      string `xml:"State,omitempty"`
	PostalCode string `xml:"ZipCode,omitempty"`
}

// Date wraps a date, which SCRIPT nests rather than writing as text.
type Date struct {
	Date string `xml:"Date,omitempty"`
}

// Medication is the drug, quantity and directions.
type Medication struct {
	// Description is the human-readable drug name, and is what a pharmacist reads.
	Description string    `xml:"DrugDescription,omitempty"`
	Coded       *Coded    `xml:"DrugCoded,omitempty"`
	Quantity    *Quantity `xml:"Quantity,omitempty"`
	DaysSupply  string    `xml:"DaysSupply,omitempty"`
	// Substitutions is 0 for allowed and 1 for dispense as written. Read through ParseSubstitution.
	Substitutions string `xml:"Substitutions,omitempty"`
	// NumberOfRefills is how many further supplies are authorised.
	NumberOfRefills string `xml:"NumberOfRefills,omitempty"`
	Sig             *Sig   `xml:"Sig,omitempty"`
	WrittenDate     *Date  `xml:"WrittenDate,omitempty"`
	// DrugCoverageStatusCode and Note are carried through untouched.
	Note string `xml:"Note,omitempty"`
}

// Coded is the drug's coded identity.
type Coded struct {
	ProductCode          string `xml:"ProductCode,omitempty"`
	ProductCodeQualifier string `xml:"ProductCodeQualifier,omitempty"`
	// Strength and form, when sent.
	Strength    string `xml:"Strength,omitempty"`
	DosageForm  string `xml:"DrugDBCode,omitempty"`
	DEASchedule string `xml:"DEASchedule,omitempty"`
}

// Quantity is how much, with the unit it is measured in.
type Quantity struct {
	Value string `xml:"Value,omitempty"`
	// CodeListQualifier and UnitOfMeasure say what the number counts.
	CodeListQualifier string         `xml:"CodeListQualifier,omitempty"`
	UnitOfMeasure     *UnitOfMeasure `xml:"QuantityUnitOfMeasure,omitempty"`
}

// UnitOfMeasure is the coded unit.
type UnitOfMeasure struct {
	Code string `xml:"Code,omitempty"`
}

// Sig is the directions for use.
type Sig struct {
	// Text is the free-text directions, which is what actually gets printed on the label.
	Text string `xml:"SigText,omitempty"`
}

// Type returns which message this is, from what the body contains.
//
// Derived from the body rather than read from an element, because SCRIPT identifies the message by which
// element is present. A body containing more than one is refused: a message that is both a new
// prescription and a cancellation has no meaning, and picking the first would act on one of them.
func (m Message) Type() (MessageType, error) {
	var found []MessageType
	b := m.Body
	for _, c := range []struct {
		present bool
		t       MessageType
	}{
		{b.NewRx != nil, NewRx},
		{b.RxFill != nil, RxFill},
		{b.RefillRequest != nil, RefillRequest},
		{b.RefillResponse != nil, RefillResponse},
		{b.ChangeRequest != nil, ChangeRequest},
		{b.ChangeResponse != nil, ChangeResponse},
		{b.CancelRx != nil, CancelRx},
		{b.CancelResponse != nil, CancelResponse},
		{b.HistoryRequest != nil, HistoryRequest},
		{b.HistoryResponse != nil, HistoryResponse},
		{b.Verify != nil, Verify},
		{b.Status != nil, StatusMessage},
		{b.Error != nil, ErrorMessage},
	} {
		if c.present {
			found = append(found, c.t)
		}
	}
	switch len(found) {
	case 0:
		return "", fmt.Errorf("the message body is empty, so it does not say what kind of message this is")
	case 1:
		return found[0], nil
	default:
		names := make([]string, 0, len(found))
		for _, f := range found {
			names = append(names, string(f))
		}
		return "", fmt.Errorf(
			"the message body contains %s; a SCRIPT message is identified by which element is present, so more than "+
				"one has no meaning and acting on the first would carry out one of them", strings.Join(names, " and "))
	}
}

// Prescription returns the body's prescription, whichever message type it is.
func (m Message) Prescription() *Prescription {
	b := m.Body
	for _, p := range []*Prescription{
		b.NewRx, b.RxFill, b.RefillRequest, b.RefillResponse, b.ChangeRequest,
		b.ChangeResponse, b.CancelRx, b.CancelResponse, b.HistoryRequest, b.HistoryResponse,
	} {
		if p != nil {
			return p
		}
	}
	return nil
}

// Parse reads a SCRIPT message.
func Parse(data []byte) (Message, error) {
	if len(data) == 0 {
		return Message{}, fmt.Errorf("empty message")
	}
	var m Message
	if err := xml.Unmarshal(data, &m); err != nil {
		return Message{}, fmt.Errorf("this is not a SCRIPT message: %w", err)
	}
	if m.XMLName.Local != "Message" {
		return Message{}, fmt.Errorf(
			"the root element is <%s> and a SCRIPT message is <Message>", m.XMLName.Local)
	}
	if _, err := m.Type(); err != nil {
		return Message{}, err
	}
	return m, nil
}

// Schedule returns the DEA schedule of the prescribed drug, if it says.
func (p Prescription) Schedule() (drug.Schedule, error) {
	if p.Medication == nil || p.Medication.Coded == nil {
		return drug.ScheduleNone, nil
	}
	return drug.ParseSchedule(p.Medication.Coded.DEASchedule)
}
