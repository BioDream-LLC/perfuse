package api

import (
	"net/http"
	"strconv"
	"sync"

	"github.com/biodream-llc/perfuse/internal/manual"
	"github.com/biodream-llc/perfuse/internal/store"
)

// builtManual caches the rendered manual.
//
// Built on first request rather than at startup. It parses the whole of internal/config and every package comment in the
// tree, which is fast enough that nobody notices once but not something to add to the time between the process starting
// and a channel listening - a source that is not accepting connections yet is a source that is refusing them.
var builtManual struct {
	once sync.Once
	html []byte
	err  error
}

// handleManual serves the reference manual.
//
// # Why the manual is served at all
//
// The standing rule for this project is that everything can be done from the web interface, with little to nothing to
// configure on the box. A manual that exists only as a file next to the source fails that in the case where it matters
// most: somebody logged in at two in the morning, looking at a setting they do not recognise, on a machine where the
// source is not checked out.
//
// So it ships in the binary and is one link away from every screen.
//
// # Why viewer rather than open
//
// The manual contains no credentials and no patient data, and there is a reasonable argument for serving it to anyone.
// Viewer because it enumerates this deployment's entire configuration surface, and that is a more useful document to
// somebody probing the service than it is to somebody who has not logged in.
func (s *Server) handleManual(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	builtManual.once.Do(func() {
		prose, err := manual.LoadProse()
		if err != nil {
			builtManual.err = err
			return
		}

		// Only the written chapters and whatever can be derived without the source tree.
		//
		// The generated reference chapters need internal/config as Go source, which is not present in a deployed binary.
		// Rather than serve a manual with five empty chapters, the served copy carries the written chapters and says
		// plainly where the full reference is. A document with silently missing sections would be read as a document
		// whose author forgot them.
		doc, err := manual.BuildWrittenOnly(s.Version, prose)
		if err != nil {
			builtManual.err = err
			return
		}
		builtManual.html = []byte(doc.HTML())
	})

	if builtManual.err != nil {
		s.fail(w, r, http.StatusInternalServerError, "the manual could not be rendered: "+builtManual.err.Error())
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(builtManual.html)))
	// Cached by the browser for an hour. The manual changes when the binary does, and a reader who follows twenty links
	// through a 400 kB document should not fetch it twenty times.
	w.Header().Set("Cache-Control", "private, max-age=3600")
	_, _ = w.Write(builtManual.html)
}
