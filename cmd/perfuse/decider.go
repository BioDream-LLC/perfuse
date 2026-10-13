package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/pas"
)

// The PAS decision service protocol.
//
// A decision service decides one service of a PAS request on the clinical evidence in the request - what the payer's
// rules file cannot, because a rules file sees only the service's codes. Vaglio is one; the protocol is small enough that
// a payer can put its own behind it.
//
//	POST <-pas-decider-url>?item=<Claim.item.sequence>
//	Content-Type: application/fhir+json         the PAS request Bundle, exactly as the provider sent it
//	X-PAS-Request-Id: <id>                      the ClaimResponse id this server gives the request
//
//	200 {"decision": "approve" | "deny" | "pend", "why": "...", "requested": ["...", ...]}
//	404 the service decides nothing for this item (no policy covers it)
//
// why is said to the provider in the ClaimResponse's process note and is required for a denial: CMS-0057 requires a
// specific reason for every denial, and a denial without one is pended instead. requested lists, in words, the information
// a pended request needs. Anything else - an error, a timeout, a decision this protocol does not define - pends the item
// for a reviewer, and the reason goes to the log, not to the provider. Nothing here can turn a failure into an approval.
//
// A service with its own reviewers can decide later what it pends here: X-PAS-Request-Id is the claimResponse parameter
// Claim/$decide takes, with the item's sequence and the reviewer's decision and reason, as any reviewer's decision is.

// deciderAnswer is the decision service's response body.
type deciderAnswer struct {
	Decision  string   `json:"decision"`
	Why       string   `json:"why"`
	Requested []string `json:"requested"`
}

// deciderTimeout bounds one call. PAS answers synchronously, and a provider's request should not wait on a slow service
// longer than it would wait on a person to pick up the phone.
const deciderTimeout = 20 * time.Second

// newPASDecider returns a Decide that asks the decision service at target. ok reports whether the service decided; when
// it did not (404, or a failure, which is logged), the caller keeps whatever it would otherwise have answered.
func newPASDecider(target, tokenEnv string, log *slog.Logger) (func(ctx context.Context, req *pas.Request, svc pas.Service) (fhirserver.PASAnswer, bool), error) {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" {
		return nil, fmt.Errorf("-pas-decider-url %q is not an http(s) URL without a query", target)
	}
	// The request Bundle is a patient's clinical record and the token is a credential: plain http only to this machine.
	if u.Scheme == "http" && !loopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("-pas-decider-url %q sends patient records and a token unencrypted; use https (plain http is allowed only to localhost)", target)
	}
	token := ""
	if tokenEnv != "" {
		token = strings.TrimSpace(os.Getenv(tokenEnv))
		if token == "" {
			return nil, fmt.Errorf("-pas-decider-token-env names %s, which is empty", tokenEnv)
		}
	}
	client := &http.Client{Timeout: deciderTimeout}
	fail := func(svc pas.Service, why string, err error) (fhirserver.PASAnswer, bool) {
		if log != nil {
			log.Warn("PAS: the decision service did not decide; the item is pended for a reviewer",
				"service", target, "item", svc.Sequence, "why", why, "error", err)
		}
		return fhirserver.PASAnswer{Decision: "pend", Why: "Pended for a reviewer."}, true
	}
	return func(ctx context.Context, req *pas.Request, svc pas.Service) (fhirserver.PASAnswer, bool) {
		body, err := json.Marshal(req.Bundle)
		if err != nil {
			return fail(svc, "the request could not be encoded", err)
		}
		hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, target+"?item="+url.QueryEscape(svc.Sequence), bytes.NewReader(body))
		if err != nil {
			return fail(svc, "the request could not be built", err)
		}
		hreq.Header.Set("Content-Type", "application/fhir+json")
		hreq.Header.Set("Accept", "application/json")
		if id, ok := fhirserver.PASRequestID(ctx); ok {
			// The ClaimResponse id: what a service that pends this item later sends Claim/$decide its reviewer's decision with.
			hreq.Header.Set("X-PAS-Request-Id", id)
		}
		if token != "" {
			hreq.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(hreq)
		if err != nil {
			return fail(svc, "unreachable", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
		if err != nil {
			return fail(svc, "the response could not be read", err)
		}
		if resp.StatusCode == http.StatusNotFound {
			return fhirserver.PASAnswer{}, false
		}
		if resp.StatusCode != http.StatusOK {
			return fail(svc, "answered "+resp.Status, fmt.Errorf("%s", bytes.TrimSpace(raw)))
		}
		var a deciderAnswer
		if err := json.Unmarshal(raw, &a); err != nil {
			return fail(svc, "the response is not a decision", err)
		}
		a.Why = strings.TrimSpace(a.Why)
		switch a.Decision {
		case "approve":
			return fhirserver.PASAnswer{Decision: "approve", Why: a.Why}, true
		case "deny":
			if a.Why == "" {
				return fail(svc, "a denial with no reason", nil)
			}
			return fhirserver.PASAnswer{Decision: "deny", Why: a.Why}, true
		case "pend":
			why := a.Why
			if len(a.Requested) > 0 {
				if why != "" {
					why += " "
				}
				why += "Information needed: " + strings.Join(a.Requested, "; ") + "."
			}
			if why == "" {
				why = "Pended for a reviewer."
			}
			return fhirserver.PASAnswer{Decision: "pend", Why: why}, true
		default:
			return fail(svc, fmt.Sprintf("decision %q is not approve, deny or pend", a.Decision), nil)
		}
	}, nil
}

// loopbackHost reports whether host names this machine.
func loopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// chainPASDecide answers from the rules file first, and asks the decision service only what the rules leave to a reviewer:
// a rule saying a service needs no authorization, or is not covered at all, is the payer's coverage policy and is not the
// decision service's to overrule. When the service pends, what the rule asks for (a DTR questionnaire, attachments) is kept.
func chainPASDecide(
	rules func(ctx context.Context, req *pas.Request, svc pas.Service) fhirserver.PASAnswer,
	decider func(ctx context.Context, req *pas.Request, svc pas.Service) (fhirserver.PASAnswer, bool),
) func(ctx context.Context, req *pas.Request, svc pas.Service) fhirserver.PASAnswer {
	return func(ctx context.Context, req *pas.Request, svc pas.Service) fhirserver.PASAnswer {
		base := fhirserver.PASAnswer{Decision: "pend"}
		if rules != nil {
			base = rules(ctx, req, svc)
		}
		if base.Decision == "approve" || base.Decision == "deny" {
			return base
		}
		a, ok := decider(ctx, req, svc)
		if !ok {
			return base
		}
		if a.Decision == "pend" {
			a.Questionnaire, a.Attachments, a.AttachmentModifiers = base.Questionnaire, base.Attachments, base.AttachmentModifiers
		}
		return a
	}
}
