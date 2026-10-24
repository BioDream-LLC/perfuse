# Server Options

Every option `perfuse serve` takes, generated from the program itself, so this list cannot name an option that does not exist or leave one out. Most are also a setting in the web interface, named in the last column: a value saved there wins over the flag at the next start, and the flag remains the way to set it from a service definition. The listen address, the database and the channel directory stay flags only, because they say where the server finds what it would be editing.

Secrets are never flags. An option that needs a password or a token names an environment variable that holds it.

## All options

| Option | Takes | Default | What it does | In the console |
|---|---|---|---|---|
| `-addr` | string | `127.0.0.1:8080, or 127.0.0.1:8443 with -tls-cert` | address to serve the interface on | — |
| `-alert-severity` | string | `warning` | minimum severity to send to the webhook: warning or critical | **Settings › Alerts › Delivery**: Only send alerts at least this serious |
| `-alert-webhook` | string | — | POST alerts to this URL as JSON. Works with Slack, Teams, Mattermost, Alertmanager or anything that accepts a webhook. The payload carries channel names, counts and thresholds; never message content | **Settings › Alerts › Delivery**: Send alerts to this address |
| `-alerts` | string | — | YAML file of alert rules. Default: alerts.yaml beside the database, written when the rules are first saved in the console; until then a conservative default set is used | — |
| `-allow-metadata-egress` | on/off | — | permit destinations pointed at cloud instance metadata addresses, which hold this machine's credentials | **Settings › Security › What may leave**: Allow destinations at cloud instance metadata addresses |
| `-cds-clients` | string | — | the EHRs trusted to call the CDS services with signed JWTs (issuer and jwks_url) | **Settings › Payers › Prior authorization**: EHRs trusted to call the CDS services |
| `-channels` | string | `./channels` | directory holding channel files | — |
| `-cluster` | string | — | share the work with other servers on the same PostgreSQL -db: active-passive (one runs everything, another takes over when it stops) or active-active (channels spread over all) | — |
| `-cluster-lease` | duration | `15s` | how long a crashed server holds its work before another takes it over; renewed five times as often | — |
| `-crd-advisor-ca` | string | — | a PEM file of certificate authorities to trust for -crd-advisor-url beside the system's (Vaglio's is its DATA/tls/ca.crt) | **Settings › Payers › Decision service**: Certificate authorities to trust for the advice service |
| `-crd-advisor-token-env` | string | — | name of an environment variable holding a bearer token for -crd-advisor-url (the token itself is never a flag) | **Settings › Payers › Decision service**: Environment variable holding the advice service's token |
| `-crd-advisor-url` | string | — | with -crd-rules, ask the decision service at this URL (such as Vaglio's /api/pas/advise) what a prior authorization request for each order needing one would be told as the record stands, shown as one more CRD card; the prefetched record is POSTed to it. No answer, or any failure, leaves the cards as they are without it | **Settings › Payers › Decision service**: Advice before submitting (CRD) |
| `-crd-rules` | string | — | a Da Vinci CRD rules file: serve coverage requirements over CDS Hooks at /cds-services | **Settings › Payers › Prior authorization**: Coverage rules file (Da Vinci CRD) |
| `-dashboards` | string | — | a file mapping directory groups and roles to the dashboard each opens on | **Settings › Monitoring › Dashboards**: Which dashboard each group opens on (file) |
| `-data` | string | — | directory for this server's own files (settings, alerts, sign-in configuration, the log): default beside -db when it is a file, the current directory when it is a PostgreSQL URL | — |
| `-davinci-version` | string | `2.2` | the Da Vinci CRD, DTR and PAS release answered when neither a request nor its client's registration says: 2.2 (CRD 2.2.1, DTR 2.2.0, PAS 2.2.1) or 2.1 (2.1.0) | — |
| `-db` | string | `./perfuse.db` | database for users, messages and stored resources: a file, or a postgres:// URL for servers that share one store (a cluster) | — |
| `-demo-account` | string | — | for a public demonstration server: username:password of a shared viewer account shown on the sign-in page | — |
| `-dicomweb-archive` | string | — | keep imaging objects in this directory and serve them over DICOMweb (QIDO-RS, WADO-RS, WADO-URI, STOW-RS) at /dicomweb | **Settings › Data › Imaging archive**: Keep images in this folder and serve them over DICOMweb |
| `-dicomweb-cors` | string | — | comma-separated browser origins, such as an OHIF viewer's, allowed to call /dicomweb | **Settings › Data › Imaging archive**: Browser origins allowed to call it |
| `-drain-for` | duration | — | after SIGTERM, report not-ready for this long before shutting down (for rolling deploys) | **Settings › Engine › Shutdown**: Wait for in-flight messages on shutdown |
| `-ecr-agency` | string | — | play a public health agency for testing eCR: answer case reports at $process-message with a Reportability Response, using the agency YAML file given (name, phone, address, endpoint; optional rctc, reply) | **Settings › Public health › Case reporting**: Act as a test public health agency |
| `-ecr-responses` | on/off | — | accept eCR Reportability Responses at the FHIR endpoint's $process-message, storing each as DocumentReference/<id> derived from the eICR it answers | **Settings › Public health › Case reporting**: Receive Reportability Responses |
| `-engine` | on/off | `true` | run the channel engine in this process, so the interface can start, stop and monitor channels | — |
| `-epa-payer-id` | string | — | this payer's identifier on the prescribing network, the From of every RTPB and ePA response (default: the formulary's payer id) | **Settings › Payers › Drug formulary**: Payer ID on the prescribing network |
| `-epa-reply-channel` | string | — | route ePA decisions made after the request was answered (a reviewer's, an appeal's) to this channel, which delivers them to the prescriber's network | **Settings › Payers › Drug formulary**: Channel that sends later ePA decisions |
| `-fhir` | on/off | `true` | serve a FHIR endpoint at /fhir | **Settings › FHIR › The FHIR server**: Serve a FHIR endpoint at /fhir |
| `-fhir-api-tokens` | on/off | — | with SMART configured, accept Perfuse API tokens on the FHIR endpoint as well (unscoped; the console's FHIR settings can switch it) | **Settings › FHIR › Access**: Accept Perfuse API tokens beside SMART tokens |
| `-fhir-bulk-export` | on/off | — | enable $export, which produces a file holding every record the FHIR endpoint serves | **Settings › FHIR › Bulk data**: Bulk Data export ($export) |
| `-fhir-max-resources` | int | — | for a public server: refuse new FHIR resources once this many are held (0: no limit) | **Settings › FHIR › Access**: Most resources held |
| `-fhir-member-match-without-consent` | on/off | — | let $member-match answer without an active Consent; the Payer-to-Payer API is opt-in, so only for testing | **Settings › Payers › CMS-0057-F**: Answer $member-match without the member's consent |
| `-fhir-open` | on/off | — | serve the FHIR endpoint with no authentication at all, exposing every stored record | **Settings › FHIR › Access**: Let anyone read the FHIR endpoint without signing in |
| `-fhir-payer-apis` | on/off | — | enable the CMS-0057 payer operations: $member-match (Payer-to-Payer) and Group $davinci-data-export (Provider Access); the Group exports also need -fhir-bulk-export | **Settings › Payers › CMS-0057-F**: The CMS-0057-F payer APIs |
| `-fhir-read-only` | on/off | — | refuse writes to the FHIR endpoint | **Settings › FHIR › Access**: Refuse writes to the FHIR endpoint |
| `-fhir-subscriptions` | on/off | — | enable topic-based subscriptions, which send encounter and appointment notifications to URLs FHIR clients choose | **Settings › FHIR › Notifications**: FHIR subscriptions (encounter and appointment notifications) |
| `-fhir-subscriptions-allow-http` | on/off | — | let subscriptions deliver to plain-http endpoints; for a test receiver, never for patient data on a network | **Settings › FHIR › Notifications**: Allow subscriptions to notify plain-HTTP addresses |
| `-fhir-version` | string | `4.0.1` | FHIR release to serve: R4, R4B or R5 | **Settings › FHIR › The FHIR server**: FHIR version clients see |
| `-fhir-writes-per-minute` | int | — | for a public server: writes one client address may make to the FHIR endpoint a minute (0: no limit) | **Settings › FHIR › Access**: Writes a minute from one client |
| `-fleet-label` | string | — | how this instance names itself in a fleet view (default: hostname) | **Settings › Fleet › This server**: Name for this server |
| `-formulary` | string | — | publish this drug formulary over the Da Vinci US Drug Formulary API at /formulary/fhir and answer NCPDP Real-Time Prescription Benefit and SCRIPT electronic prior authorization from it | **Settings › Payers › Drug formulary**: Drug formulary file |
| `-icd11-client-id` | string | — | a WHO ICD API client: ICD-11 codes not imported are looked up there | **Settings › Data › Code systems**: WHO ICD API client ID |
| `-icd11-client-secret` | string | — | the WHO ICD API client's secret | **Settings › Data › Code systems**: WHO ICD API client secret |
| `-icd11-release` | string | `2025-01` | the ICD-11 MMS release the WHO ICD API is asked about | **Settings › Data › Code systems**: ICD-11 release to ask for |
| `-index-identity` | on/off | `true` | index patient identifiers from message contents so a message can be found by MRN, name or accession in any format; has no effect when -store-payloads is off | **Settings › Data › What is kept**: Index patient identifiers |
| `-insecure` | on/off | — | serve over plain HTTP on a non-loopback address (not recommended) | — |
| `-inventory` | string | — | write a Markdown inventory of every interface to this file, and keep it current | **Settings › Monitoring › Inventory**: Write an interface inventory to |
| `-inventory-every` | duration | `24h0m0s` | how often to rewrite the interface inventory | **Settings › Monitoring › Inventory**: Rewrite the inventory every |
| `-json-logs` | on/off | — | emit structured JSON logs | **Settings › Monitoring › Logging**: Write logs as JSON |
| `-ldap` | string | — | YAML file configuring directory (LDAP) sign-in (default: ldap.yaml beside the database, once saved in the console) | — |
| `-log-file` | string | — | append the log, and the first administrator's one-time password, to this file (mode 0600) instead of the console. A Windows service has no console, so there it defaults to perfuse.log beside the database | — |
| `-mcp` | on/off | — | serve the Model Context Protocol at /mcp, so AI agents with an API token can read status, messages, the queue, alerts and the FHIR store with that token's own access | **Settings › Security › AI agents (MCP)**: Let AI agents connect over MCP |
| `-measure-reporter` | string | `Perfuse` | the reporting organisation's name on MeasureReports from $evaluate-measure when the request names no reporter | — |
| `-metrics-open` | on/off | — | serve /metrics to anybody who can reach the port. Only safe where that port is genuinely private: metric labels name your channels, and on a multi-tenant server they name every tenant's | **Settings › Security › Metrics**: Serve /metrics without a token |
| `-metrics-token` | string | — | credential the Prometheus scrape endpoint requires. Give the scraper the same value in bearer_token_file. Without this, only a signed-in operator can read /metrics | **Settings › Security › Metrics**: Token a metrics scraper presents |
| `-mpi` | on/off | — | keep a patient index (master patient index) in mpi.db beside the database: patient records from every system linked into one enterprise identifier per person, fed by mpi destinations and the PIX v3 identity feed, answering Patient/$match, PIXm, PIX v3 (/pixv3) and PDQ v3 (/pdqv3) | **Settings › Data › Patient index**: Keep a patient index |
| `-multi-tenant` | on/off | — | keep each tenant's channels in their own subdirectory of -channels, so one server can run integration for several organisations without them being able to see each other | — |
| `-node-address` | string | — | this server's console URL as the other servers and a load balancer reach it | — |
| `-node-name` | string | — | this server's name in the cluster (default: an id kept in -data) | — |
| `-oidc` | string | — | YAML file configuring OpenID Connect sign-in (default: oidc.yaml beside the database, once saved in the console) | — |
| `-pas` | on/off | — | serve Da Vinci PAS 2.2.1 and 2.1.0 (Claim/$submit, $inquire, $decide) on the FHIR endpoint; decisions come from -crd-rules, and what no rule decides is pended for a reviewer. With -fhir-subscriptions, the PAS topic delivers each pended request's result | **Settings › Payers › Prior authorization**: Da Vinci PAS prior authorization server |
| `-pas-278-ca` | string | — | a PEM file of certificate authorities to trust for the UM system beside the system's | **Settings › Payers › Utilization management link**: Certificate authorities to trust for the UM system |
| `-pas-278-cert` | string | — | a PEM client certificate for X.509 mutual TLS with the UM system (CORE's authentication) | **Settings › Payers › Utilization management link**: Client certificate file |
| `-pas-278-core-receiver` | string | — | the CORE envelope's ReceiverID, as the UM system's connectivity guide gives it (default: -pas-278-receiver) | **Settings › Payers › Utilization management link**: CORE envelope ReceiverID |
| `-pas-278-core-rule-version` | string | — | what to write in the CORE envelope's CORERuleVersion when the UM system's guide asks for something other than the version itself (CMS esMD's sample says v2.2.0) | **Settings › Payers › Utilization management link**: CORERuleVersion written in the envelope |
| `-pas-278-core-sender` | string | — | the CORE envelope's SenderID, as the UM system's connectivity guide gives it (default: -pas-278-sender) | **Settings › Payers › Utilization management link**: CORE envelope SenderID |
| `-pas-278-core-version` | string | `2.2.0` | the CAQH CORE Connectivity Rule the UM system runs, for core-soap and core-mime: 2.2.0 (payload in the envelope) or C4.0.0 (payload as an MTOM attachment; SOAP only) | **Settings › Payers › Utilization management link**: CAQH CORE Connectivity Rule |
| `-pas-278-key` | string | — | the private key (PEM) for -pas-278-cert | **Settings › Payers › Utilization management link**: Client certificate key file |
| `-pas-278-oauth-client-id` | string | — | the OAuth client ID for -pas-278-oauth-token-url | **Settings › Payers › Utilization management link**: OAuth client ID |
| `-pas-278-oauth-scope` | string | — | the scope asked for with the OAuth token, when the UM system wants one | **Settings › Payers › Utilization management link**: OAuth scope |
| `-pas-278-oauth-secret-env` | string | — | name of an environment variable holding the OAuth client secret (the secret itself is never a flag) | **Settings › Payers › Utilization management link**: Environment variable holding the OAuth client secret |
| `-pas-278-oauth-token-url` | string | — | an OAuth 2.0 token endpoint: a token got with the client-credentials grant is sent as a bearer token (CORE vC4.0.0's authorization) | **Settings › Payers › Utilization management link**: OAuth token endpoint |
| `-pas-278-password-env` | string | — | name of an environment variable holding the password for -pas-278-user (the password itself is never a flag) | **Settings › Payers › Utilization management link**: Environment variable holding the password |
| `-pas-278-payer-id` | string | — | the UMO's payer ID (NM1*X3) when the request's insurer carries no identifier | **Settings › Payers › Utilization management link**: Payer ID for the 278s |
| `-pas-278-production` | on/off | — | mark the 278s sent as production (ISA15 P) rather than test | **Settings › Payers › Utilization management link**: Mark the 278s as production |
| `-pas-278-receiver` | string | — | ISA08/GS03 receiver ID on the 278s sent to the UM system | **Settings › Payers › Utilization management link**: Receiver ID on the 278s |
| `-pas-278-sender` | string | `PERFUSE` | ISA06/GS02 sender ID on the 278s sent to the UM system | **Settings › Payers › Utilization management link**: Sender ID on the 278s |
| `-pas-278-sftp` | string | — | host:port of the UM system's SFTP server, for -pas-278-transport sftp | **Settings › Payers › Utilization management link**: SFTP server |
| `-pas-278-sftp-every` | duration | `1m0s` | how often the SFTP inbound folder is read | **Settings › Payers › Utilization management link**: Read the SFTP inbound folder every |
| `-pas-278-sftp-inbound` | string | — | the SFTP folder the UM system's 278 responses, 999s and TA1s are collected from | **Settings › Payers › Utilization management link**: SFTP inbound folder |
| `-pas-278-sftp-key` | string | — | an OpenSSH private key file for the SFTP user, instead of a password | **Settings › Payers › Utilization management link**: SFTP key file |
| `-pas-278-sftp-known-hosts` | string | — | an OpenSSH known_hosts file holding the SFTP server's key; required, so requests never go to whoever answers | **Settings › Payers › Utilization management link**: SFTP known_hosts file |
| `-pas-278-sftp-outbound` | string | — | the SFTP folder the 278 requests (and the 999s for the answers) are put in | **Settings › Payers › Utilization management link**: SFTP outbound folder |
| `-pas-278-sftp-password-env` | string | — | name of an environment variable holding the SFTP password (the password itself is never a flag) | **Settings › Payers › Utilization management link**: Environment variable holding the SFTP password |
| `-pas-278-sftp-user` | string | — | the SFTP user name | **Settings › Payers › Utilization management link**: SFTP user |
| `-pas-278-token-env` | string | — | name of an environment variable holding a bearer token for the UM system (the token itself is never a flag) | **Settings › Payers › Utilization management link**: Environment variable holding the token |
| `-pas-278-transport` | string | `x12` | how the 278s reach the UM system: x12 (POSTed as application/edi-x12), core-soap (a CAQH CORE SOAP envelope), core-mime (CAQH CORE HTTP MIME multipart), or sftp (batch files over -pas-278-sftp, answers collected from its inbound folder) | **Settings › Payers › Utilization management link**: How the 278s travel |
| `-pas-278-url` | string | — | forward every PAS request to the payer's utilization management system as an X12 278 request sent to this URL (by -pas-278-transport), and answer with its 278 response; later 278 responses go to Claim/$decide-278 | **Settings › Payers › Utilization management link**: Send PAS requests on to the UM system at |
| `-pas-278-user` | string | — | user name for the UM system: CORE's UserName (WS-Security UsernameToken in SOAP), or HTTP Basic for x12 | **Settings › Payers › Utilization management link**: User name |
| `-pas-decider-ca` | string | — | a PEM file of certificate authorities to trust for -pas-decider-url beside the system's, such as the payer's own authority or the decision service's (Vaglio's is its DATA/tls/ca.crt) | **Settings › Payers › Decision service**: Certificate authorities to trust for the decision service |
| `-pas-decider-token-env` | string | — | name of an environment variable holding a bearer token for -pas-decider-url (the token itself is never a flag) | **Settings › Payers › Decision service**: Environment variable holding the decision service's token |
| `-pas-decider-url` | string | — | ask the decision service at this URL (such as Vaglio) to decide each PAS item on the request's clinical evidence, when -crd-rules leave it to a reviewer; the PAS request Bundle is POSTed to it. Cannot be combined with -pas-278-url | **Settings › Payers › Decision service**: Decision service URL |
| `-passkey-allowed-models` | string | — | comma-separated authenticator AAGUIDs that may enrol; requires -passkey-attestation-roots | — |
| `-passkey-attestation-roots` | string | — | a PEM file of authenticator vendor certificates; passkey attestation is verified against these | — |
| `-passkey-domain` | string | — | the bare domain passkeys are registered for, such as perfuse.example.org. No scheme, no port, no path. Setting it turns passkeys on; leaving it empty leaves them off | **Settings › Sign-in › Passkeys**: Domain passkeys are bound to |
| `-passkey-name` | string | `Perfuse` | the name an authenticator shows a person when they register a passkey | **Settings › Sign-in › Passkeys**: Name shown on the device |
| `-passkey-origins` | string | — | comma-separated origins the console is reached at, with scheme and port: https://perfuse.example.org. Defaults to https:// plus the domain | **Settings › Sign-in › Passkeys**: Other origins passkeys are accepted from |
| `-peers` | string | — | YAML file listing other Perfuse instances to show in a fleet view | **Settings › Fleet › Fleet**: Other Perfuse servers to show (file) |
| `-public-url` | string | — | the https address patients' phones and other organisations reach this server at, for the SMART Health Links it hosts (default: the listen address) | **Settings › FHIR › The FHIR server**: Address others reach this server at |
| `-rctc` | string | — | eCR trigger codes for the case report preview: a FHIR ValueSet or Bundle of them, such as the eRSD | **Settings › Public health › Case reporting**: Trigger codes file (RCTC or eRSD) |
| `-residency` | string | — | cloud regions this server's data may go to or come from: areas (eea, uk, ch) and region codes (eu-central-1), comma-separated; channels with connectors elsewhere are refused | **Settings › Data › Data protection**: Where data may go (cloud regions) |
| `-retention-days` | int | `30` | how long to keep recorded messages; 0 keeps them for ever, which will eventually fill the disk | **Settings › Data › How long it is kept**: Delete records after |
| `-saml` | string | — | YAML file configuring SAML 2.0 sign-in (default: saml.yaml beside the database, once saved in the console) | — |
| `-scim` | on/off | — | accept SCIM 2.0 provisioning from an identity provider at /scim/v2. Okta, Entra ID and Google Workspace use it to create accounts and, more importantly, to disable them when somebody leaves | **Settings › Sign-in › Automatic accounts**: Let the identity provider manage accounts |
| `-scim-default-role` | string | `viewer` | role for an account the identity provider does not describe. Defaults downward on purpose: an account whose role could not be determined should be able to read and nothing else | — |
| `-settings` | string | — | settings file the web interface may edit (default: perfuse-settings.yaml beside the database) | — |
| `-shl-allow-http` | on/off | — | let a received SMART Health Link point at plain HTTP; for testing only | **Settings › Public health › Health links**: Allow SMART Health Links over plain HTTP |
| `-signing-cert` | string | — | certificate for signing documents (defaults to -tls-cert) | **Settings › Security › Document signing**: Certificate for signing documents |
| `-signing-key` | string | — | key for signing documents (defaults to -tls-key) | **Settings › Security › Document signing**: Key for signing documents |
| `-smart-audience` | string | — | the audience a SMART token must be issued for, normally the FHIR base URL | **Settings › SMART › Authorization**: Audience tokens must name |
| `-smart-authorize` | string | — | the authorization endpoint to advertise to SMART apps | **Settings › SMART › Authorization**: Authorization endpoint |
| `-smart-backend-services` | on/off | — | advertise that the SMART authorization server issues backend-services (client_credentials, signed JWT) tokens | **Settings › SMART › Built-in server**: Backend Services (system-to-system tokens) |
| `-smart-clients` | string | — | run Perfuse's own SMART authorization server at /auth for the apps registered in this YAML file | **Settings › SMART › Built-in server**: Registered apps file (turns on the built-in server) |
| `-smart-issuer` | string | — | accept SMART on FHIR access tokens from this authorization server | **Settings › SMART › Authorization**: Accept SMART access tokens from this authorization server |
| `-smart-jwks` | string | — | where to fetch the SMART issuer's signing keys, when not from its published metadata (split DNS, a private address) | **Settings › SMART › Authorization**: Key set file |
| `-smart-jwks-uri` | string | — | the authorization server's public key set URL, advertised to SMART apps (needed to claim OpenID Connect sign-in) | **Settings › SMART › Authorization**: Key set address |
| `-smart-key` | string | `smart-signing.key` | the built-in authorization server's RSA signing key (PEM, mode 0600), created if missing | **Settings › SMART › Built-in server**: Signing key file |
| `-smart-oidc` | string | — | with -smart-users, let people sign in at this OpenID Connect provider (YAML: issuer, client_id, client_secret_file or client_secret_env, label); each is linked by oidc_subject | **Settings › SMART › Built-in server**: Sign people in through the OpenID Connect provider |
| `-smart-token` | string | — | the token endpoint to advertise to SMART apps | **Settings › SMART › Authorization**: Token endpoint |
| `-smart-users` | string | — | with -smart-clients, the people who may sign in to authorize apps (YAML: username, password_hash, fhir_user) | **Settings › SMART › Built-in server**: People who sign in to authorize apps (file) |
| `-snmp-allow` | string | — | comma-separated networks or addresses the SNMP agent answers, such as 10.0.5.0/24; required with -snmp-listen | **Settings › Monitoring › SNMP**: Networks that may ask |
| `-snmp-base-oid` | string | `1.3.6.1.4.1.8072.9999.9999.16, Net-SNMP's experimental space` | where Perfuse's values sit, under your enterprise number | **Settings › Monitoring › SNMP**: Base OID |
| `-snmp-community` | string | — | the SNMPv2c community (a password sent in clear: at least 6 characters, not public or private) | **Settings › Monitoring › SNMP**: SNMP community |
| `-snmp-contact` | string | — | sysContact the SNMP agent reports | **Settings › Monitoring › SNMP**: Contact (sysContact) |
| `-snmp-listen` | string | — | answer SNMPv2c GET/GETNEXT/GETBULK on this UDP address, such as :161 (off when empty). Read-only: channel states, counts and queue depths | **Settings › Monitoring › SNMP**: Answer SNMP on this address |
| `-snmp-location` | string | — | sysLocation the SNMP agent reports | **Settings › Monitoring › SNMP**: Location (sysLocation) |
| `-snmp-traps` | string | — | comma-separated managers (host or host:port, default port 162) that receive an SNMPv2 trap when an alert fires or resolves | **Settings › Monitoring › SNMP**: Send traps to |
| `-store-messages` | on/off | `true` | record every message handled, which is what makes the message browser work | **Settings › Data › What is kept**: Keep a record of each message |
| `-store-payloads` | on/off | `true` | store message contents as well as their outcomes; turn this off where PHI must not be held at rest | **Settings › Data › What is kept**: Keep message contents |
| `-tls-cert` | string | — | TLS certificate file | **Settings › Security › Encryption**: TLS certificate file |
| `-tls-key` | string | — | TLS private key file | **Settings › Security › Encryption**: TLS private key file |
| `-trace-endpoint` | string | — | OTLP/HTTP base URL to export message traces to, for example http://localhost:4318 | **Settings › Monitoring › Tracing**: Send traces to |
| `-trace-instance` | string | — | name for this instance in traces; defaults to the hostname, which is what distinguishes replicas | **Settings › Monitoring › Tracing**: This instance's name in traces |
| `-trace-sample` | float | `1` | fraction of traces to record, 0 to 1 | — |
| `-tsa-url` | string | — | RFC 3161 time-stamping authority for DSDR signatures on claims attachments | **Settings › Security › Document signing**: Time-stamping authority |
| `-vpn` | string | — | partner VPN tunnels to report on the Connections dashboard (YAML: AWS Site-to-Site VPN, Azure VPN Gateway or strongSwan, and both sides' settings); see examples/vpn.yaml | **Settings › Monitoring › Connections**: VPN tunnels to watch (file) |
| `-xds` | string | — | keep clinical documents here as an IHE XDS.b registry and repository with this repository uniqueId (an OID), answering ITI-41, ITI-42, ITI-18 and ITI-43 at /xds/repository and /xds/registry | **Settings › Data › Document sharing (IHE XDS)**: Keep documents here as an XDS registry and repository |
| `-xds-home-community` | string | — | this community's home community id (urn:oid:...), so /xca/gateway answers XCA ITI-38 and ITI-39 for it | **Settings › Data › Document sharing (IHE XDS)**: Home community id, for XCA |
