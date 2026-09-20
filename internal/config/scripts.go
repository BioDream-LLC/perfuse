package config

import (
	"errors"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/codeset"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/script"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// Scripting and declarative transformation are configured here.
//
// The two exist as separate blocks on purpose, and the shape of the
// configuration is the argument for the design: transformations are a list of
// named, checkable steps, while a script is an opaque string. A channel with only
// transformations can be reviewed by somebody who does not read JavaScript, and
// every step is validated when the file loads. A channel with a script cannot be,
// which is why the interface marks it and why permissions have to be granted
// explicitly rather than assumed.
//
// The order is fixed and not configurable: filter, then transformations, then
// script. Transformations run first because they are the checkable layer, so a
// script sees a message that has already been normalised, and anything a script
// then does is visibly the exception rather than mixed in among the ordinary work.

// Scripts holds the JavaScript attached to a channel.
type Scripts struct {
	// Filter runs after the expression filter, if any, and must return a
	// boolean. A Mirth filter script drops in here unchanged.
	Filter string `yaml:"filter,omitempty"`

	// Transformer runs after the declarative steps and may modify the message.
	// A Mirth transformer script drops in here unchanged.
	Transformer string `yaml:"transformer,omitempty"`

	// Preprocessor runs before the message is parsed, reads the text as "message" and returns the
	// text to parse. A Mirth preprocessor drops in here unchanged.
	//
	// This is the only place a message that does not parse can be repaired. Sites use it for a stray
	// character from a serial gateway, a segment terminator that arrived as a line feed, or a vendor
	// padding a field with something illegal for its position. Without it, Perfuse rejects traffic
	// that Mirth accepts today.
	Preprocessor string `yaml:"preprocessor,omitempty"`

	// Deploy runs once when the channel starts, before it accepts anything.
	//
	// This is where sites warm a lookup cache or check that something they depend on is reachable. It
	// runs before the source binds, so a deploy script that fails stops the channel from starting
	// rather than letting it accept messages it cannot handle.
	Deploy string `yaml:"deploy,omitempty"`

	// Undeploy runs once when the channel stops.
	//
	// After the source has stopped accepting and after in-flight messages have finished, so it sees a
	// channel that is genuinely idle. Its failure cannot stop a stop: a channel that could not be
	// shut down because a cleanup script threw would be worse than one that logged and closed.
	Undeploy string `yaml:"undeploy,omitempty"`

	// Postprocessor runs after the message has been handled and cannot change it.
	//
	// The message and what happened to it are visible, so it can notify, count or record. It cannot
	// alter what was sent: by the time it runs the sending has happened, and a script that appeared
	// to change a delivered message would be lying about it.
	Postprocessor string `yaml:"postprocessor,omitempty"`

	// Language is which scripting language these scripts are written in: javascript or lua.
	//
	// # Why one setting for the whole block rather than one per script
	//
	// A channel's scripts share the channel map and the connector map, and they are read together by whoever maintains them. A
	// filter in Lua beside a transformer in JavaScript would mean two languages in one review, sharing state through a map
	// whose values would have to mean the same in both. Sites that want that can split the channel.
	//
	// # Why the default is javascript
	//
	// Every script written before this setting existed is JavaScript and says nothing about its language. Making lua the
	// default would silently reinterpret all of them; requiring the field would refuse them. Neither is acceptable for a
	// setting added later.
	Language string `yaml:"language,omitempty"`

	// Timeout bounds each script. Zero means five seconds.
	//
	// There is deliberately no way to disable it. Mirth allows a transformer to
	// loop forever, which stops the channel, and an interface that stops
	// accepting admissions because of a typo is not something to offer as an
	// option.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// Include names files of shared script source made available to every script on this channel, in whichever language
	// scripts.language names.
	//
	// This is Mirth's code templates. A site with forty channels usually has one library every channel
	// calls, and without it a migration means copying that library into forty files - forty places to
	// fix the next time it changes.
	//
	// Paths are relative to the channel file, so a library beside the channels travels with them. It
	// is a list because the alternative is one enormous file, and libraries tend to divide by subject.
	//
	// The files have to be in the channel's language. This was JavaScript-only for a while, and not by
	// refusal: a Lua channel read the files, refused a missing one, and then the runtime installed
	// none of it, so every call into the library failed on the first message with a nil global - the
	// exact error the refusal above exists to avoid. Both languages install it now, and a library that
	// does not parse is refused at load naming the file.
	Include []string `yaml:"include,omitempty"`

	// FileRoots are the directories FileUtil may read and write.
	//
	// Required whenever "file" is allowed. Granting file access without naming directories used to mean the whole
	// filesystem: a script could read Perfuse's own database, which holds password hashes, the LDAP service account
	// password and the OIDC client secret. Creating a channel needs only the editor role, so the escalation
	// completed the moment an administrator started it.
	FileRoots []string `yaml:"file_roots,omitempty"`

	// Allow grants capabilities a script would otherwise be refused: "file",
	// "database", "route". Mirth grants all of these to every script; requiring
	// them to be named means a transformer cannot quietly start reading the
	// filesystem after a copy-paste.
	Allow []string `yaml:"allow,omitempty"`

	compiledFilter        *script.Script
	compiledTransformer   *script.Script
	compiledPreprocessor  *script.Script
	compiledPostprocessor *script.Script
	compiledDeploy        *script.Script
	compiledUndeploy      *script.Script
	engine                *script.Engine
}

// Permissions converts the allow list, rejecting anything unrecognised so that a
// typo does not silently deny a capability the author meant to grant.
func (s *Scripts) Permissions() ([]script.Permission, error) {
	var out []script.Permission
	for _, name := range s.Allow {
		switch strings.ToLower(strings.TrimSpace(name)) {
		case "file":
			out = append(out, script.PermFile)
		case "database":
			// Refused at load rather than accepted and ignored.
			//
			// Nothing consults this permission: DatabaseConnectionFactory is refused whether or not it is
			// granted, because a transformer that silently reads nothing from a database produces messages
			// that are wrong in a way no test catches.
			//
			// Accepting the word here would validate a channel while granting nothing, so somebody would
			// read scripts.allow, see database, and reasonably conclude their lookup works - and find out at
			// three in the morning that it never did. Saying so when the file is read costs nothing.
			return nil, fmt.Errorf("scripts.allow: \"database\" cannot be granted because " +
				"DatabaseConnectionFactory is not implemented; use a database destination, or a lookup " +
				"table loaded at deploy time, rather than a query inside a transformer")
		case "route":
			out = append(out, script.PermRoute)
		case "":
			continue
		default:
			return nil, fmt.Errorf("scripts.allow: %q is not a capability; expected file, database or route", name)
		}
	}
	return out, nil
}

// Empty reports whether there is any script at all.
func (s *Scripts) Empty() bool {
	if s == nil {
		return true
	}
	// Every script counts. Listing only two here meant a channel whose only script was a preprocessor
	// was treated as having none, so nothing was compiled and the script silently never ran - the
	// worst possible outcome, because the file plainly contains it.
	for _, src := range []string{
		s.Filter, s.Transformer, s.Preprocessor, s.Postprocessor, s.Deploy, s.Undeploy,
	} {
		if strings.TrimSpace(src) != "" {
			return false
		}
	}
	return true
}

// compile prepares the scripts, returning the notes so the caller can report
// what was translated.
func (s *Scripts) compile(channelName, dir string) ([]ScriptNote, error) {
	if s == nil {
		return nil, nil
	}
	// Deliberately not returning early when Empty. A channel may have no scripts of its own and still
	// need an engine, because a destination response transformer is compiled against it.

	perms, err := s.Permissions()
	if err != nil {
		return nil, err
	}

	// The same confinement validate insists on, built here for the engine to use.
	//
	// Derived from the permission list rather than from the presence of file_roots, so that the two cannot disagree:
	// the engine is confined exactly when the permission is granted.
	var roots *script.FileRoots
	for _, p := range perms {
		if p != script.PermFile {
			continue
		}
		if len(s.FileRoots) == 0 {
			return nil, fmt.Errorf("scripts.allow includes \"file\" but scripts.file_roots is empty; " +
				"file access has to name the directories it may use")
		}
		built, rootErr := script.NewFileRoots(s.FileRoots)
		if rootErr != nil {
			return nil, fmt.Errorf("scripts.file_roots: %w", rootErr)
		}
		roots = built
	}

	timeout := s.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	if timeout > time.Minute {
		return nil, fmt.Errorf("scripts.timeout of %s is too long; a message handler that can block for a minute will stall the channel under load", timeout)
	}

	library, libraryName, err := s.readIncludes(dir)
	if err != nil {
		return nil, err
	}

	// Parsed once, before anything is compiled, so a misspelled language name is one clear error rather than the same error
	// repeated per script.
	language, err := script.ParseLanguage(s.Language)
	if err != nil {
		return nil, fmt.Errorf("scripts.language: %w", err)
	}

	s.engine = script.New(script.Options{
		Timeout:     timeout,
		Permissions: perms,
		FileRoots:   roots,

		// The language has to reach the engine because the library is compiled once at construction, and a runtime cannot
		// compile another runtime's source. Omitting it meant every library was compiled as JavaScript: a Lua library recorded a
		// JavaScript syntax error that only the JavaScript path read, while the Lua path installed nothing.
		Language:    language,
		Library:     library,
		LibraryName: libraryName,
	})

	// What a slot holds depends on the language, and for WebAssembly it is a path.
	//
	// A module is compiled output - two megabytes for anything built from Go - so it cannot be a string in a channel file the way
	// a script can. The field names a file instead, resolved relative to the channel like scripts.include already is, so a module
	// sitting beside the channels travels with them into a repository or a container image.
	//
	// The same field rather than six new ones. scripts.language is mandatory to reach this at all and sits three lines above, so
	// the field's meaning is declared next to it - and doubling six keys into twelve to avoid that would be a worse trade. The
	// loader refuses a value that is not a readable module, which is what keeps the overloading honest: a path typed into a Lua
	// channel is a syntax error, and a Lua script left in a wasm channel is refused by name.
	sourceFor := func(slot, raw string) (string, error) {
		raw = strings.TrimSpace(raw)
		if raw == "" || language != script.WASM {
			return raw, nil
		}

		path := raw
		if !filepath.IsAbs(path) {
			path = filepath.Join(dir, path)
		}

		body, err := os.ReadFile(path)
		if err != nil {
			return "", fmt.Errorf("scripts.%s: a wasm script names a module file, and this one could not be read: %w",
				slot, err)
		}

		return string(body), nil
	}

	var notes []ScriptNote

	if raw := strings.TrimSpace(s.Filter); raw != "" {
		src, err := sourceFor("filter", raw)
		if err != nil {
			return nil, err
		}

		compiled, err := s.engine.CompileIn(channelName+" filter", src, script.Filter, language)
		if err != nil {
			return nil, err
		}
		s.compiledFilter = compiled
		notes = append(notes, scriptNotes("filter", compiled)...)
	}

	if raw := strings.TrimSpace(s.Transformer); raw != "" {
		src, err := sourceFor("transformer", raw)
		if err != nil {
			return nil, err
		}

		compiled, err := s.engine.CompileIn(channelName+" transformer", src, script.Transformer, language)
		if err != nil {
			return nil, err
		}
		s.compiledTransformer = compiled
		notes = append(notes, scriptNotes("transformer", compiled)...)
	}

	if raw := strings.TrimSpace(s.Preprocessor); raw != "" {
		src, err := sourceFor("preprocessor", raw)
		if err != nil {
			return nil, err
		}

		compiled, err := s.engine.CompileIn(channelName+" preprocessor", src, script.Preprocessor, language)
		if err != nil {
			return nil, err
		}
		s.compiledPreprocessor = compiled
		notes = append(notes, scriptNotes("preprocessor", compiled)...)
	}

	if raw := strings.TrimSpace(s.Deploy); raw != "" {
		src, err := sourceFor("deploy", raw)
		if err != nil {
			return nil, err
		}

		compiled, err := s.engine.CompileIn(channelName+" deploy", src, script.Lifecycle, language)
		if err != nil {
			return nil, err
		}
		s.compiledDeploy = compiled
		notes = append(notes, scriptNotes("deploy", compiled)...)
	}

	if raw := strings.TrimSpace(s.Undeploy); raw != "" {
		src, err := sourceFor("undeploy", raw)
		if err != nil {
			return nil, err
		}

		compiled, err := s.engine.CompileIn(channelName+" undeploy", src, script.Lifecycle, language)
		if err != nil {
			return nil, err
		}
		s.compiledUndeploy = compiled
		notes = append(notes, scriptNotes("undeploy", compiled)...)
	}

	if raw := strings.TrimSpace(s.Postprocessor); raw != "" {
		src, err := sourceFor("postprocessor", raw)
		if err != nil {
			return nil, err
		}

		compiled, err := s.engine.CompileIn(channelName+" postprocessor", src, script.Postprocessor, language)
		if err != nil {
			return nil, err
		}
		s.compiledPostprocessor = compiled
		notes = append(notes, scriptNotes("postprocessor", compiled)...)
	}

	return notes, nil
}

// ScriptNote reports an E4X construct that was translated, so that somebody
// porting a channel can see what happened to their script rather than being told
// nothing did.
type ScriptNote struct {
	Where   string `json:"where"`
	Line    int    `json:"line"`
	Kind    string `json:"kind"`
	Detail  string `json:"detail"`
	Snippet string `json:"snippet,omitempty"`
}

func scriptNotes(where string, s *script.Script) []ScriptNote {
	out := make([]ScriptNote, 0, len(s.Notes))
	for _, n := range s.Notes {
		out = append(out, ScriptNote{
			Where:   where,
			Line:    n.Line,
			Kind:    n.Kind,
			Detail:  n.Detail,
			Snippet: n.Snippet,
		})
	}
	return out
}

// Engine returns the compiled script engine, or nil when the channel has none.
func (s *Scripts) Engine() *script.Engine {
	if s == nil {
		return nil
	}
	return s.engine
}

// FilterScript returns the compiled filter script, or nil.
func (s *Scripts) FilterScript() *script.Script {
	if s == nil {
		return nil
	}
	return s.compiledFilter
}

// TransformerScript returns the compiled transformer, or nil.
func (s *Scripts) TransformerScript() *script.Script {
	if s == nil {
		return nil
	}
	return s.compiledTransformer
}

// compileTransformations validates the declarative steps against the shared tables the channel loaded.
func compileTransformations(steps []transform.Step, tables *codeset.Set) (*transform.Pipeline, error) {
	if len(steps) == 0 {
		return nil, nil
	}
	return transform.CompileWith(steps, tables)
}

// PreprocessorScript returns the compiled preprocessor, or nil.
func (s *Scripts) PreprocessorScript() *script.Script {
	if s == nil {
		return nil
	}
	return s.compiledPreprocessor
}

// PostprocessorScript returns the compiled postprocessor, or nil.
func (s *Scripts) PostprocessorScript() *script.Script {
	if s == nil {
		return nil
	}
	return s.compiledPostprocessor
}

// DeployScript returns the compiled deploy script, or nil.
func (s *Scripts) DeployScript() *script.Script {
	if s == nil {
		return nil
	}
	return s.compiledDeploy
}

// UndeployScript returns the compiled undeploy script, or nil.
func (s *Scripts) UndeployScript() *script.Script {
	if s == nil {
		return nil
	}
	return s.compiledUndeploy
}

// readIncludes loads the shared library files this channel asked for.
//
// Concatenated in the order given, because a library may depend on one listed before it and reordering
// somebody's list to suit an implementation detail would be surprising.
func (s *Scripts) readIncludes(dir string) (string, string, error) {
	if len(s.Include) == 0 {
		return "", "", nil
	}

	var b strings.Builder
	var names []string

	for _, name := range s.Include {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}

		path := name
		if !filepath.IsAbs(path) {
			// Relative to the channel file, so a library sitting beside the channels travels with
			// them - into a git repository, into a container image, onto another server.
			path = filepath.Join(dir, path)
		}

		body, err := os.ReadFile(path)
		if err != nil {
			// Refused rather than skipped. A missing library means every script that calls into it
			// fails with an "undefined is not a function" pointing at the wrong file, which is a much
			// harder thing to diagnose than a missing file named at load.
			return "", "", fmt.Errorf("scripts.include: %w", err)
		}

		b.Write(body)
		// A newline between files, so a library ending without one cannot swallow the first line of
		// the next.
		b.WriteString("\n")
		names = append(names, filepath.Base(path))
	}

	return b.String(), strings.Join(names, ", "), nil
}

// Release frees anything the compiled scripts hold.
//
// Called from Channel.Stop, and only WebAssembly has anything to release: a wazero module is mapped executable memory that Go's
// collector does not account for, so a server whose config is reloaded would accumulate one runtime per module per reload. Unbounded
// growth in a process meant to run for months, and invisible in the memory statistics somebody would look at.
//
// Every slot, including deploy and undeploy, because any of them can be a module.
//
// Errors are joined rather than returned on the first failure. A close that fails is not a reason to leave the other five holding
// memory, and the caller is a shutdown path that logs rather than aborts.
func (s *Scripts) Release() error {
	if s == nil {
		return nil
	}

	return errors.Join(
		s.compiledFilter.Release(),
		s.compiledTransformer.Release(),
		s.compiledPreprocessor.Release(),
		s.compiledPostprocessor.Release(),
		s.compiledDeploy.Release(),
		s.compiledUndeploy.Release(),
	)
}
