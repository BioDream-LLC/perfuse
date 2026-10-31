package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/priorauth"
)

// PriorAuths records what PAS destinations submitted and what the payers answered, set at start-up; nil keeps no record.
var PriorAuths *priorauth.PriorAuthTracker

// PASSender submits each message, a Da Vinci PAS request Bundle, to a payer's Claim/$submit: the provider's side of
// CMS-0057 prior authorization, for systems that build the request but do not send it.
//
// A decision is the outcome, whatever it is: a denial or a pend is a delivered request, recorded with the payer's
// reference, not a failure to retry. What fails delivery is a payer that did not decide: an error status, an
// OperationOutcome, or a ClaimResponse whose outcome is error. The payer's whole answer is this destination's reply, for
// a response transformer, and goes to the channel results_to names, when there is one.
type PASSender struct {
	cfg    config.PASDestination
	name   string
	router ChannelRouter
	client *http.Client
	auth   *fhirAuthorizer
}

// TenantRouter is a router that says which tenant it routes for, so what a sender records is kept to that tenant.
type TenantRouter interface {
	ChannelRouter
	TenantName() string
}

// NewPASSender builds the sender.
func NewPASSender(d config.Destination, router ChannelRouter) (*PASSender, error) {
	if d.PAS == nil {
		return nil, fmt.Errorf("destination %q is a pas destination with no pas block", d.Name)
	}
	if d.PAS.ResultsTo != "" && router == nil {
		return nil, fmt.Errorf("%w, so destination %q cannot hand the payer's answers to %q", ErrNoRouter, d.Name, d.PAS.ResultsTo)
	}
	timeout := d.Timeout
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	return &PASSender{
		cfg: *d.PAS, name: d.Name, router: router,
		client: &http.Client{
			Timeout: timeout,
			// A redirect would repost patient data somewhere the configuration did not name.
			CheckRedirect: func(req *http.Request, _ []*http.Request) error {
				return fmt.Errorf("refusing to follow a redirect to %s", req.URL.Host)
			},
		},
		auth: newFHIRAuthorizer(d.PAS.Auth, d.PAS.Payer, &http.Client{Timeout: 30 * time.Second}),
	}, nil
}

// Send submits the request.
func (s *PASSender) Send(ctx context.Context, msg []byte) error {
	_, err := s.SendForResponse(ctx, msg)
	return err
}

// SendForResponse submits the request and returns the payer's answer.
func (s *PASSender) SendForResponse(ctx context.Context, msg []byte) ([]byte, error) {
	var bundle map[string]any
	if err := json.Unmarshal(msg, &bundle); err != nil {
		return nil, fmt.Errorf("pas: the message is not a FHIR JSON Bundle: %w", err)
	}
	req, err := priorauth.Summarise(bundle)
	if err != nil {
		return nil, err
	}
	raw, decision, err := priorauth.Submit(ctx, s.client, s.cfg.Payer, msg, func(r *http.Request, body []byte) error {
		if s.auth != nil {
			return s.auth.authorize(ctx, r, body)
		}
		if s.cfg.BearerToken != "" {
			r.Header.Set("Authorization", "Bearer "+s.cfg.ResolvedBearerToken())
		}
		return nil
	})
	if err != nil {
		return raw, err
	}
	req.Destination, req.SubmittedAt = s.name, time.Now().UTC()
	if t, ok := s.router.(TenantRouter); ok {
		req.Tenant = t.TenantName()
	}
	if PriorAuths != nil {
		if err := PriorAuths.Track(req, decision); err != nil {
			// The payer has decided; failing the delivery would submit the same request again.
			return raw, fmt.Errorf("pas: the payer decided %s (reference %s), and the decision could not be recorded: %w",
				decision.Decision, decision.PreAuthRef, err)
		}
	}
	if s.cfg.ResultsTo != "" {
		if err := s.router.Route(ctx, s.cfg.ResultsTo, raw); err != nil {
			return raw, fmt.Errorf("pas: the payer decided %s, and its answer could not be handed to channel %q: %w",
				decision.Decision, s.cfg.ResultsTo, err)
		}
	}
	return raw, nil
}

// Describe says where requests go.
func (s *PASSender) Describe() string {
	return "Da Vinci PAS prior authorization requests to " + s.cfg.Payer
}

// Close releases nothing.
func (s *PASSender) Close() error { return nil }
