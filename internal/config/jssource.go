package config

import (
	"fmt"
	"strings"
	"time"
)

// JavaScriptSource configures a source that runs a script on a timer and feeds
// the returned messages into the channel. This is the Mirth "JavaScript Reader"
// equivalent.
//
// The script runs in a goja VM with the same sandbox as transformer scripts:
// msg is not available (there is no inbound message yet), but globalMap,
// configurationMap, logger, and DateUtil are present. The script must return a
// string (one message) or an array of strings (many). Returning nothing or an
// empty array means the poll produced no messages, which is normal.
type JavaScriptSource struct {
	// Script is the JavaScript source code to execute on each poll.
	Script string `yaml:"script"`

	// PollInterval is how often the script runs. Zero means 5 seconds.
	PollInterval time.Duration `yaml:"poll_interval,omitempty"`

	// Timeout bounds a single script execution. Zero means 30 seconds.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// DefaultJSSourcePollInterval is used when PollInterval is zero.
const DefaultJSSourcePollInterval = 5 * time.Second

// DefaultJSSourceTimeout is used when Timeout is zero.
const DefaultJSSourceTimeout = 30 * time.Second

// validate checks the javascript source configuration at load time.
func (j *JavaScriptSource) validate() []error {
	var errs []error

	if strings.TrimSpace(j.Script) == "" {
		errs = append(errs, fmt.Errorf(
			"javascript source has an empty script; it would poll forever and produce nothing"))
	}

	if j.PollInterval < 0 {
		errs = append(errs, fmt.Errorf(
			"javascript.poll_interval must not be negative (got %s)", j.PollInterval))
	}
	if j.Timeout < 0 {
		errs = append(errs, fmt.Errorf(
			"javascript.timeout must not be negative (got %s)", j.Timeout))
	}
	if j.Timeout > 5*time.Minute {
		errs = append(errs, fmt.Errorf(
			"javascript.timeout of %s is too long; a reader script that blocks for that long "+
				"delays polling and stalls the channel", j.Timeout))
	}

	return errs
}
