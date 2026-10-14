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
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/biodream-llc/perfuse/internal/crd"
)

// newCRDAdvisor returns a crd.Advise that asks a decision service (such as Vaglio's /api/pas/advise) what a prior
// authorization request for an order would be told as the record stands. Anything but a clear answer - no service,
// an error, a timeout, a service that does not decide the code - is no advice, and the CRD response is as it is without
// one. The record the EHR prefetched is sent, so the same rules as -pas-decider-url apply: https off this machine.
func newCRDAdvisor(target, tokenEnv, caFile string, log *slog.Logger) (func(ctx context.Context, system, code string, record []map[string]any) (*crd.Advice, bool), error) {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.RawQuery != "" {
		return nil, fmt.Errorf("-crd-advisor-url %q is not an http(s) URL without a query", target)
	}
	if u.Scheme == "http" && !loopbackHost(u.Hostname()) {
		return nil, fmt.Errorf("-crd-advisor-url %q sends patient records and a token unencrypted; use https (plain http is allowed only to localhost)", target)
	}
	token := ""
	if tokenEnv != "" {
		token = strings.TrimSpace(os.Getenv(tokenEnv))
		if token == "" {
			return nil, fmt.Errorf("-crd-advisor-token-env names %s, which is empty", tokenEnv)
		}
	}
	client := &http.Client{}
	if caFile != "" {
		pool, err := x509.SystemCertPool()
		if err != nil || pool == nil {
			pool = x509.NewCertPool()
		}
		raw, err := os.ReadFile(caFile)
		if err != nil {
			return nil, fmt.Errorf("-crd-advisor-ca: %w", err)
		}
		if !pool.AppendCertsFromPEM(raw) {
			return nil, fmt.Errorf("-crd-advisor-ca %s holds no PEM certificate", caFile)
		}
		t := http.DefaultTransport.(*http.Transport).Clone()
		t.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool}
		client.Transport = t
	}
	none := func(why string, err error) (*crd.Advice, bool) {
		if log != nil {
			log.Warn("CRD: no advice from the decision service; the cards are as without it", "service", target,
				"why", why, "error", err)
		}
		return nil, false
	}
	return func(ctx context.Context, system, code string, record []map[string]any) (*crd.Advice, bool) {
		if record == nil {
			record = []map[string]any{}
		}
		body, err := json.Marshal(map[string]any{"service": map[string]string{"system": system, "code": code},
			"resources": record})
		if err != nil {
			return none("the record could not be encoded", err)
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return none("the request could not be built", err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return none("unreachable", err)
		}
		defer resp.Body.Close()
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return none("the response could not be read", err)
		}
		if resp.StatusCode != http.StatusOK {
			return none("answered "+resp.Status, fmt.Errorf("%s", bytes.TrimSpace(raw)))
		}
		var a crd.Advice
		if err := json.Unmarshal(raw, &a); err != nil {
			return none("the response is not advice", err)
		}
		switch a.Answer {
		case "approve", "pend", "deny", "":
		default:
			return none("an answer that is not approve, pend or deny", fmt.Errorf("%q", a.Answer))
		}
		return &a, a.Decides
	}, nil
}
