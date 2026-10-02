package api

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/biodream-llc/perfuse/internal/ldap"
	"github.com/biodream-llc/perfuse/internal/oidc"
	"github.com/biodream-llc/perfuse/internal/saml"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Reading, writing and testing the sign-on configuration as structured data.
//
// # Secrets
//
// A secret is never sent to the browser. Each is reported as set or not set, and a save that leaves the field untouched keeps whatever
// is already on disk. That is not only about exposure: the settings screen learned the hard way that a redacted placeholder can be
// saved back over a real credential, which takes single sign-on down while leaving a file that reads perfectly plausibly. Here the
// browser never holds the secret at all, so it cannot write one back.
//
// # Why testing matters more than editing
//
// Both of these configurations fail silently and identically. A username attribute that does not exist finds nobody; finding nobody is
// reported to the person signing in as a wrong password. A group name that matches nothing grants no role, and a person with no role is
// refused. In both cases the file is valid, the server starts, nothing is logged as wrong, and the first anybody knows is a telephone
// call from somebody who cannot get in - after a restart, because these settings need one.
//
// So the test endpoints run the real code paths and report what actually happened at each stage: reached the server, encrypted the
// connection, bound as the service account, found this person, read these groups, mapped them to this role. Each stage is worth
// reporting separately, because knowing which one failed is most of knowing why.

type signonSecretState struct {
	Set  bool   `json:"set"`
	From string `json:"from,omitempty"`
}

type oidcConfigJSON struct {
	Issuer      string   `json:"issuer"`
	ClientID    string   `json:"clientID"`
	RedirectURL string   `json:"redirectURL"`
	Scopes      []string `json:"scopes"`
	Label       string   `json:"label"`
	CreateUsers bool     `json:"createUsers"`

	// ClientSecretFile is a path and not a secret, so it travels.
	ClientSecretFile string `json:"clientSecretFile"`

	// ClientSecret reports only whether one is set.
	ClientSecret signonSecretState `json:"clientSecret"`

	// Roles is group name to role. Flattened from the four lists in the file, because a form with one row per group is what somebody
	// is actually trying to express, and four parallel lists make it easy to put one group in two of them.
	Roles         map[string]string `json:"roles"`
	CaseSensitive bool              `json:"caseSensitive"`
}

// samlConfigJSON is the SAML configuration as the browser exchanges it.
//
// camelCase throughout, and paired against samlFields by a test, because the browser indexes this object by a string the server
// sent. A mismatch there is not a type error - it is a control that silently edits nothing.
type samlConfigJSON struct {
	EntityID  string `json:"entityID"`
	ACSURL    string `json:"acsURL"`
	IdPSSOURL string `json:"idpSSOURL"`

	// IdPCertPEM is sent back to the browser, unlike a secret.
	//
	// A signing certificate is public by construction: it is in every response the provider sends and usually on a metadata page
	// anybody can fetch. So the rule that secrets are never read back into a form does not apply, and applying it anyway would
	// mean somebody could not see what certificate is installed - which is the first thing to check when signatures stop verifying.
	IdPCertPEM  string `json:"idpCertPEM"`
	IdPCertFile string `json:"idpCertFile"`

	GroupsAttribute string `json:"groupsAttribute"`

	Label            string `json:"label"`
	CreateUsers      bool   `json:"createUsers"`
	AllowUnsolicited bool   `json:"allowUnsolicited"`

	Roles         map[string]string `json:"roles"`
	CaseSensitive bool              `json:"caseSensitive"`
}

type ldapConfigJSON struct {
	Addr     string `json:"addr"`
	TLS      bool   `json:"tls"`
	StartTLS bool   `json:"startTLS"`
	Insecure bool   `json:"insecure"`

	BindDN       string            `json:"bindDN"`
	BindPassword signonSecretState `json:"bindPassword"`

	UserBaseDN        string `json:"userBaseDN"`
	UsernameAttribute string `json:"usernameAttribute"`
	UniqueIDAttribute string `json:"uniqueIDAttribute"`
	UserFilter        string `json:"userFilter"`
	NameAttribute     string `json:"nameAttribute"`
	EmailAttribute    string `json:"emailAttribute"`

	MemberOfAttribute  string `json:"memberOfAttribute"`
	GroupBaseDN        string `json:"groupBaseDN"`
	GroupFilter        string `json:"groupFilter"`
	GroupNameAttribute string `json:"groupNameAttribute"`

	Roles       map[string]string `json:"roles"`
	CreateUsers bool              `json:"createUsers"`
	ButtonLabel string            `json:"buttonLabel"`
	Timeout     string            `json:"timeout"`
}

type signonFile struct {
	// Configured reports whether the server was started with a path for this file at all. Without one there is nowhere to save, which
	// is a different situation from a file that exists and is empty, and the two need different advice.
	Configured bool   `json:"configured"`
	Path       string `json:"path"`
	Exists     bool   `json:"exists"`

	// Active reports whether this method is in use by the running server. A saved file that has not been loaded is the normal state
	// after an edit, and saying so is the difference between waiting hopefully and restarting.
	Active bool `json:"active"`

	// Problem carries a file that exists and does not load. Reported rather than shown as empty, because an empty form invites
	// somebody to fill it in and overwrite a file whose only fault is one bad line.
	Problem string `json:"problem,omitempty"`
}

type signonResponse struct {
	OIDC struct {
		signonFile
		Config oidcConfigJSON `json:"config"`
	} `json:"oidc"`

	LDAP struct {
		signonFile
		Config ldapConfigJSON `json:"config"`
	} `json:"ldap"`

	SAML struct {
		signonFile
		Config samlConfigJSON `json:"config"`
	} `json:"saml"`

	OIDCFields  []signonField `json:"oidcFields"`
	SAMLFields  []signonField `json:"samlFields"`
	LDAPFields  []signonField `json:"ldapFields"`
	LDAPPresets []ldapPreset  `json:"ldapPresets"`

	Roles        []string          `json:"roles"`
	LDAPDefaults map[string]string `json:"ldapDefaults"`

	// LocalAccountsExist guards against locking everybody out. If federated sign-in is the only way in and it is misconfigured, the
	// way back is a local account - so the screen has to know whether one exists before it encourages somebody to rely on this.
	LocalAdmins int `json:"localAdmins"`
}

func (s *Server) handleSignon(w http.ResponseWriter, r *http.Request, session *store.Session) {
	var out signonResponse

	out.OIDCFields = oidcFields()
	out.SAMLFields = samlFields()
	out.LDAPFields = ldapFields()
	out.LDAPPresets = ldapPresets()
	out.Roles = signonRoles()
	out.LDAPDefaults = ldapDefaults()

	// OIDC.
	oidcPath := s.SettingsPaths.OIDC
	out.OIDC.Configured = oidcPath != ""
	out.OIDC.Path = oidcPath
	out.OIDC.Active = s.OIDC != nil
	out.OIDC.Config.Roles = map[string]string{}
	out.OIDC.Config.Scopes = []string{}

	if oidcPath != "" {
		if _, err := os.Stat(oidcPath); err == nil {
			out.OIDC.Exists = true

			// Read without contacting the provider. A provider being unreachable is very likely the reason somebody has opened
			// this screen, and it must not stop them seeing or fixing what is written down.
			cfg, err := oidc.LoadFileWithoutDiscovery(oidcPath)
			switch {
			case err != nil:
				out.OIDC.Problem = err.Error()
			default:
				out.OIDC.Config = oidcToJSON(cfg)
			}
		}
	}

	// SAML.
	samlPath := s.SettingsPaths.SAML
	out.SAML.Configured = samlPath != ""
	out.SAML.Path = samlPath
	out.SAML.Active = s.SAML != nil
	out.SAML.Config.Roles = map[string]string{}

	if samlPath != "" {
		if _, err := os.Stat(samlPath); err == nil {
			out.SAML.Exists = true

			cfg, err := saml.LoadConfig(samlPath)
			switch {
			case err != nil:
				// Reported rather than hidden. A file that will not load is the likeliest reason somebody opened this screen, and
				// showing nothing would leave them with a blank form and no idea that a broken file is sitting behind it.
				out.SAML.Problem = err.Error()
			default:
				out.SAML.Config = samlToJSON(cfg)
			}
		}
	}

	// LDAP.
	ldapPath := s.SettingsPaths.LDAP
	out.LDAP.Configured = ldapPath != ""
	out.LDAP.Path = ldapPath
	out.LDAP.Active = s.LDAP != nil
	out.LDAP.Config.Roles = map[string]string{}

	if ldapPath != "" {
		if _, err := os.Stat(ldapPath); err == nil {
			out.LDAP.Exists = true

			cfg, err := ldap.LoadConfig(ldapPath)
			switch {
			case err != nil:
				out.LDAP.Problem = err.Error()
			default:
				out.LDAP.Config = ldapToJSON(cfg)
			}
		}
	}

	// How many local administrators there are, so the screen can warn before somebody depends entirely on a directory.
	//
	// Scoped to the caller's tenant rather than read from the whole store. Counting every tenant's administrators would answer the
	// wrong question here - the warning is about whether there is a way back into this tenant - and it would leak the size of other
	// tenants' user lists to somebody who cannot see them. An existing guard caught this, which is the second time today a check
	// somebody wrote earlier has found something in work I was about to commit.
	if scoped := s.storeFor(session); scoped != nil {
		if users, err := scoped.ListUsers(r.Context()); err == nil {
			for _, u := range users {
				if u.Role == store.RoleAdmin || u.Role == store.RolePlatform {
					out.LocalAdmins++
				}
			}
		}
	}

	s.ok(w, out)
}

func oidcToJSON(cfg *oidc.FileConfig) oidcConfigJSON {
	out := oidcConfigJSON{
		Issuer:           cfg.Issuer,
		ClientID:         cfg.ClientID,
		RedirectURL:      cfg.RedirectURL,
		Scopes:           cfg.Scopes,
		Label:            cfg.Label,
		CreateUsers:      cfg.CreateUsers,
		ClientSecretFile: cfg.ClientSecretFile,
		Roles:            map[string]string{},
		CaseSensitive:    cfg.Roles.CaseSensitive,
	}
	if out.Scopes == nil {
		out.Scopes = []string{}
	}

	out.ClientSecret.Set = cfg.ClientSecret != ""
	if cfg.ClientSecretFile != "" {
		out.ClientSecret.From = cfg.ClientSecretFile
	}

	// Flattened to one row per group. The file holds four lists, and a group appearing in two of them is a contradiction the file
	// format allows and this shape cannot express - which is the point.
	for role, groups := range map[string][]string{
		"platform": cfg.Roles.Platform,
		"admin":    cfg.Roles.Admin,
		"editor":   cfg.Roles.Editor,
		"viewer":   cfg.Roles.Viewer,
	} {
		for _, g := range groups {
			out.Roles[g] = role
		}
	}

	return out
}

func ldapToJSON(cfg *ldap.Config) ldapConfigJSON {
	out := ldapConfigJSON{
		Addr:               cfg.Addr,
		TLS:                cfg.TLS,
		StartTLS:           cfg.StartTLS,
		Insecure:           cfg.Insecure,
		BindDN:             cfg.BindDN,
		UserBaseDN:         cfg.UserBaseDN,
		UsernameAttribute:  cfg.UsernameAttribute,
		UniqueIDAttribute:  cfg.UniqueIDAttribute,
		UserFilter:         cfg.UserFilter,
		NameAttribute:      cfg.NameAttribute,
		EmailAttribute:     cfg.EmailAttribute,
		MemberOfAttribute:  cfg.MemberOfAttribute,
		GroupBaseDN:        cfg.GroupBaseDN,
		GroupFilter:        cfg.GroupFilter,
		GroupNameAttribute: cfg.GroupNameAttribute,
		Roles:              map[string]string{},
		CreateUsers:        cfg.CreateUsers,
		ButtonLabel:        cfg.ButtonLabel,
	}
	if cfg.Timeout > 0 {
		out.Timeout = cfg.Timeout.String()
	}
	out.BindPassword.Set = cfg.BindPassword != ""

	for group, role := range cfg.Roles {
		out.Roles[group] = role
	}

	return out
}

// handleSaveOIDC writes the OpenID Connect configuration.
func (s *Server) handleSaveOIDC(w http.ResponseWriter, r *http.Request, session *store.Session) {
	path := s.SettingsPaths.OIDC
	if path == "" {
		s.fail(w, r, http.StatusConflict,
			"this server was not started with a sign-on configuration file, so there is nowhere to save one. "+
				"Restart it with -oidc pointing at a file and this becomes editable.")

		return
	}

	var body struct {
		Config oidcConfigJSON `json:"config"`

		// NewSecret is only present when somebody typed one. Absent means keep what is on disk, which is why it is a pointer: an
		// empty string has to be able to mean "clear it" rather than "leave it alone".
		NewSecret *string `json:"newSecret"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	existing := &oidc.FileConfig{}
	if cfg, err := oidc.LoadFileWithoutDiscovery(path); err == nil {
		existing = cfg
	}

	cfg := &oidc.FileConfig{
		Issuer:           strings.TrimSpace(body.Config.Issuer),
		ClientID:         strings.TrimSpace(body.Config.ClientID),
		ClientSecretFile: strings.TrimSpace(body.Config.ClientSecretFile),
		RedirectURL:      strings.TrimSpace(body.Config.RedirectURL),
		Scopes:           body.Config.Scopes,
		Label:            body.Config.Label,
		CreateUsers:      body.Config.CreateUsers,
	}

	// The secret survives a save that did not mention it.
	switch {
	case body.NewSecret != nil:
		cfg.ClientSecret = strings.TrimSpace(*body.NewSecret)
	case cfg.ClientSecretFile == "":
		cfg.ClientSecret = existing.ClientSecret
	}

	// A secret and a secret file together are refused by the loader, so clear the inline one when a file is named. Doing it here
	// rather than reporting an error means switching to a file is one action instead of two.
	if cfg.ClientSecretFile != "" {
		cfg.ClientSecret = ""
	}

	cfg.Roles.CaseSensitive = body.Config.CaseSensitive
	for group, role := range body.Config.Roles {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		switch role {
		case "platform":
			cfg.Roles.Platform = append(cfg.Roles.Platform, group)
		case "admin":
			cfg.Roles.Admin = append(cfg.Roles.Admin, group)
		case "editor":
			cfg.Roles.Editor = append(cfg.Roles.Editor, group)
		case "viewer":
			cfg.Roles.Viewer = append(cfg.Roles.Viewer, group)
		default:
			s.fail(w, r, http.StatusBadRequest,
				fmt.Sprintf("%q is not a role. Use one of %s.", role, strings.Join(signonRoles(), ", ")))

			return
		}
	}
	sort.Strings(cfg.Roles.Platform)
	sort.Strings(cfg.Roles.Admin)
	sort.Strings(cfg.Roles.Editor)
	sort.Strings(cfg.Roles.Viewer)

	data, err := marshalYAMLWithHeader("Single sign-on through an OpenID Connect provider.", cfg)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("the configuration could not be written: %v", err))

		return
	}

	// Validated by writing to a temporary file and loading it through the real loader.
	//
	// The loader takes a path, not bytes, and reimplementing its checks here to avoid that is exactly the mistake the loader's own
	// comment warns about - a second implementation accepts things the first rejects, which is the failure validation exists to
	// prevent. So the real one runs, against the real file, before the real file is replaced.
	if problem := s.checkOIDCFileLoads(data); problem != "" {
		s.fail(w, r, http.StatusBadRequest, problem)

		return
	}

	if err := writeFileAtomically(path, data); err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("the configuration could not be saved: %v", err))

		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: session.Username,
		Action:   "signon.oidc.save",
		Target:   path,
		IP:       clientIP(r),
	})

	s.ok(w, map[string]any{
		"path": path,
		// Said plainly. Sign-on is read at startup, and somebody who saves a fix and waits for it to take effect is waiting for
		// nothing - while believing the problem is elsewhere.
		"note": "Saved. Sign-on settings are read when the server starts, so this takes effect after a restart.",
	})
}

func (s *Server) checkOIDCFileLoads(data []byte) string {
	tmp, err := os.CreateTemp("", "perfuse-oidc-check-*.yaml")
	if err != nil {
		return fmt.Sprintf("the configuration could not be checked: %v", err)
	}
	name := tmp.Name()
	defer func() { _ = os.Remove(name) }()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return fmt.Sprintf("the configuration could not be checked: %v", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Sprintf("the configuration could not be checked: %v", err)
	}

	if _, err := oidc.LoadFileWithoutDiscovery(name); err != nil {
		// The path in the message is a temporary file nobody cares about, so it is removed rather than shown.
		return strings.ReplaceAll(err.Error(), name+": ", "")
	}

	return ""
}

// handleSaveLDAP writes the directory configuration.
func (s *Server) handleSaveLDAP(w http.ResponseWriter, r *http.Request, session *store.Session) {
	path := s.SettingsPaths.LDAP
	if path == "" {
		s.fail(w, r, http.StatusConflict,
			"this server was not started with a directory configuration file, so there is nowhere to save one. "+
				"Restart it with -ldap pointing at a file and this becomes editable.")

		return
	}

	var body struct {
		Config      ldapConfigJSON `json:"config"`
		NewPassword *string        `json:"newPassword"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	cfg, problem := s.ldapFromJSON(body.Config, body.NewPassword, path)
	if problem != "" {
		s.fail(w, r, http.StatusBadRequest, problem)

		return
	}

	data, err := marshalYAMLWithHeader("Sign-in against a directory.", cfg)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("the configuration could not be written: %v", err))

		return
	}

	if err := writeFileAtomically(path, data); err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("the configuration could not be saved: %v", err))

		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: session.Username,
		Action:   "signon.ldap.save",
		Target:   path,
		IP:       clientIP(r),
	})

	s.ok(w, map[string]any{
		"path": path,
		"note": "Saved. Sign-on settings are read when the server starts, so this takes effect after a restart.",
	})
}

// ldapFromJSON builds and validates a directory configuration, returning a problem string rather than an error so the message reaches
// the form unchanged.
func (s *Server) ldapFromJSON(in ldapConfigJSON, newPassword *string, path string) (*ldap.Config, string) {
	existing := &ldap.Config{}
	if path != "" {
		if cfg, err := ldap.LoadConfig(path); err == nil {
			existing = cfg
		}
	}

	cfg := &ldap.Config{
		Addr:               strings.TrimSpace(in.Addr),
		TLS:                in.TLS,
		StartTLS:           in.StartTLS,
		Insecure:           in.Insecure,
		BindDN:             strings.TrimSpace(in.BindDN),
		UserBaseDN:         strings.TrimSpace(in.UserBaseDN),
		UsernameAttribute:  strings.TrimSpace(in.UsernameAttribute),
		UniqueIDAttribute:  strings.TrimSpace(in.UniqueIDAttribute),
		UserFilter:         strings.TrimSpace(in.UserFilter),
		NameAttribute:      strings.TrimSpace(in.NameAttribute),
		EmailAttribute:     strings.TrimSpace(in.EmailAttribute),
		MemberOfAttribute:  strings.TrimSpace(in.MemberOfAttribute),
		GroupBaseDN:        strings.TrimSpace(in.GroupBaseDN),
		GroupFilter:        strings.TrimSpace(in.GroupFilter),
		GroupNameAttribute: strings.TrimSpace(in.GroupNameAttribute),
		Roles:              map[string]string{},
		CreateUsers:        in.CreateUsers,
		ButtonLabel:        in.ButtonLabel,
	}

	if newPassword != nil {
		cfg.BindPassword = strings.TrimSpace(*newPassword)
	} else {
		cfg.BindPassword = existing.BindPassword
	}

	if in.Timeout != "" {
		d, err := time.ParseDuration(in.Timeout)
		if err != nil {
			return nil, fmt.Sprintf("%q is not a length of time; write it like 10s or 1m", in.Timeout)
		}
		cfg.Timeout = d
	}

	known := map[string]bool{}
	for _, role := range signonRoles() {
		known[role] = true
	}
	for group, role := range in.Roles {
		group = strings.TrimSpace(group)
		if group == "" {
			continue
		}
		if !known[role] {
			return nil, fmt.Sprintf("%q is not a role. Use one of %s.", role, strings.Join(signonRoles(), ", "))
		}
		cfg.Roles[group] = role
	}

	// The engine's own validation, which also fills defaults - so what is written down is what would run.
	if err := cfg.Validate(); err != nil {
		return nil, err.Error()
	}

	return cfg, ""
}

// marshalYAMLWithHeader writes a configuration file with a note saying what wrote it.
func marshalYAMLWithHeader(what string, v any) ([]byte, error) {
	var sb strings.Builder

	sb.WriteString("# " + what + "\n")
	sb.WriteString("#\n")
	sb.WriteString("# Written by Perfuse from the sign-on editor. Editing this by hand is fine; the editor reads\n")
	sb.WriteString("# whatever is here, and comments outside this header are not preserved.\n\n")

	out, err := yaml.Marshal(v)
	if err != nil {
		return nil, err
	}
	sb.Write(out)

	return []byte(sb.String()), nil
}

// ─────────────────────────────────────────────────────────────────────────────
// Testing a configuration before trusting it
// ─────────────────────────────────────────────────────────────────────────────

// signonStage is one step of a test, reported separately because knowing which step failed is most of knowing why.
type signonStage struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
}

type signonTestResult struct {
	OK     bool          `json:"ok"`
	Stages []signonStage `json:"stages"`

	// Role is what the tested person would be given, or empty if they would be refused. The single most useful line on the screen:
	// it is the difference between "the directory works" and "somebody can actually sign in".
	Role   string   `json:"role,omitempty"`
	Groups []string `json:"groups,omitempty"`
}

// handleTestOIDC contacts the provider and reports what discovery found.
func (s *Server) handleTestOIDC(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var body struct {
		Issuer string `json:"issuer"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	issuer := strings.TrimSpace(body.Issuer)
	out := signonTestResult{}

	if issuer == "" {
		out.Stages = append(out.Stages, signonStage{Name: "Issuer URL", Detail: "No issuer to test."})
		s.ok(w, out)

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()

	provider, err := oidc.Discover(ctx, http.DefaultClient, issuer)
	if err != nil {
		out.Stages = append(out.Stages, signonStage{
			Name: "Reach the provider",
			// The error carries the useful half - a TLS name mismatch, a 404 on the discovery path, an issuer that disagrees with
			// itself - so it is passed through rather than replaced with a summary.
			Detail: err.Error(),
		})
		s.ok(w, out)

		return
	}

	out.OK = true
	out.Stages = append(out.Stages,
		signonStage{Name: "Reach the provider", OK: true, Detail: "The discovery document was read and its issuer matches."},
		signonStage{Name: "Where people are sent to sign in", OK: true, Detail: provider.AuthURL},
		signonStage{Name: "Where Perfuse exchanges the code", OK: true, Detail: provider.TokenURL},
		signonStage{Name: "Where the signing keys are", OK: true, Detail: provider.JWKSURL},
	)

	// Reported because a provider that signs with nothing Perfuse implements is a configuration that discovers perfectly and then
	// refuses every sign-in, and the provider's own document says so in advance.
	if len(provider.AlgorithmsSupported) > 0 {
		out.Stages = append(out.Stages, signonStage{
			Name:   "Signing algorithms the provider offers",
			OK:     true,
			Detail: strings.Join(provider.AlgorithmsSupported, ", "),
		})
	}

	s.ok(w, out)
}

// handleTestLDAP connects to the directory and, if given a username, looks that person up.
//
// No password is asked for and none is needed. Everything that goes silently wrong here goes wrong before a password is checked: whether
// the server is reachable, whether the service account can bind, whether the username attribute finds anybody, and whether their groups
// map to a role. Asking for somebody's password to test a configuration would be worse in every way and would answer nothing extra.
func (s *Server) handleTestLDAP(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var body struct {
		Config      ldapConfigJSON `json:"config"`
		NewPassword *string        `json:"newPassword"`
		Username    string         `json:"username"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	cfg, problem := s.ldapFromJSON(body.Config, body.NewPassword, s.SettingsPaths.LDAP)
	if problem != "" {
		s.ok(w, signonTestResult{Stages: []signonStage{{Name: "Check the settings", Detail: problem}}})

		return
	}

	out := signonTestResult{}

	dir, err := ldap.NewDirectory(cfg, s.log())
	if err != nil {
		out.Stages = append(out.Stages, signonStage{Name: "Check the settings", Detail: err.Error()})
		s.ok(w, out)

		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), cfg.ResolvedTimeout()+5*time.Second)
	defer cancel()

	report := dir.Probe(ctx, strings.TrimSpace(body.Username))
	for _, stage := range report.Stages {
		out.Stages = append(out.Stages, signonStage{Name: stage.Name, OK: stage.OK, Detail: stage.Detail})
	}
	out.OK = report.OK
	out.Groups = report.Groups

	if report.Groups != nil {
		// The mapping is applied here rather than in the directory package, because the mapping is Perfuse's idea and the directory
		// package's job ends at reporting what the directory said.
		for _, g := range report.Groups {
			if role, ok := cfg.Roles[g]; ok {
				out.Role = role

				break
			}
		}
	}

	s.ok(w, out)
}
