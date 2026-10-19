#!/usr/bin/env python3
"""Fills the public test instance's screens with invented data, so a visitor sees each one at work.

Everything here is made up: the people, their numbers (the NHS numbers are from the NHS's synthetic 999 range), the
hospital, the images (drawn here) and the code system. Run on the instance by setup.sh with an administrator's API token
that it revokes afterwards; safe to run again, since every step looks before it adds.

  seed.py BASE        with PERFUSE_SEED_TOKEN set (an admin token) and, for the extra admission messages, MLLP on 127.0.0.1:6660

What it adds, by screen:
  FHIR store (Views, Measures, Patient lookups): 30 patients with encounters, conditions, HbA1c results and blood pressures
  Patient index: admissions from a second system, some the same people under other numbers, some with NHS numbers
  Imaging: 8 studies (CT, MR, CR, US) drawn here, stored over STOW-RS
  Users: five colleagues with their roles; machine tokens for a fleet peer and a payer portal
  Data protection: the controller's details, an access request answered, an erasure waiting for approval, a legal hold
  Tables: a local code system (Riverside order codes)
  Direct: two correspondents in the address book
  SMART Health Links: a visit record shared with a patient
  Views and Measures: saved ones
  TEFCA: this instance declared as a participant (a demonstration certificate)
"""
import base64
import datetime
import hashlib
import json
import os
import socket
import ssl
import struct
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request
import uuid

BASE = (sys.argv[1] if len(sys.argv) > 1 else "https://test.perfuse.health").rstrip("/")
TOKEN = os.environ.get("PERFUSE_SEED_TOKEN", "")
MLLP = os.environ.get("PERFUSE_SEED_MLLP", "127.0.0.1:6660")
# A second administrator's token, for the erasure that needs another person's approval. Optional.
APPROVER = os.environ.get("PERFUSE_SEED_APPROVER_TOKEN", "")
DATA = os.environ.get("PERFUSE_SEED_DATA", "/opt/perfuse/data")
if not TOKEN:
    sys.exit("set PERFUSE_SEED_TOKEN to an administrator's API token")

TODAY = datetime.date.today()
RIVERSIDE_MRN = "urn:oid:2.16.840.1.113883.3.9999.1"


def call(method, path, body=None, ctype="application/json", raw=False, token=None):
    data = None
    if body is not None:
        data = body if isinstance(body, (bytes, bytearray)) else json.dumps(body).encode()
    req = urllib.request.Request(BASE + path, data, {"Authorization": "Bearer " + (token or TOKEN), "Content-Type": ctype,
                                                     "Accept": "application/json, application/fhir+json"}, method=method)
    try:
        with urllib.request.urlopen(req, timeout=60) as r:
            out = r.read()
            if raw:
                return r.status, out
            return r.status, (json.loads(out) if out.strip() else None)
    except urllib.error.HTTPError as e:
        text = e.read().decode(errors="replace")[:300]
        return e.code, text


def step(name, fn):
    try:
        fn()
    except Exception as e:  # one screen's data failing must not stop the others
        print(f"  {name}: failed: {e}", flush=True)


def say(*a):
    print(" ", *a, flush=True)


# ---------- people ----------
# Invented names, birth dates and addresses.
PEOPLE = [
    ("Okafor", "Amara", "F", "1958-03-14"), ("Lindqvist", "Erik", "M", "1949-11-02"), ("Moreau", "Camille", "F", "1971-06-21"),
    ("Kowalski", "Piotr", "M", "1963-01-30"), ("Haddad", "Leila", "F", "1985-09-09"), ("Brennan", "Ciaran", "M", "1954-12-17"),
    ("Rossi", "Giulia", "F", "1979-04-05"), ("Nakamura", "Kenji", "M", "1968-08-26"), ("Fernandes", "Ines", "F", "1990-02-11"),
    ("Van Dijk", "Sander", "M", "1975-07-19"), ("Adeyemi", "Tunde", "M", "1960-05-03"), ("Novak", "Petra", "F", "1952-10-28"),
    ("Garcia", "Lucia", "F", "1983-03-22"), ("Schmidt", "Jonas", "M", "1946-09-15"), ("O'Neill", "Siobhan", "F", "1966-01-07"),
    ("Petrov", "Dmitri", "M", "1972-11-30"), ("Andersen", "Freja", "F", "1988-06-12"), ("Mensah", "Kwame", "M", "1957-02-25"),
    ("Dubois", "Theo", "M", "1994-08-08"), ("Larsen", "Ingrid", "F", "1950-04-18"), ("Silva", "Rafael", "M", "1969-12-01"),
    ("Murphy", "Aoife", "F", "1977-05-29"), ("Weber", "Lukas", "M", "1981-10-10"), ("Kaur", "Harpreet", "F", "1962-07-04"),
    ("Jensen", "Mads", "M", "1955-03-03"), ("Costa", "Beatriz", "F", "1986-11-23"), ("Ali", "Yusuf", "M", "1973-09-27"),
    ("Hughes", "Megan", "F", "1948-02-14"), ("Bianchi", "Marco", "M", "1965-06-06"), ("Popescu", "Ana", "F", "1991-01-19"),
]
STREETS = ["Elm Street", "Mill Lane", "Harbour Road", "Station Road", "Church Walk", "Orchard Way", "Riverside Drive", "Park Avenue"]


def pid(i):
    return f"pt-demo-{i + 1:02d}"


def mrn(i):
    return f"RH{400100 + i}"


def fhir_people():
    """Patients, encounters, conditions and results, PUT under fixed ids so loading again changes nothing."""
    entries = []

    def put(res):
        entries.append({"resource": res, "request": {"method": "PUT", "url": f"{res['resourceType']}/{res['id']}"}})

    for i, (family, given, sex, born) in enumerate(PEOPLE):
        p = pid(i)
        put({"resourceType": "Patient", "id": p,
             "identifier": [{"system": RIVERSIDE_MRN, "value": mrn(i),
                             "type": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/v2-0203", "code": "MR"}]}}],
             "name": [{"family": family, "given": [given]}], "gender": {"F": "female", "M": "male"}[sex], "birthDate": born,
             "address": [{"line": [f"{10 + i} {STREETS[i % len(STREETS)]}"], "city": "Springfield", "postalCode": f"0{1000 + i}"}],
             "telecom": [{"system": "phone", "value": f"555-01{i:02d}"}]})
        for v in range(1 + i % 3):
            day = TODAY - datetime.timedelta(days=20 + 45 * v + i)
            put({"resourceType": "Encounter", "id": f"{p}-enc-{v}", "status": "finished",
                 "class": {"system": "http://terminology.hl7.org/CodeSystem/v3-ActCode", "code": "AMB", "display": "ambulatory"},
                 "type": [{"coding": [{"system": "http://snomed.info/sct", "code": "185349003", "display": "Encounter for check up"}]}],
                 "subject": {"reference": f"Patient/{p}"}, "period": {"start": f"{day}T09:{10 + v:02d}:00Z", "end": f"{day}T09:{40 + v:02d}:00Z"}})
        conditions = []
        if i % 5 in (0, 2):
            conditions.append(("44054006", "Diabetes mellitus type 2", "E11.9"))
        if i % 3 != 1:
            conditions.append(("38341003", "Hypertensive disorder", "I10"))
        if i == 9:
            conditions.append(("46177005", "End-stage renal disease", "N18.6"))
        for n, (sct, text, icd) in enumerate(conditions):
            put({"resourceType": "Condition", "id": f"{p}-cond-{n}",
                 "clinicalStatus": {"coding": [{"system": "http://terminology.hl7.org/CodeSystem/condition-clinical", "code": "active"}]},
                 "code": {"coding": [{"system": "http://snomed.info/sct", "code": sct, "display": text},
                                     {"system": "http://hl7.org/fhir/sid/icd-10-cm", "code": icd}], "text": text},
                 "subject": {"reference": f"Patient/{p}"}, "onsetDateTime": f"{TODAY.year - 3 - i % 6}-0{1 + i % 9}-15"})
        if any(c[0] == "44054006" for c in conditions) and i % 4 != 3:
            day = TODAY - datetime.timedelta(days=30 + i * 3)
            put({"resourceType": "Observation", "id": f"{p}-a1c", "status": "final",
                 "category": [{"coding": [{"system": "http://terminology.hl7.org/CodeSystem/observation-category", "code": "laboratory"}]}],
                 "code": {"coding": [{"system": "http://loinc.org", "code": "4548-4", "display": "Hemoglobin A1c/Hemoglobin.total in Blood"}]},
                 "subject": {"reference": f"Patient/{p}"}, "effectiveDateTime": f"{day}T08:00:00Z",
                 "valueQuantity": {"value": round(6.1 + (i % 7) * 0.45, 1), "unit": "%", "system": "http://unitsofmeasure.org", "code": "%"}})
        for v in range(2):
            day = TODAY - datetime.timedelta(days=25 + 60 * v + i)
            systolic, diastolic = 118 + (i * 7 + v * 5) % 38, 72 + (i * 5 + v * 3) % 22
            put({"resourceType": "Observation", "id": f"{p}-bp-{v}", "status": "final",
                 "category": [{"coding": [{"system": "http://terminology.hl7.org/CodeSystem/observation-category", "code": "vital-signs"}]}],
                 "code": {"coding": [{"system": "http://loinc.org", "code": "85354-9", "display": "Blood pressure panel"}]},
                 "subject": {"reference": f"Patient/{p}"}, "effectiveDateTime": f"{day}T09:20:00Z",
                 "component": [
                     {"code": {"coding": [{"system": "http://loinc.org", "code": "8480-6", "display": "Systolic blood pressure"}]},
                      "valueQuantity": {"value": systolic, "unit": "mm[Hg]", "system": "http://unitsofmeasure.org", "code": "mm[Hg]"}},
                     {"code": {"coding": [{"system": "http://loinc.org", "code": "8462-4", "display": "Diastolic blood pressure"}]},
                      "valueQuantity": {"value": diastolic, "unit": "mm[Hg]", "system": "http://unitsofmeasure.org", "code": "mm[Hg]"}}]})
    for start in range(0, len(entries), 100):
        st, out = call("POST", "/fhir", {"resourceType": "Bundle", "type": "transaction", "entry": entries[start:start + 100]},
                       "application/fhir+json")
        if st >= 300:
            raise RuntimeError(f"FHIR transaction answered {st}: {out}")
    say(f"FHIR: {len(entries)} resources for {len(PEOPLE)} patients")


# ---------- the patient index ----------
def mllp_send(messages):
    host, port = MLLP.rsplit(":", 1)
    for m in messages:
        with socket.create_connection((host, int(port)), timeout=10) as s:
            s.sendall(b"\x0b" + m.replace("\n", "\r").encode() + b"\x1c\r")
            s.settimeout(10)
            s.recv(4096)


def index_people():
    """Admissions: the hospital's own for the 30 people, then the community clinic's for some of them under its own numbers
    (spelt differently, one with day and month swapped), and patients of a partner in England carrying NHS numbers. They go
    through hospital-feed, so they are in the message store too. The NHS number is trusted, and certain matches link."""
    st, out = call("PUT", "/api/mpi/settings", {"automatic": True, "autoLink": 0.9, "review": 0.6,
                                                 "trusted": ["https://fhir.nhs.uk/Id/nhs-number"], "euidSystem": "urn:perfuse:mpi",
                                                 "showNumbersTo": "editor"})
    if st >= 300:
        raise RuntimeError(f"patient index settings answered {st}: {out}")
    now = datetime.datetime.now().strftime("%Y%m%d%H%M%S")
    msgs = []
    for i, (family, given, sex, born) in enumerate(PEOPLE):
        msgs.append(f"MSH|^~\\&|RIVERSIDEPAS|RIVERSIDE|PERFUSE|RIVERSIDE|{now}||ADT^A04^ADT_A01|HSP{i:03d}{now}|P|2.5.1\n"
                    f"EVN|A04|{now}\n"
                    f"PID|1||{mrn(i)}^^^RIVERSIDE^MR||{family}^{given}||{born.replace('-', '')}|{sex}|||{10 + i} {STREETS[i % len(STREETS)]}^^Springfield^^0{1000 + i}||555-01{i:02d}\n"
                    f"PV1|1|O|OPD^^^RIVERSIDE")
    variants = [(0, "Okafor", "Amara", "19580314"), (3, "Kowalsky", "Piotr", "19630130"), (6, "Rossi", "Guilia", "19790405"),
                (11, "Novak", "Petra", "19521028"), (14, "O'Neil", "Siobhan", "19660107"), (19, "Larsen", "Ingrid", "19501804"),
                (22, "Weber", "Lucas", "19811010"), (27, "Hughes", "Megan", "19480214")]
    for n, (i, family, given, born) in enumerate(variants):
        sex = PEOPLE[i][2]
        msgs.append(f"MSH|^~\\&|CLINICEHR|RIVERSIDECLINIC|PERFUSE|RIVERSIDE|{now}||ADT^A04^ADT_A01|IDX{n:03d}{now}|P|2.5.1\n"
                    f"EVN|A04|{now}\n"
                    f"PID|1||CL{7700 + i}^^^CLINIC^MR||{family}^{given}||{born}|{sex}|||{10 + i} {STREETS[i % len(STREETS)]}^^Springfield^^0{1000 + i}||555-01{i:02d}\n"
                    f"PV1|1|O|CLINIC^^^RIVERSIDECLINIC")
    # Patients of a partner in England. Their NHS numbers are from the synthetic 999 range, check digit included; Hannah
    # Clarke arrives twice under two local numbers and one NHS number; Thomas Wright's NHS number is mistyped, which the
    # index keeps but never links on.
    uk = [("Patel", "Priya", "F", "19760412", "9990000018", "UK5500"), ("Evans", "Owen", "M", "19580923", "9990000026", "UK5501"),
          ("Clarke", "Hannah", "F", "19890130", "9990000034", "UK5502"), ("Clark", "Hannah", "F", "19890130", "9990000034", "UKX9001"),
          ("Wright", "Thomas", "M", "19640707", "9990000019", "UK5504")]
    for n, (family, given, sex, born, nhs, local) in enumerate(uk):
        msgs.append(f"MSH|^~\\&|PARTNEREPR|NORTHGATE|PERFUSE|RIVERSIDE|{now}||ADT^A04^ADT_A01|NHS{n:03d}{now}|P|2.5.1\n"
                    f"EVN|A04|{now}\n"
                    f"PID|1||{local}^^^NORTHGATE^MR~{nhs}^^^NHS^NH||{family}^{given}||{born}|{sex}\n"
                    f"PV1|1|O|OPD^^^NORTHGATE")
    st, out = call("GET", "/api/mpi")
    if st == 200 and isinstance(out, dict) and (out.get("counts") or {}).get("records", 0) >= 40:
        say("patient index: already filled")
        return
    mllp_send(msgs)
    say(f"patient index: {len(msgs)} admissions from three systems")


# ---------- imaging ----------
def dicom_element(tag, vr, value):
    group, elem = tag
    if isinstance(value, str):
        value = value.encode("ascii")
        if len(value) % 2:
            value += b"\0" if vr == "UI" else b" "
    if vr in ("OB", "OW", "SQ", "UN", "UT"):
        return struct.pack("<HH2sHI", group, elem, vr.encode(), 0, len(value)) + value
    return struct.pack("<HH2sH", group, elem, vr.encode(), len(value)) + value


def us(v):
    return struct.pack("<H", v)


def dicom_file(patient, study, series, instance):
    """A small 16-bit monochrome image, drawn here, as a DICOM Part 10 file (explicit VR little endian)."""
    rows = cols = 96
    pixels = bytearray()
    cx, cy = cols / 2 + (instance["n"] - 2) * 6, rows / 2
    for y in range(rows):
        for x in range(cols):
            d = ((x - cx) ** 2 + ((y - cy) * series["squash"]) ** 2) ** 0.5
            v = 3000 - int(d * 70) if d < 36 else 300 + (x * 3 + y * 2) % 200
            if 10 < d < 14:
                v = 3600
            pixels += struct.pack("<H", max(0, min(4095, v)))
    sop_class = series["sop"]
    meta_body = (dicom_element((0x0002, 0x0001), "OB", b"\x00\x01")
                 + dicom_element((0x0002, 0x0002), "UI", sop_class)
                 + dicom_element((0x0002, 0x0003), "UI", instance["uid"])
                 + dicom_element((0x0002, 0x0010), "UI", "1.2.840.10008.1.2.1")
                 + dicom_element((0x0002, 0x0012), "UI", "2.25.16438123578235711")
                 + dicom_element((0x0002, 0x0013), "SH", "PERFUSE_DEMO"))
    meta = dicom_element((0x0002, 0x0000), "UL", struct.pack("<I", len(meta_body))) + meta_body
    ds = b"".join([
        dicom_element((0x0008, 0x0016), "UI", sop_class),
        dicom_element((0x0008, 0x0018), "UI", instance["uid"]),
        dicom_element((0x0008, 0x0020), "DA", study["date"]),
        dicom_element((0x0008, 0x0030), "TM", study["time"]),
        dicom_element((0x0008, 0x0050), "SH", study["accession"]),
        dicom_element((0x0008, 0x0060), "CS", series["modality"]),
        dicom_element((0x0008, 0x0080), "LO", "Riverside Health (demonstration)"),
        dicom_element((0x0008, 0x0090), "PN", "Carter^Alex^^Dr"),
        dicom_element((0x0008, 0x1030), "LO", study["description"]),
        dicom_element((0x0008, 0x103E), "LO", series["description"]),
        dicom_element((0x0010, 0x0010), "PN", f"{patient[0]}^{patient[1]}"),
        dicom_element((0x0010, 0x0020), "LO", patient[4]),
        dicom_element((0x0010, 0x0030), "DA", patient[3].replace("-", "")),
        dicom_element((0x0010, 0x0040), "CS", patient[2]),
        dicom_element((0x0018, 0x0015), "CS", series["body"]),
        dicom_element((0x0020, 0x000D), "UI", study["uid"]),
        dicom_element((0x0020, 0x000E), "UI", series["uid"]),
        dicom_element((0x0020, 0x0010), "SH", study["accession"][-6:]),
        dicom_element((0x0020, 0x0011), "IS", str(series["number"])),
        dicom_element((0x0020, 0x0013), "IS", str(instance["n"])),
        dicom_element((0x0028, 0x0002), "US", us(1)),
        dicom_element((0x0028, 0x0004), "CS", "MONOCHROME2"),
        dicom_element((0x0028, 0x0010), "US", us(rows)),
        dicom_element((0x0028, 0x0011), "US", us(cols)),
        dicom_element((0x0028, 0x0100), "US", us(16)),
        dicom_element((0x0028, 0x0101), "US", us(12)),
        dicom_element((0x0028, 0x0102), "US", us(11)),
        dicom_element((0x0028, 0x0103), "US", us(0)),
        dicom_element((0x0028, 0x1050), "DS", "2000"),
        dicom_element((0x0028, 0x1051), "DS", "4000"),
        dicom_element((0x7FE0, 0x0010), "OW", bytes(pixels)),
    ])
    return b"\0" * 128 + b"DICM" + meta + ds


def uid(*parts):
    return "2.25." + str(int(hashlib.sha256("|".join(parts).encode()).hexdigest()[:30], 16))


def imaging():
    st, out = call("GET", "/dicomweb/studies?limit=1")
    if st == 200 and out:
        say("imaging: already has studies")
        return
    kinds = [("CT", "CT chest with contrast", "CHEST", "1.2.840.10008.5.1.4.1.1.2", 1.0),
             ("MR", "MR brain without contrast", "HEAD", "1.2.840.10008.5.1.4.1.1.4", 1.15),
             ("CR", "Chest X-ray, two views", "CHEST", "1.2.840.10008.5.1.4.1.1.1", 0.8),
             ("US", "Ultrasound abdomen", "ABDOMEN", "1.2.840.10008.5.1.4.1.1.6.1", 1.6)]
    stored = 0
    for s in range(8):
        i = (s * 4 + 1) % len(PEOPLE)
        patient = PEOPLE[i] + (mrn(i),)
        modality, desc, body, sop, squash = kinds[s % len(kinds)]
        day = TODAY - datetime.timedelta(days=3 + s * 9)
        study = {"uid": uid("study", str(s)), "date": day.strftime("%Y%m%d"), "time": f"{9 + s % 8:02d}1500",
                 "accession": f"ACC{260100 + s}", "description": desc}
        parts = []
        for n_series in range(2 if modality in ("CT", "MR") else 1):
            series = {"uid": uid("series", str(s), str(n_series)), "modality": modality, "number": n_series + 1,
                      "description": desc.split(",")[0] + (" axial" if n_series == 0 else " coronal"), "body": body,
                      "sop": sop, "squash": squash if n_series == 0 else 1 / squash}
            for n in range(1, 4):
                parts.append(dicom_file(patient, study, series, {"uid": uid("sop", str(s), str(n_series), str(n)), "n": n}))
        boundary = "perfuse-demo-" + uuid.uuid4().hex
        body_bytes = b"".join(b"--" + boundary.encode() + b"\r\nContent-Type: application/dicom\r\n\r\n" + p + b"\r\n" for p in parts)
        body_bytes += b"--" + boundary.encode() + b"--\r\n"
        st, out = call("POST", "/dicomweb/studies", body_bytes,
                       f'multipart/related; type="application/dicom"; boundary={boundary}', raw=True)
        if st >= 300:
            raise RuntimeError(f"STOW-RS answered {st}: {out[:200]}")
        stored += len(parts)
    say(f"imaging: {stored} images in 8 studies")


# ---------- people who use the console, and machine tokens ----------
COLLEAGUES = [("dana.whitfield", "admin"), ("sam.okoro", "editor"), ("priya.raman", "editor"), ("jon.berg", "operator"),
              ("maria.lopez", "monitor")]


def users():
    st, out = call("GET", "/api/users")
    have = {u["username"] for u in (out if isinstance(out, list) else out.get("users", []))} if st == 200 else set()
    for name, role in COLLEAGUES:
        if name in have:
            continue
        # A long random password nobody is told: these accounts are there to be looked at, never signed in with.
        st, out = call("POST", "/api/users", {"username": name, "password": base64.urlsafe_b64encode(os.urandom(24)).decode(), "role": role})
        if st >= 300:
            raise RuntimeError(f"creating {name}: {st} {out}")
    st, out = call("GET", "/api/tokens")
    labels = {t.get("label") for t in (out if isinstance(out, list) else (out or {}).get("tokens", []))} if st == 200 else set()
    for label, role in [("fleet-watcher-riverside-clinic", "viewer"), ("payer-portal-prior-auth", "editor")]:
        if label not in labels:
            call("POST", "/api/tokens", {"label": label, "role": role})
    say(f"users: {len(COLLEAGUES)} colleagues, 2 machine tokens")


# ---------- data protection ----------
def settings(changes):
    st, out = call("PUT", "/api/settings/values", {"changes": changes})
    if st >= 300:
        raise RuntimeError(f"settings answered {st}: {out}")


def data_protection():
    settings({"privacy.controller": "Riverside Health (demonstration), 1 Example Way, Springfield",
              "privacy.dpo": "Data Protection Officer, dpo@riverside.example",
              "privacy.dataLocation": "Amazon Web Services, us-east-1 (Virginia): the demonstration server and its backups"})
    st, out = call("GET", "/api/privacy")
    have = {r.get("subject") for r in ((out or {}).get("requests") or [])} if st == 200 and isinstance(out, dict) else set()
    added = 0

    def request(body):
        nonlocal added
        if body["subject"] in have:
            return None
        st, out = call("POST", "/api/privacy/requests", body)
        if st >= 300 or not isinstance(out, dict):
            raise RuntimeError(f"privacy request answered {st}: {out}")
        added += 1
        return out.get("id")

    # Answered: a copy given, the request closed.
    rid = request({"kind": "access", "subject": "Ms P. Patel (letter of 2 October)", "identifiers": ["UK5500", "9990000018"],
                   "note": "Asked for a copy of everything held about her; identity checked at the front desk."})
    if rid:
        call("GET", f"/api/privacy/requests/{rid}/export", raw=True)
        call("POST", f"/api/privacy/requests/{rid}/close", {"Status": "done", "Outcome": "Copy sent by secure e-mail."})
    # Carried out: approved by a second administrator, then erased; the ledger keeps a salted hash, never the number.
    rid = request({"kind": "erasure", "subject": "Mr T. Wright (form of 30 September)", "identifiers": ["UK5504", "9990000019"],
                   "note": "Registered by mistake under a mistyped NHS number; asks for the record to be erased."})
    if rid and APPROVER:
        approve = call("POST", f"/api/privacy/requests/{rid}/approve", {}, token=APPROVER)
        if approve[0] < 300:
            call("POST", f"/api/privacy/requests/{rid}/erase", {})
    # Waiting for the second administrator.
    request({"kind": "erasure", "subject": "Mr O. Evans (e-mail of 12 October)", "identifiers": ["UK5501", "9990000026"],
             "note": "Moved abroad; asks for his record to be erased. Waiting for a second administrator to approve."})
    request({"kind": "restriction", "subject": "Mrs H. Clarke (complaint, reference C-2026-118)", "identifiers": ["UK5502", "UKX9001"],
             "note": "Disputes the accuracy of an admission; kept, not changed, while the complaint is settled."})
    if not have:
        until = (TODAY + datetime.timedelta(days=180)).isoformat()
        call("POST", "/api/privacy/holds", {"Identifiers": [mrn(5)], "Reason": "Litigation hold: claim 2026-CV-0412 (invented)",
                                            "Until": until})
    say(f"data protection: {added} requests added")


# ---------- terminology ----------
def terminology():
    st, out = call("GET", "/api/terminology/systems")
    if st == 200 and "riverside" in json.dumps(out).lower():
        say("code systems: already imported")
        return
    rows = ["code,display,status",
            "RH-LAB-001,Full blood count,active", "RH-LAB-002,Urea and electrolytes,active", "RH-LAB-003,Liver function tests,active",
            "RH-LAB-004,HbA1c,active", "RH-LAB-005,Lipid profile,active", "RH-LAB-006,Thyroid function tests,active",
            "RH-LAB-007,C-reactive protein,active", "RH-LAB-008,Coagulation screen,active", "RH-LAB-009,Blood culture,active",
            "RH-LAB-010,Urine culture,active", "RH-LAB-011,Vitamin D,active", "RH-LAB-012,Ferritin,active",
            "RH-RAD-001,Chest X-ray,active", "RH-RAD-002,CT head,active", "RH-RAD-003,MR knee,active", "RH-RAD-004,Ultrasound abdomen,active",
            "RH-LAB-090,Erythrocyte sedimentation rate (replaced by CRP),inactive"]
    q = ("/api/terminology/import?format=csv&system=" + urllib.request.quote("https://riverside.example/fhir/CodeSystem/order-codes")
         + "&version=2026-10&name=" + urllib.request.quote("Riverside order codes"))
    st, out = call("POST", q, ("\n".join(rows) + "\n").encode(), "text/csv")
    if st >= 300:
        raise RuntimeError(f"import answered {st}: {out}")
    say("code systems: Riverside order codes")


# ---------- Direct ----------
def self_signed(cn, email):
    with tempfile.TemporaryDirectory() as d:
        subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes", "-days", "398",
                        "-subj", f"/O={cn} (demonstration)/CN={email}", "-addext", f"subjectAltName=email:{email}",
                        "-keyout", os.path.join(d, "k"), "-out", os.path.join(d, "c")], check=True, capture_output=True)
        return open(os.path.join(d, "c")).read()


def direct():
    st, out = call("GET", "/api/direct")
    if st == 200 and "riverside" in json.dumps(out).lower():
        say("direct: address book already filled")
        return
    for address, name, org, notes in [
            ("referrals@direct.riverside-cardiology.example", "Referrals desk", "Riverside Cardiology", "Cardiology referrals and letters back."),
            ("results@direct.springfield-lab.example", "Results office", "Springfield Reference Laboratory", "Send-out results.")]:
        st, out = call("PUT", "/api/direct/contacts/" + urllib.request.quote(address),
                       {"name": name, "organisation": org, "notes": notes, "certificate": self_signed(org, address)})
        if st >= 300:
            raise RuntimeError(f"direct contact answered {st}: {out}")
    say("direct: 2 correspondents")


# ---------- SMART Health Links ----------
def health_links():
    st, out = call("GET", "/api/shl")
    if st == 200 and (out if isinstance(out, list) else (out or {}).get("links")):
        say("health links: already shared")
        return
    st, summary = call("POST", "/api/shl/summary", {"patient": pid(2)})
    content = summary if st < 300 and isinstance(summary, dict) and summary.get("resourceType") else {
        "resourceType": "Bundle", "type": "collection",
        "entry": [{"resource": {"resourceType": "Patient", "name": [{"family": "Moreau", "given": ["Camille"]}], "birthDate": "1971-06-21"}}]}
    st, out = call("POST", "/api/shl", {"label": "Camille Moreau: visit summary", "content": content, "passcode": "demo-1234",
                                         "expiresInDays": 30})
    if st >= 300:
        raise RuntimeError(f"health link answered {st}: {out}")
    say("health links: 1 visit record")


# ---------- views and measures ----------
def views_and_measures():
    st, out = call("GET", "/api/views")
    examples = (out or {}).get("examples", []) if isinstance(out, dict) else []
    saved = 0
    for ex in examples[:3]:
        view = ex.get("view") or ex
        name = (view.get("name") or "view").replace(" ", "_")
        if call("PUT", f"/api/views/{name}", {"view": view})[0] < 300:
            saved += 1
    st, out = call("GET", "/api/measures")
    measures = 0
    for ex in (out or {}).get("examples", []) if isinstance(out, dict) else []:
        m = ex.get("measure") or ex
        if call("PUT", "/api/measures", {"measure": m})[0] < 300:
            measures += 1
    say(f"views and measures: {saved} views, {measures} measures saved")


# ---------- TEFCA ----------
def tefca():
    folder = os.path.join(DATA, "demo-tefca")
    cert, key = os.path.join(folder, "participant.pem"), os.path.join(folder, "participant.key")
    if not os.path.exists(cert):
        os.makedirs(folder, exist_ok=True)
        subprocess.run(["openssl", "req", "-x509", "-newkey", "ec", "-pkeyopt", "ec_paramgen_curve:P-256", "-nodes", "-days", "398",
                        "-subj", "/O=Riverside Health (demonstration)/CN=Riverside Health TEFCA participant (not issued by a QHIN)",
                        "-addext", "subjectAltName=URI:https://test.perfuse.health/fhir", "-keyout", key, "-out", cert],
                       check=True, capture_output=True)
    settings({"tefca.organisationName": "Riverside Health (demonstration)",
              "tefca.organisationOID": "2.16.840.1.113883.3.9999",
              "tefca.qhinEndpoint": "https://qhin.example/fhir",
              "tefca.purposes": ["treatment", "individual_access"],
              "tefca.certificatePath": cert, "tefca.keyPath": key,
              "tefca.auditPath": os.path.join(folder, "audit.jsonl")})
    say("TEFCA: declared as a participant (takes effect at the next restart)")


def main():
    print(f"seeding {BASE}", flush=True)
    for name, fn in [("FHIR", fhir_people), ("patient index", index_people), ("imaging", imaging), ("users", users),
                     ("data protection", data_protection), ("code systems", terminology), ("direct", direct),
                     ("health links", health_links), ("views and measures", views_and_measures), ("TEFCA", tefca)]:
        step(name, fn)


if __name__ == "__main__":
    main()
