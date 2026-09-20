package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// Writing messages to object storage.
//
// Mirth has no S3 connector at all, so this is not a gap being closed - it is somewhere Perfuse is simply
// ahead, and it matters because the archive requirement is now usually "put it in the bucket" rather than
// "put it on the file server".
//
// Implemented over signed HTTP rather than the AWS SDK, so the static binary and the one-screen bill of
// materials both survive. See internal/s3put for why that trade is worth making.

// S3Destination writes each message as an object.
type S3Destination struct {
	// Bucket is the bucket name.
	Bucket string `yaml:"bucket"`

	// Region is the region, for example eu-west-2. Required even for a non-AWS endpoint, because it forms
	// part of the request signature.
	Region string `yaml:"region"`

	// Key templates the object key, using the same ${date}, ${timestamp}, ${control_id},
	// ${message_type} and ${channel} placeholders as the file and SFTP destinations.
	//
	// Defaults to a date-partitioned path plus the control id.
	//
	// Date partitioning by default because the alternative - every object in one flat prefix - makes a
	// bucket that is slow to list and impossible to lifecycle by age, and nobody discovers that until
	// there are four million objects in it.
	Key string `yaml:"key,omitempty"`

	// AccessKeyID and SecretAccessKey authenticate. Either may name an environment variable instead of
	// holding the value, using the form ${NAME}.
	AccessKeyID     string `yaml:"access_key_id,omitempty"`
	SecretAccessKey string `yaml:"secret_access_key,omitempty"`

	// SessionToken is required with temporary credentials.
	SessionToken string `yaml:"session_token,omitempty"`

	// Endpoint overrides the AWS host, for MinIO, Ceph or another S3-compatible store.
	Endpoint string `yaml:"endpoint,omitempty"`

	// PathStyle addresses the bucket as a path rather than a subdomain. Most S3-compatible stores need
	// this.
	PathStyle bool `yaml:"path_style,omitempty"`

	// ServerSideEncryption sets the encryption header, for example AES256.
	ServerSideEncryption string `yaml:"server_side_encryption,omitempty"`

	// ContentType labels the object. Defaults to application/hl7-v2 for HL7 and text/plain otherwise.
	ContentType string `yaml:"content_type,omitempty"`

	// Framed wraps the message in MLLP framing, for consistency with the file and SFTP destinations.
	Framed bool `yaml:"framed,omitempty"`

	// Timeout bounds one upload. Defaults to 60s.
	Timeout time.Duration `yaml:"timeout,omitempty"`
}

// defaultS3Key is a date-partitioned path.
// The same ${...} vocabulary the file and SFTP destinations use. A second template syntax for the same
// concept would be a genuine usability failure - somebody would write one and get the other's literal
// text in their object keys.
const defaultS3Key = "${date}/${channel}/${control_id}.hl7"

// validateS3Dest checks an S3 destination.
func validateS3Dest(d *Destination) []error {
	var errs []error

	if d.S3 == nil {
		return []error{fmt.Errorf("destination %q is an s3 destination but has no s3 block", d.Name)}
	}
	s := d.S3

	if strings.TrimSpace(s.Bucket) == "" {
		errs = append(errs, fmt.Errorf("destination %q needs s3.bucket", d.Name))
	}
	if strings.TrimSpace(s.Region) == "" {
		// Not defaulted, because a wrong region fails as a signature mismatch, which reads as "your
		// credentials are wrong" and sends somebody to rotate keys that were never the problem.
		errs = append(errs, fmt.Errorf(
			"destination %q needs s3.region; it forms part of the request signature, so a wrong or "+
				"missing one looks like a credentials failure", d.Name))
	}

	// Credentials may be absent from the file and supplied by the environment, which is how they should
	// be supplied. What is refused is one without the other, because that combination is always a mistake
	// and it fails at three in the morning rather than at load.
	id := resolveSecret(s.AccessKeyID)
	secret := resolveSecret(s.SecretAccessKey)

	switch {
	case id == "" && secret == "":
		errs = append(errs, fmt.Errorf(
			"destination %q has no S3 credentials; set s3.access_key_id and s3.secret_access_key, or "+
				"reference the environment with ${AWS_ACCESS_KEY_ID} and ${AWS_SECRET_ACCESS_KEY}",
			d.Name))
	case id == "":
		errs = append(errs, fmt.Errorf("destination %q has an S3 secret but no access key id", d.Name))
	case secret == "":
		errs = append(errs, fmt.Errorf("destination %q has an S3 access key id but no secret", d.Name))
	}

	if s.Endpoint != "" && !s.PathStyle && strings.Contains(s.Endpoint, "://") {
		// A warning would be ignored, and this is a genuine footgun: nearly every S3-compatible store
		// requires path style, and virtual-host style against one fails with a DNS error naming a
		// hostname the operator never wrote down.
		if !strings.Contains(s.Endpoint, "amazonaws.com") {
			errs = append(errs, fmt.Errorf(
				"destination %q sets a custom s3.endpoint without s3.path_style; most S3-compatible "+
					"stores require path style, and without it the request goes to a bucket subdomain "+
					"that usually does not resolve", d.Name))
		}
	}

	if s.Key == "" {
		s.Key = defaultS3Key
	}
	if s.Timeout == 0 {
		s.Timeout = 60 * time.Second
	}

	return errs
}

// resolveSecret expands a ${NAME} reference from the environment.
//
// Kept deliberately narrow: exactly ${NAME} and nothing else, no partial interpolation. A general template
// would invite putting half a secret in the file, and a value that is partly literal is a value somebody
// will eventually commit.
func resolveSecret(value string) string {
	v := strings.TrimSpace(value)
	if strings.HasPrefix(v, "${") && strings.HasSuffix(v, "}") {
		return os.Getenv(v[2 : len(v)-1])
	}
	return v
}

// ResolvedAccessKeyID returns the access key, expanding an environment reference.
func (s *S3Destination) ResolvedAccessKeyID() string { return resolveSecret(s.AccessKeyID) }

// ResolvedSecretAccessKey returns the secret, expanding an environment reference.
func (s *S3Destination) ResolvedSecretAccessKey() string { return resolveSecret(s.SecretAccessKey) }

// ResolvedSessionToken returns the session token, expanding an environment reference.
func (s *S3Destination) ResolvedSessionToken() string { return resolveSecret(s.SessionToken) }
