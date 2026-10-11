# test.perfuse.health

A public Perfuse for partners and Inferno to test against: FHIR, Da Vinci CRD, DTR and PAS, SMART App Launch and Backend
Services, with test data only. Nothing on it is about a real person.

| What | Where |
|---|---|
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

## Running it

```sh
deploy/test-instance/provision.sh v0.1.8   # create or upgrade: Lightsail nano, static IP, DNS, certificate, service
deploy/test-instance/load.py               # the test data, signed in as the loader client
```

The instance is about $5 a month. SSH is open only to the address `provision.sh` ran from.
