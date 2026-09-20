package eprescribe

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/drug"
)

// Problem is something wrong with a prescription.
//
// Separated into refusals and warnings because the two need different handling and a caller given one
// list will treat them alike. A schedule II prescription with refills must not be forwarded. A
// prescription with no days supply should be, with a note.
type Problem struct {
	// Field names where the trouble is, in the words the standard uses, so it can be found in the message.
	Field string
	// Message says what is wrong and why it matters.
	Message string
}

func (p Problem) String() string {
	if p.Field == "" {
		return p.Message
	}
	return p.Field + ": " + p.Message
}

// Check examines a prescription and returns what must be fixed and what should be looked at.
//
// Refusals are things that make the prescription invalid or unsafe to act on. Warnings are things a
// pharmacy will have to chase but that do not make the message wrong.
//
// Deliberately not a single boolean. A validator returning only pass or fail forces the caller to treat a
// missing telephone number like a controlled substance with refills.
func Check(m Message) (refusals []Problem, warnings []Problem) {
	t, err := m.Type()
	if err != nil {
		return []Problem{{Message: err.Error()}}, nil
	}

	if m.Header.MessageID == "" {
		refusals = append(refusals, Problem{
			Field: "MessageID",
			Message: "the message has no identifier, so a VERIFY or an ERROR coming back could not say which " +
				"prescription it is about",
		})
	}
	if m.Header.To.Value == "" {
		refusals = append(refusals, Problem{Field: "To", Message: "the message does not say which pharmacy it is for"})
	}
	if m.Header.From.Value == "" {
		refusals = append(refusals, Problem{Field: "From", Message: "the message does not say who sent it"})
	}

	p := m.Prescription()
	if p == nil {
		// A VERIFY, STATUS or ERROR has no prescription, which is correct for those and nothing more to check.
		return refusals, warnings
	}

	refusals = append(refusals, checkPatient(p.Patient)...)

	if p.Medication == nil {
		if t == NewRx {
			refusals = append(refusals, Problem{
				Field:   "MedicationPrescribed",
				Message: "a new prescription with no medication in it prescribes nothing",
			})
		}
		return refusals, warnings
	}

	r, w := checkMedication(t, *p.Medication, p.Prescriber)
	return append(refusals, r...), append(warnings, w...)
}

func checkPatient(pt Patient) []Problem {
	var out []Problem
	if pt.Name.Last == "" {
		out = append(out, Problem{Field: "Patient/Name/LastName", Message: "the patient has no last name"})
	}
	if pt.DateOfBirth.Date == "" {
		// Not a warning. Date of birth is how a pharmacy tells two patients with the same name apart, and
		// dispensing to the wrong one of those is the error this field exists to prevent.
		out = append(out, Problem{
			Field:   "Patient/DateOfBirth",
			Message: "the patient has no date of birth, which is how a pharmacy tells two people with the same name apart",
		})
	}
	return out
}

func checkMedication(t MessageType, med Medication, presc Prescriber) (refusals, warnings []Problem) {
	if med.Description == "" && (med.Coded == nil || med.Coded.ProductCode == "") {
		refusals = append(refusals, Problem{
			Field:   "DrugDescription",
			Message: "the prescription names no drug, either in words or by code",
		})
	}

	// Substitution. Refused when absent rather than defaulted, because both defaults are wrong in a way
	// that reaches the patient: assuming allowed dispenses a generic where the brand was required, and
	// assuming not allowed refuses a substitution that was permitted and charges the patient for it.
	if _, err := ParseSubstitution(med.Substitutions); err != nil {
		refusals = append(refusals, Problem{Field: "Substitutions", Message: err.Error()})
	}

	// The drug's schedule, and what it permits.
	sched := drug.ScheduleNone
	if med.Coded != nil && med.Coded.DEASchedule != "" {
		s, err := drug.ParseSchedule(med.Coded.DEASchedule)
		if err != nil {
			refusals = append(refusals, Problem{Field: "DEASchedule", Message: err.Error()})
		} else {
			sched = s
		}
	}

	refills, refillsGiven, refillErr := parseRefills(med.NumberOfRefills)
	switch {
	case refillErr != nil:
		refusals = append(refusals, Problem{Field: "NumberOfRefills", Message: refillErr.Error()})
	case !refillsGiven:
		if t == NewRx {
			warnings = append(warnings, Problem{
				Field: "NumberOfRefills",
				Message: "the prescription does not say how many refills are authorised; a pharmacy will read that as " +
					"none, so say zero if that is what is meant",
			})
		}
	default:
		if err := sched.RefillsAllowed(refills); err != nil {
			// A schedule II with refills is the case worth being loud about: the message is well formed, a
			// pharmacy must refuse it, and the prescriber hears about it from the patient.
			refusals = append(refusals, Problem{Field: "NumberOfRefills", Message: err.Error()})
		}
	}

	if sched.Controlled() {
		if presc.DEANumber == "" && (presc.Identification == nil || presc.Identification.DEANumber == "") {
			refusals = append(refusals, Problem{
				Field: "Prescriber/DEANumber",
				Message: fmt.Sprintf(
					"this is a schedule %s substance and the prescriber has no DEA number; without one the "+
						"prescription cannot be filled, so forwarding it sends the patient to a counter to be turned away",
					strings.TrimPrefix(string(sched), "C")),
			})
		}
		if !sched.Prescribable() {
			refusals = append(refusals, Problem{
				Field:   "DEASchedule",
				Message: "a schedule I substance has no accepted medical use and cannot be prescribed at all",
			})
		}
	}

	// Quantity and its unit. The number alone is not a quantity: 30 of an inhaler is not 30 puffs, and a
	// pharmacy reading the wrong unit dispenses the wrong amount.
	if med.Quantity == nil || strings.TrimSpace(med.Quantity.Value) == "" {
		refusals = append(refusals, Problem{Field: "Quantity/Value", Message: "the prescription does not say how much to dispense"})
	} else if med.Quantity.UnitOfMeasure == nil || med.Quantity.UnitOfMeasure.Code == "" {
		refusals = append(refusals, Problem{
			Field: "Quantity/QuantityUnitOfMeasure",
			Message: "the quantity has no unit, so the number does not say what is being counted; thirty of an inhaler " +
				"is not thirty doses",
		})
	}

	if med.Sig == nil || strings.TrimSpace(med.Sig.Text) == "" {
		refusals = append(refusals, Problem{
			Field:   "Sig/SigText",
			Message: "the prescription has no directions, and the directions are what gets printed on the label",
		})
	}

	if strings.TrimSpace(med.DaysSupply) == "" {
		warnings = append(warnings, Problem{
			Field: "DaysSupply",
			Message: "the prescription does not say how many days it covers; the plan needs it to work out whether a " +
				"refill is too soon, so the first refill may be rejected",
		})
	}

	if med.WrittenDate == nil || med.WrittenDate.Date == "" {
		warnings = append(warnings, Problem{
			Field:   "WrittenDate",
			Message: "the prescription is not dated, and both expiry and refill windows are counted from that date",
		})
	}

	return refusals, warnings
}

// parseRefills reads the refill count.
//
// PRN means as needed and appears as the word or as 99 depending on the sender. Both are reported as an
// error for a controlled substance by the caller, because "as needed" is not a number of refills that any
// schedule permits.
func parseRefills(s string) (n int, given bool, err error) {
	t := strings.TrimSpace(strings.ToUpper(s))
	if t == "" {
		return 0, false, nil
	}
	if t == "PRN" || t == "AS NEEDED" {
		// 99 is what a numeric field carries for as-needed, and the two must behave the same or a channel
		// treats the word and the number differently.
		return 99, true, nil
	}
	n, convErr := strconv.Atoi(t)
	if convErr != nil {
		return 0, false, fmt.Errorf("refill count %q is not a number", s)
	}
	if n < 0 {
		return 0, false, fmt.Errorf("refill count %q is negative", s)
	}
	return n, true, nil
}

// Build writes a message as XML, with the SCRIPT namespace and an XML declaration.
func Build(m Message) ([]byte, error) {
	if _, err := m.Type(); err != nil {
		return nil, err
	}
	// Refusals are checked here rather than left to the caller, because a message built by this package and
	// then rejected by a routing network is our defect and the caller has no way to tell.
	refusals, _ := Check(m)
	if len(refusals) > 0 {
		msgs := make([]string, 0, len(refusals))
		for _, r := range refusals {
			msgs = append(msgs, r.String())
		}
		return nil, fmt.Errorf("this prescription cannot be sent:\n  - %s", strings.Join(msgs, "\n  - "))
	}
	return marshal(m)
}

// BuildUnchecked writes a message without validating it.
//
// Exists for forwarding and for tests: a channel passing a prescription through has not authored it, and
// refusing to re-serialise a message that arrived from somewhere else would strand it rather than let an
// operator see it. Named so that choosing it is deliberate.
func BuildUnchecked(m Message) ([]byte, error) {
	return marshal(m)
}
