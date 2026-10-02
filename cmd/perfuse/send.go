package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/mllp"
)

func cmdSend(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("send", flag.ContinueOnError)
	fset.SetOutput(stderr)
	addr := fset.String("addr", "", "destination host:port (required)")
	timeout := fset.Duration("timeout", 30*time.Second, "per-message timeout")
	delay := fset.Duration("delay", 0, "wait this long between messages")
	showAck := fset.Bool("show-ack", false, "print each acknowledgement in full")
	stopOnError := fset.Bool("stop-on-error", false, "stop at the first rejection or failure")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *addr == "" {
		return errors.New("send needs -addr host:port")
	}

	var messages [][]byte
	if fset.NArg() == 0 {
		// Read from stdin so messages can be piped in.
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return err
		}
		messages = splitMessages(raw)
	} else {
		for _, path := range fset.Args() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			found := splitMessages(raw)
			if len(found) == 0 {
				return fmt.Errorf("%s: no HL7 messages found", path)
			}
			messages = append(messages, found...)
		}
	}
	if len(messages) == 0 {
		return errors.New("no HL7 messages found in the input")
	}

	c := &mllp.Client{Addr: *addr, Timeout: *timeout}
	defer c.Close()

	var accepted, rejected, failed int
	for i, msg := range messages {
		if i > 0 && *delay > 0 {
			time.Sleep(*delay)
		}

		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		reply, err := c.Send(ctx, msg)
		cancel()

		label := describeMessage(msg)
		if err != nil {
			failed++
			fmt.Fprintf(stdout, "%3d  %-24s  FAILED   %v\n", i+1, label, err)
			if *stopOnError {
				return fmt.Errorf("stopped at message %d: %w", i+1, err)
			}
			continue
		}

		code, text := describeAck(reply)
		switch code {
		case "AA", "CA":
			accepted++
		default:
			rejected++
		}

		fmt.Fprintf(stdout, "%3d  %-24s  %-7s  %s\n", i+1, label, code, text)
		if *showAck {
			fmt.Fprintf(stdout, "     %s\n", printable(reply))
		}
		if *stopOnError && code != "AA" && code != "CA" {
			return fmt.Errorf("stopped at message %d: acknowledgement %s: %s", i+1, code, text)
		}
	}

	fmt.Fprintf(stdout, "\n%d sent: %d accepted, %d rejected, %d failed\n",
		len(messages), accepted, rejected, failed)

	if rejected > 0 || failed > 0 {
		return errBlocking
	}
	return nil
}

// splitMessages finds every HL7 message in a byte stream.
//
// Files come in three shapes: one message, several MLLP-framed messages
// concatenated, or several unframed messages separated by blank lines. All three
// are common, so all three are accepted.
func splitMessages(raw []byte) [][]byte {
	var out [][]byte

	// MLLP-framed, which is what perfuse listen -write-dir produces.
	if bytes.IndexByte(raw, mllp.StartBlock) >= 0 {
		r := mllp.NewReader(bytes.NewReader(raw), 0)
		for {
			msg, err := r.ReadMessage()
			if err != nil {
				break
			}
			out = append(out, msg)
		}
		if len(out) > 0 {
			return out
		}
	}

	// Otherwise split on MSH boundaries, which handles both a single message and
	// several run together.
	normalised := bytes.ReplaceAll(raw, []byte("\r\n"), []byte("\r"))
	normalised = bytes.ReplaceAll(normalised, []byte("\n"), []byte("\r"))

	for len(normalised) > 0 {
		start := bytes.Index(normalised, []byte("MSH"))
		if start < 0 {
			break
		}
		normalised = normalised[start:]

		// The next message begins at the next MSH that follows a segment
		// terminator, so an MSH appearing inside data does not split a message.
		// The terminator belongs to the message before it and must be kept.
		next := bytes.Index(normalised[1:], []byte("\rMSH"))
		if next < 0 {
			out = append(out, bytes.TrimLeft(normalised, "\r"))
			break
		}
		end := next + 2 // past the carriage return
		out = append(out, normalised[:end])
		normalised = normalised[end:]
	}
	return out
}

// describeMessage labels a message for the progress line.
func describeMessage(raw []byte) string {
	m, err := hl7.Parse(raw)
	if err != nil {
		return fmt.Sprintf("unparseable (%d bytes)", len(raw))
	}
	typ, event, _ := m.Type()
	if typ == "" {
		typ = "?"
	}
	label := typ
	if event != "" {
		label += "^" + event
	}
	if id := m.ControlID(); id != "" {
		label += " " + id
	}
	return label
}

// describeAck pulls the acknowledgement code and reason out of a reply.
func describeAck(reply []byte) (code, text string) {
	m, err := hl7.Parse(reply)
	if err != nil {
		return "?", fmt.Sprintf("unparseable acknowledgement: %v", err)
	}
	code = m.MustGet("MSA-1")
	if code == "" {
		code = "?"
	}
	text = m.MustGet("MSA-3")
	if text == "" && (code == "AA" || code == "CA") {
		text = "accepted"
	}
	return code, text
}

// printable renders a message on one line with visible segment boundaries.
func printable(raw []byte) string {
	return string(bytes.ReplaceAll(bytes.TrimRight(raw, "\r"), []byte("\r"), []byte(" | ")))
}
