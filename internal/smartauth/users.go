package smartauth

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// User is a person who signs in to authorize an app: a member reading their own record, or a clinician.
type User struct {
	Username string `yaml:"username"`
	Name     string `yaml:"name"`
	// PasswordHash is perfuse smart hash's output; the password itself is never stored.
	PasswordHash string `yaml:"password_hash"`
	// FHIRUser is the FHIR resource the person is, Patient/123 or Practitioner/456. A Patient user only ever gets their own
	// record, whatever scopes an app asks for.
	FHIRUser string `yaml:"fhir_user"`
	// OIDCSubject links the person to their account at the upstream identity provider (-smart-oidc): the ID token's sub,
	// which is stable, never an email that can be reassigned. Either this or a password_hash is needed.
	OIDCSubject string `yaml:"oidc_subject,omitempty"`
}

// Users is keyed by username.
type Users map[string]*User

// LoadUsers reads the users file.
func LoadUsers(path string) (Users, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Users []*User `yaml:"users"`
	}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	out := Users{}
	for i, u := range doc.Users {
		where := fmt.Sprintf("%s: user %d (%s)", path, i+1, u.Username)
		switch {
		case u.Username == "":
			return nil, fmt.Errorf("%s has no username", where)
		case out[u.Username] != nil:
			return nil, fmt.Errorf("%s: the username is used twice", where)
		case u.PasswordHash == "" && u.OIDCSubject == "":
			return nil, fmt.Errorf("%s: needs a password_hash (from perfuse smart hash) or an oidc_subject", where)
		case u.PasswordHash != "" && !strings.HasPrefix(u.PasswordHash, "pbkdf2-sha256$"):
			return nil, fmt.Errorf("%s: password_hash must be the output of perfuse smart hash", where)
		}
		for _, o := range out {
			if u.OIDCSubject != "" && o.OIDCSubject == u.OIDCSubject {
				return nil, fmt.Errorf("%s: oidc_subject is also %s's", where, o.Username)
			}
		}
		typ, id, ok := strings.Cut(u.FHIRUser, "/")
		if !ok || id == "" || (typ != "Patient" && typ != "Practitioner" && typ != "PractitionerRole" && typ != "RelatedPerson") {
			return nil, fmt.Errorf("%s: fhir_user must be Patient/<id>, Practitioner/<id>, PractitionerRole/<id> or RelatedPerson/<id>", where)
		}
		out[u.Username] = u
	}
	return out, nil
}

// patientID is the user's own Patient id, empty when the user is not a patient.
func (u *User) patientID() string {
	if id, ok := strings.CutPrefix(u.FHIRUser, "Patient/"); ok {
		return id
	}
	return ""
}
