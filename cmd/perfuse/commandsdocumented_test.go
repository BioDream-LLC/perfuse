package main

import (
	"os"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// Every command the binary dispatches must be listed in its own usage text.
//
// perfuse token was fully built - subcommands, flags, help text explaining why a machine credential differs from a browser
// session - and absent from the usage block, so the only way to find it was to read the dispatch switch. A command nobody
// can discover is indistinguishable from one that was never written, which makes this the same defect as an unimplemented
// feature wearing a passing test suite.
//
// Read from the source rather than by running the binary, because the fault is a discrepancy between two things in this
// file and comparing them is the whole point.
func TestEveryCommandAppearsInTheUsage(t *testing.T) {
	src, err := os.ReadFile("main.go")
	if err != nil {
		t.Fatal(err)
	}

	text := string(src)

	// The usage block is the raw string printed by help, ending before the dispatch switch.
	usageStart := strings.Index(text, "perfuse - healthcare integration tooling")
	if usageStart < 0 {
		t.Fatal("cannot find the usage text, so this test is checking nothing")
	}

	usage := text[usageStart:]
	if end := strings.Index(usage, "func "); end > 0 {
		usage = usage[:end]
	}

	// Commands reached by the dispatch switch. Aliases and help are excluded: they are ways of asking for the usage, not
	// things the usage should advertise.
	skip := map[string]bool{
		"help": true, "--help": true, "-h": true,
		"version": true, "--version": true, "-v": true,
	}

	var missing []string
	seen := map[string]bool{}

	for _, m := range regexp.MustCompile(`case ("[a-z-]+"(?:, "[a-z-]+")*):`).FindAllStringSubmatch(text, -1) {
		for _, quoted := range strings.Split(m[1], ", ") {
			name := strings.Trim(quoted, `"`)
			if skip[name] || seen[name] {
				continue
			}
			seen[name] = true

			if !strings.Contains(usage, "perfuse "+name) {
				missing = append(missing, name)
			}
		}
	}

	// A positive control. If the switch stopped matching, every command would look documented.
	if len(seen) < 10 {
		t.Fatalf("only found %d commands in the dispatch switch, so this test is not reading it properly", len(seen))
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Errorf("these commands are dispatched but not listed in the usage, so nobody can find them: %v", missing)
	}
}
