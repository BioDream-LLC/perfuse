// Package framing reads and writes messages on a byte stream that is not MLLP.
//
// # Why this exists separately from mllp
//
// MLLP is one framing: 0x0B before, 0x1C 0x0D after. It is what HL7 v2 uses over TCP and Perfuse has always spoken it.
//
// A great deal of equipment does not. A laboratory analyser, a scale, a bedside monitor, an older billing system - each
// tends to have its own convention, and the conventions are all simple variations on the same three ideas: a delimiter
// somewhere, a fixed record length, or a length written in front of the payload.
//
// Mirth's TCP connector covers this and Perfuse had nothing, so a site with a socket feed that was not HL7 could not use
// it at all.
//
// # The mistake this package is written to avoid
//
// Reading from a stream and guessing where a message ends is the whole problem. Guess short and a message is cut in
// half; guess long and two messages arrive as one. Both produce output that often looks plausible, which is worse than
// an error, so every mode here is explicit about what terminates a message and none of them fall back to another.
//
// A message with no framing at all is a legitimate configuration - some devices open a connection, send one message and
// close - but it cannot be combined with anything else and cannot be used on a connection that stays open, so it is
// named and validated rather than treated as a default.
package framing

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// Mode is how message boundaries are found on a stream.
type Mode string

// The framings this package understands.
const (
	// ModeMLLP is HL7's framing: 0x0B, payload, 0x1C 0x0D.
	ModeMLLP Mode = "mllp"

	// ModeDelimited ends a message at a byte sequence, commonly a carriage return or a newline.
	ModeDelimited Mode = "delimited"

	// ModeFixed reads a fixed number of bytes per message.
	//
	// Old equipment and mainframe exports do this. Unforgiving by nature: one byte out of step and every subsequent
	// message is misaligned, with no way for either end to notice.
	ModeFixed Mode = "fixed"

	// ModeLengthPrefixed reads a length header and then that many bytes.
	//
	// The only framing here that cannot be confused by payload content, which is why it is worth preferring when the
	// far end offers a choice.
	ModeLengthPrefixed Mode = "length"

	// ModeWhole treats everything until the peer closes as one message.
	//
	// Valid for a device that connects, sends and disconnects. Cannot be used on a connection that stays open, because
	// nothing will ever complete, and cannot be combined with a reply for the same reason.
	ModeWhole Mode = "whole"
)

// Settings describes one framing.
type Settings struct {
	Mode Mode

	// Delimiter ends a message in ModeDelimited.
	Delimiter []byte

	// StartBlock optionally begins a message in ModeDelimited. Bytes before it are discarded.
	//
	// Some devices send a control character before each record and nothing after, others do both. Where a start block is
	// configured, anything arriving before it is noise from a partial connection rather than data, and keeping it would
	// prepend rubbish to the first message of every session.
	StartBlock []byte

	// KeepDelimiter includes the delimiter in the message handed on.
	//
	// Off by default. It is framing, not content, and a downstream parser that receives it usually reports a
	// trailing empty segment rather than the delimiter itself, which sends somebody looking in the wrong place.
	KeepDelimiter bool

	// RecordLength is the message size in ModeFixed.
	RecordLength int

	// TrimPadding removes trailing spaces and NULs from a fixed-length record.
	//
	// On by default for fixed records, because padding is how the format achieves a fixed length and is never content.
	TrimPadding bool

	// LengthBytes is the width of the header in ModeLengthPrefixed: 1, 2, 4 or 8.
	LengthBytes int

	// BigEndian reads the length header most significant byte first.
	BigEndian bool

	// LengthIncludesHeader says the advertised length counts the header itself.
	//
	// Both conventions exist and the difference is silent: with a four-byte header the payload is wrong by four bytes
	// every time, which for a text format usually still parses.
	LengthIncludesHeader bool

	// MaxMessageSize bounds one message. Required, and enforced before allocating.
	MaxMessageSize int
}

// Validate checks the settings and fills in defaults.
func (s *Settings) Validate() error {
	if s.MaxMessageSize <= 0 {
		s.MaxMessageSize = 16 << 20
	}

	switch s.Mode {
	case ModeMLLP:
		return nil

	case ModeDelimited:
		if len(s.Delimiter) == 0 {
			return errors.New("a delimited stream needs a delimiter. It is the only thing that says where one " +
				"message ends and the next begins, and guessing it wrong cuts messages in half or joins two into " +
				"one, both of which usually still parse")
		}
		if len(s.Delimiter) > 8 {
			return fmt.Errorf("the delimiter is %d bytes, which is long enough that it is probably content rather "+
				"than framing", len(s.Delimiter))
		}
		return nil

	case ModeFixed:
		if s.RecordLength <= 0 {
			return errors.New("a fixed-length stream needs record_length")
		}
		if s.RecordLength > s.MaxMessageSize {
			return fmt.Errorf("record_length is %d but max_message_size is %d, so no record could ever be read",
				s.RecordLength, s.MaxMessageSize)
		}
		return nil

	case ModeLengthPrefixed:
		switch s.LengthBytes {
		case 0:
			s.LengthBytes = 4
		case 1, 2, 4, 8:
		default:
			return fmt.Errorf("length_bytes is %d; it must be 1, 2, 4 or 8", s.LengthBytes)
		}
		return nil

	case ModeWhole:
		return nil

	case "":
		return errors.New("no framing was given. A stream needs to say how messages are separated: mllp, " +
			"delimited, fixed, length or whole. There is deliberately no default, because reading a stream with " +
			"the wrong framing produces messages that look plausible rather than an error")

	default:
		return fmt.Errorf("framing %q is not known; use mllp, delimited, fixed, length or whole", s.Mode)
	}
}

// Describe names the framing for a log line.
func (s Settings) Describe() string {
	switch s.Mode {
	case ModeDelimited:
		if len(s.StartBlock) > 0 {
			return fmt.Sprintf("delimited (start %s, end %s)", hexish(s.StartBlock), hexish(s.Delimiter))
		}
		return fmt.Sprintf("delimited (end %s)", hexish(s.Delimiter))
	case ModeFixed:
		return fmt.Sprintf("fixed %d-byte records", s.RecordLength)
	case ModeLengthPrefixed:
		order := "little-endian"
		if s.BigEndian {
			order = "big-endian"
		}
		return fmt.Sprintf("%d-byte %s length prefix", s.LengthBytes, order)
	case ModeWhole:
		return "whole stream until close"
	default:
		return string(s.Mode)
	}
}

// hexish renders framing bytes readably, naming the control characters people actually configure.
func hexish(b []byte) string {
	var parts []string
	for _, c := range b {
		switch c {
		case '\r':
			parts = append(parts, "CR")
		case '\n':
			parts = append(parts, "LF")
		case 0x0B:
			parts = append(parts, "VT")
		case 0x1C:
			parts = append(parts, "FS")
		case 0x02:
			parts = append(parts, "STX")
		case 0x03:
			parts = append(parts, "ETX")
		case 0x04:
			parts = append(parts, "EOT")
		default:
			if c >= 0x20 && c < 0x7f {
				parts = append(parts, string(rune(c)))
			} else {
				parts = append(parts, fmt.Sprintf("0x%02X", c))
			}
		}
	}
	return strings.Join(parts, " ")
}

// ErrTooLarge is returned when a message exceeds MaxMessageSize.
//
// Distinguished because the connection cannot be recovered afterwards. The reader is somewhere in the middle of an
// oversize message and has no way to find the next boundary, so the caller must close rather than continue - continuing
// would deliver the tail of one message as though it were a whole one.
var ErrTooLarge = errors.New("the message is larger than max_message_size")

// Reader reads framed messages from a stream.
type Reader struct {
	br  *bufio.Reader
	s   Settings
	eof bool
}

// NewReader wraps a stream.
func NewReader(r io.Reader, s Settings) *Reader {
	// Bounded so a peer that sends nothing but framing bytes cannot make the buffer grow without limit.
	size := s.MaxMessageSize
	if size > 1<<20 {
		size = 1 << 20
	}
	if size < 4096 {
		size = 4096
	}
	return &Reader{br: bufio.NewReaderSize(r, size), s: s}
}

// ReadMessage returns the next message, or io.EOF when the stream is finished.
func (r *Reader) ReadMessage() ([]byte, error) {
	if r.eof {
		return nil, io.EOF
	}

	switch r.s.Mode {
	case ModeDelimited:
		return r.readDelimited()
	case ModeFixed:
		return r.readFixed()
	case ModeLengthPrefixed:
		return r.readLengthPrefixed()
	case ModeWhole:
		return r.readWhole()
	default:
		return nil, fmt.Errorf("framing %q cannot be read here", r.s.Mode)
	}
}

// readDelimited scans until the delimiter.
func (r *Reader) readDelimited() ([]byte, error) {
	if len(r.s.StartBlock) > 0 {
		if err := r.discardUntil(r.s.StartBlock); err != nil {
			return nil, err
		}
	}

	var buf []byte
	last := r.s.Delimiter[len(r.s.Delimiter)-1]

	for {
		chunk, err := r.br.ReadBytes(last)
		buf = append(buf, chunk...)

		if err != nil {
			if err == io.EOF {
				r.eof = true
				// Bytes with no terminator. Reported rather than delivered: a message that was cut off by the peer
				// disconnecting is exactly the case where delivering what arrived is wrong, because it will parse.
				if len(bytes.TrimSpace(buf)) > 0 {
					return nil, fmt.Errorf("the connection ended after %d bytes with no %s, so what arrived was "+
						"not a complete message and has not been delivered",
						len(buf), hexish(r.s.Delimiter))
				}
				return nil, io.EOF
			}
			return nil, err
		}

		if len(buf) > r.s.MaxMessageSize {
			return nil, fmt.Errorf("%w (%d bytes read with no %s)",
				ErrTooLarge, len(buf), hexish(r.s.Delimiter))
		}

		// ReadBytes stops at the last byte of the delimiter, which for a multi-byte delimiter can be a coincidence -
		// the final byte appearing inside the payload. Only a match of the whole sequence ends the message.
		if bytes.HasSuffix(buf, r.s.Delimiter) {
			if r.s.KeepDelimiter {
				return buf, nil
			}
			return buf[:len(buf)-len(r.s.Delimiter)], nil
		}
	}
}

// discardUntil throws away bytes up to and including the marker.
func (r *Reader) discardUntil(marker []byte) error {
	last := marker[len(marker)-1]
	var seen []byte

	for {
		chunk, err := r.br.ReadBytes(last)
		seen = append(seen, chunk...)
		if err != nil {
			if err == io.EOF {
				r.eof = true
				return io.EOF
			}
			return err
		}
		if bytes.HasSuffix(seen, marker) {
			return nil
		}
		if len(seen) > r.s.MaxMessageSize {
			return fmt.Errorf("%w (%d bytes with no %s to start a message)",
				ErrTooLarge, len(seen), hexish(marker))
		}
	}
}

// readFixed reads exactly RecordLength bytes.
func (r *Reader) readFixed() ([]byte, error) {
	buf := make([]byte, r.s.RecordLength)

	n, err := io.ReadFull(r.br, buf)
	switch {
	case err == io.EOF && n == 0:
		r.eof = true
		return nil, io.EOF

	case err == io.ErrUnexpectedEOF || (err == io.EOF && n > 0):
		r.eof = true
		// A partial record is not delivered. With fixed framing there is no way to tell a short final record from a
		// stream that was cut, and padding it out would hand a downstream system a record whose trailing fields are
		// silently empty rather than absent.
		return nil, fmt.Errorf("the connection ended %d bytes into a %d-byte record, so the record is incomplete "+
			"and has not been delivered. If the stream really does end with a short record, it is not "+
			"fixed-length framing", n, r.s.RecordLength)

	case err != nil:
		return nil, err
	}

	if r.s.TrimPadding {
		return bytes.TrimRight(buf, " \x00"), nil
	}
	return buf, nil
}

// readLengthPrefixed reads a header and then that many bytes.
func (r *Reader) readLengthPrefixed() ([]byte, error) {
	header := make([]byte, r.s.LengthBytes)

	n, err := io.ReadFull(r.br, header)
	switch {
	case err == io.EOF && n == 0:
		r.eof = true
		return nil, io.EOF
	case err == io.ErrUnexpectedEOF || (err == io.EOF && n > 0):
		r.eof = true
		return nil, fmt.Errorf("the connection ended after %d of the %d header bytes", n, r.s.LengthBytes)
	case err != nil:
		return nil, err
	}

	length := decodeLength(header, r.s.BigEndian)

	if r.s.LengthIncludesHeader {
		length -= int64(r.s.LengthBytes)
		if length < 0 {
			return nil, fmt.Errorf("the header says the message is %d bytes including a %d-byte header, which is "+
				"impossible. length_includes_header is probably set the wrong way round",
				length+int64(r.s.LengthBytes), r.s.LengthBytes)
		}
	}

	if length == 0 {
		// Not an error in itself, but nothing can be done with it. Returned as an explicit empty message rather than
		// mistaken for end of stream.
		return []byte{}, nil
	}

	// Checked before allocating. A two-byte header can advertise 65535 and an eight-byte one can advertise more memory
	// than the machine has, so trusting it is a remote peer away from an outage.
	if length > int64(r.s.MaxMessageSize) {
		return nil, fmt.Errorf("%w: the header advertises %d bytes and max_message_size is %d. If the far end is "+
			"sending smaller messages than that, the byte order or length_includes_header is probably wrong",
			ErrTooLarge, length, r.s.MaxMessageSize)
	}

	body := make([]byte, length)
	if _, err := io.ReadFull(r.br, body); err != nil {
		if err == io.ErrUnexpectedEOF || err == io.EOF {
			r.eof = true
			return nil, fmt.Errorf("the header advertised %d bytes but the connection ended first, so the message "+
				"is incomplete and has not been delivered", length)
		}
		return nil, err
	}
	return body, nil
}

// readWhole reads until the peer closes.
func (r *Reader) readWhole() ([]byte, error) {
	body, err := io.ReadAll(io.LimitReader(r.br, int64(r.s.MaxMessageSize)+1))
	r.eof = true

	if err != nil {
		return nil, err
	}
	if len(body) > r.s.MaxMessageSize {
		return nil, fmt.Errorf("%w: the peer sent more than %d bytes without closing, and with whole-stream "+
			"framing there is no boundary at which to stop. The stream probably has framing after all",
			ErrTooLarge, r.s.MaxMessageSize)
	}
	if len(body) == 0 {
		return nil, io.EOF
	}
	return body, nil
}

// decodeLength reads a big- or little-endian unsigned integer of 1, 2, 4 or 8 bytes.
func decodeLength(b []byte, bigEndian bool) int64 {
	switch len(b) {
	case 1:
		return int64(b[0])
	case 2:
		if bigEndian {
			return int64(binary.BigEndian.Uint16(b))
		}
		return int64(binary.LittleEndian.Uint16(b))
	case 4:
		if bigEndian {
			return int64(binary.BigEndian.Uint32(b))
		}
		return int64(binary.LittleEndian.Uint32(b))
	case 8:
		var v uint64
		if bigEndian {
			v = binary.BigEndian.Uint64(b)
		} else {
			v = binary.LittleEndian.Uint64(b)
		}
		// Clamped rather than wrapped. An eight-byte header with the top bit set becomes a negative length, and a
		// negative length compared against a maximum passes every check.
		if v > 1<<62 {
			return 1 << 62
		}
		return int64(v)
	default:
		return 0
	}
}

// CanRead reports whether a Reader can read a mode.
//
// MLLP is the same deliberate gap as CanFrame and for the same reason: an mllp
// source is its own type, which owns the connection and the acknowledgement it
// sends back. This package's generic reader does not implement it.
//
// Exported for the same reason too. A tcp source framed as mllp validated cleanly,
// logged that it was listening with framing=mllp, and then failed every message
// with "framing mllp cannot be read here" while resetting the sender's connection.
func CanRead(mode Mode) bool {
	switch mode {
	case ModeDelimited, ModeFixed, ModeLengthPrefixed, ModeWhole:
		return true
	default:
		return false
	}
}

// CanFrame reports whether Frame can write a mode.
//
// MLLP is readable and deliberately not writable. An MLLP destination is its own
// type because sending MLLP means reading the acknowledgement back and deciding
// what an AE means; writing the frame onto a raw socket would send the bytes and
// ignore the answer, which looks like success and is not.
//
// Exported so configuration can refuse the combination when a channel is loaded.
// Without it a tcp destination framed as mllp validated cleanly and then failed
// every single message at runtime with a framing error, which is the worst place
// to learn about a configuration mistake.
func CanFrame(mode Mode) bool {
	switch mode {
	case ModeDelimited, ModeFixed, ModeLengthPrefixed, ModeWhole:
		return true
	default:
		return false
	}
}

// Frame wraps a message for sending.
func Frame(msg []byte, s Settings) ([]byte, error) {
	switch s.Mode {
	case ModeDelimited:
		out := make([]byte, 0, len(s.StartBlock)+len(msg)+len(s.Delimiter))
		out = append(out, s.StartBlock...)
		out = append(out, msg...)
		// Not appended if it is already there. Otherwise a message that already ends in a carriage return - which most
		// HL7 does - gets two, and the far end reports a trailing empty segment.
		if !bytes.HasSuffix(msg, s.Delimiter) {
			out = append(out, s.Delimiter...)
		}
		return out, nil

	case ModeFixed:
		if len(msg) > s.RecordLength {
			return nil, fmt.Errorf("the message is %d bytes but record_length is %d. Truncating it would send a "+
				"record whose last fields are silently missing, so it has not been sent",
				len(msg), s.RecordLength)
		}
		out := make([]byte, s.RecordLength)
		copy(out, msg)
		for i := len(msg); i < s.RecordLength; i++ {
			out[i] = ' '
		}
		return out, nil

	case ModeLengthPrefixed:
		length := len(msg)
		if s.LengthIncludesHeader {
			length += s.LengthBytes
		}
		if err := lengthFits(length, s.LengthBytes); err != nil {
			return nil, err
		}
		out := make([]byte, s.LengthBytes+len(msg))
		encodeLength(out[:s.LengthBytes], int64(length), s.BigEndian)
		copy(out[s.LengthBytes:], msg)
		return out, nil

	case ModeWhole:
		// Nothing added. The boundary is the close, which the caller performs.
		return msg, nil

	default:
		return nil, fmt.Errorf("framing %q cannot be written here", s.Mode)
	}
}

// lengthFits refuses a message too large for its own header.
//
// Worth its own error. Encoded silently, a 70,000-byte message with a two-byte header advertises 4,464, and the far end
// reads 4,464 bytes as a complete message and then interprets the rest as the next one. Every subsequent message on that
// connection is garbage, and the cause is thousands of bytes back.
func lengthFits(length, width int) error {
	var max int64
	switch width {
	case 1:
		max = 0xFF
	case 2:
		max = 0xFFFF
	case 4:
		max = 0xFFFFFFFF
	default:
		return nil
	}
	if int64(length) > max {
		return fmt.Errorf("the message needs a length of %d but the header is %d byte(s), which can only express "+
			"%d. Sending it would advertise the wrong length and every message after it on this connection would "+
			"be misread", length, width, max)
	}
	return nil
}

func encodeLength(dst []byte, v int64, bigEndian bool) {
	switch len(dst) {
	case 1:
		dst[0] = byte(v)
	case 2:
		if bigEndian {
			binary.BigEndian.PutUint16(dst, uint16(v))
		} else {
			binary.LittleEndian.PutUint16(dst, uint16(v))
		}
	case 4:
		if bigEndian {
			binary.BigEndian.PutUint32(dst, uint32(v))
		} else {
			binary.LittleEndian.PutUint32(dst, uint32(v))
		}
	case 8:
		if bigEndian {
			binary.BigEndian.PutUint64(dst, uint64(v))
		} else {
			binary.LittleEndian.PutUint64(dst, uint64(v))
		}
	}
}
