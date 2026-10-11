#!/usr/bin/env python3
"""Runs Inferno's SMART App Launch STU2.2 suite (smart-app-launch-test-kit v1.0.3) against Perfuse's own authorization server,
and plays the browser: it follows Inferno's authorization links, signs in through Perfuse's forms (users.yaml, password
"correct horse"), picks the patient, approves the scopes and returns to Inferno. For the EHR launch it opens Inferno's launch
URL from Perfuse's /auth/launch, as a clinician starting the app from a chart.

Two sessions: Inferno as a public app (standalone launch, EHR launch, Backend Services, token introspection), then as a
confidential-symmetric app (the same less Backend Services, which has no client secret).

Usage: run.py [public|confidential ...]   (default: both)
"""
import base64
import collections
import json
import os
import re
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
sys.path.insert(0, os.path.join(HERE, ".."))
import inferno  # noqa: E402

SUITE = "smart_stu2_2"
PORT = 8474
PUBLIC = f"https://host.docker.internal:{PORT}"
LOCAL = f"https://127.0.0.1:{PORT}"
FHIR = PUBLIC + "/fhir"
PASSWORD = "correct horse"

RUNS = {
    "public": ("inferno-public", "public",
               ["smart_full_standalone_launch", "smart_full_ehr_launch", "smart_backend_services", "smart_token_introspection_stu2_2"]),
    "confidential": ("inferno-confidential", "symmetric",
                     ["smart_full_standalone_launch", "smart_full_ehr_launch", "smart_token_introspection_stu2_2"]),
}


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


def field(page, name):
    return re.search(r'name="%s" value="([^"]+)"' % name, page).group(1)


def authorize(url, user):
    """From Inferno's authorization link: sign in, choose the patient if asked, allow every scope offered, return to Inferno."""
    st, page, h = go(url)
    while st == 302:  # Inferno's own endpoints redirect on to /auth/authorize
        st, page, h = go(h["Location"])
    req = field(page, "req")
    st, page, h = go(LOCAL + "/auth/signin", {"req": req, "username": user, "password": PASSWORD})
    if "Choose a patient" in page:
        st, page, h = go(LOCAL + "/auth/patient", {"req": req, "patient": "p1"})
    scopes = [s.replace("&amp;", "&") for s in re.findall(r'name="scope" value="([^"]+)"', page)]
    st, page, h = go(LOCAL + "/auth/consent", {"req": req, "action": "allow", "scope": scopes})
    st, _, _ = go(h["Location"])
    print(f"  {user} signed in and allowed; Inferno answered {st}", flush=True)


def ehr_launch(app, user):
    """A clinician signs in at Perfuse's launch page and starts the app for patient p1 in encounter e1."""
    st, page, h = go(LOCAL + "/auth/launch/signin", {"username": user, "password": PASSWORD})
    req = field(page, "req")
    st, page, h = go(LOCAL + "/auth/launch/choose", {"req": req, "app": app, "patient": "p1", "encounter": "e1"})
    st, _, _ = go(h["Location"])  # Inferno's launch URL; it resumes, and its next wait links to /auth/authorize
    print(f"  {user} launched {app}; Inferno answered {st}", flush=True)


def auth_info(kind, scopes, client):
    a = {"auth_type": kind, "use_discovery": "true", "client_id": client, "requested_scopes": scopes,
         "pkce_support": "enabled", "pkce_code_challenge_method": "S256", "auth_request_method": "GET"}
    if kind == "symmetric":
        a["client_secret"] = PASSWORD
    if kind == "backend_services":
        for k in ("pkce_support", "pkce_code_challenge_method", "auth_request_method"):
            a.pop(k)
        a["encryption_algorithm"] = "ES384"
    return json.dumps(a)


def load():
    """The patient, a second patient the patient token must not reach, the clinician and the encounter of the EHR launch."""
    token = inferno.backend_token(PUBLIC + "/auth/token", scope="system/*.cruds", local_url=LOCAL + "/auth/token")
    for r in [
        {"resourceType": "Patient", "id": "p1", "name": [{"family": "Member", "given": ["Amy"]}], "birthDate": "1980-02-14", "gender": "female"},
        {"resourceType": "Patient", "id": "p2", "name": [{"family": "Other", "given": ["Ben"]}], "birthDate": "1975-05-01", "gender": "male"},
        {"resourceType": "Practitioner", "id": "d1", "name": [{"family": "Brown", "given": ["Dana"]}]},
        {"resourceType": "Encounter", "id": "e1", "status": "finished",
         "class": {"system": "http://terminology.hl7.org/CodeSystem/v3-ActCode", "code": "AMB"}, "subject": {"reference": "Patient/p1"}},
    ]:
        req = urllib.request.Request(f"{LOCAL}/fhir/{r['resourceType']}/{r['id']}", data=json.dumps(r).encode(), method="PUT",
                                     headers={"Authorization": "Bearer " + token, "Content-Type": "application/fhir+json"})
        print("loaded", r["resourceType"], r["id"], urllib.request.urlopen(req, context=inferno.INSECURE).status, flush=True)


def run(name):
    app, kind, groups = RUNS[name]
    # Inferno sends introspection requests bare unless given a header; Perfuse's introspection endpoint wants a client.
    basic = base64.b64encode(f"inferno-confidential:{PASSWORD}".encode()).decode()
    inputs = {
        "url": FHIR,
        "standalone_smart_auth_info": auth_info(kind, "launch/patient openid fhirUser offline_access patient/*.rs", app),
        "ehr_smart_auth_info": auth_info(kind, "launch openid fhirUser offline_access user/*.rs patient/*.rs", app),
        "backend_services_smart_auth_info": auth_info("backend_services", "system/*.rs", "inferno-backend"),
        "custom_authorization_header": "Authorization: Basic " + basic,
    }
    sid = inferno.session(SUITE)
    for g in groups:
        gid = f"{SUITE}-{g}"
        print(f"group {g} as {app}", flush=True)
        rid = inferno.call("POST", "/test_runs", {"test_session_id": sid, "test_group_id": gid,
                                                  "inputs": [{"name": k, "value": v} for k, v in inputs.items()]})["id"]
        handled, t0 = set(), time.time()
        while time.time() - t0 < 600:
            if inferno.call("GET", f"/test_runs/{rid}")["status"] in ("done", "cancelled"):
                break
            for res in inferno.call("GET", f"/test_sessions/{sid}/results"):
                if res["result"] != "wait" or res["id"] in handled:
                    continue
                handled.add(res["id"])
                tid, msg = res.get("test_id") or "", res.get("result_message") or ""
                links = re.findall(r"\((https?://[^)\s]+)\)", msg)
                if "launch" in tid and "redirect" not in tid:
                    ehr_launch(app, "drb")
                elif links:
                    authorize(links[0], "amy" if "standalone" in g else "drb")
            time.sleep(2)
        counts = collections.Counter(r["result"] for r in inferno.call("GET", f"/test_sessions/{sid}/results")
                                     if (r.get("test_id") or "").startswith(gid))
        print(f"  {g} {dict(counts)}", flush=True)
    inferno.report(sid, f"smart-{name}")


if __name__ == "__main__":
    load()
    for name in sys.argv[1:] or ["public", "confidential"]:
        run(name)
