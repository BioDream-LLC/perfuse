# Delivery, Queueing and Retry

A destination that cannot be reached is the normal case, not the exception. Systems restart, networks drop, and a hospital interface engine is unavailable during its own maintenance window. Delivery is built around that.

## Queue per destination

```svg
<svg viewBox="0 0 760 236" xmlns="http://www.w3.org/2000/svg" role="img"
     aria-label="A message is attempted, and on failure joins a per-destination on-disk queue that retries with growing backoff, preserving order, until it is delivered or dead-lettered.">
  <defs>
    <marker id="q-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="#64748b"/>
    </marker>
    <style>
      .q-box  { fill:#f1f5f9; stroke:#94a3b8; stroke-width:1.2; }
      .q-ok   { fill:#ecfdf5; stroke:#10b981; stroke-width:1.2; }
      .q-disk { fill:#eef2ff; stroke:#6366f1; stroke-width:1.2; }
      .q-dead { fill:#fef2f2; stroke:#ef4444; stroke-width:1.2; }
      .q-t    { font:600 12px system-ui,sans-serif; fill:#0f172a; }
      .q-s    { font:11px system-ui,sans-serif; fill:#475569; }
      .q-l    { stroke:#64748b; stroke-width:1.4; fill:none; marker-end:url(#q-arrow); }
    </style>
  </defs>

  <rect class="q-box" x="8" y="58" width="92" height="40" rx="7"/>
  <text class="q-t" x="54" y="83" text-anchor="middle">Attempt</text>

  <rect class="q-ok" x="150" y="12" width="104" height="38" rx="7"/>
  <text class="q-t" x="202" y="36" text-anchor="middle">Delivered</text>

  <!-- The queue itself, drawn as slots so that "in order" is visible rather than asserted. -->
  <rect class="q-disk" x="150" y="86" width="230" height="58" rx="8"/>
  <text class="q-s" x="158" y="103">queue on disk — per destination</text>
  <g class="q-disk">
    <rect x="160" y="110" width="28" height="24" rx="3"/>
    <rect x="194" y="110" width="28" height="24" rx="3"/>
    <rect x="228" y="110" width="28" height="24" rx="3"/>
    <rect x="262" y="110" width="28" height="24" rx="3"/>
  </g>
  <text class="q-s" x="300" y="127">1, 2, 3, 4 …</text>

  <rect class="q-box" x="430" y="86" width="118" height="58" rx="7"/>
  <text class="q-t" x="489" y="108" text-anchor="middle">Retry</text>
  <text class="q-s" x="489" y="126" text-anchor="middle">growing backoff</text>

  <rect class="q-dead" x="596" y="86" width="150" height="58" rx="7"/>
  <text class="q-t" x="671" y="108" text-anchor="middle">Dead letter</text>
  <text class="q-s" x="671" y="126" text-anchor="middle">kept, never discarded</text>

  <g class="q-l">
    <path d="M100 70 V31 H146"/>
    <path d="M100 86 V115 H146"/>
    <path d="M380 115 H426"/>
    <path d="M548 115 H592"/>
  </g>

  <!-- Retry going back to the front of the queue, which is why order survives. -->
  <path class="q-l" d="M489 86 V70 H265 V84"/>
  <text class="q-s" x="300" y="66">the same message, at the front</text>

  <text class="q-s" x="8" y="172">Each destination has its own queue, so one slow receiver does not hold up the others.</text>
  <text class="q-s" x="8" y="190">The queue is on disk, so a restart does not lose it. Order is preserved: message 2 is not attempted before message 1 succeeds or is given up on.</text>
  <text class="q-s" x="8" y="208">A message that exhausts its attempts is dead-lettered and kept. Nothing is deleted to make a queue look healthy.</text>
</svg>
```


Each destination has its own queue. A destination that is failing does not affect the others, and each has its own depth, age and retry state.

Queueing is per destination rather than per channel because the failure is per destination. A channel-wide queue would hold up delivery to three working systems because a fourth is down, and the two situations — one destination unavailable, all four unavailable — would look the same in the statistics.

## Queue settings

`queue` on a destination:

| Key | What it does |
|---|---|
| `enabled` | Whether failed messages are queued at all. |
| `max_attempts` | How many times to try before giving up. |
| `backoff` | Wait before the first retry. |
| `max_backoff` | Ceiling on the growing wait. |
| `max_depth` | Most messages to hold. Unset means no limit. |
| `retain_hours` | How long a delivered message is kept. Unset keeps it indefinitely. |

The last two were missing from this table and from the builder, and they are the pair that stops a disk filling. A queue with neither grows until the filesystem is full, and that failure arrives as something unrelated breaking — a write failing somewhere else entirely — rather than as a queue problem.

Backoff grows between attempts up to `max_backoff`. Growing rather than fixed, because a destination that is down is usually down for minutes rather than seconds, and retrying every second for ten minutes produces several hundred log entries that bury the one that matters.

## Ordering is preserved

Within a destination, messages are delivered in arrival order, and a message that fails holds its place. Later messages wait behind it rather than overtaking.

This is the right default for clinical data — an admission and a discharge applied in the wrong order is worse than both being late — and it has a consequence worth understanding: **one message that always fails will hold up everything behind it.**

That is why queue depth and queue age are separate alert conditions. A deep queue that is draining is a busy system. A queue whose oldest message keeps getting older is stuck, and the alert kinds `queue-depth`, `queue-age` and `queue-stuck` distinguish them.

> A poison message — one that will never succeed no matter how often it is retried — is the specific failure this design is vulnerable to. `max_attempts` is what limits the damage: after it, the message is recorded as failed and the queue moves on. Setting `max_attempts` very high in the hope of eventual success converts a single failed message into a stopped feed.

## When queueing is disabled

With `queue.enabled` false, a failed delivery is a failed delivery. It is recorded and not retried.

That is the right choice when late data is worse than no data — a real-time display, a paging system, anything where a message delivered forty minutes after the event would be misleading rather than merely delayed.

It is the wrong choice for anything that goes into a record, where the message is just as valid an hour later.

## Retry versus queue

There are two mechanisms and they operate at different levels.

`retry` on a destination governs immediate attempts within one delivery — a connection that fails is tried again after a short pause, without leaving the delivery path. This handles transient faults: a dropped connection, a momentary refusal.

`queue` governs what happens once those immediate attempts are exhausted. The message leaves the delivery path and is retried later on a longer schedule.

Both exist because the two failures are different. A connection reset on the first attempt is usually gone by the second, and going through the queue for it would add minutes of latency to a fault that lasted milliseconds. A destination that has been switched off for maintenance will not come back within three immediate attempts, and hammering it is pointless.

## What an HTTP endpoint counts as delivered

An HTTP destination has three settings that decide whether a message is treated as sent, and all three are on the destination in the builder.

**Status codes that mean delivered.** Empty means any 2xx. Some endpoints answer `202 Accepted` for something they have not processed yet, and some answer `200` with an error in the body — the second is what *Treat as failed if the reply contains* is for. Setting an explicit list is worth doing when a partner has told you which code means what, because the alternative is discovering the difference from a message that was never delivered and never queued.

**Follow redirects.** Off by default, and deliberately. A redirect can point at a different host, so following one sends the message somewhere nobody configured and reports success.

**Bearer token.** Sent as an `Authorization` header. It is stored with the channel and never shown again, so leaving the box empty when editing an existing channel keeps the token already saved rather than clearing it.

## Naming and pruning an archive folder

A file destination writes one file per message by default, named with a timestamp and the message's control ID. Three settings on the destination change that.

**File name** takes `${...}` placeholders, so a name can carry the date or the message type. This is worth setting: the default is correct and unfindable, and a folder of timestamps is one nobody searches.

**Suffix while writing** defaults to `.part` and is removed by a rename once the file is complete. Whoever collects these files should not pick up a half-written one, and a rename within a folder is atomic. Change it only if the collector filters on a different extension.

**Delete after** prunes files older than the given number of hours. Empty or zero keeps them forever, which is the default and is a decision rather than an oversight — but an archive nobody prunes fills the disk, and a full disk stops the channel rather than the archiving.

## Sending to a FHIR server

Three settings decide how much is checked before a bundle leaves.

**Claim US Core conformance** adds the profile to each resource. Only claim it when the resource carries what the profile requires, because a receiver may validate against the claim and reject a bundle that would otherwise have been accepted.

**Validate before sending** checks each resource here rather than learning from the receiver. A rejection at the far end tells you less, later, and often only in a log you cannot read.

**Treat warnings as rejections** only has an effect when validation is on, and the control is unavailable until it is. It is strict enough to stop a bundle a receiver would accept, so it belongs on a feed being brought up rather than one in service.

## When the transport says yes and the message did not arrive

"Did this arrive" is often not a question the transport can answer. An MLLP receiver returns an application acknowledgement whose meaning is in its text. An HTTP receiver returns 200 with an error document. In both cases the transport succeeded and the message did not arrive.

**Inspect the reply** on a destination takes a script given the receiver's response. Return false, or throw, and the delivery is marked failed — which means it retries, queues, and appears in the failure count instead of being silently counted as delivered.

It is offered only on destinations that receive a reply. On one that cannot, it is refused at load rather than left never running.

## What a delivery outcome means

| Outcome | Meaning |
|---|---|
| `Delivered` | The destination accepted the message. For MLLP, a positive acknowledgement was received. |
| `Queued` | Delivery failed and the message will be retried. |
| `Failed` | Delivery failed and will not be retried, either because queueing is off or attempts are exhausted. |
| `Filtered` | The destination declined the message by its own filter. Not an error. |

These are recorded per destination per message, so a message that reached three of four destinations is recorded as exactly that. There is no channel-level "success", because it would not mean anything.

## Slow delivery

A destination that is accepting messages but taking a long time about it is a distinct problem from one that is failing, and it is easy to miss because every outcome is a success.

The `slow-delivery` alert kind exists for this. It is worth setting on anything where the sender's acknowledgement waits on delivery, because a destination whose response time has quietly doubled will eventually cross the sender's timeout and start producing resends — and at that point the symptom appears upstream, in a system you may not administer.

## Writing to a database

`type: database` runs a statement per message with values bound from the message.

**Placeholders are the driver's own** — `$1` for postgres, `?` for mysql and sqlite, `@p1` for sqlserver — and Perfuse does not rewrite them. That is deliberate: a translation layer that got a placeholder wrong would bind a patient's name to the wrong column, and the result would look like valid data rather than an error.

A statement written in the wrong dialect is refused when it is saved, naming the style the driver wants. Before that check existed the mistake surfaced as `syntax error at or near ","` from the server when a message arrived, which names a statement you did write and so sends you looking for a typo instead of at the dialect.

```yaml
destinations:
  - name: warehouse
    type: database
    database:
      driver: postgres
      dsn: ${WAREHOUSE_DSN}
      statement: INSERT INTO messages (mrn, family_name, birth_date) VALUES ($1, $2, $3)
      params:
        - PID-3.1
        - PID-5.1
        - PID-7
```

A quoted parameter is a constant rather than a path, which is how a channel writes its own name or a source system code into a row beside the message data.

Values are always bound as parameters. A `${...}` reference inside the statement is refused: a name containing an apostrophe would break the statement, and a hostile value would rewrite it.

## Sending to a raw socket

`type: tcp` writes to a socket with framing you choose, which is what a device or a laboratory instrument usually wants. MLLP is one framing among several; this destination offers the others.

```yaml
destinations:
  - name: instrument
    type: tcp
    tcp:
      address: instrument.lab:9100
      framing: delimited
      delimiter: \r
      expect_reply: true
      timeout: 30s
```

**Framing has no default, deliberately.** Writing with the wrong framing does not fail. The far end reads messages split in the wrong places, or waits for a terminator that never comes, and the symptom appears at the receiving system rather than here.

| `framing` | What it writes |
| --- | --- |
| `mllp` | `0x0b` before, `0x1c 0x0d` after. The same framing an MLLP destination uses. |
| `delimited` | A `delimiter` after each message, and an optional `start_block` before it. |
| `fixed` | Records padded or truncated to `record_length`. |
| `length` | A `length_bytes` prefix. `big_endian` and `length_includes_header` both have two conventions in the wild and the difference is silent. |
| `whole` | One message per connection, closed to mark the end. |

Only the settings the chosen framing uses are accepted. A record length on a delimited stream is refused rather than ignored, because a setting that is silently ignored is one somebody believes is in effect.

`expect_reply` changes what delivered means. Without it, success means the bytes reached the operating system's send buffer — which a peer that crashed a moment later never read.

## Delivery to another channel

A channel destination hands the message to another channel by name. The receiving channel treats it like any other arrival: it records it, filters it, transforms it and fans it out.

This is the supported way to build a chain. It is deliberately visible — two channels, two sets of statistics, two entries in the message history — because a chain hidden inside one channel is a chain nobody can monitor.

The message is handed over in memory rather than over a network, so there is no framing, no acknowledgement and no timeout between the two.

## What happens when the process is killed

The question anyone who has run an interface engine asks first, and the one that is
rarely answered, because the honest answer is usually embarrassing.

It was answered by killing the process with `SIGKILL` part way through a batch — which
is what a power failure, an out-of-memory kill and a hypervisor reset all look like from
inside — and counting what survived against what had been promised.

The property under test is not "nothing is lost". That is not achievable and claiming it
would be a lie. It is the narrower promise a sender actually relies on: **a message that
was positively acknowledged is a message that arrived.**

With `ack.when: on_delivery`, which is the default, every destination is written before
the acknowledgement goes out, so an `AA` is a statement about the destination rather than
about a queue. A message killed before its acknowledgement may well be lost, and that is
correct: the sender was never promised anything and will send it again.

### Acknowledging on delivery

Five runs, killed after 5, 25, 40, 75 and 110 messages of 120.

| Acknowledged | Present downstream afterwards | Acknowledged but missing |
|---|---|---|
| 255 | 255 | 0 |

The promise held at every kill point, and nothing was duplicated. A duplicate would have
been acceptable and a loss would not: a resent A08 is a nuisance receivers absorb, and a
lab result that silently never arrived is a patient safety event.

### Acknowledging on receipt

`on_receipt` answers as soon as the message is queued, before any destination has been
written. This chapter has always said an acknowledged message can still be lost that way.
Here is the size of it.

| Killed after | Acknowledged | Present downstream | Lost |
|---|---|---|---|
| 5 | 5 | 1 | 4 |
| 25 | 25 | 1 | 24 |
| 75 | 75 | 2 | 73 |
| 110 | 110 | 2 | 108 |

Essentially everything in flight. The setting is not a defect and there are feeds where
it is the right choice, but it should be chosen knowing that a crash discards what has
been acknowledged and not yet written, and that this is almost all of a burst.

Nothing recovered on restart, because `perfuse run` has no durable store. A destination
that must not lose messages during an outage needs `queue.enabled`, which needs the
database that `perfuse serve` provides — and `perfuse run` now refuses such a channel
rather than starting it without the queue.

### A sender that dies half way through a message

The most dangerous of these faults, because a truncated HL7 message usually still parses.
The segments before the cut are complete and well formed, so a receiver has no way to
know that `PID` and `PV1` arrived and the `OBX` segments carrying the results did not.

A frame was opened, six tenths of a message sent, and the connection reset rather than
closed. The partial message was not delivered, the listener survived, and a well-formed
message sent immediately afterwards was accepted normally, so the fragment was not
carried into the next message either.

The connection log now reports how many bytes of an unfinished frame were abandoned, for
every failure mode rather than only a tidy close. Previously a crashed sender, a killed
process and a pulled cable all logged `messages=0` and nothing else, which cannot
distinguish an empty health check from a lab result thrown away nine tenths of the way
through.

### A full disk

Space exhaustion is detected and logged rather than swallowed. Two things about it are
worth knowing.

The sender does not get a prompt rejection. The delivery retries on the usual schedule —
one second, two, four, eight — and the negative acknowledgement arrives about fifteen
seconds later. A sender whose own timeout is shorter than that sees a stalled connection
rather than an `AE`, and will conclude the network is at fault rather than the disk.

And a volume reporting zero bytes free can still accept small appends for a while,
because adding a couple of hundred bytes to a file whose last block has room needs no new
allocation. The first write failure is later than the moment the disk filled.

### What was not tested

A destination directory losing write permission. It could not be injected with `chmod`:
the engine holds the output file open, and changing a file's mode does not affect a
descriptor already opened against it. Testing it properly needs the volume remounted
read-only or removed underneath the process, which has not been done. It is named here
rather than omitted, because a gap nobody mentions reads as a gap nobody looked for.
