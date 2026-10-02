package dbtime

import (
	"math/rand"
	"sort"
	"testing"
	"time"
)

// The property the format exists for: text order equals time order.
//
// Written as a property over many random instants rather than as a handful of examples, because the
// defect this replaces was invisible to examples. Every timestamp anyone would think to write down by
// hand - .5, .25, .123 - sorts correctly under the old format too. The failures are the pairs where one
// fraction is a prefix of the other, which is not a case that occurs to somebody choosing test data.
func TestTextOrderMatchesTimeOrder(t *testing.T) {
	r := rand.New(rand.NewSource(1)) // fixed seed, so a failure is reproducible
	base := time.Date(2026, 8, 25, 20, 56, 33, 0, time.UTC)

	for i := 0; i < 20000; i++ {
		a := base.Add(time.Duration(r.Intn(2_000_000_000)) * time.Nanosecond)
		b := base.Add(time.Duration(r.Intn(2_000_000_000)) * time.Nanosecond)

		if !SortsCorrectly(a, b) {
			t.Fatalf("text order disagrees with time order:\n  a = %s (%s)\n  b = %s (%s)",
				a.Format(time.RFC3339Nano), Format(a),
				b.Format(time.RFC3339Nano), Format(b))
		}
	}
}

// The specific pair that was failing in the wild, kept as a regression case with its explanation.
func TestTrailingZeroFractionStillSortsBefore(t *testing.T) {
	base := time.Date(2026, 8, 25, 20, 56, 33, 0, time.UTC)

	earlier := base.Add(123450 * time.Microsecond) // RFC3339Nano prints .12345 - a zero is dropped
	later := base.Add(123456 * time.Microsecond)   // RFC3339Nano prints .123456

	// Under RFC3339Nano this comparison came out backwards, which is the defect.
	if old := earlier.Format(time.RFC3339Nano) < later.Format(time.RFC3339Nano); old {
		t.Log("note: RFC3339Nano happened to order this pair correctly on this platform")
	}

	if !(Format(earlier) < Format(later)) {
		t.Errorf("the earlier instant does not sort first:\n  earlier = %s\n  later   = %s",
			Format(earlier), Format(later))
	}
}

// Sorting a batch of stored strings must reproduce the order they happened in.
//
// This is the shape the stores actually use - ORDER BY on a column - so it is worth asserting directly
// rather than inferring it from the pairwise property.
func TestSortingStoredStringsReproducesChronology(t *testing.T) {
	base := time.Date(2026, 8, 25, 20, 56, 33, 0, time.UTC)

	// Deliberately includes fractions with trailing zeros and fractions that are prefixes of others.
	offsets := []time.Duration{
		0,
		1 * time.Nanosecond,
		100 * time.Nanosecond,
		1 * time.Microsecond,
		123450 * time.Microsecond,
		123456 * time.Microsecond,
		500 * time.Millisecond,
		999999999 * time.Nanosecond,
	}

	type row struct {
		at   time.Time
		text string
	}
	rows := make([]row, 0, len(offsets))
	for _, off := range offsets {
		at := base.Add(off)
		rows = append(rows, row{at: at, text: Format(at)})
	}

	sorted := make([]row, len(rows))
	copy(sorted, rows)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].text < sorted[j].text })

	for i := range sorted {
		if !sorted[i].at.Equal(rows[i].at) {
			t.Fatalf("sorting the stored text reordered the rows at position %d:\n  want %s\n  got  %s",
				i, rows[i].at.Format(time.RFC3339Nano), sorted[i].at.Format(time.RFC3339Nano))
		}
	}
}

// Every stored timestamp is the same length, which is what makes the comparison safe.
func TestFormatIsFixedWidth(t *testing.T) {
	base := time.Date(2026, 8, 25, 20, 56, 33, 0, time.UTC)
	want := len(Format(base))

	for _, off := range []time.Duration{
		0, 1, 10, 100, 1000, 500 * time.Millisecond, 999999999 * time.Nanosecond,
	} {
		got := Format(base.Add(off))
		if len(got) != want {
			t.Errorf("%s is %d characters, want %d; a variable width breaks text comparison",
				got, len(got), want)
		}
	}
}

// A timestamp with an offset is stored as UTC, or comparing text would compare wall clocks in
// different zones.
func TestFormatNormalisesToUTC(t *testing.T) {
	zone := time.FixedZone("CEST", 2*60*60)
	local := time.Date(2026, 8, 25, 22, 56, 33, 123456789, zone)

	got := Format(local)
	if got[len(got)-1] != 'Z' {
		t.Errorf("%s is not stored in UTC", got)
	}

	back, err := Parse(got)
	if err != nil {
		t.Fatal(err)
	}
	if !back.Equal(local) {
		t.Errorf("round trip changed the instant: %s became %s", local, back)
	}
}

// Rows written before this package existed must still read.
func TestParseAcceptsTheOldVariableWidthForm(t *testing.T) {
	for _, old := range []string{
		"2026-08-25T20:56:33Z",
		"2026-08-25T20:56:33.5Z",
		"2026-08-25T20:56:33.12345Z",
		"2026-08-25T20:56:33.123456789Z",
	} {
		if _, err := Parse(old); err != nil {
			t.Errorf("cannot read %s, which existing databases contain: %v", old, err)
		}
	}
}
