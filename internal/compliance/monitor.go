package compliance

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"
)

// Watching access to patient data.
//
// Four things are watched, and each is something a breach or a misconfiguration looks like before anybody reports it:
//
//   - a channel carrying far more than it normally does at this hour (a bulk extract leaving through a feed),
//   - a daytime-only channel carrying traffic at night,
//   - a channel sending to an address it has never sent to (a feed re-pointed, by mistake or not),
//   - one person reading many different patients' records within a minute (browsing, or an account in the wrong hands).
//
// The monitor only observes and reports. It never blocks a message or a read: a feed held up because a rule guessed
// wrong is a clinical problem, and the rules here are heuristics. What it reports goes through the ordinary alert rules
// (internal/alerts), so it is configured, acknowledged and delivered like every other alert.
//
// Nothing is reported for a channel until its pattern is learned, because a judgement about "unusual" made from a few
// messages is noise. The pattern is primed from the message store at start-up, so a restart does not make every channel
// new again.

// Signal kinds, each matched by the alert kind of the same suffix.
const (
	SignalVolume         = "volume"
	SignalAfterHours     = "after-hours"
	SignalNewDestination = "new-destination"
	SignalBulkReads      = "bulk-reads"
)

// Signal is one unusual thing the monitor has seen, held for a while so an alert rule can act on it.
type Signal struct {
	Kind    string
	Channel string
	// Subject is the destination address, or the person, the signal is about.
	Subject string
	// Value is what the alert rule's threshold is compared with: times the usual rate, messages after hours, or
	// distinct records read in a minute. One for a new destination.
	Value   float64
	Summary string
	Detail  string
	// At is when this sighting happened; First and Last are when it was first and most recently seen, and it is
	// dropped once Last is older than the hold.
	At    time.Time
	First time.Time
	Last  time.Time
}

// Traffic is one message a channel handled, as the monitor needs it.
type Traffic struct {
	Channel string
	At      time.Time
	// Destinations are the addresses the message was delivered to, as the channel's configuration names them.
	Destinations []string
}

// Read is one person reading patient data, from the audit trail.
type Read struct {
	Who    string
	IP     string
	Action string
	Target string
	At     time.Time
}

// PHIReadActions are the audited actions that read patient data. Exported so a test can tie the list to the actions
// the server really records.
var PHIReadActions = map[string]bool{
	"message.read": true, "message.identity.search": true,
	"pdqm.read": true, "pdqm.query": true, "pixm.query": true,
	"xds.read": true, "xds.query": true, "mhd.find": true,
	"dicomweb.read": true, "pas.read": true, "fhir.summary": true, "view.run": true,
}

// MonitorOptions tunes the monitor. The zero value is usable.
type MonitorOptions struct {
	// Now is the clock; nil is time.Now.
	Now func() time.Time
	// Location decides what "daytime" means; nil is the server's local time.
	Location *time.Location
	// Hold is how long a signal stays after it was last seen, so an alert has time to fire and is not cleared by the
	// next quiet minute. Zero is fifteen minutes.
	Hold time.Duration
	// DayStart and DayEnd are the hours (0-23) that count as daytime. Both zero means 07:00 to 19:00.
	DayStart, DayEnd int
	// MinHistory is how many messages a channel must have carried before its pattern is judged. Zero is 200.
	MinHistory int64
}

// Monitor watches traffic and reads for the four patterns above. It is safe for concurrent use.
type Monitor struct {
	mu       sync.Mutex
	opt      MonitorOptions
	channels map[string]*pattern
	people   map[string][]readEntry
	signals  map[string]*Signal
	pruned   time.Time
}

type pattern struct {
	total, offHours int64
	// hourTotal and hourMinutes are, per hour of the day, the messages carried and the distinct minutes they fell in,
	// so the usual rate is messages per busy minute at this hour.
	hourTotal, hourMinutes [24]int64
	// minute and inMinute count the current minute.
	minute   int64
	inMinute int
	known    map[string]bool
}

type readEntry struct {
	target string
	at     time.Time
}

// maxKnownDestinations bounds what one channel remembers, so a channel whose addresses are generated cannot grow it.
const maxKnownDestinations = 1000

// NewMonitor makes a monitor.
func NewMonitor(opt MonitorOptions) *Monitor {
	if opt.Now == nil {
		opt.Now = time.Now
	}
	if opt.Location == nil {
		opt.Location = time.Local
	}
	if opt.Hold <= 0 {
		opt.Hold = 15 * time.Minute
	}
	if opt.DayStart == 0 && opt.DayEnd == 0 {
		opt.DayStart, opt.DayEnd = 7, 19
	}
	if opt.MinHistory <= 0 {
		opt.MinHistory = 200
	}
	return &Monitor{opt: opt, channels: map[string]*pattern{}, people: map[string][]readEntry{}, signals: map[string]*Signal{}}
}

func (m *Monitor) daytime(t time.Time) bool {
	h := t.In(m.opt.Location).Hour()
	if m.opt.DayStart <= m.opt.DayEnd {
		return h >= m.opt.DayStart && h < m.opt.DayEnd
	}
	return h >= m.opt.DayStart || h < m.opt.DayEnd
}

// MinuteCount is how many messages a channel carried in one minute, as the message store can count them.
type MinuteCount struct {
	Channel string
	Minute  time.Time
	Count   int64
}

// Prime learns traffic from history without reporting anything, so a restarted server knows each channel's pattern.
func (m *Monitor) Prime(history []MinuteCount) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, c := range history {
		if c.Count <= 0 {
			continue
		}
		p := m.patternFor(c.Channel)
		at := c.Minute.In(m.opt.Location)
		p.hourMinutes[at.Hour()]++
		p.hourTotal[at.Hour()] += c.Count
		p.total += c.Count
		if !m.daytime(at) {
			p.offHours += c.Count
		}
	}
}

// Known returns the addresses each channel has delivered to, so they can be kept across a restart.
func (m *Monitor) Known() map[string][]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string][]string{}
	for name, p := range m.channels {
		for d := range p.known {
			out[name] = append(out[name], d)
		}
		sort.Strings(out[name])
	}
	return out
}

// Remember restores addresses kept by Known, so a destination changed while the server was down is still news.
func (m *Monitor) Remember(known map[string][]string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, list := range known {
		p := m.patternFor(name)
		for _, d := range list {
			if d != "" && len(p.known) < maxKnownDestinations {
				p.known[d] = true
			}
		}
	}
}

func (m *Monitor) patternFor(channel string) *pattern {
	p := m.channels[channel]
	if p == nil {
		p = &pattern{known: map[string]bool{}}
		m.channels[channel] = p
	}
	return p
}

func (m *Monitor) learn(p *pattern, t Traffic) {
	at := t.At.In(m.opt.Location)
	minute := at.Unix() / 60
	if minute != p.minute {
		p.minute, p.inMinute = minute, 0
		p.hourMinutes[at.Hour()]++
	}
	p.inMinute++
	p.total++
	p.hourTotal[at.Hour()]++
	if !m.daytime(at) {
		p.offHours++
	}
	for _, d := range t.Destinations {
		if d != "" && len(p.known) < maxKnownDestinations {
			p.known[d] = true
		}
	}
}

// Observe takes one message a channel handled and reports what is unusual about it.
func (m *Monitor) Observe(t Traffic) {
	if t.At.IsZero() {
		t.At = m.opt.Now()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	p := m.patternFor(t.Channel)
	learned := p.total >= m.opt.MinHistory
	at := t.At.In(m.opt.Location)
	hour := at.Hour()

	// Judged against what was learned before this message, then learned from.
	usual := 0.0
	if p.hourMinutes[hour] >= 30 {
		usual = float64(p.hourTotal[hour]) / float64(p.hourMinutes[hour])
	}
	daytimeOnly := learned && float64(p.offHours) < 0.02*float64(p.total)
	var fresh []string
	if learned {
		for _, d := range t.Destinations {
			if d != "" && !p.known[d] {
				fresh = append(fresh, d)
			}
		}
	}
	m.learn(p, t)

	if usual > 0 {
		if ratio := float64(p.inMinute) / usual; ratio >= 2 {
			m.raise(Signal{Kind: SignalVolume, Channel: t.Channel, Value: ratio, At: t.At,
				Summary: fmt.Sprintf("%s: %d messages this minute, %.0f times the usual", t.Channel, p.inMinute, ratio),
				Detail: fmt.Sprintf("At this hour the channel usually carries %.1f a minute. A burst can be a backlog being "+
					"released after an outage; it can also be records being extracted through the feed. Check the "+
					"sender and the destinations before assuming either.", usual)}, true)
		}
	}
	if daytimeOnly && !m.daytime(at) {
		m.raise(Signal{Kind: SignalAfterHours, Channel: t.Channel, Value: 1, At: t.At,
			Summary: fmt.Sprintf("%s: patient data moving at %s on a daytime channel", t.Channel, at.Format("15:04")),
			Detail: fmt.Sprintf("Of the %d messages this channel has carried, under 2%% fell outside %02d:00 to %02d:00. "+
				"Night traffic on it is unusual: a scheduled job somebody added, or access that should not be happening.",
				p.total-1, m.opt.DayStart, m.opt.DayEnd)}, false)
	}
	for _, d := range fresh {
		m.raise(Signal{Kind: SignalNewDestination, Channel: t.Channel, Subject: d, Value: 1, At: t.At,
			Summary: fmt.Sprintf("%s: patient data sent somewhere new", t.Channel),
			Detail: fmt.Sprintf("The channel delivered to %s, which it has not sent to before. If the channel was changed "+
				"on purpose this is expected; if nobody changed it, find out who did.", d)}, false)
	}
}

// Access takes one audited action and reports a person reading many patients' records quickly.
func (m *Monitor) Access(r Read) {
	if !PHIReadActions[r.Action] || r.Who == "" || r.Target == "" {
		return
	}
	if r.At.IsZero() {
		r.At = m.opt.Now()
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruneLocked()
	who := r.Who
	if r.IP != "" {
		who += " from " + r.IP
	}
	cut := r.At.Add(-time.Minute)
	kept := m.people[who][:0]
	for _, e := range m.people[who] {
		if !e.at.Before(cut) {
			kept = append(kept, e)
		}
	}
	kept = append(kept, readEntry{target: r.Target, at: r.At})
	// A ceiling on what is kept for one person, so a script reading in a loop cannot grow it without bound; the
	// count stops mattering long before it.
	if len(kept) > 1000 {
		kept = kept[len(kept)-1000:]
	}
	m.people[who] = kept
	distinct := map[string]bool{}
	for _, e := range kept {
		distinct[e.target] = true
	}
	if len(distinct) >= 2 {
		m.raise(Signal{Kind: SignalBulkReads, Subject: who, Value: float64(len(distinct)), At: r.At,
			Summary: fmt.Sprintf("%s read %d different records in a minute", r.Who, len(distinct)),
			Detail: "Reading many patients' records in quick succession is what browsing out of curiosity, or an account " +
				"in the wrong hands, looks like. It is also what a legitimate audit or a migration check looks like: ask."}, true)
	}
}

// raise records a signal, keeping the largest value seen while it is held when keepMax is set.
func (m *Monitor) raise(s Signal, keepMax bool) {
	key := s.Kind + "|" + s.Channel + "|" + s.Subject
	if old, ok := m.signals[key]; ok {
		old.Last = s.At
		if !keepMax || s.Value >= old.Value {
			old.Value, old.Summary, old.Detail = s.Value, s.Summary, s.Detail
		}
		if s.Kind == SignalAfterHours {
			old.Value++
		}
		return
	}
	s.First, s.Last = s.At, s.At
	m.signals[key] = &s
}

func (m *Monitor) pruneLocked() {
	now := m.opt.Now()
	if now.Sub(m.pruned) < 10*time.Second {
		return
	}
	m.pruned = now
	for k, s := range m.signals {
		if now.Sub(s.Last) > m.opt.Hold {
			delete(m.signals, k)
		}
	}
	for who, entries := range m.people {
		if len(entries) == 0 || now.Sub(entries[len(entries)-1].at) > time.Minute {
			delete(m.people, who)
		}
	}
}

// Signals returns what is held now, most recent first.
func (m *Monitor) Signals() []Signal {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pruned = time.Time{}
	m.pruneLocked()
	out := make([]Signal, 0, len(m.signals))
	for _, s := range m.signals {
		out = append(out, *s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Last.After(out[j].Last) })
	return out
}

// ChannelState is what the monitor has learned about one channel, for the screen that explains it.
type ChannelState struct {
	Channel      string `json:"channel"`
	Messages     int64  `json:"messages"`
	Learned      bool   `json:"learned"`
	DaytimeOnly  bool   `json:"daytimeOnly"`
	Destinations int    `json:"destinations"`
}

// Channels returns what has been learned about each channel, by name.
func (m *Monitor) Channels() []ChannelState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]ChannelState, 0, len(m.channels))
	for name, p := range m.channels {
		learned := p.total >= m.opt.MinHistory
		out = append(out, ChannelState{Channel: name, Messages: p.total, Learned: learned,
			DaytimeOnly: learned && float64(p.offHours) < 0.02*float64(p.total), Destinations: len(p.known)})
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Channel) < strings.ToLower(out[j].Channel) })
	return out
}

// Options returns the options in force, defaults filled in.
func (m *Monitor) Options() MonitorOptions { return m.opt }
