package cda

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/pdf"
)

// Rendering a clinical document for a person to read, print, fax or file.
//
// # The narrative is rendered, not the entries
//
// This is the decision the whole file turns on, and it is not a shortcut.
//
// A C-CDA carries every fact twice: narrative a clinician reads, and coded entries a machine imports. The narrative
// is the attested content - the part a human being wrote or approved, and the part the document's legal weight
// attaches to. The entries are a machine-readable restatement of it.
//
// So rendering the entries would produce a document that looks authoritative and was never attested by anybody. It
// would also hide the defect that matters: a section with entries and no narrative is a section no clinician has
// signed off, and it must print as visibly empty rather than being quietly filled in from the codes. Somebody
// holding a printout has no way to know which parts a program invented.
//
// The repair path exists for exactly that case, and it is the honest place for it: it says what would be invisible,
// rebuilds it, and hands back a document somebody can inspect and choose to accept. Rendering does not make that
// decision silently on a printout that will be filed and read years later.
//
// Where a section has no narrative, this says so in as many words, and says how many coded facts are behind it. A
// blank space would read as "nothing to report", which is the misreading that gets somebody prescribed a drug that
// interacts with what they are already taking.

// RenderOptions controls the rendered document.
type RenderOptions struct {
	// Footer is appended to the provenance note, for a site that has to record where a document came from.
	Footer string

	// IncludeCodes prints the code and code system beside each entry in the tables.
	//
	// Off by default. A printout for a clinician wants drug names; a printout for somebody debugging an exchange
	// wants the codes, and putting them in front of a clinician who does not need them is how a page of
	// medications becomes unreadable.
	IncludeCodes bool
}

// RenderPDF writes a clinical document as a PDF.
func RenderPDF(d *Document, opts RenderOptions) ([]byte, error) {
	if d == nil {
		return nil, fmt.Errorf("no document to render")
	}

	title := strings.TrimSpace(d.Title)
	if title == "" {
		title = strings.TrimSpace(d.TypeName)
	}
	if title == "" {
		title = "Clinical Document"
	}

	doc := pdf.New(title)
	doc.SetMeta(d.Custodian, d.Patient.Name(), d.DocumentType)

	doc.Heading(title, 1)

	// The patient block first and complete.
	//
	// A printout whose patient cannot be identified beyond doubt is a clinical hazard, and it is the commonest
	// complaint about rendered documents: the name is there and the date of birth is not, so a ward with two
	// patients of the same surname cannot tell which one it belongs to.
	doc.KeyValue("Patient", d.Patient.Name(), 10)
	if born := formatCDADate(d.Patient.BirthTime); born != "" {
		doc.KeyValue("Date of birth", born, 10)
	}
	if g := strings.TrimSpace(d.Patient.GenderName); g != "" {
		doc.KeyValue("Sex", g, 10)
	} else if g := strings.TrimSpace(d.Patient.Gender); g != "" {
		doc.KeyValue("Sex", g, 10)
	}

	// Identifiers with their assigning authority, because a bare MRN is ambiguous between facilities and that
	// ambiguity is the single most common cause of an unmatchable patient downstream.
	for _, id := range d.Patient.Identifiers {
		if id.Extension == "" {
			continue
		}
		who := id.Assigner
		if who == "" {
			who = id.Root
		}
		if who == "" {
			doc.KeyValue("Identifier", id.Extension, 10)
			continue
		}
		doc.KeyValue("Identifier", fmt.Sprintf("%s (assigned by %s)", id.Extension, who), 10)
	}

	doc.Rule()

	if when := formatCDADate(d.EffectiveTime); when != "" {
		doc.KeyValue("Document date", when, 9)
	}
	if d.Custodian != "" {
		doc.KeyValue("Custodian", d.Custodian, 9)
	}
	for _, a := range d.Authors {
		name := strings.TrimSpace(strings.TrimSpace(a.Given) + " " + strings.TrimSpace(a.Family))
		if name == "" {
			name = strings.TrimSpace(a.Organisation)
		}
		if name != "" {
			doc.KeyValue("Author", name, 9)
		}
	}

	// Not printed here: which document this one replaces.
	//
	// It belongs on the printout - two versions of the same medication list filed side by side, with nothing on
	// either saying which supersedes the other, is how a superseded list stays in use. But the parser puts the
	// relatedDocument on DocumentInfo, taken from the v2 transport, not on the parsed document itself. Printing
	// "replaces nothing" from an absent field would be worse than saying nothing: it reads as a positive
	// assertion that this document supersedes none, which is not something the file states.

	doc.Rule()

	for _, section := range d.Sections {
		name := strings.TrimSpace(section.Title)
		if name == "" {
			name = strings.TrimSpace(section.CodeName)
		}
		if name == "" {
			name = "Untitled section"
		}

		doc.Heading(name, 2)

		narrative := strings.TrimSpace(narrativeToText(section))
		switch {
		case narrative != "":
			doc.Paragraph(narrative, pdf.Regular, 10, 0)

		case len(section.Entries) > 0:
			// Stated, not filled in. This is the case the repair path exists for, and a printout is the wrong
			// place to make that decision silently.
			doc.Paragraph(fmt.Sprintf(
				"This section has no narrative text. It carries %d coded %s that no reader would normally see, "+
					"because a document viewer displays the narrative and ignores the coded data. Nothing here "+
					"has been written or approved by a clinician, so it has not been printed. Use the repair "+
					"function to rebuild it and check the result before relying on it.",
				len(section.Entries), pluralWord(len(section.Entries), "fact", "facts")),
				pdf.Italic, 9, 0)

		default:
			doc.Paragraph("Nothing recorded in this section.", pdf.Italic, 9, 0)
		}

		// The coded entries as a table beside the narrative, when asked for. Not instead of it: this is a
		// restatement for somebody checking an exchange, and it is labelled as such.
		if opts.IncludeCodes && len(section.Entries) > 0 {
			doc.Paragraph("Coded entries behind this section:", pdf.Italic, 8, 0)

			header := []string{"Description", "Status", "Date", "Code"}
			rows := make([][]string, 0, len(section.Entries))
			for _, e := range section.Entries {
				code := e.Code
				if code != "" && e.CodeSystem != "" {
					code = e.CodeSystem + " " + code
				}
				rows = append(rows, []string{
					entryLabel(e), e.StatusCode, formatCDADate(e.EffectiveTime), code,
				})
			}
			doc.Table(header, rows, 8)
		}
	}

	doc.Rule()

	// Provenance, because a printed clinical document with no statement of where it came from cannot be audited,
	// and somebody finding it in a file in three years needs to know what produced it.
	note := "Rendered by Perfuse from the narrative of the source clinical document. " +
		"Coded entries are not rendered as narrative: the narrative is the attested content."
	if strings.TrimSpace(opts.Footer) != "" {
		note += " " + strings.TrimSpace(opts.Footer)
	}
	doc.Note(note)

	return doc.Bytes(), nil
}

// narrativeToText prefers the plain text and falls back to stripping the HTML.
func narrativeToText(s Section) string {
	if t := strings.TrimSpace(s.NarrativeText); t != "" {
		return t
	}
	return stripTags(s.NarrativeHTML)
}

// stripTags removes markup, turning row and cell boundaries into separators rather than deleting them.
//
// Deleting them silently joins a table into one line: "metformin500 MGactive" reads as a dose of 500 MG active,
// which is nonsense but plausible-looking nonsense, and that is worse than obviously broken output.
func stripTags(html string) string {
	var b strings.Builder
	depth := 0
	var tag strings.Builder

	for _, r := range html {
		switch {
		case r == '<':
			depth++
			tag.Reset()
		case r == '>' && depth > 0:
			depth--
			name := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(tag.String()), "/"))
			if i := strings.IndexAny(name, " \t"); i > 0 {
				name = name[:i]
			}
			switch name {
			case "tr", "p", "br", "div", "li", "table", "thead", "tbody", "caption":
				b.WriteString("\n")
			case "td", "th":
				b.WriteString("  ")
			}
		case depth > 0:
			tag.WriteRune(r)
		default:
			b.WriteRune(r)
		}
	}

	// Runs of blank lines collapsed, so a narrative built from a table does not print as a column of gaps.
	lines := strings.Split(b.String(), "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, l := range lines {
		l = strings.TrimRight(strings.ReplaceAll(l, "\u00a0", " "), " \t")
		if strings.TrimSpace(l) == "" {
			if blank {
				continue
			}
			blank = true
		} else {
			blank = false
		}
		out = append(out, l)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

func entryLabel(e Entry) string {
	if n := strings.TrimSpace(e.CodeName); n != "" {
		return n
	}
	if v := strings.TrimSpace(e.Value); v != "" {
		return v
	}
	return strings.TrimSpace(e.Describe())
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// formatCDADate turns an HL7 timestamp into something a person reads.
//
// An unrecognised value is returned unchanged rather than blanked. A date nobody can parse is still evidence, and
// dropping it from a printout loses the only clue as to what the sending system meant.
func formatCDADate(v string) string {
	v = strings.TrimSpace(v)
	if len(v) < 8 {
		return v
	}

	year, month, day := v[0:4], v[4:6], v[6:8]

	names := [...]string{
		"January", "February", "March", "April", "May", "June",
		"July", "August", "September", "October", "November", "December",
	}
	var m int
	if _, err := fmt.Sscanf(month, "%d", &m); err != nil || m < 1 || m > 12 {
		return v
	}

	var dd int
	if _, err := fmt.Sscanf(day, "%d", &dd); err != nil || dd < 1 || dd > 31 {
		return v
	}

	out := fmt.Sprintf("%d %s %s", dd, names[m-1], year)

	// The time included when present, because for a result or an administration the time of day is the point.
	if len(v) >= 12 {
		out += fmt.Sprintf(" at %s:%s", v[8:10], v[10:12])
	}
	return out
}
