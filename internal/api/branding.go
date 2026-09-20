package api

import (
	"io"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/branding"
	"github.com/biodream-llc/perfuse/internal/store"
)

// White-labelling.
//
// A site running this for their own customers needs it to look like their product. That means the name,
// an accent colour and a logo, and it means all three are visible before anyone signs in - the sign-in
// page is the first thing a customer sees, and an unbranded one gives the game away.
//
// So the read endpoints here are deliberately unauthenticated. What they expose is a product name, a
// colour and an image that the operator chose in order to show it to people. Requiring a session to
// read them would mean the sign-in page could not be branded, which is the main thing it is for.
// Writing them requires an administrator.

// brandingResponse is what the interface needs to paint itself.
type brandingResponse struct {
	// ProductName is what to call the product. Never empty: the default is filled in here rather
	// than in the interface, so there is one answer to what this is called.
	ProductName string `json:"productName"`

	// Tagline is an optional line under the name on the sign-in page.
	Tagline string `json:"tagline,omitempty"`

	// AccentColour is a hex colour, or empty for the built-in accent.
	AccentColour string `json:"accentColour,omitempty"`

	// LogoVersion changes whenever the logo does, and is empty when there is none.
	//
	// The interface appends it to the logo URL so a replaced logo appears immediately. Without it the
	// browser serves the previous logo from cache and the customer concludes the upload failed.
	LogoVersion string `json:"logoVersion,omitempty"`

	// Customised says whether anything was changed from the defaults, so the interface can offer to
	// reset without having to compare every field itself.
	Customised bool `json:"customised"`
}

// handleBranding serves the current branding. Unauthenticated, so the sign-in page can use it.
func (s *Server) handleBranding(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	s.ok(w, s.brandingState())
}

// brandingState assembles the response from settings and the logo store.
func (s *Server) brandingState() brandingResponse {
	out := brandingResponse{ProductName: "Perfuse"}

	if s.Settings != nil {
		if name := strings.TrimSpace(s.Settings.String("branding.productName")); name != "" {
			out.ProductName = name
			out.Customised = true
		}
		out.Tagline = strings.TrimSpace(s.Settings.String("branding.tagline"))
		out.AccentColour = strings.TrimSpace(s.Settings.String("branding.accentColour"))
		if out.Tagline != "" || out.AccentColour != "" {
			out.Customised = true
		}
	}

	if s.Branding != nil {
		if logo, err := s.Branding.Logo(); err == nil {
			out.LogoVersion = logo.ETag
			out.Customised = true
		}
	}
	return out
}

// handleBrandingLogo serves the uploaded logo.
//
// Unauthenticated for the same reason as the state above: it is on the sign-in page. Served through
// the branding package's own header helper so the content security policy cannot be forgotten here -
// that policy is half of the defence against a malicious upload.
func (s *Server) handleBrandingLogo(w http.ResponseWriter, r *http.Request) {
	if s.Branding == nil {
		http.NotFound(w, r)
		return
	}
	logo, err := s.Branding.Logo()
	if err != nil {
		http.NotFound(w, r)
		return
	}

	branding.ServeHeaders(w.Header(), logo)

	// Honour a conditional request, so a browser that already has this logo is not sent it again.
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, logo.ETag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = w.Write(logo.Bytes)
	}
}

// handleBrandingLogoUpload replaces the logo.
//
// Takes the raw image as the request body rather than a multipart form. There is one field, and
// multipart would add a parser and a filename - and the filename is something we would then have to
// be careful never to use, since it is attacker-controlled. Not having it is simpler than handling it.
func (s *Server) handleBrandingLogoUpload(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.Branding == nil {
		s.fail(w, r, http.StatusNotImplemented, "this server has nowhere to store a logo")
		return
	}

	// Bounded before reading, not after. Reading an unbounded body to then check its length is how a
	// size limit becomes a way to exhaust memory.
	body := http.MaxBytesReader(w, r.Body, branding.MaxLogoBytes+1)
	data, err := io.ReadAll(body)
	if err != nil {
		s.fail(w, r, http.StatusRequestEntityTooLarge,
			"that image is too large; keep it under one megabyte")
		return
	}

	if err := s.Branding.SetLogo(data); err != nil {
		// The message from the branding package names what was wrong with the image, which is
		// something the person uploading it can act on, so it is passed through. It describes the
		// upload rather than the server, so it leaks nothing.
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	s.auditBranding(r, sess, "branding.logo.set")
	s.ok(w, s.brandingState())
}

// handleBrandingLogoDelete returns the console to the built-in mark.
func (s *Server) handleBrandingLogoDelete(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if s.Branding == nil {
		s.fail(w, r, http.StatusNotImplemented, "this server has nowhere to store a logo")
		return
	}
	if err := s.Branding.ClearLogo(); err != nil {
		s.failErr(w, r, err)
		return
	}
	s.auditBranding(r, sess, "branding.logo.cleared")
	s.ok(w, s.brandingState())
}

// handleBrandingPreview checks a proposed accent colour without saving it.
//
// Exists so the interface can say why a colour was refused as it is typed, rather than only when the
// form is submitted. The same validation runs on save; this endpoint does not decide anything.
func (s *Server) handleBrandingPreview(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req struct {
		AccentColour string `json:"accentColour"`
	}
	if !s.decode(w, r, &req) {
		return
	}

	resp := struct {
		OK     bool   `json:"ok"`
		Reason string `json:"reason,omitempty"`
	}{OK: true}

	if err := s.Settings.Check("branding.accentColour", req.AccentColour); err != nil {
		resp.OK = false
		resp.Reason = err.Error()
	}
	s.ok(w, resp)
}

// auditBranding records a branding change.
//
// Worth an entry because it changes what every user of the system sees, and because "who renamed the
// product" is a question somebody eventually asks.
func (s *Server) auditBranding(r *http.Request, sess *store.Session, action string) {
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: action, IP: clientIP(r),
	})
}
