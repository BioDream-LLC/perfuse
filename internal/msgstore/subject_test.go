package msgstore

import (
	"context"
	"testing"
	"time"
)

// A person under a legal hold keeps their messages past the retention window; everyone else's go as usual.
func TestALegalHoldSparesMessagesFromRetention(t *testing.T) {
	s := indexing(t)
	s.RetentionDays = 30
	ctx := context.Background()
	old := time.Now().UTC().AddDate(0, 0, -400)
	held := record(t, s, &Message{Channel: "adt", ReceivedAt: old, Outcome: Delivered, Raw: []byte(admissionHL7), Size: len(admissionHL7)})
	other := []byte("MSH|^~\\&|A|B|C|D|20250101||ADT^A04|9|P|2.5\rPID|1||OTHER77^^^HOSP^MR||DOE^JO\r")
	gone := record(t, s, &Message{Channel: "adt", ReceivedAt: old, Outcome: Delivered, Raw: other, Size: len(other)})
	s.SetHolds(func(context.Context) []string { return NormaliseIdentifiers([]string{"MRN0012345"}) })
	if _, _, err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if m, err := s.Get(ctx, "", held); err != nil || len(m.Raw) == 0 {
		t.Fatalf("the held message (or its body) went: %v", err)
	}
	if _, err := s.Get(ctx, "", gone); err == nil {
		t.Fatal("a message nobody holds outlived the retention window")
	}
	// The hold lifted, the next prune takes it.
	s.SetHolds(func(context.Context) []string { return nil })
	if _, _, err := s.Prune(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(ctx, "", held); err == nil {
		t.Fatal("the hold was lifted but the message stayed")
	}
}
