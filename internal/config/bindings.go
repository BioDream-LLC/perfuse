package config

import (
	"fmt"
	"net"
	"sort"
	"strings"
)

// Bindings are the ports a set of channels wants to listen on.
//
// This exists because of a failure that is otherwise found at runtime, by one feed
// silently not working. Two channels asking for :2575 is not a configuration error
// either of them can see on its own - each is perfectly valid - and the second one
// to start gets "address already in use", logs it, and stops. A sending hospital
// then gets connection refused for a channel the dashboard shows as configured.
//
// In multi-tenant operation it is worse, because the two channels belong to
// different organisations and neither operator can see the other's configuration. So
// the conflict has to be refused at load with both names, before anything binds.
type Bindings struct {
	// byAddr maps a normalised address to what claimed it.
	byAddr map[string][]Claim
}

// Claim is one channel's request for an address.
type Claim struct {
	// Tenant is empty in single-tenant operation.
	Tenant  string
	Channel string
	Addr    string
	// Source names the kind of listener, so the message can say what will break.
	Source string
}

// Label describes a claim for an error message.
func (c Claim) Label() string {
	if c.Tenant == "" {
		return fmt.Sprintf("channel %q", c.Channel)
	}
	return fmt.Sprintf("tenant %q channel %q", c.Tenant, c.Channel)
}

// NewBindings starts an empty set.
func NewBindings() *Bindings {
	return &Bindings{byAddr: map[string][]Claim{}}
}

// Add records what a channel wants to listen on.
//
// A channel with no listener - a database poller, an SFTP poller, a file reader -
// contributes nothing and is not an error.
func (b *Bindings) Add(tenantID string, c *Channel) {
	addr, kind := listenerOf(c)
	if addr == "" {
		return
	}
	key := normaliseAddr(addr)
	b.byAddr[key] = append(b.byAddr[key], Claim{
		Tenant: tenantID, Channel: c.Name, Addr: addr, Source: kind,
	})
}

// listenerOf returns the address a channel binds, and what kind of listener it is.
func listenerOf(c *Channel) (addr, kind string) {
	switch c.Source.Type {
	case SourceMLLP:
		return c.Source.Listen, "MLLP"
	case SourceHTTP:
		if c.Source.HTTP != nil {
			return c.Source.HTTP.Listen, "HTTP"
		}
	}
	// database and sftp sources poll outward and bind nothing.
	return "", ""
}

// normaliseAddr makes addresses that mean the same thing compare equal.
//
// This is the part that makes the check actually useful. ":2575", "0.0.0.0:2575"
// and "[::]:2575" are all "every interface, port 2575" and all conflict with each
// other and with "127.0.0.1:2575". Comparing the strings literally would miss every
// one of those and the check would give false confidence, which is worse than not
// having it.
func normaliseAddr(addr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil {
		// Not parseable as host:port. Validation elsewhere reports that properly;
		// here it is compared as written so two identical unparseable values still
		// collide.
		return strings.TrimSpace(addr)
	}

	host = strings.TrimSpace(host)
	switch host {
	case "", "0.0.0.0", "[::]", "::", "*":
		host = "*"
	case "localhost":
		// Treated as the loopback address it resolves to, so localhost:2575 and
		// 127.0.0.1:2575 are seen to conflict.
		host = "127.0.0.1"
	}
	return host + ":" + port
}

// conflictsWith reports whether two normalised hosts contend for the same port.
//
// A wildcard bind takes the port on every interface, so it conflicts with any
// specific address on that port. Two different specific addresses on the same port
// do not conflict - binding 10.0.0.1:2575 and 10.0.0.2:2575 is legitimate and a
// check that refused it would stop a real deployment.
func conflictsWith(a, b string) bool {
	if a == b {
		return true
	}
	ah, ap, ok1 := splitNormalised(a)
	bh, bp, ok2 := splitNormalised(b)
	if !ok1 || !ok2 || ap != bp {
		return false
	}
	return ah == "*" || bh == "*"
}

func splitNormalised(s string) (host, port string, ok bool) {
	i := strings.LastIndex(s, ":")
	if i < 0 {
		return "", "", false
	}
	return s[:i], s[i+1:], true
}

// Check reports every conflict found.
//
// All of them, not the first. An operator fixing a fifty-channel configuration
// should not have to restart fifty times to discover fifty problems.
func (b *Bindings) Check() error {
	// Grouped by port so a wildcard and a specific address on the same port are
	// compared, which a straight map lookup by key would miss.
	type pair struct{ a, b Claim }
	var conflicts []pair

	keys := make([]string, 0, len(b.byAddr))
	for k := range b.byAddr {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	// Same normalised address: every claim after the first conflicts.
	for _, k := range keys {
		claims := b.byAddr[k]
		for i := 1; i < len(claims); i++ {
			conflicts = append(conflicts, pair{claims[0], claims[i]})
		}
	}

	// Different addresses that still contend, which is the wildcard case.
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if conflictsWith(keys[i], keys[j]) {
				conflicts = append(conflicts, pair{b.byAddr[keys[i]][0], b.byAddr[keys[j]][0]})
			}
		}
	}

	if len(conflicts) == 0 {
		return nil
	}

	var sb strings.Builder
	sb.WriteString("two channels cannot listen on the same address:\n")
	for _, c := range conflicts {
		fmt.Fprintf(&sb, "  %s wants %s (%s) and %s wants %s (%s)\n",
			c.a.Label(), c.a.Addr, c.a.Source,
			c.b.Label(), c.b.Addr, c.b.Source)
	}
	sb.WriteString("\nOnly one of them would start; the other would fail with \"address already in use\" " +
		"and stop, while still appearing configured. A sender pointed at it gets connection refused. " +
		"Give each channel its own port.")

	// Said at load rather than discovered at bind, because the sender's operator
	// experiences the runtime version as an unexplained outage on our side.
	return fmt.Errorf("%s", sb.String())
}

// Claims returns every recorded claim, sorted, for display.
func (b *Bindings) Claims() []Claim {
	var out []Claim
	for _, cs := range b.byAddr {
		out = append(out, cs...)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Tenant != out[j].Tenant {
			return out[i].Tenant < out[j].Tenant
		}
		return out[i].Channel < out[j].Channel
	})
	return out
}
