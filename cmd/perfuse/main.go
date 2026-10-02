// Command perfuse reads and explains healthcare integration configuration.
//
// The first thing it does is read Mirth Connect channel exports, because that
// is what the installed base is written in. You cannot replace an interface
// engine without first being able to read what people already have.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/analyze"
	"github.com/biodream-llc/perfuse/internal/mirth"
)

// version is overridden at build time with -ldflags "-X main.version=...".
var version = "dev"

// buildVersion is what `perfuse version` reports.
//
// The ldflags value wins when there is one, which is how released binaries are built. Without it the module version recorded by the
// toolchain is used instead, because the README tells people to install with `go install`, and that path applies no ldflags at all - so
// every binary installed the advertised way called itself "dev" and a bug report could not say which release it came from.
func buildVersion() string {
	if version != "dev" {
		return version
	}

	info, ok := debug.ReadBuildInfo()
	if !ok || info.Main.Version == "" {
		return version
	}

	// "(devel)" is what the toolchain reports for a build from a working tree rather than a released module, which is no more useful
	// than "dev" and is less honest about being a local build.
	if info.Main.Version == "(devel)" {
		return version
	}

	return info.Main.Version
}

const usage = `perfuse - healthcare integration tooling

Usage:
  perfuse serve   [flags]              run the web interface and API
  perfuse fhir    <subcommand>         convert HL7 v2 to FHIR, validate it, or serve it
  perfuse run     [flags] <path>...    run channels defined in YAML
  perfuse check   [flags] <path>...    validate channel definitions and print what they do
  perfuse explain [flags] <path>...    describe Mirth channels and what blocks a migration
  perfuse translate [flags] <path>...  convert Mirth channels into Perfuse channels
  perfuse compare [flags] <dir> <dir>  prove two engines agree over the same traffic
  perfuse test    [flags] <path>...    run tests against channel definitions
  perfuse generate [flags]             make synthetic HL7 v2 messages for testing
  perfuse deident [flags] <path>...    turn real messages into a corpus that can be shared
  perfuse listen  [flags]              accept HL7 v2 over MLLP and acknowledge it
  perfuse send    [flags] <path>...    send HL7 v2 messages over MLLP
  perfuse profile [flags] <path>...    report what is actually in a feed, and what changed
  perfuse contract <subcommand>        say what a feed must look like, then check it
  perfuse init    [flags]              set up a directory and a service definition
  perfuse service <cmd>               run as a Windows service
  perfuse token   <subcommand>         issue credentials for machines rather than people
  perfuse sbom    [flags]              list everything linked into this binary
  perfuse version                      print the version

Run a command with -h for its flags.

explain: paths may be channel XML files or directories, searched for *.xml.
  -json      emit machine-readable output instead of prose
  -strict    exit non-zero if any channel has a blocking finding
  -quiet     with -strict, print only the summary

MLLP has no authentication or encryption. Restrict access at the network layer.
`

// errBlocking reports that -strict was given and at least one channel had a
// blocking finding. It is a sentinel rather than an os.Exit so that run stays
// testable.
var errBlocking = errors.New("blocking findings present")

// errNoArgs reports that no command was given.
var errNoArgs = errors.New("no command")

func main() {
	err := run(os.Args[1:], os.Stdout, os.Stderr)
	switch {
	case err == nil:
		return
	case errors.Is(err, errBlocking):
		os.Exit(1)
	case errors.Is(err, errNoArgs):
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	default:
		fmt.Fprintf(os.Stderr, "perfuse: %v\n", err)
		os.Exit(2)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		return errNoArgs
	}

	// A service asks first, before anything else runs.
	//
	// Windows starts a service with whatever command line was registered - which is an ordinary serve line - so the
	// only way to tell the difference is to ask the operating system. This has to happen before the command runs,
	// because the service manager expects to be connected to within thirty seconds of start.
	switch args[0] {
	case "serve", "fhir", "run", "listen":
		handled, err := runUnderServiceManagerIfNeeded(func() error {
			return dispatchLongRunning(args, stdout, stderr)
		}, stderr)
		if handled {
			return err
		}
	}

	switch args[0] {
	case "serve":
		return cmdServe(args[1:], stdout, stderr)
	case "fhir":
		return cmdFHIR(args[1:], stdout, stderr)
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "check":
		return cmdCheck(args[1:], stdout, stderr)
	case "explain":
		return cmdExplain(args[1:], stdout, stderr)
	case "translate":
		return cmdTranslate(args[1:], stdout, stderr)
	case "compare":
		return cmdCompare(args[1:], stdout, stderr)
	case "generate":
		return cmdGenerate(args[1:])
	case "deident":
		return cmdDeident(args[1:], stdout, stderr)
	case "test":
		return cmdTest(args[1:])
	case "listen":
		return cmdListen(args[1:], stdout, stderr)
	case "send":
		return cmdSend(args[1:], stdout, stderr)
	case "contract":
		return cmdContract(args[1:], stdout, stderr)
	case "profile":
		return cmdProfile(args[1:], stdout, stderr)
	case "init":
		return cmdInit(args[1:], stdout, stderr)
	case "token":
		return runToken(args[1:], stdout, stderr)
	case "service":
		return cmdService(args[1:], stdout, stderr)
	case "sbom":
		return cmdSBOM(args[1:], stdout, stderr)
	case "version", "--version", "-v":
		fmt.Fprintf(stdout, "perfuse %s\n", buildVersion())
		return nil
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
		return nil
	default:
		return fmt.Errorf("unknown command %q\n\n%s", args[0], usage)
	}
}

func cmdExplain(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("explain", flag.ContinueOnError)
	fset.SetOutput(stderr)
	asJSON := fset.Bool("json", false, "emit machine-readable output")
	strict := fset.Bool("strict", false, "exit non-zero if any channel has a blocking finding")
	quiet := fset.Bool("quiet", false, "print only the summary")
	if err := fset.Parse(args); err != nil {
		return err
	}
	if fset.NArg() == 0 {
		return fmt.Errorf("explain needs at least one path")
	}

	files, err := collectXML(fset.Args())
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return fmt.Errorf("no .xml files found in %s", strings.Join(fset.Args(), ", "))
	}

	var (
		reports  []*analyze.Report
		failed   int
		blocking int
	)
	for _, path := range files {
		c, err := mirth.ParseChannelFile(path)
		if err != nil {
			// One unreadable file must not stop a run over a whole directory.
			fmt.Fprintf(stderr, "skipped %s: %v\n", path, err)
			failed++
			continue
		}
		r := analyze.Channel(c)
		reports = append(reports, r)
		if !r.Translatable() {
			blocking++
		}
	}

	if *asJSON {
		if err := writeJSON(stdout, reports); err != nil {
			return err
		}
	} else if !*quiet {
		for i, r := range reports {
			if i > 0 {
				fmt.Fprintln(stdout, strings.Repeat("─", 74))
			}
			if err := analyze.Explain(stdout, r); err != nil {
				return err
			}
		}
		if len(reports) > 1 {
			fmt.Fprintln(stdout, strings.Repeat("─", 74))
			writeSummary(stdout, reports, failed)
		}
	} else {
		writeSummary(stdout, reports, failed)
	}

	if *strict && blocking > 0 {
		return errBlocking
	}
	return nil
}

func writeSummary(w io.Writer, reports []*analyze.Report, failed int) {
	var blockers, warnings, notes, blocked int
	for _, r := range reports {
		b, wn, n := r.Counts()
		blockers += b
		warnings += wn
		notes += n
		if b > 0 {
			blocked++
		}
	}

	fmt.Fprintf(w, "\n%d channel(s) read", len(reports))
	if failed > 0 {
		fmt.Fprintf(w, ", %d unreadable", failed)
	}
	fmt.Fprintf(w, "\n%d translate cleanly, %d blocked\n", len(reports)-blocked, blocked)
	fmt.Fprintf(w, "%d blocker(s), %d warning(s), %d note(s)\n", blockers, warnings, notes)
}

// jsonReport is a deliberately small view. Emitting the whole parsed channel
// would make the output unusable for the thing it is for, which is feeding a
// migration checklist.
type jsonReport struct {
	Channel      string        `json:"channel"`
	Enabled      bool          `json:"enabled"`
	MirthVersion string        `json:"mirthVersion,omitempty"`
	Source       string        `json:"source,omitempty"`
	Destinations []string      `json:"destinations,omitempty"`
	Translatable bool          `json:"translatable"`
	Blockers     int           `json:"blockers"`
	Warnings     int           `json:"warnings"`
	Notes        int           `json:"notes"`
	Findings     []jsonFinding `json:"findings"`
}

type jsonFinding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Where    string `json:"where"`
	What     string `json:"what"`
	Why      string `json:"why,omitempty"`
}

func writeJSON(w io.Writer, reports []*analyze.Report) error {
	out := make([]jsonReport, 0, len(reports))
	for _, r := range reports {
		b, wn, n := r.Counts()
		jr := jsonReport{
			Channel:      r.Channel.Name,
			Enabled:      r.Channel.Enabled,
			MirthVersion: r.Channel.MirthVersion,
			Source:       r.Channel.Source.Transport,
			Translatable: r.Translatable(),
			Blockers:     b,
			Warnings:     wn,
			Notes:        n,
			Findings:     make([]jsonFinding, 0, len(r.Findings)),
		}
		for _, d := range r.Channel.Destinations {
			jr.Destinations = append(jr.Destinations, d.Transport)
		}
		for _, f := range r.Findings {
			jr.Findings = append(jr.Findings, jsonFinding{
				Severity: string(f.Severity), Code: f.Code,
				Where: f.Where, What: f.What, Why: f.Why,
			})
		}
		out = append(out, jr)
	}

	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(out)
}

// collectXML expands the given paths into a sorted, de-duplicated list of XML
// files. Directories are walked.
func collectXML(paths []string) ([]string, error) {
	seen := map[string]bool{}
	var out []string

	add := func(p string) {
		abs, err := filepath.Abs(p)
		if err != nil {
			abs = p
		}
		if !seen[abs] {
			seen[abs] = true
			out = append(out, p)
		}
	}

	for _, p := range paths {
		info, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			add(p)
			continue
		}
		err = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.EqualFold(filepath.Ext(path), ".xml") {
				return nil
			}
			add(path)
			return nil
		})
		if err != nil {
			return nil, err
		}
	}

	sort.Strings(out)
	return out, nil
}

// dispatchLongRunning runs the commands that can be a service.
//
// Separate from the main switch so the service path and the console path run exactly the same function rather than two
// that have to be kept in step.
func dispatchLongRunning(args []string, stdout, stderr io.Writer) error {
	switch args[0] {
	case "serve":
		return cmdServe(args[1:], stdout, stderr)
	case "fhir":
		return cmdFHIR(args[1:], stdout, stderr)
	case "run":
		return cmdRun(args[1:], stdout, stderr)
	case "listen":
		return cmdListen(args[1:], stdout, stderr)
	default:
		return fmt.Errorf("%q cannot run as a service", args[0])
	}
}
