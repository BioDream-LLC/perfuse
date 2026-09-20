package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/equiv"
)

// The parallel-run comparison, from the command line.
//
// Directories rather than a live capture, because that is what a real parallel run leaves behind. A site
// puts a File Writer on the incumbent and a file destination on Perfuse, points both at a share, and two
// weeks later has two folders. Asking them to build a capture harness first would put the burden in
// exactly the wrong place - the point of this command is that gathering the evidence has to be easy or
// nobody gathers it.

func cmdCompare(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("compare", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		leftName  = fs.String("left-name", "", "what to call the first side, e.g. Mirth")
		rightName = fs.String("right-name", "", "what to call the second side, e.g. Perfuse")
		content   = fs.Bool("show-values", false,
			"include message content in the report (this is patient data)")
		ignore = fs.String("ignore", "",
			"comma-separated paths whose differences are expected, e.g. MSH-7,MSH-10")
		strict = fs.Bool("strict", false, "exit non-zero unless the two sides agree completely")
		quiet  = fs.Bool("quiet", false, "print only the headline")
	)

	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage of compare:
  perfuse compare [flags] <left-dir> <right-dir>

Compares the output of two engines over the same traffic and reports where they
differ. Each directory holds one file per message; they are paired by MSH-10,
the message control id.

This is the evidence a migration actually turns on. Run both engines on the same
production messages with only the incumbent delivering, then compare what each
one produced.

Differences are grouped by cause, so three thousand messages differing for one
reason are one finding, not three thousand.

Message content is withheld by default, because a report is a thing people paste
into tickets. -show-values overrides that and says so in the output.

  perfuse compare mirth-out/ perfuse-out/
  perfuse compare -left-name Mirth -right-name Perfuse a/ b/
  perfuse compare -ignore MSH-7,MSH-10 a/ b/        # expected disagreements
  perfuse compare -strict a/ b/                     # exit 1 unless identical

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() != 2 {
		fs.Usage()
		return fmt.Errorf("compare needs exactly two directories, got %d", fs.NArg())
	}

	left, leftSkipped, err := loadOutputs(fs.Arg(0))
	if err != nil {
		return err
	}
	right, rightSkipped, err := loadOutputs(fs.Arg(1))
	if err != nil {
		return err
	}

	opts := equiv.Options{
		LeftName:       *leftName,
		RightName:      *rightName,
		IncludeContent: *content,
	}
	if *ignore != "" {
		for _, p := range strings.Split(*ignore, ",") {
			if p = strings.TrimSpace(p); p != "" {
				opts.IgnorePaths = append(opts.IgnorePaths, p)
			}
		}
	}

	report := equiv.Compare(left, right, opts)

	// Skipped files are reported before the headline, not after. A comparison that silently ignored
	// four hundred unreadable files would produce a clean-looking result that means nothing, and the
	// reader has to know the denominator is short before they read the number.
	if leftSkipped > 0 || rightSkipped > 0 {
		fmt.Fprintf(stdout,
			"warning: %d file(s) on the left and %d on the right could not be paired and were not "+
				"compared; they are not valid HL7 or have no MSH-10 control id\n\n",
			leftSkipped, rightSkipped)
	}

	fmt.Fprintln(stdout, report.Headline())

	if !*quiet {
		writeFindings(stdout, report)
	}

	if *strict && !report.Agreed() {
		return fmt.Errorf("the two sides did not agree")
	}
	return nil
}

// writeFindings prints the grouped causes.
func writeFindings(w io.Writer, report *equiv.Report) {
	if report.IncludesContent {
		fmt.Fprintln(w, "\nThis report contains message content, which is patient data.")
	}

	if len(report.Findings) > 0 {
		fmt.Fprintf(w, "\n%d cause(s), most affected first:\n\n", len(report.Findings))
	}

	for _, f := range report.Findings {
		fmt.Fprintf(w, "  %-24s %-34s %d message(s)\n", f.Path, f.Kind, f.Count)

		// One distinct pair is the useful signal: a single consistent substitution, which is a one-line
		// fix rather than a rule. Worth a sentence rather than a number the reader has to interpret.
		if f.DistinctPairs == 1 && f.Count > 1 {
			fmt.Fprintf(w, "      always the same substitution, so one change should fix all %d\n", f.Count)
		} else if f.DistinctPairs > 1 {
			fmt.Fprintf(w, "      %d different value pairs, so this depends on the data\n", f.DistinctPairs)
		}

		if report.IncludesContent && (f.Left != "" || f.Right != "") {
			fmt.Fprintf(w, "      %s: %q\n", report.LeftName, f.Left)
			fmt.Fprintf(w, "      %s: %q\n", report.RightName, f.Right)
		}

		if len(f.Examples) > 0 {
			fmt.Fprintf(w, "      for example: %s\n", strings.Join(f.Examples, ", "))
		}
	}

	// Messages one side produced and the other did not, listed last but described as the more serious
	// thing they are.
	writeMissing(w, report.LeftName, report.RightName, report.Summary.OnlyLeft)
	writeMissing(w, report.RightName, report.LeftName, report.Summary.OnlyRight)
}

func writeMissing(w io.Writer, produced, notProduced string, ids []string) {
	if len(ids) == 0 {
		return
	}

	fmt.Fprintf(w, "\n%d message(s) %s produced and %s did not. This is usually a filter that does "+
		"not agree, which matters more than a field difference:\n", len(ids), produced, notProduced)

	shown := ids
	if len(shown) > 10 {
		shown = shown[:10]
	}
	for _, id := range shown {
		fmt.Fprintf(w, "  %s\n", id)
	}
	if len(ids) > len(shown) {
		fmt.Fprintf(w, "  ...and %d more\n", len(ids)-len(shown))
	}
}

// loadOutputs reads a directory of messages keyed by control id.
//
// Returns the count that could not be keyed rather than failing. A parallel run over real traffic will
// contain a few files that are not messages - a log, a lock file, something half-written - and refusing
// the whole comparison over one of them would make the tool unusable on exactly the data it is for.
func loadOutputs(dir string) (map[string][]byte, int, error) {
	info, err := os.Stat(dir)
	if err != nil {
		return nil, 0, err
	}
	if !info.IsDir() {
		return nil, 0, fmt.Errorf("%s is not a directory; compare takes a directory per side", dir)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0, err
	}

	out := make(map[string][]byte)
	skipped := 0

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			skipped++
			continue
		}

		key, err := equiv.KeyOf(body)
		if err != nil {
			skipped++
			continue
		}

		// A duplicate control id is a real thing in real traffic - a resend, or a sender that reuses
		// ids. The first wins and the rest are counted as unpaired, because guessing which resend
		// corresponds to which output would manufacture differences.
		if _, exists := out[key]; exists {
			skipped++
			continue
		}
		out[key] = body
	}

	return out, skipped, nil
}
