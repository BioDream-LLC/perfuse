#!/usr/bin/env python3
"""Runs the Inferno Da Vinci PAS Server v2.2.1 suite (davinci-pas-test-kit v0.15.2) against Perfuse.

Perfuse serves FHIR, PAS, the CRD rules (pas/rules.yaml, which decide each request) and R5 backport subscriptions, and issues
the suite's SMART Backend Services tokens itself. While Inferno waits for a pended request's result, this script plays the
payer's reviewer: it approves the request with Claim/$decide, which is what makes Perfuse send the notification.

The request Bundles are the kit's own preset. Inferno's simulated payer is told what to answer, so its approval, denial and
pended Bundles are identical; a real payer decides from what is asked, so here the denial asks for dialysis (76) and the pended
request for surgery (2).
"""
import copy
import json
import os
import re
import sys
import time
import urllib.error
import urllib.request

sys.path.insert(0, os.path.join(os.path.dirname(os.path.abspath(__file__)), ".."))
import inferno  # noqa: E402

SUITE = "davinci_pas_server_suite_v221"
PORT = 8473
PUBLIC = f"https://host.docker.internal:{PORT}"
LOCAL = f"https://127.0.0.1:{PORT}"
X12 = "https://codesystem.x12.org/005010/1365"


def preset():
    text = open(os.path.join(inferno.kit_path("pas"), "config", "presets", "pas_server_v221_run_against_pas_client.json.erb")).read()
    values = {i["name"]: i.get("value") for i in json.loads(re.sub(r"<%=.*?%>", "", text))["inputs"]}
    return {k: json.loads(v) if isinstance(v, str) and v[:1] in "{[" else v for k, v in values.items()}


def code(item, c, display):
    item["productOrService"] = {"coding": [{"system": X12, "code": c, "display": display}]}


def recode(bundle, c, display):
    b = copy.deepcopy(bundle)
    for item in b["entry"][0]["resource"]["item"]:
        code(item, c, display)
    return b


def extra_must_support(base):
    """Requests that draw the rest of PAS's response elements from the rules: a quantity limit and an alternative (modified,
    with an added item), a request pended for documentation (Task, CommunicationRequest) and a line naming no service (an
    error)."""
    out = []
    b = copy.deepcopy(base)
    items = b["entry"][0]["resource"]["item"]
    code(items[0], "PT", "Physical Therapy")
    items[0]["quantity"] = {"value": 12, "unit": "visit"}
    code(items[1], "62", "MRI/CAT Scan")
    out.append(b)
    out.append(recode(base, "2", "Surgical"))
    b = copy.deepcopy(base)
    it = b["entry"][0]["resource"]["item"][1]
    it["productOrService"] = {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/data-absent-reason", "code": "not-applicable"}]}
    it["extension"] = [e for e in it["extension"] if not e["url"].endswith("extension-requestedService")]
    out.append(b)
    return out


def inputs():
    p = preset()
    sub = copy.deepcopy(p["subscription_resource"])
    # Without the sample's own Authorization header: Inferno adds the one it checks, and a receiver reading the first of two
    # reads the sample's.
    sub["channel"].pop("header", None)
    sub.pop("end", None)
    v = {
        "approval_pa_submit_request_payload": p["approval_pa_submit_request_payload"],
        "denial_pa_submit_request_payload": recode(p["denial_pa_submit_request_payload"], "76", "Dialysis"),
        "pended_pa_submit_request_payload": recode(p["pended_pa_submit_request_payload"], "2", "Surgical"),
        "must_support_pa_submit_request_payload": p["must_support_pa_submit_request_payload"]
        + extra_must_support(p["must_support_pa_submit_request_payload"][1]),
        "must_support_pa_inquire_request_payload": p["must_support_pa_inquire_request_payload"],
        "subscription_resource": sub,
    }
    for n in ["initial", "add_item", "modify_cancel", "cancel_all"]:
        v[f"claim_update_{n}_submit_payload"] = p[f"claim_update_{n}_submit_payload"]
    return {k: json.dumps(x) for k, x in v.items()}


def token():
    return inferno.backend_token(PUBLIC + "/auth/token", local_url=LOCAL + "/auth/token")


decided = set()
CONSOLE_TOKEN = os.environ.get("PERFUSE_TOKEN", "")


def console(method, path, body=None):
    req = urllib.request.Request(LOCAL + path, data=json.dumps(body).encode() if body is not None else None, method=method,
                                 headers={"Content-Type": "application/json", "Authorization": "Bearer " + CONSOLE_TOKEN,
                                          "X-Perfuse-Request": "1"})
    try:
        with urllib.request.urlopen(req, context=inferno.INSECURE, timeout=30) as r:
            return r.status, json.load(r)
    except urllib.error.HTTPError as e:
        return e.code, e.read()[:300]


def reviewer(run_id):
    """Approves the newest pended request not yet decided, from the reviewer queue (Prior auth -> Review in the console); with
    none left, tells Inferno every notification has been sent."""
    time.sleep(8)
    if inferno.call("GET", f"/test_runs/{run_id}")["status"] != "waiting":
        return
    st, out = console("GET", "/api/pas/cases")
    todo = [c["id"] for c in sorted(out.get("cases", []) if isinstance(out, dict) else [], key=lambda c: c["created"], reverse=True)
            if c.get("pended") and c["id"] not in decided]
    if todo:
        decided.add(todo[0])
        st, _ = console("POST", f"/api/pas/cases/{todo[0]}/decide",
                        {"decision": "approve", "reason": "Approved by the plan's reviewer after reading the attached documentation."})
        print("reviewer approved", todo[0], st, flush=True)
        return
    u = "http://localhost/custom/subscriptions_r5_backport_r4_server/resume_pass?token=notification%20pas-notify-token"
    try:
        print("confirmed the notifications", urllib.request.urlopen(u, timeout=30).status, flush=True)
    except Exception as e:  # noqa: BLE001 - reported, and the run times out on its own
        print("confirming the notifications failed:", e, flush=True)


def main():
    tok = token()
    auth = json.dumps({
        "auth_type": "backend_services", "access_token": tok, "expires_in": 3600,
        "issue_time": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()), "token_url": PUBLIC + "/auth/token",
        "client_id": "inferno-backend", "requested_scopes": "system/*.cruds", "encryption_algorithm": "RS384", "use_discovery": "false"})
    sid = inferno.run(SUITE, {"server_endpoint": PUBLIC + "/fhir", "smart_credentials": auth, "access_token": "pas-notify-token", **inputs()},
                      on_waiting=reviewer)
    inferno.report(sid, "pas")


if __name__ == "__main__":
    main()
