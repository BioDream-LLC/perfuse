// Package cda reads clinical documents.
//
// The design starts from how these documents actually travel, which is not as
// files. A discharge summary leaves an EHR inside an HL7 v2 MDM^T02 message, with
// the whole document base64-encoded in OBX-5 and described by a TXA segment. That
// is thousands of messages a day in a mid-sized hospital, and it is why this
// package sits next to an HL7 v2 parser rather than in a document toolkit: the v2
// half is already here, so the pipeline from "a message arrived" to "a FHIR
// DocumentReference exists" is short.
//
// Three things are deliberately in scope and one is deliberately not.
//
// In scope: reading a document and saying what it contains in plain terms;
// checking that the human-readable narrative and the coded entries agree; and
// converting to FHIR. The narrative check is the unusual one. Every C-CDA section
// carries the same information twice, once as text for a clinician and once as
// codes for a machine, and they are supposed to match. In practice they often do
// not, and nothing checks it - not the certification tooling, not the engines.
// When the text says a patient is allergic to penicillin and the coded entry says
// something else, one of the two readers acts on the wrong information. It is a
// patient safety defect that is mechanically detectable, so it is detected here.
//
// Not in scope: generating conformant C-CDA. Writing one means satisfying several
// hundred template rules and a Schematron, and producing documents that are 95%
// right would fail certification in ways that are miserable to debug. Reading,
// checking and converting is most of the value for a fraction of the risk.
package cda

import (
	"encoding/base64"
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Embedded is a document found inside an HL7 v2 message.
type Embedded struct {
	// Segment is which OBX the document came from, 1-based, so a message
	// carrying several attachments can be described precisely.
	Segment int

	// Encoding is what OBX-5.2 or OBX-5.3 declared: "Base64", "A" for ASCII, and
	// so on. It is reported even when the payload turned out to be something
	// else, because the disagreement is itself worth knowing.
	Encoding string

	// MimeType is from OBX-5.3 where present.
	MimeType string

	// Data is the decoded payload.
	Data []byte

	// Notes record anything surprising about the extraction.
	Notes []string
}

// DocumentInfo is what the TXA segment says about a document, which is metadata
// the document itself does not always carry.
type DocumentInfo struct {
	// Type is TXA-2, the document type code.
	Type string
	// UniqueID is TXA-12, the document identifier the sending system uses. It is
	// the key for detecting a replacement later.
	UniqueID string
	// Status is TXA-17: "AU" authenticated, "IN" incomplete, "DO" documented.
	Status string
	// Availability is TXA-19.
	Availability string
	// ActivityDate is TXA-4.
	ActivityDate string
	// ParentID is TXA-13, set when this document replaces another.
	ParentID string
	// Author is TXA-9.
	Author string
}

// Replaces reports whether this document supersedes an earlier one.
//
// It matters more than it looks. A corrected discharge summary arrives as a new
// document naming the old one, and a receiving system that files both leaves two
// contradictory summaries in the chart with nothing to say which is current.
func (d DocumentInfo) Replaces() bool { return strings.TrimSpace(d.ParentID) != "" }

// StatusMeaning renders TXA-17 in words.
func (d DocumentInfo) StatusMeaning() string {
	switch strings.ToUpper(strings.TrimSpace(d.Status)) {
	case "AU":
		return "authenticated"
	case "DI":
		return "dictated"
	case "DO":
		return "documented"
	case "IN":
		return "incomplete"
	case "IP":
		return "in progress"
	case "LA":
		return "legally authenticated"
	case "PA":
		return "pre-authenticated"
	case "":
		return ""
	default:
		return "unrecognised status " + d.Status
	}
}

// ExtractFromV2 pulls every embedded document out of an HL7 v2 message.
//
// It looks at OBX segments whose value type is ED or RP, which is how an
// encapsulated document is declared. A message with no such segment returns
// nothing and no error: most messages are not carrying a document, and treating
// that as a failure would make the function useless as a probe.
func ExtractFromV2(m *hl7.Message) ([]Embedded, error) {
	if m == nil {
		return nil, fmt.Errorf("cda: no message")
	}

	var out []Embedded

	for i, seg := range m.Segments("OBX") {
		valueType := strings.ToUpper(strings.TrimSpace(seg.Field(2).String()))
		if valueType != "ED" && valueType != "RP" {
			continue
		}

		value := seg.Field(5)
		doc := Embedded{Segment: i + 1}

		// An ED field is source^type^subtype^encoding^data. Systems disagree
		// about how many leading components they populate, so the payload is
		// found by looking for the component that actually holds the document
		// rather than by trusting a fixed position.
		components := value.ComponentCount()
		var payload, encoding, mime string

		for n := 1; n <= components; n++ {
			text := value.Component(n).String()
			switch {
			case looksLikeEncoding(text):
				encoding = text
			case strings.Contains(text, "/") && len(text) < 60 && !looksLikeBase64(text):
				mime = text
			case len(text) > len(payload):
				payload = text
			}
		}

		if payload == "" {
			// A reference variant points elsewhere rather than carrying the
			// document. Saying so is more useful than returning an empty result
			// that looks like a parsing failure.
			doc.Notes = append(doc.Notes,
				"this OBX declares an encapsulated document but carries no payload; it may be a reference to one held elsewhere")
			doc.Encoding = encoding
			doc.MimeType = mime
			out = append(out, doc)
			continue
		}

		doc.Encoding = encoding
		doc.MimeType = mime

		decoded, notes := decodePayload(payload, encoding)
		doc.Data = decoded
		doc.Notes = append(doc.Notes, notes...)

		out = append(out, doc)
	}

	return out, nil
}

func looksLikeEncoding(s string) bool {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "BASE64", "A", "HEX", "ASCII":
		return true
	}
	return false
}

func looksLikeBase64(s string) bool {
	if len(s) < 16 {
		return false
	}
	for i := 0; i < len(s) && i < 64; i++ {
		c := s[i]
		ok := (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' ||
			c == '\r' || c == '\n'
		if !ok {
			return false
		}
	}
	return true
}

// decodePayload decodes according to the declared encoding, and falls back to
// sniffing when the declaration is wrong.
//
// Falling back is not laxity. Sending systems mislabel this constantly - base64
// declared as ASCII, or no encoding component at all - and refusing a document
// that is plainly readable because a metadata field was wrong would lose clinical
// information over a formatting mistake. The disagreement is recorded instead.
func decodePayload(payload, encoding string) ([]byte, []string) {
	var notes []string
	trimmed := strings.TrimSpace(payload)
	declared := strings.ToUpper(strings.TrimSpace(encoding))

	// HL7 escape sequences survive into the field, and a payload wrapped by the
	// sender arrives with its line breaks encoded. They are resolved by scanning
	// rather than by string replacement, because the sequences abut: \X0D\ and
	// \X0A\ written consecutively share no delimiter, and a replacer for the
	// first consumes the backslash that opens the second. HL7 also permits several
	// bytes in one sequence, as \X0D0A\, which a fixed replacer never matches.
	if resolved, count := resolveHexEscapes(trimmed); count > 0 {
		trimmed = resolved
		notes = append(notes, fmt.Sprintf(
			"the payload contained %d HL7 escape sequence(s), which were resolved", count))
	}

	// Base64 in an HL7 field is frequently wrapped, and the wrapping is not part
	// of the data.
	compact := strings.NewReplacer("\r", "", "\n", "", " ", "", "\t", "").Replace(trimmed)

	if declared == "BASE64" || declared == "" {
		if decoded, err := base64.StdEncoding.DecodeString(compact); err == nil {
			if declared == "" {
				notes = append(notes, "no encoding was declared; the payload decoded as base64")
			}
			return decoded, notes
		}
		if declared == "BASE64" {
			// It said base64 and is not. Return the raw text so the document is
			// not lost, and say clearly what happened.
			notes = append(notes,
				"the payload is declared as base64 but does not decode; it has been kept as text")
			return []byte(trimmed), notes
		}
	}

	if declared == "A" || declared == "ASCII" {
		// Occasionally a system declares ASCII and sends base64 anyway.
		if looksLikeBase64(compact) {
			if decoded, err := base64.StdEncoding.DecodeString(compact); err == nil && looksLikeXML(decoded) {
				notes = append(notes,
					"the payload is declared as plain text but is base64-encoded XML; it was decoded")
				return decoded, notes
			}
		}
		return []byte(trimmed), notes
	}

	return []byte(trimmed), notes
}

// resolveHexEscapes expands HL7 \X..\ sequences and the line-break escape.
//
// It returns the resolved text and how many sequences were expanded, so the caller
// can say that it happened rather than silently changing the payload.
func resolveHexEscapes(s string) (string, int) {
	if !strings.Contains(s, `\X`) && !strings.Contains(s, `\.br\`) {
		return s, 0
	}

	var b strings.Builder
	b.Grow(len(s))
	count := 0

	for i := 0; i < len(s); {
		if strings.HasPrefix(s[i:], `\.br\`) {
			b.WriteByte('\n')
			i += 5
			count++
			continue
		}
		if s[i] != '\\' || i+1 >= len(s) || (s[i+1] != 'X' && s[i+1] != 'x') {
			b.WriteByte(s[i])
			i++
			continue
		}

		// Find the closing backslash of this sequence.
		closing := strings.IndexByte(s[i+2:], '\\')
		if closing < 0 {
			b.WriteByte(s[i])
			i++
			continue
		}
		hexDigits := s[i+2 : i+2+closing]

		// An odd count, or anything that is not hex, means this is not an escape
		// sequence after all. Passing it through unchanged is safer than dropping
		// bytes out of a clinical document.
		if len(hexDigits) == 0 || len(hexDigits)%2 != 0 || !isHex(hexDigits) {
			b.WriteByte(s[i])
			i++
			continue
		}

		for j := 0; j < len(hexDigits); j += 2 {
			b.WriteByte(hexValue(hexDigits[j])<<4 | hexValue(hexDigits[j+1]))
		}
		count++
		i += 2 + closing + 1
	}

	return b.String(), count
}

func isHex(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func hexValue(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	default:
		return c - 'A' + 10
	}
}

func looksLikeXML(data []byte) bool {
	trimmed := strings.TrimSpace(string(data))
	return strings.HasPrefix(trimmed, "<?xml") || strings.HasPrefix(trimmed, "<")
}

// InfoFromV2 reads the TXA segment.
func InfoFromV2(m *hl7.Message) (DocumentInfo, bool) {
	seg, ok := m.Segment("TXA", 1)
	if !ok {
		return DocumentInfo{}, false
	}
	return DocumentInfo{
		Type:         seg.Field(2).String(),
		ActivityDate: seg.Field(4).String(),
		Author:       seg.Field(9).String(),
		UniqueID:     seg.Field(12).Component(1).String(),
		ParentID:     seg.Field(13).Component(1).String(),
		Status:       seg.Field(17).String(),
		Availability: seg.Field(19).String(),
	}, true
}

// IsDocumentMessage reports whether a message is carrying a document, so a
// channel can route on it without decoding anything.
func IsDocumentMessage(m *hl7.Message) bool {
	if m == nil {
		return false
	}
	typ, _, _ := m.Type()
	if strings.EqualFold(typ, "MDM") {
		return true
	}
	// An ORU can carry a document too, and frequently does for radiology.
	for _, seg := range m.Segments("OBX") {
		switch strings.ToUpper(strings.TrimSpace(seg.Field(2).String())) {
		case "ED", "RP":
			return true
		}
	}
	return false
}

// ParseEmbedded extracts a document from a v2 message and parses it as CDA.
//
// This is the pipeline the package exists for. Anything that is not a clinical
// document - a PDF, a scanned image - is reported as such rather than being
// forced through the parser, because a channel handling mixed attachments needs to
// tell the difference without an error.
func ParseEmbedded(m *hl7.Message) (*Document, []Embedded, error) {
	found, err := ExtractFromV2(m)
	if err != nil {
		return nil, nil, err
	}
	if len(found) == 0 {
		return nil, nil, nil
	}

	for i := range found {
		if !looksLikeXML(found[i].Data) {
			found[i].Notes = append(found[i].Notes,
				"this attachment is not XML, so it is not a clinical document; it has been left as bytes")
			continue
		}
		doc, parseErr := Parse(found[i].Data)
		if parseErr != nil {
			found[i].Notes = append(found[i].Notes, "the attachment is XML but not a readable CDA: "+parseErr.Error())
			continue
		}
		if info, ok := InfoFromV2(m); ok {
			doc.Transport = &info
		}
		return doc, found, nil
	}

	return nil, found, nil
}

// documentRoot finds the ClinicalDocument element, tolerating a wrapper.
func documentRoot(root *xtree.Node) *xtree.Node {
	if root == nil {
		return nil
	}
	if root.Name == "ClinicalDocument" {
		return root
	}
	return root.Find("ClinicalDocument")
}
