package engine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// A client-credentials token is fetched once and reused; the FHIR server sees it; a refused secret says so without echoing it.
func TestClientCredentialsTokensAreFetchedOnceAndSent(t *testing.T) {
	var issued atomic.Int32
	tokens := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("grant_type") != "client_credentials" || r.Form.Get("client_secret") != "s3cret" ||
			r.Form.Get("scope") != "https://fhir.example/.default" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = io.WriteString(w, `{"error":"invalid_client","error_description":"bad secret"}`)
			return
		}
		issued.Add(1)
		_, _ = io.WriteString(w, `{"access_token":"tok-1","expires_in":3600,"token_type":"Bearer"}`)
	}))
	defer tokens.Close()

	a := newFHIRAuthorizer(&config.FHIRAuth{Type: "azure", TokenURL: tokens.URL, ClientID: "app", ClientSecret: "s3cret"},
		"https://fhir.example", tokens.Client())
	for i := 0; i < 3; i++ {
		req, _ := http.NewRequest(http.MethodPost, "https://fhir.example/", nil)
		if err := a.authorize(context.Background(), req, nil); err != nil {
			t.Fatal(err)
		}
		if req.Header.Get("Authorization") != "Bearer tok-1" {
			t.Fatalf("header %q", req.Header.Get("Authorization"))
		}
	}
	if issued.Load() != 1 {
		t.Errorf("%d tokens fetched for three requests", issued.Load())
	}

	bad := newFHIRAuthorizer(&config.FHIRAuth{Type: "client_credentials", TokenURL: tokens.URL, ClientID: "app", ClientSecret: "wrong"},
		"https://fhir.example", tokens.Client())
	req, _ := http.NewRequest(http.MethodPost, "https://fhir.example/", nil)
	err := bad.authorize(context.Background(), req, nil)
	if err == nil || !strings.Contains(err.Error(), "invalid_client") || strings.Contains(err.Error(), "wrong") {
		t.Errorf("%v", err)
	}
}

// HealthLake requests are signed for the healthlake service in the region read from the URL.
func TestHealthLakeRequestsAreSignedForTheirRegion(t *testing.T) {
	url := "https://healthlake.us-east-1.amazonaws.com/datastore/abc/r4/"
	a := newFHIRAuthorizer(&config.FHIRAuth{Type: "aws", AccessKeyID: "AKIDEXAMPLE", SecretAccessKey: "secret"}, url, nil)
	req, _ := http.NewRequest(http.MethodPost, url, strings.NewReader("{}"))
	if err := a.authorize(context.Background(), req, []byte("{}")); err != nil {
		t.Fatal(err)
	}
	if h := req.Header.Get("Authorization"); !strings.Contains(h, "/us-east-1/healthlake/aws4_request") {
		t.Errorf("authorization %q", h)
	}
}

func TestAuthPresetsAreCheckedAtLoad(t *testing.T) {
	for _, c := range []struct {
		auth config.FHIRAuth
		want string
	}{
		{config.FHIRAuth{Type: "azure", ClientID: "a", ClientSecret: "b"}, "tenant_id"},
		{config.FHIRAuth{Type: "client_credentials", TokenURL: "http://idp.example/token", ClientID: "a", ClientSecret: "b"}, "https"},
		{config.FHIRAuth{Type: "aws", AccessKeyID: "a", SecretAccessKey: "b"}, "region"},
		{config.FHIRAuth{Type: "magic"}, "not aws, azure"},
	} {
		yaml := "name: t\nsource: {type: mllp, listen: ':0'}\ndestinations:\n  - name: f\n    type: fhir\n    fhir:\n      url: https://fhir.example/\n      auth:\n"
		yaml += "        type: " + c.auth.Type + "\n"
		for k, v := range map[string]string{"tenant_id": c.auth.TenantID, "token_url": c.auth.TokenURL, "client_id": c.auth.ClientID,
			"client_secret": c.auth.ClientSecret, "access_key_id": c.auth.AccessKeyID, "secret_access_key": c.auth.SecretAccessKey} {
			if v != "" {
				yaml += "        " + k + ": " + v + "\n"
			}
		}
		_, err := config.Load(strings.NewReader(yaml), "t.yaml")
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v", c.auth.Type, err)
		}
	}
}
