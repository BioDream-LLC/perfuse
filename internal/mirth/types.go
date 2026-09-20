package mirth

// The types below are Perfuse's reading of a Mirth channel, not a mirror of
// Mirth's object model. Anything we could not place is recorded in Unrecognised
// so the importer can tell an operator exactly what will not survive a
// migration, which is the whole point of reading these files.

// Channel is one imported Mirth channel.
type Channel struct {
	ID          string
	Name        string
	Description string
	Enabled     bool
	Revision    int

	Source       Connector
	Destinations []Connector

	PreprocessingScript  string
	PostprocessingScript string
	DeployScript         string
	UndeployScript       string

	// rawProperties is the channel's properties subtree as parsed, or nil when
	// the channel did not come from Mirth XML. Written back verbatim on export.
	rawProperties *node

	Properties ChannelProperties

	// Unrecognised holds element names we saw but did not map.
	Unrecognised []string

	// MirthVersion is the version attribute on the root element, if present.
	MirthVersion string
}

// ChannelProperties covers the channel-level settings that change behaviour we
// have to reproduce, notably message storage, which drives disk usage.
type ChannelProperties struct {
	MessageStorageMode      string
	EncryptData             bool
	RemoveContentOnComplete bool
	StoreAttachments        bool
	PruneMetaDataDays       int
	PruneContentDays        int
	ClearGlobalChannelMap   bool
	ResourceIDs             []string
}

// ConnectorMode distinguishes the single source from the destinations.
type ConnectorMode string

const (
	ModeSource      ConnectorMode = "SOURCE"
	ModeDestination ConnectorMode = "DESTINATION"
)

// Connector is a source or destination: a transport plus its filter and
// transformer.
type Connector struct {
	MetaDataID int
	Name       string
	Mode       ConnectorMode
	Enabled    bool

	// Transport is Mirth's transportName, e.g. "TCP Listener", "HTTP Sender".
	Transport string

	// PropertiesClass is the concrete Java properties class, which identifies
	// the connector far more reliably than the display name.
	PropertiesClass string

	// rawTransformer and rawFilter are those subtrees as parsed, written back
	// verbatim on export for the same reason as rawProperties: a transformer
	// element carries the data types and their own class attributes, and a
	// reconstruction from the model adds elements Mirth's own document does not
	// have.
	rawTransformer *node
	rawFilter      *node

	// rawProperties is the properties subtree exactly as it was parsed, or nil
	// for a channel that did not come from Mirth XML.
	//
	// Export writes this verbatim when it is present, which makes a Mirth
	// channel survive a trip out through Perfuse and back in. Unexported on
	// purpose: it is a fidelity aid, not part of the model, and nothing outside
	// this package should read a channel's settings from it.
	rawProperties *node

	// Properties is the transport's settings flattened to dotted paths.
	Properties map[string]string

	WaitForPrevious bool

	Filter       Filter
	Transformer  Transformer
	Unrecognised []string
}

// StepKind is the normalised kind of a transformer step.
type StepKind string

const (
	StepMapper         StepKind = "Mapper"
	StepJavaScript     StepKind = "JavaScript"
	StepMessageBuilder StepKind = "MessageBuilder"
	StepXSLT           StepKind = "XSLT"
	StepExternalScript StepKind = "ExternalScript"
	StepIterator       StepKind = "Iterator"
	StepUnknown        StepKind = "Unknown"
)

// Transformer is an ordered list of steps plus the datatypes either side.
type Transformer struct {
	InboundDataType  string
	OutboundDataType string
	Steps            []Step
}

// Step is one transformer step. The fields are a union across step kinds;
// which ones are populated depends on Kind.
type Step struct {
	Sequence int
	Name     string
	Kind     StepKind

	// RawKind is the original XML element name, kept so an unrecognised step
	// can still be reported precisely.
	RawKind string

	// Variable and Mapping are used by Mapper steps.
	Variable string
	Mapping  string

	// Script holds JavaScript for JavaScript steps, the stylesheet for XSLT
	// steps, or the path for ExternalScript steps.
	Script string

	// Children are nested steps, used by Iterator.
	Children []Step
}

// RuleKind is the normalised kind of a filter rule.
type RuleKind string

const (
	RuleBuilder        RuleKind = "RuleBuilder"
	RuleJavaScript     RuleKind = "JavaScript"
	RuleExternalScript RuleKind = "ExternalScript"
	RuleIterator       RuleKind = "Iterator"
	RuleUnknown        RuleKind = "Unknown"
)

// Filter is the accept/reject logic in front of a transformer.
type Filter struct {
	Rules []Rule
}

// Rule is one filter rule.
type Rule struct {
	Sequence int
	Name     string
	Kind     RuleKind
	RawKind  string

	// Operator is how this rule combines with the previous one: NONE, AND, OR.
	Operator string

	// Field, Condition and Values describe a RuleBuilder rule.
	Field     string
	Condition string
	Values    []string

	// Script holds JavaScript for JavaScript rules.
	Script string

	Children []Rule
}

// HasJavaScript reports whether the channel contains any JavaScript at all.
// A channel with none can be translated mechanically; one with any needs the
// JavaScript engine, so this single answer decides how a migration goes.
func (c *Channel) HasJavaScript() bool {
	if c.PreprocessingScript != "" || c.PostprocessingScript != "" ||
		c.DeployScript != "" || c.UndeployScript != "" {
		return true
	}
	for _, conn := range c.AllConnectors() {
		for _, s := range conn.Transformer.Steps {
			if stepTreeHasKind(s, StepJavaScript) {
				return true
			}
		}
		for _, r := range conn.Filter.Rules {
			if ruleTreeHasKind(r, RuleJavaScript) {
				return true
			}
		}
	}
	return false
}

// AllConnectors returns the source followed by every destination.
func (c *Channel) AllConnectors() []Connector {
	out := make([]Connector, 0, len(c.Destinations)+1)
	out = append(out, c.Source)
	out = append(out, c.Destinations...)
	return out
}

func stepTreeHasKind(s Step, kind StepKind) bool {
	if s.Kind == kind {
		return true
	}
	for _, c := range s.Children {
		if stepTreeHasKind(c, kind) {
			return true
		}
	}
	return false
}

func ruleTreeHasKind(r Rule, kind RuleKind) bool {
	if r.Kind == kind {
		return true
	}
	for _, c := range r.Children {
		if ruleTreeHasKind(c, kind) {
			return true
		}
	}
	return false
}
