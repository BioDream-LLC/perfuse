package compliance

import (
	"fmt"
	"testing"
	"time"
)

// A fixed clock in a fixed zone, so "daytime" means the same thing on every machine that runs these.
var utc = time.UTC

func monitorAt(now *time.Time) *Monitor {
	return NewMonitor(MonitorOptions{Now: func() time.Time { return *now }, Location: utc, MinHistory: 50})
}

// primed is a monitor that knows a daytime channel's last fortnight: three messages a minute, spread over working
// hours, to one address.
func primed(m *Monitor, channel, dest string) *Monitor {
	var out []MinuteCount
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, utc)
	for day := range 14 {
		for hour := 8; hour < 17; hour++ {
			for minute := range 40 {
				at := start.AddDate(0, 0, day).Add(time.Duration(hour)*time.Hour + time.Duration(minute)*time.Minute)
				out = append(out, MinuteCount{Channel: channel, Minute: at, Count: 3})
			}
		}
	}
	m.Prime(out)
	m.Remember(map[string][]string{channel: {dest}})
	return m
}

func find(m *Monitor, kind string) *Signal {
	for _, s := range m.Signals() {
		if s.Kind == kind {
			return &s
		}
	}
	return nil
}

func TestNothingIsReportedForAnOrdinaryDay(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, utc)
	m := monitorAt(&now)
	primed(m, "adt", "MLLP to ris:2575")
	for i := range 3 {
		m.Observe(Traffic{Channel: "adt", At: now.Add(time.Duration(i) * time.Second), Destinations: []string{"MLLP to ris:2575"}})
	}
	if s := m.Signals(); len(s) != 0 {
		t.Fatalf("an ordinary minute was reported: %+v", s)
	}
}

func TestABurstIsReportedAgainstThisHoursUsualRate(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, utc)
	m := monitorAt(&now)
	primed(m, "adt", "MLLP to ris:2575")
	for i := range 30 {
		m.Observe(Traffic{Channel: "adt", At: now.Add(time.Duration(i) * time.Second), Destinations: []string{"MLLP to ris:2575"}})
	}
	s := find(m, SignalVolume)
	if s == nil || s.Value < 9 {
		t.Fatalf("thirty messages in a minute on a three-a-minute channel: %+v", s)
	}
}

func TestNightTrafficOnADaytimeChannelIsReported(t *testing.T) {
	now := time.Date(2026, 10, 20, 2, 30, 0, 0, utc)
	m := monitorAt(&now)
	primed(m, "adt", "MLLP to ris:2575")
	m.Observe(Traffic{Channel: "adt", At: now, Destinations: []string{"MLLP to ris:2575"}})
	if s := find(m, SignalAfterHours); s == nil {
		t.Fatal("a message at 02:30 on a channel that has only ever carried daytime traffic was not reported")
	}

	// A channel that runs around the clock is not daytime-only, so its night traffic is not news.
	m2 := monitorAt(&now)
	var allDay []MinuteCount
	for h := range 24 {
		for i := range 10 {
			allDay = append(allDay, MinuteCount{Channel: "lab", Minute: time.Date(2026, 10, 19, h, i, 0, 0, utc), Count: 1})
		}
	}
	m2.Prime(allDay)
	m2.Observe(Traffic{Channel: "lab", At: now})
	if s := find(m2, SignalAfterHours); s != nil {
		t.Fatalf("night traffic on a round-the-clock channel was reported: %+v", s)
	}
}

func TestANewDestinationIsReportedOnce(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, utc)
	m := monitorAt(&now)
	primed(m, "adt", "MLLP to ris:2575")
	m.Observe(Traffic{Channel: "adt", At: now, Destinations: []string{"MLLP to ris:2575", "POST to https://elsewhere.example/"}})
	s := find(m, SignalNewDestination)
	if s == nil || s.Subject != "POST to https://elsewhere.example/" {
		t.Fatalf("a delivery to a new address: %+v", s)
	}
	// Learned once seen, so it is one signal rather than one per message.
	m.Observe(Traffic{Channel: "adt", At: now.Add(time.Second), Destinations: []string{"POST to https://elsewhere.example/"}})
	if s := find(m, SignalNewDestination); s.Value != 1 {
		t.Fatalf("the second delivery to it changed the signal: %+v", s)
	}
}

func TestAChannelIsNotJudgedBeforeItsPatternIsLearned(t *testing.T) {
	now := time.Date(2026, 10, 20, 2, 0, 0, 0, utc)
	m := monitorAt(&now)
	for i := range 20 {
		m.Observe(Traffic{Channel: "new", At: now.Add(time.Duration(i) * time.Second), Destinations: []string{fmt.Sprint("dest", i)}})
	}
	if s := m.Signals(); len(s) != 0 {
		t.Fatalf("a channel with twenty messages of history was judged: %+v", s)
	}
}

func TestOnePersonReadingManyRecordsIsReported(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, utc)
	m := monitorAt(&now)
	for i := range 12 {
		m.Access(Read{Who: "jdoe", IP: "10.0.0.7", Action: "message.read", Target: fmt.Sprintf("adt/%d", i), At: now.Add(time.Duration(i) * time.Second)})
	}
	// Not patient data, so not counted.
	m.Access(Read{Who: "jdoe", Action: "channel.update", Target: "adt", At: now})
	s := find(m, SignalBulkReads)
	if s == nil || s.Value != 12 || s.Subject != "jdoe from 10.0.0.7" {
		t.Fatalf("twelve records read in twelve seconds: %+v", s)
	}
	// The same record read again is one record.
	m2 := monitorAt(&now)
	for i := range 12 {
		m2.Access(Read{Who: "jdoe", Action: "message.read", Target: "adt/1", At: now.Add(time.Duration(i) * time.Second)})
	}
	if s := find(m2, SignalBulkReads); s != nil {
		t.Fatalf("one record read twelve times was reported as many: %+v", s)
	}
}

func TestSignalsGoOnceTheyAreOld(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, utc)
	m := monitorAt(&now)
	for i := range 3 {
		m.Access(Read{Who: "a", Action: "message.read", Target: fmt.Sprint(i), At: now})
	}
	if len(m.Signals()) != 1 {
		t.Fatal("no signal to expire")
	}
	now = now.Add(16 * time.Minute)
	if s := m.Signals(); len(s) != 0 {
		t.Fatalf("a signal outlived its hold: %+v", s)
	}
}

func TestKnownAddressesSurviveARestart(t *testing.T) {
	now := time.Date(2026, 10, 20, 10, 0, 0, 0, utc)
	before := primed(monitorAt(&now), "adt", "MLLP to ris:2575")
	before.Observe(Traffic{Channel: "adt", At: now, Destinations: []string{"MLLP to pacs:104"}})
	kept := before.Known()

	after := primed(monitorAt(&now), "adt", "MLLP to ris:2575")
	after.Remember(kept)
	after.Observe(Traffic{Channel: "adt", At: now, Destinations: []string{"MLLP to pacs:104"}})
	if s := find(after, SignalNewDestination); s != nil {
		t.Fatalf("an address known before the restart was reported as new after it: %+v", s)
	}
}
