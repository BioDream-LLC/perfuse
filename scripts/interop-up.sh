#!/usr/bin/env bash
# Starts every container the interop checks need, and says which tests each one unlocks.
#
# These checks exist because the rest of the suite cannot do what they do. Everywhere else, both halves of a protocol were written
# here: a document Perfuse signed and read back with the code that signed it, a frame Perfuse wrote and parsed with the code that wrote
# it. That is agreement with oneself, and on 17 September it was shown to be capable of hiding a total failure - the SAML canonicaliser
# passed a thousand such tests while being unable to accept any assertion a real identity provider produced.
#
# Two of the three findings so far were defects nothing else would have caught. The third, mutual TLS, was correct. Both outcomes
# needed the same method.
set -euo pipefail

cd "$(dirname "$0")/.."

if ! docker info >/dev/null 2>&1; then
  cat >&2 <<'MSG'
No container runtime. On macOS:

  brew install colima docker
  colima start --cpu 4 --memory 8

Nothing in `make check` or `make e2e` needs this. These are the checks that talk to other people's software.
MSG
  exit 1
fi

start() {
  local name=$1
  shift

  if docker ps --format '{{.Names}}' | grep -qx "$name"; then
    echo "== $name already running"

    return
  fi

  echo "== starting $name"
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" "$@" >/dev/null
}

start hapi -p 8090:8080 hapiproject/hapi:latest
start pg -p 5433:5432 -e POSTGRES_PASSWORD=perfuse -e POSTGRES_DB=perfusetest postgres:16-alpine

# OpenSSH's sftp-server, which is a different implementation from the Go library the client uses and the one on the far end of nearly
# every real feed. The upload directory is bind-mounted so a test can see what landed rather than asking the server.
mkdir -p "$HOME/sftp-test/upload"
start sftpd -p 2222:22 -v "$HOME/sftp-test/upload:/home/perfuse/upload" atmoz/sftp:latest perfuse:perfuse:1001
start activemq -p 61613:61613 -p 8161:8161 apache/activemq-classic:latest

# Orthanc needs a configuration file to accept stores from an unknown modality, which is the sensible default for a PACS and the
# opposite of what a test wants.
cat > "$HOME/orthanc-config.json" <<'JSON'
{
  "Name": "Orthanc for Perfuse tests",
  "DicomServerEnabled": true,
  "DicomAet": "ORTHANC",
  "DicomPort": 4242,
  "RemoteAccessAllowed": true,
  "AuthenticationEnabled": false,
  "DicomAlwaysAllowStore": true,
  "DicomAlwaysAllowEcho": true,
  "DicomModalities": {}
}
JSON

start orthanc -p 4242:4242 -p 8042:8042 \
  -v "$HOME/orthanc-config.json:/etc/orthanc/orthanc.json:ro" \
  jodogne/orthanc:latest

echo "== mutual TLS server"
./scripts/mtls-server.sh >/dev/null

echo "== keycloak"
start keycloak -p 8080:8080 \
  -e KC_BOOTSTRAP_ADMIN_USERNAME=admin \
  -e KC_BOOTSTRAP_ADMIN_PASSWORD=admin \
  quay.io/keycloak/keycloak:26.0 start-dev

echo
echo "waiting for HAPI, which is the slow one"
for _ in $(seq 1 120); do
  if curl -s -o /dev/null "http://127.0.0.1:8090/fhir/metadata"; then break; fi
  sleep 1
done

cat <<'MSG'

Ready. What each one unlocks:

  HAPI FHIR       go test ./internal/engine/ -run HAPI -v
  ActiveMQ        go test ./internal/engine/ -run 'RealBroker|NullByte|SeveralMessages' -v
  nginx mTLS      go test ./internal/engine/ -run 'ClientCertificate|OpenSSL' -v
  Orthanc PACS    go test ./internal/engine/ -run 'RealPACS|CalledAE' -v
  PostgreSQL      go test ./internal/engine/ -run 'RealPostgres|Placeholder' -v
  OpenSSH SFTP    go test ./internal/engine/ -run 'OpenSSHServer|SFTPRefusesAWrong' -v
  Mirth 4.5.2     ./scripts/mirth-author-channel.sh   then   go test ./internal/mirth/ -run RealMirth -v
  Keycloak        scripts/keycloak-saml-setup.sh && scripts/saml-verify-serve.sh
                  then: cd web && npx playwright test --config playwright-saml.config.ts

Every one of those tests skips rather than fails when its container is absent, so `make check` passes on a
machine with no Docker. That is deliberate: a check that needs Docker is a check people stop running.

Stop everything:  docker rm -f hapi activemq orthanc pg sftpd mtls-nginx keycloak
MSG
