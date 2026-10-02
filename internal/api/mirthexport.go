package api

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/mirth"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tomirth"
)

// Exporting a channel as Mirth XML.
//
// Perfuse has been able to read Mirth's channels since the beginning. Being able to write one is the other half of the same promise: the
// objection to adopting a new engine is usually not "is it any good" but "what if we are wrong and we are stuck", and an export that goes
// back the way it came is cheap insurance.
//
// The download carries the losses in two places. The channel's description says it came from Perfuse and that its transformations did not
// come with it, because that is the line somebody reads in Mirth's channel list. The response also carries an X-Perfuse-Export-Notes
// header and the browser shows the full list before the file is offered, because a description is easy to scroll past and a migration that
// quietly does less than the original is the failure this whole feature exists to prevent.

// handleExportMirth converts one channel to Mirth XML and offers it as a download.
func (s *Server) handleExportMirth(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")

	ch, err := channels.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	res, err := tomirth.Channel(ch)
	if err != nil {
		// A refusal rather than an approximation. A channel exported with its S3 destination turned into something else would import
		// into Mirth cleanly and deliver to the wrong place, which is worse than not offering the file.
		s.fail(w, r, http.StatusUnprocessableEntity, err.Error())
		return
	}

	var doc bytes.Buffer
	if err := mirth.Export(&doc, res.Channel); err != nil {
		s.failErr(w, r, err)
		return
	}

	filename, err := filenameFor(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}
	filename = strings.TrimSuffix(filename, ".yaml") + ".mirth.xml"

	// The notes travel with the response so a scripted caller sees them too. Joined with a separator rather than one header per note,
	// because repeated headers are dropped by enough proxies to be unreliable.
	if len(res.Notes) > 0 {
		w.Header().Set("X-Perfuse-Export-Notes", strings.Join(res.Notes, " | "))
	}

	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+filename+"\"")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(doc.Bytes())
}

// mirthExportPreview is what the browser asks for before offering the download.
type mirthExportPreview struct {
	// Name is the channel this describes.
	Name string `json:"name"`

	// Convertible says whether a file can be produced at all.
	Convertible bool `json:"convertible"`

	// Refusal is why not, when it cannot. Empty otherwise.
	Refusal string `json:"refusal"`

	// Notes is everything that will not survive the conversion. Empty rather than null, so the browser can read its length.
	Notes []string `json:"notes"`
}

// handleExportMirthPreview reports what an export would lose, without producing one.
//
// Separate from the download because a browser cannot read the headers of a file it is saving, and the losses have to be shown before
// somebody carries the file to another system. A dialogue that lists what is missing is the only place this information is going to be
// read.
func (s *Server) handleExportMirthPreview(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")

	ch, err := channels.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	out := mirthExportPreview{Name: name, Notes: []string{}}

	res, err := tomirth.Channel(ch)
	if err != nil {
		out.Refusal = err.Error()
		s.writeJSON(w, http.StatusOK, out)
		return
	}

	out.Convertible = true
	out.Notes = res.Notes

	s.writeJSON(w, http.StatusOK, out)
}
