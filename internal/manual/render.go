package manual

import (
	"fmt"
	"html"
	"strings"
)

// Doc is the whole manual.
type Doc struct {
	Title    string
	Subtitle string
	Version  string

	// Description is the meta description used when this manual is published on the web.
	// Empty omits the tag rather than emitting a blank one, which is worse than absent.
	Description string

	// Canonical is the address this manual should be credited to when the same file is
	// reachable at more than one, which it is: the site publishes it and every release
	// archive contains a copy.
	Canonical string

	Chapters []Chapter
}

// Chapter is a numbered top-level division.
type Chapter struct {
	Title string
	// Intro is prose before the first section.
	Intro string
	// Sections are the numbered divisions within.
	Sections []Section
}

// Section is a numbered division within a chapter.
type Section struct {
	Title string
	Body  string
	// Subsections nest one level further. Three levels is the limit, deliberately: the Hibernate manual goes to four
	// and its table of contents runs to several screens of near-identical entries, which is harder to navigate than a
	// flatter structure with a good index.
	Subsections []Subsection
}

// Subsection is the deepest numbered division.
type Subsection struct {
	Title string
	Body  string
}

// numbering assigns 1, 1.1, 1.1.1 and anchors.
type numbered struct {
	number string
	title  string
	anchor string
	depth  int
}

// toc walks the document and produces the numbered entries.
//
// Built once and used for both the table of contents and the headings, so a number in the contents and the number on
// the heading it points at cannot disagree.
func (d *Doc) toc() []numbered {
	var out []numbered
	for ci, c := range d.Chapters {
		cn := fmt.Sprint(ci + 1)
		out = append(out, numbered{number: cn, title: c.Title, anchor: anchorFor(cn, c.Title), depth: 1})
		for si, s := range c.Sections {
			sn := fmt.Sprintf("%s.%d", cn, si+1)
			out = append(out, numbered{number: sn, title: s.Title, anchor: anchorFor(sn, s.Title), depth: 2})
			for ui, u := range s.Subsections {
				un := fmt.Sprintf("%s.%d", sn, ui+1)
				out = append(out, numbered{number: un, title: u.Title, anchor: anchorFor(un, u.Title), depth: 3})
			}
		}
	}
	return out
}

// slugFor is the anchor without the number.
//
// Cross-references in the written chapters are given as slugs, so a link reads [migration](#migration-from-mirth) and
// survives the chapter being renumbered. Numbered anchors are still what the document uses - two sections do share a
// title and a title-only anchor would collide - but nobody has to write one by hand, which is where the errors were.
func slugFor(title string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// Anchors maps every slug to the numbered anchor it resolves to.
//
// Exported so the drift test can check a link before the document is rendered, and so a link that does not resolve is a
// build failure rather than a dead link somebody finds by clicking it in a 200-page document.
func (d *Doc) Anchors() map[string]string {
	out := map[string]string{}
	for _, e := range d.toc() {
		// First writer wins, so a chapter title beats a subsection that happens to share its slug.
		if _, seen := out[slugFor(e.title)]; !seen {
			out[slugFor(e.title)] = e.anchor
		}
		// The numbered form resolves to itself, so an explicit anchor still works.
		out[e.anchor] = e.anchor
	}
	return out
}

// anchorFor builds a stable fragment identifier.
//
// The number is included so an anchor stays unique when two sections share a title, which happens throughout the
// reference chapters: several transports have a Timeout section. Titles alone would collide and every duplicate link
// would land on the first one.
func anchorFor(number, title string) string {
	var b strings.Builder
	b.WriteString("s")
	b.WriteString(strings.ReplaceAll(number, ".", "-"))
	b.WriteString("-")
	for _, r := range strings.ToLower(title) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '-' || r == '_':
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

// HTML renders the whole manual as one page.
//
// One page rather than a page per chapter, matching the reference manuals this is modelled on. The reason is search:
// a single document is searchable with the browser's own find, works offline as one file, and prints to a PDF with
// correct page numbering. A frameset of separate pages needs a search index to be usable at all.
func (d *Doc) HTML() string {
	var b strings.Builder

	b.WriteString("<!DOCTYPE html>\n<html lang=\"en\">\n<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(d.Title))

	// A description and a canonical, because this file is published on the web as well as
	// shipped in the release archives. Without the description a search engine invents one
	// from whatever text it finds first, which here is the table of contents. Without the
	// canonical the same manual at two addresses - the site and the archive somebody
	// unpacked into a web root - competes with itself.
	if d.Description != "" {
		fmt.Fprintf(&b, "<meta name=\"description\" content=\"%s\">\n", html.EscapeString(d.Description))
	}
	if d.Canonical != "" {
		fmt.Fprintf(&b, "<link rel=\"canonical\" href=\"%s\">\n", html.EscapeString(d.Canonical))
	}

	b.WriteString("<style>\n")
	b.WriteString(manualCSS)
	b.WriteString("</style>\n</head>\n<body>\n")

	// Title block.

	entries := d.toc()
	links := d.Anchors()

	// Table of contents.
	// The title block sits inside the contents rail, so the product name, subtitle and version stay visible while
	// scrolling rather than disappearing at the first chapter. In print everything returns to a single flow, so the PDF
	// still opens on a title page followed by the contents.
	b.WriteString("<nav class=\"toc\">\n")
	b.WriteString("<header class=\"titlepage\">\n")
	fmt.Fprintf(&b, "<h1>%s</h1>\n", html.EscapeString(d.Title))
	if d.Subtitle != "" {
		fmt.Fprintf(&b, "<p class=\"subtitle\">%s</p>\n", html.EscapeString(d.Subtitle))
	}
	if d.Version != "" {
		fmt.Fprintf(&b, "<p class=\"version\">%s</p>\n", html.EscapeString(d.Version))
	}
	b.WriteString("</header>\n")
	b.WriteString("<h2>Table of Contents</h2>\n")
	for _, e := range entries {
		fmt.Fprintf(&b, "<div class=\"toc-d%d\"><a href=\"#%s\"><span class=\"num\">%s</span> %s</a></div>\n",
			e.depth, e.anchor, e.number, html.EscapeString(e.title))
	}
	b.WriteString("</nav>\n")

	// Chapters live in their own element so the contents can sit beside them as a rail.
	//
	// Without a wrapper the sections are siblings of the nav, and a two-column grid on the body would drop each chapter
	// into an alternating cell rather than stacking them in one column. The print rules collapse this back to a single
	// flow, so the PDF is unaffected by the wrapper existing.
	b.WriteString("<main class=\"doc\">\n")

	// Body, walked in the same order as the contents.
	i := 0
	for _, c := range d.Chapters {
		ce := entries[i]
		i++
		b.WriteString("<section class=\"chapter\">\n")
		fmt.Fprintf(&b, "<h2 id=\"%s\"><span class=\"num\">%s</span> %s</h2>\n",
			ce.anchor, ce.number, html.EscapeString(c.Title))
		if c.Intro != "" {
			b.WriteString(renderProse(c.Intro, links))
		}

		for _, s := range c.Sections {
			se := entries[i]
			i++
			fmt.Fprintf(&b, "<h3 id=\"%s\"><span class=\"num\">%s</span> %s</h3>\n",
				se.anchor, se.number, html.EscapeString(s.Title))
			if s.Body != "" {
				b.WriteString(renderProse(s.Body, links))
			}

			for _, u := range s.Subsections {
				ue := entries[i]
				i++
				fmt.Fprintf(&b, "<h4 id=\"%s\"><span class=\"num\">%s</span> %s</h4>\n",
					ue.anchor, ue.number, html.EscapeString(u.Title))
				if u.Body != "" {
					b.WriteString(renderProse(u.Body, links))
				}
			}
		}
		b.WriteString("</section>\n")
	}

	b.WriteString("</main>\n")
	b.WriteString("</body>\n</html>\n")
	return b.String()
}

// renderProse turns the light markup used in chapter bodies into HTML.
//
// A deliberately small subset - paragraphs, fenced code, bullet and numbered lists, tables, notes and inline code -
// rather than a markdown dependency. The subset is small enough to read in one sitting, which matters because
// everything it does not support fails visibly as literal text rather than silently as missing formatting.
func renderProse(src string, links map[string]string) string {
	var b strings.Builder
	lines := strings.Split(src, "\n")

	for i := 0; i < len(lines); i++ {
		line := lines[i]

		switch {
		// Fenced code. Contents are escaped and otherwise untouched, because a configuration example that has been
		// reformatted is no longer an example of anything.
		case strings.HasPrefix(line, "```"):
			lang := strings.TrimSpace(strings.TrimPrefix(line, "```"))
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(lines[i], "```"); i++ {
				code = append(code, lines[i])
			}
			// A fence marked svg is a diagram rather than an example, and is emitted as itself.
			//
			// The manual had no way to draw anything, which meant every explanation of a flow was prose describing boxes. Inline SVG
			// is the right form here for three reasons: it needs no separate file to be shipped alongside the single-file HTML manual,
			// it prints at any size in the PDF, and it is text, so a diagram that goes out of date shows up in a diff rather than
			// sitting in a binary nobody opens.
			//
			// The contents are NOT escaped, which is the one place in this renderer where that is true. That is safe here and nowhere
			// else: these chapters are files in this repository, written by whoever can already change the program, so there is no
			// boundary being crossed. A test asserts every svg fence parses as XML, because a malformed one would otherwise become
			// broken markup in the published manual.
			if lang == "svg" {
				fmt.Fprintf(&b, "<figure class=\"diagram\">\n%s\n</figure>\n", strings.Join(code, "\n"))

				continue
			}

			cls := "code"
			if lang != "" {
				cls += " lang-" + safeClass(lang)
			}
			fmt.Fprintf(&b, "<pre class=\"%s\"><code>%s</code></pre>\n",
				cls, html.EscapeString(strings.Join(code, "\n")))

		// Tables. A leading pipe row, then a separator row, then rows.
		case strings.HasPrefix(line, "|"):
			var rows []string
			for ; i < len(lines) && strings.HasPrefix(lines[i], "|"); i++ {
				rows = append(rows, lines[i])
			}
			i--
			b.WriteString(renderTable(rows, links))

		// Notes, which the reference chapters use for the things that bite.
		case strings.HasPrefix(line, "> "):
			var note []string
			for ; i < len(lines) && strings.HasPrefix(lines[i], "> "); i++ {
				note = append(note, strings.TrimPrefix(lines[i], "> "))
			}
			i--
			fmt.Fprintf(&b, "<div class=\"note\">%s</div>\n", inline(strings.Join(note, " "), links))

		case strings.HasPrefix(line, "- "):
			b.WriteString("<ul>\n")
			for ; i < len(lines) && strings.HasPrefix(lines[i], "- "); i++ {
				fmt.Fprintf(&b, "<li>%s</li>\n", inline(strings.TrimPrefix(lines[i], "- "), links))
			}
			i--
			b.WriteString("</ul>\n")

		case numberedItem(line) != "":
			b.WriteString("<ol>\n")
			for ; i < len(lines) && numberedItem(lines[i]) != ""; i++ {
				fmt.Fprintf(&b, "<li>%s</li>\n", inline(numberedItem(lines[i]), links))
			}
			i--
			b.WriteString("</ol>\n")

		case strings.TrimSpace(line) == "":
			// Blank lines separate blocks and produce nothing themselves.

		default:
			// A paragraph runs until a blank line or the start of another block.
			var para []string
			for ; i < len(lines); i++ {
				l := lines[i]
				if strings.TrimSpace(l) == "" || strings.HasPrefix(l, "```") ||
					strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "> ") ||
					strings.HasPrefix(l, "|") || numberedItem(l) != "" {
					break
				}
				para = append(para, l)
			}
			i--
			fmt.Fprintf(&b, "<p>%s</p>\n", inline(strings.Join(para, " "), links))
		}
	}

	return b.String()
}

// renderTable renders a pipe table.
func renderTable(rows []string, links map[string]string) string {
	cells := func(r string) []string {
		r = strings.TrimPrefix(strings.TrimSuffix(strings.TrimSpace(r), "|"), "|")
		out := strings.Split(r, "|")
		for i := range out {
			out[i] = strings.TrimSpace(out[i])
		}
		return out
	}

	var b strings.Builder
	b.WriteString("<table>\n")

	for ri, r := range rows {
		// The separator row under the header carries no content.
		if ri == 1 && strings.Trim(strings.ReplaceAll(r, "|", ""), " -:") == "" {
			continue
		}
		tag := "td"
		if ri == 0 {
			tag = "th"
			b.WriteString("<thead>")
		}
		b.WriteString("<tr>")
		for _, c := range cells(r) {
			fmt.Fprintf(&b, "<%s>%s</%s>", tag, inline(c, links), tag)
		}
		b.WriteString("</tr>")
		if ri == 0 {
			b.WriteString("</thead>\n<tbody>\n")
		} else {
			b.WriteString("\n")
		}
	}

	b.WriteString("</tbody>\n</table>\n")
	return b.String()
}

// numberedItem returns the text of a numbered list item, or empty if the line is not one.
func numberedItem(line string) string {
	i := 0
	for i < len(line) && line[i] >= '0' && line[i] <= '9' {
		i++
	}
	if i == 0 || i+1 >= len(line) || line[i] != '.' || line[i+1] != ' ' {
		return ""
	}
	return line[i+2:]
}

// inline handles code spans, emphasis and links.
//
// Escaping happens first and the markup is inserted afterwards, so a configuration value containing angle brackets
// renders as itself rather than as an element.
func inline(s string, links map[string]string) string {
	s = html.EscapeString(s)

	s = pairwise(s, "`", "<code>", "</code>")
	s = pairwise(s, "**", "<strong>", "</strong>")

	// Cross-references, written as [text](#anchor).
	for {
		i := strings.Index(s, "](#")
		if i < 0 {
			break
		}
		open := strings.LastIndex(s[:i], "[")
		if open < 0 {
			break
		}
		close := strings.Index(s[i:], ")")
		if close < 0 {
			break
		}
		text := s[open+1 : i]
		target := s[i+3 : i+close]
		// Resolved through the slug map so a chapter can be renumbered without every link into it breaking. An
		// unresolvable target is left as written and caught by TestEveryCrossReferenceResolves rather than shipping as
		// a link that goes nowhere.
		anchor := target
		if a, ok := links[target]; ok {
			anchor = a
		}
		s = s[:open] + fmt.Sprintf("<a href=\"#%s\">%s</a>", anchor, text) + s[i+close+1:]
	}

	return s
}

// pairwise replaces matched delimiters, leaving an unmatched one as literal text.
//
// Unmatched delimiters are left alone rather than being treated as an opening one, because a stray backtick in a
// sentence would otherwise turn the entire rest of the manual into code.
func pairwise(s, delim, open, close string) string {
	var b strings.Builder
	rest := s
	for {
		i := strings.Index(rest, delim)
		if i < 0 {
			break
		}
		j := strings.Index(rest[i+len(delim):], delim)
		if j < 0 {
			break
		}
		b.WriteString(rest[:i])
		b.WriteString(open)
		b.WriteString(rest[i+len(delim) : i+len(delim)+j])
		b.WriteString(close)
		rest = rest[i+len(delim)+j+len(delim):]
	}
	b.WriteString(rest)
	return b.String()
}

// safeClass keeps a language hint to characters that are valid in a class name.
func safeClass(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// Size reports how big the manual is, for the build to print.
//
// Printed on every build so that a change which accidentally drops a chapter or a whole reference group is visible
// immediately, rather than at the point somebody looks for a key that is no longer documented.
func (d *Doc) Size() (chapters, sections, keys int) {
	chapters = len(d.Chapters)
	for _, c := range d.Chapters {
		sections += len(c.Sections)
		for _, s := range c.Sections {
			sections += len(s.Subsections)
		}
	}
	for _, e := range d.toc() {
		if e.depth == 3 {
			keys++
		}
	}
	return chapters, sections, keys
}
