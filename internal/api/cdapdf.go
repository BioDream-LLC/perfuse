package api

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/cda"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Rendering a clinical document as a PDF for somebody to read, print, fax or file.
//
// # Why a POST that returns a file
//
// The document arrives in the request body because nothing is stored here: a caller supplies the XML and gets a PDF
// back. That makes this a POST returning a binary body rather than a GET of something addressable, which is
// unusual enough to be worth stating so nobody later "fixes" it into a GET with the document in a query string -
// where it would land in every access log and proxy cache along the way. Patient data does not belong in a URL.
func (s *Server) handleRenderDocumentPDF(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var body struct {
		Document string `json:"document"`

		// IncludeCodes prints the coded entries as a labelled table beside the narrative.
		IncludeCodes bool `json:"includeCodes"`

		// Footer is the site's own provenance note.
		Footer string `json:"footer"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	if strings.TrimSpace(body.Document) == "" {
		s.fail(w, r, http.StatusBadRequest, "supply a clinical document to render")
		return
	}

	doc, err := cda.Parse([]byte(body.Document))
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	out, err := cda.RenderPDF(doc, cda.RenderOptions{
		IncludeCodes: body.IncludeCodes,
		Footer:       body.Footer,
	})
	if err != nil {
		s.fail(w, r, http.StatusBadRequest, err.Error())
		return
	}

	// A filename built from the patient and the document date.
	//
	// Not from a counter or a UUID: these files get saved to a desktop and looked at again later, and a folder of
	// document-1.pdf through document-40.pdf is a folder nobody can use. The name is sanitised because it goes
	// into a header, and a newline in a patient's name would otherwise let the rest of the header be rewritten.
	name := pdfFilename(doc)

	w.Header().Set("Content-Type", "application/pdf")
	w.Header().Set("Content-Length", strconv.Itoa(len(out)))

	// attachment, not inline. A PDF rendered from patient data should be a deliberate save rather than something
	// that opens in a browser tab and stays in the browser's cache.
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, name))

	// Nothing about a patient belongs in an intermediate cache.
	w.Header().Set("Cache-Control", "no-store")

	w.WriteHeader(http.StatusOK)
	if _, err := w.Write(out); err != nil {
		// Logged rather than answered: the status and headers have gone, so there is no way left to tell the
		// caller anything. A silent truncation is exactly the failure that produces a corrupt file on a desktop
		// with nobody knowing why.
		s.log().Warn("could not write the rendered document", "error", err)
	}
}

// pdfFilename builds a filename that means something in a folder six months later.
func pdfFilename(doc *cda.Document) string {
	parts := []string{}

	if n := safeFilenamePart(doc.Patient.Name()); n != "" {
		parts = append(parts, n)
	}
	if t := safeFilenamePart(doc.TypeName); t != "" {
		parts = append(parts, t)
	} else if t := safeFilenamePart(doc.Title); t != "" {
		parts = append(parts, t)
	}
	if len(doc.EffectiveTime) >= 8 {
		parts = append(parts, doc.EffectiveTime[:8])
	}

	if len(parts) == 0 {
		return "clinical-document.pdf"
	}

	name := strings.Join(parts, "-")

	// The Windows device names, using the check this package already has.
	//
	// Reused rather than reimplemented. Not filenameFor itself, which lowercases and appends .yaml because it
	// names channel files - a patient's name reads badly in lower case and this is a PDF. But the device-name
	// list is the same list, and a second copy of it is a second copy to keep correct.
	//
	// Hospitals run Windows, and a document whose name reduces to prn or aux is written to a device rather than a
	// file: the save appears to succeed, the bytes go nowhere, and the folder is empty afterwards.
	if isWindowsDeviceName(strings.ToLower(name)) {
		name += "-document"
	}

	return name + ".pdf"
}

// safeFilenamePart strips anything that would break a header, a filesystem, or a shell.
//
// Allow-listed rather than blocklisted. A blocklist of dangerous characters is a list somebody has to keep
// complete, and the consequence of missing one here is a response header an attacker controls.
func safeFilenamePart(s string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ' || r == '_' || r == '-':
			b.WriteByte('-')
		}
	}

	// Runs of separators collapsed, so "Ada  Lovelace" does not become "Ada--Lovelace".
	out := b.String()
	for strings.Contains(out, "--") {
		out = strings.ReplaceAll(out, "--", "-")
	}
	out = strings.Trim(out, "-")

	// Truncated, because some filesystems and some document management systems still have limits and a silently
	// truncated name collides with its neighbours.
	if len([]rune(out)) > 48 {
		out = string([]rune(out)[:48])
		out = strings.Trim(out, "-")
	}
	return out
}
