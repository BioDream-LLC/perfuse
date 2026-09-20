# Message Flow

This chapter follows one message from the moment it arrives to the moment it is delivered or given up on. Knowing this order is the fastest way to work out where a change belongs, and why something is not happening.

## The order of events

```svg
<svg viewBox="0 0 760 250" xmlns="http://www.w3.org/2000/svg" role="img"
     aria-label="A message passing through a channel: arrival, parse, record, filter, transform, fan out to destinations, record the outcome, acknowledge.">
  <defs>
    <marker id="mf-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="#64748b"/>
    </marker>
    <style>
      .mf-box  { fill:#f1f5f9; stroke:#94a3b8; stroke-width:1.2; }
      .mf-keep { fill:#eef2ff; stroke:#6366f1; stroke-width:1.2; }
      .mf-stop { fill:#fef2f2; stroke:#ef4444; stroke-width:1.2; }
      .mf-t    { font:600 12px system-ui,sans-serif; fill:#0f172a; }
      .mf-s    { font:11px system-ui,sans-serif; fill:#475569; }
      .mf-l    { stroke:#64748b; stroke-width:1.4; fill:none; marker-end:url(#mf-arrow); }
      .mf-d    { stroke:#94a3b8; stroke-width:1.2; fill:none; stroke-dasharray:4 3; marker-end:url(#mf-arrow); }
    </style>
  </defs>

  <!-- The happy path, left to right. -->
  <rect class="mf-box" x="8"   y="60" width="86" height="42" rx="7"/>
  <text class="mf-t" x="51"  y="80" text-anchor="middle">Arrival</text>
  <text class="mf-s" x="51"  y="94" text-anchor="middle">de-frame</text>

  <rect class="mf-box" x="118" y="60" width="78" height="42" rx="7"/>
  <text class="mf-t" x="157" y="86" text-anchor="middle">Parse</text>

  <rect class="mf-keep" x="220" y="60" width="86" height="42" rx="7"/>
  <text class="mf-t" x="263" y="80" text-anchor="middle">Record</text>
  <text class="mf-s" x="263" y="94" text-anchor="middle">as received</text>

  <rect class="mf-box" x="330" y="60" width="78" height="42" rx="7"/>
  <text class="mf-t" x="369" y="86" text-anchor="middle">Filter</text>

  <rect class="mf-box" x="432" y="60" width="96" height="42" rx="7"/>
  <text class="mf-t" x="480" y="86" text-anchor="middle">Transform</text>

  <rect class="mf-box" x="552" y="16" width="112" height="38" rx="7"/>
  <text class="mf-t" x="608" y="40" text-anchor="middle">Destination A</text>
  <rect class="mf-box" x="552" y="62" width="112" height="38" rx="7"/>
  <text class="mf-t" x="608" y="86" text-anchor="middle">Destination B</text>
  <rect class="mf-box" x="552" y="108" width="112" height="38" rx="7"/>
  <text class="mf-t" x="608" y="132" text-anchor="middle">Destination C</text>

  <g class="mf-l">
    <path d="M94 81 H116"/>
    <path d="M196 81 H218"/>
    <path d="M306 81 H328"/>
    <path d="M408 81 H430"/>
    <path d="M528 81 H548"/>
    <path d="M534 81 V35 H550"/>
    <path d="M534 81 V127 H550"/>
  </g>

  <!-- Where a message stops, which is the part prose describes badly. -->
  <rect class="mf-stop" x="118" y="150" width="78" height="34" rx="7"/>
  <text class="mf-s" x="157" y="171" text-anchor="middle">unparseable</text>
  <path class="mf-d" d="M157 102 V148"/>

  <rect class="mf-box" x="330" y="150" width="78" height="34" rx="7"/>
  <text class="mf-s" x="369" y="171" text-anchor="middle">filtered</text>
  <path class="mf-d" d="M369 102 V148"/>

  <text class="mf-s" x="8" y="210">Filtered is a normal outcome, not an error. Unparseable is recorded and stops.</text>
  <text class="mf-s" x="8" y="228">The acknowledgement is sent after delivery by default, so a sender is not told "received" before it is true.</text>
</svg>
```


1. **Arrival.** The source accepts the bytes. For a stream transport this includes de-framing — finding where one message ends and the next begins.
2. **Parse.** The bytes become a tree according to the channel's `dataType`. A message that cannot be parsed stops here and is recorded as a parse failure.
3. **Record.** The message is written to the message store as received, before anything modifies it. This is what makes replay and reprocessing possible.
4. **Filter.** If the channel has a `filter`, it decides whether to accept the message. A rejected message is recorded as filtered, which is a normal outcome and not an error.
5. **Transform.** The channel's `transformations` run in order, each against the result of the last.
6. **Fan out.** Each destination gets a copy of the transformed message, applies its own transformations if it has any, and delivers.
7. **Record the outcome.** Per destination: delivered, queued, failed, or filtered.
8. **Acknowledge.** If the source expects an acknowledgement, it is generated and sent. By default this happens here, after delivery — see below.

## When the acknowledgement is sent

This is the most consequential setting in a channel, because it decides what the sender is being promised. It is `ack.when`, and it has two values.

`on_delivery` is the default. The acknowledgement is sent only after every enabled destination has accepted the message. A positive acknowledgement therefore means the data actually arrived somewhere, which is the strongest claim an engine in the middle can honestly make.

`on_receipt` acknowledges as soon as the message is parsed and queued, before any destination has been written.

Perfuse defaults to `on_delivery` because promising delivery and then losing the message is worse than being slow. Under `on_receipt`, a message that has been acknowledged can still be lost if the process dies with work queued — the sender believes it is delivered, nothing upstream will resend it, and the only record that it existed is in Perfuse's own store.

## When to choose on_receipt

The case for it is real, and it is timeouts. An MLLP sender that does not get an acknowledgement within a few seconds will typically resend, so a slow destination under `on_delivery` produces duplicates rather than delay. If one destination is a slow archive or a system that is regularly unavailable, `on_delivery` couples the sender's timeout to that destination's worst case.

The trade is explicit: `on_receipt` moves the risk from duplicates to loss. Choosing it means accepting that an acknowledged message can be lost in a crash, and it is worth pairing with queue-depth alerting so that a queue which has stopped draining is noticed. See [alerting](#alerting).

> Under either setting, a positive acknowledgement is not a promise that the receiving application has *processed* the message — only that it accepted the bytes. If you need to know a result was filed, that has to come from the receiving system, and no integration engine can provide it.

## Where the message is recorded

Before the filter, and therefore before any transformation. Three consequences:

- A message the filter rejected is still in the store. You can see what arrived and confirm the filter was right.
- Replay and reprocessing always start from what actually arrived, not from a partially transformed version.
- A change to the transformations can be tested against real historical traffic, because the original is still there. See [testing](#testing).

The transformed form is also recorded, per destination, alongside the outcome. So the store holds what arrived, what was sent, and what happened — which is what an investigation needs.

## Failure at each stage

| Stage | What failure looks like | What happens |
|---|---|---|
| Arrival | Framing error, connection dropped mid-message | Recorded, connection closed, sender retries |
| Parse | Not valid for the declared type | Recorded as a parse failure, negative acknowledgement if the source expects one |
| Filter | The filter itself errors | Treated as a refusal, recorded with the error |
| Transform | A step fails | Depends on the step's `on_error`; see [transformation](#transformation) |
| Deliver | Destination unreachable or rejects | Queued and retried per the destination's policy; see [delivery](#delivery-queueing-and-retry) |

The important asymmetry: a parse failure is permanent and a delivery failure usually is not. Retrying an unparseable message will never help, so it is not retried. Retrying an unreachable destination usually will, so it is.

## Ordering

Within one destination, messages are delivered in the order they arrived, and a message that fails and enters the queue holds its place — later messages wait behind it rather than overtaking.

This is the right default for clinical data, where an admission followed by a discharge delivered in the wrong order is worse than both being late. It also means one poisoned message can hold up a queue, which is why queue depth is worth alerting on.

Across destinations there is no ordering guarantee, because they are independent. Two destinations will not necessarily receive the same message at the same time, and a fast one may be several messages ahead of a slow one.

> Ordering across channels is not guaranteed at all. If two feeds must be applied in a particular order relative to each other, that ordering has to be enforced by the receiving system, because nothing in the middle can know which of two independently arriving messages happened first.

## Concurrency

A source may handle several connections at once, bounded by `max_connections`. Messages from different connections are processed concurrently.

This means the arrival order of two messages sent simultaneously down two connections is not defined, and cannot be — they genuinely arrived at the same time. A sender that needs ordering has to use one connection, which is what almost all of them do.
