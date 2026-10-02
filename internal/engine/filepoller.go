package engine

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/vfs"
	"github.com/biodream-llc/perfuse/mllp"
)

// filePoller collects files from any transport and feeds their messages into a channel.
//
// # What this is actually for
//
// The transfer is the easy part. The problem is that a file being written and a file finished being written are
// indistinguishable: there is a size and a modification time, and both are true of a half-written file. Reading too
// early collects half a message, and because HL7 has no terminator, half a message is very often still parseable. The
// MSH is intact, the segments that arrived are well formed, the ones that did not are simply absent. It is accepted,
// acknowledged, stored, delivered, and nothing in the system will ever say it happened.
//
// So a file is read only once its size and modification time have been unchanged across two observations at least
// StableFor apart, and never on first sighting whatever StableFor says - one observation cannot establish that anything
// has stopped changing.
//
// # And the second problem
//
// The file is the only copy. Every message in it must be accepted before it is disposed of, because moving a file whose
// second message failed loses that message with no record anywhere.
type filePoller struct {
	ch   *Channel
	cfg  *config.FilePoll
	log  *slog.Logger
	kind string

	// open makes a connection per poll. A function rather than a held connection because polls are half a minute apart
	// by default, a firewall will close an idle connection somewhere in between, and the failure then lands on a file
	// rather than on a reconnect.
	open func(ctx context.Context) (vfs.FS, error)

	mu sync.Mutex
	// pending remembers what each file looked like last time, which is how stability is established.
	pending map[string]fileObservation
	// read remembers files already handled, for after_read: leave.
	read  map[string]time.Time
	stats FileSourceStats

	stop   chan struct{}
	closed sync.Once
	wg     sync.WaitGroup
}

// FileSourceStats reports what a poller has done.
type FileSourceStats struct {
	Polls        int64     `json:"polls"`
	FilesRead    int64     `json:"filesRead"`
	FilesWaiting int64     `json:"filesWaiting"`
	FilesFailed  int64     `json:"filesFailed"`
	FilesSkipped int64     `json:"filesSkipped"`
	Messages     int64     `json:"messages"`
	PollFailures int64     `json:"pollFailures"`
	Backlog      int64     `json:"backlog"`
	LastPoll     time.Time `json:"lastPoll,omitempty"`
	LastError    string    `json:"lastError,omitempty"`
	Connected    bool      `json:"connected"`
}

// newFilePoller prepares a poller. It does not start it.
func newFilePoller(ch *Channel, kind string, cfg *config.FilePoll,
	open func(ctx context.Context) (vfs.FS, error)) *filePoller {

	return &filePoller{
		ch: ch, cfg: cfg, log: ch.log, kind: kind, open: open,
		pending: make(map[string]fileObservation),
		read:    make(map[string]time.Time),
		stop:    make(chan struct{}),
	}
}

func (p *filePoller) start() {
	p.wg.Add(1)
	go p.loop()
}

func (p *filePoller) close() {
	p.closed.Do(func() { close(p.stop) })
	p.wg.Wait()
}

// Stats returns a copy.
func (p *filePoller) Stats() FileSourceStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stats
}

func (p *filePoller) loop() {
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
func (p *filePoller) pollOnce() {
	p.mu.Lock()
	p.stats.Polls++
	p.stats.LastPoll = time.Now()
	p.mu.Unlock()

	// Bounded by the poll interval. A poll that cannot be abandoned holds up shutdown until a network timeout expires,
	// and one that overruns its interval stacks up behind the ticker.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-p.stop:
			cancel()
		case <-ctx.Done():
		}
	}()

	fs, err := p.open(ctx)
	if err != nil {
		p.pollFailed(err)
		return
	}
	defer fs.Close()

	p.mu.Lock()
	p.stats.Connected = true
	p.mu.Unlock()

	entries, err := fs.List(ctx, p.cfg.Dir)
	if err != nil {
		p.pollFailed(fmt.Errorf("listing %s: %w", fs.Describe(), err))
		return
	}

	ready, waiting := p.assess(ctx, fs, entries)

	p.mu.Lock()
	p.stats.FilesWaiting = int64(waiting)
	// Backlog is what is ready but will not be read this poll. Reported separately from waiting, because the two mean
	// opposite things: waiting is normal and self-clearing, backlog means the batch size is smaller than the arrival
	// rate and the directory is growing.
	if over := len(ready) - p.limit(); over > 0 {
		p.stats.Backlog = int64(over)
	} else {
		p.stats.Backlog = 0
	}
	p.mu.Unlock()

	if limit := p.limit(); limit > 0 && len(ready) > limit {
		p.log.Info("more files are ready than one poll will read",
			"channel", p.ch.cfg.Name, "kind", p.kind,
			"ready", len(ready), "reading", limit,
			"why", "reading them all in one pass would hold the poll loop and make the channel look hung")
		ready = ready[:limit]
	}

	for _, name := range ready {
		select {
		case <-p.stop:
			return
		default:
		}
		p.handleFile(ctx, fs, name)
	}
}

// limit is how many files one poll will read. Zero means no limit.
func (p *filePoller) limit() int {
	if p.cfg.BatchSize <= 0 {
		return 0
	}
	return p.cfg.BatchSize
}

// assess decides which files have settled, and returns them in the configured order.
func (p *filePoller) assess(ctx context.Context, fs vfs.FS, entries []vfs.Entry) (ready []string, waiting int) {
	now := time.Now()
	seen := map[string]bool{}
	type candidate struct {
		name     string
		modified time.Time
	}
	var settled []candidate

	for _, e := range entries {
		if e.IsDir {
			continue
		}
		name := e.Name
		seen[name] = true

		if ok := matchPattern(p.cfg.Pattern, name); !ok {
			continue
		}
		// A file still being written by whoever produced it. Skipped by name because this is the convention the
		// sending side uses, for exactly the reason Perfuse uses it when writing.
		if isPartialName(name) {
			continue
		}
		if e.Size == 0 {
			// Almost always a file that has just been created and not yet written to. Counted as waiting rather than
			// read as an empty message.
			waiting++
			continue
		}
		if e.Size > p.cfg.MaxFileSize {
			p.tooBig(ctx, fs, name, e.Size)
			continue
		}

		p.mu.Lock()
		if _, already := p.read[name]; already {
			p.mu.Unlock()
			continue
		}
		prev, known := p.pending[name]
		obs := fileObservation{size: e.Size, modified: e.ModTime, seenAt: now}

		switch {
		case !known:
			// First sighting. Never read on the first sighting, whatever stable_for says: one observation cannot
			// establish that anything has stopped changing.
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
				settled = append(settled, candidate{name: name, modified: e.ModTime})
			} else {
				waiting++
			}
		}
		p.mu.Unlock()
	}

	// Forget files that have gone, so the map does not grow for the lifetime of the process.
	p.mu.Lock()
	for name := range p.pending {
		if !seen[name] {
			delete(p.pending, name)
		}
	}
	p.mu.Unlock()

	// Ordered, because directory listing order is arbitrary on most filesystems and an ADT stream where an A08 update
	// arrives before the A01 admission produces patients that do not exist yet. Unsorted, the order would change
	// between polls for no visible reason.
	switch p.cfg.SortBy {
	case config.SortByModified:
		sort.SliceStable(settled, func(i, j int) bool {
			if settled[i].modified.Equal(settled[j].modified) {
				// Same timestamp is common: many systems write a batch in one second, and some filesystems only
				// record whole seconds. Falling back to name keeps the order stable rather than arbitrary.
				return settled[i].name < settled[j].name
			}
			return settled[i].modified.Before(settled[j].modified)
		})
	case config.SortByNone:
	default:
		sort.Slice(settled, func(i, j int) bool { return settled[i].name < settled[j].name })
	}

	ready = make([]string, 0, len(settled))
	for _, c := range settled {
		ready = append(ready, c.name)
	}
	return ready, waiting
}

// handleFile reads one file and feeds its messages in.
func (p *filePoller) handleFile(ctx context.Context, fs vfs.FS, name string) {
	full := fs.Join(p.cfg.Dir, name)

	body, err := p.readFile(ctx, fs, full)
	if err != nil {
		p.fileFailed(ctx, fs, name, err)
		return
	}

	messages, err := p.split(body)
	if err != nil {
		p.fileFailed(ctx, fs, name, err)
		return
	}
	if len(messages) == 0 {
		p.fileFailed(ctx, fs, name, fmt.Errorf(
			"the file is %d bytes but no message could be found in it. If it is not HL7, set raw so the whole file "+
				"is delivered as one message; if it is, check whether framed is set correctly for it", len(body)))
		return
	}

	// Every message in the file has to be accepted before the file is disposed of. Moving a file whose second message
	// failed would lose that message with no record anywhere, and the file is the only copy.
	accepted := 0
	for i, msg := range messages {
		if _, err := p.ch.handle(ctx, msg); err != nil {
			p.fileFailed(ctx, fs, name, fmt.Errorf(
				"message %d of %d in the file could not be processed, so the whole file has been left alone "+
					"rather than partly consumed: %w", i+1, len(messages), err))
			return
		}
		switch outcome := p.ch.lastOutcome.get(); outcome {
		case Failed, Unparseable:
			p.fileFailed(ctx, fs, name, fmt.Errorf(
				"message %d of %d in the file was %s, so the whole file has been left alone rather than partly "+
					"consumed", i+1, len(messages), outcome))
			return
		}
		accepted++
	}

	p.mu.Lock()
	p.stats.FilesRead++
	p.stats.Messages += int64(accepted)
	delete(p.pending, name)
	p.mu.Unlock()

	p.dispose(ctx, fs, name)

	p.log.Info("read a file",
		"channel", p.ch.cfg.Name, "kind", p.kind, "file", name, "messages", accepted)
}

// readFile reads a whole file into memory.
//
// Bounded by MaxFileSize, which was already checked against the listing. Checked again while reading because the file
// could have grown between the two, and a connector that trusted a stat is one big file away from being an outage.
func (p *filePoller) readFile(ctx context.Context, fs vfs.FS, full string) ([]byte, error) {
	rc, err := fs.Open(ctx, full)
	if err != nil {
		return nil, fmt.Errorf("opening %s: %w", full, err)
	}
	defer rc.Close()

	body, err := io.ReadAll(io.LimitReader(rc, p.cfg.MaxFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", full, err)
	}
	if int64(len(body)) > p.cfg.MaxFileSize {
		return nil, fmt.Errorf("%s grew past max_file_size (%d bytes) while it was being read",
			full, p.cfg.MaxFileSize)
	}
	return body, nil
}

// split turns a file into messages.
func (p *filePoller) split(body []byte) ([][]byte, error) {
	// Raw first, because it overrides everything. A CSV batch, an X12 claim file or a PDF has no message boundaries to
	// find, and splitting it on lines beginning MSH produces nothing while reporting that the file is not HL7 - true,
	// but not the point, since it was never meant to be.
	if p.cfg.Raw {
		if len(bytes.TrimSpace(body)) == 0 {
			return nil, nil
		}
		return [][]byte{body}, nil
	}

	if p.cfg.Framed {
		var out [][]byte
		r := mllp.NewReader(bytes.NewReader(body), int(p.cfg.MaxFileSize))
		for {
			msg, err := r.ReadMessage()
			if err == io.EOF {
				return out, nil
			}
			if err != nil {
				return nil, fmt.Errorf("the file is set as framed but the framing is not valid after %d "+
					"message(s): %w", len(out), err)
			}
			out = append(out, msg)
		}
	}

	return splitOnMSH(body), nil
}

// dispose does whatever after_read says.
func (p *filePoller) dispose(ctx context.Context, fs vfs.FS, name string) {
	full := fs.Join(p.cfg.Dir, name)

	switch p.cfg.AfterRead {
	case config.AfterReadDelete:
		if err := fs.Remove(ctx, full); err != nil {
			// Recorded locally as well, because a file that could not be deleted would otherwise be read again on the
			// next poll and every message resent.
			p.rememberRead(name)
			p.log.Error("the messages in this file were accepted but the file could not be deleted, so it is still "+
				"there. Perfuse will not read it again while this process runs, but a restart would resend every "+
				"message in it",
				"channel", p.ch.cfg.Name, "kind", p.kind, "file", name, "error", err)
		}

	case config.AfterReadMove:
		if err := p.moveTo(ctx, fs, name, p.cfg.MoveTo); err != nil {
			p.rememberRead(name)
			p.log.Error("the messages in this file were accepted but the file could not be moved, so it is still "+
				"where it was. Perfuse will not read it again while this process runs, but a restart would resend "+
				"every message in it",
				"channel", p.ch.cfg.Name, "kind", p.kind, "file", name, "error", err)
		}

	case config.AfterReadLeave:
		p.rememberRead(name)
	}
}

// moveTo relocates a file, creating the target directory if needed.
func (p *filePoller) moveTo(ctx context.Context, fs vfs.FS, name, dir string) error {
	if err := fs.MkdirAll(ctx, dir); err != nil {
		// Only a problem if it does not already exist, which the rename will report.
		p.log.Debug("could not create the target directory", "dir", dir, "error", err)
	}

	from := fs.Join(p.cfg.Dir, name)
	to := fs.Join(dir, name)

	if err := fs.Rename(ctx, from, to); err == nil {
		return nil
	}

	// A file of that name is already there, which happens whenever a sending system reuses names. Suffixed rather than
	// overwritten: the older file is somebody's evidence.
	stamped := fs.Join(dir, fmt.Sprintf("%s.%d", name, time.Now().UnixNano()))
	if err := fs.Rename(ctx, from, stamped); err != nil {
		return fmt.Errorf("moving %s to %s: %w", from, dir, err)
	}
	p.log.Info("a file of that name was already in the target directory, so this one was given a suffix rather "+
		"than replacing it", "channel", p.ch.cfg.Name, "kind", p.kind, "file", name)
	return nil
}

// fileFailed quarantines a file that could not be processed.
func (p *filePoller) fileFailed(ctx context.Context, fs vfs.FS, name string, cause error) {
	p.mu.Lock()
	p.stats.FilesFailed++
	p.stats.LastError = cause.Error()
	p.mu.Unlock()

	p.log.Error("a file could not be processed",
		"channel", p.ch.cfg.Name, "kind", p.kind, "file", name, "error", cause)

	if p.cfg.ErrorDir == "" {
		// Nowhere to put it. Remembered so the same file does not fail on every poll forever, filling the log with one
		// problem repeated.
		p.rememberRead(name)
		return
	}

	if err := p.moveTo(ctx, fs, name, p.cfg.ErrorDir); err != nil {
		p.rememberRead(name)
		p.log.Error("the file could not be moved to the error directory either, so it has been left where it is and "+
			"will not be retried while this process runs",
			"channel", p.ch.cfg.Name, "kind", p.kind, "file", name, "error", err)
		return
	}

	p.mu.Lock()
	delete(p.pending, name)
	p.mu.Unlock()
}

// tooBig quarantines a file larger than max_file_size.
//
// Moved rather than skipped. Skipped, it is listed on every poll for the rest of the process's life and the log says the
// same thing every thirty seconds, which is how a real problem becomes background noise.
func (p *filePoller) tooBig(ctx context.Context, fs vfs.FS, name string, size int64) {
	p.mu.Lock()
	p.stats.FilesSkipped++
	p.mu.Unlock()

	p.fileFailed(ctx, fs, name, fmt.Errorf(
		"the file is %d bytes, larger than max_file_size (%d). It has not been read at all, so nothing in it has "+
			"been delivered", size, p.cfg.MaxFileSize))
}

func (p *filePoller) pollFailed(err error) {
	p.mu.Lock()
	p.stats.PollFailures++
	p.stats.LastError = err.Error()
	p.stats.Connected = false
	p.mu.Unlock()

	p.log.Error("a poll failed", "channel", p.ch.cfg.Name, "kind", p.kind, "error", err)
}

func (p *filePoller) rememberRead(name string) {
	p.mu.Lock()
	p.read[name] = time.Now()
	delete(p.pending, name)
	p.mu.Unlock()
}

// matchPattern matches a filename against a glob.
//
// Case-insensitive on the pattern's extension is deliberately *not* done: a pattern of *.hl7 not matching FILE.HL7 is
// surprising, but matching it on Linux and not on a case-sensitive share would be worse - the same configuration
// behaving differently per site is the kind of difference nobody finds.
func matchPattern(pattern, name string) bool {
	if pattern == "" || pattern == "*" {
		return true
	}
	ok, err := pathMatch(pattern, name)
	if err != nil {
		// A malformed pattern. Matching nothing is safer than matching everything: a channel that reads no files is
		// noticed immediately, one that reads every file may have already delivered something it should not have.
		return false
	}
	return ok
}
