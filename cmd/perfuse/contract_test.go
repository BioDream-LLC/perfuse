package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The contract command's risk is producing something that gets switched off, so these tests are mostly about
// the workflow being sane and the refusals being right.

func runContract(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := cmdContract(args, &out, &out)
	return out.String(), err
}

func TestPromoteThenCheckHoldsOnTheSameTraffic(t *testing.T) {
	// The property that makes promotion trustworthy: it must not generate a contract that fails on the very
	// traffic it was measured from. If that ever breaks, nobody will use this twice.
	var msgs []string
	for i := 0; i < 150; i++ {
		msgs = append(msgs, adt(string(rune('A'+i%26))+string(rune('a'+i/26)), "F"))
	}
	dir := writeCorpus(t, msgs...)

	out := filepath.Join(t.TempDir(), "c.yaml")
	if _, err := runContract(t, "promote", "-o", out, dir); err != nil {
		t.Fatal(err)
	}

	text, err := runContract(t, "check", out, dir)
	if err != nil {
		t.Fatalf("a promoted contract failed against its own traffic: %v\n%s", err, text)
	}
	if !strings.Contains(text, "held") {
		t.Errorf("the check did not report holding:\n%s", text)
	}
}

func TestCheckReportsAFieldThatStoppedBeingSent(t *testing.T) {
	var before []string
	for i := 0; i < 150; i++ {
		before = append(before, adt(string(rune('A'+i%26))+string(rune('a'+i/26)), "F"))
	}
	dir := writeCorpus(t, before...)

	contractPath := filepath.Join(t.TempDir(), "c.yaml")
	if _, err := runContract(t, "promote", "-o", contractPath, dir); err != nil {
		t.Fatal(err)
	}

	// The same feed with PID-8 no longer populated.
	var after []string
	for i := 0; i < 150; i++ {
		m := adt(string(rune('A'+i%26))+string(rune('a'+i/26)), "F")
		after = append(after, strings.Replace(m, "|19910228|F", "|19910228|", 1))
	}
	changed := writeCorpus(t, after...)

	text, err := runContract(t, "check", "-strict", contractPath, changed)
	if err == nil {
		t.Fatalf("-strict passed despite a field disappearing:\n%s", text)
	}
	if !strings.Contains(text, "PID-8") {
		t.Errorf("the violation does not name the field:\n%s", text)
	}
	// The reason is printed with the violation, because the first question about a failing expectation is
	// whether it was ever right.
	if !strings.Contains(text, "this expectation:") {
		t.Errorf("the violation does not show why the expectation existed:\n%s", text)
	}
}

func TestPromoteRefusesToOverwriteAnEditedContract(t *testing.T) {
	// An existing contract has been pruned by a person, and that pruning is the valuable part of the whole
	// workflow. It must not be lost to a re-run.
	dir := writeCorpus(t, adt("C1", "F"), adt("C2", "M"))

	out := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(out, []byte("expectations: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := runContract(t, "promote", "-o", out, dir)
	if err == nil {
		t.Fatal("promote overwrote an existing contract")
	}
	if !strings.Contains(err.Error(), "-force") {
		t.Errorf("the error does not say how to proceed deliberately: %v", err)
	}
}

func TestAnEmptyFeedIsNotAPass(t *testing.T) {
	// A feed that has stopped is the most serious thing this command can find, and reporting it as a pass is
	// how a monitoring system comes to be trusted wrongly.
	dir := writeCorpus(t, adt("C1", "F"))
	contractPath := filepath.Join(t.TempDir(), "c.yaml")
	if _, err := runContract(t, "promote", "-o", contractPath, dir); err != nil {
		t.Fatal(err)
	}

	empty := t.TempDir()
	_, err := runContract(t, "check", contractPath, empty)
	if err == nil {
		t.Fatal("an empty directory passed the contract")
	}
	if !strings.Contains(err.Error(), "stopped") {
		t.Errorf("the error does not say what it means: %v", err)
	}
}

func TestAMisspelledRuleIsRefused(t *testing.T) {
	// A rule that is silently ignored produces a contract that looks like it is checking something and is not,
	// which is worse than no contract because it is trusted.
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(
		"expectations:\n  - path: PID-3\n    rule: populatd\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := writeCorpus(t, adt("C1", "F"))
	_, err := runContract(t, "check", path, dir)
	if err == nil {
		t.Fatal("a misspelled rule was accepted")
	}
	if !strings.Contains(err.Error(), "populated") {
		t.Errorf("the error does not list the valid rules: %v", err)
	}
}

func TestAnUnknownKeyInAContractIsRefused(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.yaml")
	if err := os.WriteFile(path, []byte(
		"expectations:\n  - path: PID-3\n    rule: populated\n    minrate: 0.9\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := writeCorpus(t, adt("C1", "F"))
	if _, err := runContract(t, "check", path, dir); err == nil {
		t.Fatal("an unknown key was accepted; the expectation would silently use the default tolerance")
	}
}

func TestTheGeneratedFileTellsSomebodyToPruneIt(t *testing.T) {
	// The step that cannot be automated. A generated contract nobody has pruned is one nobody has read.
	dir := writeCorpus(t, adt("C1", "F"), adt("C2", "M"))

	text, err := runContract(t, "promote", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "delete most of it") {
		t.Errorf("the generated file does not say it is a starting point:\n%s", text)
	}
	if !strings.Contains(text, "why:") {
		t.Errorf("the generated expectations carry no reasons, so nobody can prune them sensibly:\n%s", text)
	}
}

func TestNoSubcommandExplainsTheWorkflow(t *testing.T) {
	_, err := runContract(t)
	if err == nil {
		t.Fatal("no subcommand was accepted")
	}
	if !strings.Contains(err.Error(), "promote") {
		t.Errorf("the error does not name the subcommands: %v", err)
	}
}

func TestPromotedContractsDoNotContainPatientData(t *testing.T) {
	// The failure that would matter most: a contract goes into version control, so a value set promoted from a
	// name field would put patient names in a git repository for ever.
	var msgs []string
	names := []string{"FROST^IVY", "HALE^JUNE", "OKONKWO^ADAEZE", "SVENSSON^LARS"}
	for i := 0; i < 150; i++ {
		m := adt(string(rune('A'+i%26))+string(rune('a'+i/26)), "F")
		m = strings.Replace(m, "FROST^IVY", names[i%len(names)], 1)
		msgs = append(msgs, m)
	}
	dir := writeCorpus(t, msgs...)

	text, err := runContract(t, "promote", dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, name := range []string{"OKONKWO", "SVENSSON", "ADAEZE", "LARS"} {
		if strings.Contains(text, name) {
			t.Errorf("%q was written into the contract; this file goes into version control:\n%s",
				name, text)
		}
	}
}
