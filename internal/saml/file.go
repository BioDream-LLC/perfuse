package saml

import (
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileConfig is the on-disk form of SAML sign-in settings.
//
// A file rather than flags, for the same reasons as OIDC: the certificate is multi-line and the group mapping is nested, and neither
// survives a command line intact. There is no secret here - a signing certificate is public by construction, which is the one way
// SAML is easier to handle than OIDC.
type FileConfig struct {
	EntityID string `yaml:"entity_id"`
	ACSURL   string `yaml:"acs_url"`

	IdPSSOURL string `yaml:"idp_sso_url"`

	// IdPCertPEM is the certificate inline. Multi-line YAML, usually written with a block scalar.
	IdPCertPEM string `yaml:"idp_cert_pem,omitempty"`

	// IdPCertFile reads the certificate from a separate file.
	//
	// Offered because a certificate is rotated on the identity provider's schedule rather than Perfuse's, and a separate file can be
	// replaced by whatever already manages certificates on the host without editing this one.
	IdPCertFile string `yaml:"idp_cert_file,omitempty"`

	// GroupsAttribute names the assertion attribute carrying group membership.
	//
	// Required, with no default, and that is deliberate. Entra sends "groups", Okta sends whatever the administrator named in the
	// application, ADFS sends a claim URI. A default would be wrong for two of the three and would produce a sign-in that succeeds
	// and grants nobody anything - which reads as a broken product rather than as a setting somebody has not filled in.
	GroupsAttribute string `yaml:"groups_attribute"`

	Label       string `yaml:"label,omitempty"`
	CreateUsers bool   `yaml:"create_users,omitempty"`

	// AllowUnsolicited accepts responses that answer no request this server sent.
	//
	// Off unless written down. It is how a portal tile works, and it is also what lets a response captured anywhere be posted into
	// somebody else's browser, so it is a trade a site should make on purpose.
	AllowUnsolicited bool `yaml:"allow_unsolicited,omitempty"`

	Roles struct {
		Platform      []string `yaml:"platform,omitempty"`
		Admin         []string `yaml:"admin,omitempty"`
		Editor        []string `yaml:"editor,omitempty"`
		Viewer        []string `yaml:"viewer,omitempty"`
		CaseSensitive bool     `yaml:"case_sensitive,omitempty"`
	} `yaml:"roles"`
}

// LoadConfig reads and validates a SAML configuration file.
//
// Everything is checked here rather than at somebody's first sign-in, including parsing the certificate. A certificate that is
// truncated, PEM-wrapped twice or actually a private key is a common enough mistake, and the moment to find out is when the file is
// saved rather than when a person cannot get in.
func LoadConfig(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var cfg FileConfig

	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)

	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}

	if err := cfg.Validate(path); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Validate reports everything wrong with a configuration, or nil.
//
// dir is used to resolve a relative certificate file, and may be the configuration file's own path.
func (c *FileConfig) Validate(fromPath string) error {
	var problems []string

	if strings.TrimSpace(c.EntityID) == "" {
		problems = append(problems, "entity_id is required: it identifies Perfuse to the identity provider and is the audience "+
			"an assertion must be addressed to")
	}

	if strings.TrimSpace(c.ACSURL) == "" {
		problems = append(problems, "acs_url is required: it is where the identity provider posts its response, and has to match "+
			"what is registered there exactly")
	}

	if strings.TrimSpace(c.IdPSSOURL) == "" {
		problems = append(problems, "idp_sso_url is required: it is where the browser is sent to sign in")
	}

	if strings.TrimSpace(c.GroupsAttribute) == "" {
		problems = append(problems, "groups_attribute is required and has no default: Entra sends \"groups\", Okta sends whatever "+
			"the application was configured with, ADFS sends a claim URI, so a guess would grant nobody anything")
	}

	pem, err := c.Certificate()
	switch {
	case err != nil:
		problems = append(problems, err.Error())
	default:
		if _, err := parseCertPEM(pem); err != nil {
			problems = append(problems, fmt.Sprintf("the identity provider certificate could not be read: %v", err))
		}
	}

	// An empty mapping is refused rather than accepted as "nobody yet".
	//
	// A configuration that authenticates people and grants none of them anything looks exactly like a broken product from the
	// outside: the sign-in works, the round trip completes, and every person is turned away with no indication why.
	if len(c.Roles.Platform) == 0 && len(c.Roles.Admin) == 0 && len(c.Roles.Editor) == 0 && len(c.Roles.Viewer) == 0 {
		problems = append(problems, "no group is mapped to any role, so nobody could sign in: map at least one group under roles")
	}

	if len(problems) > 0 {
		where := fromPath
		if where == "" {
			where = "the SAML configuration"
		}

		return fmt.Errorf("%s is not valid:\n  - %s", where, strings.Join(problems, "\n  - "))
	}

	return nil
}

// Certificate returns the PEM certificate, from the file when one is named.
func (c *FileConfig) Certificate() (string, error) {
	inline := strings.TrimSpace(c.IdPCertPEM)
	file := strings.TrimSpace(c.IdPCertFile)

	switch {
	case inline != "" && file != "":
		// Refused rather than resolved by precedence. Two sources for one value means somebody edited one and expected it to take
		// effect, and a precedence rule decides silently which of them was wasting their time.
		return "", fmt.Errorf("both idp_cert_pem and idp_cert_file are set: choose one, because a precedence rule would " +
			"silently ignore whichever you edited")
	case file != "":
		data, err := os.ReadFile(file)
		if err != nil {
			return "", fmt.Errorf("reading idp_cert_file %s: %w", file, err)
		}

		return string(data), nil
	case inline != "":
		return inline, nil
	default:
		return "", fmt.Errorf("one of idp_cert_pem or idp_cert_file is required: it is the only certificate trusted to have " +
			"signed a response")
	}
}

// RoleMapping is the group-to-role mapping in the form the sign-in code wants.
func (c *FileConfig) RoleMapping() (platform, admin, editor, viewer []string, caseSensitive bool) {
	return c.Roles.Platform, c.Roles.Admin, c.Roles.Editor, c.Roles.Viewer, c.Roles.CaseSensitive
}
