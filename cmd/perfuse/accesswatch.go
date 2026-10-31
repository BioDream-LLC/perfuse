package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/biodream-llc/perfuse/internal/alerts"
	"github.com/biodream-llc/perfuse/internal/compliance"
	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tenant"
)

// The patient-data access monitor, as the server runs it.
//
// It learns each channel's pattern from the last fortnight of the message store when the server starts, keeps the
// addresses each channel has sent to in a small file beside the database (so a destination changed while the server
// was down is still news), reads who read what from the audit trail, and hands what it sees to the alert rules.

// accessWatchFile holds the addresses each channel has delivered to. Addresses only: hosts, URLs and folder names.
const accessWatchFile = "access-watch.json"

func startAccessWatch(st *store.Store, messages *msgstore.Store, dataDir string, log *slog.Logger) *compliance.Monitor {
	m := compliance.NewMonitor(compliance.MonitorOptions{})
	path := filepath.Join(dataDir, accessWatchFile)
	if data, err := os.ReadFile(path); err == nil {
		var known map[string][]string
		if err := json.Unmarshal(data, &known); err != nil {
			log.Warn("the access monitor's list of known destinations could not be read; destinations will be learned again",
				"file", path, "err", err)
		} else {
			m.Remember(known)
		}
	}
	if messages != nil {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		history, err := messages.MinuteCounts(ctx, "", time.Now().AddDate(0, 0, -14))
		cancel()
		if err != nil {
			log.Warn("the access monitor could not read the message history; patterns will be learned from new traffic",
				"err", err)
		}
		counts := make([]compliance.MinuteCount, len(history))
		for i, c := range history {
			counts[i] = compliance.MinuteCount{Channel: c.Channel, Minute: c.Minute, Count: c.Count}
		}
		m.Prime(counts)
	}
	st.OnAudit = func(tid tenant.ID, e store.AuditEntry) {
		who := e.Username
		if tid != "" && tid != store.DefaultTenant {
			who = string(tid) + "/" + who
		}
		m.Access(compliance.Read{Who: who, IP: e.IP, Action: e.Action, Target: e.Target, At: e.At})
	}
	go func() {
		// Written every few minutes rather than on each new address, so a busy server does not write a file per
		// message; at most a few minutes of learning is lost to a crash, and that only means one alert again.
		var last string
		for range time.Tick(5 * time.Minute) {
			data, err := json.MarshalIndent(m.Known(), "", " ")
			if err != nil || string(data) == last {
				continue
			}
			if err := writeFileAtomic(path, data, 0o600); err != nil {
				log.Warn("could not save the access monitor's known destinations", "file", path, "err", err)
				continue
			}
			last = string(data)
		}
	}()
	log.Info("watching access to patient data", "channels learned", len(m.Channels()))
	return m
}

// privacySignals is what the monitor holds, in the alert rules' terms.
func privacySignals(m *compliance.Monitor) []alerts.PrivacySignal {
	kinds := map[string]alerts.Kind{
		compliance.SignalVolume: alerts.KindPHIVolume, compliance.SignalAfterHours: alerts.KindPHIAfterHours,
		compliance.SignalNewDestination: alerts.KindPHINewDestination, compliance.SignalBulkReads: alerts.KindPHIBulkReads,
	}
	held := m.Signals()
	out := make([]alerts.PrivacySignal, 0, len(held))
	for _, s := range held {
		out = append(out, alerts.PrivacySignal{Kind: kinds[s.Kind], Channel: s.Channel, Subject: s.Subject, Value: s.Value,
			Summary: s.Summary, Detail: s.Detail, First: s.First})
	}
	return out
}

// writeFileAtomic writes through a temporary file and a rename, so a crash cannot leave half a file to be read at the
// next start.
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".access-watch-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(name, path)
}
