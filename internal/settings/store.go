package settings

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

// Store holds the current values and writes them to a file.
//
// Safe for concurrent use: settings are read on nearly every request and written from the interface, so a mutex is cheaper than
// reasoning about which reads happen to be safe.
type Store struct {
	registry *Registry
	path     string

	mu     sync.RWMutex
	values map[string]any

	// dirty is set when values were changed and not yet written.
	//
	// Only meaningful inside Set, which writes before returning. It exists so a failed write leaves the store reporting
	// the old value rather than a value that is only in memory - a settings page that shows a change the file does not
	// have is a settings page that lies after a restart.
	dirty bool
}

// NewStore builds a store over a file, creating nothing.
//
// The file is read if it exists. Every absent key falls back to its declared default, so a partial file is normal rather than
// an error - an operator who wants to set one thing should be able to write a file with one line in it.
func NewStore(registry *Registry, path string) (*Store, error) {
	if registry == nil {
		return nil, errors.New("settings: a store needs a registry")
	}

	s := &Store{
		registry: registry,
		path:     path,
		values:   make(map[string]any),
	}

	if path == "" {
		// No file means defaults only, and saving is refused rather than silently discarded. Used by tests and
		// by anybody running the engine without a settings file.
		return s, nil
	}

	data, err := os.ReadFile(path)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return s, nil
	case err != nil:
		return nil, fmt.Errorf("settings: reading %s: %w", path, err)
	}

	loaded, err := parse(registry, data)
	if err != nil {
		// Refused at load rather than partially applied. A settings file with one bad line would otherwise start a
		// server whose configuration is neither what the file says nor what the operator intended.
		return nil, fmt.Errorf("settings: %s: %w", path, err)
	}
	s.values = loaded

	return s, nil
}

// parse reads a settings document into values, refusing anything unrecognised.
//
// Nested YAML on disk - "data:\n  retentionDays: 30" - because a flat list of thirty dotted keys is not something a person can
// scan. Flattened to dotted keys in memory, since that is what a setting is identified by.
func parse(registry *Registry, data []byte) (map[string]any, error) {
	var doc map[string]any

	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	// Not KnownFields: that works on structs and this is a map. The check below is the equivalent, and it has to exist -
	// a mistyped key that is silently ignored is a setting somebody believes they changed.
	if err := dec.Decode(&doc); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("this is not valid YAML: %w", err)
	}

	flat := make(map[string]any)
	flatten("", doc, flat)

	out := make(map[string]any, len(flat))

	// Sorted so the error reported for a file with several bad keys is the same one every time. An error message that
	// changes between runs is one people stop trusting.
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		setting, ok := registry.Lookup(key)
		if !ok {
			return nil, fmt.Errorf("%q is not a setting this version knows about", key)
		}

		value, err := coerce(setting, flat[key])
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		if err := check(setting, value); err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
		out[key] = value
	}

	return out, nil
}

// flatten turns nested maps into dotted keys.
//
// Stops descending at anything that is not a map, so a list value arrives whole rather than as indexed keys.
func flatten(prefix string, in map[string]any, out map[string]any) {
	for k, v := range in {
		key := k
		if prefix != "" {
			key = prefix + "." + k
		}

		if nested, ok := v.(map[string]any); ok {
			flatten(key, nested, out)

			continue
		}
		out[key] = v
	}
}

// coerce converts a YAML value to the Go type the setting expects.
//
// YAML gives int for a whole number and string for a quoted one, and an operator writing a settings file by hand will quote
// things inconsistently. Converting here means the rest of the program sees one type per setting.
func coerce(setting Setting, raw any) (any, error) {
	switch setting.Kind {
	case KindBool:
		switch v := raw.(type) {
		case bool:
			return v, nil
		case string:
			// Accepted because an operator writing YAML by hand quotes things, and refusing "true" would
			// be pedantry over a value whose meaning is not in doubt.
			switch strings.ToLower(strings.TrimSpace(v)) {
			case "true", "yes", "on":
				return true, nil
			case "false", "no", "off":
				return false, nil
			}
		}

		return nil, fmt.Errorf("should be true or false, got %v", raw)

	case KindInt:
		switch v := raw.(type) {
		case int:
			return v, nil
		case float64:
			// YAML gives a float for 30.0. Refused rather than truncated: a setting written as 1.5 days
			// means somebody expected fractions, and rounding it to 1 silently would be worse than saying so.
			if v != float64(int(v)) {
				return nil, fmt.Errorf("should be a whole number, got %v", v)
			}

			return int(v), nil
		}

		return nil, fmt.Errorf("should be a whole number, got %v", raw)

	case KindString, KindSecret, KindDuration, KindCode, KindChoice:
		if v, ok := raw.(string); ok {
			return v, nil
		}
		// A bare yes in YAML parses as a boolean, so a domain of "yes.example.org" is fine but a value of
		// "yes" arrives as true. Reported rather than stringified, because stringifying gives "true".
		return nil, fmt.Errorf("should be text, got %v - quote it if it looks like a number or a yes/no", raw)

	case KindList:
		switch v := raw.(type) {
		case []any:
			out := make([]string, 0, len(v))
			for _, item := range v {
				s, ok := item.(string)
				if !ok {
					return nil, fmt.Errorf("every entry should be text, got %v", item)
				}
				out = append(out, s)
			}

			return out, nil
		case string:
			// A single value written without a dash. Common enough to accept.
			return []string{v}, nil
		}

		return nil, fmt.Errorf("should be a list, got %v", raw)
	}

	return nil, fmt.Errorf("unknown kind %q", setting.Kind)
}

// Check reports whether a value would be accepted for a key, without saving it.
//
// Exists so the interface can explain a refusal as somebody types rather than only when they submit.
// It runs the same coercion and the same validator that Set does, so a value this accepts is one Set
// will accept - a preview that disagreed with the save would be worse than no preview.
func (s *Store) Check(key string, value any) error {
	setting, ok := s.registry.Lookup(key)
	if !ok {
		return fmt.Errorf("settings: no setting named %q", key)
	}
	coerced, err := coerce(setting, value)
	if err != nil {
		return err
	}
	return check(setting, coerced)
}

// check applies bounds, choices and the setting's own validator.
func check(setting Setting, value any) error {
	switch setting.Kind {
	case KindInt:
		n, ok := value.(int)
		if !ok {
			return fmt.Errorf("should be a whole number")
		}
		if setting.Min != nil && n < *setting.Min {
			return fmt.Errorf("cannot be below %d", *setting.Min)
		}
		if setting.Max != nil && n > *setting.Max {
			return fmt.Errorf("cannot be above %d", *setting.Max)
		}

	case KindChoice:
		got, _ := value.(string)

		allowed := make([]string, 0, len(setting.Choices))
		for _, c := range setting.Choices {
			if c.Value == got {
				return nil
			}
			allowed = append(allowed, c.Value)
		}

		// The message lists the options. An error saying only that a value is wrong sends the reader to the
		// source to find out what would have been right.
		return fmt.Errorf("should be one of %s", strings.Join(allowed, ", "))
	}

	if setting.Validate != nil {
		if err := setting.Validate(value); err != nil {
			return err
		}
	}

	return nil
}

// Get returns a setting's value, or its default when unset.
//
// Panics on an unknown key, deliberately. Every caller passes a constant, so an unknown key is a programming error that should
// fail on the first run rather than return a zero value that behaves like a deliberate choice - a Get of a mistyped retention
// key returning 0 would mean "keep forever".
func (s *Store) Get(key string) any {
	setting, ok := s.registry.Lookup(key)
	if !ok {
		panic("settings: no such setting: " + key)
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	if v, ok := s.values[key]; ok {
		return v
	}

	return setting.Default
}

// Bool returns a boolean setting.
func (s *Store) Bool(key string) bool {
	v, _ := s.Get(key).(bool)

	return v
}

// Int returns a numeric setting.
func (s *Store) Int(key string) int {
	v, _ := s.Get(key).(int)

	return v
}

// String returns a text setting.
func (s *Store) String(key string) string {
	v, _ := s.Get(key).(string)

	return v
}

// List returns a list setting.
func (s *Store) List(key string) []string {
	v, _ := s.Get(key).([]string)
	out := make([]string, len(v))
	copy(out, v)

	return out
}

// IsSet reports whether a value was set rather than defaulted.
//
// What the interface needs to distinguish "nobody has chosen" from "somebody chose the default", which for a secret is the
// difference between blank and configured.
func (s *Store) IsSet(key string) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()

	_, ok := s.values[key]

	return ok
}

// Set validates and applies changes, then writes the file.
//
// All or nothing. A partial application would leave a server in a state neither the operator nor the file describes, and the
// interface sends a whole form - so one bad field must not save the others.
func (s *Store) Set(changes map[string]any) error {
	if s.path == "" {
		return errors.New("this server has no settings file, so settings cannot be changed here")
	}

	validated := make(map[string]any, len(changes))

	// Sorted so the first error reported for a form with several mistakes is stable between attempts.
	keys := make([]string, 0, len(changes))
	for k := range changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		setting, ok := s.registry.Lookup(key)
		if !ok {
			return fmt.Errorf("%q is not a setting", key)
		}

		value, err := coerce(setting, changes[key])
		if err != nil {
			return fmt.Errorf("%s: %w", setting.Label, err)
		}
		if err := check(setting, value); err != nil {
			return fmt.Errorf("%s: %w", setting.Label, err)
		}
		validated[key] = value
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// The previous values are kept so a failed write can be undone. Without this, a full disk would leave the running
	// server using settings that vanish on restart.
	previous := make(map[string]any, len(s.values))
	for k, v := range s.values {
		previous[k] = v
	}

	for k, v := range validated {
		s.values[k] = v
	}
	s.dirty = true

	if err := s.writeLocked(); err != nil {
		s.values = previous
		s.dirty = false

		return err
	}
	s.dirty = false

	return nil
}

// writeLocked writes the file. The caller holds the lock.
func (s *Store) writeLocked() error {
	nested := make(map[string]any)

	for key, value := range s.values {
		parts := strings.SplitN(key, ".", 2)
		if len(parts) != 2 {
			continue
		}
		section, ok := nested[parts[0]].(map[string]any)
		if !ok {
			section = make(map[string]any)
			nested[parts[0]] = section
		}
		section[parts[1]] = value
	}

	var buf strings.Builder
	buf.WriteString("# Perfuse settings.\n")
	buf.WriteString("#\n")
	buf.WriteString("# Edited by the web interface, and safe to edit by hand. Anything absent uses its default,\n")
	buf.WriteString("# so a file with one line in it is a perfectly good settings file.\n")
	buf.WriteString("#\n")
	buf.WriteString("# This file wins over the command line. A flag that disagrees with a value here is reported\n")
	buf.WriteString("# at startup and then ignored.\n\n")

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(nested); err != nil {
		return fmt.Errorf("settings: encoding: %w", err)
	}
	if err := enc.Close(); err != nil {
		return fmt.Errorf("settings: encoding: %w", err)
	}

	// Written to a temporary file and renamed, so a crash or a full disk leaves the old file intact rather than a
	// half-written one. A settings file truncated to nothing would start the next server on defaults, which for
	// retention means silently keeping everything.
	dir := filepath.Dir(s.path)
	tmp, err := os.CreateTemp(dir, ".perfuse-settings-*")
	if err != nil {
		return fmt.Errorf("settings: cannot write in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	// 0600 because a settings file may hold a webhook address or a token. Set before writing rather than after, so the
	// contents are never briefly readable.
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		os.Remove(tmpName)

		return fmt.Errorf("settings: %w", err)
	}
	if _, err := tmp.WriteString(buf.String()); err != nil {
		tmp.Close()
		os.Remove(tmpName)

		return fmt.Errorf("settings: writing: %w", err)
	}
	// Synced before the rename. Without it the rename can land while the contents have not, which after a power loss
	// gives a file that exists and is empty.
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(tmpName)

		return fmt.Errorf("settings: syncing: %w", err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)

		return fmt.Errorf("settings: closing: %w", err)
	}
	if err := os.Rename(tmpName, s.path); err != nil {
		os.Remove(tmpName)

		return fmt.Errorf("settings: replacing %s: %w", s.path, err)
	}

	return nil
}

// Path is where settings are written, empty when there is no file.
func (s *Store) Path() string { return s.path }

// Registry is the description of every setting.
func (s *Store) Registry() *Registry { return s.registry }

// SeedFromFlags writes an initial file from values the process was started with.
//
// Only when the file does not exist. Existing installations keep working with their flags, get a file on first start, and can
// then be managed from the interface - without anybody editing a service definition to migrate.
func (s *Store) SeedFromFlags(flagValues map[string]any) error {
	if s.path == "" {
		return nil
	}
	if _, err := os.Stat(s.path); err == nil {
		return nil
	}

	return s.Set(flagValues)
}

// Conflicts reports flags whose values disagree with the file.
//
// The file wins, so a disagreement means the flag does nothing. Reported at startup by name and with both values, because
// otherwise the flag stays in the service definition for years and everybody who reads it believes it.
func (s *Store) Conflicts(flagValues map[string]any) []string {
	var out []string

	keys := make([]string, 0, len(flagValues))
	for k := range flagValues {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	s.mu.RLock()
	defer s.mu.RUnlock()

	for _, key := range keys {
		setting, ok := s.registry.Lookup(key)
		if !ok {
			continue
		}
		fileValue, inFile := s.values[key]
		if !inFile {
			continue
		}
		if fmt.Sprint(fileValue) == fmt.Sprint(flagValues[key]) {
			continue
		}
		out = append(out, fmt.Sprintf("-%s was given as %v but the settings file says %v, which wins",
			setting.Flag, flagValues[key], fileValue))
	}

	return out
}
