package dicom

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestTheCallingAETitleAllowlistIsEnforced checks the access control DICOM actually has.
//
// Verified against DCMTK's storescu rather than our own client, because what matters is that a real sender understands
// the rejection. A rejection a sender cannot parse looks like a network fault, and the operator at the far end goes
// looking at cables.
//
// The called AE title was already checked and proves nothing about who is calling: it is the name the caller dials.
// Anybody who can reach the port can send it correctly, and finding it out takes one attempt.
func TestTheCallingAETitleAllowlistIsEnforced(t *testing.T) {
	if _, err := exec.LookPath("storescu"); err != nil {
		t.Skip("storescu is not installed")
	}

	stored := 0

	srv := &Server{
		AETitle: "PERFUSE",
		// Only this one title is permitted.
		AllowedCallingAE: []string{"ALLOWED_SCU"},
		Handler: func(_ *DataSet, _ []byte) error {
			stored++
			return nil
		},
		IdleTimeout: 15 * time.Second,
	}
	if err := srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	host, port, err := splitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join("testdata", explicitFixture)

	// A permitted caller succeeds.
	out, err := exec.Command("storescu",
		"-aec", "PERFUSE", "-aet", "ALLOWED_SCU", host, port, fixture).CombinedOutput()
	if err != nil {
		t.Fatalf("a permitted calling AE title was rejected: %v\n%s", err, out)
	}
	if stored != 1 {
		t.Fatalf("the permitted caller stored %d objects, want 1", stored)
	}

	// A caller not on the list is rejected, and nothing is stored.
	out, err = exec.Command("storescu",
		"-aec", "PERFUSE", "-aet", "NOT_ALLOWED", host, port, fixture).CombinedOutput()
	if err == nil {
		t.Fatalf("a calling AE title that is not on the allowlist was accepted\n%s", out)
	}
	if stored != 1 {
		t.Errorf("a rejected caller stored an object anyway; %d stored in total", stored)
	}

	// The rejection has to be intelligible to the sender, which is the difference between an operator fixing a name
	// and an operator opening a ticket about the network.
	lower := strings.ToLower(string(out))
	if !strings.Contains(lower, "calling") && !strings.Contains(lower, "reject") &&
		!strings.Contains(lower, "associat") {
		t.Errorf("storescu did not report a recognisable rejection; it saw:\n%s", out)
	}
}

// TestAnEmptyAllowlistAcceptsAnybody covers the deliberate default.
//
// Refusing by default would break every first installation, because nobody knows the modality titles until something has
// connected - and the fix people reach for when a check blocks everything is switching it off wholesale. So the default
// accepts and warns, and the warning is emitted at every channel start rather than once at load.
func TestAnEmptyAllowlistAcceptsAnybody(t *testing.T) {
	if _, err := exec.LookPath("storescu"); err != nil {
		t.Skip("storescu is not installed")
	}

	stored := 0

	srv := &Server{
		AETitle: "PERFUSE",
		Handler: func(_ *DataSet, _ []byte) error {
			stored++
			return nil
		},
		IdleTimeout: 15 * time.Second,
	}
	if err := srv.Listen("127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	host, port, err := splitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}

	out, err := exec.Command("storescu", "-aec", "PERFUSE", "-aet", "ANY_TITLE",
		host, port, filepath.Join("testdata", explicitFixture)).CombinedOutput()
	if err != nil {
		t.Fatalf("an empty allowlist rejected a caller: %v\n%s", err, out)
	}
	if stored != 1 {
		t.Errorf("stored %d objects, want 1", stored)
	}
}

// TestTheAllowlistIgnoresCaseAndPadding covers the wire format.
//
// Titles are padded to sixteen characters on the wire and vendors disagree about capitalisation. A comparison that failed
// on either would look like an allowlist that never matches, and the fix people would reach for is emptying it - which is
// worse than never having had one.
func TestTheAllowlistIgnoresCaseAndPadding(t *testing.T) {
	srv := &Server{AllowedCallingAE: []string{"modality_one"}}

	for _, calling := range []string{
		"modality_one",
		"MODALITY_ONE",
		"Modality_One",
		"  MODALITY_ONE  ",
		"MODALITY_ONE    ",
	} {
		if !srv.callerAllowed(calling) {
			t.Errorf("%q was refused; padding and case must not matter", calling)
		}
	}

	for _, calling := range []string{"modality_two", "", "MODALITY_ON", "MODALITY_ONEX"} {
		if srv.callerAllowed(calling) {
			t.Errorf("%q was allowed and should not be", calling)
		}
	}
}
