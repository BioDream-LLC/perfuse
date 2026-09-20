package mirth

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// ParseChannelFile reads a Mirth channel export from disk.
func ParseChannelFile(path string) (*Channel, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	c, err := ParseChannel(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return c, nil
}

// ParseChannel reads a Mirth channel export.
//
// It does not fail on constructs it does not know. Unrecognised elements are
// collected on the Channel and Connector so callers can report them.
func ParseChannel(r io.Reader) (*Channel, error) {
	root, err := parseTree(r)
	if err != nil {
		return nil, err
	}
	if root.Name != "channel" {
		return nil, fmt.Errorf("mirth: expected root element <channel>, found <%s>", root.Name)
	}

	c := &Channel{
		ID:                   root.str("id"),
		Name:                 root.str("name"),
		Description:          root.str("description"),
		Revision:             root.intAt("revision"),
		PreprocessingScript:  root.str("preprocessingScript"),
		PostprocessingScript: root.str("postprocessingScript"),
		DeployScript:         root.str("deployScript"),
		UndeployScript:       root.str("undeployScript"),
	}
	if root.Attr != nil {
		c.MirthVersion = root.Attr["version"]
	}

	// Mirth has moved the enabled flag between the channel and the source
	// connector across versions; accept it in either place.
	if n := root.child("enabled"); n != nil {
		c.Enabled = strings.EqualFold(n.Text, "true")
	} else {
		c.Enabled = root.boolAt("sourceConnector", "enabled")
	}

	if p := root.child("properties"); p != nil {
		c.Properties = parseChannelProperties(p)

		// Kept for the same reason as the connector's: ChannelProperties models the settings that change behaviour, and Mirth's
		// document carries a dozen more it will not do without - attachmentProperties, metaDataColumns, initialState,
		// encryptAttachments. Modelling all of them would be a second copy of Mirth's schema to keep in step with; keeping the subtree
		// costs nothing and cannot drift.
		c.rawProperties = p
	}

	if src := root.child("sourceConnector"); src != nil {
		c.Source = parseConnector(src, ModeSource)
	}
	// Destinations live under <destinationConnectors> as repeated <connector>.
	if dests := root.child("destinationConnectors"); dests != nil {
		for _, dn := range dests.children("connector") {
			c.Destinations = append(c.Destinations, parseConnector(dn, ModeDestination))
		}
	}

	c.Unrecognised = unrecognised(root, knownChannelChildren)
	return c, nil
}

var knownChannelChildren = set(
	"id", "nextMetaDataId", "name", "description", "revision", "enabled",
	"lastModified", "sourceConnector", "destinationConnectors",
	"preprocessingScript", "postprocessingScript", "deployScript",
	"undeployScript", "properties", "exportData",
)

var knownConnectorChildren = set(
	"metaDataId", "name", "properties", "transformer", "responseTransformer",
	"filter", "transportName", "mode", "enabled", "waitForPrevious",
)

func parseChannelProperties(p *node) ChannelProperties {
	cp := ChannelProperties{
		MessageStorageMode:      p.str("messageStorageMode"),
		EncryptData:             p.boolAt("encryptData"),
		RemoveContentOnComplete: p.boolAt("removeContentOnCompletion"),
		StoreAttachments:        p.boolAt("storeAttachments"),
		PruneMetaDataDays:       p.intAt("pruneMetaDataDays"),
		PruneContentDays:        p.intAt("pruneContentDays"),
		ClearGlobalChannelMap:   p.boolAt("clearGlobalChannelMap"),
	}
	if ids := p.child("resourceIds"); ids != nil {
		for _, k := range ids.Kids {
			if k.Text != "" {
				cp.ResourceIDs = append(cp.ResourceIDs, k.Text)
			}
		}
	}
	return cp
}

func parseConnector(n *node, mode ConnectorMode) Connector {
	conn := Connector{
		MetaDataID:      n.intAt("metaDataId"),
		Name:            n.str("name"),
		Mode:            mode,
		Enabled:         n.boolAt("enabled"),
		Transport:       n.str("transportName"),
		WaitForPrevious: n.boolAt("waitForPrevious"),
		Properties:      map[string]string{},
	}
	if declared := n.str("mode"); declared != "" {
		conn.Mode = ConnectorMode(strings.ToUpper(declared))
	}
	if props := n.child("properties"); props != nil {
		conn.PropertiesClass = props.class()
		conn.Properties = props.leaves()

		// The subtree is kept as it arrived, attributes and order included.
		//
		// Properties is flattened to dotted paths and loses everything an attribute carries. That is fine for reading a channel and
		// useless for writing one back, because the attributes are exactly what Mirth requires: resourceIds needs
		// class="linked-hash-map", and every nested properties element carries its own version. An exporter working from the flattened
		// map cannot put them back, which is why Mirth threw away every channel this package exported until the round trip was
		// actually tried against a server.
		conn.rawProperties = props
	}
	if t := n.child("transformer"); t != nil {
		conn.Transformer = parseTransformer(t)
		conn.rawTransformer = t
	}
	if f := n.child("filter"); f != nil {
		conn.Filter = parseFilter(f)
		conn.rawFilter = f
	}
	conn.Unrecognised = unrecognised(n, knownConnectorChildren)
	return conn
}

func parseTransformer(t *node) Transformer {
	tr := Transformer{
		InboundDataType:  t.str("inboundDataType"),
		OutboundDataType: t.str("outboundDataType"),
	}
	if els := t.child("elements"); els != nil {
		for _, sn := range els.Kids {
			tr.Steps = append(tr.Steps, parseStep(sn))
		}
	}
	return tr
}

func parseStep(sn *node) Step {
	s := Step{
		Sequence: sn.intAt("sequenceNumber"),
		Name:     sn.str("name"),
		RawKind:  sn.Name,
		Kind:     classifyStep(sn.Name),
	}

	// Step settings sit directly on the element in some versions and under a
	// <properties> child in others.
	fields := sn
	if p := sn.child("properties"); p != nil && len(p.Kids) > 0 {
		fields = p
	}

	switch s.Kind {
	case StepMapper:
		s.Variable = fields.str("variable")
		s.Mapping = fields.str("mapping")
	case StepJavaScript, StepMessageBuilder:
		s.Script = fields.str("script")
		if s.Kind == StepMessageBuilder {
			s.Variable = fields.str("messageSegment")
			s.Mapping = fields.str("mapping")
		}
	case StepXSLT:
		s.Script = firstNonEmpty(fields.str("xsltTemplate"), fields.str("template"))
	case StepExternalScript:
		s.Script = firstNonEmpty(fields.str("scriptPath"), fields.str("script"))
	case StepIterator:
		if kids := fields.child("children"); kids != nil {
			for _, k := range kids.Kids {
				s.Children = append(s.Children, parseStep(k))
			}
		}
	default:
		// Keep whatever script-like text an unknown plugin carried, so the
		// operator can still see what it was doing.
		s.Script = fields.str("script")
	}
	return s
}

func parseFilter(f *node) Filter {
	var out Filter
	if els := f.child("elements"); els != nil {
		for _, rn := range els.Kids {
			out.Rules = append(out.Rules, parseRule(rn))
		}
	}
	return out
}

func parseRule(rn *node) Rule {
	r := Rule{
		Sequence: rn.intAt("sequenceNumber"),
		Name:     rn.str("name"),
		RawKind:  rn.Name,
		Kind:     classifyRule(rn.Name),
		Operator: rn.str("operator"),
	}

	fields := rn
	if p := rn.child("properties"); p != nil && len(p.Kids) > 0 {
		fields = p
	}
	if r.Operator == "" {
		r.Operator = fields.str("operator")
	}

	switch r.Kind {
	case RuleBuilder:
		r.Field = fields.str("field")
		r.Condition = fields.str("condition")
		if vals := fields.child("values"); vals != nil {
			for _, v := range vals.Kids {
				if v.Text != "" {
					r.Values = append(r.Values, v.Text)
				}
			}
		}
	case RuleJavaScript:
		r.Script = fields.str("script")
	case RuleExternalScript:
		r.Script = firstNonEmpty(fields.str("scriptPath"), fields.str("script"))
	case RuleIterator:
		if kids := fields.child("children"); kids != nil {
			for _, k := range kids.Kids {
				r.Children = append(r.Children, parseRule(k))
			}
		}
	default:
		r.Script = fields.str("script")
	}
	return r
}

// classifyStep normalises a plugin class name to a StepKind by its suffix, so
// that a package rename between Mirth versions does not break recognition.
func classifyStep(elem string) StepKind {
	switch short := lastSegment(elem); {
	case strings.EqualFold(short, "MapperStep"):
		return StepMapper
	case strings.EqualFold(short, "JavaScriptStep"):
		return StepJavaScript
	case strings.EqualFold(short, "MessageBuilderStep"):
		return StepMessageBuilder
	case strings.EqualFold(short, "XsltStep"):
		return StepXSLT
	case strings.EqualFold(short, "ExternalScriptStep"):
		return StepExternalScript
	case strings.EqualFold(short, "IteratorStep"):
		return StepIterator
	default:
		return StepUnknown
	}
}

func classifyRule(elem string) RuleKind {
	switch short := lastSegment(elem); {
	case strings.EqualFold(short, "RuleBuilderRule"):
		return RuleBuilder
	case strings.EqualFold(short, "JavaScriptRule"):
		return RuleJavaScript
	case strings.EqualFold(short, "ExternalScriptRule"):
		return RuleExternalScript
	case strings.EqualFold(short, "IteratorRule"):
		return RuleIterator
	default:
		return RuleUnknown
	}
}

// lastSegment returns the text after the final dot, which turns
// "com.mirth.connect.plugins.mapper.MapperStep" into "MapperStep".
func lastSegment(s string) string {
	if i := strings.LastIndex(s, "."); i >= 0 && i+1 < len(s) {
		return s[i+1:]
	}
	return s
}

func unrecognised(n *node, known map[string]bool) []string {
	var out []string
	seen := map[string]bool{}
	for _, name := range n.childNames() {
		if known[name] || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}
