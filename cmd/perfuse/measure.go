package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/biodream-llc/perfuse/internal/measure"
	"github.com/biodream-llc/perfuse/internal/sof"
)

// cmdMeasure evaluates a FHIRPath Measure over FHIR files (NDJSON from a bulk export, Bundles or JSON arrays) and writes the
// MeasureReport.
func cmdMeasure(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("measure", flag.ContinueOnError)
	fset.SetOutput(stderr)
	measureFile := fset.String("measure", "", "the Measure (JSON) to evaluate, or example:ID for a shipped example (required)")
	start := fset.String("period-start", "", "the measurement period's first day, 2026-01-01 (required)")
	end := fset.String("period-end", "", "the measurement period's last day, 2026-12-31 (required)")
	reportType := fset.String("type", "summary", "the report: summary, subject or subject-list")
	subject := fset.String("subject", "", "evaluate one patient only (Patient/ID); implies -type subject")
	reporter := fset.String("reporter", "Perfuse", "the reporting organisation's name")
	out := fset.String("o", "", "write to this file instead of stdout")
	list := fset.Bool("examples", false, "list the shipped example measures and stop")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if *list {
		for _, e := range measure.Examples() {
			fmt.Fprintf(stdout, "example:%-24s %s\n", e.ID, e.Title)
		}
		return nil
	}
	if *measureFile == "" {
		return errors.New("measure needs -measure FILE (or -measure example:ID; -examples lists them)")
	}
	if fset.NArg() == 0 {
		return errors.New("measure needs at least one FHIR file (NDJSON, a Bundle or a JSON array), or - for stdin")
	}
	var raw []byte
	if id, ok := cutPrefix(*measureFile, "example:"); ok {
		for _, e := range measure.Examples() {
			if e.ID == id {
				raw = e.Measure
			}
		}
		if raw == nil {
			return fmt.Errorf("no example measure %q; -examples lists them", id)
		}
	} else {
		var err error
		if raw, err = os.ReadFile(*measureFile); err != nil {
			return err
		}
	}
	var m map[string]any
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	if err := d.Decode(&m); err != nil {
		return fmt.Errorf("%s: %w", *measureFile, err)
	}
	compiled, err := measure.Compile(m)
	if err != nil {
		return err
	}
	period, err := measure.ParsePeriod(*start, *end)
	if err != nil {
		return err
	}
	var all []map[string]any
	for _, path := range fset.Args() {
		var r io.Reader = os.Stdin
		if path != "-" {
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			r = f
		}
		if err := sof.ReadResources(r, func(res map[string]any) error { all = append(all, res); return nil }); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	}
	rt := measure.ReportType(*reportType)
	want, _ := cutPrefix(*subject, "Patient/")
	if want != "" {
		rt = measure.Individual
	}
	if rt != measure.Summary && rt != measure.Individual && rt != measure.SubjectList {
		return fmt.Errorf("-type is summary, subject or subject-list, not %q", *reportType)
	}
	if rt == measure.Individual && want == "" {
		return errors.New("-type subject needs -subject Patient/ID")
	}
	ev := measure.NewEvaluator(compiled, period)
	for _, s := range measure.SubjectsFrom(all) {
		if id, _ := s.Patient["id"].(string); want != "" && id != want {
			continue
		}
		if err := ev.Add(s); err != nil {
			return err
		}
	}
	if ev.Patients() == 0 {
		return errors.New("no patients to evaluate: the files hold no Patient (or not the one named)")
	}
	rep := ev.Report(measure.Options{Type: rt, Subject: "Patient/" + want, ReporterName: *reporter})
	b, _ := json.MarshalIndent(rep, "", "  ")
	b = append(b, '\n')
	if *out != "" {
		return os.WriteFile(*out, b, 0o600)
	}
	_, err = stdout.Write(b)
	return err
}

func cutPrefix(s, p string) (string, bool) {
	if len(s) >= len(p) && s[:len(p)] == p {
		return s[len(p):], true
	}
	return s, false
}
