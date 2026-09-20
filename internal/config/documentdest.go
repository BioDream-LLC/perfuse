package config

import (
	"fmt"
	"strings"
	"time"
)

// The Document destination: rendering a message into something a person reads.
//
// Mirth's Document Writer renders HTML to PDF. This renders text, and says so. The requirement at almost every
// site that uses that connector is "put this report on a share so somebody can print it" - a discharge summary, a
// result report, a daily reconciliation - and those are text.
//
// Being honest about that is better than the alternatives. Embedding an HTML layout engine would cost more than
// the rest of the binary and bring a permanent CVE stream, which would undo the argument the bill of materials
// makes. Claiming to do typesetting and doing it badly would be worse still.

// DocumentFormat is what to write.
type DocumentFormat string

const (
	// DocumentPDF writes a paginated PDF with a monospaced font.
	DocumentPDF DocumentFormat = "pdf"

	// DocumentText writes the rendered template as a plain text file.
	//
	// Worth having: a site whose next step is a script or a printer queue does not want a PDF, and offering only
	// PDF would make them write a channel that produces one and a script that extracts the text back out.
	DocumentText DocumentFormat = "text"
)

// DocumentDestination renders each message into a document.
type DocumentDestination struct {
	// Dir is where documents are written.
	Dir string `yaml:"dir"`

	// Format is pdf or text. Defaults to pdf.
	Format DocumentFormat `yaml:"format,omitempty"`

	// Template is the document body, with ${PID-5.1} style references to message paths.
	//
	// Required. There is no sensible default: a document with no template would either be the raw message, which
	// nobody wants printed, or empty.
	Template string `yaml:"template"`

	// Title appears in the document metadata.
	Title string `yaml:"title,omitempty"`

	// FileName templates the name, using the same ${...} placeholders as the other file destinations.
	FileName string `yaml:"file_name,omitempty"`

	// FontSize in points. Defaults to 10, which fits 80 characters across A4.
	FontSize float64 `yaml:"font_size,omitempty"`

	// Landscape rotates the page, for a wide report.
	Landscape bool `yaml:"landscape,omitempty"`

	// TempSuffix is appended while writing and removed by a rename. Defaults to ".part".
	//
	// The same courtesy as every other file destination, and it matters more here: a print watcher picking up a
	// half-written PDF produces a page of nothing, and nobody investigates a blank page.
	TempSuffix string `yaml:"temp_suffix,omitempty"`

	// Timeout bounds one write. Defaults to 30s.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// validateDocumentDest checks a document destination.
func validateDocumentDest(d *Destination) []error {
	var errs []error

	if d.Document == nil {
		return []error{fmt.Errorf("destination %q is a document destination but has no document block", d.Name)}
	}
	doc := d.Document

	if strings.TrimSpace(doc.Dir) == "" {
		errs = append(errs, fmt.Errorf("destination %q needs document.dir", d.Name))
	}

	if strings.TrimSpace(doc.Template) == "" {
		// Refused rather than defaulted. A document with no template would be the raw message, which nobody
		// wants printed, or empty - and an empty page delivered to a ward is a support call.
		errs = append(errs, fmt.Errorf(
			"destination %q needs document.template; there is no sensible default, because a document with no "+
				"template is either the raw message or a blank page", d.Name))
	}

	if doc.Format == "" {
		doc.Format = DocumentPDF
	}
	switch doc.Format {
	case DocumentPDF, DocumentText:
	default:
		errs = append(errs, fmt.Errorf(
			"destination %q has document.format %q; it must be pdf or text", d.Name, doc.Format))
	}

	// Said plainly at load rather than discovered from output. Somebody migrating a Mirth Document Writer will
	// bring an HTML template, and finding out that the angle brackets were printed literally after the first
	// ward complains is much worse than being told now.
	if doc.Format == DocumentPDF && looksLikeHTML(doc.Template) {
		errs = append(errs, fmt.Errorf(
			"destination %q has a template that looks like HTML, and this destination renders text rather than "+
				"laying out HTML - the tags would be printed literally. Rewrite the template as plain text, or "+
				"use a file destination and a tool built for typesetting", d.Name))
	}

	if doc.FontSize < 0 {
		errs = append(errs, fmt.Errorf("destination %q has a negative font size", d.Name))
	}

	if doc.TempSuffix == "" {
		doc.TempSuffix = ".part"
	}
	if doc.Timeout == 0 {
		doc.Timeout = 30 * time.Second
	}

	return errs
}

// looksLikeHTML reports whether a template is probably markup.
//
// Deliberately crude and deliberately narrow: it looks for a handful of tags that would only appear in markup.
// A false positive would refuse a valid template, so it does not guess on a stray angle bracket - a report
// containing "<10 mmol" must still load.
func looksLikeHTML(template string) bool {
	lower := strings.ToLower(template)
	for _, tag := range []string{"<html", "<body", "<table", "<div", "<p>", "<br", "<span", "<h1", "<td"} {
		if strings.Contains(lower, tag) {
			return true
		}
	}
	return false
}
