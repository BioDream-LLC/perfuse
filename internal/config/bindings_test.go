package config

import (
	"strings"
	"testing"
)

func mllpChannel(name, listen string) *Channel {
	return &Channel{
		Name:   name,
		Source: Source{Type: SourceMLLP, Listen: listen},
	}
}

func httpChannel(name, listen string) *Channel {
	return &Channel{
		Name:   name,
		Source: Source{Type: SourceHTTP, HTTP: &HTTPSource{Listen: listen}},
	}
}

func TestNoConflictIsNoError(t *testing.T) {
	b := NewBindings()
	b.Add("", mllpChannel("a", "127.0.0.1:2575"))
	b.Add("", mllpChannel("b", "127.0.0.1:2576"))
	if err := b.Check(); err != nil {
		t.Errorf("Check() = %v, want nil", err)
	}
}

func TestIdenticalAddressesConflict(t *testing.T) {
	b := NewBindings()
	b.Add("", mllpChannel("adt", "127.0.0.1:2575"))
	b.Add("", mllpChannel("orm", "127.0.0.1:2575"))

	err := b.Check()
	if err == nil {
		t.Fatal("two channels on the same address were accepted")
	}
	// Both names, because the operator has to know which two to look at.
	for _, want := range []string{"adt", "orm", "2575"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q, got: %v", want, err)
		}
	}
}

func TestWildcardConflictsWithASpecificAddress(t *testing.T) {
	// The case that makes this check worth having. ":2575" takes the port on every
	// interface, so it collides with 127.0.0.1:2575 - and comparing the strings
	// literally would miss it entirely and give false confidence.
	for _, wildcard := range []string{":2575", "0.0.0.0:2575", "[::]:2575", "*:2575"} {
		b := NewBindings()
		b.Add("", mllpChannel("wide", wildcard))
		b.Add("", mllpChannel("narrow", "127.0.0.1:2575"))

		if err := b.Check(); err == nil {
			t.Errorf("%q and 127.0.0.1:2575 were accepted together", wildcard)
		}
	}
}

func TestLocalhostAndLoopbackAreTheSameAddress(t *testing.T) {
	b := NewBindings()
	b.Add("", mllpChannel("a", "localhost:2575"))
	b.Add("", mllpChannel("b", "127.0.0.1:2575"))

	if err := b.Check(); err == nil {
		t.Error("localhost:2575 and 127.0.0.1:2575 were accepted together")
	}
}

func TestDifferentInterfacesOnTheSamePortAreAllowed(t *testing.T) {
	// Legitimate and real: a host with two NICs serving two networks on the same
	// port. A check that refused this would stop a working deployment.
	b := NewBindings()
	b.Add("", mllpChannel("net-a", "10.0.0.1:2575"))
	b.Add("", mllpChannel("net-b", "10.0.0.2:2575"))

	if err := b.Check(); err != nil {
		t.Errorf("two specific addresses on one port were refused: %v", err)
	}
}

func TestSamePortDifferentProtocolStillConflicts(t *testing.T) {
	// An MLLP listener and an HTTP listener contend for the port just the same. The
	// operating system does not care what protocol we intend to speak.
	b := NewBindings()
	b.Add("", mllpChannel("mllp", "127.0.0.1:8080"))
	b.Add("", httpChannel("http", "127.0.0.1:8080"))

	err := b.Check()
	if err == nil {
		t.Fatal("an MLLP and an HTTP listener on one port were accepted")
	}
	if !strings.Contains(err.Error(), "MLLP") || !strings.Contains(err.Error(), "HTTP") {
		t.Errorf("the error should name both listener kinds, got: %v", err)
	}
}

func TestConflictAcrossTenantsNamesBothTenants(t *testing.T) {
	// The reason this matters more in multi-tenant operation: the two channels belong
	// to different organisations and neither operator can see the other's
	// configuration, so the message has to identify both.
	b := NewBindings()
	b.Add("acme", mllpChannel("adt", "127.0.0.1:2575"))
	b.Add("beta", mllpChannel("adt", "127.0.0.1:2575"))

	err := b.Check()
	if err == nil {
		t.Fatal("two tenants were allowed the same port")
	}
	for _, want := range []string{"acme", "beta"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should name tenant %q, got: %v", want, err)
		}
	}
}

func TestEveryConflictIsReportedNotJustTheFirst(t *testing.T) {
	// An operator fixing a fifty-channel configuration should not have to restart
	// fifty times to find fifty problems.
	b := NewBindings()
	b.Add("", mllpChannel("a1", "127.0.0.1:2575"))
	b.Add("", mllpChannel("a2", "127.0.0.1:2575"))
	b.Add("", mllpChannel("b1", "127.0.0.1:2576"))
	b.Add("", mllpChannel("b2", "127.0.0.1:2576"))

	err := b.Check()
	if err == nil {
		t.Fatal("no conflict reported")
	}
	for _, want := range []string{"a1", "a2", "b1", "b2"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should mention %q, got: %v", want, err)
		}
	}
}

func TestPollingSourcesClaimNothing(t *testing.T) {
	// A database, SFTP or file source polls outward and binds no port. Counting them
	// would invent conflicts that do not exist.
	b := NewBindings()
	b.Add("", &Channel{Name: "db-poll", Source: Source{Type: SourceDatabase}})
	b.Add("", &Channel{Name: "sftp-poll", Source: Source{Type: SourceSFTP}})

	if err := b.Check(); err != nil {
		t.Errorf("polling sources produced a conflict: %v", err)
	}
	if got := b.Claims(); len(got) != 0 {
		t.Errorf("polling sources recorded %d claims, want 0", len(got))
	}
}

func TestAnHTTPSourceWithNoBlockClaimsNothing(t *testing.T) {
	// Guard against a nil dereference on a partially written configuration. Validation
	// elsewhere reports the missing block properly; this must not panic first.
	b := NewBindings()
	b.Add("", &Channel{Name: "half-written", Source: Source{Type: SourceHTTP}})
	if err := b.Check(); err != nil {
		t.Errorf("Check() = %v", err)
	}
}

func TestClaimsAreSortedForDisplay(t *testing.T) {
	b := NewBindings()
	b.Add("beta", mllpChannel("zulu", "127.0.0.1:1"))
	b.Add("acme", mllpChannel("mike", "127.0.0.1:2"))
	b.Add("acme", mllpChannel("alpha", "127.0.0.1:3"))

	got := b.Claims()
	if len(got) != 3 {
		t.Fatalf("got %d claims, want 3", len(got))
	}
	want := []string{"acme/alpha", "acme/mike", "beta/zulu"}
	for i, c := range got {
		if c.Tenant+"/"+c.Channel != want[i] {
			t.Errorf("claim %d = %s/%s, want %s", i, c.Tenant, c.Channel, want[i])
		}
	}
}

func TestClaimLabelMentionsTheTenantOnlyWhenThereIsOne(t *testing.T) {
	// A single-tenant operator has never heard the word tenant and should not meet it
	// in an error message.
	single := Claim{Channel: "adt"}
	if strings.Contains(single.Label(), "tenant") {
		t.Errorf("a single-tenant label mentions tenancy: %q", single.Label())
	}
	multi := Claim{Tenant: "acme", Channel: "adt"}
	if !strings.Contains(multi.Label(), "acme") {
		t.Errorf("a multi-tenant label omits the tenant: %q", multi.Label())
	}
}

func TestTheErrorExplainsWhatWouldHappen(t *testing.T) {
	// "Address already in use" at bind time is experienced by the sending hospital as
	// an unexplained outage on our side. The load-time message should say so.
	b := NewBindings()
	b.Add("", mllpChannel("a", "127.0.0.1:2575"))
	b.Add("", mllpChannel("b", "127.0.0.1:2575"))

	err := b.Check()
	if err == nil {
		t.Fatal("no error")
	}
	for _, want := range []string{"address already in use", "connection refused", "own port"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error should explain %q, got: %v", want, err)
		}
	}
}

func TestUnparseableAddressesStillCollide(t *testing.T) {
	// Validation elsewhere reports a malformed address properly. Here, two identical
	// malformed values should still be seen as the same claim rather than silently
	// treated as different.
	b := NewBindings()
	b.Add("", mllpChannel("a", "not-an-address"))
	b.Add("", mllpChannel("b", "not-an-address"))

	if err := b.Check(); err == nil {
		t.Error("two identical unparseable addresses were accepted")
	}
}
