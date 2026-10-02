package api

import (
	"net/http"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// The certificate view.
//
// This is the reason the feature is worth having in the interface rather than only
// in a config file. An expired certificate on an interface feed is a real hospital
// outage, and it happens because nobody was told: the renewal was somebody's job
// three months ago, that person has moved on, and the first sign of trouble is a
// sender reporting that messages stopped.

// handleCertificates describes the TLS configuration of every channel.
func (s *Server) handleCertificates(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	list, broken, err := channels.List()
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	type endpoint struct {
		Channel     string          `json:"channel"`
		Where       string          `json:"where"`
		Destination string          `json:"destination,omitempty"`
		Address     string          `json:"address,omitempty"`
		TLS         tlsconf.Summary `json:"tls"`
	}

	var endpoints []endpoint
	var plaintext []string

	for _, summary := range list {
		cfg, err := channels.Get(summary.Name)
		if err != nil {
			continue
		}

		if cfg.Source.TLS.IsEnabled() {
			endpoints = append(endpoints, endpoint{
				Channel: cfg.Name,
				Where:   "listener",
				Address: cfg.Source.Listen,
				TLS:     tlsconf.Summarise(cfg.Source.TLS, true),
			})
		} else {
			// Reported rather than omitted. A page listing only the encrypted
			// endpoints would let somebody conclude everything is encrypted, and the
			// interesting question is usually which feed is not.
			plaintext = append(plaintext,
				cfg.Name+" listens on "+cfg.Source.Listen+" without TLS")
		}

		for _, d := range cfg.Destinations {
			if d.TLS.IsEnabled() {
				endpoints = append(endpoints, endpoint{
					Channel:     cfg.Name,
					Where:       "destination",
					Destination: d.Name,
					Address:     d.Address,
					TLS:         tlsconf.Summarise(d.TLS, false),
				})
				continue
			}
			// Only MLLP destinations carry clinical data over a socket we control.
			// A file destination has no transport to encrypt, and a fhir or cda one
			// is already checked for plain HTTP at load.
			if d.Type == "mllp" {
				plaintext = append(plaintext,
					cfg.Name+" sends to "+d.Name+" at "+d.Address+" without TLS")
			}
		}
	}

	expiring, expired := 0, 0
	for _, e := range endpoints {
		for _, c := range append(append([]tlsconf.Certificate{}, e.TLS.Certificates...),
			e.TLS.Authorities...) {
			switch c.Status {
			case "expiring":
				expiring++
			case "expired":
				expired++
			}
		}
	}

	// Always arrays. See apinull_test.go. The GUI happens to guard plaintext defensively, but a contract that is only
	// honoured because the client distrusts it is not a contract.
	if endpoints == nil {
		endpoints = []endpoint{}
	}
	if plaintext == nil {
		plaintext = []string{}
	}

	s.ok(w, map[string]any{
		"endpoints": endpoints,
		// The counts are what a dashboard badge needs, and what makes expiry
		// visible without reading the table.
		"expiring":  expiring,
		"expired":   expired,
		"plaintext": plaintext,
		"broken":    broken,
	})
}

// handleInspectCertificate describes a certificate file by path.
//
// Restricted to administrators. It reads an arbitrary path from the filesystem,
// and although it only ever returns parsed certificate metadata, the ability to
// probe for the existence of a path is not something a viewer should have.
func (s *Server) handleInspectCertificate(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var body struct {
		Path string `json:"path"`
		PEM  string `json:"pem"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	var certs []tlsconf.Certificate
	var err error

	switch {
	case body.PEM != "":
		// Pasting a certificate is the safer path and the more useful one: somebody
		// checking what a supplier sent them has it on the clipboard, not on this
		// machine.
		certs, err = tlsconf.DescribePEM([]byte(body.PEM))
	case body.Path != "":
		certs, err = tlsconf.Describe(body.Path)
	default:
		s.fail(w, r, http.StatusBadRequest, "provide either a path or a PEM certificate")
		return
	}

	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	s.ok(w, map[string]any{"certificates": certs})
}
