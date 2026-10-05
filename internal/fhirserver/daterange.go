package fhirserver

import (
	"fmt"
	"strings"
	"time"
)

// Date search is range against range, as FHIR defines it. Every date a resource carries is an interval: 2026 is the whole year,
// 2026-10-05 the whole day, 2026-10-05T08:00:00Z one second, and a Period runs from the start of its start to the end of its end,
// open-ended where either is missing. The search value is an interval the same way, and each prefix is a relation between the two.
//
// This used to compare the strings, against Period.start only. An Encounter from 29 July to 2 August was not found by
// date=ge1950-08-01, a dateTime with an offset compared wrongly against one in UTC, and date=2026 was a prefix match that a
// stay running from December into January could never meet.

// rangeLayout is fixed width and in UTC, so the stored bounds compare correctly as strings.
const rangeLayout = "2006-01-02T15:04:05.000Z"

const (
	rangeMin = "0001-01-01T00:00:00.000Z"
	rangeMax = "9999-12-31T23:59:59.999Z"
)

// dateRange is the interval a FHIR date, dateTime or instant stands for, as its first and last millisecond in UTC. A date with no
// time and a time with no offset are read as UTC.
func dateRange(v string) (lo, hi string, err error) {
	v = strings.TrimSpace(v)
	var start time.Time
	var span func(time.Time) time.Time
	switch {
	case len(v) == 4:
		start, err = time.Parse("2006", v)
		span = func(t time.Time) time.Time { return t.AddDate(1, 0, 0) }
	case len(v) == 7:
		start, err = time.Parse("2006-01", v)
		span = func(t time.Time) time.Time { return t.AddDate(0, 1, 0) }
	case len(v) == 10:
		start, err = time.Parse("2006-01-02", v)
		span = func(t time.Time) time.Time { return t.AddDate(0, 0, 1) }
	case len(v) > 10 && v[10] == 'T':
		body, zone := v, ""
		if strings.HasSuffix(body, "Z") {
			body, zone = body[:len(body)-1], "Z"
		} else if i := strings.LastIndexAny(body, "+-"); i > 10 {
			body, zone = body[:i], body[i:]
		}
		frac := ""
		if i := strings.IndexByte(body, '.'); i > 0 {
			body, frac = body[:i], body[i+1:]
		}
		var layout string
		switch len(body) {
		case 16:
			layout = "2006-01-02T15:04"
			span = func(t time.Time) time.Time { return t.Add(time.Minute) }
		case 19:
			layout = "2006-01-02T15:04:05"
			span = func(t time.Time) time.Time { return t.Add(time.Second) }
		default:
			return "", "", fmt.Errorf("%q is not a FHIR date or time", v)
		}
		loc := time.UTC
		if zone != "" && zone != "Z" {
			z, zerr := time.Parse("-07:00", zone)
			if zerr != nil {
				return "", "", fmt.Errorf("%q has an offset that is not ±hh:mm", v)
			}
			loc = z.Location()
		}
		start, err = time.ParseInLocation(layout, body, loc)
		if err == nil && frac != "" {
			ms := (frac + "000")[:3]
			var n int
			if _, serr := fmt.Sscanf(ms, "%3d", &n); serr != nil {
				return "", "", fmt.Errorf("%q has a fraction of a second that is not digits", v)
			}
			start = start.Add(time.Duration(n) * time.Millisecond)
			span = func(t time.Time) time.Time { return t.Add(time.Millisecond) }
		}
	default:
		return "", "", fmt.Errorf("%q is not a FHIR date or time", v)
	}
	if err != nil {
		return "", "", fmt.Errorf("%q is not a FHIR date or time", v)
	}
	start = start.UTC()
	return start.Format(rangeLayout), span(start).Add(-time.Millisecond).UTC().Format(rangeLayout), nil
}

// periodRange is a Period as one interval: open at either end that is missing.
func periodRange(start, end string) (lo, hi string, ok bool) {
	lo, hi = rangeMin, rangeMax
	if start != "" {
		l, _, err := dateRange(start)
		if err != nil {
			return "", "", false
		}
		lo = l
	}
	if end != "" {
		_, h, err := dateRange(end)
		if err != nil {
			return "", "", false
		}
		hi = h
	}
	if start == "" && end == "" {
		return "", "", false
	}
	return lo, hi, true
}

// dateClause is the SQL condition for one date search value against the index row x, whose value is the low bound and value_hi
// the high one. The prefixes are R4's: ge and le include a target the search range wholly contains, as well as one that reaches
// past it.
func dateClause(v string) (string, []any, error) {
	prefix := "eq"
	if len(v) >= 2 && strings.Contains("eq ne gt lt ge le sa eb ap", v[:2]) && (v[0] < '0' || v[0] > '9') {
		prefix, v = v[:2], v[2:]
	}
	lo, hi, err := dateRange(v)
	if err != nil {
		return "", nil, err
	}
	const tlo, thi = "x.value", "COALESCE(x.value_hi, x.value)"
	within := "(" + tlo + " >= ? AND " + thi + " <= ?)"
	switch prefix {
	case "eq":
		return within, []any{lo, hi}, nil
	case "ne":
		return "NOT " + within, []any{lo, hi}, nil
	case "gt":
		return thi + " > ?", []any{hi}, nil
	case "lt":
		return tlo + " < ?", []any{lo}, nil
	case "ge":
		return "(" + thi + " > ? OR " + within + ")", []any{hi, lo, hi}, nil
	case "le":
		return "(" + tlo + " < ? OR " + within + ")", []any{lo, lo, hi}, nil
	case "sa":
		return tlo + " > ?", []any{hi}, nil
	case "eb":
		return thi + " < ?", []any{lo}, nil
	default: // ap: the two ranges overlap.
		return "(" + tlo + " <= ? AND " + thi + " >= ?)", []any{hi, lo}, nil
	}
}
