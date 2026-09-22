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

## Capacity

Perfuse's throughput ceiling has not been measured on real hardware under real traffic, and this manual will not print a number it cannot support.

What can be said: the architecture is one goroutine per connection with per-destination queues, so throughput scales with destinations rather than being serialised through one worker; message recording is a SQLite write per message, which makes the disk the most likely first constraint; and the message store grows linearly with traffic, so retention is the setting that governs long-term disk use.

If you are sizing this for a large feed, measure it with your own traffic using the generated test corpus described in [testing](#testing). That is the honest answer, and it is also a better answer than a number from somebody else's hardware.

## Where this installation got stuck

Activity carries a panel, visible to administrators, showing what Perfuse has refused to do — grouped by message, most frequent first — and how far the installation has got towards a working channel.

It exists because every other claim about whether this software is easy to use comes from the people who wrote it. A refusal does not: somebody wanted something, the server said no, and neither party was guessing. It is the only evidence in the product that can contradict its author.

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

Two Perfuse processes against the same channel directory and the same database is not supported and will not behave well — both will try to listen on the same ports and both will write to the same SQLite file.

For availability, run one instance and make its restart fast. MLLP senders queue and retry, so a short outage is absorbed by the sender rather than losing data. This is a deliberate trade in favour of simplicity, and it means Perfuse is not the right choice if your requirement is continuous availability through a host failure.

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
