package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/biodream-llc/perfuse/internal/contract"
	"github.com/biodream-llc/perfuse/internal/profile"
	"gopkg.in/yaml.v3"
)

// Contract testing for an interface.
//
// The workflow this supports, in order, because the order is the whole design:
//
//  1. perfuse contract promote corpus/ -o adt.contract.yaml
//     Turn a working feed into an expectation. Nobody writes one of these from scratch.
//  2. Read it and delete most of it.
//     The generated file is a starting point. Half of what it asserts is not something anybody cares about,
//     and a contract nobody has pruned is a contract nobody has read.
//  3. perfuse contract check adt.contract.yaml today/
//     Run it in a scheduled job with -strict, and find out the morning a vendor changes something.
//
// Step 2 is the one that cannot be automated, which is why the generated file carries a reason on every line -
// somebody pruning it needs to know whether each expectation was measured or decided.

func cmdContract(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		writeContractUsage(stderr)
		return fmt.Errorf("contract needs a subcommand: promote or check")
	}

	switch args[0] {
	case "promote":
		return contractPromote(args[1:], stdout, stderr)
	case "check":
		return contractCheck(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		writeContractUsage(stdout)
		return nil
	default:
		writeContractUsage(stderr)
		return fmt.Errorf("unknown contract subcommand %q", args[0])
	}
}

func writeContractUsage(w io.Writer) {
	fmt.Fprint(w, `Usage:
  perfuse contract promote [flags] <path>...   turn real traffic into expectations
  perfuse contract check   [flags] <contract> <path>...   check traffic against them

A contract says what a feed must look like, so that the day it changes you are
told rather than finding out weeks later from a receiver that fell over. The
messages are still valid HL7 when a field disappears, so nothing else notices.

  perfuse contract promote -o adt.contract.yaml corpus/
  perfuse contract check adt.contract.yaml today/
  perfuse contract check -strict adt.contract.yaml today/    # for a cron job

Promote generates a starting point, not an answer. Read it and delete most of
it: every line says whether it was measured or decided, which is what you need
to prune it sensibly.
`)
}

func contractPromote(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("contract promote", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		out    = fs.String("o", "", "write the contract here instead of to standard output")
		framed = fs.Bool("framed", false, "the input is MLLP-framed")
		asV3   = fs.Bool("v3", false, "the input is HL7 v3 XML rather than v2")
		force  = fs.Bool("force", false, "overwrite an existing contract file")
	)

	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage of contract promote:
  perfuse contract promote [flags] <path>...

Profiles the traffic and writes the expectations it supports. Only fields
present in essentially every message become requirements, and value sets are
only asserted where the values genuinely look like a code set — asserting the
value set of a name field would write patient data into a config file.

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("promote needs at least one file or directory")
	}

	messages, err := readCorpus(fs.Args(), *framed)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		return fmt.Errorf("no messages were found in %s", strings.Join(fs.Args(), ", "))
	}

	report := profile.BuildFor(v3DataType(*asV3), messages)
	c := contract.Promote(report, contract.PromoteOptions{Source: strings.Join(fs.Args(), ", ")})

	if errs := c.Validate(); len(errs) > 0 {
		// Would be a bug in promotion rather than in the input, so it says so - otherwise somebody spends an
		// hour looking at their messages.
		var lines []string
		for _, e := range errs {
			lines = append(lines, e.Error())
		}
		return fmt.Errorf("the generated contract is not valid, which is a bug in Perfuse rather than a "+
			"problem with your messages:\n  %s", strings.Join(lines, "\n  "))
	}

	body, err := yaml.Marshal(c)
	if err != nil {
		return err
	}

	header := fmt.Sprintf(`# Expectations for this feed, generated from real traffic.
#
# This is a starting point, not an answer. Read it and delete most of it: an
# expectation nobody has pruned is one nobody has read, and a contract that
# fires on things nobody cares about gets switched off.
#
# Each line records whether it was measured or decided. That matters when you
# are deciding whether to relax one.
#
# These describe what ARRIVES on the channel, not what leaves it. Perfuse stores
# messages as they arrived, so a contract sees the incoming feed and not the
# output of your transformations. An expectation about a mapped value will fail
# confusingly, because the mapped codes never appear in the traffic being
# profiled - put that in a channel test instead, which runs the real
# transformations.
#
# Check it with:
#   perfuse contract check <this file> <a directory of recent messages>
#
# Generated from %d message(s) in %s.

`, report.Messages, strings.Join(fs.Args(), ", "))

	full := header + string(body)

	if *out == "" {
		fmt.Fprint(stdout, full)
		return nil
	}

	if !*force {
		if _, err := os.Stat(*out); err == nil {
			// An existing contract has been pruned by a person, and that pruning is the valuable part. It must
			// not be lost to a re-run.
			return fmt.Errorf("%s already exists; it has probably been edited, and regenerating would "+
				"discard that. Pass -force if you mean to replace it", *out)
		}
	}

	if err := os.WriteFile(*out, []byte(full), 0o644); err != nil {
		return err
	}

	fmt.Fprintf(stdout, "wrote %s: %d expectation(s) from %d message(s)\n",
		*out, len(c.Expectations), report.Messages)
	fmt.Fprintf(stdout, "\nRead it and delete what you do not care about. Then:\n")
	fmt.Fprintf(stdout, "  perfuse contract check %s <recent messages>\n", *out)
	return nil
}

func contractCheck(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("contract check", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		strict = fs.Bool("strict", false, "exit non-zero if the contract does not hold")
		asJSON = fs.Bool("json", false, "emit the result as JSON")
		framed = fs.Bool("framed", false, "the input is MLLP-framed")
		asV3   = fs.Bool("v3", false, "the input is HL7 v3 XML rather than v2")
	)

	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage of contract check:
  perfuse contract check [flags] <contract> <path>...

Checks recent traffic against a contract. Use -strict in a scheduled job so it
tells you without being read.

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 2 {
		fs.Usage()
		return fmt.Errorf("check needs a contract file and at least one directory of messages")
	}

	c, err := loadContract(fs.Arg(0))
	if err != nil {
		return err
	}

	messages, err := readCorpus(fs.Args()[1:], *framed)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		// Distinguished from "the contract held". A feed that has stopped is the most serious thing this
		// command can find, and reporting it as a pass is how monitoring systems come to be trusted wrongly.
		return fmt.Errorf("no messages were found in %s; a feed that has stopped is not a feed that passes",
			strings.Join(fs.Args()[1:], ", "))
	}

	report := profile.BuildFor(v3DataType(*asV3), messages)
	result := contract.Check(c, report)

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return err
		}
	} else {
		writeContractResult(stdout, result)
	}

	if *strict && !result.Holds() {
		return fmt.Errorf("%s", result.Summary())
	}
	return nil
}

func loadContract(path string) (*contract.Contract, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var c contract.Contract
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	// Unknown keys are refused, the same as channel files. A misspelled rule that is silently ignored produces
	// a contract that looks like it is checking something and is not, which is worse than no contract because
	// it is trusted.
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	if errs := c.Validate(); len(errs) > 0 {
		var lines []string
		for _, e := range errs {
			lines = append(lines, e.Error())
		}
		return nil, fmt.Errorf("%s is not a valid contract:\n  %s", path, strings.Join(lines, "\n  "))
	}

	return &c, nil
}

func writeContractResult(w io.Writer, r *contract.Result) {
	fmt.Fprintln(w, r.Summary())

	if !r.Judged {
		return
	}

	if len(r.Violations) == 0 {
		return
	}

	fmt.Fprintln(w)
	for _, v := range r.Violations {
		fmt.Fprintf(w, "  %s\n", v.Says)

		// The reason is printed with the violation, because the first question anybody asks about a failing
		// expectation is whether it was ever right - and the answer is usually in the reason.
		if v.Expectation.Why != "" {
			fmt.Fprintf(w, "      this expectation: %s\n", v.Expectation.Why)
		}
		if v.Expectation.Relaxed != "" {
			fmt.Fprintf(w, "      previously relaxed: %s\n", v.Expectation.Relaxed)
		}
	}
}
