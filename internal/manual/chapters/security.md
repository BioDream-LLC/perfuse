# Security

Perfuse handles clinical data. This chapter is what it does about that and what it leaves to you.

## Transport security

TLS on the web interface is configured with `-tls-cert` and `-tls-key`. Without them the interface is plain HTTP, which is acceptable on a loopback address for a first look and nowhere else — it carries credentials and message content, both readable on the wire.

TLS on channel transports is per source and per destination, under a `tls` block. The available settings are in the [source](#source-reference) and [destination](#destination-reference) references.

`insecure_skip_verify` accepts any certificate. It exists because hospital systems routinely present certificates that will not verify, and refusing to connect would mean refusing to integrate. It is named to be conspicuous, and using it means the connection is encrypted but not authenticated — you have protection against passive interception and none against an interposed server.

> An unverified TLS connection is not the same as a verified one, and the distinction is worth recording somewhere your auditors will find it. The better fix is almost always to obtain the far end's certificate and trust it explicitly rather than trusting everything.

## Authentication and roles

Users are held in the database. Each has one of five roles, and each role includes everything in the ones above it:

| Role | Can |
|---|---|
| Monitor | See whether things are working: channel status, counts, the flow map, alerts, dashboards, metrics and the queue's depth. No message contents, channel files or queued messages. |
| Viewer | Also read messages, channel files, traces and the activity log. |
| Operator | Also resend: reprocess a message, replay a channel and retry queued deliveries. |
| Editor | Also create and change channels, and run comparisons. |
| Admin | Also start and stop channels, clear queues, and manage people, tokens and the server's settings. |

A sixth role, Platform, belongs to whoever runs a shared server: it creates tenants and sets their limits, and holds no
channels of its own.

Monitor is for a help desk, or for an operations screen on a wall: everything needed to see that a feed has stopped, and
nothing that names a patient. Viewer can read message content, which is clinical data; it is a low-privilege role for
*configuration*, not for privacy. Operator separates fixing a failed delivery at night from changing what a feed does: the
person who resends need not be able to rewrite the channel.

### Channel groups

A person (or an API token) can be limited to some **channel groups**: they then see and act on only the channels whose
`group` is one of theirs. This is the arrangement of a hospital group on one server, each facility's analysts looking after
their own feeds and a central team, with no limit, looking after all of them. Rhapsody calls it a locker.

The limit applies everywhere at once, because it is applied to the request rather than remembered by each screen: the
channel list, the dashboard and flow map, messages and their search, counts and charts, the queue, alerts, metrics,
certificates, contracts and code-set use. A channel outside the person's groups does not exist for them; asking for it by
name answers *not found*, so its name is not confirmed either. A channel with no group is seen only by people with no limit.

Saving a channel keeps it inside the person's groups: they cannot create a channel in another group, or move one out of
their own. What every facility shares - code tables, script libraries, prior-authorisation decisions - they can read but not
change. Server-wide figures (prior authorisation, token use, tunnels) are shown only to people with no limit, and so are
metrics and alerts about the whole server rather than a channel.

An administrator limited to groups is a facility's own administrator. They manage the people and tokens of their groups,
start and stop their channels and clear their queues. A colleague they add gets their groups unless they name some of them,
they cannot give a group they do not have, and they never see or change anybody with no limit. Everything else an
administrator can do changes the whole server - settings, sign-on, restarting, alert rules, branding, fleet, the SMART
server - and is refused to them.

Groups are set in **Users** (and for tokens in **Tokens**), as a comma-separated list. Each change is recorded in the
activity log and takes effect at the person's next request.

#### A different role in each group

A person can hold a different role in each of their groups: `lab=editor, adt` in **Users** edits the lab's channels and has the
role chosen beside it - say viewer - in the admissions ones. They see both groups' channels on every screen. Saving, starting or
resending one in a group where their role is too low is refused with the role they hold there; it does not pretend the channel is
missing, since they can see it. Their role at the top of the screen is the strongest they hold, which opens the screens that role
uses, and every action on those screens is still narrowed to the groups where they hold it.

Only people limited to channel groups can have one. Somebody who sees every channel has one role, because "editor of the lab's
channels" for a person with no limit would be editor of everything that belongs to no group, and of the server-wide pages. A
facility's administrator can give a role per group only in the groups they administer, and never above their own role there.

## Who may call the FHIR endpoint

The FHIR endpoint (`serve -fhir`) checks one of two kinds of token:

- **Perfuse API tokens**, made under **Users → Machine credentials** or with `perfuse token create`. Each carries a role, and can be
  limited to FHIR Groups. This is the default when no SMART authorization server is configured.
- **SMART access tokens**, from Perfuse's own authorization server (`-smart-clients`) or an outside one (`-smart-issuer`). Each
  carries scopes, and the endpoint holds every request to them.

With SMART configured, only SMART tokens are accepted, so every caller is held to its scopes. An API token has a role and no
scopes: a viewer token can read every patient's record. A payer who turned SMART on to limit each app to what it was granted
should not have a way in that is not limited.

Some sites need both all the same - an internal claims feed that holds an API token and has no SMART client, beside member apps
that sign in through SMART. For those, **Settings → FHIR → Accept Perfuse API tokens beside SMART tokens** (`-fhir-api-tokens` at
start-up) lets API tokens in as well. It is off by default, it takes effect on the next request in either direction, and changing it
is written to the audit log. A token is told apart by its shape: a SMART token is a signed JWT, and an API token never is, so one is
never checked as the other. Where you can, give such a system a SMART Backend Services client instead, with only the system scopes
it needs.

### A FHIR endpoint open to strangers

A public test server gives out credentials that can write, because testing prior authorization or subscriptions needs writes.
Two limits keep one client from spoiling it for everybody, both under **Settings → FHIR** and both off by default:

- **Writes a minute from one client** (`-fhir-writes-per-minute`): creates, updates and deletes from one client address. Over it, a
  write is answered 429 with `Retry-After: 60`. Reads and searches, including searches by POST, are never counted.
- **Most resources held** (`-fhir-max-resources`): past it, new resources are refused with 507, while updates and deletes still work.

A hospital's own endpoint needs neither.

## AI agents: the MCP server

With **Settings › Security › AI agents (MCP)** on (or `serve -mcp`), Perfuse serves the Model Context Protocol at `/mcp`, so an
AI agent such as Claude, ChatGPT or an IDE assistant can ask it what is happening. It is off by default, and switching it off
stops every agent on its next call.

An agent connects with an API token from **Administer › Users › Machine credentials** as a bearer token, never with a person's
browser session (that is refused, so an agent is never acting with a person's full access). A client configuration looks like:

```json
{ "mcpServers": { "perfuse": { "type": "http", "url": "https://perfuse.example/mcp",
    "headers": { "Authorization": "Bearer <token>" } } } }
```

| Tool | Role | What it does |
|---|---|---|
| `server_status`, `list_channels`, `queue`, `alerts` | monitor | Whether things work, with no message contents |
| `get_channel`, `search_messages`, `get_message`, `find_patient`, `fhir_search` | viewer | Channels, messages (with contents), a patient's messages across formats, a FHIR search |
| `retry_delivery`, `reprocess_message` | operator, and `mcp:write` | Retry a queued delivery, send a message through its channel again |
| `start_channel`, `stop_channel` | admin, and `mcp:write` | Start or stop a channel |

Every tool is answered by the same route or FHIR search a person's screen uses, called with the agent's own token, so the
token's role, channel groups and tenant decide what it sees, exactly as they do in the console; and `fhir_search` gets the
token's own access on the FHIR endpoint (which may refuse API tokens altogether; see the FHIR section above). An agent is
offered only the tools its token may use. Reading is the default: a tool that changes something also needs the token's
**mcp:write** scope ("Let an AI agent act with it" when issuing the token), so a token issued for a reporting agent cannot be
talked into acting by a prompt. A monitor token's answers have message contents removed, whatever a route returns.

Every call is in **Activity** as `mcp.tool` with the token's name, the tool, its arguments and the outcome (`ok`, `refused`,
`invalid`, `rate-limited` or the HTTP status); `mcp.connect` records which client connected. Each token may make 60 tool calls a
minute (**Tool calls a minute from one token**); over that, a call is answered with an error the agent can read, and nothing is
done. A request from a browser page on another origin is refused, which MCP asks of a server to stop DNS rebinding. The transport
is MCP's Streamable HTTP, answered as JSON with no event stream; protocol versions 2025-06-18, 2025-03-26 and 2024-11-05 are
accepted. Messages and FHIR resources hold patient data: an agent's answers go to whatever model it runs on, so give a token to an
agent only where that model is allowed to see them, and give it the lowest role that does the job.

## A demonstration account

`serve -demo-account demo:PASSWORD` creates a shared account for a public demonstration server, and the sign-in page shows its
username and password with a button that signs in with them. The account is a viewer, so it can look at everything and change
nothing. Unlike any other viewer it may also open the administrators' screens (users, machine tokens, settings, data protection,
the fleet's peers), read-only: a demonstration is judged by its screens. It reads them as a viewer reads channels, with every
credential shown as set or not, never its value, and every change refused. It cannot change its password or add a passkey,
because the first visitor to do either would lock out everyone after them.
Each start puts it back as a viewer with the published password. The password must be at least 12 characters, like any other.
Only use it on a server holding test data: anyone who reads the sign-in page can read every message on the server.

## Audit

Configuration changes, logins, and access to message content are recorded with who, what and when.

The audit log is in the same database as the messages. That is convenient and it is a limitation: somebody who can modify the database can modify the audit log. If your requirements include tamper-evident audit, the log needs shipping somewhere append-only, and Perfuse's own log output is the way to do that.

## Secrets in configuration

Passwords, passcodes and keys appear in channel configuration, which means they are in the YAML files.

Two consequences. The files need filesystem permissions that reflect what they contain, and if they are in version control — which is otherwise recommended — the repository holds credentials and must be treated accordingly.

Where a value is a secret, the interface renders it as a secret and the API does not return it once saved. That protects it from being read over somebody's shoulder or leaked in a screenshot. It does not protect the file.

## What is in the database

The message store holds messages as received, which for a clinical feed means patient-identifiable data. It should be on encrypted storage, backed up as clinical data with the retention that implies, and access-controlled at the filesystem level as well as through Perfuse's own roles.

`cleanup.periodDays` limits how long it is kept. See [the message store](#the-message-store) for the tension between that and having enough history for rhythm-aware alerting.

## Data protection (GDPR and UK GDPR)

The **Data protection** screen (Administer, administrators only) answers a
person's requests under the GDPR and the UK GDPR. It decides nothing on the
controller's behalf: whether a request must be granted, and whether an exemption
applies, is the controller's decision, recorded with the request.

**Finding what is held.** A person is named by identifiers: `system|value` (an NHS
number, an MRN with its assigning authority) or a number on its own. With a patient
index, those widen to every number of every record linked to the same person, so a
request quoting one hospital number finds the records filed under the others.
**See what is held** then searches every store that keeps patient data:

| Store | Found by |
|---|---|
| Messages | the identity index (Settings → Data → Index patient identifiers); without it, messages cannot be found by identifier |
| The delivery queue | reading each queued message's identifiers |
| The FHIR store | the Patient resources carrying an identifier, and every resource whose indexed references point at one |
| Prior authorization requests | the identifiers of the request's patient |
| The patient index | the identifier, or the EUID |

What it cannot reach it says: traces sent to a collector (`-trace-endpoint`), the
audit log (kept: it is the record of who did what, this request included), and
backups.

**Requests.** A request is recorded with its kind (access, Article 15;
portability, Article 20; erasure, Article 17; restriction, Article 18), who it is
from as the controller's records refer to them, the identifiers, and a note (how
the identity was confirmed, an extension and its reason). It is due one month after
receipt (Article 12(3)); an overdue one is shown in red.

- **Access and portability:** **Download the copy** gives a zip: every message as
  received with its record, every FHIR resource with every stored version, prior
  authorization requests and their answers, and the patient index's records, JSON
  throughout, with a README saying what each part is. **Mark answered** closes it.
- **Erasure:** a second administrator approves it (whoever recorded it cannot).
  **Erase now** deletes everything found: messages whole (record, body, deliveries,
  identifiers, attachments no other message uses), queued deliveries (they are not
  sent), the FHIR resources with their history, prior authorization requests with
  their answers, attachments and tracking numbers, and the person in the patient
  index with every linked record and its history (one event records that an erasure
  happened, and by whom).
- **Restriction:** recorded as a legal hold, so nothing is erased or expires while
  it lasts.
- **Refuse** closes a request with its reason, which the person must be told.

A closed request keeps only the last characters of the person's numbers: it stays as
the record of an answer without being another copy of them. Every step is in the
audit log with the numbers masked.

**Legal holds.** A hold names a person by identifiers, with its reason and an
optional end date. While it is in force an erasure is refused outright, and the
message store's retention window spares the person's messages (records and bodies)
until the hold is lifted.

**Backups and the erasure ledger.** A backup made before an erasure still holds what
was erased until it expires. Every erasure is written to a ledger: when, by whom,
approved by whom, how much went from each store, and the person's identifiers as
salted SHA-256 hashes rather than in clear. After restoring a backup, **Apply erasures
again** hashes the identifiers the restored stores hold with each entry's salt and
erases every person the ledger names. Short numbers can be found from their hashes
by trying every value, so the ledger is itself personal data and is kept with the
database's own protection.

**Retention.** The screen lists how long each kind of record is kept here and what
changes it: message records and bodies by their retention windows, the queue until
delivered or removed, the FHIR store, prior authorization requests and the patient
index until erased (clinical records are not deleted on a schedule), the audit log
kept.

**Record of processing (Article 30).** **Record of processing** downloads, as
Markdown or CSV, what this server knows of it: the controller, the data protection
officer and where the data is kept (Settings → Data → Data protection), the retention
of each kind of record, and one processing activity per channel with what it
receives, from where, to which destinations, and the hosts it sends to. The purposes,
lawful bases and categories of data subject are left for the controller to fill in,
and say so. A CSV cell beginning with `=`, `+`, `-` or `@` is written with a leading
apostrophe, so a spreadsheet does not run it.

**Where the data is kept.** **Where the data is kept** (Settings → Data → Data
protection) states, in words a regulator can check, where the server, its database
and its backups run; the Data protection screen and the record show it. A draft data
processing agreement for organisations BioDream hosts is in
`docs/legal/dpa-template.md`; it is a draft for a lawyer, not legal advice.

The API is `/api/privacy` (summary), `/api/privacy/find`, `/api/privacy/requests`
(and `/{id}/approve`, `/{id}/export`, `/{id}/erase`, `/{id}/close`),
`/api/privacy/holds`, `/api/privacy/reapply` and `/api/privacy/register`.

## Tenants

A tenant is an isolation boundary: channels, messages, users and settings belong to one, and nothing crosses.

This exists for a service provider running feeds for several practices. Do not use it to separate departments within one
organisation that need to see each other's traffic: the isolation is real and there is no cross-tenant view. For that, use
channel groups (above), which divide the people of one organisation while letting a central team see everything.

### Limits

A platform operator sets each tenant's limits in **Tenants**, so one customer cannot crowd out the rest of a shared server.
Leave a limit empty for none.

- **Channels**: how many channels the tenant may have. Creating one more is refused, with the count and the limit.
- **Messages a minute**: how many messages its channels together may receive in a minute.
- **Storage**: how much stored message content it may keep, in megabytes.

None of them loses a message. Over the rate or the storage limit, a received message is refused the way a busy receiver
refuses one, before anything is stored or delivered: MLLP gets no acknowledgement (not a rejection, which some senders take
as "do not resend"), HTTP gets 429 with `Retry-After`, SOAP gets 503, DICOM gets a retryable status, and a queue, bucket or
file is left where it was. The sender resends, as every interface engine and EHR does. Nothing already stored is deleted to
make room. A raised limit takes effect within a minute, and every change of limits is recorded in the activity log.

## Test data

The generated messages described in [testing](#testing) exist partly as a privacy control. Reproducing a problem elsewhere, sending an example to a vendor, or populating a test environment are all things that otherwise get done with real messages.

The generator's output contains no patient data by construction rather than by redaction. Prefer it to hand-stripped real messages, which is a process that can be done incompletely and usually is.

## Single sign-on

Three mechanisms, and local accounts alongside all of them.

**OpenID Connect** (`-oidc`), **a directory over LDAP** (`-ldap`), and **SAML 2.0** (`-saml`). Each is configured from **Administer → Sign-on** and written to the file the flag names, so the screen and the file are two views of one thing rather than two places to keep in step.

Local accounts are always available and cannot be turned off. That is deliberate: an on-premises integration engine whose only way in is an identity provider becomes unreachable exactly when the identity provider is unreachable, and a clinical interface is not a good place to learn that. The sign-on screen tells you how many local administrators exist before it encourages you to depend on anything else.

### What a group mapping decides

All three map groups from the directory to Perfuse roles, and somebody in groups matching more than one gets the most privileged. That is the only safe direction to resolve it — the alternative is a person losing access they are entitled to because they are also in a lesser group.

Any of the six roles can be granted: monitor, viewer, operator, editor, admin and platform.

A group can also be limited to some channel groups, in the second box of its row (`channel_groups` in the file). Once any group
names one, every sign-in sets the person's channel groups from the groups that granted their role: somebody in a group that edits
the lab's channels and in another that views every channel is an editor of the lab's channels and nothing else. The strongest role
is never joined to the widest reach, because neither group grants that. A group with an empty box gives every channel. While no
group names any, sign-in leaves channel groups as an administrator set them in **Users**.

When every group a person matches names channel groups, each channel group gets the strongest role among the groups that name
it: with `Lab-Interfaces` (editor, lab) and `Clinical-IT` (viewer, lab and adt), the person edits the lab's channels and views
the admissions ones, as if set by hand. When one of their groups gives every channel, there is no list to hold a role per group,
and the rule above applies.

```yaml
roles:
  editor: [Lab-Interfaces]
  operator: [Night-Desk]
  viewer: [Clinical-IT]
  channel_groups:
    Lab-Interfaces: [lab, pathology]
    Night-Desk: [adt]
```

That is the OpenID Connect and SAML file. The directory's file maps each group to one role (`roles: {Lab-Interfaces: editor}`), and
`channel_groups` sits beside `roles` there.

A mapping that grants nobody anything is refused rather than saved. A configuration that authenticates people and turns all of them away looks identical to a broken product from the outside: the round trip completes and everybody is denied with nothing explaining why.

Creating an account on first sign-in is off by default. With it off, somebody has to exist in Perfuse before they can sign in, so the directory decides who they are and Perfuse decides who is allowed in.

### SAML, specifically

The parts worth knowing before you configure it:

- **Only the certificate you configure is trusted.** A certificate carried inside a response is never used. An attacker who can send a document can also put their own certificate in it, and a verifier that reads it is one anybody can authenticate to as anybody.
- **A response has to answer a sign-in this server started.** Perfuse remembers the request it sent and refuses a response naming anything else, or naming a request it has already answered. Without that check a valid response is a bearer token for whoever holds it: somebody signs in as themselves, keeps the response, and posts it into your browser, and you are then inside their account with the audit log recording their name.
- **Sign-ins started at the identity provider are therefore off by default.** Turn on *Accept sign-ins started at the identity provider* only if you need a portal tile, and knowing that it is what makes the above possible.
- **The group attribute has no default and is required.** Entra sends `groups`, Okta sends whatever the application was configured with, ADFS sends a claim URI. A guess would produce a sign-in that works and grants nobody anything. If a sign-in is refused for having no role, the server log names the attributes that did arrive — which is usually the whole answer.
- **The reply URL must match exactly** what is registered at the provider. A response says where it was destined and one addressed elsewhere is refused, because accepting it would mean accepting a response meant for a different service.

### What each provider sends

The group attribute is where sign-ins go wrong, and the three common providers each name it differently.

| Provider | Usually sends | Notes |
| --- | --- | --- |
| Keycloak | `groups` | Needs a group membership mapper added to the client. |
| Entra ID | a claim URI ending `/claims/groups` | Sends group object ids unless the application is configured to send names. Map the ids, or change the provider. |
| Okta | whatever the application was configured with | Commonly `groups`, with a filter deciding which are sent. |
| ADFS | a claim URI | Signs the Response rather than the Assertion, which Perfuse accepts. |

A claim URI may be entered by its final segment: typing `groups` finds `http://schemas.microsoft.com/ws/2008/06/identity/claims/groups`. A name that merely ends with the word does not match — `excluded_groups` is not `groups`.

Group membership arriving as one comma-separated value is treated as a single group name and matches nothing. That is the correct reading of the standard, and splitting on commas would mean a group legitimately containing one silently became two. If that is what your provider sends, change it there.

**Test** on the sign-on screen checks everything that can be checked without a person: the settings, the certificate, and whether a sign-in request can be built. It says plainly that it cannot prove somebody can sign in, because that needs a real assertion about a real person and the only way to get one is for them to try.

## Accounts provisioned by the identity provider: SCIM

With `-scim` (**Settings → Sign-in → Automatic accounts**), an identity provider such as Okta, Microsoft Entra ID or Google
Workspace creates, updates and removes Perfuse accounts itself at `/scim/v2/Users`, so a person who leaves the organisation loses
access when their directory account is disabled, not when somebody remembers. The provider authenticates with an API token holding
the admin role, made under **Users → Machine credentials**, sent as a bearer token.

- **Disabling an account ends its sessions at once**, and the server checks that none survived before answering. A provider is
  never told an account was disabled when it was not, because a provider does not retry what it was told succeeded.
- **A delete disables first, then deletes**, so a failure half way leaves an account nobody can sign in with. The audit history of
  a removed account is kept.
- **The role comes from the person's groups or roles**, matched by name: a group whose name contains *admin*, *editor*, *operator*,
  *viewer* (or *read-only*) or *monitor* gives that role, so `APP-Perfuse-Editor` and `Perfuse Operators` work as they are named.
  Anything else gets `-scim-default-role`, viewer unless set otherwise. Platform can never be granted this way.
- **No passwords.** An account made by SCIM signs in through the identity provider; a second password nobody upstream knows about
  would survive a disable there.
- `/scim/v2/ServiceProviderConfig` says what is supported: create, read, replace, patch and delete of Users, and filters up to 200
  results. Bulk operations, sorting and ETags are not.

## What Perfuse does not do

It does not encrypt the database at rest. Use filesystem or volume encryption.

It does not manage its own certificates or renew them. Point it at files and manage those files with whatever you already use.


SAML has been verified against two independent providers, each with a real browser sign-in, a real assertion and a real session: Keycloak 26 and Microsoft Entra. Two matters more than twice as much as one, because the two disagree in a way that turned out to be load-bearing — Keycloak writes its assertion with namespace prefixes and Entra writes the same elements without them, and the one defect this area has had was in prefix handling.

**Start by reading your provider's metadata.** Settings, then Sign-on, then the SAML tab: give the metadata URL or paste the document, and the sign-on address and signing certificate are filled in for you. Every SAML provider publishes one of these — Okta, Entra, AWS IAM Identity Center, Keycloak, ADFS — and it is the difference between configuring a provider and transcribing one. Doing it by hand means finding the certificate inside the XML, stripping the line breaks out of the base64 and wrapping it in PEM headers, where a single stray space produces a signature error that says nothing about formatting.

What comes back is described rather than dumped: each certificate's subject, expiry and SHA-256 fingerprint, so you can check it against what your provider's own console shows. An expired certificate is called expired rather than left as a date to compare by eye. Nothing is saved by reading — applying it to the form is a separate click, because a document somebody pasted is not yet a decision about what to trust.

A metadata URL is fetched over https only, and the address is checked against the same policy that governs channel destinations, so this cannot be used to make the server read cloud instance credentials. Redirects are not followed, because a permitted address redirecting to a blocked one would walk straight past that check.

**If you are configuring Entra, five things will not match what the examples show you.** It sends no email claim at all. Its NameID is an opaque identifier rather than an address. It sends no groups claim unless the application is explicitly configured to emit one, so a role mapping written against groups refuses every sign-in. When groups are emitted they are object GUIDs rather than names, unless your tenant synchronises from on-premises Active Directory. And its claim names live under `schemas.microsoft.com/identity` where most documentation shows `schemas.xmlsoap.org`. Perfuse handles all five; they are listed because the first four will otherwise look like faults in Perfuse when they are Entra being Entra.

That distinction is not theoretical here. The first genuine Keycloak assertion failed outright, because the canonical form the signature covers was being computed with namespace prefixes dropped. No real identity provider could have signed anybody in, and the package's own five canonicalisation tests passed throughout — each of them signed and verified with the same code, so they proved only that it agreed with itself. If you are the first to point a different provider at this, expect to find something, and the server log will name the stage that failed.
