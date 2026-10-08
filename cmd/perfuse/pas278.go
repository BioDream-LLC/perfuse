package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// newPAS278 is how PAS reaches a utilization management system: the 278 request POSTed as application/edi-x12, the 278
// response read from the body. tokenEnv names an environment variable holding a bearer token, so the token is never on a
// command line where ps would show it.
func newPAS278(target, tokenEnv string) (func(context.Context, []byte) ([]byte, error), error) {
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("-pas-278-url %q is not an http(s) URL", target)
	}
	token := ""
	if tokenEnv != "" {
		token = strings.TrimSpace(os.Getenv(tokenEnv))
		if token == "" {
			return nil, fmt.Errorf("-pas-278-token-env names %s, which is empty", tokenEnv)
		}
	}
	client := &http.Client{Timeout: 60 * time.Second}
	return func(ctx context.Context, body []byte) ([]byte, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/edi-x12")
		req.Header.Set("Accept", "application/edi-x12")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		out, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
		if err != nil {
			return nil, err
		}
		if resp.StatusCode/100 != 2 {
			return nil, fmt.Errorf("the UM system answered %s", resp.Status)
		}
		return out, nil
	}, nil
}
