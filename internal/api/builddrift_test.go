package api

import (
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// Keeping the builder honest as the schema grows.
//
// The graphical builder duplicates the channel schema by necessity: it is a form, and a form
// has to know its fields. The failure mode is silent - somebody adds a setting to the config
// package, the loader accepts it, existing channels use it, and the builder quietly cannot
// express it. A user who builds that channel graphically gets a file missing the setting
// they needed, with nothing anywhere saying why.
//
// So this walks the real config structs by reflection and requires that every YAML key is
// either present in the build model or listed below as deliberately out of scope. Adding a
// config field then breaks this test, which is the point: the decision to support it in the
// form or not becomes explicit instead of being made by omission.

// notInTheBuilder lists keys the form deliberately does not offer, with the reason.
//
// A key belongs here when a form is the wrong place to set it, not merely when it was
// inconvenient. Anything genuinely missing should be added to the model instead.
var notInTheBuilder = map[string]string{
	// Shadow is built from the shadow tab instead, where the candidate channel is picked from a list of channels that exist rather
	// than typed from memory. These are its inner settings and belong with it.
	//
	// That reason was false when it was written, and stayed false for weeks. The shadow tab reported differences and offered no way
	// to start a comparison; there was no endpoint behind it either. So this excuse silenced the guard on the one feature whose
	// whole purpose is checking a rewrite before it goes live, and the only way to switch it on was to hand-edit the YAML that a
	// shadow exists to avoid doing blind.
	//
	// It is true now: PUT and DELETE /api/channels/{name}/shadow, with an editor on the shadow tab, and specs that start a
	// comparison and stop it. Worth keeping the history, because an excuse resting on a capability that does not exist is worse
	// than no excuse - it removes the check that would have found the gap, and it reads as a decision somebody made.
	// Qualified to the shadow block. Left bare, it also excused Destination.channel - the routing
	// target - which the builder genuinely could not express, and the test said otherwise.
	"shadow.channel":  "the shadow block's candidate channel name, which the shadow tab picks from a list",
	"shadow":          "configured from the shadow tab, where the candidate can be chosen from a list of real channels",
	"sample":          "part of the shadow configuration, which the shadow tab owns",
	"compare":         "part of the shadow configuration, which the shadow tab owns",
	"ignore":          "part of the shadow configuration, which the shadow tab owns",
	"max_differences": "part of the shadow configuration, which the shadow tab owns",

	// Three settings that weaken a connection without failing. Each of them turns a
	// refused handshake into an accepted one, so the failure they cause is silent and
	// arrives later as data going somewhere it should not have. Typing one into a file is
	// a deliberate act that leaves a diff; picking it from a dropdown is not.
	"min_version":                  "lowering the floor silently weakens every connection instead of failing, so it stays in a file where a reviewer sees the diff",
	"insecure_skip_verify":         "disabling certificate verification silently accepts any server, so it must be typed into a file deliberately",
	"insecure_skip_host_key_check": "accepting any SSH host key defeats the point of the host key, so it must be typed into a file deliberately",

	// The email destination offers everything except this one. Skipping certificate
	// verification on a mail server is the same silent weakening as everywhere else.
	"smtp": "the destination-level smtp block is offered; this key is the block itself",
}

// buildModelKeys collects every yaml key the build model can emit.
func buildModelKeys(t *testing.T) map[string]bool {
	t.Helper()

	keys := map[string]bool{}

	var walk func(reflect.Type)
	walk = func(rt reflect.Type) {
		for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}

		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)

			tag := f.Tag.Get("yaml")
			name := strings.Split(tag, ",")[0]
			if name != "" && name != "-" {
				keys[name] = true
			}

			walk(f.Type)
		}
	}
	walk(reflect.TypeOf(buildModel{}))

	return keys
}

// configKeys collects every yaml key a channel file may contain, keyed by the struct it came from so
// an omission can be reported usefully.
//
// Keyed by "owner.key" rather than by key alone, and this matters. It used to record only the first
// owner of a given name, and the channel destination proved why that was wrong: adding a "channel"
// key to Destination was silently absorbed by an excuse written for the shadow block's "channel", so
// the test reported that the builder could express a setting it had never heard of. A guard with a
// blind spot is worse than no guard, because it is trusted.
func configKeys(t *testing.T) map[string]string {
	t.Helper()

	found := map[string]string{}
	seen := map[reflect.Type]bool{}

	var walk func(reflect.Type, string)
	walk = func(rt reflect.Type, owner string) {
		for rt.Kind() == reflect.Ptr || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		// Recursion guard. Duration is a struct-free named type, but a config graph that
		// ever became cyclic would hang the test suite rather than fail it.
		if seen[rt] {
			return
		}
		seen[rt] = true

		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)

			// Unexported fields are the compiled artefacts - the parsed filter, the
			// pipeline - and are not part of the file format at all.
			if f.PkgPath != "" {
				continue
			}

			tag := f.Tag.Get("yaml")
			name := strings.Split(tag, ",")[0]
			if name != "" && name != "-" {
				// Owned by the struct that declares the field, not by the one that contains that
				// struct. Using the containing type made Shadow.channel and Destination.channel both
				// report as "channel.channel", which is the same collision one level up.
				found[strings.ToLower(rt.Name())+"."+name] = name
			}

			walk(f.Type, strings.ToLower(rt.Name()))
		}
	}

	walk(reflect.TypeOf(config.Channel{}), "channel")
	walk(reflect.TypeOf(transform.Step{}), "step")

	return found
}

func TestTheBuilderCanExpressEveryChannelSetting(t *testing.T) {
	model := buildModelKeys(t)
	actual := configKeys(t)

	var missing []string
	for qualified, key := range actual {
		if model[key] {
			continue
		}
		// Excused either by bare key or qualified by its owner, so a reason can be
		// specific to one struct where the same key name appears in several.
		if _, ok := notInTheBuilder[key]; ok {
			continue
		}
		if _, ok := notInTheBuilder[qualified]; ok {
			continue
		}
		missing = append(missing, qualified)
	}

	sort.Strings(missing)
	if len(missing) > 0 {
		t.Fatalf("the graphical builder cannot express %d channel setting(s):\n  %s\n\n"+
			"Add the field to buildModel in build.go so the form can offer it, or add it to\n"+
			"notInTheBuilder with the reason a form is the wrong place to set it. Leaving it\n"+
			"out silently means somebody building this channel from the form gets a file\n"+
			"missing the setting they needed, with nothing saying why.",
			len(missing), strings.Join(missing, "\n  "))
	}
}

func TestEveryExcuseNamesARealSetting(t *testing.T) {
	// An excuse for a key that no longer exists is worse than no excuse: it suggests the
	// gap was considered when the setting has simply been renamed and the real one is
	// now unlisted.
	actual := configKeys(t)

	// The collected keys are qualified as "owner.key". A bare excuse matches any owner; a qualified
	// one must match that owner exactly.
	bare := map[string]bool{}
	for qualified, key := range actual {
		bare[key] = true
		bare[qualified] = true
	}

	for excuse := range notInTheBuilder {
		if !bare[excuse] {
			t.Errorf("notInTheBuilder excuses %q, which is not a key in any config struct: "+
				"it has probably been renamed, and the new name is now unchecked", excuse)
		}
	}
}

func TestEveryExcuseGivesAReason(t *testing.T) {
	for key, reason := range notInTheBuilder {
		if len(strings.TrimSpace(reason)) < 20 {
			t.Errorf("%q is excused with %q, which does not explain anything", key, reason)
		}
	}
}
