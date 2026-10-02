package msgstore

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func open(t *testing.T) *Store {
	t.Helper()

	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { db.Close() })

	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func record(t *testing.T, s *Store, m *Message) int64 {
	t.Helper()
	id, err := s.Record(context.Background(), m)
	if err != nil {
		t.Fatalf("Record: %v", err)
	}
	return id
}

func sample(channel string, outcome Outcome, at time.Time) *Message {
	return &Message{
		Channel:      channel,
		ReceivedAt:   at,
		ControlID:    "CTRL1",
		MessageType:  "ADT",
		TriggerEvent: "A01",
		Sender:       "SITEA",
		Outcome:      outcome,
		AckCode:      "AA",
		Size:         180,
		Segments:     4,
		DurationMS:   12,
		Raw:          []byte("MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260818||ADT^A01|CTRL1|P|2.5.1\rPID|1||MRN77\r"),
		Deliveries: []Delivery{
			{Destination: "registry", Status: DeliveryDelivered, Attempts: 1, DurationMS: 8},
			{Destination: "archive", Status: DeliveryFiltered},
		},
	}
}

func TestRecordAndGet(t *testing.T) {
	s := open(t)
	now := time.Now().UTC()

	id := record(t, s, sample("adt-inbound", Delivered, now))

	got, err := s.Get(context.Background(), "", id)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}

	if got.Channel != "adt-inbound" || got.Outcome != Delivered {
		t.Errorf("message = %+v", got)
	}
	if got.ControlID != "CTRL1" || got.MessageType != "ADT" || got.TriggerEvent != "A01" {
		t.Errorf("identity fields wrong: %+v", got)
	}
	// The payload is what makes a message viewer possible.
	if !strings.Contains(string(got.Raw), "MRN77") {
		t.Error("the payload was not stored")
	}
	if len(got.Deliveries) != 2 {
		t.Fatalf("got %d deliveries, want 2", len(got.Deliveries))
	}

	byDest := map[string]Delivery{}
	for _, d := range got.Deliveries {
		byDest[d.Destination] = d
	}
	if byDest["registry"].Status != DeliveryDelivered {
		t.Errorf("registry status = %q", byDest["registry"].Status)
	}
	// A destination that filtered the message must be distinguishable from one
	// that failed, or an operator cannot tell a routing rule from an outage.
	if byDest["archive"].Status != DeliveryFiltered {
		t.Errorf("archive status = %q, want filtered", byDest["archive"].Status)
	}
}

func TestGetMissing(t *testing.T) {
	s := open(t)
	if _, err := s.Get(context.Background(), "", 999); !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
}

func TestListDoesNotLoadPayloads(t *testing.T) {
	// Listing a thousand messages must not drag their payloads along, or the
	// browser becomes unusable exactly when there is enough traffic to need it.
	s := open(t)
	now := time.Now().UTC()

	for i := 0; i < 5; i++ {
		record(t, s, sample("adt-inbound", Delivered, now.Add(time.Duration(i)*time.Second)))
	}

	list, total, err := s.List(context.Background(), Query{})
	if err != nil {
		t.Fatal(err)
	}
	if total != 5 || len(list) != 5 {
		t.Fatalf("total = %d, listed = %d, want 5 and 5", total, len(list))
	}
	for i, m := range list {
		if len(m.Raw) != 0 {
			t.Errorf("message %d carried its payload into the list", i)
		}
	}

	// Newest first, because that is what anyone opening the screen wants.
	for i := 1; i < len(list); i++ {
		if list[i].ReceivedAt.After(list[i-1].ReceivedAt) {
			t.Error("the list is not newest first")
			break
		}
	}
}

func TestListFilters(t *testing.T) {
	s := open(t)
	now := time.Now().UTC()

	record(t, s, sample("adt-inbound", Delivered, now))
	record(t, s, sample("adt-inbound", Failed, now.Add(-time.Minute)))
	record(t, s, sample("lab-results", Delivered, now.Add(-2*time.Minute)))

	oru := sample("lab-results", Filtered, now.Add(-3*time.Minute))
	oru.MessageType = "ORU"
	oru.TriggerEvent = "R01"
	oru.ControlID = "CTRL9"
	record(t, s, oru)

	cases := map[string]struct {
		query Query
		want  int
	}{
		"all":              {Query{}, 4},
		"by channel":       {Query{Channel: "adt-inbound"}, 2},
		"by outcome":       {Query{Outcome: Failed}, 1},
		"by type":          {Query{MessageType: "ORU"}, 1},
		"by event":         {Query{TriggerEvent: "A01"}, 3},
		"by control id":    {Query{ControlID: "CTRL9"}, 1},
		"by sender":        {Query{Sender: "SITEA"}, 4},
		"channel and type": {Query{Channel: "lab-results", MessageType: "ORU"}, 1},
		"since":            {Query{Since: now.Add(-90 * time.Second)}, 2},
		"until":            {Query{Until: now.Add(-90 * time.Second)}, 2},
		"no match":         {Query{Channel: "nonexistent"}, 0},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, total, err := s.List(context.Background(), c.query)
			if err != nil {
				t.Fatal(err)
			}
			if total != c.want {
				t.Errorf("total = %d, want %d", total, c.want)
			}
		})
	}
}

func TestSearchPayload(t *testing.T) {
	// Searching message content is what somebody needs during an incident when all
	// they have is an accession number.
	s := open(t)
	now := time.Now().UTC()

	record(t, s, sample("adt-inbound", Delivered, now))

	other := sample("adt-inbound", Delivered, now.Add(-time.Minute))
	other.Raw = []byte("MSH|^~\\&|SEND|SITEB|RECV|RFAC|20260818||ADT^A03|CTRL2|P|2.5.1\rPID|1||MRN99\r")
	record(t, s, other)

	_, total, err := s.List(context.Background(), Query{Search: "MRN77"})
	if err != nil {
		t.Fatal(err)
	}
	if total != 1 {
		t.Errorf("search for MRN77 total = %d, want 1", total)
	}

	// A search term containing SQL punctuation must be a search term.
	_, _, err = s.List(context.Background(), Query{Search: "'; DROP TABLE messages;--"})
	if err != nil {
		t.Fatalf("a search containing SQL failed: %v", err)
	}
	if _, total, _ := s.List(context.Background(), Query{}); total != 2 {
		t.Error("the table did not survive a search containing SQL")
	}
}

func TestListPaging(t *testing.T) {
	s := open(t)
	now := time.Now().UTC()

	for i := 0; i < 25; i++ {
		record(t, s, sample("adt-inbound", Delivered, now.Add(-time.Duration(i)*time.Second)))
	}

	page, total, err := s.List(context.Background(), Query{Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	if total != 25 {
		t.Errorf("total = %d, want 25", total)
	}
	if len(page) != 10 {
		t.Errorf("page size = %d, want 10", len(page))
	}

	second, _, err := s.List(context.Background(), Query{Limit: 10, Offset: 20})
	if err != nil {
		t.Fatal(err)
	}
	if len(second) != 5 {
		t.Errorf("last page = %d, want 5", len(second))
	}

	// An unbounded limit would ask the server to load the whole table.
	big, _, err := s.List(context.Background(), Query{Limit: 999999})
	if err != nil {
		t.Fatal(err)
	}
	if len(big) > MaxLimit {
		t.Errorf("returned %d rows, above the cap of %d", len(big), MaxLimit)
	}
}

func TestStats(t *testing.T) {
	s := open(t)
	now := time.Now().UTC()

	record(t, s, sample("adt-inbound", Delivered, now))
	record(t, s, sample("adt-inbound", Delivered, now.Add(-time.Second)))
	record(t, s, sample("adt-inbound", Failed, now.Add(-2*time.Second)))
	record(t, s, sample("lab-results", Filtered, now.Add(-3*time.Second)))

	stats, err := s.Stats(context.Background(), "", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	if stats.Total != 4 {
		t.Errorf("total = %d, want 4", stats.Total)
	}
	if stats.ByOutcome["delivered"] != 2 || stats.ByOutcome["failed"] != 1 {
		t.Errorf("byOutcome = %v", stats.ByOutcome)
	}
	if stats.ByChannel["adt-inbound"] != 3 {
		t.Errorf("byChannel = %v", stats.ByChannel)
	}
	if stats.ByType["ADT^A01"] != 4 {
		t.Errorf("byType = %v", stats.ByType)
	}
	if stats.OldestKept == nil || stats.NewestKept == nil {
		t.Error("the time range was not reported")
	}
	if stats.StoredBytes == 0 {
		t.Error("stored bytes was not reported, so nobody can see the table growing")
	}
}

func TestThroughputBuckets(t *testing.T) {
	// Aggregating in SQL is what keeps a chart cheap enough to poll. A dashboard
	// that costs a table scan is one somebody turns off.
	s := open(t)
	base := time.Now().UTC().Truncate(time.Minute)

	for i := 0; i < 3; i++ {
		record(t, s, sample("adt-inbound", Delivered, base.Add(time.Duration(i)*time.Second)))
	}
	record(t, s, sample("adt-inbound", Failed, base.Add(2*time.Second)))
	// A different minute.
	record(t, s, sample("adt-inbound", Delivered, base.Add(-2*time.Minute)))

	buckets, err := s.Throughput(context.Background(), "", base.Add(-time.Hour), time.Minute, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) < 2 {
		t.Fatalf("got %d buckets, want at least 2", len(buckets))
	}

	var total, delivered, failed int64
	for _, b := range buckets {
		total += b.Total
		delivered += b.Delivered
		failed += b.Failed
	}
	if total != 5 {
		t.Errorf("bucketed total = %d, want 5", total)
	}
	if delivered != 4 {
		t.Errorf("delivered = %d, want 4", delivered)
	}
	if failed != 1 {
		t.Errorf("failed = %d, want 1", failed)
	}

	// Buckets must be ordered, or a chart draws backwards.
	for i := 1; i < len(buckets); i++ {
		if !buckets[i].Start.After(buckets[i-1].Start) {
			t.Error("buckets are not in time order")
			break
		}
	}

	// And filtering by channel must work, since the dashboard offers it.
	filtered, err := s.Throughput(context.Background(), "", base.Add(-time.Hour), time.Minute, "nonexistent")
	if err != nil {
		t.Fatal(err)
	}
	// The series still covers the window, because a channel with no traffic is a flat line at zero rather than an absence.
	//
	// This used to assert an empty slice. Zero-filling changed it deliberately: a feed that has gone quiet is the single most
	// important thing this chart shows, and it can only be shown if the quiet part is in the data. What matters for the filter is
	// that no counts leak in from other channels.
	if len(filtered) == 0 {
		t.Error("an unknown channel returned no buckets at all, so a silent feed cannot be drawn as silent")
	}
	for _, b := range filtered {
		if b.Total != 0 || b.Delivered != 0 || b.Failed != 0 || b.Filtered != 0 || b.Unparseable != 0 {
			t.Errorf("a bucket for an unknown channel has counts in it: %+v", b)

			break
		}
	}
}

func TestDestinationStats(t *testing.T) {
	s := open(t)
	now := time.Now().UTC()

	m := sample("adt-inbound", Partial, now)
	m.Deliveries = []Delivery{
		{Destination: "registry", Status: DeliveryDelivered, Attempts: 1, DurationMS: 5},
		{Destination: "downstream", Status: DeliveryFailed, Attempts: 5, DurationMS: 900, Error: "connection refused"},
	}
	record(t, s, m)
	record(t, s, m)

	stats, err := s.DestinationStats(context.Background(), "", now.Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if len(stats) != 2 {
		t.Fatalf("got %d destinations, want 2", len(stats))
	}

	byName := map[string]DestinationStats{}
	for _, d := range stats {
		byName[d.Destination] = d
	}
	if byName["registry"].Delivered != 2 {
		t.Errorf("registry delivered = %d, want 2", byName["registry"].Delivered)
	}
	if byName["downstream"].Failed != 2 {
		t.Errorf("downstream failed = %d, want 2", byName["downstream"].Failed)
	}
	// Average attempts is what shows a destination that only works on the fourth
	// try, which is invisible in a success count.
	if byName["downstream"].AvgAttempts != 5 {
		t.Errorf("downstream avg attempts = %v, want 5", byName["downstream"].AvgAttempts)
	}
}

func TestPruneDropsPayloadsBeforeRows(t *testing.T) {
	// Losing the fact that a message arrived is worse than losing its contents, so
	// payloads go first.
	s := open(t)
	s.RetentionDays = 10

	now := time.Now().UTC()
	recent := record(t, s, sample("adt-inbound", Delivered, now))
	middling := record(t, s, sample("adt-inbound", Delivered, now.AddDate(0, 0, -7)))
	ancient := record(t, s, sample("adt-inbound", Delivered, now.AddDate(0, 0, -20)))

	payloads, rows, err := s.Prune(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("pruned %d rows, want 1 (only the 20-day-old message)", rows)
	}
	if payloads < 1 {
		t.Errorf("dropped %d payloads, want at least 1", payloads)
	}

	// The recent one keeps everything.
	if m, err := s.Get(context.Background(), "", recent); err != nil {
		t.Errorf("the recent message was removed: %v", err)
	} else if len(m.Raw) == 0 {
		t.Error("the recent message lost its payload")
	}

	// The middling one keeps its record but not its content.
	if m, err := s.Get(context.Background(), "", middling); err != nil {
		t.Errorf("the 7-day-old message was removed: %v", err)
	} else if len(m.Raw) != 0 {
		t.Error("the 7-day-old message kept its payload past the content window")
	}

	// The ancient one is gone entirely.
	if _, err := s.Get(context.Background(), "", ancient); !errors.Is(err, ErrNotFound) {
		t.Errorf("the 20-day-old message survived pruning: %v", err)
	}

	// And its deliveries went with it, or the table grows for ever.
	var orphans int
	if err := s.db.QueryRow(
		`SELECT COUNT(*) FROM message_deliveries
		 WHERE message_id NOT IN (SELECT id FROM messages)`).Scan(&orphans); err != nil {
		t.Fatal(err)
	}
	if orphans != 0 {
		t.Errorf("%d orphaned delivery rows left behind", orphans)
	}
}

func TestPruneDoesNothingWithoutRetention(t *testing.T) {
	// Keeping messages for ever has to be a deliberate choice, but if it is the
	// choice, pruning must not quietly delete anything.
	s := open(t)
	s.RetentionDays = 0

	record(t, s, sample("adt-inbound", Delivered, time.Now().UTC().AddDate(0, -6, 0)))

	payloads, rows, err := s.Prune(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if payloads != 0 || rows != 0 {
		t.Errorf("pruned %d payloads and %d rows with retention off", payloads, rows)
	}
}

func TestStorePayloadsCanBeDisabled(t *testing.T) {
	// A deployment that cannot hold clinical content at rest still wants counts and
	// outcomes.
	s := open(t)
	s.StorePayloads = false

	id := record(t, s, sample("adt-inbound", Delivered, time.Now().UTC()))

	m, err := s.Get(context.Background(), "", id)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Raw) != 0 {
		t.Error("a payload was stored with StorePayloads off")
	}
	// The record itself must survive, or there is no audit trail.
	if m.ControlID != "CTRL1" || m.Outcome != Delivered {
		t.Errorf("the record was lost along with the payload: %+v", m)
	}
	if m.Size == 0 {
		t.Error("the size was not recorded, so nobody can tell how much traffic passed")
	}
}

func TestChannelsList(t *testing.T) {
	s := open(t)
	now := time.Now().UTC()

	record(t, s, sample("b-channel", Delivered, now))
	record(t, s, sample("a-channel", Delivered, now))
	record(t, s, sample("a-channel", Delivered, now))

	channels, err := s.Channels(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(channels, ",") != "a-channel,b-channel" {
		t.Errorf("channels = %v, want them sorted and deduplicated", channels)
	}
}

func TestUnparseableMessageIsStillRecorded(t *testing.T) {
	// A message that could not be parsed is exactly the one somebody needs to
	// look at, so it has to be stored with its bytes.
	s := open(t)

	m := &Message{
		Channel:    "adt-inbound",
		ReceivedAt: time.Now().UTC(),
		Outcome:    Unparseable,
		AckCode:    "AR",
		Error:      "input does not start with an MSH segment",
		Raw:        []byte("this is not HL7 at all"),
		Size:       22,
	}
	id := record(t, s, m)

	got, err := s.Get(context.Background(), "", id)
	if err != nil {
		t.Fatal(err)
	}
	if got.Outcome != Unparseable {
		t.Errorf("outcome = %q", got.Outcome)
	}
	if string(got.Raw) != "this is not HL7 at all" {
		t.Error("the offending bytes were not kept")
	}
	if got.Error == "" {
		t.Error("the reason was not kept")
	}
}

// TestBothCutoffsFollowTheLiveRetentionSetting pins the bug this test was written for.
//
// Prune reads retention twice: once for deleting rows and once for blanking payloads at half the window. One read used the live
// settings value and the other used the field set at startup, so changing retention through the interface moved one cutoff and not the
// other - and somebody who set ten years kept their records for ten years and lost every message body after fifteen days.
func TestBothCutoffsFollowTheLiveRetentionSetting(t *testing.T) {
	s := open(t)

	// The startup value, deliberately short, standing in for a flag nobody updated.
	s.RetentionDays = 30

	// The live value, as a settings file would supply it.
	live := 3650
	s.RetentionDaysFn = func() int { return live }

	// A message from two months ago. Well inside a ten-year window, and well outside half of thirty days.
	record(t, s, sample("adt", Delivered, time.Now().UTC().AddDate(0, 0, -60)))

	payloads, rows, err := s.Prune(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	if rows != 0 {
		t.Errorf("deleted %d rows, but the live retention is %d days", rows, live)
	}
	if payloads != 0 {
		t.Errorf("blanked %d payloads, but half of %d days has not passed - the payload cutoff is not "+
			"reading the live value", payloads, live)
	}
}

// TestThePayloadWindowIsConfigurableAndCannotOutliveTheRecord covers the newly exposed setting.
//
// It was hard-coded at half the retention window, which was a reasonable default and invisible - somebody setting thirty days believed
// they had thirty days of content and had fifteen.
func TestThePayloadWindowIsConfigurableAndCannotOutliveTheRecord(t *testing.T) {
	t.Run("an explicit window is honoured", func(t *testing.T) {
		s := open(t)
		s.RetentionDaysFn = func() int { return 90 }
		s.PayloadDaysFn = func() int { return 60 }

		// Fifty days old, which is the only range that distinguishes the two: past half of 90, which is
		// what this used to be hard-coded to, and inside the configured 60. A record at forty days sat
		// inside both and the first version of this test proved nothing - a plant that ignored the
		// setting entirely still passed.
		record(t, s, sample("adt", Delivered, time.Now().UTC().AddDate(0, 0, -50)))

		payloads, rows, err := s.Prune(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if payloads != 0 {
			t.Errorf("blanked %d payloads at 50 days with a 60 day window - the configured window is "+
				"being ignored in favour of half the retention window", payloads)
		}
		if rows != 0 {
			t.Errorf("deleted %d rows at 50 days with a 90 day window", rows)
		}
	})

	t.Run("a body cannot outlive its record whatever the window says", func(t *testing.T) {
		s := open(t)
		s.RetentionDaysFn = func() int { return 30 }
		// Asking to keep bodies for ten years while keeping records for thirty days is not an error and needs no
		// special handling: deleting the row takes the body with it. Pinned because the obvious reading is that
		// this needs a cap, and a cap here reported blanking a payload it was about to delete entirely.
		s.PayloadDaysFn = func() int { return 3650 }

		record(t, s, sample("adt", Delivered, time.Now().UTC().AddDate(0, 0, -40)))

		_, rows, err := s.Prune(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if rows != 1 {
			t.Errorf("deleted %d rows, want the one past the 30 day window", rows)
		}

		// And the record is genuinely gone, body and all, rather than merely reported as deleted.
		remaining, total, err := s.List(context.Background(), Query{Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(remaining) != 0 || total != 0 {
			t.Errorf("%d of %d records survived past the retention window", len(remaining), total)
		}
	})

	t.Run("a recent message keeps its body", func(t *testing.T) {
		// The most basic property, and nothing asserted it: pruning must not touch content inside the window.
		// A plant that blanked every payload regardless of age broke no test, because every case here recorded
		// an old message and checked how much was removed - never that anything survived.
		s := open(t)
		s.RetentionDaysFn = func() int { return 30 }
		s.PayloadDaysFn = func() int { return 15 }

		record(t, s, sample("adt", Delivered, time.Now().UTC().AddDate(0, 0, -2)))

		payloads, rows, err := s.Prune(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if payloads != 0 || rows != 0 {
			t.Fatalf("pruning removed %d payloads and %d rows from a two-day-old message", payloads, rows)
		}

		// Asserted on the content rather than only on the counts, because a count of zero would also be
		// reported by an update that failed silently.
		//
		// RecentPayloads rather than List: List is a list view and deliberately does not carry bodies, so
		// reading Raw from it returns empty for a perfectly intact message. That cost a confusing minute.
		bodies, err := s.RecentPayloads(context.Background(), "adt", 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(bodies) != 1 {
			t.Fatalf("got %d payloads back", len(bodies))
		}
		if len(bodies[0]) == 0 {
			t.Error("a two-day-old message lost its body to pruning")
		}
	})

	t.Run("unset falls back to half the retention window", func(t *testing.T) {
		s := open(t)
		s.RetentionDaysFn = func() int { return 30 }
		// Nothing set, so the behaviour from before this was configurable: bodies at 15 days.
		record(t, s, sample("adt", Delivered, time.Now().UTC().AddDate(0, 0, -20)))

		payloads, rows, err := s.Prune(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if payloads != 1 {
			t.Errorf("blanked %d payloads at 20 days, want the one past half of 30", payloads)
		}
		if rows != 0 {
			t.Errorf("deleted %d rows at 20 days with a 30 day window", rows)
		}
	})
}
