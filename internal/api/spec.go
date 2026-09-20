package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/config"

	"github.com/biodream-llc/perfuse/internal/spec"
	"github.com/biodream-llc/perfuse/internal/store"
)

// The interface specification for one channel.
//
// GET /api/channels/{name}/spec returns it as JSON; add ?format=markdown for the document itself.
//
// Viewer, because it describes configuration rather than exposing it: no credential appears in a
// specification by construction, and the people most likely to need this - an analyst answering a
// vendor's question about the interface - are exactly the people who should not need write access
// to get it.

func (s *Server) handleChannelSpec(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	name := r.PathValue("name")

	c, err := channels.Get(name)
	if err != nil {
		s.failErr(w, r, err)
		return
	}

	doc := spec.Build(c)

	if strings.EqualFold(r.URL.Query().Get("format"), "markdown") {
		body := doc.Markdown()

		// Served as a download with a sensible name, because the point of this is to be sent
		// to somebody. A specification that has to be copied out of a browser pane is a
		// specification somebody retypes into Word, which is how it starts drifting again.
		w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
		w.Header().Set("Content-Disposition",
			`attachment; filename="`+safeFileName(name)+`-interface.md"`)
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(body))
		return
	}

	s.ok(w, doc)
}

// safeFileName keeps a channel name from escaping the Content-Disposition header.
//
// A channel name comes from a file on disk and is already constrained, but a header value built
// from a name is a header value built from input, and the cost of being careful is four lines.
func safeFileName(in string) string {
	out := strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return r
		case r == '-', r == '_':
			return r
		default:
			return '-'
		}
	}, in)

	if out == "" {
		return "channel"
	}
	return out
}

// The whole-installation inventory.
//
// GET /api/spec returns one Markdown document covering every channel this server has loaded.
//
// Viewer, for the same reason the per-channel document is: it describes configuration and contains no credential by
// construction. The person who needs it is usually an analyst answering an auditor, and requiring write access to produce a
// read-only document would put the tool behind a role the reader should not have.
//
// Its own route rather than a query parameter on the channel route, because "every channel" is not a channel - a channel
// called "all" is a legitimate name and would collide.
func (s *Server) handleSpecBundle(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	channels, ok := s.channelsFor(w, r, sess)
	if !ok {
		return
	}

	// List gives summaries and the names of files that would not load; the documents need the channels
	// themselves, so each valid one is read back.
	summaries, broken, err := channels.List()
	if err != nil {
		s.failErr(w, r, err)

		return
	}

	all := make([]*config.Channel, 0, len(summaries))
	for _, sum := range summaries {
		c, err := channels.Get(sum.Name)
		if err != nil {
			// Skipped rather than failing the whole document, because one unreadable channel should not
			// deny somebody the other thirty-nine. It is named in the document instead, below.
			broken[sum.Name] = err.Error()

			continue
		}
		all = append(all, c)
	}

	body := spec.Bundle(all, broken, time.Now())

	// Markdown only, with no JSON form.
	//
	// The per-channel endpoint offers both because the interface renders the JSON. Nothing renders this: it exists to
	// be sent to somebody or committed to a repository, and a JSON version would be a second shape to keep in step
	// with no reader.
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="interface-inventory.md"`)
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte(body))
}
