package hl7v3

import (
	"fmt"
	"strings"
	"time"
)

// Acknowledgements. A v3 sender that asked to be acknowledged and is not will hold the connection open and eventually
// retry, so this is not optional politeness - it is the difference between an interface that flows and one that stalls.
//
// The v3 acknowledgement is MCCI_IN000002UV01, and its structure mirrors v2's ACK closely enough that the mapping is
// obvious once seen: a type code standing for accept or reject, a reference to the message being acknowledged, and an
// optional detail explaining a refusal.

// AckType is the acknowledgement type code.
//
// The codes are v3's, and they line up with v2's AA, AE and AR closely enough that the same reasoning applies. Perfuse's
// existing rule for v2 - delivered AA, filtered AA, queued AA, partial AE, failed AE, unparseable AR - carries over
// directly, and AckFor implements exactly that so the two protocols cannot drift apart.
type AckType string

const (
	// AcceptAcknowledgementCommitAccept is v3's AA: the message was accepted.
	AckAccept AckType = "CA"

	// AckError is v3's AE: the message was understood and something went wrong handling it. The sender should not
	// resend it unchanged, because it will fail the same way.
	AckError AckType = "CE"

	// AckReject is v3's AR: the message could not be understood. The sender should not resend it at all.
	AckReject AckType = "CR"
)

// Outcome is what happened to a message, in Perfuse's own terms.
//
// Mirrors the engine's outcomes rather than inventing a second vocabulary, so the acknowledgement a v3 sender receives
// carries the same meaning as the one a v2 sender would get for the same event.
type Outcome int

const (
	// OutcomeDelivered means every destination took it.
	OutcomeDelivered Outcome = iota

	// OutcomeFiltered means a filter declined it. Acknowledged as accepted, because the message was received and
	// understood and a filter is the receiver's own decision - telling the sender their valid message failed would
	// have them retry something that will be declined again.
	OutcomeFiltered

	// OutcomeQueued means it is durably stored and will be delivered. Accepted, because Perfuse has taken
	// responsibility for it.
	OutcomeQueued

	// OutcomePartial means some destinations took it and some did not.
	OutcomePartial

	// OutcomeFailed means delivery failed.
	OutcomeFailed

	// OutcomeUnparseable means the message could not be read.
	OutcomeUnparseable
)

// AckTypeFor maps an outcome to an acknowledgement type.
//
// Deliberately the same mapping as v2, including the two that surprise people: a filtered message is accepted, and a queued
// message is accepted. Both are about who now owns the message. A filter is the receiver's decision and the sender did
// nothing wrong; a queued message is Perfuse's responsibility and telling the sender otherwise would produce a duplicate.
func AckTypeFor(outcome Outcome) AckType {
	switch outcome {
	case OutcomeDelivered, OutcomeFiltered, OutcomeQueued:
		return AckAccept
	case OutcomePartial, OutcomeFailed:
		return AckError
	case OutcomeUnparseable:
		return AckReject
	default:
		// An outcome nothing here names is treated as an error rather than as acceptance. Accepting a message whose
		// fate is unknown loses it silently; an error at worst produces a duplicate, and a duplicate is a problem
		// somebody can see.
		return AckError
	}
}

// AckOptions configures an acknowledgement.
type AckOptions struct {
	// Original is the message being acknowledged. Its identifier and sender become this acknowledgement's target and
	// receiver.
	Original *Message

	// Type is the acknowledgement type. Use AckTypeFor rather than choosing by hand.
	Type AckType

	// Text explains a refusal, and appears in an acknowledgementDetail.
	//
	// Never put message content here. An acknowledgement travels back to the sender and is logged at both ends, so a
	// patient identifier in this field ends up in two systems' logs, one of which belongs to somebody else.
	Text string

	// ReceiverDeviceOID is this system's own identifier, which becomes the acknowledgement's sender.
	//
	// Required. A v3 acknowledgement with no sender device is not valid, and a receiver that cannot say who it is gives
	// a sender no way to tell one of its interfaces from another.
	ReceiverDeviceOID string

	// Now overrides the clock, for tests.
	Now time.Time
}

// Ack builds an MCCI_IN000002UV01 acknowledgement.
//
// Written by hand rather than marshalled from a struct because the element order in a v3 message is fixed by the schema, and
// Go's XML marshaller orders by struct field which is a coincidence rather than a guarantee. Hand-writing it also keeps the
// namespace declaration exactly where a strict receiver expects it.
func Ack(opts AckOptions) ([]byte, error) {
	if opts.Original == nil {
		return nil, fmt.Errorf("no original message to acknowledge")
	}
	if opts.ReceiverDeviceOID == "" {
		// Refused rather than defaulted. An acknowledgement that does not say who sent it gives the far end no way to
		// distinguish one of our interfaces from another, and inventing an identifier here would put a fabricated OID
		// into somebody else's audit log.
		return nil, fmt.Errorf("a receiver device OID is required; an acknowledgement that cannot say who sent it " +
			"is not valid and leaves the sender unable to tell our interfaces apart")
	}

	ackType := opts.Type
	if ackType == "" {
		ackType = AckAccept
	}

	now := opts.Now
	if now.IsZero() {
		now = time.Now()
	}

	// A message identifier of our own. Uses the original's identifier as the extension with a suffix, so the two are
	// traceable to each other in a log without needing a random identifier that means nothing to anybody.
	ackExtension := "ACK"
	if opts.Original.ID.Extension != "" {
		ackExtension = opts.Original.ID.Extension + "-ACK"
	}

	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8"?>` + "\n")
	b.WriteString(`<MCCI_IN000002UV01 xmlns="urn:hl7-org:v3" ITSVersion="XML_1.0">` + "\n")

	fmt.Fprintf(&b, "  <id root=\"%s\" extension=\"%s\"/>\n",
		escapeAttr(opts.ReceiverDeviceOID), escapeAttr(ackExtension))
	fmt.Fprintf(&b, "  <creationTime value=\"%s\"/>\n", formatTimestamp(now))
	b.WriteString(`  <interactionId root="2.16.840.1.113883.1.6" extension="MCCI_IN000002UV01"/>` + "\n")

	// The processing code is echoed rather than fixed at P. An acknowledgement to a debugging message that claims to be
	// production would let a test system's traffic look live in the sender's own logs.
	processing := opts.Original.ProcessingCode
	if processing == "" {
		processing = "P"
	}
	fmt.Fprintf(&b, "  <processingCode code=\"%s\"/>\n", escapeAttr(processing))
	b.WriteString(`  <processingModeCode code="T"/>` + "\n")

	// An acknowledgement is never itself acknowledged. NE, or two systems will acknowledge each other for ever.
	b.WriteString(`  <acceptAckCode code="NE"/>` + "\n")

	// Receiver of the acknowledgement is the sender of the original, and vice versa.
	b.WriteString("  <receiver typeCode=\"RCV\">\n    <device classCode=\"DEV\" determinerCode=\"INSTANCE\">\n")
	fmt.Fprintf(&b, "      <id root=%q/>\n", escapeAttr(opts.Original.Sender.ID.Root))
	b.WriteString("    </device>\n  </receiver>\n")

	b.WriteString("  <sender typeCode=\"SND\">\n    <device classCode=\"DEV\" determinerCode=\"INSTANCE\">\n")
	fmt.Fprintf(&b, "      <id root=%q/>\n", escapeAttr(opts.ReceiverDeviceOID))
	b.WriteString("    </device>\n  </sender>\n")

	b.WriteString("  <acknowledgement>\n")
	fmt.Fprintf(&b, "    <typeCode code=\"%s\"/>\n", escapeAttr(string(ackType)))
	b.WriteString("    <targetMessage>\n")

	// The reference back to the original. This is what lets a sender match an acknowledgement to what it sent, so an
	// absent original identifier is worth being explicit about rather than omitting the element.
	if opts.Original.ID.Presence == Present {
		if opts.Original.ID.Extension != "" {
			fmt.Fprintf(&b, "      <id root=%q extension=%q/>\n",
				escapeAttr(opts.Original.ID.Root), escapeAttr(opts.Original.ID.Extension))
		} else {
			fmt.Fprintf(&b, "      <id root=%q/>\n", escapeAttr(opts.Original.ID.Root))
		}
	} else {
		// The original carried no identifier. Said explicitly with a null flavour, because omitting the element
		// entirely produces an invalid acknowledgement and a sender comparing identifiers would see a blank rather
		// than "the message you sent had none".
		b.WriteString(`      <id nullFlavor="NI"/>` + "\n")
	}
	b.WriteString("    </targetMessage>\n")

	if opts.Text != "" {
		b.WriteString("    <acknowledgementDetail>\n")
		fmt.Fprintf(&b, "      <text>%s</text>\n", escapeText(opts.Text))
		b.WriteString("    </acknowledgementDetail>\n")
	}

	b.WriteString("  </acknowledgement>\n")
	b.WriteString("</MCCI_IN000002UV01>\n")

	return []byte(b.String()), nil
}

// formatTimestamp writes a time in v3's format, with an offset.
//
// Always includes the offset. A timestamp without one leaves the receiver guessing the sender's timezone, which is the most
// common cause of an event appearing an hour out, and there is no reason to make somebody guess about a time we know.
func formatTimestamp(t time.Time) string {
	return t.Format("20060102150405-0700")
}

// escapeAttr escapes a value for an XML attribute.
//
// Every attribute written by Ack goes through this. The first version used Go's %q verb, which looks like it quotes a string
// safely and does - for Go. Go escapes a quotation mark as \" and XML does not recognise that, so a received identifier
// containing a quote closed the attribute and wrote elements into a document this system puts its own name on. The injection
// test caught it, and it is the reason no attribute here is formatted with %q.
//
// Present because an identifier root comes from a received message, and a received message is untrusted input. A root
// containing a quotation mark would otherwise close the attribute and let the sender write elements into an acknowledgement
// that this system signs its own name to.
func escapeAttr(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")

	return s
}

// escapeText escapes character data.
func escapeText(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")

	return s
}

// AckFor builds the acknowledgement for an outcome, which is the call a channel actually makes.
func AckFor(original *Message, outcome Outcome, receiverOID, detail string) ([]byte, error) {
	return Ack(AckOptions{
		Original:          original,
		Type:              AckTypeFor(outcome),
		Text:              detail,
		ReceiverDeviceOID: receiverOID,
	})
}
