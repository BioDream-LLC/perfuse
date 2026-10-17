package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/mpi"
)

// PatientIndex is the server's patient index, set at start-up; nil when the server runs without one.
var PatientIndex *mpi.Store

// MPISender registers each message's patient in the patient index.
type MPISender struct {
	cfg     config.MPIDestination
	name    string
	channel string
	tenant  string
}

// SetChannel names the channel the records come from, which is what the index shows as their source.
func (s *MPISender) SetChannel(name string) { s.channel = name }

// NewMPISender builds the sender. The index itself is the server's, so a channel can be loaded where there is none; it
// fails on the first message instead, saying why.
func NewMPISender(d config.Destination) (*MPISender, error) {
	s := &MPISender{name: d.Name}
	if d.MPI != nil {
		s.cfg = *d.MPI
	}
	return s, nil
}

// SetTenant scopes the registrations to the channel's tenant.
func (s *MPISender) SetTenant(id string) { s.tenant = id }

func (s *MPISender) tenantID() string { return mpi.TenantOf(s.tenant) }

// Send registers the patient, and applies a merge the message asks for.
func (s *MPISender) Send(ctx context.Context, msg []byte) error {
	if PatientIndex == nil {
		return errors.New("mpi: this server runs without a patient index (serve -mpi)")
	}
	var e *mpi.Extracted
	var err error
	if t := bytes.TrimSpace(msg); len(t) > 0 && t[0] == '{' {
		e, err = mpi.FromFHIR(t)
	} else {
		e, err = mpi.FromHL7(msg, mpi.FromOptions{Authorities: s.cfg.Authorities, IDType: s.cfg.IDType, SSNSystem: s.cfg.SSNSystem})
	}
	if err != nil {
		return err
	}
	e.Record.Source = s.name
	if s.channel != "" {
		e.Record.Source = s.channel
	}
	actor := "channel:" + e.Record.Source
	if _, err := PatientIndex.RegisterWithSettings(ctx, s.tenantID(), actor, e.Record); err != nil {
		return err
	}
	if e.MergeFrom != "" && !s.cfg.NoMerge {
		if _, err := PatientIndex.MergeIdentifiers(ctx, s.tenantID(), actor, e.MergeFrom, e.Record.Key(),
			"merge from "+e.MergeFrom); err != nil {
			return fmt.Errorf("mpi: the merge could not be applied: %w", err)
		}
	}
	return nil
}

func (s *MPISender) Describe() string { return "the patient index" }
func (s *MPISender) Close() error     { return nil }
