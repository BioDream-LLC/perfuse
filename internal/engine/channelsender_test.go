package engine

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// fakeRouter records what was routed where, and can be told to fail.
type fakeRouter struct {
	mu   sync.Mutex
	sent map[string][][]byte
	err  error
}

func newFakeRouter() *fakeRouter {
	return &fakeRouter{sent: map[string][][]byte{}}
}

func (f *fakeRouter) Route(_ context.Context, channel string, raw []byte) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	// Copied, because the caller owns the buffer and a test that shares it would pass for the wrong
	// reason.
	body := make([]byte, len(raw))
	copy(body, raw)
	f.sent[channel] = append(f.sent[channel], body)
	return nil
}

func (f *fakeRouter) count(channel string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.sent[channel])
}

func channelDest(name, target string) config.Destination {
	return config.Destination{
		Name:    name,
		Type:    config.DestinationChannel,
		Channel: &config.ChannelDestination{Name: target},
	}
}

func TestChannelSenderHandsTheMessageToTheNamedChannel(t *testing.T) {
	router := newFakeRouter()

	sender, err := NewChannelSender(channelDest("to-lab", "lab"), router)
	if err != nil {
		t.Fatal(err)
	}

	if err := sender.Send(context.Background(), []byte("MSH|^~\\&|A|B\r")); err != nil {
		t.Fatal(err)
	}
	if router.count("lab") != 1 {
		t.Errorf("the lab channel received %d messages, want 1", router.count("lab"))
	}
}

func TestChannelSenderNamesTheTargetInAFailure(t *testing.T) {
	// Without the name, a failure deep in a chain of channels produces an error that could have come
	// from any of them.
	router := newFakeRouter()
	router.err = errors.New("the child is stopped")

	sender, err := NewChannelSender(channelDest("to-lab", "lab"), router)
	if err != nil {
		t.Fatal(err)
	}

	err = sender.Send(context.Background(), []byte("MSH|^~\\&|A|B\r"))
	if err == nil {
		t.Fatal("a failed route reported success")
	}
	if !strings.Contains(err.Error(), "lab") {
		t.Errorf("the error does not name the target channel: %v", err)
	}
	if !strings.Contains(err.Error(), "the child is stopped") {
		t.Errorf("the underlying reason was lost: %v", err)
	}
}

func TestAChannelDestinationWithoutARouterRefusesToBeBuilt(t *testing.T) {
	// Refused at build rather than at send. A channel that cannot possibly deliver should not start
	// and then fail every message, because the operator would see a running channel losing traffic
	// instead of a channel that refused to start with a reason.
	_, err := NewChannelSender(channelDest("to-lab", "lab"), nil)
	if err == nil {
		t.Fatal("a channel destination was built with no router")
	}
	if !errors.Is(err, ErrNoRouter) {
		t.Errorf("the error is not ErrNoRouter, so a caller cannot tell this apart from a "+
			"configuration mistake: %v", err)
	}
}

func TestAChannelDestinationNeedsATarget(t *testing.T) {
	if _, err := NewChannelSender(config.Destination{
		Name: "to-nothing",
		Type: config.DestinationChannel,
	}, newFakeRouter()); err == nil {
		t.Fatal("a channel destination with no target was built")
	}
}

func TestChannelSenderDescribesItselfWithoutPretendingToBeATransport(t *testing.T) {
	// It appears in specifications and summaries, where calling it an address would be wrong: nothing
	// travels over a network.
	sender, err := NewChannelSender(channelDest("to-lab", "lab"), newFakeRouter())
	if err != nil {
		t.Fatal(err)
	}

	got := sender.Describe()
	if !strings.Contains(got, "lab") {
		t.Errorf("the description does not name the target: %q", got)
	}
	if strings.Contains(got, "://") || strings.Contains(got, ":25") {
		t.Errorf("the description looks like a network address: %q", got)
	}
}

func TestClosingAChannelSenderDoesNotStopTheTargetChannel(t *testing.T) {
	// The target's lifetime belongs to whatever started it. Other channels may also be routing to it,
	// and other people may be watching it.
	router := newFakeRouter()
	sender, err := NewChannelSender(channelDest("to-lab", "lab"), router)
	if err != nil {
		t.Fatal(err)
	}

	if err := sender.Close(); err != nil {
		t.Fatal(err)
	}
	if err := sender.Send(context.Background(), []byte("MSH|^~\\&|A|B\r")); err != nil {
		t.Fatalf("the target became unusable after the sender was closed: %v", err)
	}
}

func TestTheRoutingFactoryBuildsEverythingElseAsUsual(t *testing.T) {
	// Wrapping rather than duplicating means a new destination type is available to a routing server
	// the moment it exists, with nothing to remember.
	factory := RoutingSenderFactory(newFakeRouter())

	file, err := factory(config.Destination{
		Name: "out",
		Type: config.DestinationFile,
		Dir:  t.TempDir(),
	})
	if err != nil {
		t.Fatalf("the routing factory could not build a file destination: %v", err)
	}
	defer func() { _ = file.Close() }()

	routed, err := factory(channelDest("to-lab", "lab"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := routed.(*ChannelSender); !ok {
		t.Errorf("a channel destination built a %T", routed)
	}
}

func TestRoutingDeliversThroughAWholeChannel(t *testing.T) {
	// The property that makes this worth having: a routed message goes in at the top, so the child's
	// filter and transformations run. A child that only received raw bytes at its destinations would
	// behave differently depending on where its traffic came from.
	child := captureDest{}

	childCfg := &config.Channel{
		Name:   "child",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Transformations: []transform.Step{{
			Description: "mark it",
			Set:         &transform.SetStep{Path: "PID-8", Value: "M"},
		}},
		Destinations: []config.Destination{{Name: "out", Type: config.DestinationFile, Dir: t.TempDir()}},
	}
	if err := childCfg.Validate(); err != nil {
		t.Fatal(err)
	}

	childChannel, err := NewChannel(childCfg, func(config.Destination) (Sender, error) {
		return &child, nil
	}, quiet())
	if err != nil {
		t.Fatal(err)
	}

	// A router that resolves the one name to the channel just built.
	router := funcRouter(func(ctx context.Context, name string, raw []byte) error {
		if name != "child" {
			return errors.New("no such channel")
		}
		_, err := childChannel.Route(ctx, raw)
		return err
	})

	sender, err := NewChannelSender(channelDest("to-child", "child"), router)
	if err != nil {
		t.Fatal(err)
	}

	msg := []byte("MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C1|P|2.5\rPID|1||123||SMITH^JOHN||19700101|1\r")
	if err := sender.Send(context.Background(), msg); err != nil {
		t.Fatal(err)
	}

	got := child.messages()
	if len(got) != 1 {
		t.Fatalf("the child channel delivered %d messages, want 1", len(got))
	}
	// The child's transformation ran, which is the whole point of routing into the top of a channel
	// rather than straight to its destinations.
	if !strings.Contains(string(got[0]), "|M\r") && !strings.Contains(string(got[0]), "|M") {
		t.Errorf("the child's transformation did not run: %q", got[0])
	}
}

// funcRouter adapts a function to ChannelRouter.
type funcRouter func(ctx context.Context, name string, raw []byte) error

func (f funcRouter) Route(ctx context.Context, name string, raw []byte) error {
	return f(ctx, name, raw)
}
