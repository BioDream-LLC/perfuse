package api

import (
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/egress"
	"github.com/biodream-llc/perfuse/internal/saml"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Reading an identity provider's metadata, so configuring SAML does not require XML surgery.
//
// Every SAML provider publishes a metadata document describing itself: who it is, where to send people, and the certificate its assertions are
// signed with. Until this existed, Perfuse asked an administrator to open that document, find the certificate, strip the line breaks out of
// the base64, wrap it in PEM headers and paste the sign-on URL separately - and a stray space in the result fails with a signature error that
// says nothing about formatting.
//
// It also decided which providers Perfuse appeared to support. A panel that accepts a certificate and a URL works for the vendor whose
// documentation you happen to be reading; a panel that reads metadata works for all of them, including the ones nobody here has tested.
//
// Two ways in, because both are how people actually have the document. A URL when the provider publishes one, and the document itself pasted
// in, which is what somebody has when their provider is behind a network this server cannot reach or when they were sent a file.

// metadataRequest is a URL to fetch or a document to read.
type metadataRequest struct {
	// URL is the provider's metadata endpoint.
	URL string `json:"url"`

	// Document is the metadata XML itself, for when a URL is not reachable from here.
	Document string `json:"document"`
}

// metadataResponse is what the browser needs to fill the form in.
type metadataResponse struct {
	// EntityID is the provider's identifier, which every assertion it issues carries as its Issuer.
	EntityID string `json:"entityId"`

	// SSOURL is where sign-on requests go, redirect binding.
	SSOURL string `json:"ssoUrl"`

	// SSOPostURL is the POST binding equivalent, empty when the provider does not offer one.
	SSOPostURL string `json:"ssoPostUrl"`

	// CertPEM is every signing certificate the document carried, in PEM.
	CertPEM string `json:"certPem"`

	// Certificates describes each one, so somebody can see what they are about to trust rather than a wall of base64.
	Certificates []metadataCert `json:"certificates"`
}

// metadataCert is a signing certificate in terms a person can check.
type metadataCert struct {
	// Subject and Issuer name the certificate.
	Subject string `json:"subject"`
	Issuer  string `json:"issuer"`

	// NotAfter is when it expires, which is the field that causes an outage nobody predicted.
	NotAfter string `json:"notAfter"`

	// Expired says so directly, rather than leaving a date to be compared by eye.
	Expired bool `json:"expired"`

	// Algorithm is the signature algorithm, so an unexpectedly weak one is visible.
	Algorithm string `json:"algorithm"`

	// Fingerprint is the SHA-256 fingerprint, for checking against what the provider's administrator says it should be.
	Fingerprint string `json:"fingerprint"`
}

// handleReadSAMLMetadata parses a provider's metadata and returns the settings it implies.
//
// Admin only, and it changes nothing: it reads a document and reports what is in it. Saving is a separate, deliberate step, because filling a
// form from a document somebody pasted is not the same as agreeing to trust it.
func (s *Server) handleReadSAMLMetadata(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var req metadataRequest
	if !s.decode(w, r, &req) {
		return
	}

	doc, err := s.metadataDocument(req)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	idp, err := saml.ParseIDPMetadata(doc)
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	out := metadataResponse{
		EntityID:     idp.EntityID,
		SSOURL:       idp.SSOURL,
		SSOPostURL:   idp.SSOPostURL,
		CertPEM:      idp.CertPEM(),
		Certificates: []metadataCert{},
	}

	now := time.Now()
	for _, cert := range idp.Certificates {
		out.Certificates = append(out.Certificates, metadataCert{
			Subject:     cert.Subject.String(),
			Issuer:      cert.Issuer.String(),
			NotAfter:    cert.NotAfter.UTC().Format(time.RFC3339),
			Expired:     now.After(cert.NotAfter),
			Algorithm:   cert.SignatureAlgorithm.String(),
			Fingerprint: certFingerprint(cert.Raw),
		})
	}

	s.writeJSON(w, http.StatusOK, out)
}

// metadataDocument returns the document to parse, from whichever of the two was given.
func (s *Server) metadataDocument(req metadataRequest) ([]byte, error) {
	pasted := strings.TrimSpace(req.Document)
	target := strings.TrimSpace(req.URL)

	if pasted != "" && target != "" {
		// Refused rather than picking one. A form that silently ignored the field somebody filled in would be worse than saying so.
		return nil, fmt.Errorf("give either a metadata URL or a document, not both")
	}
	if pasted != "" {
		return []byte(pasted), nil
	}
	if target == "" {
		return nil, fmt.Errorf("give a metadata URL or paste the document")
	}

	return s.fetchMetadata(target)
}

// fetchMetadata retrieves a metadata document, refusing addresses this server should not be made to reach.
//
// The guard is the point. This endpoint takes a URL from whoever can edit the configuration and makes the server request it, which is a way to
// probe an internal network from outside it or to read cloud instance credentials from the metadata address. The same policy that governs
// channel destinations governs this, so there is one answer to "what may this server connect to" rather than two.
func (s *Server) fetchMetadata(target string) ([]byte, error) {
	parsed, err := url.Parse(target)
	if err != nil {
		return nil, fmt.Errorf("that is not a URL: %w", err)
	}

	// https only. A metadata document fetched over http can be replaced in transit, and the certificate inside it is the thing every
	// signature will be checked against - so http here would undo the whole point of checking signatures.
	if parsed.Scheme != "https" {
		return nil, fmt.Errorf("metadata must be fetched over https, not %q: the certificate in this document is what every assertion "+
			"will be checked against, so it cannot be allowed to arrive over a connection anybody can alter", parsed.Scheme)
	}

	host := parsed.Hostname()
	if host == "" {
		return nil, fmt.Errorf("that URL has no host in it")
	}

	if err := egress.Default.CheckResolving(host); err != nil {
		return nil, fmt.Errorf("this server will not fetch from %s: %w", host, err)
	}

	client := &http.Client{
		Timeout: 20 * time.Second,

		// Redirects are not followed. A permitted host redirecting to a blocked one would walk straight past the check above, and a
		// metadata endpoint has no reason to redirect.
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return fmt.Errorf("the metadata URL redirected, which is not followed: a redirect can lead somewhere this server is not " +
				"allowed to reach. Use the address it redirects to")
		},
	}

	res, err := client.Get(target)
	if err != nil {
		return nil, fmt.Errorf("could not fetch the metadata: %w", err)
	}
	defer res.Body.Close()

	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the metadata URL answered %d", res.StatusCode)
	}

	// Bounded, because this is a document from somewhere else and a server should not be made to hold an arbitrary amount of it. Metadata
	// documents are a few kilobytes; a megabyte is generous.
	doc, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("could not read the metadata: %w", err)
	}

	return doc, nil
}

// certFingerprint is the SHA-256 fingerprint in the colon-separated form every tool prints.
//
// Offered so an administrator can compare what Perfuse read against what the provider's own console shows, which is the only way to notice a
// metadata document that was altered between the provider and here.
func certFingerprint(der []byte) string {
	sum := sha256.Sum256(der)

	parts := make([]string, 0, len(sum))
	for _, b := range sum {
		parts = append(parts, fmt.Sprintf("%02X", b))
	}
	return strings.Join(parts, ":")
}
