package tlsconf

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"strings"
	"time"
)

// Describing certificates.
//
// An expired certificate on an interface feed is a real hospital outage, and it
// happens because nobody was told. The renewal was somebody's job three months
// ago, that person has moved on, and the first sign of trouble is a sender
// reporting that messages stopped.
//
// So this reports days remaining rather than a date, because "expires 2026-11-04"
// requires arithmetic and "expires in 9 days" does not.

// Certificate describes one certificate in terms somebody can act on.
type Certificate struct {
	Subject      string    `json:"subject"`
	Issuer       string    `json:"issuer"`
	SerialNumber string    `json:"serialNumber"`
	NotBefore    time.Time `json:"notBefore"`
	NotAfter     time.Time `json:"notAfter"`

	// DaysRemaining is negative once expired.
	DaysRemaining int `json:"daysRemaining"`

	// Status is one of ok, expiring, expired or not-yet-valid.
	Status string `json:"status"`

	// Hosts are the subject alternative names, which are what verification actually
	// uses. The common name has not counted for years and reporting only that is
	// how somebody concludes a certificate should work when it cannot.
	Hosts []string `json:"hosts,omitempty"`

	// SelfSigned is worth knowing: it means the other end must be configured to
	// trust this specific certificate rather than an authority.
	SelfSigned bool `json:"selfSigned"`

	// IsCA marks an authority certificate.
	IsCA bool `json:"isCa"`

	// KeyType and KeyBits describe the key.
	KeyType string `json:"keyType"`
	KeyBits int    `json:"keyBits,omitempty"`

	// Notes are problems that will not stop it working today.
	Notes []string `json:"notes,omitempty"`
}

// Thresholds for the status field.
const (
	// ExpiringSoonDays is when a certificate starts being reported as a problem.
	// Thirty days is long enough to get a renewal through a hospital change process,
	// which is the actual constraint rather than anything technical.
	ExpiringSoonDays = 30
)

// Describe reads a PEM file and describes every certificate in it.
//
// Every certificate, not just the first, because a file usually holds a chain and
// an intermediate expiring breaks the connection just as thoroughly as the leaf.
func Describe(path string) ([]Certificate, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return DescribePEM(data)
}

// DescribePEM describes certificates in PEM bytes.
func DescribePEM(data []byte) ([]Certificate, error) {
	var out []Certificate
	rest := data
	now := time.Now()

	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			// A private key in the same file is normal and is not an error, but it is
			// also not something to describe.
			continue
		}

		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parsing a certificate: %w", err)
		}
		out = append(out, describeOne(cert, now))
	}

	if len(out) == 0 {
		return nil, fmt.Errorf("no certificates found; is this a PEM file?")
	}
	return out, nil
}

func describeOne(cert *x509.Certificate, now time.Time) Certificate {
	remaining := int(cert.NotAfter.Sub(now).Hours() / 24)

	c := Certificate{
		Subject:       nameOf(cert.Subject.String()),
		Issuer:        nameOf(cert.Issuer.String()),
		SerialNumber:  cert.SerialNumber.String(),
		NotBefore:     cert.NotBefore,
		NotAfter:      cert.NotAfter,
		DaysRemaining: remaining,
		IsCA:          cert.IsCA,
		SelfSigned:    cert.Subject.String() == cert.Issuer.String(),
	}

	c.Hosts = append(c.Hosts, cert.DNSNames...)
	for _, ip := range cert.IPAddresses {
		c.Hosts = append(c.Hosts, ip.String())
	}
	for _, uri := range cert.URIs {
		c.Hosts = append(c.Hosts, uri.String())
	}

	switch {
	case now.Before(cert.NotBefore):
		c.Status = "not-yet-valid"
	case remaining < 0:
		c.Status = "expired"
	case remaining <= ExpiringSoonDays:
		c.Status = "expiring"
	default:
		c.Status = "ok"
	}

	switch key := cert.PublicKey.(type) {
	case *rsa.PublicKey:
		c.KeyType = "RSA"
		c.KeyBits = key.N.BitLen()
		if c.KeyBits < 2048 {
			c.Notes = append(c.Notes, fmt.Sprintf(
				"the key is %d bits, below the 2048 minimum most systems now enforce; "+
					"connections may be refused by the other end even though this end is happy",
				c.KeyBits))
		}
	case *ecdsa.PublicKey:
		c.KeyType = "ECDSA"
		c.KeyBits = key.Curve.Params().BitSize
	case ed25519.PublicKey:
		c.KeyType = "Ed25519"
	default:
		c.KeyType = "unknown"
	}

	if len(c.Hosts) == 0 && !cert.IsCA {
		// Verification uses subject alternative names and has done for years. A
		// certificate with none cannot verify against any hostname, whatever its
		// common name says.
		c.Notes = append(c.Notes, "the certificate has no subject alternative names, "+
			"so name verification cannot succeed against it. The common name is not "+
			"used for this any more")
	}

	if c.SelfSigned && !cert.IsCA {
		c.Notes = append(c.Notes, "self-signed, so the other end has to be configured "+
			"to trust this exact certificate rather than an authority")
	}

	// Said in years because a certificate valid for a decade is a decision somebody
	// made, usually to avoid renewing it, and it is worth surfacing.
	if years := cert.NotAfter.Sub(cert.NotBefore).Hours() / 24 / 365; years > 5 {
		c.Notes = append(c.Notes, fmt.Sprintf(
			"valid for about %.0f years, which many systems now reject outright", years))
	}

	return c
}

// nameOf shortens an X.509 distinguished name to the common name where there is
// one, because the full form is unreadable in a table.
func nameOf(dn string) string {
	for _, part := range strings.Split(dn, ",") {
		part = strings.TrimSpace(part)
		if after, ok := strings.CutPrefix(part, "CN="); ok {
			return after
		}
	}
	return dn
}

// Summary describes the certificates a set of settings uses.
type Summary struct {
	Enabled bool `json:"enabled"`

	// Certificates is what this end presents.
	Certificates []Certificate `json:"certificates,omitempty"`

	// Authorities is what it verifies against.
	Authorities []Certificate `json:"authorities,omitempty"`

	// MutualTLS reports whether client certificates are required and verified.
	MutualTLS bool `json:"mutualTls"`

	// MinVersion is the negotiated floor.
	MinVersion string `json:"minVersion"`

	// Warnings are the things worth saying out loud.
	Warnings []string `json:"warnings,omitempty"`

	// Problems stop it working.
	Problems []string `json:"problems,omitempty"`
}

// Summarise describes a set of settings, reading the files it names.
func Summarise(s *Settings, forListener bool) Summary {
	sum := Summary{
		Enabled:   s.IsEnabled(),
		MutualTLS: forListener && s != nil && s.RequireClientCert && s.CAFile != "",
		Warnings:  s.Warnings(forListener),
	}
	if !sum.Enabled {
		return sum
	}

	sum.MinVersion = "1.2"
	if s.MinVersion != "" {
		sum.MinVersion = s.MinVersion
	}

	for _, err := range s.Validate(forListener) {
		sum.Problems = append(sum.Problems, err.Error())
	}

	if s.CertFile != "" {
		certs, err := Describe(s.CertFile)
		if err != nil {
			sum.Problems = append(sum.Problems, fmt.Sprintf("%s: %v", s.CertFile, err))
		} else {
			sum.Certificates = certs
		}
	}
	if s.CAFile != "" {
		certs, err := Describe(s.CAFile)
		if err != nil {
			sum.Problems = append(sum.Problems, fmt.Sprintf("%s: %v", s.CAFile, err))
		} else {
			sum.Authorities = certs
		}
	}

	// Expiry is promoted from a field on a certificate to a warning on the whole
	// connection, because it is the single most common cause of an interface
	// stopping and it should not be something you have to go looking for.
	for _, c := range append(append([]Certificate{}, sum.Certificates...), sum.Authorities...) {
		switch c.Status {
		case "expired":
			sum.Problems = append(sum.Problems, fmt.Sprintf(
				"the certificate for %s expired %d days ago; connections are failing now",
				c.Subject, -c.DaysRemaining))
		case "expiring":
			sum.Warnings = append(sum.Warnings, fmt.Sprintf(
				"the certificate for %s expires in %d days", c.Subject, c.DaysRemaining))
		case "not-yet-valid":
			sum.Problems = append(sum.Problems, fmt.Sprintf(
				"the certificate for %s is not valid until %s; check this machine's clock",
				c.Subject, c.NotBefore.Format("2 January 2006")))
		}
	}

	return sum
}
