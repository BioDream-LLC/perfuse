package profile

import (
	"fmt"
	"sort"
)

// Noticing when a sender changes.
//
// Interface teams find out about an upstream change when something breaks. A field that was always
// populated stops being; a code appears that was never sent before; a segment starts repeating.
// None of those is announced, and none of them is an error - the messages are still valid HL7, so
// nothing in a normal engine notices until a receiver falls over.
//
// Comparing two profiles of the same feed finds them. Structural comparison rather than volume
// monitoring: an alert on message counts catches a feed stopping, which everybody already
// monitors, and misses a feed that keeps flowing while quietly changing shape.
//
// # What is deliberately not reported
//
// Small movements in fill rate. A field populated in 94.2% of messages last week and 93.8% this
// week has not changed; it is the same field with different traffic through it. Reporting that
// produces a page of noise which trains people to ignore the report, and the one real change is
// then invisible. The thresholds below exist to keep the output short enough to be read.

// ChangeKind classifies a difference between two profiles.
type ChangeKind string

const (
	// SegmentAppeared means a segment is present now and was not before. Often the most
	// consequential kind: a new Z-segment usually means the sender has started carrying data
	// somewhere nothing downstream is reading.
	SegmentAppeared ChangeKind = "segment appeared"
	// SegmentVanished means a segment is no longer present at all.
	SegmentVanished ChangeKind = "segment vanished"
	// FieldAppeared means a field is populated now and was not before.
	FieldAppeared ChangeKind = "field appeared"
	// FieldVanished means a field is no longer populated.
	FieldVanished ChangeKind = "field vanished"
	// FillRateMoved means a field is populated substantially more or less often.
	FillRateMoved ChangeKind = "fill rate moved"
	// CodeAppeared means a value showed up that was never sent before. This is the one that
	// breaks a strict mapping, and it is invisible to any check on volume.
	CodeAppeared ChangeKind = "new code"
	// CodeVanished means a value is no longer sent. Worth knowing but rarely urgent.
	CodeVanished ChangeKind = "code no longer sent"
	// ShapeChanged means the values stopped sharing a shape, or started.
	ShapeChanged ChangeKind = "shape changed"
	// RepetitionGrew means a field started repeating, or repeats more than it did.
	RepetitionGrew ChangeKind = "repetition grew"
	// TypeAppeared means a message type is being sent that was not before.
	TypeAppeared ChangeKind = "new message type"
	// LengthGrew means a value got longer than anything seen before, which is what overflows a
	// fixed-width column downstream.
	LengthGrew ChangeKind = "values got longer"
)

// Change is one difference worth reporting.
type Change struct {
	Kind ChangeKind `json:"kind"`
	// Where is the segment or path the change is about.
	Where string `json:"where"`
	// What describes the change in a sentence somebody can act on.
	What string `json:"what"`
	// Breaking marks a change likely to make something downstream fail, as opposed to one
	// merely worth knowing. It is a judgement, and it is made conservatively: a field
	// disappearing and a new code arriving are breaking, a code no longer being sent is not.
	Breaking bool `json:"breaking"`
}

// Comparison is the result of comparing two profiles.
type Comparison struct {
	// Changes are ordered breaking first, then by where.
	Changes []Change `json:"changes"`
	// Stable is true when nothing crossed a threshold.
	Stable bool `json:"stable"`
	// Note explains the comparison when it cannot be trusted much.
	Note string `json:"note,omitempty"`
}

// fillRateThreshold is how far a fill rate must move to be reported.
//
// Ten points. Chosen because a field genuinely changing behaviour usually moves much further than
// that - a required field becoming optional goes from 100% to something like 70% - while ordinary
// variation between two samples of a busy feed stays within a few points.
const fillRateThreshold = 0.10

// minSample is the number of messages below which a comparison is unreliable.
//
// Fifty. With fewer than that a single unusual message moves a fill rate by two points and a
// vanished field may simply not have occurred yet. The comparison is still produced, because a
// small sample is often all anybody has, but it says so.
const minSample = 50

// Compare reports how a feed has changed between two profiles.
//
// The first argument is the older profile. Getting them the wrong way round would report every
// appearance as a disappearance, so the parameter names say which is which.
func Compare(before, after *Report) *Comparison {
	cmp := &Comparison{}

	if before == nil || after == nil {
		return &Comparison{Note: "one of the two profiles is missing, so there is nothing to compare"}
	}

	if before.Messages < minSample || after.Messages < minSample {
		cmp.Note = fmt.Sprintf(
			"one of these samples is small (%d and %d messages), so a field that looks to have "+
				"vanished may simply not have occurred yet, and a fill rate can move several "+
				"points on one unusual message",
			before.Messages, after.Messages)
	}

	compareTypes(cmp, before, after)
	compareSegments(cmp, before, after)

	// Breaking first, because the report is read from the top and the reader may stop. Then by
	// location, so two runs over the same data list changes in the same order - which matters
	// because these get diffed and pasted into tickets.
	sort.SliceStable(cmp.Changes, func(i, j int) bool {
		if cmp.Changes[i].Breaking != cmp.Changes[j].Breaking {
			return cmp.Changes[i].Breaking
		}
		if cmp.Changes[i].Where != cmp.Changes[j].Where {
			return cmp.Changes[i].Where < cmp.Changes[j].Where
		}
		return cmp.Changes[i].Kind < cmp.Changes[j].Kind
	})

	cmp.Stable = len(cmp.Changes) == 0
	return cmp
}

func compareTypes(cmp *Comparison, before, after *Report) {
	was := map[string]bool{}
	for _, t := range before.Types {
		was[t.Type] = true
	}
	for _, t := range after.Types {
		if !was[t.Type] {
			cmp.add(Change{
				Kind:  TypeAppeared,
				Where: t.Type,
				What: fmt.Sprintf(
					"%s is being sent now and was not before (%s of messages); anything filtering "+
						"by message type will not be expecting it", t.Type, pct(t.Rate)),
				// Not breaking on its own: an unexpected type is usually filtered out
				// harmlessly. It is reported because it is often the first visible sign of a
				// change at the sending system.
				Breaking: false,
			})
		}
	}
}

func compareSegments(cmp *Comparison, before, after *Report) {
	was := map[string]Segment{}
	for _, s := range before.Segments {
		was[s.ID] = s
	}
	now := map[string]Segment{}
	for _, s := range after.Segments {
		now[s.ID] = s
	}

	for _, s := range after.Segments {
		old, existed := was[s.ID]
		if !existed {
			label := "segment"
			if !s.Standard {
				label = "locally defined segment"
			}
			cmp.add(Change{
				Kind:  SegmentAppeared,
				Where: s.ID,
				What: fmt.Sprintf(
					"a %s %s has appeared, in %s of messages; nothing downstream is reading it "+
						"unless somebody has been told", label, s.ID, pct(s.Rate)),
				Breaking: false,
			})
			continue
		}
		compareFields(cmp, s.ID, old, s)
	}

	for _, s := range before.Segments {
		if _, still := now[s.ID]; !still {
			cmp.add(Change{
				Kind:  SegmentVanished,
				Where: s.ID,
				What: fmt.Sprintf(
					"%s is no longer being sent at all; it was in %s of messages before, so "+
						"anything reading it is now getting nothing", s.ID, pct(s.Rate)),
				// Breaking: something was reading it, and now there is nothing to read.
				Breaking: true,
			})
		}
	}
}

func compareFields(cmp *Comparison, segment string, before, after Segment) {
	was := map[string]Field{}
	for _, f := range before.Fields {
		was[f.Path] = f
	}
	now := map[string]Field{}
	for _, f := range after.Fields {
		now[f.Path] = f
	}

	if after.MaxPerMessage > before.MaxPerMessage && before.MaxPerMessage > 0 {
		cmp.add(Change{
			Kind:  RepetitionGrew,
			Where: segment,
			What: fmt.Sprintf(
				"%s now occurs up to %d times in one message, where it previously occurred up to "+
					"%d; code that reads a fixed number of them will miss the rest",
				segment, after.MaxPerMessage, before.MaxPerMessage),
			Breaking: true,
		})
	}

	for _, f := range after.Fields {
		old, existed := was[f.Path]
		if !existed {
			cmp.add(Change{
				Kind:  FieldAppeared,
				Where: f.Path,
				What: fmt.Sprintf(
					"%s%s is being populated now and was not before, in %s of messages carrying %s",
					f.Path, named(f.Name), pct(f.FillRate), segment),
				Breaking: false,
			})
			continue
		}

		if f.FillRate-old.FillRate > fillRateThreshold {
			cmp.add(Change{
				Kind:  FillRateMoved,
				Where: f.Path,
				What: fmt.Sprintf("%s%s is populated more often than before, %s against %s",
					f.Path, named(f.Name), pct(f.FillRate), pct(old.FillRate)),
				Breaking: false,
			})
		}
		if old.FillRate-f.FillRate > fillRateThreshold {
			cmp.add(Change{
				Kind:  FillRateMoved,
				Where: f.Path,
				What: fmt.Sprintf(
					"%s%s is populated less often than before, %s against %s; anything treating "+
						"it as reliably present will now find it empty",
					f.Path, named(f.Name), pct(f.FillRate), pct(old.FillRate)),
				// Breaking: a field something depends on is now sometimes absent, which is
				// the commonest cause of a downstream failure that nothing announced.
				Breaking: true,
			})
		}

		if f.MaxRepeats > old.MaxRepeats && old.MaxRepeats > 0 {
			cmp.add(Change{
				Kind:  RepetitionGrew,
				Where: f.Path,
				What: fmt.Sprintf(
					"%s%s now repeats up to %d times, where it previously repeated up to %d; a "+
						"mapping that takes one value is silently dropping the others",
					f.Path, named(f.Name), f.MaxRepeats, old.MaxRepeats),
				Breaking: true,
			})
		}

		if f.MaxLength > old.MaxLength && old.MaxLength > 0 &&
			f.MaxLength > old.MaxLength+old.MaxLength/5 {
			// A fifth longer, so ordinary variation does not trip it. This is what overflows a
			// fixed-width column or a database field downstream.
			cmp.add(Change{
				Kind:  LengthGrew,
				Where: f.Path,
				What: fmt.Sprintf(
					"%s%s now reaches %d characters, where the longest seen before was %d; a "+
						"fixed-width column or a database field sized for the old maximum will "+
						"truncate or reject", f.Path, named(f.Name), f.MaxLength, old.MaxLength),
				Breaking: true,
			})
		}

		if f.Shape != old.Shape && old.Shape != ShapeEmpty && f.Shape != ShapeEmpty {
			breaking := f.Shape == ShapeMixed || old.Shape == ShapeNumeric
			cmp.add(Change{
				Kind:  ShapeChanged,
				Where: f.Path,
				What: fmt.Sprintf(
					"%s%s used to hold %s values and now holds %s ones; anything parsing it "+
						"strictly may fail", f.Path, named(f.Name), old.Shape, f.Shape),
				Breaking: breaking,
			})
		}

		compareCodes(cmp, f, old)
	}

	for _, f := range before.Fields {
		if _, still := now[f.Path]; !still {
			cmp.add(Change{
				Kind:  FieldVanished,
				Where: f.Path,
				What: fmt.Sprintf(
					"%s%s is no longer populated at all; it was in %s of messages carrying %s, so "+
						"anything reading it now gets nothing",
					f.Path, named(f.Name), pct(f.FillRate), segment),
				Breaking: true,
			})
		}
	}
}

func compareCodes(cmp *Comparison, after, before Field) {
	if len(after.Codes) == 0 && len(before.Codes) == 0 {
		return
	}

	was := map[string]bool{}
	for _, c := range before.Codes {
		was[c.Code] = true
	}
	now := map[string]bool{}
	for _, c := range after.Codes {
		now[c.Code] = true
	}

	var appeared []string
	for _, c := range after.Codes {
		if !was[c.Code] {
			appeared = append(appeared, c.Code)
		}
	}
	if len(appeared) > 0 {
		sort.Strings(appeared)
		cmp.add(Change{
			Kind:  CodeAppeared,
			Where: after.Path,
			What: fmt.Sprintf(
				"%s%s is sending %s, which it never sent before; a strict mapping downstream will "+
					"reject %s", after.Path, named(after.Name), join(quoteAll(appeared)),
				plural(len(appeared), "it", "them")),
			// Breaking: this is the change that makes a mapping fail, and it is invisible to
			// any check on message volume.
			Breaking: true,
		})
	}

	var gone []string
	for _, c := range before.Codes {
		if !now[c.Code] {
			gone = append(gone, c.Code)
		}
	}
	if len(gone) > 0 {
		sort.Strings(gone)
		cmp.add(Change{
			Kind:  CodeVanished,
			Where: after.Path,
			What: fmt.Sprintf("%s%s is no longer sending %s",
				after.Path, named(after.Name), join(quoteAll(gone))),
			// Not breaking: nothing downstream fails because a value stopped arriving. Worth
			// knowing, because it often means an upstream configuration change.
			Breaking: false,
		})
	}
}

func (c *Comparison) add(ch Change) {
	// Bounded, because a feed that changed wholesale would otherwise produce a report too long
	// to read, which buries the one change somebody needed.
	const maxChanges = 60
	if len(c.Changes) >= maxChanges {
		return
	}
	c.Changes = append(c.Changes, ch)
}
