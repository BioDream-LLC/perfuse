# Interoperability tests against other implementations

Each of these tests runs Perfuse against an independent implementation of the same standard, built from a
pinned version in a Docker image here:

| Image | What it is | Tests | Variable |
|---|---|---|---|
| `pyas2lib/` | pyas2lib 1.4.3, an AS2 library | AS2 both ways, Triple DES and AES-256, compressed and not, signed receipts | `PERFUSE_TEST_PYAS2LIB=pf-pyas2lib` |
| `pydifact/` | pydifact 0.1.8, a UN/EDIFACT parser | EDIFACT read and written both ways | `PERFUSE_TEST_PYDIFACT=pf-pydifact` |
| `febrl/` | FEBRL's synthetic record-linkage data (BSD-3-Clause), from recordlinkage 0.16 | the patient index's accuracy | `PERFUSE_TEST_FEBRL=<dir>/dataset3.csv` |
| GreenMail 2.1.3 | a mail server (`greenmail/standalone:2.1.3`, pulled) | IMAP, POP3 and SMTP sources and destinations | `PERFUSE_TEST_GREENMAIL=127.0.0.1` |

`sh conformance/interop/run.sh` builds the images, fetches the FEBRL file into `~/.pftest/febrl`
(`PERFUSE_INTEROP_DIR` moves it), starts GreenMail and runs the tests. `run.sh build` only builds and
prints the variables. Without a variable, its test skips, so `go test ./...` needs none of this.

The other conformance runs (the Inferno suites, the HL7 validator, Keycloak, Entra, HAPI FHIR, Orthanc and the
rest) and what each found are in [docs/verification.md](../../docs/verification.md).
