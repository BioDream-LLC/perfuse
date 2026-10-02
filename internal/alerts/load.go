package alerts

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// LoadFile reads alert rules from a YAML file.
//
// Moved here from cmd/perfuse so that the settings API can validate a proposed file against the same loader the server
// uses. A second implementation for validation would accept things the real one rejects, which is precisely the failure
// that validation exists to prevent.
func LoadFile(path string) ([]Rule, error) {
	if path == "" {
		return DefaultRules(), nil
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading alert rules: %w", err)
	}

	return ParseRules(path, data)
}

// ParseRules reads alert rules from bytes.
//
// Separate from LoadFile so a caller holding a proposed file - the settings editor - can check it without writing it
// anywhere first.
func ParseRules(name string, data []byte) ([]Rule, error) {
	var file struct {
		Rules []Rule `yaml:"rules"`
	}

	dec := yaml.NewDecoder(bytes.NewReader(data))
	// An unknown key is an error, as everywhere else here: a misspelled threshold that is silently ignored means an
	// alert nobody is watching.
	dec.KnownFields(true)
	if err := dec.Decode(&file); err != nil {
		return nil, fmt.Errorf("%s: %w", name, err)
	}

	if len(file.Rules) == 0 {
		return nil, fmt.Errorf("%s defines no rules; remove -alerts to use the defaults", name)
	}

	// The kind is checked here too, not only the keys. A misspelled kind decodes into a rule that looks entirely valid
	// and never fires, which is the worst outcome an alert can have: somebody wrote down what they wanted to be told
	// about, nothing complained, and the condition goes unreported.
	for i, rule := range file.Rules {
		if err := rule.Validate(); err != nil {
			return nil, fmt.Errorf("%s: rules[%d]: %w", name, i, err)
		}
	}

	return file.Rules, nil
}
