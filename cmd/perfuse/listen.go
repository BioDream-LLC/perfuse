package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/mllp"
)

func cmdListen(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("listen", flag.ContinueOnError)
	fset.SetOutput(stderr)
	addr := fset.String("addr", ":6661", "listen address")
	app := fset.String("app", "PERFUSE", "application name used in acknowledgements (MSH-3)")
	facility := fset.String("facility", "", "facility name used in acknowledgements (MSH-4)")
	writeDir := fset.String("write-dir", "", "append received messages to files in this directory")
	mirror := fset.Bool("mirror-sender", false, "identify as the original receiver in acknowledgements instead of -app")
	maxSize := fset.Int("max-size", 0, "maximum inbound message size in bytes (0 for the default 16 MiB)")
	idle := fset.Duration("idle-timeout", 0, "close a connection idle for this long (0 for never)")
	maxConns := fset.Int("max-connections", 0, "maximum concurrent connections (0 for unlimited)")
	quiet := fset.Bool("quiet", false, "log only errors")
	if err := fset.Parse(args); err != nil {
		return err
	}

	level := slog.LevelInfo
	if *quiet {
		level = slog.LevelError
	}
	log := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: level}))

	// MLLP has no authentication and no encryption. Saying so at startup is
	// more useful than a line in the documentation nobody reads.
	log.Warn("MLLP is unauthenticated and unencrypted; restrict access at the network layer",
		"addr", *addr)

	var sink *messageSink
	if *writeDir != "" {
		if err := os.MkdirAll(*writeDir, 0o750); err != nil {
			return fmt.Errorf("write-dir: %w", err)
		}
		sink = &messageSink{dir: *writeDir}
		log.Warn("received messages will be written to disk; these may contain PHI",
			"dir", *writeDir)
	}

	var received, rejected atomic.Int64

	srv := &mllp.Server{
		Addr:           *addr,
		MaxMessageSize: *maxSize,
		IdleTimeout:    *idle,
		MaxConnections: *maxConns,
		Logger:         log,
		Handler: mllp.HandlerFunc(func(ctx context.Context, raw []byte) ([]byte, error) {
			m, err := hl7.Parse(raw)
			if err != nil {
				// A sender that transmits something unparseable still needs an
				// answer. Silence makes it retry for ever.
				rejected.Add(1)
				log.Warn("rejected unparseable message", "err", err, "bytes", len(raw))
				return hl7.AckFor(err, hl7.AckOptions{
					SendingApplication: *app,
					SendingFacility:    *facility,
				}), nil
			}

			typ, event, _ := m.Type()
			log.Info("received",
				"type", typ, "event", event,
				"control_id", m.ControlID(),
				"segments", m.SegmentCount(),
				"bytes", len(raw))

			if sink != nil {
				if err := sink.write(m, raw); err != nil {
					// Storage failed, so do not claim the message was accepted.
					log.Error("could not store message", "err", err)
					return m.Ack(hl7.AckOptions{
						Code: hl7.AckError,
						Text: "message could not be stored",
					}), nil
				}
			}

			received.Add(1)
			opts := hl7.AckOptions{Code: hl7.AckAccept}
			if !*mirror {
				opts.SendingApplication = *app
				opts.SendingFacility = *facility
			}
			return m.Ack(opts), nil
		}),
	}

	ctx, stop := shutdownContext()
	defer stop()

	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()

	select {
	case err := <-errc:
		if err != nil && !errors.Is(err, mllp.ErrNoHandler) {
			return err
		}
		return err

	case <-ctx.Done():
		log.Info("shutting down, waiting for messages in flight")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Warn("shutdown timed out, dropping connections", "err", err)
		}
		fmt.Fprintf(stdout, "%d message(s) accepted, %d rejected\n",
			received.Load(), rejected.Load())
		return nil
	}
}

// messageSink appends received messages to one file per day and message type.
//
// One file per message would be tidier but produces millions of inodes on a busy
// feed, which is a problem people only discover in production.
type messageSink struct {
	dir string
}

func (s *messageSink) write(m *hl7.Message, raw []byte) error {
	typ, event, _ := m.Type()
	if typ == "" {
		typ = "UNKNOWN"
	}
	name := typ
	if event != "" {
		name += "_" + event
	}

	path := filepath.Join(s.dir, fmt.Sprintf("%s-%s.hl7",
		time.Now().UTC().Format("20060102"), sanitise(name)))

	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()

	// Framed on disk so the file can be replayed byte for byte.
	if _, err := f.Write(mllp.Frame(raw)); err != nil {
		return err
	}
	return f.Sync()
}

// sanitise keeps a message type safe to use as a filename. Values come off the
// wire, so a sender could otherwise choose the path.
func sanitise(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s) && i < 32; i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9', c == '_', c == '-':
			out = append(out, c)
		default:
			out = append(out, '_')
		}
	}
	if len(out) == 0 {
		return "UNKNOWN"
	}
	return string(out)
}
