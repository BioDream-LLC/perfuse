# Message Formats

A channel's `dataType` decides how bytes become a message. It affects parsing, what paths mean, how the message is written back out, and which transformations are valid.

| Value | Format |
|---|---|
| `hl7` | HL7 v2, the pipe-delimited standard. The default. |
| `hl7v3` | HL7 v3 and CDA documents, which are XML. |
| `x12` | ASC X12, used for eligibility and claims. |
| `ncpdp` | NCPDP Telecommunication Standard D.0 pharmacy claims. |
| `script` | NCPDP SCRIPT, electronic prescriptions. XML. |
| `dicom` | DICOM imaging objects. |
| `delimited` | CSV and other delimited text. |
| `raw` | No parsing. Bytes in, bytes out. |

## HL7 v2

The default, and what most of this manual's examples use. Segments terminated by carriage return, fields by `|`, components by `^`, repetitions by `~`.

Two things about the encoding are worth knowing because they cause silent faults.

The delimiters are not fixed. They are declared in MSH-1 and MSH-2 of each message, and Perfuse reads them from there rather than assuming pipes. A sender that uses a different field separator parses correctly.

The consequence is that a message whose separators have been replaced — most often by a word processor or mail client substituting typographic look-alikes — parses perfectly into fields that are all wrong. Nothing errors. This is why the sample reader described in [testing](#testing) reports which separator character it found, and refuses to express confidence when it is unusual.

> When somebody sends you a sample and the channel built from it produces nonsense, check the separators before anything else. Ask for a plain text attachment rather than a message pasted into an email body.

## HL7 v3 and CDA

XML rather than delimited. Paths are element and attribute paths rather than field positions.

In v3 a value is almost always an attribute rather than element text. A comparison that only reads element text will report two documents as identical when the birth date, the gender and every identifier differ. Perfuse's comparison reads both, and this is why: the alternative silently succeeds.

`hl7v3` options are in the [format reference](#format-reference).

## X12

Segments terminated by `~` conventionally, with delimiters declared in the ISA segment. Like HL7, they are read from the message rather than assumed.

X12's structure is envelopes within envelopes — interchange, functional group, transaction set — and a single interchange can hold many transactions.

By default an interchange is **one message**. The whole file gets one record, one outcome and one row in the interface, however many transactions it contains.

### Splitting an interchange

Setting `split` makes each transaction set a message in its own right, with its own record, its own metrics and its own row. An operator looking for one claim then finds one claim rather than a file of four hundred.

It is off by default because turning it on multiplies everything downstream — message counts, rows, alert thresholds — and a threshold tuned for files that is suddenly counting claims will not mean what it used to. That should be a decision somebody made rather than a default they inherited.

> `split` and `acknowledge` cannot both be set, and a channel with both is refused at load. An acknowledgement is a statement about a whole interchange, so splitting would send one per transaction set, and a partner receiving several `999`s naming the same functional group has no way to reconcile them.

### When the counts do not add up

X12 declares its own counts — how many segments, how many transactions — and the `envelope` key says what to do when they disagree with what is actually present. Those counts are the only mechanism X12 has for detecting that half a claims file arrived.

| Value | Behaviour |
| --- | --- |
| `require` | Refuse the interchange. The default. |
| `warn` | Process it, and record the fault against the message. |
| `ignore` | Do not check at all. |

`warn` exists for a real situation: some partners' software has generated wrong counts for years in otherwise complete files, and a site that has to process those needs a way to say so out loud in the configuration rather than turning validation off wholesale.

> `ignore` is named to be uncomfortable. Setting it throws away the only means X12 gives you of noticing a truncated file, and a truncated claims file is indistinguishable from a small one.

### Transactions Perfuse reads as structured data

Any X12 interchange can be routed, filtered and addressed by path. These transactions are additionally parsed into named fields, so a channel can ask what a payer decided rather than which element sits at position four.

| Transaction | What it carries |
| --- | --- |
| `835` | Remittance advice: what was paid, what was denied, and the adjustment reasons. |
| `275` | Claims attachments: clinical documentation supporting a claim. |
| `277` | Claim status, including a payer's request for additional documentation. |
| `278` | Prior authorisation: the request for review, and the payer's decision. |
| `999` | Functional acknowledgement, which Perfuse also generates. |

| `271` | Eligibility: whether a patient is covered, and their co-payment, coinsurance, deductible and out-of-pocket amounts, in and out of network. |
| `834` | Enrolment: members added, changed and terminated, with each coverage line and its dates. |
| `837` | Claims, professional and institutional (read for CMS-0057's CARIN conversion). |

Perfuse also **builds** the `270` eligibility inquiry and the `276` claim status request, under **Exchange → Claims & auth** or through
`POST /api/x12/eligibility/build` and `/api/x12/claimstatus/build`. Both are the HIPAA 5010 versions (005010X279A1, 005010X212), read
back and envelope-checked before they are shown, and neither is sent from that page: delivering one is a channel's job, where the
trading partner and its credentials are.

### Eligibility, and the CAQH CORE data content rule

A 271 is a list of EB segments, each a single fact: active coverage, or a $25 co-payment for service type 30 in network, or $800 of
the individual deductible remaining. The reader assembles them into the answer a front desk wants - "coverage active under Gold PPO
2000; co-payment in network $25; individual deductible remaining in network $800" - and names a rejection (an AAA segment) in words,
with what the payer says to do next.

It also checks the response against the CAQH CORE Eligibility & Benefits data content rule: for an inquiry about health benefit plan
coverage (service type 30), an active response should carry plan dates, the plan name, co-payment, coinsurance, deductible and remaining
deductible both in and out of network, and the base service types (1, 33, 35, 47, 48, 50, 86, 88, 98, AL, MH, UC). Each is listed as
met or missing. This is Perfuse's reading of the rule, useful for "why does this payer never tell us the deductible?" - it is not CORE
certification, which CAQH's authorised testing vendors carry out.


### Prior authorisation

Perfuse implements prior authorisation twice, on purpose.

The **FHIR** side is what CMS-0057-F requires payers to expose by 1 January 2027. The **`278`** is what payers accept today, and the rule does not replace it — Da Vinci PAS is in practice a mapping onto this transaction, because a `278` is what a utilisation management system speaks. An engine reading only the FHIR half can talk to the systems that will exist and not to the ones that do.

A `278` response carries the **authorisation number**, and that is the field the whole transaction exists to deliver. Without it a certified service is not billable: the claim goes in with no authorisation number and is denied for the lack of one, which reads downstream as a clinical denial.

#### A denial and a refusal to consider are not the same thing

This is the most expensive confusion in the transaction, and Perfuse reports the two separately.

| What arrived | What it means | What to do |
| --- | --- | --- |
| `HCR` with action `A3` | The payer considered the request and will not certify it. | Appeal, or change the plan of care. |
| An `AAA` segment | The payer would not consider it — patient not found, requester not recognised, review not covered. **Nothing was decided.** | Correct the request and send it again. |

Both read as "not approved" to anybody scanning, and the correct actions are opposite. Appealing something nobody ruled on achieves nothing; resubmitting a denial produces the same denial. The parsed outcome names which happened — `denied` against `not-considered` — and `AAA04` carries the payer's own advice on whether the request is recoverable at all.

Partial certification is reported as `partially-certified` rather than folded into approval. There is an authorisation number to bill against, so it counts as approved, but the quantity certified is not the quantity requested — and a practice booking six sessions against three approved discovers it at the fourth claim.

An action code Perfuse does not recognise is reported as `unknown`, not as pending. Pending reads as "wait", and waiting is the wrong action for most of what an unrecognised code could be.

A channel can route on the decision directly:

```yaml
filter: HCR-1 == "A3"
```

#### Converting between the two

Perfuse converts a parsed `278` into a Da Vinci PAS `ClaimResponse`, and reports every judgement the conversion needed rather than only the result. The conversion follows three rules:

- **No decision is upgraded.** Partial certification stays partial, and a request the payer refused to consider does not become a denial. Both collapses are available in the obvious mapping and both are wrong in the expensive direction.
- **Nothing is invented to fill a required field.** A certified event with no authorisation number is reported as such, not given a plausible one — a fabricated number produces a claim that is denied later for a reason nobody can trace.
- **A decision carried only at line level is used.** A payer answering per service can leave the event without a decision of its own; reading only the event level would report that as pending, and a practice waits for an answer it already has while the approval expires. Where the lines disagree, the least favourable wins, because an event with a refused line is not an approval.

Narrowing to a three-state vocabulary — approved, denied, pended — is available and explicit about what it discards. Narrowing a refusal to consider yields "denied" and says in words that the correct action is a corrected resubmission rather than an appeal.

#### Checked against PAS 2.2.1, not only against itself

The `ClaimResponse` is shaped to the Da Vinci PAS 2.2.1 profile and was checked with the official HL7 validator loaded with the published PAS package. The first version had only been checked by Perfuse's own tests, and the validator rejected it with six to eight errors per resource. Every one of them is something a payer's PAS endpoint would also have rejected:

| What was wrong | Where PAS puts it |
| --- | --- |
| The decision was in `adjudication.category` and `reason`. | The `reviewAction` extension, on an adjudication whose category is `submitted`. |
| The reason code named X12 element 1034. | X12 external code list 886. |
| A pended request answered `outcome: queued`. | `outcome` is about processing and PAS allows only `complete`, `error` and `partial`. A pend is `complete`, with review action `A4`. |
| `processNote.type` was a CodeableConcept. | R4 has a code: `print`. |
| `patient`, `created`, `insurer` and `request` were missing. | All four are required. They are given as identifier references, because the response stands alone rather than in a bundle. |

The corrected output validates with no errors for a certification, a denial and a pend. The warnings that remain are the validator saying it cannot load X12's code systems, which X12 licenses. The authorisation number is carried in `reviewAction.number`, where the guide's own example puts it, and in `preAuthRef` when the response has only one.

The validator is not part of `make check`, because it needs Java and a download of the guide and its dependencies. What it found is held by a test instead: each rule it enforced is asserted, so a change that reintroduces one of those errors fails.

The **Claims & auth** view converts a pasted `278` and shows the decisions, the notes and the resource.

### Attachments, and why they need special handling

CMS-0053-F makes the `275` a HIPAA standard for claims attachments, with compliance required by 26 May 2028. It carries a document — an operative note, an imaging report, a scanned form — tied to the claim it supports.

The `277` is the other half of the same conversation. A payer receives a claim, decides it needs documentation, and sends a `277` asking for it; the provider answers with a `275`.

A channel can route on the status code directly, because `STC01` is a composite and its second component is the status. Written in canonical form, because a component is addressed with a dot and the two notations cannot be mixed — `STC-1.2` is element 1, component 2:

```yaml
filter: STC-1.2 in ("227", "233", "252", "287")
```

Those are the codes that unambiguously mean a document is wanted. The status **category** in `STC-1.1` is deliberately not used on its own: `R4` means a documentation request in some implementation guides and "forwarded elsewhere" in others, so a filter on the category alone would put finalised claims in the queue for somebody to chase.

Two identifiers decide whether an attachment is ever associated with its claim, and both are reported: the **attachment control number**, which the provider put on the original claim to say documentation would follow, and the **trace number**, which matches the payer's request. An attachment arriving with neither is filed and never connected to anything, while the claim ages.

> A document inside a `275` is **not interpreted**. Perfuse reports its bytes and what the sender says they are. An attachment may be a PDF, a TIFF, a CDA document or an HL7 message, and a channel needing the content can hand those bytes to the format that reads it.

#### The 006020 form, which is the one the rule adopts

CMS-0053-F adopts version `006020`, and its `275` differs from the `005010` form in two ways that matter.

- **The payload is in `BDS`, not `BIN`.** `BDS01` names a filter, `ASC` or `B64`. `BDS02` is the length and `BDS03` the data.
- **The payload is a MIME entity**, with headers saying what the document is and a base64 body. With `B64` the entity itself is base64 as well.

Perfuse unwraps the entity and reports the document inside it, with its content type and file name. The bytes it reports are exactly the ones the sender wrapped. An entity that cannot be unwrapped leaves the raw payload in place, with a note saying why.

**Building a 275.** Perfuse also builds `006020X314` attachments: an unsolicited one to accompany a claim, or a response to a payer's `277` request. The difference is `BGN01` `02` against `11`, and whether `TRN02` carries the claim's `PWK06` or the `277`'s `TRN02`. Every built interchange is parsed back before it is returned, and the response says whether the document inside is the one that went in.

The structure comes from the two published companion guides for `006020X314`: the CMS esMD guide and UnitedHealthcare's. It does **not** come from the X12 technical report, which X12 licenses and which this project does not hold.

- Where the two guides agree, Perfuse follows them.
- Where they differ, it satisfies both if it can. `CAT02` is `HL` for an HL7 document, which esMD uses, and `IA` for an image, which UnitedHealthcare uses.
- Otherwise it sends the optional segment only when given the value: esMD's `REF*X1` claim identifier and UnitedHealthcare's `DTP*472` service date.

Two things a trading partner may require are not done:

- There is no validation against the technical report.
- No electronic signature is applied. CMS-0053-F also adopts a standard for signing attachments, and a partner that requires one will reject what this builds.

The partner's companion guide governs.

The provider NPI is checked against its check digit. A payer matches an attachment to its claim partly on that number, and a mistyped one leaves the attachment unmatched while the claim is denied for missing documentation.

The **Claims & auth** view builds a `275` from a pasted C-CDA or an uploaded file, and reads one back. Nothing on that page sends anything: the interchange is handed back to be delivered through a channel, which is where the partner, the credentials and the audit trail belong.

### Binary segments

A `BIN` segment carries raw bytes, and raw bytes contain delimiters. Every other X12 segment ends at the segment terminator, so an ordinary parser finds segments by searching for one byte; an attachment cannot be found that way. A PDF containing a `~` would be cut into pieces, and each piece is a structurally valid segment — so the document is truncated, every segment after it is misaligned, and nothing reports an error.

Perfuse reads the byte count the segment declares and takes exactly that many bytes. Two consequences worth knowing:

- A declared length that does not match the data is **refused**, naming both numbers. The useful question is whether the sender's count was wrong or the file was truncated in transit, and only the two numbers answer it.
- `BDS`, the `006020` binary segment, keeps its count in `BDS02` and its data in `BDS03`. It was refused until that could be confirmed from a published source rather than recalled, and two sources now confirm it: the CMS esMD companion guide's `BDS` table and UnitedHealthcare's guide.

  The esMD guide's own sample interchange declares 16 bytes for the same 53-byte payload that its table correctly counts as 53. Perfuse refuses that sample, as it should. A test holds both the table's example and the sample's typo, so a change in either direction is noticed.

## FHIR

### What an HL7 v2 message becomes

The message type decides what is produced, and every conversion reports the judgements it needed as notes beside the bundle.

| v2 | FHIR |
| --- | --- |
| `ADT` | Patient, and an Encounter when there is a visit. `A28`, `A31`, `A29` and `A24` are about the person, so they produce no Encounter. |
| `ORU` | DiagnosticReport per `OBR`, with its Observations and Specimen. Results are grouped under the `OBR` before them by position, because `OBX-4` is not reliable. |
| `ORM`, `OML`, `OMG` | ServiceRequest. |
| `SIU` | Appointment, with the patient, the practitioners from `AIP` and the locations from `AIL`. |
| `MDM` | DocumentReference carrying the document. |
| `VXU` | Immunization, one per `RXA`. |

Anything else produces the Patient and a warning naming the type that was not mapped.

**Scheduling.** A booking, a reschedule, a cancellation and a no-show all land on **the same Appointment**. Its identity comes from the appointment's own numbers in `SCH-1` and `SCH-2`, never from the message, so a cancellation updates the booking. The alternative would be a second, cancelled appointment beside a live one, which shows the patient booked into a slot that no longer exists.

- **Status.** The filler status in `SCH-25` decides it, and the trigger event is used only when that is empty.
- **Blocked slots.** A blocked slot is not an appointment with a patient, and produces none.
- **Start time.** It is read from `AIS`, `AIG`, `AIL` or `AIP` first, then `SCH-11`, because version 2.5 moved it and feeds did not all move with it.
- **Durations.** A duration in a unit Perfuse does not recognise is left out, not guessed.

**Documents.** Text notes (`TX`, `FT`, `ST`) keep their line breaks, including blank lines, because a blank repetition in a note is a paragraph break.

- **Embedded documents.** An embedded PDF, TIFF or JPEG (`ED`) is carried byte for byte. Base64 that is not valid base64 is refused rather than passed on.
- **Types.** The document type in `TXA-2` is kept as sent and, where there is a clear equivalent, given the LOINC code US Core uses.
- **Signing.** A note nobody has signed is `preliminary`, and `T11` makes the document `entered-in-error`.
- **No content.** A notification with no content (`T01`, `T03` and the other odd-numbered events) produces no DocumentReference. FHIR requires content, and an empty attachment would look like the note while saying nothing.

This is what CMS Interoperability Framework criterion 14 asks for: chart notes and clinical documents returned as FHIR attachments.

**Immunizations.** A dose is never reported as given when the message says otherwise.

- **Refused doses.** A refused or not-administered dose (`RXA-20` `RE` or `NA`) is `not-done`, with the reason from `RXA-18`.
- **Deletions.** A deleted one (`RXA-21` `D`) is `entered-in-error`.
- **Historical records.** A record transcribed from somewhere else (`RXA-9` other than `00`) is not a primary source.
- **Unknown amounts.** An amount of `999` means unknown, not 999.
- **Routes and sites.** Each dose takes the route and site from the `RXR` after it, so a message with two doses does not give the second one the first one's route.

### R4 in both directions

A FHIR server declaring R4 accepts R4. That was not true of Encounters until this release. The server converted what it served to R4 but read what it received as if it were already in its internal form, so an R4 Encounter was refused with a 400. So was every bundle Perfuse's own ADT conversion produced, because each carries `Encounter.class`, which R4 writes as a single coding.

It was found the first time anything posted a converted ADT to Perfuse's own R4 server. A test now does that for every mapped message type, and another checks that everything written as R4 is read back unchanged.

### Subscriptions

With `-fhir-subscriptions`, the FHIR endpoint sends notifications when an Encounter or an Appointment is created or changed. It follows the FHIR Subscriptions R5 Backport IG for R4, which is what CMS Interoperability Framework criterion 15 and TEFCA's FHIR roadmap use for appointment and encounter notifications.

A client creates a `Subscription` naming a topic, and optionally a filter such as `Encounter?patient=Patient/123`. The topics are listed in the capability statement:

| Topic | Fires on | Filters |
| --- | --- | --- |
| `https://perfuse.health/fhir/SubscriptionTopic/encounter` | Encounter created or updated | `patient`, `status`, `class` |
| `https://perfuse.health/fhir/SubscriptionTopic/appointment` | Appointment created or updated | `patient`, `status` |

A filter that names anything else is refused, not ignored. An ignored filter is a subscription that notifies about every patient when the client asked about one.

The properties the implementation is built around:

- **An acknowledged change is never a lost notification.** The notification is recorded in the same database transaction as the change it describes, and delivered from there, so it survives a restart. A test writes an Encounter, starts a new server on the same database, and checks the notification arrives.
- **Notifications arrive in order, and gaps are visible.** Event 2 is not sent before event 1 is accepted, and each notification carries the running count, so a receiver can tell when it has missed one.
- **The server decides when a subscription is active.** A new subscription is `requested`, and becomes `active` only when the endpoint accepts a handshake. A client that asks for `active` gets `requested`.
- **A full-resource notification carries the version that changed.** A discharge notification carries the discharge, not whatever the Encounter had become by the time delivery succeeded.
- **Failure is bounded.** A refused delivery is retried with a growing delay. After ten consecutive failures the subscription goes to `error`: later changes are counted but not queued, and updating the subscription resumes delivery from the oldest held notification. The count then shows how many were missed.

What a client can make the server do is limited deliberately:

- Endpoints must be `https` unless the operator passes `-fhir-subscriptions-allow-http`, which is for a test receiver.
- The egress policy is applied when a subscription is created and again at every delivery, and redirects are not followed.
- The payload defaults to `id-only`, which says what changed and lets the receiver fetch it with its own credentials.
- Nothing a receiver sends back is shown to anybody, only its status code.

It is off unless asked for, because once on, a FHIR client can make this server send patient data to a URL of the client's choosing. It needs an R4 endpoint that accepts writes, and refuses to start otherwise.

Not implemented, so nobody has to find out by trying:

- channels other than `rest-hook`
- heartbeats
- notifications on delete
- the `$status` and `$events` operations

The **Subscriptions** view shows each subscription's topic, filter and endpoint, how many events it has had, how many were accepted and how many are waiting, and the last reason one was refused. Header values and the endpoint's query string are withheld, because that is where a subscriber's credentials live.

## NCPDP Telecommunication Standard

Pharmacy claims. Fixed-width header followed by variable segments.

Two details that matter, both of which are easy to get wrong and produce plausible results:

The header is exactly 56 bytes and fixed width. A short header cannot be detected by length alone, because a truncated header plus the following segments still exceeds 56 bytes. Detection is that a correct header never contains a separator byte, so the header is scanned for one and a message that has one is refused with the number of bytes it is short by.

`0x1E` and `0x1C` are group and field **start** markers, not separators. Treating them as separators shifts every field one place left, and most of the resulting values stay plausible — a quantity lands where a day supply was expected and both are numbers.

> NCPDP and SCRIPT channels are excluded from MLLP framing, and this is enforced rather than advised. A claim legitimately contains `0x1C`, which is MLLP's own end-of-block byte, so framing a claim would truncate it at the first field marker.

## NCPDP SCRIPT

Electronic prescriptions, in XML. The internal package is called `eprescribe` because `script` was taken by the JavaScript engine.

The central safety concern is the substitution flag, and it reads backwards from the intuition: on the wire, `0` means substitution is **allowed** and `1` means dispense as written.

An absent substitution flag is refused rather than defaulted. Both possible defaults reach the patient — one may dispense a generic where the prescriber required the brand, the other prevents a substitution the prescriber permitted — so there is no safe assumption and the message stops.

A Schedule II prescription with refills is refused, and the builder will not produce one.

Refusals and warnings are kept as separate lists rather than one severity-tagged list, because a reader who has to check a severity field to know whether the message was sent will eventually not check it.

## DICOM

Imaging. Perfuse can receive DICOM objects, query a remote archive, and store to one.

DICOM is not a message format in the same sense as the others — objects are large, and the association handshake matters more than the payload parsing. The `called_ae` and `calling_ae` titles are the commonest reason a first connection fails, and the failure looks like a network problem rather than a configuration one.

### What a C-STORE listener accepts

Left open, a DICOM listener accepts anything from anyone that can reach the port. Four settings under **What this listener accepts** narrow that, and the first is the one worth understanding.

**Only accept these calling AE titles** is the restriction. The *called* AE title — the one alongside the address — is the name a sender dials, so it identifies this listener rather than the sender, and it is not a restriction. Anybody who can reach the port can use the correct called AE title, because it is published to them. Without a list of permitted callers, any host on the network may push images into a clinical channel.

**Only accept these SOP classes** and **only accept these transfer syntaxes** take comma-separated UIDs. Empty accepts every kind of object and negotiates whatever compression the sender offers, which includes kinds this channel's destinations cannot handle. Refusing at the association is clearer to the sender than accepting the object and failing afterwards.

**Largest object accepted** is in bytes, and zero means no limit. Whole-slide images and long series run to gigabytes, so the limit is otherwise memory.

## Delimited

CSV and its relatives. The options are on the source in the builder, and the [format reference](#format-reference) lists the file keys.

Paths address columns. A named column is available when a header row is declared or the names are given; otherwise columns are positional.

### Naming the columns

There are two ways, and they are exclusive. Either the first row of the file names them — **the first row names the columns** — or you list them yourself, in order. Declaring a header means the row is not treated as data.

If neither is set, columns can only be addressed by position, so a filter written against a name has nothing to match. Turning the header on removes the list of names rather than keeping both: with a header the names come from the file, so a list left behind would be ignored, and a channel that reads as though it chose the names when the file chose them is harder to debug than one that refuses.

### What a filter or a script sees

One delimited document becomes many messages, so this needs stating before you write either.

**With splitting on, everything runs per row.** Declarative steps, the filter and the transformer script all see one row, and a path means *this* row's column. A filter that rejects a row rejects that row and no other, and the received count is rows rather than documents.

**With splitting off, the message is the whole document** and a write that crosses rows is refused rather than applied to the first one. That refusal is deliberate: silently writing to row one is the behaviour that produces a file which looks transformed and is not.

### The three that change what counts as an error

**Accept rows with the wrong number of columns** is off by default. A row with too many or too few columns is then an error, which is what catches a file whose shape changed without warning. Turning it on accepts the row and leaves the missing columns empty.

**Trim spaces around each value** fixes the common case of a value padded to a column width. Leave it off where trailing spaces are significant, which they occasionally are in an identifier.

**Keep blank lines as rows** is off by default and usually should be. On, a blank line becomes a row of empty columns, which matters only when a count has to match the file exactly.

## Raw

No parsing at all. The message is bytes.

Use this when the content is not something Perfuse understands and does not need to: a PDF being moved between systems, a proprietary format, an already-formed document being forwarded unchanged.

The cost is that nothing which depends on structure works — no field-level transformation, no filter on a field, no field-level comparison in the parity checker. A raw channel is a pipe, and it is honest about being one.

## Choosing

If the message is HL7 v2, use `hl7`, even if you only intend to forward it unchanged. Parsing gives you the trace, the field-level statistics, the ability to filter later and the ability to compare. `raw` on parseable content buys nothing and loses all of that.

Use `raw` when parsing would fail or would be a lie about the content.

## SMART Health Links and Cards

CMS's *Kill the Clipboard* asks providers to accept a patient's records from a QR code at check-in, and to hand the visit record back the
same way. The QR code holds a **SMART Health Link** (`shlink:/...`): an address, a 256-bit key and, optionally, a label, an expiry and a
passcode flag. **Exchange → Health links** reads one.

**Receiving.** Paste the link, or scan it with the camera where the browser can (Chrome and Edge). Perfuse fetches the manifest,
decrypts each file with the key from the link, summarises any FHIR bundle - who the patient is, how many of each resource - and checks
any **SMART Health Card** inside against its issuer's published key. *Deliver to the channel* hands the FHIR content to a channel as a
message, which is how it reaches the EHR. The fetch is held to the egress policy at connect time, plain HTTP is refused, and the
response of a server that is not a manifest server is never shown, because the address came from a stranger's QR code.

**Sharing.** *Share* encrypts a FHIR bundle, hosts it, and shows the link and its QR code once. The server keeps the ciphertext and not
the key - it cannot open what it hosts, and cannot show the link again. A passcode is hashed; ten wrong ones disable the link. Links
expire after 30 days by default, can be revoked, and list how often and by whom they were fetched. Start the server with `-public-url`
set to the https address patients' phones reach; the manifest URL it builds must fit the specification's 128 characters.

**Verifying a card.** *Verify a card* takes a numeric `shc:/` QR (one chunk or several), a `.smart-health-card` file or a JWS, and
says whether the signature matches the issuer's key.
