#!/usr/bin/env python3
"""Loads the public test instance's test data, the same the conformance runs use (conformance/inferno):

- CRD's member records: Amy Shaw, her coverage, and an approved authorization for home oxygen;
- DTR's questionnaires: Inferno's dinner-order fixtures (static, with their CQL Library and ELM, and adaptive) and the
  questionnaire that uses every element DTR marks must-support, with its answer ValueSets; and DTR 2.2.0's example Coverage;
- the clinician the EHR launch signs in as (users.yaml: drb), and SMART App Launch's two patients and encounter.

Signs in as the loader client (clients.yaml) with the private key in ~/keys/perfuse-test-loader.pem. Every resource is PUT under
its own id, so loading again changes nothing. The DTR fixtures come from the DTR kit, checked out by conformance/inferno/kit.sh
(it is cloned here if missing; Docker is not needed).

Usage: load.py [https://test.perfuse.health]
"""
import gzip
import importlib.util
import io
import json
import os
import subprocess
import sys
import tarfile
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
CONFORMANCE = os.path.join(HERE, "..", "..", "conformance", "inferno")
sys.path.insert(0, CONFORMANCE)
import inferno  # noqa: E402

BASE = (sys.argv[1] if len(sys.argv) > 1 else "https://test.perfuse.health").rstrip("/")
KEY = os.path.join(os.environ.get("KEYS", os.path.expanduser("~/keys")), "perfuse-test-loader.pem")
with open(os.path.join(HERE, "loader-kid.txt")) as f:
    KID = f.read().strip()


def module(path, name):
    spec = importlib.util.spec_from_file_location(name, os.path.join(CONFORMANCE, path))
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    return m


def put(token, res):
    req = urllib.request.Request(f"{BASE}/fhir/{res['resourceType']}/{res['id']}", json.dumps(res).encode(),
                                 {"Authorization": "Bearer " + token, "Content-Type": "application/fhir+json"}, method="PUT")
    try:
        with urllib.request.urlopen(req, timeout=30) as r:
            print("loaded", res["resourceType"], res["id"], r.status, flush=True)
    except urllib.error.HTTPError as e:
        raise SystemExit(f"PUT {res['resourceType']}/{res['id']}: {e.code} {e.read()[:300]}")


def main():
    kit = inferno.kit_path("dtr")
    if not os.path.isdir(kit):
        subprocess.run(["git", "-c", "advice.detachedHead=false", "clone", "-q", "--depth", "1", "--branch", "v0.18.0",
                        "https://github.com/inferno-framework/davinci-dtr-test-kit.git", kit], check=True)
    crd = module("crd/run.py", "crd_run")
    sys.path.insert(0, os.path.join(CONFORMANCE, "dtr"))
    dtr = module("dtr/run.py", "dtr_run")

    token = inferno.backend_token(BASE + "/auth/token", client_id="loader", scope="system/*.cruds", key_file=KEY, kid=KID)
    resources = list(crd.MEMBERS)
    resources += [dtr.add_elm(e["resource"]) for e in
                  dtr.fixture("dinner_static/parameters_questionnaire_dinner_order_static.json")["parameter"][0]["resource"]["entry"]]
    resources += [dtr.adaptive(), dtr.VALUESET, dtr.BIG_VALUESET, dtr.QUESTIONNAIRE, dtr.coverage(),
                  {"resourceType": "Practitioner", "id": "d1", "name": [{"family": "Brown", "given": ["Dana"]}]}]
    # SMART App Launch (smart/run.py): the patient amy signs in as, a second patient her token must not reach, and the
    # encounter of the EHR launch.
    resources += [
        {"resourceType": "Patient", "id": "p1", "name": [{"family": "Member", "given": ["Amy"]}], "birthDate": "1980-02-14", "gender": "female"},
        {"resourceType": "Patient", "id": "p2", "name": [{"family": "Other", "given": ["Ben"]}], "birthDate": "1975-05-01", "gender": "male"},
        {"resourceType": "Encounter", "id": "e1", "status": "finished",
         "class": {"system": "http://terminology.hl7.org/CodeSystem/v3-ActCode", "code": "AMB"}, "subject": {"reference": "Patient/p1"}}]
    # Bulk Data and Provider Access: a provider's attributed members (Da Vinci ATR's Group).
    resources.append({"resourceType": "Group", "id": "attributed", "type": "person", "actual": True, "name": "Dr Brown's attributed members",
                      "member": [{"entity": {"reference": "Patient/pmember"}}, {"entity": {"reference": "Patient/p1"}}]})
    # PDex Payer-to-Payer reads the matched member's clinical record: Amy Shaw's office visit.
    resources.append({"resourceType": "Encounter", "id": "emember", "status": "finished",
                      "class": {"system": "http://terminology.hl7.org/CodeSystem/v3-ActCode", "code": "AMB"},
                      "type": [{"coding": [{"system": "http://www.ama-assn.org/go/cpt", "code": "99213"}], "text": "Office visit"}],
                      "subject": {"reference": "Patient/pmember"}, "period": {"start": "2026-08-14T09:00:00Z", "end": "2026-08-14T09:30:00Z"}})
    # PDex Payer-to-Payer: two members $member-match cannot tell apart (same name, birth date and subscriber id), for the
    # suite's "more than one match" case.
    for twin in ("ptwin-a", "ptwin-b"):
        resources.append({"resourceType": "Patient", "id": twin, "name": [{"family": "Lee", "given": ["Jordan"]}],
                          "birthDate": "1990-01-01", "gender": "male"})
        resources.append({"resourceType": "Coverage", "id": "c" + twin, "status": "active", "subscriberId": "777",
                          "beneficiary": {"reference": "Patient/" + twin}, "payor": [{"display": "Springfield Health Plan"}]})
    for res in resources:
        put(token, res)
    if "--no-reference" not in sys.argv:
        for res in reference_patients() + pdex_extras():
            put(token, res)


def pdex_extras():
    """PDex 2.0.0 resources for the reference patients, carrying the must-support elements their records lack: a prior
    authorization (PDex's ExplanationOfBenefit), a Device with a url, a MedicationDispense saying whether it was substituted,
    and a Provenance naming the source's format. Built from the PDex 2.0.0 package's own examples."""
    with urllib.request.urlopen("https://packages.fhir.org/hl7.fhir.us.davinci-pdex/2.0.0", timeout=60) as r:
        raw = r.read()
    with tarfile.open(fileobj=io.BytesIO(raw if raw[:2] != b"\x1f\x8b" else gzip.decompress(raw))) as t:
        example = lambda name: json.load(t.extractfile("package/example/" + name))
        eob = example("ExplanationOfBenefit-PDexPriorAuth1.json")
        device = example("Device-543210.json")
        dispense = example("MedicationDispense-1000001.json")
        provenance = example("Provenance-1000001.json")
    for res in (eob, device, dispense, provenance):
        res.pop("text", None)

    payer = {"reference": "Organization/98301a2f-c458-4708-b567-912c2c9758ef"}
    eob.update(id="pdex-pa-85", patient={"reference": "Patient/85"}, insurer=payer, provider=payer)
    eob.pop("related", None)
    for ins in eob.get("insurance", []):
        ins["coverage"] = {"reference": "Coverage/140806f9-67ab-480e-b5a8-dab54874e367"}
    for item in eob.get("item", []):
        item["servicedDate"] = eob.get("billablePeriod", {}).get("start", "2026-09-01")[:10]
    enrich_prior_auth(eob)

    device.update(id="pdex-device-355", patient={"reference": "Patient/355"}, url="https://test.perfuse.health/devices/543210",
                  distinctIdentifier="A1B2C3", expirationDate="2030-01-01", lotNumber="LOT7", serialNumber="SN543210",
                  # US Core requires the UDI's human-readable or machine form beside the device identifier.
                  udiCarrier=[{"deviceIdentifier": "00000023366006",
                               "carrierHRF": "(01)00000023366006(17)300101(10)LOT7(21)SN543210"}])

    dispense.update(id="pdex-dispense-85", subject={"reference": "Patient/85"},
                    substitution={"wasSubstituted": False})
    dispense.pop("performer", None)
    dispense.pop("location", None)

    provenance.update(id="pdex-provenance-85", target=[{"reference": "Patient/85"}], recorded="2026-09-01T00:00:00Z")
    provenance["agent"] = [
        {"type": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/provenance-participant-type", "code": "author"}]},
         "who": payer, "onBehalfOf": payer},
        {"type": {"coding": [{"system": "http://hl7.org/fhir/us/core/CodeSystem/us-core-provenance-participant-type", "code": "transmitter"}]},
         "who": payer, "onBehalfOf": payer},
    ]
    # A second authorization entered by a practitioner's role rather than the practitioner, so both kinds of enterer occur.
    second = json.loads(json.dumps(eob))
    second.update(id="pdex-pa-85-b", enterer={"reference": "PractitionerRole/39d27808-8ecb-4bee-89ee-0a390c5fe69b"},
                  identifier=[{"system": "https://www.exampleplan.com/fhir/EOBIdentifier", "value": "PA-85-B"}])
    return [eob, second, device, dispense, provenance]


REFERENCE = "https://inferno.healthit.gov/reference-server/r4"

# CVX renamed code 140 after the reference data was generated, and the validator rejects the old display.
CVX_DISPLAY = {"140": "Influenza, split virus, trivalent, PF"}


def current_cvx_display(res):
    if res["resourceType"] == "Immunization":
        for c in res.get("vaccineCode", {}).get("coding", []):
            if c.get("system") == "http://hl7.org/fhir/sid/cvx" and c.get("code") in CVX_DISPLAY:
                c["display"] = CVX_DISPLAY[c["code"]]
    # PDex 2.0.0's MedicationDispense names the drug by NDC, as a CodeableConcept; the reference data uses RxNorm, once by
    # reference to a Medication. The NDCs are RxNav's for the same RxNorm concepts.
    if res["resourceType"] == "MedicationDispense":
        rxnorm = {"0054-4299-25": ("313988", "Furosemide 40 MG Oral Tablet"),
                  "0409-6102-10": ("1719286", "10 ML Furosemide 10 MG/ML Injection")}
        ndc = "0409-6102-10" if "medicationReference" in res else "0054-4299-25"
        res.pop("medicationReference", None)
        code, name = rxnorm[ndc]
        res["medicationCodeableConcept"] = {"coding": [
            {"system": "http://hl7.org/fhir/sid/ndc", "code": ndc},
            {"system": "http://www.nlm.nih.gov/research/umls/rxnorm", "code": code, "display": name}], "text": name}
        res.setdefault("substitution", {"wasSubstituted": False})
    # PDex's Provenance says what format the payer received the data in.
    if res["resourceType"] == "Provenance":
        res["entity"] = [{"role": "source", "what": {"display": "Clinical data received from the provider's EHR"},
                          "extension": [{"url": "http://hl7.org/fhir/us/davinci-pdex/StructureDefinition/ProvenanceSourceFrom",
                                         "valueCodeableConcept": {"coding": [{
                                             "system": "http://hl7.org/fhir/us/davinci-pdex/CodeSystem/ProvenancePayerDataSource",
                                             "code": "hl7ccda", "display": "HL7 C-CDA"}]}}]}]


def enrich_prior_auth(eob):
    """The must-support elements of PDex 2.0.0's prior authorization the package example leaves out and that a valid
    resource can carry: who entered it, where, and the care team's practitioner.

    Left out on purpose, though the profile marks them must-support: the PAS item extensions (their definitions in PAS 2.0.1
    do not allow ExplanationOfBenefit.item) and the allowed, consumed and denial-reason adjudication slices (their category
    codes are outside the value set the same profile requires), nor a header adjudication (the profile's denialreason slice
    there has no discriminator value, so the validator cannot evaluate the slicing). A resource carrying any of them fails
    validation."""
    doctor = {"reference": "Practitioner/0032d176-a8db-4f20-92c4-a7a417ad3510"}
    eob["enterer"] = doctor
    eob["facility"] = {"reference": "Location/e5269183-280b-4e90-8a00-7ecd122bc5f6"}
    role = {"reference": "PractitionerRole/39d27808-8ecb-4bee-89ee-0a390c5fe69b"}
    eob["careTeam"] = [{"sequence": 1, "provider": doctor, "responsible": True}, {"sequence": 2, "provider": role},
                       {"sequence": 3, "provider": {"reference": "Organization/98301a2f-c458-4708-b567-912c2c9758ef"}}]


def reference_patients():
    """Patients 85 and 355 of Inferno's public reference server, and everything $everything gives for them: synthetic
    (Synthea) records that between them carry every US Core 3.1.1 must-support element, which the PDex suite checks. Fetched
    when loading rather than copied into this repository. SAMPLE_TOKEN is the server's published test token."""
    found = {}
    for patient in ("85", "355"):
        url = f"{REFERENCE}/Patient/{patient}/$everything?_count=500"
        for _ in range(40):
            req = urllib.request.Request(url, headers={"Authorization": "Bearer SAMPLE_TOKEN", "Accept": "application/fhir+json"})
            with urllib.request.urlopen(req, timeout=60) as r:
                page = json.load(r)
            for e in page.get("entry", []):
                res = e["resource"]
                if res["resourceType"] != "Group":
                    found[(res["resourceType"], res["id"])] = res
                    current_cvx_display(res)
            url = next((link["url"] for link in page.get("link", []) if link["relation"] == "next"), None)
            if not url:
                break
    return list(found.values())


if __name__ == "__main__":
    main()
