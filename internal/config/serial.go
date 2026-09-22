package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// SerialSource reads messages from a serial port.
//
// # Why a serial connector in 2026
//
// Because the equipment is still there. A blood gas analyser bought in 2009 with a working sensor and a 25-pin socket, a
// bedside monitor, a scale in a dialysis unit, an older anaesthesia machine. None of them will ever get an Ethernet port,
// several of them cost six figures, and the alternative to reading them is somebody typing results into a form.
//
// Mirth has a serial connector. Perfuse had nothing, and a site with one of these had no route in at all.
//
// # Framing is shared with the TCP connector
//
// The framings are the same wherever the bytes come from: an analyser that speaks STX/ETX over a socket speaks STX/ETX
// over a cable. So this reuses the TCP framing block rather than defining its own, which also means a device moved from a
// serial cable to a serial-to-Ethernet adapter keeps its framing configuration.
//
// # What is different about a serial line
//
// There is no connection. A socket tells you when the peer went away; a cable does not. An unplugged cable, a device
// switched off, and a device with nothing to say are indistinguishable - all three are silence. That is why quiet_after
// exists: without something to say "this line has been silent for longer than it should be", a dead feed looks exactly
// like a quiet night.
type SerialSource struct {
	// Port is the device: /dev/ttyUSB0, /dev/tty.usbserial-A1, COM3.
	Port string `yaml:"port"`

	// Baud is the speed. Required, because guessing produces bytes rather than an error.
	//
	// A wrong baud rate does not fail. It delivers plausible-looking rubbish: framing bytes appear at random, occasional
	// runs decode as printable characters, and a lenient parser accepts some of it. There is deliberately no default.
	Baud int `yaml:"baud"`

	// DataBits is 5, 6, 7 or 8. Defaults to 8.
	DataBits int `yaml:"data_bits,omitempty"`

	// Parity is none, odd, even, mark or space. Defaults to none.
	Parity string `yaml:"parity,omitempty"`

	// StopBits is 1, 1.5 or 2, written as "1", "1.5" or "2". Defaults to 1.
	StopBits string `yaml:"stop_bits,omitempty"`

	// FlowControl is none, hardware or software. Defaults to none.
	//
	// Worth setting correctly rather than leaving. With hardware flow control expected and not configured, a device stops
	// sending partway through a long message and the result is a truncated message rather than a failure.
	FlowControl string `yaml:"flow_control,omitempty"`

	TCPFraming `yaml:",inline"`

	// MaxMessageSize bounds one message.
	MaxMessageSize int `yaml:"max_message_size,omitempty"`

	// QuietAfter logs a warning when nothing has been received for this long.
	//
	// The only way to notice a dead serial feed. An unplugged cable and a quiet night are the same silence, so somebody
	// has to say how long is too long for this particular device.
	QuietAfter time.Duration `yaml:"quiet_after,omitempty"`

	// Reply is what to send back after each message: none, ack, or text.
	Reply string `yaml:"reply,omitempty"`

	// ReplyText is the literal reply when Reply is text, with Go escapes.
	ReplyText string `yaml:"reply_text,omitempty"`

	// ReopenAfter is how long to wait before reopening a port that failed.
	//
	// A USB serial adapter unplugged and plugged back in comes back as the same device path but a different kernel
	// handle, so the old one returns errors forever. Reopening is the only recovery, and doing it in a tight loop fills
	// the log.
	ReopenAfter time.Duration `yaml:"reopen_after,omitempty"`
}

// Valid parity settings.
const (
	ParityNone  = "none"
	ParityOdd   = "odd"
	ParityEven  = "even"
	ParityMark  = "mark"
	ParitySpace = "space"
)

// Valid flow control settings.
const (
	FlowNone     = "none"
	FlowHardware = "hardware"
	FlowSoftware = "software"
)

// ApplyDefaults fills in what was not set.
//
// Baud is deliberately absent: there is no safe default for it.
func (s *SerialSource) ApplyDefaults() {
	if s.DataBits == 0 {
		s.DataBits = 8
	}
	if s.Parity == "" {
		s.Parity = ParityNone
	}
	if s.StopBits == "" {
		s.StopBits = "1"
	}
	if s.FlowControl == "" {
		s.FlowControl = FlowNone
	}
	if s.MaxMessageSize == 0 {
		// Smaller than the socket default on purpose. A serial line at 9600 baud carries about a kilobyte a second, so a
		// sixteen-megabyte message would take four and a half hours and is certainly a framing error rather than a
		// message.
		s.MaxMessageSize = 1 << 20
	}
	if s.Reply == "" {
		s.Reply = ReplyNone
	}
	if s.ReopenAfter == 0 {
		s.ReopenAfter = 5 * time.Second
	}
}

// Validate checks the settings.
func (s *SerialSource) Validate() []error {
	s.ApplyDefaults()

	var errs []error

	if strings.TrimSpace(s.Port) == "" {
		errs = append(errs, errors.New("a serial source needs port: the device, such as /dev/ttyUSB0 or COM3"))
	}

	if s.Baud <= 0 {
		errs = append(errs, errors.New("a serial source needs baud, and there is deliberately no default. A wrong "+
			"speed does not fail: it delivers plausible-looking rubbish, because framing bytes appear at random and "+
			"occasional runs decode as printable characters. The device's manual states it"))
	} else if !knownBaud(s.Baud) {
		// A warning would be wrong here. An unusual rate is occasionally genuine, but far more often it is a typo, and a
		// typo in a baud rate produces exactly the plausible rubbish described above.
		errs = append(errs, fmt.Errorf("baud is %d, which is not one of the standard rates. If the device really "+
			"uses it, it will work, but check it against the manual first: a mistyped rate produces readable-looking "+
			"nonsense rather than an error", s.Baud))
	}

	switch s.DataBits {
	case 5, 6, 7, 8:
	default:
		errs = append(errs, fmt.Errorf("data_bits is %d; it must be 5, 6, 7 or 8", s.DataBits))
	}

	switch s.Parity {
	case ParityNone, ParityOdd, ParityEven, ParityMark, ParitySpace:
	default:
		errs = append(errs, fmt.Errorf("parity is %q; it must be none, odd, even, mark or space", s.Parity))
	}

	switch s.StopBits {
	case "1", "1.5", "2":
	default:
		errs = append(errs, fmt.Errorf("stop_bits is %q; it must be 1, 1.5 or 2", s.StopBits))
	}

	switch s.FlowControl {
	case FlowNone, FlowHardware, FlowSoftware:
	default:
		errs = append(errs, fmt.Errorf("flow_control is %q; it must be none, hardware or software", s.FlowControl))
	}

	errs = append(errs, s.validateFraming("serial source", s.MaxMessageSize)...)
	errs = append(errs, s.validateReadable("serial source")...)

	// Whole-stream framing needs a close to mark the end of a message, and a serial line never closes.
	if s.Framing == "whole" {
		errs = append(errs, errors.New("framing is whole, which ends a message when the connection closes. A serial "+
			"line does not close, so nothing would ever be delivered. A serial device needs a delimiter, a fixed "+
			"record length or a length prefix"))
	}

	switch s.Reply {
	case ReplyNone, ReplyACK:
	case ReplyText:
		if s.ReplyText == "" {
			errs = append(errs, errors.New("serial source: reply is text but reply_text is empty. A device waiting "+
				"for a reply usually retries the same message forever"))
		}
	default:
		errs = append(errs, fmt.Errorf("serial source: reply is %q; it must be none, ack or text", s.Reply))
	}

	if s.QuietAfter < 0 {
		errs = append(errs, errors.New("quiet_after cannot be negative"))
	}
	if s.ReopenAfter < 0 {
		errs = append(errs, errors.New("reopen_after cannot be negative"))
	}

	return errs
}

// ReplyBytes resolves the literal reply.
func (s *SerialSource) ReplyBytes() ([]byte, error) { return unescapeBytes(s.ReplyText) }

// Warnings reports settings that are legal but usually mistakes.
func (s *SerialSource) Warnings() []string {
	var out []string

	if s.QuietAfter == 0 {
		out = append(out, "quiet_after is not set, so nothing will report this feed going silent. A serial line has "+
			"no connection to lose: an unplugged cable, a device switched off and a device with nothing to say are "+
			"the same silence. Set it to somewhat longer than the longest gap this device normally leaves")
	}

	if s.FlowControl == FlowNone && s.Baud >= 19200 {
		out = append(out, fmt.Sprintf("flow control is none at %d baud. If the device expects hardware flow "+
			"control it will stop partway through a long message, and the result is a truncated message rather "+
			"than an error", s.Baud))
	}

	if s.Parity == ParityNone && s.DataBits == 7 {
		out = append(out, "seven data bits with no parity is an unusual combination and often means the parity "+
			"setting was missed. Seven-bit devices almost always use even or odd parity")
	}

	return out
}

// knownBaud reports whether a rate is one of the standard ones.
func knownBaud(b int) bool {
	for _, r := range []int{
		110, 300, 600, 1200, 2400, 4800, 9600, 14400, 19200, 38400, 57600, 115200, 230400, 460800, 921600,
	} {
		if b == r {
			return true
		}
	}
	return false
}
