package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/biodream-llc/perfuse/internal/settings"
	"github.com/biodream-llc/perfuse/internal/store"
)

// settingsSchemaResponse is everything the interface needs to draw the settings area.
//
// Schema and values together in one response rather than two endpoints. They have to agree - a control drawn from one version's
// schema and filled from another's values would render a stale field - and one request cannot disagree with itself.
type settingsSchemaResponse struct {
	// Groups are the sections, in display order.
	Groups []settingsGroup `json:"groups"`

	// Values are the current values, keyed by setting.
	//
	// A secret never appears here. See settingsSecretState.
	Values map[string]any `json:"values"`

	// Secrets says which secret settings have a value, without saying what it is.
	Secrets map[string]bool `json:"secrets"`

	// Explicit lists the settings somebody has actually chosen, as against those sitting at their default.
	//
	// The interface shows a default differently from a choice that happens to match it, because "nobody has decided this"
	// and "somebody decided this" call for different confidence when you are the next person to look.
	Explicit []string `json:"explicit"`

	// Path is the settings file, empty when the server has none.
	Path string `json:"path"`

	// Writable says whether saving will work.
	//
	// Sent so the interface can explain rather than present a form whose save button fails. A server started without a
	// settings file can still show every value; it just cannot change one.
	Writable bool `json:"writable"`

	// RestartRequired lists settings changed since startup that need a restart to take effect.
	//
	// The single most important field here. Without it somebody changes a passkey domain, sees it saved, and believes it -
	// and finds out at the next sign-in.
	RestartRequired []string `json:"restartRequired"`
}

// settingsGroup is one section of the settings area.
type settingsGroup struct {
	Name      string             `json:"name"`
	Subgroups []settingsSubgroup `json:"subgroups"`
}

// settingsSubgroup is one heading within a section.
type settingsSubgroup struct {
	Name     string             `json:"name"`
	Settings []settings.Setting `json:"settings"`
}

// handleSettingsSchema describes and reports every setting.
//
// Admin, not viewer. A settings value can name an internal host, a webhook address or a directory server, which taken together
// describe the hospital's network to somebody who only needed to read a message.
func (s *Server) handleSettingsSchema(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.requireSettingsAccess(w, r, sess) {
		return
	}

	reg := s.Settings.Registry()

	// Built by walking the registry in its display order, so the interface never sorts and the two can never disagree
	// about what comes first.
	var groups []settingsGroup
	var currentGroup *settingsGroup
	var currentSub *settingsSubgroup

	for _, setting := range reg.All() {
		if currentGroup == nil || currentGroup.Name != setting.Group {
			groups = append(groups, settingsGroup{Name: setting.Group})
			currentGroup = &groups[len(groups)-1]
			currentSub = nil
		}
		if currentSub == nil || currentSub.Name != setting.Subgroup {
			currentGroup.Subgroups = append(currentGroup.Subgroups,
				settingsSubgroup{Name: setting.Subgroup})
			currentSub = &currentGroup.Subgroups[len(currentGroup.Subgroups)-1]
		}
		currentSub.Settings = append(currentSub.Settings, setting)
	}

	values := make(map[string]any)
	secrets := make(map[string]bool)
	// Empty rather than nil, so this marshals as [] and not null.
	//
	// It reaches the browser as schema.explicit.includes(key), so a null crashed the settings screen outright - a white page, not a
	// degraded one. And it was nil in precisely one state: a server where nothing has been set explicitly, which is every fresh
	// installation. The first thing a new operator did after starting Perfuse and opening Settings was crash it.
	explicit := []string{}

	for _, setting := range reg.All() {
		if s.Settings.IsSet(setting.Key) {
			explicit = append(explicit, setting.Key)
		}

		// A secret is reported as set or not set, never as a value. This is the one line in this file that would
		// matter if it were wrong, which is why the registry refuses a secret paired with a visible control and
		// there is a test that no secret key appears in this response at all.
		if setting.Kind == settings.KindSecret {
			secrets[setting.Key] = s.Settings.String(setting.Key) != ""

			continue
		}
		values[setting.Key] = s.Settings.Get(setting.Key)
	}

	sort.Strings(explicit)

	s.ok(w, settingsSchemaResponse{
		Groups:          groups,
		Values:          values,
		Secrets:         secrets,
		Explicit:        explicit,
		Path:            s.Settings.Path(),
		Writable:        s.Settings.Path() != "",
		RestartRequired: s.pendingRestartSettings(),
	})
}

// settingsValuesRequest is a set of changes.
type settingsValuesRequest struct {
	// Changes are the settings to apply, keyed as the schema names them.
	//
	// A subset, not the whole set. Sending everything would mean two people editing different settings at the same time
	// each overwriting the other's work with values they never looked at.
	Changes map[string]any `json:"changes"`
}

// settingsValuesResponse says what happened.
//
// All three lists are always sent, empty rather than nil. A nil slice marshals to null, and the
// interface declares these as arrays and reads their length to decide what to tell the operator - so a
// save that needed no restart would have crashed the screen that reports the save succeeded.
type settingsValuesResponse struct {
	// Applied lists what changed.
	Applied []string `json:"applied"`

	// RestartRequired lists which of those need a restart.
	//
	// Returned as well as being in the schema, so the interface can say so at the moment of saving rather than only on the
	// next load.
	RestartRequired []string `json:"restartRequired"`

	// Unchanged lists submitted settings that already had that value.
	//
	// Reported rather than counted as applied, because an audit entry claiming somebody changed a setting they did not
	// is worse than no entry.
	Unchanged []string `json:"unchanged"`
}

// newSettingsValuesResponse builds the response with every list non-nil.
//
// A constructor rather than three assignments at each call site, so a caller added later cannot
// reintroduce the nulls by forgetting one.
func newSettingsValuesResponse(applied, needRestart, unchanged []string) settingsValuesResponse {
	nonNil := func(in []string) []string {
		if in == nil {
			return []string{}
		}
		return in
	}
	return settingsValuesResponse{
		Applied:         nonNil(applied),
		RestartRequired: nonNil(needRestart),
		Unchanged:       nonNil(unchanged),
	}
}

// handleSettingsUpdate applies changes.
func (s *Server) handleSettingsUpdate(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if !s.requireSettingsAccess(w, r, sess) {
		return
	}
	if s.Settings.Path() == "" {
		s.fail(w, r, http.StatusConflict,
			"this server was started without a settings file, so settings cannot be changed here")

		return
	}

	var req settingsValuesRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
		s.fail(w, r, http.StatusBadRequest, "that request was not readable")

		return
	}
	if len(req.Changes) == 0 {
		s.fail(w, r, http.StatusBadRequest, "no changes were sent")

		return
	}

	reg := s.Settings.Registry()

	// Separated before anything is applied, so the audit entry and the response describe what actually changed. Sorted
	// because both end up in front of a person.
	var applied, unchanged, needRestart []string
	keys := make([]string, 0, len(req.Changes))
	for k := range req.Changes {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	for _, key := range keys {
		setting, ok := reg.Lookup(key)
		if !ok {
			s.fail(w, r, http.StatusBadRequest, fmt.Sprintf("%q is not a setting", key))

			return
		}
		if sameSettingValue(s.Settings.Get(key), req.Changes[key]) && s.Settings.IsSet(key) {
			unchanged = append(unchanged, key)

			continue
		}
		applied = append(applied, key)
		if setting.Effect == settings.EffectRestart {
			needRestart = append(needRestart, key)
		}
	}

	if len(applied) == 0 {
		s.ok(w, newSettingsValuesResponse(nil, nil, unchanged))

		return
	}

	// Only the changes are passed on. Passing unchanged values too would rewrite the file with values nobody touched,
	// which turns every save into a diff nobody can read.
	only := make(map[string]any, len(applied))
	for _, key := range applied {
		only[key] = req.Changes[key]
	}

	if err := s.Settings.Set(only); err != nil {
		// The message comes from the setting's own validation and names the label rather than the key, so it reads
		// as a sentence about the control the person was just looking at.
		s.fail(w, r, http.StatusBadRequest, err.Error())

		return
	}

	// Applied to the running server where the setting allows it. Without this, every live setting would need a restart and
	// the distinction the registry draws would be decorative.
	s.applyLiveSettings()

	for _, key := range applied {
		setting, _ := reg.Lookup(key)
		// The value is deliberately not recorded for a secret or for anything marked sensitive. What changed, by
		// whom, and when is the useful record; the value would put a credential in a table that is read more
		// widely than the settings page.
		detail := key
		if setting.Kind == settings.KindSecret || setting.Sensitive {
			detail = key + " (value not recorded)"
		} else {
			detail = fmt.Sprintf("%s = %v", key, req.Changes[key])
		}
		s.auditSettings(r, sess, detail)
	}

	s.recordRestartPending(needRestart)

	s.ok(w, newSettingsValuesResponse(applied, needRestart, unchanged))
}

// sameSettingValue compares a stored value with one that came through JSON.
//
// JSON gives float64 for every number, so a stored int of 30 and a submitted 30 are different Go values holding the same number.
// Comparing them as text is the shortest correct answer; comparing them as numbers means handling every pair of types.
func sameSettingValue(stored, submitted any) bool {
	return fmt.Sprint(stored) == fmt.Sprint(submitted)
}

// requireSettingsAccess refuses anybody who should not be changing installation-wide settings.
//
// Thin on purpose: requirePlatform already draws the distinction and already carries the explanation, including why a
// single-tenant administrator is not refused. The role floor is on the route, so this only has to add the tenancy rule.
//
// Named and kept separate anyway, because there are two settings endpoints and an access rule applied in one of two places is
// an access rule somebody will eventually add a third endpoint without.
func (s *Server) requireSettingsAccess(w http.ResponseWriter, r *http.Request, sess *store.Session) bool {
	return s.requirePlatform(w, r, sess, "settings")
}
