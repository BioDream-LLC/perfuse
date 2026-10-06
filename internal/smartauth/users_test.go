package smartauth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestTheUsersFileIsChecked(t *testing.T) {
	if u, err := LoadUsers("../../examples/smart/users.yaml"); err != nil || u["amy"].patientID() != "example-member" {
		t.Fatalf("example: %v", err)
	}
	for want, text := range map[string]string{
		"fhir_user must be":  `users: [{username: a, password_hash: "pbkdf2-sha256$1$x$y", fhir_user: Organization/1}]`,
		"perfuse smart hash": `users: [{username: a, password_hash: plaintext, fhir_user: Patient/1}]`,
		"used twice":         `users: [{username: a, password_hash: "pbkdf2-sha256$1$x$y", fhir_user: Patient/1}, {username: a, password_hash: "pbkdf2-sha256$1$x$y", fhir_user: Patient/2}]`,
	} {
		p := filepath.Join(t.TempDir(), "u.yaml")
		os.WriteFile(p, []byte(text), 0o600)
		if _, err := LoadUsers(p); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("want %q, got %v", want, err)
		}
	}
}
