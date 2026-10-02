package hl7

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// Acknowledgements are where interface engines quietly disagree. The rules that
// matter and are easy to get wrong:
//
//   - the addressing is mirrored, not copied: the sender and receiver fields
//     swap, so an ACK sent back to a hospital carries their application in
//     MSH-5, not MSH-3;
//   - MSA-2 must echo the original MSH-10, because that is the only thing tying
//     the acknowledgement to the message it answers;
//   - MSH-12 must be the version the sender used, not the version we prefer,
//     or strict senders reject the ACK;
//   - original mode uses AA/AE/AR and enhanced mode uses CA/CE/CR. Sending the
//     wrong pair is accepted by lenient senders and rejected by strict ones,
//     which is the worst kind of bug to have in production.

// AckCode is the acknowledgement code placed in MSA-1.
type AckCode string

// Original acknowledgement mode codes.
const (
	// AckAccept means the message was accepted and processed.
	AckAccept AckCode = "AA"
	// AckError means the message was understood but could not be processed.
	AckError AckCode = "AE"
	// AckReject means the message was rejected, usually as malformed or
	// unsupported.
	AckReject AckCode = "AR"
)

// Enhanced acknowledgement mode codes, used when MSH-15 or MSH-16 request a
// commit acknowledgement.
const (
	// CommitAccept means the message was received and stored.
	CommitAccept AckCode = "CA"
	// CommitError means the message was received but could not be stored.
	CommitError AckCode = "CE"
	// CommitReject means the message was rejected on receipt.
	CommitReject AckCode = "CR"
)

// Enhanced reports whether the code belongs to the enhanced mode set.
func (c AckCode) Enhanced() bool {
	switch c {
	case CommitAccept, CommitError, CommitReject:
		return true
	}
	return false
}

// AckOptions controls acknowledgement generation. The zero value produces an
// AA with a generated control ID and the current time.
type AckOptions struct {
	// Code defaults to AckAccept.
	Code AckCode

	// Text becomes MSA-3, a human-readable reason. Required in practice for
	// AE and AR, because the receiving support team has nothing else to go on.
	Text string

	// ControlID becomes MSH-10 of the acknowledgement. Generated when empty.
	ControlID string

	// Timestamp becomes MSH-7. Defaults to time.Now.
	Timestamp time.Time

	// SendingApplication and SendingFacility override the mirrored values, for
	// the case where this engine identifies itself rather than impersonating
	// the original receiver.
	SendingApplication string
	SendingFacility    string

	// IncludeTriggerEvent appends the original trigger event to MSH-9, giving
	// ACK^A01 rather than ACK. Some receivers require it, others reject it.
	IncludeTriggerEvent bool

	// ErrorCode adds an ERR segment carrying an HL7 table 0357 code.
	ErrorCode string
}

// AckMode reports which acknowledgement code set the sender asked for, based on
// MSH-15 and MSH-16. An empty result means the sender expressed no preference,
// in which case original mode is correct.
func (m *Message) AckMode() (accept, application string) {
	seg, ok := m.Segment("MSH", 1)
	if !ok {
		return "", ""
	}
	return seg.Field(15).String(), seg.Field(16).String()
}

// WantsEnhancedAck reports whether the sender requested enhanced mode
// acknowledgements in MSH-15.
func (m *Message) WantsEnhancedAck() bool {
	accept, _ := m.AckMode()
	switch strings.ToUpper(accept) {
	case "AL", "NE", "ER", "SU":
		return true
	}
	return false
}

// Ack builds an acknowledgement for this message.
func (m *Message) Ack(opts AckOptions) []byte {
	sep := m.sep
	code := opts.Code
	if code == "" {
		code = AckAccept
	}
	ts := opts.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	controlID := opts.ControlID
	if controlID == "" {
		controlID = generateControlID(ts)
	}

	msh, hasMSH := m.Segment("MSH", 1)

	get := func(field int) string {
		if !hasMSH {
			return ""
		}
		return msh.Field(field).Raw()
	}

	sendingApp := opts.SendingApplication
	if sendingApp == "" {
		sendingApp = get(5) // their receiving application becomes our sender
	}
	sendingFac := opts.SendingFacility
	if sendingFac == "" {
		sendingFac = get(6)
	}

	msgType := "ACK"
	if opts.IncludeTriggerEvent {
		if _, event, _ := m.Type(); event != "" {
			msgType = "ACK" + string(sep.Component) + event
		}
	}

	version := get(12)
	if version == "" {
		version = "2.5.1"
	}
	processingID := get(11)
	if processingID == "" {
		processingID = "P"
	}

	fs := string(sep.Field)
	var sb strings.Builder
	sb.Grow(256)

	sb.WriteString("MSH")
	sb.WriteString(fs)
	sb.WriteString(sep.EncodingCharacters())
	for _, f := range []string{
		sendingApp,
		sendingFac,
		get(3), // their sending application becomes our receiver
		get(4),
		ts.Format("20060102150405"),
		"", // MSH-8 security
		msgType,
		controlID,
		processingID,
		version,
	} {
		sb.WriteString(fs)
		sb.WriteString(f)
	}
	sb.WriteString("\r")

	sb.WriteString("MSA")
	sb.WriteString(fs)
	sb.WriteString(string(code))
	sb.WriteString(fs)
	sb.WriteString(get(10)) // the control ID of the message being answered
	if opts.Text != "" {
		sb.WriteString(fs)
		sb.WriteString(Escape(oneLine(opts.Text), sep))
	}
	sb.WriteString("\r")

	if opts.ErrorCode != "" {
		// ERR-1 is retained for backwards compatibility and left empty; the
		// code goes in ERR-3 as an HL7 table 0357 value.
		sb.WriteString("ERR")
		sb.WriteString(fs)
		sb.WriteString(fs)
		sb.WriteString(fs)
		sb.WriteString(Escape(opts.ErrorCode, sep))
		if opts.Text != "" {
			sb.WriteString(fs)
			sb.WriteString(severityFor(code))
			sb.WriteString(fs)
			sb.WriteString(fs)
			sb.WriteString(fs)
			sb.WriteString(fs)
			sb.WriteString(Escape(oneLine(opts.Text), sep))
		}
		sb.WriteString("\r")
	}

	return []byte(sb.String())
}

// AckFor builds an acknowledgement for a message that could not be parsed.
//
// A sender that transmits something unparseable still needs an answer, and
// silence causes it to retry forever. There is nothing to mirror, so this is
// deliberately minimal and always a rejection.
func AckFor(err error, opts AckOptions) []byte {
	sep := DefaultSeparators()
	code := opts.Code
	if code == "" {
		code = AckReject
	}
	ts := opts.Timestamp
	if ts.IsZero() {
		ts = time.Now()
	}
	controlID := opts.ControlID
	if controlID == "" {
		controlID = generateControlID(ts)
	}
	text := opts.Text
	if text == "" && err != nil {
		text = err.Error()
	}

	fs := string(sep.Field)
	var sb strings.Builder
	sb.WriteString("MSH")
	sb.WriteString(fs)
	sb.WriteString(sep.EncodingCharacters())
	for _, f := range []string{
		opts.SendingApplication,
		opts.SendingFacility,
		"", "",
		ts.Format("20060102150405"),
		"",
		"ACK",
		controlID,
		"P",
		"2.5.1",
	} {
		sb.WriteString(fs)
		sb.WriteString(f)
	}
	sb.WriteString("\r")

	sb.WriteString("MSA")
	sb.WriteString(fs)
	sb.WriteString(string(code))
	sb.WriteString(fs)
	// No control ID is available, because reading it is what failed.
	sb.WriteString(fs)
	sb.WriteString(Escape(oneLine(text), sep))
	sb.WriteString("\r")

	return []byte(sb.String())
}

func severityFor(code AckCode) string {
	switch code {
	case AckReject, CommitReject:
		return "E"
	case AckError, CommitError:
		return "E"
	default:
		return "I"
	}
}

// oneLine collapses newlines, which would otherwise be read as segment
// terminators and split the acknowledgement into nonsense.
func oneLine(s string) string {
	if !strings.ContainsAny(s, "\r\n") {
		return s
	}
	return strings.Join(strings.Fields(strings.ReplaceAll(
		strings.ReplaceAll(s, "\r", " "), "\n", " ")), " ")
}

// controlSeq disambiguates control IDs generated within the same second.
//
// It is atomic because acknowledgements are generated concurrently: one
// goroutine per connection, and a busy engine answers many at once. A duplicated
// MSH-10 would give two different messages the same correlation key, which is
// the one thing a control ID exists to prevent.
var controlSeq atomic.Uint64

// generateControlID produces a control ID that is unique within a process and
// sorts by time, which makes log correlation possible.
func generateControlID(ts time.Time) string {
	n := controlSeq.Add(1)
	return fmt.Sprintf("%s%05d", ts.Format("20060102150405"), n%100000)
}
