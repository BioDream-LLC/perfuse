package v2fhir

import (
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhir"
)

// SIU: scheduling.
//
// A scheduling feed is a sequence of messages about the same appointment - booked, moved, cancelled, not attended - so the
// one property that matters above every field mapping is that they all land on the same Appointment. The id is therefore
// derived from the appointment's own identifiers (SCH-1 and SCH-2), never from the message, and a cancellation arriving a
// week after the booking updates the booking rather than creating a second, cancelled appointment beside a live one.
//
// That second appointment would be the worst outcome available here: a patient shown as booked for a slot that no longer
// exists.

// siuStatusByEvent is what the trigger event says when SCH-25 is silent.
//
// SCH-25 is the filler's own status and wins when it is present, because the trigger describes what happened to the
// message and the status describes the appointment. They usually agree; where they do not, the appointment's own account
// is the one to believe.
var siuStatusByEvent = map[string]string{
	"S12": "booked",
	"S13": "booked", // rescheduled: still booked, at a new time
	"S14": "booked", // modified
	"S15": "cancelled",
	"S16": "cancelled", // discontinued
	"S17": "entered-in-error",
	"S26": "noshow",
}

// siuStatusByFiller maps HL7 table 0278, the filler status code.
var siuStatusByFiller = map[string]string{
	"BOOKED":    "booked",
	"PENDING":   "pending",
	"WAITLIST":  "waitlist",
	"CANCELLED": "cancelled",
	"DC":        "cancelled",
	"DELETED":   "entered-in-error",
	"COMPLETE":  "fulfilled",
	"NOSHOW":    "noshow",
	"STARTED":   "arrived",
	"OVERBOOK":  "booked",
	"BLOCKED":   "", // a blocked slot is not an appointment with a patient; handled below
}

func (c *converter) convertSIU() {
	patient := c.buildPatient()
	if patient != nil {
		c.addEntry(patient, "Patient", patientConditionalURL(patient))
	}

	// The patient is still produced without SCH, for the same reason an unmapped type still produces one.
	if _, ok := c.msg.Segment("SCH", 1); !ok {
		c.note("error", "SCH", "Appointment", "no SCH segment, so there is no appointment to map")
		return
	}
	if patient == nil {
		c.note("warning", "PID", "Appointment.participant",
			"no PID segment, so the appointment has no patient participant")
	}

	var encRef *fhir.Reference
	if patient != nil {
		if enc := c.buildEncounter(patient); enc != nil {
			c.addEntry(enc, "Encounter", encounterConditionalURL(enc))
			encRef = fhir.Ref("Encounter", enc.ID)
		}
	}
	_ = encRef // R4 Appointment has no encounter element; the Encounter stands on its own in the bundle.

	appt := c.buildAppointment(patient)
	if appt == nil {
		return
	}
	c.addEntry(appt, "Appointment", appointmentConditionalURL(appt))
}

func (c *converter) buildAppointment(patient *fhir.Patient) *fhir.Appointment {
	placer := c.get("SCH-1.1")
	filler := c.get("SCH-2.1")
	if placer == "" && filler == "" {
		// Without either identifier the appointment cannot be matched by a later S13 or S15, and every message
		// would create a new one. Saying so is better than producing appointments that can never be cancelled.
		c.note("warning", "SCH-1/SCH-2", "Appointment.identifier",
			"the appointment has no placer or filler identifier, so a later reschedule or cancellation cannot find it")
	}

	appt := &fhir.Appointment{}
	appt.SetResourceID(c.deterministicID("Appointment", placer+"|"+filler))

	if placer != "" {
		appt.Identifier = append(appt.Identifier, fhir.Identifier{
			Value: placer, System: c.opts.DefaultIdentifierSystem,
			Type: fhir.NewCodeableConcept(fhir.SystemIdentifierType, "PLAC", "Placer identifier"),
		})
	}
	if filler != "" {
		appt.Identifier = append(appt.Identifier, fhir.Identifier{
			Value: filler, System: c.opts.DefaultIdentifierSystem,
			Type: fhir.NewCodeableConcept(fhir.SystemIdentifierType, "FILL", "Filler identifier"),
		})
	}

	appt.Status = c.appointmentStatus()
	if appt.Status == "" {
		return nil
	}

	if reason := c.codedValue("SCH-7", "SCH-7"); reason != nil {
		appt.ReasonCode = append(appt.ReasonCode, *reason)
	}
	if kind := c.codedValue("SCH-8", "SCH-8"); kind != nil {
		appt.AppointmentType = kind
	}

	// The service is carried per resource group in AIS; the first one names what the visit is for.
	if svc := c.codedValue("AIS-3", "AIS-3"); svc != nil {
		appt.ServiceType = append(appt.ServiceType, *svc)
	}

	c.appointmentTiming(appt)

	if patient != nil {
		appt.Participant = append(appt.Participant, fhir.AppointmentParticipant{
			Actor:    fhir.Ref("Patient", patient.ID),
			Required: "required",
			Status:   "accepted",
		})
	}

	for i := range c.msg.Segments("AIP") {
		path := "AIP(" + strconv.Itoa(i+1) + ")-3"
		if prac := c.buildPractitioner(path); prac != nil {
			c.addEntry(prac, "Practitioner", "")
			appt.Participant = append(appt.Participant, fhir.AppointmentParticipant{
				Actor:    fhir.Ref("Practitioner", prac.ID),
				Required: "required",
				Status:   "accepted",
			})
		}
	}

	for i := range c.msg.Segments("AIL") {
		prefix := "AIL(" + strconv.Itoa(i+1) + ")"
		// A location is kept as a display rather than invented into a Location resource: PL carries a point of care,
		// a room and a bed, and which of those a receiving system treats as its Location is a local decision.
		where := strings.TrimSpace(strings.Join(fhir.NonEmpty(
			c.get(prefix+"-3.1"), c.get(prefix+"-3.2"), c.get(prefix+"-3.3"), c.get(prefix+"-3.4")), " "))
		if where != "" {
			appt.Participant = append(appt.Participant, fhir.AppointmentParticipant{
				Actor:    &fhir.Reference{Display: where},
				Required: "required",
				Status:   "accepted",
			})
		}
	}

	if len(appt.Participant) == 0 {
		c.note("error", "PID/AIP/AIL", "Appointment.participant",
			"FHIR requires at least one participant and the message names no patient, practitioner or location")
	}

	if comment := c.get("NTE-3"); comment != "" {
		appt.Comment = comment
	}

	return appt
}

func (c *converter) appointmentStatus() string {
	if filler := strings.ToUpper(strings.TrimSpace(c.get("SCH-25.1"))); filler != "" {
		status, known := siuStatusByFiller[filler]
		switch {
		case known && status == "":
			c.note("info", "SCH-25", "Appointment",
				"SCH-25 is %q, a blocked slot rather than a patient appointment, so no Appointment was created", filler)
			return ""
		case known:
			return status
		default:
			c.note("warning", "SCH-25", "Appointment.status",
				"filler status %q is not in HL7 table 0278; the trigger event was used instead", filler)
		}
	}
	event := strings.ToUpper(c.res.TriggerEvent)
	if status, ok := siuStatusByEvent[event]; ok {
		return status
	}
	c.note("warning", "MSH-9.2", "Appointment.status",
		"trigger event %s has no mapped status and SCH-25 is empty, so the status is \"booked\"", event)
	return "booked"
}

// appointmentTiming reads when the appointment is.
//
// Version 2.5 moved the start time out of SCH-11 and into the resource segments, and feeds did not all move with it,
// so both places are read: AIS-4 first, then AIG-8, AIL-6 and AIP-6, and SCH-11.4 last. The duration comes from the same
// segment the start did, because a duration from one resource and a start from another describe nothing.
func (c *converter) appointmentTiming(appt *fhir.Appointment) {
	sources := []struct{ start, dur, units string }{
		{"AIS-4", "AIS-7", "AIS-8"},
		{"AIG-8", "AIG-11", "AIG-12"},
		{"AIL-6", "AIL-9", "AIL-10"},
		{"AIP-6", "AIP-9", "AIP-10"},
	}
	for _, s := range sources {
		if raw := c.get(s.start); raw != "" {
			appt.Start = c.v2Instant(raw, s.start)
			if mins, ok := c.minutes(c.get(s.dur), c.get(s.units+".1"), s.dur); ok {
				appt.MinutesDuration = &mins
			}
			break
		}
	}
	if appt.Start == "" {
		if raw := c.get("SCH-11.4"); raw != "" {
			appt.Start = c.v2Instant(raw, "SCH-11.4")
		}
		if raw := c.get("SCH-11.5"); raw != "" {
			appt.End = c.v2Instant(raw, "SCH-11.5")
		}
	}
	if appt.MinutesDuration == nil {
		if mins, ok := c.minutes(c.get("SCH-9"), c.get("SCH-10.1"), "SCH-9"); ok {
			appt.MinutesDuration = &mins
		}
	}

	// FHIR's app-4: start and end come together. v2 often gives a start and a duration and no end (an NHS Wales SIU did,
	// and the HL7 validator rejected the Appointment), so the end is the start plus the duration - stated, not invented.
	if appt.Start != "" && appt.End == "" {
		if t, err := time.Parse(time.RFC3339, appt.Start); err == nil && appt.MinutesDuration != nil {
			appt.End = t.Add(time.Duration(*appt.MinutesDuration) * time.Minute).Format(time.RFC3339)
			c.note("info", "AIS-7/SCH-9", "Appointment.end", "no end time was sent, so the end is the start plus the %d-minute duration", *appt.MinutesDuration)
		} else {
			c.note("warning", "SCH-11.5", "Appointment.end",
				"the appointment has a start but no end or duration, and FHIR requires the two together (app-4)")
		}
	}
	if appt.End != "" && appt.Start == "" {
		appt.End = ""
		c.note("warning", "SCH-11.5", "Appointment.end", "an end time without a start was dropped: FHIR requires the two together (app-4)")
	}

	// FHIR's app-3 invariant: only proposed, cancelled and waitlisted appointments may omit a start.
	if appt.Start == "" {
		switch appt.Status {
		case "proposed", "cancelled", "waitlist", "entered-in-error":
		default:
			c.note("warning", "AIS-4/SCH-11", "Appointment.start",
				"a %s appointment has no start time in AIS, AIG, AIL, AIP or SCH-11, and FHIR requires one", appt.Status)
		}
	}
}

// minutes converts a v2 duration to whole minutes, or reports why it could not.
func (c *converter) minutes(value, units, source string) (int, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	n, err := strconv.ParseFloat(value, 64)
	if err != nil || n < 0 {
		c.note("warning", source, "Appointment.minutesDuration", "duration %q is not a number", value)
		return 0, false
	}
	switch strings.ToUpper(strings.TrimSpace(units)) {
	case "", "MIN", "M", "MINUTES":
		// Minutes is the v2 default when no unit is given.
	case "S", "SEC", "SECONDS":
		n /= 60
	case "H", "HR", "HOURS":
		n *= 60
	case "D", "DAY", "DAYS":
		n *= 1440
	default:
		c.note("warning", source, "Appointment.minutesDuration",
			"duration unit %q is not recognised, so the duration was left out rather than guessed", units)
		return 0, false
	}
	return int(n + 0.5), true
}

func appointmentConditionalURL(a *fhir.Appointment) string {
	for _, id := range a.Identifier {
		if id.System != "" && id.Value != "" {
			return "Appointment?identifier=" + id.System + "|" + id.Value
		}
	}
	return ""
}
