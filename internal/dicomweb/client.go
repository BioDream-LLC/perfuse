package dicomweb

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// Client stores objects on a DICOMweb server with STOW-RS.
type Client struct {
	// URL is the service root, for example https://pacs.example/dicom-web; objects go to URL/studies.
	URL string
	// BearerToken, or Username and Password, authenticate.
	BearerToken        string
	Username, Password string
	Headers            map[string]string
	HTTP               *http.Client
}

// STOWOutcome is what a STOW-RS server said about one request.
type STOWOutcome struct {
	Status   int
	Stored   []string
	Failed   map[string]uint16
	Warnings []string
}

// Store sends objects in one STOW-RS request.
//
// Any object the server lists as failed makes this an error, and so does a 202 that lists nothing as stored: the server
// accepted the request and kept none of it, which is the case that otherwise reads as success.
func (c *Client) Store(ctx context.Context, objects ...[]byte) (*STOWOutcome, error) {
	parts := make([]Part, len(objects))
	for i, o := range objects {
		parts[i] = Part{ContentType: "application/dicom", Body: o}
	}
	var body bytes.Buffer
	ct, err := WriteMultipart(&body, "application/dicom", parts)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(c.URL, "/")+"/studies", &body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", ct)
	req.Header.Set("Accept", "application/dicom+json")
	for k, v := range c.Headers {
		req.Header.Set(k, v)
	}
	switch {
	case c.BearerToken != "":
		req.Header.Set("Authorization", "Bearer "+c.BearerToken)
	case c.Username != "":
		req.SetBasicAuth(c.Username, c.Password)
	}
	hc := c.HTTP
	if hc == nil {
		hc = http.DefaultClient
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))

	out := &STOWOutcome{Status: resp.StatusCode, Failed: map[string]uint16{}}
	if w := resp.Header.Get("Warning"); w != "" {
		out.Warnings = append(out.Warnings, w)
	}
	var doc JSON
	if len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &doc) == nil {
		for _, item := range seqItems(doc, "00081199") {
			out.Stored = append(out.Stored, firstString(item, "00081155"))
		}
		for _, item := range seqItems(doc, "00081198") {
			reason := uint16(0)
			if v, ok := item["00081197"].(map[string]any); ok {
				if vals, ok := v["Value"].([]any); ok && len(vals) > 0 {
					if f, ok := vals[0].(float64); ok {
						reason = uint16(f)
					}
				}
			}
			out.Failed[firstString(item, "00081155")] = reason
		}
	}

	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return out, fmt.Errorf("the DICOMweb server refused the credentials (%d)", resp.StatusCode)
	case len(out.Failed) > 0:
		var list []string
		for sop, reason := range out.Failed {
			if sop == "" {
				sop = "an object"
			}
			list = append(list, fmt.Sprintf("%s (failure reason 0x%04X)", sop, reason))
		}
		return out, fmt.Errorf("the DICOMweb server did not store %s", strings.Join(list, ", "))
	case resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted:
		if resp.StatusCode == http.StatusAccepted && len(out.Stored) == 0 && len(doc) > 0 {
			return out, fmt.Errorf("the DICOMweb server accepted the request but lists nothing as stored")
		}
		return out, nil
	}
	return out, fmt.Errorf("the DICOMweb server answered %d: %s", resp.StatusCode, firstLine(string(raw)))
}

func seqItems(doc JSON, key string) []map[string]any {
	v, ok := doc[key].(map[string]any)
	if !ok {
		return nil
	}
	vals, _ := v["Value"].([]any)
	var out []map[string]any
	for _, x := range vals {
		if m, ok := x.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func firstString(item map[string]any, key string) string {
	v, ok := item[key].(map[string]any)
	if !ok {
		return ""
	}
	vals, _ := v["Value"].([]any)
	if len(vals) == 0 {
		return ""
	}
	s, _ := vals[0].(string)
	return s
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200] + "…"
	}
	if s == "" {
		return "(no body)"
	}
	return s
}
