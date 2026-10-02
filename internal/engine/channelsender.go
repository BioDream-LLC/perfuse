package engine

import (
	"context"
	"errors"
	"fmt"

	"github.com/biodream-llc/perfuse/internal/config"
)

// Handing a message to another channel.
//
// This is Mirth's Channel Reader and Channel Writer, and it is the pattern most production Mirth
// installations are built around: one channel receives from a sender and fans out to several child
// channels, each owning one downstream system. It matters because it is how people keep a hundred
// interfaces comprehensible - the routing lives in one place, and each child channel is small enough
// to reason about, deploy and stop on its own.
//
// Without it, a Perfuse migration means flattening somebody's router-and-children arrangement into
// one channel per path with the routing logic copied into each, which is both a lot of work and a
// worse design than the one they had.
//
// # It goes in at the top, not at the transport
//
// A message routed to another channel does not travel over MLLP or HTTP to get there. It is handed
// straight to that channel's handling, which means the child's own filter, transformations and
// destinations all run, but its source does not. That is deliberate and it is what Mirth does: the
// child's source connector exists to receive from outside, and a routed message is already inside.
//
// A consequence worth stating: the child channel does not need to be listening on anything. A child
// with an MLLP source on a port nobody connects to is still a perfectly good routing target, and
// arranging that is how people build fan-out without opening ports they do not need.
//
// # What the parent learns
//
// The child's outcome becomes the parent's delivery result, so a child that fails makes the parent's
// delivery to it fail, and the parent's own acknowledgement reflects that in the usual way. A child
// that deliberately filters a message is a success from the parent's point of view - it was delivered
// to the child, and the child decided not to forward it. Treating that as a failure would make every
// filtered message look like a broken interface.

// ChannelRouter resolves a channel name and hands it a message.
//
// An interface rather than a concrete type because the thing that owns running channels lives above
// this package, and the engine must not import it. Whoever builds the channels supplies the router.
type ChannelRouter interface {
	// Route hands raw to the named channel and reports what happened to it.
	//
	// The error describes a delivery failure, not a filtered message: a channel that examines a
	// message and decides not to forward it has done its job.
	Route(ctx context.Context, channel string, raw []byte) error
}

// ErrNoRouter is returned when a channel destination is configured but nothing can resolve names.
//
// A distinct error because it means the server was assembled wrongly rather than that the channel was
// configured wrongly, and the two need different people to fix them.
var ErrNoRouter = errors.New("this server cannot route between channels")

// ChannelSender hands messages to another channel in the same process.
type ChannelSender struct {
	target string
	router ChannelRouter
}

// NewChannelSender builds a sender that routes to another channel.
func NewChannelSender(d config.Destination, router ChannelRouter) (*ChannelSender, error) {
	if d.Channel == nil || d.Channel.Name == "" {
		return nil, fmt.Errorf("a channel destination needs the name of the channel to route to")
	}
	if router == nil {
		// Refused here rather than at send time. A channel that cannot possibly deliver should not
		// start and then fail on every message; the operator would see a running channel losing
		// traffic instead of a channel that refused to start with a reason.
		return nil, fmt.Errorf("%w, so the destination %q cannot be used", ErrNoRouter, d.Name)
	}
	return &ChannelSender{target: d.Channel.Name, router: router}, nil
}

// Send hands the message to the target channel.
func (s *ChannelSender) Send(ctx context.Context, raw []byte) error {
	if err := s.router.Route(ctx, s.target, raw); err != nil {
		// The target's name is worth repeating. Without it, a failure deep in a chain of channels
		// produces an error that could have come from any of them.
		return fmt.Errorf("routing to channel %q: %w", s.target, err)
	}
	return nil
}

// Describe says where this destination sends.
func (s *ChannelSender) Describe() string {
	return fmt.Sprintf("the channel %q, in this server", s.target)
}

// Close releases nothing.
//
// The target channel's lifetime belongs to whatever started it. Closing it here would stop a channel
// that other channels may also be routing to, and that other people may be watching.
func (s *ChannelSender) Close() error { return nil }

// Target reports which channel this sender routes to, so loop detection can see the graph.
func (s *ChannelSender) Target() string { return s.target }

// Route handles a message that arrived from another channel rather than from a source.
//
// The same handling a received message gets, which is the point: a routed message must be filtered,
// transformed, recorded and delivered exactly as one that arrived over the wire, or the child channel
// would behave differently depending on where its traffic came from and nobody could reason about it.
//
// The acknowledgement is returned for completeness. The usual caller discards it, because it is
// addressed to whoever sent the message and the sender is another channel.
func (c *Channel) Route(ctx context.Context, raw []byte) ([]byte, error) {
	return c.handle(ctx, raw)
}
