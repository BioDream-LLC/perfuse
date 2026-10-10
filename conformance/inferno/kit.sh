#!/bin/sh
# kit.sh <crd|dtr|pas|uscore> - checks out the Inferno test kit at the version Perfuse was verified with, starts it in Docker,
# and makes its containers trust the test CA that signs Perfuse's certificate (certs.sh). Only one kit runs at a time: they
# all answer on http://localhost.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
work="$here/.work"
name=${1:?usage: kit.sh crd|dtr|pas|uscore}

case "$name" in
  crd)    repo=davinci-crd-test-kit tag=v0.14.2 ;;
  dtr)    repo=davinci-dtr-test-kit tag=v0.18.0 ;;
  pas)    repo=davinci-pas-test-kit tag=v0.15.2 ;;
  uscore) repo=us-core-test-kit    tag=v1.1.6 ;;
  *) echo "kit.sh: no kit called $name" >&2; exit 2 ;;
esac

"$here/certs.sh"

kit="$work/kits/$name"
if [ ! -d "$kit/.git" ]; then
  mkdir -p "$work/kits"
  git -c advice.detachedHead=false clone -q --depth 1 --branch "$tag" "https://github.com/inferno-framework/$repo.git" "$kit"
fi
[ "$(git -C "$kit" describe --tags --exact-match 2>/dev/null)" = "$tag" ] || { echo "kit.sh: $kit is not at $tag" >&2; exit 1; }

# The validator needs more memory than the kits give it for Perfuse's larger bundles, and keeps its package cache between runs.
# CRD's simulated EHR hands out its own address to Perfuse, which reaches it from this machine's side of Docker.
{
  echo "services:"
  echo "  hl7_validator_service:"
  echo "    environment:"
  echo "      SESSION_CACHE_DURATION: 30"
  echo "      JAVA_TOOL_OPTIONS: \"-Xmx4g -XX:+UseG1GC\""
  if [ "$name" = crd ]; then
    for s in inferno worker; do
      echo "  $s:"
      echo "    environment:"
      echo "      INFERNO_HOST: \"http://host.docker.internal\""
    done
  fi
} > "$kit/docker-compose.override.yml"

# Stop any other kit first.
for other in "$work"/kits/*; do
  [ "$other" = "$kit" ] || [ ! -d "$other" ] || (cd "$other" && docker compose down >/dev/null 2>&1 || true)
done

cd "$kit"
docker compose pull -q >/dev/null 2>&1 || true
docker compose build -q >/dev/null
# The kit's own setup.sh step: create or upgrade Inferno's database before it starts.
docker compose run --rm -T inferno bundle exec inferno migrate >/dev/null 2>&1
docker compose up -d >/dev/null
printf "waiting for Inferno"
i=0
until curl -sf http://localhost/api/test_suites >/dev/null 2>&1; do
  i=$((i + 1)); [ $i -lt 120 ] || { echo; echo "kit.sh: Inferno did not answer on http://localhost" >&2; exit 1; }
  # nginx gives up for good if it starts before the inferno container has a name to resolve; start it again.
  if [ $((i % 6)) -eq 0 ] && [ -z "$(docker compose ps -q --status running nginx 2>/dev/null)" ]; then docker compose up -d nginx >/dev/null 2>&1 || true; fi
  printf "."; sleep 5
done
echo

# Trust the test CA inside the containers that call Perfuse, then restart them: Ruby reads the CA bundle once, when it starts.
# Appended once; a restart keeps it, a recreated container starts afresh and gets it again here.
for s in inferno worker; do
  docker compose exec -T -u root "$s" sh -c 'grep -q "Perfuse conformance test CA" /etc/ssl/certs/ca-certificates.crt ||
    { echo "# Perfuse conformance test CA"; cat; } >> /etc/ssl/certs/ca-certificates.crt' < "$work/certs/ca.pem"
done
docker compose restart inferno worker >/dev/null 2>&1
i=0
until curl -sf http://localhost/api/test_suites >/dev/null 2>&1; do
  i=$((i + 1)); [ $i -lt 60 ] || { echo "kit.sh: Inferno did not come back after the restart" >&2; exit 1; }
  sleep 3
done
echo "$repo $tag is up on http://localhost"
