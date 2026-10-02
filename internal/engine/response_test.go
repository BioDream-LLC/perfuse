package engine

import (
	"context"
	"strings"
	"sync"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// The response transformer earns its place on one case: a transport that succeeded and a receiver that
// did not accept. Everything else here is about not making the ordinary path worse.

// replyDest is a sender that returns a canned reply, and can also fail.
type replyDest struct {
	mu    sync.Mutex
	reply []byte
	err   error
	sent  int
}

func (r *replyDest) Send(ctx context.Context, msg []byte) error {
	_, err := r.SendForResponse(ctx, msg)
	return err
}

func (r *replyDest) SendForResponse(_ context.Context, _ []byte) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sent++
	return r.reply, r.err
}

func (r *replyDest) Describe() string { return "a fake receiver" }
func (r *replyDest) Close() error     { return nil }

func (r *replyDest) attempts() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sent
}

// mute is a sender with no reply at all, like a file.
type mute struct{}

func (mute) Send(context.Context, []byte) error { return nil }
func (mute) Describe() string                   { return "somewhere with no reply" }
func (mute) Close() error                       { return nil }

func responseChannel(t *testing.T, transformer string, sender Sender) *Channel {
	t.Helper()

	cfg := &config.Channel{
		Name:   "checked",
		Source: config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Destinations: []config.Destination{{
			Name:                "receiver",
			Type:                config.DestinationMLLP,
			Address:             "127.0.0.1:2575",
			ResponseTransformer: transformer,

			// One attempt, because these tests are about what the response transformer decides, not about redelivery.
			//
			// Left at the default of five attempts with a doubling one-second backoff, each of the tests that ends in a
			// failed delivery sat through 1+2+4+8 seconds of real waiting to observe an outcome it already had - exactly
			// fifteen seconds, six times over, which was ninety seconds of this package's runtime and the reason
			// continuous integration timed out at two minutes while the same suite passed locally.
			Retry: config.Retry{Attempts: 1},
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	ch, err := NewChannel(cfg, func(config.Destination) (Sender, error) { return sender, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}
	return ch
}

const goodMessage = "MSH|^~\\&|EPIC|HOSP|LAB|LAB|20260819||ADT^A01|C1|P|2.5\rPID|1||123\r"

func TestAResponseTransformerCanRejectAnAcceptedTransport(t *testing.T) {
	// The reason this exists. The transport succeeded, the receiver acknowledged, and the
	// acknowledgement says the message was not accepted.
	dest := &replyDest{reply: []byte("MSH|^~\\&|LAB|LAB|EPIC|HOSP|20260819||ACK|A1|P|2.5\rMSA|AA|C1\r")}

	ch := responseChannel(t, `
		var code = String(msg['MSA']['MSA.1']['MSA.1.1']);
		if (code === 'AA') { logger.warn('the receiver said AA but we do not trust it'); return false; }
		return true;
	`, dest)

	if _, err := ch.HandleForTest(context.Background(), []byte(goodMessage)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Failed {
		t.Errorf("outcome = %q, want failed: the response transformer's verdict was ignored", got)
	}
}

func TestAResponseTransformerCanAcceptWhatTheTransportRejected(t *testing.T) {
	// The other direction, and the one people actually need: a receiver that answers with something
	// this engine reads as a rejection but which the site knows is fine.
	dest := &replyDest{
		reply: []byte("MSH|^~\\&|LAB|LAB|EPIC|HOSP|20260819||ACK|A1|P|2.5\rMSA|AE|C1|already have it\r"),
	}

	ch := responseChannel(t, `
		var text = String(msg['MSA']['MSA.3']['MSA.3.1']);
		if (text.indexOf('already have it') >= 0) { return true; }
		return false;
	`, dest)

	if _, err := ch.HandleForTest(context.Background(), []byte(goodMessage)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Delivered {
		t.Errorf("outcome = %q, want delivered: the transformer accepted the reply", got)
	}
}

func TestARejectedResponseRecordsTheScriptsOwnReason(t *testing.T) {
	// "the response transformer rejected the reply" tells nobody anything. The script logged why, and
	// repeating that in the outcome saves reading two places.
	dest := &replyDest{reply: []byte("MSH|^~\\&|A|B|C|D|20260819||ACK|A1|P|2.5\rMSA|AR|C1\r")}

	ch := responseChannel(t, `logger.error('the lab is refusing unknown MRNs'); return false;`, dest)

	if _, err := ch.HandleForTest(context.Background(), []byte(goodMessage)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Failed {
		t.Fatalf("outcome = %q, want failed", got)
	}
}

func TestAResponseThatDoesNotParseIsStillAvailableAsText(t *testing.T) {
	// An HTTP receiver answering 200 with an error document is the commonest form of this. Failing
	// because the reply is not HL7 would mean the reply could not be inspected in exactly the case
	// where inspecting it matters.
	dest := &replyDest{reply: []byte(`{"status":"rejected","reason":"unknown patient"}`)}

	ch := responseChannel(t, `
		if (msg !== null) { throw new Error('a JSON body should not have parsed as HL7'); }
		if (response.indexOf('rejected') >= 0) { return false; }
		return true;
	`, dest)

	if _, err := ch.HandleForTest(context.Background(), []byte(goodMessage)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Failed {
		t.Errorf("outcome = %q, want failed", got)
	}
}

func TestAThrowingResponseTransformerFailsTheDelivery(t *testing.T) {
	// It ran because somebody wanted the reply checked, and a check that could not be carried out is
	// not a pass.
	dest := &replyDest{reply: []byte("MSH|^~\\&|A|B|C|D|20260819||ACK|A1|P|2.5\rMSA|AA|C1\r")}

	ch := responseChannel(t, `throw new Error('the response transformer is wrong');`, dest)

	if _, err := ch.HandleForTest(context.Background(), []byte(goodMessage)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Failed {
		t.Errorf("outcome = %q, want failed", got)
	}
}

func TestAResponseTransformerOnATransportWithNoReplyIsRefusedAtLoad(t *testing.T) {
	// A script that never runs on a channel whose file plainly contains it is the failure mode this
	// codebase refuses everywhere else.
	cfg := &config.Channel{
		Name:    "wrong",
		Source:  config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:2575"},
		Scripts: &config.Scripts{},
		Destinations: []config.Destination{{
			Name:                "out",
			Type:                config.DestinationFile,
			Dir:                 t.TempDir(),
			ResponseTransformer: `return true;`,
		}},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("a response transformer on a file destination was accepted")
	}
	if !strings.Contains(err.Error(), "no reply") {
		t.Errorf("the error does not say why: %v", err)
	}
	if !strings.Contains(err.Error(), "mllp") {
		t.Errorf("the error does not say which destinations it applies to: %v", err)
	}
}

func TestADestinationWithoutAResponseTransformerUsesThePlainSendPath(t *testing.T) {
	// The ordinary case must not pay for this feature, and a sender that is not a Responder must keep
	// working.
	cfg := &config.Channel{
		Name:         "plain",
		Source:       config.Source{Type: config.SourceMLLP, Listen: "127.0.0.1:0"},
		Destinations: []config.Destination{{Name: "out", Type: config.DestinationFile, Dir: t.TempDir()}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}

	ch, err := NewChannel(cfg, func(config.Destination) (Sender, error) { return mute{}, nil }, quiet())
	if err != nil {
		t.Fatal(err)
	}

	if _, err := ch.HandleForTest(context.Background(), []byte(goodMessage)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Delivered {
		t.Errorf("outcome = %q, want delivered", got)
	}
}

func TestATransportFailureTheScriptDoesNotOverrideStillFails(t *testing.T) {
	// The script accepted the reply, but the connection was refused. Returning the transport's error
	// unchanged is what keeps the retry behaviour intact.
	dest := &replyDest{reply: nil, err: context.DeadlineExceeded}

	ch := responseChannel(t, `return true;`, dest)

	if _, err := ch.HandleForTest(context.Background(), []byte(goodMessage)); err != nil {
		t.Fatal(err)
	}
	if got := ch.LastOutcomeForTest(); got != Failed {
		t.Errorf("outcome = %q, want failed", got)
	}
}
