package spec

import (
	"fmt"
	"strings"
)

// Rendering the specification as a document somebody can send.
//
// Markdown rather than a PDF or Word file. It is readable as it stands, it survives being pasted
// into a ticket or a wiki, it diffs cleanly in a pull request, and producing it needs no dependency.
// A PDF would look more like the document it replaces and would be worse at every one of those.

// Markdown renders the specification.
func (d *Document) Markdown() string {
	var b strings.Builder

	fmt.Fprintf(&b, "# Interface specification: %s\n\n", d.Channel)

	if d.Description != "" {
		fmt.Fprintf(&b, "%s\n\n", d.Description)
	}

	// A plain summary before the tables.
	//
	// The tables are the reference and this is the part somebody reads. A vendor opening this document wants to know what the
	// interface does before being shown thirty field paths, and the person opening it during an incident may not read past here at
	// all - which is fine, as long as what they read is true and complete enough to act on.
	//
	// Derived from the same document as everything below it, so the summary and the tables cannot say different things.
	if len(d.Narration) > 0 {
		b.WriteString("## In short\n\n")
		for _, sentence := range d.Narration {
			fmt.Fprintf(&b, "%s\n", sentence)
		}
		b.WriteString("\n")
	}

	// The caveats go near the top, not in an appendix.
	//
	// An appendix is where a limitation goes to be ignored. If this document cannot describe
	// what a channel does, the reader needs to know that before they read the parts it can
	// describe, not after they have finished believing them.
	if len(d.Caveats) > 0 {
		b.WriteString("## Before you rely on this\n\n")
		b.WriteString("This document is generated from the channel that is actually running, so it\n")
		b.WriteString("cannot be out of date. It can be incomplete, and where it is, it says so.\n\n")
		for _, c := range d.Caveats {
			fmt.Fprintf(&b, "- %s\n", c)
		}
		b.WriteString("\n")
	}

	b.WriteString("## The contract with the sender\n\n")
	fmt.Fprintf(&b, "- **Message format**: %s\n", d.DataType)
	fmt.Fprintf(&b, "- **How messages arrive**: %s\n", d.Receives)
	fmt.Fprintf(&b, "- **What the sender is told**: %s\n", d.Acknowledges)
	if d.Accepts != "" {
		fmt.Fprintf(&b, "- **Which messages are accepted**: `%s`\n", d.Accepts)
		b.WriteString("  Messages that do not match are still acknowledged as accepted; they are\n")
		b.WriteString("  simply not forwarded. The sender has done nothing wrong.\n")
	} else {
		b.WriteString("- **Which messages are accepted**: all of them\n")
	}
	b.WriteString("\n")

	if len(d.Reads) > 0 {
		b.WriteString("## Fields this interface depends on\n\n")
		b.WriteString("These must be populated for the interface to behave as described. This is the\n")
		b.WriteString("list for whoever configures the sending system.\n\n")
		writeFieldTable(&b, d.Reads)
	}

	if len(d.Writes) > 0 {
		b.WriteString("## Fields this interface changes\n\n")
		b.WriteString("These will differ from what the sender transmitted. This is the list for\n")
		b.WriteString("whoever consumes the output.\n\n")
		writeFieldTable(&b, d.Writes)
	}

	if len(d.Mappings) > 0 {
		b.WriteString("## Code translations\n\n")
		for _, m := range d.Mappings {
			title := m.Path
			if m.Name != "" {
				title += " (" + m.Name + ")"
			}
			fmt.Fprintf(&b, "### %s\n\n", title)
			b.WriteString("| sender sends | becomes |\n|---|---|\n")
			for i := range m.From {
				fmt.Fprintf(&b, "| `%s` | `%s` |\n", escapePipe(m.From[i]), escapePipe(m.To[i]))
			}
			fmt.Fprintf(&b, "\n%s\n\n", capitalise(m.Unmatched))
		}
	}

	if len(d.Steps) > 0 {
		b.WriteString("## What happens to each message, in order\n\n")
		for i, s := range d.Steps {
			fmt.Fprintf(&b, "%d. %s\n", i+1, s)
		}
		b.WriteString("\n")
	}

	if len(d.Deliveries) > 0 {
		b.WriteString("## Where messages go\n\n")
		for _, dl := range d.Deliveries {
			fmt.Fprintf(&b, "### %s\n\n", dl.Name)
			fmt.Fprintf(&b, "- %s\n", capitalise(dl.How))
			if dl.When != "" {
				fmt.Fprintf(&b, "- Only messages matching `%s`\n", dl.When)
			} else {
				b.WriteString("- Every message the channel accepted\n")
			}
			if dl.Durable {
				b.WriteString("- Queued to disk and retried, so a message is not lost if this\n")
				b.WriteString("  receiver is unavailable\n")
			} else {
				b.WriteString("- **Not queued**: a message that cannot be delivered when it arrives\n")
				b.WriteString("  is recorded as failed and not retried\n")
			}
			b.WriteString("\n")
		}
	}

	b.WriteString("---\n\n")
	b.WriteString("Generated from the running channel definition. Regenerate it rather than editing\n")
	b.WriteString("it: an edited copy is a document that can be wrong, which is the problem this\n")
	b.WriteString("replaces.\n")

	return b.String()
}

func writeFieldTable(b *strings.Builder, fields []FieldUse) {
	b.WriteString("| field | what it is | notes | how this interface uses it |\n")
	b.WriteString("|---|---|---|---|\n")

	for _, f := range fields {
		name := f.Name
		if name == "" {
			// A field the standard does not define. Said plainly rather than left blank,
			// because a blank cell reads as an omission and this is information: it means the
			// interface depends on something locally defined.
			name = "_not defined by the standard_"
		}

		var notes []string
		if f.Table != "" {
			notes = append(notes, "HL7 table "+f.Table)
		}
		if f.Repeats {
			// Worth stating whether or not this sender uses it, because a receiver has to
			// cope with it if the standard allows it.
			notes = append(notes, "may repeat")
		}

		fmt.Fprintf(b, "| `%s` | %s | %s | %s |\n",
			f.Path, escapePipe(name), escapePipe(strings.Join(notes, ", ")), escapePipe(strings.Join(f.Why, "; ")))
	}
	b.WriteString("\n")
}

func capitalise(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] = r[0] - 'a' + 'A'
	}
	return string(r)
}

// escapePipe replaces pipe characters so they do not split a Markdown table cell.
//
// A pipe inside backticks is technically safe in some renderers but not all. GitHub's Markdown
// parser, for instance, splits on unescaped pipes regardless of backtick context. Since a
// specification gets pasted into wikis, tickets, and rendered by various tools, the safe approach
// is to escape unconditionally.
func escapePipe(s string) string {
	return strings.ReplaceAll(s, "|", "\\|")
}
