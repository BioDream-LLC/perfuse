package api

import (
	"bytes"
	"net/http"
	"strings"

	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/store"
	"gopkg.in/yaml.v3"
)

// Turning a filled-in form into a channel file.
//
// This is the half of the graphical builder that cannot live in the browser. The front end
// sends a structured model and gets back YAML, problems and a description; it never
// assembles YAML text itself.
//
// That division is deliberate. A path that contains a colon, a value that looks like a
// number, a password with a hash in it - each needs different quoting, and a front end that
// got one wrong would produce a file that either fails to load or, far worse, loads as
// something subtly different from what the form showed. Marshalling with the same library
// that reads the file makes the whole class of bug unavailable.
//
// The emitted YAML is minimal on purpose. It is the artifact somebody commits to git and
// reviews in a pull request six months from now, so it carries only the keys the user
// actually chose. A generated file padded with empty defaults is unreadable, and worse, it
// hides which settings were decided from which were merely present.

// buildModel mirrors a channel in the order it should be written.
//
// yaml.v3 marshals struct fields in declaration order, so the field order here is the file
// layout: identity, then what it receives, then what it does, then where it sends. That
// reads top to bottom as the message flows.
//
// Durations are strings rather than time.Duration because marshalling a Duration emits
// nanoseconds - "timeout: 30000000000" is valid, round-trips, and is unreadable.
type buildModel struct {
	Name        string `json:"name" yaml:"name"`
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Group is what to file this channel under, for a list that has grown too long to read.
	Group string `json:"group,omitempty" yaml:"group,omitempty"`

	// Contract points at a file saying what this feed must look like.
	Contract *buildContract `json:"contract,omitempty" yaml:"contract,omitempty"`

	// Tables names files of shared mapping tables a transformation may refer to by name.
	Tables []string `json:"tables,omitempty" yaml:"tables,omitempty"`

	// Attachments moves large payloads out of the stored message.
	Attachments *buildAttachments `json:"attachments,omitempty" yaml:"attachments,omitempty"`

	// Delimited configures a delimited channel.
	Delimited *buildDelimited `json:"delimited,omitempty" yaml:"delimited,omitempty"`

	// Enabled is a pointer so that "not stated" and "explicitly false" stay distinct;
	// the channel default is true and an absent key should mean the default.
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`

	DataType string      `json:"dataType,omitempty" yaml:"dataType,omitempty"`
	X12      *buildX12   `json:"x12,omitempty" yaml:"x12,omitempty"`
	NCPDP    *buildNCPDP `json:"ncpdp,omitempty" yaml:"ncpdp,omitempty"`
	HL7v3    *buildHL7v3 `json:"hl7v3,omitempty" yaml:"hl7v3,omitempty"`

	// Script carries an NCPDP SCRIPT channel's declarative steps.
	//
	// buildV3Step rather than a type of its own, because a prescription and a v3 document are both XML addressed by the same path
	// grammar - internal/eprescribe already borrows hl7v3.Path for its filter. The form omits nullflavor for SCRIPT rather than
	// offering it and having the loader refuse it, since a control that produces a channel file which will not load is worse than
	// no control.
	Script *buildScript `json:"script,omitempty" yaml:"script,omitempty"`

	// Imaging carries a DICOM channel's named steps.
	Imaging *buildDICOM   `json:"dicom,omitempty" yaml:"dicom,omitempty"`
	Source  buildSource   `json:"source" yaml:"source"`
	Filter  string        `json:"filter,omitempty" yaml:"filter,omitempty"`
	Steps   []buildStep   `json:"transformations,omitempty" yaml:"transformations,omitempty"`
	Scripts *buildScripts `json:"scripts,omitempty" yaml:"scripts,omitempty"`
	Dests   []buildDest   `json:"destinations" yaml:"destinations"`
}

type buildX12 struct {
	Envelope string `json:"envelope,omitempty" yaml:"envelope,omitempty"`
	Split    bool   `json:"split,omitempty" yaml:"split,omitempty"`

	// Steps are the X12 transformations. Their own field rather than the channel's, because the paths are X12 - CLM01 or
	// CLM-1 - and a v2 step on an X12 channel is refused at load.
	Steps []buildFormatStep `json:"transformations,omitempty" yaml:"transformations,omitempty"`

	// Acknowledge, AckSenderID and AckSenderQualifier configure the 999, 997 or TA1 sent back to a trading partner.
	Acknowledge        string `json:"acknowledge,omitempty" yaml:"acknowledge,omitempty"`
	AckSenderID        string `json:"ackSenderId,omitempty" yaml:"ack_sender_id,omitempty"`
	AckSenderQualifier string `json:"ackSenderQualifier,omitempty" yaml:"ack_sender_qualifier,omitempty"`
}

// buildNCPDP is the pharmacy claim block.
type buildNCPDP struct {
	// Steps are the NCPDP transformations, addressed by the standard's two-character field identifiers - D1, 07-D7.
	Steps []buildFormatStep `json:"transformations,omitempty" yaml:"transformations,omitempty"`
}

// buildFormatStep is a transformation step for the formats that share internal/steps.
//
// Separate from buildStep, which is the HL7 v2 vocabulary, because that one also offers remove, pad and date. The shared
// engine has none of those - remove because a blank field and an absent one are different things on these formats, pad and
// date because neither has been needed yet. Offering them in the form would produce a channel the loader refuses, which is
// worse than not offering them.
type buildFormatStep struct {
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	When        string `json:"when,omitempty" yaml:"when,omitempty"`

	Set     *buildSet       `json:"set,omitempty" yaml:"set,omitempty"`
	Copy    *buildCopy      `json:"copy,omitempty" yaml:"copy,omitempty"`
	Clear   *buildPath      `json:"clear,omitempty" yaml:"clear,omitempty"`
	Map     *buildFormatMap `json:"map,omitempty" yaml:"map,omitempty"`
	Replace *buildReplace   `json:"replace,omitempty" yaml:"replace,omitempty"`
	Trim    *buildPath      `json:"trim,omitempty" yaml:"trim,omitempty"`
	Case    *buildCase      `json:"case,omitempty" yaml:"case,omitempty"`
}

// buildFormatMap is the map step for the formats that share internal/steps.
//
// Not buildMap, which is the HL7 v2 shape, and the difference is not cosmetic: there, table is an inline mapping of value to
// value and strictness is a bool. Here, table names a code set loaded with the channel and on_missing chooses between keep
// and fail.
//
// Reusing buildMap - which the first version of this did - would have made the form emit a mapping where the loader expects
// a string, so a map step built from the GUI would have produced a channel that refuses to load. Caught by the qualified
// drift test asking why on_missing had no field.
type buildFormatMap struct {
	Path string `json:"path" yaml:"path"`

	// Table names a code set loaded with the channel.
	Table string `json:"table" yaml:"table"`

	Default string `json:"default,omitempty" yaml:"default,omitempty"`

	// OnMissing is keep or fail. Empty means keep.
	OnMissing string `json:"onMissing,omitempty" yaml:"on_missing,omitempty"`
}

// buildHL7v3 is the HL7 v3 block.
//
// Its filter is separate from the channel's ordinary Filter field, and the form has to keep them apart: the two are different
// languages against different message models, and a v2 filter on a v3 channel would never match a single message.
type buildHL7v3 struct {
	// Filter uses v3 paths. The field picker on the Playground tab produces one from a real message.
	Filter string `json:"filter,omitempty" yaml:"filter,omitempty"`

	// Acknowledge sends an MCCI_IN000002UV01 back. A pointer, so the form can distinguish "left alone" from
	// "deliberately turned off" - the default is on, and turning it off is a decision.
	Acknowledge *bool `json:"acknowledge,omitempty" yaml:"acknowledge,omitempty"`

	// SenderDevice and SenderOID are our identity in an acknowledgement. Required whenever it acknowledges, because a
	// receiver that does not recognise the device may discard the reply - which looks exactly like sending nothing.
	SenderDevice string `json:"senderDevice,omitempty" yaml:"sender_device,omitempty"`
	SenderOID    string `json:"senderOid,omitempty" yaml:"sender_oid,omitempty"`

	// Steps change the content of a v3 document.
	//
	// Their own type rather than reusing buildStep, because the vocabularies are not the same. Pad and date are
	// absent - padding to a fixed width has no meaning in XML, and a v3 timestamp is constrained by its datatype
	// so reformatting one is how a document stops validating. Nullflavor is present and has no v2 equivalent.
	Steps []buildV3Step `json:"transformations,omitempty" yaml:"transformations,omitempty"`
}

// buildV3Step carries one v3 transformation step.
//
// Clear, nullflavor and remove are three separate actions rather than one with a mode, because emptying a value, stating
// why it is missing, and saying it does not apply are three different clinical statements. A receiving system does
// different things with each: ASKU says the gap has been chased already, an empty element says nothing at all, and an
// absent element says the question does not arise.
// buildScript is the script block: declarative steps for a prescription.
type buildScript struct {
	Steps []buildV3Step `json:"transformations,omitempty" yaml:"transformations,omitempty"`
}

// buildDICOM is the dicom block: named transformation steps for an imaging channel.
type buildDICOM struct {
	Steps []buildDICOMStep `json:"transformations,omitempty" yaml:"transformations,omitempty"`
}

// buildDICOMStep carries one named imaging step.
//
// Named actions rather than a path and a value, and that is the whole design. An object is binary with pixel data in it, so a
// general tag writer can produce an image that opens and is wrong - which a radiologist reading the study cannot detect. Each
// action here knows which tags it touches and none can reach the pixels.
type buildDICOMStep struct {
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	Deidentify     *buildDICOMDeidentify  `json:"deidentify,omitempty" yaml:"deidentify,omitempty"`
	SetAETitle     *buildDICOMAETitle     `json:"setAeTitle,omitempty" yaml:"set_ae_title,omitempty"`
	StripPrivate   *buildDICOMStripKeep   `json:"stripPrivate,omitempty" yaml:"strip_private,omitempty"`
	SetInstitution *buildDICOMInstitution `json:"setInstitution,omitempty" yaml:"set_institution,omitempty"`
}

// buildDICOMDeidentify replaces the patient identifiers.
type buildDICOMDeidentify struct {
	// KeepDates leaves study and birth dates in place. A research request often needs ages and intervals to stay comparable, and
	// removing dates makes a series useless for that - so it is a control with a disclosure consequence rather than a default.
	KeepDates bool `json:"keepDates,omitempty" yaml:"keep_dates,omitempty"`

	// PatientID and PatientName are what the identifiers become. Empty means a fixed anonymous value.
	PatientID   string `json:"patientId,omitempty" yaml:"patient_id,omitempty"`
	PatientName string `json:"patientName,omitempty" yaml:"patient_name,omitempty"`
}

// buildDICOMAETitle rewrites the application entity titles.
type buildDICOMAETitle struct {
	Calling string `json:"calling,omitempty" yaml:"calling,omitempty"`
	Called  string `json:"called,omitempty" yaml:"called,omitempty"`
}

// buildDICOMStripKeep removes private tags, optionally keeping named ones.
type buildDICOMStripKeep struct {
	// Keep names tags to leave alone, because some vendors carry information in private tags that the receiver needs.
	Keep []string `json:"keep,omitempty" yaml:"keep,omitempty"`
}

// buildDICOMInstitution rewrites where the study was performed.
type buildDICOMInstitution struct {
	Name       string `json:"name,omitempty" yaml:"name,omitempty"`
	Address    string `json:"address,omitempty" yaml:"address,omitempty"`
	Department string `json:"department,omitempty" yaml:"department,omitempty"`
}

type buildV3Step struct {
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	When        string `json:"when,omitempty" yaml:"when,omitempty"`

	Set        *buildSet          `json:"set,omitempty" yaml:"set,omitempty"`
	Copy       *buildV3Copy       `json:"copy,omitempty" yaml:"copy,omitempty"`
	Clear      *buildPath         `json:"clear,omitempty" yaml:"clear,omitempty"`
	NullFlavor *buildV3NullFlavor `json:"nullflavor,omitempty" yaml:"nullflavor,omitempty"`
	Remove     *buildPath         `json:"remove,omitempty" yaml:"remove,omitempty"`
	Map        *buildV3Map        `json:"map,omitempty" yaml:"map,omitempty"`
	Replace    *buildV3Replace    `json:"replace,omitempty" yaml:"replace,omitempty"`
	Trim       *buildPath         `json:"trim,omitempty" yaml:"trim,omitempty"`
	Case       *buildCase         `json:"case,omitempty" yaml:"case,omitempty"`
}

// buildV3Copy has no default, unlike the v2 copy.
//
// Copying from an absent path is refused rather than defaulted, because writing an empty value would overwrite a good
// value at the destination with nothing.
type buildV3Copy struct {
	From string `json:"from" yaml:"from"`
	To   string `json:"to" yaml:"to"`
}

// buildV3NullFlavor states that no value is available, and why.
type buildV3NullFlavor struct {
	Path string `json:"path" yaml:"path"`

	// Reason is a NullFlavor code, validated at load. A typo does not fail: it writes a code no receiver
	// recognises, which most treat as "no information" - so a step meaning "we asked and they did not know"
	// quietly means "nothing is known", and the difference is whether anybody asks again.
	Reason string `json:"reason" yaml:"reason"`
}

// buildV3Map names a shared table only, with no inline entries.
//
// A v3 mapping is between coded vocabularies, which are the kind of thing several channels share and somebody maintains in
// one place. Inline entries are how the same mapping ends up in four channels with three of them stale.
type buildV3Map struct {
	Path  string `json:"path" yaml:"path"`
	Table string `json:"table" yaml:"table"`

	// OnMissing is keep, clear or fail. No silent default that keeps the value, because a code that failed to
	// translate and travelled on unchanged is the failure mode of every mapping table ever written.
	OnMissing string `json:"onMissing,omitempty" yaml:"on_missing,omitempty"`
}

// buildV3Replace has no "all" flag: a v3 replace always substitutes every match, matching Go's ReplaceAllString.
type buildV3Replace struct {
	Path string `json:"path" yaml:"path"`
	From string `json:"from" yaml:"from"`
	To   string `json:"to" yaml:"to"`
}

type buildSource struct {
	Type   string            `json:"type" yaml:"type"`
	Listen string            `json:"listen,omitempty" yaml:"listen,omitempty"`
	HTTP   *buildHTTPSrc     `json:"http,omitempty" yaml:"http,omitempty"`
	DB     *buildDBSrc       `json:"database,omitempty" yaml:"database,omitempty"`
	SFTP   *buildSFTPSrc     `json:"sftp,omitempty" yaml:"sftp,omitempty"`
	File   *buildFileSrc     `json:"file,omitempty" yaml:"file,omitempty"`
	TCP    *buildTCPSrc      `json:"tcp,omitempty" yaml:"tcp,omitempty"`
	FTPSrc *buildFTPSrc      `json:"ftp,omitempty" yaml:"ftp,omitempty"`
	SMB    *buildSMBSrc      `json:"smb,omitempty" yaml:"smb,omitempty"`
	WebDAV *buildWebDAVSrc   `json:"webdav,omitempty" yaml:"webdav,omitempty"`
	Serial *buildSerialSrc   `json:"serial,omitempty" yaml:"serial,omitempty"`
	DICOM  *buildDICOMSource `json:"dicom,omitempty" yaml:"dicom,omitempty"`
	SOAP   *buildSOAPSource  `json:"soap,omitempty" yaml:"soap,omitempty"`

	// JS is the JavaScript Reader: a script that runs on a timer and returns the messages.
	//
	// Mirth's equivalent is one of the connectors people most often have to reimplement by hand, so a channel migrating
	// from one needs this on day one - which is exactly when somebody is working in the form rather than a file.
	JS *buildJSSrc `json:"javascript,omitempty" yaml:"javascript,omitempty"`

	// Broker reads from a message broker.
	Broker *buildBrokerSource `json:"broker,omitempty" yaml:"broker,omitempty"`

	// Kafka reads from a Kafka topic.
	Kafka *buildKafkaSource `json:"kafka,omitempty" yaml:"kafka,omitempty"`

	// DICOMQuery polls an imaging archive.
	//
	// This was missing until the graphical builder needed it, and the drift guard did not notice - it checks destinations
	// and not sources. Worth recording rather than quietly fixing: a guard with a blind spot is more dangerous than no
	// guard, because it is trusted.
	DICOMQuery *buildDICOMQuerySource `json:"dicomQuery,omitempty" yaml:"dicom_query,omitempty"`
	TLS        *buildTLS              `json:"tls,omitempty" yaml:"tls,omitempty"`
	Ack        *buildAck              `json:"ack,omitempty" yaml:"ack,omitempty"`
	Limits     *buildSrcLimits        `json:"limits,omitempty" yaml:",inline,omitempty"`
}

// buildSrcLimits is inlined so the keys sit directly on the source, where the schema
// expects them, while staying one group in the form.
type buildSrcLimits struct {
	MaxMessageSize int    `json:"maxMessageSize,omitempty" yaml:"max_message_size,omitempty"`
	IdleTimeout    string `json:"idleTimeout,omitempty" yaml:"idle_timeout,omitempty"`
	MaxConnections int    `json:"maxConnections,omitempty" yaml:"max_connections,omitempty"`
}

type buildHTTPSrc struct {
	Listen string `json:"listen,omitempty" yaml:"listen,omitempty"`
	Path   string `json:"path,omitempty" yaml:"path,omitempty"`

	// Token is a shared secret the sender must present. Offered in the form because an
	// HTTP listener without one accepts a message from anybody who can reach the port.
	Token       string `json:"token,omitempty" yaml:"token,omitempty"`
	ReadTimeout string `json:"readTimeout,omitempty" yaml:"read_timeout,omitempty"`

	// MaxMessageSize bounds one request body. Without it a sender can post as much as it likes and the limit is memory.
	MaxMessageSize int `json:"maxMessageSize,omitempty" yaml:"max_message_size,omitempty"`

	// TLS serves the listener over HTTPS, and can require a client certificate.
	//
	// The source-level tls key covers an MLLP or TCP listener; this is the one an HTTP source reads, and they are separate
	// settings on separate structs. Without this the form could offer a token but not encryption, which is the wrong way
	// round for a listener taking patient data over a network.
	TLS *buildTLS `json:"tls,omitempty" yaml:"tls,omitempty"`

	// Ack is the HL7 acknowledgement returned in the response body.
	//
	// The same four settings as an MLLP source's ack, because it is the same acknowledgement - a sender posting over HTTP
	// still needs to know whether the message was accepted, and by which application and facility.
	Ack *buildAck `json:"ack,omitempty" yaml:"ack,omitempty"`
}

type buildDBSrc struct {
	Driver       string `json:"driver,omitempty" yaml:"driver,omitempty"`
	DSN          string `json:"dsn,omitempty" yaml:"dsn,omitempty"`
	Query        string `json:"query,omitempty" yaml:"query,omitempty"`
	Column       string `json:"column,omitempty" yaml:"column,omitempty"`
	KeyColumn    string `json:"keyColumn,omitempty" yaml:"key_column,omitempty"`
	AfterQuery   string `json:"afterQuery,omitempty" yaml:"after_query,omitempty"`
	PollInterval string `json:"pollInterval,omitempty" yaml:"poll_interval,omitempty"`

	// Template builds a message from columns, as an alternative to Column naming one that
	// already holds a whole message. Exactly one of the two is required, and the form has
	// to offer both or half the database sources in the world are unbuildable here.
	Template string `json:"template,omitempty" yaml:"template,omitempty"`

	BatchSize    int    `json:"batchSize,omitempty" yaml:"batch_size,omitempty"`
	MaxAttempts  int    `json:"maxAttempts,omitempty" yaml:"max_attempts,omitempty"`
	QueryTimeout string `json:"queryTimeout,omitempty" yaml:"query_timeout,omitempty"`
}

// buildFileSrc is a directory on this server.
//
// The connector a site tries first, so the form has to be complete: a great deal of real integration is a folder an
// analyser writes to, and somebody evaluating Perfuse will look for it before anything else.
// buildTCPFraming is how message boundaries are found on a raw socket.
//
// Shared between the source and the destination in the form as well as in the config, because the two have to agree and
// a form that described them differently would invite them to diverge.
// buildFilePoll is the part of a file-collecting source that has nothing to do with the transport.
//
// Shared by every file source in the form, because a site that has worked out the right stable_for for its analyser
// should not have to work it out again when the analyser moves to a share.
// buildSerialSrc reads a device over a cable.
//
// The form has to be complete here more than anywhere else, because every one of these settings changes how bytes are
// interpreted and a wrong one does not fail - it delivers readable-looking nonsense.
type buildSerialSrc struct {
	Port string `json:"port,omitempty" yaml:"port,omitempty"`

	// Baud has no default anywhere, including in the form. A guessed default would appear to work.
	Baud int `json:"baud,omitempty" yaml:"baud,omitempty"`

	DataBits int    `json:"dataBits,omitempty" yaml:"data_bits,omitempty"`
	Parity   string `json:"parity,omitempty" yaml:"parity,omitempty"`
	StopBits string `json:"stopBits,omitempty" yaml:"stop_bits,omitempty"`

	// FlowControl is worth setting rather than leaving. Expected and not configured, a device stops partway through a
	// long message and the result is a truncated message rather than an error.
	FlowControl string `json:"flowControl,omitempty" yaml:"flow_control,omitempty"`

	buildTCPFraming `yaml:",inline"`

	MaxMessageSize int `json:"maxMessageSize,omitempty" yaml:"max_message_size,omitempty"`

	// QuietAfter is the only way anybody learns a serial feed has died. A cable has no connection to lose, so an
	// unplugged cable and a quiet night are the same silence.
	QuietAfter string `json:"quietAfter,omitempty" yaml:"quiet_after,omitempty"`

	Reply     string `json:"reply,omitempty" yaml:"reply,omitempty"`
	ReplyText string `json:"replyText,omitempty" yaml:"reply_text,omitempty"`

	// ReopenAfter exists because a USB serial adapter unplugged and replugged returns the same device path with a
	// different kernel handle, so the old one errors forever and reopening is the only recovery.
	ReopenAfter string `json:"reopenAfter,omitempty" yaml:"reopen_after,omitempty"`
}

type buildFilePoll struct {
	Dir          string `json:"dir,omitempty" yaml:"dir,omitempty"`
	Pattern      string `json:"pattern,omitempty" yaml:"pattern,omitempty"`
	PollInterval string `json:"pollInterval,omitempty" yaml:"poll_interval,omitempty"`

	AfterRead string `json:"afterRead,omitempty" yaml:"after_read,omitempty"`
	MoveTo    string `json:"moveTo,omitempty" yaml:"move_to,omitempty"`
	ErrorDir  string `json:"errorDir,omitempty" yaml:"error_dir,omitempty"`

	// StableFor is the single most important setting here. Half an HL7 message usually still parses.
	StableFor string `json:"stableFor,omitempty" yaml:"stable_for,omitempty"`

	Framed bool `json:"framed,omitempty" yaml:"framed,omitempty"`
	Raw    bool `json:"raw,omitempty" yaml:"raw,omitempty"`

	MaxFileSize int64  `json:"maxFileSize,omitempty" yaml:"max_file_size,omitempty"`
	BatchSize   int    `json:"batchSize,omitempty" yaml:"batch_size,omitempty"`
	SortBy      string `json:"sortBy,omitempty" yaml:"sort_by,omitempty"`
}

// buildFTPSrc polls an FTP or FTPS directory.
type buildFTPSrc struct {
	Host     string `json:"host,omitempty" yaml:"host,omitempty"`
	User     string `json:"user,omitempty" yaml:"user,omitempty"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`

	// Security defaults to explicit FTPS. Plain FTP has to be asked for by name, because it sends the password in clear
	// text and that should be a decision rather than a default.
	Security string `json:"security,omitempty" yaml:"security,omitempty"`

	InsecureSkipVerify bool `json:"insecureSkipVerify,omitempty" yaml:"insecure_skip_verify,omitempty"`

	// Root is what containment is enforced against. An FTP server will happily accept ../.. as a path, so without a
	// root nothing confines the channel to one directory.
	Root string `json:"root,omitempty" yaml:"root,omitempty"`

	Timeout string `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	buildFilePoll `yaml:",inline"`
}

// buildSMBSrc polls a Windows file share.
type buildSMBSrc struct {
	Host string `json:"host,omitempty" yaml:"host,omitempty"`

	// Share is the share name only: the "data" in \\server\data. A path here is refused rather than trimmed, because
	// quietly using the first component would connect to a share nobody named.
	Share string `json:"share,omitempty" yaml:"share,omitempty"`

	User     string `json:"user,omitempty" yaml:"user,omitempty"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`

	// Domain is the Windows domain or workgroup. Empty is usually right for a local account, and it has to be offered
	// because a domain account is the normal case in a hospital and there is nowhere else to put it.
	Domain string `json:"domain,omitempty" yaml:"domain,omitempty"`

	Root    string `json:"root,omitempty" yaml:"root,omitempty"`
	Timeout string `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	buildFilePoll `yaml:",inline"`
}

// buildWebDAVSrc polls a WebDAV collection.
type buildWebDAVSrc struct {
	URL      string `json:"url,omitempty" yaml:"url,omitempty"`
	User     string `json:"user,omitempty" yaml:"user,omitempty"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`

	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty" yaml:"insecure_skip_verify,omitempty"`
	Timeout            string `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	buildFilePoll `yaml:",inline"`
}

// buildJSSrc runs a script on a timer and feeds what it returns into the channel.
type buildJSSrc struct {
	// Script is the code run on each poll. Required: a JavaScript source with no script polls forever and produces
	// nothing, which looks like a channel that is running.
	Script string `json:"script,omitempty" yaml:"script,omitempty"`

	// PollInterval defaults to 5 seconds and Timeout to 30. Both are offered because the defaults suit a database or an
	// API poll and suit neither a script doing real work nor one that should run once a minute.
	PollInterval string `json:"pollInterval,omitempty" yaml:"poll_interval,omitempty"`
	Timeout      string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

type buildTCPFraming struct {
	// Framing is required with no default. Reading a stream with the wrong framing produces messages that look
	// plausible rather than an error, so the form has to ask rather than guess.
	Framing string `json:"framing,omitempty" yaml:"framing,omitempty"`

	// Delimiter and StartBlock are written with Go escapes, because these are control characters and there is no good
	// way to type them into a form otherwise. A manual says STX; somebody types \x02.
	Delimiter  string `json:"delimiter,omitempty" yaml:"delimiter,omitempty"`
	StartBlock string `json:"startBlock,omitempty" yaml:"start_block,omitempty"`

	// KeepDelimiter includes the delimiter in the message. Off by default: it is framing, not content.
	KeepDelimiter bool `json:"keepDelimiter,omitempty" yaml:"keep_delimiter,omitempty"`

	RecordLength int   `json:"recordLength,omitempty" yaml:"record_length,omitempty"`
	TrimPadding  *bool `json:"trimPadding,omitempty" yaml:"trim_padding,omitempty"`

	LengthBytes int  `json:"lengthBytes,omitempty" yaml:"length_bytes,omitempty"`
	BigEndian   bool `json:"bigEndian,omitempty" yaml:"big_endian,omitempty"`

	// LengthIncludesHeader has to be offered because both conventions exist and the difference is silent: with a
	// four-byte header the payload is wrong by four bytes every time, which for a text format usually still parses.
	LengthIncludesHeader bool `json:"lengthIncludesHeader,omitempty" yaml:"length_includes_header,omitempty"`
}

// buildTCPSrc is a raw socket listener.
type buildTCPSrc struct {
	Listen string `json:"listen,omitempty" yaml:"listen,omitempty"`

	buildTCPFraming `yaml:",inline"`

	MaxMessageSize int    `json:"maxMessageSize,omitempty" yaml:"max_message_size,omitempty"`
	IdleTimeout    string `json:"idleTimeout,omitempty" yaml:"idle_timeout,omitempty"`
	MaxConnections int    `json:"maxConnections,omitempty" yaml:"max_connections,omitempty"`

	// Reply matters more than it looks. A device expecting a reply that never comes usually retries the same message
	// forever, which at the receiving end looks like a duplicate storm rather than a missing reply.
	Reply     string `json:"reply,omitempty" yaml:"reply,omitempty"`
	ReplyText string `json:"replyText,omitempty" yaml:"reply_text,omitempty"`

	TLS *buildTLS `json:"tls,omitempty" yaml:"tls,omitempty"`
}

// buildTCPDest writes to a raw socket.
type buildTCPDest struct {
	Address string `json:"address,omitempty" yaml:"address,omitempty"`

	buildTCPFraming `yaml:",inline"`

	MaxMessageSize int    `json:"maxMessageSize,omitempty" yaml:"max_message_size,omitempty"`
	Timeout        string `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	// ExpectReply changes what delivered means. Without it, success means the bytes reached the operating system's send
	// buffer, which a peer that crashed a moment later never read.
	ExpectReply bool `json:"expectReply,omitempty" yaml:"expect_reply,omitempty"`

	// KeepAlive holds one connection open across messages. Off by default because a connection per message works
	// everywhere, and some device endpoints refuse a second message on the same connection.
	KeepAlive bool `json:"keepAlive,omitempty" yaml:"keep_alive,omitempty"`

	TLS *buildTLS `json:"tls,omitempty" yaml:"tls,omitempty"`
}

type buildFileSrc struct {
	// Root bounds every path this source touches, and is why the form asks for it separately from Dir. Anything
	// resolving outside it is refused, so a channel built here cannot be pointed at the rest of the server.
	Root string `json:"root,omitempty" yaml:"root,omitempty"`

	// FollowSymlinks permits reading a file that links outside Root. Off by default, because for a directory that
	// arbitrary systems write into, a link out of the tree is usually the attack it looks like.
	FollowSymlinks bool `json:"followSymlinks,omitempty" yaml:"follow_symlinks,omitempty"`

	Dir          string `json:"dir,omitempty" yaml:"dir,omitempty"`
	Pattern      string `json:"pattern,omitempty" yaml:"pattern,omitempty"`
	PollInterval string `json:"pollInterval,omitempty" yaml:"poll_interval,omitempty"`

	AfterRead string `json:"afterRead,omitempty" yaml:"after_read,omitempty"`
	MoveTo    string `json:"moveTo,omitempty" yaml:"move_to,omitempty"`
	ErrorDir  string `json:"errorDir,omitempty" yaml:"error_dir,omitempty"`

	// StableFor waits for a file to stop changing before reading it, which is how a half-written file is avoided. The
	// single most important setting here: half an HL7 message usually still parses.
	StableFor string `json:"stableFor,omitempty" yaml:"stable_for,omitempty"`

	Framed bool `json:"framed,omitempty" yaml:"framed,omitempty"`

	// Raw delivers each file whole and unparsed, which is what makes this connector useful for a PDF, a zip or a
	// proprietary export. It requires dataType raw on the channel, and the two are validated against each other.
	Raw bool `json:"raw,omitempty" yaml:"raw,omitempty"`

	MaxFileSize int64 `json:"maxFileSize,omitempty" yaml:"max_file_size,omitempty"`
	BatchSize   int   `json:"batchSize,omitempty" yaml:"batch_size,omitempty"`

	// SortBy orders the files within a poll. It matters more than it looks: an ADT stream where an A08 update is read
	// before the A01 admission produces patients that do not exist yet, and directory order is arbitrary.
	SortBy string `json:"sortBy,omitempty" yaml:"sort_by,omitempty"`
}

type buildSFTPSrc struct {
	Host           string `json:"host,omitempty" yaml:"host,omitempty"`
	User           string `json:"user,omitempty" yaml:"user,omitempty"`
	Password       string `json:"password,omitempty" yaml:"password,omitempty"`
	KeyFile        string `json:"keyFile,omitempty" yaml:"key_file,omitempty"`
	KeyPassphrase  string `json:"keyPassphrase,omitempty" yaml:"key_passphrase,omitempty"`
	KnownHostsFile string `json:"knownHostsFile,omitempty" yaml:"known_hosts_file,omitempty"`
	Dir            string `json:"dir,omitempty" yaml:"dir,omitempty"`
	Pattern        string `json:"pattern,omitempty" yaml:"pattern,omitempty"`
	PollInterval   string `json:"pollInterval,omitempty" yaml:"poll_interval,omitempty"`

	// AfterRead decides what happens to a file once its messages are accepted. It
	// defaults to move, which then requires MoveTo - a collector that leaves files where
	// it found them reads them again on the next poll, so the default is the safe one and
	// the form has to ask for the destination.
	AfterRead string `json:"afterRead,omitempty" yaml:"after_read,omitempty"`
	MoveTo    string `json:"moveTo,omitempty" yaml:"move_to,omitempty"`
	ErrorDir  string `json:"errorDir,omitempty" yaml:"error_dir,omitempty"`

	// StableFor waits for a file to stop changing before reading it, which is how a
	// half-written upload is avoided.
	StableFor   string `json:"stableFor,omitempty" yaml:"stable_for,omitempty"`
	Framed      bool   `json:"framed,omitempty" yaml:"framed,omitempty"`
	MaxFileSize int64  `json:"maxFileSize,omitempty" yaml:"max_file_size,omitempty"`
	Timeout     string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

type buildTLS struct {
	Enabled           bool   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	CertFile          string `json:"certFile,omitempty" yaml:"cert_file,omitempty"`
	KeyFile           string `json:"keyFile,omitempty" yaml:"key_file,omitempty"`
	CAFile            string `json:"caFile,omitempty" yaml:"ca_file,omitempty"`
	RequireClientCert bool   `json:"requireClientCert,omitempty" yaml:"require_client_cert,omitempty"`

	// ServerName is needed whenever the certificate names something other than the
	// address dialled, which is common behind a load balancer and is otherwise a
	// handshake failure with a misleading message.
	ServerName string `json:"serverName,omitempty" yaml:"server_name,omitempty"`
}

type buildAck struct {
	When                string `json:"when,omitempty" yaml:"when,omitempty"`
	Application         string `json:"application,omitempty" yaml:"application,omitempty"`
	Facility            string `json:"facility,omitempty" yaml:"facility,omitempty"`
	IncludeTriggerEvent bool   `json:"includeTriggerEvent,omitempty" yaml:"include_trigger_event,omitempty"`
}

type buildScripts struct {
	// Language is javascript or lua, and applies to every script in the block.
	//
	// One setting for the block rather than one per script, because a channel's scripts share the channel map and are read
	// together. A filter in Lua beside a transformer in JavaScript would mean two languages in one review, sharing state
	// through a map whose values would have to mean the same in both.
	Language string `json:"language,omitempty" yaml:"language,omitempty"`

	Filter      string `json:"filter,omitempty" yaml:"filter,omitempty"`
	Transformer string `json:"transformer,omitempty" yaml:"transformer,omitempty"`

	// Preprocessor runs before parsing and is the only place a message that does not parse can be
	// repaired. In the form because a channel that needs one usually needs it on day one, when a
	// sender turns out to send something the parser will not take.
	Preprocessor string `json:"preprocessor,omitempty" yaml:"preprocessor,omitempty"`

	// Postprocessor runs after the message has been handled and cannot change it.
	Postprocessor string `json:"postprocessor,omitempty" yaml:"postprocessor,omitempty"`

	// Deploy runs once when the channel starts, Undeploy once when it stops.
	Deploy   string `json:"deploy,omitempty" yaml:"deploy,omitempty"`
	Undeploy string `json:"undeploy,omitempty" yaml:"undeploy,omitempty"`

	// Include names shared JavaScript files every script on this channel can call into.
	Include []string `json:"include,omitempty" yaml:"include,omitempty"`

	Timeout string `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	// Allow grants a script a capability it does not have by default. It is in the form
	// because it must be visible: a script that can reach the network is a different
	// proposition from one that cannot, and burying that in a file nobody opens is how it
	// gets granted by accident.
	Allow []string `json:"allow,omitempty" yaml:"allow,omitempty"`

	// FileRoots are the directories a script granted "file" may read and write.
	//
	// In the form because it is the whole difference between file access and unrestricted file access. Granting
	// "file" without naming directories used to reach the entire filesystem, which meant a channel authored by an
	// editor could read this program's own database of password hashes as soon as an administrator started it.
	//
	// It is refused at load without this, so the form has to offer it or the capability is unusable from the GUI -
	// which would push people to hand-edit YAML for the one setting that most needs to be seen.
	FileRoots []string `json:"fileRoots,omitempty" yaml:"file_roots,omitempty"`
}

// buildStep carries one declarative step. Exactly one action must be set, which the
// loader enforces, so the form's job is to make setting two of them impossible.
type buildStep struct {
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
	When        string `json:"when,omitempty" yaml:"when,omitempty"`

	Set     *buildSet     `json:"set,omitempty" yaml:"set,omitempty"`
	Copy    *buildCopy    `json:"copy,omitempty" yaml:"copy,omitempty"`
	Clear   *buildPath    `json:"clear,omitempty" yaml:"clear,omitempty"`
	Remove  *buildPath    `json:"remove,omitempty" yaml:"remove,omitempty"`
	Map     *buildMap     `json:"map,omitempty" yaml:"map,omitempty"`
	Replace *buildReplace `json:"replace,omitempty" yaml:"replace,omitempty"`
	Pad     *buildPad     `json:"pad,omitempty" yaml:"pad,omitempty"`
	Date    *buildDate    `json:"date,omitempty" yaml:"date,omitempty"`
	Trim    *buildPath    `json:"trim,omitempty" yaml:"trim,omitempty"`
	Case    *buildCase    `json:"case,omitempty" yaml:"case,omitempty"`
}

type buildPath struct {
	Path string `json:"path" yaml:"path"`
}

type buildSet struct {
	Path  string `json:"path" yaml:"path"`
	Value string `json:"value" yaml:"value"`
}

type buildCopy struct {
	From    string `json:"from" yaml:"from"`
	To      string `json:"to" yaml:"to"`
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
}

type buildMap struct {
	Path  string            `json:"path" yaml:"path"`
	Table map[string]string `json:"table" yaml:"table"`
	// Use names a shared table instead of an inline one.
	Use     string `json:"use,omitempty" yaml:"use,omitempty"`
	Default string `json:"default,omitempty" yaml:"default,omitempty"`
	Strict  bool   `json:"strict,omitempty" yaml:"strict,omitempty"`
}

type buildReplace struct {
	Path    string `json:"path" yaml:"path"`
	Pattern string `json:"pattern" yaml:"pattern"`
	With    string `json:"with" yaml:"with"`
	All     bool   `json:"all,omitempty" yaml:"all,omitempty"`
}

type buildPad struct {
	Path     string `json:"path" yaml:"path"`
	Width    int    `json:"width" yaml:"width"`
	With     string `json:"with,omitempty" yaml:"with,omitempty"`
	Right    bool   `json:"right,omitempty" yaml:"right,omitempty"`
	Truncate bool   `json:"truncate,omitempty" yaml:"truncate,omitempty"`
}

type buildDate struct {
	Path    string `json:"path" yaml:"path"`
	From    string `json:"from" yaml:"from"`
	To      string `json:"to" yaml:"to"`
	OnError string `json:"onError,omitempty" yaml:"on_error,omitempty"`
}

type buildCase struct {
	Path string `json:"path" yaml:"path"`
	To   string `json:"to" yaml:"to"`
}

type buildDest struct {
	Name    string `json:"name" yaml:"name"`
	Type    string `json:"type" yaml:"type"`
	Enabled *bool  `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	Filter  string `json:"filter,omitempty" yaml:"filter,omitempty"`

	Address string `json:"address,omitempty" yaml:"address,omitempty"`
	Dir     string `json:"dir,omitempty" yaml:"dir,omitempty"`

	// File destination shaping. FileName decides what the archive is called, which is the
	// difference between a directory somebody can find a message in and one they cannot.
	FileName    string `json:"fileName,omitempty" yaml:"file_name,omitempty"`
	TempSuffix  string `json:"tempSuffix,omitempty" yaml:"temp_suffix,omitempty"`
	Append      bool   `json:"append,omitempty" yaml:"append,omitempty"`
	RetainHours int    `json:"retainHours,omitempty" yaml:"retain_hours,omitempty"`

	HTTP *buildHTTPDest `json:"http,omitempty" yaml:"http,omitempty"`
	FHIR *buildFHIRDest `json:"fhir,omitempty" yaml:"fhir,omitempty"`
	CDA  *buildCDADest  `json:"cda,omitempty" yaml:"cda,omitempty"`
	DB   *buildDBDest   `json:"database,omitempty" yaml:"database,omitempty"`
	SFTP *buildSFTPDest `json:"sftp,omitempty" yaml:"sftp,omitempty"`
	TCP  *buildTCPDest  `json:"tcp,omitempty" yaml:"tcp,omitempty"`
	SMTP *buildSMTPDest `json:"smtp,omitempty" yaml:"smtp,omitempty"`

	// Channel routes to another channel in this server. One field, because everything else about
	// where the message goes belongs to the receiving channel.
	Channel *buildChannelDest `json:"channel,omitempty" yaml:"channel,omitempty"`

	// ResponseTransformer inspects what the receiver said back and may mark the delivery failed.
	ResponseTransformer string `json:"responseTransformer,omitempty" yaml:"response_transformer,omitempty"`

	// S3 applies to an s3 destination.
	S3 *buildS3Dest `json:"s3,omitempty" yaml:"s3,omitempty"`

	// FTP applies to an ftp destination.
	FTP *buildFTPDest `json:"ftp,omitempty" yaml:"ftp,omitempty"`

	// Document applies to a document destination.
	Document *buildDocumentDest `json:"document,omitempty" yaml:"document,omitempty"`

	// SOAP applies to a soap destination.
	SOAP *buildSOAPDest `json:"soap,omitempty" yaml:"soap,omitempty"`

	// DICOM applies to a dicom destination.
	DICOM *buildDICOMDest `json:"dicom,omitempty" yaml:"dicom,omitempty"`

	// JavaScript applies to a javascript destination.
	JavaScript *buildJSDest `json:"javascript,omitempty" yaml:"javascript,omitempty"`

	// Broker applies to a broker destination.
	Broker *buildBrokerDest `json:"broker,omitempty" yaml:"broker,omitempty"`

	// Kafka applies to a kafka destination.
	Kafka *buildKafkaDest `json:"kafka,omitempty" yaml:"kafka,omitempty"`
	TLS   *buildTLS       `json:"tls,omitempty" yaml:"tls,omitempty"`

	Timeout string      `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	Retry   *buildRetry `json:"retry,omitempty" yaml:"retry,omitempty"`
	Queue   *buildQueue `json:"queue,omitempty" yaml:"queue,omitempty"`
}

type buildHTTPDest struct {
	// TLS is the client side of the connection: a client certificate to present, and a CA to verify the server against.
	//
	// A destination posting to a payer or a hospital over mutual TLS needs this, and without it the only way to configure
	// one was to hand-write the file - which the form then could not round-trip.
	TLS *buildTLS `json:"tls,omitempty" yaml:"tls,omitempty"`

	URL         string            `json:"url,omitempty" yaml:"url,omitempty"`
	Method      string            `json:"method,omitempty" yaml:"method,omitempty"`
	ContentType string            `json:"contentType,omitempty" yaml:"content_type,omitempty"`
	Headers     map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`

	BearerToken string `json:"bearerToken,omitempty" yaml:"bearer_token,omitempty"`
	Username    string `json:"username,omitempty" yaml:"username,omitempty"`
	Password    string `json:"password,omitempty" yaml:"password,omitempty"`

	// What counts as success. A receiver that answers 200 with an error in the body is
	// common enough that treating every 200 as delivered loses messages quietly, so both
	// of these belong where somebody setting up the destination will see them.
	SuccessStatus   []int  `json:"successStatus,omitempty" yaml:"success_status,omitempty"`
	FailOnBody      string `json:"failOnBody,omitempty" yaml:"fail_on_body,omitempty"`
	FollowRedirects *bool  `json:"followRedirects,omitempty" yaml:"follow_redirects,omitempty"`
}

type buildFHIRDest struct {
	URL     string `json:"url,omitempty" yaml:"url,omitempty"`
	Version string `json:"version,omitempty" yaml:"version,omitempty"`

	// One of these two is required by the loader, and the reason is worth carrying into
	// the form: without a system URI an MRN is ambiguous between facilities, so a bundle
	// built without one asserts an identity it cannot support.
	DefaultIdentifierSystem string            `json:"defaultIdentifierSystem,omitempty" yaml:"default_identifier_system,omitempty"`
	IdentifierSystems       map[string]string `json:"identifierSystems,omitempty" yaml:"identifier_systems,omitempty"`

	Timezone string `json:"timezone,omitempty" yaml:"timezone,omitempty"`

	ClaimUSCore        bool `json:"claimUSCore,omitempty" yaml:"claim_us_core,omitempty"`
	ValidateBeforeSend bool `json:"validateBeforeSend,omitempty" yaml:"validate_before_send,omitempty"`
	RejectOnWarning    bool `json:"rejectOnWarning,omitempty" yaml:"reject_on_warning,omitempty"`

	Headers     map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	BearerToken string            `json:"bearerToken,omitempty" yaml:"bearer_token,omitempty"`
}

type buildCDADest struct {
	URL     string `json:"url,omitempty" yaml:"url,omitempty"`
	Dir     string `json:"dir,omitempty" yaml:"dir,omitempty"`
	Write   string `json:"write,omitempty" yaml:"write,omitempty"`
	Version string `json:"version,omitempty" yaml:"version,omitempty"`

	IdentifierSystems map[string]string `json:"identifierSystems,omitempty" yaml:"identifier_systems,omitempty"`

	// RequireAgreement refuses to emit a document without a recorded consent, and
	// OnNoDocument decides what happens when a message yields nothing to send. Both are
	// clinical-safety settings, so they are asked rather than defaulted out of sight.
	RequireAgreement bool   `json:"requireAgreement,omitempty" yaml:"require_agreement,omitempty"`
	OnNoDocument     string `json:"onNoDocument,omitempty" yaml:"on_no_document,omitempty"`

	Headers     map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	BearerToken string            `json:"bearerToken,omitempty" yaml:"bearer_token,omitempty"`
}

type buildDBDest struct {
	Driver    string   `json:"driver,omitempty" yaml:"driver,omitempty"`
	DSN       string   `json:"dsn,omitempty" yaml:"dsn,omitempty"`
	Statement string   `json:"statement,omitempty" yaml:"statement,omitempty"`
	Params    []string `json:"params,omitempty" yaml:"params,omitempty"`

	Timeout      string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	MaxOpenConns int    `json:"maxOpenConns,omitempty" yaml:"max_open_conns,omitempty"`
}

type buildSFTPDest struct {
	Host           string `json:"host,omitempty" yaml:"host,omitempty"`
	User           string `json:"user,omitempty" yaml:"user,omitempty"`
	Password       string `json:"password,omitempty" yaml:"password,omitempty"`
	KeyFile        string `json:"keyFile,omitempty" yaml:"key_file,omitempty"`
	KeyPassphrase  string `json:"keyPassphrase,omitempty" yaml:"key_passphrase,omitempty"`
	KnownHostsFile string `json:"knownHostsFile,omitempty" yaml:"known_hosts_file,omitempty"`
	Dir            string `json:"dir,omitempty" yaml:"dir,omitempty"`

	// FileName and TempSuffix together decide whether a receiver can ever see a
	// half-written file, which is the most common way an SFTP handoff corrupts data.
	FileName   string `json:"fileName,omitempty" yaml:"file_name,omitempty"`
	TempSuffix string `json:"tempSuffix,omitempty" yaml:"temp_suffix,omitempty"`
	Framed     bool   `json:"framed,omitempty" yaml:"framed,omitempty"`
	Append     bool   `json:"append,omitempty" yaml:"append,omitempty"`
	Timeout    string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

type buildSMTPDest struct {
	Host string   `json:"host,omitempty" yaml:"host,omitempty"`
	From string   `json:"from,omitempty" yaml:"from,omitempty"`
	To   []string `json:"to,omitempty" yaml:"to,omitempty"`
	CC   []string `json:"cc,omitempty" yaml:"cc,omitempty"`
	BCC  []string `json:"bcc,omitempty" yaml:"bcc,omitempty"`

	Subject string `json:"subject,omitempty" yaml:"subject,omitempty"`
	Body    string `json:"body,omitempty" yaml:"body,omitempty"`

	// Attach must be reachable from the form, because the alternative to setting it is
	// emailing a whole clinical message as the body - which the loader refuses precisely so
	// that it cannot be arrived at by leaving a field blank.
	Attach     bool   `json:"attach,omitempty" yaml:"attach,omitempty"`
	AttachName string `json:"attachName,omitempty" yaml:"attach_name,omitempty"`

	Username string `json:"username,omitempty" yaml:"username,omitempty"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`

	// StartTLS is a pointer so that "not stated" keeps the secure default. A plain bool
	// would send false for an untouched form and quietly turn encryption off.
	StartTLS *bool  `json:"starttls,omitempty" yaml:"starttls,omitempty"`
	Timeout  string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

// buildChannelDest names the channel to route to.
type buildChannelDest struct {
	Name string `json:"name" yaml:"name"`
}

// buildS3Dest is an S3 destination in the form.
//
// Credentials are here as fields because leaving them out would mean the form could not create a working
// S3 destination at all, and the honest advice - reference the environment with ${AWS_SECRET_ACCESS_KEY}
// - is offered as the placeholder rather than enforced. Refusing a literal secret would just send people
// to the text editor, where the same secret ends up in the same file with no advice at all.
type buildS3Dest struct {
	Bucket               string `json:"bucket,omitempty" yaml:"bucket,omitempty"`
	Region               string `json:"region,omitempty" yaml:"region,omitempty"`
	Key                  string `json:"key,omitempty" yaml:"key,omitempty"`
	AccessKeyID          string `json:"accessKeyId,omitempty" yaml:"access_key_id,omitempty"`
	SecretAccessKey      string `json:"secretAccessKey,omitempty" yaml:"secret_access_key,omitempty"`
	SessionToken         string `json:"sessionToken,omitempty" yaml:"session_token,omitempty"`
	Endpoint             string `json:"endpoint,omitempty" yaml:"endpoint,omitempty"`
	PathStyle            bool   `json:"pathStyle,omitempty" yaml:"path_style,omitempty"`
	ServerSideEncryption string `json:"serverSideEncryption,omitempty" yaml:"server_side_encryption,omitempty"`
	ContentType          string `json:"contentType,omitempty" yaml:"content_type,omitempty"`
	Framed               bool   `json:"framed,omitempty" yaml:"framed,omitempty"`
	Timeout              string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

// buildFTPDest is an FTP destination in the form.
type buildFTPDest struct {
	Host     string `json:"host,omitempty" yaml:"host,omitempty"`
	User     string `json:"user,omitempty" yaml:"user,omitempty"`
	Password string `json:"password,omitempty" yaml:"password,omitempty"`
	Security string `json:"security,omitempty" yaml:"security,omitempty"`
	// AllowClearPassword is offered in the form so somebody who genuinely needs it can say so, rather than
	// being sent to the text editor by a refusal they cannot act on.
	AllowClearPassword bool   `json:"allowClearPassword,omitempty" yaml:"allow_clear_password,omitempty"`
	InsecureSkipVerify bool   `json:"insecureSkipVerify,omitempty" yaml:"insecure_skip_verify,omitempty"`
	Dir                string `json:"dir,omitempty" yaml:"dir,omitempty"`
	FileName           string `json:"fileName,omitempty" yaml:"file_name,omitempty"`
	TempSuffix         string `json:"tempSuffix,omitempty" yaml:"temp_suffix,omitempty"`
	Framed             bool   `json:"framed,omitempty" yaml:"framed,omitempty"`
	Timeout            string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

// buildContract references a contract file from the form.
//
// The file is named rather than edited here. A contract is generated by perfuse contract promote and then
// pruned by hand, and a thirty-expectation editor embedded in the channel form would be a worse tool than a
// text editor while making the channel page unreadable.
type buildContract struct {
	File       string `json:"file,omitempty" yaml:"file,omitempty"`
	CheckEvery string `json:"checkEvery,omitempty" yaml:"check_every,omitempty"`
	Over       int    `json:"over,omitempty" yaml:"over,omitempty"`
}

// buildDocumentDest renders a message into a document.
type buildDocumentDest struct {
	Dir        string  `json:"dir,omitempty" yaml:"dir,omitempty"`
	Format     string  `json:"format,omitempty" yaml:"format,omitempty"`
	Template   string  `json:"template,omitempty" yaml:"template,omitempty"`
	Title      string  `json:"title,omitempty" yaml:"title,omitempty"`
	FileName   string  `json:"fileName,omitempty" yaml:"file_name,omitempty"`
	FontSize   float64 `json:"fontSize,omitempty" yaml:"font_size,omitempty"`
	Landscape  bool    `json:"landscape,omitempty" yaml:"landscape,omitempty"`
	TempSuffix string  `json:"tempSuffix,omitempty" yaml:"temp_suffix,omitempty"`
	Timeout    string  `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

// buildSOAPDest posts a SOAP request per message.
type buildSOAPDest struct {
	// TLS is the client side of the connection. See buildHTTPDest.
	TLS *buildTLS `json:"tls,omitempty" yaml:"tls,omitempty"`

	URL            string            `json:"url,omitempty" yaml:"url,omitempty"`
	Action         string            `json:"action,omitempty" yaml:"action,omitempty"`
	Version        string            `json:"version,omitempty" yaml:"version,omitempty"`
	Body           string            `json:"body,omitempty" yaml:"body,omitempty"`
	Header         string            `json:"header,omitempty" yaml:"header,omitempty"`
	Headers        map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Username       string            `json:"username,omitempty" yaml:"username,omitempty"`
	Password       string            `json:"password,omitempty" yaml:"password,omitempty"`
	FaultIsSuccess []string          `json:"faultIsSuccess,omitempty" yaml:"fault_is_success,omitempty"`
	Timeout        string            `json:"timeout,omitempty" yaml:"timeout,omitempty"`
}

// buildDICOMSource receives imaging objects.
type buildDICOMSource struct {
	Listen           string    `json:"listen,omitempty" yaml:"listen,omitempty"`
	AETitle          string    `json:"aeTitle,omitempty" yaml:"ae_title,omitempty"`
	SOPClasses       []string  `json:"sopClasses,omitempty" yaml:"sop_classes,omitempty"`
	TransferSyntaxes []string  `json:"transferSyntaxes,omitempty" yaml:"transfer_syntaxes,omitempty"`
	MaxObjectBytes   int       `json:"maxObjectBytes,omitempty" yaml:"max_object_bytes,omitempty"`
	IdleTimeout      string    `json:"idleTimeout,omitempty" yaml:"idle_timeout,omitempty"`
	TLS              *buildTLS `json:"tls,omitempty" yaml:"tls,omitempty"`

	// AllowedCallingAE lists the AE titles permitted to connect. Empty accepts anybody.
	//
	// In the form because it is the only access control DICOM has. The called title is checked already and proves
	// nothing about who is calling - it is the name a caller dials, so anybody who can reach the port can send it
	// correctly. Without this, any host on the network may push images into a clinical channel.
	AllowedCallingAE []string `json:"allowedCallingAe,omitempty" yaml:"allowed_calling_ae,omitempty"`
}

// buildDelimited configures a delimited channel.
type buildDelimited struct {
	Delimiter      string   `json:"delimiter,omitempty" yaml:"delimiter,omitempty"`
	Quote          string   `json:"quote,omitempty" yaml:"quote,omitempty"`
	Comment        string   `json:"comment,omitempty" yaml:"comment,omitempty"`
	HasHeader      bool     `json:"hasHeader,omitempty" yaml:"has_header,omitempty"`
	Columns        []string `json:"columns,omitempty" yaml:"columns,omitempty"`
	TrimSpace      bool     `json:"trimSpace,omitempty" yaml:"trim_space,omitempty"`
	Relaxed        bool     `json:"relaxed,omitempty" yaml:"relaxed,omitempty"`
	KeepBlankLines bool     `json:"keepBlankLines,omitempty" yaml:"keep_blank_lines,omitempty"`

	// Filter and Steps are the delimited pair, addressing columns by name or by #position rather than HL7 fields.
	//
	// Their own fields rather than the channel's for the same reason X12 has its own: the channel-level filter and
	// transformations compile HL7 paths, and using them on a delimited channel is refused at load because they would find no
	// segments in a row of columns and quietly do nothing.
	Filter string            `json:"filter,omitempty" yaml:"filter,omitempty"`
	Steps  []buildFormatStep `json:"transformations,omitempty" yaml:"transformations,omitempty"`

	// Split is a pointer because its default is true.
	//
	// Sending it only when false is what keeps a read-back honest: absent means "each row is its own message", and a
	// form that sent true would be stating the default rather than choosing it.
	Split *bool `json:"split,omitempty" yaml:"split,omitempty"`
}

// buildBrokerSource reads from a message broker.
type buildBrokerSource struct {
	Addr           string    `json:"addr,omitempty" yaml:"addr,omitempty"`
	Destination    string    `json:"destination,omitempty" yaml:"destination,omitempty"`
	Login          string    `json:"login,omitempty" yaml:"login,omitempty"`
	Passcode       string    `json:"passcode,omitempty" yaml:"passcode,omitempty"`
	Host           string    `json:"host,omitempty" yaml:"host,omitempty"`
	Selector       string    `json:"selector,omitempty" yaml:"selector,omitempty"`
	SubscriptionID string    `json:"subscriptionId,omitempty" yaml:"subscription_id,omitempty"`
	Heartbeat      string    `json:"heartbeat,omitempty" yaml:"heartbeat,omitempty"`
	Reconnect      string    `json:"reconnect,omitempty" yaml:"reconnect,omitempty"`
	Timeout        string    `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	TLS            *buildTLS `json:"tls,omitempty" yaml:"tls,omitempty"`
}

// buildBrokerDest publishes to a message broker.
type buildBrokerDest struct {
	Addr        string            `json:"addr,omitempty" yaml:"addr,omitempty"`
	Destination string            `json:"destination,omitempty" yaml:"destination,omitempty"`
	Login       string            `json:"login,omitempty" yaml:"login,omitempty"`
	Passcode    string            `json:"passcode,omitempty" yaml:"passcode,omitempty"`
	Host        string            `json:"host,omitempty" yaml:"host,omitempty"`
	ContentType string            `json:"contentType,omitempty" yaml:"content_type,omitempty"`
	Headers     map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Persistent  *bool             `json:"persistent,omitempty" yaml:"persistent,omitempty"`
	Timeout     string            `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	TLS         *buildTLS         `json:"tls,omitempty" yaml:"tls,omitempty"`
}

// buildKafkaSource reads from a Kafka topic.
//
// Brokers and topics are lists because Kafka's are: one bootstrap address is a single point of
// failure for starting up, and one channel legitimately reads several topics.
type buildKafkaSource struct {
	Brokers             []string        `json:"brokers,omitempty" yaml:"brokers,omitempty"`
	Topics              []string        `json:"topics,omitempty" yaml:"topics,omitempty"`
	Group               string          `json:"group,omitempty" yaml:"group,omitempty"`
	FromBeginning       bool            `json:"fromBeginning,omitempty" yaml:"from_beginning,omitempty"`
	CommitAfterDelivery *bool           `json:"commitAfterDelivery,omitempty" yaml:"commit_after_delivery,omitempty"`
	MaxMessageSize      int             `json:"maxMessageSize,omitempty" yaml:"max_message_size,omitempty"`
	SessionTimeout      string          `json:"sessionTimeout,omitempty" yaml:"session_timeout,omitempty"`
	Timeout             string          `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	SASL                *buildKafkaSASL `json:"sasl,omitempty" yaml:"sasl,omitempty"`
	TLS                 *buildTLS       `json:"tls,omitempty" yaml:"tls,omitempty"`
}

// buildKafkaDest publishes to a Kafka topic.
type buildKafkaDest struct {
	Brokers []string `json:"brokers,omitempty" yaml:"brokers,omitempty"`
	Topic   string   `json:"topic,omitempty" yaml:"topic,omitempty"`

	// Key decides the partition, and therefore decides ordering. PID-3.1 keys by patient.
	Key string `json:"key,omitempty" yaml:"key,omitempty"`

	Acks        string            `json:"acks,omitempty" yaml:"acks,omitempty"`
	Compression string            `json:"compression,omitempty" yaml:"compression,omitempty"`
	Headers     map[string]string `json:"headers,omitempty" yaml:"headers,omitempty"`
	Timeout     string            `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	SASL        *buildKafkaSASL   `json:"sasl,omitempty" yaml:"sasl,omitempty"`
	TLS         *buildTLS         `json:"tls,omitempty" yaml:"tls,omitempty"`
}

// buildKafkaSASL authenticates to a Kafka cluster.
type buildKafkaSASL struct {
	Mechanism string `json:"mechanism,omitempty" yaml:"mechanism,omitempty"`
	Username  string `json:"username,omitempty" yaml:"username,omitempty"`
	Password  string `json:"password,omitempty" yaml:"password,omitempty"`
}

// buildDICOMQuerySource polls an imaging archive with C-FIND.
//
// Missing until the graphical builder needed it, and the drift guard did not notice - it checks destinations and not sources.
// Worth recording rather than quietly fixing: a guard with a blind spot is more dangerous than no guard, because it is trusted.
type buildDICOMQuerySource struct {
	Address     string            `json:"address,omitempty" yaml:"address,omitempty"`
	CalledAE    string            `json:"calledAe,omitempty" yaml:"called_ae,omitempty"`
	CallingAE   string            `json:"callingAe,omitempty" yaml:"calling_ae,omitempty"`
	Level       string            `json:"level,omitempty" yaml:"level,omitempty"`
	PatientRoot bool              `json:"patientRoot,omitempty" yaml:"patient_root,omitempty"`
	Match       map[string]string `json:"match,omitempty" yaml:"match,omitempty"`
	Return      []string          `json:"return,omitempty" yaml:"return,omitempty"`
	Interval    string            `json:"interval,omitempty" yaml:"interval,omitempty"`
	Window      string            `json:"window,omitempty" yaml:"window,omitempty"`
	Overlap     string            `json:"overlap,omitempty" yaml:"overlap,omitempty"`
	Limit       int               `json:"limit,omitempty" yaml:"limit,omitempty"`
	Timeout     string            `json:"timeout,omitempty" yaml:"timeout,omitempty"`

	EmitOnFirstPoll *bool     `json:"emitOnFirstPoll,omitempty" yaml:"emit_on_first_poll,omitempty"`
	TLS             *buildTLS `json:"tls,omitempty" yaml:"tls,omitempty"`
}

// buildDICOMDest stores imaging objects to an archive.
type buildDICOMDest struct {
	Address          string    `json:"address,omitempty" yaml:"address,omitempty"`
	CalledAE         string    `json:"calledAe,omitempty" yaml:"called_ae,omitempty"`
	CallingAE        string    `json:"callingAe,omitempty" yaml:"calling_ae,omitempty"`
	TransferSyntaxes []string  `json:"transferSyntaxes,omitempty" yaml:"transfer_syntaxes,omitempty"`
	Timeout          string    `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	TLS              *buildTLS `json:"tls,omitempty" yaml:"tls,omitempty"`
}

// buildAttachments moves large payloads out of the stored message.
type buildAttachments struct {
	Extract    []buildAttachRule `json:"extract,omitempty" yaml:"extract,omitempty"`
	Reassemble *bool             `json:"reassemble,omitempty" yaml:"reassemble,omitempty"`
}

// buildAttachRule is one field to extract.
type buildAttachRule struct {
	Path     string `json:"path,omitempty" yaml:"path,omitempty"`
	MinBytes int    `json:"minBytes,omitempty" yaml:"min_bytes,omitempty"`
	Repeats  bool   `json:"repeats,omitempty" yaml:"repeats,omitempty"`
}

// buildSOAPSource accepts messages inside a SOAP envelope.
type buildSOAPSource struct {
	Listen            string    `json:"listen,omitempty" yaml:"listen,omitempty"`
	Path              string    `json:"path,omitempty" yaml:"path,omitempty"`
	Version           string    `json:"version,omitempty" yaml:"version,omitempty"`
	Element           string    `json:"element,omitempty" yaml:"element,omitempty"`
	Base64            bool      `json:"base64,omitempty" yaml:"base64,omitempty"`
	ResponseElement   string    `json:"responseElement,omitempty" yaml:"response_element,omitempty"`
	ResponseNamespace string    `json:"responseNamespace,omitempty" yaml:"response_namespace,omitempty"`
	WSDL              string    `json:"wsdl,omitempty" yaml:"wsdl,omitempty"`
	Token             string    `json:"token,omitempty" yaml:"token,omitempty"`
	Username          string    `json:"username,omitempty" yaml:"username,omitempty"`
	Password          string    `json:"password,omitempty" yaml:"password,omitempty"`
	MaxMessageSize    int       `json:"maxMessageSize,omitempty" yaml:"max_message_size,omitempty"`
	ReadTimeout       string    `json:"readTimeout,omitempty" yaml:"read_timeout,omitempty"`
	FaultOnNak        bool      `json:"faultOnNak,omitempty" yaml:"fault_on_nak,omitempty"`
	TLS               *buildTLS `json:"tls,omitempty" yaml:"tls,omitempty"`
	Ack               *buildAck `json:"ack,omitempty" yaml:"ack,omitempty"`
}

// buildJSDest runs a script instead of sending.
type buildJSDest struct {
	Script             string `json:"script,omitempty" yaml:"script,omitempty"`
	Timeout            string `json:"timeout,omitempty" yaml:"timeout,omitempty"`
	SuccessOnUndefined *bool  `json:"successOnUndefined,omitempty" yaml:"success_on_undefined,omitempty"`
}

type buildRetry struct {
	Attempts   int    `json:"attempts,omitempty" yaml:"attempts,omitempty"`
	Backoff    string `json:"backoff,omitempty" yaml:"backoff,omitempty"`
	MaxBackoff string `json:"maxBackoff,omitempty" yaml:"max_backoff,omitempty"`
}

type buildQueue struct {
	Enabled     bool   `json:"enabled,omitempty" yaml:"enabled,omitempty"`
	MaxAttempts int    `json:"maxAttempts,omitempty" yaml:"max_attempts,omitempty"`
	Backoff     string `json:"backoff,omitempty" yaml:"backoff,omitempty"`
	MaxBackoff  string `json:"maxBackoff,omitempty" yaml:"max_backoff,omitempty"`

	// MaxDepth bounds the queue on disk and RetainHours how long a delivered message is
	// kept. A queue with neither grows until the filesystem fills, which is a failure
	// that arrives as something unrelated breaking.
	MaxDepth    int `json:"maxDepth,omitempty" yaml:"max_depth,omitempty"`
	RetainHours int `json:"retainHours,omitempty" yaml:"retain_hours,omitempty"`
}

type buildResponse struct {
	YAML     string            `json:"yaml"`
	OK       bool              `json:"ok"`
	Problems []validateProblem `json:"problems"`
	Summary  *channelPlain     `json:"summary,omitempty"`
}

// handleBuildChannel renders a form model as YAML and validates it.
func (s *Server) handleBuildChannel(w http.ResponseWriter, r *http.Request, _ *store.Session) {
	var model buildModel
	if !s.decode(w, r, &model) {
		return
	}

	// Before marshalling, not after: an empty optional block has to be gone before it
	// reaches YAML, because "tls: {}" is indistinguishable from deliberate once written.
	model.pruneEmpty()

	text, err := marshalChannel(model)
	if err != nil {
		// A marshalling failure is a bug here rather than bad input, so it is reported
		// as one instead of being dressed up as a validation problem.
		s.failErr(w, r, err)
		return
	}

	out := buildResponse{YAML: text, Problems: []validateProblem{}}

	c, err := config.Load(bytes.NewReader([]byte(text)), "(unsaved)")
	if err != nil {
		out.Problems = problemsFromError(err)
		s.ok(w, out)
		return
	}

	out.OK = true
	out.Summary = describeChannel(c)
	s.ok(w, out)
}

// marshalChannel writes the model as YAML.
func marshalChannel(model buildModel) (string, error) {
	var buf bytes.Buffer

	enc := yaml.NewEncoder(&buf)
	// Two spaces, which is what every channel file in this repository uses and what the
	// loader's own error messages assume when they quote a line back.
	enc.SetIndent(2)

	if err := enc.Encode(model); err != nil {
		return "", err
	}
	if err := enc.Close(); err != nil {
		return "", err
	}

	return buf.String(), nil
}

// pruneEmpty removes a struct pointer that the form filled in with nothing.
//
// A form that shows an optional block leaves an empty object behind when the user opens it
// and changes their mind. Emitting "tls: {}" then means the channel has TLS configured with
// no certificate, which fails to load with a message about a missing file rather than
// saying the obvious thing, which is that TLS was not really wanted.
func (m *buildModel) pruneEmpty() {
	if m.Source.TLS != nil && !m.Source.TLS.Enabled && m.Source.TLS.CertFile == "" {
		m.Source.TLS = nil
	}
	if m.Source.Ack != nil && *m.Source.Ack == (buildAck{}) {
		m.Source.Ack = nil
	}
	if m.Source.Limits != nil && *m.Source.Limits == (buildSrcLimits{}) {
		m.Source.Limits = nil
	}
	if m.NCPDP != nil && len(m.NCPDP.Steps) == 0 {
		// An empty block would emit "ncpdp: {}", which reads as a decision somebody made rather than a form field left
		// alone.
		m.NCPDP = nil
	}
	if m.X12 != nil && m.X12.Envelope == "" && !m.X12.Split && len(m.X12.Steps) == 0 &&
		m.X12.Acknowledge == "" && m.X12.AckSenderID == "" && m.X12.AckSenderQualifier == "" {
		m.X12 = nil
	}
	// Every script slot counts, and settings that only make sense alongside one do not.
	//
	// This listed Filter and Transformer only, so a channel whose sole script was a preprocessor had its whole scripts block
	// discarded here: the form showed the text, the generated file did not contain it, and saving lost it silently.
	//
	// The identical bug was found and fixed in config.Scripts.Empty, whose comment says so - "Every script counts. Listing
	// only two here meant a channel whose only script was a preprocessor was treated as having none." That fix was never
	// carried across, so the loader learned to count all six while the builder went on counting two. The disagreement was
	// invisible until the form gained a preprocessor field, which it did tonight.
	//
	// Include, Allow, FileRoots and Timeout are deliberately not counted. Each is a setting *about* scripts, so a block
	// containing only those describes nothing and would emit a stanza nobody meant.
	if m.Scripts != nil {
		empty := true
		for _, src := range []string{
			m.Scripts.Filter, m.Scripts.Transformer, m.Scripts.Preprocessor,
			m.Scripts.Postprocessor, m.Scripts.Deploy, m.Scripts.Undeploy,
		} {
			if strings.TrimSpace(src) != "" {
				empty = false

				break
			}
		}
		if empty {
			m.Scripts = nil
		}
	}

	for i := range m.Dests {
		d := &m.Dests[i]
		if d.TLS != nil && !d.TLS.Enabled && d.TLS.CertFile == "" && d.TLS.CAFile == "" {
			d.TLS = nil
		}
		if d.Retry != nil && *d.Retry == (buildRetry{}) {
			d.Retry = nil
		}
		if d.Queue != nil && !d.Queue.Enabled && d.Queue.MaxAttempts == 0 {
			d.Queue = nil
		}
	}
}
