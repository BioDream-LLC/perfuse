package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/store"
)

// authenticate resolves a request to a session, from a bearer token or a session cookie.
//
// Two mechanisms because they serve different callers. A browser gets a cookie, which expires and is cleared on
// sign-out. A machine gets a token, which must not expire and must survive a person logging out - a fleet view that
// broke because somebody closed their laptop would be worse than no fleet view.
//
// The bearer token is checked first, because a request carrying one is unambiguously a machine and there is no
// reason to prefer a cookie that happens to be attached.
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*store.Session, bool) {
	if token, present := bearerToken(r); present {
		sess, err := s.Store.LookupAPIToken(r.Context(), token)
		if err != nil {
			if errors.Is(err, store.ErrTokenNotFound) {
				// A revoked token and an invented one give the same answer. Distinguishing them would confirm to
				// somebody guessing that a particular value was once real.
				s.fail(w, r, http.StatusUnauthorized, "the API token is not valid")
				return nil, false
			}
			s.failErr(w, r, err)
			return nil, false
		}
		return sess, true
	}

	cookie, err := r.Cookie(sessionCookie)
	if err != nil {
		s.fail(w, r, http.StatusUnauthorized, "not signed in")
		return nil, false
	}

	sess, err := s.Store.Lookup(r.Context(), cookie.Value)
	if err != nil {
		// Clear the cookie so the browser stops sending a dead token.
		s.clearSessionCookie(w)
		s.fail(w, r, http.StatusUnauthorized, "session has expired")
		return nil, false
	}

	return sess, true
}

// bearerToken extracts a token from the Authorization header.
//
// Reports presence separately from the value so that a malformed header is treated as an attempt to use a token
// rather than falling through to cookie authentication. Falling through would answer "not signed in" to somebody
// who plainly tried to sign in, and send them looking in the wrong place.
func bearerToken(r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	if header == "" {
		return "", false
	}
	const prefix = "Bearer "
	if len(header) <= len(prefix) || !strings.EqualFold(header[:len(prefix)], prefix) {
		return "", true
	}
	return strings.TrimSpace(header[len(prefix):]), true
}
