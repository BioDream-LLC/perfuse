package xds

import (
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/cda"
)

// CDA templates whose formatCode IHE and HL7 publish (the formatCode value set, code system 1.3.6.1.4.1.19376.1.2.3).
const formatScheme = "1.3.6.1.4.1.19376.1.2.3"

// FormatFor names a CDA document's formatCode from its templates: C-CDA 2.1 or 1.1 structured body, IHE XDS-SD
// scanned documents, or mimeTypeSufficient when nothing more specific is claimed.
func FormatFor(templates []string, raw []byte) Code {
	has := func(t string) bool {
		for _, v := range templates {
			if v == t || strings.HasPrefix(v, t+":") || strings.HasPrefix(v, t+"^") {
				return true
			}
		}
		return false
	}
	switch {
	case has("1.3.6.1.4.1.19376.1.2.20"):
		if strings.Contains(string(raw), `mediaType="application/pdf"`) {
			return Code{"urn:ihe:iti:xds-sd:pdf:2008", formatScheme, "Scanned Documents (PDF)"}
		}
		return Code{"urn:ihe:iti:xds-sd:text:2008", formatScheme, "Scanned Documents (text)"}
	case has("2.16.840.1.113883.10.20.22.1.1") && strings.Contains(string(raw), `extension="2015-08-01"`):
		return Code{"urn:hl7-org:sdwg:ccda-structuredBody:2.1", formatScheme, "C-CDA R2.1 structured body"}
	case has("2.16.840.1.113883.10.20.22.1.1"):
		return Code{"urn:hl7-org:sdwg:ccda-structuredBody:1.1", formatScheme, "C-CDA R1.1 structured body"}
	}
	return Code{"urn:ihe:iti:xds:2017:mimeTypeSufficient", formatScheme, "mimeType Sufficient"}
}

// CDAOptions are what a submission needs that a CDA document does not say.
type CDAOptions struct {
	SourceID        string
	PatientDomain   string
	FacilityType    Code
	PracticeSetting Code
	Class           *Code
	ContentType     *Code
}

// parseTS reads a CDA timestamp (YYYYMMDD[hhmm[ss]][+-zzzz]) into UTC.
func parseTS(ts string) (time.Time, bool) {
	ts = strings.TrimSpace(ts)
	if ts == "" {
		return time.Time{}, false
	}
	zone := ""
	if i := strings.IndexAny(ts, "+-"); i > 0 {
		ts, zone = ts[:i], ts[i:]
	}
	if i := strings.IndexByte(ts, '.'); i > 0 {
		ts = ts[:i]
	}
	layouts := map[int]string{4: "2006", 6: "200601", 8: "20060102", 10: "2006010215", 12: "200601021504", 14: "20060102150405"}
	layout, ok := layouts[len(ts)]
	if !ok {
		return time.Time{}, false
	}
	if zone != "" {
		t, err := time.Parse(layout+"-0700", ts+zone)
		return t.UTC(), err == nil
	}
	t, err := time.Parse(layout, ts)
	return t.UTC(), err == nil
}

// MetaFromCDA builds the metadata of a CDA document's submission.
func MetaFromCDA(d *cda.Document, raw []byte, o CDAOptions) (DocumentMeta, error) {
	m := DocumentMeta{SourceID: o.SourceID, Title: d.Title, MimeType: "text/xml", FacilityType: o.FacilityType,
		PracticeSetting: o.PracticeSetting, Language: d.LanguageCode}
	// The patient, by the affinity domain's identifier.
	var chosen *cda.Identifier
	ids := d.Patient.Identifiers
	for i := range ids {
		if o.PatientDomain != "" && ids[i].Root == o.PatientDomain && ids[i].Extension != "" {
			chosen = &ids[i]
		}
	}
	if chosen == nil && o.PatientDomain == "" {
		var usable []cda.Identifier
		for _, id := range ids {
			if id.Root != "" && id.Extension != "" {
				usable = append(usable, id)
			}
		}
		if len(usable) == 1 {
			chosen = &usable[0]
		} else if len(usable) > 1 {
			return m, fmt.Errorf("%w: the patient has %d identifiers; set patient_domain to the affinity domain's OID to say "+
				"which is registered", ErrIncomplete, len(usable))
		}
	}
	if chosen == nil {
		if o.PatientDomain != "" {
			return m, fmt.Errorf("%w: the patient has no identifier in the affinity domain %s", ErrIncomplete, o.PatientDomain)
		}
		return m, fmt.Errorf("%w: the document's patient has no identifier with a root and an extension", ErrIncomplete)
	}
	m.PatientID = chosen.Extension + "^^^&" + chosen.Root + "&ISO"
	for _, id := range ids {
		if id.Root != "" && id.Extension != "" && (id.Root != chosen.Root || id.Extension != chosen.Extension) {
			m.SourcePatientID = id.Extension + "^^^&" + id.Root + "&ISO"
			break
		}
	}
	// The document's own id as its uniqueId: root, or root^extension (an XDS uniqueId may be either, up to 128 characters).
	if d.Tree != nil {
		if idn := d.Tree.First("id"); idn != nil {
			root, ext := idn.AttrValue("root"), idn.AttrValue("extension")
			switch {
			case root != "" && ext != "":
				m.UniqueID = root + "^" + ext
			case root != "":
				m.UniqueID = root
			}
			if len(m.UniqueID) > 128 {
				m.UniqueID = ""
			}
		}
	}
	if d.TypeCode == "" {
		return m, fmt.Errorf("%w: the document has no code, so its type is unknown", ErrIncomplete)
	}
	sys := d.TypeSystem
	if sys == "" {
		sys = "2.16.840.1.113883.6.1"
	}
	m.Type = Code{d.TypeCode, sys, d.TypeName}
	m.Class = m.Type
	if o.Class != nil && o.Class.Code != "" {
		m.Class = *o.Class
	}
	m.ContentType = m.Class
	if o.ContentType != nil && o.ContentType.Code != "" {
		m.ContentType = *o.ContentType
	}
	m.Format = FormatFor(d.TemplateIDs, raw)
	conf := d.Confidentiality
	if conf == "" {
		conf = "N"
	}
	confName := map[string]string{"N": "normal", "R": "restricted", "V": "very restricted", "L": "low", "M": "moderate", "U": "unrestricted"}[conf]
	m.Confidentiality = Code{conf, "2.16.840.1.113883.5.25", confName}
	if t, ok := parseTS(d.EffectiveTime); ok {
		m.CreationTime = t
	}
	if d.Encounter != nil {
		if t, ok := parseTS(d.Encounter.Start); ok {
			m.ServiceStart = t
		}
		if t, ok := parseTS(d.Encounter.End); ok {
			m.ServiceStop = t
		}
	}
	if len(d.Authors) > 0 {
		a := d.Authors[0]
		if a.Family != "" || a.Given != "" {
			m.AuthorPerson = "^" + a.Family + "^" + a.Given + "^^^"
		}
		if a.Organisation != "" {
			m.AuthorInstitution = a.Organisation
		}
	}
	if m.AuthorInstitution == "" && d.Custodian != "" {
		m.AuthorInstitution = d.Custodian
	}
	return m, nil
}
