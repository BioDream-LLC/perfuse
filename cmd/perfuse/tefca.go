package main

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/biodream-llc/perfuse/internal/api"
	"github.com/biodream-llc/perfuse/internal/settings"
	"github.com/biodream-llc/perfuse/internal/tefca"
)

// Turning the TEFCA settings into a participant, or explaining why not.
//
// # Partly configured is the state that wastes time
//
// Somebody fills in the organisation name, means to come back for the OID, and never does. Nothing complains. The
// section looks configured, the exchange fails at the first attempt with a message from a partner about an unknown
// participant, and the connection between the two is not obvious from either end.
//
// So this is deliberate about three states rather than two. Nothing filled in is normal and silent - most instances do
// not participate. Everything filled in produces a participant. Something filled in produces a warning at startup that
// names what is missing, and no participant, because a half-configured participant is worse than none: it would exchange
// under an identity nobody agreed.
func buildTEFCA(store *settings.Store, dbPath string, log *slog.Logger) (
	*tefca.Participant, *tefca.PersistentAuditLog, error) {

	cfg := tefca.TEFCAConfig{
		OrganizationName:  strings.TrimSpace(store.String("tefca.organisationName")),
		OrganizationOID:   strings.TrimSpace(strings.TrimPrefix(store.String("tefca.organisationOID"), "urn:oid:")),
		QHINEndpoint:      strings.TrimSpace(store.String("tefca.qhinEndpoint")),
		ParticipantType:   strings.TrimSpace(store.String("tefca.participantType")),
		CertificatePath:   strings.TrimSpace(store.String("tefca.certificatePath")),
		KeyPath:           strings.TrimSpace(store.String("tefca.keyPath")),
		SupportedPurposes: trimAll(store.List("tefca.purposes")),
	}

	// The participant type has a default, so its presence says nothing about whether anybody intended to configure
	// this. The four fields with no sensible default are what decide.
	intended := cfg.OrganizationName != "" || cfg.OrganizationOID != "" ||
		cfg.QHINEndpoint != "" || cfg.CertificatePath != ""

	if !intended {
		return nil, nil, nil
	}

	missing := []string{}
	if cfg.OrganizationName == "" {
		missing = append(missing, "the organisation name")
	}
	if cfg.OrganizationOID == "" {
		missing = append(missing, "the organisation identifier")
	}
	if cfg.QHINEndpoint == "" {
		missing = append(missing, "the QHIN endpoint")
	}
	if cfg.CertificatePath == "" {
		missing = append(missing, "the certificate file")
	}
	if cfg.KeyPath == "" {
		missing = append(missing, "the private key file")
	}
	if len(cfg.SupportedPurposes) == 0 {
		missing = append(missing, "at least one purpose of use")
	}

	if len(missing) > 0 {
		// Warned rather than fatal. Refusing to start would take a working integration engine offline over a feature
		// nobody may be using yet, and somebody half-way through filling in a form should not lose their server.
		//
		// Named individually, because "invalid TEFCA configuration" sends somebody to read all seven fields.
		log.Warn("TEFCA participation is partly configured and has been left off",
			"missing", strings.Join(missing, ", "),
			"why", "a participant with an incomplete identity would exchange under an identity nobody agreed, so "+
				"nothing is exchanged until it is complete")
		return nil, nil, nil
	}

	auditPath := strings.TrimSpace(store.String("tefca.auditPath"))
	if auditPath == "" {
		// Beside the database, mirroring where the settings and peers files go. A default that lands somewhere
		// predictable is a default somebody can find without being told.
		auditPath = filepath.Join(filepath.Dir(dbPath), "perfuse-tefca-audit.jsonl")
	}

	audit, err := tefca.OpenAuditLog(auditPath, 0)
	if err != nil {
		// Fatal, and this one deserves to be. A participant that cannot write its audit trail must not exchange at
		// all: recording every exchange is a condition of taking part, and starting anyway would mean disclosing
		// patient data with no record, which is the failure the whole audit apparatus exists to prevent.
		return nil, nil, fmt.Errorf("TEFCA is configured but its audit trail cannot be opened at %s: %w. "+
			"Every exchange must be recorded, so nothing will be exchanged until this is writable", auditPath, err)
	}

	participant, err := tefca.NewParticipant(cfg, &audit.AuditLog)
	if err != nil {
		_ = audit.Close()
		return nil, nil, fmt.Errorf("TEFCA participation could not be set up: %w", err)
	}

	log.Info("participating in TEFCA",
		"organisation", cfg.OrganizationName,
		"oid", cfg.OrganizationOID,
		"type", cfg.ParticipantType,
		"purposes", strings.Join(cfg.SupportedPurposes, ", "),
		"audit", auditPath)

	if n := audit.Skipped(); n > 0 {
		log.Warn("part of the existing TEFCA audit trail could not be read",
			"lines", n,
			"why", "most likely a partial write from a process that stopped mid-entry; those exchanges are no "+
				"longer accounted for")
	}

	return participant, audit, nil
}

// attachTEFCA puts the participant on the server, or leaves it absent.
func attachTEFCA(srv *api.Server, p *tefca.Participant, audit *tefca.PersistentAuditLog) {
	srv.TEFCA = p
	srv.TEFCAAudit = audit
}

func trimAll(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
