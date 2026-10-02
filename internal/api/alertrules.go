package api

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/biodream-llc/perfuse/internal/alerts"
	"github.com/biodream-llc/perfuse/internal/store"
)

// Alert rules as structured data, so they can be edited from a form instead of by hand in YAML.
//
// # Why this is not simply the settings file editor
//
// The rules file was already reachable: the settings screen offers it as a text box and validates a proposed file with the same loader
// the server uses. That satisfies the letter of being able to do everything from the interface and fails the point of it. To add a
// rule an operator had to know that the file has a rules key, that kind is spelled error-rate rather than error_rate, that for wants a
// Go duration string, and - worst - that threshold is a share between nought and one for two of the kinds and a plain count for the
// rest. Nothing on the screen said any of that.
//
// The threshold trap is the reason this is worth server-side work rather than a nicer text box. Somebody who means five percent types
// 5, the file loads, nothing complains, and the alert can never fire. The failure is silent, it is in the direction of not being told
// about problems, and the only defence is a form that knows which kinds are shares. That knowledge lives in the catalogue in the
// alerts package, where a test pairs it against the evaluators.
//
// # Why the server writes the YAML
//
// The browser sends rules as JSON and this writes the file, rather than the browser composing YAML and posting text. Two reasons. The
// file is the thing the engine loads, so the only safe place to decide what valid content looks like is next to the loader - and it is
// the same loader, called here before anything is written. And a browser that composes YAML has to know the field names, the omitempty
// rules and that a duration is a string, which is the knowledge this whole endpoint exists to stop it needing.

// alertRuleJSON is one rule as the browser sees it.
//
// Durations cross as a string because that is what an operator types and what the file holds. Sending nanoseconds - which is how Go
// encodes a duration into JSON by default - would mean the form had to convert, and a form doing arithmetic on a value it is about to
// display is how "5m" becomes 300000000000 in somebody's face.
type alertRuleJSON struct {
	Kind        string  `json:"kind"`
	Channel     string  `json:"channel,omitempty"`
	Destination string  `json:"destination,omitempty"`
	Threshold   float64 `json:"threshold"`
	For         string  `json:"for,omitempty"`
	Severity    string  `json:"severity,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
}

type alertRulesResponse struct {
	Rules []alertRuleJSON `json:"rules"`

	// Kinds is the catalogue, so the form is built from what the engine can actually evaluate rather than from a list in the
	// browser that would drift.
	Kinds []alerts.KindInfo `json:"kinds"`

	// Channels are the channel names that exist, so narrowing a rule is a choice rather than a typing exercise. A rule naming a
	// channel that does not exist is valid and silently matches nothing.
	Channels []string `json:"channels"`

	// Severities is the closed set, for the same reason as Kinds.
	Severities []string `json:"severities"`

	// Path is where the rules are stored, and Writable says whether this server can change them.
	Path     string `json:"path"`
	Writable bool   `json:"writable"`

	// Enabled reports whether alerting is running at all. Rules can be edited when it is off - which is the sensible order to do
	// things in - but the screen has to say so, or somebody will tune a rule and wait for an alert that was never going to come.
	Enabled bool `json:"enabled"`
}

func ruleToJSON(r alerts.Rule) alertRuleJSON {
	out := alertRuleJSON{
		Kind:        string(r.Kind),
		Channel:     r.Channel,
		Destination: r.Destination,
		Threshold:   r.Threshold,
		Severity:    string(r.Severity),
		Disabled:    r.Disabled,
	}
	if r.For > 0 {
		out.For = r.For.String()
	}

	return out
}

func ruleFromJSON(in alertRuleJSON) (alerts.Rule, error) {
	out := alerts.Rule{
		Kind:        alerts.Kind(in.Kind),
		Channel:     in.Channel,
		Destination: in.Destination,
		Threshold:   in.Threshold,
		Severity:    alerts.Severity(in.Severity),
		Disabled:    in.Disabled,
	}

	if in.For != "" {
		d, err := time.ParseDuration(in.For)
		if err != nil {
			return alerts.Rule{}, fmt.Errorf("%q is not a length of time; write it like 5m or 30s or 2h", in.For)
		}
		if d < 0 {
			return alerts.Rule{}, fmt.Errorf("%q is a negative length of time", in.For)
		}
		out.For = d
	}

	return out, nil
}

// handleAlertRules returns the current rules along with everything needed to edit them.
func (s *Server) handleAlertRules(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	path := s.SettingsPaths.Alerts

	rules, err := alerts.LoadFile(path)
	if err != nil {
		// A file that cannot be read is worth reporting rather than showing as an empty list, which would invite somebody to
		// "add" rules and overwrite a file that has content in it.
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("the alert rules could not be read: %v", err))

		return
	}

	out := alertRulesResponse{
		Rules:      make([]alertRuleJSON, 0, len(rules)),
		Kinds:      alerts.KindCatalogue(),
		Channels:   make([]string, 0),
		Severities: []string{string(alerts.Warning), string(alerts.Critical)},
		Path:       path,
		Writable:   path != "",
		Enabled:    s.Alerts != nil,
	}
	for _, rule := range rules {
		out.Rules = append(out.Rules, ruleToJSON(rule))
	}

	if s.Channels != nil {
		if list, _, err := s.Channels.List(); err == nil {
			for _, c := range list {
				out.Channels = append(out.Channels, c.Name)
			}
			sort.Strings(out.Channels)
		}
	}

	s.ok(w, out)
}

// handleSaveAlertRules replaces the rules file with the posted set.
//
// The whole set rather than one rule at a time, because the file is the unit the engine loads and a partial write would need this
// endpoint to merge - which means resolving what happens when two people edit at once, in a place where getting it wrong means an
// alert quietly disappears.
func (s *Server) handleSaveAlertRules(w http.ResponseWriter, r *http.Request, session *store.Session) {
	path := s.SettingsPaths.Alerts
	if path == "" {
		s.fail(w, r, http.StatusConflict,
			"this server was not started with an alert rules file, so there is nowhere to save them. "+
				"Start it with -alerts pointing at a file and the rules can be edited here.")

		return
	}

	var body struct {
		Rules []alertRuleJSON `json:"rules"`
	}
	if !s.decode(w, r, &body) {
		return
	}

	// An empty set is refused here rather than written and refused later.
	//
	// The loader treats a file with no rules as a mistake and says so, because the alternative reading - "watch nothing" - is almost
	// never what somebody meant, and the way to get the defaults back is to stop passing -alerts. That is a good rule and this is not
	// the place to argue with it. What matters is where the refusal lands: without this check, deleting the last rule writes a file
	// the engine will not load, and the operator finds out at the next restart with alerting off.
	if len(body.Rules) == 0 {
		s.fail(w, r, http.StatusBadRequest,
			"a rules file has to contain at least one rule. To go back to the built-in rules, stop the server with -alerts "+
				"and it will use its defaults; to watch nothing, disable the rules individually instead of deleting them, "+
				"which also keeps the thresholds somebody tuned.")

		return
	}

	parsed := make([]alerts.Rule, 0, len(body.Rules))
	for i, in := range body.Rules {
		rule, err := ruleFromJSON(in)
		if err != nil {
			s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("rule %d: %v", i+1, err))

			return
		}

		// The same validation the loader performs, reported against the rule that failed so the form can say which row is wrong.
		if err := rule.Validate(); err != nil {
			s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("rule %d: %v", i+1, err))

			return
		}
		parsed = append(parsed, rule)
	}

	data, err := marshalRules(parsed)
	if err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("the rules could not be written: %v", err))

		return
	}

	// Read back through the real loader before the file is replaced.
	//
	// Every rule above has already validated, so this looks redundant. It is not: it checks that what was serialised parses to what
	// was validated, which is the step that would catch a marshalling mistake in this file rather than in the operator's rules. The
	// cost is microseconds and the failure it prevents is a rules file the engine refuses at next start.
	if _, err := alerts.ParseRules("the rules being saved", data); err != nil {
		s.fail(w, r, http.StatusInternalServerError,
			fmt.Sprintf("the rules were accepted but the file produced from them does not load, which is a fault here rather than in the rules: %v", err))

		return
	}

	if err := writeFileAtomically(path, data); err != nil {
		s.fail(w, r, http.StatusInternalServerError, fmt.Sprintf("the rules could not be saved: %v", err))

		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: session.Username,
		Action:   "alert-rules.save",
		Target:   path,
		Detail:   fmt.Sprintf("%d rule(s)", len(parsed)),
		IP:       clientIP(r),
	})

	s.ok(w, map[string]any{
		"saved": len(parsed),
		"path":  path,
		// Said explicitly because the alternative is somebody restarting a production server to make an alert take effect.
		"note": "The rules are reloaded without a restart.",
	})
}

// marshalRules turns rules into the file the engine reads.
func marshalRules(rules []alerts.Rule) ([]byte, error) {
	var buf bytes.Buffer

	buf.WriteString("# Alert rules.\n")
	buf.WriteString("#\n")
	buf.WriteString("# Written by Perfuse from the alert rules editor. Editing this by hand is fine;\n")
	buf.WriteString("# the editor reads whatever is here, and comments outside this header are not preserved.\n\n")

	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)

	if err := enc.Encode(struct {
		Rules []alerts.Rule `yaml:"rules"`
	}{Rules: rules}); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}

	return buf.Bytes(), nil
}

// writeFileAtomically writes through a temporary file in the same directory and renames.
//
// The engine watches this file and reloads it. A plain write is visible to the watcher half-finished, so a truncated file can be read
// as the current rules - which is a moment with no alerting on a server that believes it has some.
func writeFileAtomically(path string, data []byte) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	name := tmp.Name()

	defer func() {
		// Only matters when something below failed; a successful rename has already moved it.
		if _, err := os.Stat(name); err == nil {
			_ = os.Remove(name)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()

		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// Keep whatever mode the file already had, so saving from the interface does not quietly widen its permissions.
	if info, err := os.Stat(path); err == nil {
		if err := os.Chmod(name, info.Mode().Perm()); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	return os.Rename(name, path)
}
