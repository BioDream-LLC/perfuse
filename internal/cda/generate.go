package cda

import (
	"fmt"
	"sort"
	"strings"
)

// Generating clinical documents.
//
// The rest of this package reads documents. This writes them, which an integration engine needs
// for the direction nobody plans for at the start: a site that receives HL7 v2 and has to hand a
// summary to a system that only speaks C-CDA, or one that has to produce a transition-of-care
// document because a receiving facility will not accept anything else.
//
// # The rule that decides whether a generated document is usable
//
// C-CDA carries every clinical fact twice: once as coded entries a machine can act on, and once as
// narrative a clinician reads. They are not alternatives. A generator that emits entries and leaves
// the narrative empty produces a document that validates against the schema, passes a receiver's
// import, and then displays as a blank page - because most viewers render the narrative and ignore
// the entries entirely. The patient's medication list is in the file and invisible.
//
// So narrative is generated from the entries whenever a caller does not supply it, and every entry
// is given an identifier that the narrative references. That reference is what lets a viewer
// highlight the coded entry behind a line of text, and its absence is what makes half the C-CDA in
// the wild unreadable.
//
// # Element order is not cosmetic
//
// CDA's schema is a sequence, not a set. recordTarget must precede author, author must precede
// custodian, and componentOf must follow custodian. A document with the right elements in the wrong
// order is rejected outright, so this builds the header in a fixed order rather than iterating a map
// of whatever the caller happened to set.

// Well-known OIDs used when generating.
const (
	oidLOINC           = "2.16.840.1.113883.6.1"
	oidSNOMED          = "2.16.840.1.113883.6.96"
	oidRxNorm          = "2.16.840.1.113883.6.88"
	oidConfidentiality = "2.16.840.1.113883.5.25"
	oidGender          = "2.16.840.1.113883.5.1"
	oidActClass        = "2.16.840.1.113883.5.6"

	// templateUSRealmHeader is claimed by every C-CDA document.
	templateUSRealmHeader = "2.16.840.1.113883.10.20.22.1.1"
	// templateCCD is the Continuity of Care Document.
	templateCCD = "2.16.840.1.113883.10.20.22.1.2"

	// cdaTypeID is the fixed HL7 CDA R2 type identifier every document carries.
	cdaTypeIDRoot      = "2.16.840.1.113883.1.3"
	cdaTypeIDExtension = "POCD_HD000040"

	// ccdCode is the LOINC code for a Continuity of Care Document.
	ccdCode = "34133-9"
	ccdName = "Summarization of Episode Note"
)

// sectionSpec is the code and template a generated section carries for a given kind.
//
// Generation needs the reverse of the recognition maps in vocabulary.go: those answer "what is this
// section", this answers "what identifies the section I am writing". Kept as its own table rather
// than inverting sectionTemplates, because that map is deliberately many-to-one - it lists both the
// R1 and R2 template identifiers so either is recognised - and inverting it would pick one of them
// arbitrarily. A generator has to choose deliberately, and it chooses the R2.1 entries-required
// templates because those are what a current receiver validates against.
var generateSectionSpecs = map[string]sectionSpec{
	"Allergies":             {code: "48765-2", template: "2.16.840.1.113883.10.20.22.2.6.1", title: "Allergies and Adverse Reactions"},
	"Medications":           {code: "10160-0", template: "2.16.840.1.113883.10.20.22.2.1.1", title: "Medications"},
	"Problems":              {code: "11450-4", template: "2.16.840.1.113883.10.20.22.2.5.1", title: "Problem List"},
	"Results":               {code: "30954-2", template: "2.16.840.1.113883.10.20.22.2.3.1", title: "Results"},
	"Vital signs":           {code: "8716-3", template: "2.16.840.1.113883.10.20.22.2.4.1", title: "Vital Signs"},
	"Procedures":            {code: "47519-4", template: "2.16.840.1.113883.10.20.22.2.7.1", title: "Procedures"},
	"Immunisations":         {code: "11369-6", template: "2.16.840.1.113883.10.20.22.2.2.1", title: "Immunizations"},
	"Social history":        {code: "29762-2", template: "2.16.840.1.113883.10.20.22.2.17", title: "Social History"},
	"Encounters":            {code: "46240-8", template: "2.16.840.1.113883.10.20.22.2.22.1", title: "Encounters"},
	"Plan of treatment":     {code: "18776-5", template: "2.16.840.1.113883.10.20.22.2.10", title: "Plan of Treatment"},
	"Functional status":     {code: "47420-5", template: "2.16.840.1.113883.10.20.22.2.14", title: "Functional Status"},
	"Advance directives":    {code: "42348-3", template: "2.16.840.1.113883.10.20.22.2.21.1", title: "Advance Directives"},
	"Payers":                {code: "48768-6", template: "2.16.840.1.113883.10.20.22.2.18", title: "Payers"},
	"Family history":        {code: "10157-6", template: "2.16.840.1.113883.10.20.22.2.15", title: "Family History"},
	"Medical equipment":     {code: "46264-8", template: "2.16.840.1.113883.10.20.22.2.23", title: "Medical Equipment"},
	"Past medical history":  {code: "11348-0", template: "2.16.840.1.113883.10.20.22.2.20", title: "Past Medical History"},
	"Discharge medications": {code: "10183-2", template: "2.16.840.1.113883.10.20.22.2.11.1", title: "Discharge Medications"},
}

type sectionSpec struct {
	code     string
	template string
	title    string
}

// GenerateOptions controls document generation.
type GenerateOptions struct {
	// DocumentType selects the template and code claimed. Only "CCD" is supported; anything else
	// is refused rather than guessed at, because a document claiming a template it does not satisfy
	// is worse than one claiming none.
	DocumentType string

	// CustodianName is the organisation responsible for the document. C-CDA requires a custodian,
	// so generation fails without one rather than emitting a document a receiver will reject.
	CustodianName string

	// Title overrides the default title for the document type.
	Title string

	// Indent writes the document with newlines and indentation. Off by default: the wire form is
	// what gets sent, and whitespace inside a narrative element is significant.
	Indent bool
}

// Generate writes a C-CDA document from a Document struct.
//
// The Document need not have come from Parse. A caller assembling one from HL7 v2 or from FHIR sets
// the patient, the sections and their entries, and this produces conformant XML.
//
// Narrative is generated for any section that has entries but no NarrativeText, because a section
// whose narrative is empty displays as blank in most viewers regardless of what its entries say.
func Generate(d *Document, opts GenerateOptions) ([]byte, error) {
	if d == nil {
		return nil, fmt.Errorf("cda: cannot generate from a nil document")
	}
	if opts.DocumentType == "" {
		opts.DocumentType = "CCD"
	}
	if opts.DocumentType != "CCD" {
		return nil, fmt.Errorf("cda: document type %q is not supported for generation (only CCD)", opts.DocumentType)
	}
	if strings.TrimSpace(opts.CustodianName) == "" && strings.TrimSpace(d.Custodian) == "" {
		// Refused at generation rather than emitted and rejected on import. C-CDA makes custodian
		// mandatory, and a receiver's error message for a missing one names an element rather than
		// the decision that led to it.
		return nil, fmt.Errorf("cda: a custodian is required; set GenerateOptions.CustodianName or Document.Custodian")
	}
	if strings.TrimSpace(d.Patient.Family) == "" {
		return nil, fmt.Errorf("cda: the patient needs at least a family name")
	}
	if len(d.Patient.Identifiers) == 0 {
		return nil, fmt.Errorf("cda: the patient needs at least one identifier, or the document cannot be matched to a person")
	}

	custodian := opts.CustodianName
	if custodian == "" {
		custodian = d.Custodian
	}

	g := &generator{indent: opts.Indent}

	g.raw(`<?xml version="1.0" encoding="UTF-8"?>`)
	g.nl()
	g.open("ClinicalDocument", attr{"xmlns", "urn:hl7-org:v3"})

	// Header, in schema order.
	g.empty("realmCode", attr{"code", "US"})
	g.empty("typeId", attr{"root", cdaTypeIDRoot}, attr{"extension", cdaTypeIDExtension})
	g.empty("templateId", attr{"root", templateUSRealmHeader}, attr{"extension", "2015-08-01"})
	g.empty("templateId", attr{"root", templateCCD}, attr{"extension", "2015-08-01"})

	g.empty("id", idAttrs(d.ID)...)
	g.empty("code", attr{"code", ccdCode}, attr{"codeSystem", oidLOINC}, attr{"displayName", ccdName})

	title := opts.Title
	if title == "" {
		title = d.Title
	}
	if title == "" {
		title = ccdName
	}
	g.text("title", title)

	g.empty("effectiveTime", attr{"value", nonEmpty(d.EffectiveTime, "00000000")})
	g.empty("confidentialityCode",
		attr{"code", nonEmpty(d.Confidentiality, "N")},
		attr{"codeSystem", oidConfidentiality})
	g.empty("languageCode", attr{"code", nonEmpty(d.LanguageCode, "en-US")})

	if d.SetID != "" {
		g.empty("setId", idAttrs(d.SetID)...)
	}
	if d.Version != "" {
		g.empty("versionNumber", attr{"value", d.Version})
	}

	g.recordTarget(d.Patient)
	g.authors(d.Authors, d.EffectiveTime)
	g.custodian(custodian)
	if d.Encounter != nil {
		g.componentOf(d.Encounter)
	}

	// Body.
	g.open("component")
	g.open("structuredBody")
	for _, s := range d.Sections {
		g.section(s)
	}
	g.close("structuredBody")
	g.close("component")

	g.close("ClinicalDocument")
	g.nl()

	return []byte(g.b.String()), nil
}

// recordTarget writes the patient.
func (g *generator) recordTarget(p Patient) {
	g.open("recordTarget")
	g.open("patientRole")

	for _, id := range p.Identifiers {
		var as []attr
		if id.Root != "" {
			as = append(as, attr{"root", id.Root})
		}
		if id.Extension != "" {
			as = append(as, attr{"extension", id.Extension})
		}
		g.empty("id", as...)
	}

	if p.Address != nil {
		g.address(*p.Address)
	}
	if p.Phone != "" {
		g.empty("telecom", attr{"value", telecomValue(p.Phone)}, attr{"use", "HP"})
	}

	g.open("patient")
	g.open("name")
	if p.Prefix != "" {
		g.text("prefix", p.Prefix)
	}
	for _, given := range p.Given {
		g.text("given", given)
	}
	g.text("family", p.Family)
	if p.Suffix != "" {
		g.text("suffix", p.Suffix)
	}
	g.close("name")

	if p.Gender != "" {
		g.empty("administrativeGenderCode", attr{"code", p.Gender}, attr{"codeSystem", oidGender})
	} else {
		// An absent gender is stated as unknown rather than omitted. The element is required, and
		// a receiver treats its absence as a schema failure rather than as missing information.
		g.empty("administrativeGenderCode", attr{"nullFlavor", "UNK"})
	}

	if p.BirthTime != "" {
		g.empty("birthTime", attr{"value", p.BirthTime})
	} else {
		g.empty("birthTime", attr{"nullFlavor", "UNK"})
	}

	g.close("patient")
	g.close("patientRole")
	g.close("recordTarget")
}

func (g *generator) address(a Address) {
	var as []attr
	if a.Use != "" {
		as = append(as, attr{"use", a.Use})
	}
	g.open("addr", as...)
	for _, line := range a.Lines {
		g.text("streetAddressLine", line)
	}
	if a.City != "" {
		g.text("city", a.City)
	}
	if a.State != "" {
		g.text("state", a.State)
	}
	if a.Postal != "" {
		g.text("postalCode", a.Postal)
	}
	if a.Country != "" {
		g.text("country", a.Country)
	}
	g.close("addr")
}

// authors writes the author elements.
//
// C-CDA requires at least one. When the caller supplied none, a device author standing for this
// engine is written rather than omitting the element: a document with no author is rejected, and
// naming the software that produced it is more honest than inventing a person.
func (g *generator) authors(authors []Author, docTime string) {
	if len(authors) == 0 {
		g.open("author")
		g.empty("time", attr{"value", nonEmpty(docTime, "00000000")})
		g.open("assignedAuthor")
		g.empty("id", attr{"nullFlavor", "NA"})
		g.open("assignedAuthoringDevice")
		g.text("softwareName", "Perfuse")
		g.close("assignedAuthoringDevice")
		g.close("assignedAuthor")
		g.close("author")
		return
	}

	for _, a := range authors {
		g.open("author")
		g.empty("time", attr{"value", nonEmpty(a.Time, nonEmpty(docTime, "00000000"))})
		g.open("assignedAuthor")
		if a.ID != "" {
			g.empty("id", attr{"root", a.ID})
		} else {
			g.empty("id", attr{"nullFlavor", "NA"})
		}
		if a.Family != "" || a.Given != "" {
			g.open("assignedPerson")
			g.open("name")
			if a.Given != "" {
				g.text("given", a.Given)
			}
			if a.Family != "" {
				g.text("family", a.Family)
			}
			g.close("name")
			g.close("assignedPerson")
		}
		if a.Organisation != "" {
			g.open("representedOrganization")
			g.text("name", a.Organisation)
			g.close("representedOrganization")
		}
		g.close("assignedAuthor")
		g.close("author")
	}
}

func (g *generator) custodian(name string) {
	g.open("custodian")
	g.open("assignedCustodian")
	g.open("representedCustodianOrganization")
	g.empty("id", attr{"nullFlavor", "NA"})
	g.text("name", name)
	g.close("representedCustodianOrganization")
	g.close("assignedCustodian")
	g.close("custodian")
}

func (g *generator) componentOf(e *Encounter) {
	g.open("componentOf")
	g.open("encompassingEncounter")
	if e.ID != "" {
		g.empty("id", idAttrs(e.ID)...)
	} else {
		g.empty("id", attr{"nullFlavor", "NA"})
	}
	if e.Code != "" {
		g.empty("code", attr{"code", e.Code}, attr{"codeSystem", oidSNOMED})
	}

	// A period with only a start is written as a low with no high, not as a point in time.
	// Collapsing them loses the distinction between "the visit began then" and "the visit was then".
	if e.Start != "" || e.End != "" {
		g.open("effectiveTime")
		if e.Start != "" {
			g.empty("low", attr{"value", e.Start})
		} else {
			g.empty("low", attr{"nullFlavor", "UNK"})
		}
		if e.End != "" {
			g.empty("high", attr{"value", e.End})
		}
		g.close("effectiveTime")
	}

	if e.Location != "" {
		g.open("location")
		g.open("healthCareFacility")
		g.open("location")
		g.text("name", e.Location)
		g.close("location")
		g.close("healthCareFacility")
		g.close("location")
	}
	g.close("encompassingEncounter")
	g.close("componentOf")
}

// section writes one section, generating narrative from the entries when none was supplied.
func (g *generator) section(s Section) {
	spec, known := generateSectionSpecs[s.Kind]

	g.open("component")

	var sectionAttrs []attr
	if s.NilFlavor != "" {
		sectionAttrs = append(sectionAttrs, attr{"nullFlavor", s.NilFlavor})
	}
	g.open("section", sectionAttrs...)

	// Template identifiers: whatever the caller carried, else the one for this kind.
	if len(s.TemplateIDs) > 0 {
		for _, t := range s.TemplateIDs {
			g.empty("templateId", attr{"root", t})
		}
	} else if known {
		g.empty("templateId", attr{"root", spec.template}, attr{"extension", "2015-08-01"})
	}

	code := s.Code
	codeSystem := s.CodeSystem
	if code == "" && known {
		code = spec.code
		codeSystem = oidLOINC
	}
	if code != "" {
		as := []attr{{"code", code}, {"codeSystem", nonEmpty(codeSystem, oidLOINC)}}
		if s.CodeName != "" {
			as = append(as, attr{"displayName", s.CodeName})
		}
		g.empty("code", as...)
	}

	title := s.Title
	if title == "" && known {
		title = spec.title
	}
	if title != "" {
		g.text("title", title)
	}

	// Narrative. Generated from the entries when absent, because a section with entries and no
	// narrative renders blank in a viewer that reads only the narrative - which is most of them.
	narrative := s.NarrativeText
	generated := false
	if strings.TrimSpace(narrative) == "" && len(s.Entries) > 0 {
		generated = true
	}

	g.open("text")
	switch {
	case generated:
		g.narrativeTable(s.Entries)
	case strings.TrimSpace(narrative) != "":
		g.open("paragraph")
		g.chardata(narrative)
		g.close("paragraph")
	case s.NilFlavor != "":
		g.open("paragraph")
		g.chardata(nilFlavorMeaning(s.NilFlavor))
		g.close("paragraph")
	}
	g.close("text")

	for i, e := range s.Entries {
		g.entry(e, entryRefID(i))
	}

	g.close("section")
	g.close("component")
}

// narrativeTable writes entries as a narrative table with an ID on each row.
//
// The IDs matter: an entry's <text><reference value="#id"/> points at the row describing it, which
// is what lets a viewer highlight the coded entry behind a line the clinician is reading. Written
// as a table rather than a list because that is what C-CDA narrative overwhelmingly is, and a
// receiver's stylesheet expects it.
func (g *generator) narrativeTable(entries []Entry) {
	g.open("table", attr{"border", "1"}, attr{"width", "100%"})
	g.open("thead")
	g.open("tr")
	g.text("th", "Description")
	g.text("th", "Value")
	g.text("th", "Date")
	g.close("tr")
	g.close("thead")
	g.open("tbody")
	for i, e := range entries {
		g.open("tr")
		g.open("td", attr{"ID", entryRefID(i)})
		g.chardata(entryDescription(e))
		g.close("td")
		g.text("td", entryValueText(e))
		g.text("td", e.EffectiveTime)
		g.close("tr")
	}
	g.close("tbody")
	g.close("table")
}

// entry writes one clinical statement.
func (g *generator) entry(e Entry, refID string) {
	g.open("entry")
	g.clinicalStatement(e, refID)
	g.close("entry")
}

// clinicalStatement writes the element for an entry's CDA class.
func (g *generator) clinicalStatement(e Entry, refID string) {
	elem := statementElement(e.Kind)

	as := []attr{{"classCode", statementClassCode(e.Kind)}, {"moodCode", "EVN"}}
	if e.NegationInd {
		// Written explicitly rather than omitted when false. A receiver reading a negated entry as
		// positive turns "no penicillin allergy" into "penicillin allergy", and the attribute being
		// present and false is easier to review than its absence.
		as = append(as, attr{"negationInd", "true"})
	}
	g.open(elem, as...)

	if e.Code != "" {
		ca := []attr{{"code", e.Code}, {"codeSystem", nonEmpty(e.CodeSystem, oidSNOMED)}}
		if e.CodeName != "" {
			ca = append(ca, attr{"displayName", e.CodeName})
		}
		g.empty("code", ca...)
	} else if e.NilFlavor != "" {
		g.empty("code", attr{"nullFlavor", e.NilFlavor})
	}

	// The reference back to the narrative row this entry describes.
	if refID != "" {
		g.open("text")
		g.empty("reference", attr{"value", "#" + refID})
		g.close("text")
	}

	if e.StatusCode != "" {
		g.empty("statusCode", attr{"code", e.StatusCode})
	}

	if e.Low != "" || e.High != "" {
		g.open("effectiveTime")
		if e.Low != "" {
			g.empty("low", attr{"value", e.Low})
		}
		if e.High != "" {
			g.empty("high", attr{"value", e.High})
		}
		g.close("effectiveTime")
	} else if e.EffectiveTime != "" {
		g.empty("effectiveTime", attr{"value", e.EffectiveTime})
	}

	g.entryValue(e)

	for i, c := range e.Children {
		g.open("entryRelationship", attr{"typeCode", "COMP"})
		g.clinicalStatement(c, "")
		g.close("entryRelationship")
		_ = i
	}

	g.close(elem)
}

// entryValue writes the value element for whichever kind of result the entry carries.
func (g *generator) entryValue(e Entry) {
	switch {
	case e.ValueCode != "":
		as := []attr{
			{"xsi:type", "CD"},
			{"code", e.ValueCode},
			{"codeSystem", nonEmpty(e.CodeSystem, oidSNOMED)},
		}
		if e.ValueName != "" {
			as = append(as, attr{"displayName", e.ValueName})
		}
		g.emptyWithXSI("value", as...)

	case e.Value != "" && e.Unit != "":
		g.emptyWithXSI("value",
			attr{"xsi:type", "PQ"},
			attr{"value", e.Value},
			attr{"unit", e.Unit})

	case e.Value != "":
		g.openWithXSI("value", attr{"xsi:type", "ST"})
		g.chardata(e.Value)
		g.close("value")

	case e.NilFlavor != "" && e.Code != "":
		// A stated absence of a result, distinct from an entry that simply has no value element.
		g.emptyWithXSI("value", attr{"xsi:type", "CD"}, attr{"nullFlavor", e.NilFlavor})
	}
}

// statementElement maps an entry kind to its CDA element name.
func statementElement(kind string) string {
	switch kind {
	case "SubstanceAdministration":
		return "substanceAdministration"
	case "Procedure":
		return "procedure"
	case "Encounter":
		return "encounter"
	case "Act":
		return "act"
	case "Organizer":
		return "organizer"
	default:
		return "observation"
	}
}

// statementClassCode maps an entry kind to the classCode CDA requires on it.
func statementClassCode(kind string) string {
	switch kind {
	case "SubstanceAdministration":
		return "SBADM"
	case "Procedure":
		return "PROC"
	case "Encounter":
		return "ENC"
	case "Act":
		return "ACT"
	case "Organizer":
		return "BATTERY"
	default:
		return "OBS"
	}
}

// entryDescription renders an entry for the narrative.
func entryDescription(e Entry) string {
	if d := e.Describe(); d != "" {
		return d
	}
	if e.CodeName != "" {
		return e.CodeName
	}
	if e.Code != "" {
		return e.Code
	}
	return "(no description)"
}

// entryValueText renders an entry's result for the narrative column.
func entryValueText(e Entry) string {
	switch {
	case e.ValueName != "":
		return e.ValueName
	case e.Value != "" && e.Unit != "":
		return e.Value + " " + e.Unit
	case e.Value != "":
		return e.Value
	case e.NilFlavor != "":
		return nilFlavorMeaning(e.NilFlavor)
	default:
		return ""
	}
}

// entryRefID is the narrative row identifier for the entry at index i.
func entryRefID(i int) string {
	return fmt.Sprintf("entry%d", i+1)
}

// idAttrs splits a stored identifier back into root and extension.
//
// Parse joins them as "root^extension" when both are present (see identifierOf), so generation has
// to reverse that rather than writing the whole string as a root - which would produce an
// identifier no receiver could match against the one it was given.
func idAttrs(id string) []attr {
	if id == "" {
		return []attr{{"nullFlavor", "NA"}}
	}
	if root, ext, found := strings.Cut(id, "^"); found {
		return []attr{{"root", root}, {"extension", ext}}
	}
	return []attr{{"root", id}}
}

// telecomValue prefixes a bare phone number with the tel: scheme CDA requires.
func telecomValue(phone string) string {
	if strings.Contains(phone, ":") {
		return phone
	}
	return "tel:" + phone
}

func nonEmpty(v, fallback string) string {
	if strings.TrimSpace(v) != "" {
		return v
	}
	return fallback
}

// ──────────────────────────────────────────────────────────────────────────────
// The writer
// ──────────────────────────────────────────────────────────────────────────────

type attr struct {
	name  string
	value string
}

// generator accumulates XML.
//
// Written by hand rather than with encoding/xml because CDA's schema is a sequence: the order of
// elements is fixed, many are conditional, and expressing that with struct tags means either a
// struct per document type or a pile of pointers whose nil-ness encodes the schema. Building it
// directly keeps the order visible in the code that decides it.
type generator struct {
	b      strings.Builder
	depth  int
	indent bool
	// pendingText suppresses indentation inside an element that has character data, because
	// whitespace added around narrative text changes what the clinician reads.
	inText int
}

func (g *generator) raw(s string) { g.b.WriteString(s) }

func (g *generator) nl() {
	if g.indent {
		g.b.WriteByte('\n')
	}
}

func (g *generator) pad() {
	if g.indent && g.inText == 0 {
		for i := 0; i < g.depth; i++ {
			g.b.WriteString("  ")
		}
	}
}

func (g *generator) open(name string, as ...attr) {
	g.pad()
	g.b.WriteByte('<')
	g.b.WriteString(name)
	g.writeAttrs(as)
	g.b.WriteByte('>')
	g.depth++
	if name == "text" || name == "paragraph" || name == "td" || name == "th" {
		g.inText++
	}
	if g.inText == 0 {
		g.nl()
	}
}

func (g *generator) close(name string) {
	if name == "text" || name == "paragraph" || name == "td" || name == "th" {
		g.inText--
	}
	g.depth--
	if g.inText == 0 {
		g.pad()
	}
	g.b.WriteString("</")
	g.b.WriteString(name)
	g.b.WriteByte('>')
	if g.inText == 0 {
		g.nl()
	}
}

func (g *generator) empty(name string, as ...attr) {
	g.pad()
	g.b.WriteByte('<')
	g.b.WriteString(name)
	g.writeAttrs(as)
	g.b.WriteString("/>")
	if g.inText == 0 {
		g.nl()
	}
}

// emptyWithXSI writes an empty element that carries an xsi:type, declaring the namespace inline.
//
// Declared on the element rather than on ClinicalDocument because a document that declares xsi
// but never uses it is noise, and the value elements that need it are the only place it appears.
func (g *generator) emptyWithXSI(name string, as ...attr) {
	as = append([]attr{{"xmlns:xsi", "http://www.w3.org/2001/XMLSchema-instance"}}, as...)
	g.empty(name, as...)
}

func (g *generator) openWithXSI(name string, as ...attr) {
	as = append([]attr{{"xmlns:xsi", "http://www.w3.org/2001/XMLSchema-instance"}}, as...)
	g.open(name, as...)
}

// text writes an element containing character data.
func (g *generator) text(name, value string) {
	g.pad()
	g.b.WriteByte('<')
	g.b.WriteString(name)
	g.b.WriteByte('>')
	g.chardata(value)
	g.b.WriteString("</")
	g.b.WriteString(name)
	g.b.WriteByte('>')
	if g.inText == 0 {
		g.nl()
	}
}

func (g *generator) writeAttrs(as []attr) {
	for _, a := range as {
		g.b.WriteByte(' ')
		g.b.WriteString(a.name)
		g.b.WriteString(`="`)
		g.escapeAttr(a.value)
		g.b.WriteByte('"')
	}
}

// chardata writes text with XML metacharacters escaped.
func (g *generator) chardata(s string) {
	for _, r := range s {
		switch r {
		case '&':
			g.b.WriteString("&amp;")
		case '<':
			g.b.WriteString("&lt;")
		case '>':
			g.b.WriteString("&gt;")
		default:
			g.b.WriteRune(r)
		}
	}
}

func (g *generator) escapeAttr(s string) {
	for _, r := range s {
		switch r {
		case '&':
			g.b.WriteString("&amp;")
		case '<':
			g.b.WriteString("&lt;")
		case '>':
			g.b.WriteString("&gt;")
		case '"':
			g.b.WriteString("&quot;")
		default:
			g.b.WriteRune(r)
		}
	}
}

// GeneratableSections lists the section kinds Generate can identify with a template and code.
//
// Exposed so a caller assembling a document can check whether the section it wants to write will
// carry conformance identifiers, rather than discovering after the fact that it was emitted
// without them.
func GeneratableSections() []string {
	out := make([]string, 0, len(generateSectionSpecs))
	for k := range generateSectionSpecs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
