package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/generate"
	"github.com/biodream-llc/perfuse/mllp"
)

// perfuse generate makes synthetic HL7 v2 traffic.
//
// Two audiences. Somebody evaluating Perfuse needs messages to point a channel at
// within a minute of unpacking it, and a channel test needs fixtures. Mirth
// charges for a message generator, which is a reasonable signal that people want
// one.

func cmdGenerate(args []string) error {
	fset := flag.NewFlagSet("generate", flag.ContinueOnError)
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage of generate:
  perfuse generate [flags]

Produces synthetic HL7 v2 messages. Everything is invented: the names are
obviously fictional, the identifiers are sequential, the addresses are made up.

Examples:
  perfuse generate -n 20                        # 20 ADT messages to stdout
  perfuse generate -kind oru -n 50 -o lab.hl7   # 50 lab results to a file
  perfuse generate -kind mixed -n 500 -dir ./fixtures
  perfuse generate -n 100 -send 127.0.0.1:6661  # straight at a channel

Flags:
`)
		fset.PrintDefaults()
	}

	kind := fset.String("kind", "adt",
		"what to produce: adt, oru, orm, mdm, or mixed for a realistic blend")
	count := fset.Int("n", 10, "how many messages")
	seed := fset.Uint64("seed", 0,
		"make the run reproducible. Set this for test fixtures; leave it for a demo")
	out := fset.String("o", "", "write to this file instead of stdout")
	dir := fset.String("dir", "", "write one file per message into this directory")
	send := fset.String("send", "",
		"send over MLLP to this address instead of writing")
	perfect := fset.Bool("perfect", false,
		"produce only cleanly formed messages. By default about one in twelve is "+
			"given a legal but awkward shape, because a feed of perfect messages "+
			"tests very little")
	patients := fset.Int("patients", 8,
		"how many distinct patients to cycle through")
	facility := fset.String("facility", "SYNTHSITE", "sending facility (MSH-4)")
	app := fset.String("app", "SYNTHEHR", "sending application (MSH-3)")
	spread := fset.Duration("spread", 0,
		"how far apart the message timestamps are (default 7s)")
	quiet := fset.Bool("quiet", false, "print no summary")

	if err := fset.Parse(args); err != nil {
		return err
	}

	opts := generate.Options{
		Count:       *count,
		Seed:        *seed,
		Perfect:     *perfect,
		Patients:    *patients,
		Facility:    *facility,
		Application: *app,
		Spread:      *spread,
	}

	var messages []generate.Message
	switch strings.ToLower(*kind) {
	case "mixed", "corpus":
		messages = generate.Corpus(opts)
	case "adt", "oru", "orm", "mdm":
		opts.Kind = generate.Kind(strings.ToUpper(*kind))
		messages = generate.New(opts).All()
	default:
		return fmt.Errorf("unknown kind %q: use adt, oru, orm, mdm or mixed", *kind)
	}

	switch {
	case *send != "":
		return sendGenerated(messages, *send, *quiet)
	case *dir != "":
		return writeGeneratedToDir(messages, *dir, *quiet)
	case *out != "":
		return writeGeneratedToFile(messages, *out, *quiet)
	default:
		return writeGeneratedTo(os.Stdout, messages)
	}
}

// writeGeneratedTo writes MLLP-framed messages to a stream.
//
// Framed even on stdout, because several HL7 messages concatenated with no
// delimiter cannot be split apart again: a segment separator and a message
// separator would be the same byte. This is the same shape the file destination
// writes, so anything that reads one reads the other.
func writeGeneratedTo(w *os.File, messages []generate.Message) error {
	buf := bufio.NewWriter(w)
	defer buf.Flush()

	for _, m := range messages {
		if _, err := buf.Write(mllp.Frame(m.Raw)); err != nil {
			return err
		}
	}
	return buf.Flush()
}

func writeGeneratedToFile(messages []generate.Message, path string, quiet bool) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	if err := writeGeneratedTo(f, messages); err != nil {
		return err
	}
	if !quiet {
		summariseGenerated(messages, fmt.Sprintf("written to %s", path))
	}
	return f.Sync()
}

func writeGeneratedToDir(messages []generate.Message, dir string, quiet bool) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}

	for i, m := range messages {
		// Numbered first so the directory sorts into the order the messages were
		// generated, which is the order a channel should receive them. Sorting by
		// control id would interleave the types.
		name := fmt.Sprintf("%04d-%s-%s.hl7", i+1,
			strings.ToLower(m.Type), strings.ToLower(m.Event))
		path := filepath.Join(dir, name)

		// Unframed here. One message per file needs no delimiter, and a fixture a
		// person is going to read should not start with a control character.
		if err := os.WriteFile(path, m.Raw, 0o600); err != nil {
			return err
		}
	}

	if !quiet {
		summariseGenerated(messages, fmt.Sprintf("written to %s/", dir))
	}
	return nil
}

func sendGenerated(messages []generate.Message, addr string, quiet bool) error {
	client := &mllp.Client{Addr: addr, Timeout: 30 * time.Second}
	defer client.Close()

	var accepted, rejected int
	for _, m := range messages {
		ack, err := client.Send(context.Background(), m.Raw)
		if err != nil {
			return fmt.Errorf("sending %s: %w", m.ControlID, err)
		}
		// The acknowledgement is inspected rather than discarded. Reporting "500
		// sent" when the receiver rejected every one of them would be the least
		// useful possible summary.
		if strings.Contains(string(ack), "|AA") || strings.Contains(string(ack), "|CA") {
			accepted++
			continue
		}
		rejected++
	}

	if !quiet {
		summariseGenerated(messages,
			fmt.Sprintf("sent to %s: %d accepted, %d not", addr, accepted, rejected))
		if rejected > 0 {
			fmt.Fprintf(os.Stderr,
				"\n%d message(s) were not accepted. Some of that is expected: about one "+
					"in twelve is deliberately awkward, and a channel filter is supposed to "+
					"reject what it does not want. Use -perfect to send only clean messages.\n",
				rejected)
		}
	}
	return nil
}

func summariseGenerated(messages []generate.Message, where string) {
	kinds := map[string]int{}
	events := map[string]int{}
	patients := map[string]bool{}
	awkward := 0

	for _, m := range messages {
		kinds[m.Type]++
		events[m.Type+"^"+m.Event]++
		patients[m.Patient] = true
		if m.Awkward != "" {
			awkward++
		}
	}

	fmt.Fprintf(os.Stderr, "%d message(s) %s\n", len(messages), where)

	var parts []string
	for _, kind := range []string{"ADT", "ORU", "ORM", "MDM"} {
		if kinds[kind] > 0 {
			parts = append(parts, fmt.Sprintf("%d %s", kinds[kind], kind))
		}
	}
	fmt.Fprintf(os.Stderr, "  %s, %d patient(s)\n",
		strings.Join(parts, ", "), len(patients))

	if awkward > 0 {
		fmt.Fprintf(os.Stderr,
			"  %d have a legal but awkward shape: a missing optional field, an extra "+
				"repetition, an escaped separator, a Z segment\n", awkward)
	}
	fmt.Fprintln(os.Stderr,
		"  everything is synthetic: fictional names, sequential identifiers, invented addresses")
}
