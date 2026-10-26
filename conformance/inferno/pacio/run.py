"""Runs the PACIO post-acute care test kits (paciowg on GitHub) against Perfuse's FHIR server.

The kits test a FHIR server that already holds PACIO data: reads, searches and profile validation for Transitions of Care,
the Standardized Medication Profile, Personal Functioning and Engagement, and Advance Directives. Perfuse is loaded first with
PACIO's own sample data, copied from the two public servers the PACIO track uses at Connectathons (the PACIO sandbox and
the server PACIO keeps for its Inferno kits), and each resource is PUT under its own id. Nothing in Perfuse is PACIO-specific;
this is its ordinary FHIR server holding someone else's profiles.

Usage: run.py [suite ...]; with no suite, every suite the running kit offers (kit.sh pacio-toc|pacio-smp|pacio starts one).
"""
import collections
import glob
import json
import os
import sys
import re
import tarfile
import urllib.request
import urllib.error
import urllib.request

sys.path.insert(0, os.path.dirname(os.path.dirname(os.path.abspath(__file__))))
import inferno  # noqa: E402

PORT = 8475
LOCAL = f"https://127.0.0.1:{PORT}"
PUBLIC = f"https://host.docker.internal:{PORT}"
SOURCES = ["https://gw.interop.community/paciosandbox/open", "https://gw.interop.community/pacio4inferno/open"]
TYPES = ("Organization Location Practitioner PractitionerRole Patient RelatedPerson Device Medication Questionnaire "
         "Encounter Condition Observation QuestionnaireResponse MedicationStatement MedicationRequest Goal ServiceRequest "
         "DiagnosticReport List CarePlan CareTeam DeviceRequest NutritionOrder AllergyIntolerance Procedure Consent "
         "DocumentReference Composition Bundle Binary Provenance").split()


def sample_data():
    """PACIO's sample data, fetched once and kept in .work/pacio-data.json so later runs test the same thing."""
    cache = os.path.join(inferno.WORK, "pacio-data.json")
    if os.path.exists(cache):
        with open(cache) as f:
            return json.load(f)
    found = {}
    for base in SOURCES:
        for t in TYPES:
            url = f"{base}/{t}?_count=200"
            while url:
                with urllib.request.urlopen(url, timeout=90) as r:
                    bundle = json.load(r)
                for e in bundle.get("entry", []):
                    res = e["resource"]
                    found[res["resourceType"] + "/" + res["id"]] = res
                url = next((l["url"] for l in bundle.get("link", []) if l["relation"] == "next"), None)
    data = sorted(found.values(), key=lambda r: TYPES.index(r["resourceType"]))
    os.makedirs(inferno.WORK, exist_ok=True)
    with open(cache, "w") as f:
        json.dump(data, f)
    return data


def guide_package(name, version):
    path = os.path.join(inferno.WORK, "pkgs", f"{name}_{version}.tgz")
    if not os.path.exists(path):
        os.makedirs(os.path.dirname(path), exist_ok=True)
        with urllib.request.urlopen(f"https://packages.fhir.org/{name}/{version}", timeout=120) as r, open(path, "wb") as f:
            f.write(r.read())
    return path


def group_inputs(kit_dir, data):
    """The per-profile inputs of the general PACIO kit (read ids and a resource to create), filled from the loaded data:
    each group names its profile; the kit's PacioProfiles maps the name to its canonical URL."""
    lib = os.path.join(kit_dir, "lib", "pacio_test_kit")
    with open(os.path.join(lib, "pacio_profiles.rb")) as f:
        urls = dict(re.findall(r"'(\w+)' => '([^']+)'", f.read()))
    inputs = {}
    for g in glob.glob(os.path.join(lib, "*", "*_group.rb")):
        with open(g) as f:
            src = f.read()
        m = re.search(r"profile: '(\w+)'", src)
        if not m or m.group(1) not in urls:
            continue
        url = urls[m.group(1)]
        typ = re.search(r"resource_type: '(\w+)'", src)
        having = [r for r in data if url in (r.get("meta") or {}).get("profile", [])
                  and (not typ or r["resourceType"] == typ.group(1))]
        # An abstract parent (ADI's Composition header) has no examples of its own: its children's are examples of it.
        if not having and typ:
            guide = url.rsplit("/StructureDefinition/", 1)[0]
            having = [r for r in data if r["resourceType"] == typ.group(1)
                      and any(p.startswith(guide) for p in (r.get("meta") or {}).get("profile", []))]
        for name in re.findall(r"name: :(\w+_resource_ids)", src):
            if having:
                inputs[name] = ",".join(r["id"] for r in having)
        for name in re.findall(r"name: :(\w+_resource_input)", src):
            if having:
                r = dict(having[0])
                r.pop("id", None)
                inputs[name] = json.dumps(r)
    return inputs


def deleted_resource(token):
    """A resource created and then deleted, for the kit's read-after-delete check (it expects 410 Gone)."""
    body = {"resourceType": "Goal", "id": "pacio-deleted-goal", "lifecycleStatus": "cancelled",
            "description": {"text": "Deleted for the read-after-delete test"}, "subject": {"reference": "Patient/x"}}
    for method, data in (("PUT", json.dumps(body).encode()), ("DELETE", None)):
        req = urllib.request.Request(f"{LOCAL}/fhir/Goal/pacio-deleted-goal", data, method=method,
                                     headers={"Authorization": "Bearer " + token, "Content-Type": "application/fhir+json"})
        urllib.request.urlopen(req, context=inferno.INSECURE, timeout=30).close()
    return {"deleted_resource_id": "pacio-deleted-goal", "deleted_resource_type": "Goal"}


def running_kit():
    """The kit kit.sh started last: the one whose containers are up."""
    import subprocess
    names = subprocess.run(["docker", "ps", "--format", "{{.Names}}"], capture_output=True, text=True).stdout
    for kit in ("pacio-toc", "pacio-smp", "pacio"):
        if f"{kit}-inferno-1" in names:
            return kit
    return None


def load(token, data):
    counts = collections.Counter()
    for r in data:
        # The guide's example DocumentReference points at its transfer Bundle on www.example.org; held here, it lives here.
        r = json.loads(json.dumps(r).replace("http://www.example.org/fhir/", PUBLIC + "/fhir/"))
        req = urllib.request.Request(f"{LOCAL}/fhir/{r['resourceType']}/{r['id']}", json.dumps(r).encode(), method="PUT",
                                     headers={"Authorization": "Bearer " + token, "Content-Type": "application/fhir+json"})
        try:
            with urllib.request.urlopen(req, context=inferno.INSECURE, timeout=30) as resp:
                counts[resp.status] += 1
        except urllib.error.HTTPError as e:
            counts[e.code] += 1
            print("  not loaded:", r["resourceType"], r["id"], e.code, e.read()[:300])
    print("loaded", dict(counts), flush=True)


def main():
    system = lambda: inferno.backend_token(PUBLIC + "/auth/token", scope="system/*.cruds", local_url=LOCAL + "/auth/token")
    data = sample_data()
    kit = running_kit()
    if kit:
        tgzs = glob.glob(os.path.join(inferno.WORK, "kits", kit, "lib", "*", "igs", "*.tgz"))
        # The general PACIO kit tests Advance Directives too but ships only PFE's package; ADI's comes from the registry.
        if kit == "pacio":
            tgzs.append(guide_package("hl7.fhir.us.pacio-adi", "2.0.0-ballot"))
        examples = []
        for tgz in tgzs:
            with tarfile.open(tgz) as t:
                for m in t.getmembers():
                    if m.isfile() and "/example/" in m.name and m.name.endswith(".json"):
                        r = json.load(t.extractfile(m))
                        # Operation examples (Parameters) are what a server answers, not what it holds.
                        if r["resourceType"] == "Parameters":
                            continue
                        examples.append(r)
        print(f"{kit}: {len(examples)} examples from its guides", flush=True)
        data = data + sorted(examples, key=lambda r: TYPES.index(r["resourceType"]) if r["resourceType"] in TYPES else 50)
    load(system(), data)
    patients = ",".join(r["id"] for r in data if r["resourceType"] == "Patient")
    suites = sys.argv[1:] or [s["id"] for s in inferno.call("GET", "/test_suites") if s["id"] != "tls"]
    for suite in suites:
        auth = json.dumps({"auth_type": "public", "access_token": system(), "use_discovery": "false"})
        inputs = {"url": PUBLIC + "/fhir", "patient_ids": patients, "smart_auth_info": auth, "credentials": auth,
                  "search_method": "get"}
        if kit == "pacio":
            inputs.update(group_inputs(os.path.join(inferno.WORK, "kits", kit), data))
            first = next(r["id"] for r in data if r["resourceType"] == "Patient")
            inputs["unauthorized_patient_id"] = first
            inputs.update(deleted_resource(system()))
        # Bundles are read by id: the guide's examples of each Bundle profile.
        for name, marker in (("bundle_resource_ids", "smp-bundle"), ("bundle_transaction_resource_ids", "smp-bundle-transaction"),
                             ("medication_action_plan_bundle_resource_ids", "smp-medication-action-plan-bundle")):
            ids = [r["id"] for r in data if r["resourceType"] == "Bundle"
                   and any(p.rstrip("/").endswith("/" + marker) for p in (r.get("meta") or {}).get("profile", []))]
            if ids:
                inputs[name] = ",".join(ids)
        print("suite", suite, flush=True)
        sid = inferno.run(suite, inputs, limit=3600)
        inferno.report(sid, "pacio-" + suite)


if __name__ == "__main__":
    main()
