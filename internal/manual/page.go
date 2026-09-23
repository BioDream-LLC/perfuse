package manual

import (
	"fmt"
	"html"
	"strings"
)

// pageCSS covers the parts a website page has and the manual does not: a navigation bar, a footer,
// and the <details> blocks the README uses for its FAQ. Kept separate from manualCSS so that styling
// the website cannot change the typeset manual or the PDF built from it.
const pageCSS = `
/* The manual lays its body out as a grid: a 320px table-of-contents column beside the text. A
 * website page has no table of contents, so that column is empty, and worse, the remaining track
 * sizes itself to main's 832px max-width instead of clamping to the viewport. The result was a
 * page 832px wide inside a 320px phone screen, with every paragraph running off the edge - not
 * because anything on the page was too wide, but because the layout it inherited was built for a
 * document with a sidebar.
 *
 * Overriding to block rather than adjusting the grid, because there is no second column here to
 * lay out. pageCSS is emitted after manualCSS so this wins on document order.
 */
body { display: block; }

.sitenav { max-width: 52rem; margin: 0 auto 2rem; padding: 1rem 1.5rem; border-bottom: 1px solid #e3e3e3;
  display: flex; gap: 1.25rem; flex-wrap: wrap; font-size: 0.95rem; }
.sitenav a { color: #0b6bcb; text-decoration: none; }
.sitenav a:first-child { font-weight: 700; color: #1a1a1a; }
.sitenav a:hover { text-decoration: underline; }
main { max-width: 52rem; margin: 0 auto; padding: 0 1.5rem; }
.sitefooter { max-width: 52rem; margin: 4rem auto 3rem; padding: 1.5rem; border-top: 1px solid #e3e3e3;
  font-size: 0.9rem; color: #555; }
details { margin: 1rem 0; padding: 0.75rem 1rem; background: #f7f8fa; border: 1px solid #e3e3e3;
  border-radius: 6px; }
details summary { cursor: pointer; font-weight: 600; }
details[open] summary { margin-bottom: 0.75rem; }
main img { max-width: 100%; height: auto; }
main table { width: 100%; border-collapse: collapse; margin: 1.25rem 0; font-size: 0.95rem; }
main table th, main table td { border: 1px solid #e3e3e3; padding: 0.5rem 0.7rem; text-align: left;
  vertical-align: top; }
main table th { background: #f7f8fa; }

/* Narrow screens.
 *
 * These pages are rendered from the README and the docs, which lean on wide comparison tables -
 * three and four columns of prose. At phone width a table like that either overflows the page,
 * dragging every paragraph sideways with it, or squeezes into columns two words wide. Scrolling
 * the table inside its own box is the lesser evil: the surrounding text stays readable and the
 * table stays legible, at the cost of a sideways swipe on the one element that needs it.
 */
main img { max-width: 100%; height: auto; }

@media (max-width: 620px) {
  .sitenav, main, .sitefooter { padding-left: 1rem; padding-right: 1rem; }
  .sitenav { gap: 0.9rem; font-size: 0.9rem; }
  main table { display: block; overflow-x: auto; white-space: nowrap; font-size: 0.85rem; }
  main table th, main table td { padding: 0.4rem 0.55rem; }
  main pre { overflow-x: auto; }
  main code { word-break: break-word; }
  details { padding: 0.6rem 0.8rem; }
}
`

// isHorizontalRule reports whether a line is a markdown thematic break.
//
// Requires three or more of the same character and nothing else. Checked after the table case in
// renderProse, which claims any line starting with a pipe, so a table's |---|---| separator never
// reaches this.
func isHorizontalRule(line string) bool {
	t := strings.TrimSpace(line)
	if len(t) < 3 {
		return false
	}

	c := t[0]
	if c != '-' && c != '*' && c != '_' {
		return false
	}
	for i := 0; i < len(t); i++ {
		if t[i] != c {
			return false
		}
	}

	return true
}

type heading struct {
	level int
	title string
}

// headingOf returns the heading a line represents, or nil if it is not one.
//
// Requires a space after the hashes, so that a line beginning with a hash for any other reason -
// a shell comment inside an unfenced block, a C preprocessor directive, an issue reference like
// #412 - is left as prose rather than silently becoming a heading.
func headingOf(line string) *heading {
	level := 0
	for level < len(line) && line[level] == '#' {
		level++
	}
	if level == 0 || level > 6 {
		return nil
	}
	if level >= len(line) || line[level] != ' ' {
		return nil
	}

	title := strings.TrimSpace(line[level+1:])
	if title == "" {
		return nil
	}

	return &heading{level: level, title: title}
}

// isRawHTMLLine reports whether a line is a bare HTML tag that should be emitted as itself.
//
// Deliberately narrow: the line must begin at column zero with < or </, then a tag name, then a
// space, > or />. That matches the structural markup the README uses - <table>, <tr>, <details>,
// <summary>, <img>, <div> - and does not match prose that happens to contain a comparison, an
// arrow in a sentence, or an inline <code> reference, all of which must keep going through the
// markdown path and be escaped.
func isRawHTMLLine(line string) bool {
	if !strings.HasPrefix(line, "<") {
		return false
	}
	rest := strings.TrimPrefix(line[1:], "/")
	if rest == "" {
		return false
	}

	// A tag name, which must start with a letter.
	c := rest[0]
	if !(c >= 'a' && c <= 'z') && !(c >= 'A' && c <= 'Z') {
		return false
	}

	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
			continue
		case c == ' ' || c == '>' || c == '/' || c == '\n':
			// A tag name followed by something that can legally follow one.
			return true
		default:
			// Anything else means this was not a tag: an inequality, an arrow, prose.
			return false
		}
	}

	return false
}

// RenderStandalonePage renders one markdown document as a complete, self-contained HTML page.
//
// This exists so the website can be generated from the same markdown the repository already
// publishes, rather than being a hand-written copy of it. A hand-written copy is how the
// published manual came to be three chapters behind the site: two documents saying the same
// thing drift, and nothing tells you when they have.
//
// The page carries the manual's stylesheet inline. That is the same choice the manual makes and
// for the same reason - one file that renders correctly with no other request, which matters for
// a document somebody may save and read later without a network.
func RenderStandalonePage(title, description, canonical, src string) string {
	body := renderProse(src, nil)

	var b strings.Builder
	b.WriteString("<!doctype html>\n<html lang=\"en\">\n<head>\n")
	b.WriteString("<meta charset=\"utf-8\">\n")
	b.WriteString("<meta name=\"viewport\" content=\"width=device-width, initial-scale=1\">\n")
	fmt.Fprintf(&b, "<title>%s</title>\n", html.EscapeString(title))
	fmt.Fprintf(&b, "<meta name=\"description\" content=\"%s\">\n", html.EscapeString(description))
	if canonical != "" {
		fmt.Fprintf(&b, "<link rel=\"canonical\" href=\"%s\">\n", html.EscapeString(canonical))
	}
	fmt.Fprintf(&b, "<meta property=\"og:title\" content=\"%s\">\n", html.EscapeString(title))
	fmt.Fprintf(&b, "<meta property=\"og:description\" content=\"%s\">\n", html.EscapeString(description))
	b.WriteString("<meta property=\"og:type\" content=\"article\">\n")
	if canonical != "" {
		fmt.Fprintf(&b, "<meta property=\"og:url\" content=\"%s\">\n", html.EscapeString(canonical))
	}
	fmt.Fprintf(&b, "<style>%s%s</style>\n", manualCSS, pageCSS)
	b.WriteString("</head>\n<body>\n")

	b.WriteString("<nav class=\"sitenav\"><a href=\"/\">Perfuse</a>")
	b.WriteString(" <a href=\"/overview/\">Overview</a>")
	b.WriteString(" <a href=\"/manual/\">Manual</a>")
	b.WriteString(" <a href=\"/reference/\">Reference</a>")
	b.WriteString(" <a href=\"https://github.com/BioDream-LLC/perfuse\">GitHub</a>")
	b.WriteString("</nav>\n")

	b.WriteString("<main>\n")
	b.WriteString(body)
	b.WriteString("</main>\n")

	b.WriteString("<footer class=\"sitefooter\"><p>Perfuse is free software from ")
	b.WriteString("<a href=\"https://biodream.ai\">BioDream LLC</a>, licensed Apache 2.0. ")
	b.WriteString("<a href=\"https://github.com/BioDream-LLC/perfuse\">Source on GitHub</a>.</p></footer>\n")

	b.WriteString("</body>\n</html>\n")

	return b.String()
}
