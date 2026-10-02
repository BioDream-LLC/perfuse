package api

import (
	"net/http"

	"github.com/biodream-llc/perfuse/internal/cda"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Repairing a clinical document that is valid and unreadable.
//
// Reachable from the interface deliberately and immediately, because the generator this uses was 854 lines of
// working, tested code that nothing called. Writing a capability and leaving it unreachable is the same as not
// writing it, and this package has now produced that mistake three times.
//
// Viewer, not editor. Nothing is stored and nothing on this server changes; the document is supplied by the
// caller, read, and handed back repaired. The privilege being exercised is reading patient data, which is what
// viewer already means here.
func (s *Server) handleRepairDocument(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var body struct {
		Document string `json:"document"`

		// CustodianName is asked for rather than assumed. C-CDA requires a custodian and the generator refuses
		// without one, and naming the organisation answerable for a clinical document is an assertion about that
		// organisation - not a value a program should pick.
		CustodianName string `json:"custodianName"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	if body.Document == "" {
		s.fail(w, r, http.StatusBadRequest, "supply a clinical document to repair")
		return
	}

	doc, err := cda.Parse([]byte(body.Document))
	if err != nil {
		// A parse failure is the caller's document, not a server fault, and the message from the parser names the
		// element - which is more use than "bad request".
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	report, err := cda.Repair(doc, cda.RepairOptions{
		CustodianName: body.CustodianName,
		// Indented, because the result is going on a screen for somebody to check before they trust it. A repair
		// nobody can read is a repair nobody should accept.
		Indent: true,
	})
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// The report is returned even when nothing could be repaired. "This document will display blank and here is
	// why" is the finding, and a caller who gets only an error learns nothing they can act on.
	s.ok(w, report)
}
