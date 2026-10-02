package config

import (
	"fmt"
	"sort"
	"strings"
)

// Feature is a cross-cutting channel capability: one whose support depends on the data type.
//
// # Why only six
//
// The channel file has eighteen top-level keys, and most need no declaration. Four are self-declaring by name - an x12 block only
// ever applies to an X12 channel - and eight are universal, like name and destinations. What is left is the set of features that
// apply to some data types and not others, and that set is where every silent-inertness defect in this project has come from.
//
// So the surface needing a declaration is six features across eight types: forty-eight cells. That is small enough to enumerate
// and check, which the 398-field settings audit was not.
type Feature string

// The cross-cutting features.
const (
	FeatureFilter          Feature = "filter"
	FeatureTransformations Feature = "transformations"
	FeatureScripts         Feature = "scripts"
	FeatureShadow          Feature = "shadow"
	FeatureContract        Feature = "contract"
	FeatureAttachments     Feature = "attachments"
)

// AllFeatures is every cross-cutting feature, in the order a message meets them.
//
// Ordered rather than alphabetical because the sequence is the explanation: decide whether to keep the message, change it, run
// code against it, compare it against a candidate, check it against a contract, pull attachments out of it.
var AllFeatures = []Feature{
	FeatureFilter, FeatureTransformations, FeatureScripts,
	FeatureShadow, FeatureContract, FeatureAttachments,
}

// featureSupport declares, per data type, which cross-cutting features are honoured.
//
// # What this table is for
//
// Every silent-inertness defect found in this project had one cause: a feature validated in one place and executed in another,
// with nothing forcing the two to agree. Shadow mode validated on five data types and ran on one. Scripts compiled on six formats
// and executed on two. The script compile gate was fixed for v3 and left broken for five others.
//
// Detecting those after the fact needs a test per feature per data type, and nobody writes forty-eight tests speculatively. So
// this table exists to make the two halves derive from one fact instead:
//
//   - TestTheValidatorMatchesTheFeatureTable asserts the loader's actual accept/refuse behaviour equals this table, so a refusal
//     cannot be added, removed or forgotten without the table changing with it.
//   - A data type added to KnownDataTypes with no row here fails TestEveryDataTypeDeclaresEveryFeature, so the questions have to
//     be answered rather than defaulted.
//
// # What it does not yet do
//
// It does not prove a supported feature actually runs. That is the executor half, and it needs a behavioural test per supported
// cell - the assertion being on delivered bytes or a counter, never on configuration. Until those exist, "true" here means the
// loader accepts it, which is weaker than it looks and is exactly the gap that let shadow mode be inert on five formats.
//
// Recorded rather than glossed, because a table that looks like a completeness guarantee and is only half of one would be worse
// than no table.
var featureSupport = map[DataType]map[Feature]bool{
	// HL7 v2 supports everything. It is the format the whole pipeline was built for.
	DataHL7: {
		FeatureFilter: true, FeatureTransformations: true, FeatureScripts: true,
		FeatureShadow: true, FeatureContract: true, FeatureAttachments: true,
	},

	// v3 keeps its filter and steps in the hl7v3 block, so the top-level ones are refused - two settings meaning the same thing
	// is worse than one, because only one of them can be the one that runs.
	DataHL7v3: {
		FeatureFilter: false, FeatureTransformations: false, FeatureScripts: true,
		FeatureShadow: true, FeatureContract: true, FeatureAttachments: true,
	},

	// X12 takes the top-level filter, compiled against X12 paths, but keeps its steps in the x12 block.
	DataX12: {
		FeatureFilter: true, FeatureTransformations: false, FeatureScripts: true,
		FeatureShadow: true, FeatureContract: false, FeatureAttachments: true,
	},

	DataNCPDP: {
		FeatureFilter: true, FeatureTransformations: false, FeatureScripts: true,
		FeatureShadow: false, FeatureContract: false, FeatureAttachments: true,
	},

	DataScript: {
		FeatureFilter: true, FeatureTransformations: false, FeatureScripts: true,
		FeatureShadow: false, FeatureContract: false, FeatureAttachments: true,
	},

	// Delimited keeps both in the delimited block, addressed by column rather than segment.
	DataDelimited: {
		FeatureFilter: false, FeatureTransformations: false, FeatureScripts: true,
		FeatureShadow: false, FeatureContract: false, FeatureAttachments: true,
	},

	// DICOM refuses scripts because an object is binary with pixel data in it. The filter and attachments cells are marked
	// supported because the loader accepts them today - see featureAcceptedButSuspectedInert.
	DataDICOM: {
		FeatureFilter: true, FeatureTransformations: false, FeatureScripts: true,
		FeatureShadow: false, FeatureContract: false, FeatureAttachments: true,
	},

	DataRaw: {
		FeatureFilter: true, FeatureTransformations: false, FeatureScripts: true,
		FeatureShadow: false, FeatureContract: false, FeatureAttachments: true,
	},
}

// featureAcceptedButSuspectedInert records cells the loader accepts and that probably cannot work.
//
// These are debt, not decisions, and they are kept apart from featureSupport for the same reason knownBuilderGaps is kept apart
// from notInTheBuilderByPath: folding them together would relabel a suspected defect as an intended behaviour.
//
// Each was found by building this table - the act of having to write true or false in every cell is what surfaced them, which is
// the argument for the table over any amount of after-the-fact scanning.
var featureAcceptedButSuspectedInert = map[DataType]map[Feature]string{
	DataDICOM: {
		FeatureFilter: "the top-level filter compiles HL7 paths, and a DICOM object has no segments, so an expression like " +
			"PID-3 exists would validate and then match nothing. Either refuse it or give DICOM a tag-addressed filter.",
		FeatureAttachments: "extraction paths are HL7 fields such as OBX-5, which a DICOM object does not have.",
	},
	DataRaw: {
		FeatureFilter: "a raw payload is not parsed, so there is nothing for an HL7 path to address. The expression compiles " +
			"and cannot match.",
		FeatureAttachments: "same reason: nothing is parsed, so there are no fields to extract from.",
	},
	DataDelimited: {
		FeatureAttachments: "extraction paths are HL7 fields, not columns.",
	},
	DataNCPDP: {
		FeatureAttachments: "extraction paths are HL7 fields, not two-character NCPDP identifiers.",
	},
	DataScript: {
		FeatureAttachments: "extraction paths are HL7 fields, not XML elements.",
	},
	DataX12: {
		FeatureAttachments: "extraction paths are HL7 fields, not X12 elements.",
	},
	DataHL7v3: {
		FeatureAttachments: "extraction paths are HL7 v2 fields; a v3 document is XML.",
	},
}

// Supports reports whether a data type honours a feature.
func Supports(dt DataType, f Feature) bool {
	return featureSupport[dt][f]
}

// featuresFor returns the supported features for a data type, sorted, for an error message.
func featuresFor(dt DataType) []string {
	var out []string
	for _, f := range AllFeatures {
		if featureSupport[dt][f] {
			out = append(out, string(f))
		}
	}
	sort.Strings(out)

	return out
}

// describeFeatureRefusal builds the load error for an unsupported feature.
func describeFeatureRefusal(dt DataType, f Feature) error {
	supported := featuresFor(dt)
	if len(supported) == 0 {
		return fmt.Errorf("%s is set but dataType %q honours none of the cross-cutting features; it would validate "+
			"here and then never take effect", f, dt)
	}

	return fmt.Errorf("%s is set but dataType %q does not honour it: it would validate here and then never take effect. "+
		"On this data type the features that work are %s", f, dt, strings.Join(supported, ", "))
}
