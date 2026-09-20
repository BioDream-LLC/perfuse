package spec

import (
	"fmt"
	"strings"
)

// Narration: what this channel does, in sentences.
//
// The specification document is the right artefact to hand a vendor and the wrong one to read when somebody asks "what does this
// channel actually do?" at four in the morning. Tables of field paths answer a different question from the one being asked, and the
// person asking is usually looking at an incident.
//
// Derived from the Document rather than from the channel, deliberately. Two descriptions of the same channel built from the same source
// can still drift when they read it independently, and the failure mode is the worse of the two: a summary that contradicts the
// specification beneath it, with nothing to say which is right.

// Narrate describes a channel in plain sentences, in the order somebody would ask.
//
// Returns sentences rather than a paragraph so the interface can lay them out, and so a test can assert one fact at a time instead of
// matching against prose.
func (d *Document) Narrate() []string {
	var out []string

	// What this channel is, then what arrives.
	//
	// Two sentences rather than one, because Receives is already a complete sentence - "the sender connects to 127.0.0.1:6661 and
	// sends HL7 over MLLP" - and embedding it after "it receives" produced "It receives the sender connects to...". Every narration
	// opened with broken English while all the tests passed, because they checked for facts and not for grammar.
	// Phrased around the name rather than starting with it.
	//
	// Channel names are lower case by convention, so "admissions: the main ADT feed" opens every narration with a lower case letter -
	// which reads as a fragment. Capitalising the name instead would print a name the channel does not have, and somebody searching
	// the configuration for it would not find it.
	if d.Description != "" {
		out = append(out, fmt.Sprintf("The %s channel is %s.", d.Channel, lowerFirst(strings.TrimRight(d.Description, "."))))
	} else {
		out = append(out, fmt.Sprintf("The %s channel handles %s.", d.Channel, d.DataType))
	}

	if d.Receives != "" {
		out = append(out, upperFirst(strings.TrimRight(d.Receives, "."))+".")
	}

	// What it accepts. An empty filter is stated rather than skipped, because "everything" is a decision somebody made and is the
	// most common cause of surprise volume downstream.
	if d.Accepts != "" {
		out = append(out, fmt.Sprintf("It only handles messages where %s; anything else is filtered out and acknowledged.", strings.TrimRight(lowerFirst(d.Accepts), ".")))
	} else {
		out = append(out, "It accepts every message that arrives, without filtering.")
	}

	// What it changes. The count first, because "it rewrites eleven fields" is the fact that decides whether somebody keeps reading.
	switch len(d.Writes) {
	case 0:
		out = append(out, "It does not change the message.")
	case 1:
		out = append(out, fmt.Sprintf("It changes one field, %s.", d.Writes[0].Path))
	default:
		out = append(out, fmt.Sprintf("It changes %d fields, including %s.", len(d.Writes), joinPaths(d.Writes, 3)))
	}

	// Mappings, and what happens to a code the table does not hold. That last part is the sentence people want and the one a table
	// of rows does not give them: whether an unrecognised code is translated, passed through, or stops the message.
	for _, m := range d.Mappings {
		out = append(out, narrateMapping(m))
	}

	// Where it goes.
	switch len(d.Deliveries) {
	case 0:
		out = append(out, "It has no destinations, so nothing is sent anywhere.")
	case 1:
		out = append(out, "It sends to one destination. "+narrateDelivery(d.Deliveries[0])+".")
	default:
		// One sentence each, not a joined list.
		//
		// A destination carries qualifiers - queued if unavailable, only when a filter matches - and inside a list those attach
		// to whichever noun the reader assumes, which is usually the last one or all of them. "It sends to two destinations:
		// archive and downstream, queued if it is unavailable" says something false about archive.
		out = append(out, fmt.Sprintf("It sends to %d destinations.", len(d.Deliveries)))
		for _, del := range d.Deliveries {
			out = append(out, narrateDelivery(del)+".")
		}
	}

	// How it answers the sender, which is the sender's whole view of this channel.
	if d.Acknowledges != "" {
		out = append(out, fmt.Sprintf("The sender is told %s.", lowerFirst(strings.TrimRight(d.Acknowledges, "."))))
	}

	// And what this cannot tell you. Last, and never omitted.
	//
	// A narration that reads as complete while a script is doing something it did not mention is worse than no narration, because
	// somebody will act on it. The specification already refuses to describe a script's behaviour and this must not be the cheerful
	// version that forgets to.
	for _, c := range d.Caveats {
		out = append(out, strings.TrimRight(c, ".")+".")
	}

	return out
}

// narrateMapping says what a table does and what it does with a code it does not hold.
//
// Unmatched comes straight from the document, which already works it out in words. Deriving it again from the channel would give two
// sentences about the same table that can disagree - and the one that is wrong would be this one, because it is the one nobody checks
// against a vendor's specification.
func narrateMapping(m Mapping) string {
	where := m.Path
	if where == "" {
		where = "a field"
	}

	name := m.Name
	if name == "" {
		name = "an inline table"
	} else {
		name = "the " + name + " table"
	}

	sentence := fmt.Sprintf("%s is translated through %s, which holds %s",
		where, name, countOf(len(m.From), "entry", "entries"))

	if m.Unmatched != "" {
		sentence += "; " + lowerFirst(strings.TrimRight(m.Unmatched, "."))
	}

	return sentence + "."
}

// narrateDelivery describes one destination in a phrase.
//
// Built from How, which the specification derives with DescribeTransport - the one function that knows every destination type. Composing
// a description here instead would print a blank for whichever type was added last, which is the bug that made DescribeTransport
// exported in the first place.
func narrateDelivery(d Delivery) string {
	phrase := "The " + d.Name + " destination"
	if d.How != "" {
		phrase += " is " + lowerFirst(d.How)
	}

	// Whether a message survives the receiver being down is the single most operationally important fact about a destination, and it
	// is invisible in a transport description.
	if d.Durable {
		phrase += ", queued if it is unavailable"
	}

	if d.When != "" {
		phrase += ", but only when " + lowerFirst(strings.TrimRight(d.When, "."))
	}

	return phrase
}

// joinPaths names the first few paths and counts the rest.
//
// Named rather than counted, because "eleven fields" tells somebody the scale and "including PID-5, PID-8 and PV1-3" tells them whether
// it is the part of the message they are worried about.
func joinPaths(uses []FieldUse, limit int) string {
	names := make([]string, 0, limit)
	for i, u := range uses {
		if i >= limit {
			break
		}
		names = append(names, u.Path)
	}

	joined := joinWithAnd(names)
	if len(uses) > limit {
		joined += fmt.Sprintf(" and %d more", len(uses)-limit)
	}

	return joined
}

// joinWithAnd renders a list the way somebody would say it.
func joinWithAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	case 2:
		return items[0] + " and " + items[1]
	default:
		return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
	}
}

// countOf renders a count with the right noun.
func countOf(n int, singular, plural string) string {
	if n == 1 {
		return "1 " + singular
	}

	return fmt.Sprintf("%d %s", n, plural)
}

// upperFirst capitalises a sentence that the document produced as a phrase.
func upperFirst(s string) string {
	if s == "" {
		return s
	}

	runes := []rune(s)
	if runes[0] >= 'a' && runes[0] <= 'z' {
		runes[0] = runes[0] - 'a' + 'A'
	}

	return string(runes)
}

// lowerFirst lowercases an initial capital so a phrase reads inside a sentence.
//
// Only when the second character is not also upper case, so ADT and MLLP survive. Lowercasing those would produce "it receives aDT^A01
// messages", which reads as a typo and undermines everything around it.
func lowerFirst(s string) string {
	if s == "" {
		return s
	}

	runes := []rune(s)
	if len(runes) > 1 && runes[1] >= 'A' && runes[1] <= 'Z' {
		return s
	}
	if runes[0] >= 'A' && runes[0] <= 'Z' {
		runes[0] = runes[0] - 'A' + 'a'
	}

	return string(runes)
}
