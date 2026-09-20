#!/usr/bin/env bash
# Runs Perfuse against the Keycloak realm created by keycloak-saml-setup.sh, and signs in through a browser.
#
# This is the verification the queue asked for: an assertion produced by a real identity provider, verified by the real code path,
# ending in a real session. Everything before it proved Perfuse agrees with its own reading of the specification.
#
# Started here rather than by hand so it can be re-run. A verification that needs somebody to remember eleven steps is a claim.
set -euo pipefail

cd "$(dirname "$0")/.."

DIR=${SAML_VERIFY_DIR:-/tmp/perfuse-saml-verify}
PORT=${SAML_VERIFY_PORT:-8099}
KC=http://127.0.0.1:8080

rm -rf "$DIR"
mkdir -p "$DIR/channels"

if [ ! -s /tmp/keycloak-idp.crt ]; then
  echo "no /tmp/keycloak-idp.crt: run scripts/keycloak-saml-setup.sh first" >&2
  exit 1
fi

# The reply URL has to match what the client is registered with exactly, or the response is refused for being destined elsewhere.
# It also has to be somewhere the browser can actually reach, because the identity provider renders a form pointing at it - a value
# that does not resolve means nothing is ever delivered and neither side logs anything.
cat > "$DIR/saml.yaml" <<YAML
entity_id: https://perfuse.test
acs_url: http://127.0.0.1:$PORT/auth/saml/acs
idp_sso_url: $KC/realms/perfuse/protocol/saml
idp_cert_file: /tmp/keycloak-idp.crt
groups_attribute: groups
label: Sign in with Keycloak
create_users: true
roles:
  editor:
    - perfuse-editors
  admin:
    - perfuse-admins
YAML

echo "== starting perfuse on :$PORT"
./bin/perfuse serve \
  -addr "127.0.0.1:$PORT" \
  -channels "$DIR/channels" \
  -db "$DIR/perfuse.db" \
  -saml "$DIR/saml.yaml" \
  -insecure \
  -engine=false \
  > "$DIR/serve.log" 2>&1 &

echo $! > "$DIR/perfuse.pid"

for _ in $(seq 1 40); do
  if curl -s -o /dev/null "http://127.0.0.1:$PORT/api/health"; then
    echo "perfuse is up"
    exit 0
  fi
  sleep 0.5
done

echo "perfuse did not start; log follows" >&2
tail -20 "$DIR/serve.log" >&2
exit 1
