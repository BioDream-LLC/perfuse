// Java analysis for imported Mirth channels.
//
// # Why this exists
//
// Mirth runs Rhino on the JVM, so a Mirth script can call any Java on the classpath, including Mirth's own server
// classes. Perfuse runs goja and has no Java runtime. It refuses those calls with an error naming the class - but that
// refusal happens when the script runs, which for a source connector means when the first real message arrives.
//
// So a migration used to go: the import succeeds silently, the channel deploys, and the first message off the wire
// fails. A migration that looks clean and breaks on first traffic is worse than one that refuses at the door, because
// the person who did it has already told their colleagues it worked.
//
// This finds those calls at import time and says what to do about each one.
//
// # Why four verdicts and not two
//
// "Uses Java" is one bit of information and the wrong one, because the Java a real Mirth channel uses falls into
// groups with completely different amounts of work attached. Roughly half of what sites actually write is
// SimpleDateFormat and HashMap, which JavaScript does natively. Some of it is Mirth's documented script API, which
// Perfuse already implements under the same names. A little of it wants to start and stop channels, which Perfuse can
// do over its REST API but does not expose to scripts. And some of it is a vendor jar that is never going to run here.
//
// Reporting those as one number tells a migrator nothing about whether their afternoon or their quarter is at stake.
package mirth

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Verdict says what can be done about one Java reference.
type Verdict string

const (
	// VerdictSupported means Perfuse provides this already, under this name or a named alternative.
	VerdictSupported Verdict = "supported"

	// VerdictRewritable means it is ordinary Java that JavaScript does natively.
	//
	// Distinguished from VerdictSupported because somebody has to edit the script. It is minutes of work per site
	// rather than none, and a migrator planning a cutover needs to know which.
	VerdictRewritable Verdict = "rewritable"

	// VerdictNeedsFeature means Perfuse can do this but not from a script.
	//
	// Kept separate from VerdictOutOfScope, which it would otherwise be lumped into, because the distinction is the
	// difference between "this is impossible" and "this is a REST call away". Channel lifecycle is the case that
	// matters: POST /api/channels/{name}/start exists and does exactly what ChannelUtil.startChannel does.
	VerdictNeedsFeature Verdict = "needs-feature"

	// VerdictOutOfScope means it needs a JVM or Mirth's own server, and will not work here.
	//
	// Stated plainly rather than softened. A site with one of these has a decision to make, and the useful thing is
	// for them to make it now rather than discover it during a cutover.
	VerdictOutOfScope Verdict = "out-of-scope"
)

// JavaUse is one Java reference found in a channel's scripts.
type JavaUse struct {
	// Reference is what the script wrote, near enough to find with a search.
	Reference string

	// Where names the script, in the words the Mirth administrator interface uses.
	Where string

	// Line is the line within that script, counting from one.
	Line int

	// Verdict says what can be done.
	Verdict Verdict

	// Advice says what to do, naming the replacement where there is one.
	Advice string
}

// JavaReport is everything found in one channel.
type JavaReport struct {
	Uses []JavaUse

	// Counts by verdict, so a caller does not have to tally them to decide what to show.
	Supported    int
	Rewritable   int
	NeedsFeature int
	OutOfScope   int

	// ExternalScripts holds paths from ExternalScript steps.
	//
	// Separate from Uses because the script is not in the export - only a path on the old server's filesystem - so
	// nothing can be said about its contents. Reporting it as "no Java found" would be a lie by omission, and this is
	// the one finding where the honest answer is that the file has to be fetched and looked at.
	ExternalScripts []string
}

// Blocked reports whether anything found will not work after migration.
func (r *JavaReport) Blocked() bool { return r.NeedsFeature > 0 || r.OutOfScope > 0 }

// Summary is one line for a migrator, naming counts rather than a proportion.
//
// Counts because "83% translatable" is not a decision. Seventeen calls to a vendor jar is.
func (r *JavaReport) Summary() string {
	if len(r.Uses) == 0 && len(r.ExternalScripts) == 0 {
		return "No Java found in this channel's scripts."
	}

	parts := []string{}
	if r.Supported > 0 {
		parts = append(parts, fmt.Sprintf("%d already supported", r.Supported))
	}
	if r.Rewritable > 0 {
		parts = append(parts, fmt.Sprintf("%d rewritable in JavaScript", r.Rewritable))
	}
	if r.NeedsFeature > 0 {
		parts = append(parts, fmt.Sprintf("%d needing a Perfuse feature that is not exposed to scripts", r.NeedsFeature))
	}
	if r.OutOfScope > 0 {
		parts = append(parts, fmt.Sprintf("%d that will not run without a JVM", r.OutOfScope))
	}

	s := fmt.Sprintf("%d Java references: %s.", len(r.Uses), strings.Join(parts, ", "))
	if n := len(r.ExternalScripts); n > 0 {
		s += fmt.Sprintf(" %d external script file(s) are referenced by path and are not in the export, so their contents could not be checked.", n)
	}
	return s
}

// javaRule matches one recognisable Java reference and says what it means.
type javaRule struct {
	pattern *regexp.Regexp
	verdict Verdict
	advice  string
}

// The order matters: the first rule to match a line wins for that reference, so the specific rules come before the
// general ones. ChannelUtil.getChannelName must be recognised before the ChannelUtil catch-all, or a call Perfuse
// already implements would be reported as a blocker.
var javaRules = []javaRule{
	// Mirth's script API that Perfuse implements under the same name.
	{
		regexp.MustCompile(`\bChannelUtil\.(getChannelName|getChannelId)\b`),
		VerdictSupported,
		"Perfuse implements this on ChannelUtil under the same name. No change needed.",
	},
	{
		regexp.MustCompile(`\b(DatabaseConnectionFactory|SerializerFactory|UUIDGenerator|DateUtil|FileUtil)\.`),
		VerdictSupported,
		"Perfuse implements this global under the same name. FileUtil needs the file permission and a declared file root; DatabaseConnectionFactory needs the database permission.",
	},
	{
		regexp.MustCompile(`\brouter\.(routeMessage|routeMessageByChannelId)\b`),
		VerdictSupported,
		"Perfuse implements router.routeMessage under the same name. It needs the route permission on the channel.",
	},

	// Channel administration: possible, but not from a script.
	{
		regexp.MustCompile(`\bChannelUtil\.(start|stop|pause|resume|halt|undeploy|deploy)Channel\b`),
		VerdictNeedsFeature,
		"Perfuse can start and stop channels over its API (POST /api/channels/{name}/start and /stop) but does not expose that to scripts, because a script author would then be able to reach administration. Move this to an external caller with a token, or raise it as a feature request.",
	},
	{
		regexp.MustCompile(`\bChannelUtil\.(getChannelState|isChannelStarted|isChannelStopped|isChannelPaused|isChannelDeployed|getDeployedChannelIds|getChannelIds)\b`),
		VerdictNeedsFeature,
		"Channel state is available from Perfuse over its API (GET /api/channels) but not to scripts. If this script only logs or branches on state, the alert rules may already cover what it was written for.",
	},

	// Ordinary Java that JavaScript does natively.
	{
		regexp.MustCompile(`\b(?:java\.text\.)?SimpleDateFormat\b`),
		VerdictRewritable,
		"Use DateUtil.formatDate(pattern, date) and DateUtil.getDate(pattern, text), which Perfuse provides with Mirth's pattern syntax.",
	},
	{
		regexp.MustCompile(`\bjava\.util\.(HashMap|LinkedHashMap|TreeMap|Map)\b`),
		VerdictRewritable,
		"Use a JavaScript object literal. Note that Mirth's maps are ordered by insertion for LinkedHashMap only; a plain object is close enough for string keys.",
	},
	{
		regexp.MustCompile(`\bjava\.util\.(ArrayList|LinkedList|List|Vector|HashSet|Set)\b`),
		VerdictRewritable,
		"Use a JavaScript array. For a Set, an object used as a key table, or an array with indexOf.",
	},
	{
		regexp.MustCompile(`\bjava\.util\.UUID\b`),
		VerdictRewritable,
		"Use UUIDGenerator.getUUID(), which Perfuse provides.",
	},
	{
		regexp.MustCompile(`\bjava\.util\.(Date|Calendar|GregorianCalendar)\b`),
		VerdictRewritable,
		"Use the JavaScript Date, or DateUtil for formatting and parsing with a Mirth pattern.",
	},
	{
		regexp.MustCompile(`\bjava\.lang\.(String|Integer|Long|Double|Float|Boolean|Math|StringBuilder|StringBuffer)\b`),
		VerdictRewritable,
		"Use the JavaScript equivalent: String, Number, parseInt, parseFloat, Math, or string concatenation.",
	},
	{
		regexp.MustCompile(`\bjava\.io\.(File|FileReader|FileWriter|BufferedReader)\b`),
		VerdictRewritable,
		"Use FileUtil, which Perfuse provides. It needs the file permission and is confined to the file roots the channel declares.",
	},
	{
		regexp.MustCompile(`\b(?:Packages\.)?org\.apache\.commons\.(lang3?|codec|io)\b`),
		VerdictRewritable,
		"Apache Commons string, encoding and IO helpers almost always have a one-line JavaScript equivalent. Check each call: trim, isEmpty, join and base64 are all native.",
	},

	// Mirth's own server. Not Java the language - Mirth's implementation.
	{
		regexp.MustCompile(`\b(?:Packages\.)?com\.mirth\.connect\.(server|donkey|model|client)\b`),
		VerdictOutOfScope,
		"This reaches Mirth's own server classes, which are Mirth's implementation rather than part of Java, and are proprietary as of Mirth 4.6. They cannot be loaded into Perfuse, and would expect a running Mirth server and its database if they were. This script needs rewriting against Perfuse's API.",
	},
	{
		regexp.MustCompile(`\b(?:Packages\.)?com\.mirth\.connect\.(?:server\.)?userutil\.(\w+)\b`),
		VerdictOutOfScope,
		"This is Mirth's script API reached by its full class name. Perfuse provides several of these as globals under the same short name - ChannelUtil, DateUtil, FileUtil, DatabaseConnectionFactory, SerializerFactory, UUIDGenerator - so try the short name. Anything not in that list is not implemented.",
	},

	// Anything else Java-shaped.
	{
		regexp.MustCompile(`\b(?:Packages\.)?(?:java|javax)\.[a-z][\w.]*\.[A-Z]\w+`),
		VerdictOutOfScope,
		"Perfuse has no Java runtime, so this class is not available and no equivalent has been identified. Rewrite it in JavaScript, or move the work into a channel step.",
	},
	{
		regexp.MustCompile(`\bPackages\.[\w.]+\.[A-Z]\w+`),
		VerdictOutOfScope,
		"This is a Java class from the old server's classpath, most likely a vendor or site-specific jar. Perfuse has no Java runtime and cannot load it. The logic has to be reimplemented, or done outside the engine.",
	},
	{
		regexp.MustCompile(`\bimportPackage\s*\(|\bimportClass\s*\(`),
		VerdictOutOfScope,
		"Rhino's importPackage and importClass exist only to bring Java names into scope. Perfuse has no Java runtime, so the import and whatever it was for both need rewriting.",
	},
}

// ScanJava analyses every script in a channel.
func (c *Channel) ScanJava() *JavaReport {
	r := &JavaReport{}

	// Channel-level scripts. Named as the Mirth interface names them, so somebody can find the tab.
	for _, s := range []struct{ where, src string }{
		{"channel preprocessor", c.PreprocessingScript},
		{"channel postprocessor", c.PostprocessingScript},
		{"channel deploy script", c.DeployScript},
		{"channel undeploy script", c.UndeployScript},
	} {
		r.scan(s.where, s.src)
	}

	for i, conn := range c.AllConnectors() {
		label := "source"
		if i > 0 {
			// Destinations are numbered from one in the interface, and named, so both go in.
			label = fmt.Sprintf("destination %d", i)
			if conn.Name != "" {
				label = fmt.Sprintf("destination %d (%s)", i, conn.Name)
			}
		}
		for j, s := range conn.Transformer.Steps {
			r.scanStep(fmt.Sprintf("%s transformer step %d", label, j+1), s)
		}
		for j, rule := range conn.Filter.Rules {
			r.scanRule(fmt.Sprintf("%s filter rule %d", label, j+1), rule)
		}
	}

	sort.SliceStable(r.Uses, func(i, j int) bool {
		if r.Uses[i].Where != r.Uses[j].Where {
			return r.Uses[i].Where < r.Uses[j].Where
		}
		return r.Uses[i].Line < r.Uses[j].Line
	})
	return r
}

func (r *JavaReport) scanStep(where string, s Step) {
	switch s.Kind {
	case StepJavaScript:
		r.scan(where, s.Script)
	case StepExternalScript:
		if s.Script != "" {
			r.ExternalScripts = append(r.ExternalScripts, fmt.Sprintf("%s: %s", where, s.Script))
		}
	}
	for i, c := range s.Children {
		r.scanStep(fmt.Sprintf("%s, nested step %d", where, i+1), c)
	}
}

func (r *JavaReport) scanRule(where string, rule Rule) {
	switch rule.Kind {
	case RuleJavaScript:
		r.scan(where, rule.Script)
	case RuleExternalScript:
		if rule.Script != "" {
			r.ExternalScripts = append(r.ExternalScripts, fmt.Sprintf("%s: %s", where, rule.Script))
		}
	}
	for i, c := range rule.Children {
		r.scanRule(fmt.Sprintf("%s, nested rule %d", where, i+1), c)
	}
}

// scan finds Java references in one script body.
func (r *JavaReport) scan(where, src string) {
	if strings.TrimSpace(src) == "" {
		return
	}

	// Comments and string literals are blanked first.
	//
	// Without this, a script carrying "// used to use SimpleDateFormat here" reports a finding that is not there, and
	// a migrator who checks one false positive stops trusting the rest of the report. Blanked rather than removed so
	// that line numbers still match what the author sees in Mirth.
	lines := strings.Split(stripNonCode(src), "\n")

	for i, line := range lines {
		for _, rule := range javaRules {
			m := rule.pattern.FindString(line)
			if m == "" {
				continue
			}
			r.add(JavaUse{
				Reference: strings.TrimSpace(m),
				Where:     where,
				Line:      i + 1,
				Verdict:   rule.verdict,
				Advice:    rule.advice,
			})
			// One finding per line. A line reaching for two Java classes is rare, and the second is almost always the
			// same verdict as the first, so reporting both mostly inflates the count a migrator is trying to read.
			break
		}
	}
}

func (r *JavaReport) add(u JavaUse) {
	r.Uses = append(r.Uses, u)
	switch u.Verdict {
	case VerdictSupported:
		r.Supported++
	case VerdictRewritable:
		r.Rewritable++
	case VerdictNeedsFeature:
		r.NeedsFeature++
	case VerdictOutOfScope:
		r.OutOfScope++
	}
}

// stripNonCode blanks comments and string literals, preserving line structure.
//
// Deliberately a scanner and not a JavaScript parser. What is being looked for is a set of identifier shapes, and a
// parser would buy correctness on constructs - a class name assembled at runtime, a name reached through a variable -
// that no amount of parsing would resolve anyway. The report says so rather than implying completeness.
func stripNonCode(src string) string {
	out := make([]byte, 0, len(src))

	const (
		code = iota
		lineComment
		blockComment
		single
		double
	)
	state := code

	for i := 0; i < len(src); i++ {
		c := src[i]
		next := byte(0)
		if i+1 < len(src) {
			next = src[i+1]
		}

		switch state {
		case code:
			switch {
			case c == '/' && next == '/':
				state = lineComment
				out = append(out, ' ', ' ')
				i++
			case c == '/' && next == '*':
				state = blockComment
				out = append(out, ' ', ' ')
				i++
			case c == '\'':
				state = single
				out = append(out, ' ')
			case c == '"':
				state = double
				out = append(out, ' ')
			default:
				out = append(out, c)
			}

		case lineComment:
			if c == '\n' {
				state = code
				out = append(out, c)
			} else {
				out = append(out, ' ')
			}

		case blockComment:
			// Newlines are kept so that a reference after a multi-line comment still reports the right line.
			if c == '*' && next == '/' {
				state = code
				out = append(out, ' ', ' ')
				i++
			} else if c == '\n' {
				out = append(out, c)
			} else {
				out = append(out, ' ')
			}

		case single, double:
			quote := byte('\'')
			if state == double {
				quote = '"'
			}
			switch {
			case c == '\\':
				// Skip the escaped character so that a backslash before the closing quote does not end the string early.
				out = append(out, ' ')
				if next != 0 {
					out = append(out, ' ')
					i++
				}
			case c == quote:
				state = code
				out = append(out, ' ')
			case c == '\n':
				// An unterminated string. Mirth would not have run this, but the scan should not swallow the rest of
				// the file because of it.
				state = code
				out = append(out, c)
			default:
				out = append(out, ' ')
			}
		}
	}
	return string(out)
}
