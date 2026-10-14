package config

import "testing"

// The examples in examples/iis are what the manual tells a site to copy, so they must load.
func TestTheImmunizationRegistryExamplesLoad(t *testing.T) {
	cs, err := LoadDir("../../examples/iis")
	if err != nil {
		t.Fatalf("examples/iis: %v", err)
	}
	if len(cs) != 3 {
		t.Fatalf("got %d channels, want 3", len(cs))
	}
	for _, c := range cs {
		for _, d := range c.Destinations {
			if d.SOAP != nil && d.SOAP.Profile == "cdc-iis" && d.SOAP.SOAPVersion() != "1.2" {
				t.Errorf("%s: the registry profile did not default to SOAP 1.2", c.Name)
			}
		}
	}
}
