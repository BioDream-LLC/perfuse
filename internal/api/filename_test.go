package api

import (
	"strings"
	"testing"
)

// TestAChannelNameCannotEscapeItsDirectory covers path traversal.
//
// A channel name comes from an HTTP request and becomes a filename, which is the shape of every directory traversal bug.
// The defence is an allowlist rather than a blocklist of dangerous sequences: a blocklist has to anticipate every
// encoding, and the list of encodings is not knowable in advance.
func TestAChannelNameCannotEscapeItsDirectory(t *testing.T) {
	hostile := []string{
		"../../../etc/passwd",
		"..%2f..%2fetc%2fpasswd",
		"/etc/cron.d/evil",
		"....//....//etc/hosts",
		"a/../../b",
		"channel\x00.yaml",
		"..\\..\\windows\\system32\\drivers\\etc\\hosts",
		"~/.ssh/authorized_keys",
		"$HOME/.bashrc",
	}

	for _, name := range hostile {
		got, err := filenameFor(name)
		if err != nil {
			// Refusing is a fine outcome too.
			continue
		}

		// Nothing that could leave the directory, and nothing that could confuse a shell or a path parser.
		for _, bad := range []string{"/", "\\", "..", "\x00", "~", "$", ":"} {
			if strings.Contains(got, bad) {
				t.Errorf("filenameFor(%q) produced %q, which contains %q", name, got, bad)
			}
		}
		if !strings.HasSuffix(got, ".yaml") {
			t.Errorf("filenameFor(%q) produced %q, which is not a .yaml file", name, got)
		}
	}
}

// TestWindowsDeviceNamesAreNotUsedAsFilenames covers a name that fails only on Windows.
//
// CON, PRN, AUX, NUL, COM1 to COM9 and LPT1 to LPT9 are reserved on Windows whatever extension follows. A channel called
// aux would be written to aux.yaml, which on Windows is a device rather than a file: the write succeeds, the bytes go
// nowhere, and reading it back finds nothing at all.
//
// Hospitals run Windows, and aux and prn read as ordinary abbreviations somebody would plausibly choose.
func TestWindowsDeviceNamesAreNotUsedAsFilenames(t *testing.T) {
	reserved := []string{
		"con", "CON", "Con",
		"prn", "aux", "nul",
		"com1", "COM9", "lpt1", "LPT9",
	}

	for _, name := range reserved {
		got, err := filenameFor(name)
		if err != nil {
			continue
		}

		base := strings.TrimSuffix(got, ".yaml")
		if isWindowsDeviceName(base) {
			t.Errorf("the channel %q produced the filename %q, whose base %q is a Windows device name",
				name, got, base)
		}
	}
}

// TestNamesThatOnlyLookReservedAreLeftAlone covers over-correction.
//
// COM0 and LPT0 are not reserved, and neither is any longer word beginning with one of the reserved prefixes. Mangling
// those would rename channels for no reason.
func TestNamesThatOnlyLookReservedAreLeftAlone(t *testing.T) {
	fine := map[string]string{
		"com0":       "com0.yaml",
		"lpt0":       "lpt0.yaml",
		"console":    "console.yaml",
		"connection": "connection.yaml",
		"auxiliary":  "auxiliary.yaml",
		"printer":    "printer.yaml",
		"com10":      "com10.yaml",
		"nullable":   "nullable.yaml",
	}

	for name, want := range fine {
		got, err := filenameFor(name)
		if err != nil {
			t.Errorf("filenameFor(%q) failed: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("filenameFor(%q) = %q, want %q; a name that only resembles a device name should be "+
				"left alone", name, got, want)
		}
	}
}

// TestDeviceNameDetection covers the predicate directly.
func TestDeviceNameDetection(t *testing.T) {
	for _, name := range []string{"con", "PRN", "aux", "Nul", "com1", "com9", "LPT1", "lpt9"} {
		if !isWindowsDeviceName(name) {
			t.Errorf("%q is not detected as a device name", name)
		}
	}
	for _, name := range []string{"com0", "lpt0", "com", "lpt", "console", "com10", "", "nullx"} {
		if isWindowsDeviceName(name) {
			t.Errorf("%q is wrongly detected as a device name", name)
		}
	}
}
