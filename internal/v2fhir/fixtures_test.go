package v2fhir

import (
	"fmt"
	"sort"
	"strings"
)

// Test fixtures are built by field number rather than by typing pipes.
//
// The first version of these tests hand-counted separators and put the visit
// number in PV1-18, the discharge status in OBR-24 and the admit time nowhere at
// all. Every one of those looked plausible and was wrong. Counting pipes by eye is
// the exact mistake this software exists to catch, so the fixtures do not do it.

// segment builds an HL7 segment from a name and fields keyed by HL7 field number.
//
// MSH is handled specially: MSH-1 is the field separator itself and MSH-2 the
// encoding characters, so numbering is shifted by one against every other segment.
func segment(name string, fields map[int]string) string {
	if len(fields) == 0 {
		return name
	}

	highest := 0
	for n := range fields {
		if n > highest {
			highest = n
		}
	}

	var sb strings.Builder
	sb.WriteString(name)

	start := 1
	if name == "MSH" {
		// MSH-1 is the separator that follows the name, and MSH-2 is written
		// immediately after it, so output begins at MSH-2.
		sb.WriteString("|")
		sb.WriteString(fields[2])
		start = 3
	}

	for n := start; n <= highest; n++ {
		sb.WriteString("|")
		sb.WriteString(fields[n])
	}
	return sb.String()
}

// message joins segments with the carriage return HL7 requires.
func message(segments ...string) string {
	return strings.Join(segments, "\r") + "\r"
}

// mshFields returns a populated MSH for a message type, so every fixture agrees
// about where the version and control ID live.
func mshFields(messageType, controlID, timestamp string) map[int]string {
	return map[int]string{
		2:  "^~\\&",
		3:  "SENDAPP",
		4:  "SITEA",
		5:  "RECVAPP",
		6:  "RECVFAC",
		7:  timestamp,
		9:  messageType,
		10: controlID,
		11: "P",
		12: "2.5.1",
	}
}

// describe renders a segment with its field numbers, for a failure message that
// shows where a value actually landed.
func describe(segment string) string {
	parts := strings.Split(segment, "|")
	if len(parts) == 0 {
		return segment
	}

	name := parts[0]
	var lines []string
	offset := 0
	if name == "MSH" {
		// parts[1] is MSH-2 because MSH-1 is the separator.
		offset = 1
	}

	for i := 1; i < len(parts); i++ {
		if strings.TrimSpace(parts[i]) == "" {
			continue
		}
		lines = append(lines, fmt.Sprintf("  %s-%d = %q", name, i+offset, parts[i]))
	}
	sort.Strings(lines)
	return name + "\n" + strings.Join(lines, "\n")
}

// adtWithEvent rebuilds the ADT fixture with a different trigger event.
func adtWithEvent(event string) string {
	return message(
		segment("MSH", mshFields("ADT^"+event, "CTRL1", "20260818120000-0500")),
		segment("EVN", map[int]string{1: event, 2: "20260818115900-0500"}),
		segment("PID", map[int]string{
			1: "1", 3: "MRN123456^^^SITEA^MR", 5: "Doe^Jane^Q", 7: "19800101", 8: "F",
		}),
		segment("PV1", map[int]string{
			1: "1", 2: "I", 3: "ICU^0201^01^SITEA", 10: "MED",
			19: "V0012345^^^SITEA^VN", 44: "20260818120000-0500",
		}),
	)
}

// adtWithoutOffsets removes every timezone offset, which HL7 v2 permits and FHIR
// does not.
func adtWithoutOffsets() string {
	return message(
		segment("MSH", mshFields("ADT^A01^ADT_A01", "CTRL1", "20260818120000")),
		segment("EVN", map[int]string{1: "A01", 2: "20260818115900"}),
		segment("PID", map[int]string{
			1: "1", 3: "MRN123456^^^SITEA^MR", 5: "Doe^Jane^Q", 7: "19800101", 8: "F",
		}),
		segment("PV1", map[int]string{
			1: "1", 2: "I", 3: "ICU^0201^01^SITEA", 10: "MED",
			19: "V0012345^^^SITEA^VN", 44: "20260818120000",
		}),
	)
}

// oruWithFirstOBX rebuilds the ORU fixture with one replacement OBX, so a test
// about a single result does not depend on string surgery.
func oruWithFirstOBX(obx map[int]string) string {
	return message(
		segment("MSH", mshFields("ORU^R01^ORU_R01", "CTRL2", "20260818130000-0500")),
		segment("PID", map[int]string{
			1: "1", 3: "MRN123456^^^SITEA^MR", 5: "Doe^Jane^Q", 7: "19800101", 8: "F",
		}),
		segment("OBR", map[int]string{
			1: "1", 2: "ORD987", 3: "FILL654", 4: "CBC^Complete Blood Count^LN",
			7: "20260818113000-0500", 22: "20260818125900-0500", 25: "F",
		}),
		segment("OBX", obx),
	)
}
