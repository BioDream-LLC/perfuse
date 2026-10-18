package xds

import (
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// IHE MHD (Mobile access to Health Documents) is XDS in FHIR: a SubmissionSet is a List, a DocumentEntry a DocumentReference,
// the document a Binary. This file maps between the two, so an MHD Document Recipient (ITI-65) registers in the same XDS
// registry the SOAP side uses, and a document submitted either way is found either way.

// MHD profile and identifier URLs.
const (
	MHDSubmissionSetProfile = "https://profiles.ihe.net/ITI/MHD/StructureDefinition/IHE.MHD.Minimal.SubmissionSet"
	MHDDocumentProfile      = "https://profiles.ihe.net/ITI/MHD/StructureDefinition/IHE.MHD.Minimal.DocumentReference"
	MHDListType             = "https://profiles.ihe.net/ITI/MHD/CodeSystem/MHDlistTypes"
	MHDSourceIDExt          = "https://profiles.ihe.net/ITI/MHD/StructureDefinition/ihe-sourceId"
)

func m(v any) map[string]any { x, _ := v.(map[string]any); return x }
func l(v any) []any          { x, _ := v.([]any); return x }
func s(v any) string         { x, _ := v.(string); return x }

// oidOf strips urn:oid: from an identifier system.
func oidOf(system string) string { return strings.TrimPrefix(system, "urn:oid:") }

// fhirSystems are the FHIR URIs of code systems XDS names by OID.
var fhirSystems = map[string]string{
	"http://loinc.org":       "2.16.840.1.113883.6.1",
	"http://snomed.info/sct": "2.16.840.1.113883.6.96",
	"http://terminology.hl7.org/CodeSystem/v3-Confidentiality":      "2.16.840.1.113883.5.25",
	"http://ihe.net/fhir/ihe.formatcode.fhir/CodeSystem/formatcode": formatScheme,
}

// codeOf reads a CodeableConcept's first coding, its system as the OID XDS uses.
func codeOf(cc any) Code {
	for _, c := range l(m(cc)["coding"]) {
		cm := m(c)
		if s(cm["code"]) == "" {
			continue
		}
		sys := s(cm["system"])
		if oid, ok := fhirSystems[sys]; ok {
			sys = oid
		} else {
			sys = oidOf(sys)
		}
		return Code{Code: s(cm["code"]), Scheme: sys, Display: s(cm["display"])}
	}
	return Code{}
}

// dtmOf turns a FHIR instant or dateTime into a DTM in UTC.
func dtmOf(v string) string {
	if v == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02T15:04:05", "2006-01-02", "2006-01", "2006"} {
		if t, err := time.Parse(layout, v); err == nil {
			switch layout {
			case "2006-01-02":
				return t.Format("20060102")
			case "2006-01":
				return t.Format("200601")
			case "2006":
				return t.Format("2006")
			}
			return t.UTC().Format("20060102150405")
		}
	}
	return ""
}

// FromMHD maps an MHD ITI-65 transaction Bundle to an XDS submission and the documents' content. resolvePatient turns the
// Bundle's patient reference into the affinity domain's CX (MHD references a Patient on the server; the server knows its
// identifiers).
func FromMHD(bundle map[string]any, resolvePatient func(ref string) string) (*SubmitObjectsRequest, map[string][]byte, error) {
	if s(bundle["resourceType"]) != "Bundle" || s(bundle["type"]) != "transaction" {
		return nil, nil, fmt.Errorf("an MHD Provide Document Bundle is a transaction Bundle")
	}
	byURL := map[string]map[string]any{}
	var list map[string]any
	var docs []map[string]any
	for _, e := range l(bundle["entry"]) {
		em := m(e)
		res := m(em["resource"])
		byURL[s(em["fullUrl"])] = res
		switch s(res["resourceType"]) {
		case "List":
			if list != nil {
				return nil, nil, fmt.Errorf("the Bundle has more than one List; MHD Provide Document Bundle has exactly one SubmissionSet")
			}
			list = res
		case "DocumentReference":
			docs = append(docs, res)
		}
	}
	if list == nil {
		return nil, nil, fmt.Errorf("the Bundle has no List: the SubmissionSet is required")
	}
	if len(docs) == 0 {
		return nil, nil, fmt.Errorf("the Bundle has no DocumentReference")
	}
	ssPatient := resolvePatient(s(m(list["subject"])["reference"]))
	if ssPatient == "" {
		return nil, nil, fmt.Errorf("the SubmissionSet's subject is not a Patient with an identifier in an urn:oid: system")
	}
	req := &SubmitObjectsRequest{}
	content := map[string][]byte{}
	const ss = "SubmissionSet01"
	set := Object{ID: ss, ObjectType: TypeRegistryPackage, Name: Intl(s(list["title"])),
		Slots: []Slot{{Name: "submissionTime", Values: []string{dtmOf(s(list["date"]))}}}}
	if set.Slot1("submissionTime") == "" {
		set.SetSlot("submissionTime", time.Now().UTC().Format("20060102150405"))
	}
	var ssUID, source string
	// The uniqueId is the urn:oid: identifier (MHD's use official identifier is the entryUUID).
	for _, id := range l(list["identifier"]) {
		if v := s(m(id)["value"]); strings.HasPrefix(v, "urn:oid:") {
			ssUID = strings.TrimPrefix(v, "urn:oid:")
		}
	}
	for _, e := range l(list["extension"]) {
		if s(m(e)["url"]) == MHDSourceIDExt {
			source = strings.TrimPrefix(s(m(m(e)["valueIdentifier"])["value"]), "urn:oid:")
		}
	}
	if ssUID == "" {
		ssUID = UUIDOID()
	}
	if source == "" {
		return nil, nil, fmt.Errorf("the SubmissionSet has no sourceId (extension %s)", MHDSourceIDExt)
	}
	contentType := Code{}
	for _, e := range l(list["extension"]) {
		if strings.HasSuffix(s(m(e)["url"]), "designationType") {
			contentType = codeOf(m(e)["valueCodeableConcept"])
		}
	}
	if contentType.Code == "" {
		contentType = codeOf(list["code"])
	}
	set.Classifications = []Classification{classification(SchemeContentType, ss, contentType)}
	set.ExternalIDs = []ExternalIdentifier{extID(IDSSUniqueID, ss, ssUID, "XDSSubmissionSet.uniqueId"),
		extID(IDSSSourceID, ss, source, "XDSSubmissionSet.sourceId"), extID(IDSSPatientID, ss, ssPatient, "XDSSubmissionSet.patientId")}
	req.Objects.Packages = []Object{set}
	req.Objects.Classifications = []Classification{{ID: "urn:uuid:" + newUUID(), ObjectType: TypeClassification,
		ClassificationNode: NodeSubmissionSet, ClassifiedObject: ss}}

	for i, dr := range docs {
		id := fmt.Sprintf("Document%02d", i+1)
		att := m(m(l(dr["content"])[0])["attachment"])
		mt := s(att["contentType"])
		var data []byte
		switch {
		case s(att["data"]) != "":
			b, err := base64.StdEncoding.DecodeString(s(att["data"]))
			if err != nil {
				return nil, nil, fmt.Errorf("DocumentReference %d: attachment.data is not base64", i+1)
			}
			data = b
		case s(att["url"]) != "":
			bin := byURL[s(att["url"])]
			if bin == nil || s(bin["resourceType"]) != "Binary" {
				return nil, nil, fmt.Errorf("DocumentReference %d: attachment.url %s is not a Binary in the Bundle", i+1, s(att["url"]))
			}
			b, err := base64.StdEncoding.DecodeString(s(bin["data"]))
			if err != nil {
				return nil, nil, fmt.Errorf("DocumentReference %d: the Binary's data is not base64", i+1)
			}
			data = b
			if mt == "" {
				mt = s(bin["contentType"])
			}
		default:
			return nil, nil, fmt.Errorf("DocumentReference %d has neither attachment.data nor a Binary", i+1)
		}
		content[id] = data
		uid := ""
		for _, mi := range []any{dr["masterIdentifier"]} {
			uid = strings.TrimPrefix(s(m(mi)["value"]), "urn:oid:")
		}
		if uid == "" {
			uid = UUIDOID()
		}
		patient := resolvePatient(s(m(dr["subject"])["reference"]))
		if patient == "" {
			patient = ssPatient
		}
		de := Object{ID: id, ObjectType: TypeStableDocument, MimeType: mt, Name: Intl(s(att["title"]) + s(dr["description"])),
			Slots: []Slot{{Name: "creationTime", Values: []string{dtmOf(s(att["creation"]) + "")}}}}
		if de.Slot1("creationTime") == "" {
			de.SetSlot("creationTime", dtmOf(s(dr["date"])))
		}
		lang := s(att["language"])
		if lang == "" {
			lang = "en-US"
		}
		de.SetSlot("languageCode", lang)
		src := patient
		if sp := m(m(dr["context"])["sourcePatientInfo"]); sp != nil {
			if r := resolvePatient(s(sp["reference"])); r != "" {
				src = r
			}
		}
		de.SetSlot("sourcePatientId", src)
		de.SetSlot("size", strconv.Itoa(len(data)))
		if p := m(m(dr["context"])["period"]); p != nil {
			if v := dtmOf(s(p["start"])); v != "" {
				de.SetSlot("serviceStartTime", v)
			}
			if v := dtmOf(s(p["end"])); v != "" {
				de.SetSlot("serviceStopTime", v)
			}
		}
		conf := Code{"N", "2.16.840.1.113883.5.25", "normal"}
		if sl := l(dr["securityLabel"]); len(sl) > 0 {
			conf = codeOf(sl[0])
		}
		var cats []any = l(dr["category"])
		class := Code{}
		if len(cats) > 0 {
			class = codeOf(cats[0])
		}
		ctx := m(dr["context"])
		format := Code{}
		if c := l(dr["content"]); len(c) > 0 {
			f := m(m(c[0])["format"])
			if s(f["code"]) != "" {
				format = codeOf(map[string]any{"coding": []any{f}})
			}
		}
		de.Classifications = []Classification{classification(SchemeClassCode, id, class),
			classification(SchemeConfidentiality, id, conf), classification(SchemeFormatCode, id, format),
			classification(SchemeFacilityType, id, codeOf(ctx["facilityType"])),
			classification(SchemePracticeSetting, id, codeOf(ctx["practiceSetting"])), classification(SchemeTypeCode, id, codeOf(dr["type"]))}
		for _, ev := range l(ctx["event"]) {
			de.Classifications = append(de.Classifications, classification(SchemeEventCode, id, codeOf(ev)))
		}
		de.ExternalIDs = []ExternalIdentifier{extID(IDDocPatientID, id, patient, "XDSDocumentEntry.patientId"),
			extID(IDDocUniqueID, id, uid, "XDSDocumentEntry.uniqueId")}
		req.Objects.Extrinsic = append(req.Objects.Extrinsic, de)
		req.Objects.Associations = append(req.Objects.Associations, Object{ID: "urn:uuid:" + newUUID(), ObjectType: TypeAssociation,
			AssociationType: AssocHasMember, SourceObject: ss, TargetObject: id,
			Slots: []Slot{{Name: "SubmissionSetStatus", Values: []string{"Original"}}}})
		for _, rel := range l(dr["relatesTo"]) {
			rm := m(rel)
			if s(rm["code"]) != "replaces" {
				continue
			}
			target := strings.TrimPrefix(s(m(rm["target"])["reference"]), "DocumentReference/")
			if !strings.HasPrefix(target, "urn:uuid:") {
				target = "urn:uuid:" + target
			}
			req.Objects.Associations = append(req.Objects.Associations, Object{ID: "urn:uuid:" + newUUID(), ObjectType: TypeAssociation,
				AssociationType: AssocReplace, SourceObject: id, TargetObject: target})
		}
	}
	return req, content, nil
}

// ToDocumentReference maps a DocumentEntry to an MHD DocumentReference, its document at binaryURL. The id is the
// entryUUID without urn:uuid:, so ITI-67 finds a document registered over SOAP.
func ToDocumentReference(o Object, patientRef, binaryURL string) map[string]any {
	code := func(c Code) map[string]any {
		sys := map[string]string{"2.16.840.1.113883.6.1": "http://loinc.org", "2.16.840.1.113883.6.96": "http://snomed.info/sct",
			"2.16.840.1.113883.5.25": "http://terminology.hl7.org/CodeSystem/v3-Confidentiality"}[c.Scheme]
		if sys == "" {
			sys = "urn:oid:" + c.Scheme
		}
		coding := map[string]any{"system": sys, "code": c.Code}
		if c.Display != "" {
			coding["display"] = c.Display
		}
		return map[string]any{"coding": []any{coding}}
	}
	status := "current"
	if o.Status == StatusDeprecated {
		status = "superseded"
	}
	dr := map[string]any{"resourceType": "DocumentReference", "id": strings.TrimPrefix(o.ID, "urn:uuid:"),
		"meta":             map[string]any{"profile": []any{MHDDocumentProfile}},
		"masterIdentifier": map[string]any{"system": "urn:ietf:rfc:3986", "value": "urn:oid:" + o.ExternalID(IDDocUniqueID)},
		"identifier":       []any{map[string]any{"use": "official", "system": "urn:ietf:rfc:3986", "value": o.ID}},
		"status":           status, "subject": map[string]any{"reference": patientRef}}
	if c := first(o.Codes(SchemeTypeCode)); c.Code != "" {
		dr["type"] = code(c)
	}
	if c := first(o.Codes(SchemeClassCode)); c.Code != "" {
		dr["category"] = []any{code(c)}
	}
	if c := first(o.Codes(SchemeConfidentiality)); c.Code != "" {
		dr["securityLabel"] = []any{code(c)}
	}
	att := map[string]any{"contentType": o.MimeType, "url": binaryURL, "language": o.Slot1("languageCode")}
	if t := o.Name.Text(); t != "" {
		att["title"] = t
	}
	if v := o.Slot1("size"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			att["size"] = n
		}
	}
	if h := o.Slot1("hash"); h != "" {
		if b, err := hexDecode(h); err == nil {
			att["hash"] = base64.StdEncoding.EncodeToString(b)
		}
	}
	if ct := dtmToFHIR(o.Slot1("creationTime")); ct != "" {
		att["creation"] = ct
		dr["date"] = ct
	}
	content := map[string]any{"attachment": att}
	if f := first(o.Codes(SchemeFormatCode)); f.Code != "" {
		sys := "urn:oid:" + f.Scheme
		if f.Scheme == formatScheme {
			sys = "http://ihe.net/fhir/ihe.formatcode.fhir/CodeSystem/formatcode"
		}
		format := map[string]any{"system": sys, "code": f.Code}
		if f.Display != "" {
			format["display"] = f.Display
		}
		content["format"] = format
	}
	dr["content"] = []any{content}
	ctx := map[string]any{}
	if c := first(o.Codes(SchemeFacilityType)); c.Code != "" {
		ctx["facilityType"] = code(c)
	}
	if c := first(o.Codes(SchemePracticeSetting)); c.Code != "" {
		ctx["practiceSetting"] = code(c)
	}
	start, stop := dtmToFHIR(o.Slot1("serviceStartTime")), dtmToFHIR(o.Slot1("serviceStopTime"))
	if start != "" || stop != "" {
		p := map[string]any{}
		if start != "" {
			p["start"] = start
		}
		if stop != "" {
			p["end"] = stop
		}
		ctx["period"] = p
	}
	if len(ctx) > 0 {
		dr["context"] = ctx
	}
	return dr
}

func first(c []Code) Code {
	if len(c) > 0 {
		return c[0]
	}
	return Code{}
}

func hexDecode(h string) ([]byte, error) {
	if len(h)%2 != 0 {
		return nil, fmt.Errorf("odd hex")
	}
	out := make([]byte, len(h)/2)
	for i := range out {
		v, err := strconv.ParseUint(h[2*i:2*i+2], 16, 8)
		if err != nil {
			return nil, err
		}
		out[i] = byte(v)
	}
	return out, nil
}

// dtmToFHIR turns a DTM into a FHIR dateTime: YYYY, YYYY-MM, YYYY-MM-DD, or an instant in UTC.
func dtmToFHIR(d string) string {
	switch len(d) {
	case 4:
		return d
	case 6:
		return d[:4] + "-" + d[4:6]
	case 8:
		return d[:4] + "-" + d[4:6] + "-" + d[6:8]
	case 10, 12, 14:
		p := pad(d)
		return p[:4] + "-" + p[4:6] + "-" + p[6:8] + "T" + p[8:10] + ":" + p[10:12] + ":" + p[12:14] + "Z"
	}
	return ""
}
