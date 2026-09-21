// Package engine runs configured channels.
//
// A channel is a source, a filter, and a set of destinations. The runtime's job
// is to be honest about what happened to each message: an acknowledgement should
// mean what the configuration says it means, a message that is filtered out
// should be distinguishable from one that was delivered, and a message that
// could not be delivered anywhere must not be reported as accepted.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/biodream-llc/perfuse/hl7"
	"github.com/biodream-llc/perfuse/internal/admit"
	"github.com/biodream-llc/perfuse/internal/attach"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/dicom"
	"github.com/biodream-llc/perfuse/internal/metrics"
	"github.com/biodream-llc/perfuse/internal/script"
	"github.com/biodream-llc/perfuse/internal/shadow"
	"github.com/biodream-llc/perfuse/internal/tlsconf"
	"github.com/biodream-llc/perfuse/internal/trace"
	"github.com/biodream-llc/perfuse/mllp"
)

// Outcome is what happened to one received message.
type Outcome string

// Message outcomes. These are separate on purpose: "we chose not to take it" and
// "we could not take it" look the same to a sender that only sees an AA, and
// telling them apart is the difference between a quiet afternoon and a lost
// afternoon.
const (
	// Delivered means every destination that wanted the message accepted it.
	Delivered Outcome = "delivered"
	// Filtered means the channel filter rejected the message. Nothing was sent
	// anywhere, and the sender is still told AA: it did nothing wrong.
	Filtered Outcome = "filtered"
	// PartiallyDelivered means at least one destination accepted and at least
	// one failed.
	PartiallyDelivered Outcome = "partial"
	// Failed means no destination accepted the message.
	Failed Outcome = "failed"
	// Unparseable means the bytes were not an HL7 message.
	Unparseable Outcome = "unparseable"
	// Queued means the message could not be delivered now but is on disk and will
	// be retried. The sender is told AA, because the bytes are committed before the
	// acknowledgement goes out and we have therefore accepted responsibility.
	// Reporting an error instead would make a working store-and-forward queue look
	// like a fault and invite the sender to resend what we already hold.
	Queued Outcome = "queued"
)

// Recorder receives a record of every message a channel handled.
//
// The engine does not know about storage: it hands over what happened and lets
// something else decide whether to keep it. That keeps the message path free of
// database concerns and means a deployment that must not store clinical content
// simply supplies no recorder.
type Recorder interface {
	RecordMessage(ctx context.Context, record MessageRecord)
}

// MessageRecord is what a channel reports about one message.
type MessageRecord struct {
	Channel      string
	ReceivedAt   time.Time
	Raw          []byte
	ControlID    string
	MessageType  string
	TriggerEvent string
	Sender       string
	Segments     int

	// Attachments are payloads extracted from Raw and replaced with tokens, so the recorder can store them once by
	// content rather than inline in every message that carries them.
	//
	// Carried on the record rather than written by the channel, because the channel has no database handle - and
	// giving it one would put storage on the acknowledgement path, which is exactly what the recorder exists to
	// avoid.
	Attachments []attach.Attachment

	// arrivedSize is the length of the message as received, captured before extraction so throughput does not appear
	// to fall because storage got cleverer.
	arrivedSize int

	// Transformed reports whether the message was changed on the way through, and
	// Changes how many declarative steps took effect. Both are recorded because
	// the most expensive failure in an interface is a transformation nobody knew
	// was happening.
	Transformed bool
	Changes     int
	Outcome     Outcome
	AckCode     string
	Duration    time.Duration
	Error       string
	Deliveries  []DeliveryRecord
}

// DeliveryRecord is the outcome at one destination.
type DeliveryRecord struct {
	Destination string
	// Status is "delivered", "failed" or "filtered".
	Status   string
	Attempts int
	Duration time.Duration
	Error    string
}

// Sender delivers a message to one destination.
//
// An implementation must be safe for concurrent use and must respect the
// context deadline. Returning an error means the attempt failed and may be
// retried.
type Sender interface {
	Send(ctx context.Context, msg []byte) error
	// Describe is used in logs and errors.
	Describe() string
	// Close releases any resources.
	Close() error
}

// destination pairs configuration with its live sender.
type destination struct {
	cfg    config.Destination
	sender Sender

	// responseScript is the compiled response transformer, nil when this destination has none.
	responseScript *script.Script
}

// Channel is a running channel.
type Channel struct {
	cfg   *config.Channel
	dests []*destination
	log   *slog.Logger

	// ackSeq distinguishes one X12 acknowledgement from the next.
	//
	// A partner that sees a duplicate interchange control number usually discards the interchange as an accidental
	// resend, so a fixed number would mean only the first acknowledgement of the day was ever read.
	ackSeq atomic.Int64

	// recorder is optional. When nil, nothing is stored.
	recorder Recorder

	// metrics is optional; nil disables recording.
	metrics *metrics.Collector

	// queues is the durable queue coordinator, nil when nothing uses one.
	queues *Queues

	// admit bounds how many deliveries are in flight, process-wide and per destination.
	//
	// Shared between every channel on purpose: the descriptor budget it protects belongs to the process,
	// not to a channel. Nil is unlimited, which is what a test or a single-channel run gets.
	admit *admit.Controller

	server     *mllp.Server
	httpServer *http.Server

	// dicomServer is the imaging listener, nil unless the source is dicom.
	dicomServer *dicom.Server

	// poller drives a database source. Nil for every other source type.
	poller *databasePoller

	// sftp drives an sftp source. Nil for every other source type.
	sftp *sftpPoller

	// files is the poller for a file source, and later for every other file transport.
	files *filePoller

	// tcpSrc is the listener for a raw socket source.
	tcpSrc *tcpListener

	// serial is the reader for a serial port source.
	serial *serialReader

	// shadow compares a candidate channel against this one. Nil unless configured.
	// tenantID labels this channel's metrics. Empty in single-tenant operation.
	tenantID string

	// queryState remembers what a DICOM query source has already seen. Nil when the channel does not poll an archive.
	queryState DICOMQueryState

	// dicomQuery is the archive polling goroutine, nil unless this is a dicom_query source.
	dicomQuery *dicomQueryPoller

	// broker is the message broker reader, nil unless this is a broker source.
	broker *brokerPoller

	// kafka is the Kafka consumer, nil unless this is a kafka source.
	kafka *kafkaPoller

	// jsReader is the JavaScript Reader, nil unless this is a javascript source.
	jsReader *jsReader

	// tracer is nil when tracing is off, and a nil *Tracer is a working no-op, so
	// nothing on the message path needs to check.
	tracer *trace.Tracer

	shadow *shadow.Runner
	// shadowChannel is the candidate. Its senders were never constructed.
	shadowChannel *Channel

	mu    sync.Mutex
	stats Stats

	// lastOutcome is read by the channel test runner.
	lastOutcome lastOutcome
}

// Stats counts what a channel has done. Counters only, so reading them is cheap
// and they can be exposed without locking anything interesting.
type Stats struct {
	// Received is messages accepted from the source, and the unit is a message rather than a delivery from the sender's point of
	// view. That distinction only bites on delimited channels, where one document becomes one message per row: a four-hundred-row
	// file counts four hundred, not one.
	//
	// Recorded here because it was a consequence rather than a choice - rows were chosen so a filter could drop one bad row
	// instead of a whole file, and the counting followed - and because a site whose dashboard predates a delimited channel would
	// read the change as a hundredfold traffic increase. The header row is not counted; it is structure.
	Received    int64
	Delivered   int64
	Filtered    int64
	Partial     int64
	Failed      int64
	Unparseable int64
	// DatabaseQuarantined counts rows abandoned by a database source.
	DatabaseQuarantined int64
	// Queued counts messages accepted for later delivery. Separate from Delivered
	// because a dashboard that adds them together hides a backlog.
	Queued        int64
	DestDelivered map[string]int64
	DestFailed    map[string]int64
	DestFiltered  map[string]int64
	DestQueued    map[string]int64
}

func newStats() Stats {
	return Stats{
		DestDelivered: map[string]int64{},
		DestFailed:    map[string]int64{},
		DestFiltered:  map[string]int64{},
		DestQueued:    map[string]int64{},
	}
}

// SenderFactory builds a Sender for a destination. Injectable so the runtime can
// be tested without opening sockets.
type SenderFactory func(config.Destination) (Sender, error)

// DataTypeAware is implemented by senders whose output depends on the message format.
//
// An optional interface rather than a change to Sender, so the senders for which the
// format makes no difference - HTTP posts the bytes it is given, a database stores them -
// need no code at all. It follows the SetMetrics and SetRecorder pattern already used
// here: attached after construction, before anything is sent.
type DataTypeAware interface {
	SetDataType(config.DataType)
}

// SetRecorder attaches a recorder. It must be called before Start.
func (c *Channel) SetRecorder(r Recorder) { c.recorder = r }

// SetTracer attaches a tracer. A nil tracer is valid and disables tracing, which
// is why nothing on the message path guards against it.
//
// Set after construction rather than passed in, matching SetMetrics: tracing is
// an operational concern that every existing caller and test should not have to
// mention.
func (c *Channel) SetTracer(t *trace.Tracer) { c.tracer = t }

// SetAdmit gives this channel the process-wide delivery limiter.
//
// Must be the same controller every channel gets, or each will believe it has the whole descriptor budget
// to itself and the limit will be the number of channels multiplied by what the process can afford. A
// channel without one is unlimited, which is right for a test and wrong for a server.
func (c *Channel) SetAdmit(a *admit.Controller) { c.admit = a }

// NewChannel prepares a channel to run. Nothing is listening until Start.
func NewChannel(cfg *config.Channel, factory SenderFactory, log *slog.Logger) (*Channel, error) {
	if log == nil {
		log = slog.Default()
	}
	if factory == nil {
		factory = DefaultSenderFactory
	}

	ch := &Channel{
		cfg:   cfg,
		log:   log.With("channel", cfg.Name),
		stats: newStats(),
	}

	for _, d := range cfg.EnabledDestinations() {
		resolved := d.Resolved()
		sender, err := factory(resolved)
		if err != nil {
			ch.closeSenders()
			return nil, fmt.Errorf("channel %q destination %q: %w", cfg.Name, d.Name, err)
		}
		// A sender that cares about the message format is told before it is used.
		// Without this an X12 channel wrote claims into a .hl7 file wrapped in MLLP
		// control characters - a corrupt archive that reported success.
		if aware, ok := sender.(DataTypeAware); ok {
			aware.SetDataType(cfg.Type())
		}

		dest := &destination{cfg: resolved, sender: sender}

		// A script destination is built here rather than by the factory, because the factory sees only the destination
		// and this needs the channel's script engine.
		if resolved.Type == config.DestinationJavaScript {
			engine := cfg.Scripts.Engine()
			if engine == nil {
				return nil, fmt.Errorf(
					"destination %q runs a script but the channel has no script engine; the channel was not validated",
					resolved.Name)
			}
			compiled, err := engine.Compile(
				cfg.Name+" "+resolved.Name, resolved.JavaScript.Script, script.Writer)
			if err != nil {
				return nil, fmt.Errorf("destination %q: %w", resolved.Name, err)
			}
			sender = &JavaScriptSender{
				cfg:     resolved.JavaScript,
				name:    resolved.Name,
				channel: cfg.Name,
				script:  compiled,
				engine:  engine,
				log:     log,
			}
			dest.sender = sender
		}

		if src := strings.TrimSpace(resolved.ResponseTransformer); src != "" {
			engine := cfg.Scripts.Engine()
			if engine == nil {
				// Validation creates the engine when any destination has a response transformer, so
				// reaching here means the channel was built without being validated.
				return nil, fmt.Errorf(
					"destination %q has a response transformer but the channel has no script engine; "+
						"the channel was not validated", resolved.Name)
			}

			compiled, err := engine.Compile(
				cfg.Name+" "+resolved.Name+" response", src, script.Filter)
			if err != nil {
				return nil, err
			}
			dest.responseScript = compiled

			// Refused here rather than at send time. A response transformer on a sender that cannot
			// report a reply would never run, and the file plainly contains it.
			if _, ok := sender.(Responder); !ok {
				return nil, fmt.Errorf(
					"destination %q has a response transformer but its transport reports no reply to "+
						"inspect", resolved.Name)
			}
		}

		ch.dests = append(ch.dests, dest)
	}

	if len(ch.dests) == 0 {
		return nil, fmt.Errorf("channel %q has no enabled destinations", cfg.Name)
	}
	return ch, nil
}

// Name returns the channel name.
func (c *Channel) Name() string { return c.cfg.Name }

// Stats returns a snapshot of the counters.
func (c *Channel) Stats() Stats {
	c.mu.Lock()
	defer c.mu.Unlock()

	out := newStats()
	out.Received = c.stats.Received
	out.Delivered = c.stats.Delivered
	out.Filtered = c.stats.Filtered
	out.Partial = c.stats.Partial
	out.Failed = c.stats.Failed
	out.Unparseable = c.stats.Unparseable
	out.DatabaseQuarantined = c.stats.DatabaseQuarantined
	for k, v := range c.stats.DestDelivered {
		out.DestDelivered[k] = v
	}
	for k, v := range c.stats.DestFailed {
		out.DestFailed[k] = v
	}
	for k, v := range c.stats.DestFiltered {
		out.DestFiltered[k] = v
	}
	return out
}

// Start begins listening. It returns once the listener is up.
func (c *Channel) Start() error {
	// The deploy script runs first, before anything binds. A channel whose deploy script fails should
	// not start at all: it usually means something it depends on is unreachable, and accepting
	// messages it cannot handle is worse than refusing to start with the reason.
	if err := c.runLifecycle("deploy", c.cfg.Scripts.DeployScript()); err != nil {
		return err
	}

	// Before the source, so a candidate that cannot even be loaded is reported when
	// somebody starts the channel rather than on the first message.
	if err := c.startShadow(config.LoadFile); err != nil {
		return err
	}

	src := c.cfg.Source
	switch src.Type {
	case config.SourceMLLP:

	case config.SourceTCP:
		if err := c.startTCPSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceFile:
		if err := c.startFileSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceFTP:
		if err := c.startFTPSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceSMB:
		if err := c.startSMBSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceWebDAV:
		if err := c.startWebDAVSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceSerial:
		if err := c.startSerialSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceSFTP:
		if err := c.startSFTPSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceDatabase:
		if err := c.startDatabaseSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceHTTP:
		if err := c.startHTTPSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceBroker:
		if err := c.startBrokerSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceKafka:
		if err := c.startKafkaSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceDICOMQuery:
		if err := c.startDICOMQuerySource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceDICOM:
		if err := c.startDICOMSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceSOAP:
		// Shares the HTTP server field, and therefore the stop path, because it is an HTTP listener that happens to
		// speak SOAP. Two fields would mean two shutdown paths and one of them eventually being forgotten.
		if err := c.startSOAPSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil

	case config.SourceJavaScript:
		if err := c.startJavaScriptSource(); err != nil {
			return err
		}
		c.resumeQueues(context.Background())
		return nil
	default:
		return fmt.Errorf("channel %q: source type %q is not implemented", c.cfg.Name, src.Type)
	}

	// TLS is built at start rather than at load, because loading a certificate
	// touches the filesystem and validation should stay a pure check of the file.
	tlsCfg, err := tlsconf.ForListener(src.TLS)
	if err != nil {
		return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
	}
	for _, warning := range src.TLS.Warnings(true) {
		c.log.Warn("tls", "detail", warning)
	}

	c.server = &mllp.Server{
		Addr:           src.Listen,
		TLSConfig:      tlsCfg,
		MaxMessageSize: src.MaxMessageSize,
		IdleTimeout:    src.IdleTimeout,
		MaxConnections: src.MaxConnections,
		Logger:         c.log,
		Handler:        mllp.HandlerFunc(c.handle),
	}

	started := make(chan error, 1)
	go func() {
		if err := c.server.ListenAndServe(); err != nil {
			started <- err
			return
		}
		started <- nil
	}()

	// Pick up anything left in the queue by a previous run. Without this the rows
	// survive a restart but nothing looks at them until a new message happens to
	// arrive for that destination, so an overnight outage leaves the backlog
	// sitting untouched until morning.
	c.resumeQueues(context.Background())

	// Give the listener a moment to fail on a bound port, so a misconfigured
	// channel reports at startup rather than looking healthy and being deaf.
	select {
	case err := <-started:
		if err != nil {
			return fmt.Errorf("channel %q: %w", c.cfg.Name, err)
		}
		return nil
	case <-time.After(150 * time.Millisecond):
		return nil
	}
}

// Stop shuts the channel down, waiting for messages in flight.
func (c *Channel) Stop(ctx context.Context) error {
	var err error

	// The undeploy script runs after everything else has stopped, so it sees a channel that is
	// genuinely idle. Its failure is logged and does not become the stop's error: a channel that could
	// not be shut down because a cleanup script threw would be worse than one that logged and closed.
	defer func() {
		if scriptErr := c.runLifecycle("undeploy", c.cfg.Scripts.UndeployScript()); scriptErr != nil {
			c.log.Error("the undeploy script failed", "err", scriptErr, "channel", c.cfg.Name)
		}

		// After the undeploy script, so an undeploy written as a WebAssembly module still has a runtime to run in.
		//
		// This frees mapped executable memory that Go's collector does not account for, so skipping it would grow the
		// process by one runtime per module on every config reload - unbounded, and invisible in the memory figures
		// anybody would think to check. Logged rather than returned for the same reason the script failure is: a channel
		// that could not be shut down cleanly is worse than one that logged and closed.
		if releaseErr := c.cfg.Scripts.Release(); releaseErr != nil {
			c.log.Error("releasing compiled script resources failed", "err", releaseErr, "channel", c.cfg.Name)
		}
	}()

	if c.server != nil {
		err = c.server.Shutdown(ctx)
	}
	if shadowErr := c.stopShadow(); shadowErr != nil && err == nil {
		err = shadowErr
	}
	if brokerErr := c.stopBrokerSource(); brokerErr != nil && err == nil {
		err = brokerErr
	}
	if kafkaErr := c.stopKafkaSource(); kafkaErr != nil && err == nil {
		err = kafkaErr
	}
	if queryErr := c.stopDICOMQuerySource(); queryErr != nil && err == nil {
		err = queryErr
	}
	if serialErr := c.stopSerialSource(); serialErr != nil && err == nil {
		err = serialErr
	}
	if tcpErr := c.stopTCPSource(); tcpErr != nil && err == nil {
		err = tcpErr
	}
	if fileErr := c.stopFileSource(); fileErr != nil && err == nil {
		err = fileErr
	}
	if sftpErr := c.stopSFTPSource(); sftpErr != nil && err == nil {
		err = sftpErr
	}
	if dbErr := c.stopDatabaseSource(); dbErr != nil && err == nil {
		err = dbErr
	}
	// Stopped alongside the HTTP listener rather than in a separate branch, so a source type added later cannot be
	// forgotten here - a listener that outlives its channel keeps a port bound and accepts studies nothing will deliver.
	if dicomErr := c.stopDICOMSource(ctx); dicomErr != nil && err == nil {
		err = dicomErr
	}

	if httpErr := c.stopHTTPSource(ctx); httpErr != nil && err == nil {
		err = httpErr
	}
	c.stopJavaScriptSource()
	c.closeSenders()
	return err
}

func (c *Channel) closeSenders() {
	for _, d := range c.dests {
		if d.sender != nil {
			_ = d.sender.Close()
		}
	}
}

// handle processes one received message and returns the acknowledgement.
func (c *Channel) handle(ctx context.Context, raw []byte) ([]byte, error) {
	// X12 is handled separately, before anything else here runs.
	//
	// The two differ in the place that matters most: X12 has no synchronous
	// acknowledgement, and much of what follows exists to decide which one to send.
	// Threading that through would mean a nil acknowledgement travelling through code
	// written on the assumption that one always exists, and the first symptom would be
	// an empty MSA segment reaching a hospital.
	if c.cfg.Type() == config.DataX12 {
		return c.handleX12(ctx, raw)
	}

	// Same reasoning as X12: an imaging object has no segments or fields, so it needs its own path rather than a nil
	// parsed message travelling through code that assumes one exists. Found by sending a real study through and watching
	// it rejected as "input does not start with an MSH segment".
	if c.cfg.Type() == config.DataDICOM {
		return c.handleDICOM(ctx, raw)
	}

	// Delimited data needs its own path for the same reason, plus one of its own: a single arriving document can become many
	// messages, which none of the other paths do.
	if c.cfg.Type() == config.DataDelimited {
		return c.handleDelimited(ctx, raw)
	}

	// HL7 v3 sits between the others: it has a synchronous acknowledgement like v2, and no segments or fields like DICOM. It
	// gets its own path for the DICOM reason - a nil parsed v2 message travelling through code that assumes one exists
	// produces something obscure at the far end rather than an error here.
	if c.cfg.Type() == config.DataHL7v3 {
		return c.handleHL7v3(ctx, raw)
	}

	// Raw is the shortest path: no parse, no acknowledgement, no structure to inspect. It has to come before the HL7
	// work below for the DICOM reason - a nil parsed v2 message travelling through code that assumes one exists produces
	// something obscure at the far end rather than an error here.
	if c.cfg.Type() == config.DataRaw {
		return c.handleRaw(ctx, raw)
	}

	// The pharmacy formats. Same reason again - neither has HL7 segments - with one difference from raw: these are parsed,
	// so a truncated prescription or a claim with a shifted header stops here rather than at a pharmacy.
	if t := c.cfg.Type(); t == config.DataNCPDP || t == config.DataScript {
		return c.handlePharmacy(ctx, raw)
	}

	// Deferred, so the comparison happens once every message has finished the live
	// path however it finished, including the paths that return early. Putting it at
	// each return site is how a shadow ends up silently missing the filtered and
	// unparseable messages, which are exactly the ones a filter change affects.
	//
	// It runs after the acknowledgement bytes are built, so nothing here is on the
	// path that decides what the sender is told.
	if c.shadow != nil {
		defer c.observeShadow(ctx, raw)
	}

	// One root span with a deferred End covers every exit from this function,
	// including the early returns for unparseable input and filter faults. The
	// alternative - a span per return site - is how the interesting failures end up
	// being the ones with no trace.
	//
	// The remote context comes from whatever the source could parse. For HTTP that
	// is a traceparent header; MLLP has nowhere to put one, so those messages start
	// a new trace.
	ctx, span := c.tracer.Start(ctx, "channel.handle", trace.KindServer, trace.RemoteFromContext(ctx))
	defer span.End()
	span.SetString("perfuse.channel", c.cfg.Name)

	started := time.Now()
	c.count(func(s *Stats) { s.Received++ })

	record := MessageRecord{
		Channel:    c.cfg.Name,
		ReceivedAt: started.UTC(),
		Raw:        raw,
	}

	// The preprocessor runs before parsing, which is the whole reason it exists: it is the only place
	// a message that does not parse can be repaired.
	//
	// record.Raw deliberately keeps what actually arrived rather than what the preprocessor produced.
	// The stored message is evidence of what a sender sent, and a replay or a trace of it should go
	// through the preprocessor again rather than start from something already changed.
	if pre := c.cfg.Scripts.PreprocessorScript(); pre != nil {
		replaced, err := c.runPreprocessor(ctx, pre, raw)
		if err != nil {
			// Rejected with the script's own complaint rather than parsing the original anyway.
			// Falling back would hide a broken preprocessor for as long as the messages happened to
			// parse without it, and the day one did not would look like a sender problem.
			c.count(func(s *Stats) { s.Failed++ })
			c.log.Error("the preprocessor failed", "err", err, "bytes", len(raw))

			record.Outcome = Failed
			c.lastOutcome.set(Failed)
			span.SetString("perfuse.outcome", string(Failed))
			record.AckCode = string(hl7.AckError)
			record.Error = err.Error()
			span.SetError(err)
			record.Duration = time.Since(started)
			c.record(ctx, record)

			return hl7.AckFor(err, hl7.AckOptions{
				SendingApplication: c.cfg.Source.Ack.Application,
				SendingFacility:    c.cfg.Source.Ack.Facility,
			}), nil
		}
		if replaced != nil {
			raw = replaced
		}
	}

	m, err := hl7.Parse(raw)
	if err != nil {
		c.count(func(s *Stats) { s.Unparseable++ })
		c.log.Warn("rejected unparseable message", "err", err, "bytes", len(raw))

		record.Outcome = Unparseable
		c.lastOutcome.set(Unparseable)
		span.SetString("perfuse.outcome", string(Unparseable))
		record.AckCode = string(hl7.AckReject)
		record.Error = err.Error()
		span.SetError(err)
		record.Duration = time.Since(started)
		c.record(ctx, record)

		// The sender still needs an answer, or it retries for ever.
		return hl7.AckFor(err, hl7.AckOptions{
			SendingApplication: c.cfg.Source.Ack.Application,
			SendingFacility:    c.cfg.Source.Ack.Facility,
		}), nil
	}

	typ, event, _ := m.Type()
	record.MessageType = typ
	record.TriggerEvent = event
	record.ControlID = m.ControlID()
	record.Segments = m.SegmentCount()
	if msh, ok := m.Segment("MSH", 1); ok {
		record.Sender = msh.Field(4).String()
	}

	// Message type, trigger event and control ID only. No field values: an
	// attribute set from message content is how patient data reaches a third-party
	// observability platform that has no agreement covering it. The control ID is
	// the identifier every support conversation opens with, and it is metadata
	// rather than clinical content.
	span.SetString("hl7.message_type", typ)
	span.SetString("hl7.trigger_event", event)
	span.SetString("hl7.control_id", m.ControlID())
	span.SetInt("hl7.segments", int64(m.SegmentCount()))

	log := c.log.With("type", typ, "event", event, "control_id", m.ControlID())

	// Channel filter. A message that does not match is not an error: the sender
	// did nothing wrong, we are simply not interested, so it gets an AA.
	if f := c.cfg.FilterExpr(); f != nil {
		match, err := f.Eval(m)
		if err != nil {
			// A filter that cannot decide is a configuration fault, not the
			// sender's fault. Refusing the message is honest: it makes the sender
			// retry and keeps the data at its origin rather than dropping it here.
			c.count(func(s *Stats) { s.Failed++ })
			log.Error("channel filter failed to evaluate", "err", err)

			record.Outcome = Failed
			c.lastOutcome.set(Failed)
			span.SetString("perfuse.outcome", string(Failed))
			record.AckCode = string(hl7.AckError)
			record.Error = "filter could not be evaluated: " + err.Error()
			span.SetError(err)
			record.Duration = time.Since(started)
			c.record(ctx, record)

			return m.Ack(c.ackOpts(hl7.AckError, "filter could not be evaluated")), nil
		}
		if !match {
			c.count(func(s *Stats) { s.Filtered++ })
			log.Debug("filtered out by the channel filter")

			record.Outcome = Filtered
			c.lastOutcome.set(Filtered)
			span.SetString("perfuse.outcome", string(Filtered))
			record.AckCode = string(hl7.AckAccept)
			record.Duration = time.Since(started)
			c.record(ctx, record)

			return m.Ack(c.ackOpts(hl7.AckAccept, "")), nil
		}
	}

	// Transformations and scripts. This runs after the expression filter so that
	// a message which was never wanted is not transformed first, and before
	// delivery so that every destination sees the same transformed message.
	if hasTransformStage(c.cfg) {
		staged, err := c.runTransformStage(m, raw)
		if err != nil {
			// A transformation fault is ours, not the sender's. Refusing the
			// message keeps the data at its origin, where it can be resent once
			// the channel is fixed; forwarding it half-transformed would put
			// something in the receiver that the configuration says should never
			// have been sent.
			c.count(func(s *Stats) { s.Failed++ })
			log.Error("transformation failed", "err", err)

			record.Outcome = Failed
			c.lastOutcome.set(Failed)
			span.SetString("perfuse.outcome", string(Failed))
			record.AckCode = string(hl7.AckError)
			record.Error = err.Error()
			record.Duration = time.Since(started)
			c.record(ctx, record)

			return m.Ack(c.ackOpts(hl7.AckError, "transformation failed")), nil
		}

		if !staged.Accepted {
			c.count(func(s *Stats) { s.Filtered++ })
			log.Debug("filtered out by a script", "by", staged.RejectedBy)

			record.Outcome = Filtered
			c.lastOutcome.set(Filtered)
			span.SetString("perfuse.outcome", string(Filtered))
			record.AckCode = string(hl7.AckAccept)
			record.Duration = time.Since(started)
			c.record(ctx, record)

			return m.Ack(c.ackOpts(hl7.AckAccept, "")), nil
		}

		if len(staged.Changes) > 0 {
			log.Debug("message transformed", "changes", len(staged.Changes))
		}

		// Everything downstream works on the transformed message.
		m, raw = staged.Message, staged.Raw
		record.Transformed = len(staged.Changes) > 0 || c.cfg.TransformerScript() != nil
		record.Changes = len(staged.Changes)
	}

	// on_receipt acknowledges before delivery is attempted, which is fast and
	// means an acknowledged message can still be lost. The configuration says
	// which promise is being made; the runtime just keeps it.
	if c.cfg.Source.AckWhen() == config.AckOnReceipt {
		go func() {
			bg := context.WithoutCancel(ctx)
			outcome, failures, deliveries := c.deliver(bg, m, raw, log, hl7DestFilter(m))
			record.Outcome = outcome
			record.Deliveries = deliveries
			record.AckCode = string(hl7.AckAccept)
			if len(failures) > 0 {
				record.Error = joinErrors(failures)
			}
			record.Duration = time.Since(started)
			c.record(bg, record)
		}()
		return m.Ack(c.ackOpts(hl7.AckAccept, "")), nil
	}

	outcome, failures, deliveries := c.deliver(ctx, m, raw, log, hl7DestFilter(m))
	c.lastOutcome.set(outcome)
	span.SetString("perfuse.outcome", string(outcome))
	record.Outcome = outcome
	record.Deliveries = deliveries
	record.Duration = time.Since(started)
	if len(failures) > 0 {
		record.Error = joinErrors(failures)
	}

	switch outcome {
	case Delivered, Filtered, Queued:
		// Queued is an accepted message. The bytes are committed to disk before the
		// acknowledgement is written, so the promise implied by AA is one we can
		// keep.
		record.AckCode = string(hl7.AckAccept)
		c.record(ctx, record)
		return m.Ack(c.ackOpts(hl7.AckAccept, "")), nil

	case PartiallyDelivered:
		// Some destinations have the message and some do not. Reporting AA would
		// tell the sender everything is fine and lose the difference.
		record.AckCode = string(hl7.AckError)
		c.record(ctx, record)
		return m.Ack(c.ackOpts(hl7.AckError,
			fmt.Sprintf("delivered to some destinations; %s", joinErrors(failures)))), nil

	default:
		record.AckCode = string(hl7.AckError)
		c.record(ctx, record)
		return m.Ack(c.ackOpts(hl7.AckError, joinErrors(failures))), nil
	}
}

// originalSize is the message's length as it arrived, whether or not attachments were later moved out.
func (r *MessageRecord) originalSize() int {
	if r.arrivedSize > 0 {
		return r.arrivedSize
	}
	return len(r.Raw)
}

// record hands the outcome to the recorder, if there is one.
//
// A storage failure must not fail the message: the data has already been
// delivered, and refusing the acknowledgement would make the sender resend
// something that arrived. So the error is logged and the message stands.
func (c *Channel) record(ctx context.Context, r MessageRecord) {
	// The postprocessor runs here because this is the one point every path reaches, whatever happened
	// to the message - delivered, filtered, failed, unparseable. Calling it at each return site is how
	// a hook ends up silently missing the failures, which are the ones people write postprocessors to
	// be told about.
	//
	// Deferred so it runs after the message has been recorded, and so an early return below does not
	// skip it.
	defer c.runPostprocessor(ctx, &r)

	// Metrics are derived from the same record the store gets, so the dashboard and
	// the message browser cannot disagree about what happened. They are recorded
	// before the early return below, because metrics and message storage are
	// independent choices: a site that turns off payload storage for privacy
	// reasons still needs to know its throughput.
	// Extraction happens here, at the one point every path reaches, and deliberately after delivery rather than
	// before. Two consequences worth being explicit about:
	//
	// The size recorded below is the message as it arrived, not the shrunken one, because that is what the sender
	// sent and throughput should not appear to drop because storage got cleverer.
	//
	// Destinations receive the original bytes untouched. This saves the message store, which grows with retention
	// and is the thing that grows without bound; it does not shrink the durable queue, which holds full payloads for
	// as long as a retry needs them. That is a smaller and self-limiting cost, and paying for it avoids threading a
	// payload lookup through every sender.
	r.arrivedSize = len(r.Raw)
	if c.cfg.Attachments != nil && len(r.Raw) > 0 {
		if result, err := attach.Extract(r.Raw, c.cfg.Attachments.Extract); err != nil {
			// Logged, never fatal. The message has already been delivered; refusing to store it well is not a
			// reason to fail anything.
			c.log.Warn("could not extract attachments", "err", err)
		} else if result.Extracted > 0 {
			r.Raw = result.Message
			r.Attachments = result.Attachments
			c.log.Debug("moved attachments out of the stored message",
				"count", result.Extracted, "bytes_saved", result.Saved)
		}
	}

	c.recordOutcome(r.Outcome, r.originalSize(), r.Duration)
	c.recordTransform(r.Changes, r.Outcome == Failed && r.Transformed)
	for _, d := range r.Deliveries {
		c.recordDelivery(d.Destination, d.Attempts, d.Duration, d.Status == "failed")
	}

	if c.recorder == nil {
		return
	}
	c.recorder.RecordMessage(ctx, r)
}

// deliver fans the message out to every destination and reports the aggregate
// outcome.
// hl7DestFilter evaluates a destination filter against an HL7 v2 message.
func hl7DestFilter(m *hl7.Message) destinationFilter {
	return func(d *config.Destination) (bool, error) {
		f := d.FilterExpr()
		if f == nil {
			// The filter was configured but did not compile as HL7. Refusing beats delivering, which would ignore an
			// exclusion the configuration asked for.
			return false, fmt.Errorf("the filter was not compiled for this channel's data type")
		}
		if m == nil {
			return false, fmt.Errorf("there is no parsed HL7 message to evaluate against")
		}
		return f.Eval(m)
	}
}

// destinationFilter decides whether one destination's filter passes.
//
// Supplied by the caller rather than read off the destination inside deliver, because the expression is compiled against
// whichever format the channel carries - an HL7 message, an X12 interchange, later a pharmacy transaction - and delivery
// should not have to know which. Nil means this channel has no destination filters to evaluate.
type destinationFilter func(*config.Destination) (bool, error)

func (c *Channel) deliver(ctx context.Context, m *hl7.Message, raw []byte, log *slog.Logger, destFilter destinationFilter) (Outcome, []error, []DeliveryRecord) {
	type result struct {
		name     string
		err      error
		sent     bool
		queued   bool
		attempts int
		duration time.Duration
	}

	results := make([]result, len(c.dests))
	var wg sync.WaitGroup

	for i, d := range c.dests {
		// A destination filter decides whether this destination wants the
		// message at all.
		if d.cfg.HasFilter() {
			if destFilter == nil {
				// A destination carries a filter and this channel supplied no way to evaluate it. Failing loudly beats
				// skipping it, which would deliver a message the configuration says to exclude - silently, and only on
				// the channels nobody thought about.
				results[i] = result{name: d.cfg.Name, err: fmt.Errorf("filter: this channel cannot evaluate a destination filter")}
				continue
			}

			match, err := destFilter(&d.cfg)
			if err != nil {
				results[i] = result{name: d.cfg.Name, err: fmt.Errorf("filter: %w", err)}
				continue
			}
			if !match {
				results[i] = result{name: d.cfg.Name}
				c.count(func(s *Stats) { s.DestFiltered[d.cfg.Name]++ })
				continue
			}
		}

		// A destination whose queue already holds something must not be given this
		// message directly, or it would overtake what is waiting. See
		// queueing.go: reordering an HL7 feed produces a discharge for a patient
		// the receiver never admitted.
		if c.queueBlocked(ctx, d) {
			if err := c.enqueue(ctx, d, m, raw, "the queue is draining"); err != nil {
				results[i] = result{name: d.cfg.Name, err: err, sent: true, attempts: 0}
				continue
			}
			results[i] = result{name: d.cfg.Name, queued: true}
			continue
		}

		wg.Add(1)
		go func(i int, d *destination) {
			defer wg.Done()

			// A span per destination, so a trace shows which one was slow rather
			// than one aggregate figure for a fan-out. Destinations run
			// concurrently, so these are siblings and their durations overlap -
			// which is the useful shape, because it makes a single slow receiver
			// obvious next to three fast ones.
			dctx, dspan := c.tracer.Start(ctx, "destination.send", trace.KindClient, trace.SpanContext{})
			defer dspan.End()
			dspan.SetString("perfuse.destination", d.cfg.Name)
			dspan.SetString("perfuse.destination_type", string(d.cfg.Type))

			// A slot before a socket.
			//
			// Every delivery in flight holds a file descriptor here and a connection at the far end, for as
			// long as the retry budget allows - with the defaults, nearly three minutes. Without a bound,
			// a few hundred senders against one receiver that has gone quiet exhausts the process, and the
			// symptom surfaces somewhere unrelated as "too many open files".
			//
			// The wait is the destination's own timeout. If a slot cannot be had in the time one attempt is
			// allowed, this receiver is not keeping up with what is already aimed at it, and the honest
			// answer is the same as for a receiver that will not answer: queue the message. Waiting longer
			// would only make the sender wait longer for an acknowledgement it is going to be told to retry.
			slot, admitErr := c.admit.Acquire(dctx, c.cfg.Name+"/"+d.cfg.Name, d.cfg.Timeout)
			if admitErr != nil {
				dspan.SetError(admitErr)
				if d.cfg.Queue.IsEnabled() {
					if qErr := c.enqueue(dctx, d, m, raw, admitErr.Error()); qErr == nil {
						dspan.SetBool("perfuse.queued", true)
						results[i] = result{name: d.cfg.Name, queued: true}
						return
					}
				}
				results[i] = result{name: d.cfg.Name, err: admitErr, sent: true, attempts: 0}
				return
			}
			defer slot()

			start := time.Now()
			err, attempts := c.sendWithRetry(dctx, d, raw, log)
			dspan.SetInt("perfuse.attempts", int64(attempts))
			if err != nil {
				dspan.SetError(err)
			}

			// Inline retries are exhausted. If this destination has a queue, the
			// message goes there instead of being lost.
			if err != nil && d.cfg.Queue.IsEnabled() {
				if qErr := c.enqueue(dctx, d, m, raw, err.Error()); qErr == nil {
					// Queued rather than delivered, said explicitly: a trace that
					// showed this as a success would hide a growing backlog.
					dspan.SetBool("perfuse.queued", true)
					results[i] = result{
						name: d.cfg.Name, queued: true,
						attempts: attempts, duration: time.Since(start),
					}
					return
				} else {
					// Could not queue either. Report the original failure and why
					// the queue did not save it, because "queue full" and "receiver
					// down" call for different actions.
					err = fmt.Errorf("%w (and could not be queued: %v)", err, qErr)
				}
			}

			results[i] = result{
				name: d.cfg.Name, err: err, sent: true,
				attempts: attempts, duration: time.Since(start),
			}
		}(i, d)
	}
	wg.Wait()

	var attempted, succeeded, queued int
	var failures []error
	var records []DeliveryRecord

	for _, r := range results {
		if r.queued {
			// Accepted for later delivery. Counted separately from delivered,
			// because a dashboard that shows them as the same thing hides a backlog.
			queued++
			records = append(records, DeliveryRecord{
				Destination: r.name, Status: "queued",
				Attempts: r.attempts, Duration: r.duration,
			})
			continue
		}
		if !r.sent && r.err == nil {
			if r.name != "" {
				records = append(records, DeliveryRecord{
					Destination: r.name, Status: "filtered",
				})
			}
			continue // filtered out for this destination
		}
		attempted++
		if r.err == nil {
			succeeded++
			c.count(func(s *Stats) { s.DestDelivered[r.name]++ })
			records = append(records, DeliveryRecord{
				Destination: r.name, Status: "delivered",
				Attempts: r.attempts, Duration: r.duration,
			})
			continue
		}
		c.count(func(s *Stats) { s.DestFailed[r.name]++ })
		failures = append(failures, fmt.Errorf("%s: %w", r.name, r.err))
		records = append(records, DeliveryRecord{
			Destination: r.name, Status: "failed",
			Attempts: r.attempts, Duration: r.duration, Error: r.err.Error(),
		})
	}

	switch {
	case attempted == 0 && queued == 0:
		// Every destination filtered it out. Nothing was sent and nothing
		// failed, which is a normal outcome and not a loss.
		c.count(func(s *Stats) { s.Filtered++ })
		log.Debug("every destination filtered the message out")
		return Filtered, nil, records

	case queued > 0 && succeeded == attempted:
		// Nothing failed outright: what did not go out directly is on disk. The
		// message is not lost, so this is not an error, but it is not a plain
		// delivery either and the distinction is the whole point of the queue.
		c.count(func(s *Stats) { s.Queued++ })
		log.Info("queued for later delivery",
			"queued", queued, "delivered", succeeded)
		return Queued, nil, records

	case succeeded == attempted:
		c.count(func(s *Stats) { s.Delivered++ })
		log.Info("delivered", "destinations", succeeded)
		return Delivered, nil, records

	case succeeded > 0:
		c.count(func(s *Stats) { s.Partial++ })
		log.Error("delivered to some destinations only",
			"delivered", succeeded, "failed", attempted-succeeded)
		return PartiallyDelivered, failures, records

	default:
		c.count(func(s *Stats) { s.Failed++ })
		log.Error("delivery failed to every destination", "destinations", attempted)
		return Failed, failures, records
	}
}

// sendWithRetry attempts delivery, backing off between tries.
func (c *Channel) sendWithRetry(ctx context.Context, d *destination, raw []byte, log *slog.Logger) (error, int) {
	retry := d.cfg.Retry
	backoff := retry.Backoff

	var lastErr error
	attempt := 1
	for ; attempt <= retry.Attempts; attempt++ {
		attemptCtx, cancel := context.WithTimeout(ctx, d.cfg.Timeout)
		err := c.sendOnce(attemptCtx, d, raw)
		cancel()

		if err == nil {
			if attempt > 1 {
				log.Info("delivered after retrying",
					"destination", d.cfg.Name, "attempts", attempt)
			}
			return nil, attempt
		}
		lastErr = err

		// Do not sleep after the final attempt, and stop immediately if the
		// engine is shutting down.
		if attempt == retry.Attempts {
			break
		}
		if ctx.Err() != nil {
			return fmt.Errorf("%w (shutting down after %d attempt(s))", err, attempt), attempt
		}

		log.Warn("delivery attempt failed, retrying",
			"destination", d.cfg.Name, "attempt", attempt,
			"of", retry.Attempts, "retry_in", backoff, "err", err)

		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return fmt.Errorf("%w (shutting down after %d attempt(s))", err, attempt), attempt
		}

		backoff *= 2
		if backoff > retry.MaxBackoff {
			backoff = retry.MaxBackoff
		}
	}

	return fmt.Errorf("after %d attempt(s): %w", retry.Attempts, lastErr), retry.Attempts
}

func (c *Channel) ackOpts(code hl7.AckCode, text string) hl7.AckOptions {
	ack := c.cfg.Source.Ack
	return hl7.AckOptions{
		Code:                code,
		Text:                text,
		SendingApplication:  ack.Application,
		SendingFacility:     ack.Facility,
		IncludeTriggerEvent: ack.IncludeTriggerEvent,
	}
}

func (c *Channel) count(f func(*Stats)) {
	c.mu.Lock()
	f(&c.stats)
	c.mu.Unlock()
}

func joinErrors(errs []error) string {
	if len(errs) == 0 {
		return "delivery failed"
	}
	return errors.Join(errs...).Error()
}
