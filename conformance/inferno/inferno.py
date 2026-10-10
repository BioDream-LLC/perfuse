"""Shared helpers for running an Inferno test kit against Perfuse.

Inferno runs in Docker (see kit.sh) and answers its JSON API at http://localhost/api. Perfuse runs on this machine and is
reached from the containers as https://host.docker.internal:<port>.
"""
import base64
import collections
import json
import os
import ssl
import subprocess
import tempfile
import time
import urllib.parse
import urllib.request
import uuid

HERE = os.path.dirname(os.path.abspath(__file__))
WORK = os.path.join(HERE, ".work")
INFERNO = os.environ.get("INFERNO_URL", "http://localhost")

# Perfuse's certificate is from a test CA made by certs.sh, so calls from this machine do not verify it.
INSECURE = ssl._create_unverified_context()


def call(method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(INFERNO + "/api" + path, data=data, method=method, headers={"Content-Type": "application/json"})
    with urllib.request.urlopen(req, timeout=120) as r:
        return json.load(r)


def session(suite_id, preset=None):
    body = {"test_suite_id": suite_id}
    if preset:
        body["preset_id"] = preset
    return call("POST", "/test_sessions", body)["id"]


def run(suite_id, inputs, on_waiting=None, group_id=None, limit=1800):
    """Starts a run of the whole suite (or one group) and waits for it. on_waiting(run_id) is called while Inferno waits for
    something outside it, such as a reviewer's decision; it returns True when it did something."""
    sid = session(suite_id)
    body = {"test_session_id": sid, "inputs": [{"name": k, "value": v} for k, v in inputs.items()]}
    body["test_group_id" if group_id else "test_suite_id"] = group_id or suite_id
    rid = call("POST", "/test_runs", body)["id"]
    t0 = time.time()
    while time.time() - t0 < limit:
        status = call("GET", f"/test_runs/{rid}")["status"]
        if status in ("done", "cancelled"):
            break
        if status == "waiting" and on_waiting:
            on_waiting(rid)
        time.sleep(4)
    print(f"run {status} after {round(time.time() - t0)}s", flush=True)
    return sid


def report(sid, name):
    """Writes the session's results to .work/<name>/results.json and prints the counts and every test that did not pass."""
    results = call("GET", f"/test_sessions/{sid}/results")
    out = os.path.join(WORK, name)
    os.makedirs(out, exist_ok=True)
    with open(os.path.join(out, "results.json"), "w") as f:
        json.dump(results, f, indent=1)
    tests = [r for r in results if r.get("test_id")]
    print(dict(collections.Counter(r["result"] for r in tests)))
    for r in tests:
        if r["result"] not in ("pass", "omit", "skip"):
            errors = [m.get("message", "")[:200] for m in r.get("messages", []) if m.get("type") == "error"][:2]
            print(" ", r["result"], r["test_id"].split("-")[-1], "|", (r.get("result_message") or "")[:240], errors)
    return tests


def kit_path(name):
    return os.path.join(WORK, "kits", name)


def _der_int(n):
    b = n.to_bytes((n.bit_length() + 8) // 8, "big")
    return b"\x02" + _der_len(len(b)) + b


def _der_len(n):
    if n < 0x80:
        return bytes([n])
    b = n.to_bytes((n.bit_length() + 7) // 8, "big")
    return bytes([0x80 | len(b)]) + b


def _rsa_pem(jwk):
    """A PKCS#1 PEM for an RSA private JWK, so openssl can sign with it."""
    num = lambda k: int.from_bytes(base64.urlsafe_b64decode(jwk[k] + "=" * (-len(jwk[k]) % 4)), "big")
    body = _der_int(0) + b"".join(_der_int(num(k)) for k in ("n", "e", "d", "p", "q", "dp", "dq", "qi"))
    der = b"\x30" + _der_len(len(body)) + body
    lines = base64.encodebytes(der).decode().replace("\n", "")
    return "-----BEGIN RSA PRIVATE KEY-----\n" + "\n".join(lines[i:i + 64] for i in range(0, len(lines), 64)) + "\n-----END RSA PRIVATE KEY-----\n"


JWKS_URL = "https://raw.githubusercontent.com/inferno-framework/smart-app-launch-test-kit/v1.0.3/lib/smart_app_launch/smart_jwks.json"


def inferno_jwks():
    """Inferno's published test keys, from the SMART App Launch kit every Inferno kit builds on. The private halves are public
    test material, not secrets; they are downloaded once into .work rather than kept in this repository."""
    path = os.path.join(WORK, "inferno-jwks.json")
    if not os.path.exists(path):
        os.makedirs(WORK, exist_ok=True)
        with urllib.request.urlopen(JWKS_URL, timeout=30) as r, open(path, "wb") as f:
            f.write(r.read())
    with open(path) as f:
        return json.load(f)


def public_jwks():
    keys = []
    for k in inferno_jwks()["keys"]:
        if "verify" in k.get("key_ops", ["verify"]):
            keys.append({x: v for x, v in k.items() if x not in ("d", "p", "q", "dp", "dq", "qi")})
    return {"keys": keys}


def backend_token(token_url, client_id="inferno-backend", scope="system/*.cruds", local_url=None):
    """A SMART Backend Services access token: a client assertion signed RS384 with Inferno's key, exchanged at token_url
    (reached at local_url from this machine when the two differ)."""
    jwk = next(k for k in inferno_jwks()["keys"] if k["kty"] == "RSA" and "d" in k)
    b64 = lambda b: base64.urlsafe_b64encode(b).rstrip(b"=").decode()
    now = int(time.time())
    signing_input = b64(json.dumps({"alg": "RS384", "typ": "JWT", "kid": jwk["kid"]}).encode()) + "." + b64(json.dumps(
        {"iss": client_id, "sub": client_id, "aud": token_url, "iat": now, "exp": now + 240, "jti": str(uuid.uuid4())}).encode())
    with tempfile.NamedTemporaryFile("w", suffix=".pem", delete=False) as f:
        f.write(_rsa_pem(jwk))
    try:
        sig = subprocess.run(["openssl", "dgst", "-sha384", "-sign", f.name], input=signing_input.encode(),
                             capture_output=True, check=True).stdout
    finally:
        os.unlink(f.name)
    form = urllib.parse.urlencode({
        "grant_type": "client_credentials", "scope": scope,
        "client_assertion_type": "urn:ietf:params:oauth:client-assertion-type:jwt-bearer",
        "client_assertion": signing_input + "." + b64(sig)}).encode()
    with urllib.request.urlopen(urllib.request.Request(local_url or token_url, form), context=INSECURE, timeout=30) as r:
        return json.load(r)["access_token"]
