package oidc

import (
	"context"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// FileConfig is the on-disk form of federated sign-in settings.
//
// A file rather than flags. The client secret would otherwise appear in the process list and in shell history, and the group
// mapping is a nested structure that flags express badly.
type FileConfig struct {
	Issuer       string `yaml:"issuer"`
	ClientID     string `yaml:"client_id"`
	ClientSecret string `yaml:"client_secret"`

	// ClientSecretFile reads the secret from a separate file.
	//
	// Offered because the configuration file itself is often checked into a repository or templated by a deployment tool,
	// and the secret is the one thing that must not be.
	ClientSecretFile string `yaml:"client_secret_file,omitempty"`

	RedirectURL string   `yaml:"redirect_url"`
	Scopes      []string `yaml:"scopes,omitempty"`

	Label       string `yaml:"label,omitempty"`
	CreateUsers bool   `yaml:"create_users,omitempty"`

	Roles struct {
		Platform      []string `yaml:"platform,omitempty"`
		Admin         []string `yaml:"admin,omitempty"`
		Editor        []string `yaml:"editor,omitempty"`
		Viewer        []string `yaml:"viewer,omitempty"`
		CaseSensitive bool     `yaml:"case_sensitive,omitempty"`
	} `yaml:"roles"`
}

// LoadFile reads and validates a configuration file.
//
// Validated here rather than at first sign-in. A wrong issuer or an empty role mapping should stop the server starting, because
// the alternative is discovering it when somebody cannot get in - which is the worst possible moment.
// LoadFileWithoutDiscovery reads and validates a configuration file without contacting the provider.
//
// For the settings editor, which has to be able to save a corrected client secret while the provider is unreachable -
// very likely the reason somebody is editing the file. Discovery needs the network, and a provider being briefly down must
// not stop a fix being saved.
//
// Everything that can be checked locally is checked: unknown keys, a missing issuer, an empty role mapping, a secret file
// that cannot be read. Only the round trip to the provider is skipped.
func LoadFileWithoutDiscovery(path string) (*FileConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var cfg FileConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	// Unknown keys are an error, matching every other configuration file here: a misspelled key that is silently ignored is
	// a setting somebody believes is in effect and is not.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}

	if cfg.ClientSecretFile != "" {
		if cfg.ClientSecret != "" {
			return nil, fmt.Errorf("%s sets both client_secret and client_secret_file; use one", path)
		}
		secret, err := os.ReadFile(cfg.ClientSecretFile)
		if err != nil {
			return nil, fmt.Errorf("reading the client secret from %s: %w", cfg.ClientSecretFile, err)
		}
		// Trimmed, because a secret file written by an editor or an echo almost always ends in a newline, and a secret with
		// a trailing newline fails authentication with a message that blames the secret's contents.
		cfg.ClientSecret = strings.TrimSpace(string(secret))
	}

	var problems []string
	if strings.TrimSpace(cfg.Issuer) == "" {
		problems = append(problems, "issuer is required")
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		problems = append(problems, "client_id is required")
	}
	if strings.TrimSpace(cfg.RedirectURL) == "" {
		problems = append(problems, "redirect_url is required, and must match what is registered with the provider exactly")
	}
	if len(problems) > 0 {
		return nil, fmt.Errorf("%s: %s", path, strings.Join(problems, "; "))
	}

	mapping := &RoleMapping{
		Platform:      cfg.Roles.Platform,
		Admin:         cfg.Roles.Admin,
		Editor:        cfg.Roles.Editor,
		Viewer:        cfg.Roles.Viewer,
		CaseSensitive: cfg.Roles.CaseSensitive,
	}
	if errs := mapping.Validate(); len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		return nil, fmt.Errorf("%s: %s", path, strings.Join(msgs, "; "))
	}

	// The mapping validated above is the same one LoadFile builds, so nothing further is checked here: everything that
	// can be decided without the network has been.
	return &cfg, nil
}

func LoadFile(ctx context.Context, path string) (*FileConfig, *Provider, *RoleMapping, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("reading %s: %w", path, err)
	}

	var cfg FileConfig
	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	// Unknown keys are an error, matching every other configuration file here: a misspelled key that is silently ignored is
	// a setting somebody believes is in effect and is not.
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, nil, nil, fmt.Errorf("reading %s: %w", path, err)
	}

	if cfg.ClientSecretFile != "" {
		if cfg.ClientSecret != "" {
			return nil, nil, nil, fmt.Errorf("%s sets both client_secret and client_secret_file; use one", path)
		}
		secret, err := os.ReadFile(cfg.ClientSecretFile)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("reading the client secret from %s: %w", cfg.ClientSecretFile, err)
		}
		// Trimmed, because a secret file written by an editor or an echo almost always ends in a newline, and a secret with
		// a trailing newline fails authentication with a message that blames the secret's contents.
		cfg.ClientSecret = strings.TrimSpace(string(secret))
	}

	var problems []string
	if strings.TrimSpace(cfg.Issuer) == "" {
		problems = append(problems, "issuer is required")
	}
	if strings.TrimSpace(cfg.ClientID) == "" {
		problems = append(problems, "client_id is required")
	}
	if strings.TrimSpace(cfg.RedirectURL) == "" {
		problems = append(problems, "redirect_url is required, and must match what is registered with the provider exactly")
	}
	if len(problems) > 0 {
		return nil, nil, nil, fmt.Errorf("%s: %s", path, strings.Join(problems, "; "))
	}

	mapping := &RoleMapping{
		Platform:      cfg.Roles.Platform,
		Admin:         cfg.Roles.Admin,
		Editor:        cfg.Roles.Editor,
		Viewer:        cfg.Roles.Viewer,
		CaseSensitive: cfg.Roles.CaseSensitive,
	}
	if errs := mapping.Validate(); len(errs) > 0 {
		msgs := make([]string, 0, len(errs))
		for _, e := range errs {
			msgs = append(msgs, e.Error())
		}
		return nil, nil, nil, fmt.Errorf("%s: %s", path, strings.Join(msgs, "; "))
	}

	provider, err := Discover(ctx, DefaultHTTPClient, cfg.Issuer)
	if err != nil {
		// Wrapped with the path, because the error names a URL and the useful next question is which file put it there.
		return nil, nil, nil, fmt.Errorf("%s: %w", path, err)
	}

	if !provider.SupportsAnyOf(SupportedAlgorithms) {
		return nil, nil, nil, fmt.Errorf("%s: the identity provider signs tokens with %v, none of which this verifies",
			path, provider.AlgorithmsSupported)
	}

	return &cfg, provider, mapping, nil
}
