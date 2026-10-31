package config

import (
	"fmt"
	"net/url"
	"strings"
)

// PASDestination submits each message, a Da Vinci PAS request Bundle, to a payer's Claim/$submit, for a provider whose
// systems build the request (an EHR, DTR) and need it sent, the decision recorded and the answer passed on.
type PASDestination struct {
	// Payer is the payer's FHIR base URL; the request goes to {payer}/Claim/$submit.
	Payer string `yaml:"payer"`
	// BearerToken is a static token, ${ENV} allowed. Auth is OAuth client credentials instead, or Azure, as a FHIR
	// destination's.
	BearerToken string    `yaml:"bearer_token,omitempty"`
	Auth        *FHIRAuth `yaml:"auth,omitempty"`
	// ResultsTo names a channel in this server that receives each answer (the PAS response Bundle).
	ResultsTo string `yaml:"results_to,omitempty"`
}

// ResolvedBearerToken is the bearer token with ${ENV} expanded.
func (p *PASDestination) ResolvedBearerToken() string { return resolveSecret(p.BearerToken) }

func validatePASDest(d *Destination, dataType DataType) []error {
	var errs []error
	if dataType != DataRaw {
		errs = append(errs, fmt.Errorf("destination %q: a PAS destination sends a FHIR PAS request Bundle, which a raw channel "+
			"carries, and this is a %s channel", d.Name, dataType))
	}
	if d.PAS == nil || strings.TrimSpace(d.PAS.Payer) == "" {
		return append(errs, fmt.Errorf("destination %q: pas.payer is the payer's FHIR base URL, where Claim/$submit is", d.Name))
	}
	u, err := url.Parse(d.PAS.Payer)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		errs = append(errs, fmt.Errorf("destination %q: pas.payer %q is not an http or https URL", d.Name, d.PAS.Payer))
	} else if u.Scheme == "http" && !isLoopback(u.Hostname()+":0") {
		errs = append(errs, fmt.Errorf("destination %q: pas.payer is plain http off this machine; a prior authorization request "+
			"is patient data, so it goes over https", d.Name))
	}
	if d.PAS.Auth != nil {
		if d.PAS.Auth.Type == "aws" {
			errs = append(errs, fmt.Errorf("destination %q: pas.auth type aws signs requests for AWS HealthLake, which is not a "+
				"payer; use client_credentials, azure or bearer_token", d.Name))
		} else {
			for _, e := range d.PAS.Auth.validate(d.PAS.Payer, d.PAS.BearerToken) {
				errs = append(errs, fmt.Errorf("destination %q: pas: %s", d.Name, strings.ReplaceAll(e.Error(), "fhir.", "pas.")))
			}
		}
	}
	return errs
}
