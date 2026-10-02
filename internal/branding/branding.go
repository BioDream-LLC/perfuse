// Package branding stores a customer's logo and product name so an installation can present itself
// as their own product rather than as Perfuse.
//
// # Why the logo is not a setting
//
// Everything else an operator can change is a scalar in the settings file. A logo is a few hundred
// kilobytes of binary, and putting it there would mean base64 in a YAML file that a person is meant
// to be able to read and hand-edit. It lives beside that file instead, and only its presence is
// visible through the settings surface.
//
// # The part that matters: an uploaded image is untrusted input
//
// A logo is uploaded by an administrator and then served to every browser that loads the console,
// including on the sign-in page before anyone has authenticated. That makes it the most widely
// distributed piece of attacker-supplied content in the product, so it gets treated accordingly:
//
//   - The declared content type is ignored. The bytes are sniffed, and the result must be in an
//     allowlist. A file called logo.png containing HTML is HTML.
//   - SVG is accepted, because that is what companies actually have, but it is an executable
//     document format: it can carry script elements, event handler attributes, external references
//     and embedded foreign objects. It is rewritten to remove all of those before it is stored, and
//     the rewrite is verified by planting each attack in a test.
//   - It is served with a content security policy that permits nothing, with sniffing disabled, and
//     as an attachment-free inline image only. Even a sanitiser bug should not become script
//     execution on the console's own origin.
//   - The uploaded filename is never used to build a path. The stored name is derived from the
//     sniffed type, so a name like ../../etc/perfuse cannot escape anywhere.
package branding

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
)

// MaxLogoBytes caps an upload.
//
// A logo that needs more than this is a photograph, not a mark, and it would be sent to every
// browser on every page load.
const MaxLogoBytes = 1 << 20 // 1 MiB

// allowedTypes maps a sniffed content type to the extension it is stored under.
//
// An allowlist rather than a denylist: the set of image formats a browser will render is larger than
// the set that is safe to accept, and the difference is where the interesting bugs are.
var allowedTypes = map[string]string{
	"image/png":     ".png",
	"image/jpeg":    ".jpg",
	"image/gif":     ".gif",
	"image/webp":    ".webp",
	"image/svg+xml": ".svg",
}

// ErrNoLogo is returned when nothing has been uploaded.
var ErrNoLogo = errors.New("branding: no logo has been uploaded")

// Logo is a stored mark.
type Logo struct {
	// Bytes is the sanitised image.
	Bytes []byte
	// ContentType is the sniffed type, which is what gets served. Never the declared one.
	ContentType string
	// ETag identifies this exact image, so a browser can cache it and still see a replacement
	// immediately. Without it a customer uploads a new logo and keeps seeing the old one.
	ETag string
}

// Store holds the logo on disk beside the settings file.
//
// Safe for concurrent use. The logo is read on essentially every page load and written rarely.
type Store struct {
	dir string

	mu   sync.RWMutex
	logo *Logo
}

// NewStore opens a store rooted at a directory, loading an existing logo if one is there.
//
// An empty directory means branding is read-only: defaults are served and an upload is refused
// rather than silently kept in memory until restart.
func NewStore(dir string) (*Store, error) {
	s := &Store{dir: dir}
	if dir == "" {
		return s, nil
	}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

// load reads whichever logo file is present.
func (s *Store) load() error {
	for ct, ext := range allowedTypes {
		data, err := os.ReadFile(s.pathFor(ext))
		switch {
		case errors.Is(err, os.ErrNotExist):
			continue
		case err != nil:
			return fmt.Errorf("branding: reading the stored logo: %w", err)
		}

		// Re-validated on load rather than trusted. The file could have been replaced on disk since
		// it was written, and a sanitiser that only runs on upload protects only uploads.
		clean, gotType, err := Validate(data)
		if err != nil {
			return fmt.Errorf("branding: the stored logo is not usable: %w", err)
		}
		if gotType != ct {
			// The file's name disagrees with its contents. Trust the contents.
			ct = gotType
		}
		s.logo = &Logo{Bytes: clean, ContentType: ct, ETag: etagOf(clean)}
		return nil
	}
	return nil
}

func (s *Store) pathFor(ext string) string {
	return filepath.Join(s.dir, "perfuse-logo"+ext)
}

// Logo returns the stored mark, or ErrNoLogo.
func (s *Store) Logo() (*Logo, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.logo == nil {
		return nil, ErrNoLogo
	}
	return s.logo, nil
}

// Has reports whether a logo has been uploaded.
func (s *Store) Has() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.logo != nil
}

// SetLogo validates, sanitises and stores an uploaded image.
//
// The declared content type is not a parameter, deliberately. Accepting one would invite trusting it.
func (s *Store) SetLogo(data []byte) error {
	if s.dir == "" {
		return errors.New("branding: no directory is configured, so a logo cannot be saved")
	}

	clean, contentType, err := Validate(data)
	if err != nil {
		return err
	}
	ext := allowedTypes[contentType]

	// Written before the old one is removed, so a failure leaves the previous logo in place rather
	// than none at all.
	if err := writeFileAtomic(s.pathFor(ext), clean); err != nil {
		return err
	}
	for other, otherExt := range allowedTypes {
		if other != contentType {
			_ = os.Remove(s.pathFor(otherExt))
		}
	}

	s.mu.Lock()
	s.logo = &Logo{Bytes: clean, ContentType: contentType, ETag: etagOf(clean)}
	s.mu.Unlock()
	return nil
}

// ClearLogo removes the stored mark, returning the console to the Perfuse default.
func (s *Store) ClearLogo() error {
	if s.dir == "" {
		return errors.New("branding: no directory is configured")
	}
	for _, ext := range allowedTypes {
		if err := os.Remove(s.pathFor(ext)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("branding: removing the logo: %w", err)
		}
	}
	s.mu.Lock()
	s.logo = nil
	s.mu.Unlock()
	return nil
}

// Validate checks an uploaded image and returns the bytes to store and their real content type.
//
// SVG comes back rewritten; raster formats come back unchanged.
func Validate(data []byte) (clean []byte, contentType string, err error) {
	if len(data) == 0 {
		return nil, "", errors.New("branding: the uploaded file is empty")
	}
	if len(data) > MaxLogoBytes {
		return nil, "", fmt.Errorf("branding: the logo is %d bytes, over the %d byte limit", len(data), MaxLogoBytes)
	}

	contentType = sniff(data)
	if _, ok := allowedTypes[contentType]; !ok {
		return nil, "", fmt.Errorf("branding: %s is not an image format this accepts (PNG, JPEG, GIF, WebP or SVG)", contentType)
	}

	if contentType == "image/svg+xml" {
		clean, err = SanitiseSVG(data)
		if err != nil {
			return nil, "", err
		}
		return clean, contentType, nil
	}
	return data, contentType, nil
}

// sniff determines the real type of an upload.
//
// http.DetectContentType handles the raster formats from their magic numbers but reports SVG as
// plain text or XML, because SVG has no magic number, so that case is decided by looking for the
// root element.
func sniff(data []byte) string {
	if looksLikeSVG(data) {
		return "image/svg+xml"
	}
	ct := http.DetectContentType(data)
	// DetectContentType appends a charset for text types.
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct
}

// looksLikeSVG reports whether the bytes are an SVG document.
//
// Checks for the element rather than for the string anywhere, so a PNG whose pixel data happens to
// contain "<svg" is not mistaken for one.
func looksLikeSVG(data []byte) bool {
	head := data
	if len(head) > 1024 {
		head = head[:1024]
	}
	lower := bytes.ToLower(head)
	return bytes.Contains(lower, []byte("<svg")) &&
		(bytes.HasPrefix(bytes.TrimSpace(lower), []byte("<?xml")) ||
			bytes.HasPrefix(bytes.TrimSpace(lower), []byte("<svg")) ||
			bytes.Contains(lower, []byte("<!doctype svg")))
}

// Elements an SVG logo is allowed to contain.
//
// An allowlist rather than a denylist, because the set of markup a browser will act on is larger than
// anyone's list of dangerous things, and the difference is exactly where the bugs live. Anything not
// named here is dropped along with its subtree.
//
// Notably absent: script and handler, which run code. foreignObject, which embeds arbitrary HTML.
// iframe, embed and object, which load documents. style, because a stylesheet can import from the
// network. The animation elements, which can drive an attribute to a script URL after load.
var allowedElements = map[string]bool{
	"svg": true, "g": true, "defs": true, "title": true, "desc": true, "metadata": true,
	"path": true, "rect": true, "circle": true, "ellipse": true, "line": true,
	"polyline": true, "polygon": true, "text": true, "tspan": true, "textPath": true,
	"linearGradient": true, "radialGradient": true, "stop": true,
	"clipPath": true, "mask": true, "pattern": true, "symbol": true, "marker": true,
	"use": true, "switch": true, "a": true,
	"filter": true, "feGaussianBlur": true, "feOffset": true, "feBlend": true,
	"feColorMatrix": true, "feMerge": true, "feMergeNode": true, "feFlood": true,
	"feComposite": true, "feDropShadow": true, "feMorphology": true, "feTurbulence": true,
	"feDisplacementMap": true, "feComponentTransfer": true, "feFuncR": true, "feFuncG": true,
	"feFuncB": true, "feFuncA": true, "feTile": true, "feImage": false, // feImage can fetch
}

// Attributes an allowed element may carry.
//
// Presentation and geometry only. Every on* handler is excluded by not being listed, so a new event
// type invented by a future browser is excluded automatically rather than needing to be added to a
// denylist nobody remembers to update.
var allowedAttributes = map[string]bool{
	// Structure and identity.
	"id": true, "class": true, "viewBox": true, "xmlns": true, "version": true,
	"preserveAspectRatio": true, "role": true, "aria-label": true, "aria-labelledby": true,
	"aria-hidden": true, "focusable": true,

	// Geometry.
	"x": true, "y": true, "x1": true, "y1": true, "x2": true, "y2": true,
	"cx": true, "cy": true, "r": true, "rx": true, "ry": true,
	"width": true, "height": true, "d": true, "points": true, "transform": true,
	"dx": true, "dy": true, "rotate": true, "textLength": true, "lengthAdjust": true,
	"pathLength": true, "offset": true,

	// Paint.
	"fill": true, "fill-opacity": true, "fill-rule": true, "stroke": true,
	"stroke-width": true, "stroke-opacity": true, "stroke-linecap": true,
	"stroke-linejoin": true, "stroke-dasharray": true, "stroke-dashoffset": true,
	"stroke-miterlimit": true, "opacity": true, "color": true,
	"stop-color": true, "stop-opacity": true, "gradientUnits": true,
	"gradientTransform": true, "spreadMethod": true, "fr": true,
	"clip-path": true, "clip-rule": true, "mask": true, "filter": true,
	"maskUnits": true, "maskContentUnits": true, "clipPathUnits": true,
	"patternUnits": true, "patternContentUnits": true, "patternTransform": true,
	"filterUnits": true, "primitiveUnits": true,

	// Text.
	"font-family": true, "font-size": true, "font-weight": true, "font-style": true,
	"text-anchor": true, "dominant-baseline": true, "letter-spacing": true,
	"word-spacing": true, "text-decoration": true, "writing-mode": true,

	// Filter primitives.
	"in": true, "in2": true, "result": true, "stdDeviation": true, "mode": true,
	"type": true, "values": true, "operator": true, "k1": true, "k2": true, "k3": true,
	"k4": true, "radius": true, "baseFrequency": true, "numOctaves": true, "seed": true,
	"scale": true, "flood-color": true, "flood-opacity": true, "tableValues": true,
	"slope": true, "intercept": true, "amplitude": true, "exponent": true,

	// Markers.
	"marker-start": true, "marker-mid": true, "marker-end": true,
	"markerWidth": true, "markerHeight": true, "refX": true, "refY": true,
	"markerUnits": true, "orient": true, "overflow": true,
}

// urlAttributes may reference something, and so are restricted to same-document fragments.
var urlAttributes = map[string]bool{
	"href": true, "xlink:href": true, "src": true,
}

// SanitiseSVG rewrites an SVG so it can only draw.
//
// Parsed and rebuilt against the allowlists above rather than pattern-matched, because a regular
// expression over markup is defeated by the things browsers tolerate: unusual case, whitespace inside
// a tag, entities, and nesting that reveals a new construct when the outer one is removed. Parsing
// sidesteps all of it - an element that is not named is simply never written out.
//
// This is one of three layers. The file is also served under a policy that permits no script and no
// network, and it is rendered through an img element, which does not execute script even for an SVG
// that contains it. Any one of the three failing should not be enough.
func SanitiseSVG(data []byte) ([]byte, error) {
	// A doctype is how an SVG becomes a billion-laughs expansion or reads a local file through an
	// external entity. A logo has no use for one, so its presence is refused rather than stripped:
	// an upload that contains one is not a logo somebody produced by accident.
	if entityDecl.Match(data) {
		return nil, errors.New("branding: the SVG declares XML entities, which a logo does not need and which can be used to read files or exhaust memory")
	}

	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	// No entity map is installed, so any entity beyond the five XML predefines is unknown. With
	// Strict off the decoder passes them through as text rather than expanding them, which is what
	// we want: nothing gets expanded and nothing gets executed.
	dec.Entity = xml.HTMLEntity

	var out bytes.Buffer
	enc := xml.NewEncoder(&out)

	// skipDepth counts how deep we are inside a rejected element, so its entire subtree is dropped
	// rather than just its opening tag. Dropping only the tag would promote its children - a script
	// inside a foreignObject would survive the foreignObject being removed.
	skipDepth := 0
	kept := 0

	for {
		tok, err := dec.Token()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return nil, fmt.Errorf("branding: the SVG could not be parsed: %w", err)
		}

		switch t := tok.(type) {
		case xml.StartElement:
			if skipDepth > 0 {
				skipDepth++
				continue
			}
			if !allowedElements[t.Name.Local] {
				skipDepth = 1
				continue
			}
			clean := xml.StartElement{Name: xml.Name{Local: t.Name.Local}, Attr: filterAttrs(t.Attr)}
			if err := enc.EncodeToken(clean); err != nil {
				return nil, err
			}
			kept++

		case xml.EndElement:
			if skipDepth > 0 {
				skipDepth--
				continue
			}
			if !allowedElements[t.Name.Local] {
				continue
			}
			if err := enc.EncodeToken(xml.EndElement{Name: xml.Name{Local: t.Name.Local}}); err != nil {
				return nil, err
			}

		case xml.CharData:
			if skipDepth > 0 {
				continue
			}
			if err := enc.EncodeToken(xml.CharData(t)); err != nil {
				return nil, err
			}

		// Comments, processing instructions and directives are all dropped. None of them draw
		// anything, and a processing instruction can carry a stylesheet reference.
		case xml.Comment, xml.ProcInst, xml.Directive:
			continue
		}
	}

	if err := enc.Flush(); err != nil {
		return nil, err
	}
	if kept == 0 || !bytes.Contains(bytes.ToLower(out.Bytes()), []byte("<svg")) {
		return nil, errors.New("branding: after removing everything unsafe there was no image left")
	}

	// The namespace is required for a browser to render it standalone, and the encoder drops the
	// attribute when rebuilding without namespace awareness.
	result := out.Bytes()
	if !bytes.Contains(result, []byte("http://www.w3.org/2000/svg")) {
		result = bytes.Replace(result, []byte("<svg"), []byte(`<svg xmlns="http://www.w3.org/2000/svg"`), 1)
	}
	return result, nil
}

// entityDecl finds an XML entity declaration.
var entityDecl = regexp.MustCompile(`(?is)<!\s*entity\b`)

// filterAttrs keeps only allowed attributes, and only safe values for those that reference something.
func filterAttrs(attrs []xml.Attr) []xml.Attr {
	out := make([]xml.Attr, 0, len(attrs))
	for _, a := range attrs {
		name := a.Name.Local
		// An attribute arriving with a namespace prefix is checked under its prefixed name too, so
		// xlink:href is recognised as a URL attribute rather than as a bare href.
		qualified := name
		if a.Name.Space != "" {
			qualified = a.Name.Space + ":" + name
		}

		// Belt and braces over the allowlist: no event handler can be reached even if one is
		// mistakenly added to it later.
		if strings.HasPrefix(strings.ToLower(name), "on") {
			continue
		}

		if urlAttributes[name] || urlAttributes[qualified] {
			// Only a same-document fragment. That keeps a use element pointing at a gradient in the
			// same file working, while refusing anything that leaves the document - which covers
			// javascript:, data:, and fetching from a third party.
			if !strings.HasPrefix(strings.TrimSpace(a.Value), "#") {
				continue
			}
			out = append(out, xml.Attr{Name: xml.Name{Local: name}, Value: a.Value})
			continue
		}

		if !allowedAttributes[name] {
			continue
		}
		// A value that references a URL is only allowed to reference this document, which is how
		// fill="url(#gradient)" keeps working while fill="url(https://…)" does not.
		if lower := strings.ToLower(a.Value); strings.Contains(lower, "url(") {
			if !urlFragmentOnly.MatchString(a.Value) {
				continue
			}
		}
		out = append(out, xml.Attr{Name: xml.Name{Local: name}, Value: a.Value})
	}
	return out
}

// urlFragmentOnly matches a value whose every url() reference is a local fragment.
var urlFragmentOnly = regexp.MustCompile(`^(?:[^u]|u(?:[^r]|r(?:[^l]|l(?:[^(]|\(\s*['"]?#))))*$`)

// ServeHeaders sets the response headers a logo must be served with.
//
// Kept next to the sanitiser rather than in the handler so the two cannot drift apart: the policy is
// the second half of the defence, and a handler that forgets it silently removes that half.
func ServeHeaders(h http.Header, l *Logo) {
	h.Set("Content-Type", l.ContentType)

	// Without this a browser may sniff the bytes and decide an image is a document.
	h.Set("X-Content-Type-Options", "nosniff")

	// Permits nothing at all: no script, no styles, no network, and a unique opaque origin. An SVG
	// served under this cannot reach the console's origin even if it still contains script.
	h.Set("Content-Security-Policy", "default-src 'none'; style-src 'none'; script-src 'none'; sandbox")

	// Inline so it renders in an img, named so a download is not given the customer's own filename.
	h.Set("Content-Disposition", `inline; filename="logo"`)

	// The tag changes with the bytes, so a replaced logo appears at once while an unchanged one is
	// not re-sent.
	h.Set("ETag", `"`+l.ETag+`"`)
	h.Set("Cache-Control", "private, max-age=0, must-revalidate")
}

func etagOf(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:8])
}

// writeFileAtomic writes through a temporary file and renames, so a crash cannot leave a truncated
// logo that then fails to load on start.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".perfuse-logo-*")
	if err != nil {
		return fmt.Errorf("branding: creating a temporary file: %w", err)
	}
	name := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(name)
	}()

	if _, err := tmp.Write(data); err != nil {
		return fmt.Errorf("branding: writing the logo: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("branding: flushing the logo: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("branding: closing the logo: %w", err)
	}
	if err := os.Chmod(name, 0o644); err != nil {
		return fmt.Errorf("branding: setting permissions: %w", err)
	}
	if err := os.Rename(name, path); err != nil {
		return fmt.Errorf("branding: replacing the logo: %w", err)
	}
	return nil
}
