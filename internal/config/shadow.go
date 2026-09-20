package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// Shadow runs a candidate version of a channel beside the live one and reports
// where they differ.
//
// This is the answer to the question that makes interface work frightening: how do
// you know a change is safe? Not by reading it. A transformation is a program
// operating on messages whose variety nobody has catalogued, and the case that
// breaks is always the one nobody thought of - the patient with two identifiers, the
// result with an empty units field, the A08 that arrives before its A01.
//
// A channel test proves the cases somebody thought of. Shadow mode proves the cases
// that actually arrive, against real traffic, before the change carries any of it.
//
// Two properties make it safe, and both are structural rather than configurable:
//
//   - The shadow never delivers. Its destinations are not constructed, so there is
//     no code path from a shadow channel to a receiver, and no setting can create
//     one by mistake.
//   - The shadow never affects the live channel. It runs after the live message has
//     been delivered and acknowledged, and a shadow that panics, throws or hangs is
//     recorded as a shadow failure and nothing else.
type Shadow struct {
	// Channel is the path to the candidate channel file.
	//
	// A separate file rather than an inline block, on purpose: the candidate is
	// meant to become the live channel, so it should be the artefact that gets
	// reviewed, committed and promoted, not a fragment that has to be extracted.
	Channel string `yaml:"channel"`

	// Sample is the fraction of messages to shadow, from 0 to 1. Defaults to 1.
	//
	// Worth lowering only on a busy feed, and worth understanding before doing so:
	// sampling reduces cost and reduces confidence in exactly the same proportion,
	// and the message that would have shown the difference is the unusual one.
	Sample float64 `yaml:"sample,omitempty"`

	// Compare limits the comparison to these field paths. Empty compares everything.
	Compare []string `yaml:"compare,omitempty"`

	// Ignore excludes field paths from the comparison.
	//
	// Needed in practice, because a channel that stamps a timestamp or a sequence
	// number differs on every single message and would report a difference rate of
	// 100% while telling you nothing.
	Ignore []string `yaml:"ignore,omitempty"`

	// MaxDifferences bounds how many differing messages are kept for inspection.
	// Defaults to 100.
	MaxDifferences int `yaml:"max_differences,omitempty"`

	// Timeout bounds one shadow run. Defaults to 5s.
	//
	// Separate from the channel's own timeout, and shorter. A candidate with a
	// runaway script must not accumulate goroutines behind the live traffic.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// Validate checks a shadow block.
func (s *Shadow) Validate() error {
	if s == nil {
		return nil
	}

	if s.Sample == 0 {
		s.Sample = 1
	}
	if s.MaxDifferences == 0 {
		s.MaxDifferences = 100
	}
	if s.Timeout == 0 {
		s.Timeout = 5 * time.Second
	}

	if strings.TrimSpace(s.Channel) == "" {
		return errors.New("shadow.channel is required, naming the candidate channel file")
	}
	if s.Sample <= 0 || s.Sample > 1 {
		return fmt.Errorf("shadow.sample is %v; it is a fraction between 0 and 1, and "+
			"0 would shadow nothing", s.Sample)
	}
	if s.MaxDifferences < 1 {
		return fmt.Errorf("shadow.max_differences is %d, so no difference would be "+
			"kept and there would be nothing to look at", s.MaxDifferences)
	}
	if s.Timeout <= 0 {
		return errors.New("shadow.timeout must be positive")
	}

	for i, p := range s.Compare {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("shadow.compare[%d] is empty", i)
		}
	}
	for i, p := range s.Ignore {
		if strings.TrimSpace(p) == "" {
			return fmt.Errorf("shadow.ignore[%d] is empty", i)
		}
	}

	// Both together is a contradiction worth catching, because the result would be
	// silently empty: a path in Compare and Ignore is excluded, and if that is every
	// path then nothing is compared and the report says the versions agree.
	for _, c := range s.Compare {
		for _, ig := range s.Ignore {
			if c == ig {
				return fmt.Errorf("shadow lists %q in both compare and ignore, so it "+
					"would be excluded. If compare only lists ignored paths, nothing is "+
					"compared and the report would say the two versions agree", c)
			}
		}
	}
	return nil
}

// Warnings reports things worth saying at every start.
func (s *Shadow) Warnings() []string {
	if s == nil {
		return nil
	}
	var out []string

	if s.Sample < 1 {
		out = append(out, fmt.Sprintf("shadow.sample is %.2f, so %.0f%% of messages "+
			"are compared. Sampling reduces cost and confidence in the same proportion, "+
			"and the message that would show the difference is usually the unusual one",
			s.Sample, s.Sample*100))
	}
	if len(s.Ignore) == 0 {
		out = append(out, "no shadow.ignore paths are set. If the channel stamps a "+
			"timestamp or a sequence number, every message will differ and the report "+
			"will say 100% while telling you nothing")
	}
	// Said every time, because it is the property that makes this safe to run against
	// live traffic and the one somebody will want reassurance about.
	out = append(out, "the shadow channel's destinations are not constructed, so it "+
		"cannot deliver anything. It runs after the live message has been "+
		"acknowledged, and a failure in it is recorded and otherwise ignored")

	return out
}
