package manual

import (
	"fmt"
	"sort"
	"strings"
)

// Sections of the reference, by the role a component plays.
//
// Grouped by role rather than listed alphabetically as one flat run of 44 structs. A reader arrives knowing they want
// to collect files from a share, not knowing that the type is called SMBSource, so an alphabetical list is only usable
// by somebody who already knows the answer.
const (
	roleSource      = "source"
	roleDestination = "destination"
	roleFormat      = "format"
	roleBehaviour   = "behaviour"
	roleChannel     = "channel"
)

// roleOf decides which chapter a component belongs in.
//
// By name suffix, with the handful of exceptions named explicitly. Deriving it means a new transport lands in the right
// chapter without anybody remembering to add it to a list, and the explicit exceptions are visible rather than being
// silently mis-filed.
func roleOf(c Component) string {
	switch c.Name {
	case "Channel":
		return roleChannel
	case "Delimited", "HL7v3Options", "X12Options", "Attachments", "TCPFraming":
		return roleFormat
	case "Ack", "QueueConfig", "Retry", "Shadow", "Scripts", "FilePoll", "ContractRef":
		return roleBehaviour
	}
	switch {
	case strings.HasSuffix(c.Name, "Source"):
		return roleSource
	case strings.HasSuffix(c.Name, "Destination"), strings.HasSuffix(c.Name, "Dest"):
		return roleDestination
	default:
		return roleBehaviour
	}
}

// Build assembles the manual from the extracted source and the written chapters.
//
// The chapter order is written out here rather than derived from filenames. A reference manual's order is an argument -
// what a reader needs to understand before the next thing makes sense - and encoding that in a numeric filename prefix
// hides it. Anybody reordering the manual should have to edit this list and see what they are changing.
func Build(version string, prose map[string]string, components []Component, packages []PackageDoc) (*Doc, error) {
	d := &Doc{
		Title:    "Perfuse Reference Manual",
		Subtitle: "A healthcare integration engine: channels, formats, delivery and operation",
		Version:  version,

		// Set here rather than by the caller so the published manual and the copy in every
		// release archive describe themselves identically. A description assembled per
		// caller drifts, and the one nobody updates is the one a search engine shows.
		Description: "Reference manual for Perfuse, an open-source healthcare integration engine for " +
			"HL7 v2, FHIR, DICOM, X12 and CDA. Channel configuration, sources and destinations, " +
			"transformation, the durable queue, delivery, security and operation.",
		Canonical: "https://biodream-llc.github.io/perfuse/manual/",
	}

	byRole := map[string][]Component{}
	for _, c := range components {
		// A struct with no configurable keys is not a configuration block. Including it would list types a reader
		// cannot set anything on, which reads as an option whose documentation is missing.
		if len(c.Entries) == 0 {
			continue
		}
		r := roleOf(c)
		byRole[r] = append(byRole[r], c)
	}

	// The written chapters, in reading order, followed by the generated reference.
	written := []string{
		"introduction",
		"getting-started",
		"web-interface",
		"channels",
		"message-flow",
		"transformation",
		"filtering",
		"formats",
		"acknowledgements",
		"delivery",
		"alerting",
		"message-store",
		"debugging",
		"testing",
		"migration",
		"security",
		"operations",
		"command-line",
	}

	for _, name := range written {
		src, ok := prose[name]
		if !ok {
			// Named but absent is a mistake worth failing on. A missing chapter that silently does not appear leaves a
			// manual with a gap in its numbering and no indication why.
			return nil, fmt.Errorf("chapter %q is listed in the manual order but has no source file in docs/manual/", name)
		}
		ch, err := parseProseChapter(src)
		if err != nil {
			return nil, fmt.Errorf("chapter %q: %w", name, err)
		}
		d.Chapters = append(d.Chapters, *ch)
	}

	// Generated reference chapters.
	d.Chapters = append(d.Chapters,
		referenceChapter("Channel Reference",
			"Every key that can appear at the top level of a channel file. These are extracted from the source, so this "+
				"chapter cannot describe an option that does not exist or omit one that does.",
			byRole[roleChannel]),

		referenceChapter("Source Reference",
			"Every way a channel can receive messages, and every key each one accepts. A channel has exactly one source.",
			byRole[roleSource]),

		referenceChapter("Destination Reference",
			"Every way a channel can send messages, and every key each one accepts. A channel may have any number of "+
				"destinations, and they are independent: one failing does not stop the others.",
			byRole[roleDestination]),

		referenceChapter("Format Reference",
			"Options governing how messages are parsed and written, for the formats that need more than a name.",
			byRole[roleFormat]),

		referenceChapter("Behaviour Reference",
			"Acknowledgement, queueing, retry, polling, scripting and shadow settings. These apply regardless of which "+
				"transport a channel uses.",
			byRole[roleBehaviour]),
	)

	if len(packages) > 0 {
		d.Chapters = append(d.Chapters, componentChapter(packages))
	}

	if idx := buildIndex(components); idx != nil {
		d.Chapters = append(d.Chapters, *idx)
	}

	return d, nil
}

// referenceChapter renders one group of components as a chapter.
func referenceChapter(title, intro string, components []Component) Chapter {
	sort.Slice(components, func(i, j int) bool { return components[i].Name < components[j].Name })

	ch := Chapter{Title: title, Intro: intro}

	for _, c := range components {
		s := Section{Title: humanName(c.Name)}

		var b strings.Builder
		if c.Summary != "" {
			b.WriteString(c.Summary)
			b.WriteString("\n\n")
		}
		if c.Detail != "" {
			b.WriteString(c.Detail)
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "Defined as `%s` in `%s`.\n\n", c.Name, "internal/config")

		// A table of every key first, so somebody checking whether an option exists gets an answer without reading
		// prose. The explanations follow as their own subsections.
		b.WriteString("| Key | Type | Required | Default |\n|---|---|---|---|\n")
		for _, e := range c.Entries {
			req := "yes"
			if e.Optional {
				req = "no"
			}
			def := e.Default
			if def == "" {
				def = "—"
			}
			fmt.Fprintf(&b, "| `%s` | %s | %s | %s |\n", e.Name, e.Type, req, def)
		}
		s.Body = b.String()

		for _, e := range c.Entries {
			// Only keys whose explanation says more than the table already does get a subsection. A subsection
			// repeating one line of a table it sits directly beneath is padding, and in a document this long padding is
			// what stops it being read.
			if e.Detail == "" {
				continue
			}
			s.Subsections = append(s.Subsections, Subsection{
				Title: e.Name,
				Body:  e.Summary + "\n\n" + e.Detail,
			})
		}

		ch.Sections = append(ch.Sections, s)
	}

	return ch
}

// componentChapter describes each internal component from its package comment.
//
// These are the design explanations - why STOMP rather than OpenWire, why a median rather than a mean. Included because
// the question somebody has when deciding whether to run this on a clinical feed is not what the options are but
// whether the person who wrote it had thought about the failure they are worried about.
func componentChapter(packages []PackageDoc) Chapter {
	// By path for the same reason CollectPackageDocs does: the name is not unique.
	sort.Slice(packages, func(i, j int) bool { return packages[i].Path < packages[j].Path })

	ch := Chapter{
		Title: "How Each Component Works",
		Intro: "Perfuse is built from small packages, each with one job. This chapter is each one's own account of what " +
			"it does and why it is built the way it is, taken from the source. Where a decision has a reason that is " +
			"not obvious, the reason is here rather than in a commit message nobody will find.",
	}

	for _, p := range packages {
		body := p.Summary
		if p.Detail != "" {
			body += "\n\n" + p.Detail
		}
		body += fmt.Sprintf("\n\nSource: `%s`\n", p.Path)
		// The path is in the title where a name repeats, so two sections called hl7 can be told apart in the contents.
		title := p.Name
		ch.Sections = append(ch.Sections, Section{Title: title, Body: body})
	}

	return ch
}

// buildIndex lists every key alphabetically with the blocks it appears in.
//
// The counterpart to grouping by role. Grouping helps somebody who knows what they want to do; the index helps somebody
// holding a configuration file they did not write, looking at a key they do not recognise. Both readers are common and
// neither is served by the other's ordering.
func buildIndex(components []Component) *Chapter {
	where := map[string][]string{}
	for _, c := range components {
		if len(c.Entries) == 0 {
			continue
		}
		for _, e := range c.Entries {
			where[e.Name] = append(where[e.Name], humanName(c.Name))
		}
	}
	if len(where) == 0 {
		return nil
	}

	keys := make([]string, 0, len(where))
	for k := range where {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteString("| Key | Appears in |\n|---|---|\n")
	for _, k := range keys {
		blocks := where[k]
		sort.Strings(blocks)
		blocks = dedupe(blocks)
		fmt.Fprintf(&b, "| `%s` | %s |\n", k, strings.Join(blocks, ", "))
	}

	return &Chapter{
		Title: "Index of Configuration Keys",
		Intro: "Every key in alphabetical order, with the blocks it can appear in. A key that appears in several blocks " +
			"means the same thing in each one; where it does not, the blocks are documented separately and the " +
			"difference is stated there.",
		Sections: []Section{{Title: "All keys", Body: b.String()}},
	}
}

func dedupe(in []string) []string {
	out := in[:0:0]
	for i, s := range in {
		if i == 0 || s != in[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// humanName turns a Go type name into something readable without losing it.
//
// The Go name is kept in parentheses rather than dropped. A reader who has grepped the source for SMBSource needs to
// find it here, and a reader who has not needs to know what it is called.
func humanName(name string) string {
	var words []string
	var cur []rune
	runes := []rune(name)
	for i, r := range runes {
		isUpper := r >= 'A' && r <= 'Z'
		nextLower := i+1 < len(runes) && runes[i+1] >= 'a' && runes[i+1] <= 'z'
		if isUpper && len(cur) > 0 && (nextLower || !(runes[i-1] >= 'A' && runes[i-1] <= 'Z')) {
			words = append(words, string(cur))
			cur = nil
		}
		cur = append(cur, r)
	}
	if len(cur) > 0 {
		words = append(words, string(cur))
	}
	return strings.Join(words, " ")
}

// parseProseChapter reads a written chapter file.
//
// The first heading is the chapter title, ## opens a section and ### a subsection. Anything before the first ## is the
// chapter introduction.
func parseProseChapter(src string) (*Chapter, error) {
	lines := strings.Split(src, "\n")
	ch := &Chapter{}

	var introLines []string
	var curSection *Section
	var curSub *Subsection
	var buf []string

	flush := func() {
		text := strings.TrimSpace(strings.Join(buf, "\n"))
		buf = nil
		switch {
		case curSub != nil:
			curSub.Body = text
		case curSection != nil:
			curSection.Body = text
		default:
			introLines = append(introLines, text)
		}
	}

	closeSub := func() {
		if curSub != nil && curSection != nil {
			curSection.Subsections = append(curSection.Subsections, *curSub)
			curSub = nil
		}
	}
	closeSection := func() {
		closeSub()
		if curSection != nil {
			ch.Sections = append(ch.Sections, *curSection)
			curSection = nil
		}
	}

	inFence := false
	for _, l := range lines {
		// Headings inside a code fence are content, not structure. Without this a yaml comment starting with ## would
		// silently split a chapter in two.
		if strings.HasPrefix(l, "```") {
			inFence = !inFence
			buf = append(buf, l)
			continue
		}
		if inFence {
			buf = append(buf, l)
			continue
		}

		switch {
		case strings.HasPrefix(l, "### "):
			flush()
			closeSub()
			if curSection == nil {
				return nil, fmt.Errorf("a subsection appears before any section: %q", l)
			}
			curSub = &Subsection{Title: strings.TrimSpace(strings.TrimPrefix(l, "### "))}

		case strings.HasPrefix(l, "## "):
			flush()
			closeSection()
			curSection = &Section{Title: strings.TrimSpace(strings.TrimPrefix(l, "## "))}

		case strings.HasPrefix(l, "# "):
			flush()
			if ch.Title != "" {
				return nil, fmt.Errorf("a chapter file has two titles; the second is %q", l)
			}
			ch.Title = strings.TrimSpace(strings.TrimPrefix(l, "# "))

		default:
			buf = append(buf, l)
		}
	}
	flush()
	closeSection()

	if ch.Title == "" {
		return nil, fmt.Errorf("a chapter file has no title line starting with a single #")
	}
	ch.Intro = strings.TrimSpace(strings.Join(introLines, "\n\n"))

	return ch, nil
}

// BuildWrittenOnly assembles just the written chapters.
//
// For the copy served by the running binary, which has no Go source tree to extract the reference chapters from. The
// alternative - shipping the generated chapters as embedded data - was rejected because it would put a second copy of
// every configuration key in the binary, and two copies of the same facts eventually disagree.
//
// The absence is stated in the document rather than left for a reader to notice. A manual with five chapters silently
// missing reads as one whose author forgot them.
func BuildWrittenOnly(version string, prose map[string]string) (*Doc, error) {
	d, err := Build(version, prose, nil, nil)
	if err != nil {
		return nil, err
	}

	d.Chapters = append(d.Chapters, Chapter{
		Title: "The Configuration Reference",
		Intro: "The chapters listing every configuration key, every source and destination type and every component are " +
			"generated from the source, and are not in this copy.\n\n" +
			"They are in the full manual, built with `make docs`, which produces `docs/manual/perfuse-manual.html` and " +
			"`docs/manual/perfuse-manual.pdf`. That version has a chapter per source and destination, a table of every " +
			"key with its type and default, and an alphabetical index of all of them.\n\n" +
			"They are absent here rather than approximated. Shipping a second copy of every key inside the binary would " +
			"mean two records of the same facts, and two records of the same facts eventually disagree — at which point " +
			"neither can be trusted.\n\n" +
			"> Everything a channel accepts is also visible in the channel builder, which is generated from the same " +
			"definitions and is checked by a test that fails when a setting exists that the form cannot express.",
	})

	return d, nil
}
