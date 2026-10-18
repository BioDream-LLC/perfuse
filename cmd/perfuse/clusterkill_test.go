package main

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/biodream-llc/perfuse/internal/sqlitedb"
)

// The cluster's promise, tested the way it is broken: two servers on one PostgreSQL store, a sender streaming messages
// through whichever runs the feed, and the one running it killed with SIGKILL part way, twice. Every message the sender
// was told AA must have reached the receiver. Duplicates are counted and allowed (a sender resends what it was never
// answered for); a loss fails the test.
//
// Two shapes: the destination written before the acknowledgement (on_delivery, no queue), and a queue in the shared store
// with the receiver refusing everything while the kill happens, so the survivor has to deliver what the dead server queued.
//
// Needs PERFUSE_TEST_POSTGRES; builds the server binary, so it takes a while.
func TestKillingTheServerRunningAFeedLosesNothing(t *testing.T) {
	if testing.Short() || os.Getenv("PERFUSE_TEST_POSTGRES") == "" {
		t.Skip("set PERFUSE_TEST_POSTGRES to a disposable PostgreSQL database (and do not pass -short)")
	}
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL")
	}
	bin := filepath.Join(t.TempDir(), "perfuse")
	build := exec.Command("go", "build", "-o", bin, ".")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building: %v\n%s", err, out)
	}
	t.Run("delivered before the acknowledgement", func(t *testing.T) { killTest(t, bin, false) })
	t.Run("queued in the shared store, receiver down at the kill", func(t *testing.T) { killTest(t, bin, true) })
}

// receiver is the system downstream: an MLLP listener that records every message, and can refuse for a while.
type receiver struct {
	ln     net.Listener
	mu     sync.Mutex
	got     map[string]int
	refuse  bool
	refused int
}

func newReceiver(t *testing.T) *receiver {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &receiver{ln: ln, got: map[string]int{}}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go r.serve(c)
		}
	}()
	t.Cleanup(func() { _ = ln.Close() })
	return r
}

func (r *receiver) serve(c net.Conn) {
	defer c.Close()
	br := bufio.NewReader(c)
	for {
		if _, err := br.ReadBytes(0x0b); err != nil {
			return
		}
		msg, err := br.ReadBytes(0x1c)
		if err != nil {
			return
		}
		_, _ = br.ReadByte()
		cid := field(string(msg), 10)
		r.mu.Lock()
		refuse := r.refuse
		if !refuse {
			r.got[cid]++
		} else {
			r.refused++
		}
		r.mu.Unlock()
		code := "AA"
		if refuse {
			code = "AE"
		}
		ack := "MSH|^~\\&|RCV|X|PF|X|20261017||ACK|R" + cid + "|P|2.5\rMSA|" + code + "|" + cid + "\r"
		if _, err := c.Write([]byte("\x0b" + ack + "\x1c\r")); err != nil {
			return
		}
	}
}

// field is MSH field n (MSH-1 being the separator).
func field(msg string, n int) string {
	seg, _, _ := strings.Cut(msg, "\r")
	parts := strings.Split(seg, "|")
	if n-1 < len(parts) {
		return parts[n-1]
	}
	return ""
}

func (r *receiver) count(cid string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.got[cid]
}

func freePort(t *testing.T) int {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port
}

type clusterNode struct {
	name, data, channels string
	http                 int
	cmd                  *exec.Cmd
	log                  *bytes.Buffer
}

func startNode(t *testing.T, bin, db, name string, n *clusterNode) {
	t.Helper()
	if n.data == "" {
		n.data = t.TempDir()
		n.channels = filepath.Join(n.data, "channels")
		n.http = freePort(t)
		n.name = name
	}
	n.log = &bytes.Buffer{}
	n.cmd = exec.Command(bin, "serve", "-db", db, "-data", n.data, "-channels", n.channels,
		"-addr", fmt.Sprintf("127.0.0.1:%d", n.http), "-cluster", "active-passive", "-node-name", name,
		"-cluster-lease", "3s", "-fhir=false")
	n.cmd.Stdout, n.cmd.Stderr = n.log, n.log
	if err := n.cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if n.cmd.ProcessState == nil {
			_ = n.cmd.Process.Kill()
			_, _ = n.cmd.Process.Wait()
		}
	})
}

func (n *clusterNode) runs(channel string) bool {
	res, err := (&http.Client{Timeout: 2 * time.Second}).Get(fmt.Sprintf("http://127.0.0.1:%d/readyz?channel=%s", n.http, channel))
	if err != nil {
		return false
	}
	_, _ = io.Copy(io.Discard, res.Body)
	res.Body.Close()
	return res.StatusCode == http.StatusOK
}

func (n *clusterNode) kill() {
	_ = n.cmd.Process.Signal(syscall.SIGKILL)
	_, _ = n.cmd.Process.Wait()
}

func waitFor(t *testing.T, within time.Duration, what string, cond func() bool) time.Duration {
	t.Helper()
	start := time.Now()
	for time.Since(start) < within {
		if cond() {
			return time.Since(start)
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("after %s: %s", within, what)
	return 0
}

func killTest(t *testing.T, bin string, queued bool) {
	db := sqlitedb.TestURL("kill")
	rcv := newReceiver(t)
	feed := freePort(t)
	a, b := &clusterNode{}, &clusterNode{}
	startNode(t, bin, db, "a", a)
	queue := ""
	if queued {
		// One inline attempt, so a refusal goes straight to the queue and what follows it waits behind it.
		queue = "    retry: {attempts: 1}\n    queue: {enabled: true, backoff: 200ms, max_backoff: 1s}\n"
	}
	channel := fmt.Sprintf(`name: feed
source: {type: mllp, listen: "127.0.0.1:%d"}
destinations:
  - name: downstream
    type: mllp
    address: %s
    timeout: 2s
%s`, feed, rcv.ln.Addr().String(), queue)
	if err := os.MkdirAll(a.channels, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(a.channels, "feed.yaml"), []byte(channel), 0o600); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 30*time.Second, "a runs the feed", func() bool { return a.runs("feed") })
	startNode(t, bin, db, "b", b)
	waitFor(t, 20*time.Second, "b has the channel file", func() bool {
		_, err := os.Stat(filepath.Join(b.channels, "feed.yaml"))
		return err == nil
	})

	// The sender: one message at a time, resending until it gets an answer, as an MLLP sender does.
	const total = 300
	var acked []string
	var amu sync.Mutex
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	sent := make(chan struct{})
	go func() {
		defer close(sent)
		for i := 0; i < total && ctx.Err() == nil; i++ {
			cid := fmt.Sprintf("K%04d", i)
			msg := "MSH|^~\\&|SND|X|PF|X|20261017||ADT^A01|" + cid + "|P|2.5\rPID|1||" + cid + "^^^H^MR||SMITH^ALEX\r"
			for ctx.Err() == nil {
				if mllpSend(feed, msg) == "AA" {
					amu.Lock()
					acked = append(acked, cid)
					amu.Unlock()
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
		}
	}()
	ackedCount := func() int { amu.Lock(); defer amu.Unlock(); return len(acked) }

	waitFor(t, 30*time.Second, "a third of the messages acknowledged", func() bool { return ackedCount() >= total/3 })
	if queued {
		rcv.mu.Lock()
		rcv.refuse = true
		rcv.mu.Unlock()
		time.Sleep(2 * time.Second) // acknowledged messages now wait in the queue only
		store, err := sqlitedb.Open(db, sqlitedb.Options{})
		if err != nil {
			t.Fatal(err)
		}
		var waiting int
		_ = store.Read.QueryRow(`SELECT count(*) FROM queue WHERE state = 'pending'`).Scan(&waiting)
		store.Close()
		t.Logf("%d acknowledged messages wait in the shared queue when a is killed", waiting)
		if waiting < 5 {
			t.Errorf("only %d messages waited in the queue at the kill", waiting)
		}
	}
	a.kill()
	took := waitFor(t, 30*time.Second, "b takes over the feed", func() bool { return b.runs("feed") })
	t.Logf("b took over %s after a was killed", took.Round(100*time.Millisecond))
	if queued {
		rcv.mu.Lock()
		rcv.refuse = false
		rcv.mu.Unlock()
	}

	// a comes back, and b is killed in turn.
	waitFor(t, 30*time.Second, "two thirds acknowledged", func() bool { return ackedCount() >= 2*total/3 })
	startNode(t, bin, db, "a", a)
	time.Sleep(4 * time.Second) // a has watched b's lease for a while
	b.kill()
	took = waitFor(t, 30*time.Second, "a takes over again", func() bool { return a.runs("feed") })
	t.Logf("a took over %s after b was killed", took.Round(100*time.Millisecond))

	<-sent
	if n := ackedCount(); n != total {
		t.Fatalf("the sender got %d of %d acknowledgements", n, total)
	}
	// Every acknowledged message arrives; the queue may still be draining.
	var missing []string
	waitFor(t, 60*time.Second, "every acknowledged message reaches the receiver", func() bool {
		missing = missing[:0]
		amu.Lock()
		defer amu.Unlock()
		for _, cid := range acked {
			if rcv.count(cid) == 0 {
				missing = append(missing, cid)
			}
		}
		return len(missing) == 0
	})
	dupes := 0
	for _, cid := range acked {
		if c := rcv.count(cid); c > 1 {
			dupes += c - 1
		}
	}
	t.Logf("%d acknowledged, %d delivered more than once (resent by the sender or redelivered after a kill)", total, dupes)
	if queued {
		rcv.mu.Lock()
		refused := rcv.refused
		rcv.mu.Unlock()
		t.Logf("the receiver refused %d deliveries around the kill", refused)
		if refused == 0 {
			t.Error("nothing waited in the queue at the kill, so the queue's failover was not tested")
		}
	}
	if t.Failed() {
		t.Logf("a:\n%s\nb:\n%s", tail(a.log.String(), 40), tail(b.log.String(), 40))
	}
}

func tail(s string, n int) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, "\n")
}

// mllpSend sends one message and returns the MSA-1 code, or "" on any failure.
func mllpSend(port int, msg string) string {
	c, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), time.Second)
	if err != nil {
		return ""
	}
	defer c.Close()
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, err := c.Write([]byte("\x0b" + msg + "\x1c\r")); err != nil {
		return ""
	}
	br := bufio.NewReader(c)
	if _, err := br.ReadBytes(0x0b); err != nil {
		return ""
	}
	resp, err := br.ReadBytes(0x1c)
	if err != nil {
		return ""
	}
	for _, seg := range strings.Split(string(resp), "\r") {
		if strings.HasPrefix(seg, "MSA|") {
			parts := strings.Split(seg, "|")
			if len(parts) > 1 {
				return parts[1]
			}
		}
	}
	return ""
}
