package engine

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/shadow"
	"github.com/biodream-llc/perfuse/internal/xtree"
)

// TransformOnly runs the filter and the transformation pipeline and returns the
// result without delivering anything.
//
// This is what a shadow channel is given, and it is the only method it is given. A
// shadow holds a *Channel whose senders were never constructed, so there is nothing
// to deliver to even if something tried - but the narrow interface is what makes
// "the shadow cannot deliver" a fact about the code rather than a promise about it.
func (c *Channel) TransformOnly(ctx context.Context, raw []byte) (shadow.Result, error) {
	// An HL7 v3 channel takes its own path, because nothing below applies: a v3 document is not a v2 message, its
	// filter is a different language, and its transformations are a different vocabulary.
	//
	// Dispatched on the data type rather than attempted and allowed to fail, because hl7.Parse on an XML document
	// does not reliably fail - it finds no MSH and returns an error here, but a document whose first line happened to
	// look segment-shaped would parse into nonsense and be compared as though it were a message.
	if c.cfg.Type() == config.DataHL7v3 {
		return c.transformOnlyV3(raw)
	}

	m, err := hl7.Parse(raw)
	if err != nil {
		return shadow.Result{}, fmt.Errorf("parsing the message: %w", err)
	}

	// The same filter the live path evaluates, through the same compiled expression.
	// A second implementation would eventually disagree, and a shadow that disagreed
	// with the channel it is shadowing would be worse than having none.
	if f := c.cfg.FilterExpr(); f != nil {
		match, err := f.Eval(m)
		if err != nil {
			return shadow.Result{}, fmt.Errorf("filter: %w", err)
		}
		if !match {
			return shadow.Result{Accepted: false, RejectedBy: "filter"}, nil
		}
	}

	stage, err := c.runTransformStage(m, raw)
	if err != nil {
		return shadow.Result{}, err
	}
	if !stage.Accepted {
		return shadow.Result{Accepted: false, RejectedBy: stage.RejectedBy}, nil
	}

	return shadow.Result{Accepted: true, Message: stage.Raw}, nil
}

// transformOnlyV3 is the v3 equivalent: parse, filter, transform, and deliver nothing.
//
// Written against the same compiled filter and the same compiled steps the live path uses, for the reason stated above: a
// second implementation would eventually disagree, and a shadow that disagrees with the channel it is shadowing is worse than
// no shadow, because it produces confident differences that are its own fault.
func (c *Channel) transformOnlyV3(raw []byte) (shadow.Result, error) {
	root, err := xtree.Parse(raw)
	if err != nil {
		return shadow.Result{}, fmt.Errorf("parsing the document: %w", err)
	}

	if f := c.cfg.HL7v3Filter(); f != nil {
		match, err := f.Match(root)
		if err != nil {
			return shadow.Result{}, fmt.Errorf("filter: %w", err)
		}
		if !match {
			return shadow.Result{Accepted: false, RejectedBy: "filter"}, nil
		}
	}

	if steps := c.cfg.HL7v3Steps(); steps.Len() > 0 {
		if err := steps.Apply(root); err != nil {
			return shadow.Result{}, fmt.Errorf("transformation: %w", err)
		}
	}

	// Marshalled with the same indentation the live path uses.
	//
	// This matters more than it looks: the comparison is textual per field, but the two sides are re-parsed from
	// these bytes, and a candidate that differed only in whitespace would either report differences that are not
	// real or - depending on the diff - hide ones that are.
	return shadow.Result{Accepted: true, Message: root.Marshal(2)}, nil
}

// newShadowChannel builds a channel that can transform and cannot deliver.
//
// Constructed directly rather than through NewChannel, and the reason is the whole
// safety property: dests is left nil, so there is no sender object in existence for
// a shadow at all. A factory that refused to build one would also work, but this way
// "the shadow cannot deliver" is a fact about what exists in memory rather than
// about what a function returns, and no future change can start calling something
// that is not there.
func newShadowChannel(cfg *config.Channel, log *slog.Logger) (*Channel, error) {
	if len(cfg.Destinations) == 0 {
		// Still required, because a candidate with no destinations is not a channel
		// somebody could promote, and comparing against it would be misleading.
		return nil, fmt.Errorf("the candidate channel %q has no destinations, so it "+
			"could not be promoted even if it compared cleanly", cfg.Name)
	}
	if log == nil {
		log = slog.Default()
	}

	return &Channel{
		cfg:   cfg,
		log:   log.With("channel", cfg.Name, "role", "shadow"),
		stats: newStats(),
		// dests deliberately nil.
	}, nil
}

// startShadow prepares the comparison, if the channel has one configured.
func (c *Channel) startShadow(load func(path string) (*config.Channel, error)) error {
	if c.cfg.Shadow == nil {
		return nil
	}

	// LoadFile validates as well as reads, which is what is wanted here: a shadow that
	// tolerated an invalid candidate would report differences for a version that could
	// never be promoted, and that is misleading rather than merely useless.
	candidateCfg, err := load(c.cfg.Shadow.Channel)
	if err != nil {
		return fmt.Errorf("channel %q shadow: the candidate %s could not be loaded, so "+
			"it could not be promoted even if it compared cleanly: %w",
			c.cfg.Name, c.cfg.Shadow.Channel, err)
	}

	candidate, err := newShadowChannel(candidateCfg, c.log)
	if err != nil {
		return fmt.Errorf("channel %q shadow: %w", c.cfg.Name, err)
	}

	c.shadow = shadow.New(c.cfg.Name, c.cfg.Shadow, c.cfg.Type(), c, candidate)
	c.shadowChannel = candidate

	for _, w := range c.cfg.Shadow.Warnings() {
		c.log.Info("shadow", "channel", c.cfg.Name, "note", w)
	}
	c.log.Info("shadowing a candidate channel",
		"channel", c.cfg.Name,
		"candidate", c.cfg.Shadow.Channel,
		"sample", c.cfg.Shadow.Sample)

	return nil
}

// stopShadow releases the candidate.
func (c *Channel) stopShadow() error {
	if c.shadowChannel == nil {
		return nil
	}
	err := c.shadowChannel.Close()
	c.shadowChannel = nil
	c.shadow = nil
	return err
}

// observeShadow hands a message to the comparison, if there is one.
//
// Called after the live message has been delivered and acknowledged, and
// deliberately synchronous. A goroutine per message would let a slow candidate
// accumulate them without bound behind live traffic, and the whole point is that the
// shadow cannot affect the channel carrying real messages.
func (c *Channel) observeShadow(ctx context.Context, raw []byte) {
	if c.shadow == nil {
		return
	}
	c.shadow.Observe(ctx, raw)
}

// ShadowReport is what the interface shows.
type ShadowReport struct {
	Channel     string              `json:"channel"`
	Candidate   string              `json:"candidate"`
	Stats       shadow.Stats        `json:"stats"`
	Verdict     string              `json:"verdict"`
	Differences []shadow.Difference `json:"differences"`
}

// ShadowReport returns the comparison so far, or nil if there is none.
func (c *Channel) ShadowReport() *ShadowReport {
	if c.shadow == nil {
		return nil
	}
	return &ShadowReport{
		Channel:     c.cfg.Name,
		Candidate:   c.cfg.Shadow.Channel,
		Stats:       c.shadow.Stats(),
		Verdict:     c.shadow.Verdict(),
		Differences: c.shadow.Differences(),
	}
}
