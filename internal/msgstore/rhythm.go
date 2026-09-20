package msgstore

import (
	"context"
	"fmt"
	"math"
	"sort"
	"time"

	"github.com/biodream-llc/perfuse/internal/dbtime"
)

// Rhythm is what a channel's traffic normally looks like, hour by hour, across a week.
//
// # Why a week and not a day
//
// A fixed no-traffic threshold only works on a feed that never goes quiet. A hospital ADT feed is like that. Almost
// nothing in a clinic is: the lab feed stops at six, the referral feed is dead all weekend, and the immunisation
// registry submits once a night. Set a threshold those can pass at 3am on a Sunday and it will never catch a real
// outage on Tuesday afternoon. Set one that catches Tuesday and it pages every Saturday, so somebody turns it off,
// and then Tuesday is unwatched too.
//
// So the shape being learned is the hour of the week - 168 of them - because that is the smallest unit that
// distinguishes Tuesday 2pm from Sunday 2am. A day-of-week-only model cannot tell 9am from midnight; an hour-only
// model cannot tell Wednesday from Sunday.
//
// # Why the median and not the mean
//
// One day where a backlog flushed 40,000 messages through a feed that normally carries 300 moves a mean far enough
// that the feed can then go completely silent without falling below it. The median ignores it. Feeds get backlogs,
// reprocessing runs and migration dumps often enough that this is not a hypothetical.
//
// # What this deliberately is not
//
// Not a forecast, and not anomaly detection in the machine-learning sense. It answers one question: for this hour of
// this weekday, what has this channel usually done? Everything clever beyond that would be harder to explain to
// somebody at 2am deciding whether to wake a colleague, and an alert nobody trusts is an alert nobody acts on.
type Rhythm struct {
	// Channel is the feed this describes.
	Channel string `json:"channel"`

	// Hours holds one entry per hour of the week, indexed 0-167, Monday 00:00 UTC at zero.
	//
	// Every hour is present even with no observations, so a caller cannot mistake a gap in the array for a quiet
	// hour. Which of the two it is is the whole question, and Observations is what says.
	Hours [HoursInWeek]HourStat `json:"hours"`

	// Weeks is how many distinct calendar weeks contributed.
	//
	// Reported because it is the honest measure of how much this can be trusted. One week of history means every
	// hour has exactly one observation and the median is that single number, which is not a rhythm - it is last
	// Tuesday.
	Weeks int `json:"weeks"`

	// From and Until bound the history examined.
	From  time.Time `json:"from"`
	Until time.Time `json:"until"`
}

// HoursInWeek is the number of buckets in a rhythm.
const HoursInWeek = 168

// HourStat is what one hour of the week normally carries.
type HourStat struct {
	// Hour is the index, 0-167. Monday 00:00 is 0.
	Hour int `json:"hour"`

	// Median is the typical count for this hour.
	Median float64 `json:"median"`

	// Low and High are the smallest and largest counts seen.
	//
	// Kept because the spread is what tells somebody whether the median means anything. A feed that has carried
	// between 2 and 900 messages in this hour has no useful normal, and the alert should say so rather than pick a
	// number from the middle and imply confidence.
	Low  int64 `json:"low"`
	High int64 `json:"high"`

	// Floor is the level this hour normally stays above: the 20th percentile of what has been seen.
	//
	// This, not Low, is what decides whether an alert is safe. A shortfall alert fires when traffic is low, so the
	// question is how low the feed goes on an ordinary week - if it routinely dips to a fifth of its median, an alert
	// pitched anywhere useful will fire on ordinary weeks. The true minimum cannot answer that, because one bank
	// holiday drags it to zero and would disqualify a feed that is otherwise perfectly steady.
	Floor float64 `json:"floor"`

	// Observations is how many weeks contributed a count for this hour.
	Observations int `json:"observations"`

	// Silent is how many of those observations were zero.
	//
	// The distinction that matters most. An hour that has been silent nine weeks out of ten is a quiet hour, and a
	// tenth silent week is not news. An hour that has never been silent going quiet is the outage nobody would
	// otherwise hear about, because silence raises no error.
	Silent int `json:"silent"`
}

// Quiet reports whether this hour is normally silent.
//
// The test is whether silence has ever been normal here, not whether it is common. An hour that is usually busy but
// was silent once - a bank holiday, a maintenance window - should not have that one week treated as licence to be
// silent again unnoticed. Being wrong in this direction produces an alert somebody dismisses; being wrong in the
// other produces a lab feed that stopped on Friday and was noticed on Monday.
func (h HourStat) Quiet() bool {
	if h.Observations == 0 {
		return true // Never observed. Nothing is known, so nothing is claimed.
	}
	return h.Silent*2 >= h.Observations
}

// Reliable reports whether this hour's median is worth alerting on.
//
// Fewer than two observations is not a pattern, and a median of zero cannot be fallen below. The third condition is
// the one that took two failing tests to get right: the feed must normally stay within a quarter of its median.
//
// My first attempt compared the high against the median, and it was wrong in both directions at once. A feed that
// flushed a backlog once was disqualified for months afterwards, because a single 6,000-message hour against a normal
// 300 looks like chaos - it is not, it is one Tuesday. And a genuinely erratic feed carrying between 2 and 900
// messages in the same hour every week passed, because its high was only three times its median. The high says
// nothing about whether an alert will cry wolf; a shortfall alert fires when traffic is low, so only the low end can
// answer that. Hence Floor, and hence the 20th percentile rather than the minimum.
func (h HourStat) Reliable() bool {
	if h.Observations < 2 || h.Median <= 0 {
		return false
	}
	return h.Floor*4 >= h.Median
}

// HourOfWeek returns the rhythm index for a time. Monday 00:00 UTC is 0.
//
// UTC deliberately, and it is a real limitation rather than an oversight: a clinic in a timezone with daylight
// saving has two hours a year that do not line up. Recorded here because the alternative - carrying a timezone per
// channel - is worth doing and is not done yet, and the wrong version of this silently shifts every clinic's
// evening by an hour twice a year.
func HourOfWeek(t time.Time) int {
	t = t.UTC()
	// Go's Weekday has Sunday at zero. Monday first keeps a working week contiguous, which matters because the
	// interesting pattern in almost every clinical feed is weekday against weekend.
	day := (int(t.Weekday()) + 6) % 7
	return day*24 + t.Hour()
}

// DescribeHour renders an hour index as something a person reads.
func DescribeHour(hour int) string {
	if hour < 0 || hour >= HoursInWeek {
		return "an hour outside the week"
	}
	days := [...]string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}
	return fmt.Sprintf("%s %02d:00", days[hour/24], hour%24)
}

// LearnRhythm builds the weekly shape of every channel's traffic.
//
// weeks bounds how far back to look. Four is a reasonable default: enough that one odd week cannot dominate the
// median, short enough that a feed whose volume genuinely changed - a new clinic site, a retired interface - is
// re-learned within a month rather than being compared against a shape that no longer exists.
func (s *Store) LearnRhythm(ctx context.Context, tenantID string, weeks int) (map[string]*Rhythm, error) {
	if weeks < 1 {
		weeks = 4
	}

	until := time.Now().UTC().Truncate(time.Hour)
	from := until.AddDate(0, 0, -7*weeks)

	// Counted per absolute hour first, then folded into the week. Folding in SQL would need strftime arithmetic that
	// differs between engines, and this loop runs over at most 168 times the number of channels.
	rows, err := s.querier().QueryContext(ctx,
		`SELECT channel,
		        CAST(strftime('%s', received_at) AS INTEGER) / 3600 AS hour_start,
		        COUNT(*)
		   FROM messages
		  WHERE tenant_id = ? AND received_at >= ? AND received_at < ?
		  GROUP BY channel, hour_start`,
		tenantOrDefault(tenantID), dbtime.Format(from), dbtime.Format(until))
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Every observed hour, gathered per channel and per hour-of-week.
	seen := map[string]map[int][]int64{}
	// Which absolute hours produced any row at all, so an hour with no messages can be told from an hour outside
	// the retention window. A channel that was not running has no history here, and inventing zeroes for it would
	// make a newly created channel look like one that just went silent.
	present := map[string]map[int64]bool{}

	for rows.Next() {
		var channel string
		var hourStart, count int64
		if err := rows.Scan(&channel, &hourStart, &count); err != nil {
			return nil, err
		}
		if seen[channel] == nil {
			seen[channel] = map[int][]int64{}
			present[channel] = map[int64]bool{}
		}
		how := HourOfWeek(time.Unix(hourStart*3600, 0))
		seen[channel][how] = append(seen[channel][how], count)
		present[channel][hourStart] = true
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	out := map[string]*Rhythm{}
	for channel, byHour := range seen {
		r := &Rhythm{Channel: channel, Weeks: weeks, From: from, Until: until}

		// The silent hours. An hour inside the window with no row is a real zero for a channel that was otherwise
		// carrying traffic, and those zeroes are half the signal: without them, an hour that is silent six weeks
		// out of seven looks like it always carries messages.
		firstSeen, lastSeen := earliestLatest(present[channel])
		for h := firstSeen; h < lastSeen; h++ {
			if !present[channel][h] {
				how := HourOfWeek(time.Unix(h*3600, 0))
				byHour[how] = append(byHour[how], 0)
			}
		}

		for h := range HoursInWeek {
			counts := byHour[h]
			stat := HourStat{Hour: h, Observations: len(counts)}
			if len(counts) > 0 {
				sort.Slice(counts, func(i, j int) bool { return counts[i] < counts[j] })
				stat.Low = counts[0]
				stat.High = counts[len(counts)-1]
				stat.Median = medianOf(counts)
				stat.Floor = percentileOf(counts, 0.2)
				for _, c := range counts {
					if c == 0 {
						stat.Silent++
					}
				}
			}
			r.Hours[h] = stat
		}
		out[channel] = r
	}

	return out, nil
}

// Shortfall reports how far below its normal an hour has fallen, as a fraction.
//
// Zero means at or above normal. One means completely silent in an hour that is never silent. A caller alerting at
// 0.8 is asking to hear when a feed has lost four fifths of its traffic.
//
// Returns false when nothing can honestly be said: an hour with no reliable history, or one where silence is normal.
// A rule that guessed here would fire on every quiet Sunday, get switched off, and take the useful alerts with it.
func (r *Rhythm) Shortfall(at time.Time, received int64) (fraction float64, ok bool) {
	h := r.Hours[HourOfWeek(at)]
	if h.Quiet() || !h.Reliable() {
		return 0, false
	}
	if float64(received) >= h.Median {
		return 0, true
	}
	return 1 - float64(received)/h.Median, true
}

// Describe explains an hour in a sentence, for an alert somebody reads at 2am.
func (r *Rhythm) Describe(at time.Time) string {
	idx := HourOfWeek(at)
	h := r.Hours[idx]
	when := DescribeHour(idx)

	switch {
	case h.Observations == 0:
		return fmt.Sprintf("%s has no history for %s", r.Channel, when)
	case h.Quiet():
		return fmt.Sprintf("%s is normally quiet at %s (silent %d of %d weeks)",
			r.Channel, when, h.Silent, h.Observations)
	case !h.Reliable():
		return fmt.Sprintf("%s is too erratic at %s to have a normal (%d to %d messages across %d weeks, "+
			"normally as low as %s against a median of %s)",
			r.Channel, when, h.Low, h.High, h.Observations, trimFloat(h.Floor), trimFloat(h.Median))
	default:
		return fmt.Sprintf("%s normally carries about %s messages at %s (%d to %d across %d weeks)",
			r.Channel, trimFloat(h.Median), when, h.Low, h.High, h.Observations)
	}
}

// percentileOf returns a value from a sorted slice, floored to a real observation.
//
// Not interpolated. An interpolated percentile invents a count that never occurred, and every number in an alert
// somebody reads at 2am should be one the feed actually produced.
func percentileOf(sorted []int64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(p * float64(len(sorted)-1))
	return float64(sorted[idx])
}

func medianOf(sorted []int64) float64 {
	n := len(sorted)
	if n == 0 {
		return 0
	}
	if n%2 == 1 {
		return float64(sorted[n/2])
	}
	return float64(sorted[n/2-1]+sorted[n/2]) / 2
}

func earliestLatest(hours map[int64]bool) (first, last int64) {
	first, last = math.MaxInt64, math.MinInt64
	for h := range hours {
		if h < first {
			first = h
		}
		if h > last {
			last = h
		}
	}
	if first > last {
		return 0, 0
	}
	return first, last
}

func trimFloat(f float64) string {
	if f == math.Trunc(f) {
		return fmt.Sprintf("%.0f", f)
	}
	return fmt.Sprintf("%.1f", f)
}
