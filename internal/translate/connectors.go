package translate

import (
	"fmt"
	"strings"

	"github.com/biodream-llc/perfuse/internal/mirth"
)

// The connectors beyond MLLP and the database.
//
// Found by having Mirth 4.5.2, the Open Integration Engine and BridgeLink each export a corpus of ordinary channels: the translator blocked
// an HTTP Listener and a File Reader as "only MLLP listeners are supported" although Perfuse has had http and file sources for a long
// time, and it read an HTTP Sender's URL from a field none of the three engines writes. A migration tool that refuses what the target
// can do is as misleading as one that accepts what it cannot.

// listenOn joins a listener's host and port, defaulting the way Mirth does: an empty host or 0.0.0.0 means every interface.
func listenOn(c mirth.Connector, fallbackPort string) string {
	host := c.Properties["listenerConnectorProperties.host"]
	port := c.Properties["listenerConnectorProperties.port"]
	if port == "" {
		port = fallbackPort
	}
	if host == "" {
		host = "0.0.0.0"
	}
	return host + ":" + port
}

func isHTTPListener(c mirth.Connector) bool {
	return strings.Contains(classOrTransport(c), "HttpReceiver") || strings.EqualFold(c.Transport, "HTTP Listener")
}

func isFileReader(c mirth.Connector) bool {
	return strings.Contains(classOrTransport(c), "FileReceiver") || strings.EqualFold(c.Transport, "File Reader")
}

func isJavaScriptReader(c mirth.Connector) bool {
	return strings.Contains(classOrTransport(c), "JavaScriptReceiver") || strings.EqualFold(c.Transport, "JavaScript Reader")
}

func isDICOMListener(c mirth.Connector) bool {
	return strings.Contains(classOrTransport(c), "DICOMReceiver") || strings.EqualFold(c.Transport, "DICOM Listener")
}

func isWebServiceListener(c mirth.Connector) bool {
	return strings.Contains(classOrTransport(c), "WebServiceReceiver") || strings.EqualFold(c.Transport, "Web Service Listener")
}

func isChannelReader(c mirth.Connector) bool {
	return strings.Contains(classOrTransport(c), "VmReceiver") || strings.EqualFold(c.Transport, "Channel Reader")
}

func isSMTPSender(c mirth.Connector) bool {
	return strings.Contains(classOrTransport(c), "SmtpDispatcher") || strings.EqualFold(c.Transport, "SMTP Sender")
}

func isJavaScriptWriter(c mirth.Connector) bool {
	return strings.Contains(classOrTransport(c), "JavaScriptDispatcher") || strings.EqualFold(c.Transport, "JavaScript Writer")
}

func isDICOMSender(c mirth.Connector) bool {
	return strings.Contains(classOrTransport(c), "DICOMDispatcher") || strings.EqualFold(c.Transport, "DICOM Sender")
}

func isWebServiceSender(c mirth.Connector) bool {
	return strings.Contains(classOrTransport(c), "WebServiceDispatcher") || strings.EqualFold(c.Transport, "Web Service Sender")
}

func isChannelWriter(c mirth.Connector) bool {
	return strings.Contains(classOrTransport(c), "VmDispatcher") || strings.EqualFold(c.Transport, "Channel Writer")
}

// isMLLPFraming says whether a TCP connector frames with MLLP, which is Mirth's default and nearly every HL7 feed. Anything else is a raw
// socket, and treating it as MLLP produces a listener that never sees a message end.
func isMLLPFraming(c mirth.Connector) bool {
	mode := c.Properties["transmissionModeProperties.pluginPointName"]
	return mode == "" || strings.EqualFold(mode, "MLLP")
}

// tcpFraming translates Mirth's transmission mode, or reports that it cannot.
func (b *builder) tcpFraming(c mirth.Connector, where, indent string) string {
	mode := c.Properties["transmissionModeProperties.pluginPointName"]
	start := c.Properties["transmissionModeProperties.startOfMessageBytes"]
	end := c.Properties["transmissionModeProperties.endOfMessageBytes"]

	var out strings.Builder
	switch {
	case strings.EqualFold(mode, "Basic") && start == "" && end != "":
		fmt.Fprintf(&out, "%sframing: delimited\n", indent)
		fmt.Fprintf(&out, "%sdelimiter: %s\n", indent, yamlString("0x"+end))
	case strings.EqualFold(mode, "Basic") && start != "" && end != "":
		fmt.Fprintf(&out, "%sframing: delimited\n", indent)
		fmt.Fprintf(&out, "%sstart_block: %s\n", indent, yamlString("0x"+start))
		fmt.Fprintf(&out, "%sdelimiter: %s\n", indent, yamlString("0x"+end))
	default:
		b.note("warning", where, fmt.Sprintf("the connector frames with %q, which was translated as reading to the end of the "+
			"connection", mode), "Check how the other side ends a message and set framing to match")
		fmt.Fprintf(&out, "%sframing: whole\n", indent)
	}
	return out.String()
}

// buildOtherSource translates a source that is not MLLP or a database. It reports false for a connector it does not know.
func (b *builder) buildOtherSource(src mirth.Connector) (string, bool) {
	var out strings.Builder

	switch {
	case isHTTPListener(src):
		fmt.Fprintln(&out, "  type: http")
		fmt.Fprintln(&out, "  http:")
		fmt.Fprintf(&out, "    listen: %s\n", yamlString(listenOn(src, "8080")))
		if p := strings.TrimSpace(src.Properties["contextPath"]); p != "" && p != "/" {
			if !strings.HasPrefix(p, "/") {
				p = "/" + p
			}
			fmt.Fprintf(&out, "    path: %s\n", yamlString(p))
		}
		fmt.Fprintln(&out, "    # REVIEW: set a token. Mirth's HTTP Listener accepted anyone who could reach it.")
		b.note("warning", "source",
			"the HTTP Listener had no authentication in the export, so the translated listener has none either",
			"Set source.http.token and give the sending system the same value, or restrict access at the network")

	case isFileReader(src):
		scheme := strings.ToLower(src.Properties["scheme"])
		if scheme != "" && scheme != "file" {
			return "", false
		}
		dir := strings.TrimSpace(src.Properties["host"])
		if dir == "" {
			dir = "/replace/with/the/inbound/directory"
			b.note("warning", "source", "the File Reader names no directory", "Set source.file.root")
		}
		fmt.Fprintln(&out, "  type: file")
		fmt.Fprintln(&out, "  file:")
		fmt.Fprintf(&out, "    root: %s\n", yamlString(dir))
		fmt.Fprintln(&out, "    dir: .")
		if pat := src.Properties["fileFilter"]; pat != "" && pat != "*" {
			if src.Properties["regex"] == "true" {
				b.note("warning", "source", fmt.Sprintf("the File Reader matched files with the regular expression %q, and "+
					"Perfuse matches with a glob", pat), "Rewrite source.file.pattern as a glob")
				fmt.Fprintln(&out, "    # REVIEW: the original pattern was a regular expression: "+oneLine(pat))
			} else {
				fmt.Fprintf(&out, "    pattern: %s\n", yamlString(pat))
			}
		}
		switch strings.ToUpper(src.Properties["afterProcessingAction"]) {
		case "DELETE":
			fmt.Fprintln(&out, "    after_read: delete")
		case "MOVE":
			to := src.Properties["moveToDirectory"]
			if to == "" {
				to = "processed"
			}
			fmt.Fprintln(&out, "    after_read: move")
			fmt.Fprintf(&out, "    move_to: %s\n", yamlString(to))
		default:
			// Mirth's NONE leaves the file and remembers nothing, so it reads it again on the next poll unless something else removes it.
			// Perfuse's leave remembers what it read. Close enough to translate, different enough to say.
			fmt.Fprintln(&out, "    after_read: leave")
			b.note("info", "source", "the File Reader left files where they were. Perfuse does the same and remembers which "+
				"files it has read, so it does not read them again", "")
		}
		if src.Properties["directoryRecursion"] == "true" {
			b.note("warning", "source", "the File Reader read subdirectories too, and the translated source reads only the "+
				"directory itself", "Add a channel per subdirectory, or flatten the drop directory")
		}

	case isJavaScriptReader(src):
		fmt.Fprintln(&out, "  type: javascript")
		fmt.Fprintln(&out, "  javascript:")
		fmt.Fprintln(&out, "    script: |")
		writeIndented(&out, src.Properties["script"], "      ")
		if v := src.Properties["pollConnectorProperties.pollingFrequency"]; v != "" {
			if d, ok := millisToDuration(v); ok {
				fmt.Fprintf(&out, "    poll_interval: %s\n", d)
			}
		}
		b.counts.Scripted++

	case isDICOMListener(src):
		fmt.Fprintln(&out, "  type: dicom")
		fmt.Fprintln(&out, "  dicom:")
		fmt.Fprintf(&out, "    listen: %s\n", yamlString(listenOn(src, "104")))
		if ae := src.Properties["applicationEntity"]; ae != "" {
			fmt.Fprintf(&out, "    ae_title: %s\n", yamlString(ae))
		}

	case isWebServiceListener(src):
		fmt.Fprintln(&out, "  type: soap")
		fmt.Fprintln(&out, "  soap:")
		fmt.Fprintf(&out, "    listen: %s\n", yamlString(listenOn(src, "8081")))
		if svc := src.Properties["serviceName"]; svc != "" {
			fmt.Fprintf(&out, "    path: %s\n", yamlString("/services/"+svc))
		}
		b.note("info", "source", "the Web Service Listener was translated to a soap source accepting Mirth's acceptMessage "+
			"envelope", "Check the WSDL the sending system was given still matches")

	case isMLLPListener(src) && !isMLLPFraming(src):
		fmt.Fprintln(&out, "  type: tcp")
		fmt.Fprintln(&out, "  tcp:")
		fmt.Fprintf(&out, "    listen: %s\n", yamlString(listenOn(src, "6661")))
		out.WriteString(b.tcpFraming(src, "source", "    "))

	case isChannelReader(src):
		fmt.Fprintln(&out, "  # The original was a Channel Reader: it received from other channels' Channel Writers. In Perfuse")
		fmt.Fprintln(&out, "  # a channel destination delivers straight into this channel, so the source only needs to exist.")
		fmt.Fprintln(&out, "  type: mllp")
		fmt.Fprintf(&out, "  listen: %s\n", yamlString("127.0.0.1:0"))
		b.note("info", "source", "the Channel Reader became a placeholder listener on a free loopback port; the channels that "+
			"wrote to it deliver through a channel destination instead", "")

	default:
		return "", false
	}

	return out.String(), true
}

// buildOtherDestination translates a destination that is not MLLP, file, HTTP or database. It reports false for one it does not know.
func (b *builder) buildOtherDestination(d mirth.Connector, name, where string) (string, bool) {
	var out strings.Builder

	switch {
	case isMLLPSender(d) && !isMLLPFraming(d):
		host, port := d.Properties["remoteAddress"], d.Properties["remotePort"]
		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: tcp")
		fmt.Fprintln(&out, "    tcp:")
		fmt.Fprintf(&out, "      address: %s\n", yamlString(host+":"+port))
		out.WriteString(b.tcpFraming(d, where, "      "))
		if d.Properties["ignoreResponse"] == "false" {
			fmt.Fprintln(&out, "      expect_reply: true")
		}

	case isSMTPSender(d):
		host := d.Properties["smtpHost"]
		if host == "" {
			host = "smtp.replace.me"
		}
		if p := d.Properties["smtpPort"]; p != "" {
			host += ":" + p
		}
		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: smtp")
		fmt.Fprintln(&out, "    smtp:")
		fmt.Fprintf(&out, "      host: %s\n", yamlString(host))
		from := d.Properties["from"]
		if from == "" {
			from = "perfuse@replace.me"
		}
		fmt.Fprintf(&out, "      from: %s\n", yamlString(from))
		fmt.Fprintln(&out, "      to:")
		to := splitAddresses(d.Properties["to"])
		if len(to) == 0 {
			to = []string{"replace.me@replace.me"}
			b.note("warning", where, "the SMTP Sender names no recipient", "Set smtp.to")
		}
		for _, a := range to {
			fmt.Fprintf(&out, "        - %s\n", yamlString(a))
		}
		if s := d.Properties["subject"]; s != "" {
			fmt.Fprintf(&out, "      subject: %s\n", yamlString(s))
		}
		if body := d.Properties["body"]; body != "" {
			fmt.Fprintln(&out, "      body: |")
			writeIndented(&out, body, "        ")
		}
		if d.Properties["authentication"] == "true" {
			fmt.Fprintf(&out, "      username: %s\n", yamlString(d.Properties["username"]))
			fmt.Fprintf(&out, "      password: %s\n", yamlString("${"+envVarNameFor(name, "SMTP")+"_PASSWORD}"))
			b.note("warning", where, "the SMTP password was deliberately not copied from the export",
				"Set the environment variable named in smtp.password")
		}
		if strings.Contains(d.Properties["body"], "${") || strings.Contains(d.Properties["subject"], "${") {
			b.note("warning", where, "the subject or body uses Mirth's ${...} variables", "Check each against Perfuse's placeholders")
		}

	case isJavaScriptWriter(d):
		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: javascript")
		fmt.Fprintln(&out, "    javascript:")
		fmt.Fprintln(&out, "      script: |")
		writeIndented(&out, d.Properties["script"], "        ")
		b.counts.Scripted++

	case isDICOMSender(d):
		host, port := d.Properties["host"], d.Properties["port"]
		if port == "" {
			port = "104"
		}
		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: dicom")
		fmt.Fprintln(&out, "    dicom:")
		fmt.Fprintf(&out, "      address: %s\n", yamlString(host+":"+port))
		if ae := d.Properties["applicationEntity"]; ae != "" {
			fmt.Fprintf(&out, "      called_ae: %s\n", yamlString(ae))
		}
		if ae := d.Properties["localApplicationEntity"]; ae != "" {
			fmt.Fprintf(&out, "      calling_ae: %s\n", yamlString(ae))
		}

	case isWebServiceSender(d):
		url := firstNonEmpty(d.Properties["locationURI"], d.Properties["wsdlUrl"])
		if url == "" {
			url = "https://replace.me/service"
			b.note("warning", where, "the Web Service Sender names no service address", "Set soap.url")
		}
		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: soap")
		fmt.Fprintln(&out, "    soap:")
		fmt.Fprintf(&out, "      url: %s\n", yamlString(url))
		if a := d.Properties["soapAction"]; a != "" {
			fmt.Fprintf(&out, "      action: %s\n", yamlString(a))
		}
		body := d.Properties["envelope"]
		if body == "" {
			body = "${message}"
		}
		fmt.Fprintln(&out, "      body: |")
		writeIndented(&out, body, "        ")
		b.note("warning", where, "the SOAP envelope was carried across as written; Mirth's ${...} variables in it need checking",
			"Compare soap.body with what the service expects")

	case isChannelWriter(d):
		target := b.opts.ChannelNames[d.Properties["channelId"]]
		fmt.Fprintf(&out, "  - name: %s\n", yamlString(name))
		fmt.Fprintln(&out, "    type: channel")
		fmt.Fprintln(&out, "    channel:")
		if target == "" {
			id := d.Properties["channelId"]
			b.note("warning", where, fmt.Sprintf("the Channel Writer sends to channel %s, which is not in this export", id),
				"Translate the whole server backup so the target is known, or set channel.name by hand")
			target = "replace-with-target-channel"
		}
		fmt.Fprintf(&out, "      name: %s\n", yamlString(sanitiseName(target)))

	default:
		return "", false
	}

	return out.String(), true
}

func splitAddresses(s string) []string {
	var out []string
	for _, a := range strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == ';' || r == ' ' }) {
		if a = strings.TrimSpace(a); a != "" {
			out = append(out, a)
		}
	}
	return out
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
