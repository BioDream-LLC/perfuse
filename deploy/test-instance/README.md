# test.perfuse.health

A public Perfuse for partners and Inferno to test against: FHIR, Da Vinci CRD, DTR and PAS, SMART App Launch and Backend
Services, with test data only. Nothing on it is about a real person.

| What | Where |
|---|---|
| Console | `https://test.perfuse.health/`, username `demo`, password `try-perfuse-demo` (look at everything, change nothing) |
| FHIR | `https://test.perfuse.health/fhir` (SMART tokens) |
| SMART discovery | `https://test.perfuse.health/fhir/.well-known/smart-configuration` |
| CRD (CDS Hooks) | `https://test.perfuse.health/cds-services` |
| DTR | `Questionnaire/$questionnaire-package`, `$next-question` |
| PAS | `Claim/$submit`, `Claim/$inquire`, subscriptions |

The test payer's rules are `rules.yaml`, the conformance runs' rules in one file. The registered clients are `clients.yaml`:
Inferno's Backend Services client with Inferno's published test keys (it reads everything and writes only Claims and
Subscriptions), Inferno as a public and as a confidential app at inferno.healthit.gov, and Inferno's CRD client
(`cds-clients.yaml`). The test people in `users.yaml` sign in with the password "correct horse". To test with a client of your
own, open an issue on https://github.com/biodream-llc/perfuse with its JWKS URL or redirect URIs.

Eight demonstration channels (`channels/`) carry invented HL7 v2 traffic so the console has something to show: `hospital-feed`
receives a few messages of every kind each minute from `perfuse-traffic.timer` (`perfuse generate`) and routes them by type to
`adt-to-registry` (which relabels the record number, sends over MLLP with TLS to `patient-registry` and feeds the patient index),
`lab-results` (a shared mapping table, a feed contract, and a rewrite, `lab-results-v2`, compared beside it in shadow),
`orders` (which also sends to a stand-in billing system, `perfuse-billing.service`, that goes away for ten minutes every three
hours, so the queue fills and drains) and `clinical-documents` (published to this server's own XDS.b registry). Every listener is on 127.0.0.1, and the archive
files are deleted after a day.

`seed.py` fills the other screens with invented data, through the API, each time `setup.sh` runs (it looks before it adds): 30
patients in the FHIR store with encounters, conditions, HbA1c results and blood pressures; admissions from three systems in the
patient index, some the same people under other numbers, some with NHS numbers from the NHS's synthetic 999 range; 8 imaging
studies it draws itself; five colleagues and two machine tokens; data protection requests (one answered, one erasure carried
out with a second administrator's approval, one waiting, one restriction) and a legal hold; a local code system; two Direct
correspondents; a SMART Health Link; saved views and measures; and a TEFCA participant declaration with a demonstration
certificate. The Fleet page lists a second site that does not exist, to show what a server that has stopped looks like.
`setup.sh` makes two administrator tokens for it and revokes them when it has finished.

The shared `demo` account is a viewer that may also read the administrators' screens (never change them); see the manual's
security chapter.

Writes to the FHIR endpoint are limited to 300 a minute from one client address and 200,000 resources in all, so one client in
a loop cannot fill the disk. The instance requires IMDSv2, and the Lightsail role on its metadata service belongs to AWS's own
Lightsail account, not ours.

## Running it

```sh
deploy/test-instance/provision.sh v0.1.18  # create or upgrade to a release: Lightsail nano, static IP, DNS, certificate, service
deploy/test-instance/provision.sh main     # or to this checkout, built here for Linux
deploy/test-instance/load.py               # the test data, signed in as the loader client
```

The instance is about $5 a month. SSH is open only to the address `provision.sh` ran from.
