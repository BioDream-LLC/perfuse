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
import importlib.util
import json
import os
import subprocess
import sys
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
    for res in resources:
        put(token, res)


if __name__ == "__main__":
    main()
