package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/cda"
	"github.com/biodream-llc/perfuse/internal/config"
)

// CDASender extracts the clinical document from an HL7 v2 message, converts it,
// and posts or writes the result.
//
// A document arriving inside an MDM^T02 is the normal case, not an edge case, and
// an engine that cannot see inside that message leaves the most clinically dense
// payload in the feed unread.
type CDASender struct {
	cfg    config.CDADestination
	name   string
	log    *slog.Logger
	client *http.Client
	url    string
	dir    string

	mu    sync.Mutex
	files map[string]*os.File

	extracted    atomic.Int64
	noDocument   atomic.Int64
	notXML       atomic.Int64
	converted    atomic.Int64
	convertFails atomic.Int64
	disagreed    atomic.Int64
	sent         atomic.Int64
}

// NewCDASender builds a sender for a cda destination.
func NewCDASender(d config.Destination, log *slog.Logger) (*CDASender, error) {
	if d.CDA == nil {
		return nil, fmt.Errorf("destination %q has type cda but no cda block", d.Name)
	}
	if log == nil {
		log = slog.Default()
	}

	cfg := *d.CDA
	if cfg.URL == "" && cfg.Dir == "" {
		return nil, fmt.Errorf("destination %q: a cda destination needs either url or dir", d.Name)
	}
	switch cfg.WriteMode() {
	case "fhir", "document", "both":
	default:
		return nil, fmt.Errorf("destination %q: write must be fhir, document or both, not %q",
			d.Name, cfg.Write)
	}
	switch cfg.OnNoDocument {
	case "", "skip", "fail":
	default:
		return nil, fmt.Errorf("destination %q: on_no_document must be skip or fail, not %q",
			d.Name, cfg.OnNoDocument)
	}
	if cfg.URL == "" && cfg.WritesFHIR() && cfg.Dir == "" {
		return nil, fmt.Errorf("destination %q: converting to FHIR needs a url or a dir", d.Name)
	}
	if cfg.Dir != "" {
		if err := os.MkdirAll(cfg.Dir, 0o750); err != nil {
			return nil, fmt.Errorf("destination %q: %w", d.Name, err)
		}
	}

	return &CDASender{
		cfg:  cfg,
		name: d.Name,
		log:  log.With("destination", d.Name),
		url:  strings.TrimRight(cfg.URL, "/"),
		dir:  cfg.Dir,
		client: &http.Client{
			Timeout: d.Timeout,
			// A redirect on a write would repost a clinical document somewhere the
			// configuration never named.
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return fmt.Errorf("refusing to follow a redirect to %s", req.URL.Host)
			},
		},
		files: map[string]*os.File{},
	}, nil
}

// CDAStats reports what this sender has done.
type CDAStats struct {
	Extracted    int64 `json:"extracted"`
	NoDocument   int64 `json:"noDocument"`
	NotXML       int64 `json:"notXml"`
	Converted    int64 `json:"converted"`
	ConvertFails int64 `json:"conversionFailed"`
	Disagreed    int64 `json:"disagreed"`
	Sent         int64 `json:"sent"`
}

// Stats returns a snapshot.
func (s *CDASender) Stats() CDAStats {
	return CDAStats{
		Extracted:    s.extracted.Load(),
		NoDocument:   s.noDocument.Load(),
		NotXML:       s.notXML.Load(),
		Converted:    s.converted.Load(),
		ConvertFails: s.convertFails.Load(),
		Disagreed:    s.disagreed.Load(),
		Sent:         s.sent.Load(),
	}
}

// Describe identifies the destination in logs.
func (s *CDASender) Describe() string {
	target := s.url
	if target == "" {
		target = s.dir
	}
	return fmt.Sprintf("cda %s -> %s", s.cfg.WriteMode(), target)
}

// Send extracts, converts and delivers.
func (s *CDASender) Send(ctx context.Context, raw []byte) error {
	m, err := hl7.Parse(raw)
	if err != nil {
		return fmt.Errorf("the message could not be parsed as HL7 v2: %w", err)
	}

	embedded, err := cda.ExtractFromV2(m)
	if err != nil {
		return fmt.Errorf("extracting the document: %w", err)
	}

	// Every note from extraction is logged. A payload declared as plain text that
	// turned out to be base64 still decoded, and the sender should hear about the
	// disagreement even though nothing failed.
	for _, e := range embedded {
		for _, note := range e.Notes {
			s.log.Warn("document extraction note",
				"obx", e.Segment, "detail", note, "control_id", m.ControlID())
		}
	}

	var chosen *cda.Embedded
	for i := range embedded {
		if isProbablyXML(embedded[i].Data) {
			chosen = &embedded[i]
			break
		}
	}

	if chosen == nil {
		// Distinguish "carried nothing" from "carried a PDF". Both mean there is
		// no CDA to convert, but only the second says the sender is doing
		// something we could support later.
		if len(embedded) > 0 {
			s.notXML.Add(1)
			s.log.Info("the attachment is not a clinical document",
				"attachments", len(embedded), "mime", embedded[0].MimeType,
				"control_id", m.ControlID())
		} else {
			s.noDocument.Add(1)
		}
		if s.cfg.FailsWithoutDocument() {
			return fmt.Errorf("the message carries no clinical document")
		}
		return nil
	}
	s.extracted.Add(1)

	doc, err := cda.Parse(chosen.Data)
	if err != nil {
		s.convertFails.Add(1)
		return fmt.Errorf("the document could not be read: %w", err)
	}

	if report := cda.CheckAgreement(doc); report.Errors > 0 {
		s.disagreed.Add(1)
		// Always logged, gated only on whether it stops delivery. A contradiction
		// between what a clinician reads and what a system imports is worth
		// knowing about even at a site that has decided to accept it.
		for _, f := range report.Findings {
			if f.Severity != "error" {
				continue
			}
			s.log.Warn("the narrative and the coded entries disagree",
				"section", f.Section, "kind", f.Kind, "detail", f.Message,
				"control_id", m.ControlID())
		}
		if s.cfg.RequireAgreement {
			return fmt.Errorf("the document's narrative and coded entries disagree: %s",
				report.Findings[0].Message)
		}
	}

	base := documentFilename(doc, m)

	if s.cfg.WritesDocument() {
		if err := s.emitDocument(ctx, base, chosen.Data); err != nil {
			return err
		}
	}

	if s.cfg.WritesFHIR() {
		result, err := doc.ToFHIR(cda.FHIROptions{
			Version:           s.cfg.Version,
			IdentifierSystems: s.cfg.IdentifierSystems,
			Original:          chosen.Data,
		})
		if err != nil {
			s.convertFails.Add(1)
			return fmt.Errorf("converting the document to FHIR: %w", err)
		}
		s.converted.Add(1)

		for _, note := range result.Notes {
			if note.Severity == "info" {
				continue
			}
			s.log.Warn("conversion note",
				"severity", note.Severity, "path", note.Path,
				"detail", note.Message, "control_id", m.ControlID())
		}

		body, err := json.Marshal(result.Bundle)
		if err != nil {
			s.convertFails.Add(1)
			return fmt.Errorf("serialising the bundle: %w", err)
		}
		if err := s.emitBundle(ctx, base, body); err != nil {
			return err
		}
		s.log.Info("converted a clinical document",
			"document", doc.DocumentType, "sections", len(doc.Sections),
			"resources", result.Counts, "control_id", m.ControlID())
	}

	s.sent.Add(1)
	return nil
}

func (s *CDASender) emitDocument(ctx context.Context, base string, data []byte) error {
	if s.dir == "" {
		// Posting the original document requires somewhere to put it that is not a
		// FHIR endpoint. Refuse rather than quietly dropping it.
		return fmt.Errorf("write includes the document but no dir is configured")
	}
	return s.writeFile(base+".xml", data)
}

func (s *CDASender) emitBundle(ctx context.Context, base string, body []byte) error {
	if s.url != "" {
		return s.post(ctx, body)
	}
	return s.writeFile(base+".json", body)
}

// writeFile writes one file per document, unlike the HL7 file sender which
// appends. A document is a record in its own right and a records team asked for
// files, not one growing file they have to split.
func (s *CDASender) writeFile(name string, data []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	path := filepath.Join(s.dir, name)
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if !os.IsExist(err) {
			return err
		}
		// A document with the same identifier arriving twice is normal: a
		// replacement carries the same set. Add the timestamp rather than
		// overwriting, because overwriting destroys the earlier version.
		path = filepath.Join(s.dir, fmt.Sprintf("%s-%d%s",
			strings.TrimSuffix(name, filepath.Ext(name)),
			time.Now().UnixNano(), filepath.Ext(name)))
		f, err = os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
	}
	defer f.Close()

	if _, err := f.Write(data); err != nil {
		return err
	}
	// Sync before reporting success, so "delivered" does not mean "reached a
	// buffer a power cut discards".
	return f.Sync()
}

func (s *CDASender) post(ctx context.Context, body []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/fhir+json")
	req.Header.Set("Accept", "application/fhir+json")
	for k, v := range s.cfg.Headers {
		req.Header.Set(k, v)
	}
	if s.cfg.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+s.cfg.BearerToken)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("posting to %s: %w", s.url, err)
	}
	defer resp.Body.Close()

	// Read a bounded amount of the response. A FHIR server explaining a rejection
	// in an OperationOutcome is the most useful thing in the log, and an
	// unbounded read is how a hostile or broken server exhausts memory.
	detail, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<10))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("%s rejected the bundle: %s: %s",
			s.url, resp.Status, strings.TrimSpace(string(detail)))
	}
	return nil
}

// Close releases open files.
func (s *CDASender) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var first error
	for _, f := range s.files {
		if err := f.Close(); err != nil && first == nil {
			first = err
		}
	}
	s.files = map[string]*os.File{}
	return first
}

// isProbablyXML reports whether a payload looks like a clinical document rather
// than a PDF or an image. The check is on the bytes because senders disagree
// about the declared MIME type, and a document labelled octet-stream is still a
// document.
func isProbablyXML(data []byte) bool {
	trimmed := bytes.TrimLeft(data, " \t\r\n\uFEFF")
	return len(trimmed) > 0 && trimmed[0] == '<'
}

// documentFilename names the output after the document, not the message, so a
// records team can find one by its identifier. Every component is sanitised
// because both come from outside.
func documentFilename(doc *cda.Document, m *hl7.Message) string {
	id := strings.TrimSpace(doc.ID)
	if id == "" {
		if info, ok := cda.InfoFromV2(m); ok {
			id = strings.TrimSpace(info.UniqueID)
		}
	}
	if id == "" {
		id = strings.TrimSpace(m.ControlID())
	}
	if id == "" {
		id = fmt.Sprintf("document-%d", time.Now().UnixNano())
	}
	return sanitise(id)
}
