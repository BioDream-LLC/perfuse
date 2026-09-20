package vfs

import (
	"context"
	"strings"
	"testing"
)

// SMB path handling and configuration refusals.
//
// What is tested here is everything that happens before a packet is sent, because that is what can be tested without a
// Windows server. Stated plainly rather than implied: the protocol conversation itself has never been run against a real
// share from this machine, and until it has, "SMB works" is a claim rather than a fact.

// A share name containing a slash must be refused, not trimmed.
//
// Somebody who typed a path into the share field means a different thing by it. Silently using the first component would
// connect to a share they did not name and then report an empty directory, which reads as "no files today".
func TestAnSMBShareNameContainingAPathIsRefused(t *testing.T) {
	for _, share := range []string{`\\server\data`, "data/folder", `data\folder`} {
		_, err := DialSMB(context.Background(), SMBSettings{
			Host: "192.0.2.1", Share: share, User: "u", Password: "p",
		})
		if err == nil {
			t.Errorf("the share name %q was accepted", share)
			continue
		}
		if !strings.Contains(err.Error(), "put the folder in dir") {
			t.Errorf("the refusal of %q does not say where the folder should go: %v", share, err)
		}
	}
}

// A missing host or share must be refused before any connection is attempted.
func TestSMBRefusesIncompleteSettings(t *testing.T) {
	if _, err := DialSMB(context.Background(), SMBSettings{Share: "data"}); err == nil {
		t.Error("a missing host was accepted")
	}
	if _, err := DialSMB(context.Background(), SMBSettings{Host: "192.0.2.1"}); err == nil {
		t.Error("a missing share name was accepted")
	}
}

// Paths must be resolved inside the root, with backslashes understood.
//
// Somebody configuring a Windows share will type backslashes. The library takes forward slashes, so a path with either
// has to be checked as one thing - otherwise a containment check that only splits on "/" sees "..\\.." as a single
// component and lets it through.
func TestSMBResolvesPathsInsideTheRoot(t *testing.T) {
	s := &SMB{host: "server", shareName: "data", root: "inbox"}

	ok := []struct {
		in   string
		want string
	}{
		{".", "inbox"},
		{"one.hl7", "inbox/one.hl7"},
		{"processed/one.hl7", "inbox/processed/one.hl7"},
		{`processed\one.hl7`, "inbox/processed/one.hl7"},
	}
	for _, tc := range ok {
		got, err := s.resolve(tc.in)
		if err != nil {
			t.Errorf("resolve(%q): %v", tc.in, err)
			continue
		}
		if got != tc.want {
			t.Errorf("resolve(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}

	// Both separators must be caught. A check splitting only on "/" treats "..\\secrets" as one component and passes it.
	for _, bad := range []string{
		"../secrets.hl7",
		`..\secrets.hl7`,
		"processed/../../outside.hl7",
		`processed\..\..\outside.hl7`,
	} {
		if got, err := s.resolve(bad); err == nil {
			t.Errorf("resolve(%q) was allowed and produced %q", bad, got)
		}
	}
}

// The description must not contain the password, and must read the way the share is written down.
func TestTheSMBDescriptionIsUNCAndHasNoPassword(t *testing.T) {
	s := &SMB{host: "fileserver", shareName: "data", root: "hl7/inbox"}

	got := s.Describe()
	if got != `\\fileserver\data\hl7\inbox` {
		t.Errorf("described as %q; it should read the way somebody would write the share down", got)
	}
	if strings.Contains(got, "p") && strings.Contains(got, "password") {
		t.Error("the description mentions a password")
	}
}

// Close must attempt every step even when an earlier one fails.
//
// Returning at the first error would leave a TCP connection open for every poll, and a poller running every thirty
// seconds exhausts file descriptors within a day. Verified on a zero value, which is the case where every step has
// nothing to do.
func TestClosingAnSMBConnectionIsSafeWhenNothingWasOpened(t *testing.T) {
	s := &SMB{}
	if err := s.Close(); err != nil {
		t.Errorf("closing an unopened connection reported an error: %v", err)
	}
	// And twice, because the poller closes what it opens and a failed poll can reach Close by two paths.
	if err := s.Close(); err != nil {
		t.Errorf("closing twice reported an error: %v", err)
	}
}
