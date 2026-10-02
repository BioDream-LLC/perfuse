package peers

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"
)

// Config is the fleet configuration for one instance.
//
// Mirth needed a separate product for this - a plug-in registering with a Command Center server - because its
// administrator is a desktop application and each install is its own JVM, so there was nowhere in the design to put
// a fleet view. Perfuse already ships the console inside every binary, so any instance can be the aggregator and
// this is a configuration file rather than a product.
type Config struct {
	// Label names this instance in a fleet view. Defaults to the hostname.
	//
	// Configured rather than derived from anything in a message, following the rule that user-visible labels never
	// come from message content.
	Label string `yaml:"label,omitempty"`

	// Peers are the other instances to poll.
	Peers []Peer `yaml:"peers"`

	// PollEvery is how often to poll each peer. Defaults to 15 seconds.
	PollEvery time.Duration `yaml:"poll_every,omitempty"`

	// Timeout is how long to wait for one peer. Defaults to 5 seconds.
	//
	// Kept well below PollEvery so a slow peer cannot delay the whole round: an unreachable instance is exactly
	// when a fleet view matters most, and a view that stops updating because one member is wedged is worse than
	// none.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// Peer is one other Perfuse instance.
type Peer struct {
	// Name identifies the peer in the interface. Required, because a URL is not a name a human recognises at
	// three in the morning.
	Name string `yaml:"name"`

	// URL is the base address of the peer, for example https://perfuse-02.hospital.internal:8443.
	URL string `yaml:"url"`

	// Token authenticates to the peer. It should be a viewer-scoped token: the fleet view only reads.
	Token string `yaml:"token,omitempty"`

	// AllowControl permits starting and stopping that peer's channels from here.
	//
	// Off by default and audited by name when used. Reading another instance's health is a small privilege;
	// stopping its channels is not, and the two should not arrive together silently.
	AllowControl bool `yaml:"allow_control,omitempty"`

	// InsecureSkipVerify accepts the peer's TLS certificate without verifying it.
	//
	// Present because internal hospital infrastructure runs on private certificate authorities and refusing to
	// work at all would just push people onto plain HTTP, which is worse. Named so it cannot be mistaken for a
	// good idea.
	InsecureSkipVerify bool `yaml:"insecure_skip_verify,omitempty"`
}

// Defaults for anything left unset.
const (
	DefaultPollEvery = 15 * time.Second
	DefaultTimeout   = 5 * time.Second
)

// Validate fills defaults and refuses a configuration that cannot work.
//
// Refused at load rather than reported at run time, following the house rule: a fleet view that silently drops a
// peer it could not understand is a fleet view that lies about how many servers you have.
func (c *Config) Validate() error {
	if c.PollEvery == 0 {
		c.PollEvery = DefaultPollEvery
	}
	if c.Timeout == 0 {
		c.Timeout = DefaultTimeout
	}

	if c.PollEvery < time.Second {
		return fmt.Errorf("poll_every is %s, which is too frequent to be useful and will spend a peer's "+
			"capacity answering status requests; one second is the floor", c.PollEvery)
	}
	if c.Timeout >= c.PollEvery {
		return fmt.Errorf("timeout (%s) must be shorter than poll_every (%s), or a slow peer will delay every "+
			"round and the whole view will stop updating just when it matters", c.Timeout, c.PollEvery)
	}

	seenName := map[string]string{}
	seenURL := map[string]string{}

	for i := range c.Peers {
		p := &c.Peers[i]

		p.Name = strings.TrimSpace(p.Name)
		p.URL = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(p.URL), "/"))

		if p.Name == "" {
			return fmt.Errorf("peer %d has no name; a URL is not something anybody recognises in an incident", i+1)
		}
		if p.URL == "" {
			return fmt.Errorf("peer %q has no url", p.Name)
		}

		u, err := url.Parse(p.URL)
		if err != nil {
			return fmt.Errorf("peer %q has an unparseable url %q: %w", p.Name, p.URL, err)
		}
		if u.Host == "" {
			return fmt.Errorf("peer %q has url %q, which has no host; it needs a scheme too, as in "+
				"https://host:8443", p.Name, p.URL)
		}

		switch u.Scheme {
		case "https":
		case "http":
			// The same rule already applied to fhir.url. A token that authenticates to another instance travels
			// on every poll, and over plain HTTP to another machine it travels in clear text across the hospital
			// network. Localhost is exempt because there is no network to cross.
			if !isLoopback(u.Hostname()) {
				return fmt.Errorf("peer %q uses http:// to %s, which would send its token across the network "+
					"in clear text on every poll; use https, or point at localhost if you are testing",
					p.Name, u.Hostname())
			}
		default:
			return fmt.Errorf("peer %q has scheme %q; only http and https are supported", p.Name, u.Scheme)
		}

		if p.Token == "" {
			return fmt.Errorf("peer %q has no token, so every poll would be rejected and the peer would show as "+
				"unreachable when it is merely unauthenticated - which is a materially different problem",
				p.Name)
		}

		if prev, dup := seenName[strings.ToLower(p.Name)]; dup {
			return fmt.Errorf("two peers are both named %q; names identify instances in the interface and a "+
				"duplicate makes one of them invisible (%s and %s)", p.Name, prev, p.URL)
		}
		seenName[strings.ToLower(p.Name)] = p.URL

		if prev, dup := seenURL[p.URL]; dup {
			return fmt.Errorf("peers %q and %q have the same url %s, so one instance would be counted twice "+
				"and the fleet would look larger than it is", prev, p.Name, p.URL)
		}
		seenURL[p.URL] = p.Name
	}

	// Sorted so the interface, and anything diffing its output, sees a stable order regardless of file order.
	sort.SliceStable(c.Peers, func(i, j int) bool {
		return strings.ToLower(c.Peers[i].Name) < strings.ToLower(c.Peers[j].Name)
	})

	return nil
}

// Controllable reports whether any peer permits remote control, which the interface uses to decide whether to offer
// it at all rather than showing buttons that will be refused.
func (c *Config) Controllable() bool {
	for _, p := range c.Peers {
		if p.AllowControl {
			return true
		}
	}
	return false
}

func isLoopback(host string) bool {
	switch host {
	case "localhost", "127.0.0.1", "::1", "[::1]":
		return true
	}
	return strings.HasPrefix(host, "127.")
}
