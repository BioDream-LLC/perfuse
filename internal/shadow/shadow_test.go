package shadow

import (
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

const baseMessage = "MSH|^~\\&|SEND|SITEA|RECV|RFAC|20260819120000-0500||ADT^A01^ADT_A01|C1|P|2.5.1\r" +
	"PID|1||MRN1^^^SITEA^MR||Testpatient^Ada^Q||19910228|F\r" +
	"PV1|1|I|WARD^101^A\r"

func runnerFor(t *testing.T, cfg config.Shadow) *Runner {
	t.Helper()
	cfg.Channel = "candidate.yaml"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return New("live", &cfg, config.DataHL7, nil, nil)
}

func TestFieldPathsReachesComponents(t *testing.T) {
	// Component level, because that is the granularity a mapping operates on. A change
	// to PID-5.1 reported as a change to PID-5 hides which part moved.
	got := fieldPaths([]byte(baseMessage))

	want := []string{"MSH-9.1", "MSH-9.2", "PID-3.1", "PID-5.1", "PID-5.2", "PV1-3.1"}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s was not listed; got %v", w, got)
		}
	}
}

func TestFieldPathsSkipsEmptyFields(t *testing.T) {
	// Listing empty fields would compare thousands of absent paths per message and
	// report nothing useful, at real cost.
	got := fieldPaths([]byte(baseMessage))
	for _, p := range got {
		if strings.HasPrefix(p, "PID-4") {
			t.Errorf("%s is empty in the message and should not be listed", p)
		}
	}
}

func TestFieldPathsReturnsNothingForRubbish(t *testing.T) {
	// Not a panic, and not a single bogus path. An unparseable message is handled
	// higher up as a candidate or live error, and this must not invent a comparison.
	if got := fieldPaths([]byte("this is not HL7")); len(got) != 0 {
		t.Errorf("fieldPaths on rubbish = %v", got)
	}
}

func TestDiffFindsOnlyWhatChanged(t *testing.T) {
	r := runnerFor(t, config.Shadow{})

	changed := strings.Replace(baseMessage, "Testpatient^Ada", "TESTPATIENT^Ada", 1)
	got := r.diff([]byte(baseMessage), []byte(changed))

	if len(got) != 1 {
		t.Fatalf("diff found %d differences, want 1: %+v", len(got), got)
	}
	if got[0].Path != "PID-5.1" {
		t.Errorf("path = %q", got[0].Path)
	}
	if got[0].Live != "Testpatient" || got[0].Candidate != "TESTPATIENT" {
		t.Errorf("live=%q candidate=%q", got[0].Live, got[0].Candidate)
	}
}

func TestDiffFindsAnAddedField(t *testing.T) {
	// A candidate that populates something the live version leaves empty is a
	// difference, and reporting only changed-in-place values would miss it entirely.
	r := runnerFor(t, config.Shadow{})

	added := strings.Replace(baseMessage,
		"PID|1||MRN1^^^SITEA^MR||Testpatient^Ada^Q||19910228|F",
		"PID|1||MRN1^^^SITEA^MR||Testpatient^Ada^Q||19910228|F|||123 Main St", 1)

	got := r.diff([]byte(baseMessage), []byte(added))
	if len(got) == 0 {
		t.Fatal("an added field was not reported")
	}
	found := false
	for _, d := range got {
		if strings.HasPrefix(d.Path, "PID-11") {
			found = true
			if d.Live != "" {
				t.Errorf("live should be empty, got %q", d.Live)
			}
		}
	}
	if !found {
		t.Errorf("PID-11 was not reported: %+v", got)
	}
}

func TestDiffFindsARemovedField(t *testing.T) {
	r := runnerFor(t, config.Shadow{})

	removed := strings.Replace(baseMessage, "|19910228|", "||", 1)
	got := r.diff([]byte(baseMessage), []byte(removed))

	if len(got) == 0 {
		t.Fatal("a removed field was not reported")
	}
}

func TestDiffReportsNothingForIdenticalMessages(t *testing.T) {
	r := runnerFor(t, config.Shadow{})
	if got := r.diff([]byte(baseMessage), []byte(baseMessage)); len(got) != 0 {
		t.Errorf("identical messages produced %d differences: %+v", len(got), got)
	}
}

func TestDiffHonoursIgnore(t *testing.T) {
	// The practical necessity: a channel stamping a timestamp differs on every message
	// and would report 100% while telling you nothing.
	r := runnerFor(t, config.Shadow{Ignore: []string{"MSH-7"}})

	changed := strings.Replace(baseMessage, "20260819120000-0500", "20991231235959", 1)
	if got := r.diff([]byte(baseMessage), []byte(changed)); len(got) != 0 {
		t.Errorf("an ignored path was reported: %+v", got)
	}
}

func TestIgnoreIsCaseInsensitive(t *testing.T) {
	// Somebody will write msh-7 in lower case, and a comparison that then reported it
	// anyway would look like the setting does not work.
	r := runnerFor(t, config.Shadow{Ignore: []string{"msh-7"}})

	changed := strings.Replace(baseMessage, "20260819120000-0500", "20991231235959", 1)
	if got := r.diff([]byte(baseMessage), []byte(changed)); len(got) != 0 {
		t.Errorf("a lower-case ignore entry was not honoured: %+v", got)
	}
}

func TestDiffHonoursCompare(t *testing.T) {
	r := runnerFor(t, config.Shadow{Compare: []string{"PV1-3.1"}})

	// Two changes; only one is in the compare list.
	changed := strings.Replace(baseMessage, "Testpatient", "TESTPATIENT", 1)
	changed = strings.Replace(changed, "WARD^101^A", "ICU^101^A", 1)

	got := r.diff([]byte(baseMessage), []byte(changed))
	if len(got) != 1 || got[0].Path != "PV1-3.1" {
		t.Errorf("compare should have limited this to PV1-3.1: %+v", got)
	}
}

func TestDiffBoundsAnEnormousDifference(t *testing.T) {
	// A candidate that rewrites everything must not produce a report so long that the
	// one difference somebody needed is buried in it.
	r := runnerFor(t, config.Shadow{})

	// Many distinct fields on one segment, which is what a mapping applied to the
	// wrong path actually produces. Repeated segments would not do it: a repeated
	// field resolves to one path with the repetitions joined.
	var live, candidate strings.Builder
	live.WriteString("MSH|^~\\&|A|B|C|D|20260819||ADT^A01^ADT_A01|C1|P|2.5.1\rZZZ")
	candidate.WriteString("MSH|^~\\&|A|B|C|D|20260819||ADT^A01^ADT_A01|C1|P|2.5.1\rZZZ")
	for i := 0; i < 80; i++ {
		live.WriteString("|live")
		candidate.WriteString("|cand")
	}
	live.WriteString("\r")
	candidate.WriteString("\r")

	got := r.diff([]byte(live.String()), []byte(candidate.String()))
	if len(got) > 51 {
		t.Fatalf("diff returned %d rows; it should be bounded", len(got))
	}
	// And it has to say it was truncated rather than silently stopping, or the report
	// looks complete when it is not.
	last := got[len(got)-1]
	if last.Path != "…" {
		t.Errorf("a truncated diff should say so; last row = %+v", last)
	}
}

func TestDiffClipsAnEnormousValue(t *testing.T) {
	// A repeated field resolves to every repetition joined, so one path can carry
	// kilobytes. Unclipped that lands in a JSON response, a browser and a log line.
	r := runnerFor(t, config.Shadow{})

	var live, candidate strings.Builder
	live.WriteString("MSH|^~\\&|A|B|C|D|20260819||ADT^A01^ADT_A01|C1|P|2.5.1\r")
	candidate.WriteString("MSH|^~\\&|A|B|C|D|20260819||ADT^A01^ADT_A01|C1|P|2.5.1\r")
	live.WriteString("NTE|1||" + strings.Repeat("x", 5000) + "\r")
	candidate.WriteString("NTE|1||" + strings.Repeat("y", 5000) + "\r")

	got := r.diff([]byte(live.String()), []byte(candidate.String()))
	if len(got) == 0 {
		t.Fatal("the difference was not reported at all")
	}
	if len(got[0].Live) > 400 {
		t.Errorf("the live value is %d bytes; it should be clipped", len(got[0].Live))
	}
	// Said rather than silent, or somebody goes looking for a change at character 300
	// that is not there.
	if !strings.Contains(got[0].Live, "bytes in total") {
		t.Errorf("a clipped value should say so: %q", got[0].Live)
	}
}

func TestIdentifyPullsTheControlIDAndType(t *testing.T) {
	// Both go in the report, and without them a difference cannot be traced back to a
	// message anybody can find in the browser.
	id, typ := identify([]byte(baseMessage))
	if id != "C1" {
		t.Errorf("control id = %q", id)
	}
	if typ != "ADT^A01^ADT_A01" {
		t.Errorf("type = %q", typ)
	}
}

func TestIdentifyIsSafeOnRubbish(t *testing.T) {
	id, typ := identify([]byte("nonsense"))
	if id != "" || typ != "" {
		t.Errorf("identify on rubbish = %q, %q", id, typ)
	}
}

func TestVerdictLeadsWithTheWorstProblem(t *testing.T) {
	// The ordering is the point: a candidate that fails outright is not a matter of
	// reviewing differences, and a filter disagreement outranks a value change.
	cases := []struct {
		name  string
		stats Stats
		want  string
	}{
		{"nothing yet", Stats{}, "nothing has been compared"},
		{
			"failure beats everything",
			Stats{Compared: 100, CandidateFailed: 1, FilterDisagreed: 5, Differed: 50},
			"not ready",
		},
		{
			"filter beats transform",
			Stats{Compared: 100, FilterDisagreed: 5, Differed: 50},
			"whether to keep",
		},
		{
			"transform",
			Stats{Compared: 100, Differed: 50},
			"came out differently",
		},
		{
			// The honest wording matters here more than anywhere: identical output is
			// evidence, not proof.
			"all matched",
			Stats{Compared: 100, Same: 100},
			"not proof",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := runnerFor(t, config.Shadow{})
			r.stats = tc.stats
			got := r.Verdict()
			if !strings.Contains(got, tc.want) {
				t.Errorf("Verdict() = %q, want it to contain %q", got, tc.want)
			}
		})
	}
}

func TestVerdictNeverClaimsSomethingIsSafe(t *testing.T) {
	// Nothing here can tell an intended change from a mistake, so nothing here should
	// sound like an approval.
	for _, s := range []Stats{
		{Compared: 1000, Same: 1000},
		{Compared: 1000, Differed: 1},
		{Compared: 1000, CandidateFailed: 1},
	} {
		r := runnerFor(t, config.Shadow{})
		r.stats = s
		v := strings.ToLower(r.Verdict())
		for _, forbidden := range []string{"safe to promote", "safe to deploy", "approved"} {
			if strings.Contains(v, forbidden) {
				t.Errorf("Verdict() says %q: %q", forbidden, v)
			}
		}
	}
}

func TestRecordKeepsTheMostRecentDifferences(t *testing.T) {
	// The newest are kept rather than the first, because a shadow is watched while a
	// change is being iterated on.
	r := runnerFor(t, config.Shadow{MaxDifferences: 3})

	for i := 0; i < 10; i++ {
		r.record(Difference{ControlID: string(rune('a' + i))})
	}

	got := r.Differences()
	if len(got) != 3 {
		t.Fatalf("kept %d, want 3", len(got))
	}
	if got[2].ControlID != "j" {
		t.Errorf("the newest difference is %q, want j", got[2].ControlID)
	}
	if got[0].ControlID != "h" {
		t.Errorf("the oldest kept is %q, want h", got[0].ControlID)
	}
}

func TestDifferencesReturnsACopy(t *testing.T) {
	// A caller mutating the slice must not corrupt what the next request sees.
	r := runnerFor(t, config.Shadow{})
	r.record(Difference{ControlID: "original"})

	got := r.Differences()
	got[0].ControlID = "tampered"

	if r.Differences()[0].ControlID != "original" {
		t.Error("Differences() handed out the internal slice")
	}
}
