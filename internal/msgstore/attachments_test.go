package msgstore

import (
	"bytes"
	"context"
	"database/sql"
	"testing"

	"github.com/biodream-llc/perfuse/internal/attach"
	_ "modernc.org/sqlite"
)

func attachStore(t *testing.T) *Store {
	t.Helper()

	db, err := sql.Open("sqlite", "file:"+t.TempDir()+"/a.db")
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

func payloadOf(size int, filler byte) []byte {
	out := make([]byte, size)
	for i := range out {
		out[i] = filler
	}
	return out
}

func TestAPayloadStoredTwiceOccupiesOneRow(t *testing.T) {
	// Deduplication is the entire justification for content addressing. A document resent, or one message fanned out
	// to five destinations, must not be five copies.
	s := attachStore(t)
	ctx := context.Background()

	payload := payloadOf(10000, 'A')
	a := attach.Attachment{Digest: attach.Compute(payload), Size: len(payload), Payload: payload}

	for i := 0; i < 5; i++ {
		if err := s.PutAttachment(ctx, a); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}

	stats, err := s.AttachmentStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 1 {
		t.Errorf("stored %d rows for the same payload, want 1", stats.Count)
	}
	if stats.Bytes != int64(len(payload)) {
		t.Errorf("stored %d bytes, want %d", stats.Bytes, len(payload))
	}
}

func TestAStoredPayloadComesBackByteForByte(t *testing.T) {
	s := attachStore(t)
	ctx := context.Background()

	payload := payloadOf(50000, 'Z')
	digest := attach.Compute(payload)

	if err := s.PutAttachment(ctx, attach.Attachment{Digest: digest, Size: len(payload), Payload: payload}); err != nil {
		t.Fatal(err)
	}

	back, found, err := s.Attachment(ctx, digest)
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("a payload that was just stored was not found")
	}
	if !bytes.Equal(back, payload) {
		t.Error("the payload came back different")
	}
}

func TestAnAbsentPayloadIsNotAnError(t *testing.T) {
	// The caller's correct response is to refuse the delivery, and attach.Reassemble already produces an error
	// saying exactly that. A second error path would mean two ways of describing one outcome.
	s := attachStore(t)

	_, found, err := s.Attachment(context.Background(), attach.Compute([]byte("never stored")))
	if err != nil {
		t.Errorf("looking up an absent payload returned an error: %v", err)
	}
	if found {
		t.Error("an absent payload reported as found")
	}
}

func TestTheSweepDeletesOnlyOrphans(t *testing.T) {
	// The sweep is what stops attachments leaking, and the risk is that it deletes a payload something still needs.
	s := attachStore(t)
	ctx := context.Background()

	kept := payloadOf(1000, 'K')
	orphan := payloadOf(2000, 'O')

	keptDigest := attach.Compute(kept)
	orphanDigest := attach.Compute(orphan)

	for _, a := range []attach.Attachment{
		{Digest: keptDigest, Size: len(kept), Payload: kept},
		{Digest: orphanDigest, Size: len(orphan), Payload: orphan},
	} {
		if err := s.PutAttachment(ctx, a); err != nil {
			t.Fatal(err)
		}
	}

	// A message exists and refers to one of them. Inserted directly, because this test is about the sweep rather
	// than about recording.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (channel, received_at, outcome, size, duration_ms)
		 VALUES ('c', '2026-08-21T00:00:00Z', 'delivered', 10, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LinkAttachments(ctx, id, []attach.Digest{keptDigest}); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.SweepAttachments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("swept %d payloads, want 1", deleted)
	}

	if _, found, _ := s.Attachment(ctx, keptDigest); !found {
		t.Error("the sweep deleted a payload a message still refers to")
	}
	if _, found, _ := s.Attachment(ctx, orphanDigest); found {
		t.Error("the sweep left an orphan behind")
	}
}

func TestASweepAfterAMessageIsDeletedRemovesItsPayload(t *testing.T) {
	// Retention deletes messages, and without this the payloads would outlive them - a leak growing at the rate of
	// the documents rather than the traffic.
	s := attachStore(t)
	ctx := context.Background()

	payload := payloadOf(3000, 'P')
	digest := attach.Compute(payload)
	if err := s.PutAttachment(ctx, attach.Attachment{Digest: digest, Size: len(payload), Payload: payload}); err != nil {
		t.Fatal(err)
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (channel, received_at, outcome, size, duration_ms)
		 VALUES ('c', '2026-08-21T00:00:00Z', 'delivered', 10, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := s.LinkAttachments(ctx, id, []attach.Digest{digest}); err != nil {
		t.Fatal(err)
	}

	// Nothing to sweep while the message exists.
	if deleted, err := s.SweepAttachments(ctx); err != nil || deleted != 0 {
		t.Fatalf("swept %d payloads while the message existed (err %v)", deleted, err)
	}

	if _, err := s.db.ExecContext(ctx, `DELETE FROM messages WHERE id = ?`, id); err != nil {
		t.Fatal(err)
	}

	deleted, err := s.SweepAttachments(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if deleted != 1 {
		t.Errorf("swept %d payloads after deleting the message, want 1", deleted)
	}
}

func TestStatsShowTheSavingRatherThanJustTheSize(t *testing.T) {
	// Count alone does not show deduplication working. The gap between one stored payload and forty references to it
	// is the whole benefit, and it is invisible without both numbers.
	s := attachStore(t)
	ctx := context.Background()

	payload := payloadOf(5000, 'S')
	digest := attach.Compute(payload)
	if err := s.PutAttachment(ctx, attach.Attachment{Digest: digest, Size: len(payload), Payload: payload}); err != nil {
		t.Fatal(err)
	}

	for i := 0; i < 40; i++ {
		res, err := s.db.ExecContext(ctx,
			`INSERT INTO messages (channel, received_at, outcome, size, duration_ms)
			 VALUES ('c', '2026-08-21T00:00:00Z', 'delivered', 10, 1)`)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		if err := s.LinkAttachments(ctx, id, []attach.Digest{digest}); err != nil {
			t.Fatal(err)
		}
	}

	stats, err := s.AttachmentStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Count != 1 {
		t.Errorf("count = %d, want 1", stats.Count)
	}
	if stats.References != 40 {
		t.Errorf("references = %d, want 40 - without this the saving is invisible", stats.References)
	}
	if stats.Orphans != 0 {
		t.Errorf("orphans = %d, want 0", stats.Orphans)
	}
}

func TestAMessagesAttachmentsCanBeListedWithoutLoadingThem(t *testing.T) {
	// The interface needs to say "this message has a 4 MB PDF" without streaming four megabytes to do it.
	s := attachStore(t)
	ctx := context.Background()

	small := payloadOf(1000, 'a')
	large := payloadOf(9000, 'b')

	for _, p := range [][]byte{small, large} {
		if err := s.PutAttachment(ctx, attach.Attachment{
			Digest: attach.Compute(p), Size: len(p), Payload: p,
		}); err != nil {
			t.Fatal(err)
		}
	}

	res, err := s.db.ExecContext(ctx,
		`INSERT INTO messages (channel, received_at, outcome, size, duration_ms)
		 VALUES ('c', '2026-08-21T00:00:00Z', 'delivered', 10, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if err := s.LinkAttachments(ctx, id,
		[]attach.Digest{attach.Compute(small), attach.Compute(large)}); err != nil {
		t.Fatal(err)
	}

	list, err := s.MessageAttachments(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 2 {
		t.Fatalf("listed %d attachments, want 2", len(list))
	}
	// Largest first, because that is the one somebody is looking for.
	if list[0].Size != 9000 {
		t.Errorf("first listed is %d bytes, want the largest", list[0].Size)
	}
	if list[0].Messages != 1 {
		t.Errorf("reference count = %d, want 1", list[0].Messages)
	}
}

func TestLinkingNothingIsNotAnError(t *testing.T) {
	// Most messages have no attachments, and the recording path must not need a special case for the common one.
	if err := attachStore(t).LinkAttachments(context.Background(), 1, nil); err != nil {
		t.Errorf("linking no attachments returned an error: %v", err)
	}
}
