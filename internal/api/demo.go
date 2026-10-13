package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/store"
)

// DemoAccount is a shared, published sign-in for a public demonstration server.
//
// Its password is shown on the sign-in page, so it is a viewer (it can look at everything and change nothing), and it can
// never change its own password or add a passkey: the first visitor to do either would lock out everyone after them.
type DemoAccount struct {
	Username string
	Password string
}

// ParseDemoAccount reads "username:password".
func ParseDemoAccount(v string) (*DemoAccount, error) {
	v = strings.TrimSpace(v)
	if v == "" {
		return nil, nil
	}
	user, pass, ok := strings.Cut(v, ":")
	user = strings.TrimSpace(user)
	if !ok || user == "" || pass == "" {
		return nil, fmt.Errorf("-demo-account wants username:password, got %q", v)
	}
	return &DemoAccount{Username: user, Password: pass}, nil
}

// EnsureDemoAccount makes the account exist as a viewer with the published password, and puts it back that way at every
// start: if anyone with more rights changed it, the page would otherwise go on showing a password that no longer works.
func EnsureDemoAccount(ctx context.Context, st *store.Store, d *DemoAccount) error {
	if d == nil {
		return nil
	}
	u, err := st.GetUser(ctx, d.Username)
	if errors.Is(err, store.ErrNotFound) {
		_, err = st.CreateUser(ctx, d.Username, d.Password, store.RoleViewer)
		return err
	}
	if err != nil {
		return err
	}
	if err := st.SetPassword(ctx, u.ID, d.Password); err != nil {
		return err
	}
	if u.Role != store.RoleViewer {
		return st.SetRole(ctx, u.ID, store.RoleViewer)
	}
	return nil
}

// isDemo says whether a session is the shared demo account.
func (s *Server) isDemo(sess *store.Session) bool {
	return s.Demo != nil && sess != nil && strings.EqualFold(sess.Username, s.Demo.Username)
}

// refuseDemo answers 403 for the shared account and reports whether it did.
func (s *Server) refuseDemo(w http.ResponseWriter, r *http.Request, sess *store.Session, what string) bool {
	if !s.isDemo(sess) {
		return false
	}
	s.fail(w, r, http.StatusForbidden, "this is the shared demo account, so it cannot "+what+": everybody signs in with it")
	return true
}
