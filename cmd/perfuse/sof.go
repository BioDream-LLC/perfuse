package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/biodream-llc/perfuse/internal/sof"
)

// cmdSOF runs a SQL on FHIR ViewDefinition over FHIR files: NDJSON from a bulk export ($export), Bundles, or JSON arrays.
func cmdSOF(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("sof", flag.ContinueOnError)
	fset.SetOutput(stderr)
	viewFile := fset.String("view", "", "the ViewDefinition (JSON) to run (required)")
	format := fset.String("format", "csv", "output format: csv, ndjson, json or parquet")
	header := fset.Bool("header", true, "write a header row (csv)")
	out := fset.String("o", "", "write to this file instead of stdout")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *viewFile == "" {
		return errors.New("sof needs -view FILE, a SQL on FHIR ViewDefinition")
	}
	if fset.NArg() == 0 {
		return errors.New("sof needs at least one FHIR file (NDJSON, a Bundle or a JSON array), or - for stdin")
	}
	raw, err := os.ReadFile(*viewFile)
	if err != nil {
		return err
	}
	view, err := sof.Compile(raw)
	if err != nil {
		return err
	}
	if *format == "parquet" && *out == "" {
		return errors.New("parquet is binary: give -o FILE")
	}
	w := stdout
	if *out != "" {
		f, err := os.OpenFile(*out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		w = f
	}
	wr, err := sof.NewWriter(w, *format, view.Columns, *header)
	if err != nil {
		return err
	}
	rows, resources := 0, 0
	for _, path := range fset.Args() {
		// Closed after each file rather than deferred, which held every input open until the command returned.
		err := readResourceFile(path, func(res map[string]any) error {
			resources++
			out, err := view.Run(res)
			if err != nil {
				return fmt.Errorf("%s: %w", path, err)
			}
			for _, row := range out {
				if err := wr.Write(row); err != nil {
					return err
				}
				rows++
			}
			return nil
		})
		if err != nil {
			return err
		}
	}
	if err := wr.Close(); err != nil {
		return err
	}
	fmt.Fprintf(stderr, "%d rows from %d resources\n", rows, resources)
	return nil
}

// readResourceFile reads the FHIR resources in one input, "-" being standard input, and closes the file before it returns.
func readResourceFile(path string, each func(map[string]any) error) error {
	if path == "-" {
		return sof.ReadResources(os.Stdin, each)
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return sof.ReadResources(f, each)
}
