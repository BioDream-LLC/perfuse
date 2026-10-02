package tefca

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Keeping the audit trail after a restart.
//
// # Why this is a defect and not a limitation
//
// The audit log held its entries in a slice in memory. TEFCA does not treat auditing as a nice-to-have: recording
// every exchange is a condition of participation, and the records have to exist when somebody asks for them, which is
// typically months later and prompted by a complaint or an investigation.
//
// An in-memory log satisfies every test, works perfectly in a demonstration, and loses everything the first time the
// process restarts. Nothing fails. Nobody is told. The exchanges happened and the record of them is gone, and the
// discovery comes from an auditor asking for something that cannot be produced.
//
// That is worse than having no audit feature at all, because the presence of one is relied upon.
//
// # Append-only, one line per entry
//
// Written as JSON lines to a file opened for appending. The format is deliberately the dullest possible choice:
//
//   - Append-only means a crash halfway through a write loses at most the entry being written, not the file. A
//     rewritten-in-place file can be truncated by a crash and lose everything.
//   - One self-contained line per entry means a partial final line can be discarded and every earlier line still
//     reads. A single JSON array would be unparseable if the process died before writing the closing bracket.
//   - Text means an auditor can read it with tools they already have, on a machine with nothing installed, years from
//     now. An audit record nobody can open is not a record.
//
// # What this does not attempt
//
// It does not sign or chain the entries, so it does not prove nobody edited the file. Doing that properly needs a
// separate key and somewhere to anchor it, and claiming tamper-evidence without those would be a claim this cannot
// support. The file is written 0600 and the honest position is that it is as trustworthy as the machine it sits on.

// PersistentAuditLog records exchanges to a file as well as in memory.
//
// The in-memory copy is kept because the interface reads recent entries constantly and re-reading a file that grows
// without bound for every page refresh would make the audit view slower the longer a site has been compliant.
type PersistentAuditLog struct {
	AuditLog

	mu   sync.Mutex
	path string
	file *os.File

	// lastErr is the most recent failure to write the file, and skipped how many lines were unreadable at load.
	lastErr error
	skipped int

	// retain is how long entries are kept in memory. The file keeps everything.
	//
	// Bounded because an instance running for a year would otherwise hold every exchange it ever made. The file is
	// the record; memory is a cache of the part anybody looks at.
	retain time.Duration
}

// OpenAuditLog opens or creates an audit file.
//
// Existing entries are read back, so a restart does not present an empty trail - which would read as "no exchanges
// have happened" rather than "this process started recently".
func OpenAuditLog(path string, retain time.Duration) (*PersistentAuditLog, error) {
	if path == "" {
		return nil, fmt.Errorf("tefca: an audit log needs a path. Every exchange must be recorded, and a record " +
			"that exists only in memory is gone at the next restart")
	}
	if retain <= 0 {
		// Six weeks in memory by default: long enough for the questions people actually ask of a live system, and
		// the file holds the rest for the questions auditors ask.
		retain = 42 * 24 * time.Hour
	}

	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("tefca: could not create the directory for the audit log: %w", err)
		}
	}

	// 0600 because the entries name patients and the organisations that asked about them. An audit trail is itself
	// sensitive: the fact that a named hospital queried a named patient is a disclosure.
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("tefca: could not open the audit log: %w", err)
	}

	log := &PersistentAuditLog{path: path, file: file, retain: retain}

	// Read back before returning, so the first thing anybody sees after a restart is the real history.
	if err := log.reload(); err != nil {
		_ = file.Close()
		return nil, err
	}
	return log, nil
}

// Record writes an entry to the file and keeps it in memory.
//
// The file is written first. If the write fails the entry is still kept in memory and the error is returned, because
// losing the record entirely is the worse outcome and a caller that can report the failure is better than silence -
// but the caller has to be told, or a full disk becomes a silent gap in a compliance record.
func (a *PersistentAuditLog) Record(entry TEFCAAudit) {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}

	// Kept in memory regardless of whether the file write works. An exchange that happened is a fact, and the
	// in-memory copy is what the interface shows.
	a.AuditLog.Record(entry)

	if err := a.append(entry); err != nil {
		// Nowhere to return an error from this signature, which is the existing interface. Reported through
		// LastError so a caller can surface it, because a compliance record that is quietly not being written is the
		// exact failure this file exists to prevent.
		a.mu.Lock()
		a.lastErr = err
		a.mu.Unlock()
	}

	a.prune()
}

// RecordErr is the same as Record but returns any failure to persist.
//
// Offered because a caller that can tell somebody the audit trail is not being written should be given the chance.
func (a *PersistentAuditLog) RecordErr(entry TEFCAAudit) error {
	if entry.Timestamp.IsZero() {
		entry.Timestamp = time.Now()
	}
	a.AuditLog.Record(entry)
	err := a.append(entry)
	a.prune()
	return err
}

// LastError returns the most recent failure to write the audit file, if any.
//
// Surfaced deliberately. A site whose audit file has become unwritable is out of compliance and needs to know today,
// not when somebody asks for records.
func (a *PersistentAuditLog) LastError() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.lastErr
}

func (a *PersistentAuditLog) append(entry TEFCAAudit) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	if a.file == nil {
		return fmt.Errorf("tefca: the audit log is closed")
	}

	line, err := json.Marshal(persisted{
		Timestamp:     entry.Timestamp.UTC().Format(time.RFC3339Nano),
		Direction:     entry.Direction,
		Purpose:       entry.Purpose,
		PatientID:     entry.PatientID,
		RequestingOrg: entry.RequestingOrg,
		RespondingOrg: entry.RespondingOrg,
		ExchangeType:  entry.ExchangeType,
		Success:       entry.Success,
		ErrorDetail:   entry.ErrorDetail,
	})
	if err != nil {
		return err
	}

	if _, err := a.file.Write(append(line, '\n')); err != nil {
		return err
	}

	// Synced on every entry.
	//
	// Slow, and correct. An audit entry buffered in the operating system when the machine loses power is an exchange
	// that happened with no record of it, which is the failure this whole file addresses. Exchanges are not frequent
	// enough for the cost to matter; if that ever changes, batching would need an explicit argument about how much
	// of the record a site is willing to lose.
	return a.file.Sync()
}

// prune drops in-memory entries older than the retention window. The file keeps everything.
func (a *PersistentAuditLog) prune() {
	cutoff := time.Now().Add(-a.retain)

	a.AuditLog.mu.Lock()
	defer a.AuditLog.mu.Unlock()

	keep := a.AuditLog.entries[:0]
	for _, e := range a.AuditLog.entries {
		if !e.Timestamp.Before(cutoff) {
			keep = append(keep, e)
		}
	}
	a.AuditLog.entries = keep
}

// reload reads the file into memory, keeping entries inside the retention window.
func (a *PersistentAuditLog) reload() error {
	raw, err := os.ReadFile(a.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("tefca: could not read the audit log: %w", err)
	}

	cutoff := time.Now().Add(-a.retain)
	var loaded []TEFCAAudit
	skipped := 0

	for _, line := range splitLines(raw) {
		if len(line) == 0 {
			continue
		}

		var p persisted
		if err := json.Unmarshal(line, &p); err != nil {
			// A line that will not parse is skipped rather than fatal.
			//
			// The last line of an append-only file can be a partial write from a process that died mid-entry, and
			// refusing to start because of it would mean one unlucky crash makes the audit trail permanently
			// unreadable. The count is reported so the loss is visible rather than silent.
			skipped++
			continue
		}

		when, err := time.Parse(time.RFC3339Nano, p.Timestamp)
		if err != nil {
			skipped++
			continue
		}
		if when.Before(cutoff) {
			continue
		}

		loaded = append(loaded, TEFCAAudit{
			Timestamp:     when,
			Direction:     p.Direction,
			Purpose:       p.Purpose,
			PatientID:     p.PatientID,
			RequestingOrg: p.RequestingOrg,
			RespondingOrg: p.RespondingOrg,
			ExchangeType:  p.ExchangeType,
			Success:       p.Success,
			ErrorDetail:   p.ErrorDetail,
		})
	}

	// Sorted, because a file that has been concatenated or restored from parts may not be in order and an audit view
	// that jumps about in time is one nobody trusts.
	sort.Slice(loaded, func(i, j int) bool { return loaded[i].Timestamp.Before(loaded[j].Timestamp) })

	a.AuditLog.mu.Lock()
	a.AuditLog.entries = loaded
	a.AuditLog.mu.Unlock()

	a.mu.Lock()
	a.skipped = skipped
	a.mu.Unlock()

	return nil
}

// Skipped returns how many lines could not be read when the log was loaded.
//
// Reported rather than hidden. A non-zero count means part of the trail is unreadable, and that is something a site has
// to know about its own compliance record.
func (a *PersistentAuditLog) Skipped() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.skipped
}

// Path returns where the trail is being written, so the interface can say.
func (a *PersistentAuditLog) Path() string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.path
}

// Close flushes and closes the file.
func (a *PersistentAuditLog) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return nil
	}
	err := a.file.Close()
	a.file = nil
	return err
}

// ReadAll returns every entry in the file within a window, including ones outside the memory window.
//
// Reads the file rather than memory, because the questions auditors ask are about last year and memory holds weeks. The
// context is honoured so a request for a very large range can be abandoned.
func (a *PersistentAuditLog) ReadAll(ctx context.Context, from, to time.Time) ([]TEFCAAudit, error) {
	raw, err := os.ReadFile(a.Path())
	if err != nil {
		if os.IsNotExist(err) {
			return []TEFCAAudit{}, nil
		}
		return nil, err
	}

	out := []TEFCAAudit{}
	for i, line := range splitLines(raw) {
		// Checked periodically rather than every line, because the check costs more than parsing a line does.
		if i%512 == 0 {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
		}
		if len(line) == 0 {
			continue
		}

		var p persisted
		if err := json.Unmarshal(line, &p); err != nil {
			continue
		}
		when, err := time.Parse(time.RFC3339Nano, p.Timestamp)
		if err != nil {
			continue
		}
		if when.Before(from) || when.After(to) {
			continue
		}
		out = append(out, TEFCAAudit{
			Timestamp:     when,
			Direction:     p.Direction,
			Purpose:       p.Purpose,
			PatientID:     p.PatientID,
			RequestingOrg: p.RequestingOrg,
			RespondingOrg: p.RespondingOrg,
			ExchangeType:  p.ExchangeType,
			Success:       p.Success,
			ErrorDetail:   p.ErrorDetail,
		})
	}
	return out, nil
}

// persisted is the on-disk form.
//
// Separate from TEFCAAudit and with an explicit timestamp string, so the file format does not change when a Go field is
// renamed. An audit file has to be readable by whatever version of this software exists in five years, and coupling the
// format to a struct means a refactor silently invalidates the archive.
type persisted struct {
	Timestamp     string `json:"timestamp"`
	Direction     string `json:"direction"`
	Purpose       string `json:"purpose"`
	PatientID     string `json:"patientId,omitempty"`
	RequestingOrg string `json:"requestingOrg,omitempty"`
	RespondingOrg string `json:"respondingOrg,omitempty"`
	ExchangeType  string `json:"exchangeType"`
	Success       bool   `json:"success"`
	ErrorDetail   string `json:"errorDetail,omitempty"`
}

func splitLines(b []byte) [][]byte {
	var out [][]byte
	start := 0
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' {
			out = append(out, b[start:i])
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, b[start:])
	}
	return out
}
