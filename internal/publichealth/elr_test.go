package publichealth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
)

func elrOptions() ELROptions {
	return ELROptions{
		Now:                  time.Date(2026, 10, 6, 9, 0, 0, 0, time.FixedZone("", -5*3600)),
		SendingApplication:   HD{"Perfuse", "2.16.840.1.113883.19.5.9", "ISO"},
		SendingFacility:      HD{"Springfield Lab", "2.16.840.1.113883.19.5", "ISO"},
		ReceivingApplication: HD{"ELR", "2.16.840.1.113883.19.6.1", "ISO"},
		ReceivingFacility:    HD{"State DOH", "2.16.840.1.113883.19.6", "ISO"},
		Software: Software{Vendor: "BioDream LLC", Version: "1.0", Name: "Perfuse", BinaryID: "perfuse",
			Installed: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)},
		OrderingFacility: Facility{Name: "Springfield Clinic", Phone: "+1 217 555 0100", Line: "1 Main St", City: "Springfield",
			State: "IL", PostalCode: "62701"},
		PerformingLab:     Facility{Name: "Springfield Lab", Line: "2 Lab Rd", City: "Springfield", State: "IL", PostalCode: "62701"},
		PerformingLabCLIA: "14D0000000",
		PlacerAuthority:   HD{"SPRINGFIELD-EHR", "2.16.840.1.113883.19.5.1", "ISO"},
		FillerAuthority:   HD{"SPRINGFIELD-LAB", "2.16.840.1.113883.19.5.2", "ISO"},
	}
}

func buildELRFrom(t *testing.T, path string, opts ELROptions) (*ELR, error) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	m, err := hl7.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return BuildELR(m, BuiltinTriggers(), opts)
}

func elrSegments(r *ELR, name string) []string {
	var out []string
	for _, s := range strings.Split(string(r.Message), "\r") {
		if strings.HasPrefix(s, name+"|") {
			out = append(out, s)
		}
	}
	return out
}

func field(seg string, n int) string {
	f := strings.Split(seg, "|")
	if strings.HasPrefix(seg, "MSH|") {
		n-- // MSH-1 is the separator itself
	}
	if n < len(f) {
		return f[n]
	}
	return ""
}

func TestELRSendsOnlyTheReportableOrder(t *testing.T) {
	r, err := buildELRFrom(t, "testdata/oru-elr-source.hl7", elrOptions())
	if err != nil {
		t.Fatal(err)
	}
	if r.Dropped != 1 || len(r.Triggers) != 1 || r.Triggers[0].Code != "94500-6" || r.Triggers[0].Condition != "COVID-19" {
		t.Fatalf("dropped %d, triggers %+v; want the CBC left out and the SARS-CoV-2 test kept", r.Dropped, r.Triggers)
	}
	msh := elrSegments(r, "MSH")[0]
	if field(msh, 21) != elrProfile || field(msh, 12) != "2.5.1" || field(msh, 9) != "ORU^R01^ORU_R01" {
		t.Errorf("MSH does not declare ELR 2.5.1: %s", msh)
	}
	if field(msh, 6) != "State DOH^2.16.840.1.113883.19.6^ISO" || field(msh, 7) != "20261006090000-0500" {
		t.Errorf("MSH receiver or time: %s", msh)
	}
	sft := elrSegments(r, "SFT")
	if len(sft) != 2 || !strings.HasPrefix(sft[0], "SFT|Example LIS Vendor") || !strings.HasPrefix(sft[1], "SFT|BioDream LLC^L|1.0|Perfuse") {
		t.Errorf("want the lab's SFT and then Perfuse's, got %q", sft)
	}
	obr := elrSegments(r, "OBR")
	if len(obr) != 1 || field(obr[0], 1) != "1" || !strings.HasPrefix(field(obr[0], 4), "94500-6^") {
		t.Fatalf("OBR: %q", obr)
	}
	if field(obr[0], 3) != "LAB81^SPRINGFIELD-LAB^2.16.840.1.113883.19.5.2^ISO" {
		t.Errorf("OBR-3 should carry the filler's assigning authority: %s", field(obr[0], 3))
	}
	orc := elrSegments(r, "ORC")[0]
	want := "1234567893^Clinician^Morgan^^^^^^NPI&2.16.840.1.113883.4.6&ISO^^^^NPI"
	if field(orc, 12) != want || field(obr[0], 16) != want {
		t.Errorf("ORC-12 %q and OBR-16 %q should both be %q", field(orc, 12), field(obr[0], 16), want)
	}
	if field(orc, 21) != "Springfield Clinic^L" || field(orc, 23) != "^WPN^PH^^1^217^5550100" {
		t.Errorf("ordering facility: %s", orc)
	}
	obx := elrSegments(r, "OBX")
	if len(obx) != 1 || field(obx[0], 2) != "CWE" || !strings.HasPrefix(field(obx[0], 23), "Springfield Lab^^^^^CLIA&2.16.840.1.113883.4.7&ISO^XX^^^14D0000000") {
		t.Errorf("OBX: %q", obx)
	}
	if nte := elrSegments(r, "NTE"); len(nte) != 1 || !strings.Contains(nte[0], "called to the ordering clinic") {
		t.Errorf("the result's note was not carried: %q", nte)
	}
	spm := elrSegments(r, "SPM")
	if len(spm) != 1 || !strings.HasPrefix(field(spm[0], 4), "258500001^") || field(spm[0], 2) != "^SP81&SPRINGFIELD-LAB&2.16.840.1.113883.19.5.2&ISO" {
		t.Errorf("SPM: %q", spm)
	}
	pid := elrSegments(r, "PID")[0]
	if field(pid, 10) != "2106-3^White^HL70005^2106-3^White^CDCREC" || field(pid, 22) != "N^Not Hispanic or Latino^HL70189^2186-5^Not Hispanic or Latino^CDCREC" {
		t.Errorf("race and ethnicity should be in ELR's tables with the sender's codes kept: %s / %s", field(pid, 10), field(pid, 22))
	}
	if strings.Contains(string(r.Message), "\n") || !strings.HasSuffix(string(r.Message), "\r") {
		t.Error("segments must end in carriage returns only")
	}
}

func TestELRNamesWhatTheLabDidNotSend(t *testing.T) {
	r, err := buildELRFrom(t, "testdata/oru-sarscov2.hl7", elrOptions())
	if err != nil {
		t.Fatal(err)
	}
	notes := strings.Join(r.Notes, "\n")
	for _, want := range []string{"no SPM", "SPM-4 is empty", "SPM-18 is empty"} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes do not say %q:\n%s", want, notes)
		}
	}
	spm := elrSegments(r, "SPM")
	if len(spm) != 1 || field(spm[0], 17) != "20261003150000-0500" || field(spm[0], 4) != "" {
		t.Errorf("the specimen should have the collection time and no invented type: %q", spm)
	}
}

func TestELRNeedsToKnowWhoReceivesIt(t *testing.T) {
	opts := elrOptions()
	opts.ReceivingFacility = HD{Namespace: "State DOH"}
	if _, err := buildELRFrom(t, "testdata/oru-elr-source.hl7", opts); err == nil || !strings.Contains(err.Error(), "receiving facility") {
		t.Fatalf("err = %v, want the receiving facility named", err)
	}
}

func TestELROfNothingReportableSaysSo(t *testing.T) {
	r, err := buildELRFrom(t, "../v2fhir/testdata/uscdi/oru-r01.hl7", elrOptions())
	if !errors.Is(err, ErrNothingReportable) || r == nil || r.Dropped != 1 {
		t.Fatalf("err = %v, report %+v", err, r)
	}
}

func TestELRReencodesTheSendersSeparators(t *testing.T) {
	src := strings.Join([]string{
		`MSH|#~\&|LIS|LAB|EHR|FAC|20261004101500-0500||ORU#R01|X1|P|2.5.1`,
		`PID|1||7###SPRINGFIELD&2.16.840.1.113883.19.5&ISO#MR||O^Brien A#Avery||19800214|F`,
		`OBR|1|O1|F1|94500-6#SARS-CoV-2 RNA#LN|||20261003150000-0500|||||||||1234567893#Clinician#Morgan||||||20261004100000-0500|||F`,
		`OBX|1|ST|94500-6#SARS-CoV-2 RNA#LN||Detected||||||F`,
	}, "\r") + "\r"
	m, err := hl7.Parse([]byte(src))
	if err != nil {
		t.Fatal(err)
	}
	r, err := BuildELR(m, BuiltinTriggers(), elrOptions())
	if err != nil {
		t.Fatal(err)
	}
	pid := elrSegments(r, "PID")[0]
	if field(pid, 5) != `O\S\Brien A^Avery` || field(pid, 3) != "7^^^SPRINGFIELD&2.16.840.1.113883.19.5&ISO^MR" {
		t.Errorf("PID re-encoded wrongly: %s", pid)
	}
}

// TestELRForTheNISTValidator writes the reports for the NIST HL7 v2 validator when ELR_OUT names a directory (see
// docs/verification.md).
func TestELRForTheNISTValidator(t *testing.T) {
	dir := os.Getenv("ELR_OUT")
	if dir == "" {
		t.Skip("set ELR_OUT to write ELR messages for the NIST validator")
	}
	for _, f := range []string{"testdata/oru-sarscov2.hl7", "testdata/oru-elr-source.hl7"} {
		r, err := buildELRFrom(t, f, elrOptions())
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(f)), r.Message, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}
