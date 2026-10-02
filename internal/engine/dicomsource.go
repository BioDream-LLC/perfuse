package engine

import (
	"context"
	"fmt"

	"github.com/biodream-llc/perfuse/internal/config"

	"github.com/biodream-llc/perfuse/internal/dicom"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// startDICOMSource listens for imaging objects.
func (c *Channel) startDICOMSource() error {
	src := c.cfg.Source.DICOM
	if src == nil {
		return fmt.Errorf("channel %q: source type dicom but no dicom block", c.cfg.Name)
	}

	for _, w := range src.Warnings() {
		// Warned at every start rather than once at load, exactly like the HTTP source. An unauthenticated imaging
		// endpoint is the kind of thing set up for a test and then forgotten, and this one carries patient names.
		c.log.Warn("dicom source", "detail", w)
	}

	srv := &dicom.Server{
		AETitle:          src.AETitle,
		AllowedCallingAE: src.AllowedCallingAE,
		SOPClasses:       src.SOPClasses,
		TransferSyntaxes: src.TransferSyntaxes,
		MaxObjectBytes:   src.MaxObjectBytes,
		IdleTimeout:      src.IdleTimeout,
		Log:              c.log,
		Handler:          c.handleDICOMObject,
	}

	if src.TLS != nil {
		tlsCfg, err := tlsconf.ForListener(src.TLS)
		if err != nil {
			return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
		}
		srv.TLS = tlsCfg
		for _, w := range src.TLS.Warnings(true) {
			c.log.Warn("tls", "detail", w)
		}
	}

	if err := srv.Listen(src.Listen); err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}

	c.dicomServer = srv
	c.log.Info("dicom listening",
		"addr", srv.Addr(), "ae_title", src.AETitle, "tls", src.TLS != nil)

	return nil
}

// handleDICOMObject is called for each received imaging object.
//
// The raw object goes through the channel unchanged. Its metadata is read for logging and for the trace, because "which
// study was that" is the first question during an investigation and an operator cannot answer it from a byte count.
func (c *Channel) handleDICOMObject(ds *dicom.DataSet, raw []byte) error {
	c.log.Debug("received an imaging object",
		"modality", ds.Text(dicom.TagModality),
		"accession", ds.Text(dicom.TagAccessionNumber),
		"sop_instance", ds.Text(dicom.TagSOPInstanceUID),
		"bytes", len(raw))

	// The bytes arrive already wrapped into a file representation carrying the negotiated transfer syntax, so everything
	// downstream can read them without being told anything.
	if _, err := c.handleDICOMParsed(context.Background(), raw, ds); err != nil {
		// Wrapped as retryable so the sender is told to try again rather than told the object is unreadable. A delivery
		// failure downstream is this end's problem, and a modality that gives up on a transient fault leaves a study
		// stranded with nobody watching.
		return &dicom.Retryable{Err: err}
	}

	return nil
}

// stopDICOMSource shuts the listener down.
func (c *Channel) stopDICOMSource(ctx context.Context) error {
	if c.dicomServer == nil {
		return nil
	}
	err := c.dicomServer.Close()
	c.dicomServer = nil
	_ = ctx
	return err
}

// DICOMSender stores objects to a remote archive.
type DICOMSender struct {
	client *dicom.Client
	name   string
	addr   string
}

// NewDICOMSender builds the sender.
func NewDICOMSender(d config.Destination) (*DICOMSender, error) {
	if d.DICOM == nil {
		return nil, fmt.Errorf("destination %q is a dicom destination with no dicom block", d.Name)
	}

	client := &dicom.Client{
		Addr:             d.DICOM.Address,
		CalledAE:         d.DICOM.CalledAE,
		CallingAE:        d.DICOM.CallingAE,
		TransferSyntaxes: d.DICOM.TransferSyntaxes,
		Timeout:          d.DICOM.Timeout,
	}

	if d.DICOM.TLS != nil {
		tlsCfg, err := tlsconf.ForSender(d.DICOM.TLS)
		if err != nil {
			return nil, err
		}
		client.TLS = tlsCfg
	}

	return &DICOMSender{client: client, name: d.Name, addr: d.DICOM.Address}, nil
}

// Send stores one object.
func (s *DICOMSender) Send(ctx context.Context, msg []byte) error {
	return s.client.Send(ctx, msg)
}

// Describe names the destination, never the credentials.
func (s *DICOMSender) Describe() string {
	if s.client.CalledAE != "" {
		return fmt.Sprintf("stored to %s as %s (DICOM C-STORE)", s.addr, s.client.CalledAE)
	}
	return fmt.Sprintf("stored to %s (DICOM C-STORE)", s.addr)
}

// Close releases nothing: an association is opened per operation, deliberately, because a half-broken association is
// worse than none and a fresh one cannot be poisoned by the last failure.
func (s *DICOMSender) Close() error { return nil }
