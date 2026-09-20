package engine

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// These tests run transformations and scripts through the real channel path,
// because everything up to here proved the pieces work in isolation. What matters
// to somebody migrating is that a message arriving on a socket comes out the far
// side changed, and that a failure is attributed to the channel rather than
// blamed on the sender.

const stageADT = "MSH|^~\\&|SENDAPP|SITEA|RECV|RFAC|20260818120000||ADT^A01^ADT_A01|CTRL1|P|2.5.1\r" +
	"EVN|A01|20260818115900\r" +
	"PID|1||MRN9^^^SITEA^MR||Doe^Jane^Q||19800101|f|||4 Elm Rd^^Vestavia^AL^35216\r" +
	"PV1|1|I|ICU^7^01^SITEA||||1234^Smith^Sam|||MED\r"

// stageChannel builds a channel with the given transformations and scripts, and a
// capture destination so the test can read what would have been sent.
func stageChannel(t *testing.T, steps []transform.Step, scripts *config.Scripts, filter string) (*Channel, *capture) {
	t.Helper()

	cfg := &config.Channel{
		Name: "stage-test",
		Source: config.Source{
			Type:   "mllp",
			Listen: "127.0.0.1:0",
		},
		Filter:          filter,
		Transformations: steps,
		Scripts:         scripts,
		Destinations: []config.Destination{
			{Name: "capture", Type: "file", Dir: t.TempDir()},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("configuration is invalid: %v", err)
	}

	sink := &capture{}
	ch, err := NewChannel(cfg, func(config.Destination) (Sender, error) { return sink, nil },
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("could not build the channel: %v", err)
	}
	return ch, sink
}

// capture is a destination that keeps what it was given.
type capture struct {
	mu       sync.Mutex
	messages [][]byte
}

func (c *capture) Send(ctx context.Context, raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	copied := make([]byte, len(raw))
	copy(copied, raw)
	c.messages = append(c.messages, copied)
	return nil
}

func (c *capture) Describe() string { return "capture" }
func (c *capture) Close() error     { return nil }

func (c *capture) last() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.messages) == 0 {
		return ""
	}
	return string(c.messages[len(c.messages)-1])
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.messages)
}

// TestDeclarativeTransformationReachesTheDestination is the property that matters:
// a step in the configuration changes what is actually sent.
func TestDeclarativeTransformationReachesTheDestination(t *testing.T) {
	ch, sink := stageChannel(t, []transform.Step{
		{Description: "pad the MRN", Pad: &transform.PadStep{Path: "PID-3.1", Width: 10}},
		{Description: "normalise sex", Case: &transform.CaseStep{Path: "PID-8.1", To: "upper"}},
	}, nil, "")

	ack, err := ch.handle(context.Background(), []byte(stageADT))
	if err != nil {
		t.Fatal(err)
	}

	sent := sink.last()
	if !strings.Contains(sent, "000000MRN9") {
		t.Errorf("the MRN was not padded on the wire:\n%s", sent)
	}
	if !strings.Contains(sent, "|F|") {
		t.Errorf("sex was not upper-cased on the wire:\n%s", sent)
	}
	if !strings.Contains(string(ack), "MSA|AA") {
		t.Errorf("a transformed and delivered message should be accepted, got:\n%s", string(ack))
	}
}

// TestMirthTransformerScriptReachesTheDestination runs a script written for Mirth
// against a live channel.
func TestMirthTransformerScriptReachesTheDestination(t *testing.T) {
	ch, sink := stageChannel(t, nil, &config.Scripts{
		Transformer: `
			// Written the way a Mirth channel writes it.
			var mrn = msg['PID']['PID.3'][0]['PID.3.1'].toString();
			msg['PID']['PID.3'][0]['PID.3.1'] = 'X' + mrn;

			var svc = msg['PV1']['PV1.10']['PV1.10.1'].toString();
			var lookup = {'MED': 'Medicine', 'SUR': 'Surgery'};
			if (lookup[svc]) {
				msg['PV1']['PV1.10']['PV1.10.1'] = lookup[svc];
			}

			msg['PID']['PID.7']['PID.7.1'] =
				DateUtil.convertDate('yyyyMMdd', 'yyyy-MM-dd', msg['PID']['PID.7']['PID.7.1'].toString());

			msg.appendChild(<ZPF><ZPF.1><ZPF.1.1>perfuse</ZPF.1.1></ZPF.1></ZPF>);
		`,
	}, "")

	if _, err := ch.handle(context.Background(), []byte(stageADT)); err != nil {
		t.Fatal(err)
	}

	sent := sink.last()
	for _, want := range []string{"XMRN9", "Medicine", "1980-01-01", "ZPF|perfuse"} {
		if !strings.Contains(sent, want) {
			t.Errorf("the script did not produce %q:\n%s", want, sent)
		}
	}
	// The result has to still be a valid message, or the destination is being
	// handed something it cannot use.
	if _, err := hl7.Parse([]byte(sent)); err != nil {
		t.Errorf("the transformed message does not parse: %v", err)
	}
}

// TestFilterScriptRejects checks a Mirth filter script drops a message, and that
// the sender is still told everything was fine, because it was.
func TestFilterScriptRejects(t *testing.T) {
	ch, sink := stageChannel(t, nil, &config.Scripts{
		Filter: `return msg['PV1']['PV1.2']['PV1.2.1'].toString() == 'O';`,
	}, "")

	ack, err := ch.handle(context.Background(), []byte(stageADT))
	if err != nil {
		t.Fatal(err)
	}
	if sink.count() != 0 {
		t.Errorf("a rejected message should not be delivered, got %d", sink.count())
	}
	if !strings.Contains(string(ack), "MSA|AA") {
		t.Errorf("a filtered message is still acknowledged, got:\n%s", string(ack))
	}
	if got := ch.Stats().Filtered; got != 1 {
		t.Errorf("filtered count = %d, want 1", got)
	}
}

// TestBothLayersRunInOrder pins the ordering decision: declarative steps first,
// so a script sees an already-normalised message.
func TestBothLayersRunInOrder(t *testing.T) {
	ch, sink := stageChannel(t, []transform.Step{
		{Set: &transform.SetStep{Path: "PID-4.1", Value: "declarative"}},
	}, &config.Scripts{
		Transformer: `
			// If the declarative step had not run yet, this would read empty.
			var seen = msg['PID']['PID.4']['PID.4.1'].toString();
			msg['PID']['PID.19']['PID.19.1'] = 'script-saw-' + seen;
		`,
	}, "")

	if _, err := ch.handle(context.Background(), []byte(stageADT)); err != nil {
		t.Fatal(err)
	}
	if sent := sink.last(); !strings.Contains(sent, "script-saw-declarative") {
		t.Errorf("the script should see the declarative result:\n%s", sent)
	}
}

// TestTransformationFailureIsAttributedToUs is the important failure mode. A
// broken transformation must not be reported as the sender's problem, and must not
// deliver a half-transformed message.
func TestTransformationFailureIsAttributedToUs(t *testing.T) {
	ch, sink := stageChannel(t, []transform.Step{
		// The name is not a date, and the default behaviour is to fail loudly.
		{Date: &transform.DateStep{Path: "PID-5.1", From: "yyyyMMdd", To: "yyyy-MM-dd"}},
	}, nil, "")

	ack, err := ch.handle(context.Background(), []byte(stageADT))
	if err != nil {
		t.Fatal(err)
	}
	if sink.count() != 0 {
		t.Error("nothing should be delivered when a transformation fails")
	}
	// AE, not AR: the message was fine, our configuration was not, and the
	// sender should retry rather than treat it as permanently rejected.
	if !strings.Contains(string(ack), "MSA|AE") {
		t.Errorf("a transformation fault should be an application error, got:\n%s", string(ack))
	}
	if got := ch.Stats().Failed; got != 1 {
		t.Errorf("failed count = %d, want 1", got)
	}
}

// TestRunawayScriptDoesNotStallTheChannel checks the timeout is enforced through
// the real path, since this is the failure Mirth has no answer for.
func TestRunawayScriptDoesNotStallTheChannel(t *testing.T) {
	ch, sink := stageChannel(t, nil, &config.Scripts{
		Transformer: `while (true) { }`,
		Timeout:     300 * time.Millisecond,
	}, "")

	start := time.Now()
	ack, err := ch.handle(context.Background(), []byte(stageADT))
	elapsed := time.Since(start)

	if err != nil {
		t.Fatal(err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("the channel was stalled for %s", elapsed)
	}
	if sink.count() != 0 {
		t.Error("a message whose script timed out should not be delivered")
	}
	if !strings.Contains(string(ack), "MSA|AE") {
		t.Errorf("a timed-out script should be an application error, got:\n%s", string(ack))
	}

	// And the channel must still work afterwards.
	if _, err := ch.handle(context.Background(), []byte(stageADT)); err != nil {
		t.Fatalf("the channel did not survive a script timeout: %v", err)
	}
}

// TestUntransformedMessageIsForwardedByteForByte pins the optimisation that a
// channel which only filters does not re-encode. Re-encoding rewrites trailing
// separators, and a receiving system that compares bytes would see a difference
// where none was intended.
func TestUntransformedMessageIsForwardedByteForByte(t *testing.T) {
	ch, sink := stageChannel(t, nil, &config.Scripts{
		Filter: `return true;`,
	}, "")

	if _, err := ch.handle(context.Background(), []byte(stageADT)); err != nil {
		t.Fatal(err)
	}
	if sent := sink.last(); sent != stageADT {
		t.Errorf("a filter-only channel should forward the original bytes:\n in: %q\nout: %q", stageADT, sent)
	}
}

// TestScriptsAreCompiledAtLoadTime checks a syntax error stops the channel from
// starting rather than surfacing on the first message.
func TestScriptsAreCompiledAtLoadTime(t *testing.T) {
	cfg := &config.Channel{
		Name:   "broken",
		Source: config.Source{Type: "mllp", Listen: "127.0.0.1:0"},
		Scripts: &config.Scripts{
			Transformer: `var x = ;`,
		},
		Destinations: []config.Destination{
			{Name: "d", Type: "file", Dir: t.TempDir()},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("a script with a syntax error should fail validation")
	}
	if !strings.Contains(err.Error(), "scripts") {
		t.Errorf("the error should say it came from the scripts: %v", err)
	}
}

// TestUnknownPermissionIsRejected checks a typo in the allow list is refused
// rather than quietly denying a capability the author meant to grant.
func TestUnknownPermissionIsRejected(t *testing.T) {
	cfg := &config.Channel{
		Name:   "perm",
		Source: config.Source{Type: "mllp", Listen: "127.0.0.1:0"},
		Scripts: &config.Scripts{
			Transformer: `var x = 1;`,
			Allow:       []string{"fille"},
		},
		Destinations: []config.Destination{
			{Name: "d", Type: "file", Dir: t.TempDir()},
		},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("an unrecognised capability should be refused")
	}
	if !strings.Contains(err.Error(), "not a capability") {
		t.Errorf("the error should name the problem: %v", err)
	}
}

// TestScriptNotesAreReported checks the interface can tell somebody what E4X was
// translated in their script.
func TestScriptNotesAreReported(t *testing.T) {
	cfg := &config.Channel{
		Name:   "noted",
		Source: config.Source{Type: "mllp", Listen: "127.0.0.1:0"},
		Scripts: &config.Scripts{
			Transformer: `for each (var o in msg..OBX) { o['OBX.11']['OBX.11.1'] = 'C'; }`,
		},
		Destinations: []config.Destination{
			{Name: "d", Type: "file", Dir: t.TempDir()},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if !cfg.HasScripts() {
		t.Error("the channel should report that it contains a script")
	}
	notes := cfg.ScriptNotes()
	if len(notes) < 2 {
		t.Fatalf("expected notes for for-each and the descendant operator, got %+v", notes)
	}
	kinds := map[string]bool{}
	for _, n := range notes {
		kinds[n.Kind] = true
		if n.Where != "transformer" {
			t.Errorf("note should say which script it came from, got %q", n.Where)
		}
	}
	for _, want := range []string{"for-each", "descendant"} {
		if !kinds[want] {
			t.Errorf("missing a %q note in %+v", want, notes)
		}
	}
}
