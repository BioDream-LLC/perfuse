package x12

import (
	"fmt"
)

// The 277 claim status transaction, including the request for additional information.
//
// # Why it belongs beside the 275
//
// A 275 attachment does not usually arrive unprompted. The sequence is: a provider sends an 837 claim, the payer decides it needs
// documentation, the payer sends a 277 asking for it, and the provider answers with a 275. Supporting the answer and not the question
// leaves an integration engine able to send documentation nobody asked for, which is the half that does not reduce anybody's
// outstanding receivables.
//
// The same transaction carries ordinary claim status - accepted, pending, denied, and why - which is what a billing office reads every
// morning. One transaction serves both because a request for documentation is a status: the claim is not being processed, and this is
// what would unblock it.
//
// # The structure, and how it differs from an 835
//
// An 835 groups claims under LX headers. A 277 uses HL segments, which are an explicit hierarchy with parent pointers:
//
//	ISA/GS/ST(277) — envelope
//	  BHT          — beginning of hierarchical transaction
//	  HL(20)       — information source: the payer
//	    NM1        — payer name
//	    HL(21)     — information receiver: whoever asked
//	      NM1
//	      HL(19)   — service provider
//	        NM1
//	        HL(22) — subscriber, and HL(23) a dependent when the patient is not the subscriber
//	          NM1
//	          TRN  — claim trace
//	          STC  — status: the codes saying what happened and what is needed
//	          REF  — claim identifiers
//	          DTP  — service dates
//	          SVC  — a service line, with its own STC when the status differs per line
//	SE/GE/IEA
//
// Parsed into a flat list of claim statuses rather than a tree. The hierarchy exists to avoid repeating the payer's name on every
// claim, not because a reader needs it: what a billing office asks is "what happened to this claim", and answering that from a tree
// means walking up three levels to find out whose claim it was. So each status carries its own participants, filled in from whichever
// HL loop it sits under.
//
// # The status codes are the point
//
// STC01 is a composite: a category code, a status code, and an entity code. The category says broadly what state the claim is in and
// the status says specifically why. A request for documentation is category R4 or a status code in the 'additional information
// requested' family, and recognising it is the difference between a billing office seeing "pending" and seeing "they want the operative
// note".

// ClaimStatus is one claim's status from a 277.
type ClaimStatus struct {
	// TraceNumber is TRN02, which ties this back to the claim as submitted.
	TraceNumber string

	// PayerClaimControlNumber is the payer's own identifier for the claim, from a REF with qualifier 1K.
	PayerClaimControlNumber string

	// ProviderClaimNumber is the provider's identifier, from a REF with qualifier D9 or BLT. Kept separately because when the two
	// disagree, which they do, a billing office needs to search on its own number rather than the payer's.
	ProviderClaimNumber string

	// Category, Code and Entity are the three parts of STC01.
	Category string
	Code     string
	Entity   string

	// StatusDate is STC02 and Amount is STC04, what was paid or allowed when anything was.
	StatusDate string
	Amount     string

	// Description is STC12, free text the payer wrote. Often the only human-readable explanation in the transaction and frequently
	// the only thing that says which document is wanted.
	Description string

	// ServiceDateFrom and ServiceDateTo are the dates of care, from DTP.
	ServiceDateFrom string
	ServiceDateTo   string

	// Payer, Receiver, Provider and Patient are filled in from the HL loops this status sits under.
	Payer    Party
	Receiver Party
	Provider Party
	Patient  Party

	// Lines are per-service-line statuses, present when the payer answered at line level.
	Lines []ServiceLineStatus
}

// ServiceLineStatus is one service line's status.
type ServiceLineStatus struct {
	// ProcedureCode is SVC01, as a composite: the qualifier and the code.
	ProcedureCode string

	// ChargeAmount is SVC02 and PaidAmount is SVC03.
	ChargeAmount string
	PaidAmount   string

	Category    string
	Code        string
	Entity      string
	Description string
}

// StatusReport is one parsed 277 transaction set.
type StatusReport struct {
	// ReferenceID is BHT03, the sender's identifier for this transaction.
	ReferenceID string

	// Date and Time are BHT04 and BHT05.
	Date string
	Time string

	// Purpose is BHT06. "DG" marks a response to a request; a 277 that is unsolicited carries something else, and the difference
	// decides whether a billing office is reading an answer or a new demand.
	Purpose string

	Statuses []ClaimStatus
}

// hierarchicalLevel codes used by the 277.
const (
	levelInformationSource   = "20"
	levelInformationReceiver = "21"
	levelServiceProvider     = "19"
	levelSubscriber          = "22"
	levelDependent           = "23"
)

// ParseStatusReport reads the first 277 transaction set in an interchange.
func (m *Message) ParseStatusReport() (*StatusReport, error) {
	segs := m.transactionSetOfType("277")
	if segs == nil {
		return nil, fmt.Errorf("this interchange contains no 277 transaction set")
	}

	report := &StatusReport{}

	// The participants currently in scope, replaced as each HL loop opens. A 277 states the payer once and then relies on position
	// for every claim after it, so these are carried forward deliberately - and reset when a shallower level opens, because a new
	// service provider means the previous provider's claims are finished.
	var payer, receiver, provider, patient Party
	var level string
	var current *ClaimStatus
	var currentLine *ServiceLineStatus

	for _, s := range segs {
		switch s.ID {
		case "BHT":
			report.ReferenceID = s.Element(3).String()
			report.Date = s.Element(4).String()
			report.Time = s.Element(5).String()
			report.Purpose = s.Element(6).String()

		case "HL":
			level = s.Element(3).String()
			// Opening a level invalidates everything below it. Without this, a claim under the second provider in a transaction
			// would be reported with the first provider's name - which looks entirely plausible and sends somebody to ring the
			// wrong practice.
			switch level {
			case levelInformationSource:
				payer, receiver, provider, patient = Party{}, Party{}, Party{}, Party{}
			case levelInformationReceiver:
				receiver, provider, patient = Party{}, Party{}, Party{}
			case levelServiceProvider:
				provider, patient = Party{}, Party{}
			case levelSubscriber, levelDependent:
				patient = Party{}
			}
			current = nil
			currentLine = nil

		case "NM1":
			party := Party{
				EntityType:  s.Element(2).String(),
				Name:        s.Element(3).String(),
				FirstName:   s.Element(4).String(),
				IDQualifier: s.Element(8).String(),
				IDCode:      s.Element(9).String(),
			}
			switch level {
			case levelInformationSource:
				payer = party
			case levelInformationReceiver:
				receiver = party
			case levelServiceProvider:
				provider = party
			case levelSubscriber, levelDependent:
				patient = party
			}

		case "TRN":
			report.Statuses = append(report.Statuses, ClaimStatus{
				TraceNumber: s.Element(2).String(),
				Payer:       payer,
				Receiver:    receiver,
				Provider:    provider,
				Patient:     patient,
			})
			current = &report.Statuses[len(report.Statuses)-1]
			currentLine = nil

		case "STC":
			category, code, entity := statusParts(s.Element(1))
			switch {
			case currentLine != nil:
				currentLine.Category = category
				currentLine.Code = code
				currentLine.Entity = entity
				currentLine.Description = s.Element(12).String()
			case current != nil:
				current.Category = category
				current.Code = code
				current.Entity = entity
				current.StatusDate = s.Element(2).String()
				current.Amount = s.Element(4).String()
				current.Description = s.Element(12).String()
			}

		case "REF":
			if current == nil {
				continue
			}
			switch s.Element(1).String() {
			case "1K":
				current.PayerClaimControlNumber = s.Element(2).String()
			case "D9", "BLT":
				current.ProviderClaimNumber = s.Element(2).String()
			}

		case "DTP":
			if current == nil {
				continue
			}
			// DTP02 says whether DTP03 is one date or a range, and a range is written from-to with a separator. Reported as two
			// fields because "when was the care" is answered differently by each, and collapsing them loses which was sent.
			from, to := serviceDates(s.Element(2).String(), s.Element(3).String())
			if current.ServiceDateFrom == "" {
				current.ServiceDateFrom = from
				current.ServiceDateTo = to
			}

		case "SVC":
			if current == nil {
				continue
			}
			current.Lines = append(current.Lines, ServiceLineStatus{
				ProcedureCode: s.Element(1).String(),
				ChargeAmount:  s.Element(2).String(),
				PaidAmount:    s.Element(3).String(),
			})
			currentLine = &current.Lines[len(current.Lines)-1]
		}
	}

	return report, nil
}

// IsStatusReport reports whether the interchange carries a 277.
func (m *Message) IsStatusReport() bool { return m.transactionSetOfType("277") != nil }

// RequestsAdditionalInformation reports whether any status is asking for documentation.
//
// The question an integration engine is actually asked about a 277. A billing office wants "does this need something from us", and the
// answer decides whether the transaction is filed or put in front of a person - which is worth deciding once, here, rather than in
// every channel that reads one.
//
// Recognised by the status category rather than by the free text, because the text is whatever the payer wrote. R4 is the category for
// a request, and the pending categories accompanied by an entity or status code in the documentation family are how several payers say
// the same thing.
func (r *StatusReport) RequestsAdditionalInformation() bool {
	for _, s := range r.Statuses {
		if s.RequestsAdditionalInformation() {
			return true
		}
	}

	return false
}

// RequestsAdditionalInformation reports whether this one status is asking for documentation.
func (s ClaimStatus) RequestsAdditionalInformation() bool {
	// R4 is "not our claim, forwarded" in some guides and a documentation request in others, so the category alone is not enough and
	// is deliberately not used on its own here. These are the status codes that unambiguously mean a document is wanted.
	switch s.Code {
	case
		// Additional information requested, in the families payers actually send.
		"226", // Entity not found - often accompanied by a documentation request
		"227", // Request for additional information
		"233", // Request for medical records
		"252", // An attachment or other documentation is required to adjudicate
		"287": // Claim pending, documentation requested
		return true
	}

	return false
}

// statusParts splits STC01 into its three components.
func statusParts(e Element) (category, code, entity string) {
	// A composite, so the components are separated by the component separator rather than the element separator. Read through
	// Component so a partner who sends the parts in one element without separators still yields a category rather than nothing.
	// Components count from 1 in this package, deliberately, so that Element(1).Component(1) is always safe. Counting from 0 here
	// read the category as empty and the status code as the category, which reported every documentation request as an unknown state.
	if e.ComponentCount() > 1 {
		return e.Component(1), e.Component(2), e.Component(3)
	}

	return e.String(), "", ""
}

// serviceDates splits a DTP date value into a from and a to.
func serviceDates(qualifier, value string) (from, to string) {
	// RD8 is the qualifier for a range, written as two dates joined by a hyphen. Any other qualifier is one date, and returning it
	// as both would state a range the payer did not send.
	if qualifier == "RD8" {
		for i := 0; i < len(value); i++ {
			if value[i] == '-' {
				return value[:i], value[i+1:]
			}
		}
	}

	return value, ""
}
