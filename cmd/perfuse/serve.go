package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"github.com/biodream-llc/perfuse/internal/trace"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/biodream-llc/perfuse/internal/admit"
	"github.com/biodream-llc/perfuse/internal/alerts"
	"github.com/biodream-llc/perfuse/internal/api"
	"github.com/biodream-llc/perfuse/internal/branding"
	"github.com/biodream-llc/perfuse/internal/config"
	"github.com/biodream-llc/perfuse/internal/dicomstate"
	"github.com/biodream-llc/perfuse/internal/egress"
	"github.com/biodream-llc/perfuse/internal/engine"
	"github.com/biodream-llc/perfuse/internal/fhir"
	"github.com/biodream-llc/perfuse/internal/fhirserver"
	"github.com/biodream-llc/perfuse/internal/ldap"
	"github.com/biodream-llc/perfuse/internal/metrics"
	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/oidc"
	"github.com/biodream-llc/perfuse/internal/peers"
	"github.com/biodream-llc/perfuse/internal/queue"
	"github.com/biodream-llc/perfuse/internal/settings"
	"github.com/biodream-llc/perfuse/internal/sqlitedb"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/web"
	"github.com/biodream-llc/perfuse/internal/webauthn"
)

// Default addresses for the console.
//
// Two of them, because the port has to agree with the scheme. 8443 is an HTTPS port by
// convention and browsers now act on that convention by trying TLS first, so serving plain
// HTTP there produces a certificate error rather than a page. 8080 is the plain-HTTP
// counterpart and carries no such expectation.
const (
	defaultPlainAddr = "127.0.0.1:8080"
	defaultTLSAddr   = "127.0.0.1:8443"
)

func cmdServe(args []string, stdout, stderr io.Writer) error {
	fset := flag.NewFlagSet("serve", flag.ContinueOnError)
	fset.SetOutput(stderr)

	addr := fset.String("addr", "", "address to serve the interface on "+
		"(default "+defaultPlainAddr+", or "+defaultTLSAddr+" with -tls-cert)")
	channelsDir := fset.String("channels", "./channels", "directory holding channel files")
	allowMetadataEgress := fset.Bool("allow-metadata-egress", false,
		"permit destinations pointed at cloud instance metadata addresses, which hold this machine's credentials")
	multiTenant := fset.Bool("multi-tenant", false,
		"keep each tenant's channels in their own subdirectory of -channels, so one server can run "+
			"integration for several organisations without them being able to see each other")
	dbPath := fset.String("db", "./perfuse.db", "database file for users, messages and stored resources")
	settingsFile := fset.String("settings", "",
		"settings file the web interface may edit (default: perfuse-settings.yaml beside the database)")
	certFile := fset.String("tls-cert", "", "TLS certificate file")

	// A separate pair for signing documents, falling back to the TLS pair.
	//
	// Separate because they are genuinely different keys doing different jobs. A TLS certificate names a hostname
	// and proves a document passed through this server; a signing certificate names a person or an organisation and
	// asserts that they took responsibility for the content. Using one for the other is a compromise, and the
	// signing response says so - but the compromise should be a fallback rather than the only option.
	signCertFile := fset.String("signing-cert", "", "certificate for signing documents (defaults to -tls-cert)")
	signKeyFile := fset.String("signing-key", "", "key for signing documents (defaults to -tls-key)")
	keyFile := fset.String("tls-key", "", "TLS private key file")
	insecure := fset.Bool("insecure", false, "serve over plain HTTP on a non-loopback address (not recommended)")
	jsonLogs := fset.Bool("json-logs", false, "emit structured JSON logs")
	peersFile := fset.String("peers", "", "YAML file listing other Perfuse instances to show in a fleet view")
	oidcFile := fset.String("oidc", "", "YAML file configuring OpenID Connect sign-in")
	ldapFile := fset.String("ldap", "", "YAML file configuring directory (LDAP) sign-in")
	samlFile := fset.String("saml", "", "YAML file configuring SAML 2.0 sign-in")
	fleetLabel := fset.String("fleet-label", "", "how this instance names itself in a fleet view (default: hostname)")

	runEngine := fset.Bool("engine", true,
		"run the channel engine in this process, so the interface can start, stop and monitor channels")
	storeMessages := fset.Bool("store-messages", true,
		"record every message handled, which is what makes the message browser work")
	storePayloads := fset.Bool("store-payloads", true,
		"store message contents as well as their outcomes; turn this off where PHI must not be held at rest")
	retention := fset.Int("retention-days", 30,
		"how long to keep recorded messages; 0 keeps them for ever, which will eventually fill the disk")

	// The scrape endpoint was unauthenticated, which is the convention and was fine when Perfuse ran one operator's own
	// channels. On a shared server a metric label names a channel and a channel name usually names the system at the
	// other end, so an open endpoint tells anybody who can reach the port about every customer's interfaces.
	// SCIM provisioning. Off by default: these endpoints create and delete accounts, so an installation that is not being
	// provisioned by an identity provider should not expose them at all - an endpoint nobody uses is one nobody is
	// watching, and this one hands out access.
	// Passkeys. Off unless the relying party identifier is given, because there is no safe default: guessing it from a
	// request's Host header would let whoever controls DNS decide what a credential is bound to, which is the one thing
	// the value exists to prevent.
	passkeyRoots := fset.String("passkey-attestation-roots", "",
		"a PEM file of authenticator vendor certificates; passkey attestation is verified against these")
	passkeyModels := fset.String("passkey-allowed-models", "",
		"comma-separated authenticator AAGUIDs that may enrol; requires -passkey-attestation-roots")
	passkeyRPID := fset.String("passkey-domain", "",
		"the bare domain passkeys are registered for, such as perfuse.example.org. No scheme, no port, no "+
			"path. Setting it turns passkeys on; leaving it empty leaves them off")
	passkeyOrigins := fset.String("passkey-origins", "",
		"comma-separated origins the console is reached at, with scheme and port: "+
			"https://perfuse.example.org. Defaults to https:// plus the domain")
	passkeyName := fset.String("passkey-name", "Perfuse",
		"the name an authenticator shows a person when they register a passkey")

	scimEnabled := fset.Bool("scim", false,
		"accept SCIM 2.0 provisioning from an identity provider at /scim/v2. Okta, Entra ID and Google "+
			"Workspace use it to create accounts and, more importantly, to disable them when somebody leaves")
	scimDefaultRole := fset.String("scim-default-role", "viewer",
		"role for an account the identity provider does not describe. Defaults downward on purpose: an "+
			"account whose role could not be determined should be able to read and nothing else")

	metricsToken := fset.String("metrics-token", "",
		"credential the Prometheus scrape endpoint requires. Give the scraper the same value "+
			"in bearer_token_file. Without this, only a signed-in operator can read /metrics")
	metricsOpen := fset.Bool("metrics-open", false,
		"serve /metrics to anybody who can reach the port. Only safe where that port is genuinely "+
			"private: metric labels name your channels, and on a multi-tenant server they name every tenant's")

	alertWebhook := fset.String("alert-webhook", "",
		"POST alerts to this URL as JSON. Works with Slack, Teams, Mattermost, "+
			"Alertmanager or anything that accepts a webhook. The payload carries "+
			"channel names, counts and thresholds; never message content")
	alertsFile := fset.String("alerts", "",
		"YAML file of alert rules. Without it a conservative default set is used")
	alertSeverity := fset.String("alert-severity", "warning",
		"minimum severity to send to the webhook: warning or critical")

	serveFHIR := fset.Bool("fhir", true, "serve a FHIR endpoint at /fhir")
	// Defaults to what the resource structs are actually shaped to, not to the newest release.
	//
	// This served R5 while the US Core resource types are R4 shapes, so a client read fhirVersion 5.0.0 from the
	// capability statement, looked for MedicationRequest.medication as a CodeableReference, found nothing, and rendered an
	// empty medication list. No error anywhere - just a patient who appears to take no medications. See
	// fhir.ResourceShapeVersion, and versionagree_test.go which fails if these two disagree again.
	//
	// R5 remains selectable for a client that asks for it and knows what it is getting.
	fhirVersionFlag := fset.String("fhir-version", string(fhir.ResourceShapeVersion),
		"FHIR release to serve: R4, R4B or R5")
	docPath := fset.String("inventory", "",
		"write a Markdown inventory of every interface to this file, and keep it current")
	docEvery := fset.Duration("inventory-every", 24*time.Hour,
		"how often to rewrite the interface inventory")
	fhirBulkExport := fset.Bool("fhir-bulk-export", false,
		"enable $export, which produces a file holding every record the FHIR endpoint serves")
	fhirReadOnly := fset.Bool("fhir-read-only", false, "refuse writes to the FHIR endpoint")
	fhirOpen := fset.Bool("fhir-open", false,
		"serve the FHIR endpoint with no authentication at all, exposing every stored record")
	smartIssuer := fset.String("smart-issuer", "",
		"accept SMART on FHIR access tokens from this authorization server")
	smartAudience := fset.String("smart-audience", "",
		"the audience a SMART token must be issued for, normally the FHIR base URL")
	smartAuthorize := fset.String("smart-authorize", "",
		"the authorization endpoint to advertise to SMART apps")
	smartToken := fset.String("smart-token", "",
		"the token endpoint to advertise to SMART apps")
	// Zero by default, because outside an orchestrator a drain delay is just a
	// slower Ctrl-C. Set it to slightly more than the load balancer's probe
	// interval so the instance is out of rotation before it stops listening.
	drainFor := fset.Duration("drain-for", 0, "after SIGTERM, report not-ready for this long before shutting down (for rolling deploys)")
	// Tracing is off unless an endpoint is given. Nothing about a message path
	// should reach the network because a default said so.
	traceEndpoint := fset.String("trace-endpoint", "", "OTLP/HTTP base URL to export message traces to, for example http://localhost:4318")
	traceRatio := fset.Float64("trace-sample", 1, "fraction of traces to record, 0 to 1")
	traceInstance := fset.String("trace-instance", "", "name for this instance in traces; defaults to the hostname, which is what distinguishes replicas")

	if err := fset.Parse(args); err != nil {
		return err
	}

	// A misconfigured passkey domain is refused at startup rather than at the first request.
	//
	// The commonest mistake is giving a URL where a bare domain is wanted, and it produces credentials that never work.
	// Left to request time it starts a server whose passkey buttons all fail, which somebody debugs for an afternoon;
	// refused here it is one line of output before anything is running.
	if *passkeyRPID != "" {
		if err := (webauthn.Config{
			RPID:    strings.TrimSpace(*passkeyRPID),
			RPName:  *passkeyName,
			Origins: passkeyOriginList(*passkeyRPID, *passkeyOrigins),
		}).Valid(); err != nil {
			return fmt.Errorf("-passkey-domain is not usable: %w", err)
		}
	}

	// The attestation policy, built and checked before anything serves.
	//
	// Refused here rather than at enrolment. A misconfigured policy that only shows itself when somebody tries to
	// register a passkey shows itself to the person least able to fix it.
	attestation, err := passkeyAttestationPolicy(*passkeyRoots, *passkeyModels)
	if err != nil {
		return err
	}
	// An unrecognised default role is refused at startup rather than falling back.
	//
	// A typo silently becoming viewer would be the lucky outcome; silently becoming admin would not, and either way an
	// operator who typed a role and got a different one has been misled about who can do what.
	if *scimEnabled {
		role := store.Role(strings.TrimSpace(*scimDefaultRole))
		if !role.Valid() || role == store.RolePlatform {
			return fmt.Errorf("-scim-default-role %q is not usable; give viewer, editor or admin. Platform is "+
				"refused because it crosses tenant boundaries, and a role handed out by default should be the "+
				"narrowest one that is useful", *scimDefaultRole)
		}
	}

	// Applied before any channel is read, because validation consults it.
	//
	// Set here rather than in a channel file on purpose: a channel author is the person this control restrains, so an
	// exemption they could grant themselves would only stop somebody who was not trying.
	if *allowMetadataEgress {
		egress.SetDefault(egress.Policy{AllowMetadata: true})
	}

	var handler slog.Handler
	if *jsonLogs {
		handler = slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	} else {
		handler = slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo})
	}
	log := slog.New(handler)

	// Said once at startup, because an operator who configured this needs to see it took effect - and because a server
	// that is verifying attestation refuses authenticators, which is a support call waiting to happen if nobody knows.
	if attestation.Configured() {
		log.Info("passkey attestation will be verified against configured roots",
			"roots", *passkeyRoots, "approved_models", len(attestation.AllowedAAGUIDs))
	}

	useTLS := *certFile != "" && *keyFile != ""
	if (*certFile == "") != (*keyFile == "") {
		return errors.New("-tls-cert and -tls-key must be given together")
	}

	// The default port follows the scheme, because a port number is a promise to a browser.
	//
	// This defaulted to 8443 whether or not TLS was configured, and 8443 means HTTPS to
	// every convention and, increasingly, to browsers themselves: Firefox and Chrome both
	// try HTTPS first now. So a first run printed http://127.0.0.1:8443, the browser
	// upgraded it, got plain HTTP back, and showed SSL_ERROR_RX_RECORD_TOO_LONG - a
	// security warning, on first launch, for a correctly working server. Reported by
	// someone doing nothing but downloading the release and following the printed URL.
	//
	// An explicit -addr still wins, so anyone already pointing at 8443 is unaffected.
	*addr = addressOrDefault(*addr, useTLS)

	// Session cookies over plain HTTP on a routable address means anyone on the
	// path can take a session. Refusing by default is the only way that decision
	// gets made deliberately.
	if !useTLS && !isLoopback(*addr) && !*insecure {
		return fmt.Errorf(
			"refusing to serve %s over plain HTTP: session cookies and patient data would cross the network unencrypted.\n"+
				"Provide -tls-cert and -tls-key, bind to 127.0.0.1 and put a reverse proxy in front, or pass -insecure if you accept the risk",
			*addr)
	}

	fhirVersion, err := fhir.ParseVersion(*fhirVersionFlag)
	if err != nil {
		return err
	}

	// Settings are prepared before anything reads one.
	//
	// Defaulted next to the database rather than left empty, because an empty path means the settings area can show
	// every value and save none - and a settings page whose save button does not work is worse than no settings page.
	// Next to the database specifically, rather than in the channels directory, so the channel loader never has to
	// decide whether a file is a channel.
	settingsPath := *settingsFile
	if settingsPath == "" {
		settingsPath = filepath.Join(filepath.Dir(*dbPath), "perfuse-settings.yaml")
	}

	settingsRegistry, err := settings.NewRegistry(settings.Default())
	if err != nil {
		// A malformed registry is a programming error, and it is refused here rather than at first use so that it
		// cannot ship as a settings area with a control missing.
		return fmt.Errorf("preparing settings: %w", err)
	}

	settingsStore, err := settings.NewStore(settingsRegistry, settingsPath)
	if err != nil {
		// A settings file with one bad line is refused rather than partly applied, or the server runs with a
		// configuration that is neither what the file says nor what the operator meant.
		return err
	}

	// The logo lives beside the settings file rather than in it, because it is binary and that file is
	// meant to stay readable and hand-editable. With no settings file there is nowhere to put it, and
	// an upload is then refused rather than kept in memory until the next restart.
	var brandingStore *branding.Store
	if settingsPath != "" {
		brandingStore, err = branding.NewStore(filepath.Dir(settingsPath))
		if err != nil {
			// A logo that cannot be read is a startup failure rather than a silent fallback to the
			// default mark: a customer whose branding vanished after a restart would reasonably want
			// to know at the restart.
			return fmt.Errorf("preparing branding: %w", err)
		}
	}

	// Existing installations keep working: their flags become the initial file on first start, and from then on the
	// interface can manage them without anybody editing a service definition.
	if err := settingsStore.SeedFromFlags(flagSettings(fset)); err != nil {
		return fmt.Errorf("writing the initial settings file: %w", err)
	}

	// A flag the file overrides does nothing, and saying so is the whole point. Left silent, it stays in the service
	// definition for years and everyone who reads it believes it.
	for _, conflict := range settingsStore.Conflicts(flagSettings(fset)) {
		fmt.Fprintf(os.Stderr, "perfuse: note: %s\n", conflict)
	}

	// One database file for everything, so a deployment has one thing to back up
	// rather than several that can be restored out of step with each other.
	//
	// Two pools against it: one connection for writes, several for reports. A
	// message browser search scans the payload column, and on a single pool it
	// would hold the connection every arriving message needs.
	pool, err := sqlitedb.Open(*dbPath, sqlitedb.Options{})
	if err != nil {
		return fmt.Errorf("opening %s: %w", *dbPath, err)
	}
	defer pool.Close()
	db := pool.Write

	st, err := store.OpenDB(db)
	if err != nil {
		return fmt.Errorf("preparing the user store: %w", err)
	}

	// How long a login lasts, read at the moment a session is created or refreshed rather than copied now, so
	// changing it in the interface applies to the next sign-in instead of the next restart.
	//
	// Sessions already issued keep the expiry they were given. That is the honest behaviour: shortening the
	// window is usually a response to something, and silently extending or truncating live sessions to match a
	// new number would be a surprise in both directions.
	if settingsStore != nil {
		st.SessionLifetimeFn = func() time.Duration {
			return time.Duration(settingsStore.Int("signin.sessionHours")) * time.Hour
		}
	}

	repo, err := api.NewChannelRepo(*channelsDir)
	if err != nil {
		return fmt.Errorf("channel directory: %w", err)
	}

	var messages *msgstore.Store
	if *storeMessages {
		messages, err = msgstore.NewStoreWithReader(db, pool.Read)
		if err != nil {
			return fmt.Errorf("preparing the message store: %w", err)
		}
		messages.RetentionDays = *retention
		messages.StorePayloads = *storePayloads

		// Pointed at the settings store so a change through the interface takes effect without a restart.
		//
		// A function rather than a copied value: reading a plain field while somebody saves settings is a data
		// race, and the settings store already holds a lock around its values - so asking it each time is both
		// simpler and correct. Assigned once here, before anything serves.
		if settingsStore != nil {
			messages.RetentionDaysFn = func() int {
				return settingsStore.Int("data.retentionDays")
			}
			messages.StorePayloadsFn = func() bool {
				return settingsStore.Bool("data.storePayloads")
			}
			messages.PayloadDaysFn = func() int {
				return settingsStore.Int("data.payloadDays")
			}
		}

		if *retention <= 0 {
			// The most commonly reported operational failure of an interface engine
			// is its message tables filling the disk.
			log.Warn("message retention is unlimited; the database will grow without bound")
		}
		if !*storePayloads {
			log.Info("message contents will not be stored; outcomes and counts still are")
		}
	}

	var fhirStore *fhirserver.Store
	if *serveFHIR {
		fhirStore, err = fhirserver.NewStore(db, fhirVersion)
		if err != nil {
			return fmt.Errorf("preparing the FHIR store: %w", err)
		}
	}

	if err := ensureAdmin(context.Background(), st, stdout, log); err != nil {
		return err
	}

	var srvAlerts *alerts.Evaluator

	// Tracing is started before the engine, so the first message of the process
	// can already be traced. A failure here stops startup rather than silently
	// running untraced: somebody who configured an endpoint will otherwise spend an
	// afternoon looking for spans that were never going to arrive.
	instance := *traceInstance
	if instance == "" {
		if h, err := os.Hostname(); err == nil {
			instance = h
		}
	}
	tracer, err := trace.New(trace.Config{
		Endpoint:       *traceEndpoint,
		ServiceName:    "perfuse",
		ServiceVersion: version,
		Instance:       instance,
		SampleRatio:    *traceRatio,
	}, log)
	if err != nil {
		return err
	}
	if tracer != nil {
		log.Info("exporting traces", "endpoint", *traceEndpoint, "sample", *traceRatio, "instance", instance)
		for _, w := range (&trace.Config{Endpoint: *traceEndpoint, SampleRatio: *traceRatio}).Warnings() {
			log.Warn(w)
		}
		defer func() {
			// Bounded, because shutdown must not wait on a tracing backend that
			// has stopped answering.
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			if err := tracer.Close(ctx); err != nil {
				log.Warn("tracing did not flush cleanly", "err", err)
			}
		}()
	}

	var runtime *api.Runtime
	if *runEngine {
		runtime = api.NewRuntime(repo, messages, fhirStore)
		runtime.Tracer = tracer

		// Say what the delivery limits are and how they were arrived at.
		//
		// Nobody chose these - they are derived from the file descriptor limit this process turned out to
		// have - so a limit that engages having never explained itself would look like a defect. This is the
		// line somebody greps for at three in the morning when a queue is filling and they want to know
		// whether the engine is holding back or the receiver is.
		if _, budget, report := admit.Shared(); budget.TooTight {
			log.Warn("file descriptor limit is too low for comfort", "detail", budget.Explanation)
		} else {
			if report.Err != nil {
				log.Warn("could not raise the file descriptor limit, planning inside the current one",
					"err", report.Err, "limit", report.Soft)
			}
			log.Info("delivery concurrency planned", "detail", budget.Explanation,
				"in_flight_limit", budget.Total, "per_destination_limit", budget.PerDestination)
		}

		// Metrics are collected whenever an engine is running. The resolution is
		// ten seconds and the window six hours, which is enough to answer "what
		// changed twenty minutes ago" without keeping so much history that a
		// long-running process grows.
		collector := metrics.New(metrics.Options{
			Resolution: 10 * time.Second,
			Window:     6 * time.Hour,
		})
		runtime.Metrics = collector

		metricsStop := make(chan struct{})
		go collector.Run(metricsStop)
		defer close(metricsStop)

		if list, broken, err := repo.List(); err == nil {
			collector.Set(metrics.ChannelsTotal, float64(len(list)))

			// A file that will not load is reported at startup, by name and with the reason.
			//
			// It was reported only through the console's channel list, which means a headless deployment - the normal way
			// this runs - had no signal at all. The log said nothing, the status endpoint counted only the files that
			// loaded, and the dashboard therefore said "all running" while a feed was silently absent. A hospital would
			// find out when someone downstream noticed missing patients.
			//
			// Logged at warning rather than error because the server is still doing useful work for the channels that did
			// load, and logged per file because "3 files failed" without names is not actionable.
			files := make([]string, 0, len(broken))
			for file := range broken {
				files = append(files, file)
			}
			sort.Strings(files)

			for _, file := range files {
				log.Warn("a channel file could not be loaded, so that feed is not running",
					"file", file, "reason", broken[file])
			}

			// The gauge itself is refreshed live by publishChannelGauges; this only ensures it is correct before the first
			// refresh, so a scrape in the first seconds is not misleadingly zero.
			collector.Set(metrics.ChannelsBroken, float64(len(broken)))
		}

		// The durable queue. Always prepared, never used unless a destination asks
		// for it: preparing it costs one table, and having it absent would mean a
		// channel that enables queueing fails at the first delivery rather than at
		// load.
		queueStore, err := queue.NewStore(pool.Write, pool.Read)
		if err != nil {
			return fmt.Errorf("preparing the delivery queue: %w", err)
		}
		queues := engine.NewQueues(queueStore, log)
		runtime.Queues = queues
		defer queues.Stop()

		// Where a DICOM query source records what it has already seen. The same pool as the queue, because both are
		// runtime state rather than configuration - channels stay in files, what has happened to them does not.
		runtime.QueryState = dicomstate.New(pool.Write, pool.Read)

		// Queue depth and the age of the oldest waiting message are polled rather
		// than pushed, because they are properties of the table rather than of any
		// one event. Age is the number worth alerting on: depth alone cannot tell a
		// busy queue that is draining from a small one stuck since Tuesday.
		go pollQueueDepth(metricsStop, queueStore, collector, log)
		go pollDeliveryAdmission(metricsStop, collector)

		// Finished items are purged on a slow timer. They are kept at all so
		// somebody watching a backlog drain can see it happening, and so an
		// abandoned message can still be found afterwards.
		go purgeQueue(metricsStop, queueStore, log)

		// Alerting. Always evaluated so the interface can show what is wrong;
		// notification is what requires a webhook. Those are different things, and
		// tying them together would mean a deployment without a chat integration
		// also had no alert page.
		rules, err := loadAlertRules(*alertsFile)
		if err != nil {
			return err
		}
		var notifier *alerts.Notifier
		if *alertWebhook != "" {
			notifier = &alerts.Notifier{
				URL:         *alertWebhook,
				MinSeverity: alerts.Severity(*alertSeverity),
				Log:         log,
			}
			// Read when an alert is dispatched, so raising the threshold in the interface takes effect on
			// the next alert. That is the direction that matters: the threshold gets raised because
			// something is being noisy at an unwelcome hour.
			if settingsStore != nil {
				notifier.MinSeverityFn = func() alerts.Severity {
					return alerts.Severity(settingsStore.String("alerts.minSeverity"))
				}
			}
			log.Info("alerts will be sent to a webhook",
				"rules", len(rules), "min_severity", *alertSeverity)
		} else {
			log.Info("alerting is evaluated but not sent anywhere; "+
				"pass -alert-webhook to be told without watching the dashboard",
				"rules", len(rules))
		}

		notify := func(a alerts.Alert) {}
		if notifier != nil {
			notify = notifier.Notify
		}
		evaluator := alerts.NewEvaluator(rules, notify)
		srvAlerts = evaluator

		watcher := &alerts.Watcher{
			Evaluator: evaluator,
			Metrics:   collector,
			Queue:     queueStore,
			Log:       log,
			RunningChannels: func() ([]string, map[string]bool) {
				return runtime.ChannelStates()
			},
		}

		// What each channel normally carries, so silence can be judged against the feed's own history rather
		// than a fixed threshold. Needs the message store, because the history is the messages.
		if messages != nil {
			cache := newRhythmCache(messages, log)
			watcher.Rhythms = cache.Rhythms
		}

		// Contract checking, only when something asks for it. An installation with no contracts starts no
		// goroutine, holds no state and never reads the message store for this.
		if withContracts, err := anyChannelHasAContract(repo); err != nil {
			log.Warn("could not tell whether any channel has a contract", "err", err)
		} else if withContracts > 0 {
			runtime.EnableContracts(log)

			watcher.Contracts = func() map[string]alerts.ContractDrift {
				return runtime.ContractDrift()
			}

			// Re-checked from the alert loop rather than its own timer, so a contract result is never newer or
			// older than the reading it is attached to.
			watcher.BeforeRead = func() {
				list, err := loadedChannels(repo)
				if err != nil {
					return
				}
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				runtime.CheckContractsDue(ctx, list)
			}

			log.Info("checking feed contracts", "channels", withContracts)
		}

		go watcher.Run(metricsStop)
	}

	// Discovered at startup, so a wrong issuer or an unreachable provider stops the server rather than surfacing when
	// somebody cannot sign in.
	var oidcCfg *api.OIDCConfig
	var ldapCfg *api.LDAPConfig
	if *ldapFile != "" {
		cfg, err := ldap.LoadConfig(*ldapFile)
		if err != nil {
			return err
		}
		dir, err := ldap.NewDirectory(cfg, log)
		if err != nil {
			return err
		}
		ldapCfg = &api.LDAPConfig{Directory: dir, Settings: cfg}

		// The address, the search base and the role mapping are logged. The service account password is not.
		//
		// The mapping is here for the same reason as the OIDC one: "why can this person not get in" is almost
		// always a group name that does not match, and having it in the log saves reading the file.
		log.Info("directory sign-in enabled",
			"addr", cfg.Addr,
			"user_base_dn", cfg.UserBaseDN,
			"encrypted", cfg.TLS || cfg.StartTLS,
			"create_users", cfg.CreateUsers,
			"stable_ids", cfg.UniqueIDAttribute != "",
			"roles", describeRoles(cfg.Roles))

		if cfg.Insecure {
			log.Warn("directory sign-in is not encrypted",
				"detail", "a simple bind sends passwords in cleartext, including the service account's")
		}
		if cfg.UniqueIDAttribute == "" {
			// Warned rather than refused, because plenty of directories are stable in practice and refusing
			// would block a working setup. Said out loud because the consequence arrives months later.
			log.Warn("directory sign-in has no stable identifier",
				"detail", "people are identified by their DN, so moving somebody between organisational "+
					"units will look like a new person; set unique_id_attribute to objectGUID on "+
					"Active Directory or entryUUID on OpenLDAP")
		}
	}

	var samlCfg *api.SAMLConfig

	if *samlFile != "" {
		// Built here rather than at first sign-in, and a failure stops the server.
		//
		// Everything that can go wrong in this file is something somebody typed: a certificate that was truncated in a copy, a
		// reply URL that does not match what the identity provider has, an empty role mapping that would authenticate people and
		// grant them nothing. Starting anyway would move the discovery to the moment somebody cannot get in, which is the worst
		// time to learn it and the hardest to diagnose - a sign-in failure looks like the provider's fault from every angle.
		cfg, err := api.SAMLFromFile(*samlFile)
		if err != nil {
			return err
		}

		samlCfg = cfg
	}

	if *oidcFile != "" {
		// The file is parsed and validated first, without touching the network.
		//
		// A mistake in the file itself still stops the server: an unknown key, a missing issuer, an empty role
		// mapping. Those are errors somebody made and can see, and starting anyway would mean discovering them when
		// a person cannot sign in.
		if _, err := oidc.LoadFileWithoutDiscovery(*oidcFile); err != nil {
			return err
		}

		fileCfg, provider, mapping, err := oidc.LoadFile(context.Background(), *oidcFile)
		if err != nil {
			// Discovery failing is different, and used to stop the server too. That was a trap: a typo in the
			// issuer, or an identity provider that happened to be down during a restart, left a server that
			// could not start - and therefore could not serve the settings page that would fix it.
			//
			// So single sign-on is disabled and said loudly instead. Local accounts are the break-glass path
			// this project already relies on for exactly this situation, and the settings page shows the
			// failure with the file open for editing.
			log.Error("single sign-on is disabled because the identity provider could not be reached",
				"file", *oidcFile,
				"err", err,
				"detail", "local accounts still work; correct the issuer on the settings page and restart")
			oidcCfg = nil
			goto oidcDone
		}
		oidcCfg = &api.OIDCConfig{
			Issuer:       fileCfg.Issuer,
			ClientID:     fileCfg.ClientID,
			ClientSecret: fileCfg.ClientSecret,
			RedirectURL:  fileCfg.RedirectURL,
			Scopes:       fileCfg.Scopes,
			Roles:        mapping,
			CreateUsers:  fileCfg.CreateUsers,
			Label:        fileCfg.Label,
			Provider:     provider,
			Keys:         oidc.NewKeySet(provider.JWKSURL, oidc.DefaultHTTPClient),
		}
		// The mapping is logged and the secret is not. Somebody diagnosing "why does this person have no access" needs the
		// mapping in front of them, and it is the single most common thing to get wrong here.
		log.Info("openid connect sign-in enabled",
			"issuer", provider.Issuer,
			"create_users", fileCfg.CreateUsers,
			"roles", mapping.Describe())
	}

oidcDone:

	srv := &api.Server{
		Channels:        repo,
		Store:           st,
		Log:             log,
		SecureCookies:   useTLS,
		OIDC:            oidcCfg,
		SAML:            samlCfg,
		PasskeyRPID:     strings.TrimSpace(*passkeyRPID),
		PasskeyRPName:   *passkeyName,
		PasskeyOrigins:  passkeyOriginList(*passkeyRPID, *passkeyOrigins),
		SCIMEnabled:     *scimEnabled,
		SCIMDefaultRole: store.Role(*scimDefaultRole),
		MetricsToken:    *metricsToken,
		MetricsOpen:     *metricsOpen,
		LDAP:            ldapCfg,
		Started:         time.Now(),
		Startup: api.StartupSettings{
			Addr:          *addr,
			ChannelsDir:   *channelsDir,
			DatabasePath:  *dbPath,
			TLS:           useTLS,
			MultiTenant:   *multiTenant,
			EngineRunning: *runEngine,
			FHIRServing:   *serveFHIR,
			StoreMessages: *storeMessages,
			StorePayloads: *storePayloads,
			RetentionDays: *retention,
			AllowMetadata: *allowMetadataEgress,
			JSONLogs:      *jsonLogs,
			FleetLabel:    *fleetLabel,
			TraceEndpoint: *traceEndpoint,
			// The webhook is reported as present rather than printed. It frequently carries a token in its path,
			// which is a credential in a URL and belongs on the settings page no more than a password does.
			AlertWebhook:  strings.TrimSpace(*alertWebhook) != "",
			AlertSeverity: *alertSeverity,
			CommandLine:   os.Args,
		},
		SettingsPaths: api.SettingsFiles{
			Alerts: *alertsFile,
			OIDC:   *oidcFile,
			LDAP:   *ldapFile,
			SAML:   *samlFile,
			Peers:  *peersFile,
		},
		Runtime:            runtime,
		Alerts:             srvAlerts,
		Version:            version,
		FleetLabel:         *fleetLabel,
		Settings:           settingsStore,
		Branding:           brandingStore,
		PasskeyAttestation: attestation,
	}

	// The interface inventory, written to a file so it can be committed and read without the application.
	if path := strings.TrimSpace(*docPath); path != "" {
		stopDocs := (&docWriter{
			path:   path,
			source: dirSource{dir: *channelsDir},
			log:    log,
		}).start(*docEvery)
		defer stopDocs()

		log.Info("writing the interface inventory", "path", path, "every", *docEvery)
	}

	// The label may have been set through the interface on a previous run, in which case the file wins over the flag and
	// the server should start with what the file says rather than waiting for somebody to save the form again.
	if label := settingsStore.String("fleet.label"); label != "" {
		srv.FleetLabel = label
	}

	// Who may read /metrics, read per request so a change applies to the next scrape. The flags remain the fallback
	// inside the server, so a deployment that has never touched the setting behaves exactly as it did.
	srv.MetricsAccessFn = func() string {
		return settingsStore.String("security.metricsAccess")
	}

	// A fleet view needs no agent on the other instances and no second install: the console already ships inside
	// every binary, so any instance can be the aggregator and the peer side needs only a token.
	// Defaulted rather than required, and the fleet is always running.
	//
	// It used to start only when -peers pointed at a file somebody had written by hand, which meant the Fleet
	// section could explain the feature and offer no way to use it - the peer list, the token and a restart all
	// had to happen elsewhere. Now there is always somewhere to store peers, beside the database like the
	// settings file, so the section can add one and have it polled without a restart.
	peersPath := *peersFile
	if peersPath == "" {
		peersPath = filepath.Join(filepath.Dir(*dbPath), "perfuse-peers.yaml")
	}

	{
		fleetCfg := &peers.Config{}
		if _, statErr := os.Stat(peersPath); statErr == nil {
			loaded, err := peers.Load(peersPath)
			if err != nil {
				return fmt.Errorf("reading %s: %w", peersPath, err)
			}
			fleetCfg = loaded
		} else if *peersFile != "" {
			// An explicit path that does not exist is a mistake worth naming. A missing default is just a server
			// that has never had a peer added.
			return fmt.Errorf("reading %s: %w", *peersFile, statErr)
		}

		fleet := peers.New(*fleetCfg)
		fleet.Start()
		defer fleet.Stop()
		srv.Fleet = fleet
		srv.PeersFile = peersPath

		// This server's own address, used only to refuse a peer pointing back here. A self-referencing peer polls
		// this instance through its own HTTP stack and appears twice in the fleet view, which reads as two
		// machines and is one.
		srv.SelfURL = fmt.Sprintf("%s://%s", schemeFor(useTLS), *addr)

		// The certificate used to sign documents from the interface.
		//
		// Held as paths rather than a loaded pair: signing is rare, and reading the file each time means a replaced
		// certificate takes effect without a restart instead of quietly producing signatures against the old one.
		//
		// Set outside the TLS branch on purpose. An instance behind a terminating proxy serves plain HTTP and can still
		// hold a signing certificate, and tying the two together would mean the only way to sign was to also terminate
		// TLS here.
		// National exchange, when it is configured. Absent is the normal case and is silent.
		tefcaParticipant, tefcaAudit, err := buildTEFCA(settingsStore, *dbPath, log)
		if err != nil {
			return err
		}
		if tefcaAudit != nil {
			defer tefcaAudit.Close()
		}
		attachTEFCA(srv, tefcaParticipant, tefcaAudit)

		srv.TLSCertFile, srv.TLSKeyFile = *certFile, *keyFile

		if *signCertFile != "" && *signKeyFile != "" {
			srv.TLSCertFile, srv.TLSKeyFile = *signCertFile, *signKeyFile
		} else if (*signCertFile == "") != (*signKeyFile == "") {
			return errors.New("-signing-cert and -signing-key must be given together")
		}

		// The peers file may name this instance too. Honoured only when the flag is empty, so an explicit flag
		// still wins - but without this the label in the file is silently ignored, which is the "looks configured
		// and is not" failure that wastes the most time.
		if *fleetLabel == "" && fleetCfg.Label != "" {
			srv.FleetLabel = fleetCfg.Label
		}

		if len(fleetCfg.Peers) > 0 {
			log.Info("watching other instances",
				"peers", len(fleetCfg.Peers),
				"poll_every", fleetCfg.PollEvery,
				"file", peersPath)
		}
	}

	// Multi-tenant operation is a distribution strategy as much as a feature. Mirth needs one install per
	// customer, so a consultancy running integration for fifty small clinics runs fifty installs. Running
	// them on one server makes the consultancy money, which turns the people who would otherwise be this
	// project's loudest opponents - because its ease of use is their revenue - into its distributors.
	//
	// The isolation is structural rather than checked: each tenant's channels live in their own directory,
	// and a handler cannot reach them without a session, because the repository is only obtainable from one.
	if *multiTenant {
		repos, err := api.NewTenantRepos(*channelsDir)
		if err != nil {
			return fmt.Errorf("setting up per-tenant channel directories: %w", err)
		}
		srv.Repos = repos

		// Said at startup rather than left to be discovered. The directory layout changes meaning when this
		// flag is set - what was the channel directory becomes the parent of one per tenant - and somebody
		// who turns it on without realising that would find their channels apparently gone.
		// One engine per tenant, created on first use. Without this, channel isolation covered the files and
		// nothing else: start and stop, the durable queue and the contract checker were all shared, so one tenant
		// could stop another's channels and the queue endpoints acted on everybody's traffic.
		//
		// Created lazily because a platform with fifty tenants where three are active should not hold fifty
		// engines, and a tenant that never signs in should cost nothing.
		// The collector is shared, because it already labels by tenant and a metrics endpoint that had to be
		// scraped once per tenant is not how Prometheus works. Taken from the single runtime, which is nil when
		// this process was started without an engine - in which case there is nothing to collect anyway.
		var collector *metrics.Collector
		if runtime != nil {
			collector = runtime.Metrics
		}
		srv.Runtimes = api.NewTenantRuntimes(repos, messages, fhirStore, collector, log)
		defer srv.Runtimes.StopAll(context.Background())

		log.Info("multi-tenant mode: each tenant's channels live in a subdirectory",
			"base", repos.Base(),
			"note", "the default tenant continues to use the base directory itself")
	}

	if web.Available() {
		h, err := web.Handler()
		if err != nil {
			return err
		}
		srv.StaticHandler = h
	} else {
		log.Warn("no web interface is built into this binary; the API is still available. Run 'make web' to build it")
	}

	mux := http.NewServeMux()
	mux.Handle("/", srv.Handler())

	if fhirStore != nil {
		baseURL := fmt.Sprintf("%s://%s/fhir", schemeFor(useTLS), *addr)
		fhirSrv := fhirserver.NewServer(fhirStore, baseURL, log)
		fhirSrv.ReadOnly = *fhirReadOnly

		// The channels' mapping tables, published as ConceptMaps and answerable by $translate.
		//
		// Wired unconditionally: with no tables configured the endpoints say so and the capability statement does not
		// mention ConceptMap at all, which is the honest state rather than an empty list implying there could be some.
		fhirSrv.Tables = &channelTables{repo: repo, log: log}

		// Bulk export is off unless asked for.
		//
		// One request produces a file containing every record this endpoint serves, which is a different thing from
		// the rest of the API in kind rather than degree - and not a capability anybody should acquire by upgrading.
		// With the flag absent, the operation and its endpoints say it is unavailable rather than answering a 500.
		if *fhirBulkExport {
			exporter, err := fhirserver.NewExportManager(context.Background(), fhirStore)
			if err != nil {
				return fmt.Errorf("preparing bulk export: %w", err)
			}
			fhirSrv.Export = exporter

			log.Info("FHIR bulk export is enabled",
				"note", "one request can produce every record this endpoint serves")
		}

		// The authenticator, which was never set.
		//
		// NewServer leaves it nil deliberately and requireAuth refuses everything when it is - a safe default
		// doing exactly its job. But nothing here ever set one, so every request to /fhir on this command
		// answered 500 "this server has no authenticator configured", while the startup log below said the
		// endpoint was unauthenticated and advised firewalling it.
		//
		// Both halves were wrong in opposite directions, which is why it survived: the log described an open
		// endpoint that was actually shut, so nobody testing security found a hole and nobody testing the
		// feature got far enough to file it.
		fhirAuth, err := fhirAuthenticator(fhirAuthOptions{
			SMARTIssuer:   strings.TrimSpace(*smartIssuer),
			SMARTAudience: strings.TrimSpace(*smartAudience),
			BaseURL:       baseURL,
			Open:          *fhirOpen,
			ReadOnly:      *fhirReadOnly,
			Store:         st,
			Log:           log,
		})
		if err != nil {
			return err
		}
		fhirSrv.Auth = fhirAuth

		// What to tell a SMART app about where to authenticate. Empty means .well-known/smart-configuration
		// answers 404, which is the honest answer for a server nobody configured for SMART.
		fhirSrv.SMART = fhirserver.SMARTDiscovery{
			Issuer:                strings.TrimSpace(*smartIssuer),
			AuthorizationEndpoint: strings.TrimSpace(*smartAuthorize),
			TokenEndpoint:         strings.TrimSpace(*smartToken),
		}

		// Read per request, so changing the page size in the interface applies to the next search rather
		// than the next restart. Only used when a client gives no _count of its own.
		if settingsStore != nil {
			fhirSrv.DefaultCountFn = func() int {
				return settingsStore.Int("fhir.pageSize")
			}
			// Read per request, so turning the FHIR endpoint read-only in the interface takes effect on the
			// next write rather than the next restart. That direction matters: read-only is usually switched
			// on in response to something, and "after a restart" is not an answer at that moment.
			fhirSrv.ReadOnlyFn = func() bool {
				return settingsStore.Bool("fhir.readOnly")
			}
		}

		// The FHIR endpoint is deliberately outside the session-cookie API. FHIR
		// clients are machines with tokens, not browsers, and pretending otherwise
		// would mean every EHR integration had to hold a login cookie.
		mux.Handle("/fhir/", http.StripPrefix("/fhir", fhirSrv.Handler()))

		// Reports the scheme actually in use rather than asserting one. The previous line claimed the endpoint
		// was unauthenticated whatever was configured, which is the kind of log entry that gets believed.
		if *fhirOpen {
			log.Warn("THE FHIR ENDPOINT HAS NO AUTHENTICATION",
				"detail", "every stored patient record can be read by anyone who can reach this port",
				"base", baseURL, "read_only", *fhirReadOnly)
		} else {
			log.Info("serving FHIR", "base", baseURL, "read_only", *fhirReadOnly,
				"authentication", fhirAuth.Describe())
		}

		// Said when SMART tokens are accepted but no endpoints are advertised, because the app-side symptom is
		// confusing: the token works when an app is configured by hand, and automatic discovery returns 404.
		if *smartIssuer != "" && !smartConfigured(*smartAuthorize, *smartToken) {
			log.Warn("SMART tokens will be accepted but no endpoints are advertised, so an app cannot "+
				"discover where to authenticate",
				"fix", "pass -smart-authorize and -smart-token")
		}
	}

	httpSrv := &http.Server{
		Addr:              *addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		// Long, because the dashboard holds a server-sent events stream open.
		WriteTimeout: 0,
		IdleTimeout:  2 * time.Minute,
	}
	if useTLS {
		httpSrv.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	}

	stopSweeper := startSessionSweeper(st, log)
	defer stopSweeper()

	var stopPruner func()
	if messages != nil && *retention > 0 {
		// The interval comes from settings rather than being fixed at six hours, so the control that says how
		// often this runs is telling the truth.
		pruneEvery := 6 * time.Hour
		if hours := settingsStore.Int("data.purgeEveryHours"); hours > 0 {
			pruneEvery = time.Duration(hours) * time.Hour
		}

		stopPruner = messages.StartPruner(context.Background(), pruneEvery,
			func(payloads, rows int64, err error) {
				if err != nil {
					log.Warn("pruning messages failed", "err", err)
					return
				}
				if payloads > 0 || rows > 0 {
					log.Info("pruned messages",
						"payloads_cleared", payloads, "rows_removed", rows,
						"retention_days", *retention)
				}
			})
		defer stopPruner()
	}

	// Channels start after the listener is configured but before it accepts, so a
	// feed is not refused while the interface is still coming up.
	if runtime != nil {
		if errs := runtime.StartEnabled(log); len(errs) > 0 {
			for _, err := range errs {
				log.Error("a channel failed to start", "err", err)
			}
			// A channel that will not start is reported and the rest still run. A
			// single bad file must not take every other feed offline.
		}
	}

	log.Info("serving",
		"url", fmt.Sprintf("%s://%s", schemeFor(useTLS), *addr),
		"channels", *channelsDir,
		"database", *dbPath,
		"tls", useTLS,
		"engine", *runEngine,
		"messages", *storeMessages,
		"fhir", *serveFHIR)
	if !useTLS {
		log.Warn("serving over plain HTTP; sessions and patient data are only safe on loopback or behind a TLS proxy")
	}

	// Readiness flips on only after channels are up. Reporting ready earlier would
	// let a load balancer send messages to an instance with nothing listening,
	// which the sender sees as a refused connection rather than as a deploy.
	srv.MarkReady()

	errc := make(chan error, 1)
	go func() {
		var err error
		if useTLS {
			err = httpSrv.ListenAndServeTLS(*certFile, *keyFile)
		} else {
			err = httpSrv.ListenAndServe()
		}
		if errors.Is(err, http.ErrServerClosed) {
			err = nil
		}
		errc <- err
	}()

	ctx, stop := shutdownContext()
	defer stop()

	select {
	case err := <-errc:
		return err

	case <-ctx.Done():
		log.Info("shutting down")

		// Readiness goes false first, before anything is actually stopped. That
		// leaves a window in which a load balancer notices and stops routing
		// while this instance is still fully able to finish what it accepted.
		// Without the gap, a rolling update closes the listener under a sender
		// that had a healthy connection a moment earlier.
		srv.BeginDraining()
		if *drainFor > 0 {
			log.Info("draining before shutdown", "for", *drainFor)
			select {
			case <-time.After(*drainFor):
			case <-ctx.Done():
			}
		}

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()

		// Channels stop first and are given time to finish the message in flight.
		// Cutting a connection mid-message loses the acknowledgement and makes the
		// sender resend work that was already done.
		if runtime != nil {
			runtime.StopAll(shutdownCtx)
		}
		if err := httpSrv.Shutdown(shutdownCtx); err != nil {
			log.Warn("the HTTP server did not shut down cleanly", "err", err)
		}

		if messages != nil {
			// The main tenant, because this is the shutdown summary of a single process and a multi-tenant
			// instance would need one line per tenant to be honest - which belongs in the platform view rather
			// than in a farewell message.
			if stats, err := messages.Stats(context.Background(), "main", time.Time{}); err == nil {
				fmt.Fprintf(stdout, "\nrecorded %d message(s)\n", stats.Total)
				for outcome, n := range stats.ByOutcome {
					fmt.Fprintf(stdout, "  %-12s %d\n", outcome, n)
				}
			}
		}
		return nil
	}
}

// addressOrDefault returns given when set, and otherwise the default for the scheme.
func addressOrDefault(given string, useTLS bool) string {
	if given != "" {
		return given
	}
	if useTLS {
		return defaultTLSAddr
	}
	return defaultPlainAddr
}

func schemeFor(useTLS bool) string {
	if useTLS {
		return "https"
	}
	return "http"
}

// ensureAdmin creates the first administrator if there are no users at all.
func ensureAdmin(ctx context.Context, st *store.Store, stdout io.Writer, log *slog.Logger) error {
	n, err := st.UserCount(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}

	password, err := store.GeneratePassword()
	if err != nil {
		return err
	}
	if _, err := st.CreateUser(ctx, "admin", password, store.RoleAdmin); err != nil {
		return err
	}

	// Printed to stdout, not the log, so it is not swept into a log aggregator.
	fmt.Fprintf(stdout, "\n"+
		"  No users existed, so an administrator has been created.\n\n"+
		"    username:  admin\n"+
		"    password:  %s\n\n"+
		"  This is shown once. Sign in and change it.\n\n", password)

	log.Info("created the initial administrator account", "username", "admin")
	return st.Audit(ctx, store.AuditEntry{
		Username: "system", Action: "user.create", Target: "admin",
		Detail: "initial administrator created on first run",
	})
}

// sweepOnce does one pass of the expiry sweep.
//
// Separate from the ticker so a test can call it. The bug this is factored out for was not that the purge was wrong, it
// was that nothing called it - and no unit test of a purge function can detect that, because the function passes its own
// test perfectly while never running in production.
func sweepOnce(ctx context.Context, st *store.Store, log *slog.Logger) {
	if n, err := st.PurgeExpiredSessions(ctx); err != nil {
		log.Warn("could not purge expired sessions", "err", err)
	} else if n > 0 {
		log.Debug("purged expired sessions", "count", n)
	}

	// Not gated on the result above: an error expiring sessions says nothing about challenges, and skipping this
	// because of it is how a table grows unattended.
	if n, err := st.PurgeExpiredChallenges(ctx); err != nil {
		log.Warn("could not purge expired passkey challenges", "err", err)
	} else if n > 0 {
		log.Debug("purged expired passkey challenges", "count", n)
	}
}

// startSessionSweeper removes expired sessions and abandoned passkey challenges periodically.
//
// Challenges ride along with sessions rather than getting their own ticker: both are short-lived rows created by signing
// in, both are harmless once expired, and one sweeper is one thing to reason about. A challenge is abandoned whenever
// somebody opens the sign-in page and walks away, so without this the table grows by a row per abandoned attempt and
// never shrinks.
func startSessionSweeper(st *store.Store, log *slog.Logger) func() {
	ticker := time.NewTicker(time.Hour)
	done := make(chan struct{})

	go func() {
		for {
			select {
			case <-ticker.C:
				sweepOnce(context.Background(), st, log)
			case <-done:
				return
			}
		}
	}()

	return func() {
		ticker.Stop()
		close(done)
	}
}

// isLoopback reports whether an address is bound to the local machine only.
func isLoopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")

	switch host {
	case "127.0.0.1", "::1", "localhost":
		return true
	}
	return strings.HasPrefix(host, "127.")
}

// unused keeps encoding/json imported for future response helpers here.
var _ = json.Marshal

// pollDeliveryAdmission publishes what the delivery limiter is doing.
//
// Separate from the queue gauges because it answers a different question. A filling queue looks identical
// whether the receiver is slow or this engine is holding messages back, and an operator who cannot tell
// the two apart will restart the wrong thing. Waiting above zero means something is at its limit;
// refused above zero means something waited and gave up.
func pollDeliveryAdmission(stop <-chan struct{}, collector *metrics.Collector) {
	controller, budget, _ := admit.Shared()

	// The limits themselves, published once and then left: they are derived at startup and do not move, and
	// a reader needs them to know whether in-flight is comfortable or nearly at the ceiling.
	collector.Set(metrics.DeliveryLimit, float64(budget.Total), "total")
	collector.Set(metrics.DeliveryLimit, float64(budget.PerDestination), "per_destination")

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		stats := controller.Stats()
		collector.Set(metrics.DeliveriesInFlight, float64(stats.InFlight))
		collector.Set(metrics.DeliveriesWaiting, float64(stats.Waiting))
		collector.Set(metrics.DeliveriesRefused, float64(stats.Rejected))

		select {
		case <-stop:
			return
		case <-ticker.C:
		}
	}
}

// pollQueueDepth publishes the queue gauges every collection interval.
//
// The set of destinations it reports on only ever grows. Depth returns no row at
// all for a destination whose queue has fully drained, so iterating just the rows
// leaves the last non-zero value standing: a live test showed a depth of five and
// an oldest-message age of nine minutes against an empty queue, which reads as a
// backlog that never cleared. A gauge that stops being written is not the same as
// a gauge that reads zero, and on a chart it is the more alarming of the two.
func pollQueueDepth(stop <-chan struct{}, q *queue.Store, collector *metrics.Collector, log *slog.Logger) {
	// Remembered so a destination that empties is explicitly zeroed.
	type dest struct{ channel, destination string }
	seen := map[dest]bool{}

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		rows, err := q.Depth(context.Background())
		if err != nil {
			log.Error("could not read queue depth", "error", err)
			continue
		}

		current := map[dest]bool{}
		for _, r := range rows {
			key := dest{r.Channel, r.Destination}
			current[key] = true
			seen[key] = true

			collector.Set(metrics.QueueDepth, float64(r.Pending), r.Channel, r.Destination)
			collector.Set(metrics.QueueFailedDepth, float64(r.Failed), r.Channel, r.Destination)
			collector.Set(metrics.QueueOldestSeconds, r.OldestSeconds, r.Channel, r.Destination)
		}

		for key := range seen {
			if current[key] {
				continue
			}
			collector.Set(metrics.QueueDepth, 0, key.channel, key.destination)
			collector.Set(metrics.QueueFailedDepth, 0, key.channel, key.destination)
			collector.Set(metrics.QueueOldestSeconds, 0, key.channel, key.destination)
		}
	}
}

// purgeQueue deletes delivered and abandoned items once they are old enough.
//
// One retention for the whole table rather than each destination's own setting.
// The queue is a single table shared by every channel, so honouring per-destination
// retention would mean one channel's short window deleting rows another channel
// still wanted, and the failure would be invisible. Erring towards keeping them is
// the safe direction: these rows carry no clinical value on their own, only the
// record that something was delivered late or abandoned.
func purgeQueue(stop <-chan struct{}, q *queue.Store, log *slog.Logger) {
	retention := time.Duration(config.DefaultQueueRetainHours) * time.Hour

	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
		}

		n, err := q.Purge(context.Background(), time.Now().Add(-retention))
		if err != nil {
			log.Error("could not purge the queue", "error", err)
			continue
		}
		if n > 0 {
			log.Info("purged finished queue items", "removed", n,
				"older_than", retention.String())
		}
	}
}

// loadAlertRules reads a rules file, or returns the defaults.
//
// A malformed file is an error rather than a fallback to the defaults. Silently
// running different rules from the ones somebody wrote is how an alert that was
// supposed to exist turns out not to.
// loadAlertRules reads the rules file, or the defaults when none is given.
//
// A thin wrapper now: the parsing lives in internal/alerts so the settings API can validate a proposed file against the
// same loader rather than a copy of it.
func loadAlertRules(path string) ([]alerts.Rule, error) {
	return alerts.LoadFile(path)
}

// anyChannelHasAContract counts channels that carry one.
//
// Checked once at startup rather than continuously. Adding a contract to a channel is a configuration change
// that already needs a reload, and making the check dynamic would mean profiling machinery permanently resident
// in every installation to serve the ones that never use it.
func anyChannelHasAContract(repo *api.ChannelRepo) (int, error) {
	list, err := loadedChannels(repo)
	if err != nil {
		return 0, err
	}

	n := 0
	for _, c := range list {
		if c.Contract != nil && c.Contract.Contract() != nil {
			n++
		}
	}
	return n, nil
}

// loadedChannels reads every channel that loads.
//
// A file that will not load is skipped rather than failing the whole call. An operator with one broken channel
// should still get contract checking on the others - the broken one is already reported elsewhere, and refusing
// everything over it would turn one problem into two.
func loadedChannels(repo *api.ChannelRepo) ([]*config.Channel, error) {
	summaries, _, err := repo.List()
	if err != nil {
		return nil, err
	}

	var out []*config.Channel
	for _, s := range summaries {
		c, err := repo.Get(s.Name)
		if err != nil {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// describeRoles renders a group-to-role mapping for a log line.
//
// Sorted, because Go maps range randomly and a log line that changes order between restarts cannot be compared with the
// previous one. Group names are configuration, never message content.
func describeRoles(roles map[string]string) string {
	out := make([]string, 0, len(roles))
	for group, role := range roles {
		out = append(out, group+"="+role)
	}
	sort.Strings(out)
	return strings.Join(out, " ")
}

// passkeyOriginList works out which origins to accept.
//
// Defaults to https:// plus the domain, which is right for every deployment reached at one address over TLS. An operator
// reached at several names, or at a non-standard port, has to say so - and the default is deliberately the strict one, because
// an origin list that is too generous is what makes a passkey phishable again.
func passkeyOriginList(domain, origins string) []string {
	var out []string
	for _, origin := range strings.Split(origins, ",") {
		if trimmed := strings.TrimSpace(origin); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	if len(out) > 0 {
		return out
	}

	if domain = strings.TrimSpace(domain); domain != "" {
		return []string{"https://" + domain}
	}

	return nil
}
