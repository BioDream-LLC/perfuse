package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/contract"
	"gopkg.in/yaml.v3"
)

// Attaching a contract to a channel.
//
// A contract in a file beside the channel rather than inline, because it is generated - `perfuse contract
// promote` writes it - and an inline block would mean regenerating it required editing the channel. It is also
// long: thirty expectations is normal, and a channel file that is mostly contract is a channel file nobody
// reads.
//
// The channel names the file, so the two travel together into version control and a review shows both the
// change to the interface and the change to what is expected of it.

// ContractRef points a channel at its contract.
type ContractRef struct {
	// File is the contract, relative to the channel file.
	File string `yaml:"file"`

	// CheckEvery is how often to re-check, as a duration. Defaults to 15 minutes.
	//
	// Effectively floored at the alert evaluation interval, which is 30 seconds by default, because the check
	// runs from that loop rather than a timer of its own - so a contract asking for 3s gets 30s. That is
	// deliberate: one timer is easier to reason about than two, and it keeps a contract result in step with the
	// reading it is attached to. Worth knowing before somebody sets a short interval and concludes it is broken.
	//
	// Not per message. Building a profile means reading recent traffic, and doing that on every message would
	// make a channel's throughput depend on how much history it has - which is the kind of performance
	// characteristic that only shows up in production, six months in.
	CheckEvery string `yaml:"check_every,omitempty"`

	// Over is how many recent messages to profile. Defaults to 500.
	//
	// A count rather than a time window, because a quiet feed and a busy one need the same amount of evidence
	// to justify a rate, and a time window gives them wildly different amounts.
	Over int `yaml:"over,omitempty"`

	// compiled is the loaded contract.
	compiled *contract.Contract
}

// Contract returns the loaded contract, or nil.
func (r *ContractRef) Contract() *contract.Contract {
	if r == nil {
		return nil
	}
	return r.compiled
}

// defaultContractOver is how many recent messages are profiled.
const defaultContractOver = 500

// Over returns the message count to profile.
// DefaultCheckEvery is how often a contract is re-checked when it does not say.
//
// Fifteen minutes because a contract is a statement about a corpus, not about a message: checking it more often costs a profile of
// recent traffic each time and cannot detect anything sooner, since the window has barely moved.
const DefaultCheckEvery = 15 * time.Minute

// Interval is how often this contract should be re-checked.
//
// A method rather than the parsing being done at the point of use, which is where it was. The check loop applied a default of fifteen
// minutes inline, so anything else that wanted to state the interval - the check command, for one - had to reproduce both the parse and
// the default, and a second copy of a default is a second thing to forget to change.
//
// An unparseable duration falls back to the default rather than erroring here, because the value is validated at load; reaching this
// with a bad one means the validation was bypassed, and a working default is better than a zero interval that would re-check on every
// pass of the loop.
func (r *ContractRef) Interval() time.Duration {
	if r == nil || r.CheckEvery == "" {
		return DefaultCheckEvery
	}

	d, err := time.ParseDuration(r.CheckEvery)
	if err != nil || d <= 0 {
		return DefaultCheckEvery
	}

	return d
}

func (r *ContractRef) Window() int {
	if r == nil || r.Over <= 0 {
		return defaultContractOver
	}
	return r.Over
}

// loadContract reads and validates the referenced contract.
//
// Called at channel load, so a contract that does not parse stops the channel from starting rather than being
// discovered as a silently-absent check. That is the same reasoning as everywhere else here: a check that looks
// configured and is not is worse than no check, because it is trusted.
func (r *ContractRef) load(dir string) error {
	if r == nil {
		return nil
	}

	name := strings.TrimSpace(r.File)
	if name == "" {
		return fmt.Errorf("contract.file is empty; name the contract file, or remove the contract block")
	}

	path := name
	if !filepath.IsAbs(path) {
		path = filepath.Join(dir, path)
	}

	body, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("contract.file: %w", err)
	}

	var c contract.Contract
	dec := yaml.NewDecoder(strings.NewReader(string(body)))
	// Unknown keys refused, as everywhere. A misspelled rule decodes into a valid-looking expectation that
	// never fires, which is the worst outcome for something whose entire job is to notice.
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}

	if errs := c.Validate(); len(errs) > 0 {
		var lines []string
		for _, e := range errs {
			lines = append(lines, e.Error())
		}
		return fmt.Errorf("%s is not a valid contract:\n  %s", path, strings.Join(lines, "\n  "))
	}

	r.compiled = &c
	return nil
}
