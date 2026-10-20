package direct

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"time"
)

// Certificate discovery: a recipient's Direct certificate is published in DNS as a CERT record (RFC 4398), under the address
// with its @ replaced by a dot (an address-bound certificate) or under the domain (a domain-bound one). The standard
// library's resolver cannot ask for CERT records, so this is a minimal DNS client for that one query, over UDP and, when
// the answer is truncated, TCP.

const typeCERT = 37

// DNSCertificates looks up the certificates for a Direct address: the address-bound ones, else the domain's. server is a
// resolver (host:port); empty uses the system's first nameserver.
func DNSCertificates(ctx context.Context, address, server string) ([]*x509.Certificate, error) {
	address = strings.ToLower(strings.TrimSpace(address))
	at := strings.LastIndexByte(address, '@')
	if at <= 0 || at == len(address)-1 {
		return nil, fmt.Errorf("direct: %q is not a Direct address", address)
	}
	if server == "" {
		server = systemResolver()
	}
	local, domain := address[:at], address[at+1:]
	// A failed answer for the address-bound name is not the end: some servers (the ONC certificate discovery tool's
	// among them) answer SERVFAIL for a name they hold nothing for, and the domain-bound certificate must still be found.
	var first error
	for _, name := range []string{local + "." + domain, domain} {
		certs, err := lookupCERT(ctx, name, server)
		if err != nil {
			if first == nil {
				first = err
			}
			continue
		}
		if len(certs) > 0 {
			return certs, nil
		}
	}
	return nil, first
}

func systemResolver() string {
	if b, err := readFile("/etc/resolv.conf"); err == nil {
		for _, l := range strings.Split(string(b), "\n") {
			f := strings.Fields(l)
			if len(f) >= 2 && f[0] == "nameserver" {
				return net.JoinHostPort(f[1], "53")
			}
		}
	}
	return "8.8.8.8:53"
}

func encodeName(name string) ([]byte, error) {
	var out []byte
	for _, label := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		if len(label) == 0 || len(label) > 63 {
			return nil, fmt.Errorf("direct: %q is not a DNS name", name)
		}
		out = append(out, byte(len(label)))
		out = append(out, label...)
	}
	return append(out, 0), nil
}

func query(name string, id uint16) ([]byte, error) {
	qn, err := encodeName(name)
	if err != nil {
		return nil, err
	}
	msg := make([]byte, 12)
	binary.BigEndian.PutUint16(msg[0:], id)
	binary.BigEndian.PutUint16(msg[2:], 0x0100) // recursion desired
	binary.BigEndian.PutUint16(msg[4:], 1)
	msg = append(msg, qn...)
	msg = binary.BigEndian.AppendUint16(msg, typeCERT)
	msg = binary.BigEndian.AppendUint16(msg, 1)
	return msg, nil
}

func lookupCERT(ctx context.Context, name, server string) ([]*x509.Certificate, error) {
	var idb [2]byte
	_, _ = rand.Read(idb[:])
	id := binary.BigEndian.Uint16(idb[:])
	q, err := query(name, id)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var d net.Dialer
	conn, err := d.DialContext(ctx, "udp", server)
	if err != nil {
		return nil, fmt.Errorf("direct: the DNS server %s cannot be reached: %w", server, err)
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(dl)
	}
	if _, err := conn.Write(q); err != nil {
		return nil, err
	}
	buf := make([]byte, 65535)
	n, err := conn.Read(buf)
	if err != nil {
		return nil, fmt.Errorf("direct: no answer from the DNS server %s: %w", server, err)
	}
	resp := buf[:n]
	if len(resp) >= 4 && resp[2]&0x02 != 0 {
		// Truncated: certificates are large, so ask again over TCP.
		tc, err := d.DialContext(ctx, "tcp", server)
		if err != nil {
			return nil, err
		}
		defer tc.Close()
		if dl, ok := ctx.Deadline(); ok {
			_ = tc.SetDeadline(dl)
		}
		framed := binary.BigEndian.AppendUint16(nil, uint16(len(q)))
		if _, err := tc.Write(append(framed, q...)); err != nil {
			return nil, err
		}
		var lb [2]byte
		if _, err := readFull(tc, lb[:]); err != nil {
			return nil, err
		}
		resp = make([]byte, binary.BigEndian.Uint16(lb[:]))
		if _, err := readFull(tc, resp); err != nil {
			return nil, err
		}
	}
	return parseCERT(resp, id)
}

func readFull(c net.Conn, b []byte) (int, error) {
	n := 0
	for n < len(b) {
		m, err := c.Read(b[n:])
		n += m
		if err != nil {
			return n, err
		}
	}
	return n, nil
}

func skipName(msg []byte, off int) (int, error) {
	for {
		if off >= len(msg) {
			return 0, errors.New("short name")
		}
		l := int(msg[off])
		switch {
		case l == 0:
			return off + 1, nil
		case l&0xC0 == 0xC0:
			return off + 2, nil
		}
		off += 1 + l
	}
}

// parseCERT reads the CERT records of a DNS answer. Only PKIX (type 1) records are certificates; a URL (type 253 IPKIX)
// is not followed.
func parseCERT(msg []byte, id uint16) ([]*x509.Certificate, error) {
	if len(msg) < 12 || binary.BigEndian.Uint16(msg[0:]) != id {
		return nil, errors.New("direct: the DNS answer does not match the question")
	}
	rcode := msg[3] & 0x0F
	if rcode == 3 { // NXDOMAIN: no such name, so no certificate
		return nil, nil
	}
	if rcode != 0 {
		return nil, fmt.Errorf("direct: the DNS server answered with error %d", rcode)
	}
	qd, an := int(binary.BigEndian.Uint16(msg[4:])), int(binary.BigEndian.Uint16(msg[6:]))
	off := 12
	var err error
	for i := 0; i < qd; i++ {
		if off, err = skipName(msg, off); err != nil {
			return nil, err
		}
		off += 4
	}
	var out []*x509.Certificate
	for i := 0; i < an; i++ {
		if off, err = skipName(msg, off); err != nil {
			return nil, err
		}
		if off+10 > len(msg) {
			return nil, errors.New("direct: a short DNS answer")
		}
		rtype := binary.BigEndian.Uint16(msg[off:])
		rdlen := int(binary.BigEndian.Uint16(msg[off+8:]))
		off += 10
		if off+rdlen > len(msg) {
			return nil, errors.New("direct: a short DNS record")
		}
		rd := msg[off : off+rdlen]
		off += rdlen
		if rtype != typeCERT || len(rd) < 5 {
			continue
		}
		if binary.BigEndian.Uint16(rd[0:]) != 1 { // PKIX
			continue
		}
		c, err := x509.ParseCertificate(rd[5:])
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

func readFile(p string) ([]byte, error) { return os.ReadFile(p) }
