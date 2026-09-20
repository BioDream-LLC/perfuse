package config

import (
	"os"
	"regexp"
	"sort"
	"testing"
)

func TestEveryDestinationTypeConstantIsInTheList(t *testing.T) {
	// allDestinationTypes exists so a message naming the supported transports cannot fall behind them. That only holds while the list
	// holds every constant, and a list beside the thing it describes is exactly what drifted last time: the sentence it replaced named
	// nine transports while seventeen worked, so anybody who mistyped s3 or soap was told their transport did not exist.
	//
	// Read from the source rather than reflected over, because there is nothing to reflect: a constant absent from the list is absent
	// from anything the list could be compared against at runtime.
	data, err := os.ReadFile("config.go")
	if err != nil {
		t.Fatal(err)
	}

	declared := regexp.MustCompile(`\bDestination\w+\s+DestinationType\s*=\s*"([^"]+)"`)

	var fromSource []string
	for _, m := range declared.FindAllStringSubmatch(string(data), -1) {
		fromSource = append(fromSource, m[1])
	}

	// A positive control: if the pattern stops matching, the comparison below passes while reading nothing.
	if len(fromSource) < 10 {
		t.Fatalf("found only %d destination constants, so this test is not reading the real declarations", len(fromSource))
	}

	inList := map[string]bool{}
	for _, dt := range allDestinationTypes {
		inList[string(dt)] = true
	}

	sort.Strings(fromSource)

	for _, name := range fromSource {
		if !inList[name] {
			t.Errorf("DestinationType %q is declared and missing from allDestinationTypes, so the message telling somebody which "+
				"transports exist will not mention it", name)
		}
	}

	// And the other way, because a name in the list that is not a constant would advertise a transport that cannot be used.
	constants := map[string]bool{}
	for _, name := range fromSource {
		constants[name] = true
	}

	for _, dt := range allDestinationTypes {
		if !constants[string(dt)] {
			t.Errorf("allDestinationTypes offers %q and no constant declares it", string(dt))
		}
	}
}
