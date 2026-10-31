package msgstore

import (
	"context"
	"testing"
	"time"
)

func TestMinuteCountsGroupByChannelAndMinute(t *testing.T) {
	s := open(t)
	base := time.Now().UTC().Truncate(time.Minute).Add(-time.Hour)
	for i := range 3 {
		record(t, s, sample("adt", Delivered, base.Add(time.Duration(i)*time.Second)))
	}
	record(t, s, sample("adt", Delivered, base.Add(time.Minute)))
	record(t, s, sample("lab", Delivered, base))
	got, err := s.MinuteCounts(context.Background(), "", base.Add(-time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int64{"adt@0": 3, "adt@1": 1, "lab@0": 1}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for _, c := range got {
		key := c.Channel + "@" + string(rune('0'+int(c.Minute.Sub(base)/time.Minute)))
		if want[key] != c.Count {
			t.Errorf("%s: %d, want %d", key, c.Count, want[key])
		}
	}
}
