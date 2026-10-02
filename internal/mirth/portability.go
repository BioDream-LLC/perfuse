package mirth

import (
	"fmt"
	"sort"
	"strings"
)

// The same scan, asked a different question.
//
// # Two questions, one scan
//
// ScanJava was built to answer "what must I rewrite to migrate to Perfuse". That question only interests somebody who has already
// decided to look at Perfuse. The same output answers a question a site wants answered before it has any opinion about Perfuse at
// all: how much of this integration layer only runs on one vendor's software.
//
// # Why this is an audit rather than a scare
//
// Because the distinction is real and most of the answer is reassuring. Most Java in Mirth scripts is not lock-in. SimpleDateFormat,
// HashMap, Apache Commons - all of it working around a JavaScript engine from 2009, all of it ordinary, all of it rewritable in
// minutes and portable to anything. A tool reporting "you have 312 Java references, you are trapped" would be lying.
//
// Lock-in is specifically the calls that need the vendor's own server: com.mirth.connect.* and vendor jars. Five of those and a site
// is stuck. Five hundred of the other kind and it is free.
//
// So this reports the number that matters and says plainly that the large number does not. An audit that inflates its own findings is
// worth nothing to the person who has to act on it, because the first thing their engineer will do is check.

// Portability is what a site can act on: how much of its integration logic runs only on one vendor's software.
type Portability struct {
	// Total is every Java reference found, including the harmless ones.
	//
	// Reported prominently and immediately qualified. It is the number that looks alarming and is not the finding, and leaving it
	// out would be its own dishonesty - somebody who greps their own exports will find it and wonder what else was shaped.
	Total int `json:"total"`

	// Portable is references that run anywhere: already provided, or ordinary Java with a direct equivalent.
	Portable int `json:"portable"`

	// VendorOnly is the count that constitutes lock-in - calls that need the vendor's own server or a vendor jar.
	VendorOnly int `json:"vendorOnly"`

	// NeedsAnotherRoute is capability that exists outside a script: a channel started through an API rather than in JavaScript.
	//
	// Separate from VendorOnly because the difference is "this is impossible" against "this is a REST call away", and collapsing
	// them would overstate the finding in the direction that flatters the argument.
	NeedsAnotherRoute int `json:"needsAnotherRoute"`

	// Unknown is external scripts, whose contents are not in the export.
	//
	// Counted rather than assumed either way. A path on the old server's filesystem could hold anything, and both "assume it is
	// fine" and "assume it is lock-in" are guesses presented as findings.
	Unknown int `json:"unknown"`

	// VendorReferences are the specific calls behind VendorOnly, deduplicated and sorted.
	//
	// Named because a count is an assertion and a list is evidence. Anybody being shown this should be able to open the channel and
	// look at the line.
	VendorReferences []string `json:"vendorReferences"`
}

// Portability reads the scan as a lock-in audit.
func (r *JavaReport) Portability() Portability {
	p := Portability{
		Total:             len(r.Uses),
		Portable:          r.Supported + r.Rewritable,
		VendorOnly:        r.OutOfScope,
		NeedsAnotherRoute: r.NeedsFeature,
		Unknown:           len(r.ExternalScripts),
	}

	// Empty rather than nil, because a nil slice marshals to JSON null and a caller that reaches for its length gets an exception
	// instead of a zero. That is this project's standing rule for response bodies, and breaking it here produced a white screen on
	// the one channel shape that should have been the reassuring case.
	p.VendorReferences = []string{}

	seen := map[string]bool{}

	for _, u := range r.Uses {
		if u.Verdict != VerdictOutOfScope || seen[u.Reference] {
			continue
		}

		seen[u.Reference] = true
		p.VendorReferences = append(p.VendorReferences, u.Reference)
	}

	// Sorted because Go maps range randomly and this is read by a person comparing two runs.
	sort.Strings(p.VendorReferences)

	return p
}

// Verdict states what the audit found, in the words somebody would use to decide.
//
// Written to be quotable without a caveat attached, because a sentence that needs one will be quoted without it.
func (p Portability) Verdict() string {
	switch {
	case p.Total == 0 && p.Unknown == 0:
		return "Nothing in this channel's scripts depends on any vendor. There is no Java at all."

	case p.VendorOnly == 0 && p.NeedsAnotherRoute == 0 && p.Unknown == 0:
		return fmt.Sprintf("Nothing here is locked in. %s an old JavaScript engine and has a direct "+
			"equivalent, so this channel's logic runs anywhere.",
			capitalise(plural(p.Total, "Java reference works around", "Java references work around")))

	case p.VendorOnly == 0 && p.Unknown == 0:
		return fmt.Sprintf("Nothing here is locked to a vendor. %s need a route other than a script - an API call rather "+
			"than JavaScript - and the remaining %d are ordinary Java with direct equivalents.",
			capitalise(plural(p.NeedsAnotherRoute, "reference does", "references do")), p.Portable)

	case p.VendorOnly == 0:
		return fmt.Sprintf("No vendor-only calls found, but %s reference a script file that is not in the export, so "+
			"what they contain is unknown. Fetch those files before concluding anything.",
			plural(p.Unknown, "step does", "steps do"))

	default:
		return fmt.Sprintf("%s run only on the vendor's own server. That is the number that matters: the other %d are "+
			"ordinary Java with direct equivalents and are not lock-in.",
			capitalise(plural(p.VendorOnly, "reference does", "references do")), p.Portable)
	}
}

// plural renders a count with the right form of its noun.
func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}

	return fmt.Sprintf("%d %s", n, many)
}

// capitalise raises the first letter, for a sentence that starts with a count.
func capitalise(s string) string {
	if s == "" {
		return s
	}

	return strings.ToUpper(s[:1]) + s[1:]
}
