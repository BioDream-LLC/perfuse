package residency

import "testing"

func TestAreasAndRegions(t *testing.T) {
	p, err := Parse("eea, uk")
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []string{"eu-central-1", "eu-west-1", "eu-west-2", "westeurope", "uksouth", "europe-west2"} {
		if !p.Allows(r) {
			t.Errorf("%s should be allowed", r)
		}
	}
	for _, r := range []string{"us-east-1", "eu-central-2", "switzerlandnorth", "ap-southeast-2"} {
		if p.Allows(r) {
			t.Errorf("%s should not be allowed", r)
		}
	}
	if p, _ := Parse(""); p.Set() || !p.Allows("us-east-1") {
		t.Error("no residency allows everything")
	}
	if p, _ := Parse("eu-west-2"); !p.Allows("eu-west-2") || p.Allows("eu-west-1") {
		t.Error("a single region")
	}
	if _, err := Parse("europe"); err == nil {
		t.Error("an unknown word was accepted")
	}
}

func TestRegionOfHost(t *testing.T) {
	for host, want := range map[string]string{
		"sqs.eu-west-2.amazonaws.com":             "eu-west-2",
		"my-bucket.s3.eu-central-1.amazonaws.com": "eu-central-1",
		"s3-eu-west-1.amazonaws.com":              "eu-west-1",
		"sns.us-east-1.amazonaws.com:443":         "us-east-1",
		"abc.execute-api.eu-west-3.amazonaws.com": "eu-west-3",
	} {
		if _, got, ok := RegionOfHost(host); !ok || got != want {
			t.Errorf("%s: %q %v", host, got, ok)
		}
	}
	for _, host := range []string{"epr.hospital.nhs.uk", "s3.amazonaws.com", "10.0.0.5:2575", "minio.local:9000"} {
		if _, r, ok := RegionOfHost(host); ok {
			t.Errorf("%s gave %s", host, r)
		}
	}
}
