// Package channeltest runs tests against a channel definition.
//
// A channel is a program. It filters, rewrites and routes clinical messages, and
// it is edited under pressure by people who cannot try it against production. The
// absence of a way to test one is the single biggest reason interface work is
// frightening, and "I changed the transformer and nothing looked different in the
// message browser" is not a test.
//
// The central decision is that a test runs the message through the real
// transformation path — the same filter, the same declarative steps, the same
// script engine, in the same order — with only the transport replaced. A test that
// exercised a reimplementation would pass while the channel failed, which is worse
// than having no test at all because it actively misleads.
package channeltest

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Suite is a test file.
type Suite struct {
	// Channel is the channel definition under test, as a path relative to the
	// suite file. Relative to the suite rather than to the working directory so a
	// suite can be run from anywhere, including a CI job that checks out into a
	// different path.
	Channel string `yaml:"channel"`

	// Retries keeps the channel's real retry behaviour during a test.
	//
	// Off by default, and the default is the interesting decision. A test replaces
	// the transport, so retrying a fake sender five times with backoff exercises
	// the retry loop rather than the channel — and it turns one failing case into
	// fifteen seconds of waiting, which is how a test suite stops being run. With
	// this off, a destination told to fail is attempted exactly once.
	//
	// Turn it on to test the retry configuration itself.
	Retries bool `yaml:"retries,omitempty"`

	// Tests are the cases.
	Tests []Case `yaml:"tests"`

	// path is where this suite was loaded from, for resolving relative paths.
	path string
}

// Case is one test.
type Case struct {
	// Name says what is being asserted. Required, because a failure report that
	// says "test 3 failed" is a worse version of no report.
	Name string `yaml:"name"`

	// Message is the inbound message, written inline. Segment separators may be
	// newlines in the file; they are converted to carriage returns before parsing,
	// because nobody should have to embed control characters in YAML by hand.
	Message string `yaml:"message,omitempty"`

	// File is an alternative to Message: a fixture path relative to the suite.
	File string `yaml:"file,omitempty"`

	// Expect is what should happen.
	Expect Expect `yaml:"expect"`

	// Skip marks a case as not yet expected to pass, with a reason. Better than
	// commenting it out, which loses the intent and the reason together.
	Skip string `yaml:"skip,omitempty"`
}

// Expect is the assertion for one case.
type Expect struct {
	// Outcome is delivered, filtered, failed, queued, partial or unparseable.
	// Empty means do not check.
	Outcome string `yaml:"outcome,omitempty"`

	// Ack is the expected acknowledgement code: AA, AE or AR.
	//
	// Worth asserting separately from the outcome. The mapping between them is
	// where a channel most often surprises a sender: a partial delivery answers
	// AE, not AA, and getting that wrong means a sender believing a message
	// arrived somewhere it did not.
	Ack string `yaml:"ack,omitempty"`

	// Destinations asserts what each destination received, keyed by name.
	Destinations map[string]DestinationExpect `yaml:"destinations,omitempty"`

	// Fields asserts values in the transformed message, using the same path
	// notation as filters and transformations. Deliberately the same, so a path
	// means one thing everywhere.
	Fields map[string]string `yaml:"fields,omitempty"`

	// Absent asserts that these paths are empty or missing.
	Absent []string `yaml:"absent,omitempty"`

	// Contains asserts substrings anywhere in the transformed message. A blunter
	// instrument than Fields, for a Z segment or an escape sequence where a path
	// is awkward.
	Contains []string `yaml:"contains,omitempty"`

	// NotContains asserts substrings are gone. This is how a test pins the removal
	// of an identifier, which is the assertion that matters most for privacy work:
	// "the account number is not in the outbound message anywhere".
	NotContains []string `yaml:"not_contains,omitempty"`

	// MaxDuration fails a case that takes longer, for catching a transformer that
	// has become slow enough to matter.
	MaxDuration time.Duration `yaml:"max_duration,omitempty"`

	// Logged asserts that the script wrote these substrings via logger. It is the
	// only way to assert on a code path that produces no output otherwise.
	Logged []string `yaml:"logged,omitempty"`
}

// DestinationExpect is what one destination should have received.
type DestinationExpect struct {
	// Received asserts whether this destination got the message at all, which is
	// how a destination filter is tested.
	Received *bool `yaml:"received,omitempty"`

	Contains    []string          `yaml:"contains,omitempty"`
	NotContains []string          `yaml:"not_contains,omitempty"`
	Fields      map[string]string `yaml:"fields,omitempty"`

	// Fail makes this destination reject the message, so the channel's failure
	// handling can be tested without a real receiver that has to be broken on cue.
	Fail bool `yaml:"fail,omitempty"`
}

// Load reads a suite from a file.
func Load(path string) (*Suite, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var suite Suite
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	// An unknown key is an error, as everywhere else here. A misspelled assertion
	// that is silently ignored is a test that passes without testing anything,
	// which is the most expensive kind of bug in a test suite.
	dec.KnownFields(true)
	if err := dec.Decode(&suite); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	suite.path = path

	if err := suite.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &suite, nil
}

func (s *Suite) validate() error {
	var problems []string

	if strings.TrimSpace(s.Channel) == "" {
		problems = append(problems, "no channel is named")
	}
	if len(s.Tests) == 0 {
		problems = append(problems, "no tests are defined")
	}

	seen := map[string]bool{}
	for i, c := range s.Tests {
		where := fmt.Sprintf("test %d", i+1)
		if strings.TrimSpace(c.Name) == "" {
			problems = append(problems, where+" has no name")
		} else {
			where = fmt.Sprintf("%q", c.Name)
			if seen[c.Name] {
				// Two tests with the same name make a failure report ambiguous.
				problems = append(problems, where+" is defined twice")
			}
			seen[c.Name] = true
		}

		if c.Message == "" && c.File == "" {
			problems = append(problems, where+" has neither a message nor a file")
		}
		if c.Message != "" && c.File != "" {
			problems = append(problems, where+" has both a message and a file; pick one")
		}
		if c.Expect.isEmpty() {
			// A case with no assertions passes unconditionally and looks like
			// coverage. That is worse than not having it.
			problems = append(problems,
				where+" asserts nothing, so it would pass whatever the channel did")
		}
		if c.Expect.Ack != "" {
			switch strings.ToUpper(c.Expect.Ack) {
			case "AA", "AE", "AR", "CA", "CE", "CR":
			default:
				problems = append(problems, fmt.Sprintf(
					"%s expects acknowledgement %q, which is not an HL7 code",
					where, c.Expect.Ack))
			}
		}
		if c.Expect.Outcome != "" && !validOutcome(c.Expect.Outcome) {
			problems = append(problems, fmt.Sprintf(
				"%s expects outcome %q; use delivered, filtered, queued, partial, failed or unparseable",
				where, c.Expect.Outcome))
		}
	}

	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

func (e Expect) isEmpty() bool {
	return e.Outcome == "" && e.Ack == "" &&
		len(e.Destinations) == 0 && len(e.Fields) == 0 &&
		len(e.Absent) == 0 && len(e.Contains) == 0 &&
		len(e.NotContains) == 0 && len(e.Logged) == 0 &&
		e.MaxDuration == 0
}

func validOutcome(s string) bool {
	switch strings.ToLower(s) {
	case "delivered", "filtered", "queued", "partial", "failed", "unparseable":
		return true
	}
	return false
}

// ChannelPath resolves the channel file relative to the suite.
func (s *Suite) ChannelPath() string {
	if filepath.IsAbs(s.Channel) {
		return s.Channel
	}
	return filepath.Join(filepath.Dir(s.path), s.Channel)
}

// messageFor returns the inbound bytes for a case.
func (s *Suite) messageFor(c Case) ([]byte, error) {
	if c.File != "" {
		path := c.File
		if !filepath.IsAbs(path) {
			path = filepath.Join(filepath.Dir(s.path), path)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return normaliseSeparators(data), nil
	}
	return normaliseSeparators([]byte(c.Message)), nil
}

// normaliseSeparators converts newlines to carriage returns and strips MLLP
// framing.
//
// Both conversions exist so a fixture can be written or pasted the way a person
// naturally has it. Nobody should have to embed a control character in YAML by
// hand, and a message copied out of a packet capture or produced by the generator
// arrives framed.
func normaliseSeparators(data []byte) []byte {
	text := string(data)

	text = strings.TrimPrefix(text, "\x0b")
	text = strings.TrimSuffix(text, "\x1c\r")
	text = strings.TrimSuffix(text, "\x1c")

	// \r\n first, so a Windows-edited fixture does not end up with doubled
	// separators and an empty segment between every real one.
	text = strings.ReplaceAll(text, "\r\n", "\r")
	text = strings.ReplaceAll(text, "\n", "\r")

	text = strings.TrimRight(text, "\r")
	if text == "" {
		return nil
	}
	return []byte(text + "\r")
}
