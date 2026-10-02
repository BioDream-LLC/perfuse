// Package stomp speaks STOMP 1.2 to a message broker.
//
// This is what "JMS support" has to mean in a program that is not Java. JMS is an API rather than a protocol, so there is nothing
// to implement: what a site actually needs is for Perfuse to talk to the broker sitting behind their JMS applications.
//
// STOMP rather than OpenWire, and the reasoning is worth recording because the obvious answer is wrong. OpenWire is ActiveMQ's
// native protocol and a binary serialisation of JMS commands - weeks of work, specific to one broker family, and undocumented
// except by its implementation. ActiveMQ auto-detects STOMP, AMQP and MQTT on the same port it serves OpenWire on, so a text
// protocol reaches the same brokers. STOMP covers ActiveMQ Classic, Artemis and RabbitMQ; AMQP 1.0 would add Azure Service Bus
// and IBM MQ, and is the sensible next one rather than OpenWire.
//
// Written against the standard library. A broker client is not the place for a dependency whose failure modes are somebody else's
// to explain, and the framing here is small enough that owning it costs less than reading their issue tracker.
package stomp

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Frame is one STOMP frame.
type Frame struct {
	Command string
	Headers map[string]string
	Body    []byte
}

// Header returns a header value.
func (f *Frame) Header(name string) string {
	if f == nil || f.Headers == nil {
		return ""
	}
	return f.Headers[name]
}

// Commands sent by a client.
const (
	CmdConnect     = "CONNECT"
	CmdStomp       = "STOMP"
	CmdSend        = "SEND"
	CmdSubscribe   = "SUBSCRIBE"
	CmdUnsubscribe = "UNSUBSCRIBE"
	CmdAck         = "ACK"
	CmdNack        = "NACK"
	CmdBegin       = "BEGIN"
	CmdCommit      = "COMMIT"
	CmdAbort       = "ABORT"
	CmdDisconnect  = "DISCONNECT"
)

// Commands sent by a broker.
const (
	CmdConnected = "CONNECTED"
	CmdMessage   = "MESSAGE"
	CmdReceipt   = "RECEIPT"
	CmdError     = "ERROR"
)

// ErrBrokerError is returned when the broker sends an ERROR frame.
var ErrBrokerError = errors.New("stomp: the broker reported an error")

// maxFrameBytes bounds a single frame.
//
// A bound rather than none, because the body length can be declared by the sender and a broker or a man in the middle claiming
// four gigabytes should be refused rather than allocated. Sixty-four megabytes is far above any HL7 message and far below
// anything that matters.
const maxFrameBytes = 64 << 20

// maxHeaders bounds how many headers one frame may carry.
const maxHeaders = 128

// WriteFrame encodes a frame.
//
// Header values are escaped. That is required by STOMP 1.2 and easy to skip, and skipping it means a value containing a colon -
// an HL7 timestamp, a URL, a Windows path - silently becomes a different header.
func WriteFrame(w io.Writer, f Frame) error {
	var b strings.Builder

	b.WriteString(f.Command)
	b.WriteString("\n")

	// content-length is always sent. Without it a body containing a null byte is truncated at the null, because that is what
	// terminates a frame - and a base64 payload or a compressed document will contain one eventually.
	if _, ok := f.Headers["content-length"]; !ok {
		if f.Headers == nil {
			f.Headers = map[string]string{}
		}
		f.Headers["content-length"] = strconv.Itoa(len(f.Body))
	}

	for _, name := range sortedHeaderNames(f.Headers) {
		b.WriteString(escapeHeader(name))
		b.WriteString(":")
		b.WriteString(escapeHeader(f.Headers[name]))
		b.WriteString("\n")
	}

	b.WriteString("\n")

	if _, err := io.WriteString(w, b.String()); err != nil {
		return err
	}
	if len(f.Body) > 0 {
		if _, err := w.Write(f.Body); err != nil {
			return err
		}
	}

	// The null terminates the frame.
	_, err := w.Write([]byte{0})
	return err
}

// ReadFrame decodes a frame.
func ReadFrame(r *bufio.Reader) (*Frame, error) {
	command, err := readLine(r)
	if err != nil {
		return nil, err
	}

	// Heartbeats arrive as bare newlines between frames and are not frames. Skipped rather than reported, because a broker
	// configured to send them will send a great many.
	for command == "" {
		command, err = readLine(r)
		if err != nil {
			return nil, err
		}
	}

	f := &Frame{Command: command, Headers: map[string]string{}}

	for {
		line, err := readLine(r)
		if err != nil {
			return nil, err
		}
		if line == "" {
			break
		}
		if len(f.Headers) >= maxHeaders {
			return nil, fmt.Errorf("stomp: a frame carried more than %d headers", maxHeaders)
		}

		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			return nil, fmt.Errorf("stomp: a header line has no colon: %q", line)
		}

		name := unescapeHeader(line[:colon])
		value := unescapeHeader(line[colon+1:])

		// First value wins, which the specification requires. Later ones are a broker repeating itself or a sender being
		// careless, and overwriting would let a second content-length change how the body is read.
		if _, exists := f.Headers[name]; !exists {
			f.Headers[name] = value
		}
	}

	if declared := f.Headers["content-length"]; declared != "" {
		length, err := strconv.Atoi(declared)
		if err != nil || length < 0 {
			return nil, fmt.Errorf("stomp: content-length %q is not a length", declared)
		}
		if length > maxFrameBytes {
			return nil, fmt.Errorf("stomp: a frame declared %d bytes, which is beyond the %d byte limit",
				length, maxFrameBytes)
		}

		body := make([]byte, length)
		if _, err := io.ReadFull(r, body); err != nil {
			return nil, fmt.Errorf("stomp: the frame body was short: %w", err)
		}
		f.Body = body

		// The terminating null still has to be consumed, or the next frame begins with it.
		if terminator, err := r.ReadByte(); err != nil {
			return nil, err
		} else if terminator != 0 {
			return nil, fmt.Errorf("stomp: a frame of declared length was not terminated by a null")
		}

		return f, nil
	}

	// No content-length, so the body runs to the next null. This is the case that breaks on binary content, which is why
	// content-length is always sent when writing.
	body, err := readUntilNull(r)
	if err != nil {
		return nil, err
	}
	f.Body = body

	return f, nil
}

// readLine reads one line, tolerating both line endings.
//
// Both, because STOMP 1.2 allows carriage return followed by line feed and 1.1 does not, and brokers differ in what they emit
// regardless of the version they negotiated.
func readLine(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r"), nil
}

func readUntilNull(r *bufio.Reader) ([]byte, error) {
	body, err := r.ReadBytes(0)
	if err != nil {
		return nil, err
	}
	return body[:len(body)-1], nil
}

// escapeHeader applies the STOMP 1.2 header escapes.
//
// The backslash has to be escaped first, or the escapes introduced for the others get escaped in turn - which is the same
// ordering trap as HL7 escaping and produces the same result: values that survive one round trip and not two.
func escapeHeader(s string) string {
	s = strings.ReplaceAll(s, "\\", "\\\\")
	s = strings.ReplaceAll(s, "\r", "\\r")
	s = strings.ReplaceAll(s, "\n", "\\n")
	s = strings.ReplaceAll(s, ":", "\\c")
	return s
}

// unescapeHeader reverses escapeHeader.
//
// Scanned once rather than by repeated replacement, because replacing "\\c" with ":" and then "\\\\" with "\\" turns a literal
// backslash-c into a colon.
func unescapeHeader(s string) string {
	if !strings.Contains(s, "\\") {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))

	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 >= len(s) {
			b.WriteByte(s[i])
			continue
		}
		i++
		switch s[i] {
		case 'r':
			b.WriteByte('\r')
		case 'n':
			b.WriteByte('\n')
		case 'c':
			b.WriteByte(':')
		case '\\':
			b.WriteByte('\\')
		default:
			// An undefined escape. The specification says to fail the connection; kept literally instead, because dropping a
			// message over a broker's escaping quirk is a worse outcome than a header that reads slightly oddly.
			b.WriteByte('\\')
			b.WriteByte(s[i])
		}
	}

	return b.String()
}

// sortedHeaderNames orders header names.
//
// Sorted so that two identical frames encode identically. That matters for tests and for anybody comparing captured traffic; Go
// maps range randomly, so without it the same message produces different bytes each time.
func sortedHeaderNames(headers map[string]string) []string {
	out := make([]string, 0, len(headers))
	for name := range headers {
		out = append(out, name)
	}

	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}

	return out
}

// ErrorMessage extracts the useful text from an ERROR frame.
//
// The message header and the body both carry explanation and brokers use them differently: Artemis puts a summary in the header
// and a stack trace in the body, RabbitMQ the reverse. Taking whichever is present avoids an error that reports nothing.
func ErrorMessage(f *Frame) string {
	if f == nil {
		return "the broker closed the connection without saying why"
	}

	message := f.Header("message")
	body := strings.TrimSpace(string(f.Body))

	switch {
	case message != "" && body != "":
		// Truncated, because a broker's stack trace is hundreds of lines and the first is the only one that identifies the
		// problem.
		return message + ": " + firstLine(body)
	case message != "":
		return message
	case body != "":
		return firstLine(body)
	}

	return "the broker sent an error with no explanation"
}

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}
