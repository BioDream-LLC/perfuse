package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/dicom"
)

// queryResultToMessage turns one C-FIND match into an HL7 message.
//
// HL7 rather than something bespoke, because the rest of the engine already works on HL7: filters, transformations,
// mapping tables, the message browser, contracts, every destination. A bespoke shape would need every one of those taught
// about it, and the value of this connector is that a site can point existing machinery at imaging metadata.
//
// ORU^R01 specifically. An imaging study becoming available is an observation result, which is what IHE Radiology uses for
// exactly this notification, so the segment layout is a convention receivers already recognise rather than one invented
// here.
func queryResultToMessage(channel string, cfg *config.DICOMQuerySource, result dicom.QueryResult) ([]byte, error) {
	ds := result.DataSet
	if ds == nil {
		return nil, fmt.Errorf("the match had no data set")
	}

	now := time.Now()
	controlID := identifierForLevel(cfg.ResolvedLevel(), result)
	if controlID == "" {
		return nil, fmt.Errorf("the match had no identifier for level %s", cfg.ResolvedLevel())
	}

	// Truncated to what MSH-10 carries. A control id longer than the field is a message some receivers reject outright,
	// and a study instance UID is routinely longer than twenty characters.
	if len(controlID) > 20 {
		controlID = controlID[len(controlID)-20:]
	}

	var b strings.Builder

	// MSH. The sending facility is the archive's AE title, because when three of these channels run against three
	// archives, "which archive said this" is the first question anybody asks.
	b.WriteString(strings.Join([]string{
		"MSH",
		"^~\\&",
		"PERFUSE",
		hl7Escape(cfg.CalledAE),
		"",
		"",
		now.Format("20060102150405"),
		"",
		"ORU^R01",
		hl7Escape(controlID),
		"P",
		"2.5.1",
	}, "|"))
	b.WriteString("\r")

	// PID. Only the identifying fields the query returned; nothing is invented, and a field the archive did not send stays
	// empty rather than being filled with a plausible default.
	b.WriteString(strings.Join([]string{
		"PID",
		"1",
		"",
		hl7Escape(ds.Text(dicom.TagPatientID)),
		"",
		dicomNameToHL7(ds.Text(dicom.TagPatientName)),
		"",
		dicomDateToHL7(ds.Text(dicom.TagPatientBirthDate)),
		hl7Escape(ds.Text(dicom.TagPatientSex)),
	}, "|"))
	b.WriteString("\r")

	// OBR. The accession number is the filler order number, which is where a RIS puts it and therefore where a receiver
	// reconciling against the RIS will look for it.
	studyDate := dicomDateToHL7(ds.Text(dicom.TagStudyDate)) + dicomTimeToHL7(ds.Text(dicom.TagStudyTime))
	b.WriteString(strings.Join([]string{
		"OBR",
		"1",
		"",
		hl7Escape(ds.Text(dicom.TagAccessionNumber)),
		hl7Escape(ds.Text(dicom.TagStudyDescription)),
		"",
		"",
		studyDate,
		"", "", "", "", "", "", "", "",
		hl7Escape(ds.Text(dicom.TagReferringPhysician)),
		"", "", "", "", "", "",
		"",
		hl7Escape(ds.Text(dicom.TagModality)),
	}, "|"))
	b.WriteString("\r")

	// ZDS carries the study instance UID. A Z segment is non-standard by definition, but this particular one is what IHE
	// Radiology defines for this purpose, so it is the established convention rather than an invention - and there is no
	// standard HL7 field for a study instance UID to use instead.
	if uid := ds.Text(dicom.TagStudyInstanceUID); uid != "" {
		b.WriteString("ZDS|" + hl7Escape(uid) + "^PERFUSE^Application^DICOM")
		b.WriteString("\r")
	}

	return []byte(b.String()), nil
}

// hl7Escape replaces the characters that would otherwise be read as delimiters.
//
// Necessary rather than defensive: a study description containing an ampersand is ordinary, and unescaped it splits a field
// into subcomponents that were never there. The order matters - the escape character has to go first, or the escapes
// inserted for the others get escaped in turn.
func hl7Escape(s string) string {
	s = strings.ReplaceAll(s, `\`, `\E\`)
	s = strings.ReplaceAll(s, "|", `\F\`)
	s = strings.ReplaceAll(s, "^", `\S\`)
	s = strings.ReplaceAll(s, "&", `\T\`)
	s = strings.ReplaceAll(s, "~", `\R\`)

	// Segment terminators cannot be escaped into a field; a value containing one would end the segment early. Replaced
	// with a space, because dropping the character would silently join two words.
	s = strings.ReplaceAll(s, "\r", " ")
	s = strings.ReplaceAll(s, "\n", " ")

	return s
}

// dicomNameToHL7 converts a DICOM person name to HL7.
//
// Both use caret-separated components in the same order - family, given, middle, prefix, suffix - so this is mostly a
// pass-through. It exists because the two disagree about how many components there are and about padding, and because a
// name arriving with DICOM's trailing carets produces empty HL7 components that some receivers reject.
func dicomNameToHL7(name string) string {
	if name == "" {
		return ""
	}

	parts := strings.Split(name, "^")
	for i := range parts {
		parts[i] = hl7Escape(strings.TrimSpace(parts[i]))
	}

	// Trailing empty components trimmed. DICOM pads a name out with carets and HL7 receivers vary in how they treat an
	// empty component that is present versus absent.
	for len(parts) > 0 && parts[len(parts)-1] == "" {
		parts = parts[:len(parts)-1]
	}

	return strings.Join(parts, "^")
}

// dicomDateToHL7 converts a DICOM date to HL7.
//
// Both are YYYYMMDD, so this validates rather than converts: an eight digit string passes through and anything else is
// dropped. Dropped rather than passed through, because a malformed date in a timestamp field is worse than an absent one -
// it makes a receiver either reject the message or store a wrong date.
func dicomDateToHL7(date string) string {
	date = strings.TrimSpace(date)
	if len(date) != 8 || !allDigits(date) {
		return ""
	}
	return date
}

// dicomTimeToHL7 converts a DICOM time to the HL7 time portion.
//
// DICOM allows HHMMSS.FFFFFF and any truncation of it. HL7 wants HHMMSS, so the fraction is discarded and a short time is
// padded - a time of "10" means ten o'clock, and passing it through unpadded would make a receiver read it as ten seconds
// past midnight.
func dicomTimeToHL7(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	if dot := strings.IndexByte(t, '.'); dot >= 0 {
		t = t[:dot]
	}
	if !allDigits(t) || len(t) > 6 || len(t)%2 != 0 {
		return ""
	}

	for len(t) < 6 {
		t += "0"
	}

	return t
}

func allDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return len(s) > 0
}
