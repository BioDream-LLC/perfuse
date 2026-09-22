package config

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/framing"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// TCPFraming is how message boundaries are found on a raw socket.
//
// Shared by the TCP source and the TCP destination, and by the serial source, because the framings are the same wherever
// the bytes come from. A laboratory analyser that speaks STX/ETX over a socket speaks STX/ETX over a serial cable too.
type TCPFraming struct {
	// Framing is mllp, delimited, fixed, length or whole.
	//
	// Required, with no default. Reading a stream with the wrong framing produces messages that look plausible rather
	// than an error, so guessing is worse than asking.
	Framing string `yaml:"framing"`

	// Delimiter ends a message, written as a string with Go escapes: "\r", "\x03".
	Delimiter string `yaml:"delimiter,omitempty"`

	// StartBlock optionally begins a message. Bytes before it are discarded as noise from a partial connection.
	StartBlock string `yaml:"start_block,omitempty"`

	// KeepDelimiter includes the delimiter in the message. Off by default: it is framing, not content.
	KeepDelimiter bool `yaml:"keep_delimiter,omitempty"`

	// RecordLength is the message size for fixed framing.
	RecordLength int `yaml:"record_length,omitempty"`

	// TrimPadding removes trailing spaces and NULs from a fixed record. On by default.
	TrimPadding *bool `yaml:"trim_padding,omitempty"`

	// LengthBytes is the width of a length header: 1, 2, 4 or 8.
	LengthBytes int `yaml:"length_bytes,omitempty"`

	// BigEndian reads and writes the length header most significant byte first.
	BigEndian bool `yaml:"big_endian,omitempty"`

	// LengthIncludesHeader says the advertised length counts the header itself.
	LengthIncludesHeader bool `yaml:"length_includes_header,omitempty"`
}

// Settings resolves the framing, turning escapes into bytes.
func (f TCPFraming) Settings(maxSize int) (framing.Settings, error) {
	delim, err := unescapeBytes(f.Delimiter)
	if err != nil {
		return framing.Settings{}, fmt.Errorf("delimiter: %w", err)
	}
	start, err := unescapeBytes(f.StartBlock)
	if err != nil {
		return framing.Settings{}, fmt.Errorf("start_block: %w", err)
	}

	trim := true
	if f.TrimPadding != nil {
		trim = *f.TrimPadding
	}

	s := framing.Settings{
		Mode:                 framing.Mode(f.Framing),
		Delimiter:            delim,
		StartBlock:           start,
		KeepDelimiter:        f.KeepDelimiter,
		RecordLength:         f.RecordLength,
		TrimPadding:          trim,
		LengthBytes:          f.LengthBytes,
		BigEndian:            f.BigEndian,
		LengthIncludesHeader: f.LengthIncludesHeader,
		MaxMessageSize:       maxSize,
	}
	if err := s.Validate(); err != nil {
		return framing.Settings{}, err
	}
	return s, nil
}

// validateFraming checks the framing settings and reports settings that do not apply.
//
// Reported rather than ignored. A record_length set on a delimited stream means somebody believed one of the two was
// taking effect, and silence leaves them believing it.
// validateFraming checks the framing settings and reports settings that do not apply.
//
// Direction is checked separately, by validateReadable and validateWritable, because
// the two are not the same set: mllp can be neither read nor written by the generic
// code here, and a mode added later might be one and not the other.
func (f TCPFraming) validateReadable(what string) []error {
	if mode := framing.Mode(f.Framing); f.Framing != "" && !framing.CanRead(mode) {
		return []error{fmt.Errorf("%s: framing %q cannot be read on a raw socket. "+
			"For inbound MLLP use a source of type mllp, which also sends the acknowledgement; "+
			"a tcp or serial source can read framing delimited, fixed, length or whole", what, f.Framing)}
	}
	return nil
}

// validateWritable is the sending half of validateReadable.
func (f TCPFraming) validateWritable(what string) []error {
	if mode := framing.Mode(f.Framing); f.Framing != "" && !framing.CanFrame(mode) {
		return []error{fmt.Errorf("%s: framing %q cannot be sent, only configured. "+
			"For outbound MLLP use a destination of type mllp, which also reads the "+
			"acknowledgement back; a tcp destination can write framing delimited, fixed, "+
			"length or whole", what, f.Framing)}
	}
	return nil
}

func (f TCPFraming) validateFraming(what string, maxSize int) []error {
	var errs []error

	if _, err := f.Settings(maxSize); err != nil {
		errs = append(errs, fmt.Errorf("%s: %w", what, err))
		return errs
	}

	mode := framing.Mode(f.Framing)

	if f.RecordLength > 0 && mode != framing.ModeFixed {
		errs = append(errs, fmt.Errorf("%s: record_length is set but framing is %q, so it has no effect. "+
			"Fixed-length records need framing: fixed", what, f.Framing))
	}
	if f.LengthBytes > 0 && mode != framing.ModeLengthPrefixed {
		errs = append(errs, fmt.Errorf("%s: length_bytes is set but framing is %q, so it has no effect. "+
			"A length header needs framing: length", what, f.Framing))
	}
	if f.Delimiter != "" && mode != framing.ModeDelimited {
		errs = append(errs, fmt.Errorf("%s: a delimiter is set but framing is %q, so it has no effect. "+
			"Set framing: delimited to use it", what, f.Framing))
	}
	if f.StartBlock != "" && mode != framing.ModeDelimited {
		errs = append(errs, fmt.Errorf("%s: start_block is set but framing is %q, so it has no effect", what, f.Framing))
	}
	if f.LengthIncludesHeader && mode != framing.ModeLengthPrefixed {
		errs = append(errs, fmt.Errorf("%s: length_includes_header is set but framing is %q, so it has no effect",
			what, f.Framing))
	}

	return errs
}

// TCPSource listens on a socket for messages that are not necessarily HL7.
//
// # Why this is separate from an mllp source
//
// MLLP is one framing and Perfuse has always spoken it. A great deal of equipment does not: a laboratory analyser, a
// scale, a bedside monitor, an older billing system. Each tends to have its own convention, and Mirth's TCP connector
// covers all of them, so a site with a socket feed that was not HL7 could not use Perfuse at all.
type TCPSource struct {
	// Listen is the bind address, for example ":6000".
	Listen string `yaml:"listen"`

	TCPFraming `yaml:",inline"`

	// MaxMessageSize bounds one message.
	MaxMessageSize int `yaml:"max_message_size,omitempty"`

	// IdleTimeout closes a connection that has been silent this long. Zero means never, which is usually right: a
	// device feed holds a connection open for months and goes quiet overnight.
	IdleTimeout time.Duration `yaml:"idle_timeout,omitempty"`

	// MaxConnections bounds concurrent connections. Zero means unlimited.
	MaxConnections int `yaml:"max_connections,omitempty"`

	// Reply is what to send back after each message: nothing, ack, or a fixed string.
	//
	// Most raw feeds expect nothing. Some expect a single ACK byte. A device that expects a reply and does not get one
	// usually retries the same message forever, so this is worth being explicit about.
	Reply string `yaml:"reply,omitempty"`

	// ReplyText is the literal reply when Reply is text, with Go escapes.
	ReplyText string `yaml:"reply_text,omitempty"`

	// TLS encrypts inbound connections and can require a client certificate.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
}

// Reply behaviours.
const (
	// ReplyNone sends nothing back, which is what most raw device feeds expect.
	ReplyNone = "none"

	// ReplyACK sends a single 0x06 byte, the convention among devices that want one.
	ReplyACK = "ack"

	// ReplyText sends a configured literal.
	ReplyText = "text"
)

// ApplyDefaults fills in what was not set.
func (t *TCPSource) ApplyDefaults() {
	if t.MaxMessageSize == 0 {
		t.MaxMessageSize = 16 << 20
	}
	if t.Reply == "" {
		t.Reply = ReplyNone
	}
}

// Validate checks the settings.
func (t *TCPSource) Validate() []error {
	t.ApplyDefaults()

	var errs []error

	if strings.TrimSpace(t.Listen) == "" {
		errs = append(errs, errors.New("a tcp source needs listen: the address to accept connections on"))
	}

	errs = append(errs, t.validateFraming("tcp source", t.MaxMessageSize)...)
	errs = append(errs, t.validateReadable("tcp source")...)

	switch t.Reply {
	case ReplyNone, ReplyACK:
	case ReplyText:
		if t.ReplyText == "" {
			errs = append(errs, errors.New("tcp source: reply is text but reply_text is empty, so nothing would "+
				"be sent. A device waiting for a reply usually retries the same message forever"))
		}
	default:
		errs = append(errs, fmt.Errorf("tcp source: reply is %q; it must be none, ack or text", t.Reply))
	}

	// The combination that cannot work. Whole-stream framing ends a message when the peer closes the connection, so
	// there is nothing left to reply on.
	if framing.Mode(t.Framing) == framing.ModeWhole && t.Reply != ReplyNone {
		errs = append(errs, errors.New("tcp source: framing is whole, which means a message ends when the peer "+
			"closes the connection, so there is no open connection left to reply on. Either the framing or the "+
			"reply is wrong"))
	}

	if t.MaxMessageSize < 0 {
		errs = append(errs, errors.New("tcp source: max_message_size cannot be negative"))
	}
	if t.MaxConnections < 0 {
		errs = append(errs, errors.New("tcp source: max_connections cannot be negative"))
	}

	if t.TLS != nil {
		for _, err := range t.TLS.Validate(true) {
			errs = append(errs, fmt.Errorf("tcp source tls: %w", err))
		}
	}

	return errs
}

// ReplyBytes resolves the literal reply, turning escapes into bytes.
func (t *TCPSource) ReplyBytes() ([]byte, error) { return unescapeBytes(t.ReplyText) }

// Warnings reports settings that are legal but usually mistakes.
func (t *TCPSource) Warnings() []string {
	var out []string

	if framing.Mode(t.Framing) == framing.ModeFixed {
		out = append(out, fmt.Sprintf("fixed-length framing reads exactly %d bytes per message. One byte out of "+
			"step and every message after it is misaligned, and neither end can detect that. If the far end "+
			"offers a length prefix or a delimiter, prefer it", t.RecordLength))
	}

	if framing.Mode(t.Framing) == framing.ModeWhole {
		out = append(out, "whole-stream framing means one message per connection, ending when the peer "+
			"disconnects. A peer that holds the connection open will never deliver anything")
	}

	if t.TLS == nil && !isLoopback(t.Listen) {
		out = append(out, "this socket accepts connections from anywhere on the network with no TLS, so messages "+
			"arrive in clear text. A device that cannot do TLS is common and often the real answer, but it is "+
			"worth knowing rather than assuming")
	}

	return out
}

// TCPDest sends messages to a socket with configurable framing.
type TCPDest struct {
	// Address is host:port.
	Address string `yaml:"address"`

	TCPFraming `yaml:",inline"`

	// MaxMessageSize bounds one message.
	MaxMessageSize int `yaml:"max_message_size,omitempty"`

	// Timeout bounds connecting and writing.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// ExpectReply waits for bytes back before reporting success.
	//
	// Off by default, and worth thinking about. With it off, "delivered" means the bytes reached the operating
	// system's send buffer, which a peer that crashed a moment later never read. With it on, a peer that never
	// replies makes every message time out.
	ExpectReply bool `yaml:"expect_reply,omitempty"`

	// KeepAlive holds one connection open across messages rather than dialling per message.
	//
	// Most device endpoints expect this; some refuse a second message on the same connection. Off by default because a
	// connection per message is the behaviour that works everywhere, at the cost of a handshake each time.
	KeepAlive bool `yaml:"keep_alive,omitempty"`

	// TLS encrypts the outbound connection.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`
}

// ApplyDefaults fills in what was not set.
func (t *TCPDest) ApplyDefaults() {
	if t.MaxMessageSize == 0 {
		t.MaxMessageSize = 16 << 20
	}
	if t.Timeout == 0 {
		t.Timeout = 30 * time.Second
	}
}

// Validate checks the settings.
func (t *TCPDest) Validate() []error {
	t.ApplyDefaults()

	var errs []error

	if strings.TrimSpace(t.Address) == "" {
		errs = append(errs, errors.New("a tcp destination needs address"))
	}

	errs = append(errs, t.validateFraming("tcp destination", t.MaxMessageSize)...)

	errs = append(errs, t.validateWritable("tcp destination")...)

	if framing.Mode(t.Framing) == framing.ModeWhole {
		if t.KeepAlive {
			errs = append(errs, errors.New("tcp destination: framing is whole, which means the message ends when "+
				"the connection closes, so the connection cannot be kept alive for a second message"))
		}
		if t.ExpectReply {
			errs = append(errs, errors.New("tcp destination: framing is whole, so the connection is closed to "+
				"mark the end of the message and there is nothing left to read a reply on"))
		}
	}

	if t.Timeout < 0 {
		errs = append(errs, errors.New("tcp destination: timeout cannot be negative"))
	}

	if t.TLS != nil {
		for _, err := range t.TLS.Validate(false) {
			errs = append(errs, fmt.Errorf("tcp destination tls: %w", err))
		}
	}

	return errs
}

// unescapeBytes turns a configured string with Go escapes into bytes.
//
// Needed because these are control characters and YAML has no good way to write them. A manual says STX, somebody types
// "\x02", and without this it arrives as four literal characters that no device will ever send.
func unescapeBytes(s string) ([]byte, error) {
	if s == "" {
		return nil, nil
	}

	var out []byte
	for i := 0; i < len(s); {
		c := s[i]
		if c != '\\' {
			out = append(out, c)
			i++
			continue
		}
		if i+1 >= len(s) {
			return nil, fmt.Errorf("%q ends with a backslash, which escapes nothing", s)
		}
		switch s[i+1] {
		case 'r':
			out = append(out, '\r')
			i += 2
		case 'n':
			out = append(out, '\n')
			i += 2
		case 't':
			out = append(out, '\t')
			i += 2
		case '0':
			out = append(out, 0)
			i += 2
		case '\\':
			out = append(out, '\\')
			i += 2
		case 'x':
			if i+3 >= len(s) {
				return nil, fmt.Errorf("%q ends in the middle of a hex escape; it needs two digits, as in \\x02", s)
			}
			var v byte
			for _, d := range []byte{s[i+2], s[i+3]} {
				switch {
				case d >= '0' && d <= '9':
					v = v<<4 | (d - '0')
				case d >= 'a' && d <= 'f':
					v = v<<4 | (d - 'a' + 10)
				case d >= 'A' && d <= 'F':
					v = v<<4 | (d - 'A' + 10)
				default:
					return nil, fmt.Errorf("%q is not a hex escape; \\x needs two hex digits, as in \\x02", s[i:])
				}
			}
			out = append(out, v)
			i += 4
		default:
			return nil, fmt.Errorf("\\%c is not a recognised escape. Use \\r, \\n, \\t, \\0, \\\\ or \\xNN",
				s[i+1])
		}
	}
	return out, nil
}

// isLoopback reports whether a listen address is confined to this machine.
func isLoopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	return host == "127.0.0.1" || host == "localhost" || host == "::1"
}
