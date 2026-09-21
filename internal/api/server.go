// Package api serves the HTTP interface used by the web front end.
//
// The important design constraint: channels are read from and written to files
// on disk. The API is an editor for those files, not a different storage system
// that happens to export YAML. A channel created in the browser and one written
// by hand are the same artifact, which is what makes "export to share" trivial
// and keeps git as the history of record.
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/biodream-llc/perfuse/internal/alerts"
	"github.com/biodream-llc/perfuse/internal/authlimit"
	"github.com/biodream-llc/perfuse/internal/branding"
	"github.com/biodream-llc/perfuse/internal/msgstore"
	"github.com/biodream-llc/perfuse/internal/peers"
	"github.com/biodream-llc/perfuse/internal/settings"
	"github.com/biodream-llc/perfuse/internal/store"
	"github.com/biodream-llc/perfuse/internal/tefca"
	"github.com/biodream-llc/perfuse/internal/webauthn"
)

// Server is the HTTP API.
type Server struct {
	// Channels manages the channel files on disk.
	Channels *ChannelRepo
	// Alerts is the alert evaluator, nil when alerting is off.
	Alerts *alerts.Evaluator

	// Store holds users, sessions and the audit log.
	Store *store.Store
	// Log receives request and error events.
	Log *slog.Logger
	// OIDC configures federated sign-in. Nil when it is not configured.
	OIDC *OIDCConfig

	// SAML is SAML 2.0 sign-in, nil when not configured.
	SAML *SAMLConfig

	// PasskeyRPID is the relying party identifier passkeys are registered for: a bare domain.
	//
	// This is what binds a credential to this site and makes it unphishable. Empty disables passkeys, because there is no
	// safe default: guessing it from a request's Host header would let whoever controls DNS decide what a credential is
	// bound to, which is the one thing the value exists to prevent.
	PasskeyRPID string

	// PasskeyRPName is what the authenticator shows a person when they register.
	PasskeyRPName string

	// PasskeyOrigins are the exact origins accepted, scheme and port included.
	PasskeyOrigins []string

	// SCIMDefaultRole is the role an account gets when the identity provider does not say.
	//
	// Viewer unless an operator changes it, and the default is downward on purpose: an account whose role could not be
	// determined should be able to read and nothing else. Defaulting upward would hand out administrator to anybody the
	// provider failed to describe, and provisioning failures are exactly the case where a description goes missing.
	SCIMDefaultRole store.Role

	// SCIMEnabled turns the provisioning endpoints on.
	//
	// Off by default. These endpoints create and delete accounts, so an installation that is not being provisioned by an
	// identity provider should not expose them at all - an endpoint nobody uses is one nobody is watching.
	SCIMEnabled bool

	// PasskeyAttestation is what an operator requires of an authenticator's own attestation.
	//
	// Zero verifies nothing and claims nothing, which is the right default: attestation answers "which device", not
	// "is this legitimate", and the origin binding does the security work either way.
	PasskeyAttestation webauthn.AttestationPolicy

	// MetricsAccessFn supplies the configured metrics access mode, superseding MetricsOpen when set.
	//
	// A function so the setting applies without a restart, read at request time. Nil falls back to the flags.
	MetricsAccessFn func() string

	// MetricsToken is a dedicated credential for the Prometheus scrape endpoint.
	//
	// Separate from an API token because a scraper cannot sign in, cannot hold a cookie and should not hold a
	// credential that also permits writes. Empty means no scrape token is configured, and then only a signed-in
	// operator can read metrics.
	MetricsToken string

	// MetricsOpen serves metrics to anybody who can reach the port.
	//
	// Was the only behaviour, and remains available because plenty of installations really do have the port bound to a
	// private interface. Named rather than implied so it shows up in the command line of any server running this way:
	// "we thought it was firewalled" is how this turns into a finding.
	MetricsOpen bool

	// SettingsPaths are the configuration files the interface may edit.
	//
	// Paths rather than parsed values, because the interface edits the same files the flags point at. Keeping a second
	// copy of these settings in the database would raise the question of which one wins, and the answer would be
	// discovered during an incident.
	SettingsPaths SettingsFiles

	// Startup describes what the process was started with, for the read-only half of the settings page.
	//
	// Shown rather than hidden. Somebody diagnosing a problem needs to know how the process was started, and "not
	// shown anywhere" is how a wrong flag survives for months.
	Startup StartupSettings

	// Started is when this process began, for the settings page.
	Started time.Time

	// LDAP configures directory sign-in. Nil when it is not configured.
	//
	// Separate from OIDC rather than one "external identity" field, because both can be configured at once and they
	// answer different problems: OIDC needs a browser that can reach the provider, and a directory does not.
	LDAP *LDAPConfig

	// authAttempts holds federated sign-in attempts between the redirect out and the callback back.
	authAttempts     *authAttempts
	authAttemptsOnce sync.Once

	// samlPending holds the ids of SAML AuthnRequests waiting for an answer, which is what lets a response be tied to a login
	// somebody began here rather than accepted from anyone who presents one.
	samlPending *samlRequests
	samlOnce    sync.Once

	// loginLimit throttles failed sign-ins by source address.
	//
	// Added after measuring: twenty password guesses a second sustained against the administrator account, with no
	// delay and no lockout, and the only trace a log full of identical warnings. At that rate a weak password is
	// gone overnight.
	loginLimit     *authlimit.Limiter
	loginLimitOnce sync.Once

	// SecureCookies marks the session cookie Secure. It should be on whenever
	// the server is reachable over anything but plain localhost.
	SecureCookies bool
	// StaticHandler serves the built front end, if one is embedded.
	StaticHandler http.Handler

	// Repos hands out a channel repository per tenant. Nil in single-tenant
	// operation, where Channels is used directly.
	Repos *TenantRepos

	// Runtimes hands out one engine per tenant, nil in single-tenant operation.
	//
	// When set it takes precedence over Runtime, and handlers must reach engines
	// through runtimeFor rather than reading Runtime - a test enforces that,
	// because it is the mistake review cannot catch across sixty handlers.
	Runtimes *TenantRuntimes

	// Runtime owns the running engine. When nil, the interface can edit channels
	// but not run them, and the endpoints that need it say so rather than
	// pretending.
	Runtime *Runtime

	// Version is reported by the probe endpoints. During a rollback, which build
	// is answering is the only question anybody has.
	Version string

	// Settings is every option an operator may change, and the file they live in.
	//
	// Nil on a server started without one, which is legitimate: the engine runs on declared defaults and the settings
	// area says it cannot save rather than presenting a form whose button fails.
	Settings *settings.Store

	// routes records what Handler registered, so a test can check the whole surface. Set only while
	// Routes is running; nil in normal operation, when nothing is recorded.
	routes *routeRegistry

	// Branding holds a customer's logo, so an installation can present itself as their product.
	//
	// Optional: nil means the built-in mark, and an upload is refused rather than kept in memory.
	Branding *branding.Store

	// restartPending remembers settings changed since startup that need a restart.
	//
	// In memory deliberately. After a restart nothing is pending, which is what an empty map says, so persisting it would
	// mean clearing it correctly on startup - and getting that wrong leaves a banner nobody can dismiss.
	restartMu      sync.RWMutex
	restartPending map[string]bool

	// fleetLabelMu guards FleetLabel, which is the one live setting held as a plain field.
	//
	// It is read when a fleet report is built and written when somebody saves settings, and the race detector is right to
	// object to that without a lock.
	fleetLabelMu sync.Mutex

	// FleetLabel names this instance in a fleet view. Empty means the hostname.
	//
	// Configured rather than taken from anything in a message, following the rule
	// that user-visible labels never come from message content.
	FleetLabel string

	// PeersFile is where fleet peers are stored, and empty when this server has nowhere to put them.
	//
	// Needed because peers can now be added from the interface. Without a path the Fleet section says so rather
	// than offering a form whose save would fail.
	PeersFile string

	// SelfURL is this server's own address, used only to refuse a peer that points back here.
	//
	// A self-referencing peer polls this instance through its own HTTP stack and then appears twice in the fleet
	// view - once as this server and once as a peer - which reads as two machines and is one.
	SelfURL string

	// TLSCertFile and TLSKeyFile are the server's own certificate and key, when it was started with them.
	//
	// Held as paths rather than as a loaded key pair so they can be read at the moment they are used. Signing is
	// rare, the files are small, and a cached key means a replaced certificate keeps producing signatures against
	// the old one until somebody restarts - with nothing on screen to explain why.
	TLSCertFile string
	TLSKeyFile  string

	// TEFCA is this instance's participation in national exchange, nil when it does not participate.
	//
	// Held as a Participant rather than as a configuration, because a Participant cannot exchange without auditing -
	// there is no unaudited path through it, so nothing here has to remember to record anything.
	TEFCA *tefca.Participant

	// TEFCAAudit is the trail, kept separately so the audit view can read the file rather than only memory.
	TEFCAAudit *tefca.PersistentAuditLog

	// Fleet polls other instances, nil when no peers are configured.
	//
	// Any instance can be the aggregator, because the console ships inside every
	// binary. Mirth needed a separate product for this - a plug-in registering
	// with a Command Center server - because its administrator is a desktop
	// application and there was nowhere in that design to put a fleet view.
	Fleet *peers.Fleet

	// life tracks whether this instance is starting, serving or draining.
	life lifecycle
}

// sessionCookie is the name of the cookie holding the session token.
const sessionCookie = "perfuse_session"

// Handler returns the routed HTTP handler.
func (s *Server) Handler() http.Handler {
	mux := newRecordingMux(s.routes)

	// Unauthenticated: only what a login page needs.
	mux.HandleFunc("POST /api/login", s.handleLogin)

	// Federated sign-in. Both are unauthenticated by necessity: they are how somebody who is not yet signed in does so.
	mux.HandleFunc("GET /api/auth/methods", s.handleAuthMethods)

	// Branding is readable without a session, because the sign-in page is the first thing a customer
	// sees and an unbranded one defeats the purpose. What it exposes is a name, a colour and an image
	// that an operator chose specifically in order to show them to people.
	mux.HandleFunc("GET /api/branding", s.handleBranding)
	mux.HandleFunc("GET /api/branding/logo", s.handleBrandingLogo)
	mux.HandleFunc("HEAD /api/branding/logo", s.handleBrandingLogo)

	// Changing it is an administrator's job: it is what every user of the system sees.
	mux.Handle("PUT /api/branding/logo", s.require(store.RoleAdmin, s.handleBrandingLogoUpload))
	mux.Handle("DELETE /api/branding/logo", s.require(store.RoleAdmin, s.handleBrandingLogoDelete))
	mux.Handle("POST /api/branding/preview", s.require(store.RoleAdmin, s.handleBrandingPreview))
	mux.HandleFunc("GET /auth/oidc/start", s.handleOIDCStart)

	// SAML. Both are unauthenticated because they are how somebody who is not signed in signs in, and the assertion consumer is a
	// POST from a form the identity provider rendered in the user's browser - it cannot carry a Perfuse header, which is why the
	// signature, audience, destination, conditions, assertion-id cache and InResponseTo check all have to hold before it is trusted.
	mux.HandleFunc("GET /auth/saml/start", s.handleSAMLStart)
	mux.HandleFunc("POST /auth/saml/acs", s.handleSAMLACS)
	mux.HandleFunc("GET /auth/callback", s.handleOIDCCallback)
	mux.HandleFunc("GET /api/health", s.handleHealth)

	// Probes are unauthenticated. A kubelet has no credentials, and issuing it
	// some would be a worse trade than disclosing that a process is running.
	// Neither endpoint reveals anything a port scan does not already prove.
	// Tenant administration. Platform role throughout, not admin: a customer's own
	// administrator must not be able to create tenants or see that the others exist.
	mux.Handle("GET /api/tenancy", s.require(store.RoleViewer, s.handleTenancyStatus))
	mux.Handle("GET /api/tenants", s.require(store.RolePlatform, s.handleListTenants))
	mux.Handle("POST /api/tenants", s.require(store.RolePlatform, s.handleCreateTenant))
	mux.Handle("PUT /api/tenants/{id}", s.require(store.RolePlatform, s.handleUpdateTenant))
	mux.Handle("DELETE /api/tenants/{id}", s.require(store.RolePlatform, s.handleDeleteTenant))

	// Editor rather than viewer: only somebody who can write a script needs to check
	// one, and it accepts arbitrary JavaScript to parse. It compiles and never executes.
	mux.Handle("POST /api/scripts/check", s.require(store.RoleEditor, s.handleCheckScript))

	mux.HandleFunc("GET /livez", s.handleLiveness)
	mux.HandleFunc("GET /readyz", s.handleReadiness)

	// Session.
	mux.Handle("POST /api/logout", s.require(store.RoleViewer, s.handleLogout))
	mux.Handle("GET /api/me", s.require(store.RoleViewer, s.handleMe))
	mux.Handle("POST /api/me/password", s.require(store.RoleViewer, s.handleChangeOwnPassword))

	// Channels. Reading needs viewer, changing needs editor.
	mux.Handle("GET /api/channels", s.require(store.RoleViewer, s.handleListChannels))
	mux.Handle("GET /api/channels/{name}", s.require(store.RoleViewer, s.handleGetChannel))
	mux.Handle("GET /api/channels/{name}/yaml", s.require(store.RoleViewer, s.handleGetChannelYAML))
	mux.Handle("GET /api/channels/{name}/mirth", s.require(store.RoleViewer, s.handleExportMirth))
	mux.Handle("GET /api/channels/{name}/mirth/preview", s.require(store.RoleViewer, s.handleExportMirthPreview))
	// Validation writes nothing, but it compiles arbitrary filter expressions and
	// scripts, so it is gated the same as creating a channel rather than as reading one.
	mux.Handle("POST /api/channels/validate", s.require(store.RoleEditor, s.handleValidateChannel))
	// Editor: it compiles the channel to prove the round trip is faithful, exactly as validate does.
	mux.Handle("POST /api/channels/parse", s.require(store.RoleEditor, s.handleParseChannel))
	mux.Handle("POST /api/mirth/import", s.require(store.RoleEditor, s.handleImportMirth))
	mux.Handle("POST /api/mapper/suggest", s.require(store.RoleEditor, s.handleMapperSuggest))
	mux.Handle("POST /api/profiles/export", s.require(store.RoleViewer, s.handleProfileExport))
	mux.Handle("POST /api/profiles/import", s.require(store.RoleViewer, s.handleProfileImport))
	mux.Handle("POST /api/mappings/approve", s.require(store.RoleEditor, s.handleApproveMappings))
	mux.Handle("POST /api/mappings/recipe", s.require(store.RoleEditor, s.handleRecipeFromMappings))
	mux.Handle("POST /api/recipes/export", s.require(store.RoleEditor, s.handleRecipeExport))
	mux.Handle("POST /api/recipes/import", s.require(store.RoleEditor, s.handleRecipeImport))
	mux.Handle("GET /api/contracts", s.require(store.RoleViewer, s.handleContracts))
	mux.Handle("POST /api/channels/{name}/contract/propose",
		s.require(store.RoleEditor, s.handleProposeContract))
	mux.Handle("PUT /api/channels/{name}/contract", s.require(store.RoleEditor, s.handleSaveContract))

	// The fleet view, and the endpoint another instance polls. Both are viewer-scoped: reading health is the least
	// privilege there is, and a token that only needs to see whether a server is up should not need more.
	// Shared mapping tables and what each one affects. A shared table is shared so that one edit reaches every
	// channel using it, which is the feature and also the hazard.
	mux.Handle("GET /api/codesets", s.require(store.RoleViewer, s.handleCodesets))
	mux.Handle("PUT /api/codesets/table", s.require(store.RoleEditor, s.handleWriteTable))

	mux.Handle("GET /api/fleet", s.require(store.RoleViewer, s.handleFleet))
	mux.Handle("GET /api/fleet/peers", s.require(store.RoleAdmin, s.handleListPeers))
	mux.Handle("PUT /api/fleet/peers", s.require(store.RoleAdmin, s.handleAddPeer))
	mux.Handle("DELETE /api/fleet/peers/{name}", s.require(store.RoleAdmin, s.handleRemovePeer))
	mux.Handle("GET "+peers.SelfPath, s.require(store.RoleViewer, s.handleFleetSelf))

	mux.Handle("POST /api/channels/build", s.require(store.RoleEditor, s.handleBuildChannel))

	// Tools for working out an HL7 v3 path. Viewer, after starting at editor and being wrong about it.
	//
	// The reasoning for editor was that this is a channel-building tool and channels are editor. But these endpoints
	// disclose nothing: they are pure functions of a message the caller pasted, so the caller already has everything the
	// answer contains. And they live on the Playground tab, which viewers can reach - so an editor floor would have put a
	// tool on a page that answers 403 when used, which is the failure that reads as a broken product.
	mux.Handle("POST /api/hl7v3/fields", s.require(store.RoleViewer, s.handleV3FieldTree))
	mux.Handle("POST /api/hl7v3/path", s.require(store.RoleViewer, s.handleV3PathCheck))
	mux.Handle("POST /api/channels", s.require(store.RoleEditor, s.handleCreateChannel))
	mux.Handle("PUT /api/channels/{name}", s.require(store.RoleEditor, s.handleUpdateChannel))
	mux.Handle("DELETE /api/channels/{name}", s.require(store.RoleEditor, s.handleDeleteChannel))

	// Validation is deliberately available to viewers: checking whether a
	// definition is valid changes nothing.
	mux.Handle("POST /api/validate", s.require(store.RoleViewer, s.handleValidate))

	// Engine status and control. Reading status needs viewer; starting or stopping
	// a channel moves patient data, so it needs admin.
	mux.Handle("GET /api/status", s.require(store.RoleViewer, s.handleStatus))
	mux.Handle("GET /api/events", s.require(store.RoleViewer, s.handleEvents))
	mux.Handle("POST /api/channels/{name}/start", s.require(store.RoleAdmin, s.handleStartChannel))
	mux.Handle("POST /api/channels/{name}/stop", s.require(store.RoleAdmin, s.handleStopChannel))

	// Messages. Reading them means reading patient data, so viewer is the floor
	// and reprocessing needs editor because it resends clinical data.
	mux.Handle("GET /api/messages", s.require(store.RoleViewer, s.handleListMessages))

	// Viewer: it reads stored messages, which a viewer can already do one at a time, and returns
	// no payloads. POST rather than GET because the expression carries quotes and operators and
	// belongs in a body rather than a proxy log.
	mux.Handle("POST /api/messages/search", s.require(store.RoleViewer, s.handleSearchMessages))

	// Finding a message by an identifier rather than by a path into its format. A separate
	// route from the expression search above because it answers a different question with a
	// different body, and folding them together would mean one handler guessing which was
	// meant from which fields were filled in.
	mux.Handle("POST /api/messages/find", s.require(store.RoleViewer, s.handleIdentitySearch))
	mux.Handle("GET /api/messages/{id}", s.require(store.RoleViewer, s.handleGetMessage))
	mux.Handle("POST /api/messages/{id}/reprocess", s.require(store.RoleEditor, s.handleReprocessMessage))
	mux.Handle("GET /api/stats", s.require(store.RoleViewer, s.handleStats))
	mux.Handle("GET /api/throughput", s.require(store.RoleViewer, s.handleThroughput))
	mux.Handle("GET /api/flow", s.require(store.RoleViewer, s.handleFlow))

	// Inspectors. These transform what the caller supplies and store nothing.
	mux.Handle("POST /api/inspect/hl7", s.require(store.RoleViewer, s.handleInspectHL7))
	mux.Handle("POST /api/inspect/fhir", s.require(store.RoleViewer, s.handleConvertToFHIR))
	mux.Handle("GET /api/fhir/versions", s.require(store.RoleViewer, s.handleFHIRVersions))
	mux.Handle("GET /api/dictionary", s.require(store.RoleViewer, s.handleDictionary))

	// Metrics. The JSON form is for the dashboard; the Prometheus form is
	// deliberately outside the session, because a scraper cannot hold a cookie and
	// what it exposes is counts and timings labelled from configuration, with no
	// message content.
	mux.Handle("GET /api/metrics", s.require(store.RoleViewer, s.handleMetrics))
	mux.Handle("GET /api/metrics/names", s.require(store.RoleViewer, s.handleMetricNames))
	mux.HandleFunc("GET /metrics", s.handlePrometheus)

	// Clinical documents. Reading one means reading patient data, so viewer is the
	// floor, and nothing supplied here is stored.
	mux.Handle("POST /api/inspect/document", s.require(store.RoleViewer, s.handleInspectDocument))
	mux.Handle("GET /api/document/types", s.require(store.RoleViewer, s.handleDocumentTypes))

	// Repairing a document that is valid and unreadable. Viewer, because nothing is stored and nothing on this
	// server changes: the document arrives in the request and goes back in the response.
	mux.Handle("POST /api/document/repair", s.require(store.RoleViewer, s.handleRepairDocument))

	// Comparing two medication lists across a transition of care. Nothing is stored; both documents arrive in the
	// request.
	mux.Handle("POST /api/document/reconcile", s.require(store.RoleViewer, s.handleReconcileDocuments))

	// Rendering a document for a person to read. A POST returning a file rather than a GET, deliberately: the
	// document is in the body, and putting patient data in a URL puts it in every access log and proxy along the
	// way.
	mux.Handle("POST /api/document/pdf", s.require(store.RoleViewer, s.handleRenderDocumentPDF))

	// Signatures. Checking one is a viewer action: anybody who can read a document should be able to find out
	// whether it has been altered, and withholding that makes the signature pointless. Making one is not - it
	// asserts that this organisation takes responsibility for a set of bytes, using the server's own key.
	// TEFCA participation. Reading the trail is a viewer action, because knowing what was disclosed about a patient
	// is not privileged information within an organisation - and an audit trail only one person can read is an audit
	// trail nobody checks.
	//
	// The exchanges themselves are deliberately not exposed here. A button that queries a national network for a
	// named patient discloses data on somebody's behalf, and the authority being exercised has to be established
	// first; Perfuse exchanges through channels, where the purpose and the requesting party come from configuration
	// rather than from whoever happens to be signed in.
	mux.Handle("GET /api/tefca/status", s.require(store.RoleViewer, s.handleTEFCAStatus))
	mux.Handle("GET /api/tefca/audit", s.require(store.RoleViewer, s.handleTEFCAAudit))
	mux.Handle("POST /api/tefca/purpose-check", s.require(store.RoleViewer, s.handleTEFCAPurposeCheck))

	// Combining several documents into one view. A reading aid, not a record: it is deliberately not offered as a
	// document to save or send, because nobody attested it.
	mux.Handle("POST /api/document/merge", s.require(store.RoleViewer, s.handleMergeDocuments))

	mux.Handle("POST /api/document/verify", s.require(store.RoleViewer, s.handleVerifyDocument))
	mux.Handle("GET /api/document/signing-identity", s.require(store.RoleViewer, s.handleSigningIdentity))
	mux.Handle("POST /api/document/sign", s.require(store.RoleAdmin, s.handleSignDocument))

	// Certificates. Reading the summary is a viewer action because expiry is
	// operational information everybody needs; inspecting an arbitrary path is
	// administrative, since probing the filesystem is not a viewer's business.
	mux.Handle("GET /api/certificates", s.require(store.RoleViewer, s.handleCertificates))
	mux.Handle("POST /api/certificates/inspect", s.require(store.RoleAdmin, s.handleInspectCertificate))

	// Channel history, read from git. Viewing is a viewer action; restoring an
	// old version is an edit and is audited as one.
	mux.Handle("GET /api/channels/{name}/history", s.require(store.RoleViewer, s.handleChannelHistory))
	mux.Handle("GET /api/channels/{name}/history/{hash}", s.require(store.RoleViewer, s.handleChannelVersion))
	mux.Handle("POST /api/channels/{name}/history/{hash}/restore", s.require(store.RoleEditor, s.handleRestoreChannelVersion))
	mux.Handle("GET /api/channels-repo", s.require(store.RoleViewer, s.handleChannelRepoStatus))

	// Alerts. Reading is a viewer action; acknowledging one is a decision that
	// something can wait, so it needs editor rights and is audited by name.
	mux.Handle("GET /api/alerts", s.require(store.RoleViewer, s.handleAlerts))
	mux.Handle("POST /api/alerts/acknowledge", s.require(store.RoleEditor, s.handleAcknowledgeAlert))
	// Viewer may read the rules, because knowing what is being watched is part of reading the state of the system. Only an
	// administrator may change them: a rule is the difference between being told about an outage and not.
	// Sign-on. Administrator only throughout, including reading: the configuration names a service account and the groups that
	// grant administrator rights, which is a map of how to attack the directory rather than operational state anybody needs.
	mux.Handle("GET /api/signon", s.require(store.RoleAdmin, s.handleSignon))
	mux.Handle("PUT /api/signon/oidc", s.require(store.RoleAdmin, s.handleSaveOIDC))
	mux.Handle("PUT /api/signon/ldap", s.require(store.RoleAdmin, s.handleSaveLDAP))
	mux.Handle("PUT /api/signon/saml", s.require(store.RoleAdmin, s.handleSaveSAML))
	mux.Handle("POST /api/signon/oidc/test", s.require(store.RoleAdmin, s.handleTestOIDC))
	mux.Handle("POST /api/signon/ldap/test", s.require(store.RoleAdmin, s.handleTestLDAP))
	mux.Handle("POST /api/signon/saml/test", s.require(store.RoleAdmin, s.handleTestSAML))
	mux.Handle("POST /api/signon/saml/metadata", s.require(store.RoleAdmin, s.handleReadSAMLMetadata))

	mux.Handle("GET /api/alerts/rules", s.require(store.RoleViewer, s.handleAlertRules))
	mux.Handle("PUT /api/alerts/rules", s.require(store.RoleAdmin, s.handleSaveAlertRules))

	// The queue.
	//
	// Retrying is an editor action: it is how somebody who has just fixed a
	// receiver gets the backlog moving, and needing to find an administrator at
	// three in the morning to press it would be its own outage.
	//
	// Skip, drain and remove abandon clinical messages that a sender was told we
	// had accepted. Those are administrative, and audited by name.
	mux.Handle("GET /api/queue", s.require(store.RoleViewer, s.handleQueue))
	mux.Handle("GET /api/shadows", s.require(store.RoleViewer, s.handleShadows))
	mux.Handle("GET /api/channels/{name}/shadow", s.require(store.RoleViewer, s.handleShadow))
	mux.Handle("PUT /api/channels/{name}/shadow", s.require(store.RoleEditor, s.handleSaveShadow))
	mux.Handle("DELETE /api/channels/{name}/shadow", s.require(store.RoleEditor, s.handleStopShadow))

	// Editor, not viewer. It compiles arbitrary filter expressions and scripts from the
	// request body, and the fact that a replay cannot deliver anything does not make
	// compiling somebody else's script a read operation.
	mux.Handle("POST /api/channels/{name}/replay", s.require(store.RoleEditor, s.handleReplayChannel))

	// Viewer, because a profile carries no message content by construction: counts, rates
	// and shapes, with coded values as the deliberate exception since a code table value is
	// not identifying.
	mux.Handle("GET /api/channels/{name}/testmessages", s.require(store.RoleViewer, s.handleGenerateTestMessages))
	mux.Handle("GET /api/channels/{name}/profile", s.require(store.RoleViewer, s.handleChannelProfile))
	mux.Handle("GET /manual", s.require(store.RoleViewer, s.handleManual))
	mux.Handle("POST /api/channels/{name}/parity", s.require(store.RoleEditor, s.handleParity))
	mux.Handle("POST /api/channels/from-sample", s.require(store.RoleEditor, s.handleReadSample))
	mux.Handle("POST /api/channels/{name}/synthesise", s.require(store.RoleViewer, s.handleSynthesise))

	// Viewer: a specification describes configuration without exposing it, and no credential
	// appears in one by construction. The analyst answering a vendor's question about an
	// interface should not need write access to do it.
	mux.Handle("GET /api/channels/{name}/spec", s.require(store.RoleViewer, s.handleChannelSpec))
	mux.Handle("GET /api/spec", s.require(store.RoleViewer, s.handleSpecBundle))

	// Editor: the trace cannot deliver anything, but it accepts an arbitrary message and runs a
	// channel's compiled filter and transformations over it, which is closer to editing than to
	// reading.
	mux.Handle("POST /api/channels/{name}/trace", s.require(store.RoleEditor, s.handleTraceMessage))
	mux.Handle("GET /api/queue/{id}", s.require(store.RoleViewer, s.handleQueueItem))
	mux.Handle("POST /api/queue/retry", s.require(store.RoleEditor, s.handleQueueRetry))
	mux.Handle("POST /api/queue/skip", s.require(store.RoleAdmin, s.handleQueueSkip))
	mux.Handle("POST /api/queue/drain", s.require(store.RoleAdmin, s.handleQueueDrain))
	mux.Handle("POST /api/queue/remove", s.require(store.RoleAdmin, s.handleQueueRemove))

	// Users and audit are administrative.
	mux.Handle("GET /api/users", s.require(store.RoleAdmin, s.handleListUsers))
	mux.Handle("POST /api/users", s.require(store.RoleAdmin, s.handleCreateUser))
	mux.Handle("PUT /api/users/{id}", s.require(store.RoleAdmin, s.handleUpdateUser))
	mux.Handle("DELETE /api/users/{id}", s.require(store.RoleAdmin, s.handleDeleteUser))
	// Passkeys, when configured. Registration and listing need a session; signing in cannot.
	//
	// Registered conditionally on the relying party identifier being set, because without it every call would fail and an
	// endpoint that always fails is one somebody spends an afternoon debugging.
	// Listing is always available, even with passkeys unconfigured, and answers an empty list with configured:false.
	//
	// It used to be inside the conditional below, which meant that on every installation without passkeys set up the path was unregistered, fell
	// through to the web application and answered 200 with HTML - so the users screen opened showing "the server sent a response that could not
	// be read". The conditional was written to avoid an endpoint that always fails being debugged for an afternoon; what it produced instead was
	// an error message on a screen everybody visits, which is the same afternoon with a worse starting point.
	mux.Handle("GET /api/passkeys", s.require(store.RoleViewer, s.handleListPasskeys))

	if s.PasskeyRPID != "" {
		mux.Handle("POST /api/passkeys/register/begin",
			s.require(store.RoleViewer, s.handleBeginPasskeyRegistration))
		mux.Handle("POST /api/passkeys/register/finish",
			s.require(store.RoleViewer, s.handleFinishPasskeyRegistration))
		mux.Handle("DELETE /api/passkeys/{id}", s.require(store.RoleViewer, s.handleDeletePasskey))

		// Signing in is necessarily unauthenticated, which is why these two are the ones with a throttle and with a
		// single indistinguishable failure message.
		mux.HandleFunc("POST /api/passkeys/signin/begin", s.handleBeginPasskeySignIn)
		mux.HandleFunc("POST /api/passkeys/signin/finish", s.handleFinishPasskeySignIn)
	}

	// SCIM provisioning, when it is turned on.
	//
	// Registered conditionally rather than always. These endpoints create and delete accounts, so an installation that is
	// not being provisioned by an identity provider should not expose them at all - an endpoint nobody uses is one
	// nobody is watching, and this one hands out access.
	//
	// Admin, and admin is the floor rather than a considered choice: creating accounts and setting their roles is
	// exactly what an administrator does. It is deliberately not platform, because provisioning is per-tenant and a
	// platform requirement would mean one credential shared between every customer's identity provider.
	if s.SCIMEnabled {
		mux.Handle("GET /scim/v2/ServiceProviderConfig",
			s.require(store.RoleAdmin, s.handleSCIMServiceProviderConfig))
		mux.Handle("GET /scim/v2/Users", s.require(store.RoleAdmin, s.handleSCIMListUsers))
		mux.Handle("POST /scim/v2/Users", s.require(store.RoleAdmin, s.handleSCIMCreateUser))
		mux.Handle("GET /scim/v2/Users/{id}", s.require(store.RoleAdmin, s.handleSCIMGetUser))
		mux.Handle("PUT /scim/v2/Users/{id}", s.require(store.RoleAdmin, s.handleSCIMPutUser))
		mux.Handle("PATCH /scim/v2/Users/{id}", s.require(store.RoleAdmin, s.handleSCIMPatchUser))
		mux.Handle("DELETE /scim/v2/Users/{id}", s.require(store.RoleAdmin, s.handleSCIMDeleteUser))
	}

	// API tokens. Admin, because a token is a credential: issuing one is issuing access.
	//
	// These could only be created from the command line, which meant reaching a shell on the server to let a
	// monitoring system read a dashboard - and that pushes people towards giving a script a person's password, which
	// is the thing tokens exist to avoid.
	mux.Handle("GET /api/tokens", s.require(store.RoleAdmin, s.handleListAPITokens))
	mux.Handle("POST /api/tokens", s.require(store.RoleAdmin, s.handleCreateAPIToken))
	mux.Handle("DELETE /api/tokens/{label}", s.require(store.RoleAdmin, s.handleRevokeAPIToken))

	// Settings. Reading needs admin, not viewer: these files hold a client secret and a directory service account
	// password, and although both are redacted for a lesser role, the safer default for a page that exists to show
	// configuration is that only an administrator opens it.
	// Registered only when there is a registry to describe. A server built without one would otherwise answer with an
	// empty settings area, which reads as "this version has no settings" rather than "this server was started without a
	// settings file".
	if s.Settings != nil {
		mux.Handle("GET /api/settings/schema", s.require(store.RoleAdmin, s.handleSettingsSchema))
		mux.Handle("PUT /api/settings/values", s.require(store.RoleAdmin, s.handleSettingsUpdate))
	}

	mux.Handle("GET /api/settings", s.require(store.RoleAdmin, s.handleSettings))
	mux.Handle("PUT /api/settings/{kind}", s.require(store.RoleAdmin, s.handleUpdateSettings))

	mux.Handle("GET /api/audit", s.require(store.RoleViewer, s.handleAudit))

	// The friction report. Admin only, because it says where this installation got stuck and how often the product refused somebody,
	// which is operational self-criticism rather than something a viewer needs.
	mux.Handle("GET /api/friction", s.require(store.RoleAdmin, s.handleFrictionReport))

	// Anything under /api/ that no route above claimed is a 404 in JSON, not the web application.
	//
	// Found by opening the users screen and reading it. Without this, an unmatched API path fell through to the static handler and answered 200
	// with index.html - so the browser asked for JSON, received the entire application, and reported "the server sent a response that could not
	// be read". That is what a missing endpoint looked like: a successful request whose body was a web page.
	//
	// The specific case was /api/passkeys, which is registered only when a relying party identifier is set, so every installation without
	// passkeys configured showed that error on the users screen. But the general fault is worse than the instance. A mistyped path, an old client
	// calling a route since removed, or a probe all received 200 and HTML, which is indistinguishable from success to anything that does not
	// inspect the content type.
	//
	// Registered before the static handler and more specifically, so ServeMux prefers it for /api/ while the application still serves everywhere
	// else.
	mux.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		s.fail(w, r, http.StatusNotFound, fmt.Sprintf("no such endpoint: %s %s", r.Method, r.URL.Path))
	})

	if s.StaticHandler != nil {
		mux.Handle("/", s.StaticHandler)
	}

	return s.withSecurityHeaders(mux)
}

// withSecurityHeaders applies headers that are cheap and always correct.
func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		// The front end is served from the same origin and needs no external
		// resources, so the policy can be strict.
		//
		// script-src is stated separately from default-src only to add 'wasm-unsafe-eval', which the playground needs.
		// Compiling a WebAssembly module counts as evaluating code, so a policy with no eval permission refuses it - and
		// this policy refused it, which meant the playground could not run in any build. The panel rendered and then
		// reported that the engine could not be loaded, blaming a missing artefact, so the message pointed away from the
		// actual cause.
		//
		// 'wasm-unsafe-eval' and not 'unsafe-eval'. The narrow token permits WebAssembly compilation and nothing else;
		// the broad one would re-enable eval() and new Function() for all our JavaScript, which is the main thing a
		// policy like this is for. Anything that cannot express that distinction should keep the strict policy and lose
		// the playground rather than the reverse.
		h.Set("Content-Security-Policy",
			"default-src 'self'; script-src 'self' 'wasm-unsafe-eval'; "+
				"img-src 'self' data:; style-src 'self' 'unsafe-inline'; "+
				"connect-src 'self'; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// ---------- authentication ----------

type ctxKey int

const sessionKey ctxKey = iota

// require wraps a handler with authentication and a minimum role.
func (s *Server) require(minimum store.Role, h func(http.ResponseWriter, *http.Request, *store.Session)) http.Handler {
	return authedHandler{role: minimum, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.authenticate(w, r)
		if !ok {
			return
		}

		if !sess.Role.AtLeast(minimum) {
			s.log().Warn("permission denied",
				"user", sess.Username, "role", string(sess.Role),
				"needed", string(minimum), "path", r.URL.Path)
			s.fail(w, r, http.StatusForbidden,
				fmt.Sprintf("this action needs the %s role", minimum))
			return
		}

		// Any state-changing request must carry a header a cross-site form
		// cannot set. Combined with SameSite=Lax on the cookie this is enough
		// without a token round trip, because the API only accepts JSON.
		//
		// Exempt when the caller authenticated with a bearer token rather than a
		// cookie, because cross-site request forgery is an attack on ambient
		// credentials: a hostile page can make a browser attach its cookie, and
		// cannot make it attach a header the attacker does not know. A caller
		// presenting a bearer token has already proved possession of it, so there
		// is nothing left for the header to protect against.
		//
		// This is not a convenience. Identity providers performing SCIM
		// provisioning send PATCH and DELETE and will never send a Perfuse-specific
		// header, and there is no configuration in Okta or Entra to add one. Without
		// this exemption every deprovisioning would be refused with a 403 - which the
		// provider reports as a failure, so it would at least be visible, but the
		// account would stay enabled.
		if isStateChanging(r.Method) && !authenticatedWithBearer(r) &&
			r.Header.Get("X-Perfuse-Request") == "" {
			s.fail(w, r, http.StatusForbidden, "missing X-Perfuse-Request header")
			return
		}

		h(w, r, sess)
	})}
}

// authenticatedWithBearer reports whether this request carried a bearer credential.
//
// Used only to decide whether the cross-site header is required. Deliberately does not check whether the token was valid -
// that has already happened by the time this runs, and a request that reaches here authenticated by a bearer token is one
// whose sender knew the token.
func authenticatedWithBearer(r *http.Request) bool {
	_, present := bearerToken(r)

	return present
}

func isStateChanging(method string) bool {
	switch method {
	case http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete:
		return true
	}
	return false
}

type loginRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type meResponse struct {
	Username string `json:"username"`
	Role     string `json:"role"`
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	var req loginRequest
	if !s.decode(w, r, &req) {
		return
	}

	// Throttled by source address before the password is checked at all.
	//
	// Not by username: throttling by name lets anybody lock out a named account deliberately, which turns a
	// protection into a way to deny an administrator access at the moment they most need it.
	limiter := s.limiter()
	ip := clientIP(r)

	if wait, blocked := limiter.Blocked(ip); blocked {
		s.log().Warn("a sign-in was refused for repeated failures",
			"username", req.Username, "ip", ip, "wait", wait.Round(time.Second))
		w.Header().Set("Retry-After", itoaSeconds(wait))
		s.fail(w, r, http.StatusTooManyRequests,
			"too many failed sign-ins from this address; try again in "+wait.Round(time.Second).String())
		return
	}

	// A federated account is refused here with a message that says where to sign in instead. Without this it would fail as
	// "incorrect username or password" for a password that does not exist and never did, which sends somebody to reset a
	// password rather than to the button they should be using.
	if s.OIDC.Enabled() {
		if source, srcErr := s.Store.AuthSourceOf(r.Context(), store.DefaultTenant, req.Username); srcErr == nil &&
			source == store.AuthOIDC {
			s.fail(w, r, http.StatusUnauthorized,
				"this account signs in through "+s.OIDC.ButtonLabel()+" rather than with a password here")
			return
		}
	}

	// A directory account is tried before the local password, and only when the account is not a local one.
	//
	// The order matters. Trying the local password first would let a local account of the same name shadow a
	// directory account - and trying the directory for a known local account would send that person's password to
	// the directory on every sign-in, which is a password disclosure to a system that has no business seeing it.
	if s.ldapEnabled() {
		source, srcErr := s.Store.AuthSourceOf(r.Context(), store.DefaultTenant, req.Username)
		// Tried for a known directory account, and for a name nobody has yet - which is what a first sign-in
		// looks like. Never for a local account: sending a local user's password to the directory on every
		// sign-in would disclose it to a system with no business seeing it.
		if srcErr == nil && (source == store.AuthLDAP || source == store.AuthUnknown) {
			token, user, ldapErr := s.signInWithDirectory(r, store.DefaultTenant, req.Username, req.Password)
			if ldapErr != nil {
				limiter.Failed(ip)
				s.log().Warn("failed directory login",
					"username", req.Username, "ip", clientIP(r), "err", ldapErr)
				_ = s.Store.Audit(r.Context(), store.AuditEntry{
					Username: req.Username, Action: "login.failed", IP: clientIP(r),
				})
				s.fail(w, r, http.StatusUnauthorized, "incorrect username or password")
				return
			}
			if token != "" {
				s.finishLogin(w, r, token, user)
				return
			}
		}
	}

	token, user, err := s.Store.Authenticate(r.Context(),
		req.Username, req.Password, clientIP(r), r.UserAgent())
	if err != nil {
		// Counted as well as logged. Logging alone was the state of this handler until a measurement showed twenty
		// guesses a second going through it, all of them recorded and none of them slowed down.
		delay := limiter.Failed(ip)

		s.log().Warn("failed login",
			"username", req.Username, "ip", clientIP(r), "err", err, "delay", delay)
		_ = s.Store.Audit(r.Context(), store.AuditEntry{
			Username: req.Username, Action: "login.failed", IP: clientIP(r),
		})

		switch {
		case errors.Is(err, store.ErrDisabled):
			s.fail(w, r, http.StatusForbidden, "this account is disabled")
		default:
			// Deliberately the same message for an unknown user and a wrong
			// password.
			s.fail(w, r, http.StatusUnauthorized, "incorrect username or password")
		}
		return
	}

	limiter.Succeeded(ip)
	s.finishLogin(w, r, token, user)
}

// limiter returns the sign-in throttle, building it once.
func (s *Server) limiter() *authlimit.Limiter {
	s.loginLimitOnce.Do(func() {
		if s.loginLimit == nil {
			s.loginLimit = authlimit.New()
		}
	})
	return s.loginLimit
}

// itoaSeconds renders a duration as whole seconds for Retry-After.
func itoaSeconds(d time.Duration) string {
	secs := int(d.Round(time.Second) / time.Second)
	if secs < 1 {
		secs = 1
	}
	return strconv.Itoa(secs)
}

// finishLogin sets the session cookie and records the sign-in.
//
// Shared by the local and directory paths so a session minted either way is identical. Two copies would be two places to
// change the cookie flags, and getting Secure or HttpOnly wrong in one of them would be a session token readable from
// JavaScript on whichever path was forgotten.
func (s *Server) finishLogin(w http.ResponseWriter, r *http.Request, token string, user *store.User) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    token,
		Path:     "/",
		HttpOnly: true, // not readable from JavaScript, so an XSS bug cannot steal it
		Secure:   s.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(s.Store.SessionLifetime()),
	})

	s.log().Info("login", "user", user.Username, "role", string(user.Role), "ip", clientIP(r))
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: user.Username, Action: "login", IP: clientIP(r),
	})

	s.ok(w, meResponse{Username: user.Username, Role: string(user.Role)})
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	if cookie, err := r.Cookie(sessionCookie); err == nil {
		_ = s.Store.DeleteSession(r.Context(), cookie.Value)
	}
	s.clearSessionCookie(w)
	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "logout", IP: clientIP(r),
	})
	s.ok(w, map[string]string{"status": "signed out"})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookie,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		Secure:   s.SecureCookies,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

func (s *Server) handleMe(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	s.ok(w, meResponse{Username: sess.Username, Role: string(sess.Role)})
}

type changePasswordRequest struct {
	Current string `json:"current"`
	New     string `json:"new"`
}

func (s *Server) handleChangeOwnPassword(w http.ResponseWriter, r *http.Request, sess *store.Session) {
	var req changePasswordRequest
	if !s.decode(w, r, &req) {
		return
	}

	// Require the current password even though the session proves identity: it
	// stops someone with a borrowed screen from locking the owner out.
	if _, _, err := s.Store.Authenticate(r.Context(), sess.Username, req.Current, clientIP(r), r.UserAgent()); err != nil {
		s.fail(w, r, http.StatusUnauthorized, "current password is incorrect")
		return
	}

	if err := s.Store.SetPassword(r.Context(), sess.UserID, req.New); err != nil {
		s.failErr(w, r, err)
		return
	}

	_ = s.Store.Audit(r.Context(), store.AuditEntry{
		Username: sess.Username, Action: "password.change", IP: clientIP(r),
	})
	s.ok(w, map[string]string{"status": "password changed"})
}

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	s.ok(w, map[string]string{"status": "ok"})
}

// ---------- helpers ----------

func (s *Server) log() *slog.Logger {
	if s.Log != nil {
		return s.Log
	}
	return slog.Default()
}

// maxBodyBytes bounds a request body. Channel definitions are small, and an
// unbounded JSON body is a free way to exhaust memory.
const maxBodyBytes = 1 << 20

func (s *Server) decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	if ct := r.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(ct, "application/json") {
		s.fail(w, r, http.StatusUnsupportedMediaType, "expected application/json")
		return false
	}

	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	// An unknown field is an error for the same reason it is in the YAML loader:
	// a silently ignored key produces something that looks configured and is not.
	dec.DisallowUnknownFields()

	if err := dec.Decode(dst); err != nil {
		s.fail(w, r, http.StatusBadRequest, "could not read the request: "+err.Error())
		return false
	}
	return true
}

func (s *Server) ok(w http.ResponseWriter, body any) {
	s.writeJSON(w, http.StatusOK, body)
}

// writeJSON writes a body with an explicit status, for the handlers that
// legitimately answer with something other than 200 and are not errors.
func (s *Server) writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(body); err != nil {
		s.log().Error("writing response failed", "err", err)
	}
}

type errorResponse struct {
	Error string `json:"error"`
	// Problems carries per-field validation messages, so the front end can show
	// every problem at once rather than one per attempt.
	Problems []string `json:"problems,omitempty"`
}

func (s *Server) fail(w http.ResponseWriter, r *http.Request, code int, message string, problems ...string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	if err := json.NewEncoder(w).Encode(errorResponse{Error: message, Problems: problems}); err != nil {
		s.log().Error("writing error response failed", "err", err)
	}

	s.recordFriction(r, code, message, len(problems))
}

// recordFriction notes that the product refused to do something.
//
// Hooked here because this is the one function every refusal in the API passes through, so instrumenting it covers the whole product
// rather than the handlers somebody remembered. A refusal recorded in only the places that came to mind would be a measurement of this
// author's attention.
//
// Why record refusals at all: every judgement here about whether Perfuse is easy to use comes from reasoning about software written
// here. A refusal is the one signal that does not - the operator wanted something and the server said no, and neither was guessing.
func (s *Server) recordFriction(r *http.Request, code int, message string, problems int) {
	if s.Store == nil || r == nil {
		return
	}

	// Client errors only. A 5xx is a fault in this software and is already logged as one; mixing the two would bury the cases where
	// somebody could not work out what to type under cases where nothing they typed would have helped.
	if code < 400 || code >= 500 {
		return
	}

	// Authentication churn is excluded deliberately. A browser with an expired session produces a run of 401s that say nothing about
	// usability, and at volume they would drown every finding that does.
	if code == http.StatusUnauthorized {
		return
	}

	// Read only, and deliberately not authenticate(): that function writes a response when it fails, which called from here would
	// answer the same request twice and recurse through fail(). A missing or dead session just means no username.
	username := ""

	if cookie, err := r.Cookie(sessionCookie); err == nil {
		if sess, err := s.Store.Lookup(r.Context(), cookie.Value); err == nil && sess != nil {
			username = sess.Username
		}
	}

	// Recorded on the request's own context, and failures ignored. Somebody is already being told no about something else; a second
	// error because the bookkeeping failed would be this feature making the product worse.
	if err := s.Store.RecordFriction(r.Context(), store.Friction{
		Route:    store.NormaliseRoute(r.Method, r.URL.Path),
		Status:   code,
		Message:  message,
		Problems: problems,
		Username: username,
	}); err != nil {
		s.log().Debug("could not record friction", "err", err)
	}
}

// failErr maps a store or repository error onto a status code without leaking
// internal detail for anything unexpected.
func (s *Server) failErr(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, ErrChannelNotFound),
		errors.Is(err, msgstore.ErrNotFound):
		// msgstore.ErrNotFound was missing here, so a request for a message that does not exist answered 500 -
		// "something went wrong" for an ordinary absent record. It matters more now that absence is also how
		// another tenant's message reports, because a 500 there would say a record exists and something broke
		// while reading it.
		s.fail(w, r, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrDuplicate), errors.Is(err, ErrChannelExists):
		s.fail(w, r, http.StatusConflict, err.Error())
	case errors.Is(err, store.ErrPasswordTooShort):
		s.fail(w, r, http.StatusBadRequest, err.Error())
	case errors.Is(err, store.ErrInvalid):
		// The caller asked for something impossible and can fix it, so say what it was.
		//
		// Anything reaching the default branch below becomes 500 "something went wrong", which for a blank
		// username sends an administrator to the logs and eventually to a support call. A validation failure
		// is not a server failure and must never be reported as one.
		s.fail(w, r, http.StatusBadRequest, err.Error())
	case errors.Is(err, ErrInvalidName):
		s.fail(w, r, http.StatusBadRequest, err.Error())
	default:
		var ve *ValidationFailure
		if errors.As(err, &ve) {
			s.fail(w, r, http.StatusBadRequest, "the channel definition is not valid", ve.Problems...)
			return
		}
		s.log().Error("unhandled error", "err", err)
		s.fail(w, r, http.StatusInternalServerError, "something went wrong")
	}
}

// clientIP prefers the immediate peer. A forwarded header is only trusted when
// the deployment puts a proxy in front, which is not something this server can
// verify, so it is recorded separately rather than believed.
func clientIP(r *http.Request) string {
	host := r.RemoteAddr
	if i := strings.LastIndex(host, ":"); i > 0 {
		host = host[:i]
	}
	if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
		return fmt.Sprintf("%s (via %s)", host, strings.TrimSpace(strings.Split(fwd, ",")[0]))
	}
	return host
}

// startedAt reports when this process began, defaulting to the moment it is first asked.
//
// Defaulted rather than left zero, because a settings page showing "started 1 January year one" reads as a bug in the page
// rather than a field nobody set.
func (s *Server) startedAt() time.Time {
	if s.Started.IsZero() {
		return time.Now()
	}
	return s.Started
}
