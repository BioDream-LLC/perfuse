// Package settings is the single description of everything about a Perfuse server that an operator may change.
//
// # Why this exists
//
// Before this package, roughly thirty things were command-line flags and nothing else. Changing how long messages are kept
// meant editing a service definition and restarting, which in a hospital means a change ticket - so in practice nobody changed
// them, and a default chosen on the first afternoon became the setting forever.
//
// # Why one registry rather than a form
//
// The obvious way to build a settings page is to write the form. That produces two descriptions of every setting - one in the
// server that validates it and one in the interface that renders it - and they drift. The drift is quiet: a field the server
// stopped accepting still has a control, so somebody sets it, sees it saved, and it does nothing.
//
// So a setting is declared once, here, with everything needed to both check it and draw it: what kind of value it is, what
// control suits it, which group it belongs in, what it means, and whether changing it takes effect now. The interface asks for
// that description and renders whatever it is told. Adding a setting is one entry in this package and no interface work at all.
//
// TestEverySettingIsFullyDescribed enforces the parts a human has to supply, because a setting with no help text is a setting
// somebody guesses at.
//
// # Why a file rather than the database
//
// Settings live in a YAML file the interface edits in place, matching how channels and alert rules already work. Three reasons,
// in order of weight: a hospital's change control wants configuration that can be diffed and put in version control; an
// operator with the machine but not the application can read a file and cannot read a database; and restoring yesterday's
// settings should not require the application to be working.
//
// # Why flags only seed it
//
// The file wins once it exists, and flags supply the initial contents. The alternative - flags always winning - would make the
// interface a liar, since anything ever passed on a command line could be edited and saved and silently ignored.
//
// A flag that disagrees with the file is reported at startup, naming both values, because otherwise the flag stays in the
// service definition for years and the next person to read it believes it.
package settings

import (
	"fmt"
	"sort"
	"strings"
)

// Kind is what sort of value a setting holds.
//
// Deliberately small. Every kind here needs different validation and a different control, and a kind that needed neither would
// be a kind nobody could see the point of.
type Kind string

const (
	// KindBool is on or off.
	KindBool Kind = "bool"

	// KindInt is a whole number, usually with a range.
	KindInt Kind = "int"

	// KindString is a single line.
	KindString Kind = "string"

	// KindSecret is a single line that is never sent back.
	//
	// The interface is told whether one is set, not what it is. A settings page that renders a password into an input is a
	// settings page that puts it in the browser's memory, in a screenshot, and in whatever the browser saved.
	KindSecret Kind = "secret"

	// KindChoice is one of a fixed set.
	KindChoice Kind = "choice"

	// KindDuration is a length of time, held as a Go duration string.
	KindDuration Kind = "duration"

	// KindList is an ordered list of single lines.
	KindList Kind = "list"

	// KindCode is a multi-line value with a syntax - a YAML document or a script.
	KindCode Kind = "code"
)

// Widget is the control the interface should draw.
//
// Separate from Kind because one kind suits several controls and the choice is about how the value behaves, not what it is. A
// number with a known range and a number without are both integers, but a slider for a port number is useless and a slider for
// a sampling percentage is exactly right.
type Widget string

const (
	// WidgetToggle is a switch, for a boolean.
	WidgetToggle Widget = "toggle"

	// WidgetSlider is for a bounded number where the useful thing is the relative position.
	WidgetSlider Widget = "slider"

	// WidgetNumber is a spinner, for a number where the exact value matters.
	WidgetNumber Widget = "number"

	// WidgetText is a single-line box.
	WidgetText Widget = "text"

	// WidgetSecret is a single-line box that never shows an existing value.
	WidgetSecret Widget = "secret"

	// WidgetRadio is for a small set of choices that should all be visible.
	//
	// Used where the options have consequences worth reading rather than picking blind - an acknowledgement level, a
	// retention policy. A dropdown hides them until somebody opens it.
	WidgetRadio Widget = "radio"

	// WidgetSelect is for a set too long to show at once.
	WidgetSelect Widget = "select"

	// WidgetList is a repeated single-line editor.
	WidgetList Widget = "list"

	// WidgetCode is a CodeMirror editor.
	WidgetCode Widget = "code"
)

// Choice is one option of a KindChoice setting.
type Choice struct {
	// Value is what is stored.
	Value string `json:"value"`

	// Label is what is shown.
	Label string `json:"label"`

	// Help explains the consequence of this option, not what it is called.
	//
	// Optional, but the reason the type has it: "strict" tells somebody nothing, and "refuse a file whose segment count
	// disagrees" tells them whether they want it.
	Help string `json:"help,omitempty"`
}

// Effect says when a change takes hold.
type Effect string

const (
	// EffectLive takes hold on save.
	EffectLive Effect = "live"

	// EffectRestart needs the process restarted.
	//
	// Shown in the interface rather than left to be discovered. A settings page that silently needs a restart is how
	// somebody changes retention, sees that it saved, and finds out three weeks later that nothing was ever deleted.
	EffectRestart Effect = "restart"

	// EffectReconnect takes hold for connections made after the change, leaving existing ones alone.
	//
	// Its own value rather than being folded into live or restart, because both would be wrong in a way that matters: an
	// operator who tightens a credential needs to know the session already open is still open.
	EffectReconnect Effect = "reconnect"
)

// Setting is one thing an operator may change.
type Setting struct {
	// Key identifies it, dotted and stable: "data.retentionDays".
	//
	// Stable because it is what the file is keyed by. Renaming one silently abandons whatever an operator had set, so the
	// rename path is a migration rather than an edit.
	Key string `json:"key"`

	// Group and Subgroup place it in the interface.
	//
	// Two levels and no more. A third level is how a settings page becomes something you navigate rather than read.
	Group    string `json:"group"`
	Subgroup string `json:"subgroup"`

	// Label is the control's caption, in sentence case and without a trailing colon.
	Label string `json:"label"`

	// Help says what the setting does and what happens if it is wrong.
	//
	// Required. The whole reason settings were flags for so long is that a flag comes with a line of help text and a form
	// field does not, so the form is the thing that has to be made to carry it.
	Help string `json:"help"`

	// Kind is what sort of value it is.
	Kind Kind `json:"kind"`

	// Widget is how to draw it.
	Widget Widget `json:"widget"`

	// Effect says when a change takes hold.
	Effect Effect `json:"effect"`

	// Default is the value when nothing has been set.
	//
	// Typed to match Kind - bool, int, string, or []string - and checked by the drift test rather than trusted.
	Default any `json:"default"`

	// Min and Max bound a number. Both nil means unbounded.
	Min *int `json:"min,omitempty"`
	Max *int `json:"max,omitempty"`

	// Unit is what the number counts: "days", "percent", "MB".
	//
	// On the control rather than only in the help, because a box containing 30 is ambiguous and a box containing 30 next to
	// the word "days" is not.
	Unit string `json:"unit,omitempty"`

	// Choices are the options for KindChoice.
	Choices []Choice `json:"choices,omitempty"`

	// Language is the syntax for KindCode: "yaml" or "javascript".
	Language string `json:"language,omitempty"`

	// Flag names the command-line flag that seeds this setting, without its dash.
	//
	// Recorded so the interface can say where a value came from, and so a flag that disagrees with the file can be reported
	// by name at startup instead of as a mystery.
	Flag string `json:"flag,omitempty"`

	// Advanced hides it behind a disclosure.
	//
	// For settings that are real but that most installations should not touch. Hidden rather than absent, because a setting
	// nobody can find is one they will instead ask for as a feature.
	Advanced bool `json:"advanced,omitempty"`

	// Sensitive marks a setting whose change is worth an audit entry beyond the ordinary.
	//
	// Anything that alters who can get in or what leaves the building.
	Sensitive bool `json:"sensitive,omitempty"`

	// Validate checks a value beyond its kind and bounds. Nil when kind and bounds are the whole story.
	//
	// Not serialised: the interface cannot run it, and pretending otherwise would mean writing the same rule twice. The
	// interface catches what it can and the server is what refuses.
	Validate func(any) error `json:"-"`
}

// Registry is the full set of settings, in a stable order.
type Registry struct {
	settings []Setting
	byKey    map[string]Setting
}

// NewRegistry builds a registry, refusing anything malformed.
//
// Refuses rather than skips, and at construction rather than at first use: every caller of this is a program starting up, and a
// setting silently missing from the interface is worse than a server that will not start with a clear message.
func NewRegistry(all []Setting) (*Registry, error) {
	byKey := make(map[string]Setting, len(all))

	for _, s := range all {
		if err := s.check(); err != nil {
			return nil, err
		}
		if _, dup := byKey[s.Key]; dup {
			// Two settings with one key means one of them is unreachable, and which one depends on map ordering.
			return nil, fmt.Errorf("settings: %q is declared twice", s.Key)
		}
		byKey[s.Key] = s
	}

	ordered := make([]Setting, len(all))
	copy(ordered, all)

	// Sorted by group, then subgroup, then label. Go maps range randomly and a settings page whose fields move between
	// page loads is one nobody can give directions to over the telephone.
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Group != ordered[j].Group {
			return ordered[i].Group < ordered[j].Group
		}
		if ordered[i].Subgroup != ordered[j].Subgroup {
			return ordered[i].Subgroup < ordered[j].Subgroup
		}
		return ordered[i].Label < ordered[j].Label
	})

	return &Registry{settings: ordered, byKey: byKey}, nil
}

// check refuses a setting that could not be rendered or validated.
func (s Setting) check() error {
	switch {
	case s.Key == "":
		return fmt.Errorf("settings: a setting has no key")
	case s.Group == "":
		return fmt.Errorf("settings: %q has no group", s.Key)
	case s.Label == "":
		return fmt.Errorf("settings: %q has no label", s.Key)
	case s.Help == "":
		// Enforced rather than encouraged. This is the one piece a person cannot derive from the type.
		return fmt.Errorf("settings: %q has no help text", s.Key)
	case s.Effect == "":
		return fmt.Errorf("settings: %q does not say when a change takes effect", s.Key)
	}

	if !strings.Contains(s.Key, ".") {
		// Dotted keys keep the file readable as nested YAML rather than one flat list of thirty entries.
		return fmt.Errorf("settings: %q should be dotted, as in group.name", s.Key)
	}

	// The default has to match the kind, or the first read of an unset setting returns the wrong Go type and the failure
	// surfaces somewhere unrelated.
	switch s.Kind {
	case KindBool:
		if _, ok := s.Default.(bool); !ok {
			return fmt.Errorf("settings: %q is a bool with a %T default", s.Key, s.Default)
		}
	case KindInt:
		if _, ok := s.Default.(int); !ok {
			return fmt.Errorf("settings: %q is an int with a %T default", s.Key, s.Default)
		}
		if s.Min != nil && s.Max != nil && *s.Min > *s.Max {
			return fmt.Errorf("settings: %q has a minimum above its maximum", s.Key)
		}
		if s.Widget == WidgetSlider && (s.Min == nil || s.Max == nil) {
			// A slider without both ends has no meaningful travel, and the control would render at an
			// arbitrary position.
			return fmt.Errorf("settings: %q is a slider without both a minimum and a maximum", s.Key)
		}
	case KindString, KindSecret, KindDuration, KindCode:
		if _, ok := s.Default.(string); !ok {
			return fmt.Errorf("settings: %q is a %s with a %T default", s.Key, s.Kind, s.Default)
		}
	case KindList:
		if _, ok := s.Default.([]string); !ok {
			return fmt.Errorf("settings: %q is a list with a %T default", s.Key, s.Default)
		}
	case KindChoice:
		def, ok := s.Default.(string)
		if !ok {
			return fmt.Errorf("settings: %q is a choice with a %T default", s.Key, s.Default)
		}
		if len(s.Choices) == 0 {
			return fmt.Errorf("settings: %q is a choice with no options", s.Key)
		}
		// A default outside the options renders as nothing selected, and saving the form would then change a
		// setting nobody touched.
		var found bool
		for _, c := range s.Choices {
			if c.Value == def {
				found = true
			}
			if c.Label == "" {
				return fmt.Errorf("settings: %q has an option %q with no label", s.Key, c.Value)
			}
		}
		if !found {
			return fmt.Errorf("settings: %q defaults to %q, which is not one of its options", s.Key, def)
		}
	default:
		return fmt.Errorf("settings: %q has unknown kind %q", s.Key, s.Kind)
	}

	if s.Kind == KindCode && s.Language == "" {
		return fmt.Errorf("settings: %q is code with no language", s.Key)
	}

	// A secret that could be read back is the one mistake in this package that would matter, so the pairing is enforced
	// here rather than left to whoever writes the handler.
	if (s.Kind == KindSecret) != (s.Widget == WidgetSecret) {
		return fmt.Errorf("settings: %q must use the secret kind and the secret widget together", s.Key)
	}

	return nil
}

// All returns every setting in display order.
func (r *Registry) All() []Setting {
	out := make([]Setting, len(r.settings))
	copy(out, r.settings)

	return out
}

// Lookup finds a setting by key.
func (r *Registry) Lookup(key string) (Setting, bool) {
	s, ok := r.byKey[key]

	return s, ok
}

// Groups returns the group names in display order, without duplicates.
func (r *Registry) Groups() []string {
	var out []string
	seen := make(map[string]bool)

	for _, s := range r.settings {
		if !seen[s.Group] {
			seen[s.Group] = true
			out = append(out, s.Group)
		}
	}

	return out
}
