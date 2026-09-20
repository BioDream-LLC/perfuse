package spec

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
)

// One document covering every channel, for somebody who has to read them all.
//
// The per-channel specification answers "what does this interface do". This answers a different question that nobody could
// ask before: "what does this installation do". An auditor asking which feeds carry patient data, or a new engineer trying
// to understand a site, does not want to open forty pages one at a time.
//
// It is also the thing that makes the existing generator worth having. A document that can only be fetched one channel at a
// time, through an authenticated API, by somebody who already knows the channel's name, is a feature that never leaves the
// building.

// Bundle renders every channel into one Markdown document.
//
// Sorted by name, because Go maps and directory listings both range in an order nobody chose, and a document that reorders
// itself between runs cannot be diffed - which removes the main reason to keep it in version control.
// Bundle takes the channels that loaded and the files that did not, keyed by name with the reason.
//
// Broken files are part of the document rather than the caller's problem, and rendering them here rather than in each caller
// is the point: the API endpoint and the file on disk must say the same thing, and two renderers drift.
func Bundle(channels []*config.Channel, broken map[string]string, generated time.Time) string {
	sorted := make([]*config.Channel, len(channels))
	copy(sorted, channels)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	var b strings.Builder

	b.WriteString("# Interface inventory\n\n")

	// The date, and what it means. A generated document with no date invites somebody to trust a stale copy, and one
	// with a date but no explanation invites them to assume it is a snapshot of design intent rather than of what is
	// running.
	fmt.Fprintf(&b, "Generated %s from the channels this server has loaded. It describes what is running rather\n",
		generated.Format("2 January 2006 at 15:04 MST"))
	b.WriteString("than what was designed, so it cannot be out of date - but it can be incomplete, and each\n")
	b.WriteString("section says where.\n\n")

	if len(sorted) == 0 && len(broken) == 0 {
		b.WriteString("This server has no channels loaded.\n")

		return b.String()
	}

	// Named first, before the inventory, not in an appendix.
	//
	// An inventory that quietly lists thirty-nine of forty interfaces is worse than one that lists thirty-nine and
	// says so: the reader's entire purpose is completeness, so a silent omission is the one error they cannot detect.
	// Putting it at the top means they know before they start trusting the rest.
	if len(broken) > 0 {
		names := make([]string, 0, len(broken))
		for name := range broken {
			names = append(names, name)
		}
		sort.Strings(names)

		b.WriteString("## Files this document could not describe\n\n")
		b.WriteString("These are present and did not load, so they are not running and nothing below covers them.\n\n")
		for _, name := range names {
			fmt.Fprintf(&b, "- **%s**: %s\n", name, cell(broken[name]))
		}
		b.WriteString("\n")
	}

	writeInventoryTable(&b, sorted)
	writeDataSummary(&b, sorted)

	b.WriteString("---\n\n")

	for _, c := range sorted {
		doc := Build(c)

		// Each channel's own document, demoted by one heading level so the whole thing has a single top-level
		// title. Reusing Build rather than writing a second renderer, because two renderers drift and the one
		// that drifts is the one read less often.
		section := doc.Markdown()
		fmt.Fprintf(&b, "%s\n", demote(section))
		b.WriteString("---\n\n")
	}

	return b.String()
}

// writeInventoryTable lists every channel, so the first page answers "what is here".
func writeInventoryTable(b *strings.Builder, channels []*config.Channel) {
	b.WriteString("## Every interface on this server\n\n")
	b.WriteString("| Channel | Format | Running | Receives from | Sends to |\n")
	b.WriteString("| --- | --- | --- | --- | --- |\n")

	for _, c := range channels {
		doc := Build(c)

		targets := make([]string, 0, len(doc.Deliveries))
		for _, d := range doc.Deliveries {
			targets = append(targets, d.Name)
		}
		if len(targets) == 0 {
			targets = []string{"nothing"}
		}

		running := "yes"
		if !c.IsEnabled() {
			// Stated rather than omitted. A disabled channel in an inventory is information: somebody
			// turned it off, and the question of whether that was deliberate outlives whoever did it.
			running = "no — disabled"
		}

		fmt.Fprintf(b, "| %s | %s | %s | %s | %s |\n",
			c.Name, doc.DataType, running, cell(doc.Receives), cell(strings.Join(targets, ", ")))
	}

	b.WriteString("\n")
}

// writeDataSummary answers the questions an auditor actually asks.
//
// Not a list of features. These are the three things somebody assessing an installation wants: what is stored, for how
// long, and which feeds change clinical content on the way through.
func writeDataSummary(b *strings.Builder, channels []*config.Channel) {
	b.WriteString("## What this server does with the data\n\n")

	var transforms, filters, scripted []string

	for _, c := range channels {
		v3 := 0
		if c.HL7v3 != nil {
			v3 = len(c.HL7v3.Transformations)
		}
		if len(c.Transformations) > 0 || v3 > 0 {
			transforms = append(transforms, c.Name)
		}

		v3Filter := ""
		if c.HL7v3 != nil {
			v3Filter = c.HL7v3.FilterExpression()
		}
		if c.Filter != "" || v3Filter != "" {
			filters = append(filters, c.Name)
		}

		if !c.Scripts.Empty() {
			scripted = append(scripted, c.Name)
		}
	}

	// Written as sentences rather than a table, because each needs a caveat that a table cell cannot hold.
	if len(transforms) > 0 {
		fmt.Fprintf(b, "- **%s %s message content**: %s. The steps are listed in each section below.\n",
			plural(len(transforms), "interface", "interfaces"),
			verb(len(transforms), "changes", "change"),
			strings.Join(transforms, ", "))
	} else {
		b.WriteString("- **No interface changes message content.** Every message is forwarded as it arrived.\n")
	}

	if len(filters) > 0 {
		fmt.Fprintf(b, "- **%s "+verb(len(filters), "excludes", "exclude")+" some messages**: %s. A message "+
			"excluded by a filter is still "+
			"acknowledged to the sender, so an excluded message is not a lost one.\n",
			plural(len(filters), "interface", "interfaces"), strings.Join(filters, ", "))
	}

	if len(scripted) > 0 {
		// The important caveat in the whole document, and the reason this is a sentence.
		fmt.Fprintf(b, "- **%s "+verb(len(scripted), "runs", "run")+" JavaScript**: %s. What that does cannot "+
			"be established by "+
			"reading configuration, including this document — the script has to be read.\n",
			plural(len(scripted), "interface", "interfaces"), strings.Join(scripted, ", "))
	}

	b.WriteString("\n")
}

// verb picks a verb form to agree with a count.
//
// Its own helper because the count and the noun were already handled and the verb was not, so the document read "1
// interface change message content". A generated document with a grammar mistake in its summary line invites the reader to
// treat the whole thing as machine output nobody checked - which is precisely the opposite of what it is for.
func verb(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}

	return plural
}

// cell makes a value safe inside a Markdown table.
//
// A pipe in a channel name or an address would split the row and shift every column after it, which produces a table that
// renders and lies about which system talks to which. Newlines become spaces for the same reason.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", "\\|")
	s = strings.ReplaceAll(s, "\n", " ")

	return strings.TrimSpace(s)
}

// demote pushes every heading down one level.
//
// So a per-channel document, which starts at a single #, nests inside this one. Only leading hashes on their own line are
// touched: a # inside a code block or in the middle of a sentence is left alone, because a channel name or a filter
// expression may legitimately contain one.
func demote(md string) string {
	lines := strings.Split(md, "\n")
	inCode := false

	for i, line := range lines {
		if strings.HasPrefix(line, "```") {
			inCode = !inCode

			continue
		}
		if inCode {
			continue
		}
		if strings.HasPrefix(line, "#") {
			lines[i] = "#" + line
		}
	}

	return strings.Join(lines, "\n")
}
