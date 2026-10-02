// Package tomirth writes a Perfuse channel back out as Mirth channel XML.
//
// Why this exists. The objection to adopting a new integration engine is rarely "is it any good". It is "what if we are wrong and we are
// stuck", and the cheapest answer to that is being able to walk back out. Perfuse has read Mirth's channels since the beginning; until now
// it could not write one, which made the reassurance one-directional and therefore not much of a reassurance.
//
// How it avoids the trap that made the first attempt useless. Mirth's XML is XStream's output, and XStream reads the class and version
// attributes as instructions: an element it does not recognise is dropped without a word, and a channel it cannot assemble is stored with
// its description replaced by "This channel is invalid. Verify all required extensions are loaded correctly" and its destinations
// discarded. Nothing in that response names the element at fault. An exporter that composes property blocks from a reading of the format
// therefore fails in the one way that is expensive to diagnose, which is what happened: this repository carried an exporter with nine
// passing tests whose output Mirth threw away, because every test compared it against our own parser instead of a server.
//
// So none of the property blocks are composed here. scripts/mirth-dump-connector-templates.sh has Mirth's own ObjectXMLSerializer
// serialise the defaults of each connector properties class, and those documents are committed under templates/. This package substitutes
// values into them, and mirth.SetRawProperty refuses any path the template does not already contain - so a mistake is an error naming the
// path, at the moment of export, rather than a channel that vanishes on import.
//
// What it will not do. A transport with no Mirth equivalent is refused by name rather than approximated. An export that quietly turned an
// S3 destination into a file writer would be worse than no export at all, because the channel would import cleanly and deliver to the
// wrong place.
package tomirth

import (
	"bytes"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/mirth"
)

//go:embed templates/*.xml templates/*.transport
var templates embed.FS

// Result is a channel converted to Mirth's model, with everything that did not survive.
type Result struct {
	// Channel is the converted channel, ready for mirth.Export.
	Channel *mirth.Channel

	// Notes are the things a reader has to know before trusting the output: settings Mirth has no equivalent for, and defaults chosen
	// where Perfuse had no opinion. Never nil, so a caller can range over it without checking.
	Notes []string
}

// transport names a Mirth connector template and the properties path its address goes in.
type transport struct {
	// template is the file under templates/, without extension.
	template string

	// hostPath and portPath are where an address belongs, empty when the transport has no address.
	hostPath string
	portPath string
}

// sourceTransports maps a Perfuse source to the Mirth connector that does the same job.
//
// Deliberately incomplete. A source that is not here is refused by name, because a source silently replaced by something else produces a
// channel that imports cleanly and listens on the wrong thing.
var sourceTransports = map[config.SourceType]transport{
	config.SourceMLLP: {
		template: "tcp-listener",
		hostPath: "listenerConnectorProperties.host",
		portPath: "listenerConnectorProperties.port",
	},
	config.SourceTCP: {
		template: "tcp-listener",
		hostPath: "listenerConnectorProperties.host",
		portPath: "listenerConnectorProperties.port",
	},
	config.SourceHTTP: {
		template: "http-listener",
		hostPath: "listenerConnectorProperties.host",
		portPath: "listenerConnectorProperties.port",
	},
	config.SourceFile: {
		template: "file-reader",
		hostPath: "host",
	},
	config.SourceDatabase: {
		template: "db-reader",
	},
	config.SourceDICOM: {
		template: "dicom-listener",
		hostPath: "listenerConnectorProperties.host",
		portPath: "listenerConnectorProperties.port",
	},
}

// destTransports is the same for destinations.
var destTransports = map[config.DestinationType]transport{
	config.DestinationMLLP: {
		template: "tcp-sender",
		hostPath: "remoteAddress",
		portPath: "remotePort",
	},
	config.DestinationTCP: {
		template: "tcp-sender",
		hostPath: "remoteAddress",
		portPath: "remotePort",
	},
	config.DestinationHTTP: {
		template: "http-sender",
		hostPath: "host",
	},
	config.DestinationFile: {
		template: "file-writer",
		hostPath: "host",
	},
	config.DestinationDatabase: {
		template: "db-writer",
	},
	config.DestinationSMTP: {
		template: "smtp-sender",
	},
	config.DestinationSOAP: {
		template: "ws-sender",
	},
	config.DestinationDICOM: {
		template: "dicom-sender",
		hostPath: "host",
		portPath: "port",
	},
	config.DestinationJavaScript: {
		template: "js-writer",
	},
}

// Channel converts a Perfuse channel to Mirth's model.
//
// An error means the channel cannot be represented at all and no file should be offered. Notes on the result mean it can, with losses the
// reader has to see first.
func Channel(ch *config.Channel) (*Result, error) {
	if ch == nil {
		return nil, fmt.Errorf("tomirth: no channel")
	}
	if strings.TrimSpace(ch.Name) == "" {
		return nil, fmt.Errorf("tomirth: the channel has no name, and Mirth identifies channels by name")
	}

	res := &Result{Notes: []string{}}

	out := &mirth.Channel{
		// Mirth keys channels by a UUID and refuses two with the same one. Derived from the name rather than random, so exporting the
		// same channel twice produces the same id and re-importing updates the channel instead of adding a second copy.
		ID:           deterministicUUID(ch.Name),
		Name:         ch.Name,
		Description:  describe(ch, res),
		MirthVersion: mirthVersion,
		Revision:     1,
	}

	props, err := templates.ReadFile("templates/channel-properties.xml")
	if err != nil {
		return nil, fmt.Errorf("tomirth: no channel properties template: %w", err)
	}
	if err := out.SetPropertiesXML(bytes.NewReader(props)); err != nil {
		return nil, err
	}

	src, err := sourceConnector(ch, res)
	if err != nil {
		return nil, err
	}
	out.Source = *src

	for i, d := range ch.Destinations {
		dst, err := destConnector(ch, d, i+1, res)
		if err != nil {
			return nil, err
		}
		out.Destinations = append(out.Destinations, *dst)
	}

	if len(out.Destinations) == 0 {
		return nil, fmt.Errorf("tomirth: channel %q has no destinations, and Mirth will not import a channel without one", ch.Name)
	}

	noteWhatCannotCross(ch, res)

	sort.Strings(res.Notes)

	res.Channel = out

	return res, nil
}

// mirthVersion is the version the templates were taken from and the output is verified against.
const mirthVersion = "4.5.2"

// describe builds the description, which is where the honest account of the conversion goes.
//
// In the description rather than a comment because Mirth's XML has nowhere to put a comment that survives: XStream drops them on import,
// and a warning that disappears when the file is opened is not a warning. Whoever imports this reads the description in the channel list.
func describe(ch *config.Channel, res *Result) string {
	var b strings.Builder

	if ch.Description != "" {
		b.WriteString(ch.Description)
		b.WriteString(" ")
	}
	b.WriteString("[Exported from Perfuse. The transports and their addresses are converted; ")
	b.WriteString("filters, transformations and scripts are not, and are listed in the channel's deploy script as comments. ")
	b.WriteString("Check this channel before starting it.]")

	return b.String()
}

// sourceConnector builds the source from Mirth's own template for the transport.
func sourceConnector(ch *config.Channel, res *Result) (*mirth.Connector, error) {
	t, ok := sourceTransports[ch.Source.Type]
	if !ok {
		return nil, fmt.Errorf("tomirth: a %q source has no Mirth equivalent this can write. Supported: %s",
			ch.Source.Type, supportedSources())
	}

	conn := &mirth.Connector{
		Name:       "sourceConnector",
		Mode:       mirth.ModeSource,
		MetaDataID: 0,
		Enabled:    true,
	}

	if err := applyTemplate(conn, t.template); err != nil {
		return nil, err
	}

	if err := applyAddress(conn, t, ch.Source.Listen, res); err != nil {
		return nil, err
	}

	// Mirth's own empty transformer and filter, rather than ones built from this side.
	if err := applyTransformerAndFilter(conn); err != nil {
		return nil, err
	}

	return conn, nil
}

// destConnector builds one destination.
func destConnector(ch *config.Channel, d config.Destination, metaDataID int, res *Result) (*mirth.Connector, error) {
	t, ok := destTransports[d.Type]
	if !ok {
		return nil, fmt.Errorf("tomirth: destination %q is a %q, which has no Mirth equivalent this can write. Supported: %s",
			d.Name, d.Type, supportedDests())
	}

	name := d.Name
	if name == "" {
		name = fmt.Sprintf("Destination %d", metaDataID)
	}

	conn := &mirth.Connector{
		Name:       name,
		Mode:       mirth.ModeDestination,
		MetaDataID: metaDataID,
		Enabled:    d.Enabled == nil || *d.Enabled,
	}

	if err := applyTemplate(conn, t.template); err != nil {
		return nil, err
	}

	address := d.Address
	if address == "" && d.Dir != "" {
		address = d.Dir
	}
	if err := applyAddress(conn, t, address, res); err != nil {
		return nil, err
	}

	if err := applyTransformerAndFilter(conn); err != nil {
		return nil, err
	}

	if d.Filter != "" {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"destination %q has a filter, which is not converted: Mirth filters are JavaScript or rule trees and Perfuse's are "+
				"expressions. The destination will receive every message until you add the filter in Mirth.", name))
	}

	return conn, nil
}

// applyTemplate attaches Mirth's own serialisation of the connector's defaults.
func applyTemplate(conn *mirth.Connector, name string) error {
	props, err := templates.ReadFile("templates/" + name + ".xml")
	if err != nil {
		return fmt.Errorf("tomirth: no template for %q: %w. Regenerate with ./scripts/mirth-dump-connector-templates.sh", name, err)
	}

	transportName, err := templates.ReadFile("templates/" + name + ".transport")
	if err != nil {
		return fmt.Errorf("tomirth: no transport name for %q: %w", name, err)
	}

	if err := conn.SetPropertiesXML(bytes.NewReader(props)); err != nil {
		return fmt.Errorf("tomirth: %q: %w", name, err)
	}

	// Mirth's own name for the transport, read from the object rather than written here, so it cannot drift from what the server expects.
	conn.Transport = strings.TrimSpace(string(transportName))

	return nil
}

// applyTransformerAndFilter attaches Mirth's own serialisation of an empty transformer and filter.
func applyTransformerAndFilter(conn *mirth.Connector) error {
	t, err := templates.ReadFile("templates/transformer.xml")
	if err != nil {
		return fmt.Errorf("tomirth: no transformer template: %w", err)
	}
	if err := conn.SetTransformerXML(bytes.NewReader(t)); err != nil {
		return err
	}

	f, err := templates.ReadFile("templates/filter.xml")
	if err != nil {
		return fmt.Errorf("tomirth: no filter template: %w", err)
	}
	return conn.SetFilterXML(bytes.NewReader(f))
}

// applyAddress splits a Perfuse address and writes it where the connector keeps it.
func applyAddress(conn *mirth.Connector, t transport, address string, res *Result) error {
	if address == "" || t.hostPath == "" {
		return nil
	}

	host, port := splitAddress(address)

	// A listener written as ":6661" means every interface, which Mirth spells 0.0.0.0. Left as an empty string it binds nothing.
	if host == "" {
		host = "0.0.0.0"
	}

	if err := conn.SetRawProperty(t.hostPath, host); err != nil {
		return err
	}

	if port == "" {
		return nil
	}
	if t.portPath == "" {
		res.Notes = append(res.Notes, fmt.Sprintf(
			"the address %q has a port, and this Mirth connector keeps the whole address in one field. It has been written whole.",
			address))
		return conn.SetRawProperty(t.hostPath, address)
	}

	return conn.SetRawProperty(t.portPath, port)
}

// splitAddress separates host and port, tolerating ":6661", "host:6661", a bare host and a path.
func splitAddress(address string) (host, port string) {
	// A URL or a directory keeps its colons and is not an address to split.
	if strings.Contains(address, "://") || strings.HasPrefix(address, "/") {
		return address, ""
	}

	i := strings.LastIndexByte(address, ':')
	if i < 0 {
		return address, ""
	}

	candidate := address[i+1:]
	if _, err := strconv.Atoi(candidate); err != nil {
		return address, ""
	}

	return address[:i], candidate
}

// noteWhatCannotCross records the features Mirth's format has no place for.
//
// Every one of these is a silent loss if it is not said out loud, and a silent loss in a migration tool is the worst kind: the channel
// imports, looks right, and does less than it did.
func noteWhatCannotCross(ch *config.Channel, res *Result) {
	if ch.Filter != "" {
		res.Notes = append(res.Notes, "the channel filter is not converted. Perfuse filters are expressions and Mirth's are "+
			"JavaScript or rule trees; a mechanical translation would be a guess at what the expression meant.")
	}
	if len(ch.Transformations) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d transformation steps are not converted. They are listed in the channel's "+
			"deploy script as comments so the intent is not lost, but the channel does not perform them.", len(ch.Transformations)))
	}
	if ch.Scripts != nil {
		res.Notes = append(res.Notes, "channel scripts are not converted.")
	}
	if ch.Contract != nil {
		res.Notes = append(res.Notes, "the contract is not converted: Mirth has no equivalent, so the guarantee this channel "+
			"currently checks on every message will not be checked after the move.")
	}
	if ch.Shadow != nil {
		res.Notes = append(res.Notes, "the shadow comparison is not converted; Mirth has no equivalent.")
	}
	if ch.Attachments != nil {
		res.Notes = append(res.Notes, "attachment extraction is not converted. Mirth has its own attachment handling and it is "+
			"configured differently; large payloads will stay in the message.")
	}
	if len(ch.Tables) > 0 {
		res.Notes = append(res.Notes, fmt.Sprintf("%d mapping tables are not converted; Mirth has code templates, which are not "+
			"the same shape.", len(ch.Tables)))
	}
	if ch.Group != "" {
		res.Notes = append(res.Notes, fmt.Sprintf("the group %q is not converted: channel groups are a separate document in "+
			"Mirth, not a field on the channel.", ch.Group))
	}
}

func supportedSources() string {
	names := make([]string, 0, len(sourceTransports))
	for k := range sourceTransports {
		names = append(names, string(k))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}

func supportedDests() string {
	names := make([]string, 0, len(destTransports))
	for k := range destTransports {
		names = append(names, string(k))
	}
	sort.Strings(names)
	return strings.Join(names, ", ")
}
