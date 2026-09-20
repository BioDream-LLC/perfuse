package script

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestReaderReturnsString verifies a Reader script that returns a single string
// produces one message.
func TestReaderReturnsString(t *testing.T) {
	e := newTestEngine(t)
	s, err := e.Compile("reader", `return "MSH|^~\\&|A|B|C|D|202608|";`, Reader)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(s, &Context{Log: func(string, string) {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(res.Messages))
	}
	if !strings.HasPrefix(res.Messages[0], "MSH|") {
		t.Errorf("message = %q, want MSH prefix", res.Messages[0])
	}
}

// TestReaderReturnsArray verifies a Reader script that returns an array produces
// multiple messages.
func TestReaderReturnsArray(t *testing.T) {
	e := newTestEngine(t)
	s, err := e.Compile("reader", `return ["msg1", "msg2", "msg3"];`, Reader)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(s, &Context{Log: func(string, string) {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 3 {
		t.Fatalf("got %d messages, want 3", len(res.Messages))
	}
	for i, want := range []string{"msg1", "msg2", "msg3"} {
		if res.Messages[i] != want {
			t.Errorf("messages[%d] = %q, want %q", i, res.Messages[i], want)
		}
	}
}

// TestReaderReturnsNothing verifies a Reader that returns nothing produces no
// messages, which means "the poll found nothing" and is normal.
func TestReaderReturnsNothing(t *testing.T) {
	e := newTestEngine(t)
	for _, src := range []string{
		``,
		`return;`,
		`return null;`,
		`return undefined;`,
		`return [];`,
		`return "";`,
	} {
		s, err := e.Compile("empty", src, Reader)
		if err != nil {
			t.Fatalf("compile %q: %v", src, err)
		}
		res, err := e.Run(s, &Context{Log: func(string, string) {}})
		if err != nil {
			t.Fatalf("run %q: %v", src, err)
		}
		if len(res.Messages) != 0 {
			t.Errorf("script %q produced %d messages, want 0", src, len(res.Messages))
		}
	}
}

// TestReaderSkipsEmptyStringsInArray verifies empty strings in an array are
// discarded. A script that builds an array conditionally might leave empties.
func TestReaderSkipsEmptyStringsInArray(t *testing.T) {
	e := newTestEngine(t)
	s, err := e.Compile("reader", `return ["a", "", "b", ""];`, Reader)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(s, &Context{Log: func(string, string) {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 2 {
		t.Fatalf("got %d messages, want 2 (empties dropped)", len(res.Messages))
	}
}

// TestReaderCoercesNonStringValue verifies the fallback path that coerces
// non-string, non-array return values to strings.
func TestReaderCoercesNonStringValue(t *testing.T) {
	e := newTestEngine(t)
	s, err := e.Compile("reader", `return 42;`, Reader)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(s, &Context{Log: func(string, string) {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0] != "42" {
		t.Errorf("messages = %v, want [\"42\"]", res.Messages)
	}
}

// TestReaderIsolationAcrossInvocations proves a pooled VM does not leak state
// from one Reader execution to the next. A script that sets a global without
// `var` creates a property on the global object that persists unless the pool
// isolation is correct.
//
// This is the cross-message contamination check for the Reader path: if the
// reader script on one poll can see state from the previous poll via a leaked
// global, then a script intended to produce fresh data on each poll may
// silently reuse stale data.
func TestReaderIsolationAcrossInvocations(t *testing.T) {
	// Pool size 1 forces reuse of the same VM.
	e := New(Options{Timeout: 3 * time.Second, MaxVMs: 1})

	// First execution: sets a global WITHOUT var (attaches to global object).
	setter, err := e.Compile("setter", `leakedGlobal = "patient_A_data"; return "first";`, Reader)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(setter, &Context{Log: func(string, string) {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Messages) != 1 || res.Messages[0] != "first" {
		t.Fatalf("first run: messages = %v", res.Messages)
	}

	// Second execution: tries to read the global. If isolation is correct, it should be undefined.
	checker, err := e.Compile("checker",
		`if (typeof leakedGlobal !== 'undefined') { return "LEAKED:" + leakedGlobal; } return "clean";`, Reader)
	if err != nil {
		t.Fatal(err)
	}
	res2, err := e.Run(checker, &Context{Log: func(string, string) {}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res2.Messages) != 1 {
		t.Fatalf("second run: messages = %v", res2.Messages)
	}
	if strings.HasPrefix(res2.Messages[0], "LEAKED:") {
		t.Errorf("ISOLATION FAILURE: Reader script on second poll saw state from the first: %q", res2.Messages[0])
	}
}

// TestReaderTimeoutLeavesEngineUsable verifies a Reader script that times out
// does not poison the runtime for subsequent polls.
func TestReaderTimeoutLeavesEngineUsable(t *testing.T) {
	e := New(Options{Timeout: 200 * time.Millisecond, MaxVMs: 1})

	runaway, err := e.Compile("runaway", `while(true) {}`, Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Run(runaway, &Context{Log: func(string, string) {}})
	if err == nil {
		t.Fatal("expected timeout error")
	}
	if !strings.Contains(err.Error(), "still running") {
		t.Errorf("error = %v, want timeout mention", err)
	}

	// Subsequent execution must work.
	good, err := e.Compile("good", `return "recovered";`, Reader)
	if err != nil {
		t.Fatal(err)
	}
	res, err := e.Run(good, &Context{Log: func(string, string) {}})
	if err != nil {
		t.Fatalf("engine not usable after timeout: %v", err)
	}
	if len(res.Messages) != 1 || res.Messages[0] != "recovered" {
		t.Errorf("messages = %v, want [\"recovered\"]", res.Messages)
	}
}

// TestReaderPanicIsRecovered verifies a panic inside a Reader script is
// converted to an error rather than killing the process.
func TestReaderPanicIsRecovered(t *testing.T) {
	e := newTestEngine(t)
	s, err := e.Compile("panic", `throw new Error("deliberate failure");`, Reader)
	if err != nil {
		t.Fatal(err)
	}
	_, err = e.Run(s, &Context{Log: func(string, string) {}})
	if err == nil {
		t.Fatal("a throwing script should produce an error")
	}
	if !strings.Contains(err.Error(), "deliberate failure") {
		t.Errorf("error = %v, want mention of the deliberate failure", err)
	}
}

// TestReaderConcurrentIsolation pushes distinct invocations through a shared
// engine concurrently and verifies each gets its own result with no cross-
// contamination. This catches data races in shared mutable state.
func TestReaderConcurrentIsolation(t *testing.T) {
	e := New(Options{Timeout: 3 * time.Second, MaxVMs: 4})

	// Each invocation returns a unique identifier.
	s, err := e.Compile("reader",
		`return "id_" + channelMap.get("myid");`, Reader)
	if err != nil {
		t.Fatal(err)
	}

	const N = 20
	results := make([]string, N)
	var wg sync.WaitGroup

	for i := 0; i < N; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			cm := NewSharedMap()
			cm.Put("myid", idx)
			res, err := e.Run(s, &Context{
				ChannelName:  "test",
				ChannelMap:   cm,
				ConnectorMap: NewSharedMap(),
				ResponseMap:  NewSharedMap(),
				SourceMap:    NewSharedMap(),
				Log:          func(string, string) {},
			})
			if err != nil {
				t.Errorf("goroutine %d: %v", idx, err)
				return
			}
			if len(res.Messages) != 1 {
				t.Errorf("goroutine %d: got %d messages", idx, len(res.Messages))
				return
			}
			results[idx] = res.Messages[0]
		}(i)
	}
	wg.Wait()

	// Every result must correspond to its own index.
	for i, got := range results {
		expected := fmt.Sprintf("id_%d", i)
		if got != expected {
			t.Errorf("result[%d] = %q, want %q — cross-message contamination", i, got, expected)
		}
	}
}
