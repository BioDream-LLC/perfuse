package hl7v3

import (
	"errors"
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/xtree"
)

// Every v3 interaction has the same three-layer shape, and knowing it is most of understanding the messages.
//
//  1. The transmission wrapper is the root element, and its name is the interaction identifier - PRPA_IN201305UV02 rather
//     than a generic "Message". It carries the message identifier, creation time, processing code and the sender and
//     receiver devices. This is the envelope, equivalent to v2's MSH.
//
//  2. The control act wrapper sits inside it as controlActProcess, and says what kind of act this is: a record was added, a
//     query is being asked, an authorisation is being granted. It carries the author and the reason.
//
//  3. The payload is inside the control act, under subject or queryByParameter, and is the actual clinical or demographic
//     content.
//
// Version 2's equivalent of the interaction identifier is MSH-9, and the comparison is worth making because it explains why
// v3 messages look so unfamiliar: in v2 the message type is a field, in v3 it is the name of the root element. So the first
// thing to do with a v3 message is read what it calls itself.

// Errors from parsing.
var (
	// ErrNotXML means the bytes are not XML at all.
	ErrNotXML = errors.New("not XML")

	// ErrNoInteraction means the root element does not look like a v3 interaction.
	ErrNoInteraction = errors.New("the root element is not an HL7 v3 interaction")
)

// Message is a parsed v3 interaction.
type Message struct {
	// InteractionID is the root element name, which is the interaction identifier: PRPA_IN201305UV02 and so on.
	//
	// This is the routing key. Everything else about how to handle the message follows from it.
	InteractionID string

	// ID identifies this message instance, from the id element. The equivalent of v2's MSH-10, and what an
	// acknowledgement refers back to.
	ID II

	// CreationTime is when the sender built the message.
	CreationTime Timestamp

	// VersionCode is the HL7 version, usually "V3-2008N" or similar. Advisory.
	VersionCode string

	// ProcessingCode says whether this is production, training or debugging: P, T or D.
	//
	// Worth acting on rather than logging. A message marked T arriving on a production interface is a test system
	// pointed at the wrong host, and delivering it puts fictional patients into a live record.
	ProcessingCode string

	// ProcessingModeCode is T current processing, A archive, I initial load, R restore.
	ProcessingModeCode string

	// AcceptAckCode says what acknowledgement the sender wants: AL always, NE never, ER on error only, SU on
	// successful receipt.
	AcceptAckCode string

	// Sender and Receiver identify the devices at each end.
	Sender   Device
	Receiver Device

	// ControlAct describes what kind of act this is, when the message has a control act wrapper. Some interactions -
	// plain acknowledgements in particular - do not.
	ControlAct *ControlAct

	// Root is the parsed document, for reaching anything this struct does not model.
	//
	// Exposed deliberately. Version 3 has hundreds of interactions and modelling all of them would be a decade's work
	// that nobody asked for, so the envelope is modelled and the payload stays reachable. A channel that needs one
	// element from an interaction nothing here names can still get at it.
	Root *xtree.Node

	// Raw is the original bytes, kept because a v3 message is frequently forwarded onward unchanged and re-serialising
	// XML changes bytes without changing meaning - which breaks any signature over it.
	Raw []byte
}

// Device is a sending or receiving system.
type Device struct {
	// ID identifies the device. The nearest v2 equivalent is MSH-3 or MSH-5, and as there it is often the only thing
	// distinguishing two feeds from the same hospital.
	ID II

	// Name is a label when the sender gave one. Advisory.
	Name string

	// SoftwareName and ManufacturerModelName describe the product, when stated. Useful in a log when a particular
	// vendor's version is known to send something unusual.
	SoftwareName          string
	ManufacturerModelName string

	// OrganizationID identifies the organisation the device belongs to, from
	// asAgent/representedOrganization. Present in IHE transactions and frequently the thing worth authorising on,
	// because devices get replaced and organisations do not.
	OrganizationID II
}

// ControlAct is the control act wrapper.
type ControlAct struct {
	// Code says what act this is. For a patient registry record added it is a trigger event code.
	Code Coded

	// EffectiveTime is when the act happened, as distinct from when the message was built. The two differ whenever a
	// system batches or retries, and it is the effective time that belongs in a clinical record.
	EffectiveTime Timestamp

	// AuthorID identifies who or what caused the act.
	AuthorID II

	// ReasonCode says why, when given.
	ReasonCode Coded

	// Node is the control act element, for reaching the payload.
	Node *xtree.Node
}

// Parse reads a v3 message.
//
// Uses the same hardened XML reader as CDA, which resolves no external entities and does not expand them. That was verified
// by sending real payloads rather than by reading the documentation: an external entity cannot read a file or reach the
// network, and a fifty-thousand-deep document returns an error rather than overflowing the stack - which matters because a
// Go stack overflow cannot be recovered from and would take the whole engine down, not just the channel.
func Parse(data []byte) (*Message, error) {
	root, err := xtree.Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrNotXML, err)
	}
	if root == nil {
		return nil, ErrNotXML
	}

	interaction := localName(root.Name)
	if interaction == "" {
		return nil, ErrNoInteraction
	}

	// A SOAP envelope is how IHE actually carries these on the wire, so unwrap one rather than refusing. A channel
	// receiving ITI-47 over HTTP gets the envelope, not the interaction, and requiring the author to strip it by hand
	// would mean a script in every such channel.
	if isSOAPEnvelope(interaction) {
		if body := findSOAPBody(root); body != nil {
			for _, child := range body.Children {
				if looksLikeInteraction(localName(child.Name)) {
					root = child
					interaction = localName(child.Name)

					break
				}
			}
		}
	}

	if !looksLikeInteraction(interaction) {
		return nil, fmt.Errorf("%w: root element is %q", ErrNoInteraction, interaction)
	}

	m := &Message{
		InteractionID:      interaction,
		ID:                 parseII(root.First("id")),
		CreationTime:       parseTimestamp(root.First("creationTime")),
		VersionCode:        attr(root.First("versionCode"), "code"),
		ProcessingCode:     attr(root.First("processingCode"), "code"),
		ProcessingModeCode: attr(root.First("processingModeCode"), "code"),
		AcceptAckCode:      attr(root.First("acceptAckCode"), "code"),
		Root:               root,
		Raw:                data,
	}

	if s := root.First("sender"); s != nil {
		m.Sender = parseDevice(s)
	}
	if r := root.First("receiver"); r != nil {
		m.Receiver = parseDevice(r)
	}
	if ca := root.First("controlActProcess"); ca != nil {
		m.ControlAct = parseControlAct(ca)
	}

	return m, nil
}

// looksLikeInteraction reports whether an element name is a v3 interaction identifier.
//
// The shape is four letters naming the domain, an underscore, IN, then digits and a realm suffix: PRPA_IN201305UV02. Checked
// structurally rather than against a list of known interactions, because a list would refuse a valid interaction from a
// domain nobody here had thought of - and refusing a well-formed message because it was unfamiliar is the behaviour that
// makes an engine useless for the one integration a site actually needs.
func looksLikeInteraction(name string) bool {
	i := strings.IndexByte(name, '_')
	if i != 4 {
		return false
	}
	rest := name[i+1:]
	if len(rest) < 3 || !strings.HasPrefix(rest, "IN") {
		return false
	}

	for _, r := range name[:4] {
		if r < 'A' || r > 'Z' {
			return false
		}
	}

	// At least one digit after IN, so "PRPA_INSOMETHING" is not accepted.
	for _, r := range rest[2:] {
		if r >= '0' && r <= '9' {
			return true
		}
	}

	return false
}

// isSOAPEnvelope reports whether this is a SOAP wrapper.
func isSOAPEnvelope(name string) bool { return name == "Envelope" }

// findSOAPBody finds the Body of a SOAP envelope.
func findSOAPBody(root *xtree.Node) *xtree.Node {
	for _, child := range root.Children {
		if localName(child.Name) == "Body" {
			return child
		}
	}

	return nil
}

// parseDevice reads a sender or receiver.
func parseDevice(n *xtree.Node) Device {
	d := Device{}

	dev := n.First("device")
	if dev == nil {
		// Some senders put the identifier directly on the sender element.
		d.ID = parseII(n.First("id"))

		return d
	}

	d.ID = parseII(dev.First("id"))
	d.Name = text(dev.First("name"))
	d.SoftwareName = text(dev.First("softwareName"))
	d.ManufacturerModelName = text(dev.First("manufacturerModelName"))

	if agent := dev.First("asAgent"); agent != nil {
		if org := agent.First("representedOrganization"); org != nil {
			d.OrganizationID = parseII(org.First("id"))
		}
	}

	return d
}

// parseControlAct reads the control act wrapper.
func parseControlAct(n *xtree.Node) *ControlAct {
	ca := &ControlAct{
		Code:          parseCoded(n.First("code")),
		EffectiveTime: parseTimestamp(n.First("effectiveTime")),
		ReasonCode:    parseCoded(n.First("reasonCode")),
		Node:          n,
	}

	// The author is nested a couple of levels down and the intermediate names vary by interaction, so it is looked for
	// rather than pathed to.
	if author := n.First("author"); author != nil {
		if pat := author.Find("assignedPerson"); pat != nil {
			ca.AuthorID = parseII(pat.First("id"))
		}
		if ca.AuthorID.Presence != Present {
			if dev := author.Find("assignedDevice"); dev != nil {
				ca.AuthorID = parseII(dev.First("id"))
			}
		}
		if ca.AuthorID.Presence != Present {
			ca.AuthorID = parseII(author.Find("id"))
		}
	}

	return ca
}

// text reads an element's trimmed text, or the empty string when it is absent.
func text(n *xtree.Node) string {
	if n == nil {
		return ""
	}

	return strings.TrimSpace(n.Text)
}

// IsProduction reports whether the sender marked this message as production traffic.
//
// A question rather than a comparison, because the code is easy to get backwards and the consequence of getting it backwards
// is fictional patients in a live record. An absent processing code counts as production, matching how v2 is treated: a
// sender that did not say is far more likely to be a production system with a terse implementation than a test one.
func (m *Message) IsProduction() bool {
	return m.ProcessingCode == "" || strings.EqualFold(m.ProcessingCode, "P")
}

// WantsAcknowledgement reports whether the sender asked to be acknowledged.
//
// An absent code counts as yes. Version 3 says a missing acceptAckCode means the default for the interaction, and for every
// interaction that carries one the default is to acknowledge - so staying silent because a field was missing would hang a
// sender waiting for a response.
func (m *Message) WantsAcknowledgement() bool {
	switch strings.ToUpper(m.AcceptAckCode) {
	case "NE":
		return false
	default:
		return true
	}
}

// Payload returns the subject of the control act, which is where the content lives.
//
// Nil when there is no control act or no subject, which is normal for an acknowledgement.
func (m *Message) Payload() *xtree.Node {
	if m.ControlAct == nil || m.ControlAct.Node == nil {
		return nil
	}
	subject := m.ControlAct.Node.First("subject")
	if subject == nil {
		return nil
	}

	// The subject wraps a single registration or observation event, whose name varies by interaction. Return the first
	// element child rather than the subject itself, because that is what a caller wants.
	for _, child := range subject.Children {
		return child
	}

	return subject
}

// Describe summarises a message for a log line.
//
// Never includes patient content: identifiers, names and dates are all patient data, and a log is the wrong place for them.
// The interaction, the message identifier and the devices are enough to trace a message through an interface.
func (m *Message) Describe() string {
	var b strings.Builder
	b.WriteString(m.InteractionID)

	if m.ID.Presence == Present {
		b.WriteString(" id ")
		b.WriteString(m.ID.String())
	}
	if m.Sender.ID.Presence == Present {
		b.WriteString(" from ")
		b.WriteString(m.Sender.ID.String())
	}
	if m.Receiver.ID.Presence == Present {
		b.WriteString(" to ")
		b.WriteString(m.Receiver.ID.String())
	}
	if !m.IsProduction() {
		// Said loudly, because this is the field that decides whether fictional patients reach a live record.
		b.WriteString(" PROCESSING CODE ")
		b.WriteString(m.ProcessingCode)
	}

	return b.String()
}
