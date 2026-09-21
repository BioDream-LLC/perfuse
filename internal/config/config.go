// Package config loads channel definitions from files.
//
// Configuration lives in files rather than a database, which is the main thing
// Perfuse does differently from Mirth. Mirth keeps channels in its own database,
// which is why version control is a separate product, why promoting a channel
// from test to production is difficult, and why nobody can answer "what changed
// last Tuesday" without a support call. Files give git, diffs, review, CI and
// environment promotion with no extra machinery.
//
// Validation is deliberately strict and happens at load: unknown fields are an
// error, filter expressions are compiled, addresses are checked. A channel that
// cannot be understood must refuse to start rather than run in a way nobody
// intended.
package config

import (
	"errors"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/codeset"
	"github.com/biodream-llc/perfuse/internal/ncpdp"
	"github.com/biodream-llc/perfuse/internal/x12"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/biodream-llc/perfuse/internal/dicom"
	"github.com/biodream-llc/perfuse/internal/eprescribe"
	"github.com/biodream-llc/perfuse/internal/expr"
	"github.com/biodream-llc/perfuse/internal/hl7v3"
	"github.com/biodream-llc/perfuse/internal/script"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
	"github.com/biodream-llc/perfuse/internal/transform"
)

// Channel is one configured message flow: somewhere messages arrive, an optional
// filter, and one or more places they go.
type Channel struct {
	// Name identifies the channel in logs and metrics. Required and unique.
	Name string `yaml:"name"`

	// Description is free text for whoever reads this file next.
	Description string `yaml:"description,omitempty"`

	// Group is what this channel is part of, for organising a list that has grown too long to read.
	//
	// Cosmetic at five channels and structural at two hundred: a site's channels divide by the system
	// they talk to, or by the team that owns them, and a flat list of two hundred names is unusable
	// however good the rest of the interface is.
	//
	// A plain string rather than a group object with its own file, because a group has no behaviour. The
	// moment it has a file it acquires settings, and settings on a group mean a channel's behaviour
	// depends on something not written in the channel - which is the property that makes Mirth
	// configuration hard to reason about. Groups here are a label and nothing more.
	Group string `yaml:"group,omitempty"`

	// Attachments moves large payloads out of the message and puts them back before delivery.
	//
	// Opt-in per channel: a feed of plain ADT messages has nothing to extract and
	// should not pay for the machinery.
	Attachments *Attachments `yaml:"attachments,omitempty"`

	// Delimited configures a delimited channel. Only meaningful when the data type is delimited.
	Delimited *Delimited `yaml:"delimited,omitempty"`

	// Tables names files of shared mapping tables a transformation may refer to by name.
	//
	// Shared because the domain tax is largely re-deriving a mapping somebody already derived: a site's sex
	// codes and patient classes get translated identically in every channel that touches them, and a table
	// inside a channel is a table only that channel can use.
	Tables []string `yaml:"tables,omitempty"`

	// tables is the loaded set, resolved at validation.
	tables *codeset.Set

	// Contract says what this feed must look like, so a change at the sending end is reported rather than
	// discovered weeks later by a receiver falling over.
	Contract *ContractRef `yaml:"contract,omitempty"`

	// Enabled defaults to true. A disabled channel is loaded and validated but
	// not started, so a file that is temporarily off is still checked by CI.
	Enabled *bool `yaml:"enabled,omitempty"`

	// DataType is the format of the messages this channel receives. Defaults to
	// hl7, so every channel written before this existed keeps working.
	DataType DataType `yaml:"dataType,omitempty"`

	// HL7v3 holds the settings that only apply to an HL7 v3 channel.
	HL7v3 *HL7v3Options `yaml:"hl7v3,omitempty"`

	// X12 holds the settings that only mean anything on an X12 channel. Setting it
	// on any other kind is refused rather than ignored.
	X12 *X12Options `yaml:"x12,omitempty"`

	// NCPDP holds the settings that only apply to a pharmacy claim channel.
	NCPDP *NCPDPOptions `yaml:"ncpdp,omitempty"`

	// Script configures an NCPDP SCRIPT channel. Its steps live here rather than in the top-level transformations for the same
	// reason every other non-v2 format's do: the step types differ per format, so one key would mean different things depending on
	// dataType.
	Script *ScriptOptions `yaml:"script,omitempty"`

	// Imaging configures a DICOM channel's named transformation steps.
	//
	// Named Imaging rather than DICOM because Source and Destination already have a DICOM field and three fields with the same
	// name at different levels is how somebody sets the wrong one. The yaml key is dicom, which is what a person writing the file
	// expects, and the Go name is the one that stops a mistake at the call site.
	Imaging *DICOMOptions `yaml:"dicom,omitempty"`

	// Source is where messages arrive.
	Source Source `yaml:"source"`

	// Filter is an expression that must hold for a message to be forwarded.
	// A message that does not match is still acknowledged: the sender did
	// nothing wrong, we simply are not interested.
	Filter string `yaml:"filter,omitempty"`

	// Transformations are declarative changes applied to accepted messages, in
	// order, before any script runs. This is the layer meant to carry the
	// ordinary work: each step is validated when the file loads, shows up in a
	// diff, and can be reviewed by somebody who does not read JavaScript.
	Transformations []transform.Step `yaml:"transformations,omitempty"`

	// Scripts holds JavaScript for the cases the declarative steps cannot
	// express, and is what a Mirth channel's own scripts drop into unchanged.
	// A channel with a script is marked as such in the interface, because its
	// behaviour cannot be established by reading the configuration.
	Scripts *Scripts `yaml:"scripts,omitempty"`

	// Shadow runs a candidate version of this channel beside it and reports where
	// they differ. Nothing it does can reach a receiver.
	Shadow *Shadow `yaml:"shadow,omitempty"`

	// Destinations are where accepted messages are sent, in order.
	Destinations []Destination `yaml:"destinations"`

	// compiled holds the parsed filter, populated by Validate.
	compiled expr.HL7Expr

	// ncpdpFilter is the channel filter compiled against NCPDP paths, set for an ncpdp channel instead of compiled.
	ncpdpFilter expr.Expr[*ncpdp.Message]

	// scriptFilter is the channel filter compiled against a SCRIPT prescription, which is XML.
	scriptFilter expr.Expr[eprescribe.Tree]

	// x12Filter is the channel filter compiled against X12 paths, set for an X12 channel instead of compiled.
	//
	// A second field rather than a format-agnostic one, because the two have different message types and a single field
	// would have to be an interface holding either - at which point every reader has to ask which, and one of them will
	// forget.
	x12Filter expr.Expr[*x12.Message]

	// v3compiled is the parsed HL7 v3 filter, on a v3 channel.
	//
	// Separate from compiled because the two evaluate against different message models. One field would mean a
	// type assertion at the point of use, which is where a v2 channel eventually gets handed a v3 expression.
	v3compiled *hl7v3.Filter

	// pipeline holds the compiled transformations, populated by Validate.
	pipeline *transform.Pipeline

	// scriptNotes records what E4X syntax was translated, for the interface.
	scriptNotes []ScriptNote

	// path records where this channel was loaded from, for error messages.
	path string
}

// SourceType is the transport a channel listens on.
type SourceType string

// Supported source types.
const (
	SourceMLLP SourceType = "mllp"
	// SourceHTTP accepts messages posted over HTTP, which is how the modern half
	// of a hospital integrates when MLLP is not available to it.
	SourceHTTP SourceType = "http"
	// SourceDatabase polls a table. How a great many hospital interfaces start.
	SourceDatabase SourceType = "database"
	// SourceSFTP collects files from an SFTP server.
	SourceSFTP SourceType = "sftp"

	// SourceFile reads files from a directory this server can see. Mirth's File Reader, and the connector a site
	// tries first: a very large amount of real integration is a folder somebody mounted years ago.
	SourceFile SourceType = "file"

	// SourceTCP listens on a raw socket with configurable framing. Mirth's TCP Listener, and what a laboratory
	// analyser, a scale or a bedside monitor needs: equipment that speaks a socket but not MLLP.
	SourceTCP SourceType = "tcp"

	// SourceFTP collects files from an FTP or FTPS server. Mirth reaches these through its File Reader's methods;
	// here it is a connector of its own, because the settings that matter differ.
	SourceFTP SourceType = "ftp"

	// SourceSMB collects files from a Windows file share, without needing one mounted on the server.
	SourceSMB SourceType = "smb"

	// SourceWebDAV collects files from a WebDAV collection: a document store, SharePoint, or a vendor portal that
	// exposes a drop folder over HTTPS because it is the only outbound port allowed.
	SourceWebDAV SourceType = "webdav"

	// SourceSerial reads a serial port. The equipment is still there: a blood gas analyser bought in 2009 with a
	// working sensor and a 25-pin socket, a bedside monitor, a scale in a dialysis unit. None of them will ever get
	// an Ethernet port, and the alternative to reading them is somebody typing results into a form.
	SourceSerial SourceType = "serial"
	// SourceSOAP accepts messages wrapped in a SOAP envelope, which is what
	// Mirth's Web Service Listener does and what some sending systems only speak.
	SourceSOAP SourceType = "soap"
	// SourceDICOM receives imaging objects as a C-STORE provider, which is what
	// Mirth's DICOM Listener does.
	SourceDICOM SourceType = "dicom"
	// SourceDICOMQuery polls an archive with C-FIND, which Mirth cannot do at all.
	SourceDICOMQuery SourceType = "dicom_query"
	// SourceBroker reads from a message broker, which is Mirth's JMS Reader.
	SourceBroker SourceType = "broker"
	// SourceJavaScript runs a user-supplied script on a timer and feeds the
	// returned messages into the channel. This is the equivalent of Mirth's
	// JavaScript Reader.
	SourceJavaScript SourceType = "javascript"
	// SourceKafka reads from a Kafka topic, which Mirth cannot do without a
	// custom plugin. Separate from SourceBroker because Kafka's model differs
	// where it matters: partitioned ordering and a consumer-tracked position.
	SourceKafka SourceType = "kafka"
)

// Source is where a channel receives messages.
type Source struct {
	// Type is the transport. Only mllp is implemented.
	Type SourceType `yaml:"type"`

	// Listen is the bind address, for example ":6661".
	Listen string `yaml:"listen"`

	// MaxMessageSize bounds one inbound message. Zero uses the transport
	// default.
	MaxMessageSize int `yaml:"max_message_size,omitempty"`

	// IdleTimeout closes a connection that has been silent this long. Zero
	// means never, which is usually right: a hospital feed holds a connection
	// open for months and goes quiet overnight.
	IdleTimeout time.Duration `yaml:"idle_timeout,omitempty"`

	// MaxConnections bounds concurrent connections. Zero means unlimited.
	MaxConnections int `yaml:"max_connections,omitempty"`

	// TLS encrypts inbound connections, and can require a client certificate.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`

	// HTTP configures an http source.
	HTTP *HTTPSource `yaml:"http,omitempty"`

	// SOAP applies to a soap source, which accepts envelopes rather than bare
	// messages.
	SOAP *SOAPSource `yaml:"soap,omitempty"`

	// DICOM applies to a dicom source, which receives imaging objects.
	DICOM *DICOMSource `yaml:"dicom,omitempty"`

	// File configures a file source, which reads a directory on this server.
	File *FileSource `yaml:"file,omitempty"`

	// TCP configures a tcp source, which is a raw socket with configurable framing.
	TCP *TCPSource `yaml:"tcp,omitempty"`

	// FTP configures an ftp source, which polls a directory on an FTP or FTPS server.
	FTP *FTPSource `yaml:"ftp,omitempty"`

	// SMB configures an smb source, which polls a directory on a Windows file share.
	SMB *SMBSource `yaml:"smb,omitempty"`

	// WebDAV configures a webdav source, which polls a collection over HTTP.
	WebDAV *WebDAVSource `yaml:"webdav,omitempty"`

	// Serial configures a serial source, which reads a device over a cable.
	Serial *SerialSource `yaml:"serial,omitempty"`

	// DICOMQuery applies to a dicom_query source, which polls an archive rather than listening.
	DICOMQuery *DICOMQuerySource `yaml:"dicom_query,omitempty"`

	// Broker applies to a broker source.
	Broker *BrokerSource `yaml:"broker,omitempty"`

	// Kafka applies to a kafka source.
	Kafka *KafkaSource `yaml:"kafka,omitempty"`

	// Database configures a database source.
	Database *DatabaseSource `yaml:"database,omitempty"`

	// SFTP configures an sftp source.
	SFTP *SFTPSource `yaml:"sftp,omitempty"`

	// JavaScript applies to a javascript source.
	JavaScript *JavaScriptSource `yaml:"javascript,omitempty"`

	// Ack controls the acknowledgement sent back to the sender.
	Ack Ack `yaml:"ack,omitempty"`
}

// AckWhen decides when a positive acknowledgement is sent.
type AckWhen string

// Acknowledgement timing. This is the most consequential setting in a channel,
// because it decides what the sender is being promised.
const (
	// AckOnReceipt acknowledges as soon as the message is accepted and queued,
	// before any destination has been written. Fast, and it means an
	// acknowledged message can still be lost if the process dies with work
	// queued.
	AckOnReceipt AckWhen = "on_receipt"

	// AckOnDelivery acknowledges only after every enabled destination has
	// accepted the message. Slower, and an AA then means the data actually
	// arrived somewhere.
	AckOnDelivery AckWhen = "on_delivery"
)

// Ack configures acknowledgement generation.
type Ack struct {
	// When defaults to on_delivery, because promising delivery and then losing
	// the message is worse than being slow.
	When AckWhen `yaml:"when,omitempty"`

	// Application and Facility identify this engine in MSH-3 and MSH-4. When
	// both are empty the acknowledgement mirrors the original receiver, which
	// is what most senders expect.
	Application string `yaml:"application,omitempty"`
	Facility    string `yaml:"facility,omitempty"`

	// IncludeTriggerEvent sends ACK^A01 rather than ACK. Some receivers require
	// it and others reject it, so it is explicit.
	IncludeTriggerEvent bool `yaml:"include_trigger_event,omitempty"`
}

// DestinationType is where accepted messages go.
type DestinationType string

// Supported destination types.
const (
	DestinationMLLP DestinationType = "mllp"
	DestinationFile DestinationType = "file"
	// DestinationFHIR converts the message to FHIR and posts it to a FHIR
	// server. This is what turns an HL7 v2 feed into FHIR without a separate
	// integration project.
	DestinationFHIR DestinationType = "fhir"
	// DestinationCDA reads the clinical document carried inside the message,
	// converts it, and writes or posts the result. A CDA normally arrives
	// base64-encoded inside an MDM^T02, and an engine that treats that as an
	// opaque blob leaves the densest clinical payload in the feed unread.
	DestinationCDA DestinationType = "cda"
	// DestinationHTTP posts the message to an HTTP endpoint. The commonest thing a
	// Mirth channel does that Perfuse could not.
	DestinationHTTP DestinationType = "http"
	// DestinationDatabase writes each message to a table.
	DestinationDatabase DestinationType = "database"
	// DestinationSFTP writes files to an SFTP server.
	DestinationSFTP DestinationType = "sftp"
	// DestinationS3 writes each message as an object in S3 or an S3-compatible store.
	DestinationS3 DestinationType = "s3"
	// DestinationFTP writes files to an FTP or FTPS server.
	DestinationFTP DestinationType = "ftp"
	// DestinationDocument renders each message into a PDF or text document.
	DestinationDocument DestinationType = "document"
	// DestinationSOAP posts a SOAP request per message.
	DestinationSOAP DestinationType = "soap"
	// DestinationDICOM sends imaging objects as a C-STORE user, which is what
	// Mirth's DICOM Sender does.
	DestinationDICOM DestinationType = "dicom"
	// DestinationJavaScript runs a script instead of sending, which is Mirth's
	// JavaScript Writer and the escape hatch sites reach for constantly.
	DestinationJavaScript DestinationType = "javascript"
	// DestinationBroker publishes to a message broker, which is Mirth's JMS Writer.
	DestinationBroker DestinationType = "broker"
	// DestinationKafka publishes to a Kafka topic, keyed so one patient's events
	// stay in order while different patients go in parallel.
	DestinationKafka DestinationType = "kafka"
	// DestinationSMTP sends the message, or a note about it, as email. Its usual
	// purpose is not integration but notification: a coordinator told when a
	// particular order type arrives, or a daily report that a feed produced
	// nothing.
	DestinationSMTP DestinationType = "smtp"

	// DestinationTCP writes to a raw socket with configurable framing, which is Mirth's TCP Sender.
	DestinationTCP DestinationType = "tcp"

	// DestinationChannel hands the message to another channel in this server, so one channel can
	// route to several others. This is how a fan-out arrangement is built: one channel receives, and
	// each child owns a downstream system.
	DestinationChannel DestinationType = "channel"
)

// allDestinationTypes is every transport a destination may use.
//
// One list, so that a message naming the supported types cannot fall behind the types. The hand-written sentence it replaced named
// nine while seventeen worked, which told anybody who mistyped one of the other eight that their transport did not exist.
var allDestinationTypes = []DestinationType{
	DestinationMLLP,
	DestinationTCP,
	DestinationFile,
	DestinationHTTP,
	DestinationSOAP,
	DestinationDatabase,
	DestinationSFTP,
	DestinationFTP,
	DestinationS3,
	DestinationSMTP,
	DestinationFHIR,
	DestinationCDA,
	DestinationDocument,
	DestinationDICOM,
	DestinationBroker,
	DestinationKafka,
	DestinationJavaScript,
	DestinationChannel,
}

// destinationTypeNames lists the transports for a message, in the order above.
func destinationTypeNames() []string {
	out := make([]string, 0, len(allDestinationTypes))
	for _, t := range allDestinationTypes {
		out = append(out, string(t))
	}

	return out
}

// Destination is one place a channel sends messages.
type Destination struct {
	// Name identifies the destination in logs and metrics. Required within a
	// channel.
	Name string `yaml:"name"`

	// Type is the transport.
	Type DestinationType `yaml:"type"`

	// Enabled defaults to true.
	Enabled *bool `yaml:"enabled,omitempty"`

	// Filter narrows which messages reach this destination, on top of the
	// channel filter.
	Filter string `yaml:"filter,omitempty"`

	// Address is the remote host:port for an mllp destination.
	Address string `yaml:"address,omitempty"`

	// Dir is the output directory for a file destination.
	Dir string `yaml:"dir,omitempty"`

	// TLS encrypts the connection to this destination.
	TLS *tlsconf.Settings `yaml:"tls,omitempty"`

	// FHIR configures a fhir destination.
	FHIR *FHIRDestination `yaml:"fhir,omitempty"`

	// CDA configures a cda destination.
	CDA *CDADestination `yaml:"cda,omitempty"`

	// HTTP configures an http destination.
	HTTP *HTTPDestination `yaml:"http,omitempty"`

	// Database configures a database destination.
	Database *DatabaseDestination `yaml:"database,omitempty"`

	// SFTP configures an sftp destination.
	SFTP *SFTPDestination `yaml:"sftp,omitempty"`

	// TCP configures a tcp destination: a raw socket with configurable framing.
	TCP *TCPDest `yaml:"tcp,omitempty"`

	// SMTP configures an email destination.
	SMTP *SMTPDestination `yaml:"smtp,omitempty"`

	// Channel applies to a channel destination.
	Channel *ChannelDestination `yaml:"channel,omitempty"`

	// S3 applies to an s3 destination.
	S3 *S3Destination `yaml:"s3,omitempty"`

	// FTP applies to an ftp destination.
	FTP *FTPDestination `yaml:"ftp,omitempty"`

	// Document applies to a document destination.
	Document *DocumentDestination `yaml:"document,omitempty"`

	// SOAP applies to a soap destination.
	SOAP *SOAPDestination `yaml:"soap,omitempty"`

	// DICOM applies to a dicom destination.
	DICOM *DICOMDestination `yaml:"dicom,omitempty"`

	// JavaScript applies to a javascript destination.
	JavaScript *JavaScriptDestination `yaml:"javascript,omitempty"`

	// Broker applies to a broker destination.
	Broker *BrokerDestination `yaml:"broker,omitempty"`

	// Kafka applies to a kafka destination.
	Kafka *KafkaDestination `yaml:"kafka,omitempty"`

	// ResponseTransformer inspects what the receiver said back and may mark the delivery failed.
	//
	// It exists because "did this arrive" is often not a question the transport can answer: an MLLP
	// receiver returns an application acknowledgement whose meaning is in its text, and an HTTP
	// receiver returns 200 with an error document. In both cases the transport succeeded and the
	// message did not arrive.
	//
	// Only meaningful on a destination that receives a reply. On one that cannot, it is refused at
	// load rather than left never running.
	ResponseTransformer string `yaml:"response_transformer,omitempty"`

	// Timeout bounds one delivery attempt.
	Timeout time.Duration `yaml:"timeout,omitempty"`

	// Retry controls what happens when a delivery fails.
	Retry Retry `yaml:"retry,omitempty"`

	// Queue makes delivery durable: a message that cannot be delivered now is
	// kept on disk and retried until it can be. Off by default.
	Queue *QueueConfig `yaml:"queue,omitempty"`

	compiled expr.HL7Expr

	// x12compiled is the destination filter compiled against X12 paths, set for an X12 channel instead of compiled.
	x12compiled expr.Expr[*x12.Message]

	// ncpdpcompiled is the destination filter compiled against NCPDP paths.
	ncpdpcompiled expr.Expr[*ncpdp.Message]
}

// FHIRDestination configures conversion to FHIR and delivery to a FHIR server.
type FHIRDestination struct {
	// URL is the base URL of the FHIR server, for example
	// "https://fhir.example.org/fhir". The bundle is posted to it directly.
	URL string `yaml:"url"`

	// Version is the FHIR release to produce: R4, R4B or R5. Defaults to R5, the
	// latest published release. A deployment talking to an EHR almost always
	// wants R4 and should say so, because silently downgrading would hide a real
	// interoperability decision.
	Version string `yaml:"version,omitempty"`

	// Timezone is applied to HL7 v2 timestamps that carry no offset. v2 permits a
	// bare local time and FHIR does not, so something has to supply one.
	// Defaults to UTC, which is at least explicit.
	Timezone string `yaml:"timezone,omitempty"`

	// DefaultIdentifierSystem namespaces identifiers whose assigning authority
	// has no configured URI. Without a system, an MRN is ambiguous between
	// facilities.
	DefaultIdentifierSystem string `yaml:"default_identifier_system,omitempty"`

	// IdentifierSystems maps an HL7 assigning authority to a system URI.
	IdentifierSystems map[string]string `yaml:"identifier_systems,omitempty"`

	// ClaimUSCore adds US Core profile URLs to the resources produced. Only set
	// it once the output has actually been checked, because asserting a profile
	// that does not hold is worse than asserting none.
	ClaimUSCore bool `yaml:"claim_us_core,omitempty"`

	// ValidateBeforeSend refuses to post a bundle that fails validation.
	// Defaults to true: sending a resource a server will reject wastes a retry
	// budget and hides the real problem behind a transport error.
	ValidateBeforeSend *bool `yaml:"validate_before_send,omitempty"`

	// RejectOnWarning also refuses on warnings, such as a missing US Core
	// identifier. Off by default, because a warning is a judgement about profile
	// conformance rather than about validity.
	RejectOnWarning bool `yaml:"reject_on_warning,omitempty"`

	// Headers are added to the request, for an API key or a tenant selector.
	Headers map[string]string `yaml:"headers,omitempty"`

	// BearerToken is sent as an Authorization header. Prefer an environment
	// variable reference over a literal in a file that goes into git.
	BearerToken string `yaml:"bearer_token,omitempty"`
}

// ShouldValidate reports whether a bundle is validated before sending.
func (f *FHIRDestination) ShouldValidate() bool {
	return f == nil || f.ValidateBeforeSend == nil || *f.ValidateBeforeSend
}

// Retry controls redelivery after a failure.
type Retry struct {
	// Attempts is the total number of tries, including the first. Zero means
	// the default.
	Attempts int `yaml:"attempts,omitempty"`

	// Backoff is the wait before the second attempt. It doubles each time, up
	// to MaxBackoff.
	Backoff time.Duration `yaml:"backoff,omitempty"`

	// MaxBackoff caps the growing delay.
	MaxBackoff time.Duration `yaml:"max_backoff,omitempty"`
}

// Defaults applied when a field is left unset.
const (
	DefaultRetryAttempts   = 5
	DefaultRetryBackoff    = time.Second
	DefaultRetryMaxBackoff = time.Minute
	DefaultDestTimeout     = 30 * time.Second
)

// IsEnabled reports whether the channel should run. Absent means enabled.
func (c *Channel) IsEnabled() bool { return c.Enabled == nil || *c.Enabled }

// Path returns the file this channel was loaded from.
func (c *Channel) Path() string { return c.path }

// FilterExpr returns the compiled channel filter, or nil when there is none.
func (c *Channel) FilterExpr() expr.HL7Expr { return c.compiled }

// NCPDPFilter returns the channel filter compiled for NCPDP, or nil when there is none.
func (c *Channel) NCPDPFilter() expr.Expr[*ncpdp.Message] { return c.ncpdpFilter }

// ScriptFilter returns the compiled SCRIPT filter, or nil when there is none.
func (c *Channel) ScriptFilter() expr.Expr[eprescribe.Tree] { return c.scriptFilter }

// X12Filter returns the channel filter compiled for X12, or nil when there is none.
func (c *Channel) X12Filter() expr.Expr[*x12.Message] { return c.x12Filter }

// HL7v3Filter returns the compiled v3 filter, or nil when there is none.
//
// Nil is a working value: hl7v3.Filter's Match passes everything on a nil receiver, so the engine needs no special case - which is
// where a nil check gets forgotten and every message gets dropped.
func (c *Channel) HL7v3Filter() *hl7v3.Filter { return c.v3compiled }

// HL7v3Steps returns the compiled v3 transformations, or nil when there are none.
func (c *Channel) HL7v3Steps() *hl7v3.Steps { return c.HL7v3.Steps() }

// ScriptSteps returns the compiled SCRIPT transformations, or nil when there are none.
func (c *Channel) ScriptSteps() *hl7v3.Steps { return c.Script.Steps() }

// DICOMSteps returns the validated imaging transformations, or nil when there are none.
func (c *Channel) DICOMSteps() []dicom.Step { return c.Imaging.Steps() }

// IsEnabled reports whether the destination should receive messages.
func (d *Destination) IsEnabled() bool { return d.Enabled == nil || *d.Enabled }

// FilterExpr returns the compiled destination filter, or nil when there is none.
func (d *Destination) FilterExpr() expr.HL7Expr { return d.compiled }

// HasFilter reports whether a filter was configured, whichever format it was compiled for.
//
// Asked before evaluating, so a destination carrying a filter the channel cannot evaluate fails loudly instead of being
// delivered to as though there were no filter at all.
func (d *Destination) HasFilter() bool { return strings.TrimSpace(d.Filter) != "" }

// NCPDPFilterExpr returns the destination filter compiled for NCPDP, or nil when there is none.
func (d *Destination) NCPDPFilterExpr() expr.Expr[*ncpdp.Message] { return d.ncpdpcompiled }

// X12FilterExpr returns the destination filter compiled for X12, or nil when there is none.
func (d *Destination) X12FilterExpr() expr.Expr[*x12.Message] { return d.x12compiled }

// EnabledDestinations returns the destinations that will actually be written to.
func (c *Channel) EnabledDestinations() []Destination {
	out := make([]Destination, 0, len(c.Destinations))
	for _, d := range c.Destinations {
		if d.IsEnabled() {
			out = append(out, d)
		}
	}
	return out
}

// Paths lists every message path this channel reads, so a channel can state its
// own data dependencies.
func (c *Channel) Paths() []string {
	seen := map[string]bool{}
	var out []string
	add := func(e expr.HL7Expr) {
		if e == nil {
			return
		}
		for _, p := range e.Paths() {
			if !seen[p] {
				seen[p] = true
				out = append(out, p)
			}
		}
	}
	add(c.compiled)
	for i := range c.Destinations {
		add(c.Destinations[i].compiled)
	}
	sort.Strings(out)
	return out
}

// Load reads one channel definition.
func Load(r io.Reader, path string) (*Channel, error) {
	dec := yaml.NewDecoder(r)
	// An unknown field is an error rather than a shrug. A misspelled key that is
	// silently ignored produces a channel that looks configured and is not,
	// which is the worst possible outcome for a filter or a retry policy.
	dec.KnownFields(true)

	var c Channel
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	// A second document in the file is almost certainly a mistake, and silently
	// ignoring half a file would be worse than saying so.
	var extra Channel
	if err := dec.Decode(&extra); err == nil {
		return nil, fmt.Errorf("%s: contains more than one document; put one channel per file", path)
	} else if !isEOF(err) {
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	c.path = path
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// LoadFile reads one channel definition from disk.
func LoadFile(path string) (*Channel, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Load(f, path)
}

// sidecarSuffixes are files that live beside a channel and are not channels.
//
// Matched on a compound suffix rather than a separate directory, because keeping them apart would mean somebody
// editing an interface has to look in two places, and the point of files-on-disk is that everything about a
// channel is where you would expect it.
var sidecarSuffixes = []string{
	".contract.yaml",
	".contract.yml",
	".codeset.yaml",
	".codeset.yml",
	// Channel tests already use these, and they were being loaded as channels too - which nobody had noticed
	// because tests live in their own directory by convention rather than by rule.
	"_test.yaml",
	"_test.yml",
	".test.yaml",
	".test.yml",
}

// isSidecar reports whether a path is a companion file rather than a channel.
func isSidecar(path string) bool {
	lower := strings.ToLower(filepath.Base(path))
	for _, suffix := range sidecarSuffixes {
		if strings.HasSuffix(lower, suffix) {
			return true
		}
	}
	return false
}

// LoadDir reads every channel definition in a directory tree.
//
// Names must be unique across the whole set, because they identify channels in
// logs and metrics and two channels called the same thing make an incident
// impossible to diagnose.
func LoadDir(root string) ([]*Channel, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		switch strings.ToLower(filepath.Ext(path)) {
		case ".yaml", ".yml":
			// A contract belongs beside the channel it describes, so the two travel together into version
			// control and a review shows both the change to the interface and the change to what is expected
			// of it. That means the loader has to know the difference, or it tries to load a contract as a
			// channel and refuses the whole directory - which is exactly what happened the first time this
			// was tried on a real layout.
			if isSidecar(path) {
				return nil
			}
			paths = append(paths, path)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no .yaml or .yml channel files found in %s", root)
	}
	sort.Strings(paths)

	var channels []*Channel
	byName := map[string]string{}
	for _, path := range paths {
		c, err := LoadFile(path)
		if err != nil {
			return nil, err
		}
		if prev, dup := byName[c.Name]; dup {
			return nil, fmt.Errorf("%s: channel name %q is already used by %s", path, c.Name, prev)
		}
		byName[c.Name] = path
		channels = append(channels, c)
	}

	// Routing can only be checked once every channel is known. A destination naming a channel that
	// does not exist, or a cycle, is refused here rather than discovered at run time - where the
	// first looks like traffic being lost and the second fills the disk.
	if errs := ValidateRouting(channels); len(errs) > 0 {
		var b strings.Builder
		b.WriteString("routing between channels is not valid:")
		for _, err := range errs {
			b.WriteString("\n  ")
			b.WriteString(err.Error())
		}
		return nil, errors.New(b.String())
	}

	return channels, nil
}

func isEOF(err error) bool {
	return err == io.EOF || strings.Contains(err.Error(), "EOF")
}

// Pipeline returns the compiled declarative transformations, or nil when the
// channel has none.
func (c *Channel) Pipeline() *transform.Pipeline { return c.pipeline }

// ScriptEngine returns the channel's script engine, or nil.
func (c *Channel) ScriptEngine() *script.Engine {
	if c.Scripts == nil {
		return nil
	}
	return c.Scripts.Engine()
}

// FilterScript returns the compiled filter script, or nil.
func (c *Channel) FilterScript() *script.Script {
	if c.Scripts == nil {
		return nil
	}
	return c.Scripts.FilterScript()
}

// TransformerScript returns the compiled transformer script, or nil.
func (c *Channel) TransformerScript() *script.Script {
	if c.Scripts == nil {
		return nil
	}
	return c.Scripts.TransformerScript()
}

// ScriptNotes reports the E4X constructs that were translated when this channel's
// scripts were compiled.
func (c *Channel) ScriptNotes() []ScriptNote { return c.scriptNotes }

// HasScripts reports whether the channel's behaviour depends on JavaScript. The
// interface uses this to mark the channel, because a script means the
// configuration alone no longer explains what the channel does.
func (c *Channel) HasScripts() bool { return !c.Scripts.Empty() }

// AllDestinationTypes lists every destination type.
//
// Exists so a test can walk them and fail on one nobody has decided about, rather than checking a list somebody had to
// remember to update. Two guards on this project have already been quietly incomplete - one covered destinations but not
// sources, and one could not see the failure it was grepping for - so a list that has to be maintained by hand next to
// the thing it describes is treated as a liability here.
func AllDestinationTypes() []DestinationType {
	return []DestinationType{
		DestinationMLLP,
		DestinationHTTP,
		DestinationFHIR,
		DestinationSOAP,
		DestinationFile,
		DestinationDocument,
		DestinationSFTP,
		DestinationFTP,
		DestinationS3,
		DestinationSMTP,
		DestinationDatabase,
		DestinationChannel,
		DestinationDICOM,
		DestinationJavaScript,
		DestinationBroker,
	}
}
