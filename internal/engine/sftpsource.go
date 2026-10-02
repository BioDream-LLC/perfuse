package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"path"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/metrics"
	"github.com/biodream-llc/perfuse/internal/sftpconn"
	"github.com/biodream-llc/perfuse/mllp"
)

// sftpPoller collects files from an SFTP server and feeds their messages into the
// channel.
//
// The transfer is the easy part. The problem this connector exists to solve is that
// a file being written to and a file finished being written to are indistinguishable
// over SFTP. There is no lock, no flag and no notification: there is a size and a
// modification time, and both are true of a half-written file.
//
// Reading too early collects half a message. HL7 has no terminator, so half a
// message is very often still parseable - the MSH is intact, the segments that
// arrived are well formed, and the ones that did not are simply absent. It is
// accepted, acknowledged, stored, delivered, and the missing half is never mentioned
// again. Nothing in the system will ever say it happened.
//
// So a file is only read once its size and modification time have been unchanged
// across two observations at least StableFor apart.
type sftpPoller struct {
	ch  *Channel
	cfg *config.SFTPSource
	log *slog.Logger

	mu sync.Mutex
	// pending remembers what each file looked like last time, which is how stability
	// is established.
	pending map[string]fileObservation
	// read remembers files already handled, for after_read: leave.
	read  map[string]time.Time
	stats SFTPSourceStats

	stop   chan struct{}
	closed sync.Once
	wg     sync.WaitGroup
}

// fileObservation is one look at a remote file.
type fileObservation struct {
	size     int64
	modified time.Time
	seenAt   time.Time
	// stableSince is when the size and time were first seen unchanged.
	stableSince time.Time
}

// SFTPSourceStats reports what the poller has done.
type SFTPSourceStats struct {
	Polls        int64     `json:"polls"`
	FilesRead    int64     `json:"filesRead"`
	FilesWaiting int64     `json:"filesWaiting"`
	FilesFailed  int64     `json:"filesFailed"`
	Messages     int64     `json:"messages"`
	PollFailures int64     `json:"pollFailures"`
	LastPoll     time.Time `json:"lastPoll,omitempty"`
	LastError    string    `json:"lastError,omitempty"`
	Connected    bool      `json:"connected"`
}

// startSFTPSource begins polling.
func (c *Channel) startSFTPSource() error {
	cfg := c.cfg.Source.SFTP
	if cfg == nil {
		return fmt.Errorf("channel %q has an sftp source but no sftp block", c.cfg.Name)
	}

	p := &sftpPoller{
		ch: c, cfg: cfg, log: c.log,
		pending: make(map[string]fileObservation),
		read:    make(map[string]time.Time),
		stop:    make(chan struct{}),
	}
	c.sftp = p

	for _, w := range cfg.Warnings() {
		c.log.Warn("sftp source", "channel", c.cfg.Name, "warning", w)
	}

	// Verified now, because a wrong key or an unverifiable host key is a
	// configuration error rather than a transient one, and it should be reported when
	// somebody is looking at the channel rather than half an hour later.
	conn, err := sftpconn.Dial(p.settings())
	if err != nil {
		return fmt.Errorf("channel %q cannot reach the SFTP server: %w", c.cfg.Name, err)
	}
	_ = conn.Close()

	c.log.Info("polling an SFTP directory",
		"channel", c.cfg.Name,
		"host", cfg.Host, "dir", cfg.Dir,
		"every", cfg.PollInterval, "settles_for", cfg.StableFor)

	p.wg.Add(1)
	go p.loop()
	return nil
}

// stopSFTPSource stops polling.
func (c *Channel) stopSFTPSource() error {
	if c.sftp == nil {
		return nil
	}
	c.sftp.close()
	c.sftp = nil
	return nil
}

func (p *sftpPoller) close() {
	p.closed.Do(func() { close(p.stop) })
	p.wg.Wait()
}

func (p *sftpPoller) settings() sftpconn.Settings {
	return sftpconn.Settings{
		Host:                     p.cfg.Host,
		User:                     p.cfg.User,
		Password:                 p.cfg.Password,
		KeyFile:                  p.cfg.KeyFile,
		KeyPassphrase:            p.cfg.KeyPassphrase,
		KnownHostsFile:           p.cfg.KnownHostsFile,
		InsecureSkipHostKeyCheck: p.cfg.InsecureSkipHostKeyCheck,
		Timeout:                  p.cfg.Timeout,
	}
}

// loop polls until stopped.
func (p *sftpPoller) loop() {
	defer p.wg.Done()

	p.pollOnce()

	t := time.NewTicker(p.cfg.PollInterval)
	defer t.Stop()

	for {
		select {
		case <-p.stop:
			return
		case <-t.C:
			p.pollOnce()
		}
	}
}

// pollOnce lists the directory and reads whatever has settled.
//
// A connection per poll rather than one held open. Polls are half a minute apart by
// default, a firewall will close an idle SSH connection somewhere in between, and
// the failure then lands on a file rather than on a reconnect.
func (p *sftpPoller) pollOnce() {
	p.mu.Lock()
	p.stats.Polls++
	p.stats.LastPoll = time.Now()
	p.mu.Unlock()

	conn, err := sftpconn.Dial(p.settings())
	if err != nil {
		p.pollFailed(err)
		return
	}
	defer conn.Close()

	p.mu.Lock()
	p.stats.Connected = true
	p.mu.Unlock()

	entries, err := conn.Client.ReadDir(p.cfg.Dir)
	if err != nil {
		p.pollFailed(fmt.Errorf("listing %s: %w", p.cfg.Dir, err))
		return
	}

	now := time.Now()
	var ready []string
	waiting := 0
	seen := map[string]bool{}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		seen[name] = true

		if ok, _ := path.Match(p.cfg.Pattern, name); !ok {
			continue
		}
		// A file still being written by whoever produced it. Skipped by name because
		// this is the convention the sending side uses for exactly the reason Perfuse
		// uses it when writing.
		if isPartialName(name) {
			continue
		}
		if e.Size() == 0 {
			// An empty file is almost always a file that has just been created and not
			// yet written to.
			waiting++
			continue
		}
		if e.Size() > p.cfg.MaxFileSize {
			p.tooBig(conn, name, e.Size())
			continue
		}

		p.mu.Lock()
		if _, already := p.read[name]; already {
			p.mu.Unlock()
			continue
		}
		prev, known := p.pending[name]
		obs := fileObservation{size: e.Size(), modified: e.ModTime(), seenAt: now}

		switch {
		case !known:
			// First sighting. Never read on the first sighting, whatever stable_for says:
			// one observation cannot establish that anything has stopped changing.
			obs.stableSince = now
			p.pending[name] = obs
			waiting++

		case prev.size != obs.size || !prev.modified.Equal(obs.modified):
			// Still being written. The clock restarts.
			obs.stableSince = now
			p.pending[name] = obs
			waiting++

		default:
			obs.stableSince = prev.stableSince
			p.pending[name] = obs
			if now.Sub(prev.stableSince) >= p.cfg.StableFor {
				ready = append(ready, name)
			} else {
				waiting++
			}
		}
		p.mu.Unlock()
	}

	// Forget files that have gone, so the map does not grow for the lifetime of the
	// process.
	p.mu.Lock()
	for name := range p.pending {
		if !seen[name] {
			delete(p.pending, name)
		}
	}
	p.stats.FilesWaiting = int64(waiting)
	p.mu.Unlock()

	p.ch.setGauge(metrics.SFTPFilesWaiting, float64(waiting))

	for _, name := range ready {
		select {
		case <-p.stop:
			return
		default:
		}
		p.handleFile(conn, name)
	}
}

// handleFile downloads one file and feeds its messages in.
func (p *sftpPoller) handleFile(conn *sftpconn.Conn, name string) {
	remote := path.Join(p.cfg.Dir, name)

	body, err := p.download(conn, remote)
	if err != nil {
		p.fileFailed(conn, name, err)
		return
	}

	messages, err := p.split(body)
	if err != nil {
		p.fileFailed(conn, name, err)
		return
	}
	if len(messages) == 0 {
		p.fileFailed(conn, name, fmt.Errorf(
			"the file is %d bytes but contains no HL7 message, so either it is not HL7 "+
				"or framed is set wrongly for it", len(body)))
		return
	}

	// Every message in the file has to be accepted before the file is disposed of.
	// Moving a file whose second message failed would lose that message with no
	// record anywhere, and the file is the only copy.
	ctx := context.Background()
	accepted := 0

	for i, msg := range messages {
		if _, err := p.ch.handle(ctx, msg); err != nil {
			p.fileFailed(conn, name, fmt.Errorf(
				"message %d of %d in the file could not be processed, so the whole file "+
					"has been left alone rather than partly consumed: %w",
				i+1, len(messages), err))
			return
		}
		switch outcome := p.ch.lastOutcome.get(); outcome {
		case Failed, Unparseable:
			p.fileFailed(conn, name, fmt.Errorf(
				"message %d of %d in the file was %s, so the whole file has been left "+
					"alone rather than partly consumed", i+1, len(messages), outcome))
			return
		}
		accepted++
	}

	p.mu.Lock()
	p.stats.FilesRead++
	p.stats.Messages += int64(accepted)
	delete(p.pending, name)
	p.mu.Unlock()

	p.ch.addMetric(metrics.SFTPFilesRead, 1)
	p.dispose(conn, name)

	p.log.Info("read an SFTP file",
		"channel", p.ch.cfg.Name, "file", name, "messages", accepted)
}

// download reads a whole file into memory.
//
// Bounded by MaxFileSize, which was already checked against the listing. Checked
// again while reading because the file could have grown between the two, and a
// connector that trusted a stat is one big file away from being an outage.
func (p *sftpPoller) download(conn *sftpconn.Conn, remote string) ([]byte, error) {
	f, err := conn.Client.Open(remote)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", remote, err)
	}
	defer f.Close()

	limited := io.LimitReader(f, p.cfg.MaxFileSize+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", remote, err)
	}
	if int64(len(body)) > p.cfg.MaxFileSize {
		return nil, fmt.Errorf("%s is larger than max_file_size (%d bytes)",
			remote, p.cfg.MaxFileSize)
	}
	return body, nil
}

// split turns a file into messages.
func (p *sftpPoller) split(body []byte) ([][]byte, error) {
	if p.cfg.Framed {
		var out [][]byte
		r := mllp.NewReader(bytes.NewReader(body), int(p.cfg.MaxFileSize))
		for {
			msg, err := r.ReadMessage()
			if err == io.EOF {
				return out, nil
			}
			if err != nil {
				return nil, fmt.Errorf("the file is set as framed but the framing is not "+
					"valid after %d message(s): %w", len(out), err)
			}
			out = append(out, msg)
		}
	}
	return splitOnMSH(body), nil
}

// splitOnMSH divides a file into messages at each MSH segment.
//
// Only used for an unframed file, and only at the start of a line. MSH can appear
// inside a free-text field, and splitting there would cut a message in half and
// produce two invalid ones from one valid one.
func splitOnMSH(body []byte) [][]byte {
	normalised := bytes.ReplaceAll(body, []byte("\r\n"), []byte("\r"))
	normalised = bytes.ReplaceAll(normalised, []byte("\n"), []byte("\r"))

	var out [][]byte
	var current []byte

	for _, line := range bytes.Split(normalised, []byte("\r")) {
		if len(bytes.TrimSpace(line)) == 0 {
			continue
		}
		if bytes.HasPrefix(line, []byte("MSH|")) {
			if len(current) > 0 {
				out = append(out, current)
			}
			current = nil
		}
		if len(current) == 0 && !bytes.HasPrefix(line, []byte("MSH|")) {
			// A segment before any MSH belongs to nothing. Skipped rather than made into a
			// message that could never be valid.
			continue
		}
		current = append(current, line...)
		current = append(current, '\r')
	}
	if len(current) > 0 {
		out = append(out, current)
	}
	return out
}

// dispose does whatever after_read says.
func (p *sftpPoller) dispose(conn *sftpconn.Conn, name string) {
	remote := path.Join(p.cfg.Dir, name)

	switch p.cfg.AfterRead {
	case "delete":
		if err := conn.Client.Remove(remote); err != nil {
			// Recorded locally as well, because a file that could not be deleted would
			// otherwise be read again on the next poll and every message resent.
			p.rememberRead(name)
			p.log.Error("the messages in this file were accepted but the file could not "+
				"be deleted, so it is still on the server. Perfuse will not read it again "+
				"while this process runs, but a restart would resend every message in it",
				"channel", p.ch.cfg.Name, "file", name, "error", err)
		}

	case "move":
		if err := p.moveTo(conn, name, p.cfg.MoveTo); err != nil {
			p.rememberRead(name)
			p.log.Error("the messages in this file were accepted but the file could not "+
				"be moved, so it is still where it was. Perfuse will not read it again "+
				"while this process runs, but a restart would resend every message in it",
				"channel", p.ch.cfg.Name, "file", name, "error", err)
		}

	case "leave":
		p.rememberRead(name)
	}
}

// moveTo relocates a file, creating the target directory if needed.
func (p *sftpPoller) moveTo(conn *sftpconn.Conn, name, dir string) error {
	if err := conn.Client.MkdirAll(dir); err != nil {
		// Only a problem if it does not already exist, which the rename will report.
		p.log.Debug("could not create the target directory",
			"dir", dir, "error", err)
	}

	from := path.Join(p.cfg.Dir, name)
	to := path.Join(dir, name)

	if err := conn.Client.Rename(from, to); err == nil {
		return nil
	}

	// A file of the same name is already there, which happens whenever a sending
	// system reuses names. Suffixed rather than overwritten: the older file is
	// somebody's evidence.
	stamped := path.Join(dir, fmt.Sprintf("%s.%d", name, time.Now().UnixNano()))
	if err := conn.Client.Rename(from, stamped); err != nil {
		return fmt.Errorf("moving %s to %s: %w", from, dir, err)
	}
	p.log.Info("a file of that name was already in the target directory, so this one "+
		"was given a suffix rather than replacing it",
		"channel", p.ch.cfg.Name, "file", name)
	return nil
}

// fileFailed records a file that could not be processed and gets it out of the way.
func (p *sftpPoller) fileFailed(conn *sftpconn.Conn, name string, cause error) {
	p.mu.Lock()
	p.stats.FilesFailed++
	p.stats.LastError = cause.Error()
	p.mu.Unlock()

	p.ch.addMetric(metrics.SFTPFilesFailed, 1)

	if p.cfg.ErrorDir == "" {
		// Left in place with nowhere to put it. Said plainly, because the file will be
		// retried on every poll and the warning will repeat until somebody acts.
		p.log.Error("a file could not be processed and no error_dir is configured, so "+
			"it has been left where it is and will be tried again on every poll. Set "+
			"error_dir so one bad file does not become a permanent warning",
			"channel", p.ch.cfg.Name, "file", name, "error", cause)
		return
	}

	if err := p.moveTo(conn, name, p.cfg.ErrorDir); err != nil {
		p.log.Error("a file could not be processed and could not be moved to error_dir "+
			"either, so it will be tried again on every poll",
			"channel", p.ch.cfg.Name, "file", name,
			"error", cause, "move_error", err)
		return
	}

	p.mu.Lock()
	delete(p.pending, name)
	p.mu.Unlock()

	// Loud, because this is a file of clinical messages that has been set aside and
	// nothing else will mention it again.
	p.log.Error("a file could not be processed and has been moved to error_dir. The "+
		"messages in it have NOT been delivered and will not be retried. Nothing else "+
		"will mention this file again, so it needs a person",
		"channel", p.ch.cfg.Name, "file", name,
		"moved_to", p.cfg.ErrorDir, "error", cause)
}

// tooBig sets aside a file larger than the limit.
func (p *sftpPoller) tooBig(conn *sftpconn.Conn, name string, size int64) {
	p.fileFailed(conn, name, fmt.Errorf(
		"the file is %d bytes, over max_file_size. A file this large is usually a "+
			"batch nobody intended to send through an interface, or a log that landed "+
			"in the wrong directory", size))
}

func (p *sftpPoller) pollFailed(err error) {
	p.mu.Lock()
	p.stats.PollFailures++
	p.stats.LastError = err.Error()
	p.stats.Connected = false
	p.mu.Unlock()

	p.ch.addMetric(metrics.SFTPPollFailures, 1)

	// Not fatal. An SFTP server being briefly unreachable is normal, and stopping the
	// channel would need a person to start it again.
	p.log.Error("the SFTP poll failed",
		"channel", p.ch.cfg.Name, "host", p.cfg.Host, "error", err)
}

func (p *sftpPoller) rememberRead(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()

	p.read[name] = time.Now()
	delete(p.pending, name)

	if len(p.read) <= 100_000 {
		return
	}
	// Bounded, like the database poller's key set. Unbounded growth would be a
	// different outage six months out.
	cutoff := time.Now().Add(-24 * time.Hour)
	for k, at := range p.read {
		if at.Before(cutoff) {
			delete(p.read, k)
		}
	}
}

// isPartialName reports whether a name looks like a file still being written.
//
// These are the conventions in use in the wild, and honouring them costs nothing.
// The sending side adopted them for exactly the reason Perfuse needs them.
func isPartialName(name string) bool {
	for _, suffix := range []string{".part", ".tmp", ".temp", ".filepart", ".writing"} {
		if len(name) > len(suffix) &&
			equalFoldASCII(name[len(name)-len(suffix):], suffix) {
			return true
		}
	}
	// The dotfile convention, and what many tools use for an in-progress upload.
	return len(name) > 0 && (name[0] == '.' || name[0] == '~')
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}

// Stats returns a copy of the counters.
func (p *sftpPoller) Stats() SFTPSourceStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}
