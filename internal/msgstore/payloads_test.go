package msgstore

import (
	"context"
	"database/sql"
	"fmt"
	"testing"
	"time"
)

// The one bug in RecentPayloads worth a test of its own is the copy.
//
// A SQL driver may reuse the backing array between rows. Without copying, every entry in the
// returned slice can point at the last row read - and a replay over those would report that
// every message is identical, which is a false all-clear. For a feature whose entire value is
// being trusted when it says nothing changed, that is the worst failure available.

func payloadStore(t *testing.T) *Store {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+t.Name()+"?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })

	s, err := NewStore(db)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func recordPayload(t *testing.T, s *Store, channel, body string) {
	t.Helper()
	_, err := s.Record(context.Background(), &Message{
		Channel:    channel,
		ReceivedAt: time.Now(),
		Outcome:    Delivered,
		Raw:        []byte(body),
		Size:       len(body),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestRecentPayloadsReturnsDistinctBodies(t *testing.T) {
	s := payloadStore(t)

	// Same length on purpose. A driver reusing one buffer is most likely to go unnoticed
	// when the rows are the same size, because the result still looks like a plausible
	// message rather than obvious garbage.
	for i := 0; i < 25; i++ {
		recordPayload(t, s, "feed", fmt.Sprintf("MSH|^~\\&|A|B|C|D|20260819||ADT^A01|CTRL%03d|P|2.5\r", i))
	}

	got, err := s.RecentPayloads(context.Background(), "feed", 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 25 {
		t.Fatalf("got %d payloads, want 25", len(got))
	}

	seen := map[string]bool{}
	for _, body := range got {
		seen[string(body)] = true
	}
	if len(seen) != 25 {
		t.Fatalf("only %d of 25 payloads are distinct: the rows share a buffer, and a replay "+
			"over these would report that nothing changed", len(seen))
	}
}

func TestRecentPayloadsIsNewestFirst(t *testing.T) {
	// Recent is the right bias: a feed's shape drifts, and a change is judged against what
	// the sender is doing now rather than what it did two years ago.
	s := payloadStore(t)
	recordPayload(t, s, "feed", "oldest")
	recordPayload(t, s, "feed", "middle")
	recordPayload(t, s, "feed", "newest")

	got, err := s.RecentPayloads(context.Background(), "feed", 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d, want 2", len(got))
	}
	if string(got[0]) != "newest" {
		t.Errorf("first payload = %q, want the newest", got[0])
	}
	if string(got[1]) != "middle" {
		t.Errorf("second payload = %q", got[1])
	}
}

func TestRecentPayloadsIsScopedToOneChannel(t *testing.T) {
	// Replaying another channel's traffic through this one would produce differences that
	// mean nothing and a report nobody could act on.
	s := payloadStore(t)
	recordPayload(t, s, "wanted", "mine")
	recordPayload(t, s, "other", "not mine")

	got, err := s.RecentPayloads(context.Background(), "wanted", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || string(got[0]) != "mine" {
		t.Fatalf("got %q", got)
	}
}

func TestRecentPayloadsSkipsMessagesWithNoBody(t *testing.T) {
	// A row recorded without its payload cannot say anything about whether a change is
	// safe, and counting it would overstate how much traffic was examined.
	s := payloadStore(t)
	recordPayload(t, s, "feed", "has a body")

	if _, err := s.Record(context.Background(), &Message{
		Channel:    "feed",
		ReceivedAt: time.Now(),
		Outcome:    Delivered,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := s.RecentPayloads(context.Background(), "feed", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d payloads, want 1", len(got))
	}
}

func TestRecentPayloadsBoundsTheBatch(t *testing.T) {
	// An unbounded read asks the process to hold the whole table in memory.
	s := payloadStore(t)
	recordPayload(t, s, "feed", "one")

	if _, err := s.RecentPayloads(context.Background(), "feed", MaxPayloadBatch*10); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecentPayloads(context.Background(), "", 10); err == nil {
		t.Error("a missing channel name was accepted")
	}
}

func TestCountPayloadsMatchesWhatCanBeReplayed(t *testing.T) {
	// Shown beside a replay so the reader can see what fraction of history was covered. A
	// clean report over the last thousand of two hundred thousand is weaker evidence than
	// one over the last thousand of eleven hundred.
	s := payloadStore(t)
	for i := 0; i < 7; i++ {
		recordPayload(t, s, "feed", fmt.Sprintf("body %d", i))
	}

	n, err := s.CountPayloads(context.Background(), "feed")
	if err != nil {
		t.Fatal(err)
	}
	if n != 7 {
		t.Errorf("count = %d, want 7", n)
	}
}
