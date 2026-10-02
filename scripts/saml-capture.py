#!/usr/bin/env python3
"""Capture one SAML response from an identity provider and write it to a file.

Why this exists. An assertion is the one part of a SAML integration that cannot be obtained without a human signing in, and it is the part
most worth keeping: a document a real provider actually signed, which a test can verify forever afterwards. Keycloak's is already in
internal/saml/testdata and it is what caught the canonicalisation defect.

This stands in for Perfuse's own consumer endpoint for exactly one sign-in. It writes what arrived and nothing else - no verification, no
session, no interpretation - because the point is to preserve the document as the provider sent it. Perfuse's own handling has already been
verified by signing in for real.
"""

import base64
import datetime
import http.server
import os
import ssl
import sys
import urllib.parse

OUT = sys.argv[1] if len(sys.argv) > 1 else os.path.expanduser("~/perfuse-signin/captured")
CERT = sys.argv[2] if len(sys.argv) > 2 else os.path.expanduser("~/perfuse-signin/localhost.crt")
KEY = sys.argv[3] if len(sys.argv) > 3 else os.path.expanduser("~/perfuse-signin/localhost.key")
PORT = int(sys.argv[4]) if len(sys.argv) > 4 else 8544

os.makedirs(OUT, exist_ok=True)


class Capture(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", "0"))
        body = self.rfile.read(length).decode("utf-8", "replace")

        form = urllib.parse.parse_qs(body)
        encoded = (form.get("SAMLResponse") or [""])[0]

        if not encoded:
            self.send_error(400, "no SAMLResponse in the post")
            return

        stamp = datetime.datetime.now().strftime("%Y%m%d-%H%M%S")

        # Both forms are kept. The base64 is what arrived over the wire and is what a test replays; the decoded XML is what a person reads
        # when working out why something failed.
        with open(os.path.join(OUT, f"entra-response-{stamp}.b64"), "w") as f:
            f.write(encoded)

        decoded = base64.b64decode(encoded)
        with open(os.path.join(OUT, f"entra-response-{stamp}.xml"), "wb") as f:
            f.write(decoded)

        # The relay state too, because it is what carries the page somebody was trying to reach and it is easy to forget it exists.
        relay = (form.get("RelayState") or [""])[0]
        if relay:
            with open(os.path.join(OUT, f"entra-relaystate-{stamp}.txt"), "w") as f:
                f.write(relay)

        print(f"captured {len(encoded)} characters of base64, {len(decoded)} bytes of XML -> {OUT}", flush=True)

        page = (
            b"<!doctype html><meta charset=utf-8><title>Captured</title>"
            b"<body style='font-family:system-ui;max-width:40rem;margin:4rem auto;line-height:1.6'>"
            b"<h1>Captured</h1><p>The assertion has been saved. You can close this tab; the file has been written.</p>"
            b"<p style='color:#666'>This page is a capture point, not Perfuse. Nothing was verified here and no session was created."
            b"</p></body>"
        )
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(page)))
        self.end_headers()
        self.wfile.write(page)

    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; charset=utf-8")
        self.end_headers()
        self.wfile.write(b"waiting for a SAML response\n")

    def log_message(self, fmt, *args):
        print("%s - %s" % (self.address_string(), fmt % args), flush=True)


context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
context.load_cert_chain(CERT, KEY)

server = http.server.HTTPServer(("0.0.0.0", PORT), Capture)
server.socket = context.wrap_socket(server.socket, server_side=True)

print(f"capture point listening on https://0.0.0.0:{PORT}, writing to {OUT}", flush=True)
server.serve_forever()
