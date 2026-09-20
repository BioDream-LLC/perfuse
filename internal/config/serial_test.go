package config

import (
	"strings"
	"testing"
	"time"
)

// Serial port settings.
//
// Everything here is about one property of a serial line: a wrong setting does not fail, it delivers plausible-looking
// rubbish. Framing bytes appear at random, occasional runs decode as printable characters, and a lenient parser accepts
// some of it. So the settings that decide how bytes are interpreted are validated hard, and the ones that cannot be
// validated are warned about.
//
// Not tested here: opening an actual port. There is no serial hardware on this machine, so the protocol side of this
// connector is unverified and saying so is more useful than a test that pretends otherwise.

func serialChannel(t *testing.T, block string) error {
	t.Helper()

	// Re-indented here so each test can write its settings at a readable indent rather than counting spaces to line up
	// under two levels of nesting.
	var indented strings.Builder
	for _, line := range strings.Split(strings.TrimRight(block, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		indented.WriteString("    " + strings.TrimSpace(line) + "\n")
	}

	_, err := Load(strings.NewReader(`
name: device
dataType: raw
source:
  type: serial
  serial:
`+indented.String()+`
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "device.yaml")
	return err
}

// A missing baud rate must be refused, with the reason.
//
// There is deliberately no default. A wrong speed produces readable-looking nonsense rather than an error, so a guessed
// default would be the worst possible behaviour: it would appear to work.
func TestASerialSourceWithNoBaudRateIsRefused(t *testing.T) {
	err := serialChannel(t, "  port: /dev/ttyUSB0\n  framing: delimited\n  delimiter: \"\\r\"\n")
	if err == nil {
		t.Fatal("a serial source with no baud rate was accepted")
	}
	if !strings.Contains(err.Error(), "plausible-looking rubbish") {
		t.Errorf("the refusal does not explain why there is no default: %v", err)
	}
}

// A non-standard baud rate must be reported rather than accepted quietly.
//
// Occasionally genuine, far more often a typo, and a typo here produces exactly the nonsense described above.
func TestANonStandardBaudRateIsReported(t *testing.T) {
	err := serialChannel(t, "  port: /dev/ttyUSB0\n  baud: 9601\n  framing: delimited\n  delimiter: \"\\r\"\n")
	if err == nil {
		t.Fatal("a mistyped baud rate was accepted silently")
	}
	if !strings.Contains(err.Error(), "manual") {
		t.Errorf("the message does not tell somebody where to check: %v", err)
	}
}

// A standard baud rate with valid framing must be accepted.
//
// The test that stops all the others from passing vacuously: if this failed, every refusal above would be meaningless.
func TestAValidSerialSourceIsAccepted(t *testing.T) {
	err := serialChannel(t, "  port: /dev/ttyUSB0\n  baud: 9600\n  framing: delimited\n  delimiter: \"\\r\"\n")
	if err != nil {
		t.Fatalf("a valid serial source was refused: %v", err)
	}
}

// Whole-stream framing on a serial line must be refused.
//
// It ends a message when the connection closes, and a serial line does not close. Nothing would ever be delivered, and
// the channel would look healthy while receiving nothing.
func TestWholeStreamFramingOnASerialLineIsRefused(t *testing.T) {
	err := serialChannel(t, "  port: /dev/ttyUSB0\n  baud: 9600\n  framing: whole\n")
	if err == nil {
		t.Fatal("whole-stream framing was accepted on a serial line")
	}
	if !strings.Contains(err.Error(), "does not close") {
		t.Errorf("the refusal does not explain why it cannot work: %v", err)
	}
}

// Invalid line settings must each be refused by name.
func TestInvalidSerialLineSettingsAreRefused(t *testing.T) {
	cases := []struct {
		name  string
		block string
		want  string
	}{
		{"data bits", "  data_bits: 9\n", "5, 6, 7 or 8"},
		{"parity", "  parity: sometimes\n", "none, odd, even, mark or space"},
		{"stop bits", "  stop_bits: \"3\"\n", "1, 1.5 or 2"},
		{"flow control", "  flow_control: maybe\n", "none, hardware or software"},
	}

	base := "  port: /dev/ttyUSB0\n  baud: 9600\n  framing: delimited\n  delimiter: \"\\r\"\n"

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := serialChannel(t, base+tc.block)
			if err == nil {
				t.Fatalf("an invalid %s was accepted", tc.name)
			}
			// Listing the valid values matters more than naming the fault: the commonest reason to hit this is not
			// knowing what the options are.
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("the error does not list the valid values (%q): %v", tc.want, err)
			}
		})
	}
}

// A source with no quiet_after must warn, because nothing else will notice a dead feed.
func TestASerialSourceWithoutQuietAfterWarns(t *testing.T) {
	s := &SerialSource{Port: "/dev/ttyUSB0", Baud: 9600}
	s.Framing = "delimited"
	s.Delimiter = `\r`

	if errs := s.Validate(); len(errs) > 0 {
		t.Fatalf("the settings are invalid: %v", errs)
	}

	var found bool
	for _, w := range s.Warnings() {
		if strings.Contains(w, "no connection to lose") {
			found = true
		}
	}
	if !found {
		t.Errorf("nothing warned that a silent feed would go unnoticed: %v", s.Warnings())
	}
}

// The default maximum message size must be far smaller than the socket one.
//
// A serial line at 9600 baud carries about a kilobyte a second, so a sixteen-megabyte message would take four and a half
// hours. A message that large is a framing error, and treating it as legitimate means holding it in memory while the real
// problem goes unreported.
func TestTheSerialMessageLimitReflectsTheLineSpeed(t *testing.T) {
	s := &SerialSource{Port: "/dev/ttyUSB0", Baud: 9600}
	s.ApplyDefaults()

	if s.MaxMessageSize != 1<<20 {
		t.Errorf("the default maximum is %d bytes", s.MaxMessageSize)
	}
	if s.MaxMessageSize >= 16<<20 {
		t.Error("the serial default matches the socket default, which at 9600 baud is over four hours of data")
	}
}

// Seven data bits with no parity must be flagged.
func TestSevenDataBitsWithNoParityIsFlagged(t *testing.T) {
	s := &SerialSource{Port: "/dev/ttyUSB0", Baud: 9600, DataBits: 7, QuietAfter: time.Hour}
	s.Framing = "delimited"
	s.Delimiter = `\r`
	s.ApplyDefaults()

	var found bool
	for _, w := range s.Warnings() {
		if strings.Contains(w, "seven data bits") || strings.Contains(w, "Seven-bit") {
			found = true
		}
	}
	if !found {
		t.Errorf("nothing flagged 7-none, which usually means the parity setting was missed: %v", s.Warnings())
	}
}
