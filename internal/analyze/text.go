package analyze

import (
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/mirth"
)

// Explain writes a human-readable account of a channel and its findings.
//
// The order is deliberate: what the channel does first, then how it stores
// messages, then what is wrong with it. Somebody who has inherited a hundred
// channels needs the description more than they need the warnings.
func Explain(w io.Writer, r *Report) error {
	c := r.Channel
	b := &strings.Builder{}

	writeHeader(b, c)
	writeFlow(b, c)
	writeStorage(b, c)
	writeFindings(b, r)

	_, err := io.WriteString(w, b.String())
	return err
}

func writeHeader(b *strings.Builder, c *mirth.Channel) {
	status := "disabled"
	if c.Enabled {
		status = "enabled"
	}
	fmt.Fprintf(b, "CHANNEL  %s\n", nameOr(c.Name, "(unnamed)"))

	meta := []string{status}
	if c.MirthVersion != "" {
		meta = append(meta, "Mirth "+c.MirthVersion)
	}
	if c.Revision > 0 {
		meta = append(meta, fmt.Sprintf("revision %d", c.Revision))
	}
	fmt.Fprintf(b, "         %s\n", strings.Join(meta, "  ·  "))

	if c.Description != "" {
		fmt.Fprintf(b, "\n%s\n", indentWrap(c.Description, "         ", 72))
	}
}

func writeFlow(b *strings.Builder, c *mirth.Channel) {
	b.WriteString("\nFLOW\n")

	src := c.Source
	fmt.Fprintf(b, "  in   %s\n", describeTransport(src))
	if dt := datatypeArrow(src); dt != "" {
		fmt.Fprintf(b, "       %s\n", dt)
	}
	if s := describeFilter(src.Filter); s != "" {
		fmt.Fprintf(b, "       filter     %s\n", s)
	}
	if s := describeSteps(src.Transformer.Steps); s != "" {
		fmt.Fprintf(b, "       transform  %s\n", s)
	}

	if len(c.Destinations) == 0 {
		fmt.Fprintf(b, "  out  none\n")
		return
	}
	for _, d := range c.Destinations {
		flag := ""
		if !d.Enabled {
			flag = "  [disabled]"
		}
		fmt.Fprintf(b, "  out  %d  %-18s %s%s\n",
			d.MetaDataID, nameOr(d.Name, "unnamed"), describeTransport(d), flag)
		if dt := datatypeArrow(d); dt != "" {
			fmt.Fprintf(b, "          %s\n", dt)
		}
		if s := describeFilter(d.Filter); s != "" {
			fmt.Fprintf(b, "          filter     %s\n", s)
		}
		if s := describeSteps(d.Transformer.Steps); s != "" {
			fmt.Fprintf(b, "          transform  %s\n", s)
		}
	}
}

func writeStorage(b *strings.Builder, c *mirth.Channel) {
	p := c.Properties
	if p.MessageStorageMode == "" && p.PruneMetaDataDays == 0 && p.PruneContentDays == 0 {
		return
	}

	parts := []string{}
	if p.MessageStorageMode != "" {
		parts = append(parts, "mode "+p.MessageStorageMode)
	}
	switch {
	case p.PruneMetaDataDays == 0 && p.PruneContentDays == 0:
		parts = append(parts, "no pruning")
	default:
		parts = append(parts, fmt.Sprintf("prune metadata %dd, content %dd",
			p.PruneMetaDataDays, p.PruneContentDays))
	}
	if p.EncryptData {
		parts = append(parts, "encrypted")
	}
	if p.StoreAttachments {
		parts = append(parts, "attachments stored")
	}

	fmt.Fprintf(b, "\nSTORAGE\n  %s\n", strings.Join(parts, "  ·  "))
}

func writeFindings(b *strings.Builder, r *Report) {
	blockers, warnings, notes := r.Counts()
	if len(r.Findings) == 0 {
		b.WriteString("\nFINDINGS\n  Nothing to report. This channel translates mechanically.\n")
		return
	}

	fmt.Fprintf(b, "\nFINDINGS  %d blocker(s), %d warning(s), %d note(s)\n",
		blockers, warnings, notes)

	for _, f := range r.Findings {
		fmt.Fprintf(b, "\n  %-7s  %s\n", f.Severity, f.Where)
		fmt.Fprintf(b, "           %s\n", f.What)
		if f.Why != "" {
			fmt.Fprintf(b, "%s\n", indentWrap(f.Why, "           ", 68))
		}
	}

	b.WriteString("\n")
	if r.Translatable() {
		b.WriteString("  No blockers. Perfuse can translate this channel; check the warnings.\n")
	} else {
		fmt.Fprintf(b, "  %d blocker(s) need a decision before this channel can be translated.\n", blockers)
	}
}

func datatypeArrow(c mirth.Connector) string {
	in, out := c.Transformer.InboundDataType, c.Transformer.OutboundDataType
	switch {
	case in == "" && out == "":
		return ""
	case in == out:
		return in
	default:
		return fmt.Sprintf("%s → %s", nameOr(in, "?"), nameOr(out, "?"))
	}
}

func describeFilter(f mirth.Filter) string {
	if len(f.Rules) == 0 {
		return ""
	}
	names := make([]string, 0, len(f.Rules))
	for _, r := range f.Rules {
		names = append(names, nameOr(r.Name, string(r.Kind)))
	}
	return fmt.Sprintf("%s: %s", plural(len(f.Rules), "rule"), strings.Join(names, ", "))
}

func describeSteps(steps []mirth.Step) string {
	if len(steps) == 0 {
		return ""
	}
	counts := map[mirth.StepKind]int{}
	total := 0
	var walk func([]mirth.Step)
	walk = func(ss []mirth.Step) {
		for _, s := range ss {
			counts[s.Kind]++
			total++
			walk(s.Children)
		}
	}
	walk(steps)

	kinds := make([]string, 0, len(counts))
	for k := range counts {
		kinds = append(kinds, string(k))
	}
	sort.Strings(kinds)

	parts := make([]string, 0, len(kinds))
	for _, k := range kinds {
		parts = append(parts, fmt.Sprintf("%d %s", counts[mirth.StepKind(k)], strings.ToLower(k)))
	}
	return fmt.Sprintf("%s: %s", plural(total, "step"), strings.Join(parts, ", "))
}

func plural(n int, word string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", word)
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// indentWrap wraps text to width and prefixes every line with indent.
func indentWrap(s, indent string, width int) string {
	words := strings.Fields(s)
	if len(words) == 0 {
		return ""
	}

	var lines []string
	line := words[0]
	for _, w := range words[1:] {
		if len(line)+1+len(w) > width {
			lines = append(lines, line)
			line = w
			continue
		}
		line += " " + w
	}
	lines = append(lines, line)

	for i := range lines {
		lines[i] = indent + lines[i]
	}
	return strings.Join(lines, "\n")
}
