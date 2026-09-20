package config

import (
	"strings"
	"testing"
)

// Routing between channels is the one destination type that can destroy a server by itself, so these
// tests are led by the arrangements that must be refused.

func routed(name string, targets ...string) *Channel {
	c := &Channel{Name: name}
	for _, t := range targets {
		c.Destinations = append(c.Destinations, Destination{
			Name:    "to-" + t,
			Type:    DestinationChannel,
			Channel: &ChannelDestination{Name: t},
		})
	}
	return c
}

func TestRoutingAcceptsAFanOut(t *testing.T) {
	// The arrangement this feature exists for: one channel receives and each child owns a downstream
	// system.
	channels := []*Channel{
		routed("router", "lab", "billing", "archive"),
		{Name: "lab"},
		{Name: "billing"},
		{Name: "archive"},
	}

	if errs := ValidateRouting(channels); len(errs) > 0 {
		t.Fatalf("a fan-out was refused: %v", errs)
	}
}

func TestRoutingAcceptsAChain(t *testing.T) {
	// A routes to B routes to C is legitimate and common: a normalising channel in front of a
	// distributing one.
	channels := []*Channel{
		routed("intake", "normalise"),
		routed("normalise", "deliver"),
		{Name: "deliver"},
	}

	if errs := ValidateRouting(channels); len(errs) > 0 {
		t.Fatalf("a chain was refused: %v", errs)
	}
}

func TestRoutingRefusesAChannelRoutingToItself(t *testing.T) {
	// Almost always a typo, and worth its own sentence: "this channel routes to itself" says more
	// than a two-name cycle.
	errs := ValidateRouting([]*Channel{routed("loop", "loop")})

	if len(errs) == 0 {
		t.Fatal("a channel routing to itself was accepted")
	}
	if !strings.Contains(errs[0].Error(), "itself") {
		t.Errorf("the error does not say the channel routes to itself: %v", errs[0])
	}
}

func TestRoutingRefusesATwoChannelCycle(t *testing.T) {
	// A message entering this becomes an unbounded number of messages, each one a real delivery with
	// real recording, so it fills the message store and the disk before anybody works out why.
	errs := ValidateRouting([]*Channel{
		routed("a", "b"),
		routed("b", "a"),
	})

	if len(errs) == 0 {
		t.Fatal("a cycle was accepted")
	}
	if !strings.Contains(errs[0].Error(), "circle") {
		t.Errorf("the error does not describe a circle: %v", errs[0])
	}
	// Both members named, so the reader knows where to look without tracing it themselves.
	if !strings.Contains(errs[0].Error(), "a") || !strings.Contains(errs[0].Error(), "b") {
		t.Errorf("the cycle does not name its members: %v", errs[0])
	}
}

func TestRoutingRefusesALongerCycle(t *testing.T) {
	errs := ValidateRouting([]*Channel{
		routed("a", "b"),
		routed("b", "c"),
		routed("c", "d"),
		routed("d", "b"),
	})

	if len(errs) == 0 {
		t.Fatal("a four-channel cycle was accepted")
	}
	msg := errs[0].Error()
	for _, name := range []string{"b", "c", "d"} {
		if !strings.Contains(msg, name) {
			t.Errorf("the cycle does not name %q: %v", name, msg)
		}
	}
}

func TestRoutingRefusesAMissingChannel(t *testing.T) {
	// At run time this is a channel that starts happily and then fails every message, so the operator
	// sees traffic being lost rather than a configuration mistake.
	errs := ValidateRouting([]*Channel{routed("router", "nowhere")})

	if len(errs) == 0 {
		t.Fatal("a destination naming a channel that does not exist was accepted")
	}
	if !strings.Contains(errs[0].Error(), "nowhere") {
		t.Errorf("the error does not name the missing channel: %v", errs[0])
	}
	if !strings.Contains(errs[0].Error(), "does not exist") {
		t.Errorf("the error does not say what is wrong: %v", errs[0])
	}
}

func TestADisabledDestinationStillCounts(t *testing.T) {
	// A loop that appears the moment somebody ticks a box is worse than one that never loaded,
	// because by then nobody is looking at the routing.
	off := false
	a := routed("a", "b")
	a.Destinations[0].Enabled = &off

	errs := ValidateRouting([]*Channel{a, routed("b", "a")})
	if len(errs) == 0 {
		t.Fatal("a cycle through a disabled destination was accepted")
	}
}

func TestTheSameCycleIsReportedOnce(t *testing.T) {
	// Two channels in a cycle would otherwise each report it, and a reader would think there were two
	// problems.
	errs := ValidateRouting([]*Channel{
		routed("a", "b"),
		routed("b", "a"),
	})
	if len(errs) != 1 {
		t.Errorf("reported %d problems for one cycle: %v", len(errs), errs)
	}
}

func TestRoutingProblemsComeOutInAStableOrder(t *testing.T) {
	// These go into logs that get compared, and a Go map ranges randomly.
	channels := []*Channel{
		routed("z", "missing-one"),
		routed("y", "missing-two"),
		routed("x", "missing-three"),
	}

	first := ValidateRouting(channels)
	for i := 0; i < 20; i++ {
		again := ValidateRouting(channels)
		if len(again) != len(first) {
			t.Fatalf("run %d reported %d problems, first run reported %d", i, len(again), len(first))
		}
		for j := range first {
			if again[j].Error() != first[j].Error() {
				t.Fatalf("run %d differs at %d:\n  %v\n  %v", i, j, first[j], again[j])
			}
		}
	}
}

func TestAChannelDestinationNeedsAName(t *testing.T) {
	errs := validateChannelDest(&Destination{
		Name:    "to-nothing",
		Type:    DestinationChannel,
		Channel: &ChannelDestination{},
	})
	if len(errs) == 0 {
		t.Fatal("a channel destination with no target was accepted")
	}
}

func TestAChannelDestinationNeedsItsBlock(t *testing.T) {
	errs := validateChannelDest(&Destination{Name: "to-nothing", Type: DestinationChannel})
	if len(errs) == 0 {
		t.Fatal("a channel destination with no channel block was accepted")
	}
}

func TestAChannelDestinationRefusesAnAddress(t *testing.T) {
	// A routed message does not travel over the network. Someone writing an address here has
	// misunderstood what this destination does, and being told is better than having it ignored.
	c := &Channel{
		Name:   "router",
		Source: Source{Type: SourceMLLP, Listen: "127.0.0.1:2575"},
		Destinations: []Destination{{
			Name:    "to-lab",
			Type:    DestinationChannel,
			Address: "10.0.0.1:2575",
			Channel: &ChannelDestination{Name: "lab"},
		}},
	}

	err := c.Validate()
	if err == nil {
		t.Fatal("an address on a channel destination was accepted")
	}
	if !strings.Contains(err.Error(), "address") {
		t.Errorf("the error does not mention the address: %v", err)
	}
}

func TestAChannelBlockOnlyAppliesToAChannelDestination(t *testing.T) {
	c := &Channel{
		Name:   "confused",
		Source: Source{Type: SourceMLLP, Listen: "127.0.0.1:2575"},
		Destinations: []Destination{{
			Name:    "out",
			Type:    DestinationFile,
			Dir:     "/tmp/out",
			Channel: &ChannelDestination{Name: "lab"},
		}},
	}

	err := c.Validate()
	if err == nil {
		t.Fatal("a channel block on a file destination was accepted")
	}
}
