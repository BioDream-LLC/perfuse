// Package ncpdp reads and writes the NCPDP Telecommunication Standard.
//
// This is the format pharmacy claims travel in: a pharmacy sends a B1 billing request to a payer's
// switch and gets back an approval with what the patient owes, or a rejection with a code. It is the
// busiest transaction in American healthcare and it looks nothing like the rest of the formats here -
// no printable delimiters, a fixed-width header, and two-character field identifiers.
//
// Version D.0 is the one HIPAA mandates, so it is what this targets. The structure is the same across
// versions; the field lists differ.
//
// # What the wire looks like
//
// The separators are non-printable and they are start markers rather than separators, which is the
// detail most likely to be implemented wrongly:
//
//	0x1E  RS  starts a segment
//	0x1C  FS  starts a field
//	0x1D  GS  separates transactions within a transmission
//	0x02  STX starts a batch transaction or header/trailer
//	0x03  ETX ends one
//
// Because they start rather than separate, splitting on 0x1C leaves an empty first element that is not
// an empty field, and a transmission ending in 0x1C does not have a trailing empty field. Treating them
// as separators produces an off-by-one in every segment, which reads as a message where every value
// belongs to the field before it - and most of those values are still plausible.
//
// # What has not been verified
//
// The separator values, the fixed header layout and the field-start encoding are confirmed against
// published payer sheets and vendor documentation. No transmission from this package has been sent to a
// real pharmacy switch, and no response from one has been read. Field-level requirements vary per payer
// anyway - every processor publishes its own payer sheet saying which fields it wants - so this parses
// and builds the format without claiming to satisfy any particular processor.
package ncpdp

import "strings"

// Separator bytes. Named for what they start, not for what they sit between.
const (
	// SegmentStart introduces each segment.
	SegmentStart = 0x1E
	// FieldStart introduces each field within a segment.
	FieldStart = 0x1C
	// GroupStart separates transactions within one transmission. A transmission may carry up to four
	// claims for the same patient, which is why the header has a transaction count.
	GroupStart = 0x1D
	// STX starts a batch transaction or a batch header or trailer. Not used in a plain telecom transmission.
	STX = 0x02
	// ETX ends one.
	ETX = 0x03
)

// HeaderLength is the fixed size of the transaction header, in bytes.
//
// Fixed width with no separators inside it, so a header even one byte short does not fail to parse - it
// shifts every field after the gap. A truncated BIN turns the version into part of the BIN, the
// transaction code into part of the version, and a B1 billing request silently becomes something else.
// Which is why a short header is refused rather than padded.
const HeaderLength = 56

// Header is the transaction header segment.
//
// Every field is fixed width and space padded on the right. The widths are not negotiable and are not
// derivable from the content, so they are named here once.
type Header struct {
	// BIN is the Bank Identification Number, six digits, routing the transaction to a processor. Field 101-A1.
	BIN string
	// VersionRelease is the standard version, "D0" for the HIPAA-mandated D.0. Field 102-A2.
	VersionRelease string
	// TransactionCode says what is being asked. Field 103-A3. See the Transaction constants.
	TransactionCode string
	// ProcessorControlNumber further routes within the processor, and is usually specific to a plan. Field 104-A4.
	ProcessorControlNumber string
	// TransactionCount is how many transactions follow, one to four. Field 109-A9.
	TransactionCount string
	// ServiceProviderIDQualifier says what kind of identifier the pharmacy is using. Field 202-B2.
	ServiceProviderIDQualifier string
	// ServiceProviderID identifies the pharmacy, usually its NPI. Field 201-B1.
	ServiceProviderID string
	// DateOfService is CCYYMMDD, the date the prescription was dispensed. Field 401-D1.
	//
	// The date of service, not the date of transmission. A claim submitted on Monday for a Friday fill
	// carries Friday, and using today's date instead is how a refill-too-soon rejection appears for a
	// prescription that was filled on time.
	DateOfService string
	// SoftwareVendorCertificationID identifies the submitting software. Field 110-AK.
	SoftwareVendorCertificationID string
}

// Transaction codes. Field 103-A3.
const (
	// TxBilling is a claim for payment. B1.
	TxBilling = "B1"
	// TxReversal withdraws a previously accepted claim. B2.
	TxReversal = "B2"
	// TxRebill reverses and resubmits in one transaction. B3.
	TxRebill = "B3"
	// TxEligibility asks whether a patient is covered, without claiming. E1.
	TxEligibility = "E1"
	// TxPriorAuthRequestAndBilling requests a prior authorisation and bills together. P1.
	TxPriorAuthRequestAndBilling = "P1"
	// TxPriorAuthReversal withdraws one. P2.
	TxPriorAuthReversal = "P2"
	// TxPriorAuthInquiry asks about one. P3.
	TxPriorAuthInquiry = "P3"
	// TxPriorAuthRequestOnly requests without billing. P4.
	TxPriorAuthRequestOnly = "P4"
	// TxInformationReporting reports a dispensing that is not a claim. N1.
	TxInformationReporting = "N1"
	// TxInformationReversal withdraws one. N2.
	TxInformationReversal = "N2"
	// TxInformationRebill replaces one. N3.
	TxInformationRebill = "N3"
	// TxServiceBilling claims for a professional service rather than a product. S1.
	TxServiceBilling = "S1"
	// TxServiceReversal withdraws one. S2.
	TxServiceReversal = "S2"
	// TxServiceRebill replaces one. S3.
	TxServiceRebill = "S3"
)

// TransactionName describes a transaction code in words.
//
// Present so a log line or a message list says "billing" rather than "B1". Two-character codes are
// unreadable to anybody who does not already work in pharmacy claims, and the people looking at a
// stuck queue often do not.
func TransactionName(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case TxBilling:
		return "billing"
	case TxReversal:
		return "reversal"
	case TxRebill:
		return "rebill"
	case TxEligibility:
		return "eligibility verification"
	case TxPriorAuthRequestAndBilling:
		return "prior authorisation request and billing"
	case TxPriorAuthReversal:
		return "prior authorisation reversal"
	case TxPriorAuthInquiry:
		return "prior authorisation inquiry"
	case TxPriorAuthRequestOnly:
		return "prior authorisation request only"
	case TxInformationReporting:
		return "information reporting"
	case TxInformationReversal:
		return "information reporting reversal"
	case TxInformationRebill:
		return "information reporting rebill"
	case TxServiceBilling:
		return "service billing"
	case TxServiceReversal:
		return "service reversal"
	case TxServiceRebill:
		return "service rebill"
	default:
		return ""
	}
}

// Segment identifiers. Field 111-AM, the two characters that begin every segment.
const (
	SegPatient           = "01"
	SegPharmacyProvider  = "02"
	SegPrescriber        = "03"
	SegInsurance         = "04"
	SegCOB               = "05"
	SegWorkersComp       = "06"
	SegClaim             = "07"
	SegDUR               = "08"
	SegCoupon            = "09"
	SegCompound          = "10"
	SegPricing           = "11"
	SegPriorAuth         = "12"
	SegClinical          = "13"
	SegAdditionalDoc     = "14"
	SegFacility          = "15"
	SegNarrative         = "16"
	SegResponseMessage   = "20"
	SegResponseStatus    = "21"
	SegResponseClaim     = "22"
	SegResponsePricing   = "23"
	SegResponseDUR       = "24"
	SegResponseInsurance = "25"
	SegResponsePriorAuth = "26"
	SegResponseCOB       = "28"
	SegResponsePatient   = "29"
)

// segmentNames describes segments in words, for the same reason TransactionName exists.
var segmentNames = map[string]string{
	SegPatient:           "patient",
	SegPharmacyProvider:  "pharmacy provider",
	SegPrescriber:        "prescriber",
	SegInsurance:         "insurance",
	SegCOB:               "coordination of benefits",
	SegWorkersComp:       "workers compensation",
	SegClaim:             "claim",
	SegDUR:               "drug utilisation review",
	SegCoupon:            "coupon",
	SegCompound:          "compound",
	SegPricing:           "pricing",
	SegPriorAuth:         "prior authorisation",
	SegClinical:          "clinical",
	SegAdditionalDoc:     "additional documentation",
	SegFacility:          "facility",
	SegNarrative:         "narrative",
	SegResponseMessage:   "response message",
	SegResponseStatus:    "response status",
	SegResponseClaim:     "response claim",
	SegResponsePricing:   "response pricing",
	SegResponseDUR:       "response drug utilisation review",
	SegResponseInsurance: "response insurance",
	SegResponsePriorAuth: "response prior authorisation",
	SegResponseCOB:       "response coordination of benefits",
	SegResponsePatient:   "response patient",
}

// SegmentName describes a segment identifier in words, or returns "" if it is not one this knows.
func SegmentName(id string) string { return segmentNames[strings.TrimSpace(id)] }

// Field is one field within a segment.
//
// Held as an ordered list rather than a map, because several fields legitimately repeat - a claim can
// carry multiple other-payer amounts, multiple DUR conflicts and multiple compound ingredients - and a
// map would silently keep only the last of each. Which for a compound means dispensing one ingredient.
type Field struct {
	// ID is the two-character field identifier, such as "D1" for date of service.
	ID string
	// Value is the field contents, with padding trimmed.
	Value string
}

// Segment is one segment of a transaction.
type Segment struct {
	// ID is the two-character segment identifier. See the Seg constants.
	ID string
	// Fields are in wire order, including repeats.
	Fields []Field
}

// Get returns the first value for a field ID, and whether it was present.
//
// First rather than only, because a repeating field is not an error and a caller asking for a
// single-valued field should not have to care. Use All for fields that repeat.
func (s Segment) Get(id string) (string, bool) {
	for _, f := range s.Fields {
		if f.ID == id {
			return f.Value, true
		}
	}
	return "", false
}

// All returns every value for a field ID, in wire order.
func (s Segment) All(id string) []string {
	var out []string
	for _, f := range s.Fields {
		if f.ID == id {
			out = append(out, f.Value)
		}
	}
	return out
}

// Transaction is one claim within a transmission.
type Transaction struct {
	Segments []Segment
}

// Segment returns the first segment with this identifier, and whether it was present.
func (t Transaction) Segment(id string) (Segment, bool) {
	for _, s := range t.Segments {
		if s.ID == id {
			return s, true
		}
	}
	return Segment{}, false
}

// Message is a whole transmission: one header and up to four transactions.
type Message struct {
	Header Header
	// Transactions are in wire order. A transmission carrying four claims for one patient has four here.
	Transactions []Transaction
}
