package main

import (
	"strings"
	"testing"
)

// The console's default port must agree with the scheme it serves.
//
// This is a first-run defect that a release went out with. The default was 8443 whether or
// not TLS was configured, and 8443 is an HTTPS port by convention - one that browsers now
// act on, because Firefox and Chrome both attempt HTTPS before HTTP. So a first launch
// printed http://127.0.0.1:8443, the browser upgraded the request, the server answered in
// plain HTTP, and the user was shown SSL_ERROR_RX_RECORD_TOO_LONG: a security warning, on
// first launch, for a server that was working correctly.
//
// It was reported by someone who downloaded the Windows release and followed the URL the
// program printed, which is the whole of the intended first-run path.
func TestTheDefaultPortMatchesTheScheme(t *testing.T) {
	if !strings.HasSuffix(defaultPlainAddr, ":8080") {
		t.Errorf("the plain-HTTP default is %q; a port browsers treat as HTTPS will be upgraded to "+
			"TLS and fail against a plain server", defaultPlainAddr)
	}
	if !strings.HasSuffix(defaultTLSAddr, ":8443") {
		t.Errorf("the TLS default is %q, which is not the conventional HTTPS port", defaultTLSAddr)
	}
	if defaultPlainAddr == defaultTLSAddr {
		t.Error("both defaults are the same address, so the port cannot indicate the scheme")
	}

	// Loopback, because the scheme is only half the reason these are safe. A routable
	// address without TLS is refused elsewhere, and a default that had to be refused
	// would be a default nobody could use.
	for _, a := range []string{defaultPlainAddr, defaultTLSAddr} {
		if !isLoopback(a) {
			t.Errorf("the default %q is not loopback; sessions and patient data would cross the "+
				"network unencrypted by default", a)
		}
	}
}

// The scheme printed for each default has to be the one a browser will use.
func TestSchemeMatchesEachDefault(t *testing.T) {
	if got := schemeFor(false); got != "http" {
		t.Errorf("schemeFor(false) = %q, want http", got)
	}
	if got := schemeFor(true); got != "https" {
		t.Errorf("schemeFor(true) = %q, want https", got)
	}
}

// An explicit -addr must win over either default.
//
// Without this the fix would be a different bug: anyone already running on 8443 behind a
// proxy, or on a port their firewall allows, would silently move.
func TestAnExplicitAddressIsNotOverridden(t *testing.T) {
	for _, tc := range []struct {
		name   string
		given  string
		useTLS bool
		want   string
	}{
		{"explicit address, no TLS", "0.0.0.0:9000", false, "0.0.0.0:9000"},
		{"explicit address, with TLS", "0.0.0.0:9000", true, "0.0.0.0:9000"},
		{"explicit 8443 without TLS is still honoured", "127.0.0.1:8443", false, "127.0.0.1:8443"},
		{"empty, no TLS", "", false, defaultPlainAddr},
		{"empty, with TLS", "", true, defaultTLSAddr},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// The function cmdServe calls, not a copy of its logic. A test that
			// reimplements the decision keeps passing when the decision is deleted.
			if got := addressOrDefault(tc.given, tc.useTLS); got != tc.want {
				t.Errorf("address = %q, want %q", got, tc.want)
			}
		})
	}
}
