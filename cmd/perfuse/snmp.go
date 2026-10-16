package main

import (
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/api"
	"github.com/biodream-llc/perfuse/internal/metrics"
	"github.com/biodream-llc/perfuse/internal/snmp"
)

// splitList reads a comma-separated flag, dropping empty entries.
func splitList(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// snmpFigures gathers what the SNMP agent serves from the running engine and the metrics it already keeps, so SNMP reports
// the same numbers as the dashboard and /metrics rather than a second count that could disagree.
func snmpFigures(rt *api.Runtime, c *metrics.Collector, repo *api.ChannelRepo, ver, label string) snmp.Figures {
	name := strings.TrimSpace(label)
	if name == "" {
		name = "perfuse"
	}
	f := snmp.Figures{Version: ver, Name: name}
	configured, running := rt.ChannelStates()
	rows := map[string]*snmp.ChannelRow{}
	for _, ch := range configured {
		rows[ch] = &snmp.ChannelRow{Name: ch, Running: running[ch]}
	}
	now := time.Now()
	add := func(metric string, set func(r *snmp.ChannelRow, v float64)) {
		for _, s := range c.Query(metric, now) {
			ch := s.Labels["channel"]
			if r := rows[ch]; r != nil && s.Latest > 0 {
				set(r, s.Latest)
			}
		}
	}
	add(metrics.MessagesReceived, func(r *snmp.ChannelRow, v float64) { r.Received += uint64(v) })
	add(metrics.MessagesDelivered, func(r *snmp.ChannelRow, v float64) { r.Delivered += uint64(v) })
	add(metrics.MessagesFailed, func(r *snmp.ChannelRow, v float64) { r.Failed += uint64(v) })
	add(metrics.QueueDepth, func(r *snmp.ChannelRow, v float64) { r.Queued += uint64(v) })
	add(metrics.QueueOldestSeconds, func(r *snmp.ChannelRow, v float64) { r.OldestQueuedSeconds = max(r.OldestQueuedSeconds, uint64(v)) })
	for _, ch := range configured {
		f.Channels = append(f.Channels, *rows[ch])
	}
	if repo != nil {
		if _, broken, err := repo.List(); err == nil {
			f.Broken = len(broken)
		}
	}
	return f
}
