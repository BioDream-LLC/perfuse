package deident

// The default rules.
//
// This list is the security argument of the whole package, so it is written to be read by somebody
// who has to sign off on sharing the output. Every entry says why.
//
// The structure is deny by default: only paths named Keep survive. Everything else in a segment the
// dictionary knows is pseudonymised, and segments the dictionary does not know are dropped
// entirely unless somebody opts in.
//
// The HIPAA Safe Harbour list is the reference point for what has to go: names, geography finer
// than a state, all dates more precise than a year, telephone and fax numbers, email addresses,
// social security numbers, medical record numbers, account numbers, certificate and licence
// numbers, vehicle and device identifiers, URLs, IP addresses, biometrics, photographs, and any
// other unique identifying characteristic. That list is why the default is deny: several of those
// categories can turn up in fields nobody predicted.

// DefaultRules is the rule set used when none is given.
func DefaultRules() []Rule {
	return []Rule{
		// --- Message structure. Safe because it describes the message, not the patient. ---
		{Path: "MSH-1", Mode: Keep, Why: "the field separator, not data"},
		{Path: "MSH-2", Mode: Keep, Why: "the encoding characters, not data"},
		{Path: "MSH-3", Mode: Keep, Why: "sending application: identifies a system, not a person"},
		{Path: "MSH-4", Mode: Keep, Why: "sending facility: an organisation, not a person"},
		{Path: "MSH-5", Mode: Keep, Why: "receiving application"},
		{Path: "MSH-6", Mode: Keep, Why: "receiving facility"},
		{Path: "MSH-7", Mode: ShiftDate, Why: "the message timestamp, shifted with the patient so intervals between messages survive"},
		{Path: "MSH-9", Mode: Keep, Why: "message type and trigger event: the shape of the feed"},
		{Path: "MSH-10", Mode: Pseudonym, Why: "control ID: unique per message, so it links a scrubbed message back to a real one"},
		{Path: "MSH-11", Mode: Keep, Why: "processing ID"},
		{Path: "MSH-12", Mode: Keep, Why: "HL7 version, which anything reading the corpus needs"},
		{Path: "MSH-15", Mode: Keep, Why: "accept acknowledgement type"},
		{Path: "MSH-16", Mode: Keep, Why: "application acknowledgement type"},

		// --- Event. ---
		{Path: "EVN-1", Mode: Keep, Why: "event type code"},
		{Path: "EVN-2", Mode: ShiftDate, Why: "event occurred: a date, so it is shifted rather than kept"},
		{Path: "EVN-3", Mode: ShiftDate, Why: "event recorded"},
		{Path: "EVN-4", Mode: Keep, Why: "event reason code: a code, and knowing which ones a site uses is the point of a corpus"},
		{Path: "EVN-5", Mode: Pseudonym, Why: "operator ID: identifies a member of staff"},
		{Path: "EVN-6", Mode: ShiftDate, Why: "event occurred"},

		// --- Patient identification. Almost none of this survives. ---
		{Path: "PID-1", Mode: Keep, Why: "set ID, a sequence number"},
		// Named explicitly rather than left to the timestamp-shape default, because relying
		// on shape for something this important is fragile: a feed sending a date of birth as
		// a year alone, or with a timezone, would miss the shape test and be randomised.
		{Path: "PID-7", Mode: ShiftDate, Why: "date of birth: shifted by the patient's own offset, so age and every interval survive while the absolute date is wrong"},
		{Path: "PID-29", Mode: ShiftDate, Why: "date of death"},
		{Path: "PID-33", Mode: ShiftDate, Why: "last update timestamp"},
		{Path: "PID-8", Mode: Keep, Why: "administrative sex: a coded value, and needed to test any mapping that reads it"},
		{Path: "PID-10", Mode: Keep, Why: "race: coded, and a corpus without it cannot test demographics mapping"},
		{Path: "PID-15", Mode: Keep, Why: "primary language: coded"},
		{Path: "PID-16", Mode: Keep, Why: "marital status: coded"},
		{Path: "PID-17", Mode: Keep, Why: "religion: coded"},
		{Path: "PID-22", Mode: Keep, Why: "ethnic group: coded"},
		{Path: "PID-24", Mode: Keep, Why: "multiple birth indicator: a flag"},
		{Path: "PID-28", Mode: Keep, Why: "nationality: coded"},
		// Everything else on PID is a name, an address, a telephone number, an identifier or a
		// date, and is handled by the default rather than named here. That includes PID-5,
		// PID-7, PID-11, PID-13, PID-19 and every identifier field.

		// --- Visit. Codes and locations. ---
		{Path: "PV1-1", Mode: Keep, Why: "set ID"},
		{Path: "PV1-2", Mode: Keep, Why: "patient class: inpatient, outpatient, emergency"},
		{Path: "PV1-4", Mode: Keep, Why: "admission type: coded"},
		{Path: "PV1-10", Mode: Keep, Why: "hospital service: coded"},
		{Path: "PV1-14", Mode: Keep, Why: "admit source: coded"},
		{Path: "PV1-16", Mode: Keep, Why: "VIP indicator: a flag, and one worth testing"},
		{Path: "PV1-18", Mode: Keep, Why: "patient type: coded"},
		{Path: "PV1-36", Mode: Keep, Why: "discharge disposition: coded"},
		{Path: "PV1-41", Mode: Keep, Why: "account status: coded"},
		{Path: "PV1-44", Mode: ShiftDate, Why: "admit date"},
		{Path: "PV1-45", Mode: ShiftDate, Why: "discharge date, shifted by the same offset so length of stay survives"},
		// PV1-3 and PV1-6 are locations: a ward and bed identify a patient in a small unit, so
		// they are pseudonymised by default rather than kept.
		// PV1-7, PV1-8, PV1-9 and PV1-17 are clinician names.
		// PV1-19 is the visit number and PV1-50 the alternate visit ID.

		// --- Observations. The values are clinical, the identifiers are not. ---
		{Path: "OBX-1", Mode: Keep, Why: "set ID"},
		{Path: "OBX-2", Mode: Keep, Why: "value type"},
		{Path: "OBX-3", Mode: Keep, Why: "observation identifier: which test, not whose"},
		{Path: "OBX-5", Mode: Keep, Why: "observation value: a result is clinical data, and a corpus without results cannot test a results interface. It is kept because the patient it belongs to is no longer identifiable, which is the whole basis of a de-identified corpus"},
		{Path: "OBX-6", Mode: Keep, Why: "units"},
		{Path: "OBX-7", Mode: Keep, Why: "reference range"},
		{Path: "OBX-8", Mode: Keep, Why: "abnormal flags"},
		{Path: "OBX-11", Mode: Keep, Why: "result status"},
		{Path: "OBX-14", Mode: ShiftDate, Why: "date of the observation"},
		// OBX-16 is the responsible observer, a person.

		{Path: "OBR-1", Mode: Keep, Why: "set ID"},
		{Path: "OBR-4", Mode: Keep, Why: "universal service identifier: which test"},
		{Path: "OBR-7", Mode: ShiftDate, Why: "observation date"},
		{Path: "OBR-22", Mode: ShiftDate, Why: "results reported date"},
		{Path: "OBR-25", Mode: Keep, Why: "result status"},

		// --- Diagnosis and orders: codes throughout. ---
		{Path: "DG1-1", Mode: Keep, Why: "set ID"},
		{Path: "DG1-2", Mode: Keep, Why: "diagnosis coding method"},
		{Path: "DG1-3", Mode: Keep, Why: "diagnosis code: clinical, and the reason a corpus is worth having"},
		{Path: "DG1-6", Mode: Keep, Why: "diagnosis type"},
		{Path: "DG1-5", Mode: ShiftDate, Why: "diagnosis date"},

		{Path: "ORC-1", Mode: Keep, Why: "order control"},
		{Path: "ORC-5", Mode: Keep, Why: "order status"},
		{Path: "ORC-9", Mode: ShiftDate, Why: "date of the transaction"},

		// --- Insurance: the plan is not identifying, the policy number is. ---
		{Path: "IN1-1", Mode: Keep, Why: "set ID"},
		{Path: "IN1-2", Mode: Keep, Why: "insurance plan ID: names a plan, not a person"},
		{Path: "IN1-12", Mode: ShiftDate, Why: "plan effective date"},
		{Path: "IN1-13", Mode: ShiftDate, Why: "plan expiration date"},
		{Path: "IN1-18", Mode: ShiftDate, Why: "the insured's date of birth"},
		{Path: "IN1-15", Mode: Keep, Why: "plan type"},
		// IN1-3 is the company ID, IN1-4 its name, IN1-5 its address, IN1-16 the insured's
		// name, IN1-18 their date of birth, IN1-19 their address, IN1-36 the policy number.
		// All handled by the default.

		// --- Next of kin: a relative's name and number identify the patient. ---
		{Path: "NK1-1", Mode: Keep, Why: "set ID"},
		{Path: "NK1-3", Mode: Keep, Why: "relationship: coded, and needed to test any mapping that reads it"},
		{Path: "NK1-7", Mode: Keep, Why: "contact role: coded"},
		// NK1-2 is the name, NK1-4 the address, NK1-5 and NK1-6 telephone numbers.

		// --- Merge, which is the case a corpus most needs to exercise. ---
		{Path: "MRG-1", Mode: Pseudonym, Why: "prior patient identifier: pseudonymised with the same mapping as PID-3, so a merge still joins the two records it joined in reality"},
	}
}

// resolve builds the lookup used during a scrub.
//
// Later rules win, so a caller's own rules override the defaults rather than colliding with them.
func resolve(extra []Rule) map[string]Rule {
	out := map[string]Rule{}
	for _, r := range DefaultRules() {
		out[upperPath(r.Path)] = r
	}
	for _, r := range extra {
		out[upperPath(r.Path)] = r
	}
	return out
}
