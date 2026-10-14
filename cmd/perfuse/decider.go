package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"regexp"
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
//	200 {"decision": "approve" | "deny" | "pend", "why": "...", "requested": ["...", ...],
//	     "questionnaire": "<canonical>", "attachments": ["<LOINC>", ...]}
//	404 the service decides nothing for this item (no policy covers it)
//
// why is said to the provider in the ClaimResponse's process note and is required for a denial: CMS-0057 requires a
// specific reason for every denial, and a denial without one is pended instead. requested lists, in words, the information
// a pended request needs. questionnaire and attachments, on a pend, are optional: the DTR questionnaire to fill in and the
// LOINC attachment codes of the documents to send, which this server asks for with a Da Vinci CDex Task and its
// CommunicationRequests as it does for a rules file's (a code that is not LOINC's shape is dropped and logged; the rules
// file's own asks are kept beside them). Anything else - an error, a timeout, a decision this protocol does not define - pends the item
// for a reviewer, and the reason goes to the log, not to the provider. Nothing here can turn a failure into an approval.
//
// A service with its own reviewers can decide later what it pends here: X-PAS-Request-Id is the claimResponse parameter
// Claim/$decide takes, with the item's sequence and the reviewer's decision and reason, as any reviewer's decision is.

// deciderAnswer is the decision service's response body.
type deciderAnswer struct {
	Decision      string   `json:"decision"`
	Why           string   `json:"why"`
	Requested     []string `json:"requested"`
	Questionnaire string   `json:"questionnaire"`
	Attachments   []string `json:"attachments"`
}

// loincCode is the shape of a LOINC code: digits, a hyphen and a check digit. An attachment code becomes part of a CDex
// request a provider's system acts on, so a service's answer is not passed through unchecked.
var loincCode = regexp.MustCompile(`^\d{1,7}-\d$`)

// deciderTimeout bounds one call. PAS answers synchronously, and a provider's request should not wait on a slow service
// longer than it would wait on a person to pick up the phone.
const deciderTimeout = 20 * time.Second

// newPASDecider returns a Decide that asks the decision service at target. ok reports whether the service decided; when
// it did not (404, or a failure, which is logged), the caller keeps whatever it would otherwise have answered.
func newPASDecider(target, tokenEnv, caFile string, log *slog.Logger) (func(ctx context.Context, req *pas.Request, svc pas.Service) (fhirserver.PASAnswer, bool), error) {
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
	if caFile != "" {
		// A decision service on the payer's own network often carries a certificate from the payer's own authority, or
		// from the service's (Vaglio makes one for itself): trust that file beside the system's authorities.
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		raw, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("-pas-decider-ca: %w", err)
		}
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("-pas-decider-ca %s holds no PEM certificate", caFile)
		}
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
		client.Transport = t
	}
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
			out := fhirserver.PASAnswer{Decision: "pend", Why: why}
			if q := strings.TrimSpace(a.Questionnaire); q != "" {
				if u, err := url.Parse(q); err == nil && (u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "urn") {
					out.Questionnaire = q
				} else if log != nil {
					log.Warn("PAS: the decision service named a questionnaire that is not a canonical URL; not asked for",
						"service", target, "item", svc.Sequence, "questionnaire", q)
				}
			}
			for _, c := range a.Attachments {
				if loincCode.MatchString(c) {
					out.Attachments = append(out.Attachments, c)
				} else if log != nil {
					log.Warn("PAS: the decision service named an attachment code that is not LOINC's shape; not asked for",
						"service", target, "item", svc.Sequence, "code", c)
				}
			}
			return out, true
		default:
			return fail(svc, fmt.Sprintf("decision %q is not approve, deny or pend", a.Decision), nil)
		}
	}, nil
}

// union is a's codes then b's not already in a.
func union(a, b []string) []string {
	out := append([]string(nil), a...)
	for _, c := range b {
		dup := false
		for _, d := range out {
			dup = dup || d == c
		}
		if !dup {
			out = append(out, c)
		}
	}
	return out
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
			// The rules file's asks are the payer's standing requirements; the service's are what this request lacks. Both
			// are asked for, the rules file's questionnaire first: a CDex Task asks for one.
			if base.Questionnaire != "" {
				a.Questionnaire = base.Questionnaire
			}
			a.Attachments = union(base.Attachments, a.Attachments)
			a.AttachmentModifiers = base.AttachmentModifiers
		}
		return a
	}
}
