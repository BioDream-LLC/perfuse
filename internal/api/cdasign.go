package api

import (
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/xmldsig"
)

// Signing and verifying clinical documents from the interface.
//
// # Verification is a viewer action, signing is not
//
// Anybody who can read a document should be able to find out whether it has been altered; withholding that makes the
// signature useless. Signing is different: it asserts that this organisation takes responsibility for a set of bytes,
// using the server's own key, and that is not a thing to hand out lightly.

// handleVerifyDocument checks the signature on a document.
func (s *Server) handleVerifyDocument(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var body struct {
		Document string `json:"document"`

		// TrustPEM is one or more certificates to check the chain against.
		//
		// Supplied per request rather than configured, because the answer to "do I trust this signature" depends on
		// who is asking and about what. A pathology result from one partner and a discharge summary from another are
		// trusted through different authorities, and a single server-wide trust store cannot express that.
		TrustPEM string `json:"trustPem"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	if strings.TrimSpace(body.Document) == "" {
		s.fail(w, r, http.StatusBadRequest, "supply a document to check")
		return
	}

	opts := xmldsig.VerifyOptions{}

	if strings.TrimSpace(body.TrustPEM) != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(body.TrustPEM)) {
			// Refused rather than ignored. Silently continuing with an empty pool would report the signature as
			// untrusted and let somebody conclude the document is suspect, when in fact their paste was wrong.
			s.fail(w, r, http.StatusBadRequest,
				"no certificates could be read from that. Paste one or more certificates in PEM form, beginning "+
					"with BEGIN CERTIFICATE")
			return
		}
		opts.Roots = pool
	}

	report, err := xmldsig.Verify([]byte(body.Document), opts)
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// sound is computed here rather than left to the interface, so every consumer applies the same rule. A caller
	// that assembles "valid" from four booleans will eventually assemble it differently.
	s.ok(w, map[string]any{"report": report, "sound": report.Sound()})
}

// handleSignDocument signs a document with the server's TLS key.
//
// # Why the TLS key, with a warning
//
// A production signing arrangement uses a key issued for the purpose, held in hardware, with a certificate naming
// the responsible clinician or organisation. This uses the server's TLS key because that is the key a Perfuse
// instance definitely has, which makes the feature usable rather than aspirational.
//
// The response says so plainly. A signature made with a web server's certificate is a real cryptographic signature
// and a weak attestation: the certificate names a hostname, not a person or a professional body, so it proves the
// document passed through this server unaltered rather than that any clinician approved it. Pretending otherwise
// would be the worst outcome, because a signature nobody questions is worse than no signature.
func (s *Server) handleSignDocument(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var body struct {
		Document string `json:"document"`

		// ReferenceID names the element to sign. Empty signs the whole document.
		ReferenceID string `json:"referenceId"`

		// Role is the capacity the signer was acting in.
		Role string `json:"role"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	if strings.TrimSpace(body.Document) == "" {
		s.fail(w, r, http.StatusBadRequest, "supply a document to sign")
		return
	}

	cert, key, err := s.signingIdentity()
	if err != nil {
		// 409 rather than 500: nothing failed, the server simply has no key to sign with, and the message says what
		// to do about it.
		s.fail(w, r, http.StatusConflict, err.Error())
		return
	}

	signed, err := xmldsig.Sign([]byte(body.Document), xmldsig.SignOptions{
		Key:         key,
		Certificate: cert,
		ReferenceID: strings.TrimSpace(body.ReferenceID),
		Role:        strings.TrimSpace(body.Role),
		SigningTime: time.Now(),
	})
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// Audited by name. Signing is an assertion made in this organisation's name, and who caused it to be made is
	// exactly the sort of thing somebody asks about afterwards.
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username,
		Action:   "document.sign",
		IP:       clientIP(r),
	})

	s.ok(w, map[string]any{
		"document": string(signed),
		"signer":   cert.Subject.String(),
		"expires":  cert.NotAfter,

		// Returned with the signature rather than documented elsewhere, so nobody can use this without meeting it.
		"caveat": "This was signed with the server's TLS certificate, which names a hostname rather than a person " +
			"or a professional body. It proves the document has not been altered since it passed through this " +
			"server. It does not assert that a clinician reviewed or approved the content. For that, a key issued " +
			"for signing and a certificate naming the responsible person is needed.",
	})
}

// signingIdentity loads the server's certificate and key.
//
// Read from disk on each use rather than cached. Signing is rare, the file is small, and a cached key means a
// replaced certificate keeps producing signatures against the old one until somebody restarts - with nothing on
// screen to explain why the new certificate is not being used.
func (s *Server) signingIdentity() (*x509.Certificate, crypto.Signer, error) {
	if s.TLSCertFile == "" || s.TLSKeyFile == "" {
		return nil, nil, fmt.Errorf("this server has no certificate to sign with. Start it with -tls-cert and " +
			"-tls-key, or sign elsewhere and paste the result here to check it")
	}

	pair, err := tls.LoadX509KeyPair(s.TLSCertFile, s.TLSKeyFile)
	if err != nil {
		return nil, nil, fmt.Errorf("the server's certificate could not be read: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return nil, nil, fmt.Errorf("the server's certificate file contains no certificate")
	}

	cert, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, nil, fmt.Errorf("the server's certificate could not be parsed: %w", err)
	}

	signer, ok := pair.PrivateKey.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("the server's key is a %T, which cannot produce a signature", pair.PrivateKey)
	}
	return cert, signer, nil
}

// handleSigningIdentity reports what this server would sign as, without signing anything.
//
// Offered so the interface can say who the signature will name before anybody presses the button. Discovering that a
// clinical document was signed by "perfuse-02.hospital.internal" after the fact is a poor way to find out.
func (s *Server) handleSigningIdentity(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	cert, _, err := s.signingIdentity()
	if err != nil {
		s.ok(w, map[string]any{"available": false, "reason": err.Error()})
		return
	}

	s.ok(w, map[string]any{
		"available":   true,
		"subject":     cert.Subject.String(),
		"commonName":  cert.Subject.CommonName,
		"issuer":      cert.Issuer.String(),
		"notAfter":    cert.NotAfter,
		"selfSigned":  cert.Issuer.String() == cert.Subject.String(),
		"namesAHost":  len(cert.DNSNames) > 0 || len(cert.IPAddresses) > 0,
		"explanation": signingExplanation(cert),
	})
}

func signingExplanation(cert *x509.Certificate) string {
	if cert.Issuer.String() == cert.Subject.String() {
		return "This server's certificate is self-signed, so a signature made with it can be checked by anybody " +
			"holding a copy of the certificate and vouched for by nobody. It establishes that a document has not " +
			"changed; it establishes nothing about who this server is."
	}
	return "A signature made with this certificate proves the document passed through this server unaltered. The " +
		"certificate names a host, not a clinician, so it does not assert that anybody reviewed the content."
}
