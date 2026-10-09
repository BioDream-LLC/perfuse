package smartauth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// The console edits the clients and users files through these: the new list is written beside the file, loaded with the
// same checks as at start-up, and only then moved over the old one, so a mistake is refused and never half-written.
// Comments in the file are not kept.

type savedClient struct {
	ID           string         `yaml:"id"`
	Name         string         `yaml:"name,omitempty"`
	Kind         string         `yaml:"kind"`
	Scopes       []string       `yaml:"scopes"`
	JWKSURI      string         `yaml:"jwks_uri,omitempty"`
	JWKS         map[string]any `yaml:"jwks,omitempty"`
	SecretHash   string         `yaml:"secret_hash,omitempty"`
	RedirectURIs []string       `yaml:"redirect_uris,omitempty"`
	LaunchURL    string         `yaml:"launch_url,omitempty"`
}

type savedUser struct {
	Username     string `yaml:"username"`
	Name         string `yaml:"name,omitempty"`
	PasswordHash string `yaml:"password_hash,omitempty"`
	FHIRUser     string `yaml:"fhir_user"`
	OIDCSubject  string `yaml:"oidc_subject,omitempty"`
}

// SaveClients writes the clients file and returns the registry as loaded from it.
func SaveClients(path string, list []*Client) (Clients, error) {
	doc := struct {
		Clients []savedClient `yaml:"clients"`
	}{Clients: []savedClient{}}
	for _, c := range list {
		doc.Clients = append(doc.Clients, savedClient{ID: c.ID, Name: c.Name, Kind: c.Kind, Scopes: c.Scopes, JWKSURI: c.JWKSURI,
			JWKS: c.JWKS, SecretHash: c.SecretHash, RedirectURIs: c.RedirectURIs, LaunchURL: c.LaunchURL})
	}
	var out Clients
	err := saveYAML(path, doc, func(tmp string) (err error) { out, err = LoadClients(tmp); return })
	return out, err
}

// SaveUsers writes the users file and returns the people as loaded from it.
func SaveUsers(path string, list []*User) (Users, error) {
	doc := struct {
		Users []savedUser `yaml:"users"`
	}{Users: []savedUser{}}
	for _, u := range list {
		doc.Users = append(doc.Users, savedUser{Username: u.Username, Name: u.Name, PasswordHash: u.PasswordHash, FHIRUser: u.FHIRUser,
			OIDCSubject: u.OIDCSubject})
	}
	var out Users
	err := saveYAML(path, doc, func(tmp string) (err error) { out, err = LoadUsers(tmp); return })
	return out, err
}

func saveYAML(path string, doc any, check func(tmp string) error) error {
	if path == "" {
		return errors.New("this server was started without the file to save to")
	}
	raw, err := yaml.Marshal(doc)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer func() { _ = os.Remove(tmp) }()
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := check(tmp); err != nil {
		// The checks name the file; the person editing never saw the temporary one.
		return errors.New(strings.TrimPrefix(strings.ReplaceAll(err.Error(), tmp+": ", ""), tmp))
	}
	return os.Rename(tmp, path)
}
