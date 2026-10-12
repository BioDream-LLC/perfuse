#!/usr/bin/env python3
"""Runs Inferno's hosted test kits, at inferno.healthit.gov, against the public test instance, test.perfuse.health.

The inputs are the local runs' own (crd/run.py, dtr/run.py, pas/run.py), pointed at the public instance, whose data
deploy/test-instance/load.py put in. Nothing runs in Docker. Results go to .work/hosted-<kit>/results.json.

Usage: hosted.py <crd|dtr|pas|bulk|smart> [base URL] [public|confidential ...]
"""
import importlib.util
import json
import os
import sys
import time

os.environ.setdefault("INFERNO_URL", "https://inferno.healthit.gov/suites")
HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, HERE)
import inferno  # noqa: E402

KIT = sys.argv[1] if len(sys.argv) > 1 else ""
BASE = (sys.argv[2] if len(sys.argv) > 2 else "https://test.perfuse.health").rstrip("/")


def module(kit):
    sys.path.insert(0, os.path.join(HERE, kit))
    spec = importlib.util.spec_from_file_location(kit + "_run", os.path.join(HERE, kit, "run.py"))
    m = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(m)
    m.PUBLIC = m.LOCAL = BASE
    return m


def dtr():
    m = module("dtr")
    # Its CRD call, for a coverage assertion id, signs in as Inferno's CRD client: the public instance trusts that one.
    m.CONSOLE_TOKEN = inferno.cds_jwt(BASE + "/cds-services/crd-order-sign")
    auth = json.dumps({"auth_type": "backend_services", "token_url": BASE + "/auth/token", "client_id": "inferno-backend",
                       "requested_scopes": "system/*.rs", "encryption_algorithm": "RS384", "use_discovery": "false"})
    return inferno.run(m.SUITE, {"url": BASE + "/fhir", "backend_services_smart_auth_info": auth, **m.inputs(m.coverage(), m.adaptive())})


def crd():
    m = module("crd")
    one = lambda r: json.dumps(r)
    many = lambda r: json.dumps([r])
    return inferno.run(m.SUITE, {
        "base_url": BASE, "authentication_required": "yes", "encryption_method": "ES384",
        "any_hook_request_body": one(m.order_sign()),
        "appointment_book_request_bodies": many(m.appointment_book()),
        "order_select_request_bodies": many(m.order_select()),
        "order_dispatch_request_bodies": many(m.order_dispatch()),
        "order_sign_request_bodies": many(m.order_sign()),
        "mock_ehr_bundle": one(m.mock_ehr()),
        "technical_issues_service_ids": "crd-order-sign", "technical_issues_request_body": one(m.negative("technical")),
        "coverage_not_found_service_ids": "crd-order-sign", "coverage_not_found_request_body": one(m.negative("coverage-not-found")),
        "no_active_coverage_service_ids": "crd-order-sign", "no_active_coverage_request_body": one(m.negative("no-active")),
        "no_member_found_service_ids": "crd-order-sign", "no_member_found_request_body": one(m.negative("no-member")),
    })


def pas():
    m = module("pas")
    # The reviewer decides pended requests through the console API, with an editor token made on the instance
    # (perfuse token create) and kept in $KEYS.
    with open(os.path.join(os.environ.get("KEYS", os.path.expanduser("~/keys")), "perfuse-test-editor-token")) as f:
        m.CONSOLE_TOKEN = f.read().strip()
    scope = "system/*.rs system/Claim.c system/Subscription.cruds"
    tok = inferno.backend_token(BASE + "/auth/token", scope=scope)
    auth = json.dumps({
        "auth_type": "backend_services", "access_token": tok, "expires_in": 3600,
        "issue_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "token_url": BASE + "/auth/token",
        "client_id": "inferno-backend", "requested_scopes": scope, "encryption_algorithm": "RS384", "use_discovery": "false"})
    return inferno.run(m.SUITE, {"server_endpoint": BASE + "/fhir", "smart_credentials": auth, "access_token": "pas-notify-token",
                                 **m.inputs()}, on_waiting=m.reviewer)


def bulk():
    """Bulk Data Access v2.0.0 Server: a Group export, as Inferno's backend client."""
    auth = json.dumps({"auth_type": "backend_services", "token_url": BASE + "/auth/token", "client_id": "inferno-backend",
                       "requested_scopes": "system/*.read", "encryption_algorithm": "RS384", "use_discovery": "true"})
    return inferno.run("bulk_data_v200", {"bulk_server_url": BASE + "/fhir", "smart_auth_info": auth, "group_id": "attributed",
                                          "bulk_timeout": "180", "lines_to_validate": "100",
                                          "since_timestamp": time.strftime("%Y-%m-%dT%H:%M:%S+00:00", time.gmtime(time.time() - 86400 * 30))})


def pdex_member_match(patient, coverage_id_value, consent=True):
    """An HRex $member-match request for a member of the old payer, the shape PDex's Payer-to-Payer workflow sends."""
    params = [
        {"name": "MemberPatient", "resource": {"resourceType": "Patient", "id": "1", "identifier": [{
            "type": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/v2-0203", "code": "MB"}]},
            "system": "https://www.springfield-health-plan.example/member-id", "value": coverage_id_value}], **patient}},
        {"name": "CoverageToMatch", "resource": {
            "resourceType": "Coverage", "id": "match", "status": "active", "subscriberId": coverage_id_value,
            "beneficiary": {"reference": "Patient/1"}, "payor": [{"display": "Springfield Health Plan"}]}},
        {"name": "CoverageToLink", "resource": {
            "resourceType": "Coverage", "id": "link", "status": "active", "subscriberId": "NEW-" + coverage_id_value,
            "beneficiary": {"reference": "Patient/1"}, "payor": [{"display": "Another Health Plan"}]}},
    ]
    if consent:
        params.append({"name": "Consent", "resource": {
            "resourceType": "Consent", "id": "consent", "status": "active",
            "scope": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/consentscope", "code": "patient-privacy"}]},
            "category": [{"coding": [{"system": "http://terminology.hl7.org/CodeSystem/v3-ActCode", "code": "IDSCL"}]}],
            "patient": {"reference": "Patient/1"}, "performer": [{"reference": "Patient/1"}],
            "sourceReference": {"reference": "http://example.org/DocumentReference/consent-form"},
            "policy": [{"uri": "http://hl7.org/fhir/us/davinci-hrex/StructureDefinition-hrex-consent.html#regular"}],
            "provision": {"type": "permit", "period": {"start": "2026-01-01", "end": "2027-12-31"},
                          "actor": [{"role": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/provenance-participant-type", "code": "performer"}]},
                                     "reference": {"identifier": {"system": "http://hl7.org/fhir/sid/us-npi", "value": "9876543210"},
                                                   "display": "Springfield Health Plan"}},
                                    {"role": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/v3-ParticipationType", "code": "IRCP"}]},
                                     "reference": {"identifier": {"system": "http://hl7.org/fhir/sid/us-npi", "value": "0123456789"},
                                                   "display": "Another Health Plan"}}],
                          "action": [{"coding": [{"system": "http://terminology.hl7.org/CodeSystem/consentaction", "code": "disclose"}]}]}}})
    return json.dumps({"resourceType": "Parameters", "id": "member-match-in", "parameter": params})


def pdex(group=None):
    """Da Vinci PDex Payer Server: $member-match (one, none and several matches), then the matched member's data.

    The attestation group is left out: it asks the developer to attest, which a script must not do on anybody's behalf."""
    # A token got here rather than by Inferno: the suite reads auth_info's access_token and does not fetch one itself.
    def auth(scope):
        return json.dumps({"auth_type": "backend_services", "access_token": inferno.backend_token(BASE + "/auth/token", scope=scope),
                           "expires_in": 3600, "issue_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
                           "token_url": BASE + "/auth/token", "client_id": "inferno-backend", "requested_scopes": scope,
                           "encryption_algorithm": "RS384", "use_discovery": "false"})
    amy = {"name": [{"family": "Shaw", "given": ["Amy"]}], "birthDate": "1987-02-20", "gender": "female"}
    twin = {"name": [{"family": "Lee", "given": ["Jordan"]}], "birthDate": "1990-01-01", "gender": "male"}
    stranger = {"name": [{"family": "Nobody", "given": ["Neither"]}], "birthDate": "1950-06-06", "gender": "female"}
    inputs = {
        "url": BASE + "/fhir", "smart_auth_info": auth("system/*.read"), "bulk_auth_info": auth("system/*.read"),
        "bulk_timeout": "180", "lines_to_validate": "100", "patient_ids": "85,355",  # Inferno reference server patients, loaded by deploy/test-instance/load.py
        "member_match_request": pdex_member_match(amy, "123"),
        "no_member_match_request": pdex_member_match(stranger, "999"),
        "multiple_member_match_request": pdex_member_match(twin, "777"),
    }
    suite = "pdex_payer_server"
    return inferno.run(suite, inputs, group_id=f"{suite}-{group}" if group else None)


def smart(name):
    """smart/run.py's own driver: it signs amy and drb in at the instance's authorization pages as a browser would."""
    m = module("smart")
    m.FHIR = BASE + "/fhir"
    m.run(name)


if __name__ == "__main__":
    if KIT.startswith("smart"):
        for name in sys.argv[3:] or ["public", "confidential"]:
            smart(name)
        raise SystemExit
    if KIT == "pdex":
        for group in sys.argv[3:] or ["payer_to_payer_workflow", "api_and_ms_coverage"]:
            inferno.report(pdex(group), f"hosted-pdex-{group}")
        raise SystemExit
    runs = {"crd": crd, "dtr": dtr, "pas": pas, "bulk": bulk}
    if KIT not in runs:
        raise SystemExit(__doc__)
    inferno.report(runs[KIT](), "hosted-" + KIT)
