# Getting Started

Perfuse is a single binary with an embedded web interface. There is no application server to install, no database to provision and no separate administration console.

## Running it

```
perfuse serve -channels ./channels -db ./perfuse.db -addr 0.0.0.0:8443 \
  -tls-cert ./cert.pem -tls-key ./key.pem
```

`-channels` is a directory of YAML files, one per channel. `-db` is a SQLite file holding recorded messages, users and audit history; it is created if it does not exist.

On first start with no users configured, an administrator account is created and its password written to the log once. That password is not recoverable afterwards.

> Running without `-tls-cert` and `-tls-key` serves plain HTTP. That is acceptable on a loopback address for a first look and is not acceptable anywhere else: the web interface carries credentials and message content, both of which are readable on the wire without TLS. See [security](#security).

## The first channel

A channel needs a name, a source and at least one destination. The smallest useful one receives HL7 over MLLP and writes each message to a file:

```yaml
name: first-feed
dataType: hl7
source:
  type: mllp
  listen: "0.0.0.0:6661"
destinations:
  - name: to-disk
    type: file
    file:
      root: /var/spool/perfuse
      dir: incoming
```

Save that as `channels/first-feed.yaml` and it is picked up without a restart.

Three things about that file are worth noticing, because they are the shape of every channel:

- The source block is flat for MLLP. `listen` sits directly on `source`, not inside a `source.mllp` block. Transports that need more than an address — HTTP, SOAP, DICOM — do have their own sub-block, and those are listed in the [source reference](#source-reference).
- A file destination needs both `root` and `dir`. `root` is the boundary that paths are resolved against and cannot escape; `dir` is where messages land within it.
- There is no transformation and no filter. Both are optional, and a channel with neither simply passes messages through.

## Building a channel from the interface

Everything above can be done from the web interface instead, and for a first channel that is the better route because the form will not let you produce a file that does not load.

The builder offers three ways in:

1. **From a message somebody sent you.** Paste the sample the lab or vendor emailed. Perfuse reads it and proposes a channel, along with three separate lists: what the sample shows, what has been guessed, and what no sample can tell you. This is described in [testing](#testing).
2. **From something that already works.** A set of complete channels that need only their addresses filled in.
3. **From nothing.** A blank form. Available, but it is the slowest of the three and the easiest to get wrong.

## Verifying it works before connecting anything

Two things can be done before the sending system exists.

Send a message by hand from the interface — the message sender accepts pasted text and reports what happened to it, including which destinations accepted it and what each transformation did.

Or generate test traffic. If the channel has handled any messages at all, Perfuse can learn their shape and produce more messages like them, in the same proportions, with none of the original content. See [testing](#testing).

## What to do next

- [Channels](#channels) explains what a channel is and how its parts fit together.
- [Message flow](#message-flow) follows one message from arrival to delivery, which is the fastest way to understand where to put a change.
- [Alerting](#alerting) is worth setting up before a feed goes live rather than after the first incident.
