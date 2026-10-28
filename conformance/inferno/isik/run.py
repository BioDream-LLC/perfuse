#!/usr/bin/env python3
"""Runs gematik's ISiK Stufe 5 test suite against Perfuse's FHIR server.

ISiK (Informationstechnische Systeme im Krankenhaus) is the German hospital standard; gematik publishes its test suite
(github.com/gematik/isik-testsuite). Each test names a record it expects the server to hold; isik/data holds them, invented
(no real person), drafted from the suite's own preconditions and checks and then corrected by hand where a check failed.

    ./serve.sh isik && PERFUSE_TOKEN=$(cat .work/isik/token) python3 isik/run.py ["@Stufe5 and @Basis"]

The suite runs in gematik's own container image and reaches Perfuse through its Tiger proxy, with Perfuse's token.
Results: .work/isik-results/summary.json, and the suite's own HTML report beside it.
"""

import glob
import json
import os
import re
import shutil
import subprocess
import sys
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import inferno  # noqa: E402  (the shared harness: WORK, INSECURE)

HERE = os.path.dirname(os.path.abspath(__file__))
SUITE = ("https://github.com/gematik/isik-testsuite", "3c8e44c730c139230452c00ba582cec021fa5d61")  # release 3.0.9
IMAGE = "gematik1/isik-testsuite:3.0.9"
PORT = open(os.path.join(HERE, "flags")).read().split("port=")[1].split()[0]
LOCAL = f"https://127.0.0.1:{PORT}/fhir"
PUBLIC = f"https://host.docker.internal:{PORT}/fhir"


def checkout(repo, commit, name):
    path = os.path.join(inferno.WORK, name)
    if not os.path.isdir(path):
        os.makedirs(path)
        for cmd in (["git", "init", "-q"], ["git", "fetch", "-q", "--depth", "1", repo, commit],
                    ["git", "checkout", "-q", "FETCH_HEAD"]):
            subprocess.run(cmd, cwd=path, check=True)
    return path


def put(resource, token):
    url = f"{LOCAL}/{resource['resourceType']}/{resource['id']}"
    req = urllib.request.Request(url, json.dumps(resource).encode(), method="PUT", headers={
        "Authorization": "Bearer " + token, "Content-Type": "application/fhir+json"})
    try:
        with urllib.request.urlopen(req, context=inferno.INSECURE, timeout=60) as r:
            return r.status, ""
    except urllib.error.HTTPError as e:
        body = e.read().decode(errors="replace")
        try:
            body = "; ".join(i.get("diagnostics", "") for i in json.loads(body).get("issue", []))
        except ValueError:
            pass
        return e.code, body


def load(token):
    resources = []
    for f in sorted(glob.glob(os.path.join(HERE, "data", "*.json"))):
        r = json.load(open(f))
        resources += [e["resource"] for e in r["entry"]] if r["resourceType"] == "Bundle" and r.get("type") == "collection" else [r]
    # References first: a store that checks them would refuse a record naming one not loaded yet. Two passes do it.
    failed = []
    for _ in range(2):
        failed = []
        for r in resources:
            status, why = put(r, token)
            if status >= 300:
                failed.append((r["resourceType"], r["id"], status, why[:200]))
    print(f"isik: {len(resources)} records, {len(resources) - len(failed)} loaded")
    for f in failed:
        print("  not loaded:", *f)


def configure(suite, token, out):
    """The suite's own configuration with Perfuse's address, its token, and the ids of this data."""
    if os.path.isdir(out):
        shutil.rmtree(out)
    shutil.copytree(os.path.join(suite, "testdata"), os.path.join(out, "testdata"))
    for f in glob.glob(os.path.join(HERE, "testdata", "*.yaml")):
        target = os.path.join(out, "testdata", "Stufe5", os.path.basename(f))
        values = dict(line.split(": ", 1) for line in open(target).read().splitlines() if ": " in line)
        for line in open(f).read().splitlines():
            if ": " in line and not line.startswith("#"):
                k, v = line.split(": ", 1)
                values[k] = v
        open(target, "w").write("".join(f"{k}: {v}\n" for k, v in values.items()))
    tiger = open(os.path.join(suite, "tiger-isik-stufe5.yaml")).read()
    tiger = tiger.replace('to: "http://localhost:8080/fhir"', f'to: "{PUBLIC}"\n      authentication:\n        bearerToken: "{token}"')
    tiger = re.sub(r"activateWorkflowUi: true", "activateWorkflowUi: false", tiger)
    open(os.path.join(out, "tiger-isik-stufe5.yaml"), "w").write(tiger)


def run_suite(out, tags):
    report = os.path.join(out, "report")
    os.makedirs(report, exist_ok=True)
    cmd = ["docker", "run", "--rm", "--name", "perfuse-isik",
           "--add-host", "host.docker.internal:host-gateway",
           "-e", "MAVEN_PROFILE=stufe5", "-e", f"TESTS_TO_RUN={tags}",
           "-v", f"{out}/tiger-isik-stufe5.yaml:/app/tiger-isik-stufe5.yaml:ro",
           "-v", f"{out}/testdata:/app/testdata:ro",
           "-v", f"{report}:/app/report", IMAGE]
    with open(os.path.join(out, "suite.log"), "w") as log:
        subprocess.run(cmd, stdout=log, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL, timeout=5400)
    return report


def summarise(out):
    log = open(os.path.join(out, "suite.log"), errors="replace").read()
    m = re.search(r"Results: (\d+) tests \| (\d+) passed \| (\d+) failed \| (\d+) errors", log)
    counts = dict(zip(("tests", "passed", "failed", "errors"), map(int, m.groups()))) if m else {}
    block = log.split("[ERROR] Failures:", 1)[1].split("[ERROR] Tests run:", 1)[0] if "[ERROR] Failures:" in log else ""
    failures = [f.strip() for f in re.split(r"\n\[ERROR\]   ", block)[1:]]
    json.dump({"counts": counts, "failures": failures}, open(os.path.join(out, "summary.json"), "w"), indent=1)
    print(counts)
    for f in failures:
        print("  " + re.sub(r"\s+", " ", f)[:300])


def main():
    token = os.environ["PERFUSE_TOKEN"]
    tags = sys.argv[1] if len(sys.argv) > 1 else "@Stufe5 and @Basis"
    suite = checkout(*SUITE, "isik-testsuite")
    load(token)
    out = os.path.join(inferno.WORK, "isik-results")
    configure(suite, token, out)
    run_suite(out, tags)
    summarise(out)


if __name__ == "__main__":
    main()
