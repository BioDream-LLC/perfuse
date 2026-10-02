# Acknowledgements

An acknowledgement tells the sender what happened. For HL7 v2 over MLLP it is an ACK message; the sender will not send the next message until it arrives, and will usually resend if it does not.

Getting acknowledgements wrong is the most common way an integration silently loses data, because a sender that is being acknowledged has no reason to complain.

## Timing

Covered in [message flow](#message-flow), and the summary is: `ack.when` is `on_delivery` by default, meaning the acknowledgement is sent only after every enabled destination accepted the message. `on_receipt` sends it as soon as the message is parsed and queued.

## What Perfuse sends

An `AA` when the message was accepted, an `AE` when it was rejected for a reason the sender could act on, and an `AR` when it was rejected for a reason the sender cannot.

The distinction between `AE` and `AR` is who has to do something. `AE` means the message had a problem — a field that would not parse as a date, a required segment missing — and the sender can correct it and resend. `AR` means Perfuse could not accept it regardless of content, which usually means a configuration or availability problem at this end. A sender that resends after an `AR` will get the same answer.

## Identifying this engine

`ack.application` and `ack.facility` populate MSH-3 and MSH-4 of the acknowledgement.

When both are empty the acknowledgement mirrors the original receiver — it uses whatever the incoming message had in MSH-5 and MSH-6. That is what most senders expect, and it is the default for that reason.

Set them explicitly when the sender validates them, or when several Perfuse instances handle the same feed and you need to know which one answered.

## The trigger event

`ack.include_trigger_event` sends `ACK^A01` rather than a bare `ACK`.

Some receivers require it and others reject it, which is why it is an explicit option with no clever default. If a sender is refusing your acknowledgements and the content looks right, this is the first thing to try.

## Negative acknowledgements are not failures to hide

A `NACK` treated as a success is one of the most damaging misconfigurations possible, and it is common enough to be worth naming. It happens when a destination sends back a negative acknowledgement and the sending side records the *transmission* as successful because the bytes went out and something came back.

Perfuse reads the acknowledgement it receives from an MLLP destination and records the outcome accordingly. An `AE` or `AR` from a downstream system is a delivery failure, is retried according to the destination's policy, and counts towards the failure rate that alerts fire on.

> If you are migrating from another engine, this is worth verifying explicitly on your own traffic rather than assuming. The [parity checker](#migration-from-mirth) will show it: a destination that the old engine recorded as delivered and Perfuse records as failed usually means the old engine was ignoring a NACK.

## Timeouts

Waiting for an acknowledgement is bounded. When it does not arrive in time the delivery is a failure and is queued for retry.

A timeout is not the same as a rejection and is recorded differently. A rejection means the far side considered the message and declined it; a timeout means you do not know whether it was processed. That distinction decides whether a retry is safe: retrying after a rejection is pointless, and retrying after a timeout risks a duplicate. Perfuse retries after a timeout, because a missing clinical message is generally worse than a duplicate one, and the receiving system is generally able to detect a repeated message control ID.

Whether that trade is right for a given feed is a judgement, and it is worth making deliberately for anything where a duplicate has a clinical consequence — an order, a medication administration — rather than accepting the default silently.

## Sources that do not acknowledge

Not every transport has the concept. A file source has nowhere to send an acknowledgement; a broker source acknowledges to the broker rather than to the original sender; an HTTP source answers with a status code.

Where the source cannot deliver the acknowledgement mode a channel asks for, the channel is refused at load rather than starting and silently not acknowledging. A channel that believes it is acknowledging and is not is worse than one that does not start.

## X12 acknowledgements

Everything above is about HL7 v2. X12 has its own acknowledgements, and unlike v2 they are **off by default**: an empty setting means none.

That default is the safe one, because a trading partner who is not expecting an acknowledgement and receives one may treat it as an unsolicited interchange. The `acknowledge` key on an X12 channel takes one of four values:

| Value | What is sent |
| --- | --- |
| `none` | Nothing. The default. |
| `999` | Implementation acknowledgement. Supersedes the `997` for HIPAA transactions. |
| `997` | Functional acknowledgement. Older, and still what many payer connections expect. |
| `ta1` | Interchange acknowledgement, about the envelope rather than its contents. |

Which one to send is a property of the trading partner relationship rather than of the message, which is why it has to be configured and cannot be inferred. A `999` supersedes a `997` for HIPAA transactions, but plenty of partners — older payer connections especially — are set up to expect a `997` and will treat a `999` as an unrecognised file.

> Sending the wrong acknowledgement is worse than sending none. It answers a question nobody asked while leaving the real one open, and the sender has no way to tell the difference.

## HL7 v3 acknowledgements

An HL7 v3 channel sends an acknowledgement by default, which is the opposite of X12 and worth knowing.

The reason is the transport rather than the standard: a v3 interaction generally arrives over a synchronous connection with the sending application waiting on a reply, and a sender that receives nothing will usually retry. That is how a patient gets registered three times.

Turning it off is possible and has to be done explicitly — "not set" and "set to false" are deliberately different, so the default stands unless somebody said otherwise.
