package api

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Two starts of one channel at once used to both pass the "already running?" check, both build the channel and both
// start it: one listened, the other failed with "address in use" or, on a source without a port, ran beside the first,
// out of the runtime's books and impossible to stop. The cluster's reconciler starting a channel while a person does is the
// ordinary way to get there.
func TestTwoStartsAtOnceStartOneChannel(t *testing.T) {
	h := newHarness(t)
	// A source with no port, so a second copy is not stopped by "address in use": a script reader polling every 50 ms.
	out := t.TempDir()
	yaml := "name: relay\nsource:\n  type: javascript\n  javascript:\n    script: |\n      return \"MSH|^~\\\\&|A|B|C|D|20260101||ADT^A01|X|P|2.5.1\";\n" +
		"    poll_interval: 50ms\n    timeout: 5s\ndestinations:\n  - {name: out, type: file, dir: " +
		strings.ReplaceAll(out, `\`, `\\`) + "}\n"
	if err := os.WriteFile(filepath.Join(h.dir, "relay.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	rt := NewRuntime(h.server.Channels, nil, nil)
	gate := make(chan struct{})
	rt.Gate = func(string) error { <-gate; return nil } // hold both starts inside, past the check
	var wg sync.WaitGroup
	errs := make([]error, 8)
	for i := range errs {
		wg.Add(1)
		go func() { defer wg.Done(); errs[i] = rt.Start("relay", slog.New(slog.DiscardHandler)) }()
	}
	time.Sleep(100 * time.Millisecond)
	close(gate)
	wg.Wait()
	ok := 0
	for _, err := range errs {
		if err == nil {
			ok++
		}
	}
	if ok != 1 {
		t.Fatalf("%d of %d simultaneous starts succeeded, want exactly 1: %v", ok, len(errs), errs)
	}
	if err := rt.Stop(context.Background(), "relay"); err != nil {
		t.Fatal(err)
	}
	// Stopped means nothing still writes: no second copy left running out of the books.
	time.Sleep(300 * time.Millisecond)
	before, _ := os.ReadDir(out)
	time.Sleep(500 * time.Millisecond)
	after, _ := os.ReadDir(out)
	if len(after) != len(before) {
		t.Fatalf("after stopping, messages still arrive (%d, then %d): a second copy was started and lost", len(before), len(after))
	}
}
