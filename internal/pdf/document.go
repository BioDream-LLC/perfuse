// Structured documents, as distinct from the paginated text this package started as.
//
// Render writes a report: monospaced, plain, paginated, which is what a discharge summary going to a Windows share
// needs and what Mirth's Document Writer is used for in practice.
//
// A clinical document is a different artefact. A rendered C-CDA is the human-readable form of a legal record: it
// gets printed, faxed, filed and read years later, and it has headings, tables of medications and results, and a
// header of facts about the patient. Monospaced text cannot carry that legibly - a medication table in Courier
// wraps into porridge.
//
// So this adds a structured builder beside the text one rather than replacing it. Render keeps its contract and the
// channel destination that uses it is untouched.
//
// Still no layout engine, no HTML, no images and no embedded fonts. It uses the standard PostScript fonts every
// reader is required to provide, so nothing is embedded and no font licence has to be checked - which preserves the
// argument the text renderer makes: a renderer with no dependencies cannot fail to install on a segregated network.

package pdf

import (
	"bytes"
	"fmt"
	"strings"
)

// Page geometry, reusing the constants the text renderer already defines.
//
// Duplicating them would let the two renderers drift apart, and two documents from the same product with different
// margins is the kind of detail somebody notices on a printout and cannot explain.
const (
	pageWidth  = a4Width
	pageHeight = a4Height

	// The only measurement this adds. The text renderer never needed a right margin because a monospaced line
	// count decides where its text stops.
	marginRight = marginLeft

	contentWidth = pageWidth - marginLeft - marginRight
)

// Font identifies one of the standard fonts a reader must provide.
type Font int

const (
	Regular Font = iota
	Bold
	Italic
	Mono
)

func (f Font) resource() string {
	switch f {
	case Bold:
		return "F2"
	case Italic:
		return "F3"
	case Mono:
		return "F4"
	default:
		return "F1"
	}
}

// Document accumulates content and writes it as a PDF.
type Document struct {
	pages   []*bytes.Buffer
	current *bytes.Buffer

	// y is the baseline for the next line, measured down from the top of the page.
	y float64

	// title and subject go in the document information dictionary, which is what a reader shows in its title bar
	// and what a document management system indexes on.
	title    string
	subject  string
	author   string
	keywords string
}

// New starts a document with one page.
func New(title string) *Document {
	d := &Document{title: title}
	d.newPage()
	return d
}

// SetMeta records the document information a reader displays and an archive indexes.
func (d *Document) SetMeta(author, subject, keywords string) {
	d.author, d.subject, d.keywords = author, subject, keywords
}

func (d *Document) newPage() {
	d.current = &bytes.Buffer{}
	d.pages = append(d.pages, d.current)
	d.y = marginTop
}

// space reserves vertical room, starting a new page when there is not enough.
//
// Checked before drawing rather than after, because a heading that fits and whose first line of body text does not
// leaves a heading alone at the bottom of a page - which in a clinical document reads as a section with no content.
func (d *Document) space(need float64) {
	if d.y+need > pageHeight-marginBottom {
		d.newPage()
	}
}

// Heading writes a section heading with the space above it that makes it read as a break.
func (d *Document) Heading(text string, level int) {
	size := 15.0
	if level > 1 {
		size = 12.0
	}

	// Enough room for the heading and a line under it, so a heading never ends a page alone.
	d.space(size*1.6 + 14)

	if d.y > marginTop {
		d.y += size * 0.6
	}

	d.line(text, Bold, size, 0)
	d.y += size * 0.35
}

// Paragraph writes wrapped body text.
func (d *Document) Paragraph(text string, font Font, size float64, indent float64) {
	for _, para := range strings.Split(text, "\n") {
		para = strings.TrimRight(para, " \t\r")
		if strings.TrimSpace(para) == "" {
			d.y += size * 0.5
			continue
		}
		for _, l := range wrapWidth(para, font, size, contentWidth-indent) {
			d.space(size * 1.35)
			d.line(l, font, size, indent)
		}
	}
	d.y += size * 0.3
}

// KeyValue writes a label and value on one line, for header facts.
func (d *Document) KeyValue(label, value string, size float64) {
	if strings.TrimSpace(value) == "" {
		return
	}

	// The label bold and the value regular on the same baseline, so a column of facts scans.
	d.space(size * 1.4)

	labelWidth := textWidth(label+"  ", Bold, size)
	baseline := d.y + size

	fmt.Fprintf(d.current, "BT /%s %.1f Tf %.2f %.2f Td (%s) Tj ET\n",
		Bold.resource(), size, marginLeft, pageHeight-baseline, escapeText(label))

	// Wrapped within the space left after the label, and continuation lines align under the value rather than
	// under the label, which is what makes a long address readable.
	avail := contentWidth - labelWidth
	lines := wrapWidth(value, Regular, size, avail)
	for i, l := range lines {
		if i > 0 {
			d.y += size * 1.3
			d.space(size * 1.4)
			baseline = d.y + size
		}
		fmt.Fprintf(d.current, "BT /%s %.1f Tf %.2f %.2f Td (%s) Tj ET\n",
			Regular.resource(), size, marginLeft+labelWidth, pageHeight-baseline, escapeText(l))
	}
	d.y += size * 1.4
}

// Table writes a simple table with a bold header row.
//
// Columns are sized by their widest cell rather than evenly, because clinical tables have one wide column of drug
// or problem names and several narrow ones, and even columns waste most of the page on dates.
func (d *Document) Table(header []string, rows [][]string, size float64) {
	if len(header) == 0 {
		return
	}

	cols := len(header)
	widths := make([]float64, cols)
	for i, h := range header {
		widths[i] = textWidth(h, Bold, size) + 12
	}
	for _, row := range rows {
		for i := 0; i < cols && i < len(row); i++ {
			if w := textWidth(row[i], Regular, size) + 12; w > widths[i] {
				widths[i] = w
			}
		}
	}

	total := 0.0
	for _, w := range widths {
		total += w
	}
	// Scaled to fit rather than allowed to run off the page. A table whose last column is beyond the paper edge
	// loses the column silently, and in a medication table that column is often the dose.
	if total > contentWidth {
		scale := contentWidth / total
		for i := range widths {
			widths[i] *= scale
		}
	}

	writeRow := func(cells []string, font Font) {
		// The tallest cell decides the row height, so a wrapped drug name does not overlap the row beneath it.
		height := size * 1.35
		wrapped := make([][]string, cols)
		for i := 0; i < cols; i++ {
			var cell string
			if i < len(cells) {
				cell = cells[i]
			}
			wrapped[i] = wrapWidth(cell, font, size, widths[i]-8)
			if h := float64(len(wrapped[i])) * size * 1.2; h+4 > height {
				height = h + 4
			}
		}

		d.space(height)

		x := marginLeft
		for i := 0; i < cols; i++ {
			for j, l := range wrapped[i] {
				baseline := d.y + size + float64(j)*size*1.2
				fmt.Fprintf(d.current, "BT /%s %.1f Tf %.2f %.2f Td (%s) Tj ET\n",
					font.resource(), size, x+4, pageHeight-baseline, escapeText(l))
			}
			x += widths[i]
		}
		d.y += height
	}

	writeRow(header, Bold)

	// A rule under the header, so a header row is distinguishable when a table breaks across pages.
	d.space(3)
	fmt.Fprintf(d.current, "0.6 w 0.55 0.6 0.65 RG %.2f %.2f m %.2f %.2f l S\n",
		marginLeft, pageHeight-d.y, marginLeft+contentWidth, pageHeight-d.y)
	d.y += 4

	for _, row := range rows {
		writeRow(row, Regular)
	}
	d.y += size * 0.4
}

// Rule draws a horizontal line.
func (d *Document) Rule() {
	d.space(8)
	d.y += 4
	fmt.Fprintf(d.current, "0.5 w 0.55 0.6 0.65 RG %.2f %.2f m %.2f %.2f l S\n",
		marginLeft, pageHeight-d.y, marginLeft+contentWidth, pageHeight-d.y)
	d.y += 6
}

// Note writes small print, for the provenance a rendered clinical document must carry.
func (d *Document) Note(text string) {
	d.Paragraph(text, Italic, 8, 0)
}

func (d *Document) line(text string, font Font, size float64, indent float64) {
	baseline := d.y + size
	fmt.Fprintf(d.current, "BT /%s %.1f Tf %.2f %.2f Td (%s) Tj ET\n",
		font.resource(), size, marginLeft+indent, pageHeight-baseline, escapeText(text))
	d.y += size * 1.35
}

// Bytes writes the finished PDF.
//
// Offsets are recorded as each object is written. Calculating them afterwards means computing the length of
// everything already written, and one arithmetic slip produces a file that every reader rejects with no clue as to
// which object is wrong.
func (d *Document) Bytes() []byte {
	var out bytes.Buffer
	offsets := []int{}

	object := func(body string) {
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", len(offsets), body)
	}

	// PDF 1.4 rather than something newer. Every reader in a hospital handles it, including the ones embedded in
	// document management systems that were installed a decade ago and will not be replaced.
	out.WriteString("%PDF-1.4\n")

	// A binary comment marking the file as binary, so tools that transfer it do not helpfully convert line
	// endings and corrupt it. This is a real failure mode with FTP-based document delivery, which hospitals
	// still run.
	out.Write([]byte{'%', 0xE2, 0xE3, 0xCF, 0xD3, '\n'})

	// Object numbering is fixed so references can be written before the objects exist:
	// 1 catalog, 2 pages, 3 info, 4-7 fonts, then a content stream and a page object per page.
	pageCount := len(d.pages)
	firstPageObj := 8

	kids := make([]string, 0, pageCount)
	for i := 0; i < pageCount; i++ {
		kids = append(kids, fmt.Sprintf("%d 0 R", firstPageObj+i*2+1))
	}

	object("<< /Type /Catalog /Pages 2 0 R >>")
	object(fmt.Sprintf("<< /Type /Pages /Count %d /Kids [%s] >>", pageCount, strings.Join(kids, " ")))
	object(fmt.Sprintf("<< /Title (%s) /Author (%s) /Subject (%s) /Keywords (%s) /Producer (Perfuse) >>",
		escapeText(d.title), escapeText(d.author), escapeText(d.subject), escapeText(d.keywords)))

	// WinAnsiEncoding named explicitly. Without it a reader uses the font's built-in encoding and accented
	// characters in a patient's name come out as something else, which in a clinical document is a defect.
	for _, base := range []string{"Helvetica", "Helvetica-Bold", "Helvetica-Oblique", "Courier"} {
		object(fmt.Sprintf(
			"<< /Type /Font /Subtype /Type1 /BaseFont /%s /Encoding /WinAnsiEncoding >>", base))
	}

	for i, page := range d.pages {
		content := page.Bytes()
		offsets = append(offsets, out.Len())
		fmt.Fprintf(&out, "%d 0 obj\n<< /Length %d >>\nstream\n", len(offsets), len(content))
		out.Write(content)
		out.WriteString("\nendstream\nendobj\n")

		object(fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %.2f %.2f] "+
				"/Resources << /Font << /F1 4 0 R /F2 5 0 R /F3 6 0 R /F4 7 0 R >> >> /Contents %d 0 R >>",
			pageWidth, pageHeight, firstPageObj+i*2))
	}

	xref := out.Len()
	fmt.Fprintf(&out, "xref\n0 %d\n", len(offsets)+1)
	// The free-list head, which must be exactly this. Readers check it.
	out.WriteString("0000000000 65535 f \n")
	for _, off := range offsets {
		fmt.Fprintf(&out, "%010d 00000 n \n", off)
	}
	fmt.Fprintf(&out, "trailer\n<< /Size %d /Root 1 0 R /Info 3 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets)+1, xref)

	return out.Bytes()
}

// escapeText makes a string safe inside a PDF literal, reusing the escaping the text renderer already does.
//
// pdfString handles the parentheses, the backslashes and the octal form for high bytes, and returns the string with
// its brackets already on. Reimplementing that here would give one package two escaping rules, and one of them
// would eventually be wrong.
//
// What is added is tab expansion and the removal of control characters. A tab inside a text literal shifts
// everything after it on the line, and a clinical narrative that came through a mainframe is full of them.
func escapeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\t':
			b.WriteString("    ")
		case r == '\n' || r == '\r':
			b.WriteByte(' ')
		case r < 32 || r == 127:
			// Dropped rather than shown as a replacement character. A visible marker in a patient's name reads as
			// data corruption to whoever is holding the printout.
		default:
			b.WriteRune(r)
		}
	}

	// pdfString returns the string already bracketed, so the brackets are trimmed here and added by callers.
	out := pdfString(b.String())
	return out[1 : len(out)-1]
}

// wrap breaks text into lines that fit a width.
func wrapWidth(text string, font Font, size, width float64) []string {
	words := strings.Fields(text)
	if len(words) == 0 {
		return nil
	}

	var lines []string
	line := words[0]
	for _, w := range words[1:] {
		if textWidth(line+" "+w, font, size) <= width {
			line += " " + w
			continue
		}
		lines = append(lines, line)
		line = w
	}
	lines = append(lines, line)

	// A single word wider than the column is broken rather than allowed to run off the page. This happens with
	// long coded identifiers and with URLs in provenance notes, and losing the tail off the paper edge means
	// losing the part that distinguishes one identifier from another.
	var out []string
	for _, l := range lines {
		for textWidth(l, font, size) > width && len([]rune(l)) > 1 {
			r := []rune(l)
			cut := len(r)
			for cut > 1 && textWidth(string(r[:cut]), font, size) > width {
				cut--
			}
			out = append(out, string(r[:cut]))
			l = string(r[cut:])
		}
		out = append(out, l)
	}
	return out
}

// textWidth measures a string in points.
//
// Real per-character widths, not an average. An average makes long lines overflow the margin and short ones look
// ragged, and in a table it makes columns overlap - which in a medication table can put a dose against the wrong
// drug.
func textWidth(s string, font Font, size float64) float64 {
	widths := helveticaWidths
	switch font {
	case Bold:
		widths = helveticaBoldWidths
	case Mono:
		// Courier is monospaced at 600/1000 for every glyph.
		return float64(len([]rune(s))) * 0.6 * size
	}

	total := 0.0
	for _, r := range s {
		if r < 32 || r > 255 {
			total += 556
			continue
		}
		total += float64(widths[r-32])
	}
	return total / 1000 * size
}

// Widths for the standard fonts, in thousandths of the point size, for characters 32 to 255.
//
// These come from the font metrics the standard fonts are defined by, not from measurement. A reader uses the same
// numbers, so text measured with these lands exactly where this code expects it to.
var helveticaWidths = [224]int16{
	278, 278, 355, 556, 556, 889, 667, 191, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 278, 278, 584, 584, 584, 556,
	1015, 667, 667, 722, 722, 667, 611, 778, 722, 278, 500, 667, 556, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 278, 278, 278, 469, 556,
	333, 556, 556, 500, 556, 556, 278, 556, 556, 222, 222, 500, 222, 833, 556, 556,
	556, 556, 333, 500, 278, 556, 500, 722, 500, 500, 500, 334, 260, 334, 584, 350,
	556, 350, 222, 556, 333, 1000, 556, 556, 333, 1000, 667, 333, 1000, 350, 611, 350,
	350, 222, 222, 333, 333, 350, 556, 1000, 333, 1000, 500, 333, 944, 350, 500, 667,
	278, 333, 556, 556, 556, 556, 260, 556, 333, 737, 370, 556, 584, 333, 737, 552,
	400, 549, 333, 333, 333, 576, 537, 278, 333, 333, 365, 556, 834, 834, 834, 611,
	667, 667, 667, 667, 667, 667, 1000, 722, 667, 667, 667, 667, 278, 278, 278, 278,
	722, 722, 778, 778, 778, 778, 778, 584, 778, 722, 722, 722, 722, 667, 667, 611,
	556, 556, 556, 556, 556, 556, 889, 500, 556, 556, 556, 556, 278, 278, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 549, 611, 556, 556, 556, 556, 500, 556, 500,
}

var helveticaBoldWidths = [224]int16{
	278, 333, 474, 556, 556, 889, 722, 238, 333, 333, 389, 584, 278, 333, 278, 278,
	556, 556, 556, 556, 556, 556, 556, 556, 556, 556, 333, 333, 584, 584, 584, 611,
	975, 722, 722, 722, 722, 667, 611, 778, 722, 278, 556, 722, 611, 833, 722, 778,
	667, 778, 722, 667, 611, 722, 667, 944, 667, 667, 611, 333, 278, 333, 584, 556,
	333, 556, 611, 556, 611, 556, 333, 611, 611, 278, 278, 556, 278, 889, 611, 611,
	611, 611, 389, 556, 333, 611, 556, 778, 556, 556, 500, 389, 280, 389, 584, 350,
	556, 350, 278, 556, 500, 1000, 556, 556, 333, 1000, 667, 333, 1000, 350, 611, 350,
	350, 278, 278, 500, 500, 350, 556, 1000, 333, 1000, 556, 333, 944, 350, 500, 667,
	278, 333, 556, 556, 556, 556, 280, 556, 333, 737, 370, 556, 584, 333, 737, 552,
	400, 549, 333, 333, 333, 576, 556, 278, 333, 333, 365, 556, 834, 834, 834, 611,
	722, 722, 722, 722, 722, 722, 1000, 722, 667, 667, 667, 667, 278, 278, 278, 278,
	722, 722, 778, 778, 778, 778, 778, 584, 778, 722, 722, 722, 722, 667, 667, 611,
	556, 556, 556, 556, 556, 556, 889, 556, 556, 556, 556, 556, 278, 278, 278, 278,
	611, 611, 611, 611, 611, 611, 611, 549, 611, 611, 611, 611, 611, 556, 611, 556,
}
