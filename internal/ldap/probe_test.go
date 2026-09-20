package ldap

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// Probe tests that do not need a directory.
//
// The rest of this package skips without PERFUSE_LDAP_ADDR, which is the right choice for testing what a real directory does. It is the
// wrong choice for the probe, because the probe's whole purpose is reporting failures - and every failure worth reporting can be
// produced locally. A test that skips is indistinguishable from one that passes, and this suite has already been caught by that.

func TestProbeReportsAnUnreachableDirectoryAsTheFirstStage(t *testing.T) {
	cfg := &Config{
		Addr:       "127.0.0.1:1", // nothing listens here
		Insecure:   true,
		UserBaseDN: "ou=people,dc=example,dc=org",
		// Required by Validate: without a way to find groups nobody has a role, and the configuration refuses that rather than
		// allowing a directory that works and admits nobody.
		MemberOfAttribute: "memberOf",
		Roles:             map[string]string{"g": "viewer"},
		Timeout:           2 * time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	d, err := NewDirectory(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	report := d.Probe(context.Background(), "rturner")

	if report.OK {
		t.Fatal("a directory that is not there was reported as working")
	}
	if len(report.Stages) != 1 {
		t.Fatalf("got %d stages, want exactly 1: %+v", len(report.Stages), report.Stages)
	}

	// Stopping at the first failure is deliberate. Reporting "could not bind" and "found nobody" after failing to connect would be
	// three failures for one cause, and an operator reading them would not know which to act on.
	first := report.Stages[0]
	if first.OK || !strings.Contains(first.Name, "Reach") {
		t.Errorf("the first stage is %+v, want a failure about reaching the directory", first)
	}
	if first.Detail == "" {
		t.Error("the failure carries no detail, so it says only that something is wrong")
	}
}

func TestProbeWithoutAUsernameStopsAfterTheServiceAccountAndSaysSo(t *testing.T) {
	// A listener that accepts and immediately closes, so connecting succeeds and the bind does not. That is enough to prove the
	// stages are reported in order and that the probe distinguishes reaching a server from being able to use it.
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	cfg := &Config{
		Addr:         listener.Addr().String(),
		Insecure:     true,
		BindDN:       "cn=perfuse,dc=example,dc=org",
		BindPassword: "secret",
		UserBaseDN:   "ou=people,dc=example,dc=org",
		// Required by Validate: without a way to find groups nobody has a role, and the configuration refuses that rather than
		// allowing a directory that works and admits nobody.
		MemberOfAttribute: "memberOf",
		Roles:             map[string]string{"g": "viewer"},
		Timeout:           2 * time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	d, err := NewDirectory(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	report := d.Probe(context.Background(), "")

	if report.OK {
		t.Fatal("a server that closes every connection was reported as working")
	}

	// Connecting has to be reported as having succeeded, because it did. A probe that collapsed this into one failure would send
	// somebody to check firewalls when the connection was fine.
	if len(report.Stages) < 2 {
		t.Fatalf("got %d stages, want the connection and the encryption reported separately: %+v", len(report.Stages), report.Stages)
	}
	if !report.Stages[0].OK {
		t.Errorf("connecting was reported as failing to a listener that accepted: %+v", report.Stages[0])
	}
}

func TestProbeReportsAnUnencryptedConnectionAsAProblem(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			// Held open rather than closed, so the encryption stage is reached and reported.
			time.AfterFunc(3*time.Second, func() { _ = conn.Close() })
		}
	}()

	cfg := &Config{
		Addr:       listener.Addr().String(),
		Insecure:   true,
		UserBaseDN: "ou=people,dc=example,dc=org",
		// Required by Validate: without a way to find groups nobody has a role, and the configuration refuses that rather than
		// allowing a directory that works and admits nobody.
		MemberOfAttribute: "memberOf",
		Roles:             map[string]string{"g": "viewer"},
		Timeout:           2 * time.Second,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	d, err := NewDirectory(cfg, nil)
	if err != nil {
		t.Fatal(err)
	}

	report := d.Probe(context.Background(), "")

	var encryption *ProbeStage
	for i := range report.Stages {
		if strings.Contains(report.Stages[i].Name, "Encrypt") {
			encryption = &report.Stages[i]

			break
		}
	}
	if encryption == nil {
		t.Fatalf("no stage reports whether the connection is encrypted: %+v", report.Stages)
	}

	// This is the failure most likely to go unnoticed, because nothing breaks: everything works and every password crosses the
	// network in the clear. So it is reported as a problem even though it was configured deliberately.
	if encryption.OK {
		t.Error("an unencrypted connection was reported as fine")
	}
	// Both halves of the substance rather than one phrasing, so rewording the sentence does not fail this and dropping the warning
	// does. What matters is that it names passwords and says they are not protected - "not encrypted" alone reads like a technical
	// note rather than a consequence.
	if !strings.Contains(strings.ToLower(encryption.Detail), "password") {
		t.Errorf("the detail does not mention passwords, so it reads as a technical note rather than a consequence: %q", encryption.Detail)
	}
	if !strings.Contains(encryption.Detail, "not encrypted") {
		t.Errorf("the detail does not say the connection is unencrypted: %q", encryption.Detail)
	}
}

// TestProbeAgainstARealDirectory covers the stages that need a directory to exist.
//
// Skipped without one, like the rest of this package - but the stages above are covered locally, so a skip here loses the last two
// stages rather than the whole feature.
func TestProbeAgainstARealDirectory(t *testing.T) {
	d := testDirectory(t) // skips unless PERFUSE_LDAP_ADDR is set

	report := d.Probe(context.Background(), "rturner")
	if !report.OK {
		t.Fatalf("a probe against the seeded directory failed: %+v", report.Stages)
	}

	// Groups distinguish nil from empty on purpose: nil means nobody was looked up, empty means somebody was found and no groups
	// could be seen - which refuses the sign-in and is a configuration fault, not an absence of information.
	if report.Groups == nil {
		t.Error("a username was given and no groups were reported, not even an empty list")
	}
	if len(report.Groups) == 0 {
		t.Error("the seeded person belongs to groups and none were found")
	}

	found := map[string]bool{}
	for _, stage := range report.Stages {
		found[stage.Name] = stage.OK
	}
	for _, want := range []string{"Find a person", "Read their groups"} {
		if !found[want] {
			t.Errorf("stage %q did not pass: %+v", want, report.Stages)
		}
	}
}

func TestProbeReportsAPersonWhoIsNotThere(t *testing.T) {
	d := testDirectory(t)

	report := d.Probe(context.Background(), "nobody-by-that-name")
	if report.OK {
		t.Fatal("a username that matches nobody was reported as working")
	}

	// The point of the whole probe. Without it, this exact situation reaches the person as "wrong password", they retry, they lock
	// their account, and they telephone somebody - while the fault is one attribute name on this screen.
	var find *ProbeStage
	for i := range report.Stages {
		if report.Stages[i].Name == "Find a person" {
			find = &report.Stages[i]

			break
		}
	}
	if find == nil {
		t.Fatalf("no stage reports looking a person up: %+v", report.Stages)
	}
	if find.OK {
		t.Error("finding nobody was reported as success")
	}
}
