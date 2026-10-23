#!/usr/bin/env python3
"""National FHIR profiles: Perfuse's HL7 v2 to FHIR conversion checked against UK Core, FR Core, ISiK and HL7 Europe's
Laboratory Report.

Each set of real-shaped v2 messages is converted by `perfuse fhir convert`, every resource is pulled out of its bundle, and the
official HL7 validator checks it against the country's profile for that resource type:

  uk  NHS Wales' published v2 examples (GIG-Cymru-NHS-Wales/hl7-v2-examples)  -> UK Core STU2 2.0.2
  fr  the French agency's PAM-FR and document examples (ansforge/hl7V2-exemples) -> FR Core 2.2.0
  de  invented German messages in testdata/de (no real person)                -> ISiK Basismodul 4.0.3 (Stufe 4)
  eu  the lab results (ORU^R01) among all three                                 -> HL7 Europe Laboratory Report 2.1.0

A resource type with no profile in the country's guide is skipped and listed. The result is written to results/<set>.md and
results/<set>.json: errors and warnings per resource type, and each distinct error message with how often it occurred.

Usage: run.py [-perfuse BIN] [-validator JAR] [-sets uk,fr,de,eu] [-keep DIR]
"""
import argparse
import collections
import json
import os
import shutil
import subprocess
import sys
import tempfile

HERE = os.path.dirname(os.path.abspath(__file__))

SOURCES = {
    "uk": ("https://github.com/GIG-Cymru-NHS-Wales/hl7-v2-examples.git", "45926afcff4a3278fff345fff1cdfcb177e74b84"),
    "fr": ("https://github.com/ansforge/hl7V2-exemples.git", "60339877cf87ab36c2428dd84311ce7fcac4c33e"),
}

UK = "https://fhir.hl7.org.uk/StructureDefinition/UKCore-"
FR = "https://hl7.fr/ig/fhir/core/StructureDefinition/fr-core-"
ISIK = "https://gematik.de/fhir/isik/StructureDefinition/ISiK"
EULAB = "http://hl7.eu/fhir/laboratory/StructureDefinition/"
EUBASE = "http://hl7.eu/fhir/base/StructureDefinition/"

GUIDES = {
    "uk": ("fhir.r4.ukcore.stu2#2.0.2", "UK Core STU2 2.0.2", {
        "Patient": UK + "Patient", "Encounter": UK + "Encounter", "Observation": UK + "Observation",
        "DiagnosticReport": UK + "DiagnosticReport", "Practitioner": UK + "Practitioner", "Organization": UK + "Organization",
        "Location": UK + "Location", "Immunization": UK + "Immunization", "Specimen": UK + "Specimen",
        "Condition": UK + "Condition", "AllergyIntolerance": UK + "AllergyIntolerance", "Appointment": UK + "Appointment",
        "RelatedPerson": UK + "RelatedPerson", "Procedure": UK + "Procedure", "ServiceRequest": UK + "ServiceRequest",
        "MedicationAdministration": UK + "MedicationAdministration", "MedicationRequest": UK + "MedicationRequest",
        "PractitionerRole": UK + "PractitionerRole"}),
    "fr": ("hl7.fhir.fr.core#2.2.0", "FR Core 2.2.0", {
        "Patient": FR + "patient", "Encounter": FR + "encounter", "Practitioner": FR + "practitioner",
        "Organization": FR + "organization", "Location": FR + "location", "Appointment": FR + "appointment",
        "RelatedPerson": FR + "related-person", "PractitionerRole": FR + "practitioner-role"}),
    "de": ("de.gematik.isik-basismodul#4.0.3", "ISiK Basismodul 4.0.3", {
        "Patient": ISIK + "Patient", "Encounter": ISIK + "KontaktGesundheitseinrichtung", "Condition": ISIK + "Diagnose",
        "Practitioner": ISIK + "PersonImGesundheitsberuf", "Organization": ISIK + "Organisation", "Location": ISIK + "Standort",
        "Procedure": ISIK + "Prozedur", "AllergyIntolerance": ISIK + "AllergieUnvertraeglichkeit",
        "RelatedPerson": ISIK + "Angehoeriger", "Account": ISIK + "Abrechnungsfall"}),
    # The Laboratory Report guide's own profiles for the report's parts, and HL7 Europe Base's for the people and organisations
    # it points at (what DiagnosticReport-eu-lab's references name). The report itself is a document (Bundle-eu-lab with a
    # Composition) a sender assembles; what is checked here is what the v2 conversion produces.
    "eu": ("hl7.fhir.eu.laboratory#2.1.0", "HL7 Europe Laboratory Report 2.1.0", {
        "DiagnosticReport": EULAB + "DiagnosticReport-eu-lab", "Observation": EULAB + "Observation-resultslab-eu-lab",
        "Specimen": EULAB + "Specimen-eu-lab", "ServiceRequest": EULAB + "ServiceRequest-eu-lab",
        "Patient": EUBASE + "patient-eu-core", "Practitioner": EUBASE + "practitioner-eu-core",
        "PractitionerRole": EUBASE + "practitionerRole-eu-core", "Organization": EUBASE + "organization-eu-core"}),
}

# What a site would configure: its assigning authorities as real namespaces. Without them the converter writes a placeholder
# system and says so, which every national profile rightly rejects.
AUTHORITIES = {
    "eu": ["KLINIKUM-MUSTERSTADT=https://fhir.klinikum-musterstadt.example/sid/patientennummer"],
    "de": ["KLINIKUM-MUSTERSTADT=https://fhir.klinikum-musterstadt.example/sid/patientennummer"],
}


def message_type(path):
    """MSH-9 of the first message in a file, as TYPE^EVENT."""
    with open(path, "rb") as fh:
        text = fh.read().decode("utf-8", "replace").lstrip("\ufeff\x0b \r\n")
    first = text.replace("\n", "\r").split("\r", 1)[0]
    if not first.startswith("MSH") or len(first) < 4:
        return ""
    fields = first.split(first[3])
    return "^".join(fields[8].split("^")[:2]) if len(fields) > 8 else ""


def messages(name, cache):
    if name == "eu":
        # Lab results from every set: what a laboratory report is made from.
        return sorted(m for s in ("uk", "fr", "de") for m in messages(s, cache) if message_type(m).startswith("ORU^R01"))
    if name == "de":
        d = os.path.join(HERE, "testdata", "de")
        return sorted(os.path.join(d, f) for f in os.listdir(d) if f.endswith(".hl7"))
    url, commit = SOURCES[name]
    dest = os.path.join(cache, name)
    if not os.path.isdir(dest):
        subprocess.run(["git", "init", "-q", dest], check=True)
        subprocess.run(["git", "-C", dest, "fetch", "-q", "--depth", "1", url, commit], check=True)
        subprocess.run(["git", "-C", dest, "-c", "advice.detachedHead=false", "checkout", "-q", "FETCH_HEAD"], check=True)
    out = []
    for root, _, files in os.walk(dest):
        if "/.git" in root:
            continue
        out += [os.path.join(root, f) for f in files if f.lower().endswith((".hl7", ".er7"))]
    return sorted(out)


def convert(perfuse, name, msgs, work):
    """Each message on its own, so two messages sharing a control id never overwrite each other's bundle."""
    resources = collections.defaultdict(list)
    for i, m in enumerate(msgs):
        out = os.path.join(work, "bundles", f"{i:03d}")
        cmd = [perfuse, "fhir", "convert", "-quiet", "-out", out]
        for a in AUTHORITIES.get(name, []):
            cmd += ["-authority", a]
        r = subprocess.run(cmd + [m], capture_output=True, text=True)
        if r.returncode != 0:
            print(f"convert failed: {m}: {r.stderr.strip()[:200]}", file=sys.stderr)
            continue
        for f in sorted(os.listdir(out)):
            with open(os.path.join(out, f)) as fh:
                bundle = json.load(fh)
            for e in bundle.get("entry", []):
                res = e["resource"]
                resources[res["resourceType"]].append((os.path.basename(m), res))
    return resources


def validate(jar, package, profile, files, outfile):
    cmd = ["java", "-Xmx4g", "-jar", jar, "-version", "4.0.1", "-ig", package, "-profile", profile,
           "-output", outfile, "-tx", "https://tx.fhir.org/r4"] + files
    subprocess.run(cmd, capture_output=True, text=True)
    with open(outfile) as fh:
        doc = json.load(fh)
    oos = [e["resource"] for e in doc.get("entry", [])] if doc.get("resourceType") == "Bundle" else [doc]
    return oos


def run_set(name, args, cache):
    package, label, profiles = GUIDES[name]
    work = tempfile.mkdtemp(prefix=f"pf-national-{name}-", dir=args.keep or None)
    msgs = messages(name, cache)
    resources = convert(args.perfuse, name, msgs, work)
    summary = {"set": name, "guide": label, "package": package, "messages": len(msgs), "types": {}, "skipped": {}}
    for rtype in sorted(resources):
        items = resources[rtype]
        if rtype not in profiles:
            summary["skipped"][rtype] = len(items)
            continue
        d = os.path.join(work, "resources", rtype)
        os.makedirs(d, exist_ok=True)
        files = []
        for i, (src, res) in enumerate(items):
            p = os.path.join(d, f"{rtype}-{i:03d}.json")
            with open(p, "w") as fh:
                json.dump(res, fh)
            files.append(p)
        oos = validate(args.validator, package, profiles[rtype], files, os.path.join(work, f"{rtype}.oo.json"))
        errors, warnings, failed = collections.Counter(), collections.Counter(), 0
        for oo in oos:
            bad = False
            for iss in oo.get("issue", []):
                text = iss.get("details", {}).get("text") or iss.get("diagnostics", "")
                if iss.get("severity") in ("error", "fatal"):
                    errors[text] += 1
                    bad = True
                elif iss.get("severity") == "warning":
                    warnings[text] += 1
            failed += bad
        summary["types"][rtype] = {"profile": profiles[rtype], "resources": len(items), "failing": failed,
                                   "errors": dict(errors.most_common()), "warnings": sum(warnings.values())}
        print(f"{name} {rtype}: {len(items)} resources, {failed} failing, {sum(errors.values())} errors", flush=True)
    os.makedirs(os.path.join(HERE, "results"), exist_ok=True)
    with open(os.path.join(HERE, "results", f"{name}.json"), "w") as fh:
        json.dump(summary, fh, indent=1, ensure_ascii=False)
    with open(os.path.join(HERE, "results", f"{name}.md"), "w") as fh:
        fh.write(f"# {label}: {summary['messages']} messages\n\n| Resource | Profile | Resources | Pass | Fail |\n|---|---|---|---|---|\n")
        for rtype, t in summary["types"].items():
            fh.write(f"| {rtype} | `{t['profile'].rsplit('/', 1)[1]}` | {t['resources']} | {t['resources'] - t['failing']} | {t['failing']} |\n")
        if summary["skipped"]:
            fh.write("\nNo profile in the guide for: " + ", ".join(f"{k} ({v})" for k, v in summary["skipped"].items()) + "\n")
        for rtype, t in summary["types"].items():
            if t["errors"]:
                fh.write(f"\n## {rtype}\n\n")
                for msg, n in t["errors"].items():
                    fh.write(f"- {n} x {msg}\n")
    if not args.keep:
        shutil.rmtree(work, ignore_errors=True)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("-perfuse", default="perfuse")
    ap.add_argument("-validator", default=os.path.expanduser("~/.cache/fhir-validator/validator_cli.jar"))
    ap.add_argument("-sets", default="uk,fr,de")
    ap.add_argument("-keep", default="", help="keep bundles, resources and validator output under this directory")
    args = ap.parse_args()
    cache = os.path.expanduser("~/.cache/perfuse-national")
    os.makedirs(cache, exist_ok=True)
    for name in args.sets.split(","):
        run_set(name, args, cache)


if __name__ == "__main__":
    main()
