package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The profile command's value is that its output gets acted on, so these tests are mostly about whether the
// output can be trusted and whether it says the thing somebody needs to do.

func writeCorpus(t *testing.T, messages ...string) string {
	t.Helper()

	dir := t.TempDir()
	for i, m := range messages {
		name := filepath.Join(dir, "m"+string(rune('a'+i))+".hl7")
		if err := os.WriteFile(name, []byte(m), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func adt(control, sex string) string {
	return "MSH|^~\\&|EPIC|SITEA|LAB|SITEB|20260820120000||ADT^A08^ADT_A01|" + control + "|P|2.5.1\r" +
		"EVN|A08|20260820120000\r" +
		"PID|1||MRN" + control + "^^^SITEA^MR||FROST^IVY||19910228|" + sex + "\r"
}

func runProfile(t *testing.T, args ...string) (string, error) {
	t.Helper()
	var out bytes.Buffer
	err := cmdProfile(args, &out, &out)
	return out.String(), err
}

func TestProfileReportsWhatIsPopulated(t *testing.T) {
	dir := writeCorpus(t, adt("C1", "F"), adt("C2", "M"), adt("C3", "F"))

	out, err := runProfile(t, dir)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "3 message(s) read") {
		t.Errorf("the count is missing:\n%s", out)
	}
	if !strings.Contains(out, "PID-5") {
		t.Errorf("a populated field is missing:\n%s", out)
	}
	// The value set of a coded field is the one place values are reported, and it is the most useful part.
	if !strings.Contains(out, "F (2)") || !strings.Contains(out, "M (1)") {
		t.Errorf("the sex code counts are missing:\n%s", out)
	}
}

func TestProfileNeverReportsAnIdentifier(t *testing.T) {
	// A profile of real traffic must not become a way to read patient data. Names and record numbers are
	// exactly what must not appear, and the corpus above contains both.
	dir := writeCorpus(t, adt("C1", "F"), adt("C2", "M"))

	out, err := runProfile(t, dir)
	if err != nil {
		t.Fatal(err)
	}

	for _, secret := range []string{"FROST", "IVY", "MRNC1", "19910228"} {
		if strings.Contains(out, secret) {
			t.Errorf("%q appeared in the profile; values must not be reported: \n%s", secret, out)
		}
	}
}

func TestAnEmptyCorpusIsAnErrorNotAnEmptyProfile(t *testing.T) {
	// "0 messages, no fields populated" is technically true and would be acted on.
	dir := t.TempDir()

	_, err := runProfile(t, dir)
	if err == nil {
		t.Fatal("an empty directory produced a profile")
	}
	if !strings.Contains(err.Error(), "no messages") {
		t.Errorf("the error does not say what was wrong: %v", err)
	}
}

func TestOneUnreadableFileDoesNotCostTheCorpus(t *testing.T) {
	// A real directory of captured traffic contains a lock file or a half-written message, and refusing
	// everything over one of them would make the tool useless on exactly the data it is for.
	dir := writeCorpus(t, adt("C1", "F"), adt("C2", "M"))
	if err := os.WriteFile(filepath.Join(dir, "lockfile"), []byte("not hl7 at all"), 0o600); err != nil {
		t.Fatal(err)
	}

	out, err := runProfile(t, dir)
	if err != nil {
		t.Fatalf("one unreadable file cost the whole corpus: %v", err)
	}
	// It is counted rather than hidden, because a corpus with many unreadable messages is not one to draw
	// conclusions from and the reader has to know before reading the numbers.
	if !strings.Contains(out, "could not be parsed") {
		t.Errorf("the unreadable file was not reported:\n%s", out)
	}
}

func TestSaveAndCompareFindsAFieldThatStoppedBeingSent(t *testing.T) {
	// The change nobody can currently detect: the messages are still valid HL7, so nothing in a normal engine
	// notices until a receiver falls over.
	before := writeCorpus(t, adt("C1", "F"), adt("C2", "M"), adt("C3", "F"), adt("C4", "M"))

	saved := filepath.Join(t.TempDir(), "before.json")
	if _, err := runProfile(t, "-save", saved, before); err != nil {
		t.Fatal(err)
	}

	// The same feed, with the sex field no longer populated.
	withoutSex := writeCorpus(t,
		strings.Replace(adt("C5", "F"), "|19910228|F", "|19910228|", 1),
		strings.Replace(adt("C6", "M"), "|19910228|M", "|19910228|", 1),
		strings.Replace(adt("C7", "F"), "|19910228|F", "|19910228|", 1),
		strings.Replace(adt("C8", "M"), "|19910228|M", "|19910228|", 1),
	)

	out, err := runProfile(t, "-against", saved, withoutSex)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "PID-8") {
		t.Errorf("the field that stopped being sent was not reported:\n%s", out)
	}
	// Breaking changes are separated rather than merely sorted. "3 changes" where one will stop a feed and
	// two are cosmetic is a list somebody skims.
	if !strings.Contains(out, "break something downstream") {
		t.Errorf("the change was not classified as breaking:\n%s", out)
	}
}

func TestAnIdenticalFeedComparesAsUnchanged(t *testing.T) {
	// Just as important as detecting change: a comparison that reports noise every week trains people to
	// ignore it, and then the one real change is invisible.
	dir := writeCorpus(t, adt("C1", "F"), adt("C2", "M"), adt("C3", "F"))

	saved := filepath.Join(t.TempDir(), "p.json")
	if _, err := runProfile(t, "-save", saved, dir); err != nil {
		t.Fatal(err)
	}

	out, err := runProfile(t, "-against", saved, dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "Nothing changed") {
		t.Errorf("an identical feed reported changes:\n%s", out)
	}
}

func TestStrictExitsNonZeroOnChange(t *testing.T) {
	// So this can run in a scheduled job and tell somebody without being read.
	before := writeCorpus(t, adt("C1", "F"), adt("C2", "F"))

	saved := filepath.Join(t.TempDir(), "p.json")
	if _, err := runProfile(t, "-save", saved, before); err != nil {
		t.Fatal(err)
	}

	changed := writeCorpus(t,
		strings.Replace(adt("C3", "F"), "ADT^A08^ADT_A01", "ADT^A04^ADT_A01", 1),
		strings.Replace(adt("C4", "F"), "ADT^A08^ADT_A01", "ADT^A04^ADT_A01", 1),
	)

	if _, err := runProfile(t, "-against", saved, "-strict", changed); err == nil {
		t.Fatal("-strict exited zero despite a change")
	}

	// And zero when nothing changed, or it is useless in a job.
	if _, err := runProfile(t, "-against", saved, "-strict", before); err != nil {
		t.Errorf("-strict failed on an unchanged feed: %v", err)
	}
}

func TestAProfileFromAnIncompatibleVersionIsRefused(t *testing.T) {
	// Silently comparing against half a file would report changes that are really a format difference, and
	// send somebody looking for a problem in the feed that is not there.
	path := filepath.Join(t.TempDir(), "old.json")
	if err := os.WriteFile(path,
		[]byte(`{"messages":10,"somethingFromTheFuture":true}`), 0o600); err != nil {
		t.Fatal(err)
	}

	dir := writeCorpus(t, adt("C1", "F"))
	_, err := runProfile(t, "-against", path, dir)
	if err == nil {
		t.Fatal("a profile with unknown fields was accepted")
	}
	if !strings.Contains(err.Error(), "this version can read") {
		t.Errorf("the error does not explain the problem: %v", err)
	}
}

func TestZSegmentsAreFlaggedAsNotStandard(t *testing.T) {
	// These are the ones a specification never mentions and an integration always has to handle, so they are
	// the single most useful thing a profile can point at.
	msg := adt("C1", "F") + "ZPD|1|local extension\r"
	dir := writeCorpus(t, msg, msg)

	out, err := runProfile(t, dir)
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(out, "ZPD") {
		t.Errorf("the Z-segment was not reported:\n%s", out)
	}
	if !strings.Contains(out, "not in the standard") {
		t.Errorf("the Z-segment was not flagged as non-standard:\n%s", out)
	}
}

func TestJSONOutputIsValid(t *testing.T) {
	dir := writeCorpus(t, adt("C1", "F"))

	out, err := runProfile(t, "-json", dir)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Errorf("the JSON output does not look like JSON:\n%s", out)
	}
	if !strings.Contains(out, `"messages"`) {
		t.Errorf("the JSON output has no message count:\n%s", out)
	}
}

func TestFileOrderDoesNotChangeTheReport(t *testing.T) {
	// A profile is a document people diff between runs, and directory order is not stable across
	// filesystems.
	dir := writeCorpus(t, adt("C1", "F"), adt("C2", "M"), adt("C3", "F"))

	first, err := runProfile(t, dir)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		again, err := runProfile(t, dir)
		if err != nil {
			t.Fatal(err)
		}
		if again != first {
			t.Fatal("the report changed between runs on the same corpus")
		}
	}
}
