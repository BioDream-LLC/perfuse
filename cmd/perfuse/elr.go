package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/publichealth"
)

// cmdELR reshapes lab results into ELR 2.5.1 messages for public health, one per message with a reportable result.
func cmdELR(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("elr", flag.ContinueOnError)
	fset.SetOutput(stderr)
	config := fset.String("config", "", "YAML file naming the sender, the receiver, and the ordering facility and lab (required)")
	rctc := fset.String("rctc", "", "trigger codes: a FHIR ValueSet or Bundle of them, such as the eRSD (default: the built-in sample)")
	outDir := fset.String("out", "", "write messages to this directory instead of stdout")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *config == "" {
		return errors.New("elr needs -config: ELR names its sender and receiver by OID, which no lab message carries")
	}
	if fset.NArg() == 0 {
		return errors.New("elr needs at least one file, or - for stdin")
	}
	cfg, err := publichealth.LoadELRConfig(*config)
	if err != nil {
		return err
	}
	triggers := publichealth.BuiltinTriggers()
	if *rctc != "" {
		if triggers, err = publichealth.LoadTriggers(*rctc); err != nil {
			return err
		}
	}
	if *outDir != "" {
		if err := os.MkdirAll(*outDir, 0o750); err != nil {
			return err
		}
		fmt.Fprintf(stderr, "warning: lab reports contain PHI and are being written to %s\n", *outDir)
	}
	opts := cfg.Options(time.Now(), publichealth.Software{Vendor: "BioDream LLC", Version: buildVersion(), Name: "Perfuse",
		BinaryID: "perfuse-" + buildVersion()})
	var reported, skipped, failed int
	for _, path := range fset.Args() {
		raw, err := readInput(path)
		if err != nil {
			return err
		}
		for i, chunk := range splitMessages(raw) {
			m, err := hl7.Parse(chunk)
			if err != nil {
				failed++
				fmt.Fprintf(stderr, "%s message %d: %v\n", path, i+1, err)
				continue
			}
			r, err := publichealth.BuildELR(m, triggers, opts)
			if errors.Is(err, publichealth.ErrNothingReportable) {
				skipped++
				fmt.Fprintf(stderr, "%s message %d: %v\n", path, i+1, err)
				continue
			}
			if err != nil {
				failed++
				fmt.Fprintf(stderr, "%s message %d: %v\n", path, i+1, err)
				continue
			}
			reported++
			for _, t := range r.Triggers {
				fmt.Fprintf(stderr, "%s message %d: trigger %s %s (%s) in %s\n", path, i+1, t.System, t.Code, t.Condition, t.Resource)
			}
			if r.Dropped > 0 {
				fmt.Fprintf(stderr, "  %d order(s) with nothing reportable left out\n", r.Dropped)
			}
			for _, n := range r.Notes {
				fmt.Fprintf(stderr, "  note: %s\n", n)
			}
			if *outDir != "" {
				name := fmt.Sprintf("elr-%s.hl7", sanitise(m.ControlID()))
				if err := os.WriteFile(filepath.Join(*outDir, name), r.Message, 0o600); err != nil {
					return err
				}
			} else {
				fmt.Fprintf(stdout, "%s\n", r.Message)
			}
		}
	}
	fmt.Fprintf(stderr, "%d reported, %d with nothing reportable, %d failed\n", reported, skipped, failed)
	if failed > 0 {
		return errBlocking
	}
	return nil
}
