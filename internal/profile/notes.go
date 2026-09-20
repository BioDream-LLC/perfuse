package profile

import "fmt"

// Saying in words what the numbers imply.
//
// A table of fill rates is accurate and easy to skim past. The three or four facts that will
// actually change what somebody builds deserve a sentence, because the point of profiling a feed
// is not to produce a document - it is to stop an integrator being surprised in production.
//
// Each note is something a specification would not have told them.

func notes(rep *Report) []string {
	var out []string

	if rep.Messages == 0 {
		return []string{"no messages could be read, so there is nothing to say about this feed"}
	}

	if rep.Unreadable > 0 {
		// Stated first, because it undermines everything below it.
		out = append(out, fmt.Sprintf(
			"%d message(s) could not be parsed at all and are not included in any figure below; "+
				"a feed with unreadable messages in it is worth investigating before drawing "+
				"conclusions from the rest", rep.Unreadable))
	}

	// Non-standard segments. These are the ones a vendor specification never mentions and an
	// integration always has to deal with.
	var custom []string
	for _, s := range rep.Segments {
		if !s.Standard {
			custom = append(custom, fmt.Sprintf("%s (in %s of messages)", s.ID, pct(s.Rate)))
		}
	}
	if len(custom) > 0 {
		out = append(out, fmt.Sprintf(
			"this feed carries segments the standard does not define: %s — these usually hold "+
				"the site-specific data an interface is being built for, and no published "+
				"specification will describe them", join(custom)))
	}

	// More than one trigger event. The commonest wrong assumption about a feed.
	if len(rep.Types) > 1 {
		var parts []string
		for _, t := range rep.Types {
			parts = append(parts, fmt.Sprintf("%s %s", t.Type, pct(t.Rate)))
		}
		out = append(out, fmt.Sprintf(
			"the feed carries %d different message types: %s — a filter written for one of them "+
				"silently ignores the rest", len(rep.Types), join(parts)))
	}

	for _, s := range rep.Segments {
		// A segment that is usually but not always present. Code written against the common
		// case fails on the remainder, and the remainder is where the incidents are.
		if s.Rate > 0.05 && s.Rate < 0.95 {
			out = append(out, fmt.Sprintf(
				"%s appears in only %s of messages, so anything reading it must cope with its "+
					"absence", s.ID, pct(s.Rate)))
		}

		if s.MaxPerMessage > 1 {
			out = append(out, fmt.Sprintf(
				"%s occurs up to %d times in one message; code that reads it once takes the "+
					"first and ignores the others", s.ID, s.MaxPerMessage))
		}

		for _, f := range s.Fields {
			// A field populated most of the time but not always. This is the single most
			// common cause of a three-in-the-morning call: the specification said required,
			// the sender mostly sends it, and one message a week does not.
			if f.FillRate > 0.5 && f.FillRate < 0.999 {
				out = append(out, fmt.Sprintf(
					"%s%s is populated in %s of the messages that carry %s, not all of them",
					f.Path, named(f.Name), pct(f.FillRate), s.ID))
			}

			if f.MaxRepeats > 1 {
				out = append(out, fmt.Sprintf(
					"%s%s repeats, up to %d times; a mapping that treats it as one value takes "+
						"the first", f.Path, named(f.Name), f.MaxRepeats))
			}

			// A field the specification calls numeric that sometimes is not. A receiver
			// parsing it as a number fails on the exception.
			if f.Shape == ShapeMixed && f.Distinct > 1 {
				out = append(out, fmt.Sprintf(
					"%s%s holds values of inconsistent shape, so anything parsing it strictly "+
						"will fail on some messages", f.Path, named(f.Name)))
			}

			// Codes outside the table. The sender is using local values, and anything
			// downstream mapping strictly will reject them.
			var unknown []string
			for _, c := range f.Codes {
				if !c.Known {
					unknown = append(unknown, c.Code)
				}
			}
			if len(unknown) > 0 && f.Table != "" {
				out = append(out, fmt.Sprintf(
					"%s%s uses %s, which HL7 table %s does not define — a strict mapping "+
						"downstream will reject %s",
					f.Path, named(f.Name), join(quoteAll(unknown)), f.Table,
					plural(len(unknown), "that value", "those values")))
			}
		}
	}

	// Bounded. A feed with two hundred observations produces a page nobody reads, and the
	// tables carry everything anyway.
	const maxNotes = 25
	if len(out) > maxNotes {
		extra := len(out) - maxNotes
		out = out[:maxNotes]
		out = append(out, fmt.Sprintf(
			"and %d further observation(s), which the tables above carry in full", extra))
	}

	return out
}

func pct(rate float64) string {
	switch {
	case rate >= 0.9995:
		return "100%"
	case rate < 0.001 && rate > 0:
		// Anything this rare rounds to zero, which reads as "never" and is the opposite of
		// what it means: a case occurring once in a thousand messages is the one that breaks
		// an interface six weeks after go-live.
		return "under 0.1%"
	default:
		return fmt.Sprintf("%.1f%%", rate*100)
	}
}

func named(name string) string {
	if name == "" {
		return ""
	}
	return " (" + name + ")"
}

func join(parts []string) string {
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	case 2:
		return parts[0] + " and " + parts[1]
	}
	out := ""
	for i, p := range parts[:len(parts)-1] {
		if i > 0 {
			out += ", "
		}
		out += p
	}
	return out + " and " + parts[len(parts)-1]
}

func quoteAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, s := range in {
		out = append(out, fmt.Sprintf("%q", s))
	}
	return out
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
