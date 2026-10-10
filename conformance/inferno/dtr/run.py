#!/usr/bin/env python3
"""Runs the Inferno Da Vinci DTR Payer Server v2.2.0 suite (davinci-dtr-test-kit v0.18.0) against Perfuse.

The payer data is Inferno's own dinner-order fixtures, from the kit:
- the static questionnaire and its CQL Library, from its package fixture, with the compiled ELM DTR asks for beside each CQL
  expression (the fixture's items do not say where it is);
- the adaptive questionnaire, Inferno's template with its private inclusion-criteria FHIRPath rewritten as the standard
  enableWhen it expresses, so Perfuse's $next-question answers from the questionnaire alone;
- questionnaire.py, a questionnaire that uses every element DTR marks must-support, and its answer ValueSets.

The package requests ask by questionnaire, by CRD's coverage assertion id (from Perfuse's own CRD, on the same rules) and by the
order CRD annotated.
"""
import copy
import gzip
import io
import json
import os
import sys
import tarfile
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, ".."))
sys.path.insert(0, HERE)
import inferno  # noqa: E402
from questionnaire import BIG_VALUESET, QUESTIONNAIRE, VALUESET  # noqa: E402

SUITE = "dtr_payer_server_v220"
PORT = 8473
PUBLIC = f"https://host.docker.internal:{PORT}"
LOCAL = f"https://127.0.0.1:{PORT}"
FIXTURES = os.path.join(inferno.kit_path("dtr"), "lib", "davinci_dtr_test_kit", "full_ehr", "fixtures")
ALT_URL = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/alternativeExpression"
CONSOLE_TOKEN = os.environ.get("PERFUSE_TOKEN", "")


def fixture(path):
    with open(os.path.join(FIXTURES, path)) as f:
        return json.load(f)


def coverage():
    """CoverageExample from the published DTR 2.2.0 package."""
    with urllib.request.urlopen("https://packages.fhir.org/hl7.fhir.us.davinci-dtr/2.2.0", timeout=60) as r:
        raw = r.read()
    with tarfile.open(fileobj=io.BytesIO(raw if raw[:2] != b"\x1f\x8b" else gzip.decompress(raw))) as t:
        cov = json.load(t.extractfile("package/example/Coverage-CoverageExample.json"))
    cov.pop("text", None)
    cov["id"] = "cov1"
    return cov


def put(token, res):
    req = urllib.request.Request(f"{LOCAL}/fhir/{res['resourceType']}/{res['id']}", json.dumps(res).encode(),
                                 {"Authorization": "Bearer " + token, "Content-Type": "application/fhir+json"}, method="PUT")
    try:
        with urllib.request.urlopen(req, context=inferno.INSECURE, timeout=30) as r:
            status = r.status
    except urllib.error.HTTPError as e:
        raise SystemExit(f"PUT {res['resourceType']}/{res['id']}: {e.code} {e.read()[:300]}")
    print("loaded", res["resourceType"], res["id"], status)


def add_elm(res):
    def fix(exts):
        for x in exts or []:
            v = x.get("valueExpression")
            if v and v.get("language", "").startswith("text/cql") and not any(e["url"] == ALT_URL for e in v.get("extension", [])):
                v.setdefault("extension", []).append({"url": ALT_URL, "valueExpression": {
                    "language": "application/elm+json", "expression": v["expression"],
                    "reference": "https://madie.cms.gov/Library/DTRTest|0.3.000"}})
            fix(x.get("extension"))

    def walk(items):
        for it in items or []:
            fix(it.get("extension"))
            walk(it.get("item"))
    fix(res.get("extension"))
    walk(res.get("item"))
    return res


def adaptive():
    src = fixture("dinner_adaptive/dinner_order_adaptive_next_question_template.json")
    src["url"] = "urn:inferno:dtr-test-kit:dinner-order-adaptive"
    src["id"] = "DinnerOrderAdaptive"
    src.pop("meta", None)
    src["version"] = "1.0.0"
    src["extension"].append({"url": "http://hl7.org/fhir/StructureDefinition/artifact-versionAlgorithm",
                             "valueCoding": {"system": "http://hl7.org/fhir/version-algorithm", "code": "semver"}})
    src["effectivePeriod"] = {"start": "2026-01-01"}
    rules = {
        "3": [{"question": "LOC.1", "operator": "exists", "answerBoolean": True}],
        "3.2.a": [{"question": "3.1", "operator": "=", "answerCoding": {"code": "Hamburger"}}],
        "3.2.b": [{"question": "3.1", "operator": "=", "answerCoding": {"code": "Bean Burrito"}}],
        "3.3": [{"question": "3.1", "operator": "exists", "answerBoolean": True}],
    }

    def rewrite(items):
        for it in items:
            ext = [x for x in it.get("extension", []) if x["url"] != "urn:inferno:dtr:inclusion-criteria"]
            if ext:
                it["extension"] = ext
            else:
                it.pop("extension", None)
            if it["linkId"] in rules:
                it["enableWhen"] = rules[it["linkId"]]
            rewrite(it.get("item", []))
    rewrite(src["item"])
    return add_elm(src)


def crd_order():
    """Asks Perfuse's CRD for coverage on a dinner order; returns (coverage assertion id, the order CRD annotated)."""
    order = {"resourceType": "ServiceRequest", "id": "dinner-order", "status": "draft", "intent": "order", "authoredOn": "2026-10-05",
             "code": {"coding": [{"system": "urn:inferno:dtr-test-kit:codes", "code": "DINNER", "display": "Dinner"}]},
             "subject": {"reference": "Patient/pat015"}, "requester": {"reference": "Practitioner/pra1234"},
             "insurance": [{"reference": "Coverage/cov1"}]}
    cov = {"resourceType": "Coverage", "id": "cov1", "status": "active", "beneficiary": {"reference": "Patient/pat015"},
           "payor": [{"reference": "Organization/plan"}]}
    hook = {"hookInstance": "dinner-1", "hook": "order-sign",
            "context": {"userId": "Practitioner/pra1234", "patientId": "pat015",
                        "draftOrders": {"resourceType": "Bundle", "type": "collection", "entry": [{"resource": order}]}},
            "prefetch": {"patient": {"resourceType": "Patient", "id": "pat015"},
                         "coverage": {"resourceType": "Bundle", "type": "searchset", "entry": [{"resource": cov}]}}}
    req = urllib.request.Request(LOCAL + "/cds-services/crd-order-sign", json.dumps(hook).encode(),
                                 {"Content-Type": "application/json", "Authorization": "Bearer " + CONSOLE_TOKEN})
    with urllib.request.urlopen(req, context=inferno.INSECURE, timeout=30) as r:
        resp = json.load(r)
    actions = resp.get("systemActions", []) + [a for c in resp.get("cards", []) for s in c.get("suggestions", []) for a in s.get("actions", [])]
    for action in actions:
        res = action.get("resource", {})
        for ext in res.get("extension", []):
            if ext["url"].endswith("ext-coverage-information"):
                for sub in ext["extension"]:
                    if sub["url"] == "coverage-assertion-id":
                        return sub["valueString"], res
    raise SystemExit("CRD returned no coverage-assertion-id: " + json.dumps(resp)[:800])


def inputs(cov, src):
    asked = [{"resourceType": "Parameters", "parameter": [{"name": "coverage", "resource": cov}, {"name": "questionnaire", "valueCanonical": url}]}
             for url in ["urn:inferno:dtr-test-kit:dinner-order-static", "urn:inferno:dtr-test-kit:dinner-order-adaptive",
                         "urn:inferno:dtr-test-kit:dinner-order-full"]]
    assertion, order = crd_order()
    asked.append({"resourceType": "Parameters", "parameter": [{"name": "coverage", "resource": cov}, {"name": "context", "valueString": assertion}]})
    asked.append({"resourceType": "Parameters", "parameter": [{"name": "coverage", "resource": cov}, {"name": "order", "resource": order}]})

    # Answers Inferno copies into its $next-question requests, by linkId: a hamburger order.
    template = {
        "resourceType": "QuestionnaireResponse", "status": "in-progress", "questionnaire": "#DinnerOrderAdaptive",
        "contained": [{"resourceType": "Questionnaire", "id": "DinnerOrderAdaptive", "status": "active",
                       "url": "urn:inferno:dtr-test-kit:dinner-order-adaptive", "version": "1.0.0"}],
        "item": [
            {"linkId": "PBD", "item": [{"linkId": "PBD.1", "answer": [{"valueString": "Oster"}]},
                                       {"linkId": "PBD.2", "answer": [{"valueString": "William"}]}]},
            {"linkId": "LOC", "item": [{"linkId": "LOC.1", "answer": [{"valueString": "Room 202"}]}]},
            {"linkId": "3", "item": [{"linkId": "3.1", "answer": [{"valueCoding": {"code": "Hamburger"}}]},
                                     {"linkId": "3.2.a", "answer": [{"valueCoding": {"code": "Ketchup"}}]},
                                     {"linkId": "3.3", "answer": [{"valueString": "None"}]}]}]}

    # The negative cases: a coverage assertion this server never issued, and an answer the questionnaire does not offer.
    shell = {k: v for k, v in src.items() if k != "item"}
    shell["item"] = [copy.deepcopy(src["item"][2])]
    shell["item"][0]["item"] = [shell["item"][0]["item"][0]]
    return {
        "questionnaire_package_request_parameters": json.dumps(asked),
        "questionnaire_response_templates": json.dumps([template]),
        "source_data_error_request": json.dumps({"resourceType": "Parameters", "parameter": [
            {"name": "coverage", "resource": cov}, {"name": "context", "valueString": "no-such-coverage-assertion"}]}),
        "invalid_questionnaire_response": json.dumps({
            "resourceType": "QuestionnaireResponse", "status": "in-progress", "questionnaire": "#DinnerOrderAdaptive",
            "contained": [shell], "item": [{"linkId": "3", "item": [{"linkId": "3.1", "answer": [{"valueCoding": {"code": "Pizza"}}]}]}]}),
        "questionnaire_canonical": "urn:inferno:dtr-test-kit:dinner-order-static",
    }


def main():
    token = inferno.backend_token(PUBLIC + "/auth/token", local_url=LOCAL + "/auth/token")
    for e in fixture("dinner_static/parameters_questionnaire_dinner_order_static.json")["parameter"][0]["resource"]["entry"]:
        put(token, add_elm(e["resource"]))
    src = adaptive()
    put(token, src)
    for res in (VALUESET, BIG_VALUESET, QUESTIONNAIRE):
        put(token, res)

    auth = json.dumps({"auth_type": "backend_services", "token_url": PUBLIC + "/auth/token", "client_id": "inferno-backend",
                       "requested_scopes": "system/*.rs system/Questionnaire.rs", "encryption_algorithm": "RS384", "use_discovery": "false"})
    sid = inferno.run(SUITE, {"url": PUBLIC + "/fhir", "backend_services_smart_auth_info": auth, **inputs(coverage(), src)})
    inferno.report(sid, "dtr")


if __name__ == "__main__":
    main()
