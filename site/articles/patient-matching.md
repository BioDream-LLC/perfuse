# Patient matching: one person, many records, and why automatic linking should start switched off

![Records from several systems linked to one enterprise identifier](docs/assets/articles/hero-patient-matching.svg)

## In one minute

- Every system that sees a patient gives them its own number: the hospital, the clinic, the lab, the imaging centre. Bringing those records together is called **patient matching**, and the table that holds the result is a **master patient index** (MPI).
- There are two ways to decide two records are the same person: **deterministic** (a trusted identifier agrees) and **probabilistic** (names, birth date, address and so on, weighed together).
- The two mistakes are not equal. Missing a match leaves a split record. Linking two different people (an **overlay**) puts one patient's results in another's chart, which is far worse.
- So automatic linking on a score alone should start off, and be turned on only once someone has checked what it would do.
- Perfuse includes an optional patient index that works this way, tested on a public research dataset with no wrong links.

## Who this is for

This guide is for people who run hospital or regional systems and are tired of duplicate records, and for anyone weighing whether they need an MPI at all. No statistics background needed.

> In plain words: a **record number** (MRN) is the number one system gives a patient. An **enterprise identifier** is one number for the person across every system.

## Why the same person has many records

A patient registers at the emergency department with a name typed in a hurry. A month later the clinic books them under their full given name. The lab receives a specimen labelled with the surname only, and the birth date with day and month swapped. Each system is internally consistent. Together they hold three people who are really one.

The consequences are concrete: an allergy recorded in one record and missing from the one a doctor opens; a repeated test because the earlier result could not be found; a bill sent to the wrong address.

## Deterministic matching: when a number agrees

The simplest rule is that two records with the same trusted identifier are the same person. A national health number checked by its check digit is the best such identifier, where a country has one: the NHS number in England, the BSN in the Netherlands, the KVNR on a German insurance card.

Two cautions:

- **Only trust a number you can check.** A mistyped digit usually makes somebody else's valid-looking number. A number that fails its check digit should be kept, flagged, and never used to link.
- **The same number arrives in different spellings.** One system sends it with spaces under an old identifier, another without spaces under a web address. It has to be stored one way, or it will not match itself.

## Probabilistic matching: weighing the evidence

Where no trusted number is shared, the evidence has to be weighed. The classic method, named after Fellegi and Sunter, gives each field a weight: agreement on a rare surname is strong evidence, agreement on sex is weak, and disagreement on a birth date counts against. Fuzzy comparisons catch typing slips (Jaro-Winkler for names, a sound code such as Soundex for spelling variants), and a day and month swapped counts as partial agreement.

There is a practical trap. Comparing every record with every other is too slow, so systems compare only records that share a **blocking key**, such as surname sound plus birth year. One blocking key per record is fragile: a mistyped birth year hides the person completely. Several keys, so one error does not hide a match, matter more than people expect.

## The two mistakes are not equal

A missed match is a nuisance: two records that should be one. An overlay, two people merged into one, is a safety event, because results, allergies and medicines flow into the wrong chart and are hard to separate again.

That asymmetry should shape the design:

- Records whose score is high but not certain go to a **review queue** for a person to decide.
- Linking on a score alone, with nobody looking, is a switch an administrator turns on, after the queue has shown the certain matches really are right.
- Every link, unlink and merge keeps its history, so a mistake can be undone.

> In plain words: let the computer suggest; let a person decide, until the computer has earned trust on your own data.

## How Perfuse does it

Perfuse has an optional patient index, off until a site turns it on (it is, after all, a table of names and birth dates). Channels that carry registrations feed it, from HL7 v2 or FHIR. It links records deterministically on trusted identifiers, including 37 national patient numbers each checked by its check digit, and probabilistically with Fellegi-Sunter weights it can estimate from the site's own records. Five blocking keys keep one error from hiding a person. By default nothing links on a score alone: likely matches wait in a review queue, and automatic linking of certain matches is one switch an administrator turns on. HL7 A40 merges are applied with their history, and the index answers FHIR `Patient/$match`, IHE PIXm and PDQm, and PIX and PDQ in HL7 version 3.

How it was checked: on FEBRL dataset 3, a public research set of 5,000 synthetic records of 2,000 people, no record was linked to the wrong person, with automatic linking off, on with published weights, or on with weights estimated from the records. The measuring found a real weakness on the way (one blocking key let 30% of duplicates go unseen) which the extra keys fixed. It has not yet run on a real hospital's population.

## Get started

1. Turn on **Settings → Data → Patient index** and add a patient index destination to your registration feed.
2. Watch the review queue for a few weeks before considering automatic linking.
3. The [manual](https://perfuse.health/manual/) describes every setting, and the [verification record](https://perfuse.health/verification/) has the FEBRL results.

## Perfuse on GitHub

Perfuse is free and open source under Apache 2.0. The source, every release, the issue tracker and the full documentation are on GitHub: **[github.com/biodream-llc/perfuse](https://github.com/biodream-llc/perfuse)**.

- [Download the latest release](https://github.com/biodream-llc/perfuse/releases/latest) for Linux, macOS or Windows
- [Read the source](https://github.com/biodream-llc/perfuse)
- [Report an issue or ask a question](https://github.com/biodream-llc/perfuse/issues)
