package x12

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Building a 278 request (005010X217): the transaction a provider, or a FHIR-to-X12 intermediary, sends a payer's utilisation
// management system to ask for a prior authorisation. Da Vinci PAS was designed around it: a PAS Claim converts to a 278, the
// payer's UM system answers in 278, and the answer converts back. A payer whose decisions are made in an X12 UM system needs
// exactly this to put a FHIR prior authorisation API in front of it.

// ServiceReviewRequest is a 278 request.
type ServiceReviewRequest struct {
	Envelope
	// Payer is the utilisation management organisation (NM1*X3), with its payer ID.
	Payer Person `json:"payer"`
	// Provider is the requester (NM1*1P), with its NPI.
	Provider Person `json:"provider"`
	// Subscriber is the member (NM1*IL), with the member ID; DOB and Gender go in DMG when known.
	Subscriber Person `json:"subscriber"`
	// Reference is BHT03, the submitter's identifier for this request. Empty means generated.
	Reference string `json:"reference,omitempty"`
	// Events are the services asked for, one patient event (2000E) each.
	Events []ReviewRequestEvent `json:"events"`
}

// ReviewRequestEvent is one service asked for.
type ReviewRequestEvent struct {
	// Trace is TRN02 in the patient event loop: the 278 response repeats it, which is how its answer is matched to this event.
	Trace string `json:"trace"`
	// Category is UM01: HS health services review (the default), AR admission review, SC specialty care review.
	Category string `json:"category,omitempty"`
	// CertificationType is UM02: I initial (the default), 3 cancel, 4 extension, R renewal, S revised.
	CertificationType string `json:"certificationType,omitempty"`
	// ServiceType is UM03, an X12 service type code (1365): 3 consultation, 2 surgical, 73 diagnostic medical.
	ServiceType string `json:"serviceType,omitempty"`
	// LevelOfService is UM06 (code list 1338): E elective, U urgent, 03 emergency. Empty sends none, unless Urgent.
	LevelOfService string `json:"levelOfService,omitempty"`
	// Urgent puts U (urgent) in UM06 when LevelOfService is empty, which is what makes a UM system work it as expedited. 03 is
	// emergency, not urgent: an urgent request sent as 03 is sent as an emergency.
	Urgent bool `json:"urgent,omitempty"`
	// Location is UM04: where the service is done, a place of service code (11 office, 21 inpatient hospital, 22 outpatient),
	// sent with qualifier B. Payers' companion guides (CMS esMD, UnitedHealthcare) require it.
	Location string `json:"location,omitempty"`
	// AdmissionDate and DischargeDate are CCYYMMDD, sent as DTP 435 and 096 on an admission review (UM01 AR).
	AdmissionDate string `json:"admissionDate,omitempty"`
	DischargeDate string `json:"dischargeDate,omitempty"`
	// Provider is who will do the service (2010EA, NM1*SJ), with its NPI. Empty sends none, which payers read as the requester.
	Provider Person `json:"provider,omitempty"`
	// Diagnoses are ICD-10-CM codes, the first principal (ABK), the rest ABF. Dots are dropped, as X12 requires.
	Diagnoses []string `json:"diagnoses,omitempty"`
	// ServiceDate is CCYYMMDD, the proposed date of service.
	ServiceDate string `json:"serviceDate,omitempty"`
	// Procedure is a CPT or HCPCS code for the service (SV1 in a 2000F service loop); empty sends the event with no service line.
	Procedure string `json:"procedure,omitempty"`
	// Quantity is how many units are asked for, with the procedure; 0 means 1.
	Quantity float64 `json:"quantity,omitempty"`
	// Institutional sends the service line as SV2 (a hospital's, as on a UB-04) rather than SV1 (a professional's), with
	// RevenueCode, the UB-04 revenue code, in SV201 when known.
	Institutional bool   `json:"institutional,omitempty"`
	RevenueCode   string `json:"revenueCode,omitempty"`
}

// BuildServiceReviewRequest renders a 278 request, or says what is missing.
func BuildServiceReviewRequest(req ServiceReviewRequest) ([]byte, error) {
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
	need(req.Subscriber.LastName, "subscriber last name")
	need(req.Subscriber.ID, "subscriber member ID")
	if len(req.Events) == 0 {
		missing = append(missing, "at least one service (event)")
	}
	for i, e := range req.Events {
		need(e.Trace, fmt.Sprintf("a trace number for service %d", i+1))
		if strings.TrimSpace(e.ServiceType) == "" && strings.TrimSpace(e.Procedure) == "" {
			missing = append(missing, fmt.Sprintf("a service type or procedure code for service %d", i+1))
		}
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("a 278 request needs %s", strings.Join(missing, ", "))
	}
	if !validNPI(req.Provider.ID) {
		return nil, fmt.Errorf("provider NPI %q fails the NPI check digit", req.Provider.ID)
	}
	for i, e := range req.Events {
		switch e.LevelOfService {
		case "", "E", "U", "03":
		default:
			return nil, fmt.Errorf("service %d: level of service %q is not E, U or 03", i+1, e.LevelOfService)
		}
		if e.Provider.ID != "" && !validNPI(e.Provider.ID) {
			return nil, fmt.Errorf("service %d: provider NPI %q fails the NPI check digit", i+1, e.Provider.ID)
		}
		for _, d := range []string{e.ServiceDate, e.AdmissionDate, e.DischargeDate} {
			if d != "" && !isCCYYMMDD(d) {
				return nil, fmt.Errorf("service %d: date %q is not CCYYMMDD", i+1, d)
			}
		}
	}
	fields := []string{req.SenderID, req.ReceiverID, req.Payer.LastName, req.Payer.ID, req.Provider.LastName, req.Provider.FirstName,
		req.Provider.ID, req.Subscriber.LastName, req.Subscriber.FirstName, req.Subscriber.ID, req.Subscriber.DOB, req.Subscriber.Gender,
		req.Reference}
	for _, e := range req.Events {
		fields = append(fields, e.Trace, e.Category, e.CertificationType, e.ServiceType, e.ServiceDate, e.Procedure, e.LevelOfService,
			e.Location, e.RevenueCode, e.AdmissionDate, e.DischargeDate, e.Provider.LastName, e.Provider.FirstName, e.Provider.ID)
		fields = append(fields, e.Diagnoses...)
	}
	if err := checkDelimiters(fields...); err != nil {
		return nil, err
	}

	return req.wrap("HI", "005010X217", "278", func(b *builder, st string, now time.Time) {
		ref := req.Reference
		if ref == "" {
			ref = st + now.Format("150405")
		}
		// 0007: request for review. 13: request.
		b.add("BHT", "0007", "13", ref, now.Format("20060102"), now.Format("1504"))
		b.add("HL", "1", "", "20", "1")
		b.add("NM1", "X3", "2", req.Payer.LastName, "", "", "", "", "PI", req.Payer.ID)
		b.add("HL", "2", "1", "21", "1")
		b.add("NM1", "1P", personOrOrg(req.Provider), req.Provider.LastName, req.Provider.FirstName, "", "", "", "XX", req.Provider.ID)
		b.add("HL", "3", "2", "22", "1")
		b.add("NM1", "IL", "1", req.Subscriber.LastName, req.Subscriber.FirstName, "", "", "", "MI", req.Subscriber.ID)
		if req.Subscriber.DOB != "" {
			b.add("DMG", "D8", req.Subscriber.DOB, req.Subscriber.Gender)
		}
		hl := 3
		for _, e := range req.Events {
			hl++
			event := hl
			child := "0"
			if e.Procedure != "" {
				child = "1"
			}
			b.add("HL", strconv.Itoa(event), "3", "EV", child)
			// TRN03 is the originator's identifier: 9 and the submitter's ID, as the 270 sends it.
			b.add("TRN", "1", e.Trace, "9"+padID(req.SenderID))
			category, cert := orDefault(e.Category, "HS"), orDefault(e.CertificationType, "I")
			level := e.LevelOfService
			if level == "" && e.Urgent {
				level = "U"
			}
			location := ""
			if e.Location != "" {
				location = e.Location + ":B"
			}
			b.add("UM", category, cert, e.ServiceType, location, "", level)
			if e.ServiceDate != "" {
				// AAH: the event date, the proposed date of service for the whole event.
				b.add("DTP", "AAH", "D8", e.ServiceDate)
			}
			if e.AdmissionDate != "" {
				b.add("DTP", "435", "D8", e.AdmissionDate)
			}
			if e.DischargeDate != "" {
				b.add("DTP", "096", "D8", e.DischargeDate)
			}
			// HI follows DTP: the TR3 orders 2000E UM, HCR, REF, DTP, HI, HSD.
			if len(e.Diagnoses) > 0 {
				elems := []string{"HI"}
				for i, d := range e.Diagnoses {
					q := "ABF"
					if i == 0 {
						q = "ABK"
					}
					elems = append(elems, q+":"+strings.ToUpper(strings.ReplaceAll(d, ".", "")))
				}
				b.add(elems...)
			}
			if e.Provider.LastName != "" && e.Provider.ID != "" {
				// 2010EA, the patient event provider: SJ, the service provider. Its name loop closes the 2000E segments.
				b.add("NM1", "SJ", personOrOrg(e.Provider), e.Provider.LastName, e.Provider.FirstName, "", "", "", "XX", e.Provider.ID)
			}
			if e.Procedure != "" {
				hl++
				b.add("HL", strconv.Itoa(hl), strconv.Itoa(event), "SS", "0")
				// The service line's UM01 is HS, as in the TR3's own examples (an admission review's surgery is a health service).
				b.add("UM", "HS", cert, e.ServiceType)
				if e.ServiceDate != "" {
					b.add("DTP", "472", "D8", e.ServiceDate)
				}
				qty := e.Quantity
				if qty <= 0 {
					qty = 1
				}
				units := strconv.FormatFloat(qty, 'f', -1, 64)
				if e.Institutional {
					b.add("SV2", e.RevenueCode, "HC:"+strings.ToUpper(e.Procedure), "", "UN", units)
				} else {
					b.add("SV1", "HC:"+strings.ToUpper(e.Procedure), "", "UN", units)
				}
			}
		}
	}), nil
}

func isCCYYMMDD(d string) bool {
	_, err := time.Parse("20060102", d)
	return err == nil
}

func orDefault(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return v
}
