#!/usr/bin/env python3
"""Runs the Inferno Da Vinci CRD Server v2.2.1 suite (davinci-crd-test-kit v0.14.2) against Perfuse's CDS Hooks services.

The hook requests are the kit's own complete-prefetch requests (execution_scripts/prefetch), with these changes:
- Orders carry codes the payer's rules (crd/rules.yaml) answer: home oxygen (HCPCS E0424, one written with the https:// HCPCS
  URI CRD 2.2.1 uses), a physical therapy evaluation and pulse oximetry (CPT). The kit's orders are all "Implant Pacemaker",
  which a payer with no rule for it can only answer with "no rule matched".
- order-select leaves CommunicationRequest out of `selections`: the kit's own sample fails its context check with it there.
- appointment-book gives the appointment a start and end, which crd-apt1 requires and the kit's sample lacks.
- order-dispatch names the performer, which the hook's context requires.
- The negative cases: no coverage in prefetch and an EHR that cannot be reached (technical issue), a subscriber id the payer
  does not know (coverage not found), a cancelled coverage (no active coverage), and a stranger (no member found).

Inferno's simulated EHR holds the resources those requests refer to, so Perfuse can fetch what prefetch leaves out. Perfuse's
own FHIR store holds the payer's member records (members: fhir): Amy Shaw, her coverage and an approved authorization for home
oxygen.
"""
import copy
import json
import os
import sys
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, ".."))
import inferno  # noqa: E402

SUITE = "crd_server_v221"
PORT = 8473
PUBLIC = f"https://host.docker.internal:{PORT}"
LOCAL = f"https://127.0.0.1:{PORT}"
PREFETCH = os.path.join(inferno.kit_path("crd"), "execution_scripts", "prefetch")
HCPCS = "http://www.cms.gov/Medicare/Coding/HCPCSReleaseCodeSets"
CONSOLE_TOKEN = os.environ.get("PERFUSE_TOKEN", "")

OXYGEN = {"coding": [{"system": HCPCS, "code": "E0424"}]}
OXYGEN_HTTPS = {"coding": [{"system": HCPCS.replace("http://", "https://"), "code": "E0424",
                            "display": "Stationary compressed gaseous oxygen system, rental"}]}


def kit(hook):
    with open(os.path.join(PREFETCH, f"{hook}-request_complete-prefetch.json")) as f:
        return json.load(f)


def oxygen(req, code=OXYGEN):
    """The kit's DeviceRequest that names its device by reference, ordering home oxygen instead."""
    dr = req["context"]["draftOrders"]["entry"][3]["resource"]
    assert dr["resourceType"] == "DeviceRequest", dr["resourceType"]
    dr.pop("codeReference", None)
    dr["codeCodeableConcept"] = copy.deepcopy(code)
    return req


def order_sign():
    req = oxygen(kit("order-sign"))
    entries = req["context"]["draftOrders"]["entry"]
    for i, (code, text) in {9: ("97161", "Physical therapy evaluation, low complexity"), 10: ("94760", "Pulse oximetry, single determination")}.items():
        entries[i]["resource"]["code"]["coding"] = [{"system": "http://www.ama-assn.org/go/cpt", "code": code}]
        entries[i]["resource"]["code"]["text"] = text
    return req


def order_select():
    req = kit("order-select")
    req["context"]["selections"] = [s for s in req["context"]["selections"] if not s.startswith("CommunicationRequest/")]
    return req


def appointment_book():
    req = kit("appointment-book")
    appt = req["context"]["appointments"]["entry"][0]["resource"]
    appt["start"], appt["end"] = "2026-11-02T09:00:00-05:00", "2026-11-02T09:30:00-05:00"
    return req


def order_dispatch():
    req = kit("order-dispatch")
    req["context"]["performer"] = "Practitioner/example"
    return req


def negative(kind):
    req = oxygen(kit("order-sign"), OXYGEN_HTTPS)
    cov = req["prefetch"]["coverage"]["entry"][0]["resource"]
    if kind == "technical":
        del req["prefetch"]["coverage"]  # Perfuse must fetch it, from an EHR (https://example/r4) that is not there
    elif kind == "coverage-not-found":
        cov["subscriberId"] = "UNKNOWN"
    elif kind == "no-active":
        cov["status"] = "cancelled"
    elif kind == "no-member":
        cov["subscriberId"] = "UNKNOWN"
        req["prefetch"]["patient"].update({"birthDate": "1990-01-01", "name": [{"family": "Stranger", "given": ["Pat"]}]})
    return req


def mock_ehr():
    """Every resource the kit's prefetch requests carry, the oxygen order among them, for Inferno's simulated EHR to serve. The
    appointment the kit's appointment-book request books, and those it names, are left out: Perfuse must take them from the
    request itself."""
    left_out = {("Appointment", "forprefetch-pra"), ("Location", "app-par-act"), ("Organization", "app-par-act-org"),
                ("Practitioner", "app-par-act"), ("Practitioner", "app-par-act-pra"), ("PractitionerRole", "app-par-act")}
    pool = {}

    def collect(x):
        if isinstance(x, dict):
            if "resourceType" in x and "id" in x and x["resourceType"] != "Bundle":
                pool.setdefault((x["resourceType"], x["id"]), x)
            for v in x.values():
                collect(v)
        elif isinstance(x, list):
            for v in x:
                collect(v)
    for name in sorted(os.listdir(PREFETCH)):
        if name.endswith(".json"):
            with open(os.path.join(PREFETCH, name)) as f:
                collect(json.load(f))
    pool[("DeviceRequest", "forprefetch-rol")] = oxygen(kit("order-sign"), OXYGEN_HTTPS)["context"]["draftOrders"]["entry"][3]["resource"]
    return {"resourceType": "Bundle", "type": "collection",
            "entry": [{"resource": r} for k, r in sorted(pool.items()) if k not in left_out]}


MEMBERS = [
    {"resourceType": "Patient", "id": "pmember", "birthDate": "1987-02-20", "gender": "female", "name": [{"family": "Shaw", "given": ["Amy"]}]},
    {"resourceType": "Coverage", "id": "cmember", "status": "active", "subscriberId": "123", "beneficiary": {"reference": "Patient/pmember"},
     "payor": [{"display": "Springfield Health Plan"}]},
    {"resourceType": "ClaimResponse", "id": "pa1", "status": "active", "use": "preauthorization", "outcome": "complete", "created": "2026-09-01",
     "type": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/claim-type", "code": "professional"}]},
     "patient": {"reference": "Patient/pmember"}, "insurer": {"display": "Springfield Health Plan"}, "preAuthRef": "AUTH-778",
     "preAuthPeriod": {"start": "2026-09-01", "end": "2027-03-01"},
     "addItem": [{"productOrService": {"coding": [{"system": HCPCS, "code": "E0424"}]}}]},
]


def load_members():
    for res in MEMBERS:
        req = urllib.request.Request(f"{LOCAL}/fhir/{res['resourceType']}/{res['id']}", json.dumps(res).encode(),
                                     {"Authorization": "Bearer " + CONSOLE_TOKEN, "Content-Type": "application/fhir+json"}, method="PUT")
        try:
            with urllib.request.urlopen(req, context=inferno.INSECURE, timeout=30) as r:
                print("loaded", res["resourceType"], res["id"], r.status)
        except urllib.error.HTTPError as e:
            raise SystemExit(f"loading {res['resourceType']}/{res['id']}: {e.code} {e.read()[:300]}")


def main():
    load_members()
    one = lambda r: json.dumps(r)
    many = lambda r: json.dumps([r])
    sid = inferno.run(SUITE, {
        "base_url": PUBLIC,  # the kit adds /cds-services
        "authentication_required": "yes",
        "encryption_method": "ES384",
        "any_hook_request_body": one(order_sign()),
        "appointment_book_request_bodies": many(appointment_book()),
        "order_select_request_bodies": many(order_select()),
        "order_dispatch_request_bodies": many(order_dispatch()),
        "order_sign_request_bodies": many(order_sign()),
        "mock_ehr_bundle": one(mock_ehr()),
        "technical_issues_service_ids": "crd-order-sign",
        "technical_issues_request_body": one(negative("technical")),
        "coverage_not_found_service_ids": "crd-order-sign",
        "coverage_not_found_request_body": one(negative("coverage-not-found")),
        "no_active_coverage_service_ids": "crd-order-sign",
        "no_active_coverage_request_body": one(negative("no-active")),
        "no_member_found_service_ids": "crd-order-sign",
        "no_member_found_request_body": one(negative("no-member")),
    })
    inferno.report(sid, "crd")


if __name__ == "__main__":
    main()
