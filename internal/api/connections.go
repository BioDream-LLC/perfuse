package api

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/store"
)

// connectionCheck is one destination's connection, checked in layers: the name resolves, a TCP connection opens, TLS (when
// used) completes, and the destination's own record of delivering. Each layer that fails says why in words a person who is not
// a network engineer can act on. Nothing is sent: a probe that wrote a message into a partner's system would be a test message
// in production, so the application layer is read from real traffic instead.
type connectionCheck struct {
	Channel     string `json:"channel"`
	Destination string `json:"destination"`
	Type        string `json:"type"`
	Target      string `json:"target"`
	TLS         bool   `json:"tls"`

	DNS     layer `json:"dns"`
	TCP     layer `json:"tcp"`
	TLSStep layer `json:"tlsHandshake"`
	App     layer `json:"application"`

	// Verdict is ok, degraded or down, and Reason the one sentence that explains it.
	Verdict string `json:"verdict"`
	Reason  string `json:"reason"`

	CertificateExpires *time.Time `json:"certificateExpires,omitempty"`
	LastDelivered      *time.Time `json:"lastDelivered,omitempty"`
	LastFailed         *time.Time `json:"lastFailed,omitempty"`
}

type layer struct {
	State  string  `json:"state"` // ok, failed, skipped
	Detail string  `json:"detail,omitempty"`
	Millis float64 `json:"ms,omitempty"`
}

// targetOf is where a destination connects, and whether over TLS. Destinations with no network endpoint (file, database
// DSNs, cloud queues addressed by name) are left out.
func targetOf(d config.Destination) (string, bool) {
	tlsOn := d.TLS != nil && d.TLS.Enabled
	fromURL := func(raw string) (string, bool) {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return "", false
		}
		host := u.Host
		if u.Port() == "" {
			if u.Scheme == "https" {
				host = net.JoinHostPort(u.Hostname(), "443")
			} else {
				host = net.JoinHostPort(u.Hostname(), "80")
			}
		}
		return host, u.Scheme == "https"
	}
	switch {
	case d.Type == config.DestinationMLLP && d.Address != "":
		return d.Address, tlsOn
	case d.Type == config.DestinationHTTP && d.HTTP != nil:
		return fromURL(d.HTTP.URL)
	case d.Type == config.DestinationFHIR && d.FHIR != nil:
		return fromURL(d.FHIR.URL)
	case d.Type == config.DestinationSOAP && d.SOAP != nil:
		return fromURL(d.SOAP.URL)
	case d.Type == config.DestinationSFTP && d.SFTP != nil && d.SFTP.Host != "":
		h := d.SFTP.Host
		if _, _, err := net.SplitHostPort(h); err != nil {
			h = net.JoinHostPort(h, "22")
		}
		return h, false
	case d.Type == config.DestinationDICOM && d.DICOM != nil && d.DICOM.Address != "":
		return d.DICOM.Address, tlsOn
	}
	return "", false
}

func (s *Server) handleConnections(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	rt, ok := s.runtimeFor(w, r, sess)
	if !ok {
		return
	}
	if !s.requireRuntime(w, r) {
		return
	}
	valid, _, err := rt.Repo.List()
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	filter := r.URL.Query().Get("channel")
	var checks []*connectionCheck
	for _, summary := range valid {
		if filter != "" && summary.Name != filter {
			continue
		}
		c, err := rt.Repo.Get(summary.Name)
		if err != nil {
			continue
		}
		for _, d := range c.Destinations {
			target, tlsOn := targetOf(d)
			if target == "" {
				continue
			}
			checks = append(checks, &connectionCheck{Channel: c.Name, Destination: d.Name, Type: string(d.Type), Target: target, TLS: tlsOn})
		}
	}
	last := map[string]msgstore.LastDelivery{}
	if rt.Messages != nil {
		if rows, err := rt.Messages.LastDeliveries(r.Context(), string(sess.TenantID), time.Now().Add(-7*24*time.Hour)); err == nil {
			for _, l := range rows {
				last[l.Channel+"\x00"+l.Destination] = l
			}
		}
	}
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, c := range checks {
		wg.Add(1)
		go func(c *connectionCheck) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			probe(r.Context(), c)
			judge(c, last[c.Channel+"\x00"+c.Destination])
		}(c)
	}
	wg.Wait()
	sort.Slice(checks, func(i, j int) bool {
		if checks[i].Channel != checks[j].Channel {
			return checks[i].Channel < checks[j].Channel
		}
		return checks[i].Destination < checks[j].Destination
	})
	if checks == nil {
		checks = []*connectionCheck{}
	}
	s.ok(w, map[string]any{"connections": checks, "checkedAt": time.Now().UTC()})
}

// probe runs the network layers with short timeouts, so one dead partner cannot hold up the page.
func probe(ctx context.Context, c *connectionCheck) {
	host, port, err := net.SplitHostPort(c.Target)
	if err != nil {
		c.DNS = layer{State: "failed", Detail: "the address is not host:port: " + c.Target}
		c.TCP, c.TLSStep = layer{State: "skipped"}, layer{State: "skipped"}
		return
	}
	start := time.Now()
	dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	addrs, err := net.DefaultResolver.LookupHost(dctx, host)
	cancel()
	if err != nil {
		c.DNS = layer{State: "failed", Detail: plainDNS(host, err)}
		c.TCP, c.TLSStep = layer{State: "skipped"}, layer{State: "skipped"}
		return
	}
	c.DNS = layer{State: "ok", Detail: strings.Join(addrs, ", "), Millis: ms(start)}

	start = time.Now()
	conn, err := (&net.Dialer{Timeout: 4 * time.Second}).DialContext(ctx, "tcp", net.JoinHostPort(host, port))
	if err != nil {
		c.TCP = layer{State: "failed", Detail: plainTCP(c.Target, err), Millis: ms(start)}
		c.TLSStep = layer{State: "skipped"}
		return
	}
	c.TCP = layer{State: "ok", Millis: ms(start)}
	defer func() { _ = conn.Close() }()
	if !c.TLS {
		c.TLSStep = layer{State: "skipped", Detail: "this destination does not use TLS"}
		return
	}
	start = time.Now()
	_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
	tc := tls.Client(conn, &tls.Config{ServerName: host, MinVersion: tls.VersionTLS12})
	if err := tc.HandshakeContext(ctx); err != nil {
		c.TLSStep = layer{State: "failed", Detail: plainTLS(err), Millis: ms(start)}
		return
	}
	state := tc.ConnectionState()
	c.TLSStep = layer{State: "ok", Detail: tls.VersionName(state.Version), Millis: ms(start)}
	if len(state.PeerCertificates) > 0 {
		exp := state.PeerCertificates[0].NotAfter
		c.CertificateExpires = &exp
	}
}

// judge reads the application layer from the destination's own deliveries and decides the verdict.
func judge(c *connectionCheck, l msgstore.LastDelivery) {
	c.LastDelivered, c.LastFailed = l.Delivered, l.Failed
	switch {
	case l.Delivered == nil && l.Failed == nil:
		c.App = layer{State: "skipped", Detail: "no messages in the last seven days"}
	case l.Failed != nil && (l.Delivered == nil || l.Failed.After(*l.Delivered)):
		c.App = layer{State: "failed", Detail: "the last delivery failed: " + l.Error}
	default:
		c.App = layer{State: "ok", Detail: "the last message was delivered and acknowledged"}
	}
	switch {
	case c.DNS.State == "failed":
		c.Verdict, c.Reason = "down", c.DNS.Detail
	case c.TCP.State == "failed":
		c.Verdict, c.Reason = "down", c.TCP.Detail
	case c.TLSStep.State == "failed":
		c.Verdict, c.Reason = "down", c.TLSStep.Detail
	case c.App.State == "failed":
		c.Verdict, c.Reason = "degraded", "The network path is open, but "+c.App.Detail
	case c.CertificateExpires != nil && time.Until(*c.CertificateExpires) < 14*24*time.Hour:
		c.Verdict = "degraded"
		c.Reason = fmt.Sprintf("The partner's certificate expires on %s; ask them to renew it before then.", c.CertificateExpires.Format("2 January 2006"))
	default:
		c.Verdict, c.Reason = "ok", "Reachable, and delivering."
		if c.App.State == "skipped" {
			c.Reason = "Reachable; nothing has been sent to it in the last seven days."
		}
	}
}

func ms(t time.Time) float64 { return float64(time.Since(t).Microseconds()) / 1000 }

func plainDNS(host string, err error) string {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
		return fmt.Sprintf("The name %s does not exist in DNS. Check the spelling, or whether it is only known inside the partner's network (which would need the VPN or their DNS).", host)
	}
	return fmt.Sprintf("The name %s could not be looked up: %v. This is usually this server's DNS, not the partner.", host, err)
}

func plainTCP(target string, err error) string {
	switch {
	case isRefused(err):
		return fmt.Sprintf("%s refused the connection: the host is up, but nothing is listening on that port. The partner's interface engine is probably stopped, or the port is wrong.", target)
	case isUnreachable(err):
		return fmt.Sprintf("There is no route to %s. If this partner is reached over a VPN, the tunnel is probably down.", target)
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return fmt.Sprintf("Connecting to %s timed out. Something between here and there is silently dropping the traffic: a firewall rule that does not allow this server, or a VPN tunnel that is down.", target)
	}
	return fmt.Sprintf("Connecting to %s failed: %v.", target, err)
}

func plainTLS(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "certificate has expired") || strings.Contains(msg, "expired"):
		return "The partner's certificate has expired. Ask them to renew it; nothing here can fix it."
	case strings.Contains(msg, "unknown authority"):
		return "The partner's certificate is from an authority this server does not trust. Add their CA to this destination's TLS settings, or ask which one they use."
	case strings.Contains(msg, "not valid for") || strings.Contains(msg, "doesn't contain any IP SANs"):
		return "The partner's certificate is for a different name than the address configured here. Use the name on the certificate, or ask them to reissue it."
	case strings.Contains(msg, "protocol version"):
		return "The partner only offers a TLS version older than 1.2, which this server refuses."
	}
	return "The TLS handshake failed: " + msg + "."
}
