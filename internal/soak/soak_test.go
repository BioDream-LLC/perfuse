package soak_test

import (
	"context"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// Soak test: sustained load for several minutes.
//
// This is the biggest remaining hole. Every test so far runs for seconds and sends at most a handful of messages. The engine has never
// been left running under load long enough to expose:
//   - memory growth from unclosed resources or growing maps
//   - file descriptor leaks from connection churn
//   - correctness drift (messages lost, duplicated, or reordered)
//   - database growth and contention under concurrent writes
//
// Run with: go test -tags soak -timeout 10m -run TestSoak ./internal/soak/
// Not part of `make check` — this takes minutes and is not a gate.

// TestSoak sends sustained traffic through a channel and checks no messages are lost.
func TestSoak(t *testing.T) {
	if os.Getenv("PERFUSE_SOAK") == "" {
		t.Skip("set PERFUSE_SOAK=1 to run the soak test (takes several minutes)")
	}

	// Capture goroutine baseline before any work begins.
	runtime.GC()
	baselineGoroutines := runtime.NumGoroutine()

	// Configuration.
	duration := 2 * time.Minute
	if v := os.Getenv("PERFUSE_SOAK_DURATION"); v != "" {
		d, err := time.ParseDuration(v)
		if err == nil && d > 0 {
			duration = d
		}
	}
	workers := 8
	messagesPerWorker := 500

	addr := os.Getenv("PERFUSE_SOAK_ADDR")
	if addr == "" {
		t.Fatal("set PERFUSE_SOAK_ADDR to the MLLP listen address (e.g. 127.0.0.1:6661)")
	}

	t.Logf("soak test: %d workers × %d messages over %v to %s", workers, messagesPerWorker, duration, addr)

	ctx, cancel := context.WithTimeout(context.Background(), duration+30*time.Second)
	defer cancel()

	var (
		sent      atomic.Int64
		acked     atomic.Int64
		failed    atomic.Int64
		wrongAck  atomic.Int64
		mu        sync.Mutex
		latencies []time.Duration
	)

	var wg sync.WaitGroup
	start := time.Now()

	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for i := 0; i < messagesPerWorker; i++ {
				if ctx.Err() != nil {
					return
				}

				controlID := fmt.Sprintf("SOAK-%d-%d", workerID, i)
				msg := fmt.Sprintf(
					"MSH|^~\\&|SOAK|W%d|PERFUSE|TEST|%s||ADT^A01|%s|P|2.5.1\rPID|1||MRN%d%d||Doe^Soak||19800101|M\r",
					workerID, time.Now().Format("20060102150405"), controlID, workerID, i,
				)

				t0 := time.Now()
				ack, err := sendMLLP(addr, msg, 10*time.Second)
				elapsed := time.Since(t0)

				sent.Add(1)
				if err != nil {
					failed.Add(1)
					if i == 0 {
						t.Logf("worker %d first send failed: %v", workerID, err)
					}

					continue
				}
				acked.Add(1)

				mu.Lock()
				latencies = append(latencies, elapsed)
				mu.Unlock()

				// Verify the ACK echoes our control ID.
				if !strings.Contains(ack, controlID) {
					wrongAck.Add(1)
				}

				// Pace: don't overwhelm faster than the duration allows.
				pace := duration / time.Duration(messagesPerWorker)
				time.Sleep(pace / time.Duration(workers))
			}
		}(w)
	}

	wg.Wait()
	elapsed := time.Since(start)

	t.Logf("completed in %v", elapsed)
	t.Logf("sent: %d, acked: %d, failed: %d, wrong ACK: %d", sent.Load(), acked.Load(), failed.Load(), wrongAck.Load())

	if len(latencies) > 0 {
		var total time.Duration
		var max time.Duration
		for _, l := range latencies {
			total += l
			if l > max {
				max = l
			}
		}
		avg := total / time.Duration(len(latencies))
		t.Logf("latency: avg=%v, max=%v, samples=%d", avg, max, len(latencies))
	}

	// Assertions.
	if failed.Load() > 0 {
		failRate := float64(failed.Load()) / float64(sent.Load())
		if failRate > 0.01 {
			t.Errorf("%.1f%% of messages failed to send — %d of %d", failRate*100, failed.Load(), sent.Load())
		} else {
			t.Logf("%.3f%% failure rate (acceptable)", failRate*100)
		}
	}

	if wrongAck.Load() > 0 {
		t.Errorf("%d ACKs did not echo the control ID — messages may be crossing or reordering", wrongAck.Load())
	}

	// Goroutine leak detection: after all workers finish, the count must return
	// to near-baseline. Allow a small margin for runtime housekeeping goroutines.
	// Poll briefly because goroutines may not exit instantly after connections close.
	const maxGoroutineMargin = 5
	var finalGoroutines int
	for attempt := 0; attempt < 10; attempt++ {
		runtime.GC()
		time.Sleep(100 * time.Millisecond)
		finalGoroutines = runtime.NumGoroutine()
		if finalGoroutines <= baselineGoroutines+maxGoroutineMargin {
			break
		}
	}
	if finalGoroutines > baselineGoroutines+maxGoroutineMargin {
		t.Errorf("goroutine leak: baseline=%d, after soak=%d (margin=%d)",
			baselineGoroutines, finalGoroutines, maxGoroutineMargin)
	} else {
		t.Logf("goroutines: baseline=%d, final=%d (no leak)", baselineGoroutines, finalGoroutines)
	}

	// At least one message must have been sent and acked, or the test proved nothing.
	if acked.Load() == 0 {
		t.Errorf("no messages were acknowledged — the soak test exercised nothing")
	}
}

// sendMLLP sends one MLLP-framed message and returns the ACK.
func sendMLLP(addr, message string, timeout time.Duration) (string, error) {
	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return "", fmt.Errorf("connect: %w", err)
	}
	defer conn.Close()

	if err := conn.SetDeadline(time.Now().Add(timeout)); err != nil {
		return "", err
	}

	// MLLP frame: VT + message + FS + CR.
	frame := append([]byte{0x0B}, []byte(message)...)
	frame = append(frame, 0x1C, 0x0D)
	if _, err := conn.Write(frame); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}

	buf := make([]byte, 4096)
	var response []byte
	for {
		n, err := conn.Read(buf)
		if n > 0 {
			response = append(response, buf[:n]...)
		}
		if strings.Contains(string(response), "\x1c") {
			break
		}
		if err != nil {
			return "", fmt.Errorf("read: %w", err)
		}
	}

	return string(response), nil
}
