package egress

import (
	"strings"
	"testing"
	"time"
)

// TestTheMetadataServiceIsRefused is the case this package exists for.
//
// On AWS, Azure and Google, 169.254.169.254 serves the instance's own role credentials to anything that can make an HTTP
// request from the machine. A SOAP destination with a response transformer can read a reply, so without this a channel
// could fetch those credentials and write them into a log.
func TestTheMetadataServiceIsRefused(t *testing.T) {
	var p Policy

	for _, host := range []string{
		"169.254.169.254",
		"169.254.169.254:80",
		"169.254.1.1",
		"100.100.100.200",
		"192.0.0.192",
	} {
		if err := p.Check(host); err == nil {
			t.Errorf("Check(%q) allowed a metadata or link-local address", host)
		}
	}
}

// TestTheRefusalSaysWhatTheAddressIs covers the message.
//
// Somebody hitting this is either doing something unwise or has a genuine service on that address. Both need to be told
// which it is, and how to proceed if they meant it.
func TestTheRefusalSaysWhatTheAddressIs(t *testing.T) {
	var p Policy

	err := p.Check("169.254.169.254")
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "credentials") {
		t.Errorf("the refusal should say what is at that address, got: %v", err)
	}
	if !strings.Contains(err.Error(), "-allow-metadata-egress") {
		t.Errorf("the refusal should say how to permit it deliberately, got: %v", err)
	}
}

// TestOrdinaryPrivateAddressesAreAllowed covers over-blocking.
//
// Hospital systems live on private networks. Refusing 10.0.0.0/8 or 192.168.0.0/16 would refuse nearly every real
// deployment, and a control that blocks the normal case gets switched off entirely.
func TestOrdinaryPrivateAddressesAreAllowed(t *testing.T) {
	var p Policy

	for _, host := range []string{
		"10.1.2.3",
		"192.168.1.50",
		"172.16.0.9",
		"127.0.0.1",
		"8.8.8.8",
		// A hostname is deliberately not used here. It would need a DNS lookup, and an unresolvable one costs
		// this test five seconds of timeout for no extra coverage - the unresolvable case has its own test.
	} {
		if err := p.Check(host); err != nil {
			t.Errorf("Check(%q) refused an ordinary address: %v", host, err)
		}
	}
}

// TestAllowMetadataPermitsItDeliberately covers the escape hatch.
func TestAllowMetadataPermitsItDeliberately(t *testing.T) {
	p := Policy{AllowMetadata: true}

	if err := p.Check("169.254.169.254"); err != nil {
		t.Errorf("allow_metadata did not permit the address: %v", err)
	}
}

// TestAPositiveAllowListRefusesEverythingElse covers the stricter mode.
func TestAPositiveAllowListRefusesEverythingElse(t *testing.T) {
	p := Policy{Allow: []string{"10.0.0.0/8", "203.0.113.5"}}

	for _, host := range []string{"10.1.2.3", "203.0.113.5"} {
		if err := p.Check(host); err != nil {
			t.Errorf("Check(%q) refused a permitted address: %v", host, err)
		}
	}

	for _, host := range []string{"192.168.1.1", "8.8.8.8"} {
		if err := p.Check(host); err == nil {
			t.Errorf("Check(%q) allowed an address outside the allow list", host)
		}
	}
}

// TestAnAllowListStillRefusesMetadata covers precedence.
//
// A metadata address inside an allowed CIDR must still be refused, or an allow list of 169.254.0.0/16 - or of 0.0.0.0/0,
// which somebody will write - would quietly re-open it.
func TestAnAllowListStillRefusesMetadata(t *testing.T) {
	p := Policy{Allow: []string{"0.0.0.0/0"}}

	if err := p.Check("169.254.169.254"); err == nil {
		t.Error("an allow list of everything re-opened the metadata address")
	}
}

// TestAnUnresolvableHostIsNotRefused covers a name that does not resolve yet.
//
// A system that is down, or a DNS entry not yet created, must not make a channel invalid - otherwise a channel's validity
// depends on the state of the network at the moment somebody looked.
func TestAnUnresolvableHostIsNotRefused(t *testing.T) {
	var p Policy

	if err := p.Check("this-host-does-not-exist.invalid"); err != nil {
		t.Errorf("an unresolvable name was refused: %v", err)
	}
}

// TestAnEmptyHostIsRefused covers the degenerate case.
func TestAnEmptyHostIsRefused(t *testing.T) {
	var p Policy

	if err := p.Check(""); err == nil {
		t.Error("an empty host was allowed")
	}
	if err := p.Check("   "); err == nil {
		t.Error("a blank host was allowed")
	}
}

// TestTheBlocklistIsNotEmpty guards the init function.
//
// Every entry is parsed at startup and a malformed constant panics. If that ever became a silent skip, this package would
// allow everything while appearing to be a control - which is worse than not having it.
func TestTheBlocklistIsNotEmpty(t *testing.T) {
	if len(blockedRanges) == 0 {
		t.Fatal("the blocklist is empty")
	}
	for _, blocked := range blockedRanges {
		if blocked.net == nil {
			t.Errorf("%s was not parsed, so it blocks nothing", blocked.cidr)
		}
		if blocked.reason == "" {
			t.Errorf("%s has no reason, and a refusal without one cannot be acted on", blocked.cidr)
		}
	}
	if len(BlockedDescriptions()) != len(blockedRanges) {
		t.Error("BlockedDescriptions does not describe every blocked range")
	}
}

// TestCheckDoesNotTouchTheNetwork covers the tradeoff this split exists for.
//
// Check runs during channel validation, so it must not resolve names: perfuse check would take seconds per destination,
// and a channel's validity would depend on whether DNS answered at the moment somebody looked. A system being unreachable
// is not the same as a channel being wrong.
//
// Measured rather than asserted structurally, because the point is the elapsed time. An earlier version resolved in Check
// and made the config package's tests take eleven seconds instead of under one.
func TestCheckDoesNotTouchTheNetwork(t *testing.T) {
	var p Policy

	started := time.Now()
	for i := 0; i < 20; i++ {
		// Names chosen to be unresolvable, which is the slow case: a lookup that fails waits for a timeout.
		if err := p.Check("host-" + itoa(i) + ".invalid"); err != nil {
			t.Errorf("Check refused a hostname: %v", err)
		}
	}
	elapsed := time.Since(started)

	// Generous, because this has to hold on a loaded machine. A single DNS timeout is seconds, so twenty lookups
	// could not finish inside this.
	if elapsed > 500*time.Millisecond {
		t.Errorf("twenty checks took %s, which means names are being resolved", elapsed)
	}
}

// TestCheckResolvingDoesCatchAName covers the other half.
//
// A hostname pointing at a metadata address is caught when a channel starts, which is a moment the network is necessarily
// involved anyway. localhost is used because it is the one name guaranteed to resolve without a network.
func TestCheckResolvingDoesCatchAName(t *testing.T) {
	p := Policy{Allow: []string{"203.0.113.0/24"}}

	// localhost resolves to a loopback address, which is not in the allow list, so a resolving check refuses it and a
	// literal-only check cannot.
	if err := p.Check("localhost"); err != nil {
		t.Errorf("the non-resolving check refused a name, which it cannot judge: %v", err)
	}
	if err := p.CheckResolving("localhost"); err == nil {
		t.Error("the resolving check allowed a name outside the allow list")
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
