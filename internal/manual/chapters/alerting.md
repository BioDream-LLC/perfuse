# Alerting

Nothing outside Perfuse will tell you a feed has stopped. The sender is being acknowledged and is satisfied; the receiver has no way to know a message was sent. Alerting is the only thing standing between a broken interface and somebody noticing weeks later.

## The alert kinds

| Kind | Fires when |
|---|---|
| `error-rate` | Failures exceed a proportion of traffic. |
| `queue-depth` | A queue has more than N messages waiting. |
| `queue-age` | The oldest queued message is older than a duration. |
| `queue-stuck` | A queue is not draining at all. |
| `no-traffic` | Nothing has arrived for a fixed period. |
| `below-rhythm` | Traffic is well below what this feed normally does *at this time of week*. |
| `channel-down` | A channel that should be running is not. |
| `script-errors` | A script is failing. |
| `rows-quarantined` | A database source is setting rows aside. |
| `slow-delivery` | Deliveries are succeeding but taking too long. |
| `contract` | A message violates the channel's declared contract. |

## Silence is the hardest thing to detect

A channel that has stopped receiving produces no errors. There is nothing to count. This is why `no-traffic` exists, and why it is not good enough on its own.

`no-traffic` is a fixed threshold: alert if nothing has arrived for, say, two hours. On a feed that genuinely never goes quiet, that works. On a clinic's laboratory feed it is useless — the feed is silent every night and all weekend, so a two-hour threshold pages somebody at 1am every night, and a threshold long enough to survive the weekend will not notice a Tuesday morning outage until Tuesday afternoon.

There is no single number that is both. That is not a tuning problem; it is that the question "has this feed stopped" cannot be answered by a constant when the feed's normal varies by a factor of a hundred across the week.

## Rhythm-aware detection

`below-rhythm` compares current traffic against what this channel normally does **in this hour of this day of the week**.

Perfuse learns each channel's week from its own recorded history: 168 hourly buckets, Monday first so a working week is contiguous. For each bucket it holds a median, a low and a high, a floor, and how many observations it is based on.

The threshold is a fraction, defaulting to `0.8` — alert when traffic falls more than eighty per cent below normal for this hour.

### Median, not mean

A single backlog flush of forty thousand messages against a normal three hundred moves a mean far enough that the feed can go completely silent afterwards without ever falling below it. The median is unmoved by one extraordinary hour, which is exactly the property needed.

### The floor is a percentile, not the minimum

Whether a channel's rhythm is reliable enough to alert on is decided by its low end, not its high end. A shortfall alert fires when traffic is *low*, so only the low end can tell you whether it will produce false positives. An early version of this compared the high against the median and got it wrong in both directions at once: it disqualified a perfectly steady feed for a month after one backlog, and it passed a genuinely erratic feed that swung between two and nine hundred messages in the same hour because its high was only three times its median.

The floor is the twentieth percentile rather than the minimum, because the minimum is dragged to zero by a single bank holiday. And the percentile is not interpolated — every number in an alert at two in the morning should be a count the feed actually produced, not an average of two counts it did not.

### It will say when it cannot tell

A channel with no history at all is omitted entirely rather than treated as one that has stopped. A new channel must not look like a broken one.

Where the history is too thin or too erratic to support a judgement, the rule declines to fire rather than guessing. "I cannot honestly say" is a first-class answer here, and it is preferred to an alert that is right slightly more often than chance.

> Absent hours inside the learning window are recorded as real zeroes, not as missing data. Half the signal in a clinic feed is *when it is legitimately quiet*, and treating those hours as unknown would throw it away.

## Setting alerts up

From the interface, per channel or across all of them. Each rule has a kind, a threshold and where to send the alert.

Worth doing before a feed goes live rather than after the first incident. The specific set worth having on any clinical feed:

1. `below-rhythm` — the feed has gone quiet when it should not be quiet.
2. `error-rate` — messages are arriving and failing.
3. `queue-age` — something is stuck rather than merely busy.
4. `channel-down` — the channel is not running at all.

The first is the one that catches the failure nobody else will report.

## Where alerts go

Alerts can be delivered by email, by HTTP to whatever you already use for on-call, or written to the log for a collector to pick up.

An alert that is only written to a log nobody reads is not alerting. If Perfuse is the only thing that will notice a stopped feed, the alert has to reach a person.
