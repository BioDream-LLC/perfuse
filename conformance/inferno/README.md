# Inferno conformance runs

The scripts that produce the Inferno results in [docs/verification.md](../../docs/verification.md). Each runs ONC's official test
kit, at the version the result was recorded with, against a Perfuse built from this tree.

```sh
go build -o bin/perfuse ./cmd/perfuse
conformance/inferno/run.sh pas      # or crd, dtr, smart, uscore
```

| Suite | Kit | Perfuse serves | Recorded |
|---|---|---|---|
| `crd` | davinci-crd-test-kit v0.14.2, CRD Server v2.2.1 | `-fhir -crd-rules -cds-clients` | passes as a whole |
| `dtr` | davinci-dtr-test-kit v0.18.0, DTR Payer Server v2.2.0 | `-fhir -crd-rules -smart-clients -smart-users` | 43 pass, 1 omit, 2 fail |
| `pas` | davinci-pas-test-kit v0.15.2, PAS Server v2.2.1 | `-fhir -pas -crd-rules -fhir-subscriptions -smart-clients` | 82 pass, 2 fail |
| `smart` | smart-app-launch-test-kit v1.0.3, SMART App Launch STU2.2 | `-fhir -smart-clients -smart-users` | public app 80 of 80; confidential app 67 pass, 3 omit |
| `uscore` | us-core-test-kit v1.1.6, US Core Server v7.0.0 | `-fhir -smart-clients -smart-users` | launch and both granular-scope groups pass; API 272 pass, 14 fail (patient token), 501 pass, 4 fail (system token) |

verification.md explains every failure.

## What each step does

- `kit.sh` clones the kit at its tag into `.work/kits`, starts it with Docker Compose on http://localhost, and adds the test CA to
  its containers. One kit runs at a time.
- `certs.sh` makes a throwaway test CA and a certificate for `host.docker.internal`, the name Inferno's containers use for this
  machine. Inferno checks TLS.
- `serve.sh` starts Perfuse on a fresh database in `.work/<suite>` with the flags in `<suite>/flags`, and writes an editor API
  token for the run script's console calls.
- `<suite>/run.py` loads the suite's data, starts the run through Inferno's API, plays any part a person would (the PAS reviewer,
  the patient or clinician signing in), and prints the results.

Inferno signs SMART Backend Services and CDS Hooks requests with its published test keys. `inferno.py` downloads them from the
SMART App Launch kit and registers their public halves with Perfuse (`clients.py`). Nothing in this directory is a secret: the test
CA, the keys and the one password (`correct horse`, for the US Core test patient) exist only for these runs.

The test data is the kits' own (their presets and fixtures) or Inferno's reference server data, changed only where a run says
so and why. Everything generated goes in `.work/`, which git ignores.
