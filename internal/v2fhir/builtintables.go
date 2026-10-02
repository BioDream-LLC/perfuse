package v2fhir

import (
	"sort"

	"github.com/biodream-llc/perfuse/internal/codeset"
)

// The mappings built into the converter, exposed as tables.
//
// These decide what an HL7 v2 code becomes in FHIR - what PID-8 "A" turns into, which patient classes are recognised, what an ADT
// trigger event says about a visit's state. They were only discoverable by reading Go source, which means the one question an
// integration analyst asks most often about an engine could not be asked of it.
//
// Returned as codeset.Table, the same type a site's own mapping files produce, so they travel through the same projection and the same
// $translate. A separate shape would have meant a second projection to keep in step with the first, and the second one always lags.

// BuiltinTablePrefix marks a mapping that comes with Perfuse rather than from a site's own file.
//
// Prefixed so the two can never be confused, and so a site table called sex and the built-in v2 sex mapping can both be published. They
// are different things - one is this hospital's decision, the other is what the converter does by default - and merging them under one
// name would hide whichever was loaded second.
const BuiltinTablePrefix = "perfuse-"

// BuiltinTables returns the converter's own mappings, sorted by name.
//
// Provenance is recorded honestly per table. Some of these are defined by HL7 and can be applied with confidence; others are this
// engine's reading of a specification that does not fully determine the answer. A client cannot tell those apart unless it is told, and
// the difference decides whether somebody should override the mapping with a table of their own.
func BuiltinTables() []*codeset.Table {
	tables := []*codeset.Table{
		fromStrings("sex", "HL7 v2 PID-8 administrative sex into the FHIR gender value set",
			"HL7 v2 table 0001. Several v2 codes have no distinct FHIR equivalent and collapse onto other, "+
				"which loses the distinction between ambiguous and not applicable.",
			genderMap, ""),

		fromCodeDisplay("patient-class", "HL7 v2 PV1-2 patient class into the v3 ActCode encounter class",
			"HL7 v2 table 0004 into v3 ActEncounterCode. Defined by HL7 and one of the few that can be applied "+
				"with confidence. U is deliberately unmapped: it means unknown, and guessing a class from it "+
				"would invent a fact about the visit.",
			encounterClassMap),

		fromStrings("encounter-status", "ADT trigger event into the encounter status it implies",
			"This is the mapping that most often goes wrong, because a trigger event says what happened rather "+
				"than what state the visit is now in. Codes are the R5 value set; the serialiser maps them to R4.",
			encounterStatusForEvent, ""),

		fromStrings("observation-status", "HL7 v2 OBX-11 result status into the FHIR observation status",
			"HL7 v2 table 0085.", observationStatusMap, ""),

		fromStrings("report-status", "HL7 v2 OBR-25 result status into the FHIR diagnostic report status",
			"HL7 v2 table 0123.", reportStatusMap, ""),

		fromCodeDisplay("interpretation", "HL7 v2 OBX-8 abnormal flags into the FHIR observation interpretation",
			"HL7 v2 table 0078 into the observation-interpretation code system.",
			interpretationMap),

		fromStrings("coding-system", "HL7 v2 coding system identifiers into their canonical URIs",
			"The identifier in the third component of a coded field - LN, SCT, I9, and the local variants seen "+
				"in real feeds - into the system URI FHIR expects.",
			codingSystemMap, ""),

		fromStrings("units", "HL7 v2 unit strings into UCUM",
			"Units as they are actually sent, which is not always what UCUM defines. An unrecognised unit is "+
				"passed through unchanged rather than guessed at, because a wrong unit on a result is worse "+
				"than an unconverted one.",
			ucumUnits, ""),
	}

	sort.Slice(tables, func(i, j int) bool { return tables[i].Name < tables[j].Name })

	return tables
}

// fromStrings builds a table from a plain code-to-code map.
//
// Entries are sorted by source code, because Go maps range randomly and a published mapping that reorders itself between requests looks
// like the mapping changed.
func fromStrings(name, describes, source string, m map[string]string, dflt string) *codeset.Table {
	from := make([]string, 0, len(m))
	for k := range m {
		from = append(from, k)
	}
	sort.Strings(from)

	t := &codeset.Table{
		Name:      BuiltinTablePrefix + name,
		Describes: describes,
		Source:    source,
		DecidedBy: "built in to Perfuse",
		Default:   dflt,
	}

	for _, k := range from {
		t.Entries = append(t.Entries, codeset.Entry{From: k, To: m[k]})
	}
	t.Compile()

	return t
}

// fromCodeDisplay builds a table from a map that also carries a display name.
//
// The display goes into the entry's reason, which is where the projection puts it. It is the only field FHIR gives a target here that a
// human reads, and dropping it would make a table of codes like IMP and OBSENC unreadable to anybody who does not already know them.
//
// An entry mapping to an empty code is recorded as deliberately unmapped rather than dropped. Dropping it would make the code look
// unrecognised, when in fact it is recognised and understood to carry no usable value.
func fromCodeDisplay(name, describes, source string, m map[string]struct{ Code, Display string }) *codeset.Table {
	from := make([]string, 0, len(m))
	for k := range m {
		from = append(from, k)
	}
	sort.Strings(from)

	t := &codeset.Table{
		Name:      BuiltinTablePrefix + name,
		Describes: describes,
		Source:    source,
		DecidedBy: "built in to Perfuse",
	}

	for _, k := range from {
		v := m[k]

		why := v.Display
		if v.Code == "" {
			why = "the code means unknown or not applicable, and choosing a value would assert something " +
				"the sender did not say"
		}

		t.Entries = append(t.Entries, codeset.Entry{From: k, To: v.Code, Why: why})
	}
	t.Compile()

	return t
}
