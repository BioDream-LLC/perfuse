package msgstore

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// Searching by content is the feature most likely to be used under pressure, so what matters most
// is that a partial answer never looks like a complete one.

func searchStore(t *testing.T) *Store {
	t.Helper()
	return payloadStore(t)
}

func recordADT(t *testing.T, s *Store, channel, control, sex, mrn string) {
	t.Helper()
	body := fmt.Sprintf(
		"MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|%s|P|2.5\r"+
			"PID|1||%s^^^MRN||SMITH^JOHN||19700101|%s\r", control, mrn, sex)

	if _, err := s.Record(context.Background(), &Message{
		Channel:     channel,
		ReceivedAt:  time.Now(),
		Outcome:     Delivered,
		MessageType: "ADT",
		ControlID:   control,
		Raw:         []byte(body),
		Size:        len(body),
	}); err != nil {
		t.Fatal(err)
	}
}

func TestSearchFindsMessagesByContent(t *testing.T) {
	// The question the old substring search could not answer: not "which message contains this
	// string" but "which messages have this field set to this value".
	s := searchStore(t)
	recordADT(t, s, "feed", "C1", "1", "1111111")
	recordADT(t, s, "feed", "C2", "2", "2222222")
	recordADT(t, s, "feed", "C3", "1", "3333333")

	res, err := s.SearchByExpression(context.Background(), ExpressionSearch{
		Where: "PID-8 == '1'",
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(res.Matches) != 2 {
		t.Fatalf("matched %d messages, want 2", len(res.Matches))
	}
	if res.Examined != 3 {
		t.Errorf("examined = %d, want 3", res.Examined)
	}
}

func TestSearchUsesTheSameLanguageAsAFilter(t *testing.T) {
	// One query syntax rather than two, so an expression worked out here can be pasted straight
	// into a channel filter - which is usually the outcome of the investigation.
	s := searchStore(t)
	recordADT(t, s, "feed", "C1", "1", "1111111")

	// PID-8 genuinely absent, not merely empty. The segment ends before it, so no delimiter
	// creates the field at all.
	//
	// My first attempt at this fixture used an empty value with a trailing pipe, and the search
	// correctly matched it: "exists" in this expression language means present, whether or not
	// there is content, because in an A08 update an empty field means "no change" while an absent
	// one means the sender never sent that field. That distinction is deliberate and the test was
	// wrong to assume otherwise.
	absent := "MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C2|P|2.5\r" +
		"PID|1||2222222^^^MRN||SMITH^JOHN\r"
	if _, err := s.Record(context.Background(), &Message{
		Channel: "feed", ReceivedAt: time.Now(), Outcome: Delivered,
		MessageType: "ADT", ControlID: "C2", Raw: []byte(absent), Size: len(absent),
	}); err != nil {
		t.Fatal(err)
	}

	res, err := s.SearchByExpression(context.Background(), ExpressionSearch{
		Where: "PID-8 exists and PID-3 exists",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("matched %d, want 1: %+v", len(res.Matches), res.Matches)
	}
	if res.Matches[0].ControlID != "C1" {
		t.Errorf("matched %q", res.Matches[0].ControlID)
	}
}

func TestSearchReportsThePathsItLookedAt(t *testing.T) {
	// A useful check that the expression says what its author meant, before they trust the answer.
	s := searchStore(t)
	recordADT(t, s, "feed", "C1", "1", "1111111")

	res, err := s.SearchByExpression(context.Background(), ExpressionSearch{
		Where: "PID-8 == '1'",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Paths) == 0 {
		t.Fatal("the search did not report which paths it read")
	}
	found := false
	for _, p := range res.Paths {
		if p == "PID-8" {
			found = true
		}
	}
	if !found {
		t.Errorf("paths = %v", res.Paths)
	}
}

func TestSearchSaysWhenItStoppedEarly(t *testing.T) {
	// Without this a partial answer reads as a complete one, and somebody concludes a message does
	// not exist when it was simply beyond the bound.
	s := searchStore(t)
	for i := 0; i < 20; i++ {
		recordADT(t, s, "feed", fmt.Sprintf("C%d", i), "2", "1111111")
	}

	res, err := s.SearchByExpression(context.Background(), ExpressionSearch{
		Where:   "PID-8 == '1'",
		Examine: 5,
	})
	if err != nil {
		t.Fatal(err)
	}

	if !res.Truncated {
		t.Error("a bounded scan did not say it stopped early")
	}
	if res.Examined != 5 {
		t.Errorf("examined = %d, want 5", res.Examined)
	}
	if res.Available != 20 {
		t.Errorf("available = %d, want 20: a reader needs to see what fraction was looked at",
			res.Available)
	}
	if len(res.Matches) != 0 {
		t.Errorf("matched %d despite none having sex 1", len(res.Matches))
	}
}

func TestSearchNarrowsBeforeItParses(t *testing.T) {
	// The difference between examining a thousand messages and a hundred thousand.
	s := searchStore(t)
	recordADT(t, s, "wanted", "C1", "1", "1111111")
	for i := 0; i < 10; i++ {
		recordADT(t, s, "other", fmt.Sprintf("O%d", i), "1", "2222222")
	}

	res, err := s.SearchByExpression(context.Background(), ExpressionSearch{
		Where:   "PID-8 == '1'",
		Channel: "wanted",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Examined != 1 {
		t.Errorf("examined = %d, want 1: the channel narrowing did not apply before parsing",
			res.Examined)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("matched %d, want 1", len(res.Matches))
	}
}

func TestSearchCountsUnreadableMessages(t *testing.T) {
	// A search over traffic that is largely unparseable found nothing for a reason worth knowing.
	s := searchStore(t)
	recordADT(t, s, "feed", "C1", "1", "1111111")
	recordPayload(t, s, "feed", "this is not a message")

	res, err := s.SearchByExpression(context.Background(), ExpressionSearch{
		Where: "PID-8 == '1'",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Unreadable != 1 {
		t.Errorf("unreadable = %d, want 1", res.Unreadable)
	}
	if len(res.Matches) != 1 {
		t.Errorf("matched %d, want 1", len(res.Matches))
	}
}

func TestSearchRejectsABadExpressionBeforeReadingAnything(t *testing.T) {
	// A bad expression should cost nothing, and it is the most likely thing to be wrong.
	s := searchStore(t)
	recordADT(t, s, "feed", "C1", "1", "1111111")

	if _, err := s.SearchByExpression(context.Background(), ExpressionSearch{
		Where: "this is not an expression (((",
	}); err == nil {
		t.Fatal("a malformed expression was accepted")
	}

	if _, err := s.SearchByExpression(context.Background(), ExpressionSearch{}); err == nil {
		t.Fatal("an empty expression was accepted")
	}
}

func TestSearchDoesNotReturnPayloads(t *testing.T) {
	// A search returning a hundred messages must not return a hundred payloads to a browser. The
	// detail view fetches one when somebody opens it.
	s := searchStore(t)
	recordADT(t, s, "feed", "C1", "1", "1111111")

	res, err := s.SearchByExpression(context.Background(), ExpressionSearch{Where: "PID-8 exists"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 1 {
		t.Fatalf("matched %d", len(res.Matches))
	}
	if len(res.Matches[0].Raw) != 0 {
		t.Error("the search returned a payload in a result row")
	}
}

func TestSearchTestsEachMessageAgainstItsOwnBytes(t *testing.T) {
	// A driver may reuse the scan buffer between rows. Without copying, every message in a scan
	// gets tested against the same bytes, and the search returns either everything or nothing for
	// reasons impossible to see from the outside.
	s := searchStore(t)

	// Equal-length payloads, because a shared buffer is least likely to be noticed when the rows
	// are the same size.
	for i := 0; i < 20; i++ {
		sex := "1"
		if i%2 == 0 {
			sex = "2"
		}
		recordADT(t, s, "feed", fmt.Sprintf("C%02d", i), sex, "1111111")
	}

	res, err := s.SearchByExpression(context.Background(), ExpressionSearch{Where: "PID-8 == '1'"})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Matches) != 10 {
		t.Fatalf("matched %d of 20, want exactly half: a result of 0 or 20 means the rows shared "+
			"a buffer", len(res.Matches))
	}
}

func TestSearchStopsWhenTheCallerGivesUp(t *testing.T) {
	s := searchStore(t)
	for i := 0; i < 100; i++ {
		recordADT(t, s, "feed", fmt.Sprintf("C%d", i), "1", "1111111")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := s.SearchByExpression(ctx, ExpressionSearch{Where: "PID-8 == '1'"}); err == nil {
		t.Fatal("a cancelled search ran to completion")
	}
}

func TestASearchWithNoMatchesReturnsAnEmptyListNotNil(t *testing.T) {
	// A client doing matches.length on null crashes, and it crashes on precisely the search that
	// found nothing - the common case, and the one nobody tests by hand. Found by running a live
	// search for a value that did not occur.
	s := searchStore(t)
	recordADT(t, s, "feed", "C1", "1", "1111111")

	res, err := s.SearchByExpression(context.Background(), ExpressionSearch{
		Where: "PID-8 == 'nothing-like-this'",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Matches == nil {
		t.Fatal("Matches is nil, so it marshals as null rather than as an empty array")
	}
	if len(res.Matches) != 0 {
		t.Errorf("matched %d", len(res.Matches))
	}
}
