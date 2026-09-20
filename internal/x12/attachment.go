package x12

import (
	"fmt"
	"strings"
)

// The 275 attachment transaction: clinical documentation sent to support a claim.
//
// # Why this matters and when
//
// CMS-0053-F adopts X12N 275 version 006020, together with HL7 attachment implementation guides, as HIPAA standards for health care
// claims attachments. Compliance is required by 26 May 2028. That makes this the one dated federal obligation an integration engine
// cannot decline: a HIPAA transaction standard applies to every covered entity doing the transaction, not only to those who chose to.
//
// The business problem it solves is old and expensive. A payer receives a claim, decides it needs the operative note or the imaging
// report, and asks for it - historically by post or fax, with the claim ageing while somebody finds the document. The 275 carries that
// document electronically, tied to the claim it supports.
//
// # The structure, simplified
//
//	ISA/GS/ST(275) — envelope
//	  BGN          — transaction identification and date
//	  NM1 loops    — who is sending, who is receiving, and about which patient
//	  LX           — assigned number, one per attachment being sent
//	    TRN        — trace number, which is how the payer matches this to the request
//	    REF        — reference identifiers: the claim control number and the attachment control number
//	    DTP        — dates, including the date of the service being documented
//	    CAT        — category of the information, so the payer knows what kind of document arrived
//	    EFI        — electronic format identification: what the payload is
//	    BIN        — the payload itself, as raw bytes
//	SE/GE/IEA      — envelope close
//
// # The part that is genuinely different from every other transaction here
//
// The BIN segment holds arbitrary bytes, and arbitrary bytes contain delimiters. Everything else in X12 can be found by scanning for
// the segment terminator; an attachment cannot. That is handled in binary.go, and it had to be fixed before this file could exist -
// before it, a PDF containing a tilde was cut into fragments and every segment after it was misaligned, with no error reported because
// each fragment is a structurally valid segment.
//
// # What this package does and does not do with the payload
//
// It reports the bytes and what the sender says they are. It does not decode them: an attachment may be a PDF, a TIFF, a CDA document
// or an HL7 message, and decoding all of those to move one of them would be a large body of format-specific work whose only purpose
// here would be to make the binary bigger. A channel that needs the content can hand the bytes to the packages that already read those
// formats - cda, hl7v3, hl7xml - which is the point of reporting them intact.

// Attachment is one parsed 275 transaction set: one or more documents supporting a claim.
type Attachment struct {
	// TransactionID is BGN02, the sender's identifier for this transaction.
	TransactionID string

	// Date and Time are BGN03 and BGN04, when the sender created it.
	Date string
	Time string

	// Purpose is BGN01, the transaction set purpose code. 11 means an original submission and 06 means a confirmation, which is the
	// difference between a document arriving and an acknowledgement of one.
	Purpose string

	// Submitter and Receiver are the two ends, as named in the NM1 loops.
	Submitter Party
	Receiver  Party

	// Patient is who the documentation is about. Often present, and legitimately absent when the attachment is identified only by
	// the claim it supports.
	Patient Party

	// Documents is one entry per LX loop: one attachment each.
	Documents []AttachedDocument
}

// Party is one named participant.
type Party struct {
	// Name is NM103 for an organisation, or the surname for a person.
	Name string

	// FirstName is NM104, empty for an organisation.
	FirstName string

	// IDCode is NM109, the identifier, and IDQualifier is NM108 saying what kind it is - a national provider identifier, a payer
	// identifier, a member number. The qualifier is kept because the same digits mean different things under different qualifiers.
	IDCode      string
	IDQualifier string

	// EntityType is NM102: 1 for a person, 2 for an organisation.
	EntityType string
}

// AttachedDocument is one document inside a 275.
type AttachedDocument struct {
	// Number is LX01, the sender's sequence number within this transaction.
	Number string

	// TraceNumber is TRN02. This is how a payer matches an attachment to the request that asked for it, so an attachment sent
	// without one is very likely to be filed and never associated with the claim.
	TraceNumber string

	// ClaimControlNumber is the payer's identifier for the claim being supported, from a REF with qualifier BLT or similar. Empty
	// when the attachment is matched by trace number alone.
	ClaimControlNumber string

	// AttachmentControlNumber is the identifier the provider put on the claim to say "documentation follows under this number". The
	// single most important field for reconciliation: without it a payer holding an unsolicited attachment cannot tell which claim
	// it belongs to.
	AttachmentControlNumber string

	// ServiceDate is the date of the care the document describes, from DTP.
	ServiceDate string

	// Category is CAT01, what kind of information this is.
	Category string

	// FormatQualifier and Format are EFI01 and EFI02: what the payload is, as the sender describes it.
	FormatQualifier string
	Format          string

	// Payload is the bytes exactly as they arrived, delimiters and all. Not decoded - see the package comment.
	Payload []byte

	// DeclaredLength is what BIN01 said. Kept alongside the payload rather than checked and discarded, because a mismatch between
	// the two is the interesting case and the parser refuses it before this point - so a caller seeing both agree knows it.
	DeclaredLength int
}

// ParseAttachment reads the first 275 transaction set in an interchange.
//
// One transaction set rather than all of them, matching ParseRemittance: an interchange may carry several, and a caller wanting each
// separately should split first. Returns an error when there is no 275 in the interchange, because a caller asking for an attachment
// and receiving an empty one would have no way to tell that from an attachment with no documents.
func (m *Message) ParseAttachment() (*Attachment, error) {
	segs := m.transactionSetOfType("275")
	if segs == nil {
		return nil, fmt.Errorf("this interchange contains no 275 transaction set")
	}

	a := &Attachment{}

	// The current LX loop. Documents are appended as each LX is met, and the fields that follow attach to the last one - which is
	// how X12 loops work and why a stray segment before the first LX is ignored rather than assigned to document zero.
	var current *AttachedDocument

	// NM1 entity identifier codes for the two ends and the patient. 41 is the submitter, 40 the receiver, QC the patient.
	const (
		entitySubmitter = "41"
		entityReceiver  = "40"
		entityPatient   = "QC"
	)

	for _, s := range segs {
		switch s.ID {
		case "BGN":
			a.Purpose = s.Element(1).String()
			a.TransactionID = s.Element(2).String()
			a.Date = s.Element(3).String()
			a.Time = s.Element(4).String()

		case "NM1":
			party := Party{
				EntityType:  s.Element(2).String(),
				Name:        s.Element(3).String(),
				FirstName:   s.Element(4).String(),
				IDQualifier: s.Element(8).String(),
				IDCode:      s.Element(9).String(),
			}
			switch s.Element(1).String() {
			case entitySubmitter:
				a.Submitter = party
			case entityReceiver:
				a.Receiver = party
			case entityPatient:
				a.Patient = party
			}

		case "LX":
			a.Documents = append(a.Documents, AttachedDocument{Number: s.Element(1).String()})
			current = &a.Documents[len(a.Documents)-1]

		case "TRN":
			if current != nil {
				current.TraceNumber = s.Element(2).String()
			}

		case "REF":
			if current == nil {
				continue
			}
			// The qualifier decides which identifier this is, and the two matter for different reasons: one matches the claim, the
			// other matches the payer's request for documentation.
			switch s.Element(1).String() {
			case "BLT", "D9", "1K":
				current.ClaimControlNumber = s.Element(2).String()
			case "XX9", "EJ":
				current.AttachmentControlNumber = s.Element(2).String()
			}

		case "DTP":
			if current != nil && current.ServiceDate == "" {
				// DTP03 holds the date, DTP02 says what format it is in. The value is reported as sent rather than reformatted,
				// because a range and a single date are both legal here and collapsing them would lose which was meant.
				current.ServiceDate = s.Element(3).String()
			}

		case "CAT":
			if current != nil {
				current.Category = s.Element(1).String()
			}

		case "EFI":
			if current != nil {
				current.FormatQualifier = s.Element(1).String()
				current.Format = s.Element(2).String()
			}

		case "BIN":
			if current == nil {
				continue
			}
			current.DeclaredLength = intOrZero(s.Element(1).String())
			// Copied rather than referenced. The message holds the whole interchange, and handing a caller a slice of it means an
			// attachment keeps a megabyte-scale buffer alive for as long as anybody holds the document.
			payload := s.Element(2).Bytes()
			current.Payload = append([]byte(nil), payload...)
		}
	}

	return a, nil
}

// IsAttachment reports whether the interchange carries a 275.
func (m *Message) IsAttachment() bool { return m.transactionSetOfType("275") != nil }

// transactionSetOfType returns the segments of the first transaction set with the given identifier, ST through SE.
//
// Shared by the transaction parsers rather than each finding its own, because "the segments between this ST and its SE" is one idea and
// three copies of it would eventually disagree about whether the SE is included.
func (m *Message) transactionSetOfType(id string) []Segment {
	inSet := false
	var out []Segment

	for _, s := range m.segs {
		switch {
		case s.ID == "ST":
			inSet = s.Element(1).String() == id
			if inSet {
				out = append(out, s)
			}

		case s.ID == "SE" && inSet:
			out = append(out, s)

			return out

		case inSet:
			out = append(out, s)
		}
	}

	// An ST with no SE. Returned rather than discarded, so a truncated interchange reports the fields it did carry instead of looking
	// like an interchange with no attachment in it at all.
	return out
}

// intOrZero reads a decimal integer, returning zero for anything else.
//
// Zero rather than an error because the caller has already been told the length is consistent: the parser refuses a binary segment
// whose declared length does not match the data, so by the time this runs a bad number cannot be reached.
func intOrZero(s string) int {
	n := 0
	for _, r := range strings.TrimSpace(s) {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}

	return n
}
