package x12

import (
	"fmt"
	"strings"
	"time"
)

// Building a 278 inquiry (005010X215): asking a payer's utilization management system what it has on file for a member, which
// is how a provider (or a FHIR-to-X12 intermediary answering Da Vinci PAS's $inquire) finds an authorization whichever way it was
// asked for: by fax, through a portal, by phone, or in a 278 sent by someone else. PAS's $inquire "follows the X12 278 Inquiry
// rules" (its OperationDefinition).
//
// The shape follows the payers' published companion guides for 005010X215 (UnitedHealthcare's, Cigna's, Blue Cross Blue Shield
// of Massachusetts'): BHT02 28 (query); the UMO, requester and subscriber levels as in the request for review; one patient event
// with UM01, the authorization or administrative reference number when known (REF BB, REF NT), and the event dates (DTP AAH, or
// 435 for an admission review). The TR3 itself is X12's and not public; what is here is what those guides print.

// ServiceReviewInquiry is a 278 inquiry.
type ServiceReviewInquiry struct {
	Envelope
	// Payer is the utilization management organization (NM1*X3), with its payer ID.
	Payer Person `json:"payer"`
	// Provider is the requester (NM1*1P), with its NPI.
	Provider Person `json:"provider"`
	// Subscriber is the member (NM1*IL), with the member ID; DOB and Gender go in DMG when known. Payers match on the ID with a
	// name or a date of birth.
	Subscriber Person `json:"subscriber"`
	// Reference is BHT03; empty means generated.
	Reference string `json:"reference,omitempty"`
	// Trace is TRN02 in the patient event: the response repeats it.
	Trace string `json:"trace"`
	// Category is UM01: HS health services review (the default), AR admission review, SC specialty care review.
	Category string `json:"category,omitempty"`
	// AuthorizationNumber (REF*BB) and AdministrativeReference (REF*NT) name one review the payer already gave a number.
	AuthorizationNumber     string `json:"authorizationNumber,omitempty"`
	AdministrativeReference string `json:"administrativeReference,omitempty"`
	// From and To are CCYYMMDD: the event dates to search. One date alone sends D8; both send RD8.
	From string `json:"from,omitempty"`
	To   string `json:"to,omitempty"`
}

// BuildServiceReviewInquiry renders a 278 inquiry, or says what is missing.
func BuildServiceReviewInquiry(req ServiceReviewInquiry) ([]byte, error) {
	missing := req.missing()
	need := func(v, what string) {
		if strings.TrimSpace(v) == "" {
			missing = append(missing, what)
		}
	}
	need(req.Payer.LastName, "payer name")
	need(req.Payer.ID, "payer ID")
	need(req.Provider.LastName, "provider name")
	need(req.Provider.ID, "provider NPI")
	need(req.Subscriber.ID, "subscriber member ID")
	need(req.Trace, "a trace number")
	if strings.TrimSpace(req.Subscriber.LastName) == "" && strings.TrimSpace(req.Subscriber.DOB) == "" {
		// The guides match a member on the ID with a name or a date of birth, never the ID alone.
		missing = append(missing, "subscriber name or date of birth")
	}
	if req.AuthorizationNumber == "" && req.AdministrativeReference == "" && req.From == "" {
		// A query with no number must say when: the guides refuse one without event dates.
		missing = append(missing, "an authorization number or the event dates")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("a 278 inquiry needs %s", strings.Join(missing, ", "))
	}
	if !validNPI(req.Provider.ID) {
		return nil, fmt.Errorf("provider NPI %q fails the NPI check digit", req.Provider.ID)
	}
	for _, d := range []string{req.From, req.To, req.Subscriber.DOB} {
		if d != "" && !isCCYYMMDD(d) {
			return nil, fmt.Errorf("date %q is not CCYYMMDD", d)
		}
	}
	if req.From != "" && req.To != "" && req.To < req.From {
		return nil, fmt.Errorf("the dates %s to %s run backwards", req.From, req.To)
	}
	category := orDefault(req.Category, "HS")
	switch category {
	case "HS", "AR", "SC":
	default:
		return nil, fmt.Errorf("request category %q is not HS, AR or SC", category)
	}
	if err := checkDelimiters(req.SenderID, req.ReceiverID, req.Payer.LastName, req.Payer.ID, req.Provider.LastName,
		req.Provider.FirstName, req.Provider.ID, req.Subscriber.LastName, req.Subscriber.FirstName, req.Subscriber.ID,
		req.Subscriber.DOB, req.Subscriber.Gender, req.Reference, req.Trace, req.AuthorizationNumber,
		req.AdministrativeReference); err != nil {
		return nil, err
	}

	return req.wrap("HI", "005010X215", "278", func(b *builder, st string, now time.Time) {
		ref := req.Reference
		if ref == "" {
			ref = st + now.Format("150405")
		}
		// 0007: request for review and response. 28: query.
		b.add("BHT", "0007", "28", ref, now.Format("20060102"), now.Format("1504"))
		b.add("HL", "1", "", "20", "1")
		b.add("NM1", "X3", "2", req.Payer.LastName, "", "", "", "", "PI", req.Payer.ID)
		b.add("HL", "2", "1", "21", "1")
		b.add("NM1", "1P", personOrOrg(req.Provider), req.Provider.LastName, req.Provider.FirstName, "", "", "", "XX", req.Provider.ID)
		b.add("HL", "3", "2", "22", "1")
		b.add("NM1", "IL", "1", req.Subscriber.LastName, req.Subscriber.FirstName, "", "", "", "MI", req.Subscriber.ID)
		if req.Subscriber.DOB != "" {
			b.add("DMG", "D8", req.Subscriber.DOB, req.Subscriber.Gender)
		}
		b.add("HL", "4", "3", "EV", "0")
		b.add("TRN", "1", req.Trace, "9"+padID(req.SenderID))
		b.add("UM", category)
		if req.AuthorizationNumber != "" {
			b.add("REF", "BB", req.AuthorizationNumber)
		}
		if req.AdministrativeReference != "" {
			b.add("REF", "NT", req.AdministrativeReference)
		}
		if req.From != "" {
			qualifier := "AAH"
			if category == "AR" {
				qualifier = "435"
			}
			if req.To != "" && req.To != req.From {
				b.add("DTP", qualifier, "RD8", req.From+"-"+req.To)
			} else {
				b.add("DTP", qualifier, "D8", req.From)
			}
		}
	}), nil
}
