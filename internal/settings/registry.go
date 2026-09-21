package settings

import (
	"fmt"
	"math"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Groups, named as they appear in the interface.
//
// Constants rather than literals so a typo becomes a duplicate group in the navigation rather than a compile error nobody gets.
const (
	GroupData     = "Data"
	GroupSecurity = "Security"
	GroupIdentity = "Sign-in"
	GroupAlerts   = "Alerts"
	GroupObserve  = "Monitoring"
	GroupEngine   = "Engine"
	GroupFHIR     = "FHIR"
	GroupFleet    = "Fleet"
	GroupBranding = "Branding"

	// GroupTEFCA is national exchange. Its own group rather than a corner of FHIR, because the questions are about
	// agreements, identifiers and audit obligations rather than about a wire format.
	GroupTEFCA = "TEFCA"
)

func intp(v int) *int { return &v }

// Default is the registry of everything an operator may change.
//
// Ordered by group here for reading; the registry sorts for display. Grouped by what somebody is trying to achieve rather than
// by which part of the code reads the value - "I want to stop keeping message bodies" is a thought about data, and it should not
// require knowing that payload storage is implemented in the message store.
//
// Deliberately absent: the listen address, the database path and the channel directory. All three decide where the process
// finds the thing it is editing, so changing them through the interface would mean the interface saving a change to a file it
// would then stop being able to read. They stay flags and are shown read-only.
func Default() []Setting {
	return []Setting{
		// ---------------------------------------------------------------- Data
		{
			Key:      "data.storeMessages",
			Group:    GroupData,
			Subgroup: "What is kept",
			Label:    "Keep a record of each message",
			Help: "Records that a message passed through, with its status and timings. Turning this off makes the " +
				"message browser and the replay feature empty, because there is nothing to list or replay.",
			Kind:   KindBool,
			Widget: WidgetToggle,
			Effect: EffectRestart,
			// On, because the commonest question asked of an integration engine is "did the message arrive",
			// and a server that cannot answer it is a server somebody has to put a packet capture in front of.
			Default: true,
			Flag:    "store-messages",
		},
		{
			Key:      "data.storePayloads",
			Group:    GroupData,
			Subgroup: "What is kept",
			Label:    "Keep message contents",
			Help: "Stores the message body itself, not just the record of it. Needed to look at what a message " +
				"contained or to replay it. This is patient data, so it is what a retention policy is about.",
			Kind:   KindBool,
			Widget: WidgetToggle,
			Effect: EffectLive,
			// On, because a record with no content answers "did it arrive" and not "what did it say", and the
			// second question is the one asked when something has gone wrong.
			Default:   true,
			Flag:      "store-payloads",
			Sensitive: true,
		},
		{
			Key:      "data.indexIdentity",
			Group:    GroupData,
			Subgroup: "What is kept",
			Label:    "Index patient identifiers",
			Help: "Lets a message be found by MRN, patient name, date of birth, accession or claim number, " +
				"whatever format it arrived in, by reading those out of the contents when the message is " +
				"recorded. Without it, finding a patient's messages means knowing where identity sits in " +
				"each format. The identifiers are already stored inside the message contents; indexing " +
				"them makes them quick to search and also makes them enumerable, so this is a decision " +
				"about exposure and not only about speed. Has no effect when message contents are not kept.",
			Kind:   KindBool,
			Widget: WidgetToggle,
			Effect: EffectLive,
			// On, because the console already offers a substring search over message contents, so
			// finding a patient by name is possible without this - just slow and inexact. What this
			// changes is that the search is an index lookup and that it works across formats. A
			// site that wants neither turns it off here, and one that keeps no contents at all never
			// gets it regardless.
			Default:   true,
			Flag:      "index-identity",
			Sensitive: true,
		},
		{
			Key:      "data.retentionDays",
			Group:    GroupData,
			Subgroup: "How long it is kept",
			Label:    "Delete records after",
			Help: "How long to keep the record that a message passed through: which channel, when, and whether " +
				"it was delivered. Message contents are kept for less, set separately below. Zero keeps " +
				"records forever, which is a decision rather than a default - on a busy interface it is " +
				"also how a disk fills up.",
			Kind:    KindInt,
			Widget:  WidgetSlider,
			Effect:  EffectLive,
			Default: 30,
			Min:     intp(0),
			Max:     intp(3650),
			Unit:    "days",
			Flag:    "retention-days",
			// Deleting patient data on a schedule is a records-retention decision, and somebody will
			// eventually need to know when it changed and who changed it.
			Sensitive: true,
		},
		{
			Key:      "data.payloadDays",
			Group:    GroupData,
			Subgroup: "How long it is kept",
			Label:    "Delete message contents after",
			Help: "How long to keep the message bodies themselves. Shorter than the records above on purpose: " +
				"losing the fact that a message arrived is worse than losing what it said, so the audit " +
				"trail outlives the content. This is the setting that decides how much patient data is " +
				"held at rest.",
			Kind:    KindInt,
			Widget:  WidgetSlider,
			Effect:  EffectLive,
			Default: 15,
			Min:     intp(1),
			Max:     intp(3650),
			Unit:    "days",
			// Sensitive for the same reason retention is: this is a records decision about patient data, and
			// somebody will eventually need to know when it changed and who changed it.
			Sensitive: true,
			// It was not configurable at all before, and was hard-coded at half the retention window. Which was
			// a reasonable default and invisible - somebody setting thirty days believed they had thirty days
			// of content and had fifteen. Exposed rather than left as a rule nobody could see, with the old
			// behaviour as the default so no installation changes.
		},
		{
			Key:      "data.purgeEveryHours",
			Group:    GroupData,
			Subgroup: "How long it is kept",
			Label:    "Check for expired records every",
			Help: "How often the deletion runs. It removes anything past the retention window, so a longer gap " +
				"means records can outlive their window by up to that long before being removed.",
			Kind:     KindInt,
			Widget:   WidgetSlider,
			Effect:   EffectRestart,
			Default:  6,
			Min:      intp(1),
			Max:      intp(168),
			Unit:     "hours",
			Advanced: true,
			// An interval rather than an hour of the day, because that is what the pruner actually does - it
			// runs on a ticker from startup. The first version of this setting said "run the deletion at 2
			// o'clock", which described a scheduler that does not exist: it would have been saved, believed,
			// and had no effect on anything. Found by being asked how pruning works.
		},

		// ------------------------------------------------------------ Security
		{
			Key:      "security.allowMetadataEgress",
			Group:    GroupSecurity,
			Subgroup: "What may leave",
			Label:    "Allow message details in outbound notifications",
			Help: "Lets alerts and traces carry identifiers from message content. Off by default because an alert " +
				"goes to a chat system outside the hospital, and a medical record number in a chat message is a " +
				"disclosure that nobody meant to make.",
			Kind:      KindBool,
			Widget:    WidgetToggle,
			Effect:    EffectRestart,
			Default:   false,
			Flag:      "allow-metadata-egress",
			Sensitive: true,
		},
		{
			Key:      "security.egressReason",
			Group:    GroupSecurity,
			Subgroup: "What may leave",
			Label:    "Why this was allowed",
			Help: "Recorded alongside the setting above and shown wherever it takes effect. Required when it is on, " +
				"because in two years the only useful question about this setting is who decided and on what basis.",
			Kind:     KindString,
			Widget:   WidgetText,
			Effect:   EffectRestart,
			Default:  "",
			Advanced: false,
		},
		{
			Key:      "security.metricsAccess",
			Group:    GroupSecurity,
			Subgroup: "Monitoring access",
			Label:    "Who may read /metrics",
			Help: "Metric names include channel names, and a channel is usually named after the system at the other " +
				"end - so this endpoint is a list of who you exchange data with.",
			Kind:   KindChoice,
			Widget: WidgetRadio,
			Effect: EffectLive,
			// Token, not open. The Prometheus convention is an open endpoint, which is defensible for one
			// operator's own channels and indefensible on a shared server.
			Default: "token",
			Choices: []Choice{
				{
					Value: "token",
					Label: "A scrape token, or anyone signed in",
					Help:  "What Prometheus can send. A scraper cannot follow a redirect or hold a cookie.",
				},
				{
					Value: "session",
					Label: "Only people signed in",
					Help:  "No automated scraping. Suitable when metrics are read by a person, not a system.",
				},
				{
					Value: "open",
					Label: "Anyone who can reach the port",
					Help: "Only for a port that is genuinely unreachable from anywhere else. It appears " +
						"in the command line so that assumption can be checked.",
				},
			},
			Sensitive: true,
		},

		// ------------------------------------------------------------- Sign-in
		{
			Key:      "signin.sessionHours",
			Group:    GroupIdentity,
			Subgroup: "Sessions",
			Label:    "Sign people out after",
			Help: "How long a sign-in lasts without activity. Short is safer on a shared workstation and irritating " +
				"on a dedicated one, which is why it is a setting rather than a decision made here. Applies to " +
				"the next sign-in: people already signed in keep the window they were given.",
			Kind:    KindInt,
			Widget:  WidgetSlider,
			Effect:  EffectLive,
			Default: 12,
			Min:     intp(1),
			Max:     intp(720),
			Unit:    "hours",
		},
		{
			Key:      "signin.passkeyDomain",
			Group:    GroupIdentity,
			Subgroup: "Passkeys",
			Label:    "Domain passkeys are bound to",
			Help: "The bare domain this server is reached at - no https://, no port, no path. Passkeys only work on " +
				"this exact domain, which is what makes them impossible to phish. Changing it stops every " +
				"existing passkey from working, so people must add new ones.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "",
			Flag:    "passkey-domain",
			Validate: func(v any) error {
				host, _ := v.(string)
				if host == "" {
					return nil
				}

				return validateBareDomain(host)
			},
			Sensitive: true,
		},
		{
			Key:      "signin.passkeyName",
			Group:    GroupIdentity,
			Subgroup: "Passkeys",
			Label:    "Name shown on the device",
			Help: "What appears in the fingerprint or face prompt when somebody adds a passkey. Worth naming the " +
				"hospital rather than the product, since that is what tells them the prompt is genuine.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "Perfuse",
			Flag:    "passkey-name",
		},
		{
			Key:      "signin.scimEnabled",
			Group:    GroupIdentity,
			Subgroup: "Automatic accounts",
			Label:    "Let the identity provider manage accounts",
			Help: "Allows your identity provider to create, update and disable accounts here. The valuable half is " +
				"disabling: when somebody leaves, the provider is the system that knows first.",
			Kind:      KindBool,
			Widget:    WidgetToggle,
			Effect:    EffectRestart,
			Default:   false,
			Flag:      "scim",
			Sensitive: true,
		},
		{
			Key:      "signin.scimDefaultRole",
			Group:    GroupIdentity,
			Subgroup: "Automatic accounts",
			Label:    "Role for a new account with no matching group",
			Help: "What somebody gets when the identity provider sends no group this server recognises. Defaults " +
				"downward on purpose: too little access is a support call and too much is an incident.",
			Kind:    KindChoice,
			Widget:  WidgetRadio,
			Effect:  EffectRestart,
			Default: "viewer",
			Choices: []Choice{
				{Value: "viewer", Label: "Viewer", Help: "Can look at channels and messages."},
				{Value: "editor", Label: "Editor", Help: "Can change channels."},
				{Value: "admin", Label: "Administrator", Help: "Can change accounts and settings."},
			},
			// Platform is deliberately not an option. It crosses tenant boundaries, and creating a group in
			// an identity provider is not a privileged action - so it must not be a route to the role that is.
			Sensitive: true,
		},

		// -------------------------------------------------------------- Alerts
		{
			Key:      "alerts.webhook",
			Group:    GroupAlerts,
			Subgroup: "Delivery",
			Label:    "Send alerts to this address",
			Help: "An HTTPS address that receives an alert as JSON - a Slack or Teams incoming webhook, or anything " +
				"that accepts a POST. Leave empty to keep alerts inside Perfuse only.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "",
			Flag:    "alert-webhook",
			Validate: func(v any) error {
				raw, _ := v.(string)
				if raw == "" {
					return nil
				}
				u, err := url.Parse(raw)
				if err != nil {
					return fmt.Errorf("that is not a valid address: %w", err)
				}
				// Refused rather than warned about. An alert says which channel is failing, and a channel
				// name names the system at the other end, so plain HTTP would put that on the network in
				// clear text - and an alert is sent precisely when nobody is watching closely.
				if u.Scheme != "https" {
					return fmt.Errorf("the address must start with https:// - an alert names your channels, " +
						"and plain http would send that in the clear")
				}
				if u.Host == "" {
					return fmt.Errorf("the address has no host")
				}

				return nil
			},
			Sensitive: true,
		},
		{
			Key:      "alerts.minSeverity",
			Group:    GroupAlerts,
			Subgroup: "Delivery",
			Label:    "Only send alerts at least this serious",
			Help: "Anything below this is recorded here but not sent on. The point is to keep the destination worth " +
				"reading - a channel that alerts on everything is one people mute.",
			Kind:    KindChoice,
			Widget:  WidgetRadio,
			Effect:  EffectLive,
			Default: "warning",
			Choices: []Choice{
				{Value: "info", Label: "Everything", Help: "Including routine notices."},
				{Value: "warning", Label: "Warnings and errors", Help: "Something needs attention soon."},
				{Value: "error", Label: "Errors only", Help: "Something is broken now."},
			},
			Flag: "alert-severity",
		},
		{
			Key:      "alerts.throttleMinutes",
			Group:    GroupAlerts,
			Subgroup: "Delivery",
			Label:    "Wait between repeats of the same alert",
			Help: "How long before the same alert is sent again. Without this, one broken channel sends an alert per " +
				"failed message, which buries every other alert at the moment they matter most.",
			Kind:     KindInt,
			Widget:   WidgetSlider,
			Effect:   EffectRestart,
			Default:  15,
			Min:      intp(0),
			Max:      intp(1440),
			Unit:     "minutes",
			Advanced: true,
		},

		// ---------------------------------------------------------- Monitoring
		{
			Key:      "monitoring.jsonLogs",
			Group:    GroupObserve,
			Subgroup: "Logging",
			Label:    "Write logs as JSON",
			Help: "One JSON object per line, for a log collector. Off gives lines meant for a person reading a " +
				"terminal. Neither is more detailed than the other; it is only the shape that differs.",
			Kind:    KindBool,
			Widget:  WidgetToggle,
			Effect:  EffectRestart,
			Default: false,
			Flag:    "json-logs",
		},
		{
			Key:      "monitoring.traceEndpoint",
			Group:    GroupObserve,
			Subgroup: "Tracing",
			Label:    "Send traces to",
			Help: "An OpenTelemetry collector address. Traces show where time went inside a channel, which is what " +
				"answers 'why is this interface slow' rather than 'is it slow'.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "",
			Flag:    "trace-endpoint",
		},
		{
			Key:      "monitoring.traceSamplePercent",
			Group:    GroupObserve,
			Subgroup: "Tracing",
			Label:    "Trace this share of messages",
			Help: "Tracing every message on a busy interface costs more than the interface. Ten percent is usually " +
				"enough to see a pattern; raise it while chasing something specific.",
			Kind:     KindInt,
			Widget:   WidgetSlider,
			Effect:   EffectRestart,
			Default:  10,
			Min:      intp(0),
			Max:      intp(100),
			Unit:     "percent",
			Advanced: true,
		},

		// -------------------------------------------------------------- Engine
		{
			Key:      "engine.queueWorkers",
			Group:    GroupEngine,
			Subgroup: "Throughput",
			Label:    "Messages delivered at once per destination",
			Help: "More gets through a backlog faster. Above one, messages can arrive out of order, which some " +
				"receiving systems mind a great deal - a patient update overtaking the admission that created " +
				"the patient is rejected.",
			Kind:    KindInt,
			Widget:  WidgetSlider,
			Effect:  EffectRestart,
			Default: 1,
			Min:     intp(1),
			Max:     intp(64),
			Unit:    "at a time",
		},
		{
			Key:      "engine.retryBackoff",
			Group:    GroupEngine,
			Subgroup: "Retries",
			Label:    "Wait before the first retry",
			Help: "How long to wait after a delivery fails, doubling each time up to the maximum below. Too short " +
				"and a system that is merely restarting gets hammered while it starts.",
			Kind:    KindDuration,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "5s",
			Validate: func(v any) error {
				return validateDuration(v, time.Second, 10*time.Minute)
			},
		},
		{
			Key:      "engine.retryMax",
			Group:    GroupEngine,
			Subgroup: "Retries",
			Label:    "Longest wait between retries",
			Help: "The ceiling on the doubling above. This is how long a queue can sit still while the far end is " +
				"down, so it is also how stale the first message will be when it comes back.",
			Kind:    KindDuration,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "5m",
			Validate: func(v any) error {
				return validateDuration(v, time.Second, 24*time.Hour)
			},
		},
		{
			Key:      "engine.drainFor",
			Group:    GroupEngine,
			Subgroup: "Shutdown",
			Label:    "Wait for in-flight messages on shutdown",
			Help: "How long to keep delivering before stopping. Zero drops whatever is in flight, which means " +
				"resending it later and possibly twice.",
			Kind:    KindDuration,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "30s",
			Flag:    "drain-for",
			Validate: func(v any) error {
				return validateDuration(v, 0, 30*time.Minute)
			},
		},

		// ---------------------------------------------------------------- FHIR
		{
			Key:      "fhir.readOnly",
			Group:    GroupFHIR,
			Subgroup: "Access",
			Label:    "Refuse writes to the FHIR endpoint",
			Help: "Allows reads and rejects anything that would change data. Worth having on unless something " +
				"genuinely needs to write, since a read-only endpoint cannot be used to alter a record.",
			Kind:      KindBool,
			Widget:    WidgetToggle,
			Effect:    EffectLive,
			Default:   false,
			Flag:      "fhir-read-only",
			Sensitive: true,
		},
		{
			Key:      "fhir.pageSize",
			Group:    GroupFHIR,
			Subgroup: "Access",
			Label:    "Results per page",
			Help: "How many resources a search returns at once. Large pages are fewer requests and more memory per " +
				"request, and some clients will not follow the link to the next page at all.",
			Kind:     KindInt,
			Widget:   WidgetNumber,
			Effect:   EffectLive,
			Default:  50,
			Min:      intp(1),
			Max:      intp(1000),
			Unit:     "resources",
			Advanced: true,
		},

		// --------------------------------------------------------------- Fleet
		{
			Key:      "fleet.label",
			Group:    GroupFleet,
			Subgroup: "This server",
			Label:    "Name for this server",
			Help: "How this server identifies itself in the fleet view. Worth using the name people actually say - " +
				"'Radiology test' beats a hostname nobody recognises when something is wrong at two in the morning.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectLive,
			Default: "",
			Flag:    "fleet-label",
		},

		// --------------------------------------------------------------- TEFCA
		//
		// Configurable from the interface rather than from a file, like everything else here. Taking part in national
		// exchange needs an agreement with a QHIN and a certificate issued for it, neither of which this can arrange -
		// but once somebody has those, entering them should not require a terminal on the server.
		{
			Key:      "tefca.organisationName",
			Group:    GroupTEFCA,
			Subgroup: "This participant",
			Label:    "Organisation name",
			Help: "The name this organisation exchanges under, as agreed with the QHIN. It appears in every audit " +
				"entry at both ends, so it wants to be the name a partner would recognise on a support call.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "",
		},
		{
			Key:      "tefca.organisationOID",
			Group:    GroupTEFCA,
			Subgroup: "This participant",
			Label:    "Organisation identifier",
			Help: "The OID assigned to this organisation, like 2.16.840.1.113883.19.5. This is what partners match " +
				"on, and a wrong one produces refusals that name an unknown participant rather than a wrong number.",
			Kind:     KindString,
			Widget:   WidgetText,
			Effect:   EffectRestart,
			Default:  "",
			Validate: validateOID,
		},
		{
			Key:      "tefca.participantType",
			Group:    GroupTEFCA,
			Subgroup: "This participant",
			Label:    "Kind of participant",
			Help: "What this organisation is on the network. It decides which exchanges partners will accept from " +
				"it: a payer asking for treatment data and a provider asking for the same thing are answered " +
				"differently.",
			Kind:   KindChoice,
			Widget: WidgetSelect,
			Effect: EffectRestart,
			Choices: []Choice{
				{
					Value: "provider", Label: "Provider",
					Help: "An organisation that delivers care. The widest access, because treatment is the purpose " +
						"partners answer most readily.",
				},
				{
					Value: "payer", Label: "Payer",
					Help: "An insurer or plan. Partners answer payment and operations requests and are more " +
						"restrictive about treatment data than they are for a provider.",
				},
				{
					Value: "public_health", Label: "Public health authority",
					Help: "A reporting authority. Exchange is for public health purposes, which have their own " +
						"legal basis and do not depend on a treatment relationship.",
				},
			},
			Default: "provider",
		},
		{
			Key:      "tefca.qhinEndpoint",
			Group:    GroupTEFCA,
			Subgroup: "Network",
			Label:    "QHIN endpoint",
			Help: "The base URL of the Qualified Health Information Network this participant exchanges through. " +
				"Given by the QHIN, and https by definition - the traffic is patient data crossing organisations.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "",
		},
		{
			Key:      "tefca.purposes",
			Group:    GroupTEFCA,
			Subgroup: "Network",
			Label:    "Purposes of use declared",
			Help: "Which purposes this participant exchanges for: treatment, payment, operations, public_health or " +
				"individual_access. An exchange claiming a purpose that is not listed here is refused before it " +
				"leaves, because otherwise the refusal comes from the partner after the request has been logged " +
				"at their end.",
			Kind:     KindList,
			Widget:   WidgetList,
			Effect:   EffectRestart,
			Default:  []string{"treatment"},
			Validate: validatePurposes,
		},
		{
			Key:      "tefca.certificatePath",
			Group:    GroupTEFCA,
			Subgroup: "Credentials",
			Label:    "Certificate file",
			Help: "The mutual-TLS certificate issued for TEFCA exchange. A path on this server, because the private " +
				"key it pairs with must never travel through a browser.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "",
		},
		{
			Key:      "tefca.keyPath",
			Group:    GroupTEFCA,
			Subgroup: "Credentials",
			Label:    "Private key file",
			Help: "The key for the certificate above. Never read by the interface and never sent to a browser; only " +
				"its location is configured here.",
			Kind:      KindString,
			Widget:    WidgetText,
			Effect:    EffectRestart,
			Default:   "",
			Sensitive: true,
		},
		{
			Key:      "tefca.auditPath",
			Group:    GroupTEFCA,
			Subgroup: "Credentials",
			Label:    "Audit trail file",
			Help: "Where the record of every exchange is written. Recording them is a condition of participation and " +
				"the records have to exist months later, so this is a file rather than memory. Left empty it sits " +
				"beside the database.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "",
		},

		// Facilitated FHIR, per the Sequoia Project SOP effective 8 March 2026.
		//
		// A separate subgroup from the credentials above because these describe the partner and this client's identity within a trust
		// community, which is a different question from which key file to sign with. Everything here is reachable from the interface for
		// the standing reason: a feature configurable only by editing YAML is a feature most operators do not have.
		{
			Key:      "tefca.partnerFHIRBase",
			Group:    GroupTEFCA,
			Subgroup: "Facilitated FHIR",
			Label:    "Partner FHIR base URL",
			Help: "The responding participant's FHIR base URL. Its UDAP metadata is discovered at " +
				"{base}/.well-known/udap, and the endpoints used for exchange come from the signed part of that " +
				"document rather than from the document body.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "",
		},
		{
			Key:      "tefca.clientURI",
			Group:    GroupTEFCA,
			Subgroup: "Facilitated FHIR",
			Label:    "This client's community URI",
			Help: "How the trust community knows this client. It must appear as a URI in the subject alternative " +
				"names of the certificate above, and the server refuses to start if it does not - without that " +
				"binding, any member of a community could register as any other.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "",
		},
		{
			Key:      "tefca.trustAnchorPath",
			Group:    GroupTEFCA,
			Subgroup: "Facilitated FHIR",
			Label:    "Trust anchor bundle",
			Help: "The community's certificate authorities, in PEM form. Required, because a partner's signed " +
				"metadata can otherwise only be checked against the certificate that arrived with it - which " +
				"proves that one party made both and nothing else. Include the intermediates: a UDAP server need " +
				"only send its leaf certificate, and the public reference server does exactly that.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "",
		},
		{
			Key:      "tefca.clientName",
			Group:    GroupTEFCA,
			Subgroup: "Facilitated FHIR",
			Label:    "Client name for registration",
			Help:     "The human readable name a partner's authorization server records for this client.",
			Kind:     KindString,
			Widget:   WidgetText,
			Effect:   EffectRestart,
			Default:  "",
		},
		{
			Key:      "tefca.contacts",
			Group:    GroupTEFCA,
			Subgroup: "Facilitated FHIR",
			Label:    "Operational contacts",
			Help: "At least one mailto address, which the specification requires and which exists for a practical " +
				"reason: when an exchange starts failing at three in the morning, the other organisation needs " +
				"somebody to tell.",
			Kind:    KindList,
			Widget:  WidgetList,
			Effect:  EffectRestart,
			Default: []string{},
		},
		{
			Key:      "tefca.scope",
			Group:    GroupTEFCA,
			Subgroup: "Facilitated FHIR",
			Label:    "Requested scope",
			Help: "The space-delimited scopes requested at registration. System scopes, since this exchange is " +
				"machine to machine with no user at a browser.",
			Kind:    KindString,
			Widget:  WidgetText,
			Effect:  EffectRestart,
			Default: "system/Patient.read system/DocumentReference.read",
		},

		// ------------------------------------------------------------ Branding
		//
		// A site running this for its own customers needs it to look like their product rather than
		// like somebody else's. All of it takes effect immediately, because the point of a branding
		// screen is to try something and look at it.
		{
			Key:      "branding.productName",
			Group:    GroupBranding,
			Subgroup: "Name",
			Label:    "Product name",
			Help: "Replaces the word Perfuse everywhere it appears: the sign-in page, the header, the " +
				"browser tab. Leave it empty to keep Perfuse.",
			Kind:     KindString,
			Widget:   WidgetText,
			Effect:   EffectLive,
			Default:  "",
			Validate: validateBrandText(40),
		},
		{
			Key:      "branding.tagline",
			Group:    GroupBranding,
			Subgroup: "Name",
			Label:    "Tagline",
			Help: "One line under the product name on the sign-in page. Left empty, nothing is shown " +
				"rather than a blank space.",
			Kind:     KindString,
			Widget:   WidgetText,
			Effect:   EffectLive,
			Default:  "",
			Validate: validateBrandText(80),
		},
		{
			Key:      "branding.accentColour",
			Group:    GroupBranding,
			Subgroup: "Colour",
			Label:    "Accent colour",
			Help: "The highlight colour used for the active view, links, focus rings and charts. Given " +
				"as a hex colour such as #0ea5e9. Contrast against the dark background is checked, " +
				"because an accent nobody can read is worse than the default.",
			Kind:     KindString,
			Widget:   WidgetText,
			Effect:   EffectLive,
			Default:  "",
			Validate: validateAccentColour,
		},
	}
}

// validateBrandText refuses text that would break the layout or carry markup.
//
// Length is bounded because these strings go into a fixed header, and a two hundred character product
// name does not wrap, it overlaps. Control characters are refused because they are invisible in the
// form and visible in the output.
// validateOID checks an object identifier is at least shaped like one.
//
// Not a full check against a registry, which would need the network. But a value with letters in it or with empty arcs
// is definitely wrong, and finding that out here beats finding out when a partner rejects every exchange with a message
// about an unknown participant.
func validateOID(v any) error {
	s, ok := v.(string)
	if !ok || strings.TrimSpace(s) == "" {
		return nil
	}
	s = strings.TrimSpace(s)

	// The urn:oid: prefix is accepted and stripped, because half the specifications write it that way and somebody
	// pasting from one of those should not be told their identifier is malformed.
	s = strings.TrimPrefix(s, "urn:oid:")

	parts := strings.Split(s, ".")
	if len(parts) < 2 {
		return fmt.Errorf("an object identifier has at least two parts separated by dots, like 2.16.840.1.113883.19.5")
	}
	for _, part := range parts {
		if part == "" {
			return fmt.Errorf("that has an empty part in it, so one of the dots is doubled or trailing")
		}
		for _, r := range part {
			if r < '0' || r > '9' {
				return fmt.Errorf("an object identifier is only digits and dots, and that contains %q", r)
			}
		}
	}
	return nil
}

// validatePurposes checks a list of purposes of use against the ones TEFCA recognises.
//
// Refused here rather than at the moment of an exchange. A purpose nobody recognises produces a refusal from a partner
// that names the purpose and not the fact that it was never valid, and the round trip to find that out is expensive.
func validatePurposes(v any) error {
	list, ok := v.([]string)
	if !ok {
		return nil
	}

	valid := map[string]bool{
		"treatment": true, "payment": true, "operations": true,
		"public_health": true, "individual_access": true,
	}

	seen := map[string]bool{}
	for _, p := range list {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if !valid[p] {
			return fmt.Errorf("%q is not a purpose of use TEFCA recognises. The recognised ones are treatment, "+
				"payment, operations, public_health and individual_access", p)
		}
		// A duplicate is named rather than tolerated. It means somebody added the same purpose twice and probably
		// meant to add a different one, and a list that silently deduplicates hides the mistake.
		if seen[p] {
			return fmt.Errorf("%q is listed twice", p)
		}
		seen[p] = true
	}
	return nil
}

func validateBrandText(max int) func(any) error {
	return func(v any) error {
		s, ok := v.(string)
		if !ok {
			return fmt.Errorf("expected text")
		}
		if len([]rune(s)) > max {
			return fmt.Errorf("keep it to %d characters or fewer; this appears in the header and longer text overlaps rather than wrapping", max)
		}
		if strings.TrimSpace(s) != s {
			return fmt.Errorf("there is a space at the start or end")
		}
		for _, r := range s {
			if r < 0x20 || r == 0x7f {
				return fmt.Errorf("that contains a control character")
			}
		}
		return nil
	}
}

// validateAccentColour refuses anything that is not a hex colour, and refuses one too dark to read.
//
// The check on brightness is the point. An accent is used for the active view and for focus rings, so
// a customer who picks their brand's very dark navy makes the interface unusable rather than branded -
// and they would reasonably blame the product. Refusing at the point of entry, with the reason, is
// kinder than accepting it and letting them discover it.
func validateAccentColour(v any) error {
	s, ok := v.(string)
	if !ok {
		return fmt.Errorf("expected text")
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil // empty means the default
	}
	if !hexColour.MatchString(s) {
		return fmt.Errorf("give a hex colour such as #0ea5e9")
	}

	r, g, b := parseHexColour(s)
	// Relative luminance, the sRGB coefficients. Compared against a floor rather than computing a
	// full contrast ratio because the background is known to be near black, so luminance alone
	// decides whether the accent is legible on it.
	lum := 0.2126*channelLuminance(r) + 0.7152*channelLuminance(g) + 0.0722*channelLuminance(b)
	if lum < 0.12 {
		return fmt.Errorf("that colour is too dark to read against the dark background; try a lighter shade of it")
	}
	return nil
}

var hexColour = regexp.MustCompile(`^#(?:[0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

func parseHexColour(s string) (r, g, b float64) {
	h := strings.TrimPrefix(s, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	var v [3]int64
	for i := 0; i < 3; i++ {
		v[i], _ = strconv.ParseInt(h[i*2:i*2+2], 16, 32)
	}
	return float64(v[0]) / 255, float64(v[1]) / 255, float64(v[2]) / 255
}

func channelLuminance(c float64) float64 {
	if c <= 0.03928 {
		return c / 12.92
	}
	return math.Pow((c+0.055)/1.055, 2.4)
}

// validateBareDomain refuses anything that is not a bare hostname.
//
// The commonest mistake by a wide margin is pasting a URL, and it produces credentials that register and then never verify -
// which surfaces at somebody's next sign-in rather than at the mistake.
func validateBareDomain(host string) error {
	switch {
	case strings.Contains(host, "://"):
		return fmt.Errorf("give just the domain, with no https:// in front")
	case strings.Contains(host, "/"):
		return fmt.Errorf("give just the domain, with no path after it")
	case strings.Contains(host, ":"):
		return fmt.Errorf("give just the domain, with no port")
	case strings.TrimSpace(host) != host:
		return fmt.Errorf("there is a space at the start or end")
	case !strings.Contains(host, "."):
		return fmt.Errorf("that does not look like a domain name")
	}

	return nil
}

// validateDuration parses a Go duration and bounds it.
func validateDuration(v any, min, max time.Duration) error {
	raw, _ := v.(string)

	d, err := time.ParseDuration(raw)
	if err != nil {
		return fmt.Errorf("write it as a length of time, like 30s or 5m")
	}
	if d < min {
		return fmt.Errorf("that is shorter than %s", min)
	}
	if d > max {
		return fmt.Errorf("that is longer than %s", max)
	}

	return nil
}
