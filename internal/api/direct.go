package api

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/direct"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Direct secure messaging: this server's Direct addresses (read from the channels that send or receive Direct) and the
// address book of correspondents, each with the certificate their mail is encrypted to. The book is a directory of PEM
// files, one per address or domain, which is exactly what a direct destination's recipient_certs_dir reads; contacts.json
// beside them holds names and notes.

// directBook is the address book on disk.
type directBook struct {
	mu  sync.Mutex
	dir string
}

// DirectContact is one correspondent.
type DirectContact struct {
	// Address is a Direct address, or a domain for a domain-bound certificate that covers every address at it.
	Address      string `json:"address"`
	Name         string `json:"name,omitempty"`
	Organisation string `json:"organisation,omitempty"`
	Notes        string `json:"notes,omitempty"`
	// Source says where the certificate came from: dns, upload, or none yet.
	Source  string            `json:"source"`
	Added   time.Time         `json:"added"`
	Updated time.Time         `json:"updated"`
	Certs   []directCertShown `json:"certificates"`
}

type directCertShown struct {
	Subject     string    `json:"subject"`
	Issuer      string    `json:"issuer"`
	NotBefore   time.Time `json:"notBefore"`
	NotAfter    time.Time `json:"notAfter"`
	Bound       string    `json:"bound"`
	Fingerprint string    `json:"sha256"`
	Expired     bool      `json:"expired"`
	PEM         string    `json:"pem,omitempty"`
}

var directKeyRE = regexp.MustCompile(`^([a-z0-9!#$%&'*+/=?^_{|}~.-]+@)?[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$`)

func directKey(addr string) (string, error) {
	k := strings.ToLower(strings.TrimSpace(addr))
	if len(k) > 254 || !directKeyRE.MatchString(k) || strings.Contains(k, "..") {
		return "", errors.New("give a Direct address (name@direct.example.org) or a domain (direct.example.org)")
	}
	return k, nil
}

func showCert(c *x509.Certificate, withPEM bool) directCertShown {
	sum := sha256.Sum256(c.Raw)
	bound := strings.Join(c.EmailAddresses, ", ")
	if bound == "" {
		bound = strings.Join(c.DNSNames, ", ")
	}
	out := directCertShown{Subject: c.Subject.CommonName, Issuer: c.Issuer.CommonName, NotBefore: c.NotBefore, NotAfter: c.NotAfter,
		Bound: bound, Fingerprint: hex.EncodeToString(sum[:]), Expired: time.Now().After(c.NotAfter)}
	if out.Subject == "" {
		out.Subject = c.Subject.String()
	}
	if withPEM {
		out.PEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw}))
	}
	return out
}

var errNoDirectBook = errors.New("this server has no Direct address book directory")

func (b *directBook) index() (map[string]DirectContact, error) {
	m := map[string]DirectContact{}
	if b.dir == "" {
		return m, nil
	}
	raw, err := os.ReadFile(filepath.Join(b.dir, "contacts.json"))
	if errors.Is(err, os.ErrNotExist) {
		return m, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, err
	}
	return m, nil
}

func (b *directBook) save(m map[string]DirectContact) error {
	if b.dir == "" {
		return errNoDirectBook
	}
	if err := os.MkdirAll(b.dir, 0o700); err != nil {
		return err
	}
	for k, c := range m {
		c.Certs = nil
		m[k] = c
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := filepath.Join(b.dir, "contacts.json.tmp")
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(b.dir, "contacts.json"))
}

func parsePEMCerts(raw []byte) []*x509.Certificate {
	var out []*x509.Certificate
	for {
		var blk *pem.Block
		blk, raw = pem.Decode(raw)
		if blk == nil {
			return out
		}
		if blk.Type == "CERTIFICATE" {
			if c, err := x509.ParseCertificate(blk.Bytes); err == nil {
				out = append(out, c)
			}
		}
	}
}

func (b *directBook) certs(key string) []*x509.Certificate {
	raw, err := os.ReadFile(filepath.Join(b.dir, key+".pem"))
	if err != nil {
		return nil
	}
	return parsePEMCerts(raw)
}

func (b *directBook) list() ([]DirectContact, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, err := b.index()
	if err != nil {
		return nil, err
	}
	// A PEM file put in the directory by hand is a contact too.
	if b.dir == "" {
		return []DirectContact{}, nil
	}
	if entries, err := os.ReadDir(b.dir); err == nil {
		for _, e := range entries {
			n := e.Name()
			if !strings.HasSuffix(n, ".pem") {
				continue
			}
			k := strings.TrimSuffix(n, ".pem")
			if _, ok := m[k]; !ok {
				if _, err := directKey(k); err == nil {
					m[k] = DirectContact{Address: k, Source: "file"}
				}
			}
		}
	}
	out := make([]DirectContact, 0, len(m))
	for k, c := range m {
		c.Address = k
		for _, cert := range b.certs(k) {
			c.Certs = append(c.Certs, showCert(cert, false))
		}
		if c.Certs == nil {
			c.Certs = []directCertShown{}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out, nil
}

type directContactIn struct {
	Name         string `json:"name"`
	Organisation string `json:"organisation"`
	Notes        string `json:"notes"`
	// Certificate is PEM; empty keeps the one held. LookUp fetches it from DNS instead.
	Certificate string `json:"certificate"`
	LookUp      bool   `json:"lookUp"`
	DNS         string `json:"dns"`
}

type directAddressShown struct {
	Address string `json:"address"`
	Channel string `json:"channel"`
	// Role is send (a direct destination) or receive (a direct source).
	Role        string            `json:"role"`
	Destination string            `json:"destination,omitempty"`
	Listen      string            `json:"listen,omitempty"`
	HISP        string            `json:"hisp,omitempty"`
	To          []string          `json:"to,omitempty"`
	Cert        *directCertShown  `json:"certificate,omitempty"`
	Anchors     []directCertShown `json:"anchors,omitempty"`
	Problem     string            `json:"problem,omitempty"`
}

func (s *Server) registerDirect(mux *recordingMux) {
	mux.Handle("GET /api/direct", s.require(store.RoleViewer, s.handleDirect))
	mux.Handle("PUT /api/direct/contacts/{address}", s.require(store.RoleEditor, s.handleDirectPut))
	mux.Handle("DELETE /api/direct/contacts/{address}", s.require(store.RoleEditor, s.handleDirectDelete))
	mux.Handle("GET /api/direct/contacts/{address}/certificate", s.require(store.RoleViewer, s.handleDirectCert))
	mux.Handle("POST /api/direct/lookup", s.require(store.RoleViewer, s.handleDirectLookup))
}

func (s *Server) directBook() *directBook {
	s.directOnce.Do(func() { s.directBk = &directBook{dir: s.DirectDir} })
	return s.directBk
}

// directLookup finds a correspondent's certificates in DNS, then in LDAP, and says where; swapped in tests.
var directLookup = func(ctx context.Context, address, dns string) ([]*x509.Certificate, string, error) {
	return direct.FindCertificates(ctx, address, dns, "")
}

func readAnchors(path string) ([]directCertShown, string) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "the trust anchors cannot be read: " + err.Error()
	}
	var out []directCertShown
	for _, c := range parsePEMCerts(raw) {
		out = append(out, showCert(c, false))
	}
	if len(out) == 0 {
		return nil, "the trust anchors file holds no certificate"
	}
	return out, ""
}

func addressShown(a config.DirectAddress) directAddressShown {
	out := directAddressShown{Address: a.Address}
	if raw, err := os.ReadFile(a.CertFile); err != nil {
		out.Problem = "the certificate cannot be read: " + err.Error()
	} else if certs := parsePEMCerts(raw); len(certs) == 0 {
		out.Problem = "the certificate file holds no certificate"
	} else {
		c := showCert(certs[0], false)
		out.Cert = &c
		if c.Expired {
			out.Problem = "the certificate expired on " + c.NotAfter.Format("2 January 2006")
		}
	}
	anchors, problem := readAnchors(a.AnchorsFile)
	out.Anchors = anchors
	if out.Problem == "" {
		out.Problem = problem
	}
	return out
}

func (s *Server) handleDirect(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var addresses []directAddressShown
	if channels, ok := s.channelsFor(w, r, sess); ok {
		valid, _, err := channels.List()
		if err == nil {
			valid, _ = visibleSummaries(r, valid, nil)
			for _, sum := range valid {
				c, err := channels.Get(sum.Name)
				if err != nil {
					continue
				}
				if c.Source.Type == config.SourceDirect && c.Source.Direct != nil {
					a := addressShown(c.Source.Direct.DirectAddress)
					a.Channel, a.Role, a.Listen, a.HISP = c.Name, "receive", c.Source.Direct.Listen, c.Source.Direct.SMTPHost
					addresses = append(addresses, a)
				}
				for _, d := range c.Destinations {
					if d.Type == config.DestinationDirect && d.Direct != nil {
						a := addressShown(d.Direct.DirectAddress)
						a.Channel, a.Role, a.Destination, a.HISP, a.To = c.Name, "send", d.Name, d.Direct.SMTPHost, d.Direct.To
						addresses = append(addresses, a)
					}
				}
			}
		}
	} else {
		return
	}
	contacts, err := s.directBook().list()
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if addresses == nil {
		addresses = []directAddressShown{}
	}
	s.ok(w, map[string]any{"addresses": addresses, "contacts": contacts, "certsDir": s.DirectDir})
}

func (s *Server) handleDirectPut(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	key, err := directKey(r.PathValue("address"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	var in directContactIn
	if !s.decode(w, r, &in) {
		return
	}
	if s.DirectDir == "" {
		s.fail(w, r, http.StatusServiceUnavailable, errNoDirectBook.Error())
		return
	}
	if len(in.Name) > 200 || len(in.Organisation) > 200 || len(in.Notes) > 2000 {
		s.fail(w, r, http.StatusBadRequest, "a name or organisation is at most 200 characters, notes at most 2,000")
		return
	}
	var certs []*x509.Certificate
	source := ""
	switch {
	case in.LookUp:
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		lookupAddr := key
		if !strings.Contains(key, "@") {
			lookupAddr = "postmaster@" + key
		}
		if msg := checkDNSServer(in.DNS); msg != "" {
			s.fail(w, r, http.StatusBadRequest, msg)
			return
		}
		var from string
		certs, from, err = directLookup(ctx, lookupAddr, strings.TrimSpace(in.DNS))
		if err != nil {
			// The lookup's own failure (no answer, a refused query) is about what was asked, not a fault here.
			s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
			return
		}
		if len(certs) == 0 {
			s.fail(w, r, http.StatusNotFound, "neither DNS nor LDAP publishes a certificate for "+key+"; ask the correspondent's HISP for it "+
				"and upload it")
			return
		}
		source = from
	case strings.TrimSpace(in.Certificate) != "":
		certs = parsePEMCerts([]byte(in.Certificate))
		if len(certs) == 0 {
			s.fail(w, r, http.StatusBadRequest, "the certificate is not PEM (-----BEGIN CERTIFICATE-----)")
			return
		}
		source = "upload"
	}
	for _, c := range certs {
		if !directCertCovers(c, key) {
			s.fail(w, r, http.StatusBadRequest, "the certificate "+c.Subject.CommonName+" is not for "+key+
				" (it names "+showCert(c, false).Bound+"); mail encrypted to it could not be read there")
			return
		}
	}
	b := s.directBook()
	b.mu.Lock()
	m, err := b.index()
	if err == nil {
		c := m[key]
		now := time.Now().UTC()
		if c.Added.IsZero() {
			c.Added = now
		}
		c.Updated, c.Name, c.Organisation, c.Notes = now, strings.TrimSpace(in.Name), strings.TrimSpace(in.Organisation), strings.TrimSpace(in.Notes)
		if source != "" {
			c.Source = source
		} else if c.Source == "" {
			c.Source = "none"
		}
		m[key] = c
		if len(certs) > 0 {
			var buf []byte
			for _, cert := range certs {
				buf = append(buf, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})...)
			}
			if err = os.MkdirAll(b.dir, 0o700); err == nil {
				err = os.WriteFile(filepath.Join(b.dir, key+".pem"), buf, 0o600)
			}
		}
		if err == nil {
			err = b.save(m)
		}
	}
	b.mu.Unlock()
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "direct.contact.save", Target: key, Detail: source, IP: clientIP(r)})
	list, _ := b.list()
	for _, c := range list {
		if c.Address == key {
			s.ok(w, c)
			return
		}
	}
	s.ok(w, map[string]string{"address": key})
}

// directCertCovers says whether a certificate is for the address (its e-mail name) or the domain (its DNS name), or, for
// a domain entry, for that domain.
func directCertCovers(c *x509.Certificate, key string) bool {
	domain := key[strings.LastIndexByte(key, '@')+1:]
	for _, e := range c.EmailAddresses {
		if strings.EqualFold(e, key) {
			return true
		}
	}
	for _, d := range c.DNSNames {
		if strings.EqualFold(d, domain) {
			return true
		}
	}
	return false
}

func (s *Server) handleDirectDelete(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	key, err := directKey(r.PathValue("address"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	b := s.directBook()
	b.mu.Lock()
	m, err := b.index()
	found := false
	if err == nil {
		_, found = m[key]
		delete(m, key)
		if rmErr := os.Remove(filepath.Join(b.dir, key+".pem")); rmErr == nil {
			found = true
		}
		if found {
			err = b.save(m)
		}
	}
	b.mu.Unlock()
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	if !found {
		s.fail(w, r, http.StatusNotFound, key+" is not in the address book")
		return
	}
	_ = s.Store.Audit(r.Context(), store.AuditEntry{Username: sess.Username, Action: "direct.contact.delete", Target: key, IP: clientIP(r)})
	s.ok(w, map[string]string{"deleted": key})
}

func (s *Server) handleDirectCert(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	key, err := directKey(r.PathValue("address"))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}
	raw, err := os.ReadFile(filepath.Join(s.DirectDir, key+".pem"))
	if err != nil {
		s.fail(w, r, http.StatusNotFound, "no certificate is held for "+key)
		return
	}
	w.Header().Set("Content-Type", "application/x-pem-file")
	w.Header().Set("Content-Disposition", `attachment; filename="`+key+`.pem"`)
	_, _ = w.Write(raw)
}

// checkDNSServer says what is wrong with a DNS server given as host:port, or "" when it is empty or well formed.
func checkDNSServer(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(v)
	if err != nil || host == "" || port == "" || !regexp.MustCompile(`^[A-Za-z0-9.:-]+$`).MatchString(host) {
		return "the DNS server is host:port, such as 1.1.1.1:53"
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return "the DNS server's port is a number from 1 to 65535"
	}
	return ""
}

type directLookupIn struct {
	Address string `json:"address"`
	DNS     string `json:"dns"`
}

// handleDirectLookup asks DNS, then LDAP, for an address's certificates without saving them: what the book would get.
func (s *Server) handleDirectLookup(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var in directLookupIn
	if !s.decode(w, r, &in) {
		return
	}
	key, err := directKey(in.Address)
	if err != nil || !strings.Contains(key, "@") {
		s.fail(w, r, http.StatusBadRequest, "give a Direct address, such as name@direct.example.org")
		return
	}
	if msg := checkDNSServer(in.DNS); msg != "" {
		s.fail(w, r, http.StatusBadRequest, msg)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	certs, from, err := directLookup(ctx, key, strings.TrimSpace(in.DNS))
	if err != nil {
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}
	shown := []directCertShown{}
	for _, c := range certs {
		shown = append(shown, showCert(c, true))
	}
	s.ok(w, map[string]any{"address": key, "certificates": shown, "from": from})
}
