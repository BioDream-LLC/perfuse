package api

import (
	"context"
	"fmt"
	"net/http"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/engine"
	"github.com/biodream-llc/perfuse/internal/parity"
	"github.com/biodream-llc/perfuse/internal/store"
)

// channelTransformer produces a channel's output for a message, without sending anything.
//
// Built on the trace rather than on a second implementation of the pipeline. Two ways of asking what a channel does with
// a message would eventually give two answers, and for a tool whose entire purpose is to be believed about a migration
// that would be worse than having neither.
type channelTransformer struct {
	cfg *config.Channel
}

func (c channelTransformer) Transform(raw []byte) ([]byte, error) {
	tr, err := engine.TraceMessage(context.Background(), c.cfg, raw)
	if err != nil {
		return nil, err
	}
	if !tr.Accepted {
		// A filtered message has no output, and calling that an empty string would report it as a difference against
		// every message the old engine passed through. Named as what it is.
		var why string
		for _, st := range tr.Stages {
			if !st.OK {
				why = st.Detail
			}
		}
		if why == "" {
			why = "the message was not accepted"
		}
		return nil, fmt.Errorf("%s", why)
	}
	return []byte(tr.Output), nil
}

// handleParity compares this channel's output against the old engine's, on the site's own traffic.
//
// # Why the pairs are posted
//
// This cannot reach into Mirth. What it needs is the message as it arrived and the message the old engine produced from
// it - both of which are in Mirth's own message store and exportable from its message browser. Requiring them is the
// honest shape of the feature: a tool that claimed to verify a migration without ever seeing the old engine's output
// would be verifying nothing.
//
// # Why editor
//
// Nothing is sent and nothing is saved, so viewer would be defensible. Editor because the output of this is the evidence
// somebody uses to decide to move a live clinical feed, and that is not a read-only act.
func (s *Server) handleParity(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	name := r.PathValue("name")

	repo, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	var req struct {
		Pairs []struct {
			Input     string `json:"input"`
			Expected  string `json:"expected"`
			Reference string `json:"reference"`
		} `json:"pairs"`
	}
	if !s.decode(w, r, &req) {
		return
	}

	if len(req.Pairs) > 20000 {
		s.fail(w, r, http.StatusRequestEntityTooLarge,
			"that is more than 20000 pairs in one request. Parity over a larger corpus is better run in batches, and "+
				"the findings from each batch are directly comparable because they are grouped by field")
		return
	}

	cfg, err := repo.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	pairs := make([]parity.Pair, 0, len(req.Pairs))
	for _, p := range req.Pairs {
		pairs = append(pairs, parity.Pair{
			// Line endings normalised with the same helper the rest of the API uses. An export that went through a
			// text editor or a mail client arrives with newlines, and comparing those against carriage returns
			// reports every message as differing at every field - a result that says nothing about either engine.
			Input:     []byte(normaliseTerminators(p.Input)),
			Expected:  []byte(normaliseTerminators(p.Expected)),
			Reference: p.Reference,
		})
	}

	report, err := parity.Compare(channelTransformer{cfg: cfg}, pairs)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	s.ok(w, report)
}
