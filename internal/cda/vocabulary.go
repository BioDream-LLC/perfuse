package cda

import (
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// This file turns identifiers into English.
//
// A C-CDA is full of numbers that mean something: 2.16.840.1.113883.10.20.22.2.6.1
// is the allergies section, 2.16.840.1.113883.6.1 is LOINC. Nobody reading a
// document can be expected to know these, and every tool that shows them raw makes
// the reader look them up. So they are named here, and where a name is unknown the
// document says "not recognised" rather than showing a bare OID and leaving the
// reader to guess whether that matters.

// sectionTemplates maps a section template identifier to plain words.
//
// Both the R1 and R2 identifiers are present where they differ, because documents
// in the wild claim either and a viewer that only knows one will fail to recognise
// half of what it is shown.
var sectionTemplates = map[string]string{
	"2.16.840.1.113883.10.20.22.2.6":    "Allergies",
	"2.16.840.1.113883.10.20.22.2.6.1":  "Allergies",
	"2.16.840.1.113883.10.20.22.2.1":    "Medications",
	"2.16.840.1.113883.10.20.22.2.1.1":  "Medications",
	"2.16.840.1.113883.10.20.22.2.5":    "Problems",
	"2.16.840.1.113883.10.20.22.2.5.1":  "Problems",
	"2.16.840.1.113883.10.20.22.2.3":    "Results",
	"2.16.840.1.113883.10.20.22.2.3.1":  "Results",
	"2.16.840.1.113883.10.20.22.2.4":    "Vital signs",
	"2.16.840.1.113883.10.20.22.2.4.1":  "Vital signs",
	"2.16.840.1.113883.10.20.22.2.7":    "Procedures",
	"2.16.840.1.113883.10.20.22.2.7.1":  "Procedures",
	"2.16.840.1.113883.10.20.22.2.2":    "Immunisations",
	"2.16.840.1.113883.10.20.22.2.2.1":  "Immunisations",
	"2.16.840.1.113883.10.20.22.2.17":   "Social history",
	"2.16.840.1.113883.10.20.22.2.22":   "Encounters",
	"2.16.840.1.113883.10.20.22.2.22.1": "Encounters",
	"2.16.840.1.113883.10.20.22.2.10":   "Plan of treatment",
	"2.16.840.1.113883.10.20.22.2.14":   "Functional status",
	"2.16.840.1.113883.10.20.22.2.21":   "Advance directives",
	"2.16.840.1.113883.10.20.22.2.21.1": "Advance directives",
	"2.16.840.1.113883.10.20.22.2.18":   "Payers",
	"2.16.840.1.113883.10.20.22.2.15":   "Family history",
	"2.16.840.1.113883.10.20.22.2.23":   "Medical equipment",
	"2.16.840.1.113883.10.20.22.2.20":   "Past medical history",
	"2.16.840.1.113883.10.20.22.2.11":   "Discharge medications",
	"2.16.840.1.113883.10.20.22.2.11.1": "Discharge medications",
	"2.16.840.1.113883.10.20.22.2.8":    "Assessment",
	"2.16.840.1.113883.10.20.22.2.13":   "Chief complaint and reason for visit",
	"2.16.840.1.113883.10.20.22.2.12":   "Reason for referral",
	"2.16.840.1.113883.10.20.22.2.65":   "Notes",
	"1.3.6.1.4.1.19376.1.5.3.1.3.1":     "Reason for referral",
	"1.3.6.1.4.1.19376.1.5.3.1.1.9.15":  "Discharge summary narrative",
}

// sectionCodes maps a LOINC section code to plain words, used when the template
// identifier is absent, which happens in older documents.
var sectionCodes = map[string]string{
	"48765-2": "Allergies",
	"10160-0": "Medications",
	"11450-4": "Problems",
	"30954-2": "Results",
	"8716-3":  "Vital signs",
	"47519-4": "Procedures",
	"11369-6": "Immunisations",
	"29762-2": "Social history",
	"46240-8": "Encounters",
	"18776-5": "Plan of treatment",
	"47420-5": "Functional status",
	"42348-3": "Advance directives",
	"48768-6": "Payers",
	"10157-6": "Family history",
	"46264-8": "Medical equipment",
	"11348-0": "Past medical history",
	"10183-2": "Discharge medications",
	"51848-0": "Assessment",
	"10154-3": "Chief complaint",
	"29299-5": "Reason for visit",
	"42349-1": "Reason for referral",
	"8648-8":  "Hospital course",
	"11493-4": "Hospital discharge studies summary",
	"10164-2": "History of present illness",
}

// documentTemplates names the document types.
var documentTemplates = map[string]string{
	"2.16.840.1.113883.10.20.22.1.2":  "Continuity of Care Document",
	"2.16.840.1.113883.10.20.22.1.1":  "US Realm Header",
	"2.16.840.1.113883.10.20.22.1.8":  "Discharge Summary",
	"2.16.840.1.113883.10.20.22.1.9":  "Consultation Note",
	"2.16.840.1.113883.10.20.22.1.4":  "History and Physical",
	"2.16.840.1.113883.10.20.22.1.14": "Progress Note",
	"2.16.840.1.113883.10.20.22.1.6":  "Diagnostic Imaging Report",
	"2.16.840.1.113883.10.20.22.1.7":  "Operative Note",
	"2.16.840.1.113883.10.20.22.1.13": "Procedure Note",
	"2.16.840.1.113883.10.20.22.1.15": "Referral Note",
	"2.16.840.1.113883.10.20.22.1.10": "Transfer Summary",
	"2.16.840.1.113883.10.20.22.1.3":  "Unstructured Document",
	"2.16.840.1.113883.10.20.24.1.2":  "Quality Reporting Document",
	"2.16.840.1.113883.10.20.22.1.16": "Care Plan",
}

// documentCodes names document types by their LOINC code, for documents that do
// not carry a recognisable template.
var documentCodes = map[string]string{
	"34133-9": "Summary of episode note",
	"18842-5": "Discharge summary",
	"11488-4": "Consultation note",
	"34117-2": "History and physical note",
	"11506-3": "Progress note",
	"18748-4": "Diagnostic imaging report",
	"11504-8": "Operative note",
	"28570-0": "Procedure note",
	"57133-1": "Referral note",
	"18761-7": "Transfer summary note",
	"57827-8": "Unstructured document",
	"52521-2": "Overall plan of care",
}

// oids names the code systems and assigning authorities that appear constantly.
var oids = map[string]string{
	"2.16.840.1.113883.6.1":      "LOINC",
	"2.16.840.1.113883.6.96":     "SNOMED CT",
	"2.16.840.1.113883.6.88":     "RxNorm",
	"2.16.840.1.113883.6.69":     "NDC",
	"2.16.840.1.113883.6.103":    "ICD-9-CM diagnosis",
	"2.16.840.1.113883.6.104":    "ICD-9-CM procedure",
	"2.16.840.1.113883.6.90":     "ICD-10-CM",
	"2.16.840.1.113883.6.4":      "ICD-10-PCS",
	"2.16.840.1.113883.6.12":     "CPT-4",
	"2.16.840.1.113883.6.101":    "NUCC provider taxonomy",
	"2.16.840.1.113883.6.238":    "CDC race and ethnicity",
	"2.16.840.1.113883.5.1":      "HL7 administrative gender",
	"2.16.840.1.113883.5.4":      "HL7 act code",
	"2.16.840.1.113883.5.83":     "HL7 observation interpretation",
	"2.16.840.1.113883.5.25":     "HL7 confidentiality",
	"2.16.840.1.113883.5.1119":   "HL7 address use",
	"2.16.840.1.113883.6.8":      "UCUM",
	"2.16.840.1.113883.4.1":      "US Social Security Number",
	"2.16.840.1.113883.4.6":      "US National Provider Identifier",
	"2.16.840.1.113883.6.59":     "CVX vaccine codes",
	"2.16.840.1.113883.12.292":   "CVX vaccine codes",
	"2.16.840.1.113883.3.26.1.1": "NCI Thesaurus",
}

// oidName gives a plain name for an OID, or a clear statement that it is not one
// this package recognises.
//
// Returning the raw OID with a marker rather than an empty string is deliberate: a
// viewer that shows nothing for an unrecognised system leaves the reader unable to
// tell a missing code system from an unfamiliar one, and those need different
// responses.
func oidName(oid string) string {
	oid = strings.TrimSpace(oid)
	if oid == "" {
		return ""
	}
	if name, ok := oids[oid]; ok {
		return name
	}
	return oid + " (not recognised)"
}

// recogniseSection names a section from its template identifiers, falling back to
// its code.
func recogniseSection(s Section) string {
	for _, t := range s.TemplateIDs {
		if name, ok := sectionTemplates[t]; ok {
			return name
		}
	}
	if name, ok := sectionCodes[s.Code]; ok {
		return name
	}
	return ""
}

// recogniseDocument names the document type.
func recogniseDocument(d *Document) string {
	// The most specific template wins, and the generic US Realm Header is
	// considered last because nearly every document claims it.
	for _, t := range d.TemplateIDs {
		if t == "2.16.840.1.113883.10.20.22.1.1" {
			continue
		}
		if name, ok := documentTemplates[t]; ok {
			return name
		}
	}
	if name, ok := documentCodes[d.TypeCode]; ok {
		return name
	}
	for _, t := range d.TemplateIDs {
		if name, ok := documentTemplates[t]; ok {
			return name
		}
	}
	if d.TypeName != "" {
		return d.TypeName
	}
	return ""
}

func genderMeaning(code, display string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "M":
		return "male"
	case "F":
		return "female"
	case "UN":
		return "undifferentiated"
	case "":
		return display
	default:
		if display != "" {
			return display
		}
		return code
	}
}

func confidentialityMeaning(code string) string {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case "N":
		return "normal"
	case "R":
		return "restricted"
	case "V":
		return "very restricted"
	case "L":
		return "low"
	case "M":
		return "moderate"
	case "U":
		return "unrestricted"
	case "":
		return ""
	default:
		return code
	}
}

// narrativeText renders a narrative block as plain text.
//
// Table structure is preserved as separators rather than discarded, because CDA
// narrative is overwhelmingly tables - a medication list, a result panel - and
// running the cells together produces text where "Penicillin" and "Rash" become
// one word and the agreement check cannot compare anything.
func narrativeText(text *xtree.Node) string {
	var b strings.Builder
	writeNarrative(&b, text, 0)
	return collapseBlankLines(b.String())
}

func writeNarrative(b *strings.Builder, node *xtree.Node, depth int) {
	if node == nil || depth > 64 {
		return
	}

	if len(node.Fragments) > 0 {
		for _, f := range node.Fragments {
			if f.Node != nil {
				writeNarrativeElement(b, f.Node, depth)
			} else if trimmed := strings.TrimSpace(f.Text); trimmed != "" {
				b.WriteString(trimmed)
				b.WriteByte(' ')
			}
		}
		return
	}

	if len(node.Children) == 0 {
		if trimmed := strings.TrimSpace(node.Text); trimmed != "" {
			b.WriteString(trimmed)
		}
		return
	}
	for _, c := range node.Children {
		writeNarrativeElement(b, c, depth)
	}
}

func writeNarrativeElement(b *strings.Builder, node *xtree.Node, depth int) {
	switch node.Name {
	case "td", "th":
		writeNarrative(b, node, depth+1)
		b.WriteString(" | ")
	case "tr":
		writeNarrative(b, node, depth+1)
		b.WriteByte('\n')
	case "item", "li":
		b.WriteString("- ")
		writeNarrative(b, node, depth+1)
		b.WriteByte('\n')
	case "paragraph", "p", "caption", "title":
		writeNarrative(b, node, depth+1)
		b.WriteByte('\n')
	case "br":
		b.WriteByte('\n')
	case "table", "list", "tbody", "thead", "content", "text":
		writeNarrative(b, node, depth+1)
	default:
		writeNarrative(b, node, depth+1)
		b.WriteByte(' ')
	}
}

func collapseBlankLines(s string) string {
	lines := strings.Split(s, "\n")
	var out []string
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), "|"))
		line = collapseSpace(line)
		if line == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// narrativeContent renders the narrative with table headers omitted, for
// comparison against coded entries. Column headings describe the table rather
// than the patient, so treating them as content produces findings that are always
// wrong.
func narrativeContent(text *xtree.Node) string {
	var b strings.Builder
	writeContentOnly(&b, text, 0)
	return collapseBlankLines(b.String())
}

func writeContentOnly(b *strings.Builder, node *xtree.Node, depth int) {
	if node == nil || depth > 64 {
		return
	}
	if node.Name == "thead" || node.Name == "th" {
		return
	}
	if len(node.Children) == 0 && len(node.Fragments) == 0 {
		if trimmed := strings.TrimSpace(node.Text); trimmed != "" {
			b.WriteString(trimmed)
		}
		return
	}
	if len(node.Fragments) > 0 {
		for _, f := range node.Fragments {
			if f.Node != nil {
				writeContentElement(b, f.Node, depth)
			} else if trimmed := strings.TrimSpace(f.Text); trimmed != "" {
				b.WriteString(trimmed)
				b.WriteByte(' ')
			}
		}
		return
	}
	for _, c := range node.Children {
		writeContentElement(b, c, depth)
	}
}

func writeContentElement(b *strings.Builder, node *xtree.Node, depth int) {
	switch node.Name {
	case "thead", "th":
		return
	case "td":
		writeContentOnly(b, node, depth+1)
		b.WriteString(" | ")
	case "tr":
		writeContentOnly(b, node, depth+1)
		b.WriteByte('\n')
	case "item", "li", "paragraph", "p", "caption":
		writeContentOnly(b, node, depth+1)
		b.WriteByte('\n')
	case "br":
		b.WriteByte('\n')
	default:
		writeContentOnly(b, node, depth+1)
		b.WriteByte(' ')
	}
}

// narrativeHTML converts a narrative block into a safe subset of HTML.
//
// Only a fixed set of elements survives, and no attributes at all except the
// identifier a coded entry references. A CDA arrives from outside the
// organisation, so treating its markup as trustworthy would be a cross-site
// scripting hole in a clinical viewer - which is a worse place to have one than
// most.
func narrativeHTML(text *xtree.Node) string {
	var b strings.Builder
	writeHTML(&b, text, 0)
	return b.String()
}

var htmlAllowed = map[string]string{
	"paragraph": "p",
	"p":         "p",
	"table":     "table",
	"thead":     "thead",
	"tbody":     "tbody",
	"tr":        "tr",
	"th":        "th",
	"td":        "td",
	"list":      "ul",
	"item":      "li",
	"li":        "li",
	"caption":   "caption",
	"br":        "br",
	"content":   "span",
	"linkHtml":  "span",
	"footnote":  "small",
	"sub":       "sub",
	"sup":       "sup",
	"title":     "strong",
}

func writeHTML(b *strings.Builder, node *xtree.Node, depth int) {
	if node == nil || depth > 64 {
		return
	}

	emit := func(children func()) {
		tag, ok := htmlAllowed[node.Name]
		if !ok {
			children()
			return
		}
		if tag == "br" {
			b.WriteString("<br/>")
			return
		}
		b.WriteByte('<')
		b.WriteString(tag)
		// The identifier is kept because the entry-to-narrative reference depends
		// on it, and it is the only attribute that survives.
		if id, ok := node.Attr("ID"); ok && safeID(id) {
			b.WriteString(` id="cda-`)
			b.WriteString(id)
			b.WriteString(`"`)
		}
		b.WriteByte('>')
		children()
		b.WriteString("</")
		b.WriteString(tag)
		b.WriteByte('>')
	}

	emit(func() {
		if len(node.Fragments) > 0 {
			for _, f := range node.Fragments {
				if f.Node != nil {
					writeHTML(b, f.Node, depth+1)
				} else {
					escapeHTML(b, f.Text)
				}
			}
			return
		}
		if len(node.Children) == 0 {
			escapeHTML(b, node.Text)
			return
		}
		for _, c := range node.Children {
			writeHTML(b, c, depth+1)
		}
	})
}

func safeID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		ok := c == '_' || c == '-' || c == '.' ||
			(c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9')
		if !ok {
			return false
		}
	}
	return true
}

func escapeHTML(b *strings.Builder, s string) {
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		case '\'':
			b.WriteString("&#39;")
		default:
			b.WriteByte(s[i])
		}
	}
}

// RecognisedDocuments lists the document types this package names, so the
// interface can state what it supports rather than leaving somebody to discover
// the limits by trial.
func RecognisedDocuments() []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range documentTemplates {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// RecognisedSections lists the section types this package names.
func RecognisedSections() []string {
	seen := map[string]bool{}
	var out []string
	for _, name := range sectionTemplates {
		if !seen[name] {
			seen[name] = true
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}
