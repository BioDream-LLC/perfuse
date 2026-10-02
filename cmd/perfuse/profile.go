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

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/profile"
	"github.com/biodream-llc/perfuse/mllp"
)

// What is actually in a feed, from the command line.
//
// This is the first item of the domain-tax work, and the reason it matters is arithmetic rather than
// technical. Every interface is built from a specification describing what the sending system is supposed to
// send. That document is usually years old, was written for a different version, and omits the three
// Z-segments the site added in 2019. So the real shape of a feed gets discovered in production, one surprise
// at a time, and each surprise costs an outage or a consultant.
//
// Reading ten thousand real messages and stating what is in them replaces that document with fact. Every
// analyst already does a worse version of this with grep, which is the clearest possible signal that the tool
// should exist.
//
// # The safety constraint that shapes it
//
// A profile of real traffic must not become a way to read patient data. So this reports counts, fill rates,
// cardinality and shapes - never values - with the single exception of fields whose values are a code set
// small enough to be a code set. That distinction is made by the profile package, not here.
//
// # Comparing two profiles is where the money is
//
// A profile on its own answers "what am I receiving". Two profiles of the same feed a month apart answer
// "what changed", which is the question nobody can currently answer at all. A field that was always populated
// and stopped being is not an error - the messages are still valid HL7 - so nothing in a normal engine
// notices until a receiver falls over.

func cmdProfile(args []string, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("profile", flag.ContinueOnError)
	fs.SetOutput(stderr)

	var (
		asJSON  = fs.Bool("json", false, "emit the profile as JSON")
		against = fs.String("against", "",
			"compare with a profile saved earlier as JSON, and report only what changed")
		save   = fs.String("save", "", "write the profile to this file as JSON")
		strict = fs.Bool("strict", false, "with -against, exit non-zero if anything changed")
		framed = fs.Bool("framed", false, "the input is MLLP-framed, with several messages per file")
		asV3   = fs.Bool("v3", false, "the input is HL7 v3 XML rather than v2")
	)

	fs.Usage = func() {
		fmt.Fprint(stderr, `Usage of profile:
  perfuse profile [flags] <path>...

Reads messages and reports what is actually in them: which fields are always
populated, which never are, the real value sets of coded fields, repetition
counts, and which Z-segments appear.

Paths may be files or directories. Values are never reported except for fields
whose values form a small code set.

  perfuse profile corpus/                       # what am I receiving
  perfuse profile -save last-month.json corpus/ # keep it for comparison
  perfuse profile -against last-month.json new/ # what changed since
  perfuse profile -against last-month.json -strict new/

The comparison is the part worth automating. A field that was always populated
and stopped being is still valid HL7, so nothing else notices until a receiver
falls over.

Flags:
`)
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		return err
	}

	if fs.NArg() == 0 {
		fs.Usage()
		return fmt.Errorf("profile needs at least one file or directory")
	}

	messages, err := readCorpus(fs.Args(), *framed)
	if err != nil {
		return err
	}
	if len(messages) == 0 {
		// Not an empty report. "0 messages, no fields populated" is technically true and would be acted on.
		return fmt.Errorf("no messages were found in %s", strings.Join(fs.Args(), ", "))
	}

	// Declared with a flag rather than sniffed. Detecting XML here would be easy and would be the one place in this
	// codebase that guesses at a data type, and the failure it invites is quiet: a v2 feed misread as v3 profiles as
	// nothing populated, which is a plausible-looking report rather than an error.
	report := profile.BuildFor(v3DataType(*asV3), messages)

	if *save != "" {
		if err := saveProfile(*save, report); err != nil {
			return err
		}
		fmt.Fprintf(stdout, "wrote %s (%d messages)\n\n", *save, report.Messages)
	}

	if *against != "" {
		before, err := loadProfile(*against)
		if err != nil {
			return err
		}

		comparison := profile.Compare(before, report)

		if *asJSON {
			enc := json.NewEncoder(stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(comparison); err != nil {
				return err
			}
		} else {
			writeComparison(stdout, comparison, before, report)
		}

		if *strict && len(comparison.Changes) > 0 {
			return fmt.Errorf("%d change(s) since %s", len(comparison.Changes), *against)
		}
		return nil
	}

	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(report)
	}

	writeProfile(stdout, report)
	return nil
}

func saveProfile(path string, report *profile.Report) error {
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o644)
}

func loadProfile(path string) (*profile.Report, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var report profile.Report
	dec := json.NewDecoder(strings.NewReader(string(body)))
	// Unknown fields are refused, so a profile written by a much older or newer version is rejected rather
	// than silently compared with half its data missing - which would report changes that are really just
	// a format difference, and send somebody looking for a problem in the feed.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&report); err != nil {
		return nil, fmt.Errorf("%s is not a profile this version can read: %w", path, err)
	}
	return &report, nil
}

// writeProfile prints the report as prose.
func writeProfile(w io.Writer, r *profile.Report) {
	fmt.Fprintf(w, "%d message(s) read", r.Messages)
	if r.Unreadable > 0 {
		// Stated in the first line rather than in a footnote. A corpus with many unreadable messages is not
		// a corpus to draw conclusions from, and the reader has to know that before reading the numbers.
		fmt.Fprintf(w, ", %d could not be parsed", r.Unreadable)
	}
	fmt.Fprintln(w)

	if len(r.Types) > 0 {
		fmt.Fprintf(w, "\nMessage types:\n")
		for _, t := range r.Types {
			fmt.Fprintf(w, "  %-24s %6d  %5.1f%%\n", t.Type, t.Count, t.Rate*100)
		}
		if len(r.Types) > 1 {
			// The commonest first surprise, and worth saying rather than leaving to be inferred from a list.
			fmt.Fprintf(w, "  (%d different types — a feed described as one thing usually carries several)\n",
				len(r.Types))
		}
	}

	for _, seg := range r.Segments {
		fmt.Fprintf(w, "\n%s", seg.ID)
		if seg.Name != "" {
			fmt.Fprintf(w, " — %s", seg.Name)
		}
		if !seg.Standard {
			// The interesting case: a segment the standard does not define is the one a specification
			// never mentions and an integration always has to handle.
			fmt.Fprintf(w, "  [not in the standard]")
		}
		fmt.Fprintf(w, "   in %.1f%% of messages", seg.Rate*100)
		if seg.MaxPerMessage > 1 {
			fmt.Fprintf(w, ", up to %d per message", seg.MaxPerMessage)
		}
		fmt.Fprintf(w, "\n")

		for _, f := range seg.Fields {
			fmt.Fprintf(w, "  %-14s %5.1f%% populated", f.Path, f.FillRate*100)

			switch {
			case f.FillRate == 0:
				// The most actionable fact in a profile: a field the specification describes and the sender
				// has never once populated.
				fmt.Fprintf(w, "  — never sent")
			case f.FillRate == 1:
				fmt.Fprintf(w, "  — always sent")
			}

			if f.MaxRepeats > 1 {
				fmt.Fprintf(w, ", repeats up to %d", f.MaxRepeats)
			}
			if f.Shape != "" {
				fmt.Fprintf(w, ", %s", f.Shape)
			}
			fmt.Fprintln(w)

			if len(f.Codes) > 0 {
				var codes []string
				for _, c := range f.Codes {
					// An unknown code is flagged, because it means the sender is using a local value and
					// anything downstream mapping strictly will reject it. That is the actionable half.
					mark := ""
					if !c.Known {
						mark = " [not in the table]"
					}
					codes = append(codes, fmt.Sprintf("%s (%d)%s", c.Code, c.Count, mark))
				}
				fmt.Fprintf(w, "                 values: %s\n", strings.Join(codes, ", "))
			}
		}
	}

	if len(r.Notes) > 0 {
		fmt.Fprintf(w, "\nWorth knowing:\n")
		for _, n := range r.Notes {
			fmt.Fprintf(w, "  - %s\n", n)
		}
	}
}

// writeComparison prints only what changed.
func writeComparison(w io.Writer, c *profile.Comparison, before, after *profile.Report) {
	fmt.Fprintf(w, "comparing %d message(s) against a profile of %d\n",
		after.Messages, before.Messages)

	if len(c.Changes) == 0 {
		fmt.Fprintf(w, "\nNothing changed shape.\n")
		return
	}

	fmt.Fprintf(w, "\n%d change(s):\n\n", len(c.Changes))

	if c.Note != "" {
		// Printed before the changes, because it says how much the comparison can be trusted and that
		// changes how the list below should be read.
		fmt.Fprintf(w, "\n%s\n", c.Note)
	}

	// Breaking changes are separated rather than merely sorted. "3 changes" where one will stop a feed and
	// two are cosmetic is a list somebody skims; two headings is a list somebody acts on.
	var breaking, other []string
	for _, ch := range c.Changes {
		line := fmt.Sprintf("%-18s %s", ch.Where, ch.What)
		if ch.Breaking {
			breaking = append(breaking, line)
		} else {
			other = append(other, line)
		}
	}

	if len(breaking) > 0 {
		fmt.Fprintf(w, "  Likely to break something downstream:\n")
		for _, line := range breaking {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	if len(other) > 0 {
		fmt.Fprintf(w, "  Worth knowing:\n")
		for _, line := range other {
			fmt.Fprintf(w, "    %s\n", line)
		}
	}
	return
}

// readCorpus reads messages from files and directories.
func readCorpus(paths []string, framed bool) ([][]byte, error) {
	var out [][]byte

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}

		if !info.IsDir() {
			msgs, err := readMessageFile(p, framed)
			if err != nil {
				return nil, err
			}
			out = append(out, msgs...)
			continue
		}

		var files []string
		err = filepath.WalkDir(p, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			files = append(files, path)
			return nil
		})
		if err != nil {
			return nil, err
		}

		// Sorted, because a profile is a document people diff between runs and directory order is not
		// stable across filesystems.
		sort.Strings(files)

		for _, f := range files {
			msgs, err := readMessageFile(f, framed)
			if err != nil {
				// One unreadable file does not cost the whole corpus. A real directory of captured traffic
				// contains a lock file or a half-written message, and refusing everything over one of them
				// would make the tool useless on exactly the data it is for.
				continue
			}
			out = append(out, msgs...)
		}
	}

	return out, nil
}

func readMessageFile(path string, framed bool) ([][]byte, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	if framed {
		// The real MLLP reader rather than a split on the delimiters, so a frame that is malformed is
		// reported as malformed instead of quietly producing two half-messages.
		var out [][]byte
		reader := mllp.NewReader(strings.NewReader(string(body)), 16<<20)
		for {
			msg, err := reader.ReadMessage()
			if err != nil {
				break
			}
			out = append(out, msg)
		}
		if len(out) == 0 {
			return nil, fmt.Errorf("%s has no complete MLLP frames in it", path)
		}
		return out, nil
	}

	// One message per file unless framed. Splitting on "MSH|" would be a guess, and a guess that split a
	// message containing "MSH|" inside a field would corrupt the profile silently.
	if len(strings.TrimSpace(string(body))) == 0 {
		return nil, fmt.Errorf("%s is empty", path)
	}
	return [][]byte{body}, nil
}

// v3DataType turns the -v3 flag into the data type string the profiler dispatches on.
//
// A helper rather than the string inline at three call sites, because the dispatcher compares it literally and a typo would silently
// select the v2 profiler - which is the failure this whole arrangement exists to prevent.
func v3DataType(v3 bool) string {
	if v3 {
		return string(config.DataHL7v3)
	}

	return ""
}
