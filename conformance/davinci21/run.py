#!/usr/bin/env python3
"""Da Vinci CRD, DTR and PAS 2.1.0: Perfuse's payer side checked against the 2.1.0 guides.

There is no Inferno test kit for 2.1.0: the Da Vinci kits test 2.0.1 and 2.2.x. So this run takes each guide's own 2.1.0
examples as the client's requests, sends them to Perfuse, and has the official HL7 validator check every answer against the
2.1.0 profiles (packages hl7.fhir.us.davinci-crd#2.1.0, -dtr#2.1.0, -pas#2.1.0):

  CRD  the order-sign service requests in the guide (Binary-CRDServiceRequest*): the coverage-information each answer
       records on the order, checked against the order's CRD 2.1.0 profile.
  DTR  $questionnaire-package with the guide's input Parameters (output: dtr-qpackage-output-parameters), $next-question
       (dtr-next-question-output-parameters) and $log-questionnaire-errors.
  PAS  $submit with each request Bundle in the guide (response: profile-pas-response-bundle) and $inquire with its inquiry
       Bundle (profile-pas-inquiry-response-bundle).

The client says it speaks 2.1 the ways Perfuse reads: CRD's davinci-crd.requestedVersion "2.1", and for DTR and PAS the input's
meta.profile carrying the 2.1.0 version (profile|2.1.0). The same calls made without saying a version are then checked against
2.2.x, to show one server answering both. Results go to results/<ig>.md and results/<ig>.json.

Usage: run.py [-perfuse BIN] [-validator JAR] [-keep]
"""
import argparse
import base64
import collections
import glob
import json
import os
import shutil
import socket
import ssl
import subprocess
import sys
import tarfile
import tempfile
import time
import urllib.error
import urllib.request

HERE = os.path.dirname(os.path.abspath(__file__))
CACHE = os.path.expanduser("~/.cache/perfuse-davinci21")
NEW = {"crd": "2.2.1", "dtr": "2.2.0", "pas": "2.2.1"}
DTR = "http://hl7.org/fhir/us/davinci-dtr/StructureDefinition/"
PAS = "http://hl7.org/fhir/us/davinci-pas/StructureDefinition/"
CRD = "http://hl7.org/fhir/us/davinci-crd/StructureDefinition/"


def package(ig, version):
    dest = os.path.join(CACHE, f"{ig}-{version}")
    if not os.path.isdir(dest):
        os.makedirs(dest)
        url = f"https://packages.fhir.org/hl7.fhir.us.davinci-{ig}/{version}"
        with urllib.request.urlopen(url, timeout=120) as r, tempfile.TemporaryFile() as fh:
            shutil.copyfileobj(r, fh)
            fh.seek(0)
            with tarfile.open(fileobj=fh, mode="r:gz") as t:
                t.extractall(dest, filter="data")
    return os.path.join(dest, "package")


def free_port():
    s = socket.socket()
    s.bind(("127.0.0.1", 0))
    p = s.getsockname()[1]
    s.close()
    return p


class Perfuse:
    def __init__(self, binary, work):
        binary = os.path.abspath(binary) if os.sep in binary else binary
        self.work = work
        self.port = free_port()
        self.base = f"http://127.0.0.1:{self.port}"
        shutil.copy(os.path.join(HERE, "rules.yaml"), work)
        self.log = open(os.path.join(work, "serve.log"), "w")
        self.proc = subprocess.Popen(
            [binary, "serve", "-db", "perfuse.db", "-addr", f"127.0.0.1:{self.port}", "-public-url", self.base,
             "-fhir", "-pas", "-crd-rules", "rules.yaml", "-fhir-subscriptions", "-fhir-subscriptions-allow-http"],
            cwd=work, stdout=self.log, stderr=subprocess.STDOUT, stdin=subprocess.DEVNULL)
        for _ in range(60):
            try:
                urllib.request.urlopen(self.base + "/fhir/metadata", timeout=2)
                break
            except Exception:
                time.sleep(0.5)
        else:
            raise SystemExit("Perfuse did not start; see " + os.path.join(work, "serve.log"))
        out = subprocess.run([binary, "token", "create", "-db", "perfuse.db", "-label", "davinci21", "-role", "admin"],
                             cwd=work, capture_output=True, text=True).stdout
        self.token = [w for w in out.split() if len(w) >= 30][0]

    def call(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method, headers={
            "Authorization": "Bearer " + self.token, "Content-Type": "application/fhir+json", "Accept": "application/fhir+json"})
        try:
            with urllib.request.urlopen(req, timeout=60) as r:
                return r.status, json.loads(r.read() or b"null")
        except urllib.error.HTTPError as e:
            raw = e.read()
            try:
                return e.code, json.loads(raw)
            except Exception:
                return e.code, raw.decode("utf-8", "replace")

    def stop(self):
        self.proc.terminate()
        self.proc.wait(10)
        self.log.close()


def versioned(resource, version):
    """The input as a client of that version would send it: its profiles carrying the version."""
    r = json.loads(json.dumps(resource))
    if version:
        meta = r.setdefault("meta", {})
        meta["profile"] = [p.split("|")[0] + "|" + version for p in meta.get("profile", [])] or meta.get("profile", [])
    return r


def load_examples(pf, pkgs):
    """The guides' example resources (not Bundles or Parameters) into the store: patients, coverages, orders, questionnaires,
    libraries and value sets the requests refer to."""
    n = 0
    for pkg in pkgs:
        for f in sorted(glob.glob(os.path.join(pkg, "example", "*.json")) + glob.glob(os.path.join(pkg, "*.json"))):
            r = json.load(open(f))
            skip = ("Bundle", "Parameters", "Binary", "ImplementationGuide", "CapabilityStatement", "StructureDefinition",
                    "OperationDefinition", "SearchParameter", "ConceptMap", "Basic", "Subscription")
            if r.get("resourceType") in skip or not r.get("id"):
                continue
            status, _ = pf.call("PUT", f"/fhir/{r['resourceType']}/{r['id']}", r)
            n += status < 300
    return n


def crd_calls(pf, pkg, version):
    """Each CRD order-sign example; returns (name, resource, profile) for the orders the answers record coverage on."""
    out = []
    for f in sorted(glob.glob(os.path.join(pkg, "Binary-CRDServiceRequest*.json"))):
        req = json.loads(base64.b64decode(json.load(open(f))["data"]))
        req.pop("fhirAuthorization", None)
        req.pop("fhirServer", None)
        if version:
            req.setdefault("extension", {})["davinci-crd.requestedVersion"] = version
        status, resp = pf.call("POST", "/cds-services/crd-order-sign", req)
        name = os.path.basename(f)[:-5]
        off = req.get("extension", {}).get("davinci-crd.configuration", {}).get("coverage-info") is False
        if off and status == 200 and resp == {"cards": []}:
            # The guide's second example switches coverage-info off; CRD (dev-5) then wants an empty answer.
            out.append((name + "-coverage-info-off", {"accepted": "an empty answer, as asked"}, None))
            continue
        if status != 200 or not isinstance(resp, dict):
            out.append((name, {"error": status, "body": resp}, None))
            continue
        orders = []
        for a in resp.get("systemActions", []) + [a for c in resp.get("cards", []) for s in c.get("suggestions", []) for a in s.get("actions", [])]:
            res = a.get("resource")
            if isinstance(res, dict) and res.get("resourceType") in ("ServiceRequest", "DeviceRequest", "MedicationRequest",
                                                                       "NutritionOrder", "VisionPrescription", "Appointment",
                                                                       "CommunicationRequest", "Encounter"):
                orders.append(res)
        if not orders:
            out.append((name, {"error": "no order with coverage information in the answer", "body": resp}, None))
        for i, o in enumerate(orders):
            prof = {"ServiceRequest": "profile-servicerequest", "DeviceRequest": "profile-devicerequest",
                    "MedicationRequest": "profile-medicationrequest", "NutritionOrder": "profile-nutritionorder",
                    "VisionPrescription": "profile-visionprescription", "Appointment": "profile-appointment-with-order",
                    "CommunicationRequest": "profile-communicationrequest", "Encounter": "profile-encounter"}[o["resourceType"]]
            o = dict(o)
            o["meta"] = {"profile": [CRD + prof]}
            out.append((f"{name}-{o['resourceType']}-{i + 1}", o, CRD + prof))
    return out


ADAPTIVE = {
    "resourceType": "Questionnaire", "id": "o2-adaptive", "url": "https://payer.perfuse-test.net/fhir/Questionnaire/o2-adaptive",
    "name": "HomeOxygenAdaptive", "title": "Home oxygen (adaptive)", "status": "active",
    "subjectType": ["Patient"], "date": "2026-10-24", "publisher": "Perfuse test data",
    "extension": [{"url": "http://hl7.org/fhir/uv/sdc/StructureDefinition/sdc-questionnaire-questionnaireAdaptive",
                   "valueBoolean": True}],
    "item": [
        {"linkId": "spo2", "text": "Oxygen saturation at rest (%)", "type": "decimal", "required": True},
        {"linkId": "low", "text": "Was it measured below 88% within the last 30 days?", "type": "boolean",
         "enableWhen": [{"question": "spo2", "operator": "<", "answerDecimal": 88}]},
    ],
}


def dtr_calls(pf, pkg, version):
    ex = os.path.join(pkg, "example")
    out = []
    # An adaptive questionnaire: its package, then the first $next-question on the response the app starts from it.
    pf.call("PUT", "/fhir/Questionnaire/o2-adaptive", ADAPTIVE)
    params = versioned(json.load(open(os.path.join(ex, "Parameters-QuestionnairePackageInputParamsExample.json"))), version)
    params["parameter"] = [p for p in params["parameter"] if p["name"] == "coverage"] + [
        {"name": "questionnaire", "valueCanonical": ADAPTIVE["url"]}]
    status, resp = pf.call("POST", "/fhir/Questionnaire/$questionnaire-package", params)
    out.append(("questionnaire-package-adaptive", resp if status == 200 else {"error": status, "body": resp},
                DTR + "dtr-qpackage-output-parameters"))
    shell, seeded = None, None
    if status == 200:
        for p in resp.get("parameter", []):
            for e in (p.get("resource") or {}).get("entry", []):
                if e["resource"].get("resourceType") == "Questionnaire":
                    shell = e["resource"]
                if e["resource"].get("resourceType") == "QuestionnaireResponse":
                    seeded = e["resource"]
    if shell:
        if seeded is not None:
            # 2.2.0: the package carries the response to start from, the adaptive shell contained in it.
            qr = seeded
        else:
            # 2.1.0: the app starts the response itself, as the 2.1.0 profiles shape it: the order and coverage as contexts,
            # what the answers are for, the contained questionnaire derived from (not named as) the payer's, and the first
            # answer given.
            contained = {k: v for k, v in shell.items() if k not in ("url", "meta")}
            contained.update(id="o2", derivedFrom=[shell["url"]],
                             item=[ADAPTIVE["item"][0]],
                             text={"status": "generated", "div": '<div xmlns="http://www.w3.org/1999/xhtml">Home oxygen</div>'},
                             meta={"profile": [DTR + "dtr-questionnaire-adapt|2.1.0"]})
            qr = {"resourceType": "QuestionnaireResponse", "meta": {"profile": [DTR + "dtr-questionnaireresponse|2.1.0"]},
                  "extension": [
                      {"url": DTR + "qr-context", "valueReference": {"reference": "Coverage/CoverageExample"}},
                      {"url": DTR + "qr-context", "valueReference": {"reference": "ServiceRequest/ServiceRequestExample"}},
                      {"url": DTR + "intendedUse", "valueCodeableConcept": {"coding": [
                          {"system": "http://hl7.org/fhir/us/davinci-crd/CodeSystem/temp", "code": "withpa"}]}}],
                  "contained": [contained], "questionnaire": "#o2", "status": "in-progress",
                  "subject": {"reference": "Patient/examplepatient"}, "authored": "2026-10-24T09:00:00Z",
                  "item": [{"linkId": "spo2", "text": "Oxygen saturation at rest (%)", "answer": [{"valueDecimal": 86, "extension": [
                      {"url": DTR + "information-origin", "extension": [{"url": "source", "valueCode": "auto"}]}]}]}]}
        name = "in" if version == "2.1.0" else "questionnaire-response"
        status, resp = pf.call("POST", "/fhir/Questionnaire/$next-question",
                               {"resourceType": "Parameters", "parameter": [{"name": name, "resource": qr}]})
        out.append(("next-question-adaptive", resp if status == 200 else {"error": status, "body": resp},
                    DTR + "dtr-next-question-output-parameters"))
    status, resp = pf.call("POST", "/fhir/Questionnaire/$questionnaire-package",
                           versioned(json.load(open(os.path.join(ex, "Parameters-QuestionnairePackageInputParamsExample.json"))), version))
    out.append(("questionnaire-package", resp if status == 200 else {"error": status, "body": resp}, DTR + "dtr-qpackage-output-parameters"))
    # The guide's own $next-question example is a completed response to a standard (not adaptive) questionnaire, with no
    # Questionnaire contained in it: there is no next question to give, and a 4xx with an OperationOutcome is the answer.
    status, resp = pf.call("POST", "/fhir/Questionnaire/$next-question",
                           versioned(json.load(open(os.path.join(ex, "Parameters-NextQuestionInputParamsExample.json"))), version))
    out.append(("next-question-guide-example", {"accepted": status} if 400 <= status < 500 and isinstance(resp, dict) and
                resp.get("resourceType") == "OperationOutcome" else {"error": status, "body": resp}, None))
    status, resp = pf.call("POST", "/fhir/Questionnaire/$log-questionnaire-errors",
                           versioned(json.load(open(os.path.join(ex, "Parameters-LogQuestionnaireErrorsInputParamsExample.json"))), version))
    out.append(("log-questionnaire-errors", {"accepted": status} if status < 300 else {"error": status, "body": resp}, None))
    return out


def pas_calls(pf, pkg, version):
    ex = os.path.join(pkg, "example")
    out = []
    requests = ["HomecareAuthorizationBundleExample", "MedicalServicesAuthorizationBundleExample",
                "ReferralAuthorizationBundleExample", "SurgicalRequestBundleExample", "HomecareAuthorizationUpdateBundleExample"]
    for name in requests:
        f = os.path.join(ex, f"Bundle-{name}.json")
        if not os.path.exists(f):
            continue
        b = versioned(json.load(open(f)), version)
        status, resp = pf.call("POST", "/fhir/Claim/$submit", b)
        out.append((f"submit-{name}", resp if status == 200 else {"error": status, "body": resp}, PAS + "profile-pas-response-bundle"))
    f = os.path.join(ex, "Bundle-PASClaimInquiryBundleExample.json")
    status, resp = pf.call("POST", "/fhir/Claim/$inquire", versioned(json.load(open(f)), version))
    if isinstance(resp, dict) and resp.get("resourceType") == "Parameters":
        # 2.2.x answers in Parameters (return 0..*); each return is checked.
        for i, p in enumerate(resp.get("parameter", [])):
            out.append((f"inquire-{i + 1}", p.get("resource"), PAS + "profile-pas-inquiry-response-bundle"))
    else:
        out.append(("inquire", resp if status == 200 else {"error": status, "body": resp}, PAS + "profile-pas-inquiry-response-bundle"))
    return out


# Errors that are in the guides' own example data, which Perfuse returns as it was sent, each with why. Anything else counts
# against Perfuse.
GUIDE_DATA = [
    ("Example URLs are not allowed", "an example.org URL in the guide's example data"),
    ("Unable to find a profile match for http://example.org/fhir/Patient/SubscriberExample",
     "the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too)"),
    ("Unable to find a profile match for Patient/SubscriberExample",
     "the guide's Patient SubscriberExample does not meet profile-beneficiary (the guide's own request fails this too)"),
    ("Coverage/InsuranceExample*/: Coverage.identifier: minimum required",
     "the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires"),
    ("Coverage/InsuranceExample*/: Slice 'Coverage.identifier:memberid'",
     "the 2.1.0 example Coverage has no identifier, which PAS 2.2.1 requires"),
    ("home-o2-std-questionnaire*/: Constraint failed: sdc-2",
     "the guide's questionnaire has a version and no versionAlgorithm (SDC 4.0.0)"),
    ("'18776-5'", "terminology is off, so the validator cannot check a LOINC attachment code against its value set"),
    ("Unable to find a profile match for urn:uuid:", "follows from the LOINC attachment code the validator could not check"),
]


def classify(error):
    for needle, why in GUIDE_DATA:
        if needle in error:
            return why
    return None


def validate(validator, files, ig, version, txlog):
    if not files:
        return {}
    cmd = ["java", "-Xmx4g", "-jar", validator, *files, "-version", "4.0.1", "-ig", f"hl7.fhir.us.davinci-{ig}#{version}",
           "-tx", "n/a", "-output", os.path.join(os.path.dirname(files[0]), "oo.json")]
    with open(txlog, "w") as log:
        subprocess.run(cmd, stdout=log, stderr=subprocess.STDOUT)
    doc = json.load(open(os.path.join(os.path.dirname(files[0]), "oo.json")))
    oos = [e["resource"] for e in doc["entry"]] if doc.get("resourceType") == "Bundle" else [doc]
    result = {}
    for oo in oos:
        src = next((e["valueString"] for e in oo.get("extension", []) if e.get("url", "").endswith("operationoutcome-file")), None)
        errors = []
        for iss in oo.get("issue", []):
            if iss.get("severity") in ("error", "fatal"):
                text = iss.get("details", {}).get("text") or iss.get("diagnostics") or ""
                # Terminology is off (-tx n/a): bindings cannot be checked, and saying so is not a defect in the answer.
                if "No Terminology Service" in text or "terminology server" in text.lower():
                    continue
                where = (iss.get("expression") or iss.get("location") or [""])[0]
                errors.append(f"{where}: {text}")
        result[os.path.basename(src or "")] = errors
    return result


def run(args):
    pkgs = {ig: package(ig, "2.1.0") for ig in NEW}
    for ig, v in NEW.items():
        package(ig, v)
    work = tempfile.mkdtemp(prefix="pf-davinci21-")
    pf = Perfuse(args.perfuse, work)
    summary = {}
    try:
        loaded = load_examples(pf, pkgs.values())
        print(f"loaded {loaded} example resources", flush=True)
        for label, version in (("2.1.0", "2.1"), ("default", "")):
            for ig in ("crd", "dtr", "pas"):
                v = {"crd": version, "dtr": version and "2.1.0", "pas": version and "2.1.0"}[ig]
                calls = {"crd": crd_calls, "dtr": dtr_calls, "pas": pas_calls}[ig](pf, pkgs[ig], v)
                d = os.path.join(work, label, ig)
                os.makedirs(d)
                files, meta = [], {}
                for name, res, profile in calls:
                    path = os.path.join(d, name + ".json")
                    if isinstance(res, dict) and "error" not in res and "accepted" not in res and profile:
                        r = json.loads(json.dumps(res))
                        r.setdefault("meta", {})["profile"] = [profile]
                        json.dump(r, open(path, "w"), indent=1)
                        files.append(path)
                    meta[name + ".json"] = res if (isinstance(res, dict) and ("error" in res or "accepted" in res)) else profile
                checked_version = "2.1.0" if label == "2.1.0" else NEW[ig]
                res = validate(args.validator, files, ig, checked_version, os.path.join(d, "validator.log"))
                answers = {}
                for n, m in meta.items():
                    if isinstance(m, str):
                        errs = res.get(n, ["not validated"])
                        answers[n] = {"profile": m.rsplit("/", 1)[1],
                                      "errors": [e for e in errs if not classify(e)],
                                      "guideData": sorted({classify(e) for e in errs if classify(e)})}
                    else:
                        answers[n] = {"profile": None, "guideData": [],
                                      "errors": [] if "accepted" in m else [f"HTTP {m['error']}: {json.dumps(m['body'])[:300]}"]}
                summary.setdefault(ig, {})[label] = {"checkedAgainst": checked_version, "answers": answers}
                bad = sum(1 for a in answers.values() if a["errors"])
                print(f"{label} {ig}: {len(meta)} answers, {bad} with errors in Perfuse's answer (vs {checked_version})", flush=True)
    finally:
        pf.stop()
    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    for ig, s in summary.items():
        json.dump(s, open(os.path.join(HERE, "results", f"{ig}.json"), "w"), indent=1)
        with open(os.path.join(HERE, "results", f"{ig}.md"), "w") as fh:
            fh.write(f"# {ig.upper()}: Perfuse's answers to the 2.1.0 guide's own example requests\n\n")
            for label, block in s.items():
                who = "Client asks for 2.1" if label == "2.1.0" else "Client names no version"
                fh.write(f"## {who}: checked against {ig.upper()} {block['checkedAgainst']}\n\n"
                         "| Answer | Profile | Errors in Perfuse's answer | In the guide's example data |\n|---|---|---|---|\n")
                for n, a in block["answers"].items():
                    fh.write(f"| {n[:-5]} | {('`' + a['profile'] + '`') if a['profile'] else '-'} | {len(a['errors'])} | "
                             f"{'; '.join(a['guideData']) or '-'} |\n")
                for n, a in block["answers"].items():
                    if a["errors"]:
                        fh.write(f"\n### {n[:-5]}\n\n" + "".join(f"- {e}\n" for e in a["errors"]))
                fh.write("\n")
    if args.keep:
        print("kept", work)
    else:
        shutil.rmtree(work, ignore_errors=True)
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("-perfuse", default="perfuse")
    ap.add_argument("-validator", default=os.path.expanduser("~/.cache/fhir-validator/validator_cli.jar"))
    ap.add_argument("-keep", action="store_true")
    sys.exit(run(ap.parse_args()))


if __name__ == "__main__":
    main()
