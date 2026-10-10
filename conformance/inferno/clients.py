#!/usr/bin/env python3
"""Prints a SMART clients file with Inferno's backend client, registered with the public half of Inferno's published test keys
(inferno.py downloads them), allowed every system scope, then the suite's own clients-extra.yaml if it has one.
Usage: clients.py [suite]"""
import json
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import inferno  # noqa: E402

print("clients:")
print("  - id: inferno-backend")
print("    name: Inferno")
print("    kind: backend")
print('    scopes: ["system/*"]')
print("    jwks: " + json.dumps(inferno.public_jwks()))

extra = os.path.join(os.path.dirname(os.path.abspath(__file__)), sys.argv[1] if len(sys.argv) > 1 else "", "clients-extra.yaml")
if len(sys.argv) > 1 and os.path.exists(extra):
    print(open(extra).read().rstrip())
