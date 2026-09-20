package mirth

import (
	"encoding/xml"
	"fmt"
	"io"
	"sort"
	"strings"
)

// defaultMirthVersion is the version stamped on a channel that did not come from Mirth.
//
// 4.5.2 is the version this exporter is verified against, by importing its output into a running Mirth 4.5.2 and checking the server
// did not quietly replace the channel with "This channel is invalid".
const defaultMirthVersion = "4.5.2"

// ExportResult reports what happened during an export.
type ExportResult struct {
	// Warnings lists elements or features that exist on the Channel struct but
	// could not be represented in Mirth XML. A non-empty list means the export
	// is lossy: something will be lost if this file is imported into Mirth.
	Warnings []string
}

// ExportWithWarnings is like Export but also returns an ExportResult reporting
// any lossy conversion: features the channel has that Mirth XML cannot express.
// A caller that cares about silent data loss (which should be all of them)
// should use this rather than Export.
func ExportWithWarnings(w io.Writer, ch *Channel) (ExportResult, error) {
	var result ExportResult

	// Collect warnings for anything that will be dropped.
	for _, u := range ch.Unrecognised {
		result.Warnings = append(result.Warnings, fmt.Sprintf("channel element %q has no Mirth XML equivalent and was not exported", u))
	}
	for _, conn := range ch.AllConnectors() {
		for _, u := range conn.Unrecognised {
			result.Warnings = append(result.Warnings, fmt.Sprintf("connector %q element %q has no Mirth XML equivalent and was not exported", conn.Name, u))
		}
	}

	err := Export(w, ch)
	return result, err
}

// Export writes a Mirth channel XML file from a Channel struct.
//
// This is the reverse of ParseChannel: it produces XML that Mirth Connect (4.x)
// and Open Integration Engine can import. The output includes the Java class
// attributes that Mirth requires on properties elements — without those, the
// import silently fails or rejects the file.
//
// Limitations:
//   - Only connectors whose PropertiesClass we recognise get full property export.
//     Unknown connector types get their properties map written as flat elements,
//     which Mirth may or may not accept depending on the plugin.
//   - Iterator steps and rules are exported but complex nesting may not survive
//     round-trip through all Mirth versions.
//
// The output targets Mirth 4.5.x / OIE 4.6.x format.
func Export(w io.Writer, ch *Channel) error {
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")

	// XML declaration.
	if _, err := io.WriteString(w, xml.Header); err != nil {
		return err
	}

	// One version for the whole document, used on the root and on every connector.
	//
	// defaultMirthVersion is what a channel authored in Perfuse gets. It is a real released version rather than a round number: XStream
	// matches on it, and 4.5.0 was being written into documents that claimed to be 4.5.2 elsewhere in the same file.
	docVersion := ch.MirthVersion
	if docVersion == "" {
		docVersion = defaultMirthVersion
	}

	start := xml.StartElement{Name: xml.Name{Local: "channel"}}
	start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "version"}, Value: docVersion})

	if err := enc.EncodeToken(start); err != nil {
		return err
	}

	writeElem(enc, "id", ch.ID)
	writeElem(enc, "nextMetaDataId", fmt.Sprintf("%d", nextMetaDataID(ch)))
	writeElem(enc, "name", ch.Name)
	writeElem(enc, "description", ch.Description)
	writeElem(enc, "revision", fmt.Sprintf("%d", ch.Revision))
	// No enabled element on the channel, deliberately.
	//
	// Mirth's Channel model has no setter for one and its serialiser writes none, so an element here is one XStream does not recognise -
	// and its answer to that is to discard the channel and store "This channel is invalid", naming nothing. This was already written
	// down: the comment on real_channel_from_mirth.xml has said "Channel has no enabled element at all" since the fixture was made. It
	// stayed written down and never reached this function, which is why every channel this package exported was thrown away. Enabled is
	// still read on import, because Mirth's older exports do carry it.
	_ = ch.Enabled

	// Source connector.
	writeConnector(enc, ch.Source, docVersion)

	// Destination connectors.
	enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "destinationConnectors"}})
	for _, d := range ch.Destinations {
		writeConnector(enc, d, docVersion)
	}
	enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "destinationConnectors"}})

	// Scripts.
	writeElem(enc, "preprocessingScript", ch.PreprocessingScript)
	writeElem(enc, "postprocessingScript", ch.PostprocessingScript)
	writeElem(enc, "deployScript", ch.DeployScript)
	writeElem(enc, "undeployScript", ch.UndeployScript)

	// Channel properties.
	if ch.rawProperties != nil {
		writeNode(enc, ch.rawProperties)
	} else {
		writeChannelProperties(enc, ch.Properties)
	}

	enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "channel"}})
	enc.Flush()

	// Trailing newline.
	_, err := io.WriteString(w, "\n")
	return err
}

func writeConnector(enc *xml.Encoder, conn Connector, version string) {
	elemName := "connector"
	if conn.Mode == ModeSource {
		elemName = "sourceConnector"
	}

	// Every connector carries the version, source and destination alike, and it is the document's version rather than a constant. A
	// destination written without one was the other half of why Mirth emptied destinationConnectors.
	start := xml.StartElement{Name: xml.Name{Local: elemName}}
	start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: "version"}, Value: version})
	enc.EncodeToken(start)

	writeElem(enc, "metaDataId", fmt.Sprintf("%d", conn.MetaDataID))
	writeElem(enc, "name", conn.Name)
	writeConnectorProperties(enc, conn)
	if conn.rawTransformer != nil {
		writeNode(enc, conn.rawTransformer)
	} else {
		writeTransformer(enc, conn.Transformer)
	}
	if conn.rawFilter != nil {
		writeNode(enc, conn.rawFilter)
	} else {
		writeFilter(enc, conn.Filter)
	}
	writeElem(enc, "transportName", conn.Transport)
	writeElem(enc, "mode", string(conn.Mode))
	writeElem(enc, "enabled", boolStr(conn.Enabled))
	writeElem(enc, "waitForPrevious", boolStr(conn.WaitForPrevious))

	enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: elemName}})
}

// writeNode writes a parsed subtree back out exactly as it came in, attributes and child order included.
//
// Needed because Mirth's XML is XStream's output and XStream reads the attributes as instructions: class names it back to a Java type,
// version tells it which form of that type to expect. An element written without them is an element Mirth discards, silently, keeping
// the channel and losing the connector.
func writeNode(enc *xml.Encoder, n *node) {
	start := xml.StartElement{Name: xml.Name{Local: n.Name}}

	// Sorted, because Attr is a map and Go ranges maps randomly. Two exports of one channel have to be the same bytes or a diff
	// between them says things changed that did not.
	keys := make([]string, 0, len(n.Attr))
	for k := range n.Attr {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: k}, Value: n.Attr[k]})
	}

	enc.EncodeToken(start)

	// Text and children are exclusive in this format, and a leaf's text is written even when empty: Mirth writes <remoteAddress></remoteAddress>
	// and an element that disappears is not the same as an element that is blank.
	if len(n.Kids) == 0 {
		if n.Text != "" {
			enc.EncodeToken(xml.CharData(n.Text))
		}
	} else {
		for _, kid := range n.Kids {
			writeNode(enc, kid)
		}
	}

	enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: n.Name}})
}

func writeConnectorProperties(enc *xml.Encoder, conn Connector) {
	// A channel that came from Mirth goes back as Mirth wrote it.
	//
	// The synthesised path below reverses the dotted-path flattening, which reconstructs the element names and none of the attributes.
	// Mirth accepts the result only by accident. When the original subtree is available there is no reason to guess.
	if conn.rawProperties != nil {
		writeNode(enc, conn.rawProperties)
		return
	}

	start := xml.StartElement{Name: xml.Name{Local: "properties"}}
	if conn.PropertiesClass != "" {
		start.Attr = append(start.Attr, xml.Attr{
			Name: xml.Name{Local: "class"}, Value: conn.PropertiesClass,
		})
	}
	enc.EncodeToken(start)

	// The importer flattens nested XML into dotted paths (e.g.
	// "listenerConnectorProperties.port"). The exporter must reverse this:
	// group by the first segment and emit nested elements.
	writeNestedProps(enc, sortedProps(conn.Properties))

	enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "properties"}})
}

// writeNestedProps takes sorted key-value pairs where keys may contain dots
// (indicating nested XML elements) and writes them as properly nested XML.
func writeNestedProps(enc *xml.Encoder, pairs []kv) {
	// Group by first path segment. Maintain insertion order from sorted pairs.
	type group struct {
		prefix string
		items  []kv // each item has the prefix stripped
	}
	var groups []group
	groupIdx := map[string]int{}

	for _, p := range pairs {
		dot := strings.IndexByte(p.k, '.')
		if dot < 0 {
			// Top-level property: emit directly.
			writeElem(enc, p.k, p.v)
			continue
		}
		prefix := p.k[:dot]
		rest := p.k[dot+1:]
		if idx, ok := groupIdx[prefix]; ok {
			groups[idx].items = append(groups[idx].items, kv{rest, p.v})
		} else {
			groupIdx[prefix] = len(groups)
			groups = append(groups, group{prefix: prefix, items: []kv{{rest, p.v}}})
		}
	}

	// Emit each group as a nested element.
	for _, g := range groups {
		enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: g.prefix}})
		// Recursively handle deeper nesting (e.g. a.b.c).
		writeNestedProps(enc, g.items)
		enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: g.prefix}})
	}
}

func writeTransformer(enc *xml.Encoder, t Transformer) {
	enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "transformer"}})

	writeElem(enc, "inboundDataType", t.InboundDataType)
	writeElem(enc, "outboundDataType", t.OutboundDataType)

	if len(t.Steps) > 0 {
		enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "elements"}})
		for _, s := range t.Steps {
			writeStep(enc, s)
		}
		enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "elements"}})
	}

	enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "transformer"}})
}

func writeStep(enc *xml.Encoder, s Step) {
	elemName := s.RawKind
	if elemName == "" {
		elemName = stepKindToElement(s.Kind)
	}

	enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: elemName}})
	writeElem(enc, "sequenceNumber", fmt.Sprintf("%d", s.Sequence))
	writeElem(enc, "name", s.Name)

	switch s.Kind {
	case StepMapper:
		writeElem(enc, "variable", s.Variable)
		writeElem(enc, "mapping", s.Mapping)
	case StepJavaScript:
		writeElem(enc, "script", s.Script)
	case StepMessageBuilder:
		writeElem(enc, "messageSegment", s.Variable)
		writeElem(enc, "mapping", s.Mapping)
		writeElem(enc, "script", s.Script)
	case StepXSLT:
		writeElem(enc, "xsltTemplate", s.Script)
	case StepExternalScript:
		writeElem(enc, "scriptPath", s.Script)
	case StepIterator:
		if len(s.Children) > 0 {
			enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "children"}})
			for _, c := range s.Children {
				writeStep(enc, c)
			}
			enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "children"}})
		}
	default:
		if s.Script != "" {
			writeElem(enc, "script", s.Script)
		}
	}

	enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: elemName}})
}

func writeFilter(enc *xml.Encoder, f Filter) {
	enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "filter"}})

	if len(f.Rules) > 0 {
		enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "elements"}})
		for _, r := range f.Rules {
			writeRule(enc, r)
		}
		enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "elements"}})
	}

	enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "filter"}})
}

func writeRule(enc *xml.Encoder, r Rule) {
	elemName := r.RawKind
	if elemName == "" {
		elemName = ruleKindToElement(r.Kind)
	}

	enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: elemName}})
	writeElem(enc, "sequenceNumber", fmt.Sprintf("%d", r.Sequence))
	writeElem(enc, "name", r.Name)
	if r.Operator != "" {
		writeElem(enc, "operator", r.Operator)
	}

	switch r.Kind {
	case RuleBuilder:
		writeElem(enc, "field", r.Field)
		writeElem(enc, "condition", r.Condition)
		if len(r.Values) > 0 {
			enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "values"}})
			for _, v := range r.Values {
				writeElem(enc, "string", v)
			}
			enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "values"}})
		}
	case RuleJavaScript:
		writeElem(enc, "script", r.Script)
	case RuleExternalScript:
		writeElem(enc, "scriptPath", r.Script)
	case RuleIterator:
		if len(r.Children) > 0 {
			enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "children"}})
			for _, c := range r.Children {
				writeRule(enc, c)
			}
			enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "children"}})
		}
	default:
		if r.Script != "" {
			writeElem(enc, "script", r.Script)
		}
	}

	enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: elemName}})
}

func writeChannelProperties(enc *xml.Encoder, p ChannelProperties) {
	start := xml.StartElement{Name: xml.Name{Local: "properties"}}
	start.Attr = append(start.Attr, xml.Attr{
		Name: xml.Name{Local: "class"}, Value: "com.mirth.connect.model.ChannelProperties",
	})
	enc.EncodeToken(start)

	writeElem(enc, "messageStorageMode", nonEmpty(p.MessageStorageMode, "DEVELOPMENT"))
	writeElem(enc, "encryptData", boolStr(p.EncryptData))
	writeElem(enc, "removeContentOnCompletion", boolStr(p.RemoveContentOnComplete))
	writeElem(enc, "storeAttachments", boolStr(p.StoreAttachments))
	if p.PruneMetaDataDays > 0 {
		writeElem(enc, "pruneMetaDataDays", fmt.Sprintf("%d", p.PruneMetaDataDays))
	}
	if p.PruneContentDays > 0 {
		writeElem(enc, "pruneContentDays", fmt.Sprintf("%d", p.PruneContentDays))
	}
	writeElem(enc, "clearGlobalChannelMap", boolStr(p.ClearGlobalChannelMap))

	if len(p.ResourceIDs) > 0 {
		enc.EncodeToken(xml.StartElement{Name: xml.Name{Local: "resourceIds"}})
		for _, id := range p.ResourceIDs {
			writeElem(enc, "string", id)
		}
		enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "resourceIds"}})
	}

	enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: "properties"}})
}

// ──────────────────────────────────────────────────────────────────────────────
// Helpers
// ──────────────────────────────────────────────────────────────────────────────

func writeElem(enc *xml.Encoder, name, value string) {
	start := xml.StartElement{Name: xml.Name{Local: name}}
	enc.EncodeToken(start)
	enc.EncodeToken(xml.CharData(value))
	enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: name}})
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func nonEmpty(val, fallback string) string {
	if val != "" {
		return val
	}
	return fallback
}

func nextMetaDataID(ch *Channel) int {
	max := 1
	for _, d := range ch.Destinations {
		if d.MetaDataID >= max {
			max = d.MetaDataID + 1
		}
	}
	return max
}

type kv struct {
	k, v string
}

func sortedProps(m map[string]string) []kv {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]kv, len(keys))
	for i, k := range keys {
		out[i] = kv{k, m[k]}
	}
	return out
}

func stepKindToElement(k StepKind) string {
	switch k {
	case StepMapper:
		return "com.mirth.connect.plugins.mapper.MapperStep"
	case StepJavaScript:
		return "com.mirth.connect.plugins.javascriptstep.JavaScriptStep"
	case StepMessageBuilder:
		return "com.mirth.connect.plugins.messagebuilder.MessageBuilderStep"
	case StepXSLT:
		return "com.mirth.connect.plugins.xsltstep.XsltStep"
	case StepExternalScript:
		return "com.mirth.connect.plugins.scriptfilestep.ExternalScriptStep"
	case StepIterator:
		return "com.mirth.connect.plugins.iteratorstep.IteratorStep"
	default:
		return "com.mirth.connect.plugins.unknown.UnknownStep"
	}
}

func ruleKindToElement(k RuleKind) string {
	switch k {
	case RuleBuilder:
		return "com.mirth.connect.plugins.rulebuilder.RuleBuilderRule"
	case RuleJavaScript:
		return "com.mirth.connect.plugins.javascriptrule.JavaScriptRule"
	case RuleExternalScript:
		return "com.mirth.connect.plugins.scriptfilerule.ExternalScriptRule"
	case RuleIterator:
		return "com.mirth.connect.plugins.iteratorrule.IteratorRule"
	default:
		return "com.mirth.connect.plugins.unknown.UnknownRule"
	}
}
