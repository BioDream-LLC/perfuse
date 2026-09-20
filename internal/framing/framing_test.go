package framing

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

// Framing on a stream that is not MLLP.
//
// Every test here is about the same failure: reading the wrong number of bytes produces a message that looks plausible.
// A short read is a truncated message that still parses; a long read is two messages joined into one that also parses.
// So the assertions are always on exact bytes, never on "no error".

func read(t *testing.T, in []byte, s Settings) ([][]byte, error) {
	t.Helper()
	if err := s.Validate(); err != nil {
		t.Fatalf("the settings are invalid: %v", err)
	}

	r := NewReader(bytes.NewReader(in), s)
	var out [][]byte
	for {
		msg, err := r.ReadMessage()
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return out, err
		}
		out = append(out, msg)
	}
}

// A delimiter must separate messages and must not be delivered as content.
func TestADelimiterSeparatesMessagesAndIsNotDelivered(t *testing.T) {
	got, err := read(t, []byte("first\rsecond\rthird\r"),
		Settings{Mode: ModeDelimited, Delimiter: []byte("\r")})
	if err != nil {
		t.Fatal(err)
	}

	want := []string{"first", "second", "third"}
	if len(got) != len(want) {
		t.Fatalf("%d messages, want %d: %q", len(got), len(want), got)
	}
	for i := range want {
		if string(got[i]) != want[i] {
			// The delimiter is framing, not content. Delivered, a downstream parser usually reports a trailing empty
			// segment rather than the delimiter, which sends somebody looking in the wrong place entirely.
			t.Errorf("message %d is %q, want %q", i, got[i], want[i])
		}
	}
}

// A multi-byte delimiter must not be ended by its final byte appearing in the payload.
//
// The reader scans for the last byte of the sequence because that is what bufio can do efficiently. If a coincidental
// match ended the message, a payload containing a bare newline would be cut in half whenever the delimiter was CR LF.
func TestAMultiByteDelimiterIsNotEndedByACoincidentalByte(t *testing.T) {
	got, err := read(t, []byte("line one\nstill the same message\r\nsecond\r\n"),
		Settings{Mode: ModeDelimited, Delimiter: []byte("\r\n")})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("%d messages, want 2: %q", len(got), got)
	}
	if string(got[0]) != "line one\nstill the same message" {
		t.Errorf("the first message was cut at a bare newline: %q", got[0])
	}
}

// A start block must discard what came before it.
//
// Bytes arriving before the start of a message are noise from a partial connection, not data. Kept, they prepend rubbish
// to the first message of every session.
func TestAStartBlockDiscardsWhatCameBeforeIt(t *testing.T) {
	got, err := read(t, []byte("junk\x02real message\x03"),
		Settings{Mode: ModeDelimited, StartBlock: []byte{0x02}, Delimiter: []byte{0x03}})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 {
		t.Fatalf("%d messages, want 1: %q", len(got), got)
	}
	if string(got[0]) != "real message" {
		t.Errorf("got %q; bytes before the start block were kept", got[0])
	}
}

// A truncated final message must be refused, not delivered.
//
// The case where delivering what arrived is exactly wrong, because it will parse. A peer that disconnected mid-message
// has sent something that looks like a shorter valid message.
func TestATruncatedFinalMessageIsRefusedRatherThanDelivered(t *testing.T) {
	got, err := read(t, []byte("complete\rcut off here"),
		Settings{Mode: ModeDelimited, Delimiter: []byte("\r")})

	if len(got) != 1 {
		t.Fatalf("%d messages delivered, want only the complete one: %q", len(got), got)
	}
	if err == nil {
		t.Fatal("the truncated tail was accepted silently")
	}
	if !strings.Contains(err.Error(), "not been delivered") {
		t.Errorf("the error does not say the data was dropped: %v", err)
	}
}

// Fixed-length records must be read exactly, with padding removed.
func TestFixedLengthRecordsAreReadExactly(t *testing.T) {
	got, err := read(t, []byte("AAAA0001BBBB0002"),
		Settings{Mode: ModeFixed, RecordLength: 8, TrimPadding: true})
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 2 {
		t.Fatalf("%d records, want 2: %q", len(got), got)
	}
	if string(got[0]) != "AAAA0001" || string(got[1]) != "BBBB0002" {
		t.Errorf("records read as %q", got)
	}
}

// A short final fixed record must be refused.
//
// There is no way to tell a short record from a cut stream, and padding it out hands a downstream system a record whose
// trailing fields are silently empty rather than absent.
func TestAShortFinalFixedRecordIsRefused(t *testing.T) {
	got, err := read(t, []byte("AAAA0001BB"),
		Settings{Mode: ModeFixed, RecordLength: 8})

	if len(got) != 1 {
		t.Fatalf("%d records delivered, want 1: %q", len(got), got)
	}
	if err == nil {
		t.Fatal("a partial record was accepted")
	}
	if !strings.Contains(err.Error(), "not fixed-length framing") {
		// Naming the likely cause matters: a stream that really does end short is not fixed-length framed, and
		// somebody needs to be told that rather than told about byte counts.
		t.Errorf("the error does not suggest the framing is wrong: %v", err)
	}
}

// A length prefix must be read in both byte orders, and the two must disagree.
//
// If the test passed with either order, it would not be testing the byte order at all.
func TestALengthPrefixIsReadInTheConfiguredByteOrder(t *testing.T) {
	// 5 as a big-endian uint16 is 0x00 0x05; little-endian it is 0x05 0x00.
	big := append([]byte{0x00, 0x05}, []byte("hello")...)

	got, err := read(t, big, Settings{Mode: ModeLengthPrefixed, LengthBytes: 2, BigEndian: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0]) != "hello" {
		t.Fatalf("big-endian read produced %q", got)
	}

	// The same bytes read little-endian advertise 1280, which the stream cannot satisfy. It must fail rather than
	// return something.
	if _, err := read(t, big, Settings{Mode: ModeLengthPrefixed, LengthBytes: 2}); err == nil {
		t.Error("the wrong byte order was accepted, so the byte order setting does nothing")
	}
}

// A length that includes its own header must be handled, in the right direction.
//
// Both conventions exist and the difference is silent: with a four-byte header the payload is wrong by four bytes every
// time, which for a text format usually still parses.
func TestALengthIncludingItsOwnHeaderIsHandled(t *testing.T) {
	// Payload of 5, header of 2, so the advertised length is 7.
	in := append([]byte{0x00, 0x07}, []byte("hello")...)

	got, err := read(t, in, Settings{
		Mode: ModeLengthPrefixed, LengthBytes: 2, BigEndian: true, LengthIncludesHeader: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0]) != "hello" {
		t.Fatalf("read %q, want one message of \"hello\"", got)
	}

	// And set the wrong way round it must not silently produce a longer or shorter payload.
	wrong, err := read(t, in, Settings{Mode: ModeLengthPrefixed, LengthBytes: 2, BigEndian: true})
	if err == nil && len(wrong) == 1 && string(wrong[0]) == "hello" {
		t.Error("length_includes_header made no difference, so one of the two settings is being ignored")
	}
}

// An advertised length larger than the maximum must be refused before anything is allocated.
//
// A two-byte header can advertise 65535 and an eight-byte one more memory than the machine has. Trusting it is one
// remote peer away from an outage.
func TestAnOversizeAdvertisedLengthIsRefusedBeforeAllocating(t *testing.T) {
	in := []byte{0xFF, 0xFF, 0xFF, 0xFF}

	_, err := read(t, in, Settings{Mode: ModeLengthPrefixed, LengthBytes: 4, BigEndian: true, MaxMessageSize: 1024})
	if err == nil {
		t.Fatal("a 4GB advertised length was accepted")
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("the error is not ErrTooLarge, so a caller cannot tell it must close the connection: %v", err)
	}
	// Naming the likely cause, because an advertised length wildly larger than reality is almost always the byte order.
	if !strings.Contains(err.Error(), "byte order") {
		t.Errorf("the error does not suggest what is actually wrong: %v", err)
	}
}

// An eight-byte length with the top bit set must not become negative.
//
// A negative length compared against a maximum passes every check, and then allocating it panics.
func TestAnEightByteLengthWithTheTopBitSetDoesNotBecomeNegative(t *testing.T) {
	in := []byte{0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF, 0xFF}

	_, err := read(t, in, Settings{Mode: ModeLengthPrefixed, LengthBytes: 8, BigEndian: true, MaxMessageSize: 1024})
	if err == nil {
		t.Fatal("a length with every bit set was accepted")
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("expected ErrTooLarge, got %v", err)
	}
}

// Whole-stream framing must deliver everything up to the close as one message.
func TestWholeStreamFramingDeliversEverythingAsOneMessage(t *testing.T) {
	body := "no framing at all\rjust bytes\runtil close\r"

	got, err := read(t, []byte(body), Settings{Mode: ModeWhole})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("%d messages, want 1", len(got))
	}
	if string(got[0]) != body {
		t.Errorf("the stream was altered: %q", got[0])
	}
}

// No framing at all must be refused rather than defaulted.
//
// Reading a stream with the wrong framing produces messages that look plausible rather than an error, so there is
// deliberately no default to fall back on.
func TestNoFramingIsRefusedRatherThanDefaulted(t *testing.T) {
	s := Settings{}
	err := s.Validate()
	if err == nil {
		t.Fatal("a stream with no framing setting was accepted")
	}
	if !strings.Contains(err.Error(), "plausible") {
		t.Errorf("the refusal does not explain why there is no default: %v", err)
	}
}

// A delimited stream with no delimiter must be refused.
func TestADelimitedStreamWithNoDelimiterIsRefused(t *testing.T) {
	s := Settings{Mode: ModeDelimited}
	if err := s.Validate(); err == nil {
		t.Fatal("a delimited stream with no delimiter was accepted")
	}
}

// Framing a message must round-trip through reading it.
//
// The test that matters most for the sender: framing and reading are two halves of one agreement, and a mismatch between
// them is only visible at the far end.
func TestEveryFramingRoundTrips(t *testing.T) {
	cases := []struct {
		name string
		s    Settings
		msg  string
	}{
		{"delimited", Settings{Mode: ModeDelimited, Delimiter: []byte{0x03}}, "a message"},
		{"delimited with start", Settings{
			Mode: ModeDelimited, StartBlock: []byte{0x02}, Delimiter: []byte{0x03}}, "a message"},
		{"fixed", Settings{Mode: ModeFixed, RecordLength: 16, TrimPadding: true}, "short"},
		{"length 2 big", Settings{Mode: ModeLengthPrefixed, LengthBytes: 2, BigEndian: true}, "a message"},
		{"length 4 little", Settings{Mode: ModeLengthPrefixed, LengthBytes: 4}, "a message"},
		{"length including header", Settings{
			Mode: ModeLengthPrefixed, LengthBytes: 4, LengthIncludesHeader: true}, "a message"},
		{"whole", Settings{Mode: ModeWhole}, "a message"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := tc.s
			if err := s.Validate(); err != nil {
				t.Fatal(err)
			}

			framed, err := Frame([]byte(tc.msg), s)
			if err != nil {
				t.Fatalf("framing: %v", err)
			}

			got, err := read(t, framed, s)
			if err != nil {
				t.Fatalf("reading back what we framed: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("%d messages came back from one framed message: %q", len(got), got)
			}
			if string(got[0]) != tc.msg {
				t.Errorf("round trip changed the message: sent %q, read %q", tc.msg, got[0])
			}
		})
	}
}

// Framing must not double a delimiter the message already ends with.
//
// Most HL7 already ends in a carriage return. Doubled, the far end reports a trailing empty segment, which sends
// somebody looking at the message content rather than at the framing.
func TestFramingDoesNotDoubleADelimiterTheMessageAlreadyHas(t *testing.T) {
	msg := []byte("MSH|one\r")

	framed, err := Frame(msg, Settings{Mode: ModeDelimited, Delimiter: []byte("\r")})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(framed, msg) {
		t.Errorf("framed as %q; a delimiter was added to a message that already ended with one", framed)
	}
}

// A message too large for its own length header must be refused, not truncated.
//
// Encoded silently, a 70,000-byte message with a two-byte header advertises 4,464. The far end reads 4,464 bytes as a
// complete message and interprets the rest as the next one, so every subsequent message on that connection is garbage
// and the cause is thousands of bytes back.
func TestAMessageTooLargeForItsLengthHeaderIsRefused(t *testing.T) {
	big := bytes.Repeat([]byte("x"), 70000)

	_, err := Frame(big, Settings{Mode: ModeLengthPrefixed, LengthBytes: 2, MaxMessageSize: 1 << 20})
	if err == nil {
		t.Fatal("a message too large for its own header was framed anyway")
	}
	if !strings.Contains(err.Error(), "misread") {
		t.Errorf("the error does not explain the consequence: %v", err)
	}
}

// A message too long for a fixed record must be refused, not truncated.
func TestAMessageTooLongForAFixedRecordIsRefused(t *testing.T) {
	_, err := Frame([]byte("far too long for the record"), Settings{Mode: ModeFixed, RecordLength: 8})
	if err == nil {
		t.Fatal("an oversize message was truncated into a fixed record")
	}
	if !strings.Contains(err.Error(), "silently missing") {
		t.Errorf("the error does not say what truncation would cost: %v", err)
	}
}

// The description must name control characters, not print their bytes.
//
// Somebody configuring this has a manual in front of them that says STX and ETX. A log line saying 0x02 makes them
// convert, and a log line saying an unprintable character makes them guess.
func TestTheDescriptionNamesControlCharacters(t *testing.T) {
	s := Settings{Mode: ModeDelimited, StartBlock: []byte{0x02}, Delimiter: []byte{0x03}}
	got := s.Describe()

	for _, want := range []string{"STX", "ETX"} {
		if !strings.Contains(got, want) {
			t.Errorf("the description %q does not name %s", got, want)
		}
	}
}
