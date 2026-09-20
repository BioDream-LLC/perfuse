# The Message Store

Every message Perfuse receives is recorded, as received, before anything modifies it. So is what was sent to each destination and what happened to it. The store is a SQLite file given by `-db`.

## What is kept

Per message: the raw bytes as they arrived, when they arrived, which channel, the parsed message type and trigger event, and the filter outcome.

Per destination per message: the bytes that were sent, the outcome, the time taken, and any error or negative acknowledgement received.

This is the shape an investigation needs. "What did they send us", "what did we send them" and "what did they say about it" are three different questions and each of them gets asked.

## Searching

By channel, by time range, by outcome, by message type and trigger event, and by field value.

Field search works on the parsed message, so searching for a record number finds it wherever in the message that field lives, rather than matching the digits anywhere in the text. A text search for `12345` in a message containing a quantity of 12345 finds a message that has nothing to do with the patient you are looking for.

## Replay and reprocessing

Because the original is kept, a message can be run through the current configuration again.

**Trace** takes a recorded message and shows what the channel would do with it now, without sending anything. Covered in [debugging](#debugging).

**Reprocess** actually re-runs it, including delivery. This is what fixes a batch of messages that failed because a destination was misconfigured: correct the configuration, select the affected messages, reprocess.

> Reprocessing delivers again. If the messages partly succeeded the first time, reprocessing will produce duplicates at the destinations that already had them. Perfuse records which destinations succeeded, so reprocessing can be limited to the ones that failed — do that rather than reprocessing everything, unless the receiving system deduplicates on message control ID.

## Staleness

A trace of an old message against the current configuration is answering a hypothetical: what *would* happen now. That is usually what you want, but not always — if you are investigating what went wrong last Tuesday, the channel may have changed since.

Perfuse detects this and says so. A trace of a message that arrived before the channel was last edited is marked as such, so the answer is not mistaken for a reconstruction of what actually happened.

## Retention

The store grows. `cleanup.periodDays` controls how long messages are kept.

Two things to weigh. Clinical messages may fall under retention requirements that are longer than anything you would choose for operational reasons, and the store is not the system of record — the receiving system is. Perfuse's copy exists to diagnose and to reprocess, and both of those needs fall off sharply after a few weeks.

Retention also decides how much history the rhythm learner has to work with. See [alerting](#alerting): a retention window shorter than a few weeks leaves it unable to distinguish a quiet Sunday from a stopped feed.

## The store holds clinical data

This is the part of Perfuse that contains patient information, and it should be treated as such: on encrypted storage, backed up as clinical data, and access-controlled. See [security](#security).

The generated test messages described in [testing](#testing) exist partly so that this data does not have to leave the environment in order to reproduce a problem elsewhere.
