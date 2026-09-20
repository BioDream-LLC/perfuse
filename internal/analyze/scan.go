package analyze

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/biodream-llc/perfuse/internal/mirth"
)

// Script scanning looks for the constructs that actually stop a Mirth script
// from running anywhere else. The big one is JVM interop: Mirth's JavaScript is
// Rhino embedded in a Java process, so scripts reach straight into Java and
// into Mirth's own helper classes. None of that exists outside the JVM, and it
// is invisible until you try to move.

type scriptPattern struct {
	// match is the literal text to look for. Kept as a substring rather than a
	// parsed expression because Rhino accepts things a JS parser will not.
	match    string
	severity Severity
	code     string
	what     string
	why      string
}

var scriptPatterns = []scriptPattern{
	{
		match: "importPackage(", severity: Blocker, code: "JAVA_INTEROP",
		what: "Imports a Java package",
		why:  "The script runs inside a JVM and uses Java classes directly. There is no equivalent outside the JVM; this logic has to be rewritten.",
	},
	{
		match: "importClass(", severity: Blocker, code: "JAVA_INTEROP",
		what: "Imports a Java class",
		why:  "The script depends on the JVM. This logic has to be rewritten.",
	},
	{
		match: "Packages.", severity: Blocker, code: "JAVA_INTEROP",
		what: "Reaches into the JVM through Packages.",
		why:  "Rhino's Java bridge. Not portable outside a JVM.",
	},
	{
		match: "java.", severity: Blocker, code: "JAVA_INTEROP",
		what: "Calls a Java class directly",
		why:  "Scripts commonly use java.util.Date, java.text.SimpleDateFormat and similar. These need JavaScript or Go equivalents.",
	},
	{
		match: "com.mirth.connect.", severity: Blocker, code: "MIRTH_INTERNALS",
		what: "Calls Mirth's internal Java classes",
		why:  "This ties the channel to a specific Mirth build. There is no substitute; the behaviour has to be reimplemented deliberately.",
	},
	{
		match: "DatabaseConnectionFactory", severity: Blocker, code: "SCRIPT_DATABASE",
		what: "Opens a database connection from inside a script",
		why:  "The connection details are in the script rather than in connector configuration, so this dependency is invisible to anyone reading the channel setup.",
	},
	{
		match: "SerializerFactory", severity: Warning, code: "MIRTH_UTIL",
		what: "Uses SerializerFactory to convert between message formats",
		why:  "A Mirth-supplied Java helper. Perfuse needs an equivalent conversion for the datatypes involved.",
	},
	{
		match: "AttachmentUtil", severity: Warning, code: "MIRTH_UTIL",
		what: "Manipulates attachments through AttachmentUtil", why: "",
	},
	{
		match: "FileUtil", severity: Warning, code: "MIRTH_UTIL",
		what: "Reads or writes files through FileUtil",
		why:  "Touches the server filesystem from inside message processing.",
	},
	{
		match: "DateUtil", severity: Warning, code: "MIRTH_UTIL",
		what: "Uses DateUtil for date parsing or formatting", why: "",
	},
	{
		match: "ChannelUtil", severity: Warning, code: "MIRTH_UTIL",
		what: "Starts, stops or queries other channels through ChannelUtil",
		why:  "The channel controls other channels at runtime, so it cannot be migrated on its own.",
	},
	{
		match: "router.routeMessage", severity: Warning, code: "CHANNEL_CHAINING",
		what: "Sends the message to another channel from script",
		why:  "This is a dependency on another channel that does not appear in the connector configuration. Migrate the pair together.",
	},
	{
		match: "globalMap", severity: Warning, code: "GLOBAL_MAP",
		what: "Reads or writes the global map",
		why:  "The global map is shared mutable state across every channel on the server. Behaviour depends on what other channels do, which makes this channel not self-contained.",
	},
	{
		match: "$g(", severity: Warning, code: "GLOBAL_MAP",
		what: "Reads the global map through $g()",
		why:  "Shared mutable state across every channel on the server.",
	},
	{
		match: "configurationMap", severity: Warning, code: "CONFIGURATION_MAP",
		what: "Reads the server configuration map",
		why:  "These values live in Mirth server configuration, not in the channel export. Collect them before migrating.",
	},
	{
		match: "$cfg(", severity: Warning, code: "CONFIGURATION_MAP",
		what: "Reads the server configuration map through $cfg()",
		why:  "These values are not in the channel export.",
	},
	{
		match: "globalChannelMap", severity: Note, code: "GLOBAL_CHANNEL_MAP",
		what: "Uses the global channel map",
		why:  "State that persists between messages on this channel. Check whether anything depends on it surviving a restart.",
	},
	{
		match: "responseMap", severity: Note, code: "RESPONSE_MAP",
		what: "Reads or writes the response map", why: "",
	},
}

var (
	lineComment  = regexp.MustCompile(`(?m)//.*$`)
	blockComment = regexp.MustCompile(`(?s)/\*.*?\*/`)
	// trivialScript matches the boilerplate Mirth puts in an empty script slot.
	trivialScript = regexp.MustCompile(`^(return\s*(message|true|;)?\s*;?)?$`)
)

// stripComments removes comments so a pattern in commented-out code is not
// reported as a live dependency.
func stripComments(s string) string {
	s = blockComment.ReplaceAllString(s, " ")
	s = lineComment.ReplaceAllString(s, " ")
	return s
}

// scriptIsSubstantive reports whether a script does anything at all. Mirth
// prefills script slots with a bare return, and reporting those as findings
// would bury the real ones.
func scriptIsSubstantive(s string) bool {
	body := strings.TrimSpace(stripComments(s))
	body = strings.Join(strings.Fields(body), " ")
	return body != "" && !trivialScript.MatchString(body)
}

// scanScript reports every distinct construct found in one script. Each code is
// reported once per location, so a script using java.util.Date five times
// produces one finding rather than five.
func (r *Report) scanScript(body, where string) {
	if !scriptIsSubstantive(body) {
		return
	}
	clean := stripComments(body)

	seen := map[string]bool{}
	for _, p := range scriptPatterns {
		if !strings.Contains(clean, p.match) || seen[p.code] {
			continue
		}
		seen[p.code] = true
		r.add(p.severity, p.code, where, p.what, p.why)
	}
}

// transportKey identifies a connector by its Java properties class where
// available, falling back to the display name.
func transportKey(c mirth.Connector) string {
	if c.PropertiesClass != "" {
		if i := strings.LastIndex(c.PropertiesClass, "."); i >= 0 {
			return c.PropertiesClass[i+1:]
		}
		return c.PropertiesClass
	}
	return c.Transport
}

type transportInfo struct {
	blocker bool
	why     string
}

// transportSupport records which connectors Perfuse can carry. Anything absent
// is reported as unrecognised rather than assumed to work.
var transportSupport = map[string]transportInfo{
	// Supported.
	"TcpReceiverProperties":    {},
	"TcpDispatcherProperties":  {},
	"HttpReceiverProperties":   {},
	"HttpDispatcherProperties": {},
	"FileReceiverProperties":   {},
	"FileDispatcherProperties": {},
	"VmReceiverProperties":     {},
	"VmDispatcherProperties":   {},

	// Not implemented.
	"DatabaseReceiverProperties": {blocker: true,
		why: "Polls a database for work. Needs a database connector and a driver decision."},
	"DatabaseDispatcherProperties": {blocker: true,
		why: "Writes to a database. Needs a database connector and a driver decision."},
	"JmsReceiverProperties":   {blocker: true, why: "Needs a JMS client."},
	"JmsDispatcherProperties": {blocker: true, why: "Needs a JMS client."},
	"SmtpDispatcherProperties": {blocker: true,
		why: "Sends email. Straightforward to add, but not present."},
	"WebServiceSenderProperties": {blocker: true,
		why: "SOAP. Needs WSDL handling."},
	"WebServiceReceiverProperties": {blocker: true,
		why: "SOAP endpoint. Needs WSDL handling."},
	"DicomReceiverProperties": {blocker: true,
		why: "DICOM is a different protocol stack entirely."},
	"DicomDispatcherProperties": {blocker: true,
		why: "DICOM is a different protocol stack entirely."},
	"JavaScriptReceiverProperties": {blocker: true,
		why: "The source is an arbitrary script rather than a transport, so what it connects to is only discoverable by reading the code."},
	"JavaScriptDispatcherProperties": {blocker: true,
		why: "The destination is an arbitrary script rather than a transport."},
	"DocumentDispatcherProperties": {blocker: true,
		why: "Renders PDF or RTF. Needs a document renderer."},
}

// describeTransport builds a one-line description of where a connector actually
// listens or sends, pulled from whichever properties that transport uses.
func describeTransport(c mirth.Connector) string {
	name := c.Transport
	if name == "" {
		name = transportKey(c)
	}
	if d := endpointOf(c); d != "" {
		return fmt.Sprintf("%s (%s)", name, d)
	}
	return name
}

func endpointOf(c mirth.Connector) string {
	p := c.Properties
	get := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := p[k]; ok && v != "" {
				return v
			}
		}
		return ""
	}

	switch transportKey(c) {
	case "TcpReceiverProperties":
		host := get("listenerConnectorProperties.host", "host")
		port := get("listenerConnectorProperties.port", "port")
		if port != "" {
			return fmt.Sprintf("listening on %s:%s", nameOr(host, "0.0.0.0"), port)
		}
	case "TcpDispatcherProperties":
		host := get("remoteAddress", "host")
		port := get("remotePort", "port")
		if host != "" || port != "" {
			return fmt.Sprintf("sending to %s:%s", host, port)
		}
	case "HttpReceiverProperties":
		if path := get("contextPath", "listenerConnectorProperties.contextPath"); path != "" {
			return "path " + path
		}
	case "HttpDispatcherProperties":
		if u := get("url"); u != "" {
			return u
		}
	case "FileReceiverProperties":
		dir := get("host")
		filter := get("fileFilter")
		if dir != "" && filter != "" {
			return fmt.Sprintf("reading %s matching %s", dir, filter)
		}
		if dir != "" {
			return "reading " + dir
		}
	case "FileDispatcherProperties":
		dir := get("host")
		pattern := get("outputPattern")
		if dir != "" && pattern != "" {
			return fmt.Sprintf("writing %s/%s", strings.TrimRight(dir, "/"), pattern)
		}
		if dir != "" {
			return "writing " + dir
		}
	case "VmDispatcherProperties":
		if ch := get("channelId"); ch != "" {
			return "to channel " + ch
		}
	case "DatabaseReceiverProperties", "DatabaseDispatcherProperties":
		if u := get("url"); u != "" {
			return u
		}
	}
	return ""
}
