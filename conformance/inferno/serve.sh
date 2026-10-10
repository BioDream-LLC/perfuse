#!/bin/sh
# serve.sh <suite> [extra perfuse flags...] - starts Perfuse for one suite on https://host.docker.internal:<port>, from a fresh
# database in .work/<suite>, with the test certificate from certs.sh. Writes .work/<suite>/token, an editor API token for the
# run scripts' console calls (the reviewer queue, loading data).
#
# The suite's own flags are in <suite>/flags; anything after the suite name is added to them.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
suite=${1:?usage: serve.sh <suite> [flags...]}
shift
work="$here/.work/$suite"
perfuse=${PERFUSE:-"$here/../../bin/perfuse"}
port=$(sed -n 's/^port=//p' "$here/$suite/flags")
[ -x "$perfuse" ] || { echo "serve.sh: build Perfuse first (go build -o bin/perfuse ./cmd/perfuse), or set PERFUSE" >&2; exit 1; }

"$here/certs.sh"
pkill -f "perfuse serve .*-addr 0.0.0.0:$port" 2>/dev/null || true
sleep 1
rm -rf "$work"
mkdir -p "$work"
for f in "$here/$suite"/*.yaml; do case "$f" in *clients-extra.yaml) ;; *) cp "$f" "$work"/ ;; esac; done 2>/dev/null || true
cd "$work"

# The flags file: port=, then the flags, a flag and its value per line. A suite that names clients.yaml gets Inferno's backend
# client (clients.py).
flags=$(sed -n '/^-/p' "$here/$suite/flags" | tr '\n' ' ')
case "$flags" in *clients.yaml*) python3 "$here/clients.py" "$suite" > clients.yaml ;; esac

# shellcheck disable=SC2086 # flags are one word each
nohup "$perfuse" serve -db perfuse.db -addr "0.0.0.0:$port" -tls-cert ../certs/cert.pem -tls-key ../certs/key.pem \
  -public-url "https://host.docker.internal:$port" -fleet-label "Inferno $suite" $flags "$@" > serve.log 2>&1 </dev/null &

i=0
until curl -skf "https://127.0.0.1:$port/fhir/metadata" >/dev/null 2>&1; do
  i=$((i + 1)); [ $i -lt 30 ] || { echo "serve.sh: Perfuse did not start; see $work/serve.log" >&2; tail -5 serve.log >&2; exit 1; }
  sleep 1
done
"$perfuse" token create -db perfuse.db -label inferno-run -role editor 2>/dev/null | grep -oE '[A-Za-z0-9_-]{30,}' | head -1 > token
chmod 600 token
echo "Perfuse is up for $suite on https://host.docker.internal:$port"
