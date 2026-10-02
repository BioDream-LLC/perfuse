package msgstore

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The property the whole feature exists for.
//
// A clinic's lab feed is silent every night and every weekend. A fixed threshold that tolerates Sunday 3am can never
// catch a real outage on Tuesday afternoon, and one that catches Tuesday pages every Saturday until somebody turns it
// off - taking Tuesday with it.
func TestAQuietWeekendDoesNotLookLikeAnOutageButAQuietTuesdayDoes(t *testing.T) {
	s := open(t)
	ctx := context.Background()

	// Four weeks of a clinic feed: busy 09:00-17:00 on weekdays, silent otherwise.
	base := mondayAt(t, 9, 5)
	for week := range 4 {
		for day := range 7 {
			for hour := range 24 {
				at := base.AddDate(0, 0, week*7+day).Add(time.Duration(hour-9) * time.Hour)
				n := 0
				if day < 5 && hour >= 9 && hour < 17 {
					n = 40
				}
				recordAt(t, s, "clinic-labs", at, n)
			}
		}
	}

	rhythms, err := s.LearnRhythm(ctx, "", 8)
	if err != nil {
		t.Fatal(err)
	}
	r := rhythms["clinic-labs"]
	if r == nil {
		t.Fatal("the feed was not learned at all")
	}

	// Sunday 03:00: silence is normal, so nothing can be said and nothing should be.
	sunday3am := base.AddDate(0, 0, 6).Add(-6 * time.Hour)
	if _, ok := r.Shortfall(sunday3am, 0); ok {
		t.Errorf("silence at %s produced a verdict; this is the alert that gets switched off every weekend",
			DescribeHour(HourOfWeek(sunday3am)))
	}

	// Tuesday 14:00: silence here is the outage nobody would otherwise hear about, because silence raises no error.
	tuesday2pm := base.AddDate(0, 0, 1).Add(5 * time.Hour)
	frac, ok := r.Shortfall(tuesday2pm, 0)
	if !ok {
		t.Fatalf("no verdict for %s, which is a busy hour", DescribeHour(HourOfWeek(tuesday2pm)))
	}
	if frac < 0.99 {
		t.Errorf("shortfall = %.2f for a completely silent busy hour, want ~1", frac)
	}

	// And normal traffic in that same hour is not an alert.
	if frac, _ := r.Shortfall(tuesday2pm, 40); frac != 0 {
		t.Errorf("shortfall = %.2f when the feed carried its usual 40", frac)
	}
}

// A backlog flush must not raise the bar so far that real silence passes underneath it.
//
// This is why the median is used rather than the mean. Feeds get backlogs, reprocessing runs and migration dumps often
// enough that a mean is not safe: one 40,000-message hour against a normal 300 moves a mean above 3,000, and the feed
// can then drop to 500 - a sixth of normal - while still looking fine.
func TestOneHugeBacklogHourDoesNotDistortTheNormal(t *testing.T) {
	s := open(t)
	base := mondayAt(t, 10, 9)

	// Eight weeks at 300, and one week where a backlog flushed 40,000.
	for week := range 8 {
		n := 300
		if week == 3 {
			n = 6000
		}
		recordAt(t, s, "adt", base.AddDate(0, 0, week*7), n)
	}

	r := mustLearn(t, s, "adt", 12)
	h := r.Hours[HourOfWeek(base)]

	if h.Median != 300 {
		t.Errorf("median = %v, want 300; the mean here is above 1000", h.Median)
	}
	// The spread is still reported, because a caller deciding how much to trust the number needs it.
	if h.High != 6000 {
		t.Errorf("high = %d, want the backlog hour recorded", h.High)
	}

	// The consequence: a drop to a sixth of normal is caught.
	if frac, ok := r.Shortfall(base, 50); !ok || frac < 0.8 {
		t.Errorf("shortfall = %.2f, ok = %v; 50 against a normal 300 should read as a serious drop", frac, ok)
	}
}

// An erratic hour has no normal, and claiming one produces an alert that fires on an ordinary quiet day.
func TestAnErraticHourIsRefusedRatherThanAveraged(t *testing.T) {
	s := open(t)
	base := mondayAt(t, 13, 7)

	// Wildly different every week: 2, 500, 8, 900, 15, 700.
	for i, n := range []int{2, 500, 8, 900, 15, 700} {
		recordAt(t, s, "erratic", base.AddDate(0, 0, i*7), n)
	}

	r := mustLearn(t, s, "erratic", 12)
	if _, ok := r.Shortfall(base, 1); ok {
		t.Error("a feed ranging from 2 to 900 in the same hour was given a normal to fall below")
	}

	// And the explanation says why, rather than going quiet about it.
	if got := r.Describe(base); !strings.Contains(got, "erratic") {
		t.Errorf("Describe = %q, which does not explain why no alert is possible", got)
	}
}

// One week of history is last Tuesday, not a rhythm.
func TestASingleWeekIsNotTreatedAsAPattern(t *testing.T) {
	s := open(t)
	base := mondayAt(t, 11, 2)
	recordAt(t, s, "brand-new", base, 500)

	r := mustLearn(t, s, "brand-new", 4)
	if _, ok := r.Shortfall(base, 0); ok {
		t.Error("a single observation was treated as a normal; every new channel would alert in its second week")
	}
}

// An hour that has never been silent going silent must be caught even if it happened once.
//
// Being wrong in the tolerant direction is a lab feed that stopped on Friday and was noticed on Monday.
func TestOneMaintenanceWindowDoesNotMakeSilenceAcceptable(t *testing.T) {
	s := open(t)
	base := mondayAt(t, 15, 11)

	// Nine busy weeks and one silent one - a bank holiday or a maintenance window.
	for week := range 10 {
		n := 200
		if week == 5 {
			n = 0
		}
		recordAt(t, s, "results", base.AddDate(0, 0, week*7), n)
	}

	r := mustLearn(t, s, "results", 12)
	h := r.Hours[HourOfWeek(base)]
	if h.Quiet() {
		t.Errorf("one silent week out of %d made silence normal for this hour", h.Observations)
	}
	if _, ok := r.Shortfall(base, 0); !ok {
		t.Error("silence in an hour that is silent one week in ten produced no verdict")
	}
}

// A channel that has not been running has no history, and must not look like one that just stopped.
func TestAChannelWithNoHistoryIsNotReportedAsSilent(t *testing.T) {
	s := open(t)
	base := mondayAt(t, 8, 2)
	recordAt(t, s, "other", base, 100)

	rhythms, err := s.LearnRhythm(context.Background(), "", 4)
	if err != nil {
		t.Fatal(err)
	}
	if _, present := rhythms["never-ran"]; present {
		t.Error("a channel that never recorded a message appeared in the rhythms")
	}
}

// Monday must be index zero, or every weekday/weekend comparison is off by a day.
func TestTheWeekStartsOnMonday(t *testing.T) {
	// 2026-08-31 is a Monday.
	monday := time.Date(2026, 8, 31, 0, 0, 0, 0, time.UTC)
	if got := HourOfWeek(monday); got != 0 {
		t.Errorf("Monday 00:00 = %d, want 0", got)
	}
	if got := HourOfWeek(monday.Add(14 * time.Hour)); got != 14 {
		t.Errorf("Monday 14:00 = %d, want 14", got)
	}
	// Sunday is last, so a working week is contiguous.
	sunday := monday.AddDate(0, 0, 6)
	if got := HourOfWeek(sunday); got != 144 {
		t.Errorf("Sunday 00:00 = %d, want 144", got)
	}
	if got := HourOfWeek(monday.AddDate(0, 0, 7)); got != 0 {
		t.Errorf("the following Monday = %d, want 0", got)
	}

	// And the description has to be readable, since it goes into an alert somebody reads at 2am.
	if got := DescribeHour(0); got != "Monday 00:00" {
		t.Errorf("DescribeHour(0) = %q", got)
	}
	if got := DescribeHour(144); got != "Sunday 00:00" {
		t.Errorf("DescribeHour(144) = %q", got)
	}
}

// Every hour of the week is present even with no observations, so a caller cannot read a gap as a quiet hour.
func TestEveryHourOfTheWeekIsPresent(t *testing.T) {
	s := open(t)
	base := mondayAt(t, 9, 2)
	recordAt(t, s, "sparse", base, 10)

	r := mustLearn(t, s, "sparse", 4)
	for h := range HoursInWeek {
		if r.Hours[h].Hour != h {
			t.Fatalf("hour %d is missing or misindexed (got %d)", h, r.Hours[h].Hour)
		}
	}
}

func mustLearn(t *testing.T, s *Store, channel string, weeks int) *Rhythm {
	t.Helper()
	rhythms, err := s.LearnRhythm(context.Background(), "", weeks)
	if err != nil {
		t.Fatal(err)
	}
	r := rhythms[channel]
	if r == nil {
		t.Fatalf("no rhythm learned for %q", channel)
	}
	return r
}

// mondayAt returns a Monday at the given hour, weeksBack weeks ago.
//
// The caller has to say how far back, because history written outside the window LearnRhythm examines is simply not
// there - and the symptom is a channel with no rhythm at all rather than an obviously wrong one.
func mondayAt(t *testing.T, hour, weeksBack int) time.Time {
	t.Helper()
	d := time.Now().UTC().AddDate(0, 0, -7*weeksBack).Truncate(24 * time.Hour)
	for d.Weekday() != time.Monday {
		d = d.AddDate(0, 0, 1)
	}
	return d.Add(time.Duration(hour) * time.Hour)
}

// recordAt writes n messages into one hour, which is the unit a rhythm is built from.
func recordAt(t *testing.T, s *Store, channel string, at time.Time, n int) {
	t.Helper()
	for i := range n {
		// Milliseconds, not seconds. Spreading by seconds pushes a few thousand messages into the following
		// hours, which silently moves them into other buckets and made this helper build the wrong history.
		record(t, s, sample(channel, Delivered, at.Add(time.Duration(i)*time.Millisecond)))
	}
}
