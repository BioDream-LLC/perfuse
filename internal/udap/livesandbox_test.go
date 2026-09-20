package udap

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"
)

// Against the live sandbox, rather than the committed copy of it.
//
// The fixture in realmetadata_test.go is the evidence; this is the early warning. A fixture proves the implementation once, on a document
// that will never change again, and an implementation can drift away from a real server without any frozen test noticing - the SAML
// canonicaliser passed its own tests for weeks while being unable to read anything anybody else produced.
//
// Skipped when the network is unavailable, like every other test in this repository that needs software it did not start. A test that
// fails when the internet does teaches people to ignore failures.
//
// Set PERFUSE_SKIP_NETWORK to skip it deliberately.

const sandboxBase = "https://fhirlabs.net/fhir/r4"

func TestTheLiveSandboxStillVerifies(t *testing.T) {
	if os.Getenv("PERFUSE_SKIP_NETWORK") != "" {
		t.Skip("PERFUSE_SKIP_NETWORK is set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	client := &http.Client{Timeout: 15 * time.Second}

	md, err := Fetch(ctx, client, sandboxBase, "")
	if err != nil {
		// Not a failure. The sandbox is somebody else's machine and may be down, moved or retired.
		t.Skipf("could not reach the UDAP sandbox at %s: %v", sandboxBase, err)
	}

	// Anchored against the real trust community, exactly as the fixture test is. The clock is now rather than frozen, which is the point:
	// the sandbox signs a fresh document with a sixty second lifetime on every request, so this checks that a document made moments ago
	// verifies rather than one made in September.
	claims, err := VerifySignedMetadata(md, VerifyOptions{
		BaseURL: sandboxBase,
		Anchors: realAnchors(t),
	})
	if err != nil {
		t.Fatalf("a freshly signed document from the live sandbox did not verify: %v", err)
	}

	if claims.Issuer != sandboxBase {
		t.Errorf("iss is %q, want %q", claims.Issuer, sandboxBase)
	}

	// The advertised capabilities are checked too, because they are what a TEFCA exchange depends on and the sandbox could stop offering
	// them. Logged rather than failed where they are the server's choice to make.
	if !md.SupportsClientCredentials() {
		t.Log("the sandbox no longer advertises client_credentials")
	}

	if !md.SupportsB2BExtension() {
		t.Log("the sandbox no longer advertises the hl7-b2b authorization extension")
	}
}

func TestTheLiveSandboxIsRefusedWhenClaimedAsAnotherServer(t *testing.T) {
	// The negative control for the test above, against live data. Without it, that test would pass just as well against a verifier that
	// checked nothing about which server the document belongs to.
	if os.Getenv("PERFUSE_SKIP_NETWORK") != "" {
		t.Skip("PERFUSE_SKIP_NETWORK is set")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	md, err := Fetch(ctx, &http.Client{Timeout: 15 * time.Second}, sandboxBase, "")
	if err != nil {
		t.Skipf("could not reach the UDAP sandbox: %v", err)
	}

	if _, err := VerifySignedMetadata(md, VerifyOptions{
		BaseURL: "https://not-the-sandbox.example.org/fhir/r4",
		Anchors: realAnchors(t),
	}); err == nil {
		t.Fatal("a live document verified for a server it was not issued to")
	}
}
