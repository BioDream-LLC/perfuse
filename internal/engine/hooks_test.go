package engine

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The preprocessor earns its place on one case: a message that does not parse, made to parse. Every
// other test here is about not making things worse.

func hookChannel(t *testing.T, scripts *config.Scripts) (*Channel, *captureDest) {
	t.Helper()

	dest := &captureDest{}
	cfg := &config.Channel{
		Name:         "hooked",
		Source:       config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Scripts:      scripts,
		Destinations: []config.Destination{{Name: "out", Type: config.DestinationFile, Dir: t.TempDir()}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	ch, err := NewChannel(cfg, func(config.Destination) (Sender, error) { return dest, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	return ch, dest
}

func TestAPreprocessorRepairsAMessageThatWouldNotParse(t *testing.T) {
	// The reason this hook exists. Segment terminators arriving as line feeds is the single most
	// common shape of this problem, and without a preprocessor every one of those messages is
	// rejected - traffic Mirth accepts today.
	ch, dest := hookChannel(t, &config.Scripts{
		Preprocessor: `return message.replace(/\n/g, '\r');`,
	})

	broken := "MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C1|P|2.5\nPID|1||123||SMITH^JOHN\n"

	if _, err := ch.HandleForTest(context.Background(), []byte(broken)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Delivered {
		t.Fatalf("outcome = %q, want delivered: the preprocessor did not repair the message", got)
	}
	if len(dest.messages()) != 1 {
		t.Errorf("the destination received %d messages", len(dest.messages()))
	}
}

func TestAPreprocessorThatReturnsNothingLeavesTheMessageAlone(t *testing.T) {
	// The common case: most preprocessors edit conditionally and fall off the end. Treating a missing
	// return as "the message is now empty" would discard traffic on a technicality.
	ch, dest := hookChannel(t, &config.Scripts{
		Preprocessor: `if (false) { return 'never'; }`,
	})

	msg := "MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C1|P|2.5\rPID|1||123||SMITH^JOHN\r"
	if _, err := ch.HandleForTest(context.Background(), []byte(msg)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Delivered {
		t.Fatalf("outcome = %q, want delivered", got)
	}

	got := dest.messages()
	if len(got) != 1 {
		t.Fatalf("received %d messages", len(got))
	}
	if string(got[0]) != msg {
		t.Errorf("the message was changed by a preprocessor that returned nothing:\n  %q", got[0])
	}
}

func TestAPreprocessorReturningAnEmptyMessageIsRefused(t *testing.T) {
	// The alternative is an unparseable outcome, which blames the sender for something a script did.
	ch, _ := hookChannel(t, &config.Scripts{Preprocessor: `return '';`})

	if _, err := ch.HandleForTest(context.Background(),
		[]byte("MSH|^~\\&|A|B|C|D|20260819||ADT^A01|C1|P|2.5\r")); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Failed {
		t.Errorf("outcome = %q, want failed", got)
	}
}

func TestAPreprocessorThatFailsDoesNotFallBackToTheOriginal(t *testing.T) {
	// Falling back would hide a broken preprocessor for as long as the messages happened to parse
	// without it, and the day one did not would look like a sender problem.
	ch, dest := hookChannel(t, &config.Scripts{
		Preprocessor: `throw new Error('the preprocessor is wrong');`,
	})

	if _, err := ch.HandleForTest(context.Background(),
		[]byte("MSH|^~\\&|A|B|C|D|20260819||ADT^A01|C1|P|2.5\r")); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Failed {
		t.Errorf("outcome = %q, want failed", got)
	}
	if n := len(dest.messages()); n != 0 {
		t.Errorf("the destination received %d messages despite the preprocessor failing", n)
	}
}

func TestAPreprocessorCannotReturnAnUnboundedMessage(t *testing.T) {
	// A script building a string in a loop can exhaust memory before the timeout notices, because the
	// timeout interrupts between operations and one concatenation is one operation.
	ch, _ := hookChannel(t, &config.Scripts{
		Preprocessor: `var s = message; for (var i = 0; i < 12; i++) { s = s + s; } return s;`,
	})

	if _, err := ch.HandleForTest(context.Background(),
		[]byte("MSH|^~\\&|A|B|C|D|20260819||ADT^A01|C1|P|2.5\r")); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Failed {
		t.Errorf("outcome = %q, want failed: an unbounded preprocessor result was accepted", got)
	}
}

func TestAPreprocessorSeesTheTextAndNotATree(t *testing.T) {
	// It runs before parsing, so there may be no tree. Binding msg to null rather than leaving it
	// unset means a script that mentions it gets a sensible complaint instead of a ReferenceError.
	ch, dest := hookChannel(t, &config.Scripts{
		Preprocessor: `if (msg !== null) { throw new Error('msg should be null before parsing'); }
return message;`,
	})

	if _, err := ch.HandleForTest(context.Background(),
		[]byte("MSH|^~\\&|A|B|C|D|20260819||ADT^A01|C1|P|2.5\r")); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Delivered {
		t.Errorf("outcome = %q, want delivered", got)
	}
	if len(dest.messages()) != 1 {
		t.Error("the message did not survive a preprocessor that only read it")
	}
}

func TestAChannelWithOnlyAPreprocessorStillCompilesIt(t *testing.T) {
	// Scripts.Empty listed only the filter and the transformer, so a channel whose only script was a
	// preprocessor was treated as having none: nothing compiled and the script silently never ran,
	// while the file plainly contained it.
	scripts := &config.Scripts{Preprocessor: `return message;`}
	if scripts.Empty() {
		t.Fatal("a channel with a preprocessor reports as having no scripts")
	}

	ch, _ := hookChannel(t, scripts)
	if ch.cfg.Scripts.PreprocessorScript() == nil {
		t.Error("the preprocessor was not compiled")
	}
}

func TestAPostprocessorRunsForADeliveredMessage(t *testing.T) {
	var mu sync.Mutex
	var lines []string

	ch, _ := hookChannel(t, &config.Scripts{
		Postprocessor: `logger.info('postprocessor ran');`,
	})
	ch.log = collectLogs(&mu, &lines)

	if _, err := ch.HandleForTest(context.Background(),
		[]byte("MSH|^~\\&|A|B|C|D|20260819||ADT^A01|C1|P|2.5\rPID|1||123\r")); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(lines, "\n"), "postprocessor ran") {
		t.Errorf("the postprocessor did not run: %v", lines)
	}
}

func TestAPostprocessorRunsForAnUnparseableMessage(t *testing.T) {
	// Failures are what people write postprocessors to be told about. Hooking each return site
	// separately is how a hook ends up silently missing them.
	var mu sync.Mutex
	var lines []string

	ch, _ := hookChannel(t, &config.Scripts{
		Postprocessor: `logger.info('postprocessor ran');`,
	})
	ch.log = collectLogs(&mu, &lines)

	if _, err := ch.HandleForTest(context.Background(), []byte("this is not a message")); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(lines, "\n"), "postprocessor ran") {
		t.Errorf("the postprocessor did not run for an unparseable message: %v", lines)
	}
}

func TestAFailingPostprocessorDoesNotChangeTheOutcome(t *testing.T) {
	// By the time it runs, the message has been delivered and the sender acknowledged. Failing now
	// could undo neither, and turning a delivered message into a failure because a notification
	// script had a typo would make the outcome a lie.
	ch, dest := hookChannel(t, &config.Scripts{
		Postprocessor: `throw new Error('the postprocessor is wrong');`,
	})

	if _, err := ch.HandleForTest(context.Background(),
		[]byte("MSH|^~\\&|A|B|C|D|20260819||ADT^A01|C1|P|2.5\rPID|1||123\r")); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Delivered {
		t.Errorf("outcome = %q, want delivered despite the postprocessor failing", got)
	}
	if len(dest.messages()) != 1 {
		t.Error("the message was not delivered")
	}
}

// collectLogs returns a logger that appends every message to lines.
func collectLogs(mu *sync.Mutex, lines *[]string) *slog.Logger {
	return slog.New(slog.NewTextHandler(&lockedWriter{mu: mu, lines: lines}, nil))
}

type lockedWriter struct {
	mu    *sync.Mutex
	lines *[]string
}

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	*w.lines = append(*w.lines, string(p))
	return len(p), nil
}

func TestADeployScriptRunsBeforeTheChannelAcceptsAnything(t *testing.T) {
	var mu sync.Mutex
	var lines []string

	ch, _ := hookChannel(t, &config.Scripts{Deploy: `logger.info('deploy ran');`})
	ch.log = collectLogs(&mu, &lines)

	if err := ch.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ch.Stop(context.Background()) }()

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(lines, "\n"), "deploy ran") {
		t.Errorf("the deploy script did not run: %v", lines)
	}
}

func TestAFailingDeployScriptStopsTheChannelStarting(t *testing.T) {
	// It usually means something the channel depends on is unreachable. Accepting messages it cannot
	// handle is worse than refusing to start with the reason.
	ch, _ := hookChannel(t, &config.Scripts{
		Deploy: `throw new Error('the database is not reachable');`,
	})

	err := ch.Start()
	if err == nil {
		_ = ch.Stop(context.Background())
		t.Fatal("the channel started despite its deploy script failing")
	}
	if !strings.Contains(err.Error(), "deploy") {
		t.Errorf("the error does not say which script failed: %v", err)
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("the script's own reason was lost: %v", err)
	}
}

func TestAnUndeployScriptRunsWhenTheChannelStops(t *testing.T) {
	var mu sync.Mutex
	var lines []string

	ch, _ := hookChannel(t, &config.Scripts{Undeploy: `logger.info('undeploy ran');`})
	ch.log = collectLogs(&mu, &lines)

	if err := ch.Start(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !strings.Contains(strings.Join(lines, "\n"), "undeploy ran") {
		t.Errorf("the undeploy script did not run: %v", lines)
	}
}

func TestAFailingUndeployScriptStillLetsTheChannelStop(t *testing.T) {
	// A channel that could not be shut down because a cleanup script threw would be worse than one
	// that logged and closed.
	ch, _ := hookChannel(t, &config.Scripts{
		Undeploy: `throw new Error('the cleanup is wrong');`,
	})

	if err := ch.Start(); err != nil {
		t.Fatal(err)
	}
	if err := ch.Stop(context.Background()); err != nil {
		t.Errorf("stopping failed because the undeploy script did: %v", err)
	}
}

func TestALifecycleScriptHasNoMessage(t *testing.T) {
	// There is none, and a script reaching for one should be told rather than handed an empty message
	// that makes its lookups quietly return nothing.
	ch, _ := hookChannel(t, &config.Scripts{
		Deploy: `if (msg !== null) { throw new Error('there should be no message at deploy'); }`,
	})

	if err := ch.Start(); err != nil {
		t.Fatalf("a deploy script that checked msg was null failed: %v", err)
	}
	_ = ch.Stop(context.Background())
}
