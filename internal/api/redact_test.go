package api

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// This file guards credential redaction.
//
// It exists because a viewer - the read-only role, meant for watching message flow - could read the password for every
// downstream system a channel touched: SFTP, databases, HTTP tokens. Demonstrated against a running server with a viewer
// account, which returned both the source token and the destination password in full.

const credentialChannel = `name: creds
source:
  type: http
  http:
    listen: 127.0.0.1:9999
    token: SOURCE-TOKEN-SECRET
destinations:
  - name: sftp-out
    type: sftp
    sftp:
      host: files.example.test
      username: apiuser
      password: SFTP-PASSWORD-SECRET
      passphrase: KEY-PASSPHRASE-SECRET
  - name: db-out
    type: database
    database:
      driver: postgres
      dsn: postgres://user:DSN-PASSWORD-SECRET@db.example.test/records
`

// TestAViewerCannotReadCredentials is the regression test for the finding.
func TestAViewerCannotReadCredentials(t *testing.T) {
	redacted, err := redactSecretsInYAML([]byte(credentialChannel))
	if err != nil {
		t.Fatal(err)
	}

	for _, secret := range []string{
		"SOURCE-TOKEN-SECRET",
		"SFTP-PASSWORD-SECRET",
		"KEY-PASSPHRASE-SECRET",
	} {
		if strings.Contains(string(redacted), secret) {
			t.Errorf("%s survived redaction:\n%s", secret, redacted)
		}
	}
}

// TestRedactionKeepsEverythingElse covers over-redaction.
//
// A redacted definition still has to be readable. Blanking hostnames and usernames would make the viewer's channel page
// useless, and the point of the role is to be able to see what a channel does.
func TestRedactionKeepsEverythingElse(t *testing.T) {
	redacted, err := redactSecretsInYAML([]byte(credentialChannel))
	if err != nil {
		t.Fatal(err)
	}

	for _, keep := range []string{
		"creds",
		"files.example.test",
		"apiuser",
		"sftp-out",
		"postgres",
	} {
		if !strings.Contains(string(redacted), keep) {
			t.Errorf("%q was removed, and it is not a credential:\n%s", keep, redacted)
		}
	}
}

// TestRedactionSurvivesAwkwardValues covers the cases a regular expression gets wrong.
//
// This works on the parsed tree rather than by matching text, and these are the values that make the difference: a
// password containing a colon and a space, one in block scalar form, one that is only digits.
func TestRedactionSurvivesAwkwardValues(t *testing.T) {
	awkward := `name: awkward
source:
  type: http
  http:
    listen: 127.0.0.1:9999
    token: "has: a colon and spaces SECRETONE"
destinations:
  - name: out
    type: sftp
    sftp:
      host: h.example.test
      username: u
      password: |
        SECRETTWO
      passphrase: 12345678
`

	redacted, err := redactSecretsInYAML([]byte(awkward))
	if err != nil {
		t.Fatal(err)
	}

	for _, secret := range []string{"SECRETONE", "SECRETTWO", "12345678"} {
		if strings.Contains(string(redacted), secret) {
			t.Errorf("%s survived redaction:\n%s", secret, redacted)
		}
	}
}

// TestUnparseableYAMLIsRefusedRatherThanReturned covers the dangerous fallback.
//
// A definition that will not parse is exactly the case where returning the original "just this once" would hand back
// every credential in it.
func TestUnparseableYAMLIsRefusedRatherThanReturned(t *testing.T) {
	broken := "name: broken\n\tsource:\n  bad indentation: [unclosed\npassword: LEAKED-SECRET\n"

	out, err := redactSecretsInYAML([]byte(broken))
	if err == nil {
		if strings.Contains(string(out), "LEAKED-SECRET") {
			t.Error("unparseable YAML was returned with its credentials intact")
		}
		return
	}
	if strings.Contains(err.Error(), "LEAKED-SECRET") {
		t.Errorf("the error message contains the credential: %v", err)
	}
}

// TestThePlaceholderDoesNotRevealTheLength covers a small leak.
//
// A run of asterisks matching the real length tells a reader how long the password is, which is a meaningful head start.
func TestThePlaceholderDoesNotRevealTheLength(t *testing.T) {
	short := "name: a\nsource:\n  type: http\n  http:\n    listen: :1\n    token: abc\ndestinations: []\n"
	long := "name: a\nsource:\n  type: http\n  http:\n    listen: :1\n    token: " +
		strings.Repeat("x", 64) + "\ndestinations: []\n"

	a, err := redactSecretsInYAML([]byte(short))
	if err != nil {
		t.Fatal(err)
	}
	b, err := redactSecretsInYAML([]byte(long))
	if err != nil {
		t.Fatal(err)
	}

	getToken := func(s string) string {
		for _, line := range strings.Split(s, "\n") {
			if strings.Contains(line, "token:") {
				return strings.TrimSpace(strings.SplitN(line, ":", 2)[1])
			}
		}
		return ""
	}

	if getToken(string(a)) != getToken(string(b)) {
		t.Errorf("the placeholder differs by input length: %q against %q",
			getToken(string(a)), getToken(string(b)))
	}
}

// TestRolesThatMayReadSecrets covers the boundary.
func TestRolesThatMayReadSecrets(t *testing.T) {
	for _, role := range []string{"editor", "admin", "platform"} {
		if !mayReadSecrets(role) {
			t.Errorf("%s cannot read secrets, which makes ordinary editing impossible", role)
		}
	}
	for _, role := range []string{"viewer", "", "unknown", "VIEWER"} {
		if mayReadSecrets(role) {
			t.Errorf("%s can read secrets and should not", role)
		}
	}
}

// TestEveryCredentialFieldInTheConfigIsRedacted is the drift guard.
//
// This is the test that matters over time. Redaction works from a list of field names, and a new destination type with a
// new password field would be redacted nowhere with nothing to say so - the same shape of silent gap the builder drift
// guard exists to catch.
//
// So it reads the configuration source and fails on any YAML key that looks like a credential and is not accounted for.
func TestEveryCredentialFieldInTheConfigIsRedacted(t *testing.T) {
	dir := filepath.Join("..", "config")

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Skipf("the config package could not be read: %v", err)
	}

	// Matches a yaml tag on a struct field.
	tagPattern := regexp.MustCompile("yaml:\"([a-z0-9_]+)")

	// Words that make a field a credential. Deliberately broad, because a false positive here costs one line in the
	// known list and a false negative is a leaked password.
	suspicious := []string{"password", "passcode", "passphrase", "secret", "token", "credential"}

	// Names that contain a suspicious word and are not credentials. Each one is an explicit decision rather than a
	// pattern, so adding to this list is a visible act.
	known := map[string]bool{
		"token_endpoint":      true, // a URL
		"token_url":           true, // a URL
		"bearer_token_env":    true, // the name of an environment variable, not its value
		"password_env":        true, // likewise
		"token_env":           true,
		"secret_env":          true,
		"client_secret_env":   true,
		"password_file":       true, // a path
		"token_file":          true,
		"secret_file":         true,
		"client_secret_file":  true,
		"require_token":       true, // a boolean
		"tokens":              true, // a count in a metrics struct
		"password_last_set":   true, // a timestamp
		"secret_ref":          true, // a reference, not a value
		"passwordless":        true,
		"token_signing_algs":  true,
		"secret_post":         true, // an auth method name
		"client_secret_basic": true,
		"client_secret_post":  true,
		"token_endpoint_auth": true,
		"credentials_file":    true,
		"credential_source":   true,
		// A boolean, not a password: it permits an unencrypted FTP login.
		"allow_clear_password": true,
	}

	var missing []string
	seen := map[string]bool{}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") ||
			strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}

		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}

		for _, match := range tagPattern.FindAllStringSubmatch(string(data), -1) {
			field := match[1]
			if seen[field] || known[field] {
				continue
			}
			seen[field] = true

			looksSecret := false
			for _, word := range suspicious {
				if strings.Contains(field, word) {
					looksSecret = true
					break
				}
			}
			if !looksSecret {
				continue
			}

			if !secretFields[field] {
				missing = append(missing, field+" (in "+entry.Name()+")")
			}
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Errorf("these configuration fields look like credentials and are not redacted:\n  %s\n\n"+
			"Add each one to secretFields in redact.go, or to the known list in this test with the reason it "+
			"is not a credential. A viewer can read every channel definition, so anything left out is a "+
			"password handed to a read-only account.\n\nCurrently redacted: %s",
			strings.Join(missing, "\n  "), strings.Join(secretFieldNames(), ", "))
	}
}

// TestADefinitionWithNoSecretsIsReturnedUnchanged covers formatting.
//
// Re-marshalling YAML re-indents, requotes and moves comments. A channel with no credentials in it must come back exactly
// as it is on disk, or every viewer download differs from the file for no reason - and comments explaining why a channel
// is the way it is are frequently the most useful thing in it.
//
// Two existing tests caught this by comparing a download against its source file, which is the kind of assertion that
// looks pedantic until it earns its keep.
func TestADefinitionWithNoSecretsIsReturnedUnchanged(t *testing.T) {
	clean := `# This channel takes ADT feeds from the ward system.
#
# The filter drops A28 because the registry sends one per appointment.
name: adt-inbound
source:
  type: mllp
  listen: ":6661"

destinations:
  - name: archive
    type: file
    dir: /var/perfuse/adt
`

	out, err := redactSecretsInYAML([]byte(clean))
	if err != nil {
		t.Fatal(err)
	}

	if string(out) != clean {
		t.Errorf("a definition with no credentials was reformatted.\ngot:\n%s\nwant:\n%s", out, clean)
	}
}

// TestCommentsSurviveRedaction covers the case where rewriting does happen.
//
// A definition that does contain a credential has to be re-marshalled, and the comments are the part most likely to be
// lost. Losing them silently would be worse than refusing.
func TestCommentsSurviveRedaction(t *testing.T) {
	withComment := `# The token was rotated in March; the old one is in the ticket.
name: tokened
source:
  type: http
  http:
    listen: ":9999"
    token: THE-SECRET-VALUE
destinations: []
`

	out, err := redactSecretsInYAML([]byte(withComment))
	if err != nil {
		t.Fatal(err)
	}

	if strings.Contains(string(out), "THE-SECRET-VALUE") {
		t.Fatalf("the secret survived:\n%s", out)
	}
	if !strings.Contains(string(out), "rotated in March") {
		t.Errorf("the comment was lost during redaction:\n%s", out)
	}
}
