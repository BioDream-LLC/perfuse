# GUI audit

The goal, stated plainly: **everything configured via the web GUI.** Everything.

This is the inventory. Each item is either reachable from the interface today, or it is not and says what is needed.

## Already fully in the GUI

These need nothing.

| Thing | Where | Notes |
| --- | --- | --- |
| Channels | Channels tab, builder | Create, edit, delete, start, stop, version history, restore |
| Users | Users tab | Create, edit, disable, delete, change role |
| API tokens | Users tab | Create, revoke — **added 2026-08-22**; see the correction below |
| Tenants | Users tab (platform role) | Create, edit, delete |
| Queue | Queue tab | Retry, skip, drain, remove |
| Messages | Messages tab | Search, inspect, reprocess, replay |
| Lookup tables | Tables tab | Editable |
| Certificates | Certificates tab | Inspect |
| Alerts | Alerts tab | **View and acknowledge only** — see below |
| Scripts | Script lab | Check and edit within a channel |
| Mirth import | Migrate tab | Full |

### A correction to this document

The row above originally claimed API tokens were already manageable in the interface. **They were not.** I wrote that from
memory and did not check, and the checking took thirty seconds: there was no `/api/tokens` route and no mention of tokens
anywhere in the front end. They could only be created with `perfuse token create` on the server.

That mattered more than it looks, because the FHIR server's authentication — added the same night — uses these tokens. So
securing the FHIR server had made a shell on the server a prerequisite for letting a lab system connect to it.

Now built, and the audit is more trustworthy for having been wrong once in public: every other "already there" row in the
table above was re-checked against the route table afterwards.

## Added since the audit

- **Machine credentials** — issue, list and revoke API tokens. Was CLI-only, which mattered because FHIR authentication uses
  them.
- **X12 acknowledgement settings** — level, sender identifier and qualifier, with the two mutually exclusive configurations
  warned about inline rather than only at save time.
- **Passkeys** — add and remove your own, plus a sign-in button. Three separate availability checks, because a button that
  does nothing reads as a broken product rather than an unconfigured one.
- **Everything in the settings registry** — 26 settings in 8 groups, one page, generated from a single Go registry so the
  server and the interface cannot describe a setting differently. See the revision below for which items on the gap list
  this closed.

## Revision, 2026-08-22: centralized settings landed, and this list shrank

A settings page now exists covering **26 settings in 8 groups**, generated from one registry in Go so that adding a
setting is one entry and no front-end work. That closes several items below outright, and it changes the shape of the rest.

**Closed by it:**

- **Item 4, retention and payload storage.** Now `data.retentionDays`, `data.storeMessages`, `data.storePayloads`, plus
  two that did not exist when the item was written: `data.payloadDays` and `data.purgeEveryHours`. All editable, and the
  first three take effect without a restart.
- **Item 6, alert delivery.** `alerts.webhook`, `alerts.minSeverity`, `alerts.throttleMinutes`. The webhook is treated as
  a credential, exactly as the item warned: reported as set or not set, never as a value, and kept out of the audit
  detail.
- **Item 7, tracing.** `monitoring.traceEndpoint`, `monitoring.traceSamplePercent`, `monitoring.jsonLogs`.
- **Item 8, egress policy.** Present as `security.allowMetadataEgress` with `security.egressReason` — which is a reversal
  of what this document argued, and the reasoning below was wrong. The control an author must not be able to grant
  themselves is the *channel-level* exemption, and that is still refused at load. A platform administrator deciding
  installation policy is a different person doing a different thing, and hiding that decision in a command line did not
  make it safer, it made it invisible. It is platform-only and audited.

**Also now in the interface, and not previously on this list:** the passkey domain and display name, SCIM enable and
default role, FHIR read-only and page size, engine queue workers, retry limits, retry backoff, drain timeout, and the
fleet label.

**Still absent, and the list is now short.** Alert *rules*, single sign-on, directory sign-in, and fleet peers — items
1, 2, 3 and 5. Every one is a file with real structure rather than a set of scalar values, which is why none of them fit
the settings registry: the registry describes a setting with a kind and a widget, and an OIDC configuration is a nested
object with a credential in it. These need their own editors, in the shape the channel builder already uses — read,
validate, write, version, restore.

**One correction to the section below.** Item 5b argued SCIM and the passkey domain must stay out of the interface
because they are decisions about how the server itself is reached. They are now in it. The argument was half right: a
*channel author* must not be able to grant themselves a route to account creation, and cannot — these are platform-only
and audited. But an operator with the platform role already has every power SCIM would grant them, so withholding the
switch protected nothing and meant a hospital had to redeploy to connect its identity provider. The passkey domain is
still refused at startup if it is wrong, which is where that check belongs.

## Not in the GUI

Ordered by how much it hurts. Items 4, 6, 7 and 8 were here and are now closed — see the revision above.

### 1. Alert rules — file only (`-alerts`)

The Alerts tab shows what has fired and lets somebody acknowledge it. The **rules** are a YAML file, so deciding what
should alert means editing a file on the server and restarting. This is the one an operator hits first: alerting is
tuned constantly in the first weeks of a deployment.

### 2. Single sign-on — file only (`-oidc`)

A whole security feature configured nowhere in the interface. The sign-in button appears, but the issuer, client
credentials, group mapping and whether accounts may be created are all in a file.

### 3. Directory sign-in — file only (`-ldap`)

Same. Worse in one respect: the group-to-role mapping is the thing people get wrong, and there is no way to see it in
the interface, let alone change it.

### 4. Message retention and payload storage — flags only

`-retention-days`, `-store-messages`, `-store-payloads`. These decide how much patient data is kept on disk and for how
long, which is a question a hospital's privacy officer asks and an operator should be able to answer and change without
a restart.

### 5. Fleet peers — file only (`-peers`)

The Fleet tab shows peers. Adding one means editing a file.

### 5b. SCIM and passkeys — flags only, deliberately

Both are turned on with a flag and neither is editable in the interface, and that is the right way round rather than an
omission.

Turning on SCIM exposes endpoints that create and delete accounts. Turning on passkeys fixes the domain every credential is
bound to. Both are decisions about how the server itself is reached, made once at deployment - and letting them be changed
through the interface would mean an administrator could grant a route to account creation, or change what existing credentials
are bound to, from inside the thing being protected.

What *is* in the interface is everything that follows from them: a person's own passkeys, and the accounts SCIM provisions.

### 6. Alert delivery — flags only

`-alert-webhook`, `-alert-severity`.

Worth noting the webhook URL frequently carries a token in its path, so if this becomes editable it is a credential and
belongs behind the same redaction as everything else.

### 7. Tracing — flags only

`-trace-endpoint`, `-trace-instance`, `-trace-sample`.

### 8. Egress policy — flag only

`-allow-metadata-egress`. Deliberately operator-only and should **stay** out of the GUI: the whole point is that a
channel author cannot grant themselves the exemption. It belongs in the inventory as a decision, not a gap.

## Genuinely cannot be GUI settings

Startup topology. A GUI that rewrote these would be editing its own foundations while standing on them.

- `-addr`, `-db`, `-channels` — where the process listens, and which files and database it is using
- `-tls-cert`, `-tls-key` — needed before the interface can be served at all
- `-multi-tenant`, `-engine`, `-fhir` — what the process *is*
- `-json-logs`, `-insecure`, `-drain-for`

These should be **visible** in the interface as read-only, with an explanation, rather than absent. Somebody
diagnosing a problem needs to know what the process was started with, and "not shown anywhere" is how a wrong flag
survives for months.

**Partly done, and the exclusion list is now narrower than this.** `-json-logs` and `-drain-for` are in the settings
registry: neither decides where the process finds anything, and both were on this list by association rather than for a
reason. The three that genuinely cannot be there are the listen address, the database path and the channels directory,
because each one decides where the process finds the thing it is editing — and they are absent from the registry
deliberately, with that reasoning recorded beside it.

The read-only display of what the process was started with does exist: `api.StartupSettings` carries the flags and the
settings page reports a conflict at startup when a flag disagrees with the file, naming both values.

## The approach

The GUI already edits YAML files for channels: read, validate, write, version, restore. That pattern is proven and it
keeps two properties worth keeping — configuration stays diffable and reviewable on disk, and there is no second source
of truth.

So the settings pages should edit the **same files** the flags point at, through the same shape of API. Not a parallel
set of values in the database, which would raise the question of which wins.

Where no file is configured, the interface should offer to create one rather than silently having nowhere to write.

**This is what was built, and the "which wins" question got an explicit answer.** Settings live in a YAML file beside the
database, not in the database. Flags seed that file on first start and are ignored afterwards, and a flag that disagrees
with the file is reported at startup naming both values. The file wins.

Three reasons that way round: change control wants configuration diffable, an operator with the machine but not the
application can read a file and cannot read a database, and restoring yesterday's settings should not require the
application to be working. Only flags actually *passed* are recorded — writing every default would assert each one as a
deliberate choice and no future version could change any of them.

The remaining editors — alert rules, OIDC, LDAP, peers — should follow the channel builder rather than the settings
registry, because each is a structured document rather than a set of scalars.
