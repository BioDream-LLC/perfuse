// Package shadow compares a candidate channel against the live one.
//
// The comparison itself is the easy part. The whole value is in two guarantees, and
// both are enforced by construction rather than by configuration:
//
//   - A shadow cannot deliver. It holds a transformation pipeline and no senders, so
//     there is no code path from here to a receiver. A setting cannot create one and
//     a mistake cannot enable one.
//   - A shadow cannot affect the live channel. It is called after the live message
//     has been delivered and acknowledged, its panics are recovered, and its
//     timeouts are its own.
//
// Anything less than that and shadow mode becomes a way to break production while
// trying to avoid breaking production.
package shadow

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// Transformer is the part of a channel a shadow needs.
//
// Deliberately this narrow. A shadow is handed the ability to transform a message and
// nothing else - no senders, no queue, no store - so that "the shadow cannot deliver"
// is a fact about the type rather than a promise in a comment.
type Transformer interface {
	// TransformOnly applies the filter and the transformation pipeline and returns
	// the result. It must not deliver anything.
	TransformOnly(ctx context.Context, raw []byte) (Result, error)
}

// Result is what a pipeline made of one message.
type Result struct {
	// Accepted is false when the filter declined the message.
	Accepted bool
	// RejectedBy names what declined it.
	RejectedBy string
	// Message is the transformed message, when it was accepted.
	Message []byte
}

// Runner compares one candidate against the live channel.
type Runner struct {
	channel   string
	cfg       *config.Shadow
	live      Transformer
	candidate Transformer

	// dataType decides which walker compares the two outputs.
	//
	// Carried here rather than sniffed from the bytes for the reason diffFor gives: sniffing works almost always and fails
	// in the case that matters, where a v2 message whose first characters look like XML would be compared with the wrong
	// walker and reported as wholly different - which reads as a candidate that rewrites every field.
	dataType config.DataType

	ignore  map[string]bool
	compare []string

	mu          sync.Mutex
	stats       Stats
	differences []Difference
	rnd         *rand.Rand
}

// Stats summarises a shadow run.
type Stats struct {
	// Compared is how many messages went through both pipelines.
	Compared int64 `json:"compared"`
	// Skipped is how many were not sampled.
	Skipped int64 `json:"skipped"`
	// Same is how many produced identical output.
	Same int64 `json:"same"`
	// Differed is how many produced different output.
	Differed int64 `json:"differed"`
	// FilterDisagreed is counted separately, because it is the most consequential
	// difference available: one version keeps a message the other drops.
	FilterDisagreed int64 `json:"filterDisagreed"`
	// CandidateFailed is how many the candidate could not process at all.
	CandidateFailed int64 `json:"candidateFailed"`
	// LiveFailed is how many the live channel could not process, which is not the
	// candidate's fault and must not be reported as though it were.
	LiveFailed int64 `json:"liveFailed"`
	// CandidateSlower is the candidate's total time minus the live channel's.
	CandidateExtra time.Duration `json:"candidateExtraNanos"`
	StartedAt      time.Time     `json:"startedAt"`
	LastCompared   time.Time     `json:"lastCompared,omitempty"`
}

// Difference is one message the two versions disagreed about.
type Difference struct {
	At        time.Time `json:"at"`
	ControlID string    `json:"controlId,omitempty"`
	Type      string    `json:"messageType,omitempty"`

	// Kind is filter, transform, candidate-error or live-error.
	Kind string `json:"kind"`

	// Fields lists the paths that differ, with both values.
	Fields []FieldDifference `json:"fields,omitempty"`

	// Note explains a difference that is not field-level.
	Note string `json:"note,omitempty"`
}

// FieldDifference is one path where the two versions disagree.
type FieldDifference struct {
	Path      string `json:"path"`
	Live      string `json:"live"`
	Candidate string `json:"candidate"`
}

// New builds a runner.
//
// dataType decides the comparison. Passing it explicitly rather than defaulting to HL7 is deliberate: the previous signature
// had no way to say, so every shadow compared with the v2 walker regardless of what the channel carried.
func New(channel string, cfg *config.Shadow, dataType config.DataType, live, candidate Transformer) *Runner {
	r := &Runner{
		channel:   channel,
		cfg:       cfg,
		dataType:  dataType,
		live:      live,
		candidate: candidate,
		ignore:    make(map[string]bool, len(cfg.Ignore)),
		compare:   cfg.Compare,
		// Seeded per runner so two channels sampling at the same rate do not choose the
		// same messages, which would leave a correlated blind spot.
		rnd:   rand.New(rand.NewSource(time.Now().UnixNano())),
		stats: Stats{StartedAt: time.Now()},
	}
	for _, p := range cfg.Ignore {
		r.ignore[strings.ToUpper(strings.TrimSpace(p))] = true
	}
	return r
}

// Observe runs one message through both pipelines and records the comparison.
//
// Called after the live message has been delivered and acknowledged. It returns
// nothing, because there is nothing the caller should do differently based on the
// result: a shadow exists to inform a person, not to change what happens to a
// message.
func (r *Runner) Observe(parent context.Context, raw []byte) {
	if !r.sampled() {
		r.mu.Lock()
		r.stats.Skipped++
		r.mu.Unlock()
		return
	}

	// Recovered here rather than anywhere else. A candidate channel is by definition
	// not trusted yet, and a panic in it must not take down the process carrying live
	// clinical traffic.
	defer func() {
		if p := recover(); p != nil {
			r.mu.Lock()
			r.stats.CandidateFailed++
			r.mu.Unlock()
			r.record(Difference{
				At:   time.Now(),
				Kind: "candidate-error",
				Note: fmt.Sprintf("the candidate panicked: %v. The live channel was "+
					"unaffected and the message was delivered normally", p),
			})
		}
	}()

	// A context of its own, detached from the message's. The live message is already
	// finished, so inheriting its deadline would cancel every shadow run.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), r.cfg.Timeout)
	defer cancel()

	liveStart := time.Now()
	liveResult, liveErr := r.live.TransformOnly(ctx, raw)
	liveTook := time.Since(liveStart)

	candStart := time.Now()
	candResult, candErr := r.candidate.TransformOnly(ctx, raw)
	candTook := time.Since(candStart)

	r.mu.Lock()
	r.stats.Compared++
	r.stats.LastCompared = time.Now()
	r.stats.CandidateExtra += candTook - liveTook
	r.mu.Unlock()

	controlID, msgType := identify(raw)

	switch {
	case liveErr != nil && candErr != nil:
		// Both failed the same way, which says nothing about the change.
		r.mu.Lock()
		r.stats.LiveFailed++
		r.mu.Unlock()
		return

	case liveErr != nil:
		// Counted apart, because a live failure is not the candidate's fault and
		// reporting it as a difference would make a working candidate look broken.
		r.mu.Lock()
		r.stats.LiveFailed++
		r.mu.Unlock()
		r.record(Difference{
			At: time.Now(), ControlID: controlID, Type: msgType,
			Kind: "live-error",
			Note: fmt.Sprintf("the live channel failed and the candidate did not: %v. "+
				"That is the candidate fixing something, not breaking it", liveErr),
		})
		return

	case candErr != nil:
		r.mu.Lock()
		r.stats.CandidateFailed++
		r.mu.Unlock()
		r.record(Difference{
			At: time.Now(), ControlID: controlID, Type: msgType,
			Kind: "candidate-error",
			Note: fmt.Sprintf("the candidate failed on a message the live channel "+
				"handled: %v", candErr),
		})
		return
	}

	if liveResult.Accepted != candResult.Accepted {
		// The most consequential disagreement there is: one version keeps a message the
		// other drops. A transformation difference changes a value; this changes whether
		// the receiving system hears about the patient at all.
		r.mu.Lock()
		r.stats.FilterDisagreed++
		r.stats.Differed++
		r.mu.Unlock()

		kept, dropped := "the candidate", "the live channel"
		if liveResult.Accepted {
			kept, dropped = "the live channel", "the candidate"
		}
		r.record(Difference{
			At: time.Now(), ControlID: controlID, Type: msgType,
			Kind: "filter",
			Note: fmt.Sprintf("%s keeps this message and %s drops it. A filter change "+
				"decides whether the receiving system hears about this patient at all, "+
				"which is a larger change than any transformation", kept, dropped),
		})
		return
	}

	if !liveResult.Accepted {
		// Both filtered it. Agreement, and nothing to compare.
		r.mu.Lock()
		r.stats.Same++
		r.mu.Unlock()
		return
	}

	fields := r.diff(liveResult.Message, candResult.Message)
	if len(fields) == 0 {
		r.mu.Lock()
		r.stats.Same++
		r.mu.Unlock()
		return
	}

	r.mu.Lock()
	r.stats.Differed++
	r.mu.Unlock()

	r.record(Difference{
		At: time.Now(), ControlID: controlID, Type: msgType,
		Kind:   "transform",
		Fields: fields,
	})
}

// sampled decides whether to compare this message.
func (r *Runner) sampled() bool {
	if r.cfg.Sample >= 1 {
		return true
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.rnd.Float64() < r.cfg.Sample
}

// diff compares two messages field by field.
//
// Field by field rather than byte by byte, and that is the difference between a
// usable report and an unusable one. Two messages that differ in one component
// produce one line here; comparing bytes would report "they differ" and leave
// somebody to find it, or produce a character-level diff of a pipe-delimited string,
// which nobody can read.
// Dispatched on the channel's data type. Before this, every shadow used the v2 walker: an XML document compared segment by
// segment finds no MSH and reports either nothing or everything, and "everything" is the dangerous answer because a report
// claiming the candidate rewrote every field is indistinguishable from one that did.
func (r *Runner) diff(live, candidate []byte) []FieldDifference {
	opts := DiffOptions{Compare: r.compare, Ignore: r.ignore}

	switch r.dataType {
	case config.DataHL7v3:
		return DiffXML(live, candidate, opts)
	case config.DataX12:
		return DiffX12(live, candidate, opts)
	}

	return Diff(live, candidate, opts)
}

// DiffOptions narrows what a comparison looks at.
type DiffOptions struct {
	// Compare limits the comparison to these paths. Empty means every populated field in
	// either message.
	Compare []string
	// Ignore skips these paths, keyed upper case. Timestamps and control IDs belong here:
	// they differ on every message and say nothing about whether a change is safe.
	Ignore map[string]bool
}

// maxDifferenceFields bounds one message's difference list.
//
// Named rather than repeated as a literal, because the XML diff needs the same bound and two numbers that are meant to be the
// same eventually are not. Fifty is enough to see a pattern and few enough to read; a candidate that rewrites everything would
// otherwise produce a report so long that the one difference somebody needed is buried in it.
const maxDifferenceFields = 50

// Diff reports where two versions of the same message disagree.
//
// Extracted from the shadow runner so that retroactive replay can use it. Both features are
// asking the same question - did this change alter the output - and answering it twice would
// eventually produce two answers, which for a tool whose whole job is to be trusted about
// safety would be worse than having one of them.
func Diff(live, candidate []byte, opts DiffOptions) []FieldDifference {
	paths := opts.Compare
	if len(paths) == 0 {
		paths = union(fieldPaths(live), fieldPaths(candidate))
	}
	return diffPaths(live, candidate, paths, opts.Ignore)
}

func diffPaths(live, candidate []byte, paths []string, ignore map[string]bool) []FieldDifference {
	var out []FieldDifference

	for _, p := range paths {
		if ignore[strings.ToUpper(p)] {
			continue
		}
		lv, lerr := transform.ValueAt(live, p)
		cv, cerr := transform.ValueAt(candidate, p)
		if lerr != nil || cerr != nil {
			continue
		}
		if lv == cv {
			continue
		}
		// Values are truncated individually as well as the list being bounded. A
		// repeated field resolves to every repetition joined, so one path can carry
		// kilobytes - which would land in a JSON response, a browser and a log line.
		out = append(out, FieldDifference{
			Path: p, Live: clip(lv), Candidate: clip(cv),
		})

		// Bounded per message. A candidate that rewrites everything would otherwise
		// produce a report so long that the one difference somebody needed is buried.
		if len(out) >= maxDifferenceFields {
			out = append(out, FieldDifference{
				Path: "…",
				Live: "more differences than can usefully be listed",
				Candidate: "the two versions disagree about most of the message, which " +
					"is usually a segment or a mapping applied to the wrong path",
			})
			return out
		}
	}
	return out
}

// fieldPaths lists every populated field path in a message.
func fieldPaths(raw []byte) []string {
	msg, err := hl7.Parse(raw)
	if err != nil {
		return nil
	}

	var out []string
	seen := map[string]bool{}

	for i := 0; i < msg.SegmentCount(); i++ {
		seg, ok := msg.SegmentAt(i)
		if !ok {
			continue
		}
		name := seg.Name()

		for f := 1; f <= seg.FieldCount(); f++ {
			val := seg.Field(f)
			if val.String() == "" {
				continue
			}

			// Compared at component level, because that is the granularity a mapping
			// operates on: a change to PID-5.1 should not be reported as a change to
			// PID-5, which would hide which part actually moved.
			n := val.ComponentCount()
			if n <= 1 {
				add(&out, seen, fmt.Sprintf("%s-%d", name, f))
				continue
			}
			for c := 1; c <= n; c++ {
				if val.Component(c).String() == "" {
					continue
				}
				add(&out, seen, fmt.Sprintf("%s-%d.%d", name, f, c))
			}
		}
	}
	sort.Strings(out)
	return out
}

// maxValueLength bounds one reported value.
//
// Long enough for any real field, including a repeated one with a few repetitions,
// and short enough that a runaway mapping cannot make a report unreadable or a
// response enormous.
const maxValueLength = 300

// clip shortens a value and says that it did.
//
// Saying so matters: a silently truncated value looks like the difference, so
// somebody would go looking for a change at character 300 that is not there.
func clip(v string) string {
	if len(v) <= maxValueLength {
		return v
	}
	return v[:maxValueLength] + fmt.Sprintf("… (%d bytes in total)", len(v))
}

func add(out *[]string, seen map[string]bool, p string) {
	if seen[p] {
		return
	}
	seen[p] = true
	*out = append(*out, p)
}

func union(a, b []string) []string {
	seen := make(map[string]bool, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, s := range append(a, b...) {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// identify pulls the control ID and type out of a message, for the report.
// Identify is exported so retroactive replay can label a message the same way a shadow
// difference does. The two features report on the same messages, and a control ID formatted
// one way in one report and another way in the other is a small thing that makes somebody
// doubt both.
func Identify(raw []byte) (controlID, msgType string) { return identify(raw) }

func identify(raw []byte) (controlID, msgType string) {
	msg, err := hl7.Parse(raw)
	if err != nil {
		return "", ""
	}
	msh, ok := msg.Segment("MSH", 1)
	if !ok {
		return "", ""
	}
	return msh.Field(10).String(), msh.Field(9).String()
}

// record keeps a difference, bounded.
func (r *Runner) record(d Difference) {
	r.mu.Lock()
	defer r.mu.Unlock()

	if len(r.differences) < r.cfg.MaxDifferences {
		r.differences = append(r.differences, d)
		return
	}
	// The oldest is dropped rather than the newest refused. A shadow is usually
	// watched while a change is being iterated on, so the most recent differences are
	// the ones somebody is looking at.
	copy(r.differences, r.differences[1:])
	r.differences[len(r.differences)-1] = d
}

// Stats returns a copy of the counters.
func (r *Runner) Stats() Stats {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.stats
}

// Differences returns a copy of the recorded differences.
func (r *Runner) Differences() []Difference {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Difference, len(r.differences))
	copy(out, r.differences)
	return out
}

// Verdict summarises whether the candidate looks safe to promote.
//
// Phrased as an observation rather than an approval. Nothing here can know whether a
// difference is the intended one; only that there is one, or that there is not.
func (r *Runner) Verdict() string {
	s := r.Stats()

	switch {
	case s.Compared == 0:
		return "nothing has been compared yet"

	case s.CandidateFailed > 0:
		return fmt.Sprintf("the candidate failed on %d of %d messages the live channel "+
			"handled. Whatever else is true, it is not ready", s.CandidateFailed, s.Compared)

	case s.FilterDisagreed > 0:
		return fmt.Sprintf("the two versions disagree about whether to keep %d of %d "+
			"messages. Check that is the intended change, because it decides whether a "+
			"receiving system hears about those patients at all",
			s.FilterDisagreed, s.Compared)

	case s.Differed > 0:
		return fmt.Sprintf("%d of %d messages came out differently. Read the differences "+
			"and confirm each one is what you meant to change; nothing here can tell an "+
			"intended change from a mistake", s.Differed, s.Compared)

	default:
		return fmt.Sprintf("%d messages produced identical output. That is evidence the "+
			"candidate changes nothing on the traffic seen so far, not proof it changes "+
			"nothing", s.Compared)
	}
}
