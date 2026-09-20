package mirth

import (
	"fmt"
	"io"
	"strings"
)

// Building a connector from a property document Mirth itself serialised.
//
// Perfuse writes Mirth XML for channels that were authored in Perfuse, and the property blocks cannot be composed here. XStream reads the
// class and version attributes as instructions and drops an element it does not recognise without saying so, so a block written from a
// reading of the format is stored as "This channel is invalid. Verify all required extensions are loaded correctly" with nothing naming
// the element at fault.
//
// The blocks therefore come from Mirth: scripts/mirth-dump-connector-templates.sh has Mirth's own ObjectXMLSerializer write the defaults
// for each connector properties class, and those documents are committed. What happens here is substitution into them, and the important
// property of SetRawProperty is that it refuses a path the template does not already have. A caller cannot invent an element, which is
// the only mistake in this area that is expensive to find.

// SetPropertiesXML attaches a properties document to the connector.
//
// The document is what XStream writes for a bare properties object, whose root element is the fully qualified class name. Inside a
// channel the same block appears as <properties class="..."> with the class as an attribute, so the root is renamed and the class moved.
func (c *Connector) SetPropertiesXML(r io.Reader) error {
	root, err := parseTree(r)
	if err != nil {
		return fmt.Errorf("mirth: parsing the properties template: %w", err)
	}

	// The root element name is the Java class. Anything else means the template is not what this expects, and continuing would produce a
	// properties block with no class attribute - one of the shapes Mirth discards silently.
	if !strings.Contains(root.Name, ".") {
		return fmt.Errorf("mirth: properties template root is %q, expected a fully qualified class name", root.Name)
	}

	class := root.Name
	root.Name = "properties"
	if root.Attr == nil {
		root.Attr = map[string]string{}
	}
	root.Attr["class"] = class

	c.PropertiesClass = class
	c.rawProperties = root

	// Properties is kept in step so the rest of this package, which reads the flattened form, sees the same connector.
	c.Properties = root.leaves()

	return nil
}

// SetRawProperty sets a leaf inside the attached properties document, addressed by dotted path.
//
// It is an error for the path not to exist. That is the point: Mirth's own default document lists every element the connector has, so a
// path that is not there is a path Mirth does not know, and writing it anyway produces a channel the server throws away while reporting
// nothing useful. Failing here names the path instead.
func (c *Connector) SetRawProperty(path, value string) error {
	if c.rawProperties == nil {
		return fmt.Errorf("mirth: no properties document on connector %q; call SetPropertiesXML first", c.Name)
	}

	n := c.rawProperties
	segments := strings.Split(path, ".")

	for i, seg := range segments {
		kid := n.child(seg)
		if kid == nil {
			return fmt.Errorf("mirth: connector %q (%s) has no property %q: Mirth's own default document for this connector "+
				"does not contain %q, so setting it would produce a channel Mirth discards",
				c.Name, shortClass(c.PropertiesClass), path, strings.Join(segments[:i+1], "."))
		}
		n = kid
	}

	if len(n.Kids) != 0 {
		return fmt.Errorf("mirth: property %q on connector %q is a group of %d elements, not a value",
			path, c.Name, len(n.Kids))
	}

	n.Text = value

	// The flattened view has to follow, or a caller reading Properties after a write sees the old value.
	c.Properties = c.rawProperties.leaves()

	return nil
}

// HasRawProperty reports whether the attached document has a leaf at the path, so a caller can adapt to a connector that differs between
// Mirth versions rather than failing.
func (c *Connector) HasRawProperty(path string) bool {
	if c.rawProperties == nil {
		return false
	}
	n := c.rawProperties
	for _, seg := range strings.Split(path, ".") {
		if n = n.child(seg); n == nil {
			return false
		}
	}
	return len(n.Kids) == 0
}

// shortClass trims a Java class name to its final segment, for error messages that have to be read in a hurry.
func shortClass(class string) string {
	if i := strings.LastIndexByte(class, '.'); i >= 0 {
		return class[i+1:]
	}
	return class
}

// SetPropertiesXML attaches the channel-level properties document.
//
// Mirth's ChannelProperties serialises as <channelProperties>, and inside a channel the same block is <properties> with no class
// attribute. The elements matter more than they look: a channel missing metaDataColumns, attachmentProperties or initialState is one
// Mirth discards, and those are exactly the ones a reconstruction from a model leaves out.
func (c *Channel) SetPropertiesXML(r io.Reader) error {
	root, err := parseTree(r)
	if err != nil {
		return fmt.Errorf("mirth: parsing the channel properties template: %w", err)
	}

	root.Name = "properties"
	c.rawProperties = root

	return nil
}

// SetTransformerXML attaches a transformer document to the connector, and SetFilterXML a filter.
//
// Both come from Mirth's own serialisation of an empty Transformer and Filter. Writing them from this side added inboundDataType and
// outboundDataType elements that Mirth's own output does not have.
func (c *Connector) SetTransformerXML(r io.Reader) error {
	root, err := parseTree(r)
	if err != nil {
		return fmt.Errorf("mirth: parsing the transformer template: %w", err)
	}
	root.Name = "transformer"
	c.rawTransformer = root
	return nil
}

// SetFilterXML attaches a filter document. See SetTransformerXML.
func (c *Connector) SetFilterXML(r io.Reader) error {
	root, err := parseTree(r)
	if err != nil {
		return fmt.Errorf("mirth: parsing the filter template: %w", err)
	}
	root.Name = "filter"
	c.rawFilter = root
	return nil
}
