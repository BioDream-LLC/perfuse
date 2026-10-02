package config

import (
	"strings"
	"testing"
)

// FTP's configuration mistakes are mostly security ones that are easy to make by accident, so most of these
// tests are about what is refused and whether the refusal tells somebody what to do instead.

func ftpChannel(t *testing.T, ftp *FTPDestination) *Channel {
	t.Helper()
	return &Channel{
		Name:   "to-the-lab",
		Source: Source{Type: SourceMLLP, Listen: "127.0.0.1:2575"},
		Destinations: []Destination{{
			Name: "lab", Type: DestinationFTP, FTP: ftp,
		}},
	}
}

func TestAValidFTPDestinationLoads(t *testing.T) {
	c := ftpChannel(t, &FTPDestination{
		Host: "ftp.lab.internal", User: "interface", Password: "secret", Dir: "/incoming",
	})
	if err := c.Validate(); err != nil {
		t.Fatalf("a valid FTP destination was refused: %v", err)
	}
}

func TestExplicitTLSIsTheDefault(t *testing.T) {
	// Defaulting to plain FTP would make the insecure choice the quiet one.
	c := ftpChannel(t, &FTPDestination{Host: "ftp.lab", User: "u", Password: "p", Dir: "/in"})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := c.Destinations[0].FTP.Security; got != FTPSecurityExplicit {
		t.Errorf("security = %q, want explicit by default", got)
	}
}

func TestAPasswordOverPlainFTPIsRefused(t *testing.T) {
	// A warning in a log is read once and never again, and this one sends a credential across a hospital
	// network in clear text on every single delivery.
	c := ftpChannel(t, &FTPDestination{
		Host: "ftp.lab", User: "u", Password: "p", Dir: "/in", Security: FTPSecurityNone,
	})

	err := c.Validate()
	if err == nil {
		t.Fatal("a password over plain FTP was accepted")
	}
	if !strings.Contains(err.Error(), "clear text") {
		t.Errorf("the error does not say what the problem is: %v", err)
	}
	// And it names both ways out, because "this is refused" with no alternative sends somebody to the text
	// editor to work around the check.
	if !strings.Contains(err.Error(), "explicit") {
		t.Errorf("the error does not offer the secure option: %v", err)
	}
	if !strings.Contains(err.Error(), "allow_clear_password") {
		t.Errorf("the error does not name the escape hatch: %v", err)
	}
}

func TestTheEscapeHatchNamedInTheErrorActuallyExists(t *testing.T) {
	// An error naming a setting that does not exist is worse than no error at all - it sends somebody to
	// look for something they will never find, and they will assume they misread it.
	c := ftpChannel(t, &FTPDestination{
		Host: "ftp.lab", User: "u", Password: "p", Dir: "/in",
		Security: FTPSecurityNone, AllowClearPassword: true,
	})

	if err := c.Validate(); err != nil {
		t.Fatalf("allow_clear_password did not permit what the error said it would: %v", err)
	}
}

func TestPlainFTPWithNoPasswordIsFine(t *testing.T) {
	// Anonymous FTP to a drop box is a real arrangement and there is no credential to protect.
	c := ftpChannel(t, &FTPDestination{Host: "ftp.lab", Dir: "/in", Security: FTPSecurityNone})
	if err := c.Validate(); err != nil {
		t.Fatalf("anonymous plain FTP was refused: %v", err)
	}
}

func TestAnUnknownSecurityModeIsRefusedWithTheOptions(t *testing.T) {
	c := ftpChannel(t, &FTPDestination{
		Host: "ftp.lab", Dir: "/in", Security: FTPSecurity("tls"),
	})

	err := c.Validate()
	if err == nil {
		t.Fatal("an unknown security mode was accepted")
	}
	if !strings.Contains(err.Error(), "implicit") {
		t.Errorf("the error does not list the valid options: %v", err)
	}
}

func TestAMissingHostOrDirIsRefused(t *testing.T) {
	if err := ftpChannel(t, &FTPDestination{Dir: "/in"}).Validate(); err == nil {
		t.Error("a destination with no host was accepted")
	}
	if err := ftpChannel(t, &FTPDestination{Host: "ftp.lab"}).Validate(); err == nil {
		t.Error("a destination with no dir was accepted")
	}
}

func TestTheTemporarySuffixDefaultsToPart(t *testing.T) {
	// Whoever collects these files is usually a scheduled job that takes whatever it finds, and without the
	// rename it eventually takes half a message.
	c := ftpChannel(t, &FTPDestination{Host: "ftp.lab", Dir: "/in"})
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := c.Destinations[0].FTP.TempSuffix; got != ".part" {
		t.Errorf("temp suffix = %q, want .part", got)
	}
}

func TestAnFTPDestinationWithNoBlockIsRefused(t *testing.T) {
	c := &Channel{
		Name:         "to-the-lab",
		Source:       Source{Type: SourceMLLP, Listen: "127.0.0.1:2575"},
		Destinations: []Destination{{Name: "lab", Type: DestinationFTP}},
	}

	err := c.Validate()
	if err == nil {
		t.Fatal("an ftp destination with no ftp block was accepted")
	}
	if !strings.Contains(err.Error(), "ftp block") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}
