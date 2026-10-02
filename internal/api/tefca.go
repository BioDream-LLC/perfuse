package api

import (
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tefca"
)

// TEFCA participation, reachable at last.
//
// The whole package had no caller anywhere: 346 lines of implementation and 552 of tests that nothing outside it
// imported. Correct code, passing tests, and no way for anybody to use it - the same defect found in the CDA package
// three times over and in the certificate inspector before that. An implementation with no caller is indistinguishable
// from a feature that does not exist.
//
// # What is exposed and what is not
//
// The audit trail, the configuration, and a way to check that a purpose of use would be accepted before an exchange is
// attempted. Not the exchanges themselves, and that is deliberate: a button in a web interface that queries a national
// network for a named patient is a button that discloses patient data on somebody's behalf, and the party whose
// authority is being exercised has to be established first. Perfuse exchanges through channels, where the purpose and
// the requesting party come from the configuration rather than from whoever happens to be signed in.
//
// So this section is for seeing what was exchanged and confirming the participation is configured correctly, which are
// the two things a person actually needs from a screen.

// handleTEFCAStatus reports whether this instance is configured to participate, and how.
func (s *Server) handleTEFCAStatus(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.TEFCA == nil {
		// Not an error. Most instances do not participate in TEFCA, and reporting the absence as a failure would make
		// every ordinary installation look misconfigured.
		s.ok(w, map[string]any{
			"configured": false,
			"explanation": "This instance is not configured as a TEFCA participant. TEFCA is national exchange " +
				"through a Qualified Health Information Network, and taking part needs an agreement with one, an " +
				"organisation identifier, and a certificate issued for the purpose.",
			"purposes": []string{},

			// The purposes are listed even here, because deciding whether to take part means knowing what would be
			// declared, and that question comes before any configuration exists. A section that can only be read
			// about until it is switched on is a section nobody can evaluate.
			"allPurposes": allPurposes(),
		})
		return
	}

	cfg := s.TEFCA.Config()

	// Purposes reported as declared, so somebody can see what this instance would be refused for before a partner
	// refuses it.
	purposes := make([]string, 0, len(cfg.SupportedPurposes))
	purposes = append(purposes, cfg.SupportedPurposes...)
	sort.Strings(purposes)

	out := map[string]any{
		"configured":      true,
		"organisation":    cfg.OrganizationName,
		"oid":             cfg.OrganizationOID,
		"qhinEndpoint":    cfg.QHINEndpoint,
		"participantType": cfg.ParticipantType,
		"purposes":        purposes,

		// Every purpose that exists, so the interface can show which are declared and which are not without
		// hard-coding a list that would drift from the package.
		"allPurposes": allPurposes(),

		// Whether this build can actually carry an exchange.
		//
		// Reported because a configured participant with no transport looks identical to a working one from this screen, and the
		// difference is the whole thing. An operator who believes exchange is running will not go looking for why no records ever
		// arrive - and the audit trail, which is real, would show a run of failures nobody was watching.
		// Two transports, reported separately, because they are at genuinely different stages and one number covering both would be a
		// lie in whichever direction it was rounded.
		//
		// Facilitated FHIR is built. Its security layer - UDAP, a public key infrastructure over OAuth 2.0 - is verified against a live
		// third-party reference server, which refused a registration signed here with "Untrusted: Certificate is not a member of
		// community": the document was parsed, its signature checked and its chain walked before membership was judged. What is not
		// verified, and cannot be from here, is whether a particular QHIN accepts it, because the certificate that would prove membership
		// comes out of onboarding and is not something code produces.
		//
		// The older QHIN-to-QHIN exchange built on the IHE profiles is not implemented at all.
		"facilitatedFHIRImplemented": true,
		"facilitatedFHIRExplanation": "Facilitated FHIR is implemented per the Sequoia Project SOP effective 8 March 2026. The UDAP " +
			"security layer is verified against a live third-party reference server. It has not been exercised against a real QHIN, " +
			"which needs a certificate issued through onboarding.",
		"exchangeImplemented": false,
		"exchangeExplanation": tefca.ErrExchangeNotImplemented.Error(),
	}

	// The state of the trail itself, because a participant whose audit file has stopped being written is out of
	// compliance and nothing else on this screen would say so.
	if s.TEFCAAudit != nil {
		trail := map[string]any{
			"path":     s.TEFCAAudit.Path(),
			"writable": true,
		}
		if err := s.TEFCAAudit.LastError(); err != nil {
			trail["writable"] = false
			trail["problem"] = "The audit trail is not being written: " + err.Error() +
				". Every exchange must be recorded, so this instance is exchanging data without keeping the record " +
				"it is required to keep."
		}
		if n := s.TEFCAAudit.Skipped(); n > 0 {
			trail["unreadableLines"] = n
			trail["unreadableNote"] = "Part of the existing trail could not be read, most likely a partial write " +
				"from a process that stopped mid-entry. Those exchanges are no longer accounted for."
		}
		out["trail"] = trail
	}

	s.ok(w, out)
}

// handleTEFCAAudit returns the audit trail for a window.
//
// Read from the file rather than from memory when a range is asked for, because the questions this answers are about
// last month and memory holds weeks.
func (s *Server) handleTEFCAAudit(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	if s.TEFCAAudit == nil {
		s.ok(w, map[string]any{"entries": []any{}, "configured": false})
		return
	}

	// A window, defaulting to the last week. Unbounded by default would mean the first person to open the page on an
	// instance that has been running for a year waits while a year of exchanges is parsed.
	to := time.Now()
	from := to.Add(-7 * 24 * time.Hour)

	if v := r.URL.Query().Get("from"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest,
				"from must be a timestamp like 2026-08-26T00:00:00Z")
			return
		}
		from = parsed
	}
	if v := r.URL.Query().Get("to"); v != "" {
		parsed, err := time.Parse(time.RFC3339, v)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, "to must be a timestamp like 2026-08-26T00:00:00Z")
			return
		}
		to = parsed
	}

	if to.Before(from) {
		// Named rather than silently swapped. Swapping them would return results for a range nobody asked for, and an
		// audit answer to a question that was not asked is worse than a refusal.
		s.fail(w, r, http.StatusBadRequest, "the end of the range is before its start")
		return
	}

	entries, err := s.TEFCAAudit.ReadAll(r.Context(), from, to)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, "the audit trail could not be read: "+err.Error())
		return
	}

	// Newest first, because somebody opening an audit view is nearly always asking what happened recently.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Timestamp.After(entries[j].Timestamp) })

	// A ceiling on what is returned, with the total reported separately so a truncated answer is visibly truncated.
	// A page that silently shows the first thousand of fifty thousand exchanges is a page that misleads.
	const limit = 1000
	total := len(entries)
	truncated := false
	if total > limit {
		entries = entries[:limit]
		truncated = true
	}

	views := make([]map[string]any, 0, len(entries))
	for _, e := range entries {
		views = append(views, map[string]any{
			"timestamp":     e.Timestamp,
			"direction":     e.Direction,
			"purpose":       e.Purpose,
			"patientId":     e.PatientID,
			"requestingOrg": e.RequestingOrg,
			"respondingOrg": e.RespondingOrg,
			"exchangeType":  e.ExchangeType,
			"success":       e.Success,
			"errorDetail":   e.ErrorDetail,
		})
	}

	s.ok(w, map[string]any{
		"configured": true,
		"entries":    views,
		"total":      total,
		"truncated":  truncated,
		"from":       from,
		"to":         to,
		"summary":    summariseExchanges(entries),
	})
}

// handleTEFCAPurposeCheck reports whether a purpose of use would be accepted, without exchanging anything.
//
// Offered because the alternative way to find out is to attempt a real exchange and read the refusal, and on a national
// network that means a request that left the building, was logged at the far end, and produced a support call.
func (s *Server) handleTEFCAPurposeCheck(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var body struct {
		Purpose string `json:"purpose"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	purpose := strings.TrimSpace(body.Purpose)
	if purpose == "" {
		s.fail(w, r, http.StatusBadRequest, "supply a purpose of use to check")
		return
	}

	recognised := tefca.ValidPurpose(purpose)

	declared := false
	if s.TEFCA != nil {
		for _, p := range s.TEFCA.Config().SupportedPurposes {
			if p == purpose {
				declared = true
			}
		}
	}

	// Two separate answers, because they are two different problems with two different fixes. An unrecognised purpose
	// is a spelling error or an invention and no configuration will make it work. A recognised purpose that is not
	// declared is a configuration gap this site can close.
	out := map[string]any{
		"purpose":    purpose,
		"recognised": recognised,
		"declared":   declared,
		"acceptable": recognised && declared,
	}

	switch {
	case !recognised:
		out["explanation"] = "TEFCA does not recognise that purpose of use, so no configuration here would make it " +
			"work. A partner would refuse any exchange claiming it."
	case s.TEFCA == nil:
		out["explanation"] = "That is a recognised purpose of use, but this instance is not configured as a TEFCA " +
			"participant, so it cannot exchange for any purpose."
	case !declared:
		out["explanation"] = "That is a recognised purpose of use and this participant has not declared it. An " +
			"exchange claiming it would look correct here and be refused by the partner."
	default:
		out["explanation"] = "This participant is configured for that purpose, so an exchange claiming it will not " +
			"be refused on those grounds."
	}

	s.ok(w, out)
}

// summariseExchanges counts what happened, which is what somebody reads first.
//
// The failure count is separated by pattern rather than totalled. Twelve failures spread evenly across four patterns is
// a network problem; twelve failures all in deliveries to one recipient is one partner, and a single total cannot tell
// them apart.
func summariseExchanges(entries []tefca.TEFCAAudit) map[string]any {
	byType := map[string]map[string]int{}
	byPurpose := map[string]int{}
	failuresByOrg := map[string]int{}

	total, failed := 0, 0

	for _, e := range entries {
		total++

		kind := e.ExchangeType
		if kind == "" {
			kind = "unknown"
		}
		if byType[kind] == nil {
			byType[kind] = map[string]int{}
		}
		byType[kind]["total"]++

		if e.Success {
			byType[kind]["succeeded"]++
		} else {
			failed++
			byType[kind]["failed"]++

			who := e.RespondingOrg
			if who == "" {
				who = e.RequestingOrg
			}
			if who != "" {
				failuresByOrg[who]++
			}
		}

		if e.Purpose != "" {
			byPurpose[e.Purpose]++
		}
	}

	// The worst partner named, because a run of failures against one organisation is the finding, and it is invisible
	// in a total.
	worstOrg, worstCount := "", 0
	for org, n := range failuresByOrg {
		if n > worstCount || (n == worstCount && org < worstOrg) {
			worstOrg, worstCount = org, n
		}
	}

	out := map[string]any{
		"total":      total,
		"failed":     failed,
		"byType":     byType,
		"byPurpose":  byPurpose,
		"worstOrg":   worstOrg,
		"worstCount": worstCount,
	}

	// An exchange with no purpose recorded is a compliance gap, not a display gap, so it is counted and named.
	missing := 0
	for _, e := range entries {
		if e.Purpose == "" {
			missing++
		}
	}
	out["withoutPurpose"] = missing

	return out
}

// allPurposes lists every purpose of use TEFCA recognises.
//
// Served rather than hard-coded in the interface, so a purpose added to the specification appears everywhere at once
// instead of in the places somebody remembered to update.
func allPurposes() []string {
	return []string{
		tefca.PurposeTreatment,
		tefca.PurposePayment,
		tefca.PurposeOperations,
		tefca.PurposePublicHealth,
		tefca.PurposeIndividualAccess,
	}
}
