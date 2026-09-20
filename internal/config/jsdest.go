package config

import (
	"fmt"
	"strings"
	"time"
)

// JavaScriptDestination runs a script instead of sending anywhere.
//
// Mirth's JavaScript Writer, and it is used far more than its position in a feature list suggests. It is the escape hatch:
// when a site needs to talk to something no connector covers, or apply logic no transformation expresses, they write a
// JavaScript Writer. A migration that cannot run those cannot move the channels that have them, which in a mature Mirth
// installation is a large fraction.
//
// Deliberately not a general scripting host. The script gets the message and the same helpers a transformer gets; what it
// does not get is a way to reach the filesystem or the network, because that is what the other destinations are for and a
// script that opens its own socket is invisible to every retry, queue and metric in the engine.
type JavaScriptDestination struct {
	// Script is the code to run. Required.
	Script string `yaml:"script"`

	// Timeout bounds one execution. Zero applies a default.
	//
	// Bounded because a destination script is on the delivery path: an infinite loop in one does not merely fail a
	// message, it holds the queue behind it.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// SuccessOnUndefined treats a script that returns nothing as success. Defaults to true.
	//
	// True is the right default and worth explaining. Mirth's JavaScript Writers overwhelmingly do their work and return
	// nothing, so requiring an explicit return would break every one of them on import. Setting it false is for a site
	// that wants a script to be explicit about whether it worked.
	SuccessOnUndefined *bool `yaml:"success_on_undefined,omitempty"`
}

// ReturnsSuccessOnUndefined reports whether an empty return counts as success.
func (j *JavaScriptDestination) ReturnsSuccessOnUndefined() bool {
	if j == nil || j.SuccessOnUndefined == nil {
		return true
	}
	return *j.SuccessOnUndefined
}

// DefaultJavaScriptTimeout bounds one execution when nothing else says.
//
// Ten seconds. Long enough for a script doing real work through the helpers, short enough that a runaway one is noticed as
// a slow channel rather than a stopped one.
const DefaultJavaScriptTimeout = 10 * time.Second

func validateJavaScriptDest(d *Destination) []error {
	cfg := d.JavaScript
	if cfg == nil {
		return []error{fmt.Errorf("destination %q is a javascript destination with no javascript block", d.Name)}
	}

	var errs []error

	if strings.TrimSpace(cfg.Script) == "" {
		errs = append(errs, fmt.Errorf("destination %q has an empty script, so it would accept every message and do "+
			"nothing with it - which looks like a working destination and is not", d.Name))
	}

	if cfg.Timeout < 0 {
		errs = append(errs, fmt.Errorf("destination %q has a negative timeout", d.Name))
	}
	if cfg.Timeout > 5*time.Minute {
		// Refused rather than allowed. A destination script is on the delivery path, so a five minute one does not just
		// delay a message, it holds every message queued behind it for five minutes each.
		errs = append(errs, fmt.Errorf("destination %q allows a script %s to run; a destination script blocks the "+
			"queue behind it, so anything above five minutes will present as a stalled channel rather than a slow one",
			d.Name, cfg.Timeout))
	}

	return errs
}
