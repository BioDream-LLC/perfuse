package config

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

// FHIRAuth is how a fhir destination authenticates to a hosted FHIR service.
//
// Three presets, because the hosted services are where a bearer token pasted into a file stops working within the hour:
//
//   - aws: AWS HealthLake, which takes requests signed with Signature Version 4 for the service "healthlake" - no token at all.
//   - azure: Azure Health Data Services, which takes a Microsoft Entra ID token for the FHIR service's own audience, got with the
//     client-credentials grant of an app registration that holds the FHIR Data Contributor role.
//   - client_credentials: any OAuth 2.0 token endpoint - Keycloak, Okta, a SMART backend service with a client secret.
//
// Tokens are fetched when needed and reused until a minute before they expire.
type FHIRAuth struct {
	// Type is aws, azure or client_credentials.
	Type string `yaml:"type"`

	// Region is the AWS region. Read from a HealthLake URL when it is not given.
	Region string `yaml:"region,omitempty"`
	// AccessKeyID, SecretAccessKey and SessionToken are the AWS credentials, literal or ${ENV} references.
	AccessKeyID     string `yaml:"access_key_id,omitempty"`
	SecretAccessKey string `yaml:"secret_access_key,omitempty"`
	SessionToken    string `yaml:"session_token,omitempty"`

	// TenantID is the Entra ID tenant, for azure.
	TenantID string `yaml:"tenant_id,omitempty"`
	// TokenURL is the OAuth token endpoint, for client_credentials. For azure it is derived from the tenant.
	TokenURL string `yaml:"token_url,omitempty"`
	// ClientID and ClientSecret identify the application. The secret may be a ${ENV} reference, which is how it should be given.
	ClientID     string `yaml:"client_id,omitempty"`
	ClientSecret string `yaml:"client_secret,omitempty"`
	// Scope is requested with the token. For azure it defaults to the FHIR service URL followed by /.default, which is its audience.
	Scope string `yaml:"scope,omitempty"`
}

// Resolved returns a copy with environment references expanded and the azure defaults filled in.
func (a *FHIRAuth) Resolved(fhirURL string) FHIRAuth {
	out := *a
	out.AccessKeyID = resolveSecret(a.AccessKeyID)
	out.SecretAccessKey = resolveSecret(a.SecretAccessKey)
	out.SessionToken = resolveSecret(a.SessionToken)
	out.ClientSecret = resolveSecret(a.ClientSecret)
	if out.Type == "azure" {
		if out.TokenURL == "" && out.TenantID != "" {
			out.TokenURL = "https://login.microsoftonline.com/" + url.PathEscape(out.TenantID) + "/oauth2/v2.0/token"
		}
		if out.Scope == "" {
			out.Scope = strings.TrimRight(fhirURL, "/") + "/.default"
		}
	}
	if out.Type == "aws" && out.Region == "" {
		// https://healthlake.us-east-1.amazonaws.com/datastore/<id>/r4/
		if u, err := url.Parse(fhirURL); err == nil {
			parts := strings.Split(u.Hostname(), ".")
			if len(parts) >= 4 && parts[0] == "healthlake" {
				out.Region = parts[1]
			}
		}
	}
	return out
}

func (a *FHIRAuth) validate(fhirURL string, bearer string) []error {
	var errs []error
	r := a.Resolved(fhirURL)
	if bearer != "" {
		errs = append(errs, errors.New("fhir.auth and fhir.bearer_token cannot both be set"))
	}
	switch a.Type {
	case "aws":
		if r.Region == "" {
			errs = append(errs, errors.New("fhir.auth type aws needs region, which could not be read from the URL"))
		}
		if r.AccessKeyID == "" || r.SecretAccessKey == "" {
			errs = append(errs, errors.New("fhir.auth type aws needs access_key_id and secret_access_key, e.g. ${AWS_ACCESS_KEY_ID}"))
		}
	case "azure", "client_credentials":
		if r.TokenURL == "" {
			errs = append(errs, fmt.Errorf("fhir.auth type %s needs %s", a.Type, map[bool]string{true: "tenant_id", false: "token_url"}[a.Type == "azure"]))
		} else if !strings.HasPrefix(r.TokenURL, "https://") && !strings.HasPrefix(r.TokenURL, "http://127.0.0.1") &&
			!strings.HasPrefix(r.TokenURL, "http://localhost") {
			errs = append(errs, errors.New("fhir.auth token_url must be https: the client secret is sent to it"))
		}
		if r.ClientID == "" || r.ClientSecret == "" {
			errs = append(errs, fmt.Errorf("fhir.auth type %s needs client_id and client_secret, e.g. ${FHIR_CLIENT_SECRET}", a.Type))
		}
	default:
		errs = append(errs, fmt.Errorf("fhir.auth type %q is not aws, azure or client_credentials", a.Type))
	}
	return errs
}
