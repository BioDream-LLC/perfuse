package profile

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/hl7v3"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Profiling HL7 v3 documents.
//
// This produces the same Report as the v2 profiler, deliberately. Contracts, promotion and the mapping suggestions are all built on
// Report, and they are built on the parts of it that do not care whether a path names a segment and field or an element and
// attribute: a path, a fill rate, a distinct count, a shape. Giving v3 its own report type would have meant a second Check, a second
// promotion path and a second suggestion layer, three of which already work.
//
// What does not carry over is the segment. v3 has no segments, so the report has one entry standing for the document, named after the
// interaction. That is the honest mapping rather than an invented one, and it is why v3ContainerID is a constant rather than
// something derived from the content.

// v3ContainerID stands in for a segment identifier, since a v3 document has no segments.
//
// A fixed string rather than the interaction identifier, because the alternative makes the report's shape depend on its content: a
// corpus containing two interactions would produce two "segments" whose fill rates were each relative to a different denominator,
// and a contract written against one would silently stop being checked when the other arrived.
const v3ContainerID = "document"

// BuildV3 profiles a corpus of HL7 v3 documents.
//
// Paths are in the notation the v3 filter, picker and transformation steps accept, so an observation here can be pasted into a
// contract or a filter unchanged. That is the same property the v2 profiler has and it is the reason either is usable: a profile
// whose paths need translating before they can be acted on is a report somebody reads once.
func BuildV3(documents [][]byte) *Report {
	rep := &Report{}

	container := &segmentAcc{
		id:     v3ContainerID,
		fields: map[string]*fieldAcc{},
	}
	interactions := map[string]int{}

	for _, raw := range documents {
		msg, err := hl7v3.Parse(raw)
		if err != nil {
			rep.Unreadable++
			continue
		}
		if msg.Root == nil {
			// Parsed but empty. Counted as unreadable rather than as a message with no fields, because a
			// document with no root contributes nothing and would otherwise drag every fill rate down while
			// looking like evidence.
			rep.Unreadable++
			continue
		}
		rep.Messages++
		container.messages++

		// The interaction identifier is what v2's message type is: the routing key, and the first thing that
		// surprises somebody about a feed. A corpus described as one interaction routinely carries two.
		if msg.InteractionID != "" {
			interactions[msg.InteractionID]++
		}

		// One document contributes at most one observation per path, which is what makes a fill rate a rate. The
		// occurrence suffix already distinguishes repeats, so this cannot collapse two genuine repetitions.
		values := map[string]string{}
		collectV3(msg.Root, "", 0, values)

		repeats := countV3Repeats(values)

		for path, value := range values {
			acc, seen := container.fields[path]
			if !seen {
				acc = newV3FieldAcc(path)
				container.fields[path] = acc
				container.fieldOrder = append(container.fieldOrder, path)
			}

			// Counted as present because the element or attribute was there, whether or not it carries a
			// value. Presence and emptiness are different facts in v3 - an element sent empty is a statement
			// that the sender has no value, and an element not sent at all is a statement about the interface
			// - and collapsing them is what made the earlier XML diff unable to tell a removal from a
			// blanking. Absent is expressed by the path not appearing at all.
			acc.present++

			if n := repeats[baseV3Path(path)]; n > acc.maxRepeats {
				acc.maxRepeats = n
			}

			// A v3 code is already its own attribute, so the value and the code-table value are the same
			// thing. In v2 they differ because a composite carries the code in its first component.
			observeString(acc, value, value)
		}
	}

	rep.Types = interactionCounts(interactions, rep.Messages)

	if container.messages > 0 {
		rep.Segments = []Segment{finishV3Container(container)}
	}

	rep.Notes = v3Notes(rep, container)

	return rep
}

// collectV3 walks a document into path/value pairs.
//
// Attributes are collected as well as element text, and that is the point rather than a detail: in v3 nearly every value lives in an
// attribute. A profiler reading only element text would report a document full of identifiers, codes, genders and birth dates as
// having no populated fields at all, and would do it while looking like it worked.
func collectV3(node *xtree.Node, parentPath string, occurrence int, out map[string]string) {
	if node == nil {
		return
	}

	name := node.Name
	if occurrence > 0 {
		name = fmt.Sprintf("%s(%d)", node.Name, occurrence)
	}
	path := parentPath + "/" + name

	// Recorded even when empty, so a path that exists is distinguishable from one that does not.
	out[path] = strings.TrimSpace(node.Text)

	for _, a := range node.Attrs {
		out[path+"@"+a.Name] = a.Value
	}

	counts := map[string]int{}
	for _, c := range node.Children {
		counts[c.Name]++
	}

	seen := map[string]int{}
	for _, c := range node.Children {
		next := 0
		if counts[c.Name] > 1 {
			seen[c.Name]++
			next = seen[c.Name]
		}
		collectV3(c, path, next, out)
	}
}

// newV3FieldAcc creates an accumulator, collecting vocabulary only for paths that are coded by definition.
//
// The v2 profiler asks the segment dictionary whether a table constrains the field. There is no such dictionary here, but v3 does not
// need one: the naming is normative rather than conventional. An attribute called code, classCode, moodCode or nullFlavor is a coded
// attribute because the RIM says so.
//
// What matters more is the other side of the line. Vocabulary is a list of the actual values seen, so collecting it for the wrong path
// puts patient data in a profile - and in v3 an identifier lives in root and extension, a name in element text, a birth date in a
// value attribute. Those are precisely the paths that must never be collected, so this is an allow-list of attribute names and never a
// deny-list: an unrecognised path collects nothing, which fails towards not retaining data.
func newV3FieldAcc(path string) *fieldAcc {
	fa := newFieldAcc(path, "", 0)

	if v3PathIsCoded(path) {
		fa.codes = map[string]int{}
	}

	return fa
}

// v3PathIsCoded reports whether a path names an attribute whose values are a code set.
func v3PathIsCoded(path string) bool {
	at := strings.LastIndexByte(path, '@')
	if at < 0 {
		// Element text. Never collected: in v3 the text of an element is a name, an address line or a comment,
		// which is to say it is the patient data this must not retain.
		//
		// Kept although planting showed it changes no outcome. Without it the whole path is compared against the
		// attribute names below and matches none, so the default-false switch is what actually protects element
		// text - but that only holds because a path always begins with a slash and so can never equal a bare
		// attribute name. This states the rule directly instead of resting on that, and the cost is one
		// comparison. The behavioural guard is proven separately by the plant that collects vocabulary for every
		// path, which does fire.
		return false
	}

	switch name := path[at+1:]; name {
	case "code", "classCode", "moodCode", "determinerCode", "typeCode", "contextControlCode",
		"nullFlavor", "use", "codeSystem", "codeSystemName", "qualifier", "unit", "operator",
		"negationInd", "inversionInd", "contextConductionInd", "independentInd":
		return true
	default:
		// Deliberately not a suffix test on "Code". administrativeGenderCode and confidentialityCode would pass
		// it and are genuinely coded, but so would any element an implementer named to end that way, and the
		// cost of being wrong here is patient data in a report. Named attributes only.
		return name == "administrativeGenderCode" || name == "confidentialityCode" ||
			name == "religiousAffiliationCode" || name == "raceCode" || name == "ethnicGroupCode" ||
			name == "maritalStatusCode" || name == "languageCode" || name == "statusCode"
	}
}

// baseV3Path strips occurrence suffixes, so name(1) and name(2) are recognised as repetitions of one path.
//
// The attribute suffix is handled explicitly, and a test found out why the obvious version does not work. Requiring the part to end in
// a closing bracket looks right and is wrong for name(1)@use, which ends in "e" - so the occurrence survived, every attribute of a
// repeating element was its own base path, and every one of them reported exactly one repetition. A receiver sizing a column from
// MaxRepeats would have been told once where the answer was four.
func baseV3Path(path string) string {
	var b strings.Builder

	for i, part := range strings.Split(path, "/") {
		if i > 0 {
			b.WriteByte('/')
		}

		// The attribute travels with the base path rather than being dropped: an attribute is not a repetition
		// of its element, so name@use and name are counted separately.
		attr := ""
		if at := strings.IndexByte(part, '@'); at >= 0 {
			attr = part[at:]
			part = part[:at]
		}

		if open := strings.IndexByte(part, '('); open >= 0 && strings.HasSuffix(part, ")") {
			part = part[:open]
		}

		b.WriteString(part)
		b.WriteString(attr)
	}

	return b.String()
}

// countV3Repeats counts how many occurrences each path had in one document.
func countV3Repeats(values map[string]string) map[string]int {
	counts := map[string]int{}

	for path := range values {
		// Attributes are not repetitions of their element, so they are counted under their own base path and
		// not folded into it. Without this, an element with four attributes would report four repetitions.
		counts[baseV3Path(path)]++
	}

	return counts
}

// interactionCounts turns the interaction tally into the report's type counts, worst-ambiguity-first being meaningless here so it is
// sorted by frequency and then by name.
func interactionCounts(interactions map[string]int, messages int) []TypeCount {
	if len(interactions) == 0 {
		return nil
	}

	out := make([]TypeCount, 0, len(interactions))
	for id, n := range interactions {
		rate := 0.0
		if messages > 0 {
			rate = float64(n) / float64(messages)
		}
		out = append(out, TypeCount{Type: id, Count: n, Rate: rate})
	}

	// Commonest first, because that is the one somebody is looking at. Ties broken by name so the order is stable:
	// Go maps range randomly and a report that reorders itself between runs cannot be diffed.
	sort.Slice(out, func(i, j int) bool {
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Type < out[j].Type
	})

	return out
}

// finishV3Container turns the accumulator into the reported container.
func finishV3Container(acc *segmentAcc) Segment {
	seg := Segment{
		ID: acc.id,
		// Named in words, because "document" alone in a column headed Segment reads like a bug.
		Name: "the document as a whole",
		// Not standard, and that is accurate rather than pejorative: the HL7 segment dictionary has no entry
		// for this because it is not a segment. Claiming otherwise would put a tick beside something the
		// standard says nothing about.
		Standard:      false,
		Messages:      acc.messages,
		Rate:          1,
		MaxPerMessage: 1,
	}

	// Paths sorted, because the order first seen is the order of one arbitrary document's element tree, and across a
	// corpus that is not a reading order - it is whichever document happened to be first. The v2 profiler keeps first-seen
	// order because a message's segments genuinely do have one.
	paths := append([]string(nil), acc.fieldOrder...)
	sort.Strings(paths)

	for _, path := range paths {
		fa := acc.fields[path]
		if fa == nil {
			continue
		}
		seg.Fields = append(seg.Fields, fa.summarise("", acc.messages))
	}

	return seg
}

// v3Notes states in words what the numbers do not carry.
func v3Notes(rep *Report, container *segmentAcc) []string {
	var notes []string

	if rep.Unreadable > 0 {
		notes = append(notes, fmt.Sprintf(
			"%d document(s) could not be parsed and are not counted in any rate below",
			rep.Unreadable))
	}

	if len(rep.Types) > 1 {
		// Said plainly, because it changes how every rate below should be read. A path populated in half the
		// documents may be populated in all of one interaction and none of the other, which is a different fact
		// from a field that is sometimes filled.
		var names []string
		for _, t := range rep.Types {
			names = append(names, t.Type)
		}
		notes = append(notes, fmt.Sprintf(
			"this corpus contains %d interactions (%s), so a rate below may reflect which interaction a "+
				"path belongs to rather than how often it is populated",
			len(rep.Types), strings.Join(names, ", ")))
	}

	if container.messages > 0 && len(container.fieldOrder) == 0 {
		// Worth saying because the alternative reading is that the feed carries nothing, and the likelier cause
		// is a namespace or a wrapper this walked past.
		notes = append(notes, "no paths were found in any document, which is more likely to mean these are "+
			"not the documents this expects than that they are empty")
	}

	return notes
}

// BuildFor profiles a corpus according to the channel's declared data type.
//
// One dispatcher rather than the choice being made at each call site. There are five places that build a profile - two CLI commands, the
// profile endpoint, the contract endpoint and the contract command - and five independent decisions would be five chances to profile a
// v3 corpus with the v2 reader. That failure is silent and convincing: the v2 parser finds no segments in an XML document, so the report
// comes back with no fields, which reads exactly like a feed that populates nothing.
//
// The data type is taken as a string rather than importing the config package, because profiling has no other reason to depend on
// configuration and a cycle here would be paid for in both directions.
func BuildFor(dataType string, messages [][]byte) *Report {
	if dataType == "hl7v3" {
		return BuildV3(messages)
	}

	// Anything else profiles as v2, which is the right default: the data type is empty on every channel written before
	// v3 existed, and those are v2 feeds.
	return Build(messages)
}
