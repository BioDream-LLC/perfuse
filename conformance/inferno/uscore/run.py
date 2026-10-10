#!/usr/bin/env python3
"""Runs the Inferno US Core Server v7.0.0 suite (us-core-test-kit v1.1.6, SMART App Launch 2) against Perfuse, Perfuse issuing
the tokens itself, and plays the browser through Perfuse's sign-in and consent pages as the test patient (users.yaml: amy, with
the throwaway password "correct horse", signed in as Patient/85).

The data is Inferno's own reference server patient bundles (inferno-reference-server-data), patients 85 and 355, each resource
PUT under its own id with its urn:uuid references rewritten to Type/id, as the transaction would have resolved them.

Usage: run.py [step,...]   steps: launch, api, sysapi, g1launch, g1, g2launch, g2 (default: all, in that order)
"""
import collections
import json
import os
import re
import subprocess
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, ".."))
import inferno  # noqa: E402

SUITE = "us_core_v700"
PORT = 8474
PUBLIC = f"https://host.docker.internal:{PORT}"
LOCAL = f"https://127.0.0.1:{PORT}"
FHIR = PUBLIC + "/fhir"
REFDATA = ("https://github.com/inferno-framework/inferno-reference-server-data.git", "d3618f8")


class NoRedirect(urllib.request.HTTPRedirectHandler):
    def redirect_request(self, *a, **k):
        return None


browser = urllib.request.build_opener(NoRedirect, urllib.request.HTTPSHandler(context=inferno.INSECURE))


def go(url, form=None):
    data = urllib.parse.urlencode(form, doseq=True).encode() if form is not None else None
    try:
        r = browser.open(url.replace(PUBLIC, LOCAL), data)
        return r.status, r.read().decode(), r.headers
    except urllib.error.HTTPError as e:
        return e.code, e.read().decode(), e.headers


def authorize(url):
    """Follows Inferno's authorization link to Perfuse's sign-in page, signs in, grants every scope offered and returns to Inferno."""
    st, page, h = go(url)
    while st == 302 and "/auth/authorize" not in h["Location"]:
        st, page, h = go(h["Location"])
    if st == 302:
        st, page, h = go(h["Location"])
    req = re.search(r'name="req" value="([^"]+)"', page).group(1)
    st, page, h = go(LOCAL + "/auth/signin", {"req": req, "username": "amy", "password": "correct horse"})
    scopes = [s.replace("&amp;", "&") for s in re.findall(r'name="scope" value="([^"]+)"', page)]
    st, page, h = go(LOCAL + "/auth/consent", {"req": req, "action": "allow", "scope": scopes})
    st, _, _ = go(h["Location"])
    print("  signed in and granted; Inferno answered", st, flush=True)


def refdata():
    path = os.path.join(inferno.WORK, "refdata")
    if not os.path.isdir(path):
        subprocess.run(["git", "clone", "-q", REFDATA[0], path], check=True)
    subprocess.run(["git", "-C", path, "-c", "advice.detachedHead=false", "checkout", "-q", REFDATA[1]], check=True)
    return os.path.join(path, "resources")


def load(token):
    resources, urls = [], {}
    for name in ["uscore_bundle_patient_85.json", "uscore_bundle_patient_355.json"]:
        with open(os.path.join(refdata(), name)) as f:
            for e in json.load(f)["entry"]:
                r = e["resource"]
                urls[e["fullUrl"]] = r["resourceType"] + "/" + r["id"]
                resources.append(r)

    def fix(n):
        if isinstance(n, dict):
            return {k: (urls.get(v, v) if k == "reference" and isinstance(v, str) else fix(v)) for k, v in n.items()}
        if isinstance(n, list):
            return [fix(x) for x in n]
        return n
    order = ["Organization", "Location", "Endpoint", "Practitioner", "PractitionerRole", "Patient", "RelatedPerson", "Encounter", "Device", "Medication"]
    resources.sort(key=lambda r: order.index(r["resourceType"]) if r["resourceType"] in order else 99)
    counts = collections.Counter()
    for r in map(fix, resources):
        req = urllib.request.Request(f"{LOCAL}/fhir/{r['resourceType']}/{r['id']}", json.dumps(r).encode(), method="PUT",
                                     headers={"Authorization": "Bearer " + token, "Content-Type": "application/fhir+json"})
        try:
            with urllib.request.urlopen(req, context=inferno.INSECURE, timeout=30) as resp:
                counts[resp.status] += 1
        except urllib.error.HTTPError as e:
            counts[e.code] += 1
            print("  not loaded:", r["resourceType"], r["id"], e.code, e.read()[:200])
    print("loaded", dict(counts), flush=True)


def public_auth(scopes):
    return json.dumps({"auth_type": "public", "use_discovery": "true", "client_id": "inferno-public", "requested_scopes": scopes,
                       "pkce_support": "enabled", "pkce_code_challenge_method": "S256", "auth_request_method": "GET"})


BASE = "launch/patient openid fhirUser offline_access"
G1 = " ".join([BASE,
               "patient/Condition.rs?category=http://terminology.hl7.org/CodeSystem/condition-category|encounter-diagnosis",
               "patient/Condition.rs?category=http://hl7.org/fhir/us/core/CodeSystem/condition-category|health-concern",
               "patient/Observation.rs?category=http://terminology.hl7.org/CodeSystem/observation-category|laboratory",
               "patient/Observation.rs?category=http://terminology.hl7.org/CodeSystem/observation-category|social-history"])
G2 = " ".join([BASE,
               "patient/Condition.rs?category=http://terminology.hl7.org/CodeSystem/condition-category|problem-list-item",
               "patient/Observation.rs?category=http://terminology.hl7.org/CodeSystem/observation-category|vital-signs",
               "patient/Observation.rs?category=http://terminology.hl7.org/CodeSystem/observation-category|survey",
               "patient/Observation.rs?category=http://hl7.org/fhir/us/core/CodeSystem/us-core-category|sdoh"])
STEPS = {
    "launch": ("us_core_v700-us_core_v700_smart_app_launch_stu2-us_core_smart_standalone_launch_stu2",
               {"url": FHIR, "smart_auth_info": public_auth(BASE + " patient/*.rs")}),
    # Under the patient token the launch step granted (kept in the session).
    "api": ("us_core_v700-us_core_v700_fhir_api", {"url": FHIR, "patient_ids": "85"}),
    # The same group as a backend-services client: no patient context, both of the data's patients.
    "sysapi": ("us_core_v700-us_core_v700_fhir_api", {"url": FHIR, "patient_ids": "85,355"}),
    "g1launch": ("us_core_v700-us_core_v700_smart_granular_scopes-Group01-Group01-us_core_smart_standalone_launch_stu2",
                 {"url": FHIR, "granular_scopes_1_auth_info": public_auth(G1)}),
    "g1": ("us_core_v700-us_core_v700_smart_granular_scopes-Group01-us_core_v700_smart_granular_scopes_1", {"url": FHIR, "patient_ids": "85"}),
    "g2launch": ("us_core_v700-us_core_v700_smart_granular_scopes-Group02-Group01-us_core_smart_standalone_launch_stu2",
                 {"url": FHIR, "granular_scopes_2_auth_info": public_auth(G2)}),
    "g2": ("us_core_v700-us_core_v700_smart_granular_scopes-Group02-us_core_v700_smart_granular_scopes_2", {"url": FHIR, "patient_ids": "85"}),
}


def main():
    steps = (sys.argv[1] if len(sys.argv) > 1 else ",".join(STEPS)).split(",")
    system = lambda: inferno.backend_token(PUBLIC + "/auth/token", scope="system/*.cruds", local_url=LOCAL + "/auth/token")
    load(system())
    new_session = lambda: inferno.call("POST", f"/test_sessions?test_suite_id={SUITE}",
                                       {"suite_options": [{"id": "smart_app_launch_version", "value": "smart_app_launch_2"}]})["id"]
    sid = new_session()
    # The backend-services run of the API group has a session of its own: the granular-scope groups compare what their
    # tokens return with the patient-token API run in the same session, and a system token over both patients is not that.
    system_sid = new_session()
    for step in steps:
        gid, inputs = STEPS[step]
        run_sid = system_sid if step == "sysapi" else sid
        if step == "sysapi":
            inputs = dict(inputs, smart_auth_info=json.dumps({"auth_type": "public", "access_token": system(), "use_discovery": "false"}))
        if step in ("g1", "g2"):
            data = {d["name"]: d["value"] for d in inferno.call("GET", f"/test_sessions/{sid}/session_data")}
            n = step[1]
            inputs = dict(inputs, **{f"granular_scopes_{n}_auth_info": data.get(f"granular_scopes_{n}_auth_info"),
                                     "received_scopes": data.get("standalone_received_scopes") or data.get("received_scopes")})
        rid = inferno.call("POST", "/test_runs", {"test_session_id": run_sid, "test_group_id": gid,
                                                  "inputs": [{"name": k, "value": v} for k, v in inputs.items()]})["id"]
        print("step", step, flush=True)
        handled, t0 = set(), time.time()
        while time.time() - t0 < 1800:
            if inferno.call("GET", f"/test_runs/{rid}")["status"] in ("done", "cancelled"):
                break
            for res in inferno.call("GET", f"/test_sessions/{run_sid}/results"):
                if res["result"] == "wait" and res["id"] not in handled:
                    handled.add(res["id"])
                    links = re.findall(r"\((https?://[^)\s]+)\)", res.get("result_message") or "")
                    if links:
                        authorize(links[0])
            time.sleep(2)
        tests = [r for r in inferno.call("GET", f"/test_sessions/{run_sid}/results") if (r.get("test_id") or "").startswith(gid)]
        print(" ", step, dict(collections.Counter(r["result"] for r in tests)), flush=True)
    inferno.report(sid, "uscore")
    if "sysapi" in steps:
        inferno.report(system_sid, "uscore-system")


if __name__ == "__main__":
    main()
