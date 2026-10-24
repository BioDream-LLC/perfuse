# Da Vinci CRD, DTR and PAS 2.1.0 beside 2.2.x

CMS-0057-F's list of recommended guides names CRD, DTR and PAS 2.0.1 and 2.1.0. The 2.0.1 releases rest on US Core 3.1.1,
which expired on 1 January 2026, so 2.1.0 is the release a payer is pointed at today. Perfuse is built and tested on the newest
releases, CRD 2.2.1, DTR 2.2.0 and PAS 2.2.1, and answers a 2.1.0 client in 2.1.0. This run shows both.

There is no Inferno test kit for 2.1.0: the Da Vinci kits test 2.0.1 and 2.2.x. So `run.py` takes each 2.1.0 guide's own
examples as the client's requests, sends them to Perfuse, and has the official HL7 validator check every answer against the
2.1.0 profiles. It then makes the same calls without naming a version and checks those answers against 2.2.x. The 2.2.x
releases are also checked by Inferno's own suites (`conformance/inferno`), which this change was re-run against.

```sh
go build -o /tmp/perfuse ./cmd/perfuse
python3 conformance/davinci21/run.py -perfuse /tmp/perfuse
```

## How Perfuse knows which release a client speaks

Most specific first:

1. The request. CRD: the CDS Hooks extension `davinci-crd.requestedVersion` (`"2.1"`). DTR and PAS: the version on a Da Vinci
   profile the input claims (`.../profile-pas-request-bundle|2.1.0`).
2. The client's registration: `davinci_version: "2.1"` on a SMART client (`-smart-clients`) or a CDS client (`-cds-clients`).
3. The server: `serve -davinci-version 2.1`. The default is 2.2.

Inside, every answer is built, decided and stored in the 2.2.x form; a 2.1.0 client gets it rewritten into 2.1.0's as it is
sent. One form inside keeps one implementation of the decisions, the X12 278 mapping, the review queue and the metrics.

## What differs, and what Perfuse does

| Guide | 2.1.0 | 2.2.x |
|---|---|---|
| CRD | `indeterminate` is not a code: conditional, with `info-needed` OTH and the reason as text | `indeterminate` |
| CRD | reasons are only `gold-card` and `detail-code`; a membership or technical reason goes as text | `no-member-found`, `technical`, ... |
| CRD | `detail` has no `category`; its code is from CRD's `temp` code system | `category` required; `crd-coverage-detail` codes |
| CRD | `contact` is a ContactPoint, one per phone, email or address | a ContactDetail |
| DTR | `$questionnaire-package` returns `PackageBundle` | `packagebundle` |
| DTR | the package's QuestionnaireResponse is optional and left out (its profile wants what only a filled-in one has) | required, and sent |
| DTR | `$next-question` reads `in` (the profile and example) or `questionnaire-response-in` (the operation), and answers in `out` | `questionnaire-response`, `return` |
| DTR | the adaptive questionnaire in a response has no `url`, is `derivedFrom` the payer's, has a narrative, and its items carry no `enableWhen` | `url` required |
| DTR | an adaptive response is a `dtr-questionnaireresponse` | `dtr-questionnaireresponse-adapt` |
| PAS | `$inquire` returns one Bundle (`return` 1..1) holding every match; none found is a 404 | a Parameters of Bundles (`return` 0..*) |
| PAS | no `claimResponseReviewer`, `TransmissionIdentifiers`, `admissionDates`, `dischargeDate` | defined |
| PAS | `errorElement` is a string | `error` and `processNote` sub-extensions |
| PAS | a Task input's line is `extension-paLineNumber`; a CommunicationRequest's goes on its payload | `serviceLineNumber` |

The DTR 2.1.0 `$next-question` operation and its own profile disagree on the parameter names; Perfuse reads both and answers with
the profile's (`out`), which is what the published example and an app built on it use.

## Results (24 October 2026, validator_cli 6.10.4, terminology off)

[results/crd.md](results/crd.md), [results/dtr.md](results/dtr.md), [results/pas.md](results/pas.md) list every answer and
every error.

| Guide | Calls | Errors in Perfuse's answers, 2.1.0 | Errors in Perfuse's answers, 2.2.x |
|---|---|---|---|
| CRD | the guide's two order-sign requests (the second switches coverage-info off, and gets the empty answer CRD asks for) | 0 | 0 |
| DTR | `$questionnaire-package` (the guide's input, and an adaptive questionnaire), `$next-question`, `$log-questionnaire-errors` | 0 | 0 |
| PAS | `$submit` with the guide's five request Bundles, `$inquire` with its inquiry Bundle | 0 | 0 |

Every error the validator reports is in the guides' own example data, which Perfuse returns as it was sent:

- example URLs (`http://example.org/...`) the validator refuses in a real instance;
- PAS: the guide's Patient `SubscriberExample` does not meet `profile-beneficiary`. The guide's own request example fails the
  same check when validated alone;
- PAS 2.2.1: the 2.1.0 examples' Coverage has no identifier, which 2.2.1's profile requires;
- DTR: the guide's `home-o2-std-questionnaire` carries a version without the `versionAlgorithm` SDC 4.0.0 asks for;
- PAS 2.2.1, terminology off: the validator cannot check the LOINC attachment code `18776-5` against its value set.

The guide's `$next-question` example is a completed response to a standard questionnaire, with nothing contained in it: there is
no next question, and Perfuse answers with a 4xx and an OperationOutcome. The adaptive call is made with a questionnaire of this
run's own, as an app would.

## What this does not show

- That a 2.1.0 client from a vendor works with Perfuse: these are the guides' examples, not a client. No 2.1.0 test kit exists.
- PAS subscription notifications in 2.1.0 follow the server's `-davinci-version`, not the subscriber's: a notification is not an
  answer to a request, and the Subscription does not say which release its owner reads.
- Inferno's 2.2.x suites were re-run after this change and their results are those recorded in `docs/verification.md`.
