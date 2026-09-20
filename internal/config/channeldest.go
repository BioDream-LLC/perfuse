package config

import (
	"fmt"
	"sort"
	"strings"
)

// Routing a message to another channel.
//
// The configuration is deliberately tiny - a channel name and nothing else - because everything that
// would otherwise be options here already exists on the receiving channel. Where a routed message
// goes, what it is filtered on and how it is transformed are the child channel's business, and
// duplicating any of that on the sending side would create two places to look.

// ChannelDestination routes to another channel in the same server.
type ChannelDestination struct {
	// Name is the channel to route to.
	Name string `yaml:"name"`
}

// validateChannelDest checks a channel destination in isolation.
//
// Whether the named channel exists, and whether routing to it makes a loop, cannot be answered from
// one file. Those are checked across the whole set by ValidateRouting.
func validateChannelDest(d *Destination) []error {
	var errs []error

	if d.Channel == nil {
		return []error{fmt.Errorf("destination %q is a channel destination but has no channel block", d.Name)}
	}
	if strings.TrimSpace(d.Channel.Name) == "" {
		errs = append(errs, fmt.Errorf("destination %q needs the name of the channel to route to", d.Name))
	}

	return errs
}

// ValidateRouting checks channel-to-channel routing across a whole set of channels.
//
// Separate from loading one file because none of it can be decided from one file. It is called after a
// directory is loaded and before anything starts.
//
// Two things are refused here, and both would otherwise be discovered at run time in the worst
// possible way:
//
// A destination naming a channel that does not exist. At run time that is a channel which starts
// happily and then fails every message, and the operator sees traffic being lost rather than a
// configuration mistake.
//
// A cycle. A routes to B routes to A means one message becomes an unbounded number, and because each
// hop is a real delivery with real recording and real metrics, it fills the message store and the
// disk before anybody works out why. A depth limit at run time would stop the damage but would still
// let somebody build the loop; refusing it at load means it cannot be built.
func ValidateRouting(channels []*Channel) []error {
	var errs []error

	known := map[string]bool{}
	for _, c := range channels {
		known[c.Name] = true
	}

	// Sorted so the same set of channels always reports the same problems in the same order. These
	// go into logs that get compared.
	sorted := make([]*Channel, len(channels))
	copy(sorted, channels)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Name < sorted[j].Name })

	routes := map[string][]string{}
	for _, c := range sorted {
		for _, d := range c.Destinations {
			if d.Type != DestinationChannel || d.Channel == nil {
				continue
			}
			target := d.Channel.Name

			if target == c.Name {
				// Named separately from a longer cycle because it is almost always a typo, and
				// "this channel routes to itself" is a more useful sentence than a two-name cycle.
				errs = append(errs, fmt.Errorf(
					"channel %q routes to itself through destination %q; a message would never stop",
					c.Name, d.Name))
				continue
			}

			if !known[target] {
				errs = append(errs, fmt.Errorf(
					"channel %q has a destination %q routing to channel %q, which does not exist",
					c.Name, d.Name, target))
				continue
			}

			// A disabled destination still counts. It is one edit away from live, and a loop that
			// appears the moment somebody ticks a box is worse than one that never loaded, because
			// by then nobody is looking at the routing.
			routes[c.Name] = append(routes[c.Name], target)
		}
	}

	for _, cycle := range findCycles(routes) {
		errs = append(errs, fmt.Errorf(
			"routing between channels goes in a circle: %s; a message entering it would never stop",
			strings.Join(cycle, " -> ")))
	}

	return errs
}

// findCycles reports every distinct cycle in the routing graph, each named once.
func findCycles(routes map[string][]string) [][]string {
	names := make([]string, 0, len(routes))
	for name := range routes {
		names = append(names, name)
	}
	// Sorted, because a map ranges randomly and the same broken configuration must produce the same
	// message every time somebody tries to start it.
	sort.Strings(names)

	var found [][]string
	seen := map[string]bool{}

	// Colour-marking depth-first search. grey means on the current path, black means finished.
	const (
		white = 0
		grey  = 1
		black = 2
	)
	colour := map[string]int{}

	var path []string
	var walk func(name string)
	walk = func(name string) {
		colour[name] = grey
		path = append(path, name)

		targets := append([]string(nil), routes[name]...)
		sort.Strings(targets)

		for _, target := range targets {
			switch colour[target] {
			case grey:
				// Found a cycle. Report it starting from the repeated name so the same cycle reads
				// the same way whichever member happened to be visited first.
				at := 0
				for i, p := range path {
					if p == target {
						at = i
						break
					}
				}
				cycle := append(append([]string(nil), path[at:]...), target)

				key := strings.Join(cycle, ">")
				if !seen[key] {
					seen[key] = true
					found = append(found, cycle)
				}
			case white:
				walk(target)
			}
		}

		path = path[:len(path)-1]
		colour[name] = black
	}

	for _, name := range names {
		if colour[name] == white {
			walk(name)
		}
	}

	return found
}
