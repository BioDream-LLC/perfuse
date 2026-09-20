package engine

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/mllp"
)

const adtA01 = "MSH|^~\\&|SENDAPP|SITEA|RECVAPP|RECVFAC|20260818120000||ADT^A01^ADT_A01|CTRL1|P|2.5.1\r" +
	"EVN|A01|20260818115900\r" +
	"PID|1||MRN123^^^SITEA^MR||Doe^Jane^Q||19800101|F\r"

const adtA28 = "MSH|^~\\&|SENDAPP|SITEA|RECVAPP|RECVFAC|20260818120100||ADT^A28^ADT_A05|CTRL2|P|2.5.1\r" +
	"EVN|A28|20260818120000\r" +
	"PID|1||MRN124^^^SITEA^MR||Roe^John||19750202|M\r"

func quiet() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// recordingSender captures what it was asked to deliver and can be told to fail.
type recordingSender struct {
	name string

	mu       sync.Mutex
	received [][]byte

	failures   atomic.Int64 // fail this many times before succeeding
	attempts   atomic.Int64
	alwaysFail atomic.Bool
	delay      atomic.Int64 // nanoseconds
}

func (s *recordingSender) Send(ctx context.Context, msg []byte) error {
	s.attempts.Add(1)

	if d := s.delay.Load(); d > 0 {
		select {
		case <-time.After(time.Duration(d)):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.alwaysFail.Load() {
		return errors.New("destination is down")
	}
	if s.failures.Load() > 0 {
		s.failures.Add(-1)
		return errors.New("temporary failure")
	}

	s.mu.Lock()
	s.received = append(s.received, append([]byte(nil), msg...))
	s.mu.Unlock()
	return nil
}

func (s *recordingSender) Describe() string { return "recording " + s.name }
func (s *recordingSender) Close() error     { return nil }

// all returns copies of every message received.
//
// A copy rather than the slice itself: a test that ranged over the live slice while the poller was still delivering
// would race, and -race would report it as a flake in whichever test happened to be running.
func (s *recordingSender) all() [][]byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([][]byte, len(s.received))
	for i, m := range s.received {
		out[i] = append([]byte(nil), m...)
	}
	return out
}

func (s *recordingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.received)
}

func (s *recordingSender) last() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.received) == 0 {
		return ""
	}
	return string(s.received[len(s.received)-1])
}

// harness builds a channel from YAML with recording senders, and starts it.
type harness struct {
	t       *testing.T
	channel *Channel
	senders map[string]*recordingSender
	addr    string
}

func newHarness(t *testing.T, yaml string) *harness {
	t.Helper()

	cfg, err := config.Load(strings.NewReader(yaml), "test.yaml")
	if err != nil {
		t.Fatalf("config.Load: %v", err)
	}

	h := &harness{t: t, senders: map[string]*recordingSender{}}
	factory := func(d config.Destination) (Sender, error) {
		s := &recordingSender{name: d.Name}
		h.senders[d.Name] = s
		return s, nil
	}

	ch, err := NewChannel(cfg, factory, quiet())
	if err != nil {
		t.Fatalf("NewChannel: %v", err)
	}
	h.channel = ch

	if err := ch.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = ch.Stop(ctx)
	})

	h.addr = ch.server.Addrs()
	if h.addr == "" {
		t.Fatal("channel is not listening")
	}
	return h
}

// send delivers one message to the running channel and returns the MSA-1 code
// and MSA-3 text from the acknowledgement.
func (h *harness) send(msg string) (code, text string) {
	h.t.Helper()

	c := &mllp.Client{Addr: h.addr, Timeout: 10 * time.Second}
	defer c.Close()

	reply, err := c.Send(context.Background(), []byte(msg))
	if err != nil {
		h.t.Fatalf("send: %v", err)
	}
	ack, err := hl7.Parse(reply)
	if err != nil {
		h.t.Fatalf("acknowledgement did not parse: %v", err)
	}
	return ack.MustGet("MSA-1"), ack.MustGet("MSA-3")
}

const twoDestinations = `
name: adt-inbound
source:
  type: mllp
  listen: "127.0.0.1:0"
  ack:
    when: on_delivery
    application: PERFUSE
destinations:
  - name: registry
    type: mllp
    address: 127.0.0.1:1
  - name: archive
    type: mllp
    address: 127.0.0.1:2
`

func TestDeliversToEveryDestination(t *testing.T) {
	h := newHarness(t, twoDestinations)

	code, _ := h.send(adtA01)
	if code != "AA" {
		t.Errorf("MSA-1 = %q, want AA", code)
	}

	for name, s := range h.senders {
		if s.count() != 1 {
			t.Errorf("destination %q received %d messages, want 1", name, s.count())
		}
		if s.last() != adtA01 {
			t.Errorf("destination %q received a modified message", name)
		}
	}

	st := h.channel.Stats()
	if st.Received != 1 || st.Delivered != 1 {
		t.Errorf("stats = %+v, want 1 received and 1 delivered", st)
	}
}

func TestChannelFilterRejectsWithoutFailing(t *testing.T) {
	// A filtered message is not an error. The sender did nothing wrong, so it
	// gets an AA, and nothing is forwarded.
	h := newHarness(t, `
name: adt-inbound
source:
  type: mllp
  listen: "127.0.0.1:0"
filter: MSH-9.2 != "A28"
destinations:
  - name: registry
    type: mllp
    address: 127.0.0.1:1
`)

	if code, _ := h.send(adtA28); code != "AA" {
		t.Errorf("MSA-1 = %q, want AA for a filtered message", code)
	}
	if got := h.senders["registry"].count(); got != 0 {
		t.Errorf("destination received %d messages, want 0", got)
	}

	if code, _ := h.send(adtA01); code != "AA" {
		t.Errorf("MSA-1 = %q, want AA", code)
	}
	if got := h.senders["registry"].count(); got != 1 {
		t.Errorf("destination received %d messages, want 1", got)
	}

	st := h.channel.Stats()
	if st.Filtered != 1 || st.Delivered != 1 {
		t.Errorf("stats = %+v, want 1 filtered and 1 delivered", st)
	}
}

func TestDestinationFilter(t *testing.T) {
	h := newHarness(t, `
name: adt-inbound
source:
  type: mllp
  listen: "127.0.0.1:0"
destinations:
  - name: all
    type: mllp
    address: 127.0.0.1:1
  - name: admits-only
    type: mllp
    address: 127.0.0.1:2
    filter: MSH-9.2 == "A01"
`)

	h.send(adtA01)
	h.send(adtA28)

	if got := h.senders["all"].count(); got != 2 {
		t.Errorf("all received %d, want 2", got)
	}
	if got := h.senders["admits-only"].count(); got != 1 {
		t.Errorf("admits-only received %d, want 1", got)
	}
}

func TestEveryDestinationFilteredIsNotAFailure(t *testing.T) {
	h := newHarness(t, `
name: adt-inbound
source:
  type: mllp
  listen: "127.0.0.1:0"
destinations:
  - name: admits-only
    type: mllp
    address: 127.0.0.1:1
    filter: MSH-9.2 == "A01"
`)

	code, _ := h.send(adtA28)
	if code != "AA" {
		t.Errorf("MSA-1 = %q, want AA: nothing was sent and nothing failed", code)
	}
	if st := h.channel.Stats(); st.Filtered != 1 || st.Failed != 0 {
		t.Errorf("stats = %+v, want 1 filtered and 0 failed", st)
	}
}

func TestFailedDeliveryIsReportedAsAE(t *testing.T) {
	// Reporting AA when nothing was delivered would tell the sender the data is
	// safe when it has been lost.
	h := newHarness(t, `
name: adt-inbound
source:
  type: mllp
  listen: "127.0.0.1:0"
destinations:
  - name: registry
    type: mllp
    address: 127.0.0.1:1
    retry:
      attempts: 1
`)
	h.senders["registry"].alwaysFail.Store(true)

	code, text := h.send(adtA01)
	if code != "AE" {
		t.Errorf("MSA-1 = %q, want AE", code)
	}
	if !strings.Contains(text, "registry") {
		t.Errorf("MSA-3 = %q, want it to name the destination", text)
	}
	if st := h.channel.Stats(); st.Failed != 1 {
		t.Errorf("stats = %+v, want 1 failed", st)
	}
}

func TestPartialDeliveryIsNotSuccess(t *testing.T) {
	// One destination has the message and one does not. An AA would tell the
	// sender everything is fine and lose the difference.
	h := newHarness(t, `
name: adt-inbound
source:
  type: mllp
  listen: "127.0.0.1:0"
destinations:
  - name: good
    type: mllp
    address: 127.0.0.1:1
  - name: bad
    type: mllp
    address: 127.0.0.1:2
    retry:
      attempts: 1
`)
	h.senders["bad"].alwaysFail.Store(true)

	code, text := h.send(adtA01)
	if code != "AE" {
		t.Errorf("MSA-1 = %q, want AE for a partial delivery", code)
	}
	if !strings.Contains(text, "bad") {
		t.Errorf("MSA-3 = %q, want it to name the failing destination", text)
	}
	if got := h.senders["good"].count(); got != 1 {
		t.Errorf("the working destination received %d messages, want 1", got)
	}

	st := h.channel.Stats()
	if st.Partial != 1 {
		t.Errorf("stats = %+v, want 1 partial", st)
	}
	if st.DestDelivered["good"] != 1 || st.DestFailed["bad"] != 1 {
		t.Errorf("per-destination stats = %+v", st)
	}
}

func TestRetrySucceedsEventually(t *testing.T) {
	h := newHarness(t, `
name: adt-inbound
source:
  type: mllp
  listen: "127.0.0.1:0"
destinations:
  - name: flaky
    type: mllp
    address: 127.0.0.1:1
    retry:
      attempts: 4
      backoff: 10ms
      max_backoff: 20ms
`)
	h.senders["flaky"].failures.Store(2)

	code, _ := h.send(adtA01)
	if code != "AA" {
		t.Errorf("MSA-1 = %q, want AA after successful retries", code)
	}
	if got := h.senders["flaky"].attempts.Load(); got != 3 {
		t.Errorf("made %d attempts, want 3", got)
	}
	if got := h.senders["flaky"].count(); got != 1 {
		t.Errorf("delivered %d copies, want 1", got)
	}
}

func TestRetryGivesUpAfterAttempts(t *testing.T) {
	h := newHarness(t, `
name: adt-inbound
source:
  type: mllp
  listen: "127.0.0.1:0"
destinations:
  - name: down
    type: mllp
    address: 127.0.0.1:1
    retry:
      attempts: 3
      backoff: 5ms
      max_backoff: 5ms
`)
	h.senders["down"].alwaysFail.Store(true)

	code, text := h.send(adtA01)
	if code != "AE" {
		t.Errorf("MSA-1 = %q, want AE", code)
	}
	if got := h.senders["down"].attempts.Load(); got != 3 {
		t.Errorf("made %d attempts, want exactly 3", got)
	}
	if !strings.Contains(text, "3 attempt") {
		t.Errorf("MSA-3 = %q, want it to say how many attempts were made", text)
	}
}

func TestUnparseableMessageStillGetsAnAnswer(t *testing.T) {
	// Silence makes a sender retry for ever.
	h := newHarness(t, twoDestinations)

	code, text := h.send("this is not an HL7 message")
	if code != "AR" {
		t.Errorf("MSA-1 = %q, want AR", code)
	}
	if text == "" {
		t.Error("MSA-3 is empty; the sender has nothing to go on")
	}
	if st := h.channel.Stats(); st.Unparseable != 1 {
		t.Errorf("stats = %+v, want 1 unparseable", st)
	}
	for name, s := range h.senders {
		if s.count() != 0 {
			t.Errorf("destination %q received an unparseable message", name)
		}
	}
}

func TestAckOnReceiptAcknowledgesBeforeDelivery(t *testing.T) {
	// on_receipt promises only that the message was accepted, so a slow or
	// broken destination must not hold the acknowledgement up.
	h := newHarness(t, `
name: adt-inbound
source:
  type: mllp
  listen: "127.0.0.1:0"
  ack:
    when: on_receipt
destinations:
  - name: slow
    type: mllp
    address: 127.0.0.1:1
    timeout: 5s
    retry:
      attempts: 1
`)
	h.senders["slow"].delay.Store(int64(500 * time.Millisecond))

	start := time.Now()
	code, _ := h.send(adtA01)
	elapsed := time.Since(start)

	if code != "AA" {
		t.Errorf("MSA-1 = %q, want AA", code)
	}
	if elapsed > 300*time.Millisecond {
		t.Errorf("acknowledgement took %v; on_receipt should not wait for delivery", elapsed)
	}

	// Delivery still happens, in the background.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if h.senders["slow"].count() == 1 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Error("the message was never delivered in the background")
}

func TestAckOnDeliveryWaits(t *testing.T) {
	h := newHarness(t, `
name: adt-inbound
source:
  type: mllp
  listen: "127.0.0.1:0"
  ack:
    when: on_delivery
destinations:
  - name: slow
    type: mllp
    address: 127.0.0.1:1
    timeout: 5s
`)
	h.senders["slow"].delay.Store(int64(200 * time.Millisecond))

	start := time.Now()
	code, _ := h.send(adtA01)
	elapsed := time.Since(start)

	if code != "AA" {
		t.Errorf("MSA-1 = %q, want AA", code)
	}
	if elapsed < 200*time.Millisecond {
		t.Errorf("acknowledgement took %v; on_delivery must wait for the destination", elapsed)
	}
}

func TestAckIdentifiesTheEngine(t *testing.T) {
	h := newHarness(t, twoDestinations)

	c := &mllp.Client{Addr: h.addr, Timeout: 5 * time.Second}
	defer c.Close()

	reply, err := c.Send(context.Background(), []byte(adtA01))
	if err != nil {
		t.Fatal(err)
	}
	ack, err := hl7.Parse(reply)
	if err != nil {
		t.Fatal(err)
	}

	if got := ack.MustGet("MSH-3"); got != "PERFUSE" {
		t.Errorf("MSH-3 = %q, want PERFUSE", got)
	}
	// MSA-2 must echo the original control ID.
	if got := ack.MustGet("MSA-2"); got != "CTRL1" {
		t.Errorf("MSA-2 = %q, want CTRL1", got)
	}
}

func TestMessageIsForwardedUnmodified(t *testing.T) {
	h := newHarness(t, twoDestinations)
	h.send(adtA01)

	for name, s := range h.senders {
		if got := s.last(); got != adtA01 {
			t.Errorf("destination %q received a modified message:\n got %q\nwant %q", name, got, adtA01)
		}
	}
}

func TestConcurrentSenders(t *testing.T) {
	h := newHarness(t, twoDestinations)

	const n = 25
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := &mllp.Client{Addr: h.addr, Timeout: 10 * time.Second}
			defer c.Close()
			if _, err := c.Send(context.Background(), []byte(adtA01)); err != nil {
				t.Errorf("send: %v", err)
			}
		}()
	}
	wg.Wait()

	for name, s := range h.senders {
		if got := s.count(); got != n {
			t.Errorf("destination %q received %d messages, want %d", name, got, n)
		}
	}
	if st := h.channel.Stats(); st.Received != n || st.Delivered != n {
		t.Errorf("stats = %+v, want %d received and delivered", st, n)
	}
}

func TestFileSenderRoundTrip(t *testing.T) {
	dir := t.TempDir()
	s, err := NewFileSender(config.Destination{Dir: dir}.Resolved())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	for i := 0; i < 3; i++ {
		if err := s.Send(context.Background(), []byte(adtA01)); err != nil {
			t.Fatalf("Send %d: %v", i, err)
		}
	}
	if err := s.Send(context.Background(), []byte(adtA28)); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// One file per message type per day, not one per message.
	if len(entries) != 2 {
		t.Fatalf("got %d files, want 2: %v", len(entries), entries)
	}

	var found int
	for _, e := range entries {
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		// Stored MLLP-framed so the file replays byte for byte.
		r := mllp.NewReader(strings.NewReader(string(raw)), 0)
		for {
			msg, err := r.ReadMessage()
			if err != nil {
				break
			}
			found++
			if string(msg) != adtA01 && string(msg) != adtA28 {
				t.Errorf("stored message does not match what was sent")
			}
		}
	}
	if found != 4 {
		t.Errorf("replayed %d messages, want 4", found)
	}
}

func TestFileSenderRejectsPathFromMessageType(t *testing.T) {
	// The message type arrives over the wire, so a sender must not be able to
	// choose a path on our disk.
	dir := t.TempDir()
	s, err := NewFileSender(config.Destination{Dir: dir}.Resolved())
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	evil := "MSH|^~\\&|A|B|C|D|20260818||../../../etc/passwd^X|1|P|2.5.1\rPID|1||M1\r"
	if err := s.Send(context.Background(), []byte(evil)); err != nil {
		t.Fatalf("Send: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("got %d files, want 1", len(entries))
	}
	if strings.Contains(entries[0].Name(), "/") || strings.Contains(entries[0].Name(), "..") {
		t.Errorf("filename %q escaped the directory", entries[0].Name())
	}
}

func TestEngineStartsAndStopsChannels(t *testing.T) {
	cfgA, err := config.Load(strings.NewReader(`
name: a
source: {type: mllp, listen: "127.0.0.1:0"}
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfgB, err := config.Load(strings.NewReader(`
name: b
enabled: false
source: {type: mllp, listen: "127.0.0.1:0"}
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "b.yaml")
	if err != nil {
		t.Fatal(err)
	}

	factory := func(d config.Destination) (Sender, error) {
		return &recordingSender{name: d.Name}, nil
	}

	e, err := New([]*config.Channel{cfgA, cfgB}, factory, quiet())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// The disabled channel is validated but not run.
	if got := len(e.Channels()); got != 1 {
		t.Fatalf("got %d channels, want 1", got)
	}

	if err := e.Start(); err != nil {
		t.Fatalf("Start: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := e.Stop(ctx); err != nil {
		t.Errorf("Stop: %v", err)
	}

	if _, ok := e.Stats()["a"]; !ok {
		t.Error("stats do not include channel a")
	}
}

func TestEngineRefusesWhenNoChannelsAreEnabled(t *testing.T) {
	cfg, err := config.Load(strings.NewReader(`
name: off
enabled: false
source: {type: mllp, listen: "127.0.0.1:0"}
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`), "off.yaml")
	if err != nil {
		t.Fatal(err)
	}

	if _, err := New([]*config.Channel{cfg}, nil, quiet()); err == nil {
		t.Error("an engine with nothing to run started successfully")
	}
}

func TestPortConflictIsReportedAtStartup(t *testing.T) {
	// Two channels on the same port means one of them is deaf. Better to fail
	// loudly at startup than to look healthy and drop a feed.
	body := `
name: %s
source: {type: mllp, listen: "127.0.0.1:17777"}
destinations:
  - name: out
    type: mllp
    address: 127.0.0.1:1
`
	cfgA, err := config.Load(strings.NewReader(strings.Replace(body, "%s", "a", 1)), "a.yaml")
	if err != nil {
		t.Fatal(err)
	}
	cfgB, err := config.Load(strings.NewReader(strings.Replace(body, "%s", "b", 1)), "b.yaml")
	if err != nil {
		t.Fatal(err)
	}

	factory := func(d config.Destination) (Sender, error) {
		return &recordingSender{name: d.Name}, nil
	}
	e, err := New([]*config.Channel{cfgA, cfgB}, factory, quiet())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = e.Stop(ctx)
	}()

	if err := e.Start(); err == nil {
		t.Error("two channels bound the same port without complaint")
	}
}

// TestConcurrentMessagesReturnCorrectAcks sends distinct messages concurrently
// through one channel and asserts each acknowledgement echoes the control ID of
// its own request. This catches cross-message contamination: if shared mutable
// state is used to build the acknowledgement (e.g., a field on the channel struct
// written per-message), one patient's ACK could carry another patient's control
// ID under concurrency.
func TestConcurrentMessagesReturnCorrectAcks(t *testing.T) {
	h := newHarness(t, twoDestinations)

	const n = 30
	type result struct {
		sentControlID string
		ackControlID  string
		err           error
	}
	results := make([]result, n)
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			// Each message has a unique control ID and patient MRN.
			controlID := fmt.Sprintf("CTRL%05d", idx)
			mrn := fmt.Sprintf("MRN%05d", idx)
			msg := fmt.Sprintf(
				"MSH|^~\\&|SENDAPP|SITEA|RECVAPP|RECVFAC|20260818120000||ADT^A01^ADT_A01|%s|P|2.5.1\r"+
					"EVN|A01|20260818115900\r"+
					"PID|1||%s^^^SITEA^MR||Patient%d^Test||19800101|F\r",
				controlID, mrn, idx)

			c := &mllp.Client{Addr: h.addr, Timeout: 10 * time.Second}
			defer c.Close()

			reply, err := c.Send(context.Background(), []byte(msg))
			if err != nil {
				results[idx] = result{sentControlID: controlID, err: err}
				return
			}
			ack, err := hl7.Parse(reply)
			if err != nil {
				results[idx] = result{sentControlID: controlID, err: fmt.Errorf("ack parse: %w", err)}
				return
			}
			results[idx] = result{
				sentControlID: controlID,
				ackControlID:  ack.MustGet("MSA-2"),
			}
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Errorf("message %d: %v", i, r.err)
			continue
		}
		if r.ackControlID != r.sentControlID {
			t.Errorf("message %d: sent control ID %q but ACK echoed %q — cross-message contamination",
				i, r.sentControlID, r.ackControlID)
		}
	}

	// Also verify every message was delivered (no silent drops).
	for name, s := range h.senders {
		if got := s.count(); got != n {
			t.Errorf("destination %q received %d messages, want %d", name, got, n)
		}
	}
}
