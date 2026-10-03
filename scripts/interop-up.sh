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
start activemq -p 61613:61613 -p 8161:8161 -p 5673:5672 apache/activemq-classic:latest

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

# LocalStack, for S3, SQS and SNS. It does not check signatures by default; internal/awsv4 is held to AWS's published example for that.
start localstack -p 4566:4566 -e SERVICES=s3,sqs,sns localstack/localstack:4

# AMQP 1.0: RabbitMQ 4 speaks it natively. ActiveMQ above does too, on 5673. Azurite is Microsoft's Blob Storage emulator, and checks
# Shared Key signatures the way the service does.
start rabbitmq -p 5672:5672 -p 15672:15672 -e RABBITMQ_DEFAULT_USER=perfuse -e RABBITMQ_DEFAULT_PASS=perfuse rabbitmq:4-management
start azurite -p 10000:10000 mcr.microsoft.com/azure-storage/azurite azurite-blob --blobHost 0.0.0.0

# The Mirth family: where a site leaving Mirth goes. Mirth 4.5.2 is the last open-source release; the Open Integration Engine (the
# Eclipse fork) and BridgeLink (Innovar's fork) continue it. OIE publishes images only up to 4.5.2, so 4.6.0 is built locally from
# the project's signed release tarball, with its checksum verified first. OIE's image is amd64 only and runs emulated on Apple silicon.
echo "== the Mirth family"
start mirth -p 8443:8443 nextgenhealthcare/connect:4.5.2
start oie452 --platform linux/amd64 -p 8444:8443 openintegrationengine/engine:latest
start bridgelink -p 8445:8443 innovarhealthcare/bridgelink:latest
if ! docker image inspect perfuse-local/oie:4.6.0 >/dev/null 2>&1; then
  oie="$HOME/.cache/perfuse-oie"
  mkdir -p "$oie"
  (
    cd "$oie"
    curl -sSLO https://github.com/OpenIntegrationEngine/engine/releases/download/v4.6.0/oie_unix_4_6_0.tar.gz
    curl -sSLO https://github.com/OpenIntegrationEngine/engine/releases/download/v4.6.0/sha256sums
    grep oie_unix_4_6_0.tar.gz sha256sums | shasum -a 256 -c -
    rm -rf oie && tar xzf oie_unix_4_6_0.tar.gz
    printf 'FROM eclipse-temurin:17-jdk\nCOPY oie /opt/oie\nWORKDIR /opt/oie\nENV INSTALL4J_JAVA_HOME_OVERRIDE=/opt/java/openjdk\nEXPOSE 8443\nCMD ["./oieserver"]\n' >Dockerfile
    docker build -q -t perfuse-local/oie:4.6.0 . >/dev/null
  )
fi
start oie460 -p 8446:8443 perfuse-local/oie:4.6.0

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
  RabbitMQ 4,     go test ./internal/amqp/ ./internal/engine/ -run 'RealBrokers|AMQPChannel' -v
  ActiveMQ AMQP
  Azurite         go test ./internal/azblob/ ./internal/engine/ -run 'Azurite|AzureBlob' -v
  LocalStack      go test ./internal/awsmsg/ ./internal/s3put/ ./internal/engine/ -run LocalStack -v
  Mirth, OIE,     go test ./internal/mirth/... ./internal/tomirth/ -v    (each test runs once per engine)
  BridgeLink      ./scripts/mirth-engine-corpus.sh    regenerates internal/mirth/testdata/engines from all four
  Keycloak        scripts/keycloak-saml-setup.sh && scripts/saml-verify-serve.sh
                  then: cd web && npx playwright test --config playwright-saml.config.ts

Every one of those tests skips rather than fails when its container is absent, so `make check` passes on a
machine with no Docker. That is deliberate: a check that needs Docker is a check people stop running.

Stop everything:  docker rm -f hapi activemq orthanc pg sftpd mtls-nginx keycloak mirth oie452 bridgelink oie460 localstack rabbitmq azurite
MSG
