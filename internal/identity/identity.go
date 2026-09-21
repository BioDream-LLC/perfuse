// Finding a message by what you know about it, whatever format it arrived in.
//
// The existing content search matches metadata, offers a substring match against the payload,
// and can evaluate a filter expression - but the expression is evaluated against HL7 v2 only,
// and every expression requires knowing the path. So finding one patient's traffic meant
// knowing that identity lives at PID-3 in an HL7 v2 admission, at Patient.identifier in a
// FHIR resource, at (0010,0020) in a DICOM instance and at NM109 of an NM1*IL loop in an 837
// claim. Four formats, four vocabularies, and during an incident the message being hunted
// might be in any of them.
//
// This package maps a small set of concepts - who the message is about, which visit, which
// study, which claim - onto the place each format keeps them, so a search can be for "MRN
// 12345" rather than for a path. The concepts are deliberately few: these are the things
// people actually have in their hand when they need to find a message, which is a number off
// a phone call or a name off a complaint.
//
// # Why extraction happens once, at record time
//
// The alternative is parsing payloads at query time, which is what the expression search
// does. That works and it is honest about being a bounded scan, but it cannot answer "every
// message about this patient across four feeds and two months" without reading every row.
// Extracting once into an indexed table turns that into an index lookup.
//
// # This is a privacy escalation, and it is optional
//
// Patient names and identifiers currently sit inside an opaque payload column. Indexing them
// makes them queryable, which is the point, and also makes them enumerable, which is a real
// change in exposure: a database that could only be grepped can now be asked "list every
// patient". That is a decision for the site, not for this package, so extraction is
// switchable and every search through it is recorded in the PHI audit log.
package identity

import (
	"bytes"
	"strings"
	"unicode"
)

// Kind is what a value means, independent of the format it was found in.
//
// Named for what somebody would call it out loud rather than for any one standard's term,
// because the point of this layer is that the standard's term is what they do not know.
type Kind string

const (
	// KindPatientID is any identifier for the person: MRN, member ID, subscriber ID.
	//
	// One kind rather than several, because the person searching has a number and does not
	// necessarily know which kind of number it is. Splitting them would mean asking.
	KindPatientID Kind = "patient_id"

	// KindPatientName is a name as written in the message, family and given together.
	KindPatientName Kind = "patient_name"

	// KindBirthDate is the date of birth, as an ISO date where the source allows it.
	KindBirthDate Kind = "birth_date"

	// KindAccount is the visit: account number, encounter identifier, visit number.
	KindAccount Kind = "account"

	// KindAccession is the order or the imaging accession, which is what a radiology
	// question arrives as.
	KindAccession Kind = "accession"

	// KindClaim is a claim or prior-authorisation identifier.
	KindClaim Kind = "claim"

	// KindStudyUID is the DICOM study instance UID, which is how a PACS refers to a study.
	KindStudyUID Kind = "study_uid"
)

// Format is the shape a payload turned out to be.
type Format string

const (
	FormatUnknown Format = ""
	FormatHL7     Format = "hl7"
	FormatFHIR    Format = "fhir"
	FormatDICOM   Format = "dicom"
	FormatX12     Format = "x12"
)

// Value is one extracted identifier.
type Value struct {
	Kind Kind

	// Text is the value as the message wrote it, for display.
	Text string

	// Norm is Text reduced for matching: lowercased, punctuation and separators removed.
	//
	// Stored alongside rather than computed at query time so the index can be used. A
	// LOWER(value) LIKE ... in the query cannot be, which is the whole reason this table
	// exists.
	Norm string
}

// Identity is everything a message said about who and what it concerns.
type Identity struct {
	Format Format
	Values []Value
}

// Get returns the first value of a kind, or the empty string.
func (id Identity) Get(k Kind) string {
	for _, v := range id.Values {
		if v.Kind == k {
			return v.Text
		}
	}
	return ""
}

// All returns every value of a kind.
func (id Identity) All(k Kind) []string {
	var out []string
	for _, v := range id.Values {
		if v.Kind == k {
			out = append(out, v.Text)
		}
	}
	return out
}

// Normalise reduces a value to what it should be matched on.
//
// Identifiers are written inconsistently by the systems that emit them and worse by the
// people who retype them: MRN 001234567 is pasted as 1234567, a name arrives as SMITH^JOHN
// in one feed and "Smith, John" in another. Matching on the raw text means the obvious
// search fails and the person concludes the feature is broken.
//
// Leading zeros are deliberately kept. Two different patients can have MRNs that differ
// only by one, and silently making them equal is worse than a search that needs the zero.
func Normalise(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// mllpFrame is the wrapper a file archive or a network read may still have around a message.
//
// Stripped before anything is sniffed, because the framing bytes come before MSH and a
// payload that starts with 0x0b is not recognised as HL7 by a prefix test. The file
// destination writes these deliberately so an archive replays through perfuse send, so a
// stored payload having them is normal rather than a corruption.
const (
	mllpStart = 0x0b
	mllpEnd   = 0x1c
)

func unframe(raw []byte) []byte {
	raw = bytes.TrimLeft(raw, string([]byte{mllpStart}))
	if i := bytes.IndexByte(raw, mllpEnd); i >= 0 {
		raw = raw[:i]
	}
	return bytes.TrimSpace(raw)
}

// Detect reports what format a payload appears to be.
//
// Cheap tests in the order that cannot be confused. DICOM is checked first because its magic
// is unambiguous, and last-resort guessing is deliberately absent: a payload nothing
// recognises returns FormatUnknown and is left alone rather than being parsed hopefully by
// every parser in turn, which is slow and produces nonsense values from coincidence.
func Detect(raw []byte) Format {
	body := unframe(raw)
	if len(body) == 0 {
		return FormatUnknown
	}

	// The preamble is 128 bytes of anything, then "DICM". Also accept a data set with no
	// preamble, which is what arrives over the wire rather than from a file.
	if len(raw) > 132 && bytes.Equal(raw[128:132], []byte("DICM")) {
		return FormatDICOM
	}

	switch {
	case body[0] == '{' && bytes.Contains(body, []byte(`"resourceType"`)):
		return FormatFHIR
	case bytes.HasPrefix(body, []byte("ISA")):
		return FormatX12
	case bytes.HasPrefix(body, []byte("MSH|")) || bytes.Contains(body, []byte("\rMSH|")) ||
		bytes.Contains(body, []byte("\nMSH|")):
		return FormatHL7
	}
	return FormatUnknown
}

// Extract pulls every identifier it can find out of a stored payload.
//
// Never returns an error. A payload that cannot be parsed yields no values, because the
// caller is recording a message that has already been accepted and delivered - failing to
// index it must not fail the recording. What a caller needs to know is whether anything was
// found, which is len(Values).
func Extract(raw []byte) Identity {
	format := Detect(raw)
	id := Identity{Format: format}
	body := unframe(raw)

	switch format {
	case FormatHL7:
		id.Values = fromHL7(body)
	case FormatFHIR:
		id.Values = fromFHIR(body)
	case FormatDICOM:
		id.Values = fromDICOM(raw)
	case FormatX12:
		id.Values = fromX12(body)
	}
	return dedupe(id)
}

// dedupe removes repeated kind/value pairs while keeping order.
//
// Messages repeat identity constantly - the MRN appears in PID-3, in PV1, and again in every
// OBR - and indexing the same pair eight times makes the table larger and the results
// duplicated without adding a way to find anything.
func dedupe(id Identity) Identity {
	seen := make(map[[2]string]bool, len(id.Values))
	out := id.Values[:0]
	for _, v := range id.Values {
		if v.Norm == "" {
			continue
		}
		key := [2]string{string(v.Kind), v.Norm}
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, v)
	}
	id.Values = out
	return id
}

// add appends a value if it has any content.
func add(vals []Value, k Kind, text string) []Value {
	text = strings.TrimSpace(text)
	if text == "" {
		return vals
	}
	n := Normalise(text)
	if n == "" {
		return vals
	}
	return append(vals, Value{Kind: k, Text: text, Norm: n})
}
