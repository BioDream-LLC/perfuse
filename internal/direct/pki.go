package direct

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/egress"
)

// Checker decides whether a certificate found for a recipient may be encrypted to: it must chain to a trust anchor,
// fetching intermediate issuers named by its Authority Information Access extension when the chain is not complete, and
// it must not be revoked on its issuers' certificate revocation lists. Both are what the Direct Applicability Statement
// (4.2.2) asks for and what ONC's certificate discovery tool tests (D17, D18).
//
// Revocation fails open: a list that cannot be fetched or read does not stop delivery, because a CRL server that is down
// would otherwise stop all Direct mail to everyone the authority issued for. A certificate listed as revoked, on a list
// its issuer signed, is always refused.
type Checker struct {
	Anchors *x509.CertPool
	// Now is the time certificates are checked at; time.Now when nil.
	Now func() time.Time
	// Client fetches issuers and revocation lists; nil means one that refuses the cloud metadata address (egress).
	Client *http.Client

	mu      sync.Mutex
	issuers map[string]fetched[[]*x509.Certificate]
	crls    map[string]fetched[*x509.RevocationList]
}

type fetched[T any] struct {
	v     T
	until time.Time
}

const (
	pkiFetchLimit = 1 << 20
	issuerTTL     = 6 * time.Hour
	maxIssuerHops = 4
)

func (k *Checker) now() time.Time {
	if k.Now != nil {
		return k.Now()
	}
	return time.Now()
}

func (k *Checker) client() *http.Client {
	if k.Client != nil {
		return k.Client
	}
	return &http.Client{Transport: egress.Transport(), Timeout: 10 * time.Second}
}

// Check returns nil when c may be used, else why not.
func (k *Checker) Check(ctx context.Context, c *x509.Certificate) error {
	opts := x509.VerifyOptions{Roots: k.Anchors, CurrentTime: k.now(), KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageAny},
		Intermediates: x509.NewCertPool()}
	chains, err := c.Verify(opts)
	var unknown x509.UnknownAuthorityError
	if err != nil && errors.As(err, &unknown) {
		// Fetch what the certificate says issued it, and what those say issued them, a few hops up.
		frontier := []*x509.Certificate{c}
		for hop := 0; hop < maxIssuerHops && len(frontier) > 0 && err != nil; hop++ {
			var next []*x509.Certificate
			for _, f := range frontier {
				for _, u := range f.IssuingCertificateURL {
					for _, ic := range k.issuersAt(ctx, u) {
						opts.Intermediates.AddCert(ic)
						next = append(next, ic)
					}
				}
			}
			frontier = next
			chains, err = c.Verify(opts)
		}
	}
	if err != nil {
		return err
	}
	// Any one valid chain whose every link is unrevoked will do.
	var revoked error
	for _, chain := range chains {
		if r := k.revokedIn(ctx, chain); r != nil {
			revoked = r
			continue
		}
		return nil
	}
	return revoked
}

// revokedIn is the first certificate of the chain its issuer's list says is revoked.
func (k *Checker) revokedIn(ctx context.Context, chain []*x509.Certificate) error {
	for i := 0; i+1 < len(chain); i++ {
		cert, issuer := chain[i], chain[i+1]
		for _, u := range cert.CRLDistributionPoints {
			crl := k.crlAt(ctx, u, issuer)
			if crl == nil {
				continue
			}
			for _, e := range crl.RevokedCertificateEntries {
				if e.SerialNumber != nil && e.SerialNumber.Cmp(cert.SerialNumber) == 0 {
					return fmt.Errorf("x509: %s is revoked (since %s, on %s)", cert.Subject.CommonName,
						e.RevocationTime.UTC().Format("2006-01-02"), u)
				}
			}
			break
		}
	}
	return nil
}

func (k *Checker) get(ctx context.Context, u string) ([]byte, error) {
	if !strings.HasPrefix(u, "http://") && !strings.HasPrefix(u, "https://") {
		return nil, fmt.Errorf("only http and https are fetched, not %s", u)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	res, err := k.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s answered %s", u, res.Status)
	}
	b, err := io.ReadAll(io.LimitReader(res.Body, pkiFetchLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > pkiFetchLimit {
		return nil, fmt.Errorf("%s is larger than %d bytes", u, pkiFetchLimit)
	}
	return b, nil
}

// issuersAt fetches the certificates at an AIA caIssuers URL (DER or PEM); none on failure.
func (k *Checker) issuersAt(ctx context.Context, u string) []*x509.Certificate {
	k.mu.Lock()
	if f, ok := k.issuers[u]; ok && k.now().Before(f.until) {
		k.mu.Unlock()
		return f.v
	}
	k.mu.Unlock()
	b, err := k.get(ctx, u)
	var certs []*x509.Certificate
	if err == nil {
		certs = parseCerts(b)
	}
	k.mu.Lock()
	if k.issuers == nil {
		k.issuers = map[string]fetched[[]*x509.Certificate]{}
	}
	ttl := issuerTTL
	if err != nil {
		ttl = 5 * time.Minute
	}
	k.issuers[u] = fetched[[]*x509.Certificate]{v: certs, until: k.now().Add(ttl)}
	k.mu.Unlock()
	return certs
}

// crlAt fetches and checks a revocation list: it must be signed by issuer and still current. nil when it cannot be used.
func (k *Checker) crlAt(ctx context.Context, u string, issuer *x509.Certificate) *x509.RevocationList {
	key := u + "\x00" + string(issuer.Raw)
	k.mu.Lock()
	if f, ok := k.crls[key]; ok && k.now().Before(f.until) {
		k.mu.Unlock()
		return f.v
	}
	k.mu.Unlock()
	var crl *x509.RevocationList
	until := k.now().Add(5 * time.Minute)
	if b, err := k.get(ctx, u); err == nil {
		if p, _ := pem.Decode(b); p != nil {
			b = p.Bytes
		}
		if l, err := x509.ParseRevocationList(b); err == nil && l.CheckSignatureFrom(issuer) == nil &&
			(l.NextUpdate.IsZero() || k.now().Before(l.NextUpdate)) {
			crl = l
			until = k.now().Add(24 * time.Hour)
			if !l.NextUpdate.IsZero() && l.NextUpdate.Before(until) {
				until = l.NextUpdate
			}
		}
	}
	k.mu.Lock()
	if k.crls == nil {
		k.crls = map[string]fetched[*x509.RevocationList]{}
	}
	k.crls[key] = fetched[*x509.RevocationList]{v: crl, until: until}
	k.mu.Unlock()
	return crl
}

func parseCerts(b []byte) []*x509.Certificate {
	var out []*x509.Certificate
	if bytes.Contains(b, []byte("-----BEGIN")) {
		for {
			var p *pem.Block
			p, b = pem.Decode(b)
			if p == nil {
				break
			}
			if c, err := x509.ParseCertificate(p.Bytes); err == nil {
				out = append(out, c)
			}
		}
		return out
	}
	if cs, err := x509.ParseCertificates(b); err == nil {
		out = cs
	}
	return out
}
