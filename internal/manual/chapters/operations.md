# Operations

## Deployment

One binary and two paths: a directory of channel files and a database file. There is no application server, no separate web tier and no message broker required.

```
perfuse serve -channels ./channels -db ./perfuse.db -addr 0.0.0.0:8443 \
  -tls-cert ./cert.pem -tls-key ./key.pem
```

Run it under whatever supervises services on the host — systemd, launchd, a container runtime. It expects to be restarted if it exits.

## Configuration as files

Channels are files, which means they can be in version control, reviewed before deployment, and diffed after an incident.

Files are re-read without a restart. A file that does not parse is reported and the previously loaded version keeps running, so a syntax error cannot take a working feed off the air.

> A channel edited in the web interface is written back to its file. If those files are in version control, expect commits from configuration changes made through the interface, and do not hand-edit a file while somebody has it open in the builder.

## Backup

Two things to back up, and they have different characteristics.

The channel directory is small, changes rarely, and is what you need to rebuild the service. Back it up as configuration; version control is usually sufficient.

The database holds messages, users and audit history. It is large, changes constantly, and contains clinical data — so it needs backing up as clinical data, with the retention and encryption that implies. Losing it does not stop the service, but it loses the ability to investigate anything that already happened and the history the rhythm learner depends on.

## Restart behaviour

On restart, channels are loaded and sources begin listening. Queued deliveries resume.

The thing to understand is what happens to work in flight. Under the default `on_delivery` acknowledgement, a message that had not yet been acknowledged is not acknowledged, so the sender will resend it — which is the correct outcome. Under `on_receipt`, a message that was acknowledged but not yet delivered is in the queue, and resumes; if the queue is disabled, it is lost. See [message flow](#message-flow).

## Upgrading

Replace the binary and restart. The database schema is migrated forward automatically on start.

Migrations are forward-only. There is no downgrade, so a rollback to a previous binary after the schema has moved needs the previous database. Take a copy before upgrading — this is the one operational precaution worth being disciplined about.

## Monitoring

Alerting from within Perfuse is covered in [alerting](#alerting) and is what will tell you a feed has stopped.

From outside, monitor the process, the disk the database is on, and whether the web interface responds. The database grows and a full disk stops message recording, which is a failure mode with no other warning.

### SNMP

For monitoring tools that speak SNMP (Nagios, Zabbix, PRTG, SolarWinds, LibreNMS), Perfuse runs a read-only SNMPv2c agent and
sends a trap when an alert fires or resolves. Set it under **Settings → Monitoring → SNMP**, or with flags:

```
perfuse serve -snmp-listen :161 -snmp-community 'long-random-community' -snmp-allow 10.0.5.0/24 \
  -snmp-traps nms.example.org,10.0.5.9:1162
```

- The agent answers GET, GETNEXT and GETBULK, and only from the networks in `-snmp-allow`: v2c's community is a password sent in
  clear, so it is never open to everyone. A community under 6 characters, or `public`/`private`, stops the server at startup.
- It serves the system group (sysDescr, sysUpTime, sysName from the fleet label, sysContact, sysLocation), totals under
  `BASE.1` (channels, running, broken, received, delivered, failed, queued) and a channel table under `BASE.2.1` (name, running
  or stopped, received, delivered, failed, queued, age of the oldest queued message). The numbers are the ones the dashboard and
  `/metrics` show.
- Traps go to every manager in `-snmp-traps`: `BASE.0.1` an alert fired, `BASE.0.2` it resolved, carrying the alert's kind,
  severity, channel, destination and summary. Never message content. An acknowledged alert sends no trap.
- `BASE` is `1.3.6.1.4.1.8072.9999.9999.16` (Net-SNMP's experimental space) unless `-snmp-base-oid` puts it under your own
  enterprise number. **Download the MIB** on the Settings page (or `GET /api/snmp/mib`) gives a MIB for the base in use; the
  same panel says whether the agent is answering and how many traps went.

### An inventory of every interface

`-inventory interfaces.md` (**Settings → Monitoring → Inventory**) writes a Markdown document listing every channel - its format,
whether it runs, what it receives from and what it sends to - followed by each channel's own description, and rewrites it every
`-inventory-every` (a day by default). It is meant to be committed beside the channel files, or handed to an auditor, and read
without the application running.

## Capacity

Perfuse's throughput ceiling has not been measured on real hardware under real traffic, and this manual will not print a number it cannot support.

What can be said: the architecture is one goroutine per connection with per-destination queues, so throughput scales with destinations rather than being serialised through one worker; message recording is a SQLite write per message, which makes the disk the most likely first constraint; and the message store grows linearly with traffic, so retention is the setting that governs long-term disk use.

If you are sizing this for a large feed, measure it with your own traffic using the generated test corpus described in [testing](#testing). That is the honest answer, and it is also a better answer than a number from somebody else's hardware.

## Where this installation got stuck

Activity carries a panel, visible to administrators, showing what Perfuse has refused to do — grouped by message, most frequent first — and how far the installation has got towards a working channel.

It exists because every other claim about whether this software is easy to use comes from the people who wrote it. A refusal does not: somebody wanted something, the server said no, and neither party was guessing. It is the only evidence in the product that does not come from BioDream LLC.

The funnel names the step that has not happened yet rather than leaving timestamps to compare. The state worth watching for is **messages arrive and none has been delivered to a destination** — a channel can take traffic for weeks and deliver none of it, and from every other screen that looks like it is working.

**Nothing anybody typed is recorded.** The table holds the request route with identifiers replaced by `{id}`, the status, and the server's own sentence. A validation message names a field and a rule, which is the useful part; the value that broke the rule is very often patient data, and a table of those would be a worse liability than the friction it measured.

Two deliberate exclusions. A 5xx is a fault in this software and is logged as one already — mixing the two would bury the cases where somebody could not work out what to type under the cases where nothing they typed would have helped. And an expired session produces a run of 401s that say nothing about usability and would drown every finding that does.

Reading the report also trims the table to its most recent five thousand rows. Refusals are 4xx responses so it grows slowly, and reading is a natural moment to tidy: no timer and nothing that runs when nobody is looking.

`GET /api/friction` returns the same thing for anyone who would rather read it as JSON.

## Logs

Perfuse logs to standard output, which is where a supervisor or container runtime will collect it.

Message-level detail goes to the message store rather than the log, deliberately. A log line per message on a busy feed is noise that hides the lines that matter, and the store is searchable in ways a log file is not.

The log carries what the store cannot: startup and shutdown, configuration load results, channel state changes, and errors that are not attributable to a specific message.

## Running two instances

Two or more servers can run as one cluster: when the one running a feed stops, another takes it over within a few
seconds, and the sender's next attempt reaches it. They share one PostgreSQL database instead of each keeping a SQLite
file, and agree between them, through it, which of them runs what.

```sh
perfuse serve -db 'postgres://perfuse:SECRET@db.hospital.internal/perfuse?sslmode=verify-full' \
  -cluster active-passive -node-name perfuse-01 -node-address https://perfuse-01.hospital.internal:8443 \
  -data /var/lib/perfuse
```

Start each server the same way with its own name and address. The database account needs to create tables and, once,
the `citext` extension (or an administrator runs `CREATE EXTENSION citext` first). Each server keeps its own files in
`-data`: its log, its node id, and its copies of the configuration.

**Two ways of sharing the work.**

- `active-passive`: the leader runs every channel and every job that must run once (FHIR subscription notifications). The
  others wait. The simplest to reason about, and what a site with one busy server and a standby wants.
- `active-active`: the channels are spread over the live servers, about evenly. A server that stops hands its channels
  to the rest; one that joins takes its share as channels come free, one at a time. Channels that route to one another
  (a channel destination) run on one server and move together, because routing happens inside a server. An
  administrator can move a channel by hand from **Monitor → Fleet**; it stays where it was put while that server lives.

**Who runs what, and no moment with two.** Each piece of work is a lease in the database, renewed every fifth of
`-cluster-lease` (15 s by default). A server that cannot renew it (it crashed, its network failed, the database is
unreachable) stops that work by itself once its own clock says the lease is nearly done, with a third of the lease to
spare. Another server takes the lease only after its own clock has watched the lease's counter stand still for a whole
lease. No clock is compared with another, so the servers' clocks and the database's need not agree; they need only run
at about the same rate, which every clock does. Between the old holder stopping and the new one starting there is a short
gap with nobody listening, which a sender sees as a refused connection and retries; there is never a moment with two
listening on one feed or two workers draining one queue.

A server shutting down cleanly, or an administrator choosing **Step down** on the leader, hands over at once rather than
after the lease.

**What every server shares.** Messages, the delivery queue, accounts and sessions, the audit log, the FHIR store, the
terminology tables and the patient index are in the database, so whichever server runs a channel finds the queue the last
one left. The channel files, the settings file, the alert rules, single sign-on files, the logo, Direct certificates and
saved views are kept the same on every server through the database: each keeps its own copy on its own disk, a change on
any server (in the console, by hand, by a `git pull`) reaches the others within two seconds, and a channel changed on
another server restarts with its new file. The same file changed on two servers at once keeps the version that reached
the database first, and the other beside it as `NAME.conflict-SERVER`. Settings that apply only at a restart (the
message database, the ports the console listens on) are applied by each server at its next restart, and the log says so.
What names something on one machine stays on that server's command line and out of the shared file: the certificate and
key (`-tls-cert`, `-tls-key`), the SNMP listening address and the server's name in traces. Settings shows them as set
on each server.

**Start and stop.** Starting or stopping a channel on any server applies to the cluster: the server that runs it starts or
stops it within a second, and the answer says which server that is.

**The load balancer in front.** A feed's address must reach whichever server runs it. Point the sender at a virtual
address (a load balancer, a floating IP, a DNS name with a short TTL) and have the load balancer check
`/readyz?channel=NAME`, which answers 200 only on the server running that channel, and 503 on the others. For the console,
`/readyz` is 200 on every live server; `/readyz?role=leader` only on the leader.

On an AWS Network Load Balancer, use a target group per feed with an HTTPS health check on `/readyz?channel=NAME`, a
5-second interval and a threshold of 2, and turn **cross-zone load balancing on**. With it off, the load balancer's node
in a zone where no server runs the feed sees only unhealthy targets, sends to all of them anyway, and a sender that
resolved to that node waits until the feed comes back to its zone (95 seconds in a test). Set the deregistration delay
to 0, since a server that stops running a feed has nothing to drain.

**What was tested.** Two servers on PostgreSQL 16, a sender streaming 300 messages, resending until answered, and the
server running the feed killed with SIGKILL twice (the first, then the second after the first came back). Every
acknowledged message reached the receiver, in both shapes tried: delivered before the acknowledgement, and queued in the
shared store while the receiver refused (185 acknowledged messages waited in the queue at the kill; the survivor
delivered them all). Failover took 3 to 4 seconds on a 3-second lease. A duplicate is possible and was seen once in 600: a
message the dying server delivered and could not acknowledge, which the sender resent. The coordinator's own tests cover
a crash, a server cut off from the database while it runs on, step-down, spreading, moves, and shared configuration
with conflicts. They run in CI against PostgreSQL with the race detector, with the whole suite.

The same was then done on AWS: two Linux servers (ARM) and one Windows Server 2022 in three availability zones, Amazon
RDS PostgreSQL 16 over TLS with the certificate checked (`sslmode=verify-full`), an internal Network Load Balancer in
front, and senders resending until answered:

| Test | Messages | Acknowledged but lost | Duplicates | Longest a sender waited |
|---|---|---|---|---|
| Active-passive, the server running the feed killed twice (Linux, then Windows), receiver refusing in between | 2,000 | 0 | 0 | 15 s |
| A Linux leader cut off from the database (firewall), its process left running | 3,000 | 0 | 1 | 16 s |
| A Windows leader cut off from the database | 3,000 | 0 | 0 | 20 s |
| The leader killed while one server's clock ran 2 minutes fast and another's 90 seconds slow | 2,000 | 0 | 0 | 18 s |
| Active-active, four feeds over three servers, one killed and brought back | 6,000 | 0 | 2 | 24 s |

With the default 15-second lease, a server cut off from the database stopped its feed 10 seconds after its last renewal,
and the next server started it 6 to 8 seconds after that: never both at once. The skewed clocks changed nothing, since no
clock is compared with another.

**Not yet.** A cluster serves one organisation: `-cluster` with `-multi-tenant` is refused. Listeners move with their
channel, so the load balancer is the site's to provide.

A server on its own keeps working as it always has, on SQLite, with no cluster at all.

## More than one server

A site with two instances has a question no single console can answer: is everything
running? Mirth charges for the answer. This is the fleet view.

One instance is nominated as the place you look. It polls the others and shows every
channel on every server in one table, with a rollup across all of them.

### Adding a peer

Settings → Fleet, or `PUT /api/fleet/peers`. A peer needs a name, a URL, and a token
issued by the peer itself.

The name is required and is not the URL, because a URL is not something anybody recognises
at three in the morning.

The token must be created **on the peer**, under Users, and should be viewer-scoped. Adding
a peer without one is refused, with those instructions, rather than accepted and then
failing quietly on every poll. Reading another instance's health is the smallest privilege
there is, and it is the only one the fleet view needs.

### Controlling a peer, which is off

`allow_control` permits starting and stopping that peer's channels from here. It is off by
default and every use is audited by name.

The two privileges are deliberately separate. Reading another server's health is minor;
stopping its channels during a transfusion is not, and the second should never arrive
silently attached to the first.

### What the report says

Each server reports the total number of channels, how many are running, stopped or errored,
the queue depth and the age of the oldest waiting message, how many alerts are firing, and
whether it is draining for shutdown.

The rollup counts servers three ways: **reachable**, **unreachable** and **undetermined**.
The third covers peers whose health nobody knows — not yet polled, refusing the token, or
running a version this one cannot read. It is kept apart from unreachable because "we
cannot tell" is not "it is down", and folding the two together is how a fleet page starts
lying.

For the same reason the rollup carries **knownFrom**: how many instances the channel and
queue figures were actually read from. Twelve channels running across a fleet means
something different when two of five servers did not answer, and the total alone cannot say
so.

An unreachable peer says why in terms an operator can act on. A refused connection is
reported as the host being up with nothing listening on that port, which is a different
problem from a host that does not answer at all.

### Clock skew

The rollup reports the largest difference between the clocks of the servers in it. Worth a
number of its own: correlating an incident across two instances whose clocks disagree by
four minutes produces a sequence of events that did not happen.

### What a peer learns about you

Counts and rates. The report one instance gives another carries no message identifiers, no
channel-level detail and no patient data of any kind.

That is what stops a fleet view being a centralisation of clinical data: the aggregating
instance learns how many channels are running and nothing whatsoever about what flowed
through them. If you want the messages you open that server's own console, where the audit
log records that you did.

### A peer pointed at itself

Refused. An instance polling its own address through its own HTTP stack appears twice in
its own fleet view and double-counts every channel it has.

### TLS on internal networks

`insecure_skip_verify` accepts a peer's certificate without verifying it. It exists because
hospital infrastructure runs on private certificate authorities, and refusing to work at all
would push people onto plain HTTP, which is worse. It is named so it cannot be mistaken for
a good idea.
