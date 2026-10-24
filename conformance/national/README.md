# National and European FHIR profiles: UK Core, FR Core, ISiK, Nictiz nl-core and HL7 Europe Laboratory and Discharge Reports

`run.py` converts real-shaped HL7 v2 messages with `perfuse fhir convert` and checks every resource with the official HL7
validator against the national profile for its type. It is a check of Perfuse's v2-to-FHIR conversion against what each
country's guide requires, not a certification, and not a test of Perfuse's FHIR server.

| Set | Messages | Guide | Resources checked | Pass |
|---|---|---|---|---|
| `uk` | NHS Wales' published v2 examples ([GIG-Cymru-NHS-Wales/hl7-v2-examples](https://github.com/GIG-Cymru-NHS-Wales/hl7-v2-examples) at 45926af), 22 | UK Core STU2 2.0.2 (`fhir.r4.ukcore.stu2`) | 187 | 185 |
| `fr` | The French agency's PAM-FR and document examples ([ansforge/hl7V2-exemples](https://github.com/ansforge/hl7V2-exemples) at 6033987), 59 | FR Core 2.2.0 (`hl7.fhir.fr.core`) | 156 | 138 |
| `de` | Invented German messages in `testdata/de` (no real person), 8 | ISiK Basismodul 4.0.3, Stufe 4 (`de.gematik.isik-basismodul`) | 47 | 18 |
| `de-site` | The same, with the hospital's own bundle script ([examples/isik/bundle-script.js](../../examples/isik/bundle-script.js)) | ISiK Basismodul 4.0.3, Stufe 4 | 47 | 42 |
| `nl` | Invented Dutch messages in `testdata/nl` (no real person), 3 | Nictiz nl-core 0.12.1-beta.1, zib 2020 (`nictiz.fhir.nl.r4.nl-core`) | 24 | 12 |
| `nl-site` | The same, with the hospital's own bundle script ([examples/nictiz/bundle-script.js](../../examples/nictiz/bundle-script.js)) | Nictiz nl-core 0.12.1-beta.1 | 24 | 18 |
| `hdr` | The patient administration messages (ADT) among all three sets, 15 | HL7 Europe Hospital Discharge Report 1.0.0 (`hl7.fhir.eu.hdr`) | 69 | 68 |
| `eu` | The lab results (ORU^R01) among all three sets, 23 | HL7 Europe Laboratory Report 2.1.0 (`hl7.fhir.eu.laboratory`), with HL7 Europe Base 2.0.1 for patients, practitioners and organisations | 426 | 105 |

Per resource type and every error message: [results/uk.md](results/uk.md), [results/fr.md](results/fr.md),
[results/de.md](results/de.md), [results/de-site.md](results/de-site.md), [results/nl.md](results/nl.md),
[results/nl-site.md](results/nl-site.md), [results/eu.md](results/eu.md), [results/hdr.md](results/hdr.md). All re-run
2026-10-24, validator_cli 6.10.4, FHIR R4, terminology from tx.fhir.org.

## What is left, and why

- **UK Core, 2 of 187.** One Welsh sample sends a US race code (CDC 2028-9), which Perfuse carries in US Core's race
  extension; the UK run does not load US Core, so the validator refuses an extension it cannot find. One Specimen error is
  the terminology server failing to parse a SNOMED CT expression in the profile's own binding (tx.fhir.org), not the data.
- **FR Core, 18 Encounters.** FR Core requires a period and an identifier. 14 of the French document messages carry a PV1
  with no admission date (PV1-44), and 4 no visit number (PV1-19). Perfuse does not invent either.
- **ISiK, 18 of 47 on its own, 42 of 47 with the hospital's bundle script.** What ISiK requires that a v2 ADT does not say:
  - every Encounter needs a *Kontaktebene* (facility, department or care-unit contact), which no v2 field carries;
  - every Organization needs an identifier (IKNR, BSNR or a department id); the v2 messages name the facility only;
  - the patient number must be typed `MR`; the German messages here send `PI`, as the French national profile does;
  - every stay's diagnosis needs a *Diagnosetyp* (treatment or referral diagnosis), which DG1 does not say.

  A site meets these with a FHIR destination's `bundle_script`, which adds what its own systems know. The example here is the
  hospital saying its IK number, that a PV1 visit is a department contact, that its PI numbers are its medical record numbers
  and that the diagnoses on a stay are treatment diagnoses. Perfuse does not guess any of them. What is left with it:
  - two diagnoses need `recordedDate`, from DG1-19 (attestation), which two of the messages leave empty;
  - two ORU ordering providers have no name type (XCN-10), so their name cannot be called official;
  - one OPS-coded Procedure: the validator reads the version `*` in Basis DE's OPS value set as a version, not as "any", and
    refuses the coding's `2026`, which Basis DE requires.
- **Nictiz nl-core, 12 of 24 on its own, 18 of 24 with the hospital's bundle script.** nl-core asks of every given name whether
  it is a first name or an initial (the `iso21090-EN-qualifier` extension, `BR` or `IN`); a v2 name does not say (XPN.3 is
  "second and further given names or initials thereof"). The example script is the hospital saying its systems send first names
  in XPN.2 and initials in XPN.3. What is left with it is six codes the terminology server could not check: nl-core binds the
  allergen, the diagnosis, the procedure and the specimen type to Dutch SNOMED CT reference sets, and tx.fhir.org does not
  evaluate their `concept in` filters. Whether the codes are in those sets was therefore not shown either way. nl-core has no
  profile for a laboratory report (its TextResult is a free-text one), so DiagnosticReports are not checked.

- **HL7 Europe Hospital Discharge Report, 68 of 69.** The stays, their diagnoses, the procedure, the allergies, the patients,
  practitioners and organisations meet the guide's profiles (its obligation profiles, `-obl-eu-hdr`, where it has them). The
  one failure is the Welsh sample's US race code, as in the UK run. As with the lab report, the discharge report itself is a
  document (`bundle-eu-hdr` with its Composition) that a sender assembles from these; the conversion makes the parts. The
  warnings are not counted: codes from the sender's own tables (admission type, discharge disposition, marital status, PV1-4,
  PV1-36, PID-16) carried without a code system, since the sender's local values cannot be claimed for HL7's.
  The French and German samples carry almost no DG1, PR1 or AL1, so `testdata/de` gained one invented discharge with an
  admission and a final diagnosis, a procedure and an allergy.

## What the runs fixed in Perfuse

Before these runs: FR Core Patient 33 of 40 and Practitioner 14 of 19; ISiK Practitioner 0 of 7, and German diagnoses and
insurance numbers written in forms no German receiver reads.

- **German code systems.** Table 0396 names ICD-10-GM and OPS with their year (`I10G2006`, `ICD10GM2016`, `O3012006`,
  `OPS2016`, `ALPHAID2016`); senders keep the pattern (`I10G2026`). They went to a placeholder system; now
  `http://fhir.de/CodeSystem/bfarm/icd-10-gm` (and `ops`, `alpha-id`) with the year as `Coding.version`, which ISiK requires.
  CWE-7/CWE-8, the coding system version, is now read for every coded field. `I10C` and `I9C` are read as ICD-10-CM and
  ICD-9-CM.
- **National patient numbers in the FHIR output.** A KVNR under gematik's OID (1.2.276.0.76.4.8) or type `NNDEU`, an NHS
  number under its OID: written under the system the country's guides use (`http://fhir.de/sid/gkv/kvid-10`,
  `https://fhir.nhs.uk/Id/nhs-number`, ...), without spacing, with fhir.de's `KVZ10` type for the KVNR. A number failing its
  check digit is kept as sent, untyped, with a warning.
- **The patient's own number** sent as `PI` (French, German) is now `use: usual`, as `MR` already was. FR Core requires it.
- **Dutch names and the BSN** (from the `nl` run). A family name sent in its parts (XPN.1 and XCN.2 as FN: `van
  Voorbeeld&van&Voorbeeld`) became the family name subcomponent separators and all. It is now the surname, with its parts in
  HL7's family-name extensions (own prefix, own name, partner's prefix and name), as HL7's v2-to-FHIR FN map writes them and as
  nl-core builds a Dutch name. The BSN's issuer as Dutch v2 names it, `NLMINBIZA`, is now known, so it no longer draws a
  warning that the authority is unconfigured.
- **OPS categories, and Procedure.category's shape** (from the `de` run). An OPS-coded procedure now carries its SNOMED CT
  category from the code's chapter, by HL7 Deutschland's `OPS-SNOMED-Category` map (with chapter 1 as ISiK's value set has it),
  which ISiK requires. And `Procedure.category` was a list, which R4 does not allow (it is one CodeableConcept): it was never
  written before PR1 was converted, so nothing had shown it.
- **Procedures and the stay's diagnoses** (from the `hdr` run). PR1 was not converted at all: it is now a Procedure, as HL7's
  v2-to-FHIR PR1 map has it (code, time, category from table 0230, the anaesthetist, surgeon and other performers with the map's
  SNOMED CT roles, reason, identifier), with status `unknown`, the map's answer when the message does not say whether it was
  done. DG1 now also puts the Condition in the stay's `Encounter.diagnosis`, the admitting one (DG1-6 `A`) as `AD`; working and
  final have no diagnosis-role code in the map and are left without one.
- **A result's time and SNOMED CT versions** (from the `eu` run). A result without OBX-14 now takes its report's OBR-7, as v2
  defines OBX-14 (sent only when a result's time differs from its order's). A SNOMED CT version given as a bare date in CWE-7
  (`20140131`) names no edition and is refused by a terminology server; it is left out instead (it was written since the
  CWE-7 change above). Specimens went from 18 to 19 of 19.
- **The laboratory that produced a result** (from the `eu` run). OBX-23 (with OBX-24, its address), or else OBX-15, the
  producer's id, and OBX-25, its medical director, were not read at all. They are now the result's performers, as HL7's
  v2-to-FHIR OBX map has them: 15 results passed that had failed, and the UK run checks 3 more resources (2 laboratories, 1 director), all passing.
- **Who ordered a result, who signed it off** (found answering a question on chat.fhir.org). OBR-16, the ordering provider,
  was written as the DiagnosticReport's performer, which said the person who asked for a test produced its result. It is now
  the requester of the order the report is `basedOn`: each OBR of a result is also a ServiceRequest, as HL7's v2-to-FHIR
  ORU_R01 map has it, its status from ORC-5 or `unknown`. OBR-32, the principal result interpreter, was not read; it is now
  `resultsInterpreter`, and OBR-34 and OBR-35 (technicians, transcriptionists) are performers with their function. OBR-32 is
  read as an NDL, or as an XCN when the sender writes it so. The UK run now checks 4 orders and 1 more practitioner (all
  pass), the French 14 more practitioners (all pass), and the EU run 20 orders (18 pass; 2 have no order number) and 15 more
  practitioners (5 named by number only, which HL7 Europe Base refuses).
- **Practitioners' names and national numbers.** XCN-10 `L` is now `use: official` (it was dropped). XCN-13 is now the
  identifier type, including France's `IDNPS` (RPPS/ADELI) in FR Core's code system.

## Running it

```
go build -o /tmp/perfuse ./cmd/perfuse
python3 conformance/national/run.py -perfuse /tmp/perfuse            # uk, fr and de; -sets uk,fr,de,de-site,nl,nl-site,eu,hdr
```

Needs Java 17 and `validator_cli.jar` (`-validator`, default `~/.cache/fhir-validator/validator_cli.jar`), and network
access for the packages and tx.fhir.org. The two public sample sets are fetched at the commits above into
`~/.cache/perfuse-national`. `-keep DIR` keeps the bundles, resources and validator output.
