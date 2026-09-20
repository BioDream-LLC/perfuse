// Package hl7v3 reads HL7 version 3 messages.
//
// Version 3 is a different thing from version 2 wearing the same name. Version 2 is a pipe-delimited stream of segments
// whose meaning is positional; version 3 is XML generated from a reference information model, where meaning is carried by
// element names and by a small set of datatypes that recur everywhere. Almost nothing transfers between the two beyond the
// clinical vocabulary.
//
// In practice v3 arrives in one of two shapes. Either it is a CDA document, which is v3-derived and handled by the cda
// package, or it is an IHE transaction - PIX and PDQ v3 in particular, which is how a great many hospitals resolve patient
// identifiers across systems. This package targets the second.
//
// # Why the datatypes come first
//
// The single most consequential thing about v3 is that an element can be present and explicitly null, with a stated reason.
// A birth date carrying nullFlavor="ASKU" means the patient was asked and declined to answer. One carrying nullFlavor="NAV"
// means the value exists but is not available right now, so asking again later may work. One that is simply absent means
// nobody recorded anything at all.
//
// Collapsing those into an empty string is the mistake this package is arranged to prevent. It is not a cosmetic loss: a
// downstream system that receives an empty birth date where the source said "patient declined" will usually treat it as
// missing data to be chased, and a demographic match that should have been left unresolved gets resolved by a human
// guessing. So every datatype here distinguishes absent, null-with-reason, and present.
package hl7v3

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// NullFlavor is a stated reason a value is absent.
//
// The set is from the HL7 v3 NullFlavor vocabulary. Only the ones that appear in practice are named; anything else is kept
// verbatim rather than rejected, because a sender using a code from a newer release of the vocabulary is not sending a
// broken message and refusing it would strand a working interface.
type NullFlavor string

const (
	// NoInformation is the generic "nothing is known", and the parent of the rest.
	NoInformation NullFlavor = "NI"

	// NotApplicable means the question does not apply - a delivery date for a male patient.
	NotApplicable NullFlavor = "NA"

	// Unknown means a value exists and is not known.
	Unknown NullFlavor = "UNK"

	// AskedButUnknown means somebody asked and the answer was not forthcoming.
	//
	// Clinically distinct from Unknown and the distinction is the reason this type exists: it says the gap has already
	// been chased once, so chasing it again is unlikely to help.
	AskedButUnknown NullFlavor = "ASKU"

	// NotAsked means nobody asked, which is the one worth chasing.
	NotAsked NullFlavor = "NASK"

	// TemporarilyUnavailable means the value exists and could not be retrieved now. Asking later may work.
	TemporarilyUnavailable NullFlavor = "NAV"

	// Masked means the value is being withheld deliberately, usually for privacy.
	//
	// Never treat this as missing. A masked address on a patient under a protection order is a decision somebody made,
	// and a downstream system that fills the gap from another source has undone it.
	Masked NullFlavor = "MSK"

	// Other means a value exists but is outside the permitted value set.
	Other NullFlavor = "OTH"

	// Positive and negative infinity, which appear in date ranges meaning "no end".
	PositiveInfinity NullFlavor = "PINF"
	NegativeInfinity NullFlavor = "NINF"
)

// Explain gives a null flavor in words.
//
// Present because these codes appear in operational logs and dashboards, and "ASKU" tells an integration analyst nothing
// on a bad morning. An unrecognised code is returned as itself rather than as "unknown reason", because the code is more
// use to somebody searching a specification than a guess would be.
func (n NullFlavor) Explain() string {
	switch n {
	case NoInformation:
		return "no information"
	case NotApplicable:
		return "not applicable to this patient"
	case Unknown:
		return "unknown"
	case AskedButUnknown:
		return "asked, and the answer was not known"
	case NotAsked:
		return "not asked"
	case NotAsked + "R":
		return "not asked, and asking is not permitted"
	case TemporarilyUnavailable:
		return "temporarily unavailable; asking again later may work"
	case Masked:
		return "withheld deliberately"
	case Other:
		return "a value exists but is outside the permitted set"
	case PositiveInfinity:
		return "no upper bound"
	case NegativeInfinity:
		return "no lower bound"
	case "":
		return ""
	default:
		return string(n)
	}
}

// Presence says whether a value was given, deliberately withheld, or never mentioned.
type Presence int

const (
	// Absent means the element was not in the document at all. Nobody recorded anything.
	Absent Presence = iota

	// Null means the element was present and carried a nullFlavor, so the sender said something about why there is no
	// value. That statement is information and is kept.
	Null

	// Present means there is a value.
	Present
)

// String names a presence for a log.
func (p Presence) String() string {
	switch p {
	case Absent:
		return "absent"
	case Null:
		return "null"
	case Present:
		return "present"
	default:
		return fmt.Sprintf("presence(%d)", int(p))
	}
}

// II is an instance identifier: the v3 way of saying "this identifier, in this namespace".
//
// Root is an OID naming the assigning authority and Extension is the identifier within it. The nearest v2 equivalent is a
// CX, where the assigning authority sits in the fourth component.
//
// # Why the root matters more than it looks
//
// Two patients can share the extension "12345" in different hospitals, so an extension without its root is not an
// identifier - it is a number. Code that compares extensions alone will merge records across organisations, and that is the
// single worst failure mode in patient identity work. Equal compares both, and there is no accessor that returns the
// extension in a way that invites comparing it on its own.
type II struct {
	// Root is the OID of the assigning authority, or occasionally a UUID for a one-off identifier.
	Root string

	// Extension is the identifier within that authority. Empty when the root alone is the identifier, which is how a
	// document or message identifier is often expressed.
	Extension string

	// AssigningAuthorityName is a human label for the root. Advisory only: never match on it, because the same
	// authority is written "St Joseph's", "St. Josephs" and "SJH" by three interfaces on the same day.
	AssigningAuthorityName string

	// Presence and NullFlavor record whether this identifier was given at all.
	Presence   Presence
	NullFlavor NullFlavor
}

// Equal reports whether two identifiers denote the same thing.
//
// Both parts must match. Comparing extensions alone across organisations merges patient records, so this deliberately has
// no shortcut for it.
func (i II) Equal(other II) bool {
	if i.Presence != Present || other.Presence != Present {
		// Two absent identifiers are not the same identifier. Treating them as equal would make every patient with no
		// national number match every other, which is exactly the kind of comparison that quietly merges records.
		return false
	}

	return i.Root == other.Root && i.Extension == other.Extension
}

// String renders an identifier for a log, in the root^extension shape an analyst will recognise.
func (i II) String() string {
	switch i.Presence {
	case Absent:
		return ""
	case Null:
		return "null(" + string(i.NullFlavor) + ")"
	}
	if i.Extension == "" {
		return i.Root
	}

	return i.Extension + "^^^" + i.Root
}

// parseII reads an II element.
func parseII(n *xtree.Node) II {
	if n == nil {
		return II{Presence: Absent}
	}
	if nf := attr(n, "nullFlavor"); nf != "" {
		return II{Presence: Null, NullFlavor: NullFlavor(nf)}
	}

	id := II{
		Root:                   attr(n, "root"),
		Extension:              attr(n, "extension"),
		AssigningAuthorityName: attr(n, "assigningAuthorityName"),
		Presence:               Present,
	}

	// An II with neither part is not an identifier. Reported as absent rather than as an empty present value, so
	// nothing downstream compares two of them and finds them equal.
	if id.Root == "" && id.Extension == "" {
		return II{Presence: Absent}
	}

	return id
}

// Coded is a coded value: CS, CE, CV, CD and CO all reduce to this.
//
// The variants differ in whether translations and qualifiers are permitted, not in what a channel needs from them.
type Coded struct {
	// Code is the value within the code system.
	Code string

	// System is the OID of the code system. As with II.Root, a code without its system is not a code: "250" is
	// diabetes in ICD-9 and something else entirely elsewhere.
	System string

	// SystemName and DisplayName are labels. Advisory: display names drift between releases while codes do not, so
	// matching on a display name is matching on a sender's spelling.
	SystemName  string
	DisplayName string

	// OriginalText is what a person actually wrote or chose, when the sender included it. Worth keeping, because a
	// coded value plus its original text sometimes disagree and the text is what a clinician saw.
	OriginalText string

	// Translations are the same concept in other code systems, which is how a sender offers a local code alongside a
	// standard one.
	Translations []Coded

	Presence   Presence
	NullFlavor NullFlavor
}

// Equal compares code and system, never the display name.
func (c Coded) Equal(other Coded) bool {
	if c.Presence != Present || other.Presence != Present {
		return false
	}

	return c.Code == other.Code && c.System == other.System
}

// String renders a coded value as code^display^system, which is the v2 CE shape an analyst reads fluently.
func (c Coded) String() string {
	switch c.Presence {
	case Absent:
		return ""
	case Null:
		return "null(" + string(c.NullFlavor) + ")"
	}

	return c.Code + "^" + c.DisplayName + "^" + c.System
}

// parseCoded reads any of the coded datatypes.
func parseCoded(n *xtree.Node) Coded {
	if n == nil {
		return Coded{Presence: Absent}
	}
	if nf := attr(n, "nullFlavor"); nf != "" {
		return Coded{Presence: Null, NullFlavor: NullFlavor(nf)}
	}

	c := Coded{
		Code:        attr(n, "code"),
		System:      attr(n, "codeSystem"),
		SystemName:  attr(n, "codeSystemName"),
		DisplayName: attr(n, "displayName"),
		Presence:    Present,
	}

	if ot := n.First("originalText"); ot != nil {
		c.OriginalText = strings.TrimSpace(ot.Text)
	}
	for _, tn := range n.All("translation") {
		if t := parseCoded(tn); t.Presence == Present {
			c.Translations = append(c.Translations, t)
		}
	}

	if c.Code == "" && c.OriginalText == "" {
		return Coded{Presence: Absent}
	}

	return c
}

// attr reads an attribute, ignoring any namespace prefix on it.
//
// Prefixes are chosen by the sender and vary between identity providers of the same specification, so matching on a
// prefixed name works against one vendor and fails against the next.
func attr(n *xtree.Node, name string) string {
	if n == nil {
		return ""
	}

	for _, a := range n.Attrs {
		if a.Name == name {
			return strings.TrimSpace(a.Value)
		}
		if i := strings.IndexByte(a.Name, ':'); i >= 0 && a.Name[i+1:] == name {
			return strings.TrimSpace(a.Value)
		}
	}

	return ""
}

// knownNullFlavors is the set a transformation may write.
//
// Reading is deliberately permissive - an unrecognised flavour from a sender is kept verbatim, because a newer release of
// the vocabulary is not a broken message. Writing is deliberately strict, and the asymmetry is the point: a flavour in an
// arriving message came from another system and refusing it would strand a working interface, while a flavour in a
// transformation was typed by a person into a channel that will run unattended.
//
// A typo there does not fail. It writes a flavour no receiver recognises, which most will treat as "no information" - so
// a step written to say "the patient was asked and did not know" quietly says "nothing is known", and the difference is
// whether anybody asks again.
var knownNullFlavors = map[NullFlavor]bool{
	NoInformation:          true,
	NotApplicable:          true,
	Unknown:                true,
	AskedButUnknown:        true,
	NotAsked:               true,
	TemporarilyUnavailable: true,
	Masked:                 true,
	Other:                  true,
	PositiveInfinity:       true,
	NegativeInfinity:       true,
}

// KnownNullFlavor reports whether this is a flavour a transformation may write.
func KnownNullFlavor(s string) bool { return knownNullFlavors[NullFlavor(s)] }

// knownNullFlavorList names the writable flavours, for an error message.
//
// Sorted, because a map ranges randomly in Go and an error message that lists valid options in a different order each time
// is one somebody cannot search for.
func knownNullFlavorList() string {
	out := make([]string, 0, len(knownNullFlavors))
	for f := range knownNullFlavors {
		out = append(out, string(f))
	}
	sort.Strings(out)

	return strings.Join(out, ", ")
}
