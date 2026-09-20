package cda

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Document is a parsed clinical document.
//
// The structure is flattened deliberately. A C-CDA is nested five or six levels
// deep before reaching anything a person cares about, and every consumer here -
// the viewer, the narrative check, the FHIR conversion - wants the same short
// list: who it is about, what kind of document it is, and what the sections say.
// Keeping the tree available alongside means nothing is lost.
type Document struct {
	// Title is the document's own title, which is what a clinician recognises it
	// by.
	Title string `json:"title,omitempty"`

	// TypeCode and TypeName come from the document's code element.
	TypeCode   string `json:"typeCode,omitempty"`
	TypeSystem string `json:"typeSystem,omitempty"`
	TypeName   string `json:"typeName,omitempty"`

	// TemplateIDs are the conformance claims the document makes. They are the
	// only reliable way to know which implementation guide applies.
	TemplateIDs []string `json:"templateIds,omitempty"`

	// DocumentType is the recognised kind, derived from the template IDs and the
	// code, in plain words.
	DocumentType string `json:"documentType,omitempty"`

	// ID and SetID identify the document and its revision series.
	ID    string `json:"id,omitempty"`
	SetID string `json:"setId,omitempty"`
	// Version is the revision number within the set.
	Version string `json:"version,omitempty"`

	EffectiveTime   string `json:"effectiveTime,omitempty"`
	Confidentiality string `json:"confidentiality,omitempty"`
	LanguageCode    string `json:"languageCode,omitempty"`

	Patient   Patient    `json:"patient"`
	Authors   []Author   `json:"authors,omitempty"`
	Custodian string     `json:"custodian,omitempty"`
	Encounter *Encounter `json:"encounter,omitempty"`

	Sections []Section `json:"sections"`

	// Transport is what the HL7 v2 message carrying this document said about it,
	// when it arrived that way.
	Transport *DocumentInfo `json:"transport,omitempty"`

	// Notes record anything about the document worth telling somebody, including
	// parts that were present but not understood.
	Notes []Note `json:"notes,omitempty"`

	// Tree is the parsed XML, kept so the viewer can show anything this struct
	// does not model.
	Tree *xtree.Node `json:"-"`
}

// Patient is the subject of the document.
type Patient struct {
	Identifiers []Identifier `json:"identifiers,omitempty"`
	Family      string       `json:"family,omitempty"`
	Given       []string     `json:"given,omitempty"`
	Prefix      string       `json:"prefix,omitempty"`
	Suffix      string       `json:"suffix,omitempty"`
	Gender      string       `json:"gender,omitempty"`
	GenderName  string       `json:"genderName,omitempty"`
	BirthTime   string       `json:"birthTime,omitempty"`
	Address     *Address     `json:"address,omitempty"`
	Phone       string       `json:"phone,omitempty"`
}

// Name renders the patient's name the way a person writes it.
func (p Patient) Name() string {
	parts := append([]string{}, p.Given...)
	if p.Family != "" {
		parts = append(parts, p.Family)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// Identifier is a namespaced identifier.
type Identifier struct {
	// Root is the OID or UUID that namespaces the value. Without it an MRN is
	// ambiguous between facilities, which is the single most common cause of
	// unmatchable patients downstream.
	Root      string `json:"root,omitempty"`
	Extension string `json:"extension,omitempty"`
	// Assigner is a human-readable name for the root when one is recognised.
	Assigner string `json:"assigner,omitempty"`
}

// Address is a postal address.
type Address struct {
	Use     string   `json:"use,omitempty"`
	Lines   []string `json:"lines,omitempty"`
	City    string   `json:"city,omitempty"`
	State   string   `json:"state,omitempty"`
	Postal  string   `json:"postal,omitempty"`
	Country string   `json:"country,omitempty"`
}

// Author is who wrote the document.
type Author struct {
	Time         string `json:"time,omitempty"`
	Family       string `json:"family,omitempty"`
	Given        string `json:"given,omitempty"`
	ID           string `json:"id,omitempty"`
	Organisation string `json:"organisation,omitempty"`
}

// Encounter is the visit the document describes.
type Encounter struct {
	ID       string `json:"id,omitempty"`
	Code     string `json:"code,omitempty"`
	Start    string `json:"start,omitempty"`
	End      string `json:"end,omitempty"`
	Location string `json:"location,omitempty"`
}

// Note is something worth telling somebody about a document.
type Note struct {
	// Severity is "error", "warning" or "info".
	Severity string `json:"severity"`
	// Path locates the problem in the document.
	Path string `json:"path,omitempty"`
	// Message is written for a person, not a machine.
	Message string `json:"message"`
	// Rule names the check, so a recurring note can be looked up.
	Rule string `json:"rule,omitempty"`
}

// Section is one section of the document.
type Section struct {
	// Title is the section heading as written.
	Title string `json:"title,omitempty"`

	Code       string `json:"code,omitempty"`
	CodeSystem string `json:"codeSystem,omitempty"`
	CodeName   string `json:"codeName,omitempty"`

	// TemplateIDs are the section's conformance claims.
	TemplateIDs []string `json:"templateIds,omitempty"`

	// Kind is the recognised section type in plain words, empty when the section
	// is not one this package knows.
	Kind string `json:"kind,omitempty"`

	// NarrativeText is the human-readable content with markup removed. This is
	// what a clinician actually reads, and half of what the narrative check
	// compares.
	NarrativeText string `json:"narrativeText,omitempty"`

	// NarrativeHTML is the narrative converted to safe HTML for display, with
	// the table structure preserved because CDA narrative is mostly tables and
	// flattening them makes a result list unreadable.
	NarrativeHTML string `json:"narrativeHtml,omitempty"`

	// Entries are the coded, machine-readable observations.
	Entries []Entry `json:"entries,omitempty"`

	// Empty reports a section with neither narrative nor entries, which usually
	// means the sending system had nothing to say rather than that the patient
	// has nothing.
	Empty bool `json:"empty,omitempty"`

	// NilFlavor carries an explicit statement of absence, such as "no known
	// allergies". It is important to keep distinct from an empty section: one
	// says there is nothing, the other says nobody looked.
	NilFlavor string `json:"nilFlavor,omitempty"`

	// narrativeContent is the narrative with table headers removed. Comparing
	// terms against the full text would flag a column heading like "Substance" as
	// an unmatched term, and a check that reports a table header as a defect gets
	// switched off within a day.
	narrativeContent string

	node *xtree.Node
}

// contentText returns the narrative with table headers removed, falling back to
// the full text when the section was built without one.
func (s Section) contentText() string {
	if s.narrativeContent != "" {
		return s.narrativeContent
	}
	return s.NarrativeText
}

// Entry is one coded item inside a section.
type Entry struct {
	// Kind is the CDA class: "Observation", "SubstanceAdministration", "Act",
	// "Procedure", "Encounter", "Organizer".
	Kind string `json:"kind,omitempty"`

	Code       string `json:"code,omitempty"`
	CodeSystem string `json:"codeSystem,omitempty"`
	CodeName   string `json:"codeName,omitempty"`

	// Value is the observation result as text.
	Value string `json:"value,omitempty"`
	// ValueCode and ValueName are set when the result is itself coded.
	ValueCode string `json:"valueCode,omitempty"`
	ValueName string `json:"valueName,omitempty"`
	Unit      string `json:"unit,omitempty"`

	// StatusCode is the entry's status, which decides whether it is current.
	StatusCode string `json:"statusCode,omitempty"`

	EffectiveTime string `json:"effectiveTime,omitempty"`
	Low           string `json:"low,omitempty"`
	High          string `json:"high,omitempty"`

	// NegationInd inverts the meaning of the entry. It is carried explicitly
	// because ignoring it turns "no penicillin allergy" into "penicillin
	// allergy", which is the most dangerous single mistake available here.
	NegationInd bool `json:"negationInd,omitempty"`

	// NilFlavor states why a value is absent.
	NilFlavor string `json:"nilFlavor,omitempty"`

	// Text is the narrative this entry points at, resolved through its reference.
	Text string `json:"text,omitempty"`

	// Children are nested entries, which is how a medication carries its dose or
	// an organiser groups results.
	Children []Entry `json:"children,omitempty"`
}

// Describe renders an entry the way a person would say it.
func (e Entry) Describe() string {
	name := e.CodeName
	if name == "" {
		name = e.Code
	}
	if name == "" {
		name = e.Kind
	}

	var b strings.Builder
	if e.NegationInd {
		b.WriteString("no ")
	}
	b.WriteString(name)

	switch {
	case e.ValueName != "":
		b.WriteString(": " + e.ValueName)
	case e.Value != "":
		b.WriteString(": " + e.Value)
		if e.Unit != "" {
			b.WriteString(" " + e.Unit)
		}
	case e.NilFlavor != "":
		b.WriteString(" (" + nilFlavorMeaning(e.NilFlavor) + ")")
	}
	return b.String()
}

func nilFlavorMeaning(flavor string) string {
	switch strings.ToUpper(strings.TrimSpace(flavor)) {
	case "NI":
		return "no information"
	case "NA":
		return "not applicable"
	case "UNK":
		return "unknown"
	case "ASKU":
		return "asked but unknown"
	case "NASK":
		return "not asked"
	case "NAV":
		return "temporarily unavailable"
	case "OTH":
		return "other"
	case "MSK":
		return "masked"
	case "":
		return ""
	default:
		return "nil flavor " + flavor
	}
}

// Parse reads a clinical document.
func Parse(data []byte) (*Document, error) {
	root, err := xtree.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("cda: %w", err)
	}

	clinical := documentRoot(root)
	if clinical == nil {
		return nil, fmt.Errorf("cda: no ClinicalDocument element; this is XML but not a clinical document")
	}

	doc := &Document{Tree: clinical}

	doc.Title = strings.TrimSpace(clinical.First("title").Value())
	doc.ID = identifierOf(clinical.First("id"))
	doc.SetID = identifierOf(clinical.First("setId"))
	if v := clinical.First("versionNumber"); v != nil {
		doc.Version = v.AttrValue("value")
	}
	if e := clinical.First("effectiveTime"); e != nil {
		doc.EffectiveTime = e.AttrValue("value")
	}
	if c := clinical.First("confidentialityCode"); c != nil {
		doc.Confidentiality = confidentialityMeaning(c.AttrValue("code"))
	}
	if l := clinical.First("languageCode"); l != nil {
		doc.LanguageCode = l.AttrValue("code")
	}

	if code := clinical.First("code"); code != nil {
		doc.TypeCode = code.AttrValue("code")
		doc.TypeSystem = oidName(code.AttrValue("codeSystem"))
		doc.TypeName = code.AttrValue("displayName")
	}

	for _, t := range clinical.All("templateId") {
		if root := t.AttrValue("root"); root != "" {
			doc.TemplateIDs = append(doc.TemplateIDs, root)
		}
	}
	doc.DocumentType = recogniseDocument(doc)

	doc.Patient = parsePatient(clinical, &doc.Notes)
	doc.Authors = parseAuthors(clinical)
	if c := clinical.Find("custodian"); c != nil {
		if org := c.Find("name"); org != nil {
			doc.Custodian = strings.TrimSpace(org.Value())
		}
	}
	doc.Encounter = parseEncounter(clinical)

	doc.Sections = parseSections(clinical, &doc.Notes)

	if len(doc.Sections) == 0 {
		doc.Notes = append(doc.Notes, Note{
			Severity: "warning",
			Path:     "ClinicalDocument/component/structuredBody",
			Message:  "the document has no sections; it may be an unstructured document whose content is an attachment rather than markup",
			Rule:     "structured-body-present",
		})
	}

	return doc, nil
}

func identifierOf(node *xtree.Node) string {
	if node == nil {
		return ""
	}
	root := node.AttrValue("root")
	ext := node.AttrValue("extension")
	switch {
	case root != "" && ext != "":
		return root + "^" + ext
	case ext != "":
		return ext
	default:
		return root
	}
}

func parsePatient(clinical *xtree.Node, notes *[]Note) Patient {
	var p Patient

	// The patient must be under recordTarget. Searching the whole document for a
	// patientRole would find one in a family history or a related person and
	// silently file the document against the wrong human being.
	var role *xtree.Node
	if target := clinical.First("recordTarget"); target != nil {
		role = target.First("patientRole")
	}
	if role == nil {
		if loose := clinical.Find("patientRole"); loose != nil {
			role = loose
			*notes = append(*notes, Note{
				Severity: "warning",
				Path:     "ClinicalDocument/recordTarget",
				Message: "the patient is not under recordTarget, which is where the specification puts it. " +
					"One was found elsewhere in the document and used, but confirm it is the subject and not a relative",
				Rule: "record-target-misplaced",
			})
		}
	}
	if role == nil {
		*notes = append(*notes, Note{
			Severity: "error",
			Path:     "ClinicalDocument/recordTarget/patientRole",
			Message:  "the document does not say who it is about; it cannot be filed against a patient",
			Rule:     "record-target-present",
		})
		return p
	}

	for _, id := range role.All("id") {
		ident := Identifier{
			Root:      id.AttrValue("root"),
			Extension: id.AttrValue("extension"),
		}
		ident.Assigner = oidName(ident.Root)
		if ident.Root == "" && ident.Extension != "" {
			*notes = append(*notes, Note{
				Severity: "warning",
				Path:     "patientRole/id",
				Message:  fmt.Sprintf("the identifier %q has no assigning authority, so it cannot be matched reliably between facilities", ident.Extension),
				Rule:     "identifier-root-present",
			})
		}
		p.Identifiers = append(p.Identifiers, ident)
	}

	patient := role.First("patient")
	if patient == nil {
		return p
	}

	if name := patient.First("name"); name != nil {
		p.Family = strings.TrimSpace(name.First("family").Value())
		for _, g := range name.All("given") {
			if v := strings.TrimSpace(g.Value()); v != "" {
				p.Given = append(p.Given, v)
			}
		}
		p.Prefix = strings.TrimSpace(name.First("prefix").Value())
		p.Suffix = strings.TrimSpace(name.First("suffix").Value())
	}

	if g := patient.First("administrativeGenderCode"); g != nil {
		p.Gender = g.AttrValue("code")
		p.GenderName = genderMeaning(p.Gender, g.AttrValue("displayName"))
	}
	if b := patient.First("birthTime"); b != nil {
		p.BirthTime = b.AttrValue("value")
		if p.BirthTime == "" {
			if flavor := b.AttrValue("nullFlavor"); flavor != "" {
				*notes = append(*notes, Note{
					Severity: "warning",
					Path:     "patient/birthTime",
					Message:  "the date of birth is absent (" + nilFlavorMeaning(flavor) + "), which most receiving systems require for matching",
					Rule:     "birth-time-present",
				})
			}
		}
	}

	if addr := role.First("addr"); addr != nil {
		a := &Address{Use: addr.AttrValue("use")}
		for _, line := range addr.All("streetAddressLine") {
			if v := strings.TrimSpace(line.Value()); v != "" {
				a.Lines = append(a.Lines, v)
			}
		}
		a.City = strings.TrimSpace(addr.First("city").Value())
		a.State = strings.TrimSpace(addr.First("state").Value())
		a.Postal = strings.TrimSpace(addr.First("postalCode").Value())
		a.Country = strings.TrimSpace(addr.First("country").Value())
		p.Address = a
	}
	if tel := role.First("telecom"); tel != nil {
		p.Phone = strings.TrimPrefix(tel.AttrValue("value"), "tel:")
	}

	return p
}

func parseAuthors(clinical *xtree.Node) []Author {
	var out []Author
	for _, a := range clinical.All("author") {
		author := Author{}
		if t := a.First("time"); t != nil {
			author.Time = t.AttrValue("value")
		}
		if assigned := a.First("assignedAuthor"); assigned != nil {
			author.ID = identifierOf(assigned.First("id"))
			if person := assigned.First("assignedPerson"); person != nil {
				if name := person.First("name"); name != nil {
					author.Family = strings.TrimSpace(name.First("family").Value())
					author.Given = strings.TrimSpace(name.First("given").Value())
				}
			}
			if org := assigned.First("representedOrganization"); org != nil {
				author.Organisation = strings.TrimSpace(org.First("name").Value())
			}
		}
		out = append(out, author)
	}
	return out
}

func parseEncounter(clinical *xtree.Node) *Encounter {
	visit := clinical.Find("encompassingEncounter")
	if visit == nil {
		return nil
	}
	e := &Encounter{ID: identifierOf(visit.First("id"))}
	if c := visit.First("code"); c != nil {
		e.Code = c.AttrValue("displayName")
		if e.Code == "" {
			e.Code = c.AttrValue("code")
		}
	}
	if t := visit.First("effectiveTime"); t != nil {
		if low := t.First("low"); low != nil {
			e.Start = low.AttrValue("value")
		}
		if high := t.First("high"); high != nil {
			e.End = high.AttrValue("value")
		}
		if e.Start == "" {
			e.Start = t.AttrValue("value")
		}
	}
	if loc := visit.Find("healthCareFacility"); loc != nil {
		if name := loc.Find("name"); name != nil {
			e.Location = strings.TrimSpace(name.Value())
		}
	}
	return e
}

func parseSections(clinical *xtree.Node, notes *[]Note) []Section {
	body := clinical.Find("structuredBody")
	if body == nil {
		return nil
	}

	var out []Section
	for _, component := range body.All("component") {
		node := component.First("section")
		if node == nil {
			continue
		}
		out = append(out, parseSection(node, notes))
	}
	return out
}

func parseSection(node *xtree.Node, notes *[]Note) Section {
	s := Section{node: node}

	s.Title = strings.TrimSpace(node.First("title").Value())
	s.NilFlavor = node.AttrValue("nullFlavor")

	if code := node.First("code"); code != nil {
		s.Code = code.AttrValue("code")
		s.CodeSystem = oidName(code.AttrValue("codeSystem"))
		s.CodeName = code.AttrValue("displayName")
	}
	for _, t := range node.All("templateId") {
		if root := t.AttrValue("root"); root != "" {
			s.TemplateIDs = append(s.TemplateIDs, root)
		}
	}
	s.Kind = recogniseSection(s)

	if text := node.First("text"); text != nil {
		s.NarrativeText = narrativeText(text)
		s.NarrativeHTML = narrativeHTML(text)
		s.narrativeContent = narrativeContent(text)
	}

	for _, entry := range node.All("entry") {
		for _, child := range entry.Children {
			if isClinicalStatement(child.Name) {
				s.Entries = append(s.Entries, parseEntry(child, node))
			}
		}
	}

	s.Empty = strings.TrimSpace(s.NarrativeText) == "" && len(s.Entries) == 0

	if s.Empty && s.NilFlavor == "" {
		*notes = append(*notes, Note{
			Severity: "info",
			Path:     "section/" + s.Code,
			Message: fmt.Sprintf("the section %q is empty and does not say why; an explicit nullFlavor would distinguish "+
				"'nothing to report' from 'not assessed'", sectionLabel(s)),
			Rule: "empty-section-unexplained",
		})
	}

	return s
}

func sectionLabel(s Section) string {
	switch {
	case s.Title != "":
		return s.Title
	case s.Kind != "":
		return s.Kind
	case s.CodeName != "":
		return s.CodeName
	default:
		return s.Code
	}
}

func isClinicalStatement(name string) bool {
	switch name {
	case "observation", "substanceAdministration", "act", "procedure",
		"encounter", "organizer", "supply", "observationMedia", "regionOfInterest":
		return true
	}
	return false
}

func parseEntry(node *xtree.Node, section *xtree.Node) Entry {
	e := Entry{Kind: strings.ToUpper(node.Name[:1]) + node.Name[1:]}

	e.NegationInd = strings.EqualFold(node.AttrValue("negationInd"), "true")

	if code := node.First("code"); code != nil {
		e.Code = code.AttrValue("code")
		e.CodeSystem = oidName(code.AttrValue("codeSystem"))
		e.CodeName = code.AttrValue("displayName")
		if e.CodeName == "" {
			if orig := code.First("originalText"); orig != nil {
				e.CodeName = strings.TrimSpace(resolveReference(orig, section))
			}
		}
	}

	// A medication carries its drug code four levels down, in
	// consumable/manufacturedProduct/manufacturedMaterial/code, rather than on the
	// administration itself. Missing it produces a medication list with no drugs
	// in it, which is worse than an empty one because it looks populated.
	if e.Code == "" && node.Name == "substanceAdministration" {
		if material := node.Find("manufacturedMaterial"); material != nil {
			if code := material.First("code"); code != nil {
				e.Code = code.AttrValue("code")
				e.CodeSystem = oidName(code.AttrValue("codeSystem"))
				e.CodeName = code.AttrValue("displayName")
				if e.CodeName == "" {
					if name := material.First("name"); name != nil {
						e.CodeName = strings.TrimSpace(name.Value())
					}
				}
			}
		}
	}

	if status := node.First("statusCode"); status != nil {
		e.StatusCode = status.AttrValue("code")
	}

	if t := node.First("effectiveTime"); t != nil {
		e.EffectiveTime = t.AttrValue("value")
		if low := t.First("low"); low != nil {
			e.Low = low.AttrValue("value")
		}
		if high := t.First("high"); high != nil {
			e.High = high.AttrValue("value")
		}
		if e.EffectiveTime == "" && e.Low != "" {
			e.EffectiveTime = e.Low
		}
	}

	if value := node.First("value"); value != nil {
		e.NilFlavor = value.AttrValue("nullFlavor")
		e.ValueCode = value.AttrValue("code")
		e.ValueName = value.AttrValue("displayName")
		if v := value.AttrValue("value"); v != "" {
			e.Value = v
			e.Unit = value.AttrValue("unit")
		}
		if e.Value == "" && e.ValueCode == "" {
			if text := strings.TrimSpace(value.Value()); text != "" {
				e.Value = text
			}
		}
	}

	// The narrative this entry points at, which is what makes the agreement check
	// possible at all.
	if text := node.First("text"); text != nil {
		e.Text = strings.TrimSpace(resolveReference(text, section))
	}

	for _, rel := range node.All("entryRelationship") {
		for _, child := range rel.Children {
			if isClinicalStatement(child.Name) {
				e.Children = append(e.Children, parseEntry(child, section))
			}
		}
	}
	// An organiser holds its members directly rather than through a relationship.
	for _, comp := range node.All("component") {
		for _, child := range comp.Children {
			if isClinicalStatement(child.Name) {
				e.Children = append(e.Children, parseEntry(child, section))
			}
		}
	}

	return e
}

// resolveReference follows a <reference value="#id"/> into the narrative, which
// is how a coded entry names the text it corresponds to.
func resolveReference(node *xtree.Node, section *xtree.Node) string {
	if direct := strings.TrimSpace(node.Value()); direct != "" && node.First("reference") == nil {
		return direct
	}

	ref := node.First("reference")
	if ref == nil {
		return strings.TrimSpace(node.Value())
	}
	target := strings.TrimPrefix(ref.AttrValue("value"), "#")
	if target == "" || section == nil {
		return ""
	}

	found := findByID(section, target)
	if found == nil {
		return ""
	}
	return collapseSpace(found.Value())
}

func findByID(node *xtree.Node, id string) *xtree.Node {
	if node == nil {
		return nil
	}
	if v, ok := node.Attr("ID"); ok && v == id {
		return node
	}
	if v, ok := node.Attr("id"); ok && v == id {
		return node
	}
	for _, c := range node.Children {
		if found := findByID(c, id); found != nil {
			return found
		}
	}
	return nil
}

func collapseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

// AllEntries flattens the entry tree of every section, which is what the FHIR
// conversion and the agreement check both want.
func (d *Document) AllEntries() []Entry {
	var out []Entry
	var walk func([]Entry)
	walk = func(entries []Entry) {
		for _, e := range entries {
			out = append(out, e)
			walk(e.Children)
		}
	}
	for _, s := range d.Sections {
		walk(s.Entries)
	}
	return out
}

// SectionByKind finds a recognised section.
func (d *Document) SectionByKind(kind string) *Section {
	for i := range d.Sections {
		if d.Sections[i].Kind == kind {
			return &d.Sections[i]
		}
	}
	return nil
}

// Summary is a short account of a document for a list view.
type Summary struct {
	Title        string `json:"title"`
	DocumentType string `json:"documentType"`
	Patient      string `json:"patient"`
	Effective    string `json:"effective"`
	Sections     int    `json:"sections"`
	Entries      int    `json:"entries"`
	Warnings     int    `json:"warnings"`
	Errors       int    `json:"errors"`
}

// Summarise reduces a document to a line.
func (d *Document) Summarise() Summary {
	s := Summary{
		Title:        d.Title,
		DocumentType: d.DocumentType,
		Patient:      d.Patient.Name(),
		Effective:    d.EffectiveTime,
		Sections:     len(d.Sections),
		Entries:      len(d.AllEntries()),
	}
	for _, n := range d.Notes {
		switch n.Severity {
		case "error":
			s.Errors++
		case "warning":
			s.Warnings++
		}
	}
	return s
}

// SortedNotes returns the notes worst first, so a reader sees what matters.
func (d *Document) SortedNotes() []Note {
	out := append([]Note(nil), d.Notes...)
	rank := map[string]int{"error": 0, "warning": 1, "info": 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Severity] < rank[out[j].Severity] })
	return out
}
