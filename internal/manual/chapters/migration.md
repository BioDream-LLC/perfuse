# Migration from Mirth

Two things are needed to replace a working engine: the channels have to come across, and somebody has to be able to show that the replacement produces the same output. Perfuse does the first by importing Mirth's own exports, and the second by comparing the two engines on your own traffic.

## Importing channels

Perfuse reads Mirth channel XML exports. Point it at a directory of them and it reports, per channel, whether it translated cleanly, translated with warnings, or could not be read.

The headline is a fraction with its denominator — "37 of 40 ready" — because "37 ready" tells you nothing about what you still have to do.

A channel that could not be **read** is a different problem from one that could not be **translated**. The first means the XML itself did not parse and is usually a truncated or partial export; the second means the channel's content has something without a Perfuse equivalent. The import separates them.

## What translates and what needs a decision

Sources, destinations, filters and the ordinary transformer steps translate directly.

JavaScript transformers do not, in general. Mirth channels commonly hold substantial code in a transformer, and there is no automatic conversion from arbitrary JavaScript to declarative steps. The importer reports these rather than attempting a translation that might be subtly wrong: a mistranslated transformer is worse than one flagged for a human, because it will run.

Where a script genuinely has to remain a script, Perfuse can run it — see the `scripts` key in the [channel reference](#channel-reference) — at the cost of the step-level trace described in [debugging](#debugging).

## What the importer has been tested against

A channel exported by Mirth 4.5.2, authored by Mirth itself rather than written here. That distinction earned its place: the only fixture in this repository used to be hand-written, and a real Mirth **will not load it** — the API accepts the POST and then stores the channel as `This channel is invalid. Verify all required extensions are loaded correctly`, with every destination connector discarded.

Hand-writing one was never going to work, and the reason is worth knowing if you are producing exports of your own. Mirth's serialiser ignores elements it does not recognise, so a wrong element name produces no error whatsoever — the connector simply is not there afterwards. There is nothing to read and nothing to correct against.

`scripts/mirth-author-channel.sh` builds one using Mirth's own model classes and its own serialiser, then posts it to a running server and fails if the server rejects it. Perfuse's importer reads the result: name, source transport and destinations all survive.

## Proving it matches

This is the part that decides whether a migration happens, and it needs evidence rather than confidence.

Perfuse compares its own output against the old engine's, message by message, on your traffic. What it needs is **pairs**: the message as it arrived, and the message the old engine produced from it. Both are in Mirth's own message store and can be exported from its message browser.

> Perfuse cannot reach into Mirth, and this manual will not pretend otherwise. A tool that claimed to verify a migration without ever seeing the old engine's actual output would be verifying nothing. Supplying the pairs is the honest shape of the exercise.

### Four numbers, not a pass rate

| Result | Meaning |
|---|---|
| Identical | Byte for byte the same. |
| Cosmetic only | Differs in ways that carry no information — trailing empty fields, a final terminator. |
| Differ | A field's value is different. |
| Would not run | Perfuse could not process the message at all. |

They are four numbers because they mean four different things and collapsing them hides the ones that need a decision.

Identical means byte for byte, not equivalent. A weaker test would let differences in segment order or trailing separators through, and those are exactly what a fussy downstream system rejects.

Cosmetic differences are counted separately rather than folded into the good column. They genuinely carry no information, so treating them as failures would bury real findings under thousands of nothing — but they are not identical either, and a receiver doing its own strict parsing may disagree.

"Would not run" is separate from "differ" because it is usually a configuration gap the importer could not fill, not a mapping decision. If everything is in that column, the verdict says so and tells you to look at the configuration before reading anything else.

### Differences are grouped by field

A run over fifty thousand messages reporting four thousand differences has told you nothing you can act on. The same run reporting that `PID-8` differs in every message because the old engine wrote `Male` where Perfuse writes `M` has told you exactly one thing to decide.

So each finding is a field, with a count, and it says whether the difference is **systematic** — the same value pair every time — or whether the field's content differs.

That distinction is the most useful thing in the report. One distinct value pair across four thousand messages is a single mapping decision. Four thousand distinct pairs is the content differing, which is a different and much worse problem, and the two must not read alike.

Findings are ordered by how many messages they affect, so the largest single cause is first.

### The verdict does not round

It gives exact counts: 49,993 of 50,000, not "over 99%". The seven are the whole point of running it.

It is written to be quotable in a change request, because that is where the number ends up.

## A suggested sequence

1. Import the channels and resolve anything reported as unreadable.
2. For each channel, review the warnings. Most will be scripts.
3. Export a few thousand message pairs from Mirth for the busiest channel and run the comparison.
4. Work through the findings. Systematic ones are one decision each.
5. Re-run until the only differences are ones you have decided are correct.
6. Run Perfuse in shadow alongside Mirth on live traffic, delivering nowhere, and compare again over a period that includes a weekend and a month end.
7. Cut over one channel, not all of them.

## What only runs on one vendor's software

The migration screen answers a second question, and it is worth asking before you have any opinion about Perfuse: how much of your integration logic can only run on the software you have.

Every channel gets a **Portability** verdict, from the same scan that produces the migration notes. It runs against a channel export, so it needs nothing installed and changes nothing.

### The distinction is the whole point

Most Java in Mirth scripts is not lock-in. `SimpleDateFormat`, `HashMap`, Apache Commons — all of it working around a JavaScript engine from 2009, all of it ordinary, all of it with direct equivalents. A channel with three hundred of those references is fully portable, and a tool that reported "300 Java references, you are trapped" would be lying to you.

Lock-in is specifically the calls that need the vendor's own server: `com.mirth.connect.*` and vendor jars. Five of those and you are stuck. Five hundred of the other kind and you are free.

So the verdict leads with the number that matters and says in the same sentence that the larger number is not it. Where there are vendor calls, they are listed by name — a count is an assertion, and a list is something your own engineer can go and check.

A third category is kept separate: capability that exists but not from a script. Starting a channel from JavaScript has no equivalent here, and there is an API endpoint that does exactly the same thing. Calling that lock-in would overstate the finding.

### External scripts are reported as unknown

A step that runs an external script file references a path on the old server, and the file is not in the export. Nothing can be said about what it contains, so it is counted as unknown rather than assumed either way. Fetch those files before concluding anything — in both directions.

### Perfuse's own lock-in, stated in the same breath

This section would be dishonest without it.

Perfuse is Apache 2.0, which is irrevocable for code already released. You can fork what exists today and nobody can withdraw that. Channels are YAML files and scripts are ordinary JavaScript, so there is no proprietary surface to call — Mirth's flavour of Java dependency is structurally unavailable here, which is the one place a limitation is genuinely a feature.

What that does not promise: that a future version stays open, that a hosted service exists at a price you like, or that migrating away from Perfuse costs nothing. Moving any integration layer means re-testing every channel against real traffic. What you are choosing is whether the artefacts you build are readable and portable, and whether the exit depends on a licence somebody else controls.

### Setting up step 6

```svg
<svg viewBox="0 0 760 214" xmlns="http://www.w3.org/2000/svg" role="img"
     aria-label="Shadow mode: one incoming message is copied to both the live channel, which delivers, and a candidate channel, which delivers nothing and is only compared field by field against the live result.">
  <defs>
    <marker id="s-arrow" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto-start-reverse">
      <path d="M0 0 L10 5 L0 10 z" fill="#64748b"/>
    </marker>
    <marker id="s-stop" viewBox="0 0 10 10" refX="5" refY="5" markerWidth="8" markerHeight="8" orient="auto">
      <circle cx="5" cy="5" r="4" fill="none" stroke="#ef4444" stroke-width="1.6"/>
      <path d="M2.5 2.5 L7.5 7.5" stroke="#ef4444" stroke-width="1.6"/>
    </marker>
    <style>
      .s-box  { fill:#f1f5f9; stroke:#94a3b8; stroke-width:1.2; }
      .s-live { fill:#ecfdf5; stroke:#10b981; stroke-width:1.4; }
      .s-cand { fill:#eef2ff; stroke:#6366f1; stroke-width:1.4; stroke-dasharray:5 3; }
      .s-cmp  { fill:#fffbeb; stroke:#f59e0b; stroke-width:1.2; }
      .s-t    { font:600 12px system-ui,sans-serif; fill:#0f172a; }
      .s-s    { font:11px system-ui,sans-serif; fill:#475569; }
      .s-l    { stroke:#64748b; stroke-width:1.4; fill:none; marker-end:url(#s-arrow); }
      .s-x    { stroke:#ef4444; stroke-width:1.4; fill:none; stroke-dasharray:5 3; marker-end:url(#s-stop); }
    </style>
  </defs>

  <rect class="s-box" x="8" y="66" width="96" height="42" rx="7"/>
  <text class="s-t" x="56" y="84" text-anchor="middle">One message</text>
  <text class="s-s" x="56" y="99" text-anchor="middle">real traffic</text>

  <rect class="s-live" x="188" y="18" width="150" height="44" rx="7"/>
  <text class="s-t" x="263" y="38" text-anchor="middle">Live channel</text>
  <text class="s-s" x="263" y="53" text-anchor="middle">unchanged</text>

  <rect class="s-cand" x="188" y="112" width="150" height="44" rx="7"/>
  <text class="s-t" x="263" y="132" text-anchor="middle">Candidate</text>
  <text class="s-s" x="263" y="147" text-anchor="middle">the version you are testing</text>

  <rect class="s-box" x="426" y="18" width="128" height="44" rx="7"/>
  <text class="s-t" x="490" y="44" text-anchor="middle">Receiver</text>

  <text class="s-s" x="430" y="140">delivers nothing</text>

  <rect class="s-cmp" x="596" y="60" width="150" height="56" rx="7"/>
  <text class="s-t" x="671" y="82" text-anchor="middle">Comparison</text>
  <text class="s-s" x="671" y="99" text-anchor="middle">field by field</text>

  <g class="s-l">
    <path d="M104 78 V40 H184"/>
    <path d="M104 96 V134 H184"/>
    <path d="M338 40 H422"/>
  </g>

  <!-- The candidate's output going nowhere is the whole point, so it is drawn as a stopped line. -->
  <path class="s-x" d="M338 134 H418"/>

  <g class="s-l">
    <path d="M554 40 H575 V78 H592"/>
    <path d="M338 128 H560 V96 H592"/>
  </g>

  <text class="s-s" x="8" y="180">The candidate sees the same real messages as the live channel and delivers none of them. Only the live channel reaches the receiver.</text>
  <text class="s-s" x="8" y="198">What you read afterwards is where the two disagreed, field by field — not a pass or a fail.</text>
</svg>
```


The **Shadow** section starts a comparison. Choose the channel that is running, choose the candidate to compare against from the list, and start.

Three things about it are worth knowing before you rely on the numbers.

**The candidate is chosen from a list, not typed.** A shadow names a channel file, and a name that does not resolve makes the *live* channel invalid — Perfuse refuses a channel wholesale when anything it references is broken. So a typo does not produce a warning, it makes the running channel disappear.

**The share is a percentage.** Lower it only on a busy feed, and know what it costs: sampling reduces work and confidence in the same proportion, and the message that would have shown the difference is the unusual one.

**Fields to ignore is usually needed.** A channel that stamps a timestamp or a sequence number differs on every single message, which reports a difference rate of 100% and tells you nothing. `MSH-7` and `MSH-10` are the common pair.

A comparison is written to the channel file as soon as you start it, and begins observing when the channel next loads — the screen says so rather than claiming it is already running. Stopping a comparison leaves the candidate channel in place, because the candidate is the thing meant to go live.

Step 6 is the one that cannot be shortened. The differences that matter are usually in traffic that only appears occasionally — a rare trigger event, a month-end batch, a sender that behaves differently at midnight — and a sample taken over an afternoon will not contain them.

## Going back the other way

A channel can be exported as a Mirth channel file, from the browser: **Channels**, then **To Mirth** on the channel you want.

This exists because the objection to replacing a working engine is rarely whether the replacement is any good. It is what happens if the decision turns out to be wrong, and an export that goes back the way it came is the cheapest answer to that.

**Read what the dialogue tells you before you take the file.** Mirth's format has nowhere to put a Perfuse filter, a transformation chain, a contract or a shadow comparison, so an exported channel does less than the one it came from. The transports and their addresses convert; the logic does not. Each loss is listed by name before the file is offered, and the same list goes into the channel's description so that whoever imports it sees it too.

A channel Mirth cannot express is refused rather than approximated. An S3 destination has no Mirth equivalent, and a channel that imported cleanly while delivering somewhere other than the bucket would be worse than no file at all. The refusal names the destination at fault.

The connector definitions are not written from a reading of Mirth's format. `scripts/mirth-dump-connector-templates.sh` has Mirth's own serialiser produce the defaults for each connector class, and Perfuse substitutes values into those documents — refusing any setting the template does not already contain. This matters because of how Mirth answers a channel it cannot parse: it does not refuse the import. It stores a channel whose description has been replaced with "This channel is invalid. Verify all required extensions are loaded correctly" and whose destinations have been silently discarded. An exporter checked against its own output rather than against a server passes every test while producing documents that are thrown away, which is exactly what happened here before the exports were tried against a running Mirth.

What crosses today: MLLP and TCP in both directions, HTTP in both directions, file reading and writing, database reading and writing, DICOM in both directions, and SMTP, SOAP and JavaScript destinations. Each pair is verified by importing into a real Mirth 4.5.2 and checking the server did not quietly replace the channel.
