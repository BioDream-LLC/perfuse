package api

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/contract"
	"github.com/biodream-llc/perfuse/internal/profile"
	"github.com/biodream-llc/perfuse/internal/store"
	"gopkg.in/yaml.v3"
)

// Building a contract from traffic that has already arrived, from the interface.
//
// This existed only as a command-line step. The Contracts section could report that a feed had drifted from
// its contract and could not be used to make one, so the feature's first move required a terminal on the
// server - which for a product whose whole claim is that everything is doable from the interface is a defect
// rather than an omission.
//
// The shape is propose-then-save rather than one button that writes a file. A contract is an assertion about
// what a sending system does, and the person who knows whether last week's traffic is representative is the
// one reading the screen. Writing it silently would produce contracts nobody had agreed to, which fire alerts
// nobody understands, which get switched off.

// proposeContractResponse is a contract offered for review, not yet saved.
type proposeContractResponse struct {
	// Channel is what was profiled.
	Channel string `json:"channel"`

	// Messages is how many were read. Shown because a contract built from four messages is a guess and one
	// built from four hundred is evidence, and the person deciding needs to know which they have.
	Messages int `json:"messages"`

	// YAML is the contract as it would be written.
	YAML string `json:"yaml"`

	// Notes are things worth knowing before saving. Never null.
	Notes []string `json:"notes"`

	// Expectations is how many things the contract asserts.
	Expectations int `json:"expectations"`
}

// handleProposeContract profiles recent traffic and returns a contract for review.
func (s *Server) handleProposeContract(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	name := r.PathValue("name")

	channels, ok2 := s.channelsFor(w, r, sess)
	if !ok2 {
		return
	}

	cfg, err := channels.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}
	if !s.requireMessages(w, r) {
		return
	}
	messages := rt.Messages

	// The same default the contract checker uses, so what is proposed here is what will be compared later.
	over := 500
	if cfg.Contract != nil && cfg.Contract.Over > 0 {
		over = cfg.Contract.Over
	}

	payloads, err := messages.RecentPayloads(r.Context(), name, over)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	// Refused rather than answered with an empty contract.
	//
	// A contract built from nothing describes nothing, and would then report every real message as a
	// departure from it. Saying so is more useful than producing a document that looks finished.
	if len(payloads) == 0 {
		s.fail(w, r, http.StatusConflict,
			fmt.Sprintf("no messages have been recorded for %q yet, so there is nothing to learn a "+
				"contract from. Let the feed run first, then come back", name))
		return
	}

	report := profile.BuildFor(string(cfg.DataType), payloads)
	if report == nil {
		s.fail(w, r, http.StatusConflict, "the recorded messages could not be profiled")
		return
	}

	proposed := contract.Promote(report, contract.PromoteOptions{
		Source: fmt.Sprintf("traffic recorded by this server for %s", name),
	})

	// Reported as a fault in Perfuse rather than in the messages, which is what the command-line path does.
	// Somebody told their input is wrong will spend an hour looking at it.
	if errs := proposed.Validate(); len(errs) > 0 {
		lines := make([]string, 0, len(errs))
		for _, e := range errs {
			lines = append(lines, e.Error())
		}
		s.fail(w, r, http.StatusInternalServerError,
			"the generated contract is not valid, which is a fault in Perfuse rather than a problem with "+
				"your messages", lines...)
		return
	}

	raw, err := yaml.Marshal(proposed)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	notes := []string{}
	if len(payloads) < 50 {
		notes = append(notes, fmt.Sprintf(
			"Only %d message(s) were available. A contract built from this few will describe whatever "+
				"happened to be in them, so expect it to need revising once more traffic has been through.",
			len(payloads)))
	}
	if cfg.Contract != nil && cfg.Contract.File != "" {
		notes = append(notes, fmt.Sprintf(
			"This channel already has a contract at %s. Saving will replace it.", cfg.Contract.File))
	}

	s.ok(w, proposeContractResponse{
		Channel:      name,
		Messages:     len(payloads),
		YAML:         string(raw),
		Notes:        notes,
		Expectations: len(proposed.Expectations),
	})
}

// saveContractRequest is a reviewed contract being written.
type saveContractRequest struct {
	// YAML is the contract to write, as reviewed. Sent back rather than rebuilt so that what is saved is
	// exactly what was on screen: rebuilding could profile different traffic and write something the person
	// never read.
	YAML string `json:"yaml"`

	// CheckEvery is how often the contract is re-checked, as a duration.
	//
	// Sent with the contract rather than through a channel edit, because it belongs to the same decision: somebody choosing what to
	// assert about a feed is the person who knows how often it is worth asking. It was previously settable only by editing the
	// channel file by hand - the builder has no contract section, deliberately, and nothing else offered it.
	//
	// Empty leaves whatever is there, which for a new attachment means the default of fifteen minutes.
	CheckEvery string `json:"checkEvery,omitempty"`
}

// handleSaveContract writes a reviewed contract and attaches it to the channel.
func (s *Server) handleSaveContract(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	name := r.PathValue("name")

	var req saveContractRequest
	if !s.decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.YAML) == "" {
		s.fail(w, r, http.StatusBadRequest, "there is no contract to save")
		return
	}

	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	cfg, err := channels.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	// Parsed before anything is written.
	//
	// A contract file that will not load makes the channel referencing it invalid, and Perfuse refuses a
	// channel wholesale when anything it references is broken - so an unparseable contract does not produce a
	// warning, it makes the channel disappear. That was learned the hard way when a fixture did it.
	var check contract.Contract
	if err := yaml.Unmarshal([]byte(req.YAML), &check); err != nil {
		s.fail(w, r, http.StatusBadRequest,
			"that is not a contract this can read: "+err.Error())
		return
	}
	if len(check.Expectations) == 0 {
		s.fail(w, r, http.StatusBadRequest,
			"a contract with no expectations asserts nothing, so nothing would ever be reported")
		return
	}
	if errs := check.Validate(); len(errs) > 0 {
		lines := make([]string, 0, len(errs))
		for _, e := range errs {
			lines = append(lines, e.Error())
		}
		s.fail(w, r, http.StatusBadRequest, "that contract is not valid", lines...)
		return
	}

	file := name + ".contract.yaml"
	if cfg.Contract != nil && cfg.Contract.File != "" {
		file = cfg.Contract.File
	}

	if err := channels.WriteSidecar(name, file, []byte(req.YAML)); err != nil {
		s.failErr(w, r, err)
		return
	}

	// Attach it, if it is not attached already. Writing the file without the reference would leave somebody
	// looking at a contract that is never checked.
	attach := cfg.Contract == nil || cfg.Contract.File == ""

	interval := strings.TrimSpace(req.CheckEvery)
	if interval != "" {
		if _, err := time.ParseDuration(interval); err != nil {
			s.fail(w, r, http.StatusBadRequest,
				"that is not a duration this can read: "+interval+`. Write it as 15m, 1h or 30s.`)

			return
		}
	}

	if attach {
		cfg.Contract = &config.ContractRef{File: filepath.Base(file)}
	}

	// Changed only when asked. An empty field means "leave it alone" rather than "use the default", because the two differ for a
	// contract that already had an interval somebody chose.
	if interval != "" && cfg.Contract.CheckEvery != interval {
		cfg.Contract.CheckEvery = interval
		attach = true
	}

	if attach {
		if err := channels.Save(cfg); err != nil {
			s.failErr(w, r, err)
			return
		}
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "contract.save",
		Target: name, Detail: "from observed traffic", IP: clientIP(r),
	})

	s.ok(w, map[string]any{"channel": name, "file": filepath.Base(file)})
}
