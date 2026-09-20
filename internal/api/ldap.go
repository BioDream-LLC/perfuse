package api

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/ldap"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tenant"
)

// LDAPConfig holds the directory the server authenticates against.
type LDAPConfig struct {
	// Directory talks to the directory.
	Directory *ldap.Directory

	// Settings are the loaded configuration, for role mapping and the button label.
	Settings *ldap.Config
}

// ldapEnabled reports whether a directory is configured.
func (s *Server) ldapEnabled() bool {
	return s.LDAP != nil && s.LDAP.Directory != nil && s.LDAP.Settings != nil
}

// signInWithDirectory authenticates a username and password against the directory.
//
// Returns the session token and the user. A nil error with an empty token means the directory did not recognise them and
// the caller should fall through to a local account, which is what makes break-glass access work when the directory is
// unreachable.
func (s *Server) signInWithDirectory(r *http.Request, tid tenant.ID, username, password string) (string, *store.User, error) {
	if !s.ldapEnabled() {
		return "", nil, nil
	}

	id, err := s.LDAP.Directory.Authenticate(r.Context(), username, password)
	if err != nil {
		return "", nil, err
	}

	role, ok := s.LDAP.Settings.RoleFor(id.Groups)
	if !ok {
		// The groups that were actually sent are named, because the commonest cause is a mapping written against a
		// group name that does not exist - and without this an administrator has to guess what the directory
		// returned. Group names are not patient data.
		s.log().Warn("a directory sign-in was refused for having no mapped role",
			"username", username,
			"dn", id.DN,
			"groups", strings.Join(id.Groups, ","),
		)
		return "", nil, fmt.Errorf("this account is in no group that maps to a Perfuse role")
	}

	external := store.ExternalIdentity{
		// The address identifies the directory. Two directories can each hold a person with the same DN, and the
		// subject alone would then let one stand in for the other.
		Issuer: "ldap:" + s.LDAP.Settings.Addr,
		// The stable identifier rather than the DN when the directory has one, so moving somebody between
		// organisational units does not read as a different person on their next sign-in.
		Subject:     id.StableID(),
		Email:       id.Email,
		DisplayName: id.DisplayName,
		Username:    username,
		Role:        store.Role(role),
	}

	token, u, created, err := s.Store.SignInExternal(
		r.Context(), external, s.LDAP.Settings.CreateUsers, clientIP(r), r.UserAgent())
	if err != nil {
		if errors.Is(err, store.ErrLocalAccountExists) {
			// Refused rather than merged, and the message says what to do. Attaching a directory identity to an
			// existing local account by name would mean anybody the directory recognises as "admin" takes over
			// the local admin account - and that failure is silent.
			return "", nil, fmt.Errorf("a local account is already called %q; rename one of the two, because "+
				"matching them by name would let the directory take over a local account", username)
		}
		if errors.Is(err, store.ErrNoExternalMatch) {
			return "", nil, fmt.Errorf("this directory account has no Perfuse account, and creating them is " +
				"switched off")
		}
		return "", nil, err
	}

	if created {
		s.log().Info("a Perfuse account was created from a directory sign-in",
			"username", username, "dn", id.DN, "role", role)
	}

	return token, u, nil
}

// handleAuthMethodsLDAP adds the directory to the advertised sign-in methods.
//
// The local form is never hidden. An integration engine often sits where the directory is one more thing that can be
// unreachable, and when it is, somebody still has to get in to find out why.
func (s *Server) ldapButtonLabel() string {
	if !s.ldapEnabled() {
		return ""
	}
	return s.LDAP.Settings.ButtonLabel
}
