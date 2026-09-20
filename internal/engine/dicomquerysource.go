package engine

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/dicom"
	"github.com/biodream-llc/perfuse/internal/dicomstate"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// DICOMQueryState is what a query source needs to remember between polls.
//
// An interface so the channel can run without one - a test harness, a dry run - and so the engine does not depend on the
// database package directly.
type DICOMQueryState interface {
	Load(ctx context.Context, tenantID, channel string) (dicomstate.Mark, error)
	SaveMark(ctx context.Context, tenantID, channel string, highWater, polledAt time.Time) error
	Unseen(ctx context.Context, tenantID, channel string, identifiers []string) ([]string, error)
	MarkSeen(ctx context.Context, tenantID, channel string, identifiers []string, at time.Time) error
	Prune(ctx context.Context, tenantID, channel string, retain time.Duration, now time.Time) (int64, error)
}

// dicomQueryPoller owns the polling goroutine.
//
// A stop channel and a waitgroup, matching the SFTP poller, so a channel that is stopped waits for a poll in flight rather
// than leaving an association half open against the archive.
type dicomQueryPoller struct {
	stop   chan struct{}
	closed sync.Once
	wg     sync.WaitGroup
}

func (p *dicomQueryPoller) close() {
	p.closed.Do(func() { close(p.stop) })
	p.wg.Wait()
}

// stopDICOMQuerySource ends polling and waits for a poll in flight.
func (c *Channel) stopDICOMQuerySource() error {
	if c.dicomQuery == nil {
		return nil
	}
	c.dicomQuery.close()
	c.dicomQuery = nil
	return nil
}

// startDICOMQuerySource begins polling an archive.
func (c *Channel) startDICOMQuerySource() error {
	cfg := c.cfg.Source.DICOMQuery
	if cfg == nil {
		return fmt.Errorf("channel %q has a dicom_query source with no configuration", c.cfg.Name)
	}

	if c.queryState == nil {
		// Refused rather than run without state. A query source with nothing to remember re-emits every matching study on
		// every poll, which against a real archive is a message storm that looks like the archive malfunctioning.
		return fmt.Errorf("channel %q polls a DICOM archive, which needs somewhere to record what it has already "+
			"seen; run with a database so the source can remember between polls", c.cfg.Name)
	}

	if cfg.TLS == nil {
		c.log.Warn("dicom query source",
			"detail", "this connection to the archive is not encrypted, and the responses carry patient names")
	}

	c.log.Info("dicom query starting",
		"archive", cfg.Address,
		"called_ae", cfg.CalledAE,
		"level", cfg.ResolvedLevel(),
		"every", cfg.Interval,
		"window", cfg.Window,
	)

	poller := &dicomQueryPoller{stop: make(chan struct{})}
	c.dicomQuery = poller

	poller.wg.Add(1)
	go func() {
		defer poller.wg.Done()
		c.pollDICOMLoop(poller, cfg)
	}()

	return nil
}

// pollDICOMLoop polls until the channel is stopped.
func (c *Channel) pollDICOMLoop(p *dicomQueryPoller, cfg *config.DICOMQuerySource) {
	// Derived from the stop channel so a poll in flight is cancelled rather than waited out. An archive that has stopped
	// answering would otherwise hold a shutdown open for the whole poll timeout.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		<-p.stop
		cancel()
	}()

	// Polled immediately rather than after one interval. An interval of an hour would otherwise mean an hour of a channel
	// showing as started while having done nothing, and the first thing anybody does after starting a channel is look at
	// whether it worked.
	c.pollDICOMOnce(ctx, cfg)

	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-p.stop:
			return
		case <-ticker.C:
			c.pollDICOMOnce(ctx, cfg)
		}
	}
}

// pollDICOMOnce runs one query and handles what it finds.
func (c *Channel) pollDICOMOnce(ctx context.Context, cfg *config.DICOMQuerySource) {
	pollCtx, cancel := context.WithTimeout(ctx, cfg.ResolvedTimeout())
	defer cancel()

	tenant := c.tenantID
	started := time.Now()

	mark, err := c.queryState.Load(pollCtx, tenant, c.cfg.Name)
	if err != nil {
		c.log.Error("could not read what this channel has already seen", "err", err)
		return
	}

	identifier, err := buildQueryIdentifier(cfg, mark, started)
	if err != nil {
		c.log.Error("the query could not be built", "err", err)
		return
	}

	client := &dicom.Client{
		Addr:      cfg.Address,
		CalledAE:  cfg.CalledAE,
		CallingAE: orDefaultAE(cfg.CallingAE),
		Timeout:   cfg.ResolvedTimeout(),
	}
	if cfg.TLS != nil {
		tlsCfg, err := tlsconf.ForSender(cfg.TLS)
		if err != nil {
			c.log.Error("the archive tls settings could not be used", "err", err)
			return
		}
		client.TLS = tlsCfg
	}

	results, err := client.Find(pollCtx, dicom.QueryRequest{
		Level:       dicom.QueryLevel(cfg.ResolvedLevel()),
		Match:       identifier,
		PatientRoot: cfg.PatientRoot,
		Limit:       cfg.ResolvedLimit(),
	})
	if err != nil {
		// The mark is deliberately not advanced. A failed poll must re-ask the same period next time, or a transient
		// archive outage becomes a permanent hole in what was collected.
		c.log.Error("the archive query failed", "archive", cfg.Address, "err", err)
		return
	}

	if len(results) >= cfg.ResolvedLimit() {
		// Worth saying plainly. Hitting the limit means the archive had more to give, so the poll is a partial answer and
		// the next one will re-cover the same period - but if the arrival rate genuinely exceeds the limit, the channel
		// never catches up and nothing else says so.
		c.log.Warn("the poll hit its limit, so the archive had more matches than were collected",
			"limit", cfg.ResolvedLimit(),
			"detail", "raise dicom_query.limit or shorten the interval; until then this poll is a partial answer")
	}

	ids, byID := identifyResults(cfg, results)

	unseen, err := c.queryState.Unseen(pollCtx, tenant, c.cfg.Name, ids)
	if err != nil {
		c.log.Error("could not work out which studies are new", "err", err)
		return
	}

	// The first poll records without emitting. Pointing this at an archive that already holds a million studies and
	// having it emit a message for each is not a useful first run, and the mistake would be noticed downstream rather
	// than here.
	if mark.FirstPoll && !cfg.EmitOnFirstPoll {
		if err := c.queryState.MarkSeen(pollCtx, tenant, c.cfg.Name, unseen, started); err != nil {
			c.log.Error("could not record the first poll", "err", err)
			return
		}
		c.saveQueryMark(pollCtx, tenant, cfg, started)
		c.log.Info("first poll recorded what the archive already holds and emitted nothing",
			"recorded", len(unseen),
			"detail", "set dicom_query.emit_on_first_poll if the existing contents should be sent")
		return
	}

	emitted := 0
	for _, id := range unseen {
		result, ok := byID[id]
		if !ok {
			continue
		}

		raw, err := queryResultToMessage(c.cfg.Name, cfg, result)
		if err != nil {
			c.log.Error("a match could not be turned into a message", "identifier", id, "err", err)
			continue
		}

		if _, err := c.handle(ctx, raw); err != nil {
			// Not recorded as seen, so the next poll tries again. A study that failed to hand off is exactly the one that
			// must not be forgotten.
			c.log.Error("a study could not be handled", "identifier", id, "err", err)
			continue
		}

		// Recorded one at a time, after handling. A batch recorded up front would lose everything after a mid-batch
		// failure; a batch recorded at the end would re-emit everything already handled.
		if err := c.queryState.MarkSeen(pollCtx, tenant, c.cfg.Name, []string{id}, started); err != nil {
			c.log.Error("a study was handled but could not be recorded as seen, so it may be sent again",
				"identifier", id, "err", err)
		}
		emitted++
	}

	c.saveQueryMark(pollCtx, tenant, cfg, started)

	// Pruned with a generous multiple of the window, because seen_at is our clock and the query window is the archive's.
	// Pruning at the boundary would reintroduce the duplicate the seen table exists to prevent.
	retain := cfg.Window + cfg.ResolvedOverlap()
	if retain <= 0 {
		retain = 30 * 24 * time.Hour
	}
	if removed, err := c.queryState.Prune(pollCtx, tenant, c.cfg.Name, retain*3, started); err != nil {
		c.log.Warn("could not prune the list of seen studies", "err", err)
	} else if removed > 0 {
		c.log.Debug("pruned studies too old to be returned again", "removed", removed)
	}

	c.log.Info("archive polled",
		"matches", len(results),
		"new", len(unseen),
		"emitted", emitted,
		"took", time.Since(started).Round(time.Millisecond),
	)
}

// saveQueryMark advances the high water mark.
func (c *Channel) saveQueryMark(ctx context.Context, tenant string, cfg *config.DICOMQuerySource, at time.Time) {
	if err := c.queryState.SaveMark(ctx, tenant, c.cfg.Name, at, at); err != nil {
		c.log.Error("could not record how far this channel has polled", "err", err)
	}
}

// buildQueryIdentifier turns the configured keys into query elements.
func buildQueryIdentifier(cfg *config.DICOMQuerySource, mark dicomstate.Mark, now time.Time) ([]dicom.Element, error) {
	var out []dicom.Element

	for name, value := range cfg.Match {
		tag, ok := config.DICOMTagFor(name)
		if !ok {
			return nil, fmt.Errorf("%q is not a dicom keyword this understands", name)
		}
		parsed, err := parseConfigTag(tag)
		if err != nil {
			return nil, err
		}
		out = append(out, dicom.Element{Tag: parsed, Value: []byte(value)})
	}

	for _, name := range cfg.Return {
		tag, ok := config.DICOMTagFor(name)
		if !ok {
			return nil, fmt.Errorf("%q is not a dicom keyword this understands", name)
		}
		parsed, err := parseConfigTag(tag)
		if err != nil {
			return nil, err
		}
		// Empty value: a request for the field rather than a filter on it.
		out = append(out, dicom.Element{Tag: parsed})
	}

	if cfg.Window > 0 {
		from := now.Add(-cfg.Window)
		if !mark.FirstPoll && !mark.HighWater.IsZero() {
			// From the last mark less the overlap, but never further back than the window. The overlap re-asks for a
			// period already polled, because a study can be registered with an earlier date than the day it arrived and a
			// poll boundary landing exactly on an arrival would otherwise lose it.
			candidate := mark.HighWater.Add(-cfg.ResolvedOverlap())
			if candidate.After(from) {
				from = candidate
			}
		}

		// A date range, which is the only range matching DICOM defines. Dates rather than date-times because an archive's
		// support for a time range within a date range is patchy, and a query some archives silently widen is worse than
		// one that is explicitly a day granularity.
		out = append(out, dicom.Element{
			Tag:   dicom.TagStudyDate,
			VR:    "DA",
			Value: []byte(from.UTC().Format("20060102") + "-" + now.UTC().Format("20060102")),
		})
	}

	return out, nil
}

// identifyResults picks the identifier that makes each result unique at the queried level.
func identifyResults(cfg *config.DICOMQuerySource, results []dicom.QueryResult) ([]string, map[string]dicom.QueryResult) {
	byID := make(map[string]dicom.QueryResult, len(results))
	ids := make([]string, 0, len(results))

	for _, r := range results {
		id := identifierForLevel(cfg.ResolvedLevel(), r)
		if id == "" {
			// Skipped rather than given a synthetic identifier. Something without the UID for its own level cannot be
			// deduplicated, so emitting it would mean emitting it again on every poll forever.
			continue
		}
		if _, dup := byID[id]; dup {
			continue
		}
		byID[id] = r
		ids = append(ids, id)
	}

	return ids, byID
}

// identifierForLevel returns the UID that is unique at a query level.
func identifierForLevel(level string, r dicom.QueryResult) string {
	switch level {
	case "PATIENT":
		return r.DataSet.Text(dicom.TagPatientID)
	case "SERIES":
		return r.DataSet.Text(dicom.TagSeriesInstanceUID)
	case "IMAGE":
		return r.DataSet.Text(dicom.TagSOPInstanceUID)
	default:
		return r.DataSet.Text(dicom.TagStudyInstanceUID)
	}
}

// parseConfigTag reads a "group,element" pair from the keyword table.
func parseConfigTag(s string) (dicom.Tag, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return dicom.Tag{}, fmt.Errorf("%q is not a dicom tag", s)
	}

	group, err := strconv.ParseUint(strings.TrimSpace(parts[0]), 16, 16)
	if err != nil {
		return dicom.Tag{}, fmt.Errorf("%q has an unreadable group: %w", s, err)
	}
	element, err := strconv.ParseUint(strings.TrimSpace(parts[1]), 16, 16)
	if err != nil {
		return dicom.Tag{}, fmt.Errorf("%q has an unreadable element: %w", s, err)
	}

	return dicom.Tag{Group: uint16(group), Element: uint16(element)}, nil
}

// orDefaultAE supplies an AE title when none is configured.
func orDefaultAE(configured string) string {
	if strings.TrimSpace(configured) == "" {
		return "PERFUSE"
	}
	return configured
}
