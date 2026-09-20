// Package analyze turns a parsed Mirth channel into something a human can act
// on: a plain description of what the channel does, and an honest list of what
// will not survive a migration.
//
// The design rule here is that a finding must be specific enough to act on.
// "This channel uses JavaScript" is not a finding. "Destination 1 calls
// java.util.Date, which has no equivalent outside the JVM" is.
package analyze

import (
	"fmt"
	"sort"

	"github.com/biodream-llc/perfuse/internal/mirth"
)

// Severity is how much a finding should worry you.
type Severity string

const (
	// Blocker means the channel cannot be translated without a decision from
	// a human or new code in Perfuse.
	Blocker Severity = "BLOCKER"
	// Warning means it can be translated but the behaviour needs checking.
	Warning Severity = "WARNING"
	// Note is informational, usually operational rather than functional.
	Note Severity = "NOTE"
)

func (s Severity) rank() int {
	switch s {
	case Blocker:
		return 0
	case Warning:
		return 1
	default:
		return 2
	}
}

// Finding is one specific thing worth knowing about a channel.
type Finding struct {
	Severity Severity
	// Code is a stable identifier so findings can be suppressed or counted
	// without matching on prose.
	Code string
	// Where locates the finding, e.g. "source" or "destination 1 (Registry API)".
	Where string
	// What is the problem, in one line.
	What string
	// Why explains the consequence. Empty when the What says it all.
	Why string
}

// Report is the full analysis of one channel.
type Report struct {
	Channel  *mirth.Channel
	Findings []Finding
}

// Counts returns the number of findings at each severity.
func (r *Report) Counts() (blockers, warnings, notes int) {
	for _, f := range r.Findings {
		switch f.Severity {
		case Blocker:
			blockers++
		case Warning:
			warnings++
		default:
			notes++
		}
	}
	return
}

// Translatable reports whether the channel has no blocking findings.
func (r *Report) Translatable() bool {
	b, _, _ := r.Counts()
	return b == 0
}

// Channel analyses a single parsed channel.
func Channel(c *mirth.Channel) *Report {
	r := &Report{Channel: c}

	r.checkStorage()
	r.checkChannelScripts()
	r.checkResources()
	r.checkUnrecognised()

	r.checkConnector(c.Source, "source")
	for i, d := range c.Destinations {
		r.checkConnector(d, fmt.Sprintf("destination %d (%s)", d.MetaDataID, nameOr(d.Name, "unnamed")))
		_ = i
	}

	r.checkOrdering()

	sort.SliceStable(r.Findings, func(i, j int) bool {
		return r.Findings[i].Severity.rank() < r.Findings[j].Severity.rank()
	})
	return r
}

func (r *Report) add(sev Severity, code, where, what, why string) {
	r.Findings = append(r.Findings, Finding{
		Severity: sev, Code: code, Where: where, What: what, Why: why,
	})
}

// checkStorage covers the settings behind Mirth's most common production
// failure, which is the message tables filling the disk.
func (r *Report) checkStorage() {
	p := r.Channel.Properties

	switch p.MessageStorageMode {
	case "DEVELOPMENT":
		r.add(Warning, "STORAGE_DEVELOPMENT", "channel",
			"Message storage mode is DEVELOPMENT",
			"Every message and every intermediate transformation is retained. This is the heaviest setting and is not meant for production.")
	case "PRODUCTION", "RAW", "METADATA", "DISABLED", "":
		// Nothing to say; these are deliberate choices.
	default:
		r.add(Note, "STORAGE_UNKNOWN", "channel",
			fmt.Sprintf("Unrecognised message storage mode %q", p.MessageStorageMode), "")
	}

	if p.MessageStorageMode != "DISABLED" {
		if p.PruneMetaDataDays == 0 && p.PruneContentDays == 0 {
			r.add(Warning, "NO_PRUNING", "channel",
				"No pruning configured",
				"Stored messages are never removed, so the message tables grow without limit. This is the usual cause of an interface engine running a server out of disk.")
		} else if p.PruneContentDays > p.PruneMetaDataDays && p.PruneMetaDataDays > 0 {
			r.add(Note, "PRUNE_INCONSISTENT", "channel",
				fmt.Sprintf("Content is kept longer (%dd) than metadata (%dd)", p.PruneContentDays, p.PruneMetaDataDays),
				"Metadata is removed first, so content beyond the metadata window becomes unreachable through search.")
		}
	}

	if p.EncryptData {
		r.add(Note, "ENCRYPTED_STORAGE", "channel",
			"Stored message content is encrypted",
			"Migration needs the original key material, or the history is unreadable.")
	}
	if p.StoreAttachments {
		r.add(Note, "ATTACHMENTS", "channel",
			"Attachment storage is on",
			"Attachments are held separately from the message and have to be migrated with it.")
	}
	if p.ClearGlobalChannelMap {
		r.add(Note, "CLEARS_GLOBAL_MAP", "channel",
			"Clears the global channel map on deploy", "")
	}
}

func (r *Report) checkChannelScripts() {
	c := r.Channel
	scripts := []struct {
		name, body string
	}{
		{"preprocessor", c.PreprocessingScript},
		{"postprocessor", c.PostprocessingScript},
		{"deploy script", c.DeployScript},
		{"undeploy script", c.UndeployScript},
	}
	for _, s := range scripts {
		if !scriptIsSubstantive(s.body) {
			continue
		}
		r.add(Warning, "CHANNEL_SCRIPT", "channel",
			fmt.Sprintf("The %s contains logic", s.name),
			"Channel-level scripts run outside any connector and are easy to miss when reproducing behaviour.")
		r.scanScript(s.body, "channel "+s.name)
	}
}

func (r *Report) checkResources() {
	for _, id := range r.Channel.Properties.ResourceIDs {
		if id == "Default Resource" {
			continue
		}
		r.add(Warning, "CUSTOM_RESOURCE", "channel",
			fmt.Sprintf("Depends on the custom resource %q", id),
			"Custom resources are libraries loaded from the Mirth server's filesystem. They are not in the channel export, so this channel is not self-contained.")
	}
}

func (r *Report) checkUnrecognised() {
	for _, name := range r.Channel.Unrecognised {
		r.add(Note, "UNKNOWN_ELEMENT", "channel",
			fmt.Sprintf("Unrecognised channel element <%s>", name),
			"Perfuse read the rest of the channel but does not know what this is.")
	}
}

func (r *Report) checkConnector(conn mirth.Connector, where string) {
	if conn.Transport == "" && conn.PropertiesClass == "" {
		return
	}

	if !conn.Enabled && conn.Mode == mirth.ModeDestination {
		r.add(Note, "DISABLED_DESTINATION", where,
			"Destination is disabled",
			"It is part of the channel but not running. Confirm whether it should be migrated at all.")
	}

	if k, ok := transportSupport[transportKey(conn)]; ok && k.blocker {
		r.add(Blocker, "TRANSPORT_UNSUPPORTED", where,
			fmt.Sprintf("%s is not implemented in Perfuse", describeTransport(conn)), k.why)
	}

	for _, s := range conn.Transformer.Steps {
		r.checkStep(s, where)
	}
	for _, rule := range conn.Filter.Rules {
		r.checkRule(rule, where)
	}
	for _, name := range conn.Unrecognised {
		r.add(Note, "UNKNOWN_ELEMENT", where,
			fmt.Sprintf("Unrecognised connector element <%s>", name), "")
	}
}

func (r *Report) checkStep(s mirth.Step, where string) {
	loc := fmt.Sprintf("%s, step %d (%s)", where, s.Sequence, nameOr(s.Name, "unnamed"))

	switch s.Kind {
	case mirth.StepUnknown:
		r.add(Blocker, "UNKNOWN_STEP_PLUGIN", loc,
			fmt.Sprintf("Unknown step plugin %s", s.RawKind),
			"This is a third-party or newer Mirth plugin. Its behaviour is not described anywhere in the export, so it has to be reimplemented by hand.")
	case mirth.StepXSLT:
		r.add(Blocker, "XSLT_STEP", loc,
			"XSLT transformation step",
			"Perfuse has no XSLT engine. The stylesheet has to be rewritten, or XSLT support added.")
	case mirth.StepExternalScript:
		r.add(Blocker, "EXTERNAL_SCRIPT", loc,
			fmt.Sprintf("Runs an external script from the server filesystem (%s)", nameOr(s.Script, "path not recorded")),
			"The script is not in the export. Migration needs the file from the Mirth server.")
	case mirth.StepIterator:
		r.add(Warning, "ITERATOR_STEP", loc,
			"Iterator step",
			"Iterators repeat nested steps over a repeating segment or field. The nesting and index behaviour needs checking after translation.")
	case mirth.StepJavaScript, mirth.StepMessageBuilder:
		if scriptIsSubstantive(s.Script) {
			r.scanScript(s.Script, loc)
		}
	}

	for _, child := range s.Children {
		r.checkStep(child, loc)
	}
}

func (r *Report) checkRule(rule mirth.Rule, where string) {
	loc := fmt.Sprintf("%s, filter rule %d (%s)", where, rule.Sequence, nameOr(rule.Name, "unnamed"))

	switch rule.Kind {
	case mirth.RuleUnknown:
		r.add(Blocker, "UNKNOWN_RULE_PLUGIN", loc,
			fmt.Sprintf("Unknown filter rule plugin %s", rule.RawKind), "")
	case mirth.RuleExternalScript:
		r.add(Blocker, "EXTERNAL_SCRIPT", loc,
			"Filter runs an external script from the server filesystem", "")
	case mirth.RuleJavaScript:
		r.scanScript(rule.Script, loc)
	case mirth.RuleIterator:
		r.add(Warning, "ITERATOR_RULE", loc, "Iterator filter rule", "")
	}

	for _, child := range rule.Children {
		r.checkRule(child, loc)
	}
}

// checkOrdering flags the queueing choice that changes clinical behaviour.
func (r *Report) checkOrdering() {
	var unordered []string
	for _, d := range r.Channel.Destinations {
		if d.Enabled && !d.WaitForPrevious {
			unordered = append(unordered, nameOr(d.Name, fmt.Sprintf("destination %d", d.MetaDataID)))
		}
	}
	if len(unordered) > 1 {
		r.add(Note, "UNORDERED_DESTINATIONS", "channel",
			fmt.Sprintf("%d destinations do not wait for the previous one", len(unordered)),
			"They run concurrently. For ADT that is usually fine; where one destination depends on another having succeeded, it is not.")
	}
}

func nameOr(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}
