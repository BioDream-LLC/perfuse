package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/compliance"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tenant"
)

// accessWatchView is what the console shows about the patient-data access monitor.
type accessWatchView struct {
	On bool `json:"on"`
	// DayStart and DayEnd are the hours that count as daytime, in the server's time zone.
	DayStart   int    `json:"dayStart"`
	DayEnd     int    `json:"dayEnd"`
	Zone       string `json:"zone"`
	MinHistory int64  `json:"minHistory"`
	HoldMins   int    `json:"holdMinutes"`

	Channels []compliance.ChannelState `json:"channels"`
	Signals  []accessSignal            `json:"signals"`
}

type accessSignal struct {
	Kind    string    `json:"kind"`
	Channel string    `json:"channel,omitempty"`
	Subject string    `json:"subject,omitempty"`
	Value   float64   `json:"value"`
	Summary string    `json:"summary"`
	Detail  string    `json:"detail"`
	First   time.Time `json:"first"`
	Last    time.Time `json:"last"`
}

// handleAccessWatch reports what the monitor has learned and what it is holding.
//
// Administrators only: what it holds names people and what they read, which is itself something to keep close.
func (s *Server) handleAccessWatch(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.PHIWatch == nil {
		s.ok(w, accessWatchView{Channels: []compliance.ChannelState{}, Signals: []accessSignal{}})
		return
	}
	opt := s.PHIWatch.Options()
	out := accessWatchView{On: true, DayStart: opt.DayStart, DayEnd: opt.DayEnd, Zone: opt.Location.String(),
		MinHistory: opt.MinHistory, HoldMins: int(opt.Hold / time.Minute), Channels: s.PHIWatch.Channels(),
		Signals: []accessSignal{}}
	// One monitor serves the whole server, and its names carry the tenant ("acme/admissions", "acme/alice"): an
	// administrator sees their own tenant's channels and people, never another's.
	out.Channels = out.Channels[:0]
	for _, c := range s.PHIWatch.Channels() {
		if name, ok := ownWatchName(sess.TenantID, c.Channel); ok {
			c.Channel = name
			out.Channels = append(out.Channels, c)
		}
	}
	for _, sig := range s.PHIWatch.Signals() {
		named := sig.Channel
		if named == "" {
			named = sig.Subject
		}
		if _, ok := ownWatchName(sess.TenantID, named); !ok {
			continue
		}
		channel, _ := ownWatchName(sess.TenantID, sig.Channel)
		subject := sig.Subject
		if sig.Channel == "" {
			subject, _ = ownWatchName(sess.TenantID, sig.Subject)
		}
		out.Signals = append(out.Signals, accessSignal{Kind: sig.Kind, Channel: channel, Subject: subject,
			Value: sig.Value, Summary: strings.ReplaceAll(sig.Summary, string(sess.TenantID)+"/", ""), Detail: sig.Detail,
			First: sig.First, Last: sig.Last})
	}
	s.ok(w, out)
}

// ownWatchName says whether a monitor name (a channel, or a person) belongs to the session's tenant, and returns it
// without the tenant. The default tenant's names carry no prefix.
func ownWatchName(tid tenant.ID, name string) (string, bool) {
	if name == "" {
		return "", true
	}
	if tid == "" || tid == store.DefaultTenant {
		if strings.HasPrefix(name, store.DefaultTenant+"/") {
			return strings.TrimPrefix(name, store.DefaultTenant+"/"), true
		}
		return name, !strings.Contains(name, "/")
	}
	rest, ok := strings.CutPrefix(name, string(tid)+"/")
	return rest, ok
}
