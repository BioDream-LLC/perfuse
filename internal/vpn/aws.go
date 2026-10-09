package vpn

import (
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/awsv4"
)

// AWSSource reads an AWS Site-to-Site VPN connection with EC2 DescribeVpnConnections, signed with our own SigV4. Credentials
// come from AWS_ACCESS_KEY_ID, AWS_SECRET_ACCESS_KEY and AWS_SESSION_TOKEN; the policy needs ec2:DescribeVpnConnections only.
type AWSSource struct {
	Region       string `yaml:"region"`
	ConnectionID string `yaml:"connection_id"`
	// Endpoint overrides https://ec2.<region>.amazonaws.com, for a VPC endpoint or a test.
	Endpoint string `yaml:"endpoint,omitempty"`
}

type awsValues struct {
	Items []struct {
		Value string `xml:"value"`
	} `xml:"item"`
}

func (v awsValues) list() []string {
	var out []string
	for _, i := range v.Items {
		out = append(out, i.Value)
	}
	return out
}

type awsDescribe struct {
	Connections []struct {
		State     string `xml:"state"`
		Telemetry []struct {
			Outside string `xml:"outsideIpAddress"`
			Status  string `xml:"status"`
			Changed string `xml:"lastStatusChange"`
			Message string `xml:"statusMessage"`
		} `xml:"vgwTelemetry>item"`
		Options struct {
			Local   string `xml:"localIpv4NetworkCidr"`
			Remote  string `xml:"remoteIpv4NetworkCidr"`
			Tunnels []struct {
				P1Enc  awsValues `xml:"phase1EncryptionAlgorithmSet"`
				P1Int  awsValues `xml:"phase1IntegrityAlgorithmSet"`
				P1DH   awsValues `xml:"phase1DHGroupNumberSet"`
				P2Enc  awsValues `xml:"phase2EncryptionAlgorithmSet"`
				P2Int  awsValues `xml:"phase2IntegrityAlgorithmSet"`
				P2DH   awsValues `xml:"phase2DHGroupNumberSet"`
				IKE    awsValues `xml:"ikeVersionSet"`
				P1Life int       `xml:"phase1LifetimeSeconds"`
				P2Life int       `xml:"phase2LifetimeSeconds"`
			} `xml:"tunnelOptionSet>item"`
		} `xml:"options"`
	} `xml:"vpnConnectionSet>item"`
}

type awsError struct {
	Errors []struct {
		Code    string `xml:"Code"`
		Message string `xml:"Message"`
	} `xml:"Errors>Error"`
}

func (m *Monitor) readAWS(ctx context.Context, t *Tunnel, st *Status) error {
	creds := awsv4.Credentials{AccessKeyID: os.Getenv("AWS_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("AWS_SECRET_ACCESS_KEY"),
		SessionToken: os.Getenv("AWS_SESSION_TOKEN")}
	if !creds.Valid() {
		return fmt.Errorf("no AWS credentials: set AWS_ACCESS_KEY_ID and AWS_SECRET_ACCESS_KEY for a principal allowed ec2:DescribeVpnConnections")
	}
	base := t.AWS.Endpoint
	if base == "" {
		base = "https://ec2." + t.AWS.Region + ".amazonaws.com"
	}
	q := url.Values{"Action": {"DescribeVpnConnections"}, "Version": {"2016-11-15"}, "VpnConnectionId.1": {t.AWS.ConnectionID}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/?"+awsv4.CanonicalQuery(q), nil)
	if err != nil {
		return err
	}
	req.Host = req.URL.Host
	awsv4.Sign(req, nil, creds, t.AWS.Region, "ec2", m.now())
	body, status, err := m.fetch(req)
	if err != nil {
		return fmt.Errorf("AWS could not be reached: %w", err)
	}
	if status != http.StatusOK {
		var e awsError
		_ = xml.Unmarshal(body, &e)
		if len(e.Errors) > 0 {
			return fmt.Errorf("AWS refused DescribeVpnConnections: %s: %s", e.Errors[0].Code, e.Errors[0].Message)
		}
		return fmt.Errorf("AWS answered %d", status)
	}
	var d awsDescribe
	if err := xml.Unmarshal(body, &d); err != nil {
		return fmt.Errorf("AWS's answer could not be read: %w", err)
	}
	if len(d.Connections) == 0 {
		return fmt.Errorf("AWS has no VPN connection %s in %s", t.AWS.ConnectionID, t.AWS.Region)
	}
	c := d.Connections[0]
	for _, tel := range c.Telemetry {
		since, _ := time.Parse(time.RFC3339, tel.Changed)
		st.Endpoints = append(st.Endpoints, Endpoint{Address: tel.Outside, Up: strings.EqualFold(tel.Status, "UP"), Since: since, Message: tel.Message})
	}
	if c.State != "available" {
		st.State, st.Detail = Down, "The VPN connection is "+c.State+"."
	}
	if c.Options.Local != "" {
		st.Reported.Networks = []string{c.Options.Local}
	}
	if len(c.Options.Tunnels) > 0 {
		o := c.Options.Tunnels[0]
		st.Reported.Phase1Encryption, st.Reported.Phase1Integrity, st.Reported.Phase1DHGroups = o.P1Enc.list(), o.P1Int.list(), o.P1DH.list()
		st.Reported.Phase2Encryption, st.Reported.Phase2Integrity, st.Reported.Phase2DHGroups = o.P2Enc.list(), o.P2Int.list(), o.P2DH.list()
		st.Reported.IKEVersions, st.Reported.Phase1Lifetime, st.Reported.Phase2Lifetime = o.IKE.list(), o.P1Life, o.P2Life
	}
	return nil
}

func (m *Monitor) fetch(req *http.Request) ([]byte, int, error) {
	client := m.HTTP
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	return body, resp.StatusCode, err
}
