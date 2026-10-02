package translate

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/biodream-llc/perfuse/internal/mirth"
	"github.com/biodream-llc/perfuse/internal/sqldb"
)

// Transports and the YAML they become.
//
// Mirth identifies a connector by a display name and a Java properties class. The
// class is the reliable one: display names have changed between versions and can be
// localised, whereas the class is what the serialiser wrote.

// build produces the whole channel definition.
func (b *builder) build() string {
	var out strings.Builder

	name := sanitiseName(b.ch.Name)

	// The header explains where this came from. A migration is reviewed by
	// somebody who has to sign it off, and an unannotated wall of YAML cannot be
	// reviewed.
	fmt.Fprintf(&out, "# Translated from the Mirth channel %q\n", b.ch.Name)
	if b.ch.MirthVersion != "" {
		fmt.Fprintf(&out, "# Mirth version %s", b.ch.MirthVersion)
		if b.ch.Revision > 0 {
			fmt.Fprintf(&out, ", channel revision %d", b.ch.Revision)
		}
		fmt.Fprintln(&out)
	}
	fmt.Fprintln(&out, "#")
	fmt.Fprintln(&out, "# Read this before deploying it. Anything the translation was")
	fmt.Fprintln(&out, "# unsure about is marked with a REVIEW comment below.")
	fmt.Fprintln(&out)

	fmt.Fprintf(&out, "name: %s\n", yamlString(name))
	if desc := strings.TrimSpace(b.ch.Description); desc != "" {
		fmt.Fprintf(&out, "description: %s\n", yamlString(collapse(desc)))
	}
	if !b.ch.Enabled {
		// Carried across rather than quietly enabled. A channel that was off in
		// Mirth was off for a reason.
		fmt.Fprintln(&out, "enabled: false")
	}

	out.WriteString(b.buildSource())
	out.WriteString(b.buildTransformations())
	out.WriteString(b.buildScripts())
	out.WriteString(b.buildDestinations())

	return out.String()
}

// buildSource translates the inbound connector.
func (b *builder) buildSource() string {
	var out strings.Builder
	src := b.ch.Source

	fmt.Fprintln(&out, "\nsource:")

	if isDatabaseReader(src) {
		out.WriteString(b.buildDatabaseSource(src))
		out.WriteString(b.buildAck(src))
		if filter := b.buildFilter(src.Filter, "source"); filter != "" {
			fmt.Fprintf(&out, "\nfilter: %s\n", filter)
		}
		return out.String()
	}

	listen, ok := b.listenAddress(src)
	if !ok {
		// Everything else can be translated and reviewed; a source we cannot host is
		// the one thing that stops the channel existing at all.
		b.note("blocker", "source", fmt.Sprintf(
			"the source is a %s, which Perfuse does not implement. Only MLLP listeners "+
				"are supported today", describeTransport(src)),
			"Either point the sending system at an MLLP listener, or keep this channel "+
				"in Mirth until the connector exists here")
		fmt.Fprintln(&out, "  # REVIEW: the original source was a "+describeTransport(src))
		fmt.Fprintln(&out, "  #         Perfuse only listens for MLLP today.")
		fmt.Fprintln(&out, "  type: mllp")
		fmt.Fprintf(&out, "  listen: %s\n", yamlString("127.0.0.1:6661"))
	} else {
		fmt.Fprintln(&out, "  type: mllp")
		fmt.Fprintf(&out, "  listen: %s\n", yamlString(listen))
	}

	if v := src.Properties["receiveTimeout"]; v != "" {
		if d, ok := millisToDuration(v); ok {
			fmt.Fprintf(&out, "  idle_timeout: %s\n", d)
		}
	}
	if v := src.Properties["maxConnections"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			fmt.Fprintf(&out, "  max_connections: %d\n", n)
		}
	}
	if v := src.Properties["bufferSize"]; v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			fmt.Fprintf(&out, "  max_message_size: %d\n", n)
		}
	}

	out.WriteString(b.buildAck(src))

	if filter := b.buildFilter(src.Filter, "source"); filter != "" {
		fmt.Fprintf(&out, "\nfilter: %s\n", filter)
	}

	return out.String()
}

// buildAck translates the response behaviour.
//
// This is the setting most worth getting right and most easily got wrong. Mirth's
// source connector has a response setting that decides whether the sender is
// acknowledged before or after the destinations have run, and the two produce very
// different behaviour when a receiver is down.
func (b *builder) buildAck(src mirth.Connector) string {
	var out strings.Builder

	response := src.Properties["sourceConnectorProperties.responseVariable"]
	respondAfter := src.Properties["sourceConnectorProperties.respondAfterProcessing"]

	when := "on_delivery"
	switch {
	case respondAfter == "false":
		when = "on_receipt"
		b.note("warning", "source", fmt.Sprintf(
			"the original acknowledged the sender before processing "+
				"(respondAfterProcessing was false), so it answered AA even when delivery "+
				"failed. That is translated as ack.when: on_receipt"),
			"Consider on_delivery instead, so a sender is told the truth about whether "+
				"the message reached its destination. If you keep on_receipt, enable a "+
				"queue on the destinations so nothing is lost")
	case strings.Contains(strings.ToLower(response), "auto"), response == "":
		when = "on_delivery"
	default:
		// A named response variable means a script or a destination produced the
		// acknowledgement, which Perfuse does not do.
		b.note("warning", "source", fmt.Sprintf(
			"the original sent a custom acknowledgement from the response variable %q. "+
				"Perfuse generates the acknowledgement itself from the delivery outcome",
			response),
			"If the sending system depends on specific text in MSA-3, check what it "+
				"expects; the code (AA, AE, AR) will be correct but the text will differ")
	}

	fmt.Fprintln(&out, "  ack:")
	fmt.Fprintf(&out, "    when: %s\n", when)

	return out.String()
}

// listenAddress works out where a source should listen.
func (b *builder) listenAddress(src mirth.Connector) (string, bool) {
	if !isMLLPListener(src) {
		return "", false
	}

	host := src.Properties["listenerConnectorProperties.host"]
	port := src.Properties["listenerConnectorProperties.port"]

	if port == "" {
		b.note("warning", "source",
			"the original does not record a listen port",
			"Set source.listen before starting the channel")
		return "127.0.0.1:6661", true
	}

	switch host {
	case "", "0.0.0.0":
		// Mirth's default is every interface. Kept, because narrowing it would break
		// a working feed, but said out loud because it is a wider exposure than most
		// people intend.
		b.note("info", "source", fmt.Sprintf(
			"the original listened on every interface (port %s). That is preserved", port),
			"Consider binding to a specific address if only one network should reach it")
		return "0.0.0.0:" + port, true
	default:
		return host + ":" + port, true
	}
}

// buildDestinations translates the outbound connectors.
func (b *builder) buildDestinations() string {
	var out strings.Builder
	fmt.Fprintln(&out, "\ndestinations:")

	emitted := 0
	for i, d := range b.ch.Destinations {
		where := fmt.Sprintf("destination %d (%s)", i+1, d.Name)

		block, ok := b.buildDestination(d, where)
		if !ok {
			continue
		}
		out.WriteString(block)
		emitted++
	}

	if emitted == 0 {
		// A channel with no destination Perfuse can host still has a translated
		// source and filter, which is most of the work. Emitting a placeholder keeps
		// the file loadable so the rest can be reviewed.
		fmt.Fprintln(&out, "  # REVIEW: none of the original destinations could be translated.")
		fmt.Fprintln(&out, "  #         This placeholder writes to disk so the channel loads.")
		fmt.Fprintln(&out, "  - name: review-me")
		fmt.Fprintln(&out, "    type: file")
		fmt.Fprintf(&out, "    dir: %s\n", yamlString("./review-me"))
	}

	return out.String()
}

func (b *builder) buildDestination(d mirth.Connector, where string) (string, bool) {
	var out strings.Builder

	name := sanitiseName(d.Name)
	if name == "" {
		name = "destination"
	}

	switch {
	case isMLLPSender(d):
		host := d.Properties["remoteAddress"]
		port := d.Properties["remotePort"]
		if host == "" || port == "" {
			b.note("warning", where,
				"the destination is an MLLP sender with no recorded address",
				"Set the address before starting the channel")
			host, port = "127.0.0.1", "6661"
		}
		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: mllp")
		fmt.Fprintf(&out, "    address: %s\n", yamlString(host+":"+port))

	case isFileWriter(d):
		dir := d.Properties["host"]
		if dir == "" {
			dir = "./" + name
		}
		scheme := strings.ToLower(d.Properties["scheme"])

		if scheme == "sftp" {
			out.WriteString(b.buildSFTPDestination(d, name, where))
			break
		}
		if scheme == "ftp" || scheme == "ftps" {
			// Deliberately not translated to sftp. They are different protocols with
			// different security properties, and quietly upgrading one to the other would
			// produce a channel that cannot connect and a reason nobody would guess.
			b.note("blocker", where, fmt.Sprintf(
				"the destination writes over %s, and Perfuse implements sftp only. They "+
					"are different protocols, so this is not a matter of changing a name",
				scheme),
				"If the server also speaks SFTP, change this to an sftp destination. "+
					"Otherwise write to a local directory and have something else move the "+
					"files, or keep this destination in Mirth")
			fmt.Fprintf(&out, "  # REVIEW: the original wrote over %s to %s\n", scheme, dir)
			dir = "./" + name
		} else if scheme != "" && scheme != "file" {
			b.note("blocker", where, fmt.Sprintf(
				"the destination writes over %s, which Perfuse does not implement", scheme),
				"Write to a local directory and have something else move the files, or "+
					"keep this destination in Mirth")
			fmt.Fprintf(&out, "  # REVIEW: the original wrote over %s to %s\n", scheme, dir)
			dir = "./" + name
		}

		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: file")
		fmt.Fprintf(&out, "    dir: %s\n", yamlString(dir))

	case isHTTPSender(d):
		url := d.Properties["url"]
		if url == "" {
			b.note("warning", where,
				"the destination posts over HTTP but no URL was recorded",
				"Set http.url before starting the channel")
			url = "https://example.invalid/messages"
		}

		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: http")
		fmt.Fprintln(&out, "    http:")
		fmt.Fprintf(&out, "      url: %s\n", yamlString(url))

		if method := d.Properties["method"]; method != "" &&
			!strings.EqualFold(method, "post") {
			fmt.Fprintf(&out, "      method: %s\n", strings.ToUpper(method))
		}
		if ct := d.Properties["headers.Content-Type"]; ct != "" {
			fmt.Fprintf(&out, "      content_type: %s\n", yamlString(ct))
		}
		if user := d.Properties["username"]; user != "" {
			fmt.Fprintf(&out, "      username: %s\n", yamlString(user))
			// The password is not carried across. Mirth stores it in the export, and
			// writing it into a file destined for git would turn a migration into a
			// credential leak.
			fmt.Fprintln(&out, "      # REVIEW: set the password. It was deliberately not")
			fmt.Fprintln(&out, "      #         copied from the export, which would have put a")
			fmt.Fprintln(&out, "      #         credential into version control.")
			fmt.Fprintf(&out, "      password: %s\n", yamlString(""))
			b.note("warning", where,
				"the destination used HTTP basic authentication. The username was carried "+
					"across and the password deliberately was not",
				"Set http.password, ideally from an environment variable rather than a "+
					"literal in a file that goes into git")
		}

		if strings.HasPrefix(strings.ToLower(url), "http://") &&
			!strings.Contains(url, "127.0.0.1") && !strings.Contains(url, "localhost") {
			// The channel will refuse to load like this, which is the intended
			// behaviour, so say why rather than letting somebody discover it.
			b.note("blocker", where, fmt.Sprintf(
				"the destination posts to %s over plain HTTP, which Perfuse refuses for a "+
					"remote host because the message would cross the network unencrypted", url),
				"Change the URL to https, or terminate TLS in front of the receiver")
		}

		// Worth suggesting once: a great many Mirth HTTP senders are posting to
		// something that would be better served by the FHIR destination, which
		// converts as well as delivers.
		b.note("info", where,
			"if that endpoint is a FHIR server, a fhir destination would convert the "+
				"message as well as deliver it",
			"")

	case isDatabaseWriter(d):
		out.WriteString(b.buildDatabaseDestination(d, name, where))

	default:
		b.note("blocker", where, fmt.Sprintf(
			"the destination is a %s, which Perfuse does not implement", describeTransport(d)),
			"Keep this destination in Mirth until the connector exists here")
		fmt.Fprintf(&out, "  # REVIEW: the original was a %s. Not translated.\n",
			describeTransport(d))
		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: file")
		fmt.Fprintf(&out, "    dir: %s\n", yamlString("./"+name))
	}

	if !d.Enabled {
		fmt.Fprintln(&out, "    enabled: false")
	}

	if v := d.Properties["sendTimeout"]; v != "" {
		if dur, ok := millisToDuration(v); ok {
			fmt.Fprintf(&out, "    timeout: %s\n", dur)
		}
	}

	// Mirth's queueing is per destination and its settings map closely enough to
	// be worth carrying, because a destination that was queued in Mirth and is not
	// queued here loses messages during an outage that previously survived one.
	if queued := d.Properties["destinationConnectorProperties.queueEnabled"]; queued == "true" {
		fmt.Fprintln(&out, "    queue:")
		fmt.Fprintln(&out, "      enabled: true")
		if v := d.Properties["destinationConnectorProperties.retryCount"]; v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				fmt.Fprintf(&out, "      max_attempts: %d\n", n)
			}
		}
		if v := d.Properties["destinationConnectorProperties.retryIntervalMillis"]; v != "" {
			if dur, ok := millisToDuration(v); ok {
				fmt.Fprintf(&out, "      backoff: %s\n", dur)
			}
		}
		if threads := d.Properties["destinationConnectorProperties.threadCount"]; threads != "" {
			if n, err := strconv.Atoi(threads); err == nil && n > 1 {
				// This is worth being emphatic about. Concurrent queue threads are how
				// a Mirth backlog gets drained faster and how messages arrive out of
				// order, and the consequence is a discharge for a patient the receiver
				// never admitted.
				b.note("warning", where, fmt.Sprintf(
					"the original drained its queue with %s concurrent threads, which "+
						"delivers messages out of order", threads),
					"Perfuse always drains a queue in order, one message at a time. That "+
						"is slower during a backlog and it is correct: an A03 delivered "+
						"before its A01 tells the receiver about a discharge for a patient "+
						"it never admitted. No action needed unless you were relying on "+
						"the throughput")
			}
		}
	}

	if filter := b.buildFilter(d.Filter, where); filter != "" {
		fmt.Fprintf(&out, "    filter: %s\n", filter)
	}

	// A destination transformer has nowhere to go: Perfuse transforms once, before
	// fan-out. Saying so is important, because it changes behaviour.
	if len(d.Transformer.Steps) > 0 {
		b.note("warning", where, fmt.Sprintf(
			"the destination has its own transformer with %d step(s). Perfuse transforms "+
				"a message once, before it is sent to any destination, so per-destination "+
				"transformation has no equivalent", len(d.Transformer.Steps)),
			"If each destination needs different content, split this into one channel "+
				"per destination. If the steps happen to be the same for every "+
				"destination, move them to the channel transformations")
	}

	return out.String(), true
}

// --- transport identification ----------------------------------------------
//
// Matched on the properties class where possible. Display names have changed
// between Mirth versions and can be localised; the class is what the serialiser
// wrote and is far more reliable.

func classOrTransport(c mirth.Connector) string {
	if c.PropertiesClass != "" {
		return c.PropertiesClass
	}
	return c.Transport
}

func isMLLPListener(c mirth.Connector) bool {
	id := classOrTransport(c)
	return strings.Contains(id, "TcpReceiver") ||
		strings.Contains(id, "TcpDispatcherProperties") && c.Mode == mirth.ModeSource ||
		strings.EqualFold(c.Transport, "TCP Listener")
}

func isMLLPSender(c mirth.Connector) bool {
	id := classOrTransport(c)
	return strings.Contains(id, "TcpDispatcher") ||
		strings.EqualFold(c.Transport, "TCP Sender")
}

func isFileWriter(c mirth.Connector) bool {
	id := classOrTransport(c)
	return strings.Contains(id, "FileDispatcher") ||
		strings.EqualFold(c.Transport, "File Writer")
}

func isHTTPSender(c mirth.Connector) bool {
	id := classOrTransport(c)
	return strings.Contains(id, "HttpDispatcher") ||
		strings.EqualFold(c.Transport, "HTTP Sender")
}

func isDatabaseReader(c mirth.Connector) bool {
	id := classOrTransport(c)
	return strings.Contains(id, "DatabaseReceiver") ||
		strings.EqualFold(c.Transport, "Database Reader")
}

func isDatabaseWriter(c mirth.Connector) bool {
	id := classOrTransport(c)
	return strings.Contains(id, "DatabaseDispatcher") ||
		strings.EqualFold(c.Transport, "Database Writer")
}

// describeTransport names a connector for a human.
func describeTransport(c mirth.Connector) string {
	if c.Transport != "" {
		return c.Transport
	}
	if c.PropertiesClass != "" {
		// The bare class name is more use than the full package path.
		if i := strings.LastIndexByte(c.PropertiesClass, '.'); i >= 0 {
			return c.PropertiesClass[i+1:]
		}
		return c.PropertiesClass
	}
	return "connector of unknown type"
}

// --- helpers ---------------------------------------------------------------

func millisToDuration(v string) (string, bool) {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return "", false
	}
	switch {
	case n%60000 == 0:
		return fmt.Sprintf("%dm", n/60000), true
	case n%1000 == 0:
		return fmt.Sprintf("%ds", n/1000), true
	default:
		return fmt.Sprintf("%dms", n), true
	}
}

// collapse flattens whitespace so a multi-line Mirth description becomes one line.
func collapse(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// yamlString quotes a value when it needs quoting.
func yamlString(s string) string {
	if s == "" {
		return `""`
	}
	// Only the characters that actually change meaning where they appear. A hyphen
	// or a colon inside a scalar is fine; what matters is a leading indicator, or a
	// colon followed by a space. Quoting everything containing a hyphen would make
	// every generated file noisier than a hand-written one, and the whole point of
	// this output is that somebody reads it.
	needsQuote := strings.ContainsAny(s, "#{}[]&*!|>'\"%@`\n\t") ||
		strings.Contains(s, ": ") ||
		strings.HasSuffix(s, ":") ||
		strings.HasPrefix(s, " ") || strings.HasSuffix(s, " ") ||
		strings.IndexAny(s[:1], "-?:,") == 0 ||
		isYAMLKeyword(s)
	if !needsQuote {
		return s
	}
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`, "\t", `\t`).Replace(s) + `"`
}

// isYAMLKeyword reports whether a bare scalar would be read as something other
// than a string.
func isYAMLKeyword(s string) bool {
	switch strings.ToLower(s) {
	case "true", "false", "yes", "no", "on", "off", "null", "~":
		return true
	}
	// A value that parses as a number has to be quoted or a port-like string
	// becomes an integer.
	if s == "" {
		return true
	}
	digits := true
	for _, r := range s {
		if (r < '0' || r > '9') && r != '.' && r != '-' && r != '+' && r != 'e' && r != 'E' {
			digits = false
			break
		}
	}
	return digits
}

// buildDatabaseDestination translates a Mirth Database Writer.
//
// Mirth offers two modes and only one of them can be translated. A statement with
// Mirth velocity references in it maps onto a parameterised statement here. A
// destination set to run its own JavaScript against a JDBC connection cannot,
// because the script is arbitrary and reproducing it would mean guessing.
func (b *builder) buildDatabaseDestination(d mirth.Connector, name, where string) string {
	var out strings.Builder

	driver := mirthDriverToPerfuse(d.Properties["driver"])
	url := d.Properties["url"]
	user := d.Properties["username"]
	query := d.Properties["query"]
	useScript := strings.EqualFold(d.Properties["useScript"], "true")

	if useScript {
		// The one case that genuinely cannot be translated. The script opens its own
		// connection and does whatever it likes with it.
		b.note("blocker", where,
			"the destination runs a JavaScript block against its own JDBC connection "+
				"rather than a statement, so there is no statement to translate",
			"Rewrite it as a single INSERT or UPDATE with ${...} references, which "+
				"translates directly. If it needs several statements per message, a "+
				"stored procedure called from one statement is usually the shortest path")
		fmt.Fprintln(&out, "  # REVIEW: the original ran a script against a JDBC")
		fmt.Fprintln(&out, "  #         connection. There is no statement to translate.")
		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: file")
		fmt.Fprintf(&out, "    dir: %s\n", yamlString("./"+name))
		return out.String()
	}

	if driver == "" {
		b.note("blocker", where, fmt.Sprintf(
			"the destination writes to a database through JDBC driver %q, which has no "+
				"pure-Go equivalent in Perfuse", d.Properties["driver"]),
			"Perfuse speaks postgres, mysql, sqlserver and sqlite. If this is Oracle or "+
				"DB2, keep the destination in Mirth")
		fmt.Fprintln(&out, "  # REVIEW: the original used a JDBC driver Perfuse does not have.")
		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: file")
		fmt.Fprintf(&out, "    dir: %s\n", yamlString("./"+name))
		return out.String()
	}

	stmt, params, notes := convertVelocityStatement(query, driver)

	fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
	fmt.Fprintln(&out, "    type: database")
	fmt.Fprintln(&out, "    database:")
	fmt.Fprintf(&out, "      driver: %s\n", driver)

	dsn := jdbcToDSN(driver, url, user, envVarName(name))
	fmt.Fprintf(&out, "      dsn: %s\n", yamlString(dsn))

	// Never carried across. Mirth stores it in the export, and writing it into a file
	// destined for git would turn a migration into a credential leak.
	fmt.Fprintln(&out, "      # REVIEW: the password is referenced as an environment")
	fmt.Fprintln(&out, "      #         variable on purpose. It was NOT copied from the")
	fmt.Fprintln(&out, "      #         export, which would have put a credential in git.")

	if stmt != "" {
		fmt.Fprintf(&out, "      statement: %s\n", yamlString(stmt))
		if len(params) > 0 {
			fmt.Fprintln(&out, "      params:")
			for _, p := range params {
				fmt.Fprintf(&out, "        - %s\n", yamlString(p))
			}
		}
	} else {
		fmt.Fprintln(&out, "      # REVIEW: the original statement could not be converted.")
		fmt.Fprintf(&out, "      statement: %s\n", yamlString(""))
	}

	for _, n := range notes {
		b.note("warning", where, n, "Check the statement and the params before running it")
	}

	b.note("info", where,
		"the destination now binds values as parameters rather than building them "+
			"into the SQL, which is how the original worked. Besides the injection "+
			"risk, a concatenated statement breaks on a name containing an apostrophe",
		"")

	if d.Properties["password"] != "" {
		b.note("warning", where,
			"the original stored a database password in the channel export. It has "+
				"deliberately not been carried across",
			"Set the environment variable named in the DSN in the environment Perfuse "+
				"runs in, so the channel file can go into git without the credential")
	}

	return out.String()
}

// convertVelocityStatement turns Mirth's inline ${...} references into a
// parameterised statement plus an ordered list of paths.
//
// This is the whole value of translating this connector, and the reason it is worth
// doing rather than reporting. The original pasted values into the SQL; the result
// binds them. Every one of these statements in the wild is one apostrophe away from
// failing on a real patient.
func convertVelocityStatement(query, driver string) (string, []string, []string) {
	return convertVelocity(query, driver, false)
}

// convertVelocityColumns is the same, for a statement whose references are column
// names rather than message fields.
//
// A Database Reader's on-update statement runs against the row that was just read,
// so ${row_id} means the row_id column. Resolving it as a message field finds
// nothing, and the key column then has to be filled in by hand - which is what it
// was doing until a real export showed it.
func convertVelocityColumns(query, driver string) (string, []string, []string) {
	return convertVelocity(query, driver, true)
}

func convertVelocity(query, driver string, columns bool) (string, []string, []string) {
	if strings.TrimSpace(query) == "" {
		return "", nil, []string{"the original had no statement"}
	}

	var params []string
	var notes []string
	n := 0

	stmt := velocityRefPattern.ReplaceAllStringFunc(query, func(m string) string {
		inner := strings.TrimSpace(m[2 : len(m)-1])

		if columns {
			if columnRefPattern.MatchString(inner) {
				n++
				params = append(params, inner)
				return sqldb.Placeholder(driver, n)
			}
		}

		path, ok := velocityToPath(inner)
		if !ok {
			// Left as a marker rather than guessed. A wrong path binds a value to the
			// wrong column and writes data that looks entirely valid, which is far worse
			// than a statement that refuses to load.
			notes = append(notes, fmt.Sprintf(
				"the reference %s could not be resolved to a field path and has been left "+
					"as a placeholder", m))
			n++
			params = append(params, "REVIEW-"+inner)
			return sqldb.Placeholder(driver, n)
		}
		n++
		params = append(params, path)
		return sqldb.Placeholder(driver, n)
	})

	// Mirth statements are routinely written with the reference inside quotes,
	// because the value was being pasted in. Once it is a parameter the quotes would
	// make the placeholder a literal string.
	stmt = stripQuotesAroundPlaceholders(stmt, driver)

	return strings.TrimSpace(collapseWhitespace(stmt)), params, notes
}

// velocityRefPattern matches ${...} in a Mirth statement.
var velocityRefPattern = regexp.MustCompile(`\$\{[^}]*\}`)

// velocityToPath converts a Mirth message reference into an HL7 path.
func velocityToPath(inner string) (string, bool) {
	// The common forms are msg['PID']['PID.5']['PID.5.1'] and a plain PID.5.1.
	if m := bracketRefPattern.FindAllStringSubmatch(inner, -1); len(m) > 0 {
		last := m[len(m)-1][1]
		return dottedToPath(last)
	}
	if plainFieldPattern.MatchString(inner) {
		return dottedToPath(inner)
	}
	return "", false
}

var bracketRefPattern = regexp.MustCompile(`\['([^']+)'\]`)

// columnRefPattern matches a bare column name, for an on-update statement.
var columnRefPattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
var plainFieldPattern = regexp.MustCompile(`^[A-Z][A-Z0-9]{2}(\.\d+){1,3}$`)

// dottedToPath turns PID.5.1 into PID-5.1, which is how Perfuse addresses fields.
func dottedToPath(s string) (string, bool) {
	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return "", false
	}
	seg := parts[0]
	if len(seg) != 3 {
		return "", false
	}
	rest := strings.Join(parts[1:], ".")
	return seg + "-" + rest, true
}

// stripQuotesAroundPlaceholders removes quotes a Mirth author put around a value.
func stripQuotesAroundPlaceholders(stmt, driver string) string {
	for i := 1; i <= 64; i++ {
		ph := sqldb.Placeholder(driver, i)
		if !strings.Contains(stmt, ph) {
			if driver != "postgres" && driver != "sqlserver" {
				break
			}
			continue
		}
		stmt = strings.ReplaceAll(stmt, "'"+ph+"'", ph)
		stmt = strings.ReplaceAll(stmt, `"`+ph+`"`, ph)
	}
	// The ? dialect has no numbering, so handle it directly.
	stmt = strings.ReplaceAll(stmt, "'?'", "?")
	stmt = strings.ReplaceAll(stmt, `"?"`, "?")
	return stmt
}

func collapseWhitespace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// mirthDriverToPerfuse maps a JDBC driver class to a Perfuse driver name.
func mirthDriverToPerfuse(jdbc string) string {
	l := strings.ToLower(jdbc)
	switch {
	case strings.Contains(l, "postgres"):
		return "postgres"
	case strings.Contains(l, "mysql"), strings.Contains(l, "mariadb"):
		return "mysql"
	case strings.Contains(l, "sqlserver"), strings.Contains(l, "jtds"),
		strings.Contains(l, "mssql"):
		return "sqlserver"
	case strings.Contains(l, "sqlite"):
		return "sqlite"
	}
	// Oracle and DB2 land here, and there is no pure-Go driver for either that is
	// worth staking a clinical feed on.
	return ""
}

// jdbcToDSN converts a JDBC URL into the DSN shape the Go driver wants.
//
// Best effort, and marked as such. The host and database usually come across
// cleanly; the options rarely do.
func jdbcToDSN(driver, jdbcURL, user, envName string) string {
	if user == "" {
		user = "CHANGEME"
	}
	// A distinct variable per connector. A channel that reads one database and writes
	// another would otherwise reference the same ${DB_PASSWORD} twice, and whichever
	// value was set would be wrong for one of them - a failure that looks like a bad
	// password on a connection whose password is fine.
	if envName == "" {
		envName = "DB"
	}
	pwRef := "${" + envName + "_PASSWORD}"

	host, database := parseJDBCURL(jdbcURL)
	if host == "" {
		host = "CHANGEME:5432"
	}
	if database == "" {
		database = "CHANGEME"
	}

	switch driver {
	case "postgres":
		return fmt.Sprintf("postgres://%s:%s@%s/%s?sslmode=require", user, pwRef, host, database)
	case "mysql":
		return fmt.Sprintf("%s:%s@tcp(%s)/%s?tls=preferred", user, pwRef, host, database)
	case "sqlserver":
		return fmt.Sprintf("sqlserver://%s:%s@%s?database=%s", user, pwRef, host, database)
	case "sqlite":
		return "file:" + database
	}
	return ""
}

// parseJDBCURL pulls the host and database out of a jdbc: URL.
func parseJDBCURL(u string) (host, database string) {
	if u == "" {
		return "", ""
	}
	rest := strings.TrimPrefix(u, "jdbc:")

	// sqlserver uses jdbc:sqlserver://host:port;databaseName=x
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	} else if i := strings.Index(rest, ":"); i >= 0 {
		rest = rest[i+1:]
	}

	// databaseName= form.
	if i := strings.Index(strings.ToLower(rest), "databasename="); i >= 0 {
		database = rest[i+len("databasename="):]
		if j := strings.IndexAny(database, ";&?"); j >= 0 {
			database = database[:j]
		}
		host = rest[:i]
		host = strings.TrimRight(host, ";")
		return host, database
	}

	// host:port/database form.
	if i := strings.Index(rest, "/"); i >= 0 {
		host = rest[:i]
		database = rest[i+1:]
		if j := strings.IndexAny(database, ";&?"); j >= 0 {
			database = database[:j]
		}
		return host, database
	}
	return rest, ""
}

// buildDatabaseSource translates a Mirth Database Reader.
//
// This connector is why Perfuse has a quarantine at all. A Database Reader that
// meets a row it cannot process retries it forever, and the channel then looks
// completely healthy while the feed has stopped. It is the single commonest way a
// Mirth channel stalls, and translating the connector without translating that
// behaviour would be reproducing the bug.
func (b *builder) buildDatabaseSource(src mirth.Connector) string {
	var out strings.Builder

	driver := mirthDriverToPerfuse(src.Properties["driver"])
	if driver == "" {
		b.note("blocker", "source", fmt.Sprintf(
			"the source polls a database through JDBC driver %q, which has no pure-Go "+
				"equivalent in Perfuse", src.Properties["driver"]),
			"Perfuse speaks postgres, mysql, sqlserver and sqlite. If this is Oracle or "+
				"DB2, keep the channel in Mirth")
		fmt.Fprintln(&out, "  # REVIEW: the original polled a database through a JDBC")
		fmt.Fprintln(&out, "  #         driver Perfuse does not have.")
		fmt.Fprintln(&out, "  type: mllp")
		fmt.Fprintf(&out, "  listen: %s\n", yamlString("127.0.0.1:6661"))
		return out.String()
	}

	if strings.EqualFold(src.Properties["useScript"], "true") {
		b.note("blocker", "source",
			"the source runs a JavaScript block to fetch rows rather than a query, so "+
				"there is no query to translate",
			"Rewrite it as a SELECT. If it needs logic, a view or a stored procedure "+
				"called from a SELECT translates directly")
		fmt.Fprintln(&out, "  # REVIEW: the original fetched rows with a script.")
		fmt.Fprintln(&out, "  type: mllp")
		fmt.Fprintf(&out, "  listen: %s\n", yamlString("127.0.0.1:6661"))
		return out.String()
	}

	fmt.Fprintln(&out, "  type: database")
	fmt.Fprintln(&out, "  database:")
	fmt.Fprintf(&out, "    driver: %s\n", driver)
	fmt.Fprintf(&out, "    dsn: %s\n",
		yamlString(jdbcToDSN(driver, src.Properties["url"],
			src.Properties["username"], envVarName("source"))))
	fmt.Fprintln(&out, "    # REVIEW: the password is an environment variable on purpose.")
	fmt.Fprintln(&out, "    #         It was NOT copied from the export.")

	query := collapseWhitespace(src.Properties["select"])
	if query == "" {
		query = "SELECT * FROM CHANGEME"
		b.note("blocker", "source", "the source had no SELECT recorded in the export",
			"Set database.query before starting the channel")
	}
	fmt.Fprintf(&out, "    query: %s\n", yamlString(query))

	if !strings.Contains(strings.ToUpper(query), "ORDER BY") {
		// Order is meaning in a clinical feed. Mirth does not require this either, which
		// is why so many of these queries do not have it.
		b.note("warning", "source",
			"the polling query has no ORDER BY, so rows may arrive in whatever order "+
				"the database finds convenient. For clinical events order is meaning: an "+
				"admission has to precede its discharge",
			"Add ORDER BY on the insert time or the key")
	}

	// Mirth calls this the on-update statement, and it is the thing that stops a row
	// being read twice.
	update := collapseWhitespace(src.Properties["update"])
	if update != "" {
		// Column references, not message fields: this statement runs against the row
		// that was just read, so ${row_id} means the row_id column.
		stmt, params, _ := convertVelocityColumns(update, driver)
		fmt.Fprintf(&out, "    after_query: %s\n", yamlString(stmt))

		key := "CHANGEME"
		if len(params) == 1 && !strings.HasPrefix(params[0], "REVIEW-") {
			// The single bound value in an on-update statement is the row key, near
			// enough always. Reported as a deduction rather than asserted silently.
			key = params[0]
			b.note("info", "source", fmt.Sprintf(
				"the on-update statement bound %s, so that has been taken as the key "+
					"column", key),
				"Check key_column names the column that identifies a row")
		} else if len(params) > 1 {
			b.note("warning", "source", fmt.Sprintf(
				"the on-update statement binds %d values, and Perfuse binds exactly one: "+
					"the key of the row that was processed", len(params)),
				"Rewrite after_query so it identifies the row by its key alone, and set "+
					"key_column to that column")
		}
		fmt.Fprintf(&out, "    key_column: %s\n", yamlString(key))
	} else {
		fmt.Fprintln(&out, "    # REVIEW: the original had no on-update statement, so")
		fmt.Fprintln(&out, "    #         the query must exclude rows already sent.")
		fmt.Fprintf(&out, "    key_column: %s\n", yamlString("CHANGEME"))
		b.note("warning", "source",
			"the original had no on-update statement, so nothing marked a row as read. "+
				"Perfuse will remember keys itself, which works while the process runs",
			"Set key_column to the column that identifies a row, and preferably add an "+
				"after_query so the record survives a restart")
	}

	if v := src.Properties["pollingFrequency"]; v != "" {
		if d, ok := millisToDuration(v); ok {
			fmt.Fprintf(&out, "    poll_interval: %s\n", d)
		}
	}

	fmt.Fprintln(&out, "    # REVIEW: template builds a message from the row. Perfuse")
	fmt.Fprintln(&out, "    #         escapes every value, so a column containing a")
	fmt.Fprintln(&out, "    #         pipe cannot invent fields.")
	fmt.Fprintln(&out, "    template: |")
	fmt.Fprintln(&out, "      MSH|^~\\&|CHANGEME|CHANGEME|PERFUSE|CHANGEME|${CHANGEME_TIMESTAMP}||ADT^A08^ADT_A01|${CHANGEME_ID}|P|2.5.1")
	fmt.Fprintln(&out, "      PID|1||${CHANGEME_MRN}^^^CHANGEME^MR||${CHANGEME_SURNAME}^${CHANGEME_FORENAME}")

	// The behaviour difference that matters most, and the reason this is worth
	// migrating rather than leaving alone.
	b.note("info", "source",
		"Perfuse gives a row that cannot be processed a bounded number of attempts "+
			"and then quarantines it and moves on, where the original would have "+
			"retried it forever. That is the difference between one abandoned row and "+
			"a stalled feed, and it is the commonest way a Database Reader channel "+
			"stops without appearing to",
		"Watch the quarantine count; a row landing there needs a person")

	b.note("blocker", "source",
		"the message template could not be derived from the export, because Mirth "+
			"built the message in a transformer rather than in the connector",
		"Fill in database.template using ${column_name} for each column the query "+
			"returns. The transformer steps have been translated separately, so check "+
			"whether they are still needed once the template does the mapping")

	return out.String()
}

// envVarName turns a connector name into an environment variable name for a
// database credential.
func envVarName(name string) string {
	return envVarNameFor(name, "DB")
}

// envVarNameFor turns a connector name into an environment variable name.
//
// The suffix distinguishes what kind of credential it is, because a channel can have
// a database password and an SFTP password and they are not the same secret.
func envVarNameFor(name, suffix string) string {
	var b strings.Builder
	for _, r := range strings.ToUpper(name) {
		switch {
		case r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	out := strings.Trim(b.String(), "_")
	// Repeated underscores come from punctuation runs and read badly in an
	// environment variable name.
	for strings.Contains(out, "__") {
		out = strings.ReplaceAll(out, "__", "_")
	}
	if out == "" {
		return suffix
	}
	// A connector called "Registry DB" would otherwise become REGISTRY_DB_DB.
	if strings.HasSuffix(out, "_"+suffix) || out == suffix {
		return out
	}
	return out + "_" + suffix
}

// buildSFTPDestination translates a Mirth File Writer whose scheme is sftp.
func (b *builder) buildSFTPDestination(d mirth.Connector, name, where string) string {
	var out strings.Builder

	host := d.Properties["host"]
	dir := "."
	// Mirth puts host and path together in one field, which is convenient for it and
	// has to be pulled apart here.
	if i := strings.IndexByte(host, '/'); i >= 0 {
		dir = host[i:]
		host = host[:i]
	}
	if host == "" {
		host = "CHANGEME"
	}

	user := d.Properties["username"]
	if user == "" {
		user = "CHANGEME"
	}

	fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
	fmt.Fprintln(&out, "    type: sftp")
	fmt.Fprintln(&out, "    sftp:")
	fmt.Fprintf(&out, "      host: %s\n", yamlString(host))
	fmt.Fprintf(&out, "      user: %s\n", yamlString(user))

	if key := d.Properties["privateKey"]; key != "" {
		fmt.Fprintf(&out, "      key_file: %s\n", yamlString(key))
	} else {
		fmt.Fprintf(&out, "      password: %s\n",
			yamlString("${"+envVarNameFor(name, "SFTP")+"_PASSWORD}"))
		fmt.Fprintln(&out, "      # REVIEW: the password is an environment variable on")
		fmt.Fprintln(&out, "      #         purpose. It was NOT copied from the export.")
	}

	fmt.Fprintf(&out, "      dir: %s\n", yamlString(dir))

	if tmpl := d.Properties["outputPattern"]; tmpl != "" {
		fmt.Fprintf(&out, "      # REVIEW: the original named files %s\n", tmpl)
		fmt.Fprintln(&out, "      #         Perfuse understands ${timestamp}, ${date},")
		fmt.Fprintln(&out, "      #         ${control_id}, ${message_type} and ${channel}.")
	}

	if strings.EqualFold(d.Properties["outputAppend"], "true") {
		// Appending needs framing here, because several messages in one file cannot
		// otherwise be split again: MSH can appear inside a free-text field.
		fmt.Fprintln(&out, "      append: true")
		fmt.Fprintln(&out, "      framed: true")
		b.note("info", where,
			"the original appended messages to one file. Perfuse does the same and also "+
				"frames them, because several messages in one file cannot be separated "+
				"again reliably otherwise: MSH can appear inside a free-text field",
			"If the receiving side cannot cope with framing, turn append off and write "+
				"one file per message instead")
	}

	// The whole security of this connector, and the thing the original almost
	// certainly was not doing.
	fmt.Fprintln(&out, "      # REVIEW: known_hosts_file is required. Get the key with")
	fmt.Fprintln(&out, "      #         ssh-keyscan -H "+host+" >> known_hosts")
	fmt.Fprintf(&out, "      known_hosts_file: %s\n", yamlString("/etc/perfuse/known_hosts"))

	b.note("blocker", where,
		"Perfuse requires known_hosts_file on an SFTP connector, and Mirth did not "+
			"record a host key because it does not verify one by default. Without it "+
			"there is nothing to distinguish the real server from anything answering on "+
			"that address, and the credentials go over before anybody notices",
		"Run \"ssh-keyscan -H "+host+" >> /etc/perfuse/known_hosts\", check the key "+
			"against what the other side says it should be, and point known_hosts_file "+
			"at that file. If you genuinely cannot, set insecure_skip_host_key_check "+
			"and read the warning it prints at every start")

	if d.Properties["password"] != "" {
		b.note("warning", where,
			"the original stored an SFTP password in the channel export. It has "+
				"deliberately not been carried across",
			"Set the environment variable named in the config, or better, switch to a "+
				"key_file")
	}

	return out.String()
}
