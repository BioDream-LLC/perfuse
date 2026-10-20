package engine

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/cda"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
	"github.com/biodream-llc/perfuse/internal/xds"
)

// XDSSender publishes each clinical document to an XDS.b repository with ITI-41. The repository's RegistryResponse
// decides: a Failure, or an error inside a PartialSuccess, is a failed delivery with the registry's error codes.
type XDSSender struct {
	cfg    *config.XDSDestination
	client *xds.Client
}

// NewXDSSender builds the sender.
func NewXDSSender(d config.Destination) (*XDSSender, error) {
	cfg := d.XDS
	if cfg == nil {
		return nil, fmt.Errorf("destination %q is an xds destination with no xds block", d.Name)
	}
	hc := &http.Client{Timeout: cfg.Timeout}
	if cfg.TLS != nil {
		tlsCfg, err := tlsconf.ForSender(cfg.TLS)
		if err != nil {
			return nil, err
		}
		hc.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	}
	headers := map[string]string{}
	// Credentials may be ${ENV} references, as on every other destination; they were sent as written, so a token kept out of
	// the channel file the documented way reached the repository as the literal text "${NAME}".
	for k, v := range cfg.Headers {
		headers[k] = config.ResolveSecret(v)
	}
	if token := config.ResolveSecret(cfg.Token); token != "" {
		headers["Authorization"] = "Bearer " + token
	}
	c := &xds.Client{HTTP: hc, Headers: headers}
	if cfg.DirectFrom != "" {
		// XDR as Direct's transport: the addressing block says who it is from and for.
		c.SOAPHeaders = xds.DirectAddressBlock(cfg.DirectFrom, cfg.DirectTo)
	}
	if user := config.ResolveSecret(cfg.Username); user != "" {
		hc.Transport = basicAuth{next: transportOf(hc), user: user, pass: config.ResolveSecret(cfg.Password)}
	}
	return &XDSSender{cfg: cfg, client: c}, nil
}

type basicAuth struct {
	next       http.RoundTripper
	user, pass string
}

func (b basicAuth) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.SetBasicAuth(b.user, b.pass)
	return b.next.RoundTrip(r)
}

func transportOf(hc *http.Client) http.RoundTripper {
	if hc.Transport != nil {
		return hc.Transport
	}
	return http.DefaultTransport
}

// document finds the CDA document a message is or carries.
func xdsDocument(msg []byte) (*cda.Document, []byte, error) {
	trimmed := bytes.TrimSpace(msg)
	if bytes.HasPrefix(trimmed, []byte("<")) {
		d, err := cda.Parse(trimmed)
		if err != nil {
			return nil, nil, fmt.Errorf("an XDS repository stores clinical documents, and this XML is not CDA: %w", err)
		}
		return d, trimmed, nil
	}
	m, err := hl7.Parse(msg)
	if err != nil {
		return nil, nil, fmt.Errorf("the message is neither a CDA document nor an HL7 v2 message carrying one")
	}
	d, embedded, err := cda.ParseEmbedded(m)
	if err != nil {
		return nil, nil, err
	}
	if d == nil || len(embedded) == 0 {
		return nil, nil, fmt.Errorf("the HL7 v2 message carries no CDA document")
	}
	return d, embedded[0].Data, nil
}

func code(c config.XDSCode) xds.Code {
	return xds.Code{Code: c.Code, Scheme: c.Scheme, Display: c.Display}
}

// Send publishes one document.
func (s *XDSSender) Send(ctx context.Context, msg []byte) error {
	d, raw, err := xdsDocument(msg)
	if err != nil {
		return err
	}
	o := xds.CDAOptions{SourceID: s.cfg.SourceID, PatientDomain: s.cfg.PatientDomain, FacilityType: code(s.cfg.FacilityType),
		PracticeSetting: code(s.cfg.PracticeSetting)}
	if s.cfg.Class != nil {
		c := code(*s.cfg.Class)
		o.Class = &c
	}
	if s.cfg.ContentType != nil {
		c := code(*s.cfg.ContentType)
		o.ContentType = &c
	}
	meta, err := xds.MetaFromCDA(d, raw, o)
	if err != nil {
		return err
	}
	for _, to := range s.cfg.DirectTo {
		// XON|XCN|XTN, the recipient as a telecom address of type Internet: how XDR for Direct names a Direct address.
		meta.IntendedRecipients = append(meta.IntendedRecipients, "||^^Internet^"+to)
	}
	req, err := xds.Build(meta, time.Now())
	if err != nil {
		return err
	}
	rr, err := s.client.ProvideAndRegister(ctx, s.cfg.URL, req, map[string][]byte{"Document01": raw})
	if err != nil {
		return err
	}
	if rr.Failed() {
		return fmt.Errorf("the repository refused the document: %s", rr.Summary())
	}
	return nil
}

// Describe names the destination, never the credentials.
func (s *XDSSender) Describe() string {
	if s.cfg.DirectFrom != "" {
		return s.cfg.URL + " (XDR to " + strings.Join(s.cfg.DirectTo, ", ") + ")"
	}
	return s.cfg.URL + " (XDS.b ITI-41)"
}

// Close releases nothing.
func (s *XDSSender) Close() error { return nil }
