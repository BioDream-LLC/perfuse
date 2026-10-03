package translate

import (
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/biodream-llc/perfuse/internal/mirth"
)

// Translating a whole export at once.
//
// A server backup or a channel group is more than its channels. Its code template libraries are the functions the channels call, so
// they become script library files that each channel includes - but only the channels Mirth had them enabled for, because those are the
// scripts that could see them. Its groups become each channel's group, and a Channel Writer can name the channel it delivers to, since
// the target is in the same document.

// LibraryDir is where library files go, relative to the channel files.
const LibraryDir = "lib"

// LibraryFile is a code template library carried across as one script file.
type LibraryFile struct {
	// Name is the library's name in Mirth.
	Name string `json:"name"`
	// File is the path a channel includes, relative to the channel files, e.g. lib/site-helpers.js.
	File string `json:"file"`
	// Source is the file's content.
	Source string `json:"source"`
	// Functions counts the templates loaded into a script's scope.
	Functions int `json:"functions"`
	// Skipped counts drag-and-drop snippets, which Mirth only ever pastes into a step by hand.
	Skipped int `json:"skipped"`
	// Channels names the translated channels that include it.
	Channels []string `json:"channels"`
}

// BundleResult is a whole export translated.
type BundleResult struct {
	// Kind and Version describe the document.
	Kind    string `json:"kind"`
	Version string `json:"version,omitempty"`

	Channels  []*Result     `json:"channels"`
	Libraries []LibraryFile `json:"libraries"`
	// Groups are the channel group names, in the order the document lists them.
	Groups []string `json:"groups"`
	// Notes are about the export as a whole, such as server-wide scripts with nowhere to go.
	Notes []Note `json:"notes"`
}

// Bundle translates every channel in an export, with its libraries and groups.
func Bundle(b *mirth.Bundle) *BundleResult {
	out := &BundleResult{Kind: b.Kind, Version: b.Version, Channels: []*Result{}, Libraries: []LibraryFile{}, Groups: []string{},
		Notes: []Note{}}

	files := map[string]*LibraryFile{}
	used := map[string]bool{}
	for _, l := range b.Libraries {
		base := sanitiseName(l.Name)
		if base == "" {
			base = "library"
		}
		name := base
		for i := 2; used[name]; i++ {
			name = fmt.Sprintf("%s-%d", base, i)
		}
		used[name] = true

		lf := &LibraryFile{Name: l.Name, File: path.Join(LibraryDir, name+".js"), Source: l.Source(), Channels: []string{}}
		for _, t := range l.Templates {
			if t.Loaded() {
				lf.Functions++
			} else {
				lf.Skipped++
			}
		}
		files[l.ID] = lf
		out.Libraries = append(out.Libraries, *lf)
	}

	for _, g := range b.Groups {
		out.Groups = append(out.Groups, g.Name)
	}

	names := b.ChannelNames()
	for _, ch := range b.Channels {
		opts := Options{ChannelNames: names, Group: b.GroupOf(ch.ID)}
		for _, l := range b.LibrariesFor(ch.ID) {
			if lf := files[l.ID]; lf != nil && lf.Functions > 0 {
				opts.Includes = append(opts.Includes, lf.File)
			}
		}
		res := ChannelWith(ch, opts)
		if len(opts.Includes) > 0 {
			res.Notes = append(res.Notes, Note{Severity: "info", Where: "channel", Message: fmt.Sprintf(
				"the code template libraries enabled for this channel were carried across as %s, and the channel includes them",
				strings.Join(opts.Includes, ", "))})
		}
		res.SortNotes()
		out.Channels = append(out.Channels, res)
		for _, f := range opts.Includes {
			for i := range out.Libraries {
				if out.Libraries[i].File == f {
					out.Libraries[i].Channels = append(out.Libraries[i].Channels, res.Name)
				}
			}
		}
	}

	if len(b.GlobalScripts) > 0 {
		keys := make([]string, 0, len(b.GlobalScripts))
		for k, v := range b.GlobalScripts {
			if strings.TrimSpace(stripDefaultGlobalScript(v)) != "" {
				keys = append(keys, k)
			}
		}
		sort.Strings(keys)
		if len(keys) > 0 {
			out.Notes = append(out.Notes, Note{Severity: "warning", Where: "server", Message: fmt.Sprintf(
				"the server-wide %s script(s) were not translated: Perfuse has no server-wide scripts", strings.Join(keys, ", ")),
				Action: "Move what they do into the channels that depend on it, usually their deploy or preprocessor script"})
		}
	}

	return out
}

// stripDefaultGlobalScript removes Mirth's comment-only default, so an untouched global script is not reported.
func stripDefaultGlobalScript(s string) string {
	var keep []string
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if t == "" || strings.HasPrefix(t, "//") || t == "return;" || t == "return message;" {
			continue
		}
		keep = append(keep, t)
	}
	return strings.Join(keep, "\n")
}
