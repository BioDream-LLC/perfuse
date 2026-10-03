package v2fhir

import (
	"encoding/base64"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/fhir"
)

// MDM: clinical documents.
//
// This is how dictated notes, discharge summaries and scanned reports leave most EHRs, and the CMS Interoperability
// Framework (criterion 14) asks for exactly those - chart notes and clinical documents, human-readable, returned as FHIR
// attachments. A DocumentReference carrying the document is that attachment.
//
// Two things are deliberately not done.
//
// A notification with no content (T01, T03, T05 and the rest of the odd-numbered events) produces no DocumentReference.
// FHIR requires DocumentReference.content, and filling it with an empty or placeholder attachment would publish a
// document that says nothing while appearing to be the note. The conversion says so instead.
//
// And a document's bytes are never altered on the way through. An embedded PDF is decoded only to prove it is the base64
// it claims to be, then carried byte for byte: the document a clinician signed is the document the receiver gets.

// documentTypeLOINC maps HL7 table 0270, the document type in TXA-2, to the LOINC document codes US Core uses for
// clinical notes. Only codes with a clear equivalent are listed; anything else keeps its v2 code without a LOINC one.
var documentTypeLOINC = map[string][2]string{
	"AR": {"18743-5", "Autopsy report"},
	"CN": {"11488-4", "Consult note"},
	"DI": {"18748-4", "Diagnostic imaging study"},
	"DS": {"18842-5", "Discharge summary"},
	"ED": {"34111-5", "Emergency department note"},
	"HP": {"34117-2", "History and physical note"},
	"OP": {"11504-8", "Surgical operation note"},
	"PN": {"28570-0", "Procedure note"},
	"PR": {"11506-3", "Progress note"},
	"SP": {"11526-1", "Pathology study"},
	"TS": {"18761-7", "Transfer summary note"},
}

// edContentTypes maps the ED data subtype (OBX-5.3) to a MIME type. PDF, TIFF and JPEG are the formats the CMS
// framework names for scanned and faxed documents.
var edContentTypes = map[string]string{
	"PDF":          "application/pdf",
	"TIFF":         "image/tiff",
	"TIF":          "image/tiff",
	"JPEG":         "image/jpeg",
	"JPG":          "image/jpeg",
	"PNG":          "image/png",
	"RTF":          "application/rtf",
	"HTML":         "text/html",
	"XML":          "application/xml",
	"TEXT":         "text/plain",
	"PLAIN":        "text/plain",
	"OCTET-STREAM": "application/octet-stream",
}

const systemUSCoreDocumentCategory = "http://hl7.org/fhir/us/core/CodeSystem/us-core-documentreference-category"

func (c *converter) convertMDM() {
	if _, ok := c.msg.Segment("TXA", 1); !ok {
		c.note("error", "TXA", "DocumentReference", "no TXA segment, so there is no document to describe")
		return
	}

	patient := c.buildPatient()
	if patient == nil {
		c.note("error", "PID", "DocumentReference.subject", "no PID segment, so the document has no subject")
		return
	}
	c.addEntry(patient, "Patient", patientConditionalURL(patient))

	var encRef *fhir.Reference
	if enc := c.buildEncounter(patient); enc != nil {
		c.addEntry(enc, "Encounter", encounterConditionalURL(enc))
		encRef = fhir.Ref("Encounter", enc.ID)
	}

	if doc := c.buildDocumentReference(fhir.Ref("Patient", patient.ID), encRef); doc != nil {
		c.addEntry(doc, "DocumentReference", documentConditionalURL(doc))
	}
}

func (c *converter) buildDocumentReference(patient, encounter *fhir.Reference) *fhir.DocumentReference {
	content := c.documentContent()
	if content == nil {
		c.note("info", "OBX", "DocumentReference.content",
			"%s carries no document content, and FHIR requires content, so no DocumentReference was created",
			strings.ToUpper(c.res.TriggerEvent))
		return nil
	}

	number := c.get("TXA-12.1")
	doc := &fhir.DocumentReference{
		Subject: patient,
		Content: []fhir.DocumentContent{{Attachment: content}},
		Category: []fhir.CodeableConcept{*fhir.NewCodeableConcept(
			systemUSCoreDocumentCategory, "clinical-note", "Clinical Note")},
	}
	// Keyed on the document's own number, so an edit (T08), an addendum's parent or a replacement (T10) finds the
	// document it concerns rather than creating another.
	doc.SetResourceID(c.deterministicID("DocumentReference", number))
	if number == "" {
		c.note("warning", "TXA-12", "DocumentReference.identifier",
			"the document has no unique document number, so a later edit or cancellation cannot find it")
	} else {
		doc.Identifier = []fhir.Identifier{{System: c.opts.DefaultIdentifierSystem, Value: number}}
	}

	docType := strings.ToUpper(strings.TrimSpace(c.get("TXA-2.1")))
	if sys, _ := mapCodingSystem(c.get("TXA-2.3")); docType != "" && sys == fhir.SystemLOINC {
		// A LOINC document code sent as such, which is what a US Core document type needs. Not a table 0270 code, so it is not
		// labelled as one.
		doc.Type = &fhir.CodeableConcept{Coding: []fhir.Coding{{System: fhir.SystemLOINC, Code: c.get("TXA-2.1")}}, Text: c.get("TXA-2.2")}
	} else if docType != "" {
		cc := &fhir.CodeableConcept{}
		if l, ok := documentTypeLOINC[docType]; ok {
			cc.Coding = append(cc.Coding, fhir.Coding{System: fhir.SystemLOINC, Code: l[0], Display: l[1]})
		} else {
			c.note("info", "TXA-2", "DocumentReference.type",
				"document type %q has no LOINC equivalent here, so only the v2 code was kept", docType)
		}
		cc.Coding = append(cc.Coding, fhir.Coding{System: fhir.SystemV2Table + "0270", Code: docType})
		doc.Type = cc
	} else {
		c.note("warning", "TXA-2", "DocumentReference.type", "the document has no type")
	}

	doc.Date = c.v2Instant(c.get("TXA-4"), "TXA-4")
	if doc.Date == "" {
		doc.Date = c.v2Instant(c.get("TXA-6"), "TXA-6")
	}
	content.Creation = c.v2DateTime(c.get("TXA-6"), "TXA-6")

	if prac := c.buildPractitioner("TXA-9"); prac != nil {
		c.addEntry(prac, "Practitioner", "")
		doc.Author = append(doc.Author, *fhir.Ref("Practitioner", prac.ID))
	}

	doc.Status, doc.DocStatus = c.documentStatus()

	if encounter != nil {
		doc.Context = &fhir.DocumentContext{Encounter: []fhir.Reference{*encounter}}
	}
	return doc
}

// documentStatus maps TXA-19 (availability) and TXA-17 (completion), letting the trigger event overrule both for a
// cancellation, because T11 means "this document should not have existed" whatever its fields still say.
func (c *converter) documentStatus() (status, docStatus string) {
	status = "current"
	if strings.EqualFold(c.res.TriggerEvent, "T11") {
		return "entered-in-error", "entered-in-error"
	}

	switch avail := strings.ToUpper(c.get("TXA-19")); avail {
	case "", "AV", "UN":
		// UN (unavailable) describes access, not validity; the document is still the current one.
	case "OB":
		status = "superseded"
	case "CA":
		status = "entered-in-error"
	default:
		c.note("warning", "TXA-19", "DocumentReference.status",
			"availability status %q is not in HL7 table 0273, so the status is \"current\"", avail)
	}

	switch completion := strings.ToUpper(c.get("TXA-17")); completion {
	case "":
	case "AU", "LA":
		docStatus = "final"
	case "DI", "DO", "IN", "IP", "PA":
		// Dictated, documented, incomplete, in progress and pre-authenticated are all a note nobody has signed.
		docStatus = "preliminary"
	default:
		c.note("warning", "TXA-17", "DocumentReference.docStatus",
			"completion status %q is not in HL7 table 0271, so docStatus was left out", completion)
	}
	if strings.EqualFold(c.res.TriggerEvent, "T06") && docStatus == "final" {
		docStatus = "amended"
	}
	return status, docStatus
}

// documentContent assembles the document from its OBX segments.
//
// Text (TX, FT, ST) is one line per OBX and one line per repetition within it, in the order sent - blank lines
// included, because a blank repetition in a note is a paragraph break and dropping it runs paragraphs together.
// Encapsulated data (ED) is a single document and is passed through untouched.
func (c *converter) documentContent() *fhir.Attachment {
	var lines []string
	var embedded *fhir.Attachment

	for i, obx := range c.msg.Segments("OBX") {
		source := "OBX(" + strconv.Itoa(i+1) + ")"
		switch kind := strings.ToUpper(obx.Field(2).String()); kind {
		case "TX", "FT", "ST", "":
			for _, rep := range obx.Field(5).Repeats() {
				lines = append(lines, rep.String())
			}
		case "ED":
			if embedded != nil {
				c.note("warning", source, "DocumentReference.content",
					"a second encapsulated document was ignored; one DocumentReference carries one document")
				continue
			}
			embedded = c.encapsulated(obx.Field(5), source)
		default:
			c.note("warning", source+"-2", "DocumentReference.content",
				"value type %s is not document content and was left out", kind)
		}
	}

	if embedded != nil {
		if len(lines) > 0 {
			c.note("info", "OBX", "DocumentReference.content",
				"the message carries both text and an encapsulated document; the encapsulated document was used")
		}
		return embedded
	}
	if len(lines) == 0 {
		return nil
	}
	text := strings.Join(lines, "\n")
	size := int64(len(text))
	return &fhir.Attachment{
		ContentType: "text/plain; charset=utf-8",
		Data:        base64.StdEncoding.EncodeToString([]byte(text)),
		Size:        &size,
	}
}

// encapsulated reads an ED value: source application ^ type of data ^ data subtype ^ encoding ^ data.
func (c *converter) encapsulated(v hl7.Value, source string) *fhir.Attachment {
	subtype := strings.ToUpper(strings.TrimSpace(v.Component(3).String()))
	encoding := strings.ToUpper(strings.TrimSpace(v.Component(4).String()))
	data := v.Component(5).String()

	contentType, known := edContentTypes[subtype]
	if !known {
		contentType = "application/octet-stream"
		c.note("warning", source+"-5.3", "Attachment.contentType",
			"data subtype %q is not recognised, so the content type is application/octet-stream", subtype)
	}

	var raw []byte
	switch encoding {
	case "BASE64":
		// Line breaks are legal inside base64 in v2 and illegal in FHIR's base64Binary.
		cleaned := strings.Map(func(r rune) rune {
			if r == '\r' || r == '\n' || r == ' ' || r == '\t' {
				return -1
			}
			return r
		}, data)
		decoded, err := base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			c.note("error", source+"-5.5", "Attachment.data",
				"the encapsulated data says it is base64 and is not, so the document was not carried: %v", err)
			return nil
		}
		raw = decoded
	case "A", "":
		raw = []byte(data)
	case "HEX":
		c.note("error", source+"-5.4", "Attachment.data",
			"hex-encoded encapsulated data is not supported, so the document was not carried")
		return nil
	default:
		c.note("error", source+"-5.4", "Attachment.data",
			"encoding %q is not in HL7 table 0299, so the document was not carried", encoding)
		return nil
	}
	if len(raw) == 0 {
		c.note("error", source+"-5.5", "Attachment.data", "the encapsulated document is empty")
		return nil
	}

	size := int64(len(raw))
	return &fhir.Attachment{
		ContentType: contentType,
		Data:        base64.StdEncoding.EncodeToString(raw),
		Size:        &size,
	}
}

func documentConditionalURL(d *fhir.DocumentReference) string {
	for _, id := range d.Identifier {
		if id.System != "" && id.Value != "" {
			return "DocumentReference?identifier=" + id.System + "|" + id.Value
		}
	}
	return ""
}
