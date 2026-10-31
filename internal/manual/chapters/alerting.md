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
| `phi-volume` | A channel carries several times its usual rate for the hour. |
| `phi-after-hours` | A channel that only carries daytime traffic carries some at night. |
| `phi-new-destination` | A channel delivers to an address it has not sent to before. |
| `phi-bulk-reads` | One person reads more than N different patients' records within a minute. |

The last four watch access to patient data, and are described below.

## Silence is the hardest thing to detect

A channel that has stopped receiving produces no errors. There is nothing to count. This is why `no-traffic` exists, and why it is not good enough on its own.

`no-traffic` is a fixed threshold: alert if nothing has arrived for, say, two hours. On a feed that genuinely never goes quiet, that works. On a clinic's laboratory feed it is useless — the feed is silent every night and all weekend, so a two-hour threshold pages somebody at 1am every night, and a threshold long enough to survive the weekend will not notice a Tuesday morning outage until Tuesday afternoon.

There is no single number that is both. That is not a tuning problem; it is that the question "has this feed stopped" cannot be answered by a constant when the feed's normal varies by a factor of a hundred across the week.

## Rhythm-aware detection

`below-rhythm` compares current traffic against what this channel normally does **in this hour of this day of the week**.

Perfuse learns each channel's week from its own recorded history: 168 hourly buckets, Monday first so a working week is contiguous. For each bucket it holds a median, a low and a high, a floor, and how many observations it is based on.

The threshold is a share of normal, defaulting to `0.8` (80 per cent in the console) — alert when traffic falls below four fifths of what is normal for this hour.

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

From the interface, under Alerts, per channel or across all of them. Each rule has a kind, a threshold and where to send the alert. The rules are saved to `alerts.yaml` beside the database (or the file `-alerts` names) and take effect at once; until the first save the built-in rules apply. The file can also be edited by hand: the change is picked up within thirty seconds, and a file that no longer loads is reported in the log and the rules in force are kept.

Worth doing before a feed goes live rather than after the first incident. The specific set worth having on any clinical feed:

1. `below-rhythm` — the feed has gone quiet when it should not be quiet.
2. `error-rate` — messages are arriving and failing.
3. `queue-age` — something is stuck rather than merely busy.
4. `channel-down` — the channel is not running at all.

The first is the one that catches the failure nobody else will report.

## Watching access to patient data

A breach rarely announces itself. It looks like a feed carrying more than usual, traffic at three in the morning on a channel that only works office hours, a destination nobody set up, or one person opening patient after patient. Perfuse watches for those four, on by default (`serve -watch-access=false` turns it off), and reports what it sees through the alert rules above, so it is configured, acknowledged and delivered like any other alert.

- **Bursts** (`phi-volume`). Each channel's rate is compared with its own history at the same hour of the day, so a busy morning is not news. The threshold is a multiple of the usual rate, three by default. A burst is usually a backlog being released after an outage, and it is also what records being extracted through a feed look like.
- **Night traffic on a daytime channel** (`phi-after-hours`). A channel counts as daytime-only when under 2 per cent of what it has carried fell outside 07:00 to 19:00, in the server's time zone.
- **A new destination** (`phi-new-destination`). The address is what the destination names: a host and port, a URL, a folder. Renaming a destination is not news; pointing one at a new host is. The addresses each channel has sent to are kept in `access-watch.json` beside the database, so a destination changed while the server was down is still reported.
- **Bulk reads** (`phi-bulk-reads`). Counted from the audit trail: messages opened, patients searched for or looked up (PDQm, PIXm, identity search), documents and images read (XDS, MHD, DICOMweb), prior authorizations read, summaries and views run. Reading the same record again does not count twice. The threshold is how many different records in a minute, ten by default, by one account from one address.

Nothing is judged for a channel until it has carried 200 messages, because "unusual" means nothing without a pattern. The pattern is learned from the last fortnight of the message store when the server starts, so a restart does not make every channel new again. Anything seen is held for fifteen minutes, then the alert clears itself.

It only watches and reports. It never holds up a message or refuses a read: these are heuristics, and a feed stopped because a guess was wrong is a clinical problem. Each alert says what was seen and what it could innocently be, because most of them will be innocent.

Administrators see what the monitor has learned on the Alerts screen, under **Watching access to patient data**: each channel's pattern (still learning, daytime only, or day and night), how many destinations it knows, and everything seen in the last fifteen minutes, including what is under a rule's threshold.

## Where alerts go

Alerts can be sent as JSON to a webhook (`-alert-webhook`: Slack, Teams, Mattermost, PagerDuty, Alertmanager or anything that
accepts a POST), sent as SNMPv2 traps to SNMP managers (`-snmp-traps`, see [SNMP](#snmp)), or written to the log for a
collector to pick up. There is no email delivery of its own: point the webhook at a relay if alerts must arrive as email.

An alert that is only written to a log nobody reads is not alerting. If Perfuse is the only thing that will notice a stopped feed, the alert has to reach a person.
