// Package egress decides whether this engine may connect outward to an address.
//
// It exists because a destination can be pointed anywhere, and one combination turns that into a way to read the internal
// network: a SOAP destination with a response transformer returns the receiver's reply to a script, so an editor can
// author a channel that fetches an internal URL and writes the body into a log or a message.
//
// Demonstrated rather than theorised: a SOAP destination aimed at a local HTTP server put that server's response body
// into a channel log line.
//
// The proportionate answer is not to forbid outbound connections - delivering messages to configured endpoints is the
// entire purpose of the software, and an editor is meant to configure them. It is to refuse the specific addresses that
// are never a message destination and are always a credential store.
package egress

import (
	"fmt"
	"net"
	"strings"
)

// blockedRanges are refused by default.
//
// Each one is here because it holds credentials or infrastructure control rather than because it is "internal". An
// ordinary private address is not blocked: hospital systems live on private networks, and refusing 10.0.0.0/8 would
// refuse nearly every real deployment.
var blockedRanges = []struct {
	cidr   string
	reason string
	net    *net.IPNet
}{
	{
		cidr: "169.254.169.254/32",
		// The single most valuable address on a cloud host. On AWS, Azure and Google it serves the instance's own
		// role credentials to anything that can make an HTTP request from the machine.
		reason: "the cloud instance metadata service, which serves this machine's own credentials",
	},
	{
		cidr:   "169.254.0.0/16",
		reason: "link-local addressing, which is never a message destination",
	},
	{
		cidr:   "fe80::/10",
		reason: "IPv6 link-local addressing, which is never a message destination",
	},
	{
		cidr: "100.100.100.200/32",
		// Alibaba Cloud's equivalent of the address above. Included because a blocklist that covers only the
		// providers somebody happened to think of is the kind of gap that looks complete.
		reason: "the Alibaba Cloud instance metadata service",
	},
	{
		cidr:   "192.0.0.192/32",
		reason: "the Oracle Cloud instance metadata service",
	},
}

func init() {
	for i := range blockedRanges {
		_, parsed, err := net.ParseCIDR(blockedRanges[i].cidr)
		if err != nil {
			// A malformed constant in this file is a programming error, and starting with a silently empty
			// blocklist is the failure this whole package exists to prevent.
			panic(fmt.Sprintf("egress: %q is not a valid CIDR: %v", blockedRanges[i].cidr, err))
		}
		blockedRanges[i].net = parsed
	}
}

// Policy decides which addresses may be reached.
type Policy struct {
	// Allow lists the hosts and CIDRs this engine may connect to. Empty allows anything not blocked.
	//
	// For a deployment that wants a positive list rather than a blocklist. Empty is the default because a positive
	// list has to be complete before anything works, and an integration engine at a hospital talks to dozens of
	// systems - so requiring it would mean nobody uses it and everybody turns it off.
	Allow []string

	// AllowMetadata permits the addresses this package blocks by default.
	//
	// Named for what it does rather than "insecure", because somebody genuinely running a service on a link-local
	// address should be able to say so - and should have to say so.
	//
	// Deliberately not a channel setting. It is set by whoever runs the process, with -allow-metadata-egress, because
	// a channel author is exactly the person this control restrains: if a channel could grant itself the exemption,
	// the control would only stop somebody who was not trying.
	AllowMetadata bool
}

// Check reports whether an address may be reached, without touching the network.
//
// Only literal addresses are examined. A hostname is passed, because resolving it here would make this function do DNS -
// and this runs during channel validation, where that is the wrong tradeoff twice over: perfuse check would take seconds
// per destination, and a channel's validity would depend on whether DNS answered at the moment somebody looked. A system
// being unreachable is not the same as a channel being wrong.
//
// A hostname that resolves to a blocked address is caught by CheckResolving, which the engine calls when a channel starts
// - a moment when the network is necessarily involved anyway.
func (p Policy) Check(host string) error {
	return p.check(host, false)
}

// CheckResolving is Check, and also resolves hostnames.
//
// For the moment a channel starts, where a name pointing at a metadata address should be refused and the network is
// already being used. Deliberately not a defence against a name that resolves differently between this check and the
// connection itself - that race needs the check inside the dialler.
func (p Policy) CheckResolving(host string) error {
	return p.check(host, true)
}

func (p Policy) check(host string, resolveNames bool) error {
	host = strings.TrimSpace(host)
	if host == "" {
		return fmt.Errorf("no host to connect to")
	}

	// A bracketed IPv6 literal, or a host:port pair, reduced to the host.
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")

	var addrs []net.IP
	if ip := net.ParseIP(host); ip != nil {
		addrs = []net.IP{ip}
	} else if resolveNames {
		addrs = resolve(host)
	}

	if len(addrs) == 0 {
		// Not refused. A name that does not resolve now may resolve later - a system that is down, a DNS entry not
		// yet created - and refusing would make a channel's validity depend on the state of the network.
		return nil
	}

	for _, ip := range addrs {
		if err := p.checkIP(ip, host); err != nil {
			return err
		}
	}

	if len(p.Allow) > 0 {
		for _, ip := range addrs {
			if !p.allowed(ip, host, resolveNames) {
				return fmt.Errorf("%s resolves to %s, which is not in this engine's egress allow list",
					host, ip)
			}
		}
	}

	return nil
}

// checkIP refuses a blocked address.
func (p Policy) checkIP(ip net.IP, host string) error {
	if p.AllowMetadata {
		return nil
	}

	for _, blocked := range blockedRanges {
		if blocked.net.Contains(ip) {
			if host != ip.String() {
				return fmt.Errorf("%s resolves to %s, which is %s; set allow_metadata to permit it "+
					"deliberately", host, ip, blocked.reason)
			}
			return fmt.Errorf("%s is %s; start the server with -allow-metadata-egress to permit it "+
				"deliberately", ip, blocked.reason)
		}
	}

	return nil
}

// allowed reports whether an address is in the positive list.
func (p Policy) allowed(ip net.IP, host string, resolveNames bool) bool {
	for _, entry := range p.Allow {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}

		// A CIDR.
		if strings.Contains(entry, "/") {
			if _, network, err := net.ParseCIDR(entry); err == nil && network.Contains(ip) {
				return true
			}
			continue
		}

		// A literal address.
		if parsed := net.ParseIP(entry); parsed != nil {
			if parsed.Equal(ip) {
				return true
			}
			continue
		}

		// A hostname, compared case-insensitively. Also resolved, so an entry naming a host covers whatever that
		// host currently is rather than only matching the spelling.
		if strings.EqualFold(entry, host) {
			return true
		}
		if resolveNames {
			for _, entryIP := range resolve(entry) {
				if entryIP.Equal(ip) {
					return true
				}
			}
		}
	}

	return false
}

// resolve turns a host into addresses, returning nothing rather than an error.
func resolve(host string) []net.IP {
	if ip := net.ParseIP(host); ip != nil {
		return []net.IP{ip}
	}

	addrs, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	return addrs
}

// BlockedDescriptions lists what is refused by default, for documentation and a startup log.
func BlockedDescriptions() []string {
	out := make([]string, 0, len(blockedRanges))
	for _, blocked := range blockedRanges {
		out = append(out, blocked.cidr+" ("+blocked.reason+")")
	}
	return out
}

// Default is the policy used where no policy has been passed explicitly.
//
// A package-level value rather than a parameter threaded through every destination's validation, because it is set exactly
// once - by whoever starts the process, before any channel is read - and read from many places. Nothing is concurrent at
// the point it is set.
//
// It exists so that the exemption cannot be a channel setting. A channel author is precisely the person this control
// restrains, and a control somebody can grant themselves only stops the people who were not trying.
var Default Policy

// SetDefault replaces the process-wide policy.
//
// Returns what it replaced, so a test can restore it and so a caller can log what changed.
func SetDefault(p Policy) Policy {
	previous := Default
	Default = p
	return previous
}
