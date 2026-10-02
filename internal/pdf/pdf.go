// Package pdf writes simple text documents, using only the standard library.
//
// Mirth's Document Writer renders HTML to PDF, which needs a layout engine. That is not what this is. This
// produces a plain, paginated text document with a monospaced font, and it exists because the actual requirement
// at almost every site that uses that connector is "put this report on a share so somebody can print it" - a
// discharge summary, a result report, a daily reconciliation. Those are text.
//
// Refusing the feature entirely would leave a Tier 2 migration blocker in place. Pulling in an HTML rendering
// engine would cost more than the whole rest of the binary and bring a CVE stream with it, which would undo the
// argument the bill of materials makes. So: text, honestly described as text, and a site that genuinely needs
// typeset output is better served by a file destination and a tool built for it.
//
// # Why write a PDF at all rather than a text file
//
// Because the receiving end is usually a person with a printer and a Windows share, and a .txt file printed from
// a share loses its pagination, its margins and often its line endings. A PDF prints the same everywhere, which
// is the entire reason anybody asked for one.
package pdf

import (
	"bytes"
	"fmt"
	"strings"
	"time"
)

// Options control the document.
type Options struct {
	// Title appears in the document metadata, not on the page.
	Title string

	// FontSize in points. Defaults to 10, which fits 80 characters across A4 in a monospaced font.
	FontSize float64

	// Landscape rotates the page. Useful for a wide report, which is most reconciliation output.
	Landscape bool

	// Created overrides the timestamp, for reproducible output. Zero means now.
	//
	// Exists because a PDF containing the current time cannot be compared byte for byte, and a test that cannot
	// compare its output can only check that something was produced.
	Created time.Time
}

// A4 in points, which is the unit PDF uses.
const (
	a4Width  = 595.28
	a4Height = 841.89

	marginLeft   = 56.7 // 20mm
	marginTop    = 56.7
	marginBottom = 56.7
)

func (o Options) fontSize() float64 {
	if o.FontSize <= 0 {
		return 10
	}
	return o.FontSize
}

func (o Options) pageSize() (width, height float64) {
	if o.Landscape {
		return a4Height, a4Width
	}
	return a4Width, a4Height
}

// Render writes text as a paginated PDF.
func Render(text string, opts Options) ([]byte, error) {
	width, height := opts.pageSize()
	size := opts.fontSize()

	// Leading of 1.2x, which is the conventional ratio and close enough to right that nobody will ask.
	leading := size * 1.2

	usableHeight := height - marginTop - marginBottom
	linesPerPage := int(usableHeight / leading)
	if linesPerPage < 1 {
		// A font large enough that no line fits is a configuration mistake, and producing a document with
		// zero lines per page would loop forever below.
		return nil, fmt.Errorf("a font size of %.1f leaves no room for any line on the page", size)
	}

	// Characters per line for Courier, whose advance width is exactly 0.6 em. Wrapping rather than clipping,
	// because a clipped line silently loses the end of a value and a report is often the only record.
	usableWidth := width - 2*marginLeft
	charsPerLine := int(usableWidth / (size * 0.6))
	if charsPerLine < 8 {
		return nil, fmt.Errorf("a font size of %.1f leaves room for fewer than eight characters per line", size)
	}

	lines := wrap(text, charsPerLine)
	pages := paginate(lines, linesPerPage)

	return build(pages, opts, width, height, size, leading)
}

// wrap breaks text into lines that fit, preserving existing line breaks.
//
// Existing breaks are preserved because the input is a report somebody laid out, and reflowing it into a single
// paragraph would destroy the columns that make it readable.
func wrap(text string, charsPerLine int) []string {
	// Both line ending conventions, and HL7's bare carriage return, because this text may have come straight
	// from a message.
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")

	var out []string
	for _, line := range strings.Split(text, "\n") {
		// Tabs expanded here rather than left to the viewer, since a PDF has no tab stops and a literal tab
		// would render as nothing at all.
		line = expandTabs(line, 8)

		if line == "" {
			out = append(out, "")
			continue
		}

		for len(line) > charsPerLine {
			// Break on a space when there is one in the last quarter of the line, so a wrapped line does not
			// split a word unnecessarily. Beyond that a hard break is better than a ragged page.
			cut := charsPerLine
			if idx := strings.LastIndex(line[:charsPerLine], " "); idx > charsPerLine*3/4 {
				cut = idx
			}
			out = append(out, strings.TrimRight(line[:cut], " "))
			line = strings.TrimLeft(line[cut:], " ")
		}
		out = append(out, line)
	}

	if len(out) == 0 {
		// An empty document is still a valid PDF, and producing one is better than an error: a report with no
		// rows today is a real outcome and the recipient needs the page to know the job ran.
		out = []string{""}
	}
	return out
}

func expandTabs(line string, width int) string {
	if !strings.Contains(line, "\t") {
		return line
	}

	var b strings.Builder
	col := 0
	for _, r := range line {
		if r == '\t' {
			spaces := width - (col % width)
			b.WriteString(strings.Repeat(" ", spaces))
			col += spaces
			continue
		}
		b.WriteRune(r)
		col++
	}
	return b.String()
}

func paginate(lines []string, perPage int) [][]string {
	var pages [][]string
	for len(lines) > 0 {
		n := perPage
		if len(lines) < n {
			n = len(lines)
		}
		pages = append(pages, lines[:n])
		lines = lines[n:]
	}
	if len(pages) == 0 {
		pages = [][]string{{""}}
	}
	return pages
}

// build assembles the PDF file.
//
// Written by hand because the format's minimal subset is small: a catalogue, a page tree, one font, and a content
// stream per page. The offsets in the cross-reference table are the only fiddly part, and they are why this is
// built into a buffer and measured rather than streamed.
func build(pages [][]string, opts Options, width, height, size, leading float64) ([]byte, error) {
	created := opts.Created
	if created.IsZero() {
		created = time.Now()
	}

	// Object numbering: 1 catalogue, 2 page tree, 3 font, 4 info, then a page and a content stream per page.
	const firstPageObj = 5
	pageCount := len(pages)

	objects := make([]string, 0, 4+pageCount*2)

	var kids []string
	for i := range pages {
		kids = append(kids, fmt.Sprintf("%d 0 R", firstPageObj+i*2))
	}

	objects = append(objects,
		"<< /Type /Catalog /Pages 2 0 R >>",
		fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", strings.Join(kids, " "), pageCount),
		// Courier because it is one of the fourteen fonts every PDF reader is required to have, so nothing is
		// embedded and the file stays small. Monospaced also happens to be right for a report with columns.
		"<< /Type /Font /Subtype /Type1 /BaseFont /Courier >>",
		fmt.Sprintf("<< /Title %s /Producer (Perfuse) /CreationDate (D:%s) >>",
			pdfString(opts.Title), created.UTC().Format("20060102150405Z")),
	)

	for i, page := range pages {
		contentObj := firstPageObj + i*2 + 1

		objects = append(objects, fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] "+
				"/Resources << /Font << /F1 3 0 R >> >> /Contents %d 0 R >>",
			width, height, contentObj))

		stream := content(page, size, leading, height)
		objects = append(objects, fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream",
			len(stream), stream))
	}

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")
	// A comment with high bytes, which tells anything transferring the file that it is binary. Without it a
	// naive FTP client in ASCII mode will corrupt it, and an FTP destination is exactly where these files go.
	buf.WriteString("%\xE2\xE3\xCF\xD3\n")

	offsets := make([]int, len(objects)+1)
	for i, obj := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, obj)
	}

	xref := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}

	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R /Info 4 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, xref)

	return buf.Bytes(), nil
}

// content builds one page's drawing instructions.
func content(lines []string, size, leading, height float64) string {
	var b strings.Builder

	b.WriteString("BT\n")
	fmt.Fprintf(&b, "/F1 %.2f Tf\n", size)
	fmt.Fprintf(&b, "%.2f TL\n", leading)
	// The text origin is the baseline of the first line, so it starts one leading below the top margin.
	fmt.Fprintf(&b, "%.2f %.2f Td\n", marginLeft, height-marginTop-leading)

	for _, line := range lines {
		fmt.Fprintf(&b, "%s Tj\nT*\n", pdfString(line))
	}

	b.WriteString("ET")
	return b.String()
}

// pdfString escapes a string literal.
//
// Parentheses and backslashes are the characters that would otherwise end the literal early, and a report full of
// "(see note)" is exactly the input that finds this. Anything outside printable ASCII is written as an octal
// escape, which keeps the file valid without needing a font that has the character.
func pdfString(s string) string {
	var b strings.Builder
	b.WriteByte('(')

	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '(' || c == ')' || c == '\\':
			b.WriteByte('\\')
			b.WriteByte(c)
		case c < 32 || c > 126:
			fmt.Fprintf(&b, "\\%03o", c)
		default:
			b.WriteByte(c)
		}
	}

	b.WriteByte(')')
	return b.String()
}
