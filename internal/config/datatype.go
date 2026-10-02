package config

import (
	"fmt"
	"github.com/biodream-llc/perfuse/internal/eprescribe"
	"github.com/biodream-llc/perfuse/internal/ncpdp"
	"github.com/biodream-llc/perfuse/internal/x12"
	"strings"

	"github.com/biodream-llc/perfuse/internal/dicom"
	"github.com/biodream-llc/perfuse/internal/hl7v3"
)

// DataType is the format of the messages a channel receives.
//
// One data type per channel, describing what arrives. Mirth puts a data type on every
// connector, inbound and outbound, which lets a channel declare that it receives HL7 and
// emits X12 - and then does nothing to make that true. The conversion still has to be
// written by hand in a transformer, so the outbound setting mostly documents an intention
// while the actual behaviour lives somewhere else. Converting between formats here is a
// transformation, and it says so in the transformation list where a reader will find it.
type DataType string

// Supported data types.
const (
	// DataHL7 is HL7 v2.x, and the default when a channel does not say.
	//
	// Defaulted rather than required because every channel written before this existed
	// is HL7, and making the field mandatory would break all of them on upgrade.
	DataHL7 DataType = "hl7"

	// DataX12 is ASC X12 EDI: 837 claims, 835 remittance, 834 enrolment, 270/271
	// eligibility.
	DataX12 DataType = "x12"

	// DataDICOM is an imaging object.
	//
	// Its own type rather than treating a DICOM object as an opaque payload, because
	// almost everything a channel does assumes segments and fields: HL7 filters,
	// declarative transformations, acknowledgements and contracts all mean nothing
	// here. Naming the type lets those be refused at load with an explanation rather
	// than failing per message with something obscure.
	//
	// A DICOM channel relays objects. Metadata can be read for routing - modality,
	// accession number, the study UID - and the object itself passes through
	// unchanged, because re-encoding pixel data is not something this does.
	DataDICOM DataType = "dicom"

	// DataDelimited is a delimited document: CSV, tab-separated, pipe-separated.
	//
	// Mirth's Delimited data type, and it exists for the same reason theirs does: a great deal
	// of healthcare data arrives as a file from an analyser, a bureau service or somebody's
	// export, in a format chosen decades ago by whoever wrote the sending system.
	//
	// Like DICOM, this gets its own type rather than being treated as opaque text, because HL7
	// filters, declarative transformations and acknowledgements all mean nothing on a row of
	// columns - and naming the type lets those be refused at load with a reason.
	DataDelimited DataType = "delimited"

	// DataHL7v3 is HL7 version 3 XML: IHE PIX and PDQ, and the CDA header's world.
	//
	// Its own type rather than being treated as generic XML, for the reason all of
	// these exist: almost everything a channel does assumes v2 segments and fields.
	// But unlike DICOM and delimited data, a v3 channel can filter - the filter
	// language for it addresses elements and attributes rather than segments, and it
	// exists because a v3 value nearly always lives in an attribute.
	//
	// What it cannot do yet is transform. The declarative steps address v2 paths, so
	// they are refused at load with the reason rather than silently doing nothing.
	DataHL7v3 DataType = "hl7v3"

	// DataRaw is a payload Perfuse does not parse at all: a PDF, an image, a proprietary export, a zip.
	//
	// Mirth calls this Raw, and it exists for the same reason there. A channel whose job is to move a file from a
	// share to an SFTP server does not need to understand the file, and requiring it to would rule out most of what a
	// site actually wants to automate.
	//
	// Everything that reads inside a message is refused on a raw channel rather than ignored: filters on segments,
	// declarative transformations, contracts, profiling. Each of those would find no structure and quietly do nothing,
	// which looks identical to working.
	DataRaw DataType = "raw"

	// DataNCPDP is an NCPDP Telecommunication Standard transmission: a pharmacy claim.
	//
	// Its own type rather than a flavour of delimited data, because the separators are non-printable start
	// markers and the transaction header is fixed width with no separators in it at all. A delimited reader
	// pointed at one produces a single enormous field, and a reader that treats the start markers as
	// separators produces an off-by-one in every segment where each value lands under the field before it -
	// and most of those values are still plausible.
	//
	// Everything that reads HL7 fields is refused rather than ignored, for the reason it is on every other
	// non-HL7 type: a segment-addressed rule finds nothing and quietly does nothing.
	DataNCPDP DataType = "ncpdp"

	// DataScript is an NCPDP SCRIPT message: a prescription, refill request or fill notification.
	//
	// XML, and separate from hl7v3 despite both being XML, because the two address entirely different
	// element trees. An hl7v3 filter on a prescription would compile and never match, which is the failure
	// this project spends most of its time refusing to ship.
	DataScript DataType = "script"
)

// HL7v3Options are the settings that only apply to an HL7 v3 channel.
type HL7v3Options struct {
	// Filter excludes messages, using v3 paths.
	//
	// Its own field rather than the channel's ordinary filter, because the two are
	// different languages against different message models. Sharing the field would
	// mean a v2 filter silently never matching on a v3 channel, or a v3 filter
	// failing to parse as v2 - and the second is only better because it fails loudly.
	//
	// A channel that sets both is refused at load, since one of them would have to be
	// ignored and there is no honest way to choose.
	Filter string `yaml:"filter,omitempty"`

	// Acknowledge sends an MCCI_IN000002UV01 acknowledgement back.
	//
	// On by default, unlike X12. A v3 interaction over a synchronous transport expects
	// an acknowledgement - the sending application is generally waiting on one - and a
	// sender that receives nothing will usually retry, which is how a patient gets
	// registered three times.
	//
	// A pointer so that "not set" and "set to false" are different: the default is on,
	// and somebody turning it off should have said so.
	Acknowledge *bool `yaml:"acknowledge,omitempty"`

	// SenderDevice is the device identifier we put in an acknowledgement.
	//
	// Required when acknowledging. A v3 acknowledgement names the device it comes
	// from, and a receiver that does not recognise ours may discard it - which looks
	// exactly like not sending one.
	SenderDevice string `yaml:"sender_device,omitempty"`

	// SenderOID is the root OID for our device identifier.
	SenderOID string `yaml:"sender_oid,omitempty"`

	// Transformations change the content of a v3 document.
	//
	// Its own field rather than the channel's ordinary transformations, for the same
	// reason the filter is: they are different vocabularies against different message
	// models. The v2 steps address segments and fields, which a v3 document does not
	// have, so sharing the field would mean a step that silently did nothing.
	//
	// The step names match the v2 ones wherever the meaning matches, so somebody who
	// has written one channel can write the other. What is different is what v3 needs
	// and v2 cannot express - chiefly that removing a value has three distinct
	// meanings, so clear, nullflavor and remove are three steps rather than one.
	Transformations []hl7v3.Step `yaml:"transformations,omitempty"`

	// compiled holds the prepared steps, populated by Validate.
	//
	// Compiled at load so a bad path or an unrecognised null flavour stops the channel
	// starting. The operator who deployed it is watching then, and will not be watching
	// on the first message that arrives at three the following morning.
	compiled *hl7v3.Steps
}

// Steps returns the compiled v3 transformations, or nil when there are none.
func (h *HL7v3Options) Steps() *hl7v3.Steps {
	if h == nil {
		return nil
	}

	return h.compiled
}

// ShouldAcknowledge reports whether the channel acknowledges. Defaults to true.
func (h *HL7v3Options) ShouldAcknowledge() bool {
	if h == nil || h.Acknowledge == nil {
		return true
	}

	return *h.Acknowledge
}

// FilterExpression returns the v3 filter, or empty.
func (h *HL7v3Options) FilterExpression() string {
	if h == nil {
		return ""
	}

	return strings.TrimSpace(h.Filter)
}

// KnownDataTypes lists the accepted values, for error messages.
var KnownDataTypes = []DataType{DataHL7, DataX12, DataDICOM, DataDelimited, DataHL7v3, DataRaw, DataNCPDP, DataScript}

// shadowRuns names the data types whose handler actually observes a shadow.
//
// Derived from the code rather than from intent: a name here means that format's handler contains the deferred
// observeShadow call and that Runner.diff has a walker for it. Both halves are required, and the second is easy to forget -
// comparing X12 with the HL7 walker finds no MSH and can report the whole interchange as different, which reads as a
// candidate that rewrote every field.
//
// TestEveryShadowingDataTypeActuallyObserves asserts this list against behaviour, so it cannot drift into a promise.
var shadowRuns = map[DataType]bool{
	DataHL7:   true,
	DataHL7v3: true,
	DataX12:   true,
}

// shadowingDataTypes returns the shadow-capable types in a stable order, for an error message.
func shadowingDataTypes() []DataType {
	out := make([]DataType, 0, len(shadowRuns))
	for _, t := range KnownDataTypes {
		if shadowRuns[t] {
			out = append(out, t)
		}
	}

	return out
}

// EnvelopePolicy says what to do when an X12 interchange fails its own envelope check.
//
// Three options rather than a boolean, because the honest answer depends on the trading
// partner. The counts exist to catch a truncated file and the default is to refuse one.
// But some partners' software has generated wrong counts for years in otherwise complete
// files, and a site that has to process those needs a way to say so out loud in the
// configuration rather than by turning validation off wholesale.
type EnvelopePolicy string

// Envelope policies.
const (
	// EnvelopeRequire refuses an interchange whose counts do not add up. The default.
	EnvelopeRequire EnvelopePolicy = "require"
	// EnvelopeWarn processes it and records the fault against the message.
	EnvelopeWarn EnvelopePolicy = "warn"
	// EnvelopeIgnore does not check at all.
	//
	// Named to be uncomfortable. Turning this on throws away the only mechanism X12
	// has for detecting that half a claims file arrived.
	EnvelopeIgnore EnvelopePolicy = "ignore"
)

// KnownEnvelopePolicies lists the accepted values, for error messages.
var KnownEnvelopePolicies = []EnvelopePolicy{EnvelopeRequire, EnvelopeWarn, EnvelopeIgnore}

// NCPDPOptions are the settings that only apply to a pharmacy claim channel.
type NCPDPOptions struct {
	// Transformations are declarative changes applied to each transmission before delivery.
	//
	// The same vocabulary as every other format - set, copy, clear, map, replace, trim, case, each with an optional
	// condition - because the step engine in internal/steps is generic. What differs is the notation: a path is written
	// D1 or 07-D7, using the standard's own two-character field identifiers.
	Transformations []ncpdp.Step `yaml:"transformations,omitempty"`

	// compiled holds the prepared steps, set by validation. Unexported so it cannot come from a file.
	compiled *ncpdp.Steps
}

// Steps returns the compiled transformation steps, or nil when there are none.
func (n *NCPDPOptions) Steps() *ncpdp.Steps {
	if n == nil {
		return nil
	}
	return n.compiled
}

// X12Options are the settings that only apply to an X12 channel.
type X12Options struct {
	// Envelope says what to do when the self-declared counts do not match what is
	// present. Defaults to require.
	Envelope EnvelopePolicy `yaml:"envelope,omitempty"`

	// Split sends one message per transaction set instead of one per file.
	//
	// Off by default. A single 837 can carry hundreds of claims, so turning this on
	// multiplies everything downstream - message counts, acknowledgements, rows, alert
	// thresholds - and that should be a decision somebody made rather than a default
	// they inherited.
	Split bool `yaml:"split,omitempty"`

	// Acknowledge says which acknowledgement to send back: 999, 997, ta1, or none.
	//
	// Which one is a property of the trading partner relationship rather than of the
	// message, so it has to be configured. A 999 supersedes a 997 for HIPAA
	// transactions, but plenty of partners - older payer connections especially - are
	// set up to expect a 997 and will treat a 999 as an unrecognised file. Sending the
	// wrong one is worse than sending none, because it answers a question nobody asked
	// and leaves the real one open.
	//
	// Empty means none, which is the safe default: a partner who is not expecting an
	// acknowledgement and receives one may treat it as an unsolicited interchange.
	Acknowledge string `yaml:"acknowledge,omitempty"`

	// AckSenderID and AckSenderQualifier are our own interchange identifier, becoming
	// ISA06 and ISA05 of any acknowledgement.
	//
	// Required whenever Acknowledge is set, and refused at load otherwise. These must
	// be the values the trading partner has configured for us and there is no way to
	// guess them: an interchange whose ISA06 the partner does not recognise is
	// discarded before anybody reads it, so a wrong value produces silence that looks
	// exactly like not sending anything - and somebody spends a week looking in the
	// wrong place.
	AckSenderID        string `yaml:"ack_sender_id,omitempty"`
	AckSenderQualifier string `yaml:"ack_sender_qualifier,omitempty"`

	// Transformations are declarative changes applied to accepted interchanges.
	//
	// Separate from the channel-level transformations for the same reason the v3 ones are: those address HL7 fields
	// through a parser that reads segment-field-component-subcomponent, and X12 has no subcomponent, treats repeated
	// segments as ordinary rather than exceptional, and is written CLM01 by the people who configure it. A shared
	// parser would have to accept both notations and would then be ambiguous in both.
	//
	// The step names are the same ones the HL7 and v3 steps use, so what an author has already learned carries over.
	Transformations []x12.Step `yaml:"transformations,omitempty"`

	// compiled holds the prepared steps, set by validation. Unexported so it cannot come from a file.
	compiled *x12.Steps
}

// Steps returns the compiled transformations, or nil when there are none.
func (x *X12Options) Steps() *x12.Steps {
	if x == nil {
		return nil
	}
	return x.compiled
}

// KnownAcknowledgements lists the accepted acknowledge values, for error messages.
var KnownAcknowledgements = []string{"none", "999", "997", "ta1"}

// Acknowledgement returns the effective acknowledgement level, or empty for none.
func (x *X12Options) Acknowledgement() string {
	if x == nil {
		return ""
	}
	switch strings.ToLower(strings.TrimSpace(x.Acknowledge)) {
	case "", "none":
		return ""
	case "999":
		return "999"
	case "997":
		return "997"
	case "ta1":
		return "TA1"
	default:
		// Unreachable once validation has run, and validation refuses an unknown value
		// rather than falling back - guessing which acknowledgement a partner wanted is
		// exactly the mistake this setting exists to prevent.
		return ""
	}
}

// EnvelopePolicy returns the effective policy.
func (x *X12Options) Policy() EnvelopePolicy {
	if x == nil || x.Envelope == "" {
		return EnvelopeRequire
	}
	return x.Envelope
}

// ShouldSplit reports whether the channel splits interchanges.
func (x *X12Options) ShouldSplit() bool { return x != nil && x.Split }

// Type returns the effective data type.
func (c *Channel) Type() DataType {
	if c.DataType == "" {
		return DataHL7
	}
	return c.DataType
}

// validateDataType checks the data type and the combinations that go with it.
func (c *Channel) validateDataType() []error {
	var errs []error

	// Shadow mode, checked once here rather than per data type.
	//
	// # Why this is a single check
	//
	// It used to be a refusal written into some branches and absent from others, and the omissions were silent. Channel.handle
	// sets up the shadow observation as a deferred call *after* dispatching to the format-specific handlers, so every non-v2
	// channel returned from its own handler and the comparison never happened. A shadow on a delimited, pharmacy, SCRIPT,
	// DICOM or raw channel therefore validated, appeared in perfuse check, appeared in the interface, and did nothing.
	//
	// One list, derived from which handlers actually observe, means adding a format cannot quietly gain a silent option. If a
	// handler starts observing, its name goes here and the two facts move together.
	// Scripts, checked once here for the same reason as the shadow below: refusals written per format branch left the branches
	// nobody thought about silently permitting something that never ran.
	errs = append(errs, c.validateScriptSlots()...)

	if c.Shadow != nil && !shadowRuns[c.Type()] {
		errs = append(errs, fmt.Errorf("shadow mode is set but dataType is %q, and the %s path does not run a shadow: it "+
			"would validate here and then never compare anything. Shadow works on %s",
			c.Type(), c.Type(), joinDataTypes(shadowingDataTypes())))
	}

	// A script block on anything other than a SCRIPT channel, checked once rather than per arm.
	//
	// The refusals below are written per data type, which means each new block has to be added to every arm and the one that gets
	// forgotten is silently accepted. That is how a script block on an HL7 channel was accepted while an x12 block on the same
	// channel was refused - the check was written beside the case that needed it rather than beside the reason it was needed.
	//
	// This form is the one to copy when the next format arrives: the condition is about the block and the data type, so it holds
	// for every arm without being repeated in any of them. The existing ones are left alone rather than half-migrated, because
	// moving them would change error messages that tests assert on and that is a separate change.

	// Every format block on the wrong data type, from one table rather than one arm at a time. See formatblocks.go for
	// why: the arms below used to carry these, hl7v3 and x12 never carried any, and eight mismatches were accepted.
	errs = append(errs, c.refuseForeignBlocks()...)

	switch c.Type() {
	case DataHL7:
		return errs

	case DataDICOM:
		// Refused rather than ignored. An HL7 transformation on a DICOM channel does not fail obviously - it looks for
		// segments in a binary object, finds none, and quietly does nothing, so somebody spends an afternoon wondering
		// why their mapping has no effect.
		if len(c.Transformations) > 0 {
			errs = append(errs, fmt.Errorf("this channel has %d transformation(s) but dataType is dicom; HL7 field "+
				"transformations cannot apply to an imaging object, and they would silently do nothing rather than "+
				"fail", len(c.Transformations)))
		}
		if c.Contract != nil {
			errs = append(errs, fmt.Errorf("this channel has a contract but dataType is dicom; contracts describe "+
				"HL7 feeds and profiling an imaging object would report nothing"))
		}
		// Refused rather than ignored, for the same reason as the transformations above.
		//
		// Channel.handle dispatches to this format's own path before the preprocessor, filter, transformations and
		// transformer scripts run, so a script configured here validates, compiles, and then never executes. Proven by
		// sending a message to a raw channel whose only script was a bare throw: the message was delivered, a file was
		// written, and the script never ran. The channel reports success and nobody learns the script had no effect.
		//
		// The guard existed for X12 only. It was not extended when the DICOM, delimited, pharmacy and raw paths were added,
		// which is the recurring shape - the check lives next to the first case that needed it rather than next to the
		// reason it is needed.
		return errs

	case DataRaw:
		// Everything that reads inside a message is refused rather than ignored. On a raw channel each of these would
		// find no structure and silently do nothing, which is indistinguishable from working - the channel reports
		// success, the filter never matches, and nobody learns that the rule they wrote has no effect.
		if len(c.Transformations) > 0 {
			errs = append(errs, fmt.Errorf("this channel has %d transformation(s) but dataType is raw. The "+
				"declarative steps address HL7 fields, and a raw payload has none, so they would quietly do "+
				"nothing on every message", len(c.Transformations)))
		}
		if c.Contract != nil {
			errs = append(errs, fmt.Errorf("this channel has a contract but dataType is raw; a contract describes "+
				"the fields of an HL7 feed and there are no fields to describe"))
		}
		// Refused rather than ignored, and for a different reason than the rules above: this one is not about paths
		// finding nothing. Channel.handle dispatches to this format's own path before the preprocessor, filter,
		// transformations and transformer scripts run at all, so a script here validates, compiles, and never executes.
		//
		// Proven by sending a message to a raw channel whose only script was a bare throw: the message was delivered, a
		// file was written, and the script never ran.
		return errs

	case DataNCPDP, DataScript:
		// The pharmacy types, checked together because the refusals are the same and the reason is the same.
		//
		// Nothing here addresses HL7 segments, XML v3 elements or X12 loops, so every one of these rules would
		// compile, find nothing, and quietly have no effect. Which is the failure this project exists to refuse:
		// the channel reports success, the rule never fires, and nobody learns it does nothing.
		what := "an NCPDP claim"
		instead := "the fields of a pharmacy claim are addressed by their two-character identifiers, not by segment and field number"
		if c.Type() == DataScript {
			what = "a SCRIPT prescription"
			instead = "a prescription is an XML document with its own element names, not HL7 segments"
		}
		if len(c.Transformations) > 0 {
			hint := "so they would silently do nothing on every message"
			if c.Type() == DataNCPDP {
				hint = "so move them to ncpdp.transformations, where the paths are read as NCPDP field identifiers"
			}
			errs = append(errs, fmt.Errorf("this channel has %d transformation(s) but dataType is %s. The top-level "+
				"declarative steps address HL7 fields, and %s, %s",
				len(c.Transformations), c.Type(), instead, hint))
		}
		// An ncpdp block on the wrong type is refused by the table; the steps are compiled in validate, where the code
		// sets a map step binds to are in scope.
		if c.Filter != "" {
			if c.Type() == DataNCPDP {
				// Compiled against NCPDP paths. The fields are addressed by the standard's own two-character
				// identifiers, so a filter reads A3 == "B1" for a billing request or 07-D7 for a product code.
				compiled, ferr := ncpdp.ParseFilter(c.Filter)
				if ferr != nil {
					errs = append(errs, fmt.Errorf("filter: %w", ferr))
				} else {
					c.ncpdpFilter = compiled
				}
			} else {
				// Compiled against the prescription as an XML tree, addressed by the same //Element and
				// //Element@attribute grammar v3 and CDA use. The path implementation is shared rather than copied,
				// which is deliberate: v3's attribute-reading bug lived in one operator out of nine, and a second
				// XML format resolving paths for itself would have inherited half the fix.
				compiled, ferr := eprescribe.ParseFilter(c.Filter)
				if ferr != nil {
					errs = append(errs, fmt.Errorf("filter: %w", ferr))
				} else {
					c.scriptFilter = compiled
				}
			}
		}
		if c.Contract != nil {
			errs = append(errs, fmt.Errorf("this channel has a contract but dataType is %s; contracts describe the fields "+
				"of an HL7 v2 feed and profiling %s would report nothing", c.Type(), what))
		}
		// Refused rather than ignored, and for a different reason than the rules above: this one is not about paths
		// finding nothing. Channel.handle dispatches to this format's own path before the preprocessor, filter,
		// transformations and transformer scripts run at all, so a script here validates, compiles, and never executes.
		//
		// Proven by sending a message to a raw channel whose only script was a bare throw: the message was delivered, a
		// file was written, and the script never ran.
		return errs

	case DataDelimited:
		// The HL7-shaped transformations and filter stay refused, because they address HL7 fields and would find no
		// segments in a row of columns. What a delimited channel uses instead is delimited.filter and
		// delimited.transformations, compiled in validate.go against column names.
		if len(c.Transformations) > 0 {
			errs = append(errs, fmt.Errorf("this channel has %d transformation(s) but dataType is delimited; the "+
				"declarative steps address HL7 fields and would silently do nothing on a row of columns. Use "+
				"delimited.transformations, which addresses columns by name or by #position", len(c.Transformations)))
		}
		if c.Filter != "" {
			errs = append(errs, fmt.Errorf("filter is set but dataType is delimited; the filter language understands "+
				"HL7 paths, so it would never match and every message would be dropped. Use delimited.filter, which "+
				"addresses columns by name or by #position"))
		}
		if c.Contract != nil {
			errs = append(errs, fmt.Errorf("this channel has a contract but dataType is delimited; contracts describe "+
				"HL7 feeds"))
		}
		// Refused rather than ignored. Channel.handle dispatches to this format's own path before the preprocessor,
		// filter, transformations and transformer scripts run, so a script here validates, compiles, and never executes.
		errs = append(errs, validateDelimited(c)...)
		return errs

	case DataHL7v3:
		// No script guard here, unlike the other non-v2 paths: handleHL7v3 does run scripts, through ScriptEngine and
		// runTreeScriptStage, in the same order as v2 - declarative steps first, then the script.
		//
		// Worth stating explicitly because it is the exception, and because the first version of the guard below covered
		// v3 too and broke four working tests.
		return append(errs, validateHL7v3(c)...)

	case DataX12:
		// fall through to the X12 checks below

	default:
		return append(errs, fmt.Errorf("dataType %q is not supported; use one of %s", c.DataType, joinDataTypes(KnownDataTypes)))
	}

	if c.X12 != nil {
		switch c.X12.Envelope {
		case "", EnvelopeRequire, EnvelopeWarn, EnvelopeIgnore:
		default:
			errs = append(errs, fmt.Errorf("x12.envelope %q is not supported; use one of %s", c.X12.Envelope, joinPolicies(KnownEnvelopePolicies)))
		}

		// An unknown acknowledgement is refused rather than treated as none. Guessing
		// which one a partner wanted is the mistake this setting exists to prevent, and a
		// typo that silently disabled acknowledgements would look like a working channel
		// until the partner asked why they had never been answered.
		switch strings.ToLower(strings.TrimSpace(c.X12.Acknowledge)) {
		case "", "none", "999", "997", "ta1":
		default:
			errs = append(errs, fmt.Errorf(
				"x12.acknowledge %q is not supported; use one of none, 999, 997, ta1. Which one is a "+
					"property of the trading partner relationship: a 999 supersedes a 997 for HIPAA "+
					"transactions, but many older payer connections expect a 997 and treat a 999 as an "+
					"unrecognised file", c.X12.Acknowledge))
		}

		// Our own identity is required whenever we are going to acknowledge, and refused at
		// load rather than defaulted. An interchange whose ISA06 the partner does not
		// recognise is discarded before anybody reads it, so a guessed value produces
		// silence indistinguishable from not sending anything at all.
		if c.X12.Acknowledgement() != "" {
			if strings.TrimSpace(c.X12.AckSenderID) == "" {
				errs = append(errs, fmt.Errorf(
					"x12.acknowledge is set to %q but x12.ack_sender_id is empty; this is the identifier "+
						"the trading partner has configured for us and there is no way to guess it. An "+
						"interchange they do not recognise is discarded unread, which looks exactly like "+
						"sending nothing", c.X12.Acknowledge))
			}
			if strings.TrimSpace(c.X12.AckSenderQualifier) == "" {
				errs = append(errs, fmt.Errorf(
					"x12.acknowledge is set to %q but x12.ack_sender_qualifier is empty; it goes in ISA05 "+
						"and is usually ZZ for a mutually defined identifier, 01 for a Duns number or 30 "+
						"for a tax identifier", c.X12.Acknowledge))
			}
		}

		// An acknowledgement can only be returned by a source that has somewhere to return it
		// to. This is the "refuse at load rather than skip at run time" rule: a channel whose
		// file says it acknowledges and which silently never does is worse than one that
		// refuses to start, because the trading partner is waiting and the configuration says
		// they should not be.
		//
		// HTTP and SOAP hold a request open, so a real-time 270/271 or a claim submission can
		// be answered in the response body - which is how CAQH CORE real-time transactions
		// work. A file collected from an SFTP drop has no open connection: its acknowledgement
		// has to be generated later and sent as its own interchange, which is a different
		// feature and is not built.
		if c.X12.Acknowledgement() != "" {
			switch c.Source.Type {
			case SourceHTTP, SourceSOAP, SourceMLLP:
			default:
				errs = append(errs, fmt.Errorf(
					"x12.acknowledge is set to %q but the source is %q, which has no open connection to "+
						"return an acknowledgement on. Real-time acknowledgement needs an http, soap or "+
						"mllp source; a batch file collected from %q needs its acknowledgement generated "+
						"and sent back as a separate interchange, which is not built yet. Leaving this "+
						"set would mean the file says the partner is answered and they never are",
					c.X12.Acknowledge, c.Source.Type, c.Source.Type))
			}
		}

		// Acknowledging a split channel would send one acknowledgement per transaction set,
		// and an acknowledgement is a statement about an interchange. A partner receiving
		// several 999s naming the same group has no way to reconcile them.
		if c.X12.Acknowledgement() != "" && c.X12.ShouldSplit() {
			errs = append(errs, fmt.Errorf(
				"x12.split and x12.acknowledge cannot both be set; an acknowledgement is a statement "+
					"about a whole interchange, and splitting would send one per transaction set, which "+
					"a partner cannot reconcile"))
		}
	}

	// What X12 channels cannot do yet.
	//
	// Refused at load rather than ignored at run time. A filter that is present in the
	// file and silently never evaluated is the worst of the options: the configuration
	// says messages are being excluded, the reader believes it, and everything is going
	// through. Saying so here costs one error message and removes a class of incident.
	if c.Filter != "" {
		// Parsed here rather than by the generic filter compilation, which reads HL7 paths. The grammar is the same
		// either way; only the paths differ. Held on the channel rather than in the x12 block, so a filter does not
		// depend on whether the author wrote one.
		compiled, ferr := x12.ParseFilter(c.Filter)
		if ferr != nil {
			errs = append(errs, fmt.Errorf("filter: %w", ferr))
		} else {
			c.x12Filter = compiled
		}
	}
	// The channel-level transformations still do not apply: they address HL7 fields. X12 transformations go in the x12
	// block, where they are parsed as X12 paths, and the message says so rather than only refusing.
	if len(c.Transformations) > 0 {
		errs = append(errs, fmt.Errorf("this channel has %d transformation(s) at the top level, but dataType is x12 and "+
			"those steps address HL7 fields, so they would find nothing. Move them under x12.transformations, where the "+
			"paths are read as X12 - CLM01 or CLM-1 both work", len(c.Transformations)))
	}
	// Shadow mode works on an X12 channel: shadow.DiffX12 compares by segment and element using the same CLM01 paths the
	// filter and the steps use, and handleX12 now observes the shadow. It did not before, and the refusal here was hiding a
	// larger defect - Channel.handle set up the observation after dispatching to the format handlers, so a shadow on any
	// non-v2 channel validated at load and never ran.

	// Transports and destinations that cannot carry X12.
	if c.Source.Type == SourceMLLP {
		// MLLP framing exists to carry HL7, and this codebase answers every MLLP
		// message with an HL7 acknowledgement. An X12 sender would receive an MSA
		// segment it cannot read, and would most likely treat it as a failure and
		// retry for ever.
		errs = append(errs, fmt.Errorf("source type mllp cannot carry X12: MLLP replies with an HL7 acknowledgement, which an X12 sender cannot read; use http or sftp"))
	}

	for i := range c.Destinations {
		d := &c.Destinations[i]

		switch d.Type {
		case DestinationFHIR:
			errs = append(errs, fmt.Errorf("destination %q: the fhir destination maps HL7 v2 to FHIR and cannot take X12", d.Name))
		case DestinationCDA:
			errs = append(errs, fmt.Errorf("destination %q: the cda destination builds a clinical document from HL7 v2 and cannot take X12", d.Name))
		case DestinationMLLP:
			errs = append(errs, fmt.Errorf("destination %q: mllp expects an HL7 acknowledgement in reply, which an X12 receiver will not send", d.Name))
		}
	}

	return errs
}

func joinDataTypes(in []DataType) string {
	parts := make([]string, 0, len(in))
	for _, d := range in {
		parts = append(parts, string(d))
	}
	return strings.Join(parts, ", ")
}

func joinPolicies(in []EnvelopePolicy) string {
	parts := make([]string, 0, len(in))
	for _, p := range in {
		parts = append(parts, string(p))
	}
	return strings.Join(parts, ", ")
}

// validateHL7v3 checks an HL7 v3 channel.
//
// The shape of this follows the X12 and delimited cases: anything a v3 channel cannot honestly do is refused here rather than ignored
// at run time. A filter present in a file and never evaluated is the worst available outcome, because the configuration says messages
// are being excluded, the reader believes it, and everything is going through.
//
// What is different about v3 is that it can filter. That is the point of the path language, so the checks below are about steering
// somebody to the right field rather than telling them filtering is unavailable.
func validateHL7v3(c *Channel) []error {
	var errs []error

	if c.X12 != nil {
		errs = append(errs, fmt.Errorf("x12 options are set but dataType is hl7v3; remove the x12 block"))
	}
	if c.Delimited != nil {
		errs = append(errs, fmt.Errorf("a delimited block is set but dataType is hl7v3; remove it"))
	}

	// The two filter fields are different languages against different message models. One of them would have to be ignored,
	// and there is no honest way to choose which.
	if c.Filter != "" && c.HL7v3.FilterExpression() != "" {
		errs = append(errs, fmt.Errorf(
			"both filter and hl7v3.filter are set; the top-level filter reads HL7 v2 segment paths and "+
				"hl7v3.filter reads v3 elements, so one of them could only be ignored. Keep hl7v3.filter"))
	}

	// A v2 filter on a v3 channel is the mistake somebody makes on their first v3 channel, having copied a working v2 one.
	// It is refused with the translation rather than just refused, because "PID-5 does not work here" leaves them to guess
	// what does.
	if c.Filter != "" {
		errs = append(errs, fmt.Errorf(
			"filter is set, but it reads HL7 v2 segment paths and this is a v3 channel, so it would never "+
				"match and every message would be dropped. Use hl7v3.filter with v3 paths instead - for "+
				"example hl7v3.filter: '//administrativeGenderCode@code == \"F\"'. The field picker on the "+
				"Playground tab will produce a path from one of your own messages"))
	}

	// The top-level transformations are still the wrong field, and this is where somebody finds that out. It is now a
	// redirection rather than a refusal: v3 transformations exist, they just live under hl7v3.
	if len(c.Transformations) > 0 {
		errs = append(errs, fmt.Errorf(
			"this channel has %d transformation(s) at the top level but dataType is hl7v3; those steps address "+
				"HL7 v2 segments and fields, which a v3 document does not have, so they would silently do "+
				"nothing. Move them under hl7v3.transformations, which uses the same step names against v3 "+
				"paths - and note that clear, nullflavor and remove are three different steps in v3 because "+
				"emptying a value, saying why it is missing, and saying it does not apply are three "+
				"different statements", len(c.Transformations)))
	}

	// The v3 steps are compiled in Validate rather than here, because a map step needs the channel's lookup tables and
	// those are loaded after this runs. Compiling them here would have refused every map step for want of a table that
	// was about to exist - which would have been a convincing bug, since the error would have named a real table.
	// Scripts work on a v3 channel now, and the reason they were refused turned out to be narrower than it looked. The
	// script layer already operates on an XML tree because Mirth's scripts do - the v2 path converts a pipe-delimited
	// message into one and back again, and it was that conversion that did not apply. A v3 document is already a tree,
	// so the script sees the document itself and needs no v3-specific API.
	// Contracts work on a v3 channel now that there is a v3 profiler. The refusal here said a contract is judged
	// against a profile and the profiler read v2 segments and fields, which was true and was the whole obstacle: the
	// contract machinery itself never cared whether a path named a segment and field or an element and attribute.
	//
	// So the v3 profiler produces the same report, and Check, Promote and the mapping suggestions all work unchanged.
	// The paths in a v3 expectation are the ones the v3 filter and transformation steps already accept, so an
	// expectation can be pasted between them exactly as it can for v2.
	// Shadow mode works on a v3 channel now that there is a v3 transform path and an XML-aware diff, so it is no
	// longer refused here. The comparison is field by field against the parsed documents rather than textual, because
	// a textual comparison would report every difference in whitespace and attribute order as though it mattered.

	// The filter is parsed here so a mistake in it stops the channel loading. Parsing it lazily would mean a bad path
	// failing on every message once the channel was already running and already trusted.
	if expr := c.HL7v3.FilterExpression(); expr != "" {
		if err := validateV3Filter(expr); err != nil {
			errs = append(errs, fmt.Errorf("hl7v3.filter: %w", err))
		}
	}

	// Acknowledging needs an identity, for the same reason X12 does: a receiver that does not recognise the device an
	// acknowledgement claims to be from may discard it, which looks exactly like never sending one.
	if c.HL7v3.ShouldAcknowledge() {
		if c.HL7v3 == nil || strings.TrimSpace(c.HL7v3.SenderDevice) == "" {
			errs = append(errs, fmt.Errorf(
				"hl7v3.sender_device is required because this channel acknowledges; it names the device the "+
					"acknowledgement comes from, and a receiver that does not recognise it may discard "+
					"the acknowledgement - which looks exactly like sending nothing. Set "+
					"hl7v3.acknowledge: false if this feed genuinely expects no reply"))
		}
		if c.HL7v3 == nil || strings.TrimSpace(c.HL7v3.SenderOID) == "" {
			errs = append(errs, fmt.Errorf(
				"hl7v3.sender_oid is required because this channel acknowledges; a v3 identifier is a root "+
					"OID plus an extension, and the root is what tells the receiver whose numbering "+
					"scheme the device identifier belongs to"))
		}

		// An acknowledgement needs somewhere to go back on. Same rule as X12, and the same reason: a channel whose
		// file says the sender is answered and which never answers is worse than one that refuses to start.
		switch c.Source.Type {
		case SourceHTTP, SourceSOAP:
		default:
			errs = append(errs, fmt.Errorf(
				"hl7v3.acknowledge is on but the source is %q, which has no open connection to reply on. A "+
					"v3 acknowledgement goes back in the response body, so it needs an http or soap "+
					"source. Set hl7v3.acknowledge: false for a feed collected from %q",
				c.Source.Type, c.Source.Type))
		}
	}

	// MLLP carries v2 and this codebase answers every MLLP message with a v2 acknowledgement. A v3 sender would receive an
	// MSA segment it cannot read and would most likely retry for ever.
	if c.Source.Type == SourceMLLP {
		errs = append(errs, fmt.Errorf(
			"source type mllp cannot carry HL7 v3: MLLP replies with a v2 acknowledgement, which a v3 sender "+
				"cannot read; use http or soap"))
	}

	for i := range c.Destinations {
		d := &c.Destinations[i]

		if d.Filter != "" {
			errs = append(errs, fmt.Errorf(
				"destination %q: filter is set, but a destination filter reads HL7 v2 paths; use hl7v3.filter "+
					"on the channel instead, which runs before any destination", d.Name))
		}

		switch d.Type {
		case DestinationFHIR:
			errs = append(errs, fmt.Errorf("destination %q: the fhir destination maps HL7 v2 to FHIR and cannot take v3", d.Name))
		case DestinationCDA:
			errs = append(errs, fmt.Errorf("destination %q: the cda destination builds a document from HL7 v2 and cannot take v3", d.Name))
		case DestinationMLLP:
			errs = append(errs, fmt.Errorf("destination %q: mllp expects an HL7 v2 acknowledgement in reply, which a v3 receiver will not send", d.Name))
		}
	}

	return errs
}

// ScriptOptions configures an NCPDP SCRIPT channel.
//
// # Why SCRIPT gets a block when its filter is top-level
//
// The channel filter for every data type is the top-level filter key, because internal/expr is generic over the message type: one
// key, compiled against whichever tree or claim the channel carries. Steps cannot be arranged that way. A v2 step addresses a
// segment and a field, an X12 step addresses a fixed-width element, and the types are different, so one yaml key would have to hold
// whichever the data type implied - which is a field that means different things depending on another field, and the failure mode is
// a step that silently does nothing.
//
// So this follows hl7v3, x12, ncpdp and delimited: the steps live in the format's own block, and the top-level transformations key
// is refused on a SCRIPT channel rather than accepted and ignored.
type ScriptOptions struct {
	// Transformations are the declarative steps, addressing the prescription as an XML tree.
	//
	// hl7v3.Step rather than a type of its own, and that is not laziness. A prescription and a v3 document are both XML, the path
	// grammar is the same one, and internal/eprescribe already borrows hl7v3.Path for its filter with a comment saying so - v3 and
	// CDA already use it, and a third copy would be a third set of answers to what //a/b[2]@c means.
	//
	// The steps that carry v3-only meaning are refused at load rather than being silently available. nullflavor is the one: it
	// writes the v3 attribute stating why a value is absent, which a prescription has no equivalent of, and a step that wrote it
	// would produce a document the receiver does not understand.
	Transformations []hl7v3.Step `yaml:"transformations,omitempty"`

	// compiled holds the prepared steps, populated by Validate.
	compiled *hl7v3.Steps
}

// Steps returns the compiled SCRIPT transformations, or nil when there are none.
func (s *ScriptOptions) Steps() *hl7v3.Steps {
	if s == nil {
		return nil
	}

	return s.compiled
}

// validateScriptSteps refuses the steps that mean something only in v3.
//
// Named separately from the compile so that the refusal is about the format rather than about whether the step compiles: a
// nullflavor step is perfectly well formed and would apply cleanly, which is exactly why it has to be refused rather than left to
// fail somewhere.
func validateScriptSteps(steps []hl7v3.Step) []error {
	var errs []error

	for i, st := range steps {
		if st.NullFlavor != nil {
			errs = append(errs, fmt.Errorf("script.transformations[%d]: nullflavor states why a v3 value is absent and a "+
				"prescription has no equivalent; use clear to empty the element or remove to delete it", i))
		}
	}

	return errs
}

// DICOMOptions configures an imaging channel.
//
// # Why the steps are named rather than paths
//
// Every other format gets a path writer. DICOM deliberately does not, and the reason is the pixel data: an object is binary, a
// general tag writer can set any tag to any bytes, and a mistake there does not produce a rejected message - it produces an image
// that opens and is wrong. A radiologist reading a study has no way to tell.
//
// So the four things sites actually need are named actions with bounded effects: de-identify, rewrite an AE title, strip private
// tags, set the institution. Each knows which tags it touches, and none can reach the pixel data.
//
// # Why this block exists now
//
// internal/dicom has carried steps.go and transform.go for a while - the four actions, dicom.Apply, and tests. Nothing referenced
// them. Five hundred lines with no caller, which the queue's own section 7 names: an implementation with no caller is
// indistinguishable from a feature that does not exist. This block and the engine call are the wiring, not the feature.
type DICOMOptions struct {
	// Transformations are the named steps, applied in order before delivery.
	Transformations []dicom.Step `yaml:"transformations,omitempty"`

	// compiled holds the validated steps, populated by Validate.
	//
	// Validated at load rather than per object, so a malformed tag in a strip_private keep list stops the channel starting. An
	// imaging channel that fails on the first study of the morning has already lost the study.
	compiled []dicom.Step
}

// Steps returns the validated DICOM transformations, or nil when there are none.
func (d *DICOMOptions) Steps() []dicom.Step {
	if d == nil {
		return nil
	}

	return d.compiled
}
