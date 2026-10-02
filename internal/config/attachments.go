package config

import (
	"fmt"

	"github.com/biodream-llc/perfuse/internal/attach"
)

// Attachments configures moving large payloads out of a message.
//
// A message carrying a scanned report or a PDF is ordinary in document workflows, and the payload is routinely
// hundreds of times larger than the message around it. Left inline it means the store grows at the rate of the
// documents rather than the traffic, a message queued for five destinations is copied five times, and the interface
// streams megabytes to show somebody a patient name.
//
// Mirth calls these attachment handlers. This is the equivalent, and it is opt-in per channel because a feed of plain
// ADT messages has nothing to extract and should not pay for the machinery.
type Attachments struct {
	// Extract lists the fields to move out.
	Extract []attach.Rule `yaml:"extract"`

	// Reassemble puts payloads back before delivery. Defaults to true.
	//
	// The default is on because a receiver expecting a document must get a document; a message containing a literal
	// token would be filed as a report and nobody would find out until somebody opened the record.
	//
	// Turning it off is for the case where the point of the channel is to strip documents - forwarding metadata to a
	// system that has no use for the image, where sending it would be a privacy question rather than a bandwidth one.
	Reassemble *bool `yaml:"reassemble,omitempty"`
}

// ReassembleBeforeDelivery reports whether payloads should be put back.
func (a *Attachments) ReassembleBeforeDelivery() bool {
	if a == nil {
		return false
	}
	if a.Reassemble == nil {
		return true
	}
	return *a.Reassemble
}

// Validate checks the rules.
func (a *Attachments) Validate() []error {
	if a == nil {
		return nil
	}

	var errs []error

	if len(a.Extract) == 0 {
		// Refused rather than treated as off. An attachments block with no rules looks configured and does nothing,
		// which is the failure mode that wastes the most time - somebody watches the store keep growing and
		// concludes the feature does not work.
		errs = append(errs, fmt.Errorf("the attachments block lists no fields to extract, so it would do nothing; "+
			"add extract: with at least one path, or remove the block"))
		return errs
	}

	seen := map[string]bool{}
	for i := range a.Extract {
		if err := a.Extract[i].Validate(); err != nil {
			errs = append(errs, err)
			continue
		}
		if seen[a.Extract[i].Path] {
			// Two rules on one path would extract it twice, and the second would find a token rather than a payload.
			errs = append(errs, fmt.Errorf("two attachment rules both name %s; the second would find a token "+
				"rather than a payload", a.Extract[i].Path))
		}
		seen[a.Extract[i].Path] = true
	}

	return errs
}
