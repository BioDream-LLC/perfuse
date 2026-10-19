package engine

import (
	"net/http/httptest"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
)

// A DICOMweb source's credentials may be ${ENV} references, as the manual writes them. The variable's value is the token;
// the reference's own text is not; and a reference to an unset variable refuses everything rather than leaving it open.
func TestDICOMwebSourceCredentialsReadTheEnvironment(t *testing.T) {
	t.Setenv("PERFUSE_TEST_STOW_TOKEN", "s3cret-value")
	c := &Channel{}
	ask := func(src *config.DICOMwebSource, header string) bool {
		r := httptest.NewRequest("POST", "/dicom-web/studies", nil)
		if header != "" {
			r.Header.Set("Authorization", header)
		}
		return c.dicomwebAuthorised(r, src)
	}
	src := &config.DICOMwebSource{Token: "${PERFUSE_TEST_STOW_TOKEN}"}
	if !ask(src, "Bearer s3cret-value") {
		t.Error("the variable's value was refused")
	}
	if ask(src, "Bearer ${PERFUSE_TEST_STOW_TOKEN}") {
		t.Error("the reference's own text was accepted as the token")
	}
	if ask(&config.DICOMwebSource{Token: "${PERFUSE_TEST_UNSET_VARIABLE}"}, "Bearer ") || ask(&config.DICOMwebSource{Token: "${PERFUSE_TEST_UNSET_VARIABLE}"}, "") {
		t.Error("a reference to an unset variable let a request in")
	}
	if !ask(&config.DICOMwebSource{Token: "literal"}, "Bearer literal") {
		t.Error("a literal token stopped working")
	}
}

// The XDS destination sends the variable's value, not the reference.
func TestXDSDestinationTokenReadsTheEnvironment(t *testing.T) {
	t.Setenv("PERFUSE_TEST_XDS_TOKEN", "xds-value")
	s, err := NewXDSSender(config.Destination{Name: "x", Type: config.DestinationXDS, XDS: &config.XDSDestination{
		URL: "https://repo.example/xds", SourceID: "1.2.3", Token: "${PERFUSE_TEST_XDS_TOKEN}"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.client.Headers["Authorization"]; got != "Bearer xds-value" {
		t.Errorf("Authorization = %q, want the variable's value", got)
	}
}
