package smartauth

import (
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"time"

	"github.com/biodream-llc/perfuse/internal/store"
)

// EHR launch: a signed-in person chooses an app and a patient here, and the app is opened at its launch URL with iss and an
// opaque launch id. The app then sends the person to /authorize with that launch, and the token carries the patient (and
// encounter) chosen here. This is the launch an EHR does from inside a chart; here it stands in for the EHR.

const launchLife = 5 * time.Minute

type launchSession struct {
	user    *User
	expires time.Time
}

func (s *Server) launchApps() []*Client {
	var out []*Client
	for _, c := range s.Clients {
		if c.LaunchURL != "" && c.Kind != KindBackend {
			out = append(out, c)
		}
	}
	slices.SortFunc(out, func(a, b *Client) int {
		if a.ID < b.ID {
			return -1
		}
		return 1
	})
	return out
}

func (s *Server) handleLaunchPage(w http.ResponseWriter, r *http.Request) {
	s.render(w, "launch-signin", map[string]any{})
}

func (s *Server) handleLaunchSignIn(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		problem(w, http.StatusBadRequest, "The form could not be read.")
		return
	}
	u := s.Users[r.PostForm.Get("username")]
	ok := false
	if u != nil {
		ok, _ = store.VerifyPassword(u.PasswordHash, r.PostForm.Get("password"))
	} else {
		_, _ = store.VerifyPassword(dummyHash, r.PostForm.Get("password"))
	}
	if !ok {
		w.WriteHeader(http.StatusUnauthorized)
		s.render(w, "launch-signin", map[string]any{"Error": "That username and password did not match."})
		return
	}
	id := newID()
	s.mu.Lock()
	if s.launchSessions == nil {
		s.launchSessions = map[string]*launchSession{}
	}
	s.launchSessions[id] = &launchSession{user: u, expires: s.now().Add(pendingLife)}
	s.mu.Unlock()
	s.showLaunchChoice(w, r, id, u)
}

func (s *Server) showLaunchChoice(w http.ResponseWriter, r *http.Request, id string, u *User) {
	var patients []PatientChoice
	if own := u.patientID(); own != "" {
		patients = []PatientChoice{{ID: own, Name: u.Name}}
	} else if s.Patients != nil {
		patients, _ = s.Patients(r.Context(), r.PostForm.Get("search"))
	}
	s.render(w, "launch", map[string]any{"Req": id, "Apps": s.launchApps(), "Patients": patients, "Search": r.PostForm.Get("search")})
}

func (s *Server) handleLaunch(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		problem(w, http.StatusBadRequest, "The form could not be read.")
		return
	}
	id := r.PostForm.Get("req")
	s.mu.Lock()
	ls := s.launchSessions[id]
	s.mu.Unlock()
	if ls == nil || s.now().After(ls.expires) {
		problem(w, http.StatusBadRequest, "This session has expired. Sign in again.")
		return
	}
	if r.PostForm.Has("search") && !r.PostForm.Has("app") {
		s.showLaunchChoice(w, r, id, ls.user)
		return
	}
	client := s.Clients[r.PostForm.Get("app")]
	patient := r.PostForm.Get("patient")
	if own := ls.user.patientID(); own != "" {
		patient = own // a member launches with their own record, whatever the form says
	}
	if client == nil || client.LaunchURL == "" || client.Kind == KindBackend || patient == "" ||
		(ls.user.patientID() == "" && (s.PatientExists == nil || !s.PatientExists(r.Context(), patient))) {
		s.showLaunchChoice(w, r, id, ls.user)
		return
	}
	launch := newID()
	lc := launchContext{Patient: patient, Encounter: r.PostForm.Get("encounter"), Expires: s.now().Add(launchLife)}
	data, _ := json.Marshal(lc)
	if err := s.grants().Put(r.Context(), kindLaunch, hashKey(launch), data, lc.Expires); err != nil {
		problem(w, http.StatusServiceUnavailable, "The launch could not be recorded. Try again.")
		return
	}
	u, err := url.Parse(client.LaunchURL)
	if err != nil {
		problem(w, http.StatusInternalServerError, "The app's launch URL is not a URL.")
		return
	}
	q := u.Query()
	q.Set("iss", s.Audience)
	q.Set("launch", launch)
	u.RawQuery = q.Encode()
	http.Redirect(w, r, u.String(), http.StatusFound)
}
