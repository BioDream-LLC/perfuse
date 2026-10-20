package direct

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/ldap"
)

// LDAPCertificates finds the certificates published for a Direct address in its domain's LDAP directory: the second
// discovery method of the Direct Project's certificate discovery guide, after DNS CERT records. The directory is server
// (host:port) when given, else the ones the domain's _ldap._tcp SRV records name, in their priority order. Each is asked
// anonymously for its naming contexts, then, under each, for an entry whose mail is the address and its
// userSMIMECertificate (or userCertificate); with none, for an entry whose mail is the domain (a domain-bound
// certificate). Only certificates are read, so the bind is anonymous and the connection may be plain: what is found is
// checked against the trust anchors afterwards, exactly like a certificate found in DNS.
func LDAPCertificates(ctx context.Context, address, server string) ([]*x509.Certificate, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndexByte(address, '@')
	if at < 0 {
		return nil, fmt.Errorf("direct: %q is not a Direct address", address)
	}
	domain := address[at+1:]
	servers := []string{}
	if server != "" {
		servers = append(servers, server)
	} else {
		_, srvs, err := net.DefaultResolver.LookupSRV(ctx, "ldap", "tcp", domain)
		if err != nil || len(srvs) == 0 {
			return nil, fmt.Errorf("direct: %s publishes no LDAP directory (no _ldap._tcp.%s SRV record)", domain, domain)
		}
		sort.SliceStable(srvs, func(i, j int) bool { return srvs[i].Priority < srvs[j].Priority })
		for _, s := range srvs {
			servers = append(servers, net.JoinHostPort(strings.TrimSuffix(s.Target, "."), strconv.Itoa(int(s.Port))))
		}
	}
	var errs []string
	for _, addr := range servers {
		certs, err := ldapLookup(ctx, addr, address, domain)
		if err == nil && len(certs) > 0 {
			return certs, nil
		}
		if err == nil {
			err = errors.New("no certificate for " + address + " or " + domain)
		}
		errs = append(errs, addr+": "+err.Error())
	}
	return nil, fmt.Errorf("direct: no certificate in LDAP (%s)", strings.Join(errs, "; "))
}

// certAttributes are where a directory keeps a person's or a domain's certificate, in the order the guide gives.
var certAttributes = []string{"userSMIMECertificate", "userCertificate;binary", "userCertificate"}

func ldapLookup(ctx context.Context, server, address, domain string) ([]*x509.Certificate, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	conn, err := ldap.Dial(ctx, ldap.Options{Addr: server, Insecure: true, Timeout: 10 * time.Second})
	if err != nil {
		return nil, err
	}
	defer conn.Close()
	if err := conn.BindAnonymous(); err != nil {
		return nil, err
	}
	root, err := conn.Search(ldap.SearchRequest{BaseDN: "", Scope: ldap.ScopeBase, Filter: "(objectClass=*)",
		Attributes: []string{"namingContexts"}, SizeLimit: 1, TimeLimit: 10})
	if err != nil {
		return nil, fmt.Errorf("reading the directory's naming contexts: %w", err)
	}
	var bases []string
	for _, e := range root {
		bases = append(bases, e.GetAll("namingContexts")...)
	}
	if len(bases) == 0 {
		return nil, errors.New("the directory names no naming contexts to search")
	}
	for _, who := range []string{address, domain} {
		var found []*x509.Certificate
		for _, base := range bases {
			entries, err := conn.Search(ldap.SearchRequest{BaseDN: base, Scope: ldap.ScopeSubtree,
				Filter: "(mail=" + ldap.EscapeFilter(who) + ")", Attributes: certAttributes, SizeLimit: 20, TimeLimit: 10})
			if err != nil {
				continue
			}
			for _, e := range entries {
				for _, a := range certAttributes {
					for _, v := range e.GetAll(a) {
						if c, err := x509.ParseCertificate([]byte(v)); err == nil {
							found = append(found, c)
						}
					}
				}
			}
		}
		if len(found) > 0 {
			return found, nil
		}
	}
	return nil, nil
}

// FindCertificates looks a recipient's certificates up the ways the certificate discovery guide allows, DNS first, then
// LDAP; ldapServer "off" skips the directory. It says where they were found.
func FindCertificates(ctx context.Context, address, dnsServer, ldapServer string) ([]*x509.Certificate, string, error) {
	certs, err := DNSCertificates(ctx, address, dnsServer)
	if err == nil && len(certs) > 0 {
		return certs, "dns", nil
	}
	if strings.EqualFold(strings.TrimSpace(ldapServer), "off") {
		if err == nil {
			err = fmt.Errorf("direct: no certificate for %s in DNS", address)
		}
		return nil, "", err
	}
	lc, lerr := LDAPCertificates(ctx, address, strings.TrimSpace(ldapServer))
	if lerr == nil {
		return lc, "ldap", nil
	}
	if err == nil {
		err = fmt.Errorf("direct: no certificate for %s in DNS", address)
	}
	return nil, "", fmt.Errorf("%v; %v", err, lerr)
}
