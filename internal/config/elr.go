package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/biodream-llc/perfuse/internal/publichealth"
)

// load resolves the ELR file and trigger codes against the channel file's directory and reads the ELR file, so a missing
// file or a misspelt key stops the channel loading instead of failing on the first lab result.
func (e *ELRDestination) load(dir, dest string) []error {
	var errs []error
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(dir, p)
	}
	if strings.TrimSpace(e.Config) == "" {
		errs = append(errs, fmt.Errorf("destination %q: elr.config is required: ELR names its sender and receiver by OID, "+
			"which no lab message carries", dest))
	} else {
		e.Config = resolve(e.Config)
		c, err := publichealth.LoadELRConfig(e.Config)
		if err != nil {
			errs = append(errs, fmt.Errorf("destination %q: elr.config: %w", dest, err))
		} else {
			// The three BuildELR refuses every message without, said once at load.
			for _, x := range []struct {
				hd   publichealth.HD
				name string
			}{{c.SendingFacility, "sending_facility"}, {c.ReceivingApplication, "receiving_application"},
				{c.ReceivingFacility, "receiving_facility"}} {
				if x.hd.ID == "" || x.hd.Type == "" {
					errs = append(errs, fmt.Errorf("destination %q: elr.config: %s needs id and type (an OID the state assigns, type ISO)", dest, x.name))
				}
			}
		}
	}
	if e.RCTC != "" {
		e.RCTC = resolve(e.RCTC)
		if _, err := os.Stat(e.RCTC); err != nil {
			errs = append(errs, fmt.Errorf("destination %q: elr.rctc: %w", dest, err))
		}
	}
	return errs
}

// CompanionFiles are the files beside a channel that its destinations read: each ELR destination's config. A directory of
// channels holds them too, so the loaders use this to tell a channel's companion from a channel file that will not load.
func (c *Channel) CompanionFiles() []string {
	var out []string
	for _, d := range c.Destinations {
		if d.ELR != nil && d.ELR.Config != "" {
			if abs, err := filepath.Abs(d.ELR.Config); err == nil {
				out = append(out, abs)
			}
		}
	}
	return out
}

// IsCompanion reports whether path is one of the companion files of these channels.
func IsCompanion(path string, channels []*Channel) bool {
	abs, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	for _, c := range channels {
		for _, f := range c.CompanionFiles() {
			if f == abs {
				return true
			}
		}
	}
	return false
}
