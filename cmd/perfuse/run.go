package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/egress"
	"github.com/biodream-llc/perfuse/internal/engine"
	"github.com/biodream-llc/perfuse/internal/spec"
)

func cmdCheck(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("check", flag.ContinueOnError)
	fset.SetOutput(stderr)
	allowMetadataEgress := fset.Bool("allow-metadata-egress", false,
		"permit destinations pointed at cloud instance metadata addresses, which hold this machine's credentials")
	quiet := fset.Bool("quiet", false, "print nothing on success")
	if err := fset.Parse(args); err != nil {
		return err
	}

	// Applied before any channel is read, because validation consults it.
	//
	// Set here rather than in a channel file on purpose: a channel author is the person this control restrains, so an
	// exemption they could grant themselves would only stop somebody who was not trying.
	if *allowMetadataEgress {
		egress.SetDefault(egress.Policy{AllowMetadata: true})
	}
	if fset.NArg() == 0 {
		return errors.New("check needs a channel file or directory")
	}

	channels, err := loadChannels(fset.Args())
	if err != nil {
		return err
	}

	// Two channels wanting the same port is not something either of them can see on
	// its own - each is individually valid. Only the second one to start fails, with
	// "address already in use", and it then sits there looking configured while the
	// hospital pointed at it gets connection refused. Refused here instead.
	bindings := config.NewBindings()
	for _, c := range channels {
		if c.IsEnabled() {
			bindings.Add("", c)
		}
	}
	if err := bindings.Check(); err != nil {
		return err
	}

	if *quiet {
		return nil
	}

	for _, c := range channels {
		state := "enabled"
		if !c.IsEnabled() {
			state = "disabled"
		}

		fmt.Fprintf(stdout, "%-24s %-8s  %s\n", c.Name, state, c.Source.Listen)

		// Printed whenever it is not the default, because the data type changes what
		// every other line means. A reader who does not know this is an X12 channel will
		// read "reads" and "acknowledges" as HL7 and be wrong about both.
		if c.Type() != config.DataHL7 {
			fmt.Fprintf(stdout, "  data type     %s\n", c.Type())
		}
		if c.Type() == config.DataX12 && c.X12 != nil {
			fmt.Fprintf(stdout, "  envelope      %s\n", c.X12.Policy())
			if c.X12.ShouldSplit() {
				fmt.Fprintf(stdout, "  splits        one message per transaction set\n")
			}
		}
		if c.Filter != "" {
			fmt.Fprintf(stdout, "  filter        %s\n", c.Filter)
		}

		// The v3 filter and steps are printed here, because both live under hl7v3 and neither appears in the
		// lines above. Without this, perfuse check reported a channel that masks a birth date and upper-cases a
		// surname as though it forwarded messages untouched - which is exactly the output somebody would paste
		// into a change request as evidence that nothing happens to the data.
		if c.Type() == config.DataHL7v3 && c.HL7v3 != nil {
			if expr := c.HL7v3.FilterExpression(); expr != "" {
				fmt.Fprintf(stdout, "  filter        %s\n", expr)
			}
			for i, st := range c.HL7v3.Transformations {
				label := "changes"
				if i > 0 {
					label = ""
				}
				fmt.Fprintf(stdout, "  %-13s %d. %s\n", label, i+1, spec.DescribeV3Step(st))
			}
		}
		for _, d := range c.EnabledDestinations() {
			r := d.Resolved()
			// The shared description rather than a local guess. This used to read Address, falling back to Dir
			// for a file destination, which meant every other type - database, sftp, s3, ftp, soap, document,
			// smtp, fhir, cda, channel - printed a blank where its target should be.
			target := spec.DescribeTransport(r)
			fmt.Fprintf(stdout, "  → %-12s %-6s %s (timeout %s, %d attempt(s))\n",
				r.Name, r.Type, target, r.Timeout, r.Retry.Attempts)
			if r.Filter != "" {
				fmt.Fprintf(stdout, "      filter    %s\n", r.Filter)
			}
		}
		if paths := c.Paths(); len(paths) > 0 {
			// A channel stating its own data dependencies is worth printing: it
			// is the answer to "what does this actually read".
			fmt.Fprintf(stdout, "  reads         %s\n", strings.Join(paths, " "))
		}
		if c.Type() == config.DataX12 {
			// Saying so out loud, because somebody reading this having configured HL7
			// channels for years will otherwise assume an acknowledgement is going back.
			if level := c.X12.Acknowledgement(); level != "" {
				fmt.Fprintf(stdout, "  acknowledges  with a %s in the response, as %s\n",
					level, c.X12.AckSenderID)
			} else {
				fmt.Fprintf(stdout, "  acknowledges  nothing synchronously; X12 uses a 997 or 999 sent separately\n")
			}
		} else {
			fmt.Fprintf(stdout, "  acknowledges  %s\n", c.Source.AckWhen())
		}

		// Attachment extraction is stated for the same reason a contract is: it changes what this channel stores.
		//
		// A field moved out of the message is not in the message any more, so anybody reading a stored message or
		// writing a filter against that path needs to know - and the alternative is discovering it from a filter
		// that matches nothing.
		if a := c.Attachments; a != nil && len(a.Extract) > 0 {
			paths := make([]string, 0, len(a.Extract))
			for _, rule := range a.Extract {
				paths = append(paths, rule.Path)
			}
			sort.Strings(paths)
			fmt.Fprintf(stdout, "  attachments   %s stored out of line and replaced with a token\n",
				strings.Join(paths, ", "))
		}

		// A contract is stated here because this command answers "what does this channel do", and a standing
		// promise about the traffic is part of that. It was not mentioned at all before - for v2 either - so a
		// channel carrying twelve expectations about its sender looked identical to one carrying none.
		if c.Contract != nil {
			if ct := c.Contract.Contract(); ct != nil {
				fmt.Fprintf(stdout, "  contract      %d expectation(s) from %s, re-checked every %s\n",
					len(ct.Expectations), c.Contract.File, c.Contract.Interval())
			}
		}
	}

	fmt.Fprintf(stdout, "\n%d channel(s) valid\n", len(channels))
	return nil
}

func cmdRun(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("run", flag.ContinueOnError)
	allowMetadataEgress := fset.Bool("allow-metadata-egress", false,
		"permit destinations pointed at cloud instance metadata addresses, which hold this machine's credentials")
	fset.SetOutput(stderr)
	quiet := fset.Bool("quiet", false, "log only errors")
	jsonLogs := fset.Bool("json-logs", false, "emit structured JSON logs")
	drain := fset.Duration("drain-timeout", 15*time.Second,
		"how long to wait for messages in flight when shutting down")
	if err := fset.Parse(args); err != nil {
		return err
	}

	if *allowMetadataEgress {
		egress.SetDefault(egress.Policy{AllowMetadata: true})
	}
	if fset.NArg() == 0 {
		return errors.New("run needs a channel file or directory")
	}

	level := slog.LevelInfo
	if *quiet {
		level = slog.LevelError
	}
	var handler slog.Handler
	if *jsonLogs {
		handler = slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: level})
	} else {
		handler = slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level})
	}
	log := slog.New(handler)

	channels, err := loadChannels(fset.Args())
	if err != nil {
		return err
	}

	// MLLP has no authentication and no encryption. Say so at startup rather
	// than in documentation nobody reads.
	var listens []string
	for _, c := range channels {
		if c.IsEnabled() {
			listens = append(listens, c.Source.Listen)
		}
	}
	if len(listens) > 0 {
		log.Warn("MLLP is unauthenticated and unencrypted; restrict access at the network layer",
			"listening_on", strings.Join(listens, " "))
	}

	eng, err := engine.New(channels, nil, log)
	if err != nil {
		return err
	}
	if err := eng.Start(); err != nil {
		return err
	}

	ctx, stop := shutdownContext()
	defer stop()
	<-ctx.Done()

	log.Info("shutting down, waiting for messages in flight", "timeout", *drain)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), *drain)
	defer cancel()
	if err := eng.Stop(shutdownCtx); err != nil {
		log.Warn("shutdown did not complete cleanly", "err", err)
	}

	writeStats(stdout, eng.Stats())
	return nil
}

// loadChannels accepts a mix of files and directories.
func loadChannels(paths []string) ([]*config.Channel, error) {
	var out []*config.Channel
	byName := map[string]string{}

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}

		var found []*config.Channel
		if info.IsDir() {
			found, err = config.LoadDir(path)
		} else {
			var c *config.Channel
			c, err = config.LoadFile(path)
			if c != nil {
				found = []*config.Channel{c}
			}
		}
		if err != nil {
			return nil, err
		}

		for _, c := range found {
			if prev, dup := byName[c.Name]; dup {
				return nil, fmt.Errorf("channel name %q appears in both %s and %s",
					c.Name, prev, c.Path())
			}
			byName[c.Name] = c.Path()
			out = append(out, c)
		}
	}

	if len(out) == 0 {
		return nil, errors.New("no channels found")
	}
	return out, nil
}

func writeStats(w io.Writer, stats map[string]engine.Stats) {
	names := make([]string, 0, len(stats))
	for name := range stats {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		s := stats[name]
		fmt.Fprintf(w, "\n%s\n", name)
		fmt.Fprintf(w, "  received %d  delivered %d  filtered %d  partial %d  failed %d  unparseable %d\n",
			s.Received, s.Delivered, s.Filtered, s.Partial, s.Failed, s.Unparseable)

		dests := make([]string, 0, len(s.DestDelivered))
		seen := map[string]bool{}
		for _, m := range []map[string]int64{s.DestDelivered, s.DestFailed, s.DestFiltered} {
			for d := range m {
				if !seen[d] {
					seen[d] = true
					dests = append(dests, d)
				}
			}
		}
		sort.Strings(dests)
		for _, d := range dests {
			fmt.Fprintf(w, "    %-16s delivered %d  failed %d  filtered %d\n",
				d, s.DestDelivered[d], s.DestFailed[d], s.DestFiltered[d])
		}
	}
}
