#!/bin/sh
# run.sh <crd|dtr|pas|uscore> - runs one Inferno suite against a freshly started Perfuse, end to end: the kit at its pinned
# version (kit.sh), Perfuse from bin/perfuse with the suite's flags (serve.sh), then the suite's run.py. Results are printed and
# written to .work/<suite>/results.json. Needs Docker, Python 3, openssl and git.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
suite=${1:?usage: run.sh crd|dtr|pas|uscore}
"$here/kit.sh" "$suite"
"$here/serve.sh" "$suite"
PERFUSE_TOKEN=$(cat "$here/.work/$suite/token") exec python3 "$here/$suite/run.py"
