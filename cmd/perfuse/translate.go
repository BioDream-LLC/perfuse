package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/mirth"
	"github.com/biodream-llc/perfuse/internal/translate"
)

// perfuse translate converts Mirth channels into Perfuse channel definitions.
//
// `perfuse explain` says what blocks a migration. This does most of the migration,
// which is a much larger claim, so the output leads with what it was unsure about
// rather than with what it managed.

func cmdTranslate(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("translate", flag.ContinueOnError)
	fset.SetOutput(stderr)
	fset.Usage = func() {
		fmt.Fprint(stderr, `Usage of translate:
  perfuse translate [flags] <path>...

Converts Mirth channel exports into Perfuse channel files. Paths may be XML
files or directories, which are searched for *.xml.

Nothing is silently dropped. Every part of a channel is either translated,
carried across as a script that runs unchanged, or reported as needing you.

  perfuse translate channel.xml                 # print to stdout
  perfuse translate -o ./channels channels/     # write a file per channel
  perfuse translate -strict channels/           # exit 1 if anything is blocked
  perfuse translate -json channels/             # machine-readable report

Flags:
`)
		fset.PrintDefaults()
	}

	outDir := fset.String("o", "", "write channel files into this directory")
	strict := fset.Bool("strict", false, "exit non-zero if any channel has a blocker")
	jsonOut := fset.Bool("json", false, "emit a machine-readable report")
	force := fset.Bool("force", false, "overwrite existing channel files")
	quiet := fset.Bool("quiet", false, "print only the summary")

	if err := fset.Parse(args); err != nil {
		return err
	}
	paths := fset.Args()
	if len(paths) == 0 {
		fset.Usage()
		return fmt.Errorf("name a Mirth channel export or a directory")
	}

	files, err := collectXML(paths)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no XML files found")
	}

	var results []*translate.Result
	var failures []string

	for _, file := range files {
		ch, err := mirth.ParseChannelFile(file)
		if err != nil {
			// Reported rather than skipped. A channel that silently did not translate
			// is one somebody discovers is missing after the migration.
			failures = append(failures, fmt.Sprintf("%s: %v", file, err))
			continue
		}

		res := translate.Channel(ch)
		res.SortNotes()
		results = append(results, res)

		if *outDir != "" {
			if err := writeTranslated(*outDir, res, *force); err != nil {
				failures = append(failures, err.Error())
			}
		}
	}

	if *jsonOut {
		return translateJSON(stdout, results, failures, *strict)
	}

	// With no output directory and one channel, print the YAML so it can be piped.
	if *outDir == "" && len(results) == 1 && !*quiet {
		fmt.Fprint(stdout, results[0].YAML)
		fmt.Fprintln(stderr)
		printTranslateNotes(stderr, results[0])
		return translateExit(results, failures, *strict)
	}

	if !*quiet {
		for _, res := range results {
			printTranslateResult(stderr, res, *outDir)
		}
	}
	printTranslateSummary(stderr, results, failures, *outDir)

	return translateExit(results, failures, *strict)
}

func writeTranslated(dir string, res *translate.Result, force bool) error {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return err
	}
	path := filepath.Join(dir, res.FileName())

	if !force {
		if _, err := os.Stat(path); err == nil {
			// Refusing beats overwriting. A second run after somebody has edited the
			// output by hand would otherwise destroy their work.
			return fmt.Errorf("%s already exists; use -force to overwrite it", path)
		}
	}

	if err := os.WriteFile(path, []byte(res.YAML), 0o600); err != nil {
		return err
	}

	// Validated after writing, because a file that does not load is worth knowing
	// about immediately rather than when somebody tries to start it.
	if _, err := config.LoadFile(path); err != nil {
		return fmt.Errorf("%s was written but does not load: %v", path, err)
	}
	return nil
}

func printTranslateResult(w io.Writer, res *translate.Result, outDir string) {
	fmt.Fprintf(w, "\n%s\n", res.SourceName)

	switch res.Confidence {
	case translate.Complete:
		fmt.Fprintf(w, "  translated completely\n")
	case translate.Reviewable:
		fmt.Fprintf(w, "  translated, with %d thing(s) to check\n", len(res.Notes))
	case translate.Partial:
		fmt.Fprintf(w, "  translated in part\n")
	case translate.Blocked:
		fmt.Fprintf(w, "  BLOCKED: %d thing(s) stop this running here\n", res.Counts.Blockers)
	}

	if outDir != "" {
		fmt.Fprintf(w, "  written to %s\n", filepath.Join(outDir, res.FileName()))
	}

	c := res.Counts
	var parts []string
	if c.FilterRules > 0 {
		parts = append(parts, fmt.Sprintf("%d filter rule(s) became a filter expression", c.FilterRules))
	}
	if c.Declarative > 0 {
		parts = append(parts, fmt.Sprintf("%d step(s) became declarative transformations", c.Declarative))
	}
	if c.Scripted > 0 {
		parts = append(parts, fmt.Sprintf("%d step(s) carried over as JavaScript", c.Scripted))
	}
	for _, p := range parts {
		fmt.Fprintf(w, "  %s\n", p)
	}

	printTranslateNotes(w, res)
}

func printTranslateNotes(w io.Writer, res *translate.Result) {
	for _, n := range res.Notes {
		label := "  note"
		switch n.Severity {
		case "blocker":
			label = "  BLOCKED"
		case "warning":
			label = "  check"
		}

		where := ""
		if n.Where != "" {
			where = n.Where + ": "
		}
		fmt.Fprintf(w, "%s  %s%s\n", label, where, n.Message)
		if n.Action != "" {
			// The action is indented under the message rather than joined to it,
			// because somebody scanning a long report reads the left column.
			fmt.Fprintf(w, "            → %s\n", n.Action)
		}
	}
}

func printTranslateSummary(w io.Writer, results []*translate.Result, failures []string, outDir string) {
	var complete, reviewable, blocked int
	for _, r := range results {
		switch r.Confidence {
		case translate.Complete:
			complete++
		case translate.Blocked:
			blocked++
		default:
			reviewable++
		}
	}

	if len(failures) > 0 {
		fmt.Fprintf(w, "\n%d file(s) could not be handled:\n", len(failures))
		for _, f := range failures {
			fmt.Fprintf(w, "  %s\n", f)
		}
	}

	fmt.Fprintf(w, "\n%d channel(s): %d complete, %d to review, %d blocked\n",
		len(results), complete, reviewable, blocked)

	if blocked > 0 {
		fmt.Fprintln(w,
			"\nA blocked channel is one that needs a connector Perfuse does not have, or a\n"+
				"script that reaches outside it. The rest of the translation is still there and\n"+
				"still useful: keep that channel in Mirth and move the others.")
	}
}

func translateExit(results []*translate.Result, failures []string, strict bool) error {
	if len(failures) > 0 {
		return fmt.Errorf("%d file(s) could not be translated", len(failures))
	}
	if !strict {
		return nil
	}
	blocked := 0
	for _, r := range results {
		if r.Blocked() {
			blocked++
		}
	}
	if blocked > 0 {
		return fmt.Errorf("%d channel(s) have blockers", blocked)
	}
	return nil
}

func translateJSON(w io.Writer, results []*translate.Result, failures []string, strict bool) error {
	var out struct {
		Channels []*translate.Result `json:"channels"`
		Failures []string            `json:"failures,omitempty"`
	}
	out.Channels = results
	out.Failures = failures

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		return err
	}
	return translateExit(results, failures, strict)
}

// suggestChannelName is used by the tests to check the sanitiser through the
// command surface.
func suggestChannelName(name string) string {
	return strings.TrimSuffix((&translate.Result{Name: name}).FileName(), ".yaml")
}
