package translate

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/mirth"
)

// Carrying scripts over.
//
// This is the part that makes the whole translator worth having, and it is the part
// that does the least. Perfuse runs Mirth's JavaScript unchanged, including E4X, so
// a script does not need converting — it needs moving, in order, with a comment
// saying which step it came from.
//
// The temptation is to rewrite recognisable scripts into declarative steps. That is
// resisted for anything beyond the trivial, because a rewrite that is subtly
// different is worse than a script that is honestly a script: the first loses data
// quietly and the second is merely something to read.

// buildScripts assembles the scripts block.
func (b *builder) buildScripts() string {
	if len(b.scriptSteps) == 0 && len(b.scriptFilter) == 0 && !b.hasChannelScripts() {
		return ""
	}

	var out strings.Builder
	fmt.Fprintln(&out, "\nscripts:")

	if len(b.scriptFilter) > 0 {
		fmt.Fprintln(&out, "  filter: |")
		for _, rule := range b.scriptFilter {
			fmt.Fprintf(&out, "    // From filter rule %d: %s\n",
				rule.Sequence, oneLine(rule.Name))
			writeIndented(&out, rule.Script, "    ")
			fmt.Fprintln(&out)
		}
	}

	// The preprocessor has its own stage now, so it goes where it belongs rather than being smuggled
	// into the start of the transformer. Perfuse runs it before parsing, which is if anything earlier
	// than Mirth's before-the-filter, so a script that repaired an unparseable message still works -
	// and that is the case people actually use it for.
	if pre := strings.TrimSpace(b.ch.PreprocessingScript); pre != "" {
		fmt.Fprintln(&out, "  preprocessor: |")
		writeIndented(&out, pre, "    ")
		fmt.Fprintln(&out)

		b.note("info", "channel",
			"the channel's preprocessor was carried over to Perfuse's preprocessor stage, which runs "+
				"before the message is parsed",
			"")
	}

	if post := strings.TrimSpace(b.ch.PostprocessingScript); post != "" {
		fmt.Fprintln(&out, "  postprocessor: |")
		writeIndented(&out, post, "    ")
		fmt.Fprintln(&out)

		b.note("info", "channel",
			"the channel's postprocessor was carried over to Perfuse's postprocessor stage, which runs "+
				"after the outcome is known",
			"")
	}

	b.buildLifecycleScripts(&out)

	if b.hasTransformerScripts() {
		fmt.Fprintln(&out, "  transformer: |")

		for _, step := range b.scriptSteps {
			fmt.Fprintf(&out, "    // From step %d: %s\n", step.Sequence, oneLine(step.Name))

			script := step.Script
			if step.Kind == mirth.StepMapper {
				// A mapper carried over as a script is an assignment, which the export
				// stores as the two halves rather than as code.
				script = fmt.Sprintf("%s = %s;", step.Variable, strings.TrimSuffix(
					strings.TrimSpace(step.Mapping), ";"))
			}
			writeIndented(&out, script, "    ")
			fmt.Fprintln(&out)
		}

	}

	// Permissions are granted only where the scripts appear to need them, because
	// the default is denial and a channel that silently had file access would defeat
	// the point of having the setting.
	if allow := b.neededPermissions(); len(allow) > 0 {
		fmt.Fprintf(&out, "  allow: [%s]\n", strings.Join(allow, ", "))
		b.note("info", "channel", fmt.Sprintf(
			"the scripts appear to use %s, so those permissions have been granted",
			strings.Join(allow, " and ")),
			"Remove anything from scripts.allow that is not actually needed. Everything "+
				"not listed is denied")

		// File access has to name the directories it may use, so a placeholder is written and flagged as a blocker.
		//
		// The channel does load with the placeholder in place - a root that does not exist yet is accepted, so that
		// deployment order does not matter - but every FileUtil call will be refused until it is replaced, naming
		// the placeholder in the error. That is the honest tradeoff: guessing a directory would be worse, and
		// emitting an unconfined grant is what let a script read this program's own database of password hashes.
		//
		// Mirth grants file access to every script with no confinement at all, so any channel reaching this line
		// has been running that way. The migration is the right moment to decide what it should actually reach.
		for _, name := range allow {
			if name != "file" {
				continue
			}
			fmt.Fprintf(&out, "  file_roots:\n")
			fmt.Fprintf(&out, "    - /replace/with/the/directory/these/scripts/may/use\n")
			b.note("blocker", "channel",
				"the scripts read or write files, and Perfuse requires the directories to be named",
				"Replace the placeholder in scripts.file_roots with the directories these scripts "+
					"actually use. The channel will start, but every file read or write will be "+
					"refused until you do. Mirth granted file access to the whole filesystem, so "+
					"this is the moment to decide what it should reach")
		}
	}

	return out.String()
}

// hasChannelScripts reports whether the channel needs a scripts block at all.
//
// Deploy and undeploy count now that they have somewhere real to go. Leaving them out meant a channel
// whose only script was a deploy script produced no scripts block, and the script was lost - which is
// exactly the silent-drop this translator exists to avoid.
func (b *builder) hasChannelScripts() bool {
	return strings.TrimSpace(b.ch.PreprocessingScript) != "" ||
		strings.TrimSpace(b.ch.PostprocessingScript) != "" ||
		strings.TrimSpace(b.ch.DeployScript) != "" ||
		strings.TrimSpace(b.ch.UndeployScript) != ""
}

// hasTransformerScripts reports whether anything belongs in the transformer specifically.
//
// Separate from hasChannelScripts because the preprocessor and postprocessor now have their own stages.
// Without this distinction a channel with only a preprocessor emitted an empty "transformer: |" block,
// which is invalid YAML for a scalar and would refuse to load.
func (b *builder) hasTransformerScripts() bool {
	return len(b.scriptSteps) > 0
}

// neededPermissions inspects the carried scripts for the things Perfuse denies by
// default.
//
// Deliberately crude: it looks for the names, and over-granting a permission a
// script does not use is a smaller problem than a channel that fails at three in
// the morning because a permission was missing. The note tells the reader to trim
// it.
func (b *builder) neededPermissions() []string {
	var all strings.Builder
	for _, s := range b.scriptSteps {
		all.WriteString(s.Script)
		all.WriteString(s.Mapping)
	}
	for _, r := range b.scriptFilter {
		all.WriteString(r.Script)
	}
	all.WriteString(b.ch.PreprocessingScript)

	text := all.String()
	var out []string

	if strings.Contains(text, "FileUtil.read") || strings.Contains(text, "FileUtil.write") {
		out = append(out, "file")
	}
	if strings.Contains(text, "router.routeMessage") {
		out = append(out, "route")
	}
	if strings.Contains(text, "DatabaseConnectionFactory") {
		// Not granted, because there is nothing to grant: it is refused outright.
		b.note("blocker", "channel",
			"a script opens a database connection with DatabaseConnectionFactory, which "+
				"Perfuse does not implement",
			"Move the lookup out of the channel. A database call inside a transformer is "+
				"also the most common cause of a Mirth channel stalling under load, so this "+
				"is worth removing rather than reproducing")
	}

	return out
}

// buildLifecycleScripts emits the deploy and undeploy scripts, which now have a home.
//
// They used to be reported as having nowhere to go, which was true when the note was written and stopped
// being true when Perfuse grew the two stages. A translator that undersells what it can carry is a
// specific kind of harmful: it tells somebody a migration is harder than it is, and they believe it.
func (b *builder) buildLifecycleScripts(out *strings.Builder) {
	if deploy := strings.TrimSpace(b.ch.DeployScript); deploy != "" {
		fmt.Fprintln(out, "  deploy: |")
		writeIndented(out, deploy, "    ")
		fmt.Fprintln(out)

		b.note("info", "channel",
			"the channel's deploy script was carried over to Perfuse's deploy stage, which runs once "+
				"before the channel starts listening",
			"One difference worth knowing: if it throws, the channel does not start. In Mirth it would "+
				"have started anyway")
	}

	if undeploy := strings.TrimSpace(b.ch.UndeployScript); undeploy != "" {
		fmt.Fprintln(out, "  undeploy: |")
		writeIndented(out, undeploy, "    ")
		fmt.Fprintln(out)

		b.note("info", "channel",
			"the channel's undeploy script was carried over to Perfuse's undeploy stage, which runs "+
				"once after the channel stops",
			"")
	}
}

// storageNotes reports channel properties that change behaviour.
func (b *builder) storageNotes() {
	switch strings.ToUpper(b.ch.Properties.MessageStorageMode) {
	case "", "DEVELOPMENT", "PRODUCTION":
		// Full storage, which is what Perfuse does by default.
	case "RAW":
		b.note("info", "channel",
			"the original stored only raw messages. Perfuse stores the raw bytes and the "+
				"outcome by default, and -store-payloads=false turns the payload off",
			"")
	case "METADATA":
		b.note("warning", "channel",
			"the original stored metadata only, with no message content. Perfuse stores "+
				"payloads by default, which will use more disk and hold clinical data at rest",
			"Start the server with -store-payloads=false to match the original behaviour")
	case "DISABLED":
		b.note("warning", "channel",
			"the original stored nothing at all. Perfuse stores messages by default",
			"Start the server with -store-messages=false to match, or accept the "+
				"storage and set a retention period")
	}

	if b.ch.Properties.EncryptData {
		b.note("warning", "channel",
			"the original encrypted stored message content. Perfuse does not encrypt at "+
				"rest",
			"Use filesystem or volume encryption, or turn payload storage off with "+
				"-store-payloads=false")
	}

	if days := b.ch.Properties.PruneContentDays; days > 0 {
		b.note("info", "channel", fmt.Sprintf(
			"the original pruned message content after %d days", days),
			fmt.Sprintf("Start the server with -retention-days=%d to match", days))
	}
}

// oneLine flattens a name for a comment.
func oneLine(s string) string {
	s = collapse(s)
	if s == "" {
		return "(unnamed)"
	}
	return s
}

// writeIndented writes a script under a YAML block scalar.
//
// Trailing whitespace is stripped per line and tabs are left alone. A blank line
// is written empty rather than indented, because trailing spaces in a block scalar
// are preserved by YAML and would show up as invisible noise in every diff.
func writeIndented(w *strings.Builder, script, indent string) {
	for line := range strings.SplitSeq(strings.ReplaceAll(script, "\r\n", "\n"), "\n") {
		line = strings.TrimRight(line, " \t")
		if line == "" {
			fmt.Fprintln(w)
			continue
		}
		fmt.Fprintf(w, "%s%s\n", indent, line)
	}
}
