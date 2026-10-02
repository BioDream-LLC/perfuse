package channeltest

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/engine"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// Result is what happened to one case.
type Result struct {
	Name     string
	Passed   bool
	Skipped  string
	Duration time.Duration

	// Failures are the assertions that did not hold. All of them, not just the
	// first: fixing one at a time when four are wrong wastes four runs, and the
	// four together usually describe a single underlying mistake.
	Failures []string

	// Error is set when the case could not be run at all, which is different from
	// an assertion failing and should read differently.
	Error error

	// Outcome and Ack are what actually happened, reported whether or not they
	// were asserted, because they are the first thing anybody wants to know.
	Outcome string
	Ack     string

	// Transformed is the message as the destinations saw it, so a failure report
	// can show it rather than describing it.
	Transformed string

	// Delivered records which destinations received the message.
	Delivered map[string]string
}

// Report is the result of a whole suite.
type Report struct {
	Channel string
	Results []Result
}

// Passed reports whether every case passed.
func (r Report) Passed() bool {
	for _, res := range r.Results {
		if !res.Passed && res.Skipped == "" {
			return false
		}
	}
	return true
}

// Counts summarises the run.
func (r Report) Counts() (passed, failed, skipped int) {
	for _, res := range r.Results {
		switch {
		case res.Skipped != "":
			skipped++
		case res.Passed:
			passed++
		default:
			failed++
		}
	}
	return passed, failed, skipped
}

// captureSender stands in for a real destination.
//
// Replacing only the transport is the whole point. The filter, the declarative
// steps and the script all run exactly as they do in production, in the same
// order, so a passing test means the channel works rather than meaning a
// reimplementation of the channel works.
type captureSender struct {
	name string
	fail bool

	mu       sync.Mutex
	received [][]byte
}

func (c *captureSender) Send(ctx context.Context, raw []byte) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Copied, because the caller's buffer is reused for the next message and a
	// stored slice would show whatever arrived afterwards.
	stored := make([]byte, len(raw))
	copy(stored, raw)
	c.received = append(c.received, stored)

	if c.fail {
		return fmt.Errorf("this destination was told to fail by the test")
	}
	return nil
}

func (c *captureSender) Describe() string { return "test capture for " + c.name }
func (c *captureSender) Close() error     { return nil }

func (c *captureSender) last() ([]byte, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.received) == 0 {
		return nil, false
	}
	return c.received[len(c.received)-1], true
}

// Run executes a suite.
func Run(suite *Suite) (*Report, error) {
	cfg, err := config.LoadFile(suite.ChannelPath())
	if err != nil {
		return nil, fmt.Errorf("loading the channel: %w", err)
	}

	if !suite.Retries {
		// One attempt per destination. See Suite.Retries: retrying a capture sender
		// measures the retry loop, not the channel, and makes a failing case take
		// fifteen seconds.
		//
		// The channel is copied first so the file on disk is never treated as
		// having been changed by having been tested.
		cfg = withoutRetries(cfg)
	}

	report := &Report{Channel: cfg.Name}

	for _, c := range suite.Tests {
		report.Results = append(report.Results, runCase(suite, cfg, c))
	}
	return report, nil
}

func runCase(suite *Suite, cfg *config.Channel, c Case) Result {
	res := Result{Name: c.Name, Delivered: map[string]string{}}

	if c.Skip != "" {
		res.Skipped = c.Skip
		return res
	}

	raw, err := suite.messageFor(c)
	if err != nil {
		res.Error = err
		return res
	}
	if len(raw) == 0 {
		res.Error = fmt.Errorf("the message is empty")
		return res
	}

	// A fresh channel per case. Sharing one would let a script's globalMap or a
	// destination's state leak between tests, and a test suite whose results depend
	// on order is not a test suite.
	captures := map[string]*captureSender{}
	var logged strings.Builder
	var logMu sync.Mutex

	logger := slog.New(slog.NewTextHandler(&lockedWriter{w: &logged, mu: &logMu},
		&slog.HandlerOptions{Level: slog.LevelDebug}))

	ch, err := engine.NewChannel(cfg, func(d config.Destination) (engine.Sender, error) {
		capture := &captureSender{
			name: d.Name,
			fail: c.Expect.Destinations[d.Name].Fail,
		}
		captures[d.Name] = capture
		return capture, nil
	}, logger)
	if err != nil {
		res.Error = err
		return res
	}
	defer ch.Close()

	start := time.Now()
	ack, err := ch.HandleForTest(context.Background(), raw)
	res.Duration = time.Since(start)

	if err != nil {
		res.Error = err
		return res
	}

	res.Ack = ackCodeOf(ack)
	res.Outcome = string(ch.LastOutcomeForTest())

	for name, capture := range captures {
		if body, ok := capture.last(); ok {
			res.Delivered[name] = string(body)
			if res.Transformed == "" {
				res.Transformed = string(body)
			}
		}
	}
	// A filtered message reaches no destination, so there is nothing captured to
	// show. Fall back to the inbound bytes rather than reporting nothing, because
	// "which message was this" is the first question about a failure.
	if res.Transformed == "" {
		res.Transformed = string(raw)
	}

	logMu.Lock()
	logText := logged.String()
	logMu.Unlock()

	res.Failures = check(c.Expect, res, logText)
	res.Passed = len(res.Failures) == 0
	return res
}

// check evaluates every assertion and returns the ones that failed.
func check(expect Expect, res Result, logText string) []string {
	var failures []string

	if expect.Outcome != "" && !strings.EqualFold(expect.Outcome, res.Outcome) {
		failures = append(failures, fmt.Sprintf(
			"expected the message to be %s, but it was %s", expect.Outcome, res.Outcome))
	}

	if expect.Ack != "" && !strings.EqualFold(expect.Ack, res.Ack) {
		failures = append(failures, fmt.Sprintf(
			"expected acknowledgement %s, got %s", strings.ToUpper(expect.Ack), res.Ack))
	}

	if expect.MaxDuration > 0 && res.Duration > expect.MaxDuration {
		failures = append(failures, fmt.Sprintf(
			"took %s, longer than the %s allowed",
			res.Duration.Round(time.Microsecond), expect.MaxDuration))
	}

	failures = append(failures,
		checkBody("the message", res.Transformed, expect.Contains, expect.NotContains)...)
	failures = append(failures,
		checkFields("the message", res.Transformed, expect.Fields, expect.Absent)...)

	for _, want := range expect.Logged {
		if !strings.Contains(logText, want) {
			failures = append(failures, fmt.Sprintf(
				"expected something logged to mention %q, but nothing did", want))
		}
	}

	for name, want := range expect.Destinations {
		body, got := res.Delivered[name]

		if want.Received != nil {
			if *want.Received && !got {
				failures = append(failures, fmt.Sprintf(
					"expected destination %q to receive the message, but it did not", name))
				continue
			}
			if !*want.Received && got {
				failures = append(failures, fmt.Sprintf(
					"expected destination %q not to receive the message, but it did", name))
				continue
			}
		}

		if !got {
			// Only complain about content when the destination was expected to have
			// something. Otherwise "received: false" plus no content assertions is a
			// perfectly good test.
			if len(want.Contains) > 0 || len(want.Fields) > 0 {
				failures = append(failures, fmt.Sprintf(
					"destination %q received nothing, so its content cannot be checked", name))
			}
			continue
		}

		label := fmt.Sprintf("destination %q", name)
		failures = append(failures, checkBody(label, body, want.Contains, want.NotContains)...)
		failures = append(failures, checkFields(label, body, want.Fields, nil)...)
	}

	return failures
}

func checkBody(label, body string, contains, notContains []string) []string {
	var failures []string
	for _, want := range contains {
		if !strings.Contains(body, want) {
			failures = append(failures, fmt.Sprintf("%s does not contain %q", label, want))
		}
	}
	for _, unwanted := range notContains {
		if strings.Contains(body, unwanted) {
			// Worth being emphatic. This is the assertion that pins the removal of an
			// identifier, and a silent regression here is a privacy incident.
			failures = append(failures, fmt.Sprintf(
				"%s still contains %q, which the test says it must not", label, unwanted))
		}
	}
	return failures
}

func checkFields(label, body string, fields map[string]string, absent []string) []string {
	if len(fields) == 0 && len(absent) == 0 {
		return nil
	}

	// Parsed once here only to give a clear message when the body is not HL7 at
	// all; the path reads go through transform so an assertion path means the same
	// thing it means in a transformation.
	if _, err := hl7.Parse([]byte(body)); err != nil {
		return []string{fmt.Sprintf(
			"%s could not be parsed as HL7, so its fields cannot be checked: %v", label, err)}
	}

	var failures []string
	for path, want := range fields {
		got, err := transform.ValueAt([]byte(body), path)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s is not a valid path: %v",
				label, path, err))
			continue
		}
		if got != want {
			failures = append(failures, fmt.Sprintf("%s: %s is %q, expected %q",
				label, path, got, want))
		}
	}

	for _, path := range absent {
		got, err := transform.ValueAt([]byte(body), path)
		if err != nil {
			failures = append(failures, fmt.Sprintf("%s: %s is not a valid path: %v",
				label, path, err))
			continue
		}
		if got != "" {
			failures = append(failures, fmt.Sprintf(
				"%s: %s should be empty but is %q", label, path, got))
		}
	}
	return failures
}

func ackCodeOf(ack []byte) string {
	if len(ack) == 0 {
		return ""
	}
	parsed, err := hl7.Parse(ack)
	if err != nil {
		return ""
	}
	msa, ok := parsed.Segment("MSA", 1)
	if !ok {
		return ""
	}
	return msa.Field(1).String()
}

// lockedWriter serialises writes from the channel's goroutines into one buffer.
type lockedWriter struct {
	w  io.Writer
	mu *sync.Mutex
}

func (l *lockedWriter) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.w.Write(p)
}

// withoutRetries returns a copy of the channel with one delivery attempt per
// destination.
func withoutRetries(cfg *config.Channel) *config.Channel {
	copied := *cfg
	copied.Destinations = make([]config.Destination, len(cfg.Destinations))
	copy(copied.Destinations, cfg.Destinations)

	for i := range copied.Destinations {
		copied.Destinations[i].Retry = config.Retry{Attempts: 1}
		// A queue would hold the message for later rather than reporting a failure,
		// so a test asserting a failed outcome would see a queued one. Testing the
		// queue is what internal/queue is for.
		copied.Destinations[i].Queue = nil
	}
	return &copied
}
