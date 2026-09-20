package config

import (
	"strings"
	"testing"
)

// The S3 destination's failures are all configuration mistakes that fail late and read as something else,
// so the tests are mostly about what is refused at load and why the message says what it says.

func s3Channel(t *testing.T, s3 *S3Destination) *Channel {
	t.Helper()
	return &Channel{
		Name:   "archive",
		Source: Source{Type: SourceMLLP, Listen: "127.0.0.1:2575"},
		Destinations: []Destination{{
			Name: "bucket", Type: DestinationS3, S3: s3,
		}},
	}
}

func goodS3() *S3Destination {
	return &S3Destination{
		Bucket:          "traffic",
		Region:          "eu-west-2",
		AccessKeyID:     "AKIAEXAMPLE",
		SecretAccessKey: "secret",
	}
}

func TestAValidS3DestinationLoads(t *testing.T) {
	c := s3Channel(t, goodS3())
	if err := c.Validate(); err != nil {
		t.Fatalf("a valid S3 destination was refused: %v", err)
	}
}

func TestTheDefaultKeyPartitionsByDate(t *testing.T) {
	// A flat prefix makes a bucket slow to list and impossible to lifecycle by age, and nobody discovers
	// that until there are four million objects in it.
	c := s3Channel(t, goodS3())
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}

	key := c.Destinations[0].S3.Key
	if !strings.Contains(key, "${date}") {
		t.Errorf("default key = %q, want it partitioned by date", key)
	}
	if !strings.Contains(key, "${control_id}") {
		t.Errorf("default key = %q, want the control id so two messages cannot collide", key)
	}
}

func TestTheKeyTemplateUsesTheSameSyntaxAsTheOtherFileDestinations(t *testing.T) {
	// A second template syntax for the same concept would be a genuine usability failure: somebody writes
	// one and gets the other's literal text in their object keys.
	c := s3Channel(t, goodS3())
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}

	key := c.Destinations[0].S3.Key
	if strings.Contains(key, "{{") {
		t.Errorf("the default key uses a different placeholder syntax from the file and SFTP "+
			"destinations: %q", key)
	}
}

func TestAMissingRegionExplainsWhyItMatters(t *testing.T) {
	// A wrong region fails as a signature mismatch, which reads as "your credentials are wrong" and sends
	// somebody to rotate keys that were never the problem.
	s3 := goodS3()
	s3.Region = ""

	err := s3Channel(t, s3).Validate()
	if err == nil {
		t.Fatal("a destination with no region was accepted")
	}
	if !strings.Contains(err.Error(), "signature") {
		t.Errorf("the error does not explain why a region is needed: %v", err)
	}
}

func TestAMissingBucketIsRefused(t *testing.T) {
	s3 := goodS3()
	s3.Bucket = ""

	if err := s3Channel(t, s3).Validate(); err == nil {
		t.Fatal("a destination with no bucket was accepted")
	}
}

func TestHalfACredentialIsRefused(t *testing.T) {
	// Absent credentials are legitimate - they can come from the environment. One without the other is
	// always a mistake, and it fails at three in the morning rather than at load.
	withoutSecret := goodS3()
	withoutSecret.SecretAccessKey = ""

	err := s3Channel(t, withoutSecret).Validate()
	if err == nil {
		t.Fatal("an access key with no secret was accepted")
	}
	if !strings.Contains(err.Error(), "secret") {
		t.Errorf("the error does not say which half is missing: %v", err)
	}

	withoutID := goodS3()
	withoutID.AccessKeyID = ""

	err = s3Channel(t, withoutID).Validate()
	if err == nil {
		t.Fatal("a secret with no access key id was accepted")
	}
	if !strings.Contains(err.Error(), "access key id") {
		t.Errorf("the error does not say which half is missing: %v", err)
	}
}

func TestNoCredentialsAtAllSuggestsTheEnvironment(t *testing.T) {
	// Referencing the environment is how credentials should be supplied, so the error should say how
	// rather than only that something is missing.
	s3 := goodS3()
	s3.AccessKeyID = ""
	s3.SecretAccessKey = ""

	err := s3Channel(t, s3).Validate()
	if err == nil {
		t.Fatal("a destination with no credentials at all was accepted")
	}
	if !strings.Contains(err.Error(), "${") {
		t.Errorf("the error does not show how to reference the environment: %v", err)
	}
}

func TestCredentialsCanComeFromTheEnvironment(t *testing.T) {
	t.Setenv("PERFUSE_TEST_KEY", "AKIAFROMENV")
	t.Setenv("PERFUSE_TEST_SECRET", "secretfromenv")

	s3 := goodS3()
	s3.AccessKeyID = "${PERFUSE_TEST_KEY}"
	s3.SecretAccessKey = "${PERFUSE_TEST_SECRET}"

	c := s3Channel(t, s3)
	if err := c.Validate(); err != nil {
		t.Fatalf("environment-referenced credentials were refused: %v", err)
	}
	if got := c.Destinations[0].S3.ResolvedAccessKeyID(); got != "AKIAFROMENV" {
		t.Errorf("resolved key = %q, want the environment value", got)
	}
	if got := c.Destinations[0].S3.ResolvedSecretAccessKey(); got != "secretfromenv" {
		t.Errorf("resolved secret = %q, want the environment value", got)
	}
}

func TestAnUnsetEnvironmentReferenceIsRefusedAtLoad(t *testing.T) {
	// Otherwise the channel starts and every delivery fails with a 403, which looks like a permissions
	// problem at the bucket rather than a variable nobody exported.
	s3 := goodS3()
	s3.AccessKeyID = "${PERFUSE_TEST_DEFINITELY_UNSET}"
	s3.SecretAccessKey = "${PERFUSE_TEST_ALSO_UNSET}"

	if err := s3Channel(t, s3).Validate(); err == nil {
		t.Fatal("credentials referencing unset variables were accepted")
	}
}

func TestACustomEndpointWithoutPathStyleIsRefused(t *testing.T) {
	// Nearly every S3-compatible store requires path style, and without it the request goes to a bucket
	// subdomain that usually does not resolve - failing with a DNS error naming a hostname the operator
	// never wrote down.
	s3 := goodS3()
	s3.Endpoint = "https://minio.hospital.local:9000"

	err := s3Channel(t, s3).Validate()
	if err == nil {
		t.Fatal("a custom endpoint without path style was accepted")
	}
	if !strings.Contains(err.Error(), "path_style") {
		t.Errorf("the error does not name the setting to add: %v", err)
	}
}

func TestACustomEndpointWithPathStyleIsFine(t *testing.T) {
	s3 := goodS3()
	s3.Endpoint = "https://minio.hospital.local:9000"
	s3.PathStyle = true

	if err := s3Channel(t, s3).Validate(); err != nil {
		t.Fatalf("a correctly configured MinIO destination was refused: %v", err)
	}
}

func TestAnS3DestinationWithNoBlockAtAllIsRefused(t *testing.T) {
	c := &Channel{
		Name:         "archive",
		Source:       Source{Type: SourceMLLP, Listen: "127.0.0.1:2575"},
		Destinations: []Destination{{Name: "bucket", Type: DestinationS3}},
	}

	err := c.Validate()
	if err == nil {
		t.Fatal("an s3 destination with no s3 block was accepted")
	}
	if !strings.Contains(err.Error(), "s3 block") {
		t.Errorf("the error does not say what is missing: %v", err)
	}
}

func TestASecretIsNotPartiallyInterpolated(t *testing.T) {
	// Deliberately exactly ${NAME} and nothing else. A general template would invite putting half a secret
	// in the file, and a value that is partly literal is a value somebody will eventually commit.
	t.Setenv("PERFUSE_TEST_HALF", "world")

	if got := resolveSecret("hello-${PERFUSE_TEST_HALF}"); got != "hello-${PERFUSE_TEST_HALF}" {
		t.Errorf("resolved = %q, want the literal left alone", got)
	}
}
