#!/usr/bin/env python3
"""Runs Inferno's hosted test kits, at inferno.healthit.gov, against the public test instance, test.perfuse.health.

The inputs are the local runs' own (crd/run.py, dtr/run.py, pas/run.py), pointed at the public instance, whose data
deploy/test-instance/load.py put in. Nothing runs in Docker. Results go to .work/hosted-<kit>/results.json.

Usage: hosted.py <crd|dtr|pas|smart> [base URL] [public|confidential ...]
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
    runs = {"crd": crd, "dtr": dtr, "pas": pas}
    if KIT not in runs:
        raise SystemExit(__doc__)
    inferno.report(runs[KIT](), "hosted-" + KIT)
