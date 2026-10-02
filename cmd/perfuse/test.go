package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/channeltest"
)

// perfuse test runs tests against channel definitions.
//
// A channel is a program that rewrites clinical messages, and it gets edited under
// pressure by people who cannot try it against production. That is most of why
// interface work is frightening, and "I changed the transformer and nothing looked
// different in the message browser" is not a test.

func cmdTest(args []string) error {
	fset := flag.NewFlagSet("test", flag.ContinueOnError)
	fset.Usage = func() {
		fmt.Fprint(os.Stderr, `Usage of test:
  perfuse test [flags] <path>...

Runs channel tests. A path may be a test file or a directory, which is searched
for *_test.yaml and *.test.yaml.

A test sends a message through the channel's real filter, transformations and
script, in the real order, with only the network replaced. What passes here is
what the channel does.

Example test file:

  channel: ./adt.yaml
  tests:
    - name: an admission has its MRN padded to ten characters
      message: |
        MSH|^~\&|SEND|SITEA|RECV|RFAC|20260819080000-0500||ADT^A01^ADT_A01|T1|P|2.5.1
        PID|1||MRN7^^^SITEA^MR||Frost^Ivy||19910228|F
      expect:
        outcome: delivered
        ack: AA
        fields:
          PID-3.1: "000000MRN7"

    - name: an A28 is filtered out
      message: |
        MSH|^~\&|SEND|SITEA|RECV|RFAC|20260819080000-0500||ADT^A28^ADT_A05|T2|P|2.5.1
        PID|1||MRN8^^^SITEA^MR||Frost^Ivy||19910228|F
      expect:
        outcome: filtered
        ack: AA
        destinations:
          archive: {received: false}

Flags:
`)
		fset.PrintDefaults()
	}

	verbose := fset.Bool("v", false, "print every test, not only failures")
	showMessage := fset.Bool("show", false,
		"print the transformed message for each failure")
	jsonOut := fset.Bool("json", false, "emit machine-readable results")

	if err := fset.Parse(args); err != nil {
		return err
	}
	paths := fset.Args()
	if len(paths) == 0 {
		fset.Usage()
		return fmt.Errorf("name a test file or a directory")
	}

	files, err := findTestFiles(paths)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no test files found; they are named *_test.yaml or *.test.yaml")
	}

	out := os.Stdout
	var reports []*channeltest.Report
	var loadErrors []string

	for _, file := range files {
		suite, err := channeltest.Load(file)
		if err != nil {
			// A malformed test file is a failure of the run, not a skipped file. A
			// suite that silently does not run is indistinguishable from one that
			// passes.
			loadErrors = append(loadErrors, err.Error())
			continue
		}

		report, err := channeltest.Run(suite)
		if err != nil {
			loadErrors = append(loadErrors, fmt.Sprintf("%s: %v", file, err))
			continue
		}
		reports = append(reports, report)

		if !*jsonOut {
			printReport(out, file, report, *verbose, *showMessage)
		}
	}

	if *jsonOut {
		return writeTestJSON(out, reports, loadErrors)
	}

	return summariseTests(out, reports, loadErrors)
}

func findTestFiles(paths []string) ([]string, error) {
	var files []string
	seen := map[string]bool{}

	add := func(path string) {
		abs, err := filepath.Abs(path)
		if err != nil {
			abs = path
		}
		if !seen[abs] {
			seen[abs] = true
			files = append(files, path)
		}
	}

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			add(path)
			continue
		}

		err = filepath.WalkDir(path, func(p string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			name := d.Name()
			if strings.HasSuffix(name, "_test.yaml") || strings.HasSuffix(name, ".test.yaml") {
				add(p)
			}
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	sort.Strings(files)
	return files, nil
}

func printReport(w io.Writer, file string, report *channeltest.Report, verbose, showMessage bool) {
	passed, failed, skipped := report.Counts()

	// The channel name as well as the file, because the interesting identity is
	// which channel was tested and a file can be named anything.
	fmt.Fprintf(w, "\n%s\n", report.Channel)
	fmt.Fprintf(w, "  %s\n", file)

	for _, res := range report.Results {
		switch {
		case res.Skipped != "":
			if verbose {
				fmt.Fprintf(w, "  SKIP  %s\n        %s\n", res.Name, res.Skipped)
			}

		case res.Error != nil:
			// Distinguished from a failed assertion. "The test could not run" and
			// "the channel did the wrong thing" need different responses.
			fmt.Fprintf(w, "  ERROR %s\n        could not run: %v\n", res.Name, res.Error)

		case res.Passed:
			if verbose {
				fmt.Fprintf(w, "  ok    %s  (%s, %s)\n",
					res.Name, res.Outcome, res.Duration.Round(time.Microsecond))
			}

		default:
			fmt.Fprintf(w, "  FAIL  %s\n", res.Name)
			// Every failure, not just the first. Fixing one at a time when four are
			// wrong wastes four runs, and the four together usually describe one
			// underlying mistake.
			for _, f := range res.Failures {
				fmt.Fprintf(w, "        %s\n", f)
			}
			if showMessage {
				fmt.Fprintf(w, "\n        the message as the destinations saw it:\n")
				for _, line := range strings.Split(strings.TrimRight(res.Transformed, "\r"), "\r") {
					fmt.Fprintf(w, "          %s\n", line)
				}
				fmt.Fprintln(w)
			}
		}
	}

	if failed == 0 && skipped == 0 && !verbose {
		fmt.Fprintf(w, "  %d test(s) passed\n", passed)
	}
}

func summariseTests(w io.Writer, reports []*channeltest.Report, loadErrors []string) error {
	var passed, failed, skipped int
	for _, r := range reports {
		p, f, s := r.Counts()
		passed += p
		failed += f
		skipped += s
	}

	if len(loadErrors) > 0 {
		fmt.Fprintf(w, "\n%d test file(s) could not be read:\n", len(loadErrors))
		for _, e := range loadErrors {
			fmt.Fprintf(w, "  %s\n", e)
		}
	}

	fmt.Fprintf(w, "\n%d passed", passed)
	if failed > 0 {
		fmt.Fprintf(w, ", %d failed", failed)
	}
	if skipped > 0 {
		fmt.Fprintf(w, ", %d skipped", skipped)
	}
	fmt.Fprintln(w)

	if failed > 0 || len(loadErrors) > 0 {
		// Non-zero exit so this is usable in CI without parsing the output.
		return fmt.Errorf("%d test(s) failed", failed+len(loadErrors))
	}
	return nil
}

func writeTestJSON(w io.Writer, reports []*channeltest.Report, loadErrors []string) error {
	type jsonResult struct {
		Name     string   `json:"name"`
		Passed   bool     `json:"passed"`
		Skipped  string   `json:"skipped,omitempty"`
		Outcome  string   `json:"outcome,omitempty"`
		Ack      string   `json:"ack,omitempty"`
		Failures []string `json:"failures,omitempty"`
		Error    string   `json:"error,omitempty"`
		Millis   float64  `json:"milliseconds"`
	}
	type jsonReport struct {
		Channel string       `json:"channel"`
		Results []jsonResult `json:"results"`
	}

	var out struct {
		Reports    []jsonReport `json:"reports"`
		LoadErrors []string     `json:"loadErrors,omitempty"`
		Passed     int          `json:"passed"`
		Failed     int          `json:"failed"`
		Skipped    int          `json:"skipped"`
	}
	out.LoadErrors = loadErrors

	for _, r := range reports {
		jr := jsonReport{Channel: r.Channel}
		for _, res := range r.Results {
			j := jsonResult{
				Name:     res.Name,
				Passed:   res.Passed,
				Skipped:  res.Skipped,
				Outcome:  res.Outcome,
				Ack:      res.Ack,
				Failures: res.Failures,
				Millis:   float64(res.Duration.Microseconds()) / 1000,
			}
			if res.Error != nil {
				j.Error = res.Error.Error()
			}
			jr.Results = append(jr.Results, j)
		}
		out.Reports = append(out.Reports, jr)

		p, f, s := r.Counts()
		out.Passed += p
		out.Failed += f
		out.Skipped += s
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	if out.Failed > 0 || len(loadErrors) > 0 {
		return fmt.Errorf("%d test(s) failed", out.Failed+len(loadErrors))
	}
	return nil
}
