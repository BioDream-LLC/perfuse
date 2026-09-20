# Channels

A channel is one flow of messages: somewhere they come from, optional changes, and one or more places they go. It is the unit of configuration, the unit of monitoring and the unit of deployment. Everything else in Perfuse exists to serve channels.

## Anatomy

A channel file has these parts, in the order they take effect:

| Part | Key | Required | What it does |
|---|---|---|---|
| Identity | `name` | yes | Names the channel. Must be unique. |
| Data type | `dataType` | no | How to parse messages. Defaults to HL7 v2. |
| Source | `source` | yes | Where messages arrive from. Exactly one. |
| Filter | `filter` | no | Decides which messages to accept. |
| Transformations | `transformations` | no | Ordered changes to the message. |
| Destinations | `destinations` | yes | Where messages go. One or more. |

Plus several that apply across the whole channel: `scripts`, `shadow`, `contract`, `tables`, `attachments`, `group` and `enabled`. Each is in the [channel reference](#channel-reference).

## One source, many destinations

This asymmetry is deliberate and it is worth understanding early, because it is the single most common source of confusion when coming from a different engine.

A channel has exactly one source. If messages arrive from two places, that is two channels. Trying to express it as one channel produces a configuration where a failure in either transport looks the same from outside, and where the two feeds' traffic is mixed together in the statistics — so a drop in one is hidden by the other.

A channel has any number of destinations, and they are independent. One failing does not stop the others, and each has its own queue, its own retry policy and its own delivery record. A message that reached three of four destinations is recorded as exactly that, not as a success or a failure.

## Destinations are not a chain

Each destination receives the message as it stood after the channel's transformations. A destination does not receive the output of the destination before it.

This matters when a destination has its own transformations, which it may. Those apply to that destination's copy only. A common mistake is to put a change on the first destination and expect the second to see it.

If you genuinely need a chain — the output of one flow feeding into another — use a channel destination, which sends to another channel by name. That is explicit, appears in the message history as two channels, and can be monitored as two things, which a hidden chain cannot.

## Naming

The name is the channel's identity everywhere: in the file, in the message store, in alerts, in the audit log and in the URL. Changing it is not a rename; it is a new channel, and the old one's history stays under the old name.

`group` is separate and exists for organising the interface. A group has no effect on behaviour, and grouping channels does not make them share anything.

> On a multi-facility deployment, put the facility in the name rather than only in the group. Mirth deployments across several sites reliably drift into inconsistent naming, and the point at which that becomes painful is an incident at two in the morning where the channel list has four things called `adt-inbound`.

## Enabling and disabling

`enabled: false` stops a channel starting without deleting it. Its configuration, history and statistics stay.

A disabled channel is not a paused one. Its source is not listening, so a sender attempting to connect gets a refused connection rather than a hung one. For MLLP that is the right behaviour — the sender will queue and retry — but it does mean the sender's own error log will fill up, so a disabled channel is not a substitute for arranging an outage with whoever is sending.

## Where channels live

One YAML file per channel, in the directory given by `-channels`. The filename is not the channel name; the `name` key is. Keeping them the same is strongly advised and nothing enforces it.

Files are re-read without a restart. A file that does not parse is reported and the previously loaded version keeps running, so a syntax error cannot take a working feed off the air.

> A channel edited in the web interface is written back to its file. If those files are in version control — which is recommended — expect commits from configuration changes made through the interface, and do not hand-edit a file at the same time as somebody has it open in the builder.

## Encrypting a channel's own listener

A channel that listens on a port is its own server, and its TLS is separate from the TLS on Perfuse's web interface. Setting one does not set the other. For an HTTP source the settings are under **TLS and limits** on the source.

Switching TLS on asks for a certificate and a private key. Requiring a client certificate turns it into mutual TLS: the sender must present one signed by an authority you name, which is authentication, so a listener with it on does not also need a shared token — though having both is not wrong.

The authority is asked for only once a client certificate is demanded, and is left out of the file until then. A channel naming an authority that is never consulted reads as though senders are being verified when they are not.

An MLLP listener has the same settings under **Encrypt this listener**, with one difference worth stating: on MLLP a client certificate is the only authentication available. There is no token and no header, so a listener without one accepts messages from any host that can reach the port.

Two limits sit alongside it. **Give up reading after** a duration, because a sender that opens a connection and stops writing otherwise holds it open. And **largest message accepted**, in bytes, because zero means no limit and the limit is then memory — a sender that posts a gigabyte is refused rather than absorbed.

## Validation

A channel is checked when it loads, and the checks are refusals rather than warnings where the alternative would be a channel that runs and does the wrong thing.

Examples of things refused outright: a destination with no type, a transformation addressing a path that cannot exist in the declared data type, an acknowledgement mode that the source transport cannot deliver, and a Schedule II prescription with refills.

The distinction Perfuse tries to hold to is that anything which would produce a plausible-looking wrong result is refused, and anything which is merely unusual is allowed and reported. A channel that cannot be wrong is more valuable than a channel that starts.
