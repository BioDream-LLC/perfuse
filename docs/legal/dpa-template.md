# Data processing agreement (draft template)

> **Draft for a lawyer, not legal advice.** BioDream wrote this as a starting point for organisations that ask BioDream LLC
> to host Perfuse for them. It follows Article 28(3) of the GDPR and of the UK GDPR, but it has not been reviewed by a
> lawyer, and it must be before anyone signs it. Text in [square brackets] is for the parties to fill in.

**Between** [the customer, its registered name, number and address] (the **Controller**)
**and** BioDream LLC, [address] (the **Processor**),
together the parties, dated [date].

## 1. What this agreement covers

1.1 The Processor hosts and operates Perfuse, a healthcare integration engine, for the Controller under [the service
agreement of date] (the **Service Agreement**). In doing so it processes personal data on the Controller's behalf. This
agreement sets out how, as Article 28 of the GDPR and of the UK GDPR require.

1.2 Where this agreement and the Service Agreement disagree about personal data, this agreement wins.

## 2. The processing

| | |
|---|---|
| Subject matter | Running Perfuse: receiving, transforming, storing and delivering the Controller's messages and records between the systems the Controller names |
| Duration | The term of the Service Agreement, and afterwards only as clause 11 says |
| Nature and purpose | Hosting, operation, monitoring, backup and support of the Controller's Perfuse servers, on the Controller's instructions |
| Types of personal data | Identification (names, dates of birth, addresses, patient and national identifiers), contact details, and health data (a special category under Article 9): clinical messages, results, orders, documents, images and claims [adjust] |
| Categories of data subjects | Patients and service users of the Controller; the Controller's staff and its partners' staff named in messages; the Controller's users of the console [adjust] |
| Where | [AWS Region, for example eu-central-1 (Frankfurt), eu-west-1 (Ireland) or eu-west-2 (London)], with backups, logs and traces in the same Region |

## 3. Instructions

3.1 The Processor processes the personal data only on the Controller's documented instructions: the Service Agreement,
this agreement, the configuration the Controller makes (its channels, destinations and settings), and written
instructions given later. That includes transfers to a third country, unless the law requires otherwise, in which case
the Processor tells the Controller first unless the law forbids that.

3.2 The Processor tells the Controller at once if it believes an instruction breaks data protection law.

## 4. People

4.1 Everyone the Processor lets process the personal data is bound to confidentiality, by contract or by law, and has
been trained in it.

4.2 Access to the Controller's servers is limited to the Processor's staff who operate them, by named account, with every
sign-in and every read of message content recorded in the server's audit log, which the Controller can read.

## 5. Security (Article 32)

The Processor keeps at least the measures in Schedule 1 and reviews them [yearly]. It may change them only to measures no
less protective.

## 6. Sub-processors

6.1 The Controller authorises the sub-processors in Schedule 2. The Processor tells the Controller at least [30] days
before adding or replacing one, so that the Controller can object; if the Controller objects on reasonable grounds and
the parties cannot agree, the Controller may end the affected service.

6.2 The Processor puts the same data protection obligations as in this agreement on each sub-processor by contract, and
stays responsible to the Controller for them.

## 7. Transfers outside the UK and the EEA

7.1 The personal data stays in the Region named in clause 2, backups, logs, traces and support access included. The
Processor makes no transfer outside the UK and the EEA without the Controller's written instruction.

7.2 Where a transfer is instructed, the parties first put in place [the EU Standard Contractual Clauses (Commission
Decision 2021/914) / the UK International Data Transfer Agreement or Addendum] and any supplementary measures the
assessment calls for.

## 8. Helping with people's rights

8.1 The Processor helps the Controller answer requests from data subjects (access, rectification, erasure, restriction,
portability, objection) by the means Perfuse provides (its Data protection screen: finding what is held, a copy as a zip,
erasure with a second person's approval, legal holds) and, where those are not enough, by other reasonable help.

8.2 A request that reaches the Processor directly is passed to the Controller without undue delay and not answered by the
Processor.

## 9. Helping with the Controller's other duties

The Processor helps the Controller, taking into account what it knows, with security (Article 32), notifying breaches
(Articles 33 and 34), data protection impact assessments and prior consultation (Articles 35 and 36).

## 10. Personal data breaches

10.1 The Processor tells the Controller without undue delay, and in any case within [24] hours, of becoming aware of a
personal data breach affecting the Controller's data, with what it knows: what happened, the data and people affected,
the likely consequences, and what has been and will be done. It adds to this as it learns more.

10.2 The Processor does not notify a supervisory authority or data subjects on the Controller's behalf unless the
Controller asks it to in writing.

## 11. At the end

11.1 When the service ends, the Processor, as the Controller chooses, returns the personal data (an export of the message
store, the FHIR store and the patient index in the formats Perfuse writes) or deletes it, and deletes the remaining copies,
unless the law requires it to keep them.

11.2 Backups are deleted when their retention ends, at most [35] days after the service ends; until then they are kept as
securely as the live data and used for nothing else.

11.3 The Processor confirms the deletion in writing.

## 12. Information and audits

The Processor makes available all information needed to show it meets Article 28, and allows and contributes to audits,
including inspections, by the Controller or an auditor it appoints, [on 30 days' notice, once a year, and after a breach].

## 13. Liability and law

[Liability as in the Service Agreement, subject to the GDPR and UK GDPR.] This agreement is governed by the law of
[England and Wales / Ireland / the Member State of the Controller], and its courts have jurisdiction.

## Schedule 1: security measures

- **Isolation:** each Controller's servers in their own [AWS account / VPC], reachable only through TLS; no shared
  database between customers.
- **Encryption:** TLS for every connection the console, the FHIR endpoint and MLLP accept (TLS 1.2 or later); storage
  encrypted at rest with keys held in the Region (AWS KMS); backups encrypted.
- **Access:** console access by named account, with roles (viewer, operator, editor, administrator) and optional single
  sign-on and passkeys; the Processor's own administrative access through [a named, multi-factor route], recorded.
- **Audit:** every sign-in, configuration change and read of message content recorded with who, what and when.
- **Retention:** message records and bodies deleted at the retention windows the Controller sets; erasures applied on
  request and recorded in a ledger that is applied again to any restored backup.
- **Resilience:** [backups every N hours, kept N days, restored and checked every N months]; [a second server in another
  Availability Zone].
- **Software:** Perfuse's releases are built reproducibly, checked with govulncheck before release and carry a bill of
  materials; security fixes applied within [N] days.
- **People:** confidentiality undertakings and training; access removed the day someone leaves.

## Schedule 2: sub-processors

| Sub-processor | What it does | Where |
|---|---|---|
| Amazon Web Services EMEA SARL (or Amazon Web Services, Inc.) | Hosting: compute, storage, database, key management | [the Region in clause 2] |
| [any other] | | |
