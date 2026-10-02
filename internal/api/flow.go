package api

import (
	"net/http"
	"sort"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/spec"
	"github.com/biodream-llc/perfuse/internal/store"
)

// The flow map: the shape of the site, and what was moving through it at any moment in a window.
//
// The dashboard answers "is it working now". This answers "what was it doing at 03:40", which is the question asked after the fact, when
// somebody has a complaint from a ward and a time. Scrubbing back through a window and watching one strand go dark while the others keep
// running is a faster diagnosis than reading four charts and comparing timestamps.
//
// The map is built from two sources deliberately. The strands and their traffic come from the message store, so they describe what
// happened. The nodes come from the loaded configuration, so a destination that has never received a message still appears - a strand
// that has been dark since it was configured is a real finding and the one most easily missed, because there is no traffic anywhere to
// draw attention to it.

// flowNode is one channel on the map, with its configured destinations and what it does in words.
type flowNode struct {
	Channel string `json:"channel"`
	Running bool   `json:"running"`
	// Broken means the file did not load, so this channel is not processing anything regardless of what the map shows.
	Broken bool   `json:"broken"`
	Reason string `json:"reason,omitempty"`

	// Narration is what this channel does, in sentences, so the map can explain a strand rather than only draw it.
	//
	// Alongside the map because the two questions arrive together: somebody looking at a dark strand needs to know what that strand
	// was meant to be doing before they can tell whether dark is wrong. Sending them to a different tab to find out loses the time
	// the map just saved them.
	Narration []string `json:"narration"`

	// Destinations as configured, whether or not anything has been sent to them.
	Destinations []flowDestination `json:"destinations"`

	// Received is the arrival series for this channel, zero-filled across the window.
	Received []msgstore.Bucket `json:"received"`
}

// flowDestination is one configured destination and its series.
type flowDestination struct {
	Name string `json:"name"`
	// How describes the transport in words, never with credentials.
	How string `json:"how"`
	// Durable says whether a message survives the receiver being unavailable, which decides whether a dark strand is data loss.
	Durable bool `json:"durable"`
	// Buckets is the delivery series, zero-filled. Empty of traffic is not the same as absent.
	Buckets []msgstore.Bucket `json:"buckets"`
	// EverUsed distinguishes a strand that stopped from one that never started.
	EverUsed bool `json:"everUsed"`
}

type flowResponse struct {
	Since  time.Time  `json:"since"`
	Until  time.Time  `json:"until"`
	Bucket string     `json:"bucket"`
	Window string     `json:"window"`
	Nodes  []flowNode `json:"nodes"`
}

// handleFlow returns the flow map over a window.
func (s *Server) handleFlow(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}

	if !s.requireMessages(w, r) {
		return
	}

	q := r.URL.Query()
	window := time.Hour
	if v := q.Get("window"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			window = d
		}
	}
	// A week is generous for a flow map. Beyond that, the SQL scan touches the
	// whole table and the output is capped at 5000 buckets regardless.
	const maxWindow = 7 * 24 * time.Hour
	if window > maxWindow {
		window = maxWindow
	}
	bucket := time.Minute
	if v := q.Get("bucket"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			bucket = d
		}
	}
	// A sub-second bucket truncates to zero seconds, causing division by zero in
	// the SQL bucketing expression.
	if bucket < time.Second {
		bucket = time.Second
	}

	since := time.Now().UTC().Add(-window)

	flow, err := rt.Messages.Flow(r.Context(), string(sess.TenantID), since, bucket)
	if err != nil {
		s.failErr(w, r, err)

		return
	}

	// Index the traffic so the configured nodes can be matched against it.
	traffic := map[string]msgstore.FlowChannel{}
	for _, c := range flow.Channels {
		traffic[c.Channel] = c
	}

	out := flowResponse{
		Since:  since,
		Until:  time.Now().UTC(),
		Bucket: bucket.String(),
		Window: window.String(),
		Nodes:  []flowNode{},
	}

	for _, name := range s.flowChannelNames(rt, traffic) {
		out.Nodes = append(out.Nodes, s.flowNodeFor(rt, name, traffic[name], since, bucket))
	}

	s.ok(w, out)
}

// flowChannelNames lists every channel that should appear, from configuration and from traffic.
//
// The union, not either one alone. Configuration alone would omit a channel whose file has since been removed while its messages are
// still in the store and still being asked about. Traffic alone would omit a channel that has never received anything, which is the
// state that most needs to be visible.
func (s *Server) flowChannelNames(rt *Runtime, traffic map[string]msgstore.FlowChannel) []string {
	seen := map[string]bool{}
	valid, broken, err := rt.Repo.List()
	if err == nil {
		for _, c := range valid {
			seen[c.Name] = true
		}
		// A file that will not load is a node on the map too. Omitting it would draw a site with a hole in it and no explanation,
		// which is the failure this codebase has already been bitten by: a channel that will not load being invisible everywhere.
		for _, name := range broken {
			seen[name] = true
		}
	}
	for name := range traffic {
		seen[name] = true
	}

	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)

	return out
}

// flowNodeFor builds one node from its configuration and its traffic.
func (s *Server) flowNodeFor(rt *Runtime, name string, seen msgstore.FlowChannel, since time.Time, bucket time.Duration) flowNode {
	node := flowNode{
		Channel:      name,
		Narration:    []string{},
		Destinations: []flowDestination{},
		Received:     seen.Received,
	}
	if node.Received == nil {
		node.Received = []msgstore.Bucket{}
	}

	// Index this channel's strands by destination.
	strands := map[string][]msgstore.Bucket{}
	for _, st := range seen.Strands {
		strands[st.Destination] = st.Buckets
	}

	cfg, cfgErr := rt.Repo.Get(name)
	if cfgErr != nil || cfg == nil {
		// Traffic with no configuration behind it. Said plainly rather than drawn as a healthy node, because a map that shows a
		// channel which is not running as though it were is worse than one that omits it.
		node.Broken = true
		node.Reason = "this channel is not loaded, so nothing is being processed for it now"
		if cfgErr != nil {
			node.Reason = "this channel file will not load, so nothing is being processed for it: " + cfgErr.Error()
		}
		node.Narration = []string{"This channel is not running. Anything drawn here is what it did before it stopped loading."}

		for _, dest := range sortedKeys(strands) {
			node.Destinations = append(node.Destinations, flowDestination{
				Name:     dest,
				How:      "no longer configured",
				Buckets:  strands[dest],
				EverUsed: anyTraffic(strands[dest]),
			})
		}

		return node
	}

	node.Running = rt.IsRunning(name)
	node.Narration = spec.Build(cfg).Narration

	for _, d := range cfg.Destinations {
		buckets := strands[d.Name]
		if buckets == nil {
			// Zero-filled rather than empty, so a destination that has never received a message sits on the same time axis as
			// the ones that have. An empty series would draw as nothing, and nothing is indistinguishable from a strand that
			// was never configured - which is the one thing this is trying to tell somebody.
			buckets = zeroSeries(since, bucket)
		}

		node.Destinations = append(node.Destinations, flowDestination{
			Name:     d.Name,
			How:      spec.DescribeTransport(d),
			Durable:  d.Queue != nil && d.Queue.Enabled,
			Buckets:  buckets,
			EverUsed: anyTraffic(buckets),
		})
	}

	// A destination that has traffic but is no longer in the file. Kept, because the messages are still in the store and somebody
	// asking about them deserves to see where they went rather than an incomplete map.
	for _, dest := range sortedKeys(strands) {
		if configuredDestination(cfg, dest) {
			continue
		}
		node.Destinations = append(node.Destinations, flowDestination{
			Name:     dest,
			How:      "no longer configured",
			Buckets:  strands[dest],
			EverUsed: anyTraffic(strands[dest]),
		})
	}

	return node
}

// configuredDestination reports whether a destination is still in the channel file.
func configuredDestination(cfg *config.Channel, name string) bool {
	for _, d := range cfg.Destinations {
		if d.Name == name {
			return true
		}
	}

	return false
}

// zeroSeries builds an all-zero series across the window, on the same bucket boundaries as the store uses.
func zeroSeries(since time.Time, bucket time.Duration) []msgstore.Bucket {
	seconds := int64(bucket.Seconds())
	if seconds <= 0 {
		seconds = 60
	}

	first := (since.UTC().Unix() / seconds) * seconds
	last := (time.Now().UTC().Unix() / seconds) * seconds

	const maxBuckets = 5000
	if last > first && (last-first)/seconds > maxBuckets {
		first = last - maxBuckets*seconds
	}

	out := make([]msgstore.Bucket, 0, (last-first)/seconds+1)
	for at := first; at <= last; at += seconds {
		out = append(out, msgstore.Bucket{Start: time.Unix(at, 0).UTC()})
	}

	return out
}

// anyTraffic reports whether a series contains anything at all.
func anyTraffic(buckets []msgstore.Bucket) bool {
	for _, b := range buckets {
		if b.Total > 0 {
			return true
		}
	}

	return false
}

// sortedKeys returns map keys in order, because Go ranges them randomly and this reaches a screen.
func sortedKeys(m map[string][]msgstore.Bucket) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)

	return out
}
