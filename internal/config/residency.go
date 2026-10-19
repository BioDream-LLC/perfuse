package config

import (
	"fmt"
	"net/url"
	"strings"

	"github.com/biodream-llc/perfuse/internal/residency"
)

// Data residency: where a channel's cloud connectors read from and write to, checked against the regions this server's
// data may go to (serve -residency). Only what names its region is judged: an AWS connector's region, or a cloud hostname
// that carries one. A connector pointed at another endpoint (MinIO, LocalStack, a VPC endpoint by IP) is judged by that
// endpoint's host, when it is a cloud one, and not otherwise.

// awsRegion is the region an AWS connector really reaches: its endpoint's, when the endpoint names one; none when the
// endpoint is not AWS's; its own region field otherwise.
func awsRegion(region, endpoint string) (string, bool) {
	if strings.TrimSpace(endpoint) != "" {
		host := endpoint
		if u, err := url.Parse(endpoint); err == nil && u.Host != "" {
			host = u.Host
		}
		if _, r, ok := residency.RegionOfHost(host); ok {
			return r, true
		}
		return "", false
	}
	return strings.TrimSpace(region), strings.TrimSpace(region) != ""
}

// CloudRegions lists where the channel's cloud connectors are, as "what: region".
func (c *Channel) CloudRegions() map[string]string {
	out := map[string]string{}
	if s := c.Source.SQS; s != nil {
		if r, ok := awsRegion(s.Region, s.Endpoint); ok {
			out["the sqs source"] = r
		}
	}
	if s := c.Source.S3; s != nil {
		if r, ok := awsRegion(s.Region, s.Endpoint); ok {
			out["the s3 source"] = r
		}
	}
	for i := range c.Destinations {
		d := &c.Destinations[i]
		name := fmt.Sprintf("destination %q", d.Name)
		switch {
		case d.S3 != nil && d.Type == DestinationS3:
			if r, ok := awsRegion(d.S3.Region, d.S3.Endpoint); ok {
				out[name] = r
			}
		case d.SQS != nil && d.Type == DestinationSQS:
			if r, ok := awsRegion(d.SQS.Region, d.SQS.Endpoint); ok {
				out[name] = r
			}
		case d.SNS != nil && d.Type == DestinationSNS:
			if r, ok := awsRegion(d.SNS.Region, d.SNS.Endpoint); ok {
				out[name] = r
			}
		}
		if _, have := out[name]; have {
			continue
		}
		for _, h := range d.OutboundHosts() {
			if _, r, ok := residency.RegionOfHost(h); ok {
				out[name] = r
				break
			}
		}
	}
	return out
}

func (c *Channel) checkResidency(p residency.Policy) []error {
	if !p.Set() {
		return nil
	}
	var errs []error
	for what, region := range c.CloudRegions() {
		if err := p.Check(what, region); err != nil {
			errs = append(errs, err)
		}
	}
	return errs
}
