package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/pdf"
)

// DocumentSender renders each message into a document a person reads.
type DocumentSender struct {
	cfg  *config.DocumentDestination
	name string
}

// NewDocumentSender builds the sender.
func NewDocumentSender(d config.Destination) (*DocumentSender, error) {
	if d.Document == nil {
		return nil, fmt.Errorf("destination %q is a document destination with no document block", d.Name)
	}
	return &DocumentSender{cfg: d.Document, name: d.Name}, nil
}

// Send renders and writes one document.
func (s *DocumentSender) Send(_ context.Context, msg []byte) error {
	parsed, err := hl7.Parse(msg)
	if err != nil {
		return fmt.Errorf("rendering a document needs the message parsed, and a transformation produced "+
			"something invalid: %w", err)
	}

	body, missing := renderTemplate(s.cfg.Template, parsed)

	// A missing field is reported rather than left as an empty gap. A discharge summary with a blank where the
	// patient's name should be is a document somebody will print, sign and file, and nothing about it says it is
	// incomplete.
	if len(missing) > 0 {
		return fmt.Errorf("the template refers to %s, which this message does not carry; a document with a "+
			"silent gap in it would be printed and filed as if it were complete",
			strings.Join(missing, ", "))
	}

	name, err := s.fileName(parsed)
	if err != nil {
		return err
	}

	var out []byte
	switch s.cfg.Format {
	case config.DocumentText:
		out = []byte(body)
	default:
		out, err = pdf.Render(body, pdf.Options{
			Title:     s.title(parsed),
			FontSize:  s.cfg.FontSize,
			Landscape: s.cfg.Landscape,
		})
		if err != nil {
			return err
		}
	}

	if err := os.MkdirAll(s.cfg.Dir, 0o750); err != nil {
		return err
	}

	final := filepath.Join(s.cfg.Dir, name)
	temp := final + s.cfg.TempSuffix

	if err := os.WriteFile(temp, out, 0o640); err != nil {
		return err
	}

	// Renamed into place, which matters more here than for other file destinations: a print watcher picking up a
	// half-written PDF produces a page of nothing, and nobody investigates a blank page.
	if err := os.Rename(temp, final); err != nil {
		// The partial file is removed, or the directory accumulates .part files that a watcher may eventually be
		// reconfigured to collect.
		_ = os.Remove(temp)
		return err
	}

	return nil
}

// Describe names the destination.
func (s *DocumentSender) Describe() string {
	format := "PDF"
	if s.cfg.Format == config.DocumentText {
		format = "text"
	}
	return fmt.Sprintf("rendered as %s into %s", format, s.cfg.Dir)
}

// Close releases nothing.
func (s *DocumentSender) Close() error { return nil }

func (s *DocumentSender) title(msg *hl7.Message) string {
	if s.cfg.Title == "" {
		return s.name
	}
	rendered, _ := renderTemplate(s.cfg.Title, msg)
	return rendered
}

// renderTemplate substitutes ${path} references from the message.
//
// Returns the paths that had no value, so the caller can refuse rather than deliver a document with a gap. The
// same placeholder syntax as every file name in this engine, because a second syntax for substitution would mean
// somebody writes one and gets the other's literal text.
func renderTemplate(template string, msg *hl7.Message) (string, []string) {
	var missing []string
	var b strings.Builder

	rest := template
	for {
		start := strings.Index(rest, "${")
		if start < 0 {
			b.WriteString(rest)
			break
		}
		end := strings.Index(rest[start:], "}")
		if end < 0 {
			// An unterminated placeholder is written literally rather than swallowing the remainder of the
			// document, which is what a naive parser would do and would produce a mysteriously short page.
			b.WriteString(rest)
			break
		}

		b.WriteString(rest[:start])
		path := strings.TrimSpace(rest[start+2 : start+end])
		rest = rest[start+end+1:]

		switch path {
		case "now":
			b.WriteString(time.Now().Format("2006-01-02 15:04"))
			continue
		case "today":
			b.WriteString(time.Now().Format("2006-01-02"))
			continue
		}

		value := msg.MustGet(path)
		if value == "" {
			missing = append(missing, path)
			continue
		}
		b.WriteString(value)
	}

	return b.String(), missing
}

// fileName renders the document's name.
func (s *DocumentSender) fileName(msg *hl7.Message) (string, error) {
	tmpl := s.cfg.FileName
	if tmpl == "" {
		tmpl = "${timestamp}-${control_id}"
	}

	controlID := ""
	msgType := ""
	if msh, ok := msg.Segment("MSH", 1); ok {
		controlID = msh.Field(10).String()
		msgType = strings.ReplaceAll(msh.Field(9).String(), "^", "_")
	}
	if controlID == "" {
		controlID = fmt.Sprintf("noid-%d", time.Now().UnixNano())
	}

	now := time.Now().UTC()
	out := tmpl
	out = strings.ReplaceAll(out, "${timestamp}", now.Format("20060102150405"))
	out = strings.ReplaceAll(out, "${date}", now.Format("20060102"))
	out = strings.ReplaceAll(out, "${control_id}", controlID)
	out = strings.ReplaceAll(out, "${message_type}", msgType)
	out = strings.ReplaceAll(out, "${channel}", s.name)

	out = sanitiseFileName(out)

	// The extension is added rather than expected in the template, so a template written for PDF and switched to
	// text does not produce a .pdf file full of plain text - which would open in a reader as a corrupt document.
	ext := ".pdf"
	if s.cfg.Format == config.DocumentText {
		ext = ".txt"
	}
	if !strings.HasSuffix(strings.ToLower(out), ext) {
		out += ext
	}

	return out, nil
}
