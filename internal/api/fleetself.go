package api

import (
	"net/http"
	"os"
	"time"

	"github.com/biodream-llc/perfuse/internal/peers"
	"github.com/biodream-llc/perfuse/internal/store"
)

// selfReport describes this instance for another instance's fleet view.
//
// Counts and rates only. No message identifiers, no channel-level detail, no patient data of any kind. That is what
// makes a fleet view here not a centralisation of the data: the aggregator learns how many channels are running, and
// nothing whatsoever about what flowed through them.
func (s *Server) selfReport(r *http.Request) peers.SelfReport {
	report := peers.SelfReport{
		Label:   s.currentFleetLabel(),
		Version: s.Version,
		// The peer's own clock, so the aggregator can measure skew rather than assume none.
		Now: time.Now(),
	}
	if report.Label == "" {
		if host, err := os.Hostname(); err == nil {
			report.Label = host
		}
	}

	report.Health.Draining = s.Draining()

	if s.Runtime != nil {
		for _, st := range s.Runtime.States() {
			report.Health.ChannelsTotal++
			switch {
			case st.Error != "":
				// Counted as errored and not also as stopped. A channel that failed to start is a different
				// problem from one somebody stopped on purpose, and adding it to both totals makes the sum
				// exceed the number of channels, which makes the whole panel untrustworthy.
				report.Health.ChannelsErrored++
			case st.Running:
				report.Health.ChannelsRunning++
			default:
				report.Health.ChannelsStopped++
			}
		}
	}

	// The whole instance's queue, deliberately: a fleet view reports this server rather than one tenant, and scoping
	// it would make a fleet page show a fraction of each machine with no way to tell.
	if q := s.instanceQueueStore(); q != nil {
		// A failure to read the queue leaves the depth at zero rather than failing the whole report, because a
		// fleet view that goes blank when one number is unavailable is less useful than one that shows the rest.
		if rows, err := q.Depth(r.Context()); err == nil {
			for _, row := range rows {
				report.Health.QueueDepth += row.Pending + row.Failed
				// The oldest across the whole instance, because depth alone cannot tell a busy queue that is
				// draining from a small one stuck since Tuesday, and that is the distinction worth carrying up
				// to a fleet view.
				if row.OldestSeconds > report.Health.QueueOldestSeconds {
					report.Health.QueueOldestSeconds = row.OldestSeconds
				}
			}
		}
	}

	if s.Alerts != nil {
		report.Health.AlertsFiring = len(s.Alerts.Firing())
	}

	return report
}

// handleFleetSelf answers another instance's poll.
//
// Viewer role, because reading health is the least privilege there is and a fleet token should not need more. The
// peer side of a fleet needs no agent, no plug-in and no second install - only a token.
func (s *Server) handleFleetSelf(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	s.ok(w, s.selfReport(r))
}
