package engine

import (
	"context"
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/dicom"
	"github.com/biodream-llc/perfuse/internal/dicomweb"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
)

// startDICOMwebSource serves STOW-RS for this channel.
//
// Each part is one Part 10 object, handed to the channel as a C-STORE object would be. The response lists each object as
// stored or failed in the PS3.18 form, so a sender knows which to resend.
func (c *Channel) startDICOMwebSource() error {
	src := c.cfg.Source.DICOMweb
	if src == nil {
		return fmt.Errorf("channel %q: source type dicomweb but no dicomweb block", c.cfg.Name)
	}
	for _, w := range src.Warnings() {
		c.log.Warn("dicomweb source", "detail", w)
	}
	tlsCfg, err := tlsconf.ForListener(src.TLS)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	maxObj := int64(src.MaxObjectBytes)
	if maxObj <= 0 {
		maxObj = 512 << 20
	}

	root := src.ServicePath()
	mux := http.NewServeMux()
	mux.HandleFunc(root+"/studies", c.handleSTOW(src, maxObj, ""))
	mux.HandleFunc(root+"/studies/{study}", func(w http.ResponseWriter, r *http.Request) {
		c.handleSTOW(src, maxObj, r.PathValue("study"))(w, r)
	})

	srv := &http.Server{
		Addr: src.Listen, Handler: mux, TLSConfig: tlsCfg,
		ReadHeaderTimeout: 10 * time.Second,
		// A study upload can be large and slow; the read deadline is generous, and the response waits for delivery.
		ReadTimeout: 10 * time.Minute, WriteTimeout: 10 * time.Minute,
	}
	ln, err := net.Listen("tcp", src.Listen)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	if tlsCfg != nil {
		ln = tls.NewListener(ln, tlsCfg)
	}
	c.httpServer = srv
	c.log.Info("dicomweb listening", "addr", src.Listen, "path", root+"/studies", "tls", tlsCfg != nil,
		"authenticated", src.Token != "" || src.Username != "")
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.log.Error("the dicomweb listener stopped", "error", err)
		}
	}()
	return nil
}

func (c *Channel) dicomwebAuthorised(r *http.Request, src *config.DICOMwebSource) bool {
	if src.Token != "" {
		h := r.Header.Get("Authorization")
		if len(h) < 7 || !strings.EqualFold(h[:7], "Bearer ") {
			return false
		}
		return subtle.ConstantTimeCompare([]byte(strings.TrimSpace(h[7:])), []byte(src.Token)) == 1
	}
	if src.Username != "" {
		u, p, ok := r.BasicAuth()
		uOK := subtle.ConstantTimeCompare([]byte(u), []byte(src.Username)) == 1
		pOK := subtle.ConstantTimeCompare([]byte(p), []byte(src.Password)) == 1
		return ok && uOK && pOK
	}
	return true
}

func (c *Channel) handleSTOW(src *config.DICOMwebSource, maxObj int64, study string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.Header().Set("Allow", "POST")
			http.Error(w, "this endpoint accepts STOW-RS: POST multipart/related; type=\"application/dicom\"",
				http.StatusMethodNotAllowed)
			return
		}
		if !c.dicomwebAuthorised(r, src) {
			if src.Username != "" {
				w.Header().Set("WWW-Authenticate", `Basic realm="perfuse"`)
			}
			http.Error(w, "not authorised", http.StatusUnauthorized)
			return
		}
		if study != "" && !dicomweb.ValidUID(study) {
			http.Error(w, "the study in the path is not a UID", http.StatusBadRequest)
			return
		}
		partType, parts, err := dicomweb.ReadMultipart(r.Header.Get("Content-Type"), r.Body, maxObj, maxObj*64)
		if err != nil {
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "limit") {
				status = http.StatusRequestEntityTooLarge
			}
			http.Error(w, err.Error(), status)
			return
		}
		if mt, _, _ := dicomweb.ParseMediaType(partType); mt != "" && mt != "application/dicom" {
			http.Error(w, "STOW-RS here takes application/dicom parts", http.StatusUnsupportedMediaType)
			return
		}

		var stored, failed []any
		busy := false
		for _, p := range parts {
			ds, err := dicom.Parse(p.Body)
			obj, perr := dicomweb.Parse(p.Body)
			if err != nil || perr != nil {
				failed = append(failed, stowFailure("", "", dicomweb.FailureCannotRead))
				continue
			}
			sop, class := obj.Text(0x00080018), obj.Text(0x00080016)
			if study != "" && obj.Text(0x0020000D) != study {
				failed = append(failed, stowFailure(class, sop, dicomweb.FailureStudyMismatch))
				continue
			}
			if c.intake != nil {
				if err := c.intake(); err != nil {
					busy = true
					failed = append(failed, stowFailure(class, sop, dicomweb.FailureProcessing))
					continue
				}
			}
			if _, err := c.handleDICOMParsed(r.Context(), p.Body, ds); err != nil {
				c.log.Warn("a STOW-RS object was not delivered", "sop", sop, "err", err)
				failed = append(failed, stowFailure(class, sop, dicomweb.FailureProcessing))
				continue
			}
			stored = append(stored, dicomweb.JSON{
				"00081150": map[string]any{"vr": "UI", "Value": []any{class}},
				"00081155": map[string]any{"vr": "UI", "Value": []any{sop}},
			})
		}

		resp := dicomweb.JSON{}
		if len(stored) > 0 {
			resp["00081199"] = map[string]any{"vr": "SQ", "Value": stored}
		}
		if len(failed) > 0 {
			resp["00081198"] = map[string]any{"vr": "SQ", "Value": failed}
		}
		status := http.StatusOK
		switch {
		case len(parts) == 0:
			status = http.StatusBadRequest
		case len(stored) == 0 && busy:
			w.Header().Set("Retry-After", "60")
			status = http.StatusServiceUnavailable
		case len(stored) == 0:
			status = http.StatusConflict
		case len(failed) > 0:
			status = http.StatusAccepted
		}
		w.Header().Set("Content-Type", "application/dicom+json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(resp)
	}
}

func stowFailure(class, sop string, reason uint16) dicomweb.JSON {
	j := dicomweb.JSON{"00081197": map[string]any{"vr": "US", "Value": []any{reason}}}
	if class != "" {
		j["00081150"] = map[string]any{"vr": "UI", "Value": []any{class}}
	}
	if sop != "" {
		j["00081155"] = map[string]any{"vr": "UI", "Value": []any{sop}}
	}
	return j
}

// DICOMwebSender stores each object on a DICOMweb server with STOW-RS.
type DICOMwebSender struct {
	client *dicomweb.Client
	url    string
}

// NewDICOMwebSender builds the sender.
func NewDICOMwebSender(d config.Destination) (*DICOMwebSender, error) {
	cfg := d.DICOMweb
	if cfg == nil {
		return nil, fmt.Errorf("destination %q is a dicomweb destination with no dicomweb block", d.Name)
	}
	hc := &http.Client{Timeout: cfg.Timeout}
	if cfg.TLS != nil {
		tlsCfg, err := tlsconf.ForSender(cfg.TLS)
		if err != nil {
			return nil, err
		}
		hc.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	}
	return &DICOMwebSender{url: cfg.URL, client: &dicomweb.Client{
		URL: cfg.URL, BearerToken: cfg.Token, Username: cfg.Username, Password: cfg.Password,
		Headers: cfg.Headers, HTTP: hc,
	}}, nil
}

// Send stores one object. The server's per-object answer decides: a 200 listing the object as failed is a failure.
func (s *DICOMwebSender) Send(ctx context.Context, msg []byte) error {
	if _, err := dicomweb.Parse(msg); err != nil {
		return fmt.Errorf("a DICOMweb server stores Part 10 objects, and this is not one: %w", err)
	}
	_, err := s.client.Store(ctx, msg)
	return err
}

// Describe names the destination, never the credentials.
func (s *DICOMwebSender) Describe() string { return s.url + " (DICOMweb STOW-RS)" }

// Close releases nothing.
func (s *DICOMwebSender) Close() error { return nil }
