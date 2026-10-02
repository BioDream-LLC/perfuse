package peers

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

// Load reads a fleet configuration file.
//
// Unknown keys are refused rather than ignored, the same rule applied to channel files: a mistyped setting that is
// silently dropped looks configured and is not, which is the failure mode that wastes the most time.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path) // #nosec G304 - an operator-supplied path, like every other configuration file
	if err != nil {
		return nil, err
	}

	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("this does not look like a peers file: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, err
	}

	if len(cfg.Peers) == 0 {
		// Refused rather than accepted as an empty fleet. Somebody who points at a peers file and gets no fleet
		// view would reasonably conclude the feature is broken, when in fact the file says nothing.
		return nil, fmt.Errorf("%s lists no peers, so no fleet view would appear; remove the -peers flag or add "+
			"the other instances under a peers: key", path)
	}

	return &cfg, nil
}
