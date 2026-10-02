package contract

import (
	"fmt"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/profile"
)

// Promoting an observation into an expectation.
//
// Nobody writes a contract from scratch. They have a feed that works, and what they want is "tell me when this
// stops looking like it does now". So the contract is generated from a profile of real traffic and then edited.
//
// # What is deliberately not promoted
//
// The temptation is to assert everything observed, and that produces a contract that fires constantly and gets
// deleted. The rules below exist to keep the generated contract short enough that somebody reads all of it:
//
//   - Only fields populated in essentially every message become Populated expectations. A field populated 60%
//     of the time is not a guarantee, it is a fact about the data, and asserting it means asserting the current
//     case mix - which changes every winter.
//   - Value sets are only promoted where the field genuinely looks like a code set: few distinct values, all
//     short. Promoting the value set of a name field would assert the current patients.
//   - Absent is never promoted. "This field is empty today" is almost never a requirement, and generating
//     hundreds of them would bury the handful that matter.
//   - Repeat limits are promoted with headroom, because the observed maximum is a sample and the next message
//     is allowed to be slightly bigger.
//
// Every generated expectation carries a Why saying it came from observation and how much of it, so somebody
// reading the file later can tell a measured expectation from a decided one. Those are different kinds of
// claim and the difference matters when deciding whether to relax one.

// PromoteOptions tunes what becomes an expectation.
type PromoteOptions struct {
	// AlwaysThreshold is the fill rate above which a field is treated as always populated. Defaults to 0.995.
	AlwaysThreshold float64

	// MinRate is the tolerance written into generated Populated expectations. Defaults to 0.99.
	//
	// Lower than what was observed on purpose. An expectation generated at exactly the observed rate fires on
	// the first message that is slightly worse, which is the same day it was created.
	MinRate float64

	// MaxCodeSetSize is the largest value set that will be promoted. Defaults to 12.
	//
	// Small on purpose. A field with thirty distinct values might be a code set or might be free text that
	// happens to repeat, and getting that wrong asserts patient data.
	MaxCodeSetSize int

	// MaxCodeLength is the longest single value that will be treated as a code. Defaults to 12.
	MaxCodeLength int

	// RepeatHeadroom multiplies the observed maximum repeat count. Defaults to 2.
	RepeatHeadroom int

	// Source names where the profile came from, for the DerivedFrom field.
	Source string
}

func (o PromoteOptions) withDefaults() PromoteOptions {
	if o.AlwaysThreshold <= 0 {
		o.AlwaysThreshold = 0.995
	}
	if o.MinRate <= 0 {
		o.MinRate = 0.99
	}
	if o.MaxCodeSetSize <= 0 {
		o.MaxCodeSetSize = 12
	}
	if o.MaxCodeLength <= 0 {
		o.MaxCodeLength = 12
	}
	if o.RepeatHeadroom <= 0 {
		o.RepeatHeadroom = 2
	}
	return o
}

// Promote builds a contract from an observed profile.
func Promote(p *profile.Report, opts PromoteOptions) *Contract {
	o := opts.withDefaults()

	c := &Contract{DerivedFrom: o.Source}
	if c.DerivedFrom == "" {
		c.DerivedFrom = fmt.Sprintf("a profile of %d messages", p.Messages)
	} else {
		c.DerivedFrom = fmt.Sprintf("%s (%d messages)", c.DerivedFrom, p.Messages)
	}

	observed := fmt.Sprintf("observed in %d messages", p.Messages)

	for _, seg := range p.Segments {
		// A segment present in every message becomes an expectation; one that is sometimes there is a fact
		// about message types, not a guarantee.
		if seg.Rate >= o.AlwaysThreshold {
			c.Expectations = append(c.Expectations, Expectation{
				Path:    seg.ID,
				Rule:    SegmentPresent,
				MinRate: o.MinRate,
				Why:     observed + ", in every one",
			})
		}

		for _, f := range seg.Fields {
			if f.FillRate >= o.AlwaysThreshold {
				c.Expectations = append(c.Expectations, Expectation{
					Path:    f.Path,
					Rule:    Populated,
					MinRate: o.MinRate,
					Why:     fmt.Sprintf("%s, populated in %.1f%% of them", observed, f.FillRate*100),
				})
			}

			if values, ok := codeSet(f, o); ok {
				c.Expectations = append(c.Expectations, Expectation{
					Path:    f.Path,
					Rule:    OneOf,
					Values:  values,
					MinRate: o.MinRate,
					Why: fmt.Sprintf("%s, and only %s appeared: %s",
						observed, plural(len(values), "this value", "these values"),
						strings.Join(values, ", ")),
				})
			}

			if f.MaxRepeats > 1 {
				limit := f.MaxRepeats * o.RepeatHeadroom
				c.Expectations = append(c.Expectations, Expectation{
					Path:  f.Path,
					Rule:  MaxRepeats,
					Limit: limit,
					Why: fmt.Sprintf("%s, repeating at most %d times; the limit has headroom because the "+
						"observed maximum is a sample", observed, f.MaxRepeats),
				})
			}
		}

		if seg.MaxPerMessage > 1 {
			c.Expectations = append(c.Expectations, Expectation{
				Path:  seg.ID,
				Rule:  MaxRepeats,
				Limit: seg.MaxPerMessage * o.RepeatHeadroom,
				Why: fmt.Sprintf("%s, appearing at most %d times per message", observed,
					seg.MaxPerMessage),
			})
		}
	}

	// Enough messages to justify the rates that were just written down, rather than the package default.
	// A contract promoted from 40 messages should not claim to be judgeable on 100.
	if p.Messages > 0 {
		c.MinMessages = p.Messages
		if c.MinMessages > 1000 {
			// Beyond a thousand the extra confidence is not worth making the contract unusable on a quiet
			// morning, when a feed may legitimately not have sent that many yet.
			c.MinMessages = 1000
		}
	}

	return c
}

// codeSet decides whether a field's values are a code set worth asserting.
//
// The judgement that matters most in this package. Getting it wrong in one direction means a useless
// expectation; in the other it means writing patient data into a configuration file that goes into version
// control.
func codeSet(f profile.Field, o PromoteOptions) ([]string, bool) {
	if len(f.Codes) == 0 || len(f.Codes) > o.MaxCodeSetSize {
		return nil, false
	}

	// Distinct values must be genuinely few relative to the traffic. Three distinct values across six messages
	// is not evidence of a code set; three across six thousand is.
	if f.Distinct == 0 || f.Distinct > o.MaxCodeSetSize {
		return nil, false
	}

	values := make([]string, 0, len(f.Codes))
	for _, c := range f.Codes {
		v := strings.TrimSpace(c.Code)
		if v == "" {
			continue
		}
		// A long value is not a code. This is the guard that stops a name or an address being promoted.
		if len(v) > o.MaxCodeLength {
			return nil, false
		}
		values = append(values, v)
	}

	if len(values) == 0 {
		return nil, false
	}

	// Sorted, because a contract file is diffed and Go map iteration is not the only source of instability -
	// the profile's own ordering is by frequency, which changes between runs on the same feed.
	sort.Strings(values)
	return values, true
}

// plural picks the right form, because a generated reason that reads badly is a generated reason people trust
// less than they should.
func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
