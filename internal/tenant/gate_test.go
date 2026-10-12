package tenant

import (
	"errors"
	"testing"
	"time"
)

func TestTheGateRefusesOverTheRateAndAdmitsAgainNextMinute(t *testing.T) {
	now := time.Date(2026, 10, 12, 9, 0, 0, 0, time.UTC)
	g := &Gate{Limits: func() (Limits, error) { return Limits{MaxMessagesPerMinute: 3}, nil }, now: func() time.Time { return now }}

	for i := 0; i < 3; i++ {
		if err := g.Admit(); err != nil {
			t.Fatalf("message %d refused under the limit: %v", i+1, err)
		}
	}
	if err := g.Admit(); !errors.Is(err, ErrOverRate) {
		t.Fatalf("the fourth message in a minute was admitted (%v)", err)
	}
	now = now.Add(time.Minute)
	if err := g.Admit(); err != nil {
		t.Fatalf("refused in the next minute: %v", err)
	}
}

func TestTheGateRefusesOverStorage(t *testing.T) {
	stored := int64(5 * 1024 * 1024)
	g := &Gate{
		Limits:      func() (Limits, error) { return Limits{MaxStorageMB: 5}, nil },
		StoredBytes: func() (int64, error) { return stored, nil },
	}
	if err := g.Admit(); !errors.Is(err, ErrOverStorage) {
		t.Fatalf("a tenant at its storage limit was admitted (%v)", err)
	}
}

func TestABrokenLimitReaderAdmits(t *testing.T) {
	g := &Gate{Limits: func() (Limits, error) { return Limits{}, errors.New("database is locked") }}
	for i := 0; i < 1000; i++ {
		if err := g.Admit(); err != nil {
			t.Fatalf("a limiter that cannot read its limits refused a message: %v", err)
		}
	}
	var none *Gate
	if err := none.Admit(); err != nil {
		t.Fatalf("no gate refused: %v", err)
	}
}
