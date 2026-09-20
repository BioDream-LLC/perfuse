package api

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/oidc"
	"github.com/biodream-llc/perfuse/internal/saml"
	"github.com/biodream-llc/perfuse/internal/store"
)

// SAML configuration, from the browser.
//
// Written because a feature reachable only by editing a file on the server is not reachable: somebody has to have shell access, know
// the path, know the schema, and restart the process. SAML was worse than that - it had no file format either, so the only way to
// configure it was to edit Go.

// samlToJSON converts a loaded file into what the browser edits.
func samlToJSON(cfg *saml.FileConfig) samlConfigJSON {
	out := samlConfigJSON{
		EntityID:         cfg.EntityID,
		ACSURL:           cfg.ACSURL,
		IdPSSOURL:        cfg.IdPSSOURL,
		IdPCertPEM:       cfg.IdPCertPEM,
		IdPCertFile:      cfg.IdPCertFile,
		GroupsAttribute:  cfg.GroupsAttribute,
		Label:            cfg.Label,
		CreateUsers:      cfg.CreateUsers,
		AllowUnsolicited: cfg.AllowUnsolicited,
		CaseSensitive:    cfg.Roles.CaseSensitive,
		Roles:            map[string]string{},
	}

	// Flattened to group → role, which is how the editor presents it: somebody thinks in terms of a group they know the name of,
	// not in terms of a role with a list hanging off it.
	for _, pair := range []struct {
		role   string
		groups []string
	}{
		{"platform", cfg.Roles.Platform},
		{"admin", cfg.Roles.Admin},
		{"editor", cfg.Roles.Editor},
		{"viewer", cfg.Roles.Viewer},
	} {
		for _, g := range pair.groups {
			out.Roles[g] = pair.role
		}
	}

	return out
}

// handleSaveSAML writes the SAML configuration.
func (s *Server) handleSaveSAML(w http.ResponseWriter, r *http.Request, session *store.Session) {
	path := s.SettingsPaths.SAML
	if path == "" {
		s.fail(w, r, http.StatusConflict,
			"this server was not started with a SAML configuration file, so there is nowhere to save one. "+
				"Restart it with -saml pointing at a file and this becomes editable.")

		return
	}

	var body struct {
		Config samlConfigJSON `json:"config"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	cfg := &saml.FileConfig{
		EntityID:         strings.TrimSpace(body.Config.EntityID),
		ACSURL:           strings.TrimSpace(body.Config.ACSURL),
		IdPSSOURL:        strings.TrimSpace(body.Config.IdPSSOURL),
		IdPCertPEM:       strings.TrimSpace(body.Config.IdPCertPEM),
		IdPCertFile:      strings.TrimSpace(body.Config.IdPCertFile),
		GroupsAttribute:  strings.TrimSpace(body.Config.GroupsAttribute),
		Label:            strings.TrimSpace(body.Config.Label),
		CreateUsers:      body.Config.CreateUsers,
		AllowUnsolicited: body.Config.AllowUnsolicited,
	}

	cfg.Roles.CaseSensitive = body.Config.CaseSensitive

	// Sorted so that saving twice without changing anything produces the same file. Go maps range randomly, and a diff that changes
	// every save makes a configuration file useless to keep in version control.
	groups := make([]string, 0, len(body.Config.Roles))
	for g := range body.Config.Roles {
		groups = append(groups, g)
	}

	sort.Strings(groups)

	for _, g := range groups {
		name := strings.TrimSpace(g)
		if name == "" {
			continue
		}

		switch body.Config.Roles[g] {
		case "platform":
			cfg.Roles.Platform = append(cfg.Roles.Platform, name)
		case "admin":
			cfg.Roles.Admin = append(cfg.Roles.Admin, name)
		case "editor":
			cfg.Roles.Editor = append(cfg.Roles.Editor, name)
		case "viewer":
			cfg.Roles.Viewer = append(cfg.Roles.Viewer, name)
		default:
			s.fail(w, r, http.StatusBadRequest,
				fmt.Sprintf("group %q is mapped to %q, which is not a role", name, body.Config.Roles[g]))

			return
		}
	}

	data, err := marshalYAMLWithHeader("Single sign-on through a SAML 2.0 identity provider.", cfg)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("the configuration could not be written: %v", err))

		return
	}

	// Validated by writing to a temporary file and loading it through the real loader, for the same reason the OIDC save does:
	// a second implementation of the checks accepts things the first rejects, which is the failure validation exists to prevent.
	if problem := s.checkSAMLFileLoads(data); problem != "" {
		s.fail(w, r, http.StatusBadRequest, problem)

		return
	}

	if err := writeFileAtomically(path, data); err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("the configuration could not be saved: %v", err))

		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: session.Username, Action: "signon.saml.save", IP: clientIP(r),
	})

	s.ok(w, map[string]any{
		"saved": true,
		// Said plainly, because a screen that reported success while nothing changed is the defect this project keeps finding. The
		// service provider is built at startup so that a bad certificate is reported then rather than at somebody's first sign-in,
		// and the cost of that choice is this sentence.
		"note": "Saved. SAML sign-in uses this when the server next starts.",
	})
}

// checkSAMLFileLoads runs the real loader over the bytes about to be written.
func (s *Server) checkSAMLFileLoads(data []byte) string {
	dir, err := os.MkdirTemp("", "perfuse-saml-check")
	if err != nil {
		return fmt.Sprintf("the configuration could not be checked: %v", err)
	}

	defer func() { _ = os.RemoveAll(dir) }()

	tmp := filepath.Join(dir, "saml.yaml")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return fmt.Sprintf("the configuration could not be checked: %v", err)
	}

	if _, err := saml.LoadConfig(tmp); err != nil {
		// The loader names the temporary file, which means nothing to the reader, so it is replaced by what they are editing.
		return strings.ReplaceAll(err.Error(), tmp, "the SAML configuration")
	}

	return ""
}

// handleTestSAML checks a configuration as far as it can be checked without a person signing in.
//
// The limit is honest and stated: nothing here proves somebody can sign in, because that needs a real assertion from a real identity
// provider about a real person, and the only way to get one is for that person to sign in. What this does catch is every mistake that
// can be found without them - which is most of them, and all of the ones that produce an unreadable error later.
func (s *Server) handleTestSAML(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var body struct {
		Config samlConfigJSON `json:"config"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	result := signonTestResult{Stages: []signonStage{}}

	add := func(name string, ok bool, detail string) {
		result.Stages = append(result.Stages, signonStage{Name: name, OK: ok, Detail: detail})
	}

	cfg := &saml.FileConfig{
		EntityID:         strings.TrimSpace(body.Config.EntityID),
		ACSURL:           strings.TrimSpace(body.Config.ACSURL),
		IdPSSOURL:        strings.TrimSpace(body.Config.IdPSSOURL),
		IdPCertPEM:       strings.TrimSpace(body.Config.IdPCertPEM),
		IdPCertFile:      strings.TrimSpace(body.Config.IdPCertFile),
		GroupsAttribute:  strings.TrimSpace(body.Config.GroupsAttribute),
		AllowUnsolicited: body.Config.AllowUnsolicited,
	}

	for group, role := range body.Config.Roles {
		switch role {
		case "platform":
			cfg.Roles.Platform = append(cfg.Roles.Platform, group)
		case "admin":
			cfg.Roles.Admin = append(cfg.Roles.Admin, group)
		case "editor":
			cfg.Roles.Editor = append(cfg.Roles.Editor, group)
		case "viewer":
			cfg.Roles.Viewer = append(cfg.Roles.Viewer, group)
		}
	}

	// The settings, as a whole.
	if err := cfg.Validate(""); err != nil {
		add("Settings", false, err.Error())
		result.OK = false
		s.ok(w, result)

		return
	}

	add("Settings", true, "Every required setting is present and the role mapping grants somebody something.")

	// The certificate, specifically, because it is the one field where a copy-and-paste error is both likely and silent until a
	// signature fails.
	pem, err := cfg.Certificate()
	if err != nil {
		add("Signing certificate", false, err.Error())
		result.OK = false
		s.ok(w, result)

		return
	}

	sp, err := saml.New(saml.Config{
		EntityID:         cfg.EntityID,
		ACSPath:          cfg.ACSURL,
		IdPSSOURL:        cfg.IdPSSOURL,
		IdPCertPEM:       pem,
		AllowUnsolicited: cfg.AllowUnsolicited,
	})
	if err != nil {
		add("Signing certificate", false, err.Error())
		result.OK = false
		s.ok(w, result)

		return
	}

	add("Signing certificate", true, "Read and usable for verifying signatures.")

	// A request can actually be built, which exercises the SSO URL as a URL rather than as a string somebody typed.
	redirectURL, requestID, err := sp.BuildAuthnRequest("/")
	switch {
	case err != nil:
		add("Sign-in request", false, err.Error())
		result.OK = false
	case requestID == "":
		add("Sign-in request", false, "The request carried no id, so no response could be tied back to it.")
		result.OK = false
	default:
		add("Sign-in request", true, "Built. The browser would be sent to "+redirectURL[:minInt(len(redirectURL), 60)]+"…")
	}

	// What this cannot tell you, said rather than left to be assumed.
	add("What is left to check", true,
		"Nothing here proves somebody can sign in. That needs a real assertion about a real person, so the remaining checks are "+
			"a real sign-in: whether the provider sends the group attribute named above, and whether its values match the groups "+
			"mapped to roles. If a sign-in is refused for having no role, the server log names the attributes that did arrive.")

	if result.OK || len(result.Stages) > 0 {
		result.OK = true

		for _, st := range result.Stages {
			if !st.OK {
				result.OK = false
			}
		}
	}

	s.ok(w, result)
}

// samlRoleMapping builds the runtime mapping from a loaded file.
func samlRoleMapping(cfg *saml.FileConfig) *oidc.RoleMapping {
	return &oidc.RoleMapping{
		Platform:      cfg.Roles.Platform,
		Admin:         cfg.Roles.Admin,
		Editor:        cfg.Roles.Editor,
		Viewer:        cfg.Roles.Viewer,
		CaseSensitive: cfg.Roles.CaseSensitive,
	}
}

// SAMLFromFile builds a runtime configuration from a file, for the server to use at startup.
//
// Here rather than in cmd so that the assembly is tested: this is where a field can be read from the file and silently not passed on,
// which produces a setting that exists everywhere except where it matters.
func SAMLFromFile(path string) (*SAMLConfig, error) {
	cfg, err := saml.LoadConfig(path)
	if err != nil {
		return nil, err
	}

	pem, err := cfg.Certificate()
	if err != nil {
		return nil, err
	}

	sp, err := saml.New(saml.Config{
		EntityID:         cfg.EntityID,
		ACSPath:          cfg.ACSURL,
		IdPSSOURL:        cfg.IdPSSOURL,
		IdPCertPEM:       pem,
		AllowUnsolicited: cfg.AllowUnsolicited,
	})
	if err != nil {
		return nil, err
	}

	return &SAMLConfig{
		EntityID:         cfg.EntityID,
		ACSURL:           cfg.ACSURL,
		IdPSSOURL:        cfg.IdPSSOURL,
		IdPCertPEM:       pem,
		GroupsAttribute:  cfg.GroupsAttribute,
		Roles:            samlRoleMapping(cfg),
		CreateUsers:      cfg.CreateUsers,
		AllowUnsolicited: cfg.AllowUnsolicited,
		Label:            cfg.Label,
		SP:               sp,
	}, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}

	return b
}
