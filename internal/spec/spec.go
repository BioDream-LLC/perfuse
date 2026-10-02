package spec

import (
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/hl7dict"
)

// The interface specification, derived from the channel rather than written by hand.
//
// Every integration has a specification document. Somebody writes it in Word during the build,
// hands it to the receiving vendor, and it is out of date within a fortnight because the channel
// changed and the document did not. Then a year later there is an incident and the document is
// the only description of the interface anybody has, and it is wrong.
//
// A channel is already a precise statement of what it reads, what it writes and where it sends.
// This turns that into the document, so the document cannot drift: it is regenerated from the
// thing that actually runs.
//
// What it deliberately does not claim: a channel with a script has behaviour that cannot be
// established by reading its configuration, and the specification says so rather than describing
// the declarative half and quietly omitting the rest. A document that looks complete and is not
// is worse than one that admits its limits.

// FieldUse is one path the interface touches.
type FieldUse struct {
	Path string `json:"path"`
	// Name is from the HL7 dictionary, when the standard defines the field.
	Name string `json:"name,omitempty"`
	// Description adds what the dictionary knows beyond the name.
	Description string `json:"description,omitempty"`
	// Table is the HL7 table constraining it, when there is one.
	Table string `json:"table,omitempty"`
	// Repeats reports whether the standard allows repetition, which a receiver has to handle
	// whether or not this sender uses it.
	Repeats bool `json:"repeats,omitempty"`

	// Why says what the interface does with it: tested by the filter, written by a step,
	// read by a destination.
	Why []string `json:"why"`
}

// Mapping is one code translation the interface performs.
//
// Listed in full, because this is the part a receiving vendor most needs and the part most often
// missing from a hand-written document. A translation table in a channel file is precise; the same
// table transcribed into prose is where the errors come from.
type Mapping struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
	// From and To are parallel: From[i] becomes To[i].
	From []string `json:"from"`
	To   []string `json:"to"`
	// Unmatched says what happens to a value not in the table.
	Unmatched string `json:"unmatched"`
}

// Delivery is one place the interface sends.
type Delivery struct {
	Name string `json:"name"`
	// How describes the transport in words, without credentials.
	How string `json:"how"`
	// When describes the destination filter, empty when it takes everything.
	When string `json:"when,omitempty"`
	// Durable says whether a message survives the receiver being unavailable.
	Durable bool `json:"durable"`
}

// Document is a whole interface specification.
type Document struct {
	Channel     string `json:"channel"`
	Description string `json:"description,omitempty"`
	DataType    string `json:"dataType"`

	// Receives and Acknowledges are the contract with the sender.
	Receives     string `json:"receives"`
	Acknowledges string `json:"acknowledges"`

	// Accepts describes the filter in words, empty when everything is accepted.
	Accepts string `json:"accepts,omitempty"`

	// Reads are the fields the interface depends on. This is the list a sending vendor needs:
	// these are the fields that must be populated for the interface to work.
	Reads []FieldUse `json:"reads,omitempty"`
	// Writes are the fields the interface changes. This is the list a receiving vendor needs.
	Writes []FieldUse `json:"writes,omitempty"`

	Mappings   []Mapping  `json:"mappings,omitempty"`
	Steps      []string   `json:"steps,omitempty"`
	Deliveries []Delivery `json:"deliveries,omitempty"`

	// Caveats are the things this document cannot tell you, stated rather than omitted.
	Caveats []string `json:"caveats,omitempty"`

	// Narration is the same document in sentences, for somebody who needs to know what this channel does rather than to hand a
	// specification to a vendor. Derived from the fields above, so the two cannot describe the channel differently.
	Narration []string `json:"narration,omitempty"`
}

// Build derives the specification from a validated channel.
func Build(c *config.Channel) *Document {
	doc := &Document{
		Channel:     c.Name,
		Description: c.Description,
		DataType:    string(c.Type()),
		Accepts:     c.Filter,
	}

	doc.Receives = describeArrival(c)
	doc.Acknowledges = describePromise(c)

	reads := map[string][]string{}
	writes := map[string][]string{}

	// The filter's paths are dependencies: if one of them is not populated, the interface
	// behaves differently. That is exactly what a sending vendor needs to be told.
	for _, p := range c.Paths() {
		reads[p] = append(reads[p], "tested by the filter that decides whether to accept a message")
	}

	if pipe := c.Pipeline(); pipe != nil {
		for _, st := range pipe.Steps() {
			doc.Steps = append(doc.Steps, st.Describe())
			recordStep(st, reads, writes)

			if m := mappingOf(st, c.CodeSets()); m != nil {
				doc.Mappings = append(doc.Mappings, *m)
			}
		}
	}

	for _, d := range c.Destinations {
		doc.Deliveries = append(doc.Deliveries, Delivery{
			Name:    d.Name,
			How:     describeTransport(d),
			When:    d.Filter,
			Durable: d.Queue.IsEnabled(),
		})
		// A destination filter reads fields too, and a receiving vendor asking "why did I not
		// get that message" is asking about exactly these.
		for _, p := range destinationPaths(d) {
			reads[p] = append(reads[p],
				"tested by the filter on the "+d.Name+" destination")
		}
	}

	doc.Reads = summarise(reads)
	doc.Writes = summarise(writes)

	doc.Caveats = caveats(c, doc)

	// Last, because it reads the finished document. Narrating from a half-built one would describe a channel that is not this one.
	doc.Narration = doc.Narrate()

	return doc
}

// recordStep notes which paths a step reads and writes.
//
// The distinction matters more than it looks: a sending system needs the read list, a receiving
// system needs the write list, and a document that merges them tells neither party what they
// need. A copy step is the clearest case - it reads one path and writes another.
func recordStep(st transformStep, reads, writes map[string][]string) {
	switch {
	case st.Set != nil:
		writes[st.Set.Path] = append(writes[st.Set.Path], "set to a fixed value")
	case st.Copy != nil:
		reads[st.Copy.From] = append(reads[st.Copy.From], "copied into "+st.Copy.To)
		writes[st.Copy.To] = append(writes[st.Copy.To], "populated from "+st.Copy.From)
	case st.Clear != nil:
		writes[st.Clear.Path] = append(writes[st.Clear.Path], "emptied")
	case st.Remove != nil:
		writes[st.Remove.Path] = append(writes[st.Remove.Path], "removed entirely")
	case st.Map != nil:
		reads[st.Map.Path] = append(reads[st.Map.Path], "read for translation")
		writes[st.Map.Path] = append(writes[st.Map.Path], "replaced with a translated value")
	case st.Replace != nil:
		writes[st.Replace.Path] = append(writes[st.Replace.Path], "rewritten by find and replace")
	case st.Pad != nil:
		writes[st.Pad.Path] = append(writes[st.Pad.Path], "padded to a fixed width")
	case st.Date != nil:
		writes[st.Date.Path] = append(writes[st.Date.Path], "reformatted as a date")
	case st.Trim != nil:
		writes[st.Trim.Path] = append(writes[st.Trim.Path], "trimmed of surrounding spaces")
	case st.Case != nil:
		writes[st.Case.Path] = append(writes[st.Case.Path], "changed to "+st.Case.To+" case")
	}

	// A conditional step reads whatever its condition tests. Omitting these would understate
	// the interface's dependencies, which is the failure mode that matters here.
	if st.When != "" {
		for _, p := range pathsInCondition(st.When) {
			reads[p] = append(reads[p], "tested by a condition on one of the transformation steps")
		}
	}
}

// summarise turns the collected paths into a sorted, annotated list.
func summarise(in map[string][]string) []FieldUse {
	out := make([]FieldUse, 0, len(in))

	for path, why := range in {
		use := FieldUse{Path: path, Why: dedupe(why)}

		if seg, field, ok := splitPath(path); ok {
			if def, found := hl7dict.LookupField(seg, field); found {
				use.Name = def.Name
				use.Description = def.Description
				use.Table = def.Table
				use.Repeats = def.Repeats
			}
		}

		out = append(out, use)
	}

	// Sorted, because a specification is a document that gets diffed. Two generations from the
	// same channel differing in order would make every comparison useless.
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

func dedupe(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// splitPath pulls the segment and field number out of a path like "PID-5.1".
func splitPath(path string) (segment string, field int, ok bool) {
	i := strings.IndexAny(path, "-")
	if i <= 0 {
		return "", 0, false
	}
	segment = strings.ToUpper(path[:i])
	// Strip an occurrence marker: PID(2)-5.
	if j := strings.Index(segment, "("); j > 0 {
		segment = segment[:j]
	}

	rest := path[i+1:]
	n := 0
	digits := 0
	for _, r := range rest {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		digits++
	}
	if digits == 0 {
		return "", 0, false
	}
	return segment, n, true
}
