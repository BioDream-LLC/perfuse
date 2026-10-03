package mirth

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

// The documents a migration actually starts from.
//
// A per-channel export is the smallest of them. A site leaving Mirth, the Open Integration Engine or BridgeLink more often hands over one
// of three larger documents, and every one of those engines writes them in the same shape:
//
//   - <serverConfiguration>, the backup of everything: channels, channel groups, code template libraries, global scripts.
//   - <channelGroup>, the Administrator's "Export Group", with the whole channels inside it.
//   - <list> of <codeTemplateLibrary>, the code template export - the shared functions every channel calls.
//
// Reading only the first meant a channel calling its site's library imported cleanly and failed on the first message with an undefined
// function, and a whole-server backup could not be read at all. Each shape was confirmed against documents written by Mirth 4.5.2,
// OIE 4.5.2 and 4.6.0, and BridgeLink 26.9.0 (testdata/engines).

// Bundle is everything one document carries.
type Bundle struct {
	// Kind names the document: "channel", "channel list", "server backup", "channel group" or "code template libraries".
	Kind string

	// Version is the engine version on the root element, when there is one.
	Version string

	Channels  []*Channel
	Groups    []ChannelGroup
	Libraries []CodeTemplateLibrary

	// GlobalScripts are the server-wide deploy, undeploy, preprocessor and postprocessor scripts, by name. Only a backup has them.
	GlobalScripts map[string]string
}

// ChannelGroup is a named set of channels.
type ChannelGroup struct {
	ID          string
	Name        string
	Description string
	ChannelIDs  []string
}

// CodeTemplateLibrary is a set of shared scripts and the channels that can see them.
type CodeTemplateLibrary struct {
	ID          string
	Name        string
	Description string

	// IncludeNewChannels makes the library visible to every channel not explicitly disabled. Mirth's default for a new library is false.
	IncludeNewChannels bool
	EnabledChannelIDs  []string
	DisabledChannelIDs []string

	Templates []CodeTemplate
}

// CodeTemplate is one shared script.
type CodeTemplate struct {
	ID   string
	Name string
	// Type is FUNCTION, DRAG_AND_DROP_CODE or COMPILED_CODE. Only FUNCTION and COMPILED_CODE are loaded into a script's scope; a
	// drag-and-drop snippet is pasted into a step by hand in the Administrator, so it is not code that runs by itself.
	Type string
	Code string
	// Contexts are where Mirth makes the template available, such as SOURCE_FILTER_TRANSFORMER.
	Contexts []string
}

// Loaded reports whether Mirth puts the template into a script's scope. A drag-and-drop snippet is only ever copied into a step.
func (t CodeTemplate) Loaded() bool { return !strings.EqualFold(t.Type, "DRAG_AND_DROP_CODE") }

// AppliesTo reports whether a channel's scripts could call into the library.
func (l CodeTemplateLibrary) AppliesTo(channelID string) bool {
	for _, id := range l.DisabledChannelIDs {
		if id == channelID {
			return false
		}
	}
	if l.IncludeNewChannels {
		return true
	}
	for _, id := range l.EnabledChannelIDs {
		if id == channelID {
			return true
		}
	}
	return false
}

// Source is the library as one script file: every loaded template, in order, each headed by a comment naming it.
func (l CodeTemplateLibrary) Source() string {
	var b strings.Builder
	fmt.Fprintf(&b, "// Code template library %q", l.Name)
	if d := strings.TrimSpace(l.Description); d != "" {
		fmt.Fprintf(&b, ": %s", strings.Join(strings.Fields(d), " "))
	}
	b.WriteString("\n// Carried across from Mirth unchanged. Every channel that had this library enabled includes this file.\n")
	for _, t := range l.Templates {
		if !t.Loaded() {
			continue
		}
		fmt.Fprintf(&b, "\n// Template %q (%s)\n%s\n", t.Name, strings.ToLower(t.Type), strings.TrimRight(t.Code, "\n"))
	}
	return b.String()
}

// ChannelNames maps every channel id in the bundle to its name.
func (b *Bundle) ChannelNames() map[string]string {
	out := make(map[string]string, len(b.Channels))
	for _, c := range b.Channels {
		out[c.ID] = c.Name
	}
	return out
}

// LibrariesFor returns the libraries a channel could call into, by name order.
func (b *Bundle) LibrariesFor(channelID string) []CodeTemplateLibrary {
	var out []CodeTemplateLibrary
	for _, l := range b.Libraries {
		if l.AppliesTo(channelID) {
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// GroupOf names the group a channel belongs to, or "".
func (b *Bundle) GroupOf(channelID string) string {
	for _, g := range b.Groups {
		for _, id := range g.ChannelIDs {
			if id == channelID {
				return g.Name
			}
		}
	}
	return ""
}

// ParseBundleFile reads any Mirth-family export from disk.
func ParseBundleFile(path string) (*Bundle, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	b, err := ParseBundle(f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return b, nil
}

// ParseBundle reads any of the documents a Mirth-family engine exports: a channel, a list of channels, a server backup, a channel group
// or a code template library export.
func ParseBundle(r io.Reader) (*Bundle, error) {
	root, err := parseTree(r)
	if err != nil {
		return nil, err
	}

	b := &Bundle{}
	if root.Attr != nil {
		b.Version = root.Attr["version"]
	}

	switch root.Name {
	case "channel":
		b.Kind = "channel"
		b.Channels = []*Channel{channelFromNode(root)}

	case "serverConfiguration":
		b.Kind = "server backup"
		for _, cn := range root.child("channels").children("channel") {
			b.Channels = append(b.Channels, channelFromNode(cn))
		}
		for _, gn := range root.child("channelGroups").children("channelGroup") {
			b.Groups = append(b.Groups, groupFromNode(gn))
		}
		for _, ln := range root.child("codeTemplateLibraries").children("codeTemplateLibrary") {
			b.Libraries = append(b.Libraries, libraryFromNode(ln))
		}
		if gs := root.child("globalScripts"); gs != nil {
			b.GlobalScripts = map[string]string{}
			for _, e := range gs.children("entry") {
				if kids := e.children("string"); len(kids) == 2 {
					b.GlobalScripts[kids[0].Text] = kids[1].Text
				}
			}
		}

	case "channelGroup":
		b.Kind = "channel group"
		g := groupFromNode(root)
		for _, cn := range root.child("channels").children("channel") {
			// A group export carries whole channels; a backup's group carries only their ids. Both are read the same way.
			if cn.child("sourceConnector") != nil {
				b.Channels = append(b.Channels, channelFromNode(cn))
			}
		}
		b.Groups = []ChannelGroup{g}

	case "codeTemplateLibrary":
		b.Kind = "code template libraries"
		b.Libraries = []CodeTemplateLibrary{libraryFromNode(root)}

	case "list", "set":
		for _, kid := range root.Kids {
			switch kid.Name {
			case "channel":
				b.Kind = "channel list"
				b.Channels = append(b.Channels, channelFromNode(kid))
			case "codeTemplateLibrary":
				b.Kind = "code template libraries"
				b.Libraries = append(b.Libraries, libraryFromNode(kid))
			case "channelGroup":
				b.Kind = "channel groups"
				b.Groups = append(b.Groups, groupFromNode(kid))
			}
			if b.Version == "" && kid.Attr != nil {
				b.Version = kid.Attr["version"]
			}
		}
		if b.Kind == "" {
			return nil, fmt.Errorf("mirth: the <%s> holds no channels, groups or code template libraries", root.Name)
		}

	default:
		return nil, fmt.Errorf("mirth: <%s> is not a channel, a server backup, a channel group or a code template export",
			root.Name)
	}

	return b, nil
}

func groupFromNode(n *node) ChannelGroup {
	g := ChannelGroup{ID: n.str("id"), Name: n.str("name"), Description: n.str("description")}
	for _, cn := range n.child("channels").children("channel") {
		if id := cn.str("id"); id != "" {
			g.ChannelIDs = append(g.ChannelIDs, id)
		}
	}
	return g
}

func libraryFromNode(n *node) CodeTemplateLibrary {
	l := CodeTemplateLibrary{
		ID:                 n.str("id"),
		Name:               n.str("name"),
		Description:        n.str("description"),
		IncludeNewChannels: n.boolAt("includeNewChannels"),
	}
	for _, s := range n.child("enabledChannelIds").children("string") {
		l.EnabledChannelIDs = append(l.EnabledChannelIDs, s.Text)
	}
	for _, s := range n.child("disabledChannelIds").children("string") {
		l.DisabledChannelIDs = append(l.DisabledChannelIDs, s.Text)
	}
	for _, tn := range n.child("codeTemplates").children("codeTemplate") {
		t := CodeTemplate{
			ID:   tn.str("id"),
			Name: tn.str("name"),
			Type: tn.str("properties", "type"),
			Code: tn.str("properties", "code"),
		}
		// Older exports put the code directly on the template.
		if t.Code == "" {
			t.Code = tn.str("code")
		}
		if t.Type == "" {
			t.Type = tn.str("type")
		}
		for _, c := range tn.at("contextSet", "delegate").children("contextType") {
			t.Contexts = append(t.Contexts, c.Text)
		}
		l.Templates = append(l.Templates, t)
	}
	return l
}
