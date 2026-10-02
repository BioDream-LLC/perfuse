# Filtering

A filter decides which messages a channel accepts. It is one expression on the channel, evaluated after the message has been parsed and recorded, before any transformation.

```yaml
filter: MSH-9.1 == "ADT" && MSH-9.2 in ("A01", "A03", "A08")
```

A message the filter rejects is recorded with the outcome **filtered**. That is a normal result, not an error, and it does not raise an alert or produce a negative acknowledgement.

## Filtered is not failed

This distinction is worth being firm about because conflating them is how a real problem gets hidden.

A filtered message was correctly declined: it was not for this channel. A failed message was one this channel should have handled and could not. If both were recorded the same way, a channel that starts failing every message would look exactly like a channel that is filtering most of its traffic, which is the normal state of many channels.

So the statistics count them separately, the alert rules treat them differently, and the interface shows them in different colours. A rise in filtered messages might mean the sender has started sending something new; a rise in failures means something is broken.

## What a filter can read

Any field in the parsed message, by the same paths transformations use. The filter runs before transformations, so it sees the message as it arrived — not as it will be sent.

That ordering is deliberate. A filter that read post-transformation values would be deciding whether to accept a message based on changes made on the assumption it was accepted, which is circular and produces channels whose behaviour depends on the order of two things that look independent.

## Expressions

Comparisons: `==`, `!=`, and for numbers `<`, `<=`, `>`, `>=`.

Membership: `in ("A01", "A03")`, which is clearer than a chain of `||` and is the form the builder produces.

Combination: `&&`, `||`, and parentheses. Negation with `!`.

Absence: a path that is not present compares unequal to any value, including the empty string. To test for absence explicitly, compare against `absent`. This matters because an absent field and an empty field are different, as described in [transformation](#transformation), and a filter that treats them the same will accept messages it was written to exclude.

## An error in the filter is a refusal

If the filter itself fails to evaluate — a malformed expression that got past validation, or a comparison that cannot be made — the message is refused and the error is recorded against it.

Refusing rather than accepting is the safer of the two, but neither is good, and the outcome is recorded distinctly from an ordinary filter refusal so it can be alerted on. A filter that is erroring on every message is silently dropping the entire feed, and it must not look like a filter that is working.

## Filter at the source where you can

A filter runs after the message has crossed the network and been parsed. Some sources can filter earlier and more cheaply:

- A broker source takes a `selector`, which filters at the broker. Messages this channel does not want never cross the network at all. On a shared queue that is the difference between reading a hundred messages a day and a hundred thousand.
- A database source's query decides what it returns.
- A file source's pattern decides which files it picks up.

Filtering at the source is a performance decision, not a correctness one, and it has one cost: a message filtered at the broker is never recorded by Perfuse, so it does not appear in the message store and cannot be inspected later. If you need to be able to prove what the sender sent, filter here rather than there.

## Testing a filter

A filter can be checked against real historical traffic before it is saved. The interface reports how many of the last N recorded messages the filter would accept, reject, and error on — which is the fastest way to find out that an expression which looks right accepts nothing.

The trace also shows, per message, which fields the filter read and what values it found. A filter that unexpectedly rejects everything is usually reading a path that does not exist in this feed, and the trace names it.
