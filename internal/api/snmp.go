package api

import (
	"net/http"

	"github.com/biodream-llc/perfuse/internal/snmp"
	"github.com/biodream-llc/perfuse/internal/store"
)

// handleSNMPStatus says whether the SNMP agent is running, where, and how traps have gone.
func (s *Server) handleSNMPStatus(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	writeJSON(w, http.StatusOK, s.SNMP.Status())
}

// handleSNMPMIB serves the MIB for the base OID in use, for a monitoring tool to load.
func (s *Server) handleSNMPMIB(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="PERFUSE-MIB.txt"`)
	_, _ = w.Write([]byte(snmp.MIB(s.SNMP.Status().Base)))
}
