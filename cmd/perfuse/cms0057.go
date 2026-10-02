package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/cms0057"
)

const cms0057Usage = `perfuse cms0057 - the payer data the CMS Interoperability and Prior Authorization rule requires

Usage:
  perfuse cms0057 carinbb   -system <uri> [flags] <claims.837> <remittance.835>
      Convert adjudicated claims into CARIN Blue Button ExplanationOfBenefit bundles (CARIN BB 2.2.0), for the
      Patient Access API. Each claim in the 837 is paired with its payment in the 835 by patient account number.
  perfuse cms0057 priorauth [flags] <pas-response.json> [pas-claim.json]
      Convert a Da Vinci PAS ClaimResponse (and the Claim it answers) into a PDex prior authorization
      ExplanationOfBenefit (PDex 2.2.0), for the Patient Access, Provider Access and Payer-to-Payer APIs.
  perfuse cms0057 metrics   -year <year> [flags] <decisions.csv>
      Compute the prior authorization metrics payers must post publicly each year, in the layout of CMS's template.

Run a subcommand with -h for its flags. Drugs are out of scope, as they are in the rule's prior authorization provisions.
`

func cmdCMS0057(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, cms0057Usage)
		return errors.New("cms0057 needs a subcommand")
	}
	switch args[0] {
	case "carinbb":
		return cmdCARIN(args[1:], stdout, stderr)
	case "priorauth":
		return cmdPriorAuth(args[1:], stdout, stderr)
	case "metrics":
		return cmdMetrics(args[1:], stdout, stderr)
	case "help", "-h", "--help":
		fmt.Fprint(stdout, cms0057Usage)
		return nil
	}

	return fmt.Errorf("unknown cms0057 subcommand %q\n\n%s", args[0], cms0057Usage)
}

func cmdCARIN(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cms0057 carinbb", flag.ContinueOnError)
	fs.SetOutput(stderr)
	system := fs.String("system", "", "the payer's identifier system for member ids and claim numbers, a URI the payer owns (required)")
	network := fs.String("network", "", "the billing provider's network status: innetwork or outofnetwork (neither X12 file says)")
	base := fs.String("base", "", "the FHIR server the bundle is for, so entries get fullUrls and references resolve inside it")
	out := fs.String("o", "", "write each claim's bundle to this directory as <claim number>.json instead of to standard output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 2 {
		return errors.New("carinbb needs two files: the 837 and the 835")
	}
	if *network != "" && *network != "innetwork" && *network != "outofnetwork" {
		return fmt.Errorf("-network must be innetwork or outofnetwork, not %q", *network)
	}
	c, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	r, err := os.ReadFile(fs.Arg(1))
	if err != nil {
		return err
	}
	results, skipped, err := cms0057.ConvertClaims(c, r, cms0057.CARINOptions{IdentifierSystem: *system, NetworkStatus: *network, BaseURL: *base})
	for _, s := range skipped {
		fmt.Fprintln(stderr, "skipped:", s)
	}
	if err != nil {
		return err
	}
	for _, res := range results {
		for _, n := range res.Notes {
			fmt.Fprintf(stderr, "%s: %s\n", res.ClaimNumber, n)
		}
		raw, err := json.MarshalIndent(res.Bundle, "", "  ")
		if err != nil {
			return err
		}
		if *out == "" {
			fmt.Fprintln(stdout, string(raw))
			continue
		}
		name := strings.Map(func(r rune) rune {
			if r == '/' || r == '\\' || r == ':' {
				return '-'
			}
			return r
		}, res.ClaimNumber)
		path := strings.TrimRight(*out, "/") + "/" + name + ".json"
		if err := os.WriteFile(path, raw, 0o600); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "%s  %s\n", path, res.Profile)
	}

	return nil
}

func cmdPriorAuth(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cms0057 priorauth", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() < 1 || fs.NArg() > 2 {
		return errors.New("priorauth needs the PAS response, and optionally the PAS claim")
	}
	resp, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		return err
	}
	var claim []byte
	if fs.NArg() == 2 {
		if claim, err = os.ReadFile(fs.Arg(1)); err != nil {
			return err
		}
	}
	res, err := cms0057.PriorAuthFromJSON(resp, claim, time.Now())
	if err != nil {
		return err
	}
	for _, n := range res.Notes {
		fmt.Fprintln(stderr, "note:", n)
	}
	fmt.Fprintln(stderr, "decision:", res.Decision)
	raw, err := json.MarshalIndent(res.ExplanationOfBenefit, "", "  ")
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, string(raw))

	return nil
}

func cmdMetrics(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("cms0057 metrics", flag.ContinueOnError)
	fs.SetOutput(stderr)
	year := fs.Int("year", time.Now().Year()-1, "the calendar year to report on")
	org := fs.String("org", "", "the payer's name, as the public page should show it")
	contact := fs.String("contact", "", "who to contact about the data, as the public page should show it")
	services := fs.String("services", "", "CSV of the items and services requiring prior authorization: category, code, description")
	format := fs.String("format", "html", "html (the public page), csv (one row per metric) or json")
	standardDays := fs.Int("standard-days", 7, "the standard decision timeframe in days; QHP issuers on the federal exchanges use 15")
	expeditedHours := fs.Int("expedited-hours", 72, "the expedited decision timeframe in hours")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("metrics needs one CSV of prior authorization decisions")
	}
	f, err := os.Open(fs.Arg(0))
	if err != nil {
		return err
	}
	defer f.Close()
	decisions, err := cms0057.ParseDecisionsCSV(f)
	if err != nil {
		return err
	}
	opt := cms0057.MetricsOptions{
		Year: *year, Organization: *org, Contact: *contact,
		StandardDeadline:  time.Duration(*standardDays) * 24 * time.Hour,
		ExpeditedDeadline: time.Duration(*expeditedHours) * time.Hour,
	}
	if *services != "" {
		sf, err := os.Open(*services)
		if err != nil {
			return err
		}
		defer sf.Close()
		if opt.Services, err = cms0057.ParseServicesCSV(sf); err != nil {
			return fmt.Errorf("the services list: %w", err)
		}
	}
	report, err := cms0057.BuildMetrics(decisions, opt)
	if err != nil {
		return err
	}
	for reason, n := range report.Excluded {
		fmt.Fprintf(stderr, "excluded %d: %s\n", n, reason)
	}
	for _, q := range report.DataQuality {
		fmt.Fprintln(stderr, "data quality:", q)
	}
	switch *format {
	case "html":
		page, err := cms0057.MetricsHTML(report)
		if err != nil {
			return err
		}
		_, err = stdout.Write(page)
		return err
	case "csv":
		_, err := stdout.Write(cms0057.MetricsCSV(report))
		return err
	case "json":
		raw, err := json.MarshalIndent(report, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(stdout, string(raw))
		return nil
	}

	return fmt.Errorf("-format must be html, csv or json, not %q", *format)
}
